// Package documenthttp serves /api/v1/documents and /api/v1/legal-holds. Bodies use the
// generated contract types; errors are problem+json with stable codes (ADR-015).
//
// One thing shapes every route here: **no endpoint carries a file body**. createUpload
// answers a presigned PUT and downloadDocument answers a presigned GET, so the largest
// request this handler ever reads is a JSON object of a few hundred bytes. That is why
// there is no multipart parsing, no size middleware and no content sniffing anywhere in
// the package — the size limit and the media type are signed into the URL, and the object
// store enforces them on a body the API never sees.
//
// downloadDocument is a POST rather than a GET for two reasons that both matter: it mints
// a short-lived bearer URL, which must not be cached or logged by an intermediary, and it
// records an access event with a reason the caller sends in the body.
package documenthttp

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/document/application"
	"github.com/celikbros/kapsora/internal/document/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the routes. Uploading, reading, linking and holding are four
// separate grants: a provider clerk uploads and reads, and only somebody who can put a
// document beyond the reach of retention holds one.
const (
	PermissionUpload    = application.PermissionUpload
	PermissionRead      = application.PermissionRead
	PermissionLink      = application.PermissionLink
	PermissionLegalHold = application.PermissionLegalHold
)

const maxBodyBytes = 1 << 20

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: the Idempotency-Key
// middleware on every command that changes state. A nil field means "no wrapper".
type Middlewares struct {
	CreateUpload     func(http.Handler) http.Handler
	CompleteUpload   func(http.Handler) http.Handler
	LinkDocument     func(http.Handler) http.Handler
	PutLegalHold     func(http.Handler) http.Handler
	ReleaseLegalHold func(http.Handler) http.Handler
}

// Handler serves the document and legal hold operations.
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

// DocumentRoutes mounts everything below /documents.
func (h *Handler) DocumentRoutes(r chi.Router, mw Middlewares) {
	r.Get("/", h.ListDocuments)
	r.With(wrap(mw.CreateUpload)).Post("/", h.CreateUpload)
	r.Get("/{documentId}", h.GetDocument)
	r.With(wrap(mw.CompleteUpload)).Post("/{documentId}/complete", h.CompleteUpload)
	r.Post("/{documentId}/download", h.DownloadDocument)
	r.With(wrap(mw.LinkDocument)).Post("/{documentId}/links", h.LinkDocument)
	r.Delete("/{documentId}/links/{linkId}", h.UnlinkDocument)
}

// LegalHoldRoutes mounts everything below /legal-holds.
func (h *Handler) LegalHoldRoutes(r chi.Router, mw Middlewares) {
	r.With(wrap(mw.PutLegalHold)).Post("/", h.PutLegalHold)
	r.With(wrap(mw.ReleaseLegalHold)).Post("/{legalHoldId}/release", h.ReleaseLegalHold)
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
	case errors.Is(err, application.ErrObjectNotFound):
		problem(w, r, http.StatusNotFound, "documents/not-found", "DOCUMENT_NOT_FOUND",
			"Belge bulunamadı", "")
	case errors.Is(err, application.ErrLinkNotFound):
		problem(w, r, http.StatusNotFound, "documents/link-not-found", "DOCUMENT_LINK_NOT_FOUND",
			"Belge bağlantısı bulunamadı", "")
	case errors.Is(err, application.ErrHoldNotFound):
		problem(w, r, http.StatusNotFound, "legal-holds/not-found", "LEGAL_HOLD_NOT_FOUND",
			"Hukuki saklama kaydı bulunamadı", "")
	case errors.Is(err, application.ErrNotScanned):
		problem(w, r, http.StatusConflict, "documents/not-scanned", "DOCUMENT_NOT_SCANNED",
			"Belge henüz taranmadı", "Tarama tamamlanana kadar belge indirilemez.")
	case errors.Is(err, application.ErrInfected):
		problem(w, r, http.StatusConflict, "documents/infected", "DOCUMENT_INFECTED",
			"Belgede zararlı yazılım bulundu", "Dosya silindi; yeniden yüklemeniz gerekir.")
	case errors.Is(err, application.ErrPurged):
		problem(w, r, http.StatusConflict, "documents/purged", "DOCUMENT_PURGED",
			"Belge saklama süresi dolduğu için silindi", "")
	case errors.Is(err, application.ErrUploadMissing):
		problem(w, r, http.StatusConflict, "documents/upload-missing", "DOCUMENT_UPLOAD_MISSING",
			"Yüklenen dosya bulunamadı", "Yükleme bağlantısı kullanılmamış ya da süresi dolmuş olabilir.")
	case errors.Is(err, application.ErrAlreadyCompleted):
		problem(w, r, http.StatusConflict, "documents/already-completed", "DOCUMENT_ALREADY_COMPLETED",
			"Bu yükleme zaten tamamlandı", "")
	case errors.Is(err, application.ErrLinkExists):
		problem(w, r, http.StatusConflict, "documents/link-exists", "DOCUMENT_LINK_EXISTS",
			"Belge bu kayda zaten bağlı", "")
	case errors.Is(err, application.ErrLinkPermission):
		// 403 rather than 404: the caller may see the document, and what it is missing is
		// the permission the link names. Hiding that would only make it retry.
		problem(w, r, http.StatusForbidden, "documents/link-permission", "DOCUMENT_LINK_PERMISSION_DENIED",
			"Bu belgeyi indirmek için ek yetki gerekiyor", "")
	case errors.Is(err, application.ErrHoldExists):
		problem(w, r, http.StatusConflict, "legal-holds/exists", "LEGAL_HOLD_EXISTS",
			"Bu hedef için etkin bir hukuki saklama zaten var", "")
	case errors.Is(err, application.ErrHoldReleased):
		problem(w, r, http.StatusConflict, "legal-holds/already-released", "LEGAL_HOLD_ALREADY_RELEASED",
			"Hukuki saklama zaten kaldırılmış", "")
	case errors.Is(err, application.ErrStoreUnavailable):
		// The one failure a caller can usefully retry, so it is not folded into a 500.
		h.logger.Error("document object store unavailable", "error", err)
		problem(w, r, http.StatusServiceUnavailable, "documents/store-unavailable",
			"DOCUMENT_STORE_UNAVAILABLE", "Belge deposuna ulaşılamıyor", "Kısa bir süre sonra yeniden deneyin.")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID",
			"Sayfa imleci geçersiz", "")
	default:
		// A document error carries no personal data: this module stores ids, codes, sizes
		// and statuses, and a filename never reaches an error.
		h.logger.Error("document command failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR",
			"Beklenmeyen hata", "")
	}
}

// pathUUID reads an id from the path; a malformed id is indistinguishable from an unknown
// one, so both are answered the same way.
func (h *Handler) pathUUID(w http.ResponseWriter, r *http.Request, name string, notFound error) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		h.writeError(w, r, notFound)
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
		problem(w, r, http.StatusPreconditionRequired, "generic/precondition-required", "IF_MATCH_REQUIRED",
			"If-Match başlığı gerekli", "GET yanıtındaki ETag değerini If-Match olarak gönderin.")
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

// decodeOptionalJSON accepts an empty body, which the download reason allows.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	// A response carrying a presigned URL is a response carrying a credential. Telling
	// every cache and proxy on the way not to keep it is one header and removes a whole
	// class of accident.
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
