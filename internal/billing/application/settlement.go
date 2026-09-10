package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// The settlement: what the payer owes the provider for one decided icmal (WP-I7-04 section
// 2.2).
//
// Three sentences carry this file.
//
// **Nobody raises a settlement.** It is opened by the outbox handler of `batch.decided`,
// because what is owed is the arithmetic of the decisions the payer already took. A settlement
// somebody could create by hand would be a figure with no batch behind it, and the first thing
// a dispute would ask is where it came from.
//
// **A second delivery opens nothing.** The outbox delivers at least once, so the handler looks
// for the settlement a first delivery made and stops when it finds one — and
// `uq_billing_settlement_live_batch` is underneath that read, for the case where two deliveries
// look at the same moment and both find nothing.
//
// **A settlement is never edited.** A wrong one is CANCELLED with a reason and a new version is
// opened on the same batch. The recoveries the cancelled one had netted go back to being open,
// which is what lets the replacement net them again.

// batchEventPayload is the part of WP-I7-03's two batch events this package reads. Everything
// else in them is somebody else's business, and a consumer that unmarshalled the whole thing
// would be a consumer that broke when a field was added.
type batchEventPayload struct {
	BatchID uuid.UUID `json:"batchId"`
}

// HandleBatchSubmitted consumes `batch.submitted` and deliberately does nothing.
//
// It exists because an outbox event with no handler is not ignored: the dispatcher defers it
// an hour and logs a warning, for ever. WP-I7-03 published both batch events and consumed
// neither, so a running worker warns about `batch.submitted` once an hour until somebody
// registers this.
//
// There is nothing for the settlement to do at submission and there should not be. The payer's
// finance queue already has the work item and the payer already has the notification, both
// written inside `submitBatch`'s own transaction; a settlement opened before anybody had
// decided anything would be a settlement for a figure nobody had agreed.
func (s *Service) HandleBatchSubmitted(_ context.Context, d outbox.Delivery) error {
	if _, _, err := batchEvent(d); err != nil {
		return err
	}
	return nil
}

// HandleBatchDecided opens the settlement of one decided icmal.
//
// Everything happens in one transaction: the header, the recoveries it nets, the marks that
// stop them being netted twice, and the move to PENDING_APPROVAL. A settlement in DRAFT with
// its recoveries already marked, or a settlement awaiting approval whose recoveries nobody
// took, are states no reader ever observes.
func (s *Service) HandleBatchDecided(ctx context.Context, d outbox.Delivery) error {
	tenantID, payload, err := batchEvent(d)
	if err != nil {
		return err
	}
	rc := identity.RequestContext{TenantID: tenantID}
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		// The first half of the idempotency. A redelivered event finds the settlement the
		// first delivery opened and writes nothing at all.
		if _, found, err := s.settlements.FindLiveSettlementForBatch(ctx, tx, tenantID,
			payload.BatchID); err != nil || found {
			return err
		}
		return s.openSettlement(ctx, tx, rc, payload.BatchID)
	})
	switch {
	case errors.Is(err, ErrSettlementTransitionInvalid):
		// The other half. Two deliveries looked at the same moment, both found nothing, and
		// `uq_billing_settlement_live_batch` refused the second. That is the guarantee working,
		// not a failure.
		return nil
	case errors.Is(err, ErrBatchNotFound), errors.Is(err, ErrPaymentTermMissing):
		// Neither is retryable by waiting. A batch that is not there will not appear, and a
		// contract with no payment term needs a person to add one — after which the
		// dead-lettered event is replayed and the settlement opens. Retrying ten times first
		// would only delay the moment somebody is told.
		return outbox.Permanent(err)
	default:
		return err
	}
}

// batchEvent decodes what both handlers read. A malformed event is permanent: retrying a
// payload that cannot be parsed would retry it for ever.
func batchEvent(d outbox.Delivery) (uuid.UUID, batchEventPayload, error) {
	if !d.TenantID.Valid {
		return uuid.Nil, batchEventPayload{},
			outbox.Permanent(errors.New("billing: a batch event carries no tenant"))
	}
	var payload batchEventPayload
	if err := json.Unmarshal(d.Payload, &payload); err != nil {
		return uuid.Nil, batchEventPayload{},
			outbox.Permanent(fmt.Errorf("billing: decode batch event: %w", err))
	}
	if payload.BatchID == uuid.Nil {
		return uuid.Nil, batchEventPayload{},
			outbox.Permanent(errors.New("billing: a batch event names no batch"))
	}
	return d.TenantID.UUID, payload, nil
}

// openSettlement writes the whole settlement, in the order the schema requires.
func (s *Service) openSettlement(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	batchID uuid.UUID,
) error {
	batch, err := s.settlements.BatchForSettlement(ctx, tx, rc.TenantID, batchID)
	if err != nil {
		return err
	}
	if batch.Status != domain.BatchDecided && batch.Status != domain.BatchSettling {
		// A stale event about a batch somebody has since cancelled. There is nothing to
		// settle and nothing to retry.
		return outbox.Permanent(ErrSettlementTransitionInvalid)
	}
	decidedAt := s.now().UTC()
	if batch.DecidedAt != nil {
		decidedAt = *batch.DecidedAt
	}

	// The due date is the contract's, applied to the day the batch was decided. A provider
	// with no profile has no contract to carry a term, which is the same answer as a contract
	// with no term: this platform will not invent a due date.
	term := PaymentTerm{}
	if batch.ProviderProfileID != nil {
		term, err = s.settlements.PaymentTermFor(ctx, tx, rc.TenantID, *batch.ProviderProfileID,
			batch.PayerOrganizationID, decidedAt)
		if err != nil {
			return err
		}
	}
	if !term.Found {
		return ErrPaymentTermMissing
	}

	approved := quantityOrZero(batch.ApprovedTotal)
	recoveries, err := s.settlements.ListOpenRecoveries(ctx, tx, rc.TenantID,
		batch.ProviderOrganizationID)
	if err != nil {
		return err
	}
	netted, withheld := netRecoveries(recoveries, approved)
	payable := approved.Sub(withheld)

	record, err := s.createSettlementWithReference(ctx, tx, rc, NewSettlementRow{
		BatchID: batch.ID, ProviderOrganizationID: batch.ProviderOrganizationID,
		PayerOrganizationID: batch.PayerOrganizationID, CurrencyCode: batch.CurrencyCode,
		ApprovedAmount: approved.String(), WithheldAmount: withheld.String(),
		PayableAmount:    payable.String(),
		DueDate:          domain.DueDate(decidedAt, term.DueDays),
		SettlementMethod: term.SettlementMethod, Status: domain.SettlementDraft,
	})
	if err != nil {
		return err
	}

	// Each netted recovery is marked before the settlement leaves DRAFT, so a second
	// settlement on another batch of the same provider cannot take the same money back twice.
	for _, recovery := range netted {
		if err := s.settlements.CreateSettlementRecovery(ctx, tx, rc.TenantID, record.ID,
			recovery.ClaimID, recovery.AdjustmentID, recovery.Amount, nil); err != nil {
			return err
		}
	}

	moved, err := s.settlements.SetSettlementStatus(ctx, tx, rc.TenantID, record.ID,
		domain.SettlementPendingApproval, []string{domain.SettlementDraft}, nil)
	if err != nil {
		return err
	}
	if !moved {
		return ErrSettlementTransitionInvalid
	}

	return s.recordSettlement(ctx, tx, rc, "settlement.open", record.ID, map[string]any{
		"reference":       record.Reference,
		"batch_reference": batch.Reference,
		"version_no":      record.VersionNo,
		"approved_amount": approved.String(),
		"withheld_amount": withheld.String(),
		"payable_amount":  payable.String(),
		"recovery_count":  len(netted),
		"due_date":        record.DueDate.Format(time.DateOnly),
		"currency_code":   record.CurrencyCode,
	})
}

// netRecoveries decides which open recoveries this settlement takes back, and for how much.
//
// Whole recoveries only, oldest first, while they still fit inside the approved total. A
// recovery larger than what is left is skipped rather than split: `claim.adjustment` is
// append-only and splitting one would mean writing a second adjustment nobody decided, and the
// skipped recovery is still open for the provider's next icmal. The consequence is that
// `payable_amount` never goes below nought without the CHECK having to catch it, which is the
// point.
func netRecoveries(recoveries []OpenRecovery, approved benefitdomain.Quantity,
) ([]OpenRecovery, benefitdomain.Quantity) {
	netted := make([]OpenRecovery, 0, len(recoveries))
	withheld := zero()
	for _, recovery := range recoveries {
		amount := quantityOrZero(recovery.Amount)
		if !amount.IsPositive() {
			continue
		}
		if withheld.Add(amount).Cmp(approved) > 0 {
			continue
		}
		withheld = withheld.Add(amount)
		netted = append(netted, recovery)
	}
	return netted, withheld
}

// createSettlementWithReference writes the header, retrying the reference on the collision the
// unique index refuses and taking the next version number of the batch. The tail is forty
// random bits, so a second attempt is already unlikely; the loop exists so a collision is a
// retry rather than an error somebody has to read.
func (s *Service) createSettlementWithReference(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, in NewSettlementRow,
) (SettlementRecord, error) {
	version, err := s.settlements.NextSettlementVersion(ctx, tx, rc.TenantID, in.BatchID)
	if err != nil {
		return SettlementRecord{}, err
	}
	in.VersionNo = version
	var lastErr error
	for attempt := 0; attempt < batchReferenceAttempts; attempt++ {
		reference, err := domain.NewSettlementReference(s.now())
		if err != nil {
			return SettlementRecord{}, err
		}
		in.Reference = reference
		record, err := s.settlements.CreateSettlement(ctx, tx, rc.TenantID, in)
		if err == nil {
			return record, nil
		}
		if !errors.Is(err, ErrSettlementReferenceTaken) {
			return SettlementRecord{}, err
		}
		lastErr = err
	}
	return SettlementRecord{}, lastErr
}

// ApproveSettlement releases what the batch owes (WP-I7-04 section 2.2).
//
// Three gates, in the order a caller meets them.
//
// **Step-up**, above the tenant's threshold: releasing more than that on one person's word
// means re-entering a password. It is the same shape `submitBatch` uses and the comparison is
// on exact decimals, never on a float.
//
// **The second pair of eyes**, above the same threshold: the person who decided the batch may
// not be the person who approves its settlement. That is WP-I4-03 section 2.4's rule applied to
// the money rather than to the document, and it is deliberately about the batch's decider
// rather than about anybody who touched it — what the threshold buys is a second pair of eyes
// on the release, not a rule that a reviewer may never see their own work again.
//
// **The status**: only a settlement waiting for approval can be approved, and the row version
// the caller held has to still be the row's.
func (s *Service) ApproveSettlement(ctx context.Context, rc identity.RequestContext,
	settlementID uuid.UUID, expected int64,
) (SettlementView, error) {
	var out SettlementView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		record, err := s.settlements.LockSettlement(ctx, tx, rc.TenantID, settlementID,
			scopeOf(rc))
		if err != nil {
			return err
		}
		if err := checkProviderScope(rc, record.ProviderOrganizationID); err != nil {
			return err
		}
		if !domain.CanApproveSettlement(record.Status) {
			return ErrSettlementTransitionInvalid
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}

		values, err := s.settingsFor(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}
		payable := quantityOrZero(record.PayableAmount)
		threshold := quantityOrZero(values.SettlementCheckerThreshold)
		if err := requireStepUp(rc, payable, threshold); err != nil {
			return err
		}

		// The locking read does not carry the batch's decider — it takes no joins under a
		// `FOR UPDATE` — so the batch is read for it here, and only when the threshold makes
		// it matter.
		var checker *uuid.UUID
		if payable.Cmp(threshold) > 0 {
			batch, err := s.settlements.BatchForSettlement(ctx, tx, rc.TenantID, record.BatchID)
			if err != nil {
				return err
			}
			if batch.DecidedBy != nil && *batch.DecidedBy == rc.Principal.ActorID {
				return ErrSettlementDeciderCannotApprove
			}
			if batch.DecidedBy != nil {
				decider := *batch.DecidedBy
				checker = &decider
			}
		}

		now := s.now().UTC()
		approved, err := s.settlements.ApproveSettlement(ctx, tx, rc.TenantID, record.ID,
			ApproveSettlementRow{
				ApprovedBy: rc.Principal.ActorID, ApprovedAt: now,
				// `checked_by` records the *other* pair of eyes: the person who decided the
				// batch, whose decision this approval is the second signature on. It is the
				// batch's decider rather than the approver, because the approver is already
				// `approved_by` and a row naming one person twice would record a check that
				// never happened — which is exactly what `ck_billing_settlement_checker`
				// refuses.
				CheckedBy: checker, ActorID: actorPtr(rc.Principal.ActorID),
			}, expected)
		if err != nil {
			return err
		}
		if !approved {
			return ErrVersionMismatch
		}

		if err := s.publishSettlementApproved(ctx, tx, rc, record, now); err != nil {
			return err
		}
		if err := s.notifySettlementApproved(ctx, tx, rc, record, now); err != nil {
			return err
		}
		if err := s.recordSettlement(ctx, tx, rc, "settlement.approve", record.ID,
			map[string]any{
				"reference":       record.Reference,
				"batch_id":        record.BatchID.String(),
				"payable_amount":  record.PayableAmount,
				"approved_amount": record.ApprovedAmount,
				"withheld_amount": record.WithheldAmount,
				"currency_code":   record.CurrencyCode,
				"due_date":        record.DueDate.Format(time.DateOnly),
				"checked":         checker != nil,
			}); err != nil {
			return err
		}
		out, err = s.loadSettlement(ctx, tx, rc, record.ID)
		return err
	})
	if err != nil {
		return SettlementView{}, err
	}
	return out, nil
}

// CancelSettlement withdraws a wrong settlement with a reason and opens the next version on the
// same batch.
//
// It is the only way a settlement's figures ever change, and they change by not being that
// settlement's any more. A settlement money has already moved against is not cancellable: the
// answer there is a dispute on the payment record, and pretending the settlement never existed
// would leave a payment pointing at a document nobody can explain.
func (s *Service) CancelSettlement(ctx context.Context, rc identity.RequestContext,
	settlementID uuid.UUID, reasonCode string, reasonText *string, expected int64,
) (SettlementView, error) {
	if !domain.ValidReasonCode(reasonCode) {
		return SettlementView{}, fieldError("reasonCode", "FORMAT",
			"gerekçe kodu BÜYÜK_HARF biçiminde olmalı")
	}
	if reasonText != nil && !domain.ValidCancelReasonText(*reasonText) {
		return SettlementView{}, fieldError("reasonText", "MAX_LENGTH",
			"gerekçe metni en fazla 1000 karakter olabilir")
	}

	var out SettlementView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		record, err := s.settlements.LockSettlement(ctx, tx, rc.TenantID, settlementID,
			scopeOf(rc))
		if err != nil {
			return err
		}
		if err := checkProviderScope(rc, record.ProviderOrganizationID); err != nil {
			return err
		}
		if record.Status == domain.SettlementCancelled {
			return ErrSettlementTransitionInvalid
		}
		if quantityOrZero(record.PaidAmount).IsPositive() {
			return ErrSettlementHasPayments
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}

		cancelled, err := s.settlements.CancelSettlement(ctx, tx, rc.TenantID, record.ID,
			reasonCode, reasonText, actorPtr(rc.Principal.ActorID), expected)
		if err != nil {
			return err
		}
		if !cancelled {
			return ErrSettlementTransitionInvalid
		}
		// The recoveries this settlement had netted are open again. They are released rather
		// than left marked, because a recovery marked against a cancelled settlement would be
		// money the payer never actually took back and never could again.
		if err := s.settlements.DeleteSettlementRecoveries(ctx, tx, rc.TenantID,
			record.ID); err != nil {
			return err
		}
		if err := s.recordSettlement(ctx, tx, rc, "settlement.cancel", record.ID,
			map[string]any{
				"reference":      record.Reference,
				"batch_id":       record.BatchID.String(),
				"version_no":     record.VersionNo,
				"reason_code":    reasonCode,
				"payable_amount": record.PayableAmount,
				"currency_code":  record.CurrencyCode,
			}); err != nil {
			return err
		}
		out, err = s.loadSettlement(ctx, tx, rc, record.ID)
		return err
	})
	if err != nil {
		return SettlementView{}, err
	}
	return out, nil
}

// GetSettlement answers one settlement with its recoveries and its payment records.
func (s *Service) GetSettlement(ctx context.Context, rc identity.RequestContext,
	settlementID uuid.UUID,
) (SettlementView, error) {
	var out SettlementView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		var err error
		out, err = s.loadSettlement(ctx, tx, rc, settlementID)
		return err
	})
	if err != nil {
		return SettlementView{}, err
	}
	return out, nil
}

// SettlementFilter is the listSettlements request, before it has been validated.
type SettlementFilter struct {
	Cursor                 string
	Limit                  int
	ProviderOrganizationID *uuid.UUID
	PayerOrganizationID    *uuid.UUID
	BatchID                *uuid.UUID
	Status                 string
	CurrencyCode           string
	DueFrom                *time.Time
	DueTo                  *time.Time
}

// ListSettlements answers one page of settlements, newest first.
func (s *Service) ListSettlements(ctx context.Context, rc identity.RequestContext,
	f SettlementFilter,
) (SettlementPage, error) {
	ve := &domain.ValidationError{}
	query := SettlementQuery{
		Scope: scopeOf(rc), ProviderOrganizationID: f.ProviderOrganizationID,
		PayerOrganizationID: f.PayerOrganizationID, BatchID: f.BatchID,
		DueFrom: f.DueFrom, DueTo: f.DueTo,
	}
	if f.Status != "" {
		if !domain.ValidSettlementStatus(f.Status) {
			ve.Add("status", "ENUM", "tanımlı bir settlement durumu olmalı")
		} else {
			status := f.Status
			query.Status = &status
		}
	}
	if f.CurrencyCode != "" {
		if !domain.ValidCurrency(f.CurrencyCode) {
			ve.Add("currencyCode", "FORMAT", "üç harfli para birimi kodu olmalı")
		} else {
			code := f.CurrencyCode
			query.CurrencyCode = &code
		}
	}
	if err := ve.OrNil(); err != nil {
		return SettlementPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return SettlementPage{}, err
	}
	query.After = after
	query.PageSize = pageSize + 1

	var out SettlementPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		rows, err := s.settlements.ListSettlements(ctx, tx, rc.TenantID, query)
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			out.NextCursor = s.cursors.Encode(httpx.Cursor{
				CreatedAt: rows[pageSize-1].CreatedAt, ID: rows[pageSize-1].ID,
			})
			rows = rows[:pageSize]
		}
		if rows == nil {
			rows = []SettlementRecord{}
		}
		out.Items = rows
		return nil
	})
	if err != nil {
		return SettlementPage{}, err
	}
	return out, nil
}

// loadSettlement assembles the view a caller reads: the header, the recoveries it netted and
// the payments recorded against it, in one place, so every command answers the same shape.
func (s *Service) loadSettlement(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	settlementID uuid.UUID,
) (SettlementView, error) {
	record, err := s.settlements.GetSettlement(ctx, tx, rc.TenantID, settlementID, scopeOf(rc))
	if err != nil {
		return SettlementView{}, err
	}
	recoveries, err := s.settlements.ListSettlementRecoveries(ctx, tx, rc.TenantID, record.ID)
	if err != nil {
		return SettlementView{}, err
	}
	payments, err := s.settlements.ListPaymentRecords(ctx, tx, rc.TenantID, record.ID,
		scopeOf(rc))
	if err != nil {
		return SettlementView{}, err
	}
	if recoveries == nil {
		recoveries = []SettlementRecoveryRecord{}
	}
	if payments == nil {
		payments = []PaymentRecordRecord{}
	}
	return SettlementView{Settlement: record, Recoveries: recoveries, Payments: payments}, nil
}

// requireSettlements refuses every settlement command in a process wired without the
// repository. It is a programming error rather than a user one and is answered as one.
func (s *Service) requireSettlements() error {
	if s.settlements == nil {
		return errors.New("billing: no settlement repository is wired")
	}
	return nil
}

// recordSettlement writes one business audit row about a settlement.
//
// Everything that reaches a detail map here is an id, a code, a count, a status, a date or an
// exact decimal. Every key is snake_case, because `audit.SanitizeDetail` keeps only keys
// matching `^[a-z][a-z0-9_]*$` and drops everything else silently — a camelCase key here would
// be an audit row that recorded nothing and said so to nobody.
func (s *Service) recordSettlement(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action string, settlementID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: domain.AggregateSettlement,
		ResourceID: nullUUID(settlementID), Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// publishSettlementApproved writes the outbox row an approved settlement owes, inside the
// command's own transaction. M9's accounting posting listens for it; nothing is posted here.
//
// The payload is identifiers, a reference, a currency, a date and exact decimals. There is no
// tax identity and no invoice number in it: an outbox payload is read by every consumer,
// including ones written later.
func (s *Service) publishSettlementApproved(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, record SettlementRecord, now time.Time,
) error {
	payload := map[string]any{
		"settlementId":           record.ID,
		"reference":              record.Reference,
		"batchId":                record.BatchID,
		"versionNo":              record.VersionNo,
		"providerOrganizationId": record.ProviderOrganizationID,
		"currencyCode":           record.CurrencyCode,
		"approvedAmount":         record.ApprovedAmount,
		"withheldAmount":         record.WithheldAmount,
		"payableAmount":          record.PayableAmount,
		"dueDate":                record.DueDate.Format(time.DateOnly),
		"settlementMethod":       record.SettlementMethod,
		"approvedAt":             now.Format(time.RFC3339),
	}
	if record.PayerOrganizationID != nil {
		payload["payerOrganizationId"] = *record.PayerOrganizationID
	}
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID:      nullUUID(rc.TenantID),
		AggregateType: domain.AggregateSettlement, AggregateID: record.ID,
		Type: SettlementApprovedEvent, Payload: payload,
		// The settlement's own id: one settlement is approved once, and a redelivered command
		// that moved nothing publishes no second event either.
		DeduplicationKey: record.ID.String(),
	})
	return err
}
