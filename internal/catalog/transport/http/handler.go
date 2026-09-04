// Package cataloghttp serves /api/v1/service-categories, /service-definitions and
// /code-systems. Bodies use the generated contract types; errors are problem+json with
// stable codes (ADR-015). Catalog rows carry no personal data, so responses and audit
// details may name codes freely.
package cataloghttp

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

	"github.com/celikbros/kapsora/internal/catalog/application"
	"github.com/celikbros/kapsora/internal/catalog/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the routes (migration 000008).
const (
	PermissionRead   = "catalog.read"
	PermissionManage = "catalog.manage"
)

const (
	maxBodyBytes = 256 << 10
	// A 5000 row code value batch is much larger than any other request body in the API,
	// so the import endpoint gets its own ceiling.
	maxImportBodyBytes    = 8 << 20
	mergePatchContentType = "application/merge-patch+json"
)

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the idempotency
// middleware on the create commands and on the import. A nil field means "no wrapper".
type Middlewares struct {
	CreateCategory   func(http.Handler) http.Handler
	CreateDefinition func(http.Handler) http.Handler
	CreateCodeSystem func(http.Handler) http.Handler
	ImportCodeValues func(http.Handler) http.Handler
}

// Handler serves the catalog operations.
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

// CategoryRoutes mounts everything below /service-categories.
func (h *Handler) CategoryRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListServiceCategories)
	r.With(wrap(mw.CreateCategory)).Post("/", h.CreateServiceCategory)
	r.Get("/{categoryId}", h.GetServiceCategory)
	r.Patch("/{categoryId}", h.PatchServiceCategory)
}

// DefinitionRoutes mounts everything below /service-definitions.
func (h *Handler) DefinitionRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListServiceDefinitions)
	r.With(wrap(mw.CreateDefinition)).Post("/", h.CreateServiceDefinition)
	r.Get("/{definitionId}", h.GetServiceDefinition)
	r.Patch("/{definitionId}", h.PatchServiceDefinition)
	r.Get("/{definitionId}/code-mappings", h.ListServiceCodeMappings)
	r.Put("/{definitionId}/code-mappings", h.PutServiceCodeMappings)
}

// CodeSystemRoutes mounts everything below /code-systems.
func (h *Handler) CodeSystemRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListCodeSystems)
	r.With(wrap(mw.CreateCodeSystem)).Post("/", h.CreateCodeSystem)
	r.Patch("/{codeSystemId}", h.PatchCodeSystem)
	r.Get("/{codeSystemId}/values", h.ListCodeValues)
	r.With(wrap(mw.ImportCodeValues)).Post("/{codeSystemId}/values:import", h.ImportCodeValues)
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
	case errors.Is(err, application.ErrCategoryNotFound):
		problem(w, r, http.StatusNotFound, "catalog/service-category-not-found", "SERVICE_CATEGORY_NOT_FOUND",
			"Hizmet kategorisi bulunamadı", "")
	case errors.Is(err, application.ErrDefinitionNotFound):
		problem(w, r, http.StatusNotFound, "catalog/service-definition-not-found", "SERVICE_DEFINITION_NOT_FOUND",
			"Hizmet tanımı bulunamadı", "")
	case errors.Is(err, application.ErrCodeSystemNotFound):
		problem(w, r, http.StatusNotFound, "catalog/code-system-not-found", "CODE_SYSTEM_NOT_FOUND",
			"Kod sistemi bulunamadı", "")
	case errors.Is(err, domain.ErrCategoryCycle):
		problem(w, r, http.StatusConflict, "catalog/category-cycle", "CATEGORY_CYCLE",
			"Kategori kendi alt ağacına bağlanamaz", "Seçtiğiniz üst kategori bu kategorinin altında yer alıyor.")
	case errors.Is(err, domain.ErrMappingOverlap):
		problem(w, r, http.StatusConflict, "catalog/code-mapping-overlap", "CODE_MAPPING_OVERLAP",
			"Kod eşlemesi dönemleri çakışıyor", "Aynı kod sistemi ve kod için geçerlilik dönemleri örtüşemez.")
	case errors.Is(err, application.ErrCategoryCodeTaken):
		problem(w, r, http.StatusConflict, "catalog/category-code-taken", "CATEGORY_CODE_TAKEN",
			"Bu kategori kodu zaten kullanılıyor", "")
	case errors.Is(err, application.ErrDefinitionCodeTaken):
		problem(w, r, http.StatusConflict, "catalog/service-definition-code-taken", "SERVICE_DEFINITION_CODE_TAKEN",
			"Bu hizmet kodu zaten kullanılıyor", "")
	case errors.Is(err, application.ErrCodeSystemTaken):
		problem(w, r, http.StatusConflict, "catalog/code-system-taken", "CODE_SYSTEM_TAKEN",
			"Bu kod sistemi ve sürümü zaten kayıtlı", "")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID", "Sayfa imleci geçersiz", "")
	default:
		h.logger.Error("catalog request failed", "error", err)
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
	return decodeJSONLimit(w, r, dst, maxBodyBytes)
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
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

func decodeBool(raw json.RawMessage, field string, fields *[]domain.FieldError) *bool {
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "true veya false olmalı"})
		return nil
	}
	return &b
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

// immutable is the answer to a patch that carries a field the catalog never renames.
func immutable(field string, fields *[]domain.FieldError) {
	*fields = append(*fields, domain.FieldError{Field: field, Code: "IMMUTABLE", Message: "bu alan değiştirilemez"})
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

// fieldPath names one element of a request array in a validation error.
func fieldPath(index int, field string) string {
	return "items[" + strconv.Itoa(index) + "]." + field
}

// queryLimit reads the paging limit; a malformed value falls back to the default.
func queryLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 0
	}
	return n
}

// queryBool reads a tri-state boolean filter: absent, true or false.
func queryBool(r *http.Request, name string, fields *[]domain.FieldError) *bool {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{Field: name, Code: "TYPE", Message: "true veya false olmalı"})
		return nil
	}
	return &v
}

// queryUUID reads an optional uuid filter.
func queryUUID(r *http.Request, name string, fields *[]domain.FieldError) *uuid.UUID {
	raw := r.URL.Query().Get(name)
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
