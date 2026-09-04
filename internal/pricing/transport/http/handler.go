// Package pricinghttp serves /api/v1/pricing/quotes. Bodies use the generated contract
// types; errors are problem+json with stable codes (ADR-015). The arithmetic is never
// reached from here: the application layer owns what is loaded, in what order and what may
// be written, and this package only translates between HTTP and it.
package pricinghttp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/pricing/application"
)

// PermissionQuote guards both routes (migration 000023, granted to PROGRAM_MANAGER,
// CONTRACT_MANAGER, FINANCIAL_REVIEWER and PROVIDER_STAFF).
const PermissionQuote = application.PermissionQuote

const maxBodyBytes = 1 << 20

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Handler serves the pricing quote operations.
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

// Routes mounts everything below /pricing.
//
// The platform Idempotency-Key middleware is deliberately not applied. A quote changes no
// business state, and its replay contract belongs to the quote table itself:
// contract.price_quote carries the key under a partial unique index, so the stored quote
// is the replayed answer and its request hash is what tells a repeat of the same question
// from a different question under a reused key. Wrapping the route in the middleware would
// keep a second copy of the response in system.idempotency_record and answer from there.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/quotes", h.CreatePriceQuote)
	r.Get("/quotes/{priceQuoteId}", h.GetPriceQuote)
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

// pathUUID reads a path parameter; a malformed id is indistinguishable from an unknown one.
func (h *Handler) pathUUID(w http.ResponseWriter, r *http.Request, name string, notFound error) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		h.writeError(w, r, notFound)
		return uuid.Nil, false
	}
	return id, true
}

// writeError maps the application errors to the problem codes of the work package.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *benefitdomain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.Is(err, application.ErrQuoteNotFound):
		problem(w, r, http.StatusNotFound, "pricing/quote-not-found", "PRICE_QUOTE_NOT_FOUND",
			"Fiyat teklifi bulunamadı", "")
	case errors.Is(err, application.ErrProviderNotFound):
		problem(w, r, http.StatusNotFound, "pricing/provider-not-found", "PRICE_QUOTE_PROVIDER_NOT_FOUND",
			"Sağlayıcı profili bulunamadı", "")
	case errors.Is(err, application.ErrLocationNotFound):
		problem(w, r, http.StatusUnprocessableEntity, "pricing/location-invalid", "PRICE_QUOTE_LOCATION_INVALID",
			"Lokasyon bu sağlayıcıya ait değil",
			"Fiyat, hizmetin verileceği yere göre seçilir; bu sağlayıcının bir lokasyonunu gönderin.")
	case errors.Is(err, application.ErrEvaluationNotFound):
		problem(w, r, http.StatusUnprocessableEntity, "pricing/eligibility-evaluation-invalid",
			"PRICE_QUOTE_ELIGIBILITY_INVALID", "Uygunluk değerlendirmesi bu hak sahibine ait değil", "")
	case errors.Is(err, application.ErrPackageNotFound):
		problem(w, r, http.StatusUnprocessableEntity, "pricing/package-not-found", "PRICE_QUOTE_PACKAGE_NOT_FOUND",
			"Paket tanımı bulunamadı", "")
	case errors.Is(err, application.ErrIdempotencyKeyReuse):
		problem(w, r, http.StatusConflict, "generic/idempotency-key-reused", "IDEMPOTENCY_KEY_REUSED",
			"Bu anahtar farklı bir istekle kullanılmış", "")
	case errors.Is(err, application.ErrProviderScope):
		problem(w, r, http.StatusForbidden, "identity/permission-denied", "PERMISSION_DENIED",
			"Bu işlem için yetkiniz yok", "Sağlayıcı kapsamınız dışında bir sağlayıcı için teklif alınamaz.")
	case errors.Is(err, application.ErrSerializationFailure):
		problem(w, r, http.StatusConflict, "pricing/quote-retry", "PRICE_QUOTE_RETRY",
			"Teklif hesaplanırken veriler değişti",
			"Teklif tek bir tutarlı okuma üzerinde hesaplanır; isteği aynen yeniden gönderin.")
	default:
		// A pricing error never carries personal data: this module reads ids, codes,
		// dates and quantities, and both snapshots are filtered before they are written.
		h.logger.Error("pricing request failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR",
			"Beklenmeyen hata", "")
	}
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

func writeValidation(w http.ResponseWriter, r *http.Request, fields []benefitdomain.FieldError) {
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
