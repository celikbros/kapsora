package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Adjustment statuses (migration 000016).
const (
	AdjustmentPending  = "PENDING"
	AdjustmentApproved = "APPROVED"
	AdjustmentRejected = "REJECTED"
)

// errPagingUnavailable guards the paged reads in a process built without a cursor codec.
var errPagingUnavailable = errors.New("benefit: paged reads need a cursor codec")

// AdjustmentStatuses is the set the list filter accepts.
var AdjustmentStatuses = []string{AdjustmentPending, AdjustmentApproved, AdjustmentRejected}

// DriftEvent is the outbox event published when reconciliation finds an account whose
// balances no longer match its ledger.
const DriftEvent = "benefit.entitlement.drift"

// Adjustment is one manual correction request in its maker-checker lifecycle.
type Adjustment struct {
	ID              uuid.UUID
	AccountID       uuid.UUID
	Delta           domain.Quantity
	ReasonCode      string
	ReasonText      *string
	Status          string
	RequestedBy     uuid.UUID
	RequestedAt     time.Time
	DecidedBy       *uuid.UUID
	DecidedAt       *time.Time
	DecisionComment *string
	LedgerEntryID   *uuid.UUID
	// CreatedAt is the keyset position of the list; requested_at is what the API shows.
	CreatedAt  time.Time
	RowVersion int64
}

// AdjustmentPage is one keyset page of adjustments.
type AdjustmentPage struct {
	Items      []Adjustment
	NextCursor string
}

// LedgerPage is one keyset page of movements, newest first.
type LedgerPage struct {
	Items      []Entry
	NextCursor string
}

// NewAdjustmentInput is the create command of an adjustment.
type NewAdjustmentInput struct {
	Delta      domain.Quantity
	ReasonCode string
	ReasonText *string
}

// AdjustmentFilter is the API-level list request.
type AdjustmentFilter struct {
	Status string
	Cursor string
	Limit  int
}

// Service carries the entitlement use cases: the balance and ledger reads, the
// maker-checker adjustments, the enrollment consumer and the two scheduler jobs. It owns
// its transactions (db.WithTenantTx), so a movement, its audit row and its outbox event
// commit together; the movement engine itself is Ledger and takes a caller's transaction.
type Service struct {
	pool    *pgxpool.Pool
	ledger  *Ledger
	audit   audit.Recorder
	cursors *httpx.CursorCodec
	logger  *slog.Logger
	now     func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool  *pgxpool.Pool
	Audit audit.Recorder
	// Cursors signs the keyset cursors of the paged reads. The worker and the scheduler
	// never page, so they may leave it nil; the paged reads then refuse to run.
	Cursors *httpx.CursorCodec
	Logger  *slog.Logger
	// Now overrides the clock in tests; nil means time.Now().UTC().
	Now func() time.Time
}

// New validates the dependencies and builds the service with its movement engine.
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
	return &Service{
		pool: d.Pool, ledger: NewLedger(d.Now), audit: d.Audit,
		cursors: d.Cursors, logger: d.Logger, now: d.Now,
	}, nil
}

// Ledger exposes the movement engine to callers that already own a transaction (the
// eligibility and service-request modules of later increments, and the tests).
func (s *Service) Ledger() *Ledger { return s.ledger }

// ListPersonEntitlements returns the balances a person can spend on asOf, including the
// family-shared accounts of their principal.
func (s *Service) ListPersonEntitlements(ctx context.Context, rc identity.RequestContext,
	personID uuid.UUID, asOf time.Time) ([]Account, error) {
	if asOf.IsZero() {
		asOf = s.now()
	}
	var out []Account
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		exists, err := sqlcgen.New(tx).PersonExists(ctx, sqlcgen.PersonExistsParams{TenantID: rc.TenantID, ID: personID})
		if err != nil {
			return fmt.Errorf("benefit: person exists: %w", err)
		}
		if !exists {
			return ErrNotFound
		}
		out, err = s.ledger.ResolveAccounts(ctx, tx, rc.TenantID, personID, asOf)
		return err
	})
	return out, err
}

// GetAccount returns one account with its open reservations.
func (s *Service) GetAccount(ctx context.Context, rc identity.RequestContext, accountID uuid.UUID) (Account, error) {
	var out Account
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = loadAccount(ctx, tx, rc.TenantID, accountID)
		return err
	})
	return out, err
}

// ListLedger returns one keyset page of movements, newest first. The ledger is
// append-only, so a page never changes underneath the reader.
func (s *Service) ListLedger(ctx context.Context, rc identity.RequestContext,
	accountID uuid.UUID, cursor string, limit int) (LedgerPage, error) {
	if s.cursors == nil {
		return LedgerPage{}, errPagingUnavailable
	}
	position, hasCursor, err := s.cursors.Decode(cursor)
	if err != nil {
		return LedgerPage{}, err
	}
	pageSize := httpx.ClampLimit(limit)

	var rows []Entry
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if err := accountExists(ctx, tx, rc.TenantID, accountID); err != nil {
			return err
		}
		params := sqlcgen.ListEntitlementLedgerEntriesParams{
			TenantID: rc.TenantID, AccountID: accountID,
			PageSize: int32(pageSize + 1), //nolint:gosec // clamped by httpx.ClampLimit
		}
		if hasCursor {
			params.CursorEffectiveAt = &position.CreatedAt
			params.CursorID = uuid.NullUUID{UUID: position.ID, Valid: true}
		}
		list, err := sqlcgen.New(tx).ListEntitlementLedgerEntries(ctx, params)
		if err != nil {
			return fmt.Errorf("benefit: list ledger entries: %w", err)
		}
		rows = make([]Entry, 0, len(list))
		for _, r := range list {
			entry, err := entryFromRow(entryRow{
				ID: r.ID, AccountID: r.EntitlementAccountID, MovementType: r.MovementType,
				EffectiveAt: r.EffectiveAt, DeltaTotal: r.DeltaTotal, DeltaAvailable: r.DeltaAvailable,
				DeltaReserved: r.DeltaReserved, DeltaConsumed: r.DeltaConsumed, DeltaExpired: r.DeltaExpired,
				ReferenceType: r.ReferenceType, ReferenceID: r.ReferenceID, Key: r.IdempotencyKey,
				ReasonCode: r.ReasonCode, ReasonText: r.ReasonText, ReservationID: r.ReservationID,
				CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
			})
			if err != nil {
				return err
			}
			rows = append(rows, entry)
		}
		return nil
	})
	if err != nil {
		return LedgerPage{}, err
	}
	page := LedgerPage{Items: rows}
	if len(rows) > pageSize {
		last := rows[pageSize-1]
		page.NextCursor = s.cursors.Encode(httpx.Cursor{CreatedAt: last.EffectiveAt, ID: last.ID})
		page.Items = rows[:pageSize]
	}
	return page, nil
}

// CreateAdjustment records a pending manual correction. Nothing moves until a different
// actor approves it; the request itself is audited.
func (s *Service) CreateAdjustment(ctx context.Context, rc identity.RequestContext,
	accountID uuid.UUID, in NewAdjustmentInput) (Adjustment, error) {
	ve := &domain.ValidationError{}
	in.ReasonCode = strings.TrimSpace(in.ReasonCode)
	if in.ReasonCode == "" {
		ve.Add("reasonCode", "REQUIRED", "gerekçe kodu zorunlu")
	}
	if in.Delta.IsZero() {
		ve.Add("deltaQuantity", "RANGE", "düzeltme miktarı sıfır olamaz")
	}
	if in.ReasonText != nil {
		domain.ValidateText(ve, "reasonText", *in.ReasonText, domain.MaxReasonLength)
	}
	if err := ve.OrNil(); err != nil {
		return Adjustment{}, err
	}

	var out Adjustment
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if err := accountExists(ctx, tx, rc.TenantID, accountID); err != nil {
			return err
		}
		id, err := sqlcgen.New(tx).CreateEntitlementAdjustment(ctx, sqlcgen.CreateEntitlementAdjustmentParams{
			TenantID: rc.TenantID, AccountID: accountID, DeltaQuantity: in.Delta.String(),
			ReasonCode: in.ReasonCode, ReasonText: in.ReasonText, RequestedBy: rc.Principal.ActorID,
		})
		if err != nil {
			return fmt.Errorf("benefit: create adjustment: %w", err)
		}
		if err := s.record(ctx, tx, rc, audit.CategoryBusiness, audit.OutcomeSuccess, "",
			"entitlement.adjustment.create", id, map[string]any{
				"account_id": accountID, "delta_quantity": in.Delta.String(), "reason_code": in.ReasonCode,
			}); err != nil {
			return err
		}
		out, err = loadAdjustment(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// ListAdjustments returns one keyset page of adjustments, newest first.
func (s *Service) ListAdjustments(ctx context.Context, rc identity.RequestContext, f AdjustmentFilter) (AdjustmentPage, error) {
	ve := &domain.ValidationError{}
	if f.Status != "" && !domain.Contains(AdjustmentStatuses, f.Status) {
		ve.Add("status", "ENUM", "geçersiz durum")
	}
	if err := ve.OrNil(); err != nil {
		return AdjustmentPage{}, err
	}
	if s.cursors == nil {
		return AdjustmentPage{}, errPagingUnavailable
	}
	position, hasCursor, err := s.cursors.Decode(f.Cursor)
	if err != nil {
		return AdjustmentPage{}, err
	}
	pageSize := httpx.ClampLimit(f.Limit)

	var rows []Adjustment
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		params := sqlcgen.ListEntitlementAdjustmentsParams{
			TenantID: rc.TenantID, Status: optString(f.Status),
			PageSize: int32(pageSize + 1), //nolint:gosec // clamped by httpx.ClampLimit
		}
		if hasCursor {
			params.CursorCreatedAt = &position.CreatedAt
			params.CursorID = uuid.NullUUID{UUID: position.ID, Valid: true}
		}
		list, err := sqlcgen.New(tx).ListEntitlementAdjustments(ctx, params)
		if err != nil {
			return fmt.Errorf("benefit: list adjustments: %w", err)
		}
		rows = make([]Adjustment, 0, len(list))
		for _, r := range list {
			adjustment, err := adjustmentFromRow(adjustmentRow{
				ID: r.ID, AccountID: r.EntitlementAccountID, Delta: r.DeltaQuantity,
				ReasonCode: r.ReasonCode, ReasonText: r.ReasonText, Status: r.Status,
				RequestedBy: r.RequestedBy, RequestedAt: r.RequestedAt, DecidedBy: r.DecidedBy,
				DecidedAt: r.DecidedAt, DecisionComment: r.DecisionComment,
				LedgerEntryID: r.LedgerEntryID, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
			})
			if err != nil {
				return err
			}
			rows = append(rows, adjustment)
		}
		return nil
	})
	if err != nil {
		return AdjustmentPage{}, err
	}
	page := AdjustmentPage{Items: rows}
	if len(rows) > pageSize {
		last := rows[pageSize-1]
		page.NextCursor = s.cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		page.Items = rows[:pageSize]
	}
	return page, nil
}

// ApproveAdjustment applies a pending correction and writes the ADJUST movement in the
// same transaction. The approver must differ from the requester; the refusal is audited
// in its own transaction because the command's transaction rolls back with it.
func (s *Service) ApproveAdjustment(ctx context.Context, rc identity.RequestContext,
	adjustmentID uuid.UUID, comment *string, expectedVersion int64) (Adjustment, error) {
	ve := &domain.ValidationError{}
	if comment != nil {
		domain.ValidateText(ve, "comment", *comment, domain.MaxCommentLength)
	}
	if err := ve.OrNil(); err != nil {
		return Adjustment{}, err
	}

	var out Adjustment
	var denied *Adjustment
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockAdjustment(ctx, tx, rc.TenantID, adjustmentID)
		if err != nil {
			return err
		}
		if current.RowVersion != expectedVersion {
			return ErrVersionMismatch
		}
		if current.Status != AdjustmentPending {
			return ErrAdjustmentNotPending
		}
		if current.RequestedBy == rc.Principal.ActorID {
			row := current
			denied = &row
			return ErrMakerCheckerSame
		}
		entry, err := s.ledger.Adjust(ctx, tx, AdjustInput{
			TenantID: rc.TenantID, AccountID: current.AccountID, Delta: current.Delta,
			Key:           "adjustment:" + current.ID.String(),
			ReferenceType: ReferenceAdjustment, ReferenceID: current.ID,
			ReasonCode: current.ReasonCode, ReasonText: deref(current.ReasonText),
			ActorID: rc.Principal.ActorID,
		})
		if err != nil {
			return err
		}
		if err := decide(ctx, tx, rc.TenantID, current, AdjustmentApproved,
			rc.Principal.ActorID, comment, &entry.ID); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, audit.CategoryBusiness, audit.OutcomeSuccess, "",
			"entitlement.adjustment.approve", current.ID, map[string]any{
				"account_id": current.AccountID, "delta_quantity": current.Delta.String(),
				"reason_code": current.ReasonCode, "ledger_entry_id": entry.ID,
			}); err != nil {
			return err
		}
		out, err = loadAdjustment(ctx, tx, rc.TenantID, current.ID)
		return err
	})
	if denied != nil {
		return Adjustment{}, s.auditDenial(ctx, rc, "entitlement.adjustment.approve", *denied, err)
	}
	return out, err
}

// RejectAdjustment closes a pending correction with a reason; nothing moves.
func (s *Service) RejectAdjustment(ctx context.Context, rc identity.RequestContext,
	adjustmentID uuid.UUID, reasonCode string, reasonText *string, expectedVersion int64) (Adjustment, error) {
	reasonCode = strings.TrimSpace(reasonCode)
	ve := &domain.ValidationError{}
	if reasonCode == "" {
		ve.Add("reasonCode", "REQUIRED", "gerekçe kodu zorunlu")
	}
	if reasonText != nil {
		domain.ValidateText(ve, "reasonText", *reasonText, domain.MaxCommentLength)
	}
	if err := ve.OrNil(); err != nil {
		return Adjustment{}, err
	}

	var out Adjustment
	var denied *Adjustment
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockAdjustment(ctx, tx, rc.TenantID, adjustmentID)
		if err != nil {
			return err
		}
		if current.RowVersion != expectedVersion {
			return ErrVersionMismatch
		}
		if current.Status != AdjustmentPending {
			return ErrAdjustmentNotPending
		}
		if current.RequestedBy == rc.Principal.ActorID {
			row := current
			denied = &row
			return ErrMakerCheckerSame
		}
		comment := optString(strings.TrimSpace(reasonCode + " " + deref(reasonText)))
		if err := decide(ctx, tx, rc.TenantID, current, AdjustmentRejected,
			rc.Principal.ActorID, comment, nil); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, audit.CategoryBusiness, audit.OutcomeSuccess, "",
			"entitlement.adjustment.reject", current.ID, map[string]any{
				"account_id": current.AccountID, "reason_code": reasonCode,
			}); err != nil {
			return err
		}
		out, err = loadAdjustment(ctx, tx, rc.TenantID, current.ID)
		return err
	})
	if denied != nil {
		return Adjustment{}, s.auditDenial(ctx, rc, "entitlement.adjustment.reject", *denied, err)
	}
	return out, err
}

// EnsureAccounts opens the accounts of one enrollment in its own transaction. It is the
// body of the benefit.enrollment.created consumer and the lazy repair path.
func (s *Service) EnsureAccounts(ctx context.Context, tenantID, enrollmentID uuid.UUID, asOf time.Time) (EnsureResult, error) {
	var out EnsureResult
	err := db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.ledger.EnsureAccounts(ctx, tx, tenantID, enrollmentID, asOf)
		return err
	})
	return out, err
}

// enrollmentCreatedPayload is the part of benefit.enrollment.created this module reads.
type enrollmentCreatedPayload struct {
	EnrollmentID uuid.UUID `json:"enrollmentId"`
	ValidFrom    string    `json:"validFrom"`
}

// HandleEnrollmentCreated is the outbox consumer that opens the entitlement accounts of
// a new enrollment. It is idempotent, as every outbox handler must be: re-delivery finds
// the accounts already open and changes nothing. A plan version that is not published on
// the enrollment start date is a permanent failure — retrying cannot fix a configuration
// gap — and so is a payload this handler cannot read.
func (s *Service) HandleEnrollmentCreated(ctx context.Context, d outbox.Delivery) error {
	if !d.TenantID.Valid {
		return outbox.Permanent(errors.New("benefit: enrollment event without a tenant"))
	}
	var payload enrollmentCreatedPayload
	if err := unmarshalPayload(d.Payload, &payload); err != nil {
		return outbox.Permanent(err)
	}
	if payload.EnrollmentID == uuid.Nil {
		payload.EnrollmentID = d.AggregateID
	}
	asOf := s.now()
	if payload.ValidFrom != "" {
		parsed, err := time.Parse(time.DateOnly, payload.ValidFrom)
		if err != nil {
			return outbox.Permanent(fmt.Errorf("benefit: enrollment event validFrom: %w", err))
		}
		asOf = parsed
	}

	result, err := s.EnsureAccounts(ctx, d.TenantID.UUID, payload.EnrollmentID, asOf)
	switch {
	case errors.Is(err, ErrEnrollmentNotFound), errors.Is(err, ErrPeriodUnsupported):
		return outbox.Permanent(err)
	case errors.Is(err, application.ErrNoPublishedVersion):
		return outbox.Permanent(err)
	case err != nil:
		return err
	}
	s.logger.Info("entitlement accounts ensured",
		"enrollment_id", payload.EnrollmentID, "opened", result.Opened, "accounts", len(result.Accounts))
	return nil
}

// ExpireReservations releases every hold whose expires_at has passed, tenant by tenant
// and in batches, until nothing is left. It is the body of the
// entitlement.reservation.expire job.
func (s *Service) ExpireReservations(ctx context.Context, now time.Time) (int, error) {
	tenants, err := s.activeTenants(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, tenantID := range tenants {
		for {
			released := 0
			err := db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				released, err = s.ledger.Expire(ctx, tx, tenantID, now, ExpireBatchSize)
				return err
			})
			total += released
			if err != nil {
				return total, fmt.Errorf("benefit: expire reservations of tenant %s: %w", tenantID, err)
			}
			if released < ExpireBatchSize {
				break
			}
		}
	}
	return total, nil
}

// ReconcileMetrics is what the reconciliation job reports.
type ReconcileMetrics struct {
	Tenants  int
	Accounts int64
	Drifts   int
}

// Reconcile proves, for every tenant, that the account balances equal the sum of their
// ledger movements. An account that disagrees is frozen, audited as a SYSTEM security
// event and announced on the outbox for alerting — all in the same transaction, so an
// alert without a freeze is impossible.
func (s *Service) Reconcile(ctx context.Context, now time.Time) (ReconcileMetrics, error) {
	tenants, err := s.activeTenants(ctx)
	if err != nil {
		return ReconcileMetrics{}, err
	}
	metrics := ReconcileMetrics{Tenants: len(tenants)}
	for _, tenantID := range tenants {
		err := db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
			count, err := s.ledger.CountAccounts(ctx, tx, tenantID)
			if err != nil {
				return err
			}
			metrics.Accounts += count
			drifts, err := s.ledger.Reconcile(ctx, tx, tenantID, ReconcileBatchSize)
			if err != nil {
				return err
			}
			for _, drift := range drifts {
				metrics.Drifts++
				if err := s.audit.Record(ctx, tx, audit.Event{
					TenantID: uuid.NullUUID{UUID: tenantID, Valid: true},
					// The schema's category set (migration 000007) has no SYSTEM value;
					// a balance that no longer matches its ledger is an integrity alarm,
					// so it is recorded as SECURITY with no actor.
					Category: audit.CategorySecurity, ActionCode: "entitlement.drift",
					ResourceType: "entitlement_account",
					ResourceID:   uuid.NullUUID{UUID: drift.AccountID, Valid: true},
					Outcome:      audit.OutcomeFailure, ReasonCode: "LEDGER_BALANCE_DRIFT",
					Detail: drift.Detail(),
				}); err != nil {
					return err
				}
				if _, _, err := outbox.Publish(ctx, tx, outbox.Event{
					TenantID:         uuid.NullUUID{UUID: tenantID, Valid: true},
					AggregateType:    "benefit.entitlement_account",
					AggregateID:      drift.AccountID,
					Type:             DriftEvent,
					Payload:          drift.Detail(),
					DeduplicationKey: fmt.Sprintf("%s:%s", drift.AccountID, now.UTC().Format(time.DateOnly)),
				}); err != nil {
					return err
				}
				s.logger.Error("entitlement account drift detected; account frozen",
					"tenant_id", tenantID, "account_id", drift.AccountID)
			}
			return nil
		})
		if err != nil {
			return metrics, fmt.Errorf("benefit: reconcile tenant %s: %w", tenantID, err)
		}
	}
	return metrics, nil
}

// activeTenants lists the tenants the jobs iterate. platform.tenant carries no RLS, so
// this read runs outside a tenant transaction like the other cross-tenant jobs do.
func (s *Service) activeTenants(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM platform.tenant WHERE status IN ('ACTIVE','SUSPENDED')`)
	if err != nil {
		return nil, fmt.Errorf("benefit: list tenants: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// record writes one entitlement_adjustment audit row inside the caller's transaction;
// detail carries ids, codes and exact decimal text only.
func (s *Service) record(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	category audit.Category, outcome audit.Outcome, reason, action string,
	resourceID uuid.UUID, detail map[string]any) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID),
		Category:     category, ActionCode: action,
		ResourceType: "entitlement_adjustment", ResourceID: nullUUID(resourceID),
		Outcome: outcome, ReasonCode: reason, Detail: detail,
	})
}

// auditDenial records a refused maker-checker decision in its own transaction, because
// the command's transaction rolled back with it. A failure to audit is reported
// alongside the refusal: a denial that leaves no trace is worse than a noisy error.
func (s *Service) auditDenial(ctx context.Context, rc identity.RequestContext,
	action string, row Adjustment, cause error) error {
	auditErr := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		return s.record(ctx, tx, rc, audit.CategorySecurity, audit.OutcomeDenied, "MAKER_CHECKER_SAME_ACTOR",
			action, row.ID, map[string]any{
				"account_id": row.AccountID, "requested_by": row.RequestedBy,
			})
	})
	if auditErr != nil {
		return errors.Join(cause, auditErr)
	}
	return cause
}

// unmarshalPayload decodes an outbox payload, refusing unknown fields so a producer that
// renames a field is caught instead of being silently ignored.
func unmarshalPayload(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("benefit: decode outbox payload: %w", err)
	}
	return nil
}

func tenantCtx(rc identity.RequestContext) db.TenantContext {
	return db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}
}

// accountExists answers 404 for an unknown or foreign account without reading balances.
func accountExists(ctx context.Context, tx pgx.Tx, tenantID, accountID uuid.UUID) error {
	_, err := sqlcgen.New(tx).GetEntitlementAccountDetail(ctx, sqlcgen.GetEntitlementAccountDetailParams{
		TenantID: tenantID, ID: accountID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAccountNotFound
	}
	if err != nil {
		return fmt.Errorf("benefit: read entitlement account: %w", err)
	}
	return nil
}

// loadAccount reads one account with its open reservations.
func loadAccount(ctx context.Context, tx pgx.Tx, tenantID, accountID uuid.UUID) (Account, error) {
	q := sqlcgen.New(tx)
	r, err := q.GetEntitlementAccountDetail(ctx, sqlcgen.GetEntitlementAccountDetailParams{
		TenantID: tenantID, ID: accountID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("benefit: read entitlement account: %w", err)
	}
	balances, err := parseBalances(r.TotalGranted, r.AvailableQuantity, r.ReservedQuantity,
		r.ConsumedQuantity, r.ExpiredQuantity)
	if err != nil {
		return Account{}, err
	}
	account := Account{
		ID: r.ID, EnrollmentID: r.EnrollmentID, PersonID: r.PersonID,
		PeriodFrom: dateValue(r.PeriodFrom), PeriodTo: datePtr(r.PeriodTo),
		Balances: balances, Status: r.Status,
		Definition: DefinitionSummary{
			ID: r.DefinitionID, Code: r.DefinitionCode, Name: r.DefinitionName,
			UnitType: r.UnitType, CurrencyCode: deref(r.CurrencyCode),
			FamilyShared: r.FamilyShared, AllowOverdraft: r.AllowOverdraft,
		},
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
	holds, err := q.ListOpenEntitlementReservations(ctx, sqlcgen.ListOpenEntitlementReservationsParams{
		TenantID: tenantID, EntitlementAccountID: accountID,
	})
	if err != nil {
		return Account{}, fmt.Errorf("benefit: list open reservations: %w", err)
	}
	for _, h := range holds {
		reservation, err := reservationFromRow(reservationRow{
			ID: h.ID, AccountID: h.EntitlementAccountID, ReferenceType: h.ReferenceType,
			ReferenceID: h.ReferenceID, Quantity: h.Quantity, Consumed: h.ConsumedQuantity,
			Released: h.ReleasedQuantity, Status: h.Status, ExpiresAt: h.ExpiresAt,
			Key: h.IdempotencyKey, CreatedAt: h.CreatedAt, RowVersion: h.RowVersion,
		})
		if err != nil {
			return Account{}, err
		}
		account.OpenReservations = append(account.OpenReservations, reservation)
	}
	return account, nil
}

// adjustmentRow is the shape shared by the three adjustment reads.
type adjustmentRow struct {
	ID              uuid.UUID
	AccountID       uuid.UUID
	Delta           string
	ReasonCode      string
	ReasonText      *string
	Status          string
	RequestedBy     uuid.UUID
	RequestedAt     time.Time
	DecidedBy       uuid.NullUUID
	DecidedAt       *time.Time
	DecisionComment *string
	LedgerEntryID   uuid.NullUUID
	CreatedAt       time.Time
	RowVersion      int64
}

func adjustmentFromRow(r adjustmentRow) (Adjustment, error) {
	delta, err := domain.ParseQuantity(r.Delta)
	if err != nil {
		return Adjustment{}, fmt.Errorf("benefit: adjustment %s delta: %w", r.ID, err)
	}
	return Adjustment{
		ID: r.ID, AccountID: r.AccountID, Delta: delta, ReasonCode: r.ReasonCode,
		ReasonText: r.ReasonText, Status: r.Status, RequestedBy: r.RequestedBy,
		RequestedAt: r.RequestedAt, DecidedBy: uuidPtr(r.DecidedBy), DecidedAt: r.DecidedAt,
		DecisionComment: r.DecisionComment, LedgerEntryID: uuidPtr(r.LedgerEntryID),
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}, nil
}

func loadAdjustment(ctx context.Context, tx pgx.Tx, tenantID, adjustmentID uuid.UUID) (Adjustment, error) {
	r, err := sqlcgen.New(tx).GetEntitlementAdjustment(ctx, sqlcgen.GetEntitlementAdjustmentParams{
		TenantID: tenantID, ID: adjustmentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Adjustment{}, ErrAdjustmentNotFound
	}
	if err != nil {
		return Adjustment{}, fmt.Errorf("benefit: read adjustment: %w", err)
	}
	return adjustmentFromRow(adjustmentRow{
		ID: r.ID, AccountID: r.EntitlementAccountID, Delta: r.DeltaQuantity,
		ReasonCode: r.ReasonCode, ReasonText: r.ReasonText, Status: r.Status,
		RequestedBy: r.RequestedBy, RequestedAt: r.RequestedAt, DecidedBy: r.DecidedBy,
		DecidedAt: r.DecidedAt, DecisionComment: r.DecisionComment,
		LedgerEntryID: r.LedgerEntryID, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	})
}

func lockAdjustment(ctx context.Context, tx pgx.Tx, tenantID, adjustmentID uuid.UUID) (Adjustment, error) {
	r, err := sqlcgen.New(tx).GetEntitlementAdjustmentForUpdate(ctx, sqlcgen.GetEntitlementAdjustmentForUpdateParams{
		TenantID: tenantID, ID: adjustmentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Adjustment{}, ErrAdjustmentNotFound
	}
	if err != nil {
		return Adjustment{}, fmt.Errorf("benefit: lock adjustment: %w", err)
	}
	return adjustmentFromRow(adjustmentRow{
		ID: r.ID, AccountID: r.EntitlementAccountID, Delta: r.DeltaQuantity,
		ReasonCode: r.ReasonCode, ReasonText: r.ReasonText, Status: r.Status,
		RequestedBy: r.RequestedBy, RequestedAt: r.RequestedAt, DecidedBy: r.DecidedBy,
		DecidedAt: r.DecidedAt, DecisionComment: r.DecisionComment,
		LedgerEntryID: r.LedgerEntryID, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	})
}

// decide writes the maker-checker outcome; ck_adjustment_maker_checker is the database's
// own guard behind the service-side check.
func decide(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, current Adjustment,
	status string, decider uuid.UUID, comment *string, ledgerEntry *uuid.UUID) error {
	affected, err := sqlcgen.New(tx).DecideEntitlementAdjustment(ctx, sqlcgen.DecideEntitlementAdjustmentParams{
		TenantID: tenantID, ID: current.ID, RowVersion: current.RowVersion, Status: status,
		DecidedBy: nullUUID(decider), DecisionComment: comment, LedgerEntryID: optUUID(ledgerEntry),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == checkViolation &&
			pgErr.ConstraintName == "ck_adjustment_maker_checker" {
			return ErrMakerCheckerSame
		}
		return fmt.Errorf("benefit: decide adjustment: %w", err)
	}
	if affected == 0 {
		return ErrVersionMismatch
	}
	return nil
}
