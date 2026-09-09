// Package billinghttp serves /api/v1/invoices. Bodies use the generated contract types;
// errors are problem+json with stable codes (ADR-015).
//
// Two things shape every route here.
//
// **No handler in this package decides what a caller may see.** The permission the route names
// is the permission to reach the endpoint at all; the projection is applied in the application
// service, on the record, and the mappers below can only render what they were given. A claim
// line description the service cleared is absent from the body because it is absent from the
// record, not because a mapper remembered to leave it out.
//
// **No refusal in this package carries a tax identity.** Not the VKN, which this module never
// holds, and not its blind index, which is a stable identifier of a taxpayer across every
// tenant. `PROVIDER_TAX_ID_MISSING` says that a number is missing and nothing else.
package billinghttp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the routes. Reading an invoice and managing one are two grants: a
// sponsor's HR user and a payer's finance user hold the first, and only the provider's billing
// clerk and the payer's finance user hold the second.
const (
	PermissionRead   = application.PermissionRead
	PermissionManage = application.PermissionManage
)

const (
	maxBodyBytes         = 1 << 20
	mergePatchContentTyp = "application/merge-patch+json"
)

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the Idempotency-Key
// middleware on every command that changes state. A nil field means "no wrapper".
//
// Every one of the five is required rather than optional, and the reason is the same for all
// of them: a submit replayed by a flaky network must move one invoice and publish one event,
// and a cancel replayed must release one set of claims. An invoice is money.
type Middlewares struct {
	CreateInvoice  func(http.Handler) http.Handler
	PatchInvoice   func(http.Handler) http.Handler
	PutAllocations func(http.Handler) http.Handler
	SubmitInvoice  func(http.Handler) http.Handler
	CancelInvoice  func(http.Handler) http.Handler
}

// Handler serves the invoice operations.
type Handler struct {
	svc    *application.Service
	deny   Denier
	logger *slog.Logger
}

// NewHandler wires the handler.
func NewHandler(svc *application.Service, deny Denier, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, deny: deny, logger: logger}
}

// Routes mounts everything below /invoices.
func (h *Handler) Routes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListInvoices)
	r.With(wrap(mw.CreateInvoice)).Post("/", h.CreateInvoice)
	r.Get("/{invoiceId}", h.GetInvoice)
	r.With(wrap(mw.PatchInvoice)).Patch("/{invoiceId}", h.PatchInvoiceDraft)
	r.With(wrap(mw.PutAllocations)).Put("/{invoiceId}/allocations", h.PutInvoiceAllocations)
	r.With(wrap(mw.SubmitInvoice)).Post("/{invoiceId}/submit", h.SubmitInvoice)
	r.With(wrap(mw.CancelInvoice)).Post("/{invoiceId}/cancel", h.CancelInvoice)
	r.Get("/{invoiceId}/versions", h.ListInvoiceVersions)
}

func wrap(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	if mw == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return mw
}

// require resolves the request context or writes the denial through the auditing denier.
func (h *Handler) require(w http.ResponseWriter, r *http.Request, permission string) (identity.RequestContext, bool) {
	rc, err := identity.Require(r.Context(), permission)
	if err != nil {
		h.deny.Deny(w, r, err, permission)
		return identity.RequestContext{}, false
	}
	return rc, true
}

// writeError maps application and domain errors to the problem codes of the work package.
//
// Every title is Turkish and every one of them says what the caller can do next. Two of them
// carry extension members rather than prose, because the fact is what makes them actionable:
// a mismatch carries both figures and the difference, and a refused allocation names the claim.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	var allocationErr *application.AllocationError
	var mismatch *application.MismatchError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.As(err, &allocationErr):
		writeAllocationError(w, r, allocationErr)
	case errors.As(err, &mismatch):
		writeMismatch(w, r, mismatch)
	case errors.Is(err, application.ErrInvoiceNotFound):
		problem(w, r, http.StatusNotFound, "invoices/not-found", "INVOICE_NOT_FOUND",
			"Fatura bulunamadı", "")
	case errors.Is(err, application.ErrInvoiceFrozen):
		// 409 rather than 403: the caller is allowed to manage invoices, and this one has
		// simply stopped being writable.
		problem(w, r, http.StatusConflict, "invoices/frozen", "INVOICE_FROZEN",
			"Gönderilmiş fatura değiştirilemez",
			"Düzeltme için faturayı iptal edin ya da iade edilmesini isteyip yerine yeni fatura açın.")
	case errors.Is(err, application.ErrTransitionInvalid):
		problem(w, r, http.StatusConflict, "invoices/transition-invalid",
			"INVOICE_TRANSITION_INVALID", "Fatura bu durumda bu işleme uygun değil", "")
	case errors.Is(err, application.ErrNumberTaken):
		problem(w, r, http.StatusConflict, "invoices/number-taken", "INVOICE_NUMBER_TAKEN",
			"Bu fatura numarası bu mali yılda zaten kullanılmış",
			"Bir fatura numarası sağlayıcının mali yılında tektir; iptal edilen fatura numarasını serbest bırakır.")
	case errors.Is(err, application.ErrNotSupersedable):
		problem(w, r, http.StatusConflict, "invoices/not-supersedable",
			"INVOICE_NOT_SUPERSEDABLE", "Bu fatura düzeltilebilir durumda değil",
			"Yalnızca iade edilmiş ya da reddedilmiş bir fatura yeni faturayla düzeltilir.")
	case errors.Is(err, application.ErrImageRequired):
		problem(w, r, http.StatusConflict, "invoices/image-required", "INVOICE_IMAGE_REQUIRED",
			"Fatura görüntüsü gerekli",
			"Faturayı göndermeden önce düzenlediğiniz belgenin taranmış hâlini ekleyin.")
	case errors.Is(err, application.ErrNothingAllocated):
		problem(w, r, http.StatusUnprocessableEntity, "invoices/nothing-allocated",
			"INVOICE_NOTHING_ALLOCATED", "Faturaya hiçbir hasar dosyası bağlanmamış",
			"Gönderimden önce faturanın kapsadığı dosyaları seçin.")
	case errors.Is(err, application.ErrProviderTaxIDMissing):
		problem(w, r, http.StatusUnprocessableEntity, "invoices/provider-tax-id-missing",
			"PROVIDER_TAX_ID_MISSING", "Sağlayıcının vergi kimliği tanımlı değil",
			"Fatura kaydı için kurumun VKN bilgisi kurum dizininde tanımlanmalı.")
	case errors.Is(err, application.ErrProviderUnknown):
		problem(w, r, http.StatusUnprocessableEntity, "invoices/provider-unknown",
			"INVOICE_PROVIDER_UNKNOWN", "Bu kurum tenant'ın sağlayıcısı değil", "")
	case errors.Is(err, application.ErrDocumentUnusable):
		problem(w, r, http.StatusUnprocessableEntity, "invoices/document-unusable",
			"INVOICE_DOCUMENT_UNUSABLE", "Fatura görüntüsü kullanılabilir değil",
			"Belge bu tenant'a ait, taranmış ve temiz çıkmış olmalı.")
	case errors.Is(err, application.ErrProviderScope):
		problem(w, r, http.StatusForbidden, "invoices/provider-scope", "INVOICE_PROVIDER_SCOPE",
			"Bu sağlayıcı adına işlem yapamazsınız", "")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID",
			"Sayfa imleci geçersiz", "")
	default:
		// An invoice error carries no clinical data and no identifier: this module's errors
		// name ids, codes, statuses and exact decimals, and no line description or tax number
		// ever reaches one.
		h.logger.Error("invoice command failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR",
			"Beklenmeyen hata", "")
	}
}

// writeAllocationError renders one refused allocation, naming the claim it refused and the
// figures that refused it. A provider looking at their own earnings screen knows their claims
// by reference, so the reference travels with the id.
func writeAllocationError(w http.ResponseWriter, r *http.Request, e *application.AllocationError) {
	extensions := map[string]any{"claimId": e.ClaimID.String()}
	if e.ClaimReference != "" {
		extensions["claimReference"] = e.ClaimReference
	}
	status := http.StatusUnprocessableEntity
	var typ, title, detail string
	switch e.Kind {
	case application.AllocationExceedsApproved:
		typ, title = "invoices/allocation-exceeds-approved", "Tahsis onaylanan tutarı aşıyor"
		detail = "Bir dosyaya onaylanan tutardan fazlası fatura edilemez; daha azı olağandır."
		extensions["allocatedAmount"] = e.Allocated
		extensions["approvedTotal"] = e.Approved
	case application.AllocationClaimNotInvoiceable:
		typ, title = "invoices/claim-not-invoiceable", "Hasar dosyası faturalanabilir durumda değil"
		detail = "Yalnızca onaylanmış ya da kısmen onaylanmış, bu sağlayıcıya ait dosyalar faturaya bağlanır."
		if e.ClaimStatus != "" {
			extensions["claimStatus"] = e.ClaimStatus
		}
	case application.AllocationClaimAlreadyInvoiced:
		status = http.StatusConflict
		typ, title = "invoices/claim-already-invoiced", "Hasar dosyası başka bir faturada"
		detail = "Bir dosya aynı anda yalnızca bir açık faturada yer alır."
		if e.LiveInvoiceID != uuid.Nil {
			extensions["liveInvoiceId"] = e.LiveInvoiceID.String()
		}
	case application.AllocationCurrency:
		typ, title = "invoices/allocation-currency", "Tahsis faturanın para biriminde olmalı"
		detail = "Farklı para birimlerindeki tutarlar toplanmaz."
		extensions["currencyCode"] = e.CurrencyCode
		extensions["invoiceCurrencyCode"] = e.InvoiceCurrency
	default:
		typ, title = "invoices/claim-not-invoiceable", "Hasar dosyası faturaya bağlanamaz"
	}
	httpx.WriteProblem(w, r, httpx.Problem{
		Type: httpx.ProblemTypeBase + typ, Title: title, Status: status,
		Code: string(e.Kind), Detail: detail, Extensions: extensions,
	})
}

// writeMismatch renders the submit gate's refusal. It carries both figures, the difference and
// the tolerance that was applied: "the totals do not match" is not something a provider can
// act on, and "you are 12.50 short of 1,340.00, tolerance 0.01" is.
func writeMismatch(w http.ResponseWriter, r *http.Request, e *application.MismatchError) {
	httpx.WriteProblem(w, r, httpx.Problem{
		Type:   httpx.ProblemTypeBase + "invoices/allocation-mismatch",
		Title:  "Tahsis toplamı fatura tutarıyla uyuşmuyor",
		Status: http.StatusConflict, Code: "ALLOCATION_MISMATCH",
		Detail: "Faturanın ödenecek tutarı ile bağlı dosyaların toplamı, tenant'ın " +
			"belirlediği tolerans içinde eşit olmalı.",
		Extensions: map[string]any{
			"payableAmount":   e.PayableAmount,
			"allocationTotal": e.AllocationTotal,
			"difference":      e.Difference,
			"tolerance":       e.Tolerance,
		},
	})
}

// invoiceID reads the invoice id from the path. A malformed id is indistinguishable from an
// unknown one, so both are answered 404: that an invoice exists at all is somebody else's
// business.
func (h *Handler) invoiceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "invoiceId"))
	if err != nil {
		h.writeError(w, r, application.ErrInvoiceNotFound)
		return uuid.Nil, false
	}
	return id, true
}

func etag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// requireIfMatch parses the If-Match header, answering 428 when it is missing.
func requireIfMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	v = strings.TrimPrefix(v, "W/")
	v = strings.Trim(v, `"`)
	n, err := strconv.ParseInt(v, 10, 64)
	if v == "" || err != nil || n < 1 {
		problem(w, r, http.StatusPreconditionRequired, "generic/precondition-required",
			"IF_MATCH_REQUIRED", "If-Match başlığı gerekli",
			"GET yanıtındaki ETag değerini If-Match olarak gönderin.")
		return 0, false
	}
	return n, true
}

// requireMergePatch refuses a PATCH body that is not a merge patch. The distinction matters
// here: a merge patch says "null clears this field", and a plain JSON body from a client that
// did not read the contract would clear an invoice's image by omitting it.
func requireMergePatch(w http.ResponseWriter, r *http.Request) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, mergePatchContentTyp) {
		problem(w, r, http.StatusUnsupportedMediaType, "generic/unsupported-media-type",
			"UNSUPPORTED_MEDIA_TYPE", "Desteklenmeyen içerik türü",
			"Gövde application/merge-patch+json olmalı.")
		return false
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			problem(w, r, http.StatusRequestEntityTooLarge, "generic/request-body-too-large",
				"REQUEST_BODY_TOO_LARGE", "İstek gövdesi çok büyük", "")
			return false
		}
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY",
			"İstek gövdesi geçersiz", "")
		return false
	}
	return true
}

// decodeRaw reads a merge patch into a map as well as into the typed body, so "sent as null"
// and "not sent" can be told apart. A merge patch that could not distinguish them would make
// clearing a field impossible.
func decodeMergePatch(w http.ResponseWriter, r *http.Request, dst any) (map[string]json.RawMessage, bool) {
	var raw map[string]json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return nil, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body",
			"INVALID_REQUEST_BODY", "İstek gövdesi geçersiz", "")
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(string(encoded)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body",
			"INVALID_REQUEST_BODY", "İstek gövdesi geçersiz", "")
		return nil, false
	}
	return raw, true
}

// isNull reports whether a merge patch sent this field explicitly as null.
func isNull(raw map[string]json.RawMessage, field string) bool {
	value, ok := raw[field]
	return ok && string(value) == "null"
}

func writeValidation(w http.ResponseWriter, r *http.Request, fields []domain.FieldError) {
	p := httpx.Problem{
		Type: httpx.ProblemTypeBase + "generic/validation-failed", Title: "Doğrulama hatası",
		Status: http.StatusUnprocessableEntity, Code: "VALIDATION_FAILED",
	}
	for _, f := range fields {
		p.Errors = append(p.Errors, httpx.FieldError{Field: f.Field, Code: f.Code, Message: f.Message})
	}
	httpx.WriteProblem(w, r, p)
}

func problem(w http.ResponseWriter, r *http.Request, status int, typ, code, title, detail string) {
	httpx.WriteProblem(w, r, httpx.Problem{
		Type: httpx.ProblemTypeBase + typ, Title: title, Status: status, Code: code, Detail: detail,
	})
}

// writeJSON answers with no-store. The answer to the same URL differs by who asked for it.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// queryLimit reads the paging limit; a malformed value falls back to the default.
func queryLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 0
	}
	return n
}

// queryUUID reads an optional uuid filter; a malformed one is a field error rather than a
// silently empty page.
func queryUUID(r *http.Request, name string, fields *[]domain.FieldError) *uuid.UUID {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{
			Field: name, Code: "FORMAT", Message: "geçerli bir kimlik olmalı",
		})
		return nil
	}
	return &id
}

// queryDate reads an optional date filter.
func queryDate(r *http.Request, name string, fields *[]domain.FieldError) *time.Time {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{
			Field: name, Code: "FORMAT", Message: "YYYY-MM-DD biçiminde olmalı",
		})
		return nil
	}
	return &t
}

// queryInt reads an optional whole-number filter.
func queryInt(r *http.Request, name string, fields *[]domain.FieldError) *int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{
			Field: name, Code: "FORMAT", Message: "tam sayı olmalı",
		})
		return nil
	}
	return &n
}
