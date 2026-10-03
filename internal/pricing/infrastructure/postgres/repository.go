// Package pricingpg implements the pricing quote repository with sqlc. It is stateless:
// every method takes the caller's tenant-bound transaction, so RLS is active for every
// statement and nothing here can read another tenant's quote.
//
// Where a read already exists elsewhere it is reused rather than rewritten: the price
// candidates, the category chain and the packages of a definition come from the contract
// repository, the balances from the entitlement ledger's read side, and the plan version
// from the benefit application layer. This package is the only place that knows the
// pricing module depends on those three, which is what keeps the application layer above
// it testable against one port.
package pricingpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	contractpg "github.com/celikbros/kapsora/internal/contract/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	"github.com/celikbros/kapsora/internal/pricing/application"
	rulesdomain "github.com/celikbros/kapsora/internal/rules/domain"
	"github.com/celikbros/kapsora/internal/rules/engine"
)

// Repository implements application.Repository.
type Repository struct {
	contracts contractapp.Repository
	ledger    *ledger.Ledger
}

// New returns the repository. The ledger is used for its read side only — ResolveAccounts
// lists what a person can already spend from — and never for a movement: a quote reserves
// nothing.
func New() *Repository {
	return &Repository{contracts: contractpg.New(), ledger: ledger.NewLedger(nil)}
}

// QuoteTTLHours implements application.Repository.
func (Repository) QuoteTTLHours(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (int, bool, error) {
	raw, err := sqlcgen.New(tx).GetPricingQuoteTTLHours(ctx, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("pricing: read quote ttl setting: %w", err)
	}
	hours, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		// A setting that is not a whole number of hours is a configuration mistake, not a
		// reason to refuse a quote; the service logs it and uses its documented default.
		return 0, false, fmt.Errorf("pricing: quote ttl setting %q is not a whole number of hours", raw)
	}
	return hours, true, nil
}

// GetProvider implements application.Repository.
func (Repository) GetProvider(ctx context.Context, tx pgx.Tx, tenantID, providerProfileID uuid.UUID) (application.ProviderRecord, error) {
	row, err := sqlcgen.New(tx).GetProviderProfileForQuote(ctx, sqlcgen.GetProviderProfileForQuoteParams{
		TenantID: tenantID, ID: providerProfileID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ProviderRecord{}, application.ErrProviderNotFound
	}
	if err != nil {
		return application.ProviderRecord{}, fmt.Errorf("pricing: read provider profile: %w", err)
	}
	return application.ProviderRecord{
		ID: row.ID, TenantOrganizationID: row.TenantOrganizationID, Status: row.Status,
	}, nil
}

// LocationBelongsToProvider implements application.Repository.
func (Repository) LocationBelongsToProvider(ctx context.Context, tx pgx.Tx, tenantID, locationID, providerProfileID uuid.UUID) (bool, error) {
	ok, err := sqlcgen.New(tx).ProviderLocationBelongsToProvider(ctx, sqlcgen.ProviderLocationBelongsToProviderParams{
		TenantID: tenantID, ID: locationID, ProviderProfileID: providerProfileID,
	})
	if err != nil {
		return false, fmt.Errorf("pricing: check provider location: %w", err)
	}
	return ok, nil
}

// EligibilityEvaluationExists implements application.Repository.
func (Repository) EligibilityEvaluationExists(ctx context.Context, tx pgx.Tx, tenantID, evaluationID, personID uuid.UUID) (bool, error) {
	ok, err := sqlcgen.New(tx).PriceQuoteEligibilityEvaluationExists(ctx, sqlcgen.PriceQuoteEligibilityEvaluationExistsParams{
		TenantID: tenantID, ID: evaluationID, PersonID: personID,
	})
	if err != nil {
		return false, fmt.Errorf("pricing: check eligibility evaluation: %w", err)
	}
	return ok, nil
}

// PackagesExist implements application.Repository.
func (Repository) PackagesExist(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := sqlcgen.New(tx).ListPricePackageDefinitions(ctx, sqlcgen.ListPricePackageDefinitionsParams{
		TenantID: tenantID, Ids: ids,
	})
	if err != nil {
		return nil, fmt.Errorf("pricing: list package definitions: %w", err)
	}
	for _, r := range rows {
		out[r.ID] = true
	}
	return out, nil
}

// LoadEligibility implements application.Repository. Every statement is a read: the
// accounts are listed, never opened, because opening one posts a GRANT ledger entry and a
// quote must leave the ledger exactly as it found it.
func (r Repository) LoadEligibility(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID,
	programID *uuid.UUID, serviceDate time.Time,
) (application.EligibilityInput, error) {
	day := benefitdomain.DateOnly(serviceDate)
	q := sqlcgen.New(tx)
	out := application.EligibilityInput{Person: eligibility.Person{ID: personID}}

	person, err := q.GetPersonForEligibility(ctx, sqlcgen.GetPersonForEligibilityParams{
		TenantID: tenantID, ID: personID,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return out, nil
	case err != nil:
		return application.EligibilityInput{}, fmt.Errorf("pricing: read person: %w", err)
	}
	out.Person = eligibility.Person{ID: person.ID, Found: true, Status: person.Status}

	memberships, err := q.ListMembershipsForEligibility(ctx, sqlcgen.ListMembershipsForEligibilityParams{
		TenantID: tenantID, PersonID: personID,
	})
	if err != nil {
		return application.EligibilityInput{}, fmt.Errorf("pricing: list memberships: %w", err)
	}
	for _, m := range memberships {
		out.Memberships = append(out.Memberships, eligibility.Membership{
			ID: m.ID, Status: m.Status, ValidFrom: dateValue(m.ValidFrom), ValidTo: datePtr(m.ValidTo),
		})
	}

	enrollments, err := q.ListEnrollmentsForEligibility(ctx, sqlcgen.ListEnrollmentsForEligibilityParams{
		TenantID: tenantID, PersonID: personID,
	})
	if err != nil {
		return application.EligibilityInput{}, fmt.Errorf("pricing: list enrollments: %w", err)
	}
	for _, e := range enrollments {
		out.Enrollments = append(out.Enrollments, eligibility.Enrollment{
			ID: e.ID, PlanID: e.PlanID, ProgramID: e.ProgramID, Status: e.Status,
			ValidFrom: dateValue(e.ValidFrom), ValidTo: datePtr(e.ValidTo),
		})
	}

	// The enrollment the resolver will choose decides which plan version applies; it
	// derives the same choice from the same slice, so the two can never disagree.
	program := uuid.Nil
	if programID != nil {
		program = *programID
	}
	active := eligibility.SelectEnrollments(out.Enrollments, program, day)
	if len(active) == 0 {
		return out, nil
	}
	enrollment, err := q.GetEnrollmentForEntitlement(ctx, sqlcgen.GetEnrollmentForEntitlementParams{
		TenantID: tenantID, ID: active[0].ID,
	})
	if err != nil {
		return application.EligibilityInput{}, fmt.Errorf("pricing: read enrollment: %w", err)
	}
	version, err := benefitapp.ResolvePlanVersion(ctx, tx, tenantID, enrollment.PlanID, day)
	switch {
	case errors.Is(err, benefitapp.ErrNoPublishedVersion):
		return out, nil
	case err != nil:
		return application.EligibilityInput{}, err
	}
	out.PlanVersion = &eligibility.PlanVersion{ID: version.ID}
	mappings, err := q.ListEligibilityMappings(ctx, sqlcgen.ListEligibilityMappingsParams{
		TenantID: tenantID, PlanVersionID: version.ID, ServiceDate: dateOf(day),
	})
	if err != nil {
		return application.EligibilityInput{}, fmt.Errorf("pricing: list entitlement mappings: %w", err)
	}
	out.Mappings = make(map[uuid.UUID]eligibility.Mapping, len(mappings))
	for _, m := range mappings {
		factor, err := benefitdomain.ParseQuantity(m.UnitFactor)
		if err != nil {
			return application.EligibilityInput{}, fmt.Errorf("pricing: mapping factor: %w", err)
		}
		out.Mappings[m.ServiceDefinitionID] = eligibility.Mapping{
			EntitlementCode: m.EntitlementCode, UnitFactor: factor,
		}
	}

	accounts, err := r.ledger.ResolveAccounts(ctx, tx, tenantID, personID, day)
	if err != nil {
		return application.EligibilityInput{}, err
	}
	for _, a := range accounts {
		if a.Status != ledger.AccountOpen {
			continue
		}
		out.Accounts = append(out.Accounts, eligibility.Account{
			ID: a.ID, EntitlementCode: a.Definition.Code, UnitType: a.Definition.UnitType,
			Available: a.Balances.Available, AllowOverdraft: a.Definition.AllowOverdraft, Shared: a.Shared,
		})
	}
	return out, nil
}

// CategoryPath implements application.Repository.
func (r Repository) CategoryPath(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]uuid.UUID, error) {
	return r.contracts.CategoryPath(ctx, tx, tenantID, definitionID)
}

// PackagesContaining implements application.Repository.
func (r Repository) PackagesContaining(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]uuid.UUID, error) {
	return r.contracts.PackagesContaining(ctx, tx, tenantID, definitionID)
}

// ListCandidates implements application.Repository.
func (r Repository) ListCandidates(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q contractapp.CandidateQuery,
) ([]contractapp.CandidateRecord, error) {
	return r.contracts.ListCandidates(ctx, tx, tenantID, q)
}

// ServiceDefinitionExists implements application.Repository.
func (r Repository) ServiceDefinitionExists(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) (bool, error) {
	return r.contracts.ServiceDefinitionExists(ctx, tx, tenantID, definitionID)
}

// ListPriceRuleVersions implements application.Repository.
func (Repository) ListPriceRuleVersions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	serviceDate time.Time,
) ([]application.PriceRuleVersion, error) {
	q := sqlcgen.New(tx)
	versions, err := q.ListPublishedPriceRuleSetVersions(ctx, sqlcgen.ListPublishedPriceRuleSetVersionsParams{
		TenantID: tenantID, ServiceDate: dateOf(serviceDate),
	})
	if err != nil {
		return nil, fmt.Errorf("pricing: list published price rule set versions: %w", err)
	}
	out := make([]application.PriceRuleVersion, 0, len(versions))
	for _, v := range versions {
		schema := map[string]string{}
		if len(v.InputSchema) > 0 {
			if err := json.Unmarshal(v.InputSchema, &schema); err != nil {
				return nil, fmt.Errorf("pricing: decode rule input schema: %w", err)
			}
		}
		rules, err := q.ListRules(ctx, sqlcgen.ListRulesParams{TenantID: tenantID, RuleSetVersionID: v.ID})
		if err != nil {
			return nil, fmt.Errorf("pricing: list rules: %w", err)
		}
		engineRules := make([]engine.Rule, 0, len(rules))
		for _, row := range rules {
			actions, err := decodeActions(row.Actions)
			if err != nil {
				return nil, err
			}
			params := map[string]any{}
			if len(row.ExplanationParams) > 0 {
				if err := json.Unmarshal(row.ExplanationParams, &params); err != nil {
					return nil, fmt.Errorf("pricing: decode rule explanation params: %w", err)
				}
			}
			engineRules = append(engineRules, engine.Rule{
				ID: row.ID, Code: row.Code, Priority: int(row.Priority), Condition: row.Condition,
				Actions: rulesdomain.EngineActions(actions), ExplanationCode: row.ExplanationCode,
				ExplanationParams: params, StopOnMatch: row.StopOnMatch, Active: row.Active,
			})
		}
		out = append(out, application.PriceRuleVersion{
			ID: v.ID, RuleSetID: v.RuleSetID, RuleSetCode: v.RuleSetCode,
			VersionNo: int(v.VersionNo), InputSchema: schema, Rules: engineRules,
		})
	}
	return out, nil
}

// CreateQuote implements application.Repository: the header and its lines in one
// transaction, so a quote can never be stored with half its arithmetic.
func (Repository) CreateQuote(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	row application.QuoteRow, items []application.QuoteItemRow,
) error {
	q := sqlcgen.New(tx)
	if _, err := q.CreatePriceQuote(ctx, sqlcgen.CreatePriceQuoteParams{
		ID: row.ID, TenantID: tenantID, PersonID: row.PersonID,
		ProgramID: optUUID(row.ProgramID), ProviderProfileID: row.ProviderProfileID,
		LocationID: optUUID(row.LocationID), ServiceDate: dateOf(row.ServiceDate),
		CurrencyCode: row.CurrencyCode, Outcome: row.Outcome,
		RequestedAmount: row.Requested.String(), ContractAmount: row.Contract.String(),
		CoveredAmount: row.Covered.String(), PayerAmount: row.Payer.String(),
		MemberAmount: row.Member.String(),
		RequestHash:  row.RequestHash, RequestSnapshot: row.RequestSnapshot,
		ResultSnapshot:    row.ResultSnapshot,
		ContractVersionID: optUUID(row.ContractVersionID), PlanVersionID: optUUID(row.PlanVersionID),
		EligibilityEvaluationID: optUUID(row.EligibilityEvaluationID),
		ExpiresAt:               row.ExpiresAt, QuotedAt: row.QuotedAt, QuotedBy: optUUID(row.QuotedBy),
		IdempotencyKey: row.IdempotencyKey,
	}); err != nil {
		return fmt.Errorf("pricing: store price quote: %w", err)
	}

	params := make([]sqlcgen.CreatePriceQuoteItemParams, 0, len(items))
	for _, item := range items {
		params = append(params, sqlcgen.CreatePriceQuoteItemParams{
			TenantID: tenantID, PriceQuoteID: row.ID, LineNo: int32(item.LineNo), //nolint:gosec // a line number is bounded by the contract's maxItems
			ServiceDefinitionID: optUUID(item.ServiceDefinitionID),
			PackageDefinitionID: optUUID(item.PackageDefinitionID),
			PriceItemID:         optUUID(item.PriceItemID),
			Quantity:            item.Quantity.String(),
			RequestedAmount:     item.Requested.String(), ContractAmount: item.Contract.String(),
			CoveredAmount: item.Covered.String(), PayerAmount: item.Payer.String(),
			MemberAmount: item.Member.String(), Outcome: item.Outcome,
			Explanations: item.Explanations,
		})
	}
	var firstErr error
	batch := q.CreatePriceQuoteItem(ctx, params)
	batch.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if err := batch.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if firstErr != nil {
		return fmt.Errorf("pricing: store price quote items: %w", firstErr)
	}
	return nil
}

// GetQuote implements application.Repository.
func (r Repository) GetQuote(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.QuoteRecord, []application.QuoteItemRecord, error) {
	row, err := sqlcgen.New(tx).GetPriceQuote(ctx, sqlcgen.GetPriceQuoteParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.QuoteRecord{}, nil, application.ErrQuoteNotFound
	}
	if err != nil {
		return application.QuoteRecord{}, nil, fmt.Errorf("pricing: read price quote: %w", err)
	}
	items, err := r.quoteItems(ctx, tx, tenantID, row.ID)
	if err != nil {
		return application.QuoteRecord{}, nil, err
	}
	return application.QuoteRecord{
		ID: row.ID, PersonID: row.PersonID, ProgramID: uuidPtr(row.ProgramID),
		ProviderProfileID: row.ProviderProfileID, LocationID: uuidPtr(row.LocationID),
		ServiceDate: dateValue(row.ServiceDate), CurrencyCode: row.CurrencyCode,
		Outcome:   row.Outcome,
		Requested: decimal(row.RequestedAmount), Contract: decimal(row.ContractAmount),
		Covered: decimal(row.CoveredAmount), Payer: decimal(row.PayerAmount),
		Member:      decimal(row.MemberAmount),
		RequestHash: row.RequestHash, RequestSnapshot: row.RequestSnapshot,
		ResultSnapshot:    row.ResultSnapshot,
		ContractVersionID: uuidPtr(row.ContractVersionID), PlanVersionID: uuidPtr(row.PlanVersionID),
		EligibilityEvaluationID: uuidPtr(row.EligibilityEvaluationID),
		ExpiresAt:               row.ExpiresAt, QuotedAt: row.QuotedAt, QuotedBy: uuidPtr(row.QuotedBy),
		IdempotencyKey: row.IdempotencyKey,
	}, items, nil
}

// FindQuoteByKey implements application.Repository.
func (r Repository) FindQuoteByKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, key string) (application.QuoteRecord, []application.QuoteItemRecord, bool, error) {
	row, err := sqlcgen.New(tx).FindPriceQuoteByKey(ctx, sqlcgen.FindPriceQuoteByKeyParams{
		TenantID: tenantID, IdempotencyKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.QuoteRecord{}, nil, false, nil
	}
	if err != nil {
		return application.QuoteRecord{}, nil, false, fmt.Errorf("pricing: read price quote by key: %w", err)
	}
	items, err := r.quoteItems(ctx, tx, tenantID, row.ID)
	if err != nil {
		return application.QuoteRecord{}, nil, false, err
	}
	return application.QuoteRecord{
		ID: row.ID, PersonID: row.PersonID, ProgramID: uuidPtr(row.ProgramID),
		ProviderProfileID: row.ProviderProfileID, LocationID: uuidPtr(row.LocationID),
		ServiceDate: dateValue(row.ServiceDate), CurrencyCode: row.CurrencyCode,
		Outcome:   row.Outcome,
		Requested: decimal(row.RequestedAmount), Contract: decimal(row.ContractAmount),
		Covered: decimal(row.CoveredAmount), Payer: decimal(row.PayerAmount),
		Member:      decimal(row.MemberAmount),
		RequestHash: row.RequestHash, RequestSnapshot: row.RequestSnapshot,
		ResultSnapshot:    row.ResultSnapshot,
		ContractVersionID: uuidPtr(row.ContractVersionID), PlanVersionID: uuidPtr(row.PlanVersionID),
		EligibilityEvaluationID: uuidPtr(row.EligibilityEvaluationID),
		ExpiresAt:               row.ExpiresAt, QuotedAt: row.QuotedAt, QuotedBy: uuidPtr(row.QuotedBy),
		IdempotencyKey: row.IdempotencyKey,
	}, items, true, nil
}

func (Repository) quoteItems(ctx context.Context, tx pgx.Tx, tenantID, quoteID uuid.UUID) ([]application.QuoteItemRecord, error) {
	rows, err := sqlcgen.New(tx).ListPriceQuoteItems(ctx, sqlcgen.ListPriceQuoteItemsParams{
		TenantID: tenantID, PriceQuoteID: quoteID,
	})
	if err != nil {
		return nil, fmt.Errorf("pricing: list price quote items: %w", err)
	}
	out := make([]application.QuoteItemRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.QuoteItemRecord{
			LineNo:              int(r.LineNo),
			ServiceDefinitionID: uuidPtr(r.ServiceDefinitionID),
			PackageDefinitionID: uuidPtr(r.PackageDefinitionID),
			PriceItemID:         uuidPtr(r.PriceItemID),
			Quantity:            decimal(r.Quantity), Requested: decimal(r.RequestedAmount),
			Contract: decimal(r.ContractAmount), Covered: decimal(r.CoveredAmount),
			Payer: decimal(r.PayerAmount), Member: decimal(r.MemberAmount),
			Outcome: r.Outcome, Explanations: r.Explanations,
		})
	}
	return out, nil
}

// decodeActions reads the stored action list of a rule. The shape is the rules module's
// own, so it is decoded into that module's input type rather than into a local copy.
func decodeActions(raw []byte) ([]rulesdomain.ActionInput, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out []rulesdomain.ActionInput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("pricing: decode rule actions: %w", err)
	}
	return out, nil
}

// decimal renders a numeric(20,6) column in the canonical form the rest of the system
// speaks. PostgreSQL pads the scale — 500 comes back as "500.000000" — and a figure whose
// text depends on which side of the wire it was last on is a figure two screens will
// eventually disagree about.
func decimal(raw string) string {
	q, err := benefitdomain.ParseQuantity(raw)
	if err != nil {
		return raw
	}
	return q.String()
}

func dateOf(t time.Time) pgtype.Date {
	return pgtype.Date{Time: benefitdomain.DateOnly(t), Valid: true}
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
