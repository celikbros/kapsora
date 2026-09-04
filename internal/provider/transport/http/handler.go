// Package providerhttp serves /api/v1/providers, /provider-locations and /practitioners.
// Bodies use the generated contract types; errors are problem+json with stable codes
// (ADR-015). A practitioner's registration number never appears in a response, a log, a
// URL or an error message: only the masked form leaves this package.
package providerhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/provider/application"
	"github.com/celikbros/kapsora/internal/provider/domain"
)

// Permissions guarding the routes (migration 000008). Practitioner writes and the
// registration search take the third one, because a registration number is sensitive.
const (
	PermissionRead               = "provider.read"
	PermissionManage             = "provider.manage"
	PermissionPractitionerManage = "provider.practitioner.manage"
)

const (
	maxBodyBytes          = 256 << 10
	mergePatchContentType = "application/merge-patch+json"
)

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the idempotency
// middleware on the create commands and the rate limiter on the registration search. A nil
// field means "no wrapper".
type Middlewares struct {
	CreateProvider     func(http.Handler) http.Handler
	CreateLocation     func(http.Handler) http.Handler
	CreatePractitioner func(http.Handler) http.Handler
	SearchRegistration func(http.Handler) http.Handler
}

// Handler serves the provider network operations.
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

// ProviderRoutes mounts everything below /providers. The static /search pattern is
// registered beside the {providerId} one; chi matches the literal first.
func (h *Handler) ProviderRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListProviders)
	r.With(wrap(mw.CreateProvider)).Post("/", h.CreateProvider)
	r.Get("/search", h.SearchProviders)
	r.Get("/{providerId}", h.GetProvider)
	r.Patch("/{providerId}", h.PatchProvider)
	r.Post("/{providerId}/activate", h.ActivateProvider)
	r.Post("/{providerId}/suspend", h.SuspendProvider)
	r.Post("/{providerId}/terminate", h.TerminateProvider)
	r.Get("/{providerId}/locations", h.ListProviderLocations)
	r.With(wrap(mw.CreateLocation)).Post("/{providerId}/locations", h.CreateProviderLocation)
	r.Get("/{providerId}/practitioners", h.ListPractitioners)
	r.With(wrap(mw.CreatePractitioner)).Post("/{providerId}/practitioners", h.CreatePractitioner)
}

// LocationRoutes mounts everything below /provider-locations.
func (h *Handler) LocationRoutes(r chi.Router) {
	r.Get("/{locationId}", h.GetProviderLocation)
	r.Patch("/{locationId}", h.PatchProviderLocation)
	r.Get("/{locationId}/capabilities", h.ListProviderCapabilities)
	r.Put("/{locationId}/capabilities", h.PutProviderCapabilities)
}

// PractitionerRoutes mounts everything below /practitioners.
func (h *Handler) PractitionerRoutes(r chi.Router, mw Middlewares) {
	r.With(wrap(mw.SearchRegistration)).Post("/search-by-registration", h.SearchPractitionerByRegistration)
	r.Get("/{practitionerId}", h.GetPractitioner)
	r.Patch("/{practitionerId}", h.PatchPractitioner)
	r.Put("/{practitionerId}/locations", h.PutPractitionerLocations)
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
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.Is(err, application.ErrProviderNotFound):
		// A provider outside the caller's organization scope lands here too: 404 rather
		// than 403, so the existence of another provider does not leak.
		problem(w, r, http.StatusNotFound, "provider/provider-not-found", "PROVIDER_NOT_FOUND",
			"Sağlayıcı bulunamadı", "")
	case errors.Is(err, application.ErrLocationNotFound):
		problem(w, r, http.StatusNotFound, "provider/location-not-found", "PROVIDER_LOCATION_NOT_FOUND",
			"Sağlayıcı lokasyonu bulunamadı", "")
	case errors.Is(err, application.ErrPractitionerNotFound):
		problem(w, r, http.StatusNotFound, "provider/practitioner-not-found", "PRACTITIONER_NOT_FOUND",
			"Uygulayıcı bulunamadı", "")
	case errors.Is(err, domain.ErrTransitionInvalid):
		problem(w, r, http.StatusConflict, "provider/transition-invalid", "PROVIDER_TRANSITION_INVALID",
			"Sağlayıcı bu duruma geçemez", "Sonlandırılmış bir sağlayıcı yeniden açılamaz.")
	case errors.Is(err, domain.ErrCapabilityOverlap):
		problem(w, r, http.StatusConflict, "provider/capability-overlap", "CAPABILITY_OVERLAP",
			"Yetkinlik dönemleri çakışıyor", "Aynı hizmet tanımı veya kategori için geçerlilik dönemleri örtüşemez.")
	case errors.Is(err, domain.ErrAssignmentOverlap):
		problem(w, r, http.StatusConflict, "provider/practitioner-assignment-overlap", "PRACTITIONER_ASSIGNMENT_OVERLAP",
			"Uygulayıcı görevlendirmeleri çakışıyor", "Aynı lokasyon ve görev için dönemler örtüşemez.")
	case errors.Is(err, application.ErrProviderProfileExists):
		problem(w, r, http.StatusConflict, "provider/profile-exists", "PROVIDER_PROFILE_EXISTS",
			"Bu kurumun zaten bir sağlayıcı profili var", "")
	case errors.Is(err, application.ErrLocationCodeTaken):
		problem(w, r, http.StatusConflict, "provider/location-code-taken", "LOCATION_CODE_TAKEN",
			"Bu lokasyon kodu bu sağlayıcıda kullanılıyor", "")
	case errors.Is(err, application.ErrRegistrationTaken):
		problem(w, r, http.StatusConflict, "provider/registration-taken", "PRACTITIONER_REGISTRATION_TAKEN",
			"Bu sicil numarası bu otoritede kayıtlı", "")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID", "Sayfa imleci geçersiz", "")
	default:
		// The message of a provider error never carries a registration number, so it is
		// safe to log; the value itself is normalized and dropped before any error is
		// built.
		h.logger.Error("provider request failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR", "Beklenmeyen hata", "")
	}
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

func etag(version int64) string { return fmt.Sprintf(`"%d"`, version) }

// requireIfMatch parses the If-Match header, answering 428 when it is missing.
func requireIfMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	v = strings.TrimPrefix(v, "W/")
	v = strings.Trim(v, `"`)
	n, err := strconv.ParseInt(v, 10, 64)
	if v == "" || err != nil || n < 1 {
		problem(w, r, http.StatusPreconditionRequired, "generic/precondition-required", "IF_MATCH_REQUIRED",
			"If-Match başlığı gerekli", "GET yanıtındaki ETag değerini If-Match olarak gönderin.")
		return 0, false
	}
	return n, true
}

// requireMergePatch enforces the merge-patch media type of PATCH endpoints.
func requireMergePatch(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), mergePatchContentType) {
		problem(w, r, http.StatusUnsupportedMediaType, "generic/unsupported-media-type", "UNSUPPORTED_MEDIA_TYPE",
			"Content-Type application/merge-patch+json olmalı", "")
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
			problem(w, r, http.StatusRequestEntityTooLarge, "generic/request-body-too-large", "REQUEST_BODY_TOO_LARGE",
				"İstek gövdesi çok büyük", "")
			return false
		}
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY",
			"İstek gövdesi geçersiz", "")
		return false
	}
	return true
}

// isJSONNull recognises an explicit null (merge-patch: "remove this field").
func isJSONNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

func decodeString(raw json.RawMessage, field string, fields *[]domain.FieldError) *string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "metin olmalı"})
		return nil
	}
	return &s
}

func decodeFloat(raw json.RawMessage, field string, fields *[]domain.FieldError) *float64 {
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "sayı olmalı"})
		return nil
	}
	return &f
}

func decodeDate(raw json.RawMessage, field string, fields *[]domain.FieldError) *time.Time {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "tarih metni olmalı"})
		return nil
	}
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "FORMAT", Message: "YYYY-MM-DD biçiminde olmalı"})
		return nil
	}
	return &d
}

// immutable is the answer to a patch that carries a field this module never rewrites.
func immutable(field string, fields *[]domain.FieldError) {
	*fields = append(*fields, domain.FieldError{Field: field, Code: "IMMUTABLE", Message: "bu alan değiştirilemez"})
}

func unknownField(field string, fields *[]domain.FieldError) {
	*fields = append(*fields, domain.FieldError{Field: field, Code: "UNKNOWN_FIELD", Message: "bilinmeyen alan"})
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
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

// queryDate reads an optional date filter; the zero value means "not supplied".
func queryDate(r *http.Request, name string, fields *[]domain.FieldError) time.Time {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return time.Time{}
	}
	d, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{Field: name, Code: "FORMAT", Message: "YYYY-MM-DD biçiminde olmalı"})
		return time.Time{}
	}
	return d
}
