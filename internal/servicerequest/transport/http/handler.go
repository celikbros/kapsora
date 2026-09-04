// Package servicerequesthttp serves /api/v1/service-requests. Bodies use the generated
// contract types; errors are problem+json with stable codes (ADR-015).
//
// There is no handler here that writes a status. The lifecycle is reached only through the
// six commands below, each with its own route, its own permission and its own reason, and a
// PATCH carrying `status` is answered 422 with the field code IMMUTABLE rather than quietly
// ignored — a field that is silently dropped is a field somebody will keep sending.
package servicerequesthttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/servicerequest/application"
	"github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// Permissions guarding the routes (migration 000008). Reviewing is separate from creating
// on purpose: the person who asks for something is not the person who grants it.
const (
	PermissionRead   = application.PermissionRead
	PermissionCreate = application.PermissionCreate
	PermissionSubmit = application.PermissionSubmit
	PermissionReview = application.PermissionReview
	PermissionCancel = application.PermissionCancel
)

const (
	maxBodyBytes          = 1 << 20
	mergePatchContentType = "application/merge-patch+json"
)

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the idempotency
// middleware on the create and the commands. A nil field means "no wrapper".
type Middlewares struct {
	Create           func(http.Handler) http.Handler
	Submit           func(http.Handler) http.Handler
	Return           func(http.Handler) http.Handler
	Reject           func(http.Handler) http.Handler
	Approve          func(http.Handler) http.Handler
	PartiallyApprove func(http.Handler) http.Handler
	Cancel           func(http.Handler) http.Handler
}

// Handler serves the service request operations.
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

// Routes mounts everything below /service-requests. Each command is a route of its own
// rather than a verb in a body, which is what makes "who may do this" a routing fact.
func (h *Handler) Routes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListServiceRequests)
	r.With(wrap(mw.Create)).Post("/", h.CreateServiceRequest)
	r.Get("/{requestId}", h.GetServiceRequest)
	r.Patch("/{requestId}", h.PatchServiceRequestDraft)
	r.Put("/{requestId}/items", h.PutServiceRequestItems)
	r.With(wrap(mw.Submit)).Post("/{requestId}/submit", h.SubmitServiceRequest)
	r.With(wrap(mw.Return)).Post("/{requestId}/return", h.ReturnServiceRequest)
	r.With(wrap(mw.Reject)).Post("/{requestId}/reject", h.RejectServiceRequest)
	r.With(wrap(mw.Approve)).Post("/{requestId}/approve", h.ApproveServiceRequest)
	r.With(wrap(mw.PartiallyApprove)).Post("/{requestId}/partially-approve", h.PartiallyApproveServiceRequest)
	r.With(wrap(mw.Cancel)).Post("/{requestId}/cancel", h.CancelServiceRequest)
	r.Get("/{requestId}/versions", h.ListServiceRequestVersions)
	r.Get("/{requestId}/versions/{versionNo}", h.GetServiceRequestVersion)
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
	case errors.Is(err, application.ErrRequestNotFound):
		problem(w, r, http.StatusNotFound, "service-requests/not-found", "SERVICE_REQUEST_NOT_FOUND",
			"Hizmet talebi bulunamadı", "")
	case errors.Is(err, application.ErrVersionNotFound):
		problem(w, r, http.StatusNotFound, "service-requests/version-not-found",
			"SERVICE_REQUEST_VERSION_NOT_FOUND", "Talep sürümü bulunamadı", "")
	case errors.Is(err, application.ErrProviderScope):
		// A provider that may not act for this organization is told so; a provider asking
		// about another provider's request is answered 404 by the repository, which never
		// returns the row in the first place.
		problem(w, r, http.StatusForbidden, "service-requests/provider-scope",
			"SERVICE_REQUEST_PROVIDER_SCOPE", "Bu sağlayıcı adına işlem yapamazsınız", "")
	case errors.Is(err, application.ErrDraftNotFound):
		problem(w, r, http.StatusConflict, "service-requests/draft-missing",
			"SERVICE_REQUEST_DRAFT_MISSING", "Talebin düzenlenebilir bir sürümü yok",
			"Talebi düzeltmek için önce inceleyenden geri gönderilmesini isteyin.")
	case errors.Is(err, application.ErrVersionImmutable):
		problem(w, r, http.StatusConflict, "service-requests/version-immutable",
			"SERVICE_REQUEST_VERSION_IMMUTABLE", "Gönderilmiş talep sürümü değiştirilemez",
			"Bu sürümle verilmiş kararlar var; talep geri gönderildiğinde yeni bir sürüm açılır.")
	case errors.Is(err, application.ErrTransitionInvalid):
		problem(w, r, http.StatusConflict, "service-requests/transition-invalid",
			"REQUEST_TRANSITION_INVALID", "Bu durum geçişi yapılamaz",
			"Talebin bulunduğu durumda bu komut geçerli değil.")
	case errors.Is(err, application.ErrEnrollmentMismatch):
		problem(w, r, http.StatusUnprocessableEntity, "service-requests/enrollment-mismatch",
			"SERVICE_REQUEST_ENROLLMENT_MISMATCH", "Plan kaydı bu kişiye veya programa ait değil", "")
	case errors.Is(err, application.ErrReferenceCollision):
		problem(w, r, http.StatusConflict, "service-requests/reference-unavailable",
			"SERVICE_REQUEST_REFERENCE_UNAVAILABLE", "Talep numarası üretilemedi",
			"Lütfen isteği yeniden gönderin.")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID",
			"Sayfa imleci geçersiz", "")
	default:
		// A request error carries no personal data: this module stores ids, codes, dates
		// and quantities only, and the snapshots are filtered before they are written.
		h.logger.Error("service request failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR",
			"Beklenmeyen hata", "")
	}
}

// pathUUID reads the request id from the path; a malformed id is indistinguishable from an
// unknown one, so both are answered the same way.
func (h *Handler) pathUUID(w http.ResponseWriter, r *http.Request, notFound error) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "requestId"))
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

// requireMergePatch enforces the merge-patch media type of the PATCH endpoint.
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

// decodeOptionalJSON accepts an empty body, which submit allows.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY",
			"İstek gövdesi geçersiz", "")
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
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

func decodeUUID(raw json.RawMessage, field string, fields *[]domain.FieldError) *uuid.UUID {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "metin olmalı"})
		return nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "FORMAT", Message: "geçerli bir kimlik olmalı"})
		return nil
	}
	return &id
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

func decodeTimestamp(raw json.RawMessage, field string, fields *[]domain.FieldError) *time.Time {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "zaman metni olmalı"})
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "FORMAT", Message: "RFC3339 biçiminde olmalı"})
		return nil
	}
	return &t
}

// immutable is the answer to a patch carrying a field this module never rewrites. `status`
// is the one that matters: it is refused rather than ignored, because a caller who is
// allowed to send it will keep believing it did something.
func immutable(field string, fields *[]domain.FieldError) {
	*fields = append(*fields, domain.FieldError{
		Field: field, Code: "IMMUTABLE",
		Message: "bu alan bu uçtan değiştirilemez; durum değişiklikleri kendi komutlarıyla yapılır",
	})
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

func queryDate(r *http.Request, name string, fields *[]domain.FieldError) *time.Time {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil
	}
	d, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{Field: name, Code: "FORMAT", Message: "YYYY-MM-DD biçiminde olmalı"})
		return nil
	}
	return &d
}

func queryTimestamp(r *http.Request, name string, fields *[]domain.FieldError) *time.Time {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{Field: name, Code: "FORMAT", Message: "RFC3339 biçiminde olmalı"})
		return nil
	}
	return &t
}
