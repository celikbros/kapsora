package billinghttp

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// /api/v1/batches: the icmal a provider bundles its submitted invoices into, and the payer's
// decision on each of them (WP-I7-03).
//
// The permissions are the catalogue's three, and reading is `invoice.read`: both sides of an
// icmal already hold it, and inventing a `batch.read` nobody's role template grants would be
// inventing an endpoint nobody can call.

// Permissions guarding the batch routes.
const (
	PermissionBatchCreate = application.PermissionBatchCreate
	PermissionBatchSubmit = application.PermissionBatchSubmit
	PermissionBatchReview = application.PermissionBatchReview
)

// BatchMiddlewares are the Idempotency-Key wrappers the integrator applies in cmd/api.
//
// Every one of the five is required rather than optional, and the reason is the same for all of
// them: an icmal is money. A submit replayed by a flaky network must move one batch and raise
// one work item, a decision replayed must reverse one adjustment and write one, and a decide
// replayed must close one batch and publish one event.
type BatchMiddlewares struct {
	CreateBatch        func(http.Handler) http.Handler
	PutBatchInvoices   func(http.Handler) http.Handler
	SubmitBatch        func(http.Handler) http.Handler
	ReviewBatchInvoice func(http.Handler) http.Handler
	DecideBatch        func(http.Handler) http.Handler
}

// BatchRoutes mounts everything below /batches.
func (h *Handler) BatchRoutes(r chi.Router, mw BatchMiddlewares) {
	r.Get("/", h.ListBatches)
	r.With(wrap(mw.CreateBatch)).Post("/", h.CreateBatch)
	r.Get("/{batchId}", h.GetBatch)
	r.Get("/{batchId}/summary", h.GetBatchSummary)
	r.With(wrap(mw.PutBatchInvoices)).Put("/{batchId}/invoices", h.PutBatchInvoices)
	r.With(wrap(mw.SubmitBatch)).Post("/{batchId}/submit", h.SubmitBatch)
	r.With(wrap(mw.ReviewBatchInvoice)).
		Post("/{batchId}/invoices/{invoiceId}/review", h.ReviewBatchInvoice)
	r.With(wrap(mw.DecideBatch)).Post("/{batchId}/decide", h.DecideBatch)
}

// ListBatches serves GET /batches.
func (h *Handler) ListBatches(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.BatchFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		ProviderOrganizationID: queryUUID(r, "providerOrganizationId", &fields),
		PayerOrganizationID:    queryUUID(r, "payerOrganizationId", &fields),
		Status:                 r.URL.Query().Get("status"),
		DomainCode:             r.URL.Query().Get("domainCode"),
		CurrencyCode:           r.URL.Query().Get("currencyCode"),
		DateFrom:               queryDate(r, "from", &fields),
		DateTo:                 queryDate(r, "to", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListBatches(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := kapsorav1.BatchPage{Items: make([]kapsorav1.Batch, 0, len(page.Items))}
	for _, item := range page.Items {
		// A list row carries no members: a page of fifty icmals is not a place to read five
		// hundred invoice decisions, and the detail is one request away.
		body.Items = append(body.Items, batchBody(application.BatchView{Batch: item}))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		body.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, body)
}

// CreateBatch serves POST /batches.
func (h *Handler) CreateBatch(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionBatchCreate)
	if !ok {
		return
	}
	var body kapsorav1.CreateBatch
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.CreateBatchInput{
		ProviderOrganizationID: body.ProviderOrganizationId,
		PayerOrganizationID:    body.PayerOrganizationId,
		PeriodFrom:             body.PeriodFrom.Time,
		PeriodTo:               body.PeriodTo.Time,
	}
	if body.DomainCode != nil {
		in.DomainCode = *body.DomainCode
	}
	if body.CurrencyCode != nil {
		in.CurrencyCode = *body.CurrencyCode
	}
	view, err := h.svc.CreateBatch(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Batch.RowVersion))
	w.Header().Set("Location", "/api/v1/batches/"+view.Batch.ID.String())
	writeJSON(w, http.StatusCreated, batchBody(view))
}

// GetBatch serves GET /batches/{batchId}.
func (h *Handler) GetBatch(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.batchID(w, r)
	if !ok {
		return
	}
	view, err := h.svc.GetBatch(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Batch.RowVersion))
	writeJSON(w, http.StatusOK, batchBody(view))
}

// GetBatchSummary serves GET /batches/{batchId}/summary.
func (h *Handler) GetBatchSummary(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.batchID(w, r)
	if !ok {
		return
	}
	view, err := h.svc.GetBatchSummary(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := kapsorav1.BatchSummary{
		Batch:        batchBody(application.BatchView{Batch: view.Batch}),
		PendingCount: view.PendingCount,
		PendingTotal: view.PendingTotal,
		Decisions:    make([]kapsorav1.BatchSummaryRow, 0, len(view.Decisions)),
	}
	for _, row := range view.Decisions {
		body.Decisions = append(body.Decisions, kapsorav1.BatchSummaryRow{
			Decision: kapsorav1.BatchDecision(row.Decision), Count: row.Count,
			SubmittedTotal: row.SubmittedTotal, ApprovedTotal: row.ApprovedTotal,
		})
	}
	writeJSON(w, http.StatusOK, body)
}

// PutBatchInvoices serves PUT /batches/{batchId}/invoices.
func (h *Handler) PutBatchInvoices(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionBatchCreate)
	if !ok {
		return
	}
	id, ok := h.batchID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.PutBatchInvoices
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.PutBatchInvoices(r.Context(), rc, id, body.InvoiceIds, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Batch.RowVersion))
	writeJSON(w, http.StatusOK, batchBody(view))
}

// SubmitBatch serves POST /batches/{batchId}/submit.
func (h *Handler) SubmitBatch(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionBatchSubmit)
	if !ok {
		return
	}
	id, ok := h.batchID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	view, err := h.svc.SubmitBatch(r.Context(), rc, id, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Batch.RowVersion))
	writeJSON(w, http.StatusOK, batchBody(view))
}

// ReviewBatchInvoice serves POST /batches/{batchId}/invoices/{invoiceId}/review.
func (h *Handler) ReviewBatchInvoice(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionBatchReview)
	if !ok {
		return
	}
	batchID, ok := h.batchID(w, r)
	if !ok {
		return
	}
	invoiceID, ok := h.invoiceID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReviewBatchInvoice
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.ReviewBatchInvoiceInput{Decision: string(body.Decision)}
	if body.ApprovedAmount != nil {
		in.ApprovedAmount = *body.ApprovedAmount
	}
	if body.ReasonCode != nil {
		in.ReasonCode = *body.ReasonCode
	}
	in.ReasonText = body.ReasonText
	view, err := h.svc.ReviewBatchInvoice(r.Context(), rc, batchID, invoiceID, in, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Batch.RowVersion))
	writeJSON(w, http.StatusOK, batchBody(view))
}

// DecideBatch serves POST /batches/{batchId}/decide.
func (h *Handler) DecideBatch(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionBatchReview)
	if !ok {
		return
	}
	id, ok := h.batchID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	view, err := h.svc.DecideBatch(r.Context(), rc, id, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Batch.RowVersion))
	writeJSON(w, http.StatusOK, batchBody(view))
}

// batchID reads the batch id from the path. A malformed id is indistinguishable from an unknown
// one, so both are answered 404: that a batch exists at all is somebody else's business.
func (h *Handler) batchID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "batchId"))
	if err != nil {
		h.writeError(w, r, application.ErrBatchNotFound)
		return uuid.Nil, false
	}
	return id, true
}

// batchBody maps the application record onto the wire shape.
func batchBody(v application.BatchView) kapsorav1.Batch {
	rec := v.Batch
	out := kapsorav1.Batch{
		Id:                     rec.ID,
		Reference:              rec.Reference,
		ProviderOrganizationId: rec.ProviderOrganizationID,
		PayerOrganizationId:    rec.PayerOrganizationID,
		DomainCode:             rec.DomainCode,
		CurrencyCode:           rec.CurrencyCode,
		PeriodFrom:             openapi_types.Date{Time: rec.PeriodFrom},
		PeriodTo:               openapi_types.Date{Time: rec.PeriodTo},
		Status:                 kapsorav1.BatchStatus(rec.Status),
		SubmittedAt:            rec.SubmittedAt,
		SubmittedBy:            rec.SubmittedBy,
		DecidedAt:              rec.DecidedAt,
		DecidedBy:              rec.DecidedBy,
		InvoiceCount:           rec.InvoiceCount,
		SubmittedTotal:         rec.SubmittedTotal,
		ApprovedTotal:          rec.ApprovedTotal,
		CutTotal:               rec.CutTotal,
		ReturnedTotal:          rec.ReturnedTotal,
		RejectedTotal:          rec.RejectedTotal,
		Invoices:               make([]kapsorav1.BatchInvoice, 0, len(v.Invoices)),
		CreatedAt:              rec.CreatedAt,
		RowVersion:             rec.RowVersion,
	}
	if rec.ProviderName != "" {
		name := rec.ProviderName
		out.ProviderName = &name
	}
	for _, member := range v.Invoices {
		out.Invoices = append(out.Invoices, batchInvoiceBody(member))
	}
	return out
}

// batchInvoiceBody maps one member. The decision fields are absent on a member nobody has
// answered: an empty string beside "decision" would read as a decision somebody made.
func batchInvoiceBody(member application.BatchInvoiceRecord) kapsorav1.BatchInvoice {
	active := member.Active
	out := kapsorav1.BatchInvoice{
		Id:              member.ID,
		InvoiceId:       member.InvoiceID,
		InvoiceNumber:   member.InvoiceNumber,
		InvoiceDate:     openapi_types.Date{Time: member.InvoiceDate},
		InvoiceStatus:   kapsorav1.InvoiceStatus(member.InvoiceStatus),
		CurrencyCode:    member.CurrencyCode,
		SubmittedAmount: member.SubmittedAmount,
		Active:          &active,
		CreatedAt:       &member.CreatedAt,
		ReasonText:      member.ReasonText,
		DecidedBy:       member.DecidedBy,
		DecidedAt:       member.DecidedAt,
	}
	if member.Decision != "" {
		decision := kapsorav1.BatchDecision(member.Decision)
		out.Decision = &decision
	}
	if member.ApprovedAmount != "" {
		amount := member.ApprovedAmount
		out.ApprovedAmount = &amount
	}
	if member.ReasonCode != "" {
		code := member.ReasonCode
		out.ReasonCode = &code
	}
	out.DecidedByDisplayName = member.DecidedByDisplayName
	return out
}

// writeBatchError maps the icmal's own errors to problem codes. Every title is Turkish and
// every one of them says what the caller can do next; the three that carry extension members do
// so because the fact is what makes them actionable.
//
// It is a separate function from writeError only so that the invoice's switch does not grow to
// thirty arms; writeError calls it first and falls through to its own when nothing matches.
func writeBatchError(w http.ResponseWriter, r *http.Request, err error) bool {
	var size *application.BatchSizeError
	var mixed *application.MixedBatchError
	var batched *application.InvoiceBatchedError
	switch {
	case errors.As(err, &size):
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "batches/size-out-of-range",
			Title:  "İcmaldeki fatura sayısı kurumun sınırları dışında",
			Status: http.StatusConflict, Code: "BATCH_SIZE_OUT_OF_RANGE",
			Detail: "Bir icmal, kurumun belirlediği en az ve en çok fatura sayısı arasında olmalı.",
			Extensions: map[string]any{
				"invoiceCount": size.Count, "minInvoices": size.Min, "maxInvoices": size.Max,
			},
		})
		return true
	case errors.As(err, &mixed):
		extensions := map[string]any{"field": mixed.Field}
		if mixed.InvoiceID != uuid.Nil {
			extensions["invoiceId"] = mixed.InvoiceID.String()
		}
		if mixed.InvoiceNumber != "" {
			extensions["invoiceNumber"] = mixed.InvoiceNumber
		}
		if mixed.ExpectedValue != "" {
			extensions["expectedValue"] = mixed.ExpectedValue
		}
		if mixed.ActualValue != "" {
			extensions["actualValue"] = mixed.ActualValue
		}
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "batches/mixed",
			Title:  "Bu fatura bu icmale girmiyor",
			Status: http.StatusUnprocessableEntity, Code: "BATCH_MIXED",
			Detail: "Bir icmal tek sağlayıcı, tek ödeyici, tek para birimi ve tek alan " +
				"kodundan oluşur; içine yalnızca gönderilmiş faturalar girer.",
			Extensions: extensions,
		})
		return true
	case errors.As(err, &batched):
		extensions := map[string]any{"invoiceId": batched.InvoiceID.String()}
		if batched.InvoiceNumber != "" {
			extensions["invoiceNumber"] = batched.InvoiceNumber
		}
		if batched.LiveBatchID != uuid.Nil {
			extensions["liveBatchId"] = batched.LiveBatchID.String()
		}
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "batches/invoice-already-batched",
			Title:  "Fatura başka bir icmalde",
			Status: http.StatusConflict, Code: "INVOICE_ALREADY_BATCHED",
			Detail:     "Bir fatura aynı anda yalnızca bir açık icmalde yer alır.",
			Extensions: extensions,
		})
		return true
	case errors.Is(err, application.ErrBatchNotFound):
		problem(w, r, http.StatusNotFound, "batches/not-found", "BATCH_NOT_FOUND",
			"İcmal bulunamadı", "")
		return true
	case errors.Is(err, application.ErrBatchInvoiceNotFound):
		problem(w, r, http.StatusNotFound, "batches/invoice-not-found",
			"BATCH_INVOICE_NOT_FOUND", "Bu fatura bu icmalde değil", "")
		return true
	case errors.Is(err, application.ErrBatchFrozen):
		problem(w, r, http.StatusConflict, "batches/frozen", "BATCH_FROZEN",
			"Gönderilmiş icmalin içeriği değiştirilemez",
			"İcmal gönderildikten sonra hangi faturaları kapsadığı ve tutarları donar; "+
				"değişiklik için yeni bir icmal açın.")
		return true
	case errors.Is(err, application.ErrBatchTransitionInvalid):
		problem(w, r, http.StatusConflict, "batches/transition-invalid",
			"BATCH_TRANSITION_INVALID", "İcmal bu durumda bu işleme uygun değil", "")
		return true
	case errors.Is(err, application.ErrBatchNotFullyDecided):
		problem(w, r, http.StatusConflict, "batches/not-fully-decided",
			"BATCH_NOT_FULLY_DECIDED", "İcmaldeki her fatura karara bağlanmalı",
			"İcmali kapatmadan önce kalan faturaları onaylayın, kesin, iade edin ya da reddedin.")
		return true
	case errors.Is(err, application.ErrBatchSubmitterCannotDecide):
		problem(w, r, http.StatusForbidden, "batches/submitter-cannot-decide",
			"BATCH_SUBMITTER_CANNOT_DECIDE", "İcmali gönderen kişi kararı veremez",
			"İcmali gönderen ile karara bağlayan farklı kişiler olmalı.")
		return true
	case errors.Is(err, application.ErrBatchSecondReviewerRequired):
		problem(w, r, http.StatusForbidden, "batches/second-reviewer-required",
			"BATCH_SECOND_REVIEWER_REQUIRED", "Bu tutarda ikinci bir inceleyici gerekiyor",
			"Eşiğin üzerindeki icmallerde son kararı veren kişi icmali kapatamaz.")
		return true
	case errors.Is(err, application.ErrBatchTotalsMismatch):
		problem(w, r, http.StatusConflict, "batches/totals-mismatch",
			"BATCH_TOTALS_MISMATCH", "İcmal toplamları tutmuyor",
			"Onaylanan, kesilen, iade edilen ve reddedilen tutarların toplamı gönderilen "+
				"tutara eşit olmalı.")
		return true
	case errors.Is(err, identity.ErrStepUpRequired):
		problem(w, r, http.StatusForbidden, "identity/step-up-required", "STEP_UP_REQUIRED",
			"Bu işlem için parolanızı yeniden doğrulayın",
			"İcmalin toplam tutarı kurumun eşiğinin üzerinde; göndermeden önce kimliğinizi doğrulayın.")
		return true
	}
	return false
}
