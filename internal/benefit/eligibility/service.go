package eligibility

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Errors the transport layer maps to problem+json codes.
var (
	// ErrEvaluationNotFound is an evaluation this tenant cannot see. An unknown id and
	// a foreign one are indistinguishable: RLS simply hides the other tenant's rows.
	ErrEvaluationNotFound = errors.New("benefit: eligibility evaluation not found")
	// ErrIdempotencyKeyReuse is the same Idempotency-Key with a different request: 409.
	ErrIdempotencyKeyReuse = errors.New("benefit: idempotency key reused with different arguments")
	// ErrProviderScope refuses a provider-scoped actor asking outside its own
	// organization: 403 PERMISSION_DENIED.
	ErrProviderScope = errors.New("benefit: provider-scoped actor may only check its own organization")
)

const (
	// ScopeOrganization is the iam.access_grant.scope_type of provider-side roles.
	ScopeOrganization = "ORGANIZATION"
	// PurposeEligibilityCheck is the purpose code of the access audit rows.
	PurposeEligibilityCheck = "ELIGIBILITY_CHECK"
	// ResourceEvaluation is the audited resource type.
	ResourceEvaluation = "eligibility_evaluation"

	// maxServiceItems mirrors the contract's maxItems on serviceItems.
	maxServiceItems = 100
)

// currencyPattern mirrors the contract's ^[A-Z]{3}$ on the optional currency code.
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// RequestItem is one requested service line. serviceDefinitionId stays opaque until the
// service catalogue lands in I3; the entitlement behind it comes from the request
// context (see contextHints).
type RequestItem struct {
	ServiceDefinitionID uuid.UUID
	Quantity            domain.Quantity
	RequestedAmount     *domain.Quantity
	CurrencyCode        string
}

// CheckInput is one eligibility question.
type CheckInput struct {
	PersonID               uuid.UUID
	ProgramID              *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	ServiceDate            time.Time
	Items                  []RequestItem
	// Context is the request's free-form context object; only the recognised hints are
	// read, stored or hashed.
	Context map[string]any
	// IdempotencyKey is the optional Idempotency-Key header. The evaluation table owns
	// the key itself (uq_eligibility_evaluation_key), so no separate middleware is
	// involved: the same key with the same request replays the stored evaluation, the
	// same key with a different request is refused.
	IdempotencyKey string
}

// Service runs eligibility checks and serves the stored snapshots.
//
// One check is one transaction (db.WithTenantTx): the person, the memberships, the
// enrollments, the plan version and the balances are read from a single point in time,
// the entitlement accounts of the service date are opened lazily through the ledger, and
// the evaluation row plus its access audit event commit with them. The transaction is
// therefore read-write, not the read-only one the work package sketched: step 5 of the
// resolution opens missing accounts through EnsureAccounts, and the evaluation snapshot
// is itself a write.
type Service struct {
	pool   *pgxpool.Pool
	ledger *ledger.Ledger
	audit  audit.Recorder
	logger *slog.Logger
	now    func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool  *pgxpool.Pool
	Audit audit.Recorder
	// Ledger is the movement engine used to open accounts lazily; nil builds one on the
	// service clock, which is what every process except a test does.
	Ledger *ledger.Ledger
	Logger *slog.Logger
	// Now overrides the clock in tests; nil means time.Now().UTC().
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil {
		return nil, errors.New("benefit: pool is required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	if d.Ledger == nil {
		d.Ledger = ledger.NewLedger(d.Now)
	}
	return &Service{pool: d.Pool, ledger: d.Ledger, audit: d.Audit, logger: d.Logger, now: d.Now}, nil
}

// Check answers one eligibility question and stores the evaluation snapshot.
func (s *Service) Check(ctx context.Context, rc identity.RequestContext, in CheckInput) (ResultView, error) {
	hints := parseContext(in.Context)
	if err := validate(in, hints); err != nil {
		return ResultView{}, err
	}
	if err := checkProviderScope(rc, in.ProviderOrganizationID); err != nil {
		return ResultView{}, err
	}
	hash, err := requestHash(in, hints)
	if err != nil {
		return ResultView{}, err
	}

	classification := audit.ClassPersonal
	if hints.health() {
		classification = audit.ClassHealth
	}

	var out ResultView
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if in.IdempotencyKey != "" {
			replay, found, err := s.replay(ctx, tx, rc, in.IdempotencyKey, hash)
			if err != nil {
				return err
			}
			if found {
				out = replay
				return s.recordAccess(ctx, tx, rc, in.PersonID, replay.EvaluationID, classification, replay.Outcome)
			}
		}

		result, err := s.evaluate(ctx, tx, rc.TenantID, in, hints)
		if err != nil {
			return err
		}
		evaluationID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("benefit: evaluation id: %w", err)
		}
		out = newResultView(evaluationID, s.now(), result)
		if err := s.store(ctx, tx, rc, in, hints, out, hash, classification); err != nil {
			return err
		}
		return s.recordAccess(ctx, tx, rc, in.PersonID, evaluationID, classification, result.Outcome)
	})
	if err != nil {
		return ResultView{}, err
	}
	return out, nil
}

// GetEvaluation returns one stored snapshot. A provider-scoped actor only sees the
// evaluations it made for its own organization; anything else is 404, so the endpoint
// cannot be used to probe for other providers' checks.
func (s *Service) GetEvaluation(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (EvaluationView, error) {
	var out EvaluationView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		row, err := sqlcgen.New(tx).GetEligibilityEvaluation(ctx, sqlcgen.GetEligibilityEvaluationParams{
			TenantID: rc.TenantID, ID: id,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrEvaluationNotFound
		}
		if err != nil {
			return fmt.Errorf("benefit: read eligibility evaluation: %w", err)
		}
		provider := uuidPtr(row.ProviderTenantOrganizationID)
		if err := checkProviderScope(rc, provider); err != nil {
			return ErrEvaluationNotFound
		}
		out, err = evaluationView(row)
		if err != nil {
			return err
		}
		return s.recordAccess(ctx, tx, rc, row.PersonID, row.ID,
			audit.Classification(row.DataClassification), row.Outcome)
	})
	if err != nil {
		return EvaluationView{}, err
	}
	return out, nil
}

// replay answers an Idempotency-Key that has already been used. The stored request hash
// decides: the same question replays its evaluation, a different one is a 409. The
// evaluations are append-only and the key is unique per tenant, so a replay is not
// bounded by a time window - a second row under the same key could never be written.
func (s *Service) replay(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	key string, hash []byte) (ResultView, bool, error) {
	row, err := sqlcgen.New(tx).FindEligibilityEvaluationByKey(ctx, sqlcgen.FindEligibilityEvaluationByKeyParams{
		TenantID: rc.TenantID, IdempotencyKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ResultView{}, false, nil
	}
	if err != nil {
		return ResultView{}, false, fmt.Errorf("benefit: read eligibility evaluation by key: %w", err)
	}
	if !bytes.Equal(row.RequestHash, hash) {
		return ResultView{}, false, ErrIdempotencyKeyReuse
	}
	var stored ResultView
	if err := json.Unmarshal(row.ResultSnapshot, &stored); err != nil {
		return ResultView{}, false, fmt.Errorf("benefit: decode result snapshot: %w", err)
	}
	stored.EvaluationID = row.ID
	return stored, true, nil
}

// evaluate loads everything the resolution needs and runs the pure resolver.
func (s *Service) evaluate(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in CheckInput, hints contextHints) (Result, error) {
	day := domain.DateOnly(in.ServiceDate)
	q := sqlcgen.New(tx)

	resolverInput := Input{ServiceDate: day, Items: requestItems(in.Items, hints)}
	if in.ProgramID != nil {
		resolverInput.ProgramID = *in.ProgramID
	}

	person, err := q.GetPersonForEligibility(ctx, sqlcgen.GetPersonForEligibilityParams{
		TenantID: tenantID, ID: in.PersonID,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		resolverInput.Person = Person{ID: in.PersonID}
		return Resolve(resolverInput), nil
	case err != nil:
		return Result{}, fmt.Errorf("benefit: read person: %w", err)
	}
	resolverInput.Person = Person{ID: person.ID, Found: true, Status: person.Status}

	memberships, err := q.ListMembershipsForEligibility(ctx, sqlcgen.ListMembershipsForEligibilityParams{
		TenantID: tenantID, PersonID: in.PersonID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("benefit: list memberships: %w", err)
	}
	for _, m := range memberships {
		resolverInput.Memberships = append(resolverInput.Memberships, Membership{
			ID: m.ID, Status: m.Status, ValidFrom: dateValue(m.ValidFrom), ValidTo: datePtr(m.ValidTo),
		})
	}

	enrollments, err := q.ListEnrollmentsForEligibility(ctx, sqlcgen.ListEnrollmentsForEligibilityParams{
		TenantID: tenantID, PersonID: in.PersonID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("benefit: list enrollments: %w", err)
	}
	for _, e := range enrollments {
		resolverInput.Enrollments = append(resolverInput.Enrollments, Enrollment{
			ID: e.ID, PlanID: e.PlanID, ProgramID: e.ProgramID, Status: e.Status,
			ValidFrom: dateValue(e.ValidFrom), ValidTo: datePtr(e.ValidTo),
		})
	}

	// The enrollment the resolver will choose decides which plan version and which
	// accounts to load; Resolve derives the same choice from the same slice.
	active := SelectEnrollments(resolverInput.Enrollments, resolverInput.ProgramID, day)
	if len(active) == 0 {
		return Resolve(resolverInput), nil
	}

	// Opening the accounts of the service date is what makes a first-ever check answer
	// with real balances instead of "no account yet"; it is idempotent.
	ensured, err := s.ledger.EnsureAccounts(ctx, tx, tenantID, active[0].ID, day)
	switch {
	case errors.Is(err, application.ErrNoPublishedVersion):
		return Resolve(resolverInput), nil
	case err != nil:
		return Result{}, err
	}
	resolverInput.PlanVersion = &PlanVersion{ID: ensured.PlanVersionID}

	accounts, err := s.ledger.ResolveAccounts(ctx, tx, tenantID, in.PersonID, day)
	if err != nil {
		return Result{}, err
	}
	for _, a := range accounts {
		if a.Status != ledger.AccountOpen {
			continue
		}
		resolverInput.Accounts = append(resolverInput.Accounts, Account{
			ID: a.ID, EntitlementCode: a.Definition.Code, UnitType: a.Definition.UnitType,
			Available: a.Balances.Available, AllowOverdraft: a.Definition.AllowOverdraft, Shared: a.Shared,
		})
	}
	return Resolve(resolverInput), nil
}

// store writes the immutable snapshot: ids, dates, quantities and codes only.
func (s *Service) store(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, in CheckInput,
	hints contextHints, view ResultView, hash []byte, classification audit.Classification) error {
	request, err := json.Marshal(newRequestView(in, hints))
	if err != nil {
		return fmt.Errorf("benefit: encode request snapshot: %w", err)
	}
	result, err := json.Marshal(view)
	if err != nil {
		return fmt.Errorf("benefit: encode result snapshot: %w", err)
	}
	_, err = sqlcgen.New(tx).CreateEligibilityEvaluation(ctx, sqlcgen.CreateEligibilityEvaluationParams{
		ID:                           view.EvaluationID,
		TenantID:                     rc.TenantID,
		PersonID:                     in.PersonID,
		ProgramID:                    optUUID(in.ProgramID),
		EnrollmentID:                 optUUID(view.EnrollmentID),
		PlanVersionID:                optUUID(view.PlanVersionID),
		ProviderTenantOrganizationID: optUUID(in.ProviderOrganizationID),
		ServiceDate:                  dateOf(in.ServiceDate),
		Outcome:                      view.Outcome,
		RequestHash:                  hash,
		RequestSnapshot:              request,
		ResultSnapshot:               result,
		DataClassification:           string(classification),
		IdempotencyKey:               optString(in.IdempotencyKey),
		EvaluatedAt:                  view.EvaluatedAt,
		EvaluatedBy:                  nullUUID(rc.Principal.ActorID),
	})
	if err != nil {
		return fmt.Errorf("benefit: store eligibility evaluation: %w", err)
	}
	return nil
}

// recordAccess writes the access audit row of a check: who looked at whose eligibility,
// classified HEALTH when the request was made in a clinical context.
func (s *Service) recordAccess(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	personID, evaluationID uuid.UUID, classification audit.Classification, outcome string) error {
	return s.audit.RecordAccess(ctx, tx, audit.AccessEvent{
		TenantID: rc.TenantID, ActorID: rc.Principal.ActorID, MembershipID: nullUUID(rc.MembershipID),
		PersonID: nullUUID(personID), ResourceType: ResourceEvaluation, ResourceID: nullUUID(evaluationID),
		AccessType: audit.AccessView, Classification: classification,
		PurposeCode: PurposeEligibilityCheck, ReasonText: "outcome=" + outcome,
		Outcome: audit.OutcomeSuccess,
	})
}

// requestItems pairs each requested line with the entitlement code hinted for it.
func requestItems(items []RequestItem, hints contextHints) []Item {
	out := make([]Item, 0, len(items))
	for i, item := range items {
		out = append(out, Item{Index: i, EntitlementCode: hints.codeFor(i), Quantity: item.Quantity})
	}
	return out
}

// validate answers 422 before anything is read or written.
func validate(in CheckInput, hints contextHints) error {
	ve := &domain.ValidationError{}
	if in.PersonID == uuid.Nil {
		ve.Add("personId", "REQUIRED", "hak sahibi kimliği zorunlu")
	}
	if in.ServiceDate.IsZero() {
		ve.Add("serviceDate", "REQUIRED", "hizmet tarihi zorunlu")
	}
	switch {
	case len(in.Items) == 0:
		ve.Add("serviceItems", "REQUIRED", "en az bir hizmet kalemi gerekli")
	case len(in.Items) > maxServiceItems:
		ve.Add("serviceItems", "RANGE", "en fazla 100 hizmet kalemi gönderilebilir")
	}
	for i, item := range in.Items {
		field := fmt.Sprintf("serviceItems[%d]", i)
		if item.ServiceDefinitionID == uuid.Nil {
			ve.Add(field+".serviceDefinitionId", "REQUIRED", "hizmet tanımı zorunlu")
		}
		if !item.Quantity.IsPositive() {
			ve.Add(field+".quantity", "RANGE", "miktar sıfırdan büyük olmalı")
		}
		if item.RequestedAmount != nil && item.RequestedAmount.IsNegative() {
			ve.Add(field+".requestedAmount", "RANGE", "tutar negatif olamaz")
		}
		if item.CurrencyCode != "" && !currencyPattern.MatchString(item.CurrencyCode) {
			ve.Add(field+".currencyCode", "FORMAT", "ISO-4217 üç harfli kod olmalı")
		}
	}
	if len(hints.codes) > len(in.Items) {
		ve.Add("context.entitlementCodes", "RANGE", "hizmet kalemi sayısından fazla hak kodu gönderilemez")
	}
	return ve.OrNil()
}

// checkProviderScope enforces the provider boundary: an actor whose grants are scoped to
// organizations may only ask about a provider organization inside that scope. A
// tenant-wide actor has no ORGANIZATION scope and is unaffected.
func checkProviderScope(rc identity.RequestContext, providerID *uuid.UUID) error {
	scoped := false
	for _, scope := range rc.Scopes {
		if scope.Type != ScopeOrganization || !scope.ID.Valid {
			continue
		}
		scoped = true
		if providerID != nil && scope.ID.UUID == *providerID {
			return nil
		}
	}
	if !scoped {
		return nil
	}
	return ErrProviderScope
}

// evaluationView renders a stored row as the contract's EligibilityEvaluation.
func evaluationView(row sqlcgen.GetEligibilityEvaluationRow) (EvaluationView, error) {
	out := EvaluationView{
		ID: row.ID, PersonID: row.PersonID, ProgramID: uuidPtr(row.ProgramID),
		EnrollmentID: uuidPtr(row.EnrollmentID), PlanVersionID: uuidPtr(row.PlanVersionID),
		ServiceDate: dateValue(row.ServiceDate).Format(time.DateOnly), Outcome: row.Outcome,
		EvaluatedAt: row.EvaluatedAt.UTC(), EvaluatedBy: uuidPtr(row.EvaluatedBy),
	}
	if err := json.Unmarshal(row.RequestSnapshot, &out.Request); err != nil {
		return EvaluationView{}, fmt.Errorf("benefit: decode request snapshot: %w", err)
	}
	if err := json.Unmarshal(row.ResultSnapshot, &out.Result); err != nil {
		return EvaluationView{}, fmt.Errorf("benefit: decode result snapshot: %w", err)
	}
	out.Result.EvaluationID = row.ID
	return out, nil
}

func tenantCtx(rc identity.RequestContext) db.TenantContext {
	return db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func optUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: *id != uuid.Nil}
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func dateOf(t time.Time) pgtype.Date {
	return pgtype.Date{Time: domain.DateOnly(t), Valid: true}
}

func dateValue(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return d.Time
}

func datePtr(d pgtype.Date) *time.Time {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return nil
	}
	t := d.Time
	return &t
}
