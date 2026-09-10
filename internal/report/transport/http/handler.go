// Package reporthttp serves the reporting endpoints: the provider statement, the reconciliation
// runs, the operations dashboard and the exports. Bodies use the generated contract types; errors
// are problem+json with stable codes and Turkish titles (ADR-015).
//
// Two things shape every route here.
//
// **No handler in this package computes a figure.** Every amount in every body was summed by
// PostgreSQL and travels as canonical decimal text; the mappers below copy strings. A handler that
// added two of them together would be a handler deciding what a total means.
//
// **No refusal in this package names another caller.** An export belongs to whoever asked for it,
// and "that export exists but is not yours" is a fact about a colleague. The read is narrowed in
// SQL instead, so somebody else's export is not found rather than found and then refused.
package reporthttp

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

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/report/application"
	"github.com/celikbros/kapsora/internal/report/domain"
)

// Permissions guarding the routes. Reading a figure and taking it out of the building are two
// grants, and the sensitive one is checked in the service rather than here because it is a
// property of the kind that was asked for and not of the route.
const (
	PermissionRead            = application.PermissionRead
	PermissionExport          = application.PermissionExport
	PermissionExportSensitive = application.PermissionExportSensitive
)

const maxBodyBytes = 1 << 20

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the Idempotency-Key middleware
// on every command that changes state.
//
// `CreateExport` is required for the ordinary reason a command is: an export replayed by a flaky
// network must queue one job and produce one watermarked file, not two. `DownloadExport` carries
// the optional variant, because a download that is replayed is a second download — it is counted
// and audited twice on purpose, since a person really did fetch the file twice — but a client that
// sends a key gets the first answer back rather than a second access event for one click.
type Middlewares struct {
	CreateExport   func(http.Handler) http.Handler
	DownloadExport func(http.Handler) http.Handler
}

// Handler serves the reporting operations.
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

// StatementRoutes mounts the statement under /providers/{providerId}.
func (h *Handler) StatementRoutes(r chi.Router) {
	r.Get("/{providerId}/statement", h.GetProviderStatement)
}

// ReconciliationRoutes mounts everything below /reconciliation-runs.
func (h *Handler) ReconciliationRoutes(r chi.Router) {
	r.Get("/", h.ListReconciliationRuns)
	r.Get("/{runId}", h.GetReconciliationRun)
}

// DashboardRoutes mounts everything below /operations.
func (h *Handler) DashboardRoutes(r chi.Router) {
	r.Get("/dashboard", h.GetOperationsDashboard)
}

// ExportRoutes mounts everything below /exports.
func (h *Handler) ExportRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListExports)
	r.With(wrap(mw.CreateExport)).Post("/", h.CreateExport)
	r.Get("/{exportId}", h.GetExport)
	r.With(wrap(mw.DownloadExport)).Post("/{exportId}/download", h.DownloadExport)
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

// writeError maps application and domain errors to the problem codes of this work package. Every
// title is Turkish and every one of them says what the caller can do next.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.Is(err, application.ErrExportNotFound):
		problem(w, r, http.StatusNotFound, "reports/export-not-found", "EXPORT_NOT_FOUND",
			"Dışa aktarma bulunamadı", "")
	case errors.Is(err, application.ErrRunNotFound):
		problem(w, r, http.StatusNotFound, "reports/run-not-found", "RECONCILIATION_RUN_NOT_FOUND",
			"Mutabakat kaydı bulunamadı", "")
	case errors.Is(err, application.ErrProviderScope):
		problem(w, r, http.StatusForbidden, "reports/provider-scope", "REPORT_PROVIDER_SCOPE",
			"Bu sağlayıcının raporlarını göremezsiniz", "")
	case errors.Is(err, application.ErrExportSensitive):
		// 403 rather than 422: the request is well formed and the caller is simply not allowed
		// to take claim line descriptions out of the building.
		problem(w, r, http.StatusForbidden, "reports/export-sensitive",
			"EXPORT_SENSITIVE_REQUIRED", "Bu dışa aktarma için ek yetki gerekli",
			"Hasar dosyası satır açıklamalarını dışa aktarmak report.export.sensitive yetkisi ister.")
	case errors.Is(err, application.ErrExportExpired):
		problem(w, r, http.StatusConflict, "reports/export-expired", "EXPORT_EXPIRED",
			"Dışa aktarmanın süresi doldu",
			"Dosyalar sınırlı süre saklanır; raporu yeniden oluşturun.")
	case errors.Is(err, application.ErrExportNotReady):
		problem(w, r, http.StatusConflict, "reports/export-not-ready", "EXPORT_NOT_READY",
			"Dışa aktarma henüz hazır değil", "Dosya hazırlanıyor; biraz sonra tekrar deneyin.")
	case errors.Is(err, application.ErrExportFailed):
		problem(w, r, http.StatusConflict, "reports/export-failed", "EXPORT_FAILED",
			"Dışa aktarma tamamlanamadı", "Raporu yeniden oluşturun; sorun sürerse yöneticinize başvurun.")
	case errors.Is(err, application.ErrTooManyRows):
		problem(w, r, http.StatusConflict, "reports/export-too-many-rows", "EXPORT_TOO_MANY_ROWS",
			"Dışa aktarma çok fazla satır içeriyor", "Dönemi daraltıp yeniden deneyin.")
	case errors.Is(err, application.ErrDocumentUnavailable):
		problem(w, r, http.StatusConflict, "reports/export-file-gone", "EXPORT_FILE_UNAVAILABLE",
			"Dışa aktarma dosyası artık yok", "Raporu yeniden oluşturun.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID",
			"Sayfa imleci geçersiz", "")
	default:
		// A reporting error carries no identifier and no clinical text: this module's errors name
		// codes, statuses and exact decimals, and no line description ever reaches one.
		h.logger.Error("report request failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR",
			"Beklenmeyen hata", "")
	}
}

// pathUUID reads an id from the path. A malformed id is indistinguishable from an unknown one, so
// both are answered with the same not-found: that a row exists at all is somebody else's business.
func (h *Handler) pathUUID(w http.ResponseWriter, r *http.Request, name string, notFound error) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		h.writeError(w, r, notFound)
		return uuid.Nil, false
	}
	return id, true
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

func problem(w http.ResponseWriter, r *http.Request, status int, typ, code, title, detail string) {
	httpx.WriteProblem(w, r, httpx.Problem{
		Type: httpx.ProblemTypeBase + typ, Title: title, Status: status, Code: code, Detail: detail,
	})
}

// writeJSON answers with no-store. The answer to the same URL differs by who asked for it, and a
// cached export list is a list of somebody else's files.
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

// queryUUID reads an optional uuid filter; a malformed one is a field error rather than a silently
// empty page.
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

// queryBool reads an optional flag, defaulting to `fallback` when it is absent or unreadable.
func queryBool(r *http.Request, name string, fallback bool) bool {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}
