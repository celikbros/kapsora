package billinghttp

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/billing/domain"
)

// ListInvoices serves GET /invoices.
func (h *Handler) ListInvoices(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.InvoiceFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		ProviderOrganizationID: queryUUID(r, "providerOrganizationId", &fields),
		Status:                 r.URL.Query().Get("status"),
		FiscalYear:             queryInt(r, "fiscalYear", &fields),
		DateFrom:               queryDate(r, "from", &fields),
		DateTo:                 queryDate(r, "to", &fields),
		BatchID:                queryUUID(r, "batchId", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListInvoices(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := kapsorav1.InvoicePage{
		Items: make([]kapsorav1.InvoiceSummary, 0, len(page.Items)),
	}
	for _, item := range page.Items {
		body.Items = append(body.Items, invoiceSummary(item))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		body.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, body)
}

// CreateInvoice serves POST /invoices.
func (h *Handler) CreateInvoice(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	var body kapsorav1.CreateInvoice
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.CreateInvoiceInput{
		ProviderOrganizationID: body.ProviderOrganizationId,
		PayerOrganizationID:    body.PayerOrganizationId,
		InvoiceNumber:          body.InvoiceNumber,
		InvoiceDate:            body.InvoiceDate.Time,
		LineExtensionAmount:    body.LineExtensionAmount,
		TaxAmount:              body.TaxAmount,
		PayableAmount:          body.PayableAmount,
		VatRate:                body.VatRate,
		DocumentID:             body.DocumentId,
		Notes:                  body.Notes,
		SupersedesInvoiceID:    body.SupersedesInvoiceId,
	}
	if body.CurrencyCode != nil {
		in.CurrencyCode = *body.CurrencyCode
	}
	if body.DomainCode != nil {
		in.DomainCode = *body.DomainCode
	}
	view, err := h.svc.CreateInvoice(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Invoice.RowVersion))
	w.Header().Set("Location", "/api/v1/invoices/"+view.Invoice.ID.String())
	writeJSON(w, http.StatusCreated, invoiceView(view))
}

// GetInvoice serves GET /invoices/{invoiceId}.
func (h *Handler) GetInvoice(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.invoiceID(w, r)
	if !ok {
		return
	}
	view, err := h.svc.GetInvoice(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Invoice.RowVersion))
	writeJSON(w, http.StatusOK, invoiceView(view))
}

// PatchInvoiceDraft serves PATCH /invoices/{invoiceId}.
func (h *Handler) PatchInvoiceDraft(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.invoiceID(w, r)
	if !ok {
		return
	}
	if !requireMergePatch(w, r) {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.PatchInvoiceDraft
	raw, ok := decodeMergePatch(w, r, &body)
	if !ok {
		return
	}
	in := application.PatchInvoiceInput{
		PayerOrganizationID:    body.PayerOrganizationId,
		ClearPayerOrganization: isNull(raw, "payerOrganizationId"),
		InvoiceNumber:          body.InvoiceNumber,
		CurrencyCode:           body.CurrencyCode,
		LineExtensionAmount:    body.LineExtensionAmount,
		TaxAmount:              body.TaxAmount,
		PayableAmount:          body.PayableAmount,
		VatRate:                body.VatRate,
		ClearVatRate:           isNull(raw, "vatRate"),
		DomainCode:             body.DomainCode,
		DocumentID:             body.DocumentId,
		ClearDocument:          isNull(raw, "documentId"),
		Notes:                  body.Notes,
		ClearNotes:             isNull(raw, "notes"),
	}
	if body.InvoiceDate != nil {
		date := body.InvoiceDate.Time
		in.InvoiceDate = &date
	}
	view, err := h.svc.PatchInvoiceDraft(r.Context(), rc, id, in, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Invoice.RowVersion))
	writeJSON(w, http.StatusOK, invoiceView(view))
}

// PutInvoiceAllocations serves PUT /invoices/{invoiceId}/allocations.
func (h *Handler) PutInvoiceAllocations(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.invoiceID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.PutInvoiceAllocations
	if !decodeJSON(w, r, &body) {
		return
	}
	in := make([]application.AllocationInput, 0, len(body.Allocations))
	for _, item := range body.Allocations {
		in = append(in, application.AllocationInput{
			ClaimID: item.ClaimId, AllocatedAmount: item.AllocatedAmount,
		})
	}
	view, err := h.svc.PutAllocations(r.Context(), rc, id, in, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Invoice.RowVersion))
	writeJSON(w, http.StatusOK, invoiceView(view))
}

// SubmitInvoice serves POST /invoices/{invoiceId}/submit.
func (h *Handler) SubmitInvoice(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.invoiceID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	view, err := h.svc.SubmitInvoice(r.Context(), rc, id, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Invoice.RowVersion))
	writeJSON(w, http.StatusOK, invoiceView(view))
}

// CancelInvoice serves POST /invoices/{invoiceId}/cancel.
func (h *Handler) CancelInvoice(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.invoiceID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	view, err := h.svc.CancelInvoice(r.Context(), rc, id, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Invoice.RowVersion))
	writeJSON(w, http.StatusOK, invoiceView(view))
}

// ListInvoiceVersions serves GET /invoices/{invoiceId}/versions.
func (h *Handler) ListInvoiceVersions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.invoiceID(w, r)
	if !ok {
		return
	}
	rows, err := h.svc.ListChain(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := kapsorav1.InvoiceChain{Items: make([]kapsorav1.InvoiceSummary, 0, len(rows))}
	for _, row := range rows {
		body.Items = append(body.Items, invoiceSummary(row))
	}
	writeJSON(w, http.StatusOK, body)
}

// invoiceView maps the application record onto the wire shape. It can only render what it was
// given: the projection was applied on the record, one layer up.
func invoiceView(v application.InvoiceView) kapsorav1.Invoice {
	rec := v.Invoice
	out := kapsorav1.Invoice{
		Id:                     rec.ID,
		ProviderOrganizationId: rec.ProviderOrganizationID,
		PayerOrganizationId:    rec.PayerOrganizationID,
		Source:                 kapsorav1.InvoiceSource(rec.Source),
		EdocumentId:            rec.EDocumentID,
		InvoiceNumber:          rec.InvoiceNumber,
		InvoiceDate:            openapi_types.Date{Time: rec.InvoiceDate},
		FiscalYear:             rec.FiscalYear,
		CurrencyCode:           rec.CurrencyCode,
		LineExtensionAmount:    rec.LineExtensionAmount,
		TaxAmount:              rec.TaxAmount,
		PayableAmount:          rec.PayableAmount,
		VatRate:                rec.VatRate,
		DomainCode:             rec.DomainCode,
		Status:                 kapsorav1.InvoiceStatus(rec.Status),
		SupersedesInvoiceId:    rec.SupersedesInvoiceID,
		SupersededByInvoiceId:  rec.SupersededByInvoiceID,
		SubmittedAt:            rec.SubmittedAt,
		DocumentId:             rec.DocumentID,
		BatchId:                rec.BatchID,
		Notes:                  rec.Notes,
		Allocations:            make([]kapsorav1.InvoiceAllocation, 0, len(v.Allocations)),
		AllocationTotal:        v.AllocationTotal,
		AllocationDifference:   v.AllocationDifference,
		Projection:             kapsorav1.HealthProjection(v.Projection),
		CreatedAt:              rec.CreatedAt,
		RowVersion:             rec.RowVersion,
	}
	if rec.ProviderName != "" {
		name := rec.ProviderName
		out.ProviderName = &name
	}
	for _, row := range v.Allocations {
		out.Allocations = append(out.Allocations, kapsorav1.InvoiceAllocation{
			ClaimId: row.ClaimID, ClaimReference: row.ClaimReference,
			ClaimVersionNo: row.ClaimVersionNo, AllocatedAmount: row.AllocatedAmount,
			CurrencyCode: row.CurrencyCode, ApprovedTotal: row.ApprovedTotal,
			ClaimStatus: kapsorav1.ClaimStatus(row.ClaimStatus), Active: row.Active,
			ClaimDescription: row.ClaimDescription,
		})
	}
	return out
}

// invoiceSummary maps one list row.
func invoiceSummary(rec application.InvoiceSummaryRecord) kapsorav1.InvoiceSummary {
	source := kapsorav1.InvoiceSource(rec.Source)
	out := kapsorav1.InvoiceSummary{
		Id:                     rec.ID,
		ProviderOrganizationId: rec.ProviderOrganizationID,
		PayerOrganizationId:    rec.PayerOrganizationID,
		Source:                 &source,
		InvoiceNumber:          rec.InvoiceNumber,
		InvoiceDate:            openapi_types.Date{Time: rec.InvoiceDate},
		FiscalYear:             rec.FiscalYear,
		CurrencyCode:           rec.CurrencyCode,
		LineExtensionAmount:    &rec.LineExtensionAmount,
		TaxAmount:              &rec.TaxAmount,
		PayableAmount:          rec.PayableAmount,
		Status:                 kapsorav1.InvoiceStatus(rec.Status),
		SupersedesInvoiceId:    rec.SupersedesInvoiceID,
		SupersededByInvoiceId:  rec.SupersededByInvoiceID,
		SubmittedAt:            rec.SubmittedAt,
		DocumentId:             rec.DocumentID,
		BatchId:                rec.BatchID,
		AllocationTotal:        rec.AllocationTotal,
		AllocationCount:        rec.AllocationCount,
		CreatedAt:              rec.CreatedAt,
		RowVersion:             rec.RowVersion,
	}
	if rec.ProviderName != "" {
		name := rec.ProviderName
		out.ProviderName = &name
	}
	return out
}
