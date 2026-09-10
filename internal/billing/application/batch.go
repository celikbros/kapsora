package application

import (
	"context"
	"errors"
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

// The provider's side of the icmal: opening one, saying what is in it, and sending it.
//
// Everything below is one transaction per command, opened with db.WithTenantTx, so a batch
// that was submitted, invoices that never moved into it and a work item nobody raised is a
// state no reader ever observes.

// The outbox types the icmal publishes. Both are published now and consumed later — the
// settlement of WP-I7-04 opens on `batch.decided` — because a consumer added afterwards can be
// replayed and a fact never recorded cannot be recovered.
const (
	BatchSubmittedEvent = "batch.submitted"
	BatchDecidedEvent   = "batch.decided"
)

// ErrBatchReferenceTaken is `uq_billing_batch_reference` answering. It never reaches a caller:
// the create retries with a new random tail, and the tail is forty random bits.
var ErrBatchReferenceTaken = errors.New("billing: the batch reference is already used")

// batchReferenceAttempts is how many times a create retries a reference collision. The tail is
// forty random bits, so two attempts is already generous; the loop exists so that a collision
// is a retry rather than an error somebody has to read.
const batchReferenceAttempts = 5

// CreateBatchInput is the createBatch command.
type CreateBatchInput struct {
	ProviderOrganizationID uuid.UUID
	PayerOrganizationID    *uuid.UUID
	DomainCode             string
	CurrencyCode           string
	PeriodFrom             time.Time
	PeriodTo               time.Time
}

// CreateBatch opens a draft icmal for one provider, one payer, one currency, one domain and
// one period.
//
// All five are fixed here rather than derived from whatever is put in afterwards, and that is
// the whole reason `putBatchInvoices` can refuse a mixture: a total across currencies is not a
// total, and a batch spanning two payers is two conversations in one document.
func (s *Service) CreateBatch(ctx context.Context, rc identity.RequestContext,
	in CreateBatchInput,
) (BatchView, error) {
	validated, err := domain.ValidateNewBatch(domain.NewBatchInput{
		DomainCode: in.DomainCode, CurrencyCode: in.CurrencyCode,
		Period: domain.BatchPeriod{From: in.PeriodFrom, To: in.PeriodTo},
	})
	if err != nil {
		return BatchView{}, err
	}
	if err := checkProviderScope(rc, in.ProviderOrganizationID); err != nil {
		return BatchView{}, err
	}

	var out BatchView
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireBatches(); err != nil {
			return err
		}
		// The provider has to be a provider of this tenant. It is the same gate the invoice
		// applies, asked here as well because a batch is raised against an organization and an
		// icmal for somebody who is not a provider is an icmal nobody can settle.
		identity, err := s.repo.ProviderTaxIdentity(ctx, tx, rc.TenantID, in.ProviderOrganizationID)
		if err != nil {
			return err
		}
		if !identity.Found || !identity.IsProvider {
			return ErrProviderUnknown
		}

		record, err := s.createBatchWithReference(ctx, tx, rc, in, validated)
		if err != nil {
			return err
		}
		if err := s.recordBatch(ctx, tx, rc, "batch.create", record.ID, map[string]any{
			"reference":     record.Reference,
			"domain_code":   record.DomainCode,
			"currency_code": record.CurrencyCode,
			"period_from":   record.PeriodFrom.Format(time.DateOnly),
			"period_to":     record.PeriodTo.Format(time.DateOnly),
		}); err != nil {
			return err
		}
		out = BatchView{Batch: record, Invoices: []BatchInvoiceRecord{}}
		return nil
	})
	if err != nil {
		return BatchView{}, err
	}
	return out, nil
}

// createBatchWithReference writes the header, retrying the reference on the collision the
// unique index refuses. The tail is random, so a second attempt is already unlikely; the loop
// exists so a collision is a retry rather than an error somebody has to read.
func (s *Service) createBatchWithReference(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, in CreateBatchInput, validated domain.NewBatchInput,
) (BatchRecord, error) {
	var lastErr error
	for attempt := 0; attempt < batchReferenceAttempts; attempt++ {
		reference, err := domain.NewBatchReference(s.now())
		if err != nil {
			return BatchRecord{}, err
		}
		record, err := s.batches.CreateBatch(ctx, tx, rc.TenantID, NewBatchRow{
			Reference: reference, ProviderOrganizationID: in.ProviderOrganizationID,
			PayerOrganizationID: in.PayerOrganizationID, DomainCode: validated.DomainCode,
			CurrencyCode: validated.CurrencyCode, PeriodFrom: validated.Period.From,
			PeriodTo: validated.Period.To, ActorID: actorPtr(rc.Principal.ActorID),
		})
		if err == nil {
			return record, nil
		}
		if !errors.Is(err, ErrBatchReferenceTaken) {
			return BatchRecord{}, err
		}
		lastErr = err
	}
	return BatchRecord{}, lastErr
}

// PutBatchInvoices replaces the whole set of invoices a DRAFT batch collects.
//
// The pick list is the provider's own SUBMITTED invoices of this payer, domain and currency.
// Four rules refuse a row, and each of them names the invoice rather than the position in the
// body, because a provider knows their documents by number and "row 3" is a thing only the
// request body knows about:
//
//   - the invoice is somebody else's, or is not SUBMITTED — `BATCH_MIXED`, naming the field;
//   - it is in another payer's, domain's or currency's world — `BATCH_MIXED` again, which is
//     one code because they are one mistake: this invoice does not belong in this icmal;
//   - it is already in a live batch — `INVOICE_ALREADY_BATCHED`, naming that batch;
//   - it is not this tenant's at all — the same `BATCH_MIXED`, because saying so any more
//     precisely would answer a question about somebody else's data.
func (s *Service) PutBatchInvoices(ctx context.Context, rc identity.RequestContext,
	batchID uuid.UUID, invoiceIDs []uuid.UUID, expected int64,
) (BatchView, error) {
	var out BatchView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireBatches(); err != nil {
			return err
		}
		record, err := s.batches.LockBatch(ctx, tx, rc.TenantID, batchID, scopeOf(rc))
		if err != nil {
			return err
		}
		if err := checkProviderScope(rc, record.ProviderOrganizationID); err != nil {
			return err
		}
		if !domain.CanPutBatchInvoices(record.Status) {
			return ErrBatchFrozen
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}

		unique := make(map[uuid.UUID]bool, len(invoiceIDs))
		ordered := make([]uuid.UUID, 0, len(invoiceIDs))
		for _, id := range invoiceIDs {
			if unique[id] {
				continue
			}
			unique[id] = true
			ordered = append(ordered, id)
		}

		candidates, err := s.batches.BatchCandidates(ctx, tx, rc.TenantID, ordered)
		if err != nil {
			return err
		}
		byInvoice := make(map[uuid.UUID]BatchCandidate, len(candidates))
		for _, c := range candidates {
			byInvoice[c.InvoiceID] = c
		}

		rows := make([]NewBatchInvoiceRow, 0, len(ordered))
		total := zero()
		for _, id := range ordered {
			candidate, found := byInvoice[id]
			if !found {
				return &MixedBatchError{Field: "invoiceId", InvoiceID: id}
			}
			if err := checkBatchFit(record, candidate); err != nil {
				return err
			}
			if candidate.LiveBatchID != uuid.Nil && candidate.LiveBatchID != record.ID {
				return &InvoiceBatchedError{
					InvoiceID: id, InvoiceNumber: candidate.InvoiceNumber,
					LiveBatchID: candidate.LiveBatchID,
				}
			}
			amount := quantityOrZero(candidate.PayableAmount)
			total = total.Add(amount)
			rows = append(rows, NewBatchInvoiceRow{
				BatchID: record.ID, InvoiceID: id, SubmittedAmount: amount.String(),
				ActorID: actorPtr(rc.Principal.ActorID),
			})
		}

		// Replace the whole set: an endpoint that added one invoice at a time would make "the
		// icmal now covers exactly these" a sequence of requests, and there is no state
		// between two of them a reader should be judged on.
		if err := s.batches.DeleteBatchInvoices(ctx, tx, rc.TenantID, record.ID); err != nil {
			return err
		}
		for _, row := range rows {
			if err := s.batches.CreateBatchInvoice(ctx, tx, rc.TenantID, row); err != nil {
				return err
			}
		}
		// The row version has to move even though no column of the header changed: the ETag a
		// caller holds is a statement about the icmal *and what it covers*, and a membership
		// replaced under a stale If-Match is exactly the race this guards. It is the same move
		// `putInvoiceAllocations` makes on the invoice, for the same reason.
		touched, err := s.batches.TouchDraftBatch(ctx, tx, rc.TenantID, record.ID,
			actorPtr(rc.Principal.ActorID), expected)
		if err != nil {
			return err
		}
		if !touched {
			return ErrVersionMismatch
		}
		if err := s.recordBatch(ctx, tx, rc, "batch.invoices.put", record.ID, map[string]any{
			"reference":       record.Reference,
			"invoice_count":   len(rows),
			"submitted_total": total.String(),
			"currency_code":   record.CurrencyCode,
		}); err != nil {
			return err
		}
		out, err = s.loadBatch(ctx, tx, rc, record.ID)
		return err
	})
	if err != nil {
		return BatchView{}, err
	}
	return out, nil
}

// checkBatchFit is the mixture rule, in one place, so that adding a sixth thing a batch is
// "one of" cannot be forgotten in a second copy.
func checkBatchFit(batch BatchRecord, candidate BatchCandidate) error {
	mixed := func(field, expected, actual string) error {
		return &MixedBatchError{
			Field: field, InvoiceID: candidate.InvoiceID,
			InvoiceNumber: candidate.InvoiceNumber,
			ExpectedValue: expected, ActualValue: actual,
		}
	}
	if candidate.ProviderOrganizationID != batch.ProviderOrganizationID {
		return mixed("providerOrganizationId", batch.ProviderOrganizationID.String(),
			candidate.ProviderOrganizationID.String())
	}
	if !sameOrganization(batch.PayerOrganizationID, candidate.PayerOrganizationID) {
		return mixed("payerOrganizationId", organizationText(batch.PayerOrganizationID),
			organizationText(candidate.PayerOrganizationID))
	}
	if candidate.CurrencyCode != batch.CurrencyCode {
		return mixed("currencyCode", batch.CurrencyCode, candidate.CurrencyCode)
	}
	if candidate.DomainCode != batch.DomainCode {
		return mixed("domainCode", batch.DomainCode, candidate.DomainCode)
	}
	if candidate.Status != domain.StatusSubmitted {
		return mixed("status", domain.StatusSubmitted, candidate.Status)
	}
	return nil
}

func sameOrganization(a, b *uuid.UUID) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// organizationText renders an optional organization for a refusal. The empty string is the
// tenant itself, which is what a null payer means.
func organizationText(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// SubmitBatch sends the icmal to the payer.
//
// Everything happens in one transaction: the count gate, the transition, the invoices moving
// to IN_BATCH, the freeze the database then applies, the work item in the payer's finance
// queue, the outbox event and the notification. A batch that is SUBMITTED and invoices that
// never moved is a state no reader observes.
func (s *Service) SubmitBatch(ctx context.Context, rc identity.RequestContext,
	batchID uuid.UUID, expected int64,
) (BatchView, error) {
	var out BatchView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireBatches(); err != nil {
			return err
		}
		record, err := s.batches.LockBatch(ctx, tx, rc.TenantID, batchID, scopeOf(rc))
		if err != nil {
			return err
		}
		if err := checkProviderScope(rc, record.ProviderOrganizationID); err != nil {
			return err
		}
		if !domain.CanSubmitBatch(record.Status) {
			return ErrBatchTransitionInvalid
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}

		members, err := s.batches.ListBatchInvoices(ctx, tx, rc.TenantID, record.ID)
		if err != nil {
			return err
		}
		values, err := s.settingsFor(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}
		if len(members) < values.BatchMinInvoices || len(members) > values.BatchMaxInvoices {
			return &BatchSizeError{
				Count: len(members), Min: values.BatchMinInvoices, Max: values.BatchMaxInvoices,
			}
		}

		// The step-up, above the same threshold the second pair of eyes is asked for at. A
		// provider sending an icmal worth more than the tenant's threshold re-enters their
		// password; the comparison is on exact decimals and never on a float.
		total := zero()
		for _, member := range members {
			total = total.Add(quantityOrZero(member.SubmittedAmount))
		}
		if err := requireStepUp(rc, total, quantityOrZero(values.BatchDecisionThreshold)); err != nil {
			return err
		}

		now := s.now().UTC()
		submitted, err := s.batches.SubmitBatch(ctx, tx, rc.TenantID, record.ID, SubmitBatchRow{
			SubmittedAt: now, SubmittedBy: rc.Principal.ActorID,
			InvoiceCount: len(members), SubmittedTotal: total.String(),
			ActorID: actorPtr(rc.Principal.ActorID),
		}, expected)
		if err != nil {
			return err
		}
		if !submitted {
			return ErrVersionMismatch
		}

		// The invoices join the icmal. Every one has to move: an invoice that stayed SUBMITTED
		// while the batch collecting it went out would be an invoice a second batch could pick
		// up, which is exactly how a provider comes to be paid twice.
		for _, member := range members {
			moved, err := s.batches.SetInvoiceBatch(ctx, tx, rc.TenantID, member.InvoiceID,
				record.ID, actorPtr(rc.Principal.ActorID))
			if err != nil {
				return err
			}
			if !moved {
				return &MixedBatchError{
					Field: "status", InvoiceID: member.InvoiceID,
					InvoiceNumber: member.InvoiceNumber,
					ExpectedValue: domain.StatusSubmitted, ActualValue: member.InvoiceStatus,
				}
			}
		}

		// The work item the payer's finance queue watches. A tenant with no BATCH_REVIEW queue
		// raises nothing and the submit carries on: the icmal has been sent either way, and a
		// provider told "your icmal cannot be sent because the payer has not configured a work
		// queue" is a provider told about somebody else's configuration.
		if err := s.workItems.Raise(ctx, tx, rc.TenantID, RaiseWorkItem{
			QueueCode: domain.QueueBatchReview, AggregateType: domain.AggregateBatch,
			AggregateID: record.ID, Title: record.Reference + " icmal incelemesi",
			ActorID: actorPtr(rc.Principal.ActorID),
		}); err != nil {
			return err
		}

		if err := s.publishBatchSubmitted(ctx, tx, rc, record, total, len(members)); err != nil {
			return err
		}
		if err := s.notifyBatchSubmitted(ctx, tx, rc, record, total, now); err != nil {
			return err
		}
		if err := s.recordBatch(ctx, tx, rc, "batch.submit", record.ID, map[string]any{
			"reference":       record.Reference,
			"invoice_count":   len(members),
			"submitted_total": total.String(),
			"currency_code":   record.CurrencyCode,
		}); err != nil {
			return err
		}
		out, err = s.loadBatch(ctx, tx, rc, record.ID)
		return err
	})
	if err != nil {
		return BatchView{}, err
	}
	return out, nil
}

// GetBatch answers one icmal with its invoices and their decisions.
func (s *Service) GetBatch(ctx context.Context, rc identity.RequestContext, batchID uuid.UUID,
) (BatchView, error) {
	var out BatchView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireBatches(); err != nil {
			return err
		}
		var err error
		out, err = s.loadBatch(ctx, tx, rc, batchID)
		return err
	})
	if err != nil {
		return BatchView{}, err
	}
	return out, nil
}

// BatchFilter is the listBatches request, before it has been validated.
type BatchFilter struct {
	Cursor                 string
	Limit                  int
	ProviderOrganizationID *uuid.UUID
	PayerOrganizationID    *uuid.UUID
	Status                 string
	DomainCode             string
	CurrencyCode           string
	DateFrom               *time.Time
	DateTo                 *time.Time
}

// ListBatches answers one page of icmals, newest first.
func (s *Service) ListBatches(ctx context.Context, rc identity.RequestContext, f BatchFilter,
) (BatchPage, error) {
	ve := &domain.ValidationError{}
	query := BatchQuery{
		Scope: scopeOf(rc), ProviderOrganizationID: f.ProviderOrganizationID,
		PayerOrganizationID: f.PayerOrganizationID,
		DateFrom:            f.DateFrom, DateTo: f.DateTo,
	}
	if f.Status != "" {
		if !domain.ValidBatchStatus(f.Status) {
			ve.Add("status", "ENUM", "tanımlı bir icmal durumu olmalı")
		} else {
			status := f.Status
			query.Status = &status
		}
	}
	if f.DomainCode != "" {
		if !domain.ValidDomainCode(f.DomainCode) {
			ve.Add("domainCode", "ENUM", "tanımlı bir alan kodu olmalı")
		} else {
			code := f.DomainCode
			query.DomainCode = &code
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
		return BatchPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return BatchPage{}, err
	}
	query.After = after
	query.PageSize = pageSize + 1

	var out BatchPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireBatches(); err != nil {
			return err
		}
		rows, err := s.batches.ListBatches(ctx, tx, rc.TenantID, query)
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
			rows = []BatchRecord{}
		}
		out.Items = rows
		return nil
	})
	if err != nil {
		return BatchPage{}, err
	}
	return out, nil
}

// loadBatch assembles the view a caller reads: the header and its members, in one place, so
// every command answers the same shape.
func (s *Service) loadBatch(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	batchID uuid.UUID,
) (BatchView, error) {
	record, err := s.batches.GetBatch(ctx, tx, rc.TenantID, batchID, scopeOf(rc))
	if err != nil {
		return BatchView{}, err
	}
	members, err := s.batches.ListBatchInvoices(ctx, tx, rc.TenantID, batchID)
	if err != nil {
		return BatchView{}, err
	}
	return BatchView{Batch: record, Invoices: members}, nil
}

// requireBatches refuses every batch command in a process wired without the icmal's
// repository. It is a programming error rather than a user one and is answered as one.
func (s *Service) requireBatches() error {
	if s.batches == nil {
		return errors.New("billing: no batch repository is wired")
	}
	return nil
}

// requireStepUp asks for the password again above the tenant's threshold. The comparison is on
// exact decimals and never on floats: a threshold that rounded would be a step-up that fired on
// some batches and not on others for no reason anybody could explain.
func requireStepUp(rc identity.RequestContext, amount, threshold benefitdomain.Quantity) error {
	if rc.StepUpValid {
		return nil
	}
	if amount.Cmp(threshold) > 0 {
		return identity.ErrStepUpRequired
	}
	return nil
}

// recordBatch writes one business audit row about a batch.
//
// Everything that reaches a detail map here is an id, a code, a count, a status, a date or an
// exact decimal. There is no invoice number in one — a provider's fiscal document numbers are
// not something to scatter through an audit log — and no reason text, which is free prose a
// person typed.
//
// Every key is snake_case, because `audit.SanitizeDetail` keeps only keys matching
// `^[a-z][a-z0-9_]*$` and drops everything else silently. A camelCase key here would be an
// audit row that recorded nothing and said so to nobody.
func (s *Service) recordBatch(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action string, batchID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: domain.AggregateBatch,
		ResourceID: nullUUID(batchID), Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// publishBatchSubmitted writes the outbox row a submitted icmal owes, inside the command's own
// transaction.
//
// The payload is identifiers, a reference, a currency, a count and an exact decimal. There is
// no invoice number and no tax identity in it: an outbox payload is read by every consumer,
// including ones written later.
func (s *Service) publishBatchSubmitted(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, record BatchRecord, total benefitdomain.Quantity, count int,
) error {
	payload := map[string]any{
		"batchId":                record.ID,
		"reference":              record.Reference,
		"providerOrganizationId": record.ProviderOrganizationID,
		"currencyCode":           record.CurrencyCode,
		"domainCode":             record.DomainCode,
		"periodFrom":             record.PeriodFrom.Format(time.DateOnly),
		"periodTo":               record.PeriodTo.Format(time.DateOnly),
		"invoiceCount":           count,
		"submittedTotal":         total.String(),
	}
	if record.PayerOrganizationID != nil {
		payload["payerOrganizationId"] = *record.PayerOrganizationID
	}
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID:      nullUUID(rc.TenantID),
		AggregateType: domain.AggregateBatch, AggregateID: record.ID,
		Type: BatchSubmittedEvent, Payload: payload,
		// The batch's own id: one batch is submitted once, and a redelivered command that
		// moved nothing publishes no second event either.
		DeduplicationKey: record.ID.String(),
	})
	return err
}
