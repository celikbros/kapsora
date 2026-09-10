package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// The header commands: opening a draft, correcting one, reading one, listing them, and
// reading the chain a correction belongs to.
//
// Nothing here computes a figure. The provider's document says what it says, and the only
// arithmetic this file performs is `ValidateHeader`'s check that the two halves are the whole.

// CreateInvoiceInput is the header as it arrived.
type CreateInvoiceInput struct {
	ProviderOrganizationID uuid.UUID
	PayerOrganizationID    *uuid.UUID
	InvoiceNumber          string
	InvoiceDate            time.Time
	CurrencyCode           string
	LineExtensionAmount    string
	TaxAmount              string
	PayableAmount          string
	VatRate                *string
	DomainCode             string
	DocumentID             *uuid.UUID
	Notes                  *string
	// SupersedesInvoiceID opens this draft as the correction of that invoice, copying its
	// allocations.
	SupersedesInvoiceID *uuid.UUID
}

// PatchInvoiceInput is a merge patch over a draft's header. A nil field is "not sent"; the
// service reads the missing values off the row so the result is validated as a whole.
type PatchInvoiceInput struct {
	PayerOrganizationID    *uuid.UUID
	ClearPayerOrganization bool
	InvoiceNumber          *string
	InvoiceDate            *time.Time
	CurrencyCode           *string
	LineExtensionAmount    *string
	TaxAmount              *string
	PayableAmount          *string
	VatRate                *string
	ClearVatRate           bool
	DomainCode             *string
	DocumentID             *uuid.UUID
	ClearDocument          bool
	Notes                  *string
	ClearNotes             bool
}

// InvoiceFilter is the list request as it arrived.
type InvoiceFilter struct {
	ProviderOrganizationID *uuid.UUID
	Status                 string
	FiscalYear             *int
	DateFrom               *time.Time
	DateTo                 *time.Time
	BatchID                *uuid.UUID
	Cursor                 string
	Limit                  int
}

// CreateInvoice records an invoice the provider raised somewhere else.
//
// The order of the checks is the order a caller can act on them: may I act for this provider,
// is this provider a provider at all, does it carry a tax identity, is the header arithmetic
// right, and only then is the number free. The number check is last because it is the one that
// depends on what other people have done.
func (s *Service) CreateInvoice(ctx context.Context, rc identity.RequestContext,
	in CreateInvoiceInput,
) (InvoiceView, error) {
	if err := checkProviderScope(rc, in.ProviderOrganizationID); err != nil {
		return InvoiceView{}, err
	}
	header, err := domain.ValidateHeader(domain.Header{
		InvoiceNumber: in.InvoiceNumber, InvoiceDate: domain.DateOnly(in.InvoiceDate),
		CurrencyCode: in.CurrencyCode, LineExtensionAmount: in.LineExtensionAmount,
		TaxAmount: in.TaxAmount, PayableAmount: in.PayableAmount, VatRate: in.VatRate,
		DomainCode: in.DomainCode, Notes: in.Notes,
	})
	if err != nil {
		return InvoiceView{}, err
	}

	var out InvoiceView
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		identityRow, err := s.repo.ProviderTaxIdentity(ctx, tx, rc.TenantID, in.ProviderOrganizationID)
		if err != nil {
			return err
		}
		if !identityRow.Found || !identityRow.IsProvider {
			return ErrProviderUnknown
		}
		if len(identityRow.TaxNumberHash) == 0 {
			return ErrProviderTaxIDMissing
		}
		if in.PayerOrganizationID != nil {
			payer, err := s.repo.ProviderTaxIdentity(ctx, tx, rc.TenantID, *in.PayerOrganizationID)
			if err != nil {
				return err
			}
			if !payer.Found {
				return fieldError("payerOrganizationId", "REFERENCE",
					"bu tenant'ın kurumu olmalı")
			}
		}
		if in.DocumentID != nil {
			if err := s.checkDocument(ctx, tx, rc.TenantID, *in.DocumentID); err != nil {
				return err
			}
		}

		// The correction. Its allocations are copied after the header is written, so the new
		// draft opens with exactly what the returned invoice covered and the provider corrects
		// the figures rather than rebuilding the pick list.
		var superseded *InvoiceRecord
		if in.SupersedesInvoiceID != nil {
			old, err := s.repo.LockInvoice(ctx, tx, rc.TenantID, *in.SupersedesInvoiceID, scopeOf(rc))
			if err != nil {
				return err
			}
			if old.ProviderOrganizationID != in.ProviderOrganizationID {
				return fieldError("supersedes_invoice_id", "REFERENCE",
					"düzeltilen fatura aynı sağlayıcıya ait olmalı")
			}
			if !domain.CanSupersede(old.Status) {
				return ErrNotSupersedable
			}
			superseded = &old
		}

		// **Who may reuse a number.** A cancelled invoice's number is free to anybody; a
		// returned one's is free only to the invoice that supersedes it, which is section 2.2's
		// "the same number is allowed only for the superseded chain". The unique index refuses
		// two *live* documents sharing a number whatever wrote them; this is the half about
		// another row, and it is what makes the refusal a named problem rather than a
		// constraint name.
		holder, err := s.repo.FindByNumber(ctx, tx, rc.TenantID, NumberQuery{
			ProviderOrganizationID: in.ProviderOrganizationID,
			FiscalYear:             domain.FiscalYear(header.InvoiceDate),
			InvoiceNumber:          header.InvoiceNumber,
		})
		if err != nil {
			return err
		}
		if holder.Found && (superseded == nil || holder.ID != superseded.ID) {
			return ErrNumberTaken
		}

		record, err := s.repo.CreateInvoice(ctx, tx, rc.TenantID, NewInvoiceRow{
			ProviderOrganizationID: in.ProviderOrganizationID,
			PayerOrganizationID:    in.PayerOrganizationID,
			Source:                 domain.SourceManual,
			InvoiceNumber:          header.InvoiceNumber,
			InvoiceDate:            header.InvoiceDate,
			FiscalYear:             domain.FiscalYear(header.InvoiceDate),
			ProviderTaxIDHash:      identityRow.TaxNumberHash,
			CurrencyCode:           header.CurrencyCode,
			LineExtensionAmount:    header.LineExtensionAmount,
			TaxAmount:              header.TaxAmount,
			PayableAmount:          header.PayableAmount,
			VatRate:                header.VatRate,
			DomainCode:             header.DomainCode,
			SupersedesInvoiceID:    in.SupersedesInvoiceID,
			DocumentID:             in.DocumentID,
			Notes:                  header.Notes,
			ActorID:                actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}
		record.ProviderName = identityRow.DisplayName

		if in.DocumentID != nil {
			if err := s.repo.LinkDocument(ctx, tx, rc.TenantID, record.ID, *in.DocumentID,
				actorPtr(rc.Principal.ActorID)); err != nil {
				return err
			}
		}

		allocations := []AllocationRecord{}
		if superseded != nil {
			allocations, err = s.copyAllocations(ctx, tx, rc, *superseded, record)
			if err != nil {
				return err
			}
		}

		detail := map[string]any{
			"providerOrganizationId": in.ProviderOrganizationID.String(),
			"fiscal_year":            record.FiscalYear,
			"currency_code":          record.CurrencyCode,
			"payable_amount":         record.PayableAmount,
			"allocation_count":       len(allocations),
		}
		if superseded != nil {
			detail["supersedes_invoice_id"] = superseded.ID.String()
		}
		if err := s.record(ctx, tx, rc, "invoice.create", record.ID, detail); err != nil {
			return err
		}
		out = buildView(record, allocations, projectionFor(rc))
		return nil
	})
	if err != nil {
		return InvoiceView{}, err
	}
	return out, nil
}

// copyAllocations moves the returned invoice's claim links onto the correction.
//
// The old rows stay where they are — nothing here is deleted, and "which claims did this
// returned invoice cover" is a question a dispute asks — but they are inactive, because a
// returned invoice released its claims, so the new draft can hold the same claims without
// colliding with `uq_billing_invoice_claim_live`.
//
// A claim that has meanwhile stopped being invoiceable is quietly left off rather than
// refusing the whole correction: a provider correcting one figure on a ten-claim invoice must
// not be blocked because an eleventh claim was cancelled in the meantime, and the submit gate
// will refuse the draft anyway if the total no longer adds up.
func (s *Service) copyAllocations(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	from, to InvoiceRecord,
) ([]AllocationRecord, error) {
	rows, err := s.repo.ListAllocations(ctx, tx, rc.TenantID, from.ID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ClaimID)
	}
	if len(ids) == 0 {
		return []AllocationRecord{}, nil
	}
	candidates, err := s.repo.InvoiceableClaims(ctx, tx, rc.TenantID, ids)
	if err != nil {
		return nil, err
	}
	byClaim := make(map[uuid.UUID]ClaimCandidate, len(candidates))
	for _, c := range candidates {
		byClaim[c.ClaimID] = c
	}
	for _, row := range rows {
		candidate, ok := byClaim[row.ClaimID]
		if !ok || !domain.ClaimIsInvoiceable(candidate.Status) ||
			candidate.LiveInvoiceID != uuid.Nil || candidate.CurrencyCode != to.CurrencyCode {
			continue
		}
		if err := s.repo.CreateAllocation(ctx, tx, rc.TenantID, NewAllocationRow{
			InvoiceID: to.ID, ClaimID: row.ClaimID,
			ClaimVersionNo: candidate.CurrentVersionNo, AllocatedAmount: row.AllocatedAmount,
			CurrencyCode: to.CurrencyCode, ClaimStatusBefore: candidate.Status,
			ActorID: actorPtr(rc.Principal.ActorID),
		}); err != nil {
			return nil, err
		}
	}
	return s.repo.ListAllocations(ctx, tx, rc.TenantID, to.ID)
}

// checkDocument refuses an image that is not a document object of this tenant the scanner has
// cleared. A file still in quarantine is not evidence a reviewer can open, and a reference to
// one would be an invoice that looked complete and was not.
func (s *Service) checkDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) error {
	clean, err := s.repo.DocumentIsClean(ctx, tx, tenantID, documentID)
	if err != nil {
		return err
	}
	if !clean {
		return ErrDocumentUnusable
	}
	return nil
}

// GetInvoice answers one invoice with its allocations, in the projection the caller earned.
func (s *Service) GetInvoice(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
) (InvoiceView, error) {
	var out InvoiceView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.repo.GetInvoice(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		rows, err := s.repo.ListAllocations(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		out = buildView(record, rows, projectionFor(rc))
		return nil
	})
	if err != nil {
		return InvoiceView{}, err
	}
	return out, nil
}

// ListInvoices answers one page, newest first.
func (s *Service) ListInvoices(ctx context.Context, rc identity.RequestContext, f InvoiceFilter,
) (InvoicePage, error) {
	var status *string
	if f.Status != "" {
		if !domain.ValidStatus(f.Status) {
			return InvoicePage{}, fieldError("status", "ENUM", "tanımlı bir fatura durumu olmalı")
		}
		value := f.Status
		status = &value
	}
	if f.DateFrom != nil && f.DateTo != nil && f.DateTo.Before(*f.DateFrom) {
		return InvoicePage{}, fieldError("to", "RANGE", "bitiş tarihi başlangıçtan önce olamaz")
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return InvoicePage{}, err
	}

	var out InvoicePage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.repo.ListInvoices(ctx, tx, rc.TenantID, InvoiceQuery{
			Scope: scopeOf(rc), ProviderOrganizationID: f.ProviderOrganizationID,
			Status: status, FiscalYear: f.FiscalYear, DateFrom: f.DateFrom, DateTo: f.DateTo,
			BatchID: f.BatchID, After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			out.NextCursor = s.cursors.Encode(invoiceCursor(rows[pageSize-1]))
			rows = rows[:pageSize]
		}
		out.Items = rows
		return nil
	})
	if err != nil {
		return InvoicePage{}, err
	}
	if out.Items == nil {
		out.Items = []InvoiceSummaryRecord{}
	}
	return out, nil
}

// ListChain answers the supersede chain an invoice belongs to, oldest first.
//
// The invoice itself is read first, through the caller's scope, so a provider asking about
// somebody else's chain gets 404 rather than a chain with one row missing from it.
func (s *Service) ListChain(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
) ([]InvoiceSummaryRecord, error) {
	var out []InvoiceSummaryRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetInvoice(ctx, tx, rc.TenantID, id, scopeOf(rc)); err != nil {
			return err
		}
		rows, err := s.repo.ListChain(ctx, tx, rc.TenantID, id)
		out = rows
		return err
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []InvoiceSummaryRecord{}
	}
	return out, nil
}

// PatchInvoiceDraft edits the header of a DRAFT.
//
// The patch is applied to the row and the *result* is validated as a whole, which is what
// makes "the three amounts travel together" true: a caller sending a new tax amount alone
// produces a header that no longer adds up, and is told which field rather than being handed a
// constraint name.
func (s *Service) PatchInvoiceDraft(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, in PatchInvoiceInput, expected int64,
) (InvoiceView, error) {
	var out InvoiceView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.repo.LockInvoice(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if err := checkProviderScope(rc, record.ProviderOrganizationID); err != nil {
			return err
		}
		if !domain.CanEdit(record.Status) {
			return ErrInvoiceFrozen
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}

		merged := applyPatch(record, in)
		header, err := domain.ValidateHeader(domain.Header{
			InvoiceNumber: merged.InvoiceNumber, InvoiceDate: merged.InvoiceDate,
			CurrencyCode: merged.CurrencyCode, LineExtensionAmount: merged.LineExtensionAmount,
			TaxAmount: merged.TaxAmount, PayableAmount: merged.PayableAmount,
			VatRate: merged.VatRate, DomainCode: merged.DomainCode, Notes: merged.Notes,
		})
		if err != nil {
			return err
		}
		if merged.PayerOrganizationID != nil {
			payer, err := s.repo.ProviderTaxIdentity(ctx, tx, rc.TenantID, *merged.PayerOrganizationID)
			if err != nil {
				return err
			}
			if !payer.Found {
				return fieldError("payerOrganizationId", "REFERENCE", "bu tenant'ın kurumu olmalı")
			}
		}
		if merged.DocumentID != nil {
			if err := s.checkDocument(ctx, tx, rc.TenantID, *merged.DocumentID); err != nil {
				return err
			}
		}

		// A draft may never take a number another invoice is still holding -- not even a
		// returned one, because this draft is not that invoice's correction.
		holder, err := s.repo.FindByNumber(ctx, tx, rc.TenantID, NumberQuery{
			ProviderOrganizationID: record.ProviderOrganizationID,
			FiscalYear:             domain.FiscalYear(header.InvoiceDate),
			InvoiceNumber:          header.InvoiceNumber,
			ExcludingID:            id,
		})
		if err != nil {
			return err
		}
		if holder.Found && (record.SupersedesInvoiceID == nil || holder.ID != *record.SupersedesInvoiceID) {
			return ErrNumberTaken
		}

		ok, err := s.repo.UpdateInvoiceDraft(ctx, tx, rc.TenantID, id, UpdateInvoiceRow{
			PayerOrganizationID: merged.PayerOrganizationID,
			InvoiceNumber:       header.InvoiceNumber,
			InvoiceDate:         header.InvoiceDate,
			FiscalYear:          domain.FiscalYear(header.InvoiceDate),
			CurrencyCode:        header.CurrencyCode,
			LineExtensionAmount: header.LineExtensionAmount,
			TaxAmount:           header.TaxAmount,
			PayableAmount:       header.PayableAmount,
			VatRate:             header.VatRate,
			DomainCode:          header.DomainCode,
			DocumentID:          merged.DocumentID,
			Notes:               header.Notes,
			ActorID:             actorPtr(rc.Principal.ActorID),
		}, expected)
		if err != nil {
			return err
		}
		if !ok {
			return ErrVersionMismatch
		}
		if merged.DocumentID != nil {
			if err := s.repo.LinkDocument(ctx, tx, rc.TenantID, id, *merged.DocumentID,
				actorPtr(rc.Principal.ActorID)); err != nil {
				return err
			}
		}
		if err := s.record(ctx, tx, rc, "invoice.update", id, map[string]any{
			"fiscal_year":    domain.FiscalYear(header.InvoiceDate),
			"payable_amount": header.PayableAmount,
			"currency_code":  header.CurrencyCode,
		}); err != nil {
			return err
		}

		updated, err := s.repo.GetInvoice(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		rows, err := s.repo.ListAllocations(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		out = buildView(updated, rows, projectionFor(rc))
		return nil
	})
	if err != nil {
		return InvoiceView{}, err
	}
	return out, nil
}

// applyPatch lays the sent fields over the stored row. It is a plain merge and makes no
// decision: every rule about the result is `ValidateHeader`'s, applied once, afterwards.
func applyPatch(record InvoiceRecord, in PatchInvoiceInput) InvoiceRecord {
	out := record
	switch {
	case in.ClearPayerOrganization:
		out.PayerOrganizationID = nil
	case in.PayerOrganizationID != nil:
		out.PayerOrganizationID = in.PayerOrganizationID
	}
	if in.InvoiceNumber != nil {
		out.InvoiceNumber = *in.InvoiceNumber
	}
	if in.InvoiceDate != nil {
		out.InvoiceDate = domain.DateOnly(*in.InvoiceDate)
	}
	if in.CurrencyCode != nil {
		out.CurrencyCode = *in.CurrencyCode
	}
	if in.LineExtensionAmount != nil {
		out.LineExtensionAmount = *in.LineExtensionAmount
	}
	if in.TaxAmount != nil {
		out.TaxAmount = *in.TaxAmount
	}
	if in.PayableAmount != nil {
		out.PayableAmount = *in.PayableAmount
	}
	switch {
	case in.ClearVatRate:
		out.VatRate = nil
	case in.VatRate != nil:
		out.VatRate = in.VatRate
	}
	if in.DomainCode != nil {
		out.DomainCode = *in.DomainCode
	}
	switch {
	case in.ClearDocument:
		out.DocumentID = nil
	case in.DocumentID != nil:
		out.DocumentID = in.DocumentID
	}
	switch {
	case in.ClearNotes:
		out.Notes = nil
	case in.Notes != nil:
		out.Notes = in.Notes
	}
	return out
}
