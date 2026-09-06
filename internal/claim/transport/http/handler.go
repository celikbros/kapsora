// Package claimhttp serves /api/v1/claims. Bodies use the generated contract types; errors
// are problem+json with stable codes (ADR-015).
//
// One thing shapes every route here: **no handler in this package decides what a caller may
// see**. The permission the route names is the permission to reach the endpoint at all; the
// projection is applied in the application service, on the record, and the mappers below can
// only render what they were given. A field the service cleared is absent from the body
// because it is absent from the record, not because a mapper remembered to leave it out.
package claimhttp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/claim/domain"
	healthdomain "github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the routes. Reading a claim and reading its clinical detail are two
// grants on purpose: `claim.read` is what a sponsor's HR user has, and `health.clinical.read`
// is what it deliberately does not.
const (
	PermissionRead            = application.PermissionRead
	PermissionCreate          = application.PermissionCreate
	PermissionSubmit          = application.PermissionSubmit
	PermissionMedicalReview   = application.PermissionMedicalReview
	PermissionFinancialReview = application.PermissionFinancialReview
	PermissionCancel          = application.PermissionCancel
)

const maxBodyBytes = 1 << 20

// Headers a clinical read may state its reason in.
const (
	headerAccessPurpose    = "X-Access-Purpose"
	headerAccessReason     = "X-Access-Reason"
	headerAccessProjection = "X-Access-Projection"
)

// projectionFinancial is the only value X-Access-Projection is defined to carry: a caller
// declining to read clinical detail. There is no CLINICAL counterpart, because asking for
// the clinical projection is not a thing a caller does — it is a thing it has earned.
const projectionFinancial = "FINANCIAL"

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the Idempotency-Key
// middleware on every command that changes state. A nil field means "no wrapper".
type Middlewares struct {
	CreateClaim  func(http.Handler) http.Handler
	PatchClaim   func(http.Handler) http.Handler
	PutLines     func(http.Handler) http.Handler
	SubmitClaim  func(http.Handler) http.Handler
	DecideLines  func(http.Handler) http.Handler
	ApproveClaim func(http.Handler) http.Handler
	RejectClaim  func(http.Handler) http.Handler
	ReturnClaim  func(http.Handler) http.Handler
	CancelClaim  func(http.Handler) http.Handler
}

// Handler serves the claim operations.
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

// Routes mounts everything below /claims.
func (h *Handler) Routes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListClaims)
	r.With(wrap(mw.CreateClaim)).Post("/", h.CreateClaim)
	r.Get("/{claimId}", h.GetClaim)
	r.With(wrap(mw.PatchClaim)).Patch("/{claimId}", h.PatchClaimDraft)
	r.With(wrap(mw.PutLines)).Put("/{claimId}/lines", h.PutClaimLines)
	r.With(wrap(mw.SubmitClaim)).Post("/{claimId}/submit", h.SubmitClaim)
	r.With(wrap(mw.DecideLines)).Post("/{claimId}/line-decisions", h.DecideClaimLines)
	r.With(wrap(mw.ApproveClaim)).Post("/{claimId}/approve", h.ApproveClaim)
	r.With(wrap(mw.RejectClaim)).Post("/{claimId}/reject", h.RejectClaim)
	r.With(wrap(mw.ReturnClaim)).Post("/{claimId}/return", h.ReturnClaim)
	r.With(wrap(mw.CancelClaim)).Post("/{claimId}/cancel", h.CancelClaim)
	r.Get("/{claimId}/versions", h.ListClaimVersions)
	r.Get("/{claimId}/versions/{versionNo}", h.GetClaimVersion)
	r.Get("/{claimId}/invoice-readiness", h.GetClaimInvoiceReadiness)
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

// requireReview resolves a caller holding either review grant. The stage a decision is written
// at is the claim's, not the caller's, so the route only has to establish that the caller is a
// reviewer at all — and the service refuses the wrong stage with CLAIM_STAGE_MISMATCH, which
// is a different and more useful answer than a blanket 403.
func (h *Handler) requireReview(w http.ResponseWriter, r *http.Request) (identity.RequestContext, bool) {
	rc, err := identity.Require(r.Context(), PermissionMedicalReview)
	if err == nil {
		return rc, true
	}
	rc, financialErr := identity.Require(r.Context(), PermissionFinancialReview)
	if financialErr != nil {
		h.deny.Deny(w, r, financialErr, PermissionFinancialReview)
		return identity.RequestContext{}, false
	}
	return rc, true
}

// accessRequest reads the three headers a clinical read states its terms in. The purpose and
// the reason are not validated here: the service checks the purpose against the reference
// table, so there is one authority rather than a copy of the list in the transport.
//
// X-Access-Projection is different, and is checked here, because it is not a fact about the
// tenant's data at all: the contract defines exactly one value for it, and a caller sending
// another has written a request the API does not have. A false return means the problem has
// already been written and the handler owes the caller nothing further.
//
// The reason is percent-decoded because an HTTP header value is ISO-8859-1 and a browser
// refuses to send one containing ğ, ş or ı — which is most of the Turkish a person would
// actually type.
func (h *Handler) accessRequest(w http.ResponseWriter, r *http.Request) (application.AccessRequest, bool) {
	req := application.AccessRequest{
		PurposeCode: strings.TrimSpace(r.Header.Get(headerAccessPurpose)),
		ReasonText:  decodeReason(r.Header.Get(headerAccessReason)),
	}
	switch projection := strings.TrimSpace(r.Header.Get(headerAccessProjection)); {
	case projection == "":
	case strings.EqualFold(projection, projectionFinancial):
		req.FinancialOnly = true
	default:
		// 400 rather than the 422 an unknown purpose is answered: a purpose outside the
		// reference table is a statement about the tenant's data, and a projection outside
		// this enum is a request the contract has no shape for.
		writeHealthValidationStatus(w, r, http.StatusBadRequest, []healthdomain.FieldError{{
			Field: headerAccessProjection, Code: "ENUM",
			Message: "geçerli değer: " + projectionFinancial,
		}})
		return application.AccessRequest{}, false
	}
	return req, true
}

func decodeReason(raw string) string {
	trimmed := strings.TrimSpace(raw)
	decoded, err := url.QueryUnescape(trimmed)
	if err != nil {
		return trimmed
	}
	return strings.TrimSpace(decoded)
}

// writeError maps application and domain errors to the problem codes of the work package.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	var healthVE *healthdomain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.As(err, &healthVE):
		// The access headers are validated by WP-I5-01's own domain, so its field errors
		// reach this handler unchanged rather than being re-spelled here.
		writeHealthValidation(w, r, healthVE.Fields)
	case errors.Is(err, application.ErrClaimNotFound):
		problem(w, r, http.StatusNotFound, "claims/not-found", "CLAIM_NOT_FOUND",
			"Hasar dosyası bulunamadı", "")
	case errors.Is(err, application.ErrVersionNotFound):
		problem(w, r, http.StatusNotFound, "claims/version-not-found", "CLAIM_VERSION_NOT_FOUND",
			"Hasar dosyası sürümü bulunamadı", "")
	case errors.Is(err, application.ErrVersionFrozen):
		// The freeze the whole package exists for. 409 rather than 403: the caller is
		// allowed to write claims, and this one has simply stopped being writable.
		problem(w, r, http.StatusConflict, "claims/version-frozen", "CLAIM_VERSION_FROZEN",
			"Gönderilmiş sürüm değiştirilemez",
			"Düzeltme için dosyayı iade edin; yeni sürüm taslak olarak açılır.")
	case errors.Is(err, application.ErrStageMismatch):
		problem(w, r, http.StatusConflict, "claims/stage-mismatch", "CLAIM_STAGE_MISMATCH",
			"Dosya bu inceleme aşamasında değil",
			"Tıbbi inceleme, mali incelemeden önce tamamlanır.")
	case errors.Is(err, application.ErrLineUndecided):
		problem(w, r, http.StatusConflict, "claims/line-undecided", "CLAIM_LINE_UNDECIDED",
			"Karara bağlanmamış satır var",
			"Dosyayı sonuçlandırmadan önce her satır için karar girin.")
	case errors.Is(err, application.ErrLineNotFound):
		problem(w, r, http.StatusUnprocessableEntity, "claims/line-not-found", "CLAIM_LINE_NOT_FOUND",
			"Bu sürümde böyle bir satır yok", "")
	case errors.Is(err, application.ErrLineRequired):
		problem(w, r, http.StatusUnprocessableEntity, "claims/line-required", "CLAIM_LINE_REQUIRED",
			"Gönderim için en az bir kalem gerekli", "")
	case errors.Is(err, application.ErrApprovalNotPermitted):
		problem(w, r, http.StatusForbidden, "claims/approval-not-permitted",
			"CLAIM_APPROVAL_NOT_PERMITTED", "Bu tutarı onaylama yetkiniz yok",
			"Onay politikası bu tutar için başka bir rol istiyor.")
	case errors.Is(err, application.ErrNotDecided):
		problem(w, r, http.StatusConflict, "claims/not-decided", "CLAIM_NOT_DECIDED",
			"Dosya henüz sonuçlanmadı",
			"Fatura hazırlığı yalnızca onaylanmış ya da kısmen onaylanmış dosya için sorulur.")
	case errors.Is(err, application.ErrTransitionInvalid):
		problem(w, r, http.StatusConflict, "claims/transition-invalid", "CLAIM_TRANSITION_INVALID",
			"Dosya bu durumda bu işleme uygun değil", "")
	case errors.Is(err, application.ErrProviderScope):
		problem(w, r, http.StatusForbidden, "claims/provider-scope", "CLAIM_PROVIDER_SCOPE",
			"Bu sağlayıcı adına işlem yapamazsınız", "")
	case errors.Is(err, application.ErrProviderUnknown):
		problem(w, r, http.StatusUnprocessableEntity, "claims/provider-unknown",
			"CLAIM_PROVIDER_UNKNOWN", "Bu kurum tenant'ın sağlayıcısı değil", "")
	case errors.Is(err, application.ErrProviderNoProfile):
		problem(w, r, http.StatusUnprocessableEntity, "claims/provider-profile-missing",
			"CLAIM_PROVIDER_PROFILE_MISSING", "Sağlayıcı profili tanımlı değil",
			"Fiyatlandırma için kurumun sağlayıcı profili oluşturulmalı.")
	case errors.Is(err, application.ErrEnrollmentMismatch):
		problem(w, r, http.StatusUnprocessableEntity, "claims/enrollment-mismatch",
			"CLAIM_ENROLLMENT_MISMATCH", "Plan kaydı bu kişiye veya programa ait değil", "")
	case errors.Is(err, application.ErrServiceUnknown):
		problem(w, r, http.StatusUnprocessableEntity, "claims/service-unknown",
			"CLAIM_SERVICE_UNKNOWN", "Hizmet tanımı katalogda bulunamadı", "")
	case errors.Is(err, application.ErrAccessPurposeRequired):
		problem(w, r, http.StatusPreconditionRequired, "health/access-purpose-required",
			"ACCESS_PURPOSE_REQUIRED", "Erişim amacı belirtilmeli",
			"Bu kaydı görüntülemek için X-Access-Purpose başlığıyla erişim amacınızı bildirin.")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID",
			"Sayfa imleci geçersiz", "")
	default:
		// A claim error carries no clinical data: this module's errors name ids, codes and
		// statuses, and no description or diagnosis ever reaches one.
		h.logger.Error("claim command failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR",
			"Beklenmeyen hata", "")
	}
}

// claimID reads the claim id from the path. A malformed id is indistinguishable from an
// unknown one, so both are answered 404: that a claim exists at all is somebody else's
// business.
func (h *Handler) claimID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "claimId"))
	if err != nil {
		h.writeError(w, r, application.ErrClaimNotFound)
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

func writeHealthValidation(w http.ResponseWriter, r *http.Request, fields []healthdomain.FieldError) {
	writeHealthValidationStatus(w, r, http.StatusUnprocessableEntity, fields)
}

// writeHealthValidationStatus is writeHealthValidation with the status spelled out, for the
// one field error that is a malformed request rather than a rejected value.
func writeHealthValidationStatus(w http.ResponseWriter, r *http.Request, status int,
	fields []healthdomain.FieldError,
) {
	p := httpx.Problem{
		Type: httpx.ProblemTypeBase + "generic/validation-failed", Title: "Doğrulama hatası",
		Status: status, Code: "VALIDATION_FAILED",
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

// writeJSON answers with no-store. A clinical body is not something an intermediary should
// keep a copy of, and the answer to the same URL differs by who asked for it.
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
		*fields = append(*fields, domain.FieldError{Field: name, Code: "FORMAT", Message: "geçerli bir kimlik olmalı"})
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
		*fields = append(*fields, domain.FieldError{Field: name, Code: "FORMAT", Message: "YYYY-MM-DD biçiminde olmalı"})
		return nil
	}
	return &t
}
