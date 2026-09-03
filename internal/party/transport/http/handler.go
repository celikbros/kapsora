// Package partyhttp serves /api/v1/people and /api/v1/party/catalogs. Bodies use the
// generated contract types; errors are problem+json with stable codes (ADR-015). No
// identifier value ever appears in a URL, a log line or an error message.
package partyhttp

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
	"github.com/celikbros/kapsora/internal/party/application"
	"github.com/celikbros/kapsora/internal/party/domain"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the routes (migration 000008).
const (
	PermissionRead               = "member.read"
	PermissionManage             = "member.manage"
	PermissionIdentifierSearch   = "member.identifier.search"
	PermissionRelationshipManage = "member.relationship.manage"
	PermissionMembershipManage   = "membership.manage"
)

const maxBodyBytes = 64 << 10

const mergePatchContentType = "application/merge-patch+json"

// Denier writes and audits a failed permission check (identityhttp.Middleware.Deny).
type Denier interface {
	Deny(w http.ResponseWriter, r *http.Request, err error, permission string)
}

// Middlewares are the wrappers the integrator applies in cmd/api: idempotency on the
// three create commands and the per-actor limiter on the identifier search. A nil field
// means "no wrapper".
type Middlewares struct {
	CreatePerson       func(http.Handler) http.Handler
	CreateRelationship func(http.Handler) http.Handler
	CreateMembership   func(http.Handler) http.Handler
	Search             func(http.Handler) http.Handler
}

// Handler serves the person operations.
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

// Routes mounts everything below /people.
func (h *Handler) Routes(r chi.Router, mw Middlewares) {
	r.Get("/", h.List)
	r.With(wrap(mw.CreatePerson)).Post("/", h.Create)
	r.With(wrap(mw.Search)).Post("/search-by-identifier", h.Search)
	r.Get("/{personId}", h.Get)
	r.Patch("/{personId}", h.Update)
	r.Get("/{personId}/relationships", h.ListRelationships)
	r.With(wrap(mw.CreateRelationship)).Post("/{personId}/relationships", h.CreateRelationship)
	r.Post("/{personId}/relationships/{relationshipId}/end", h.EndRelationship)
	r.Get("/{personId}/memberships", h.ListMemberships)
	r.With(wrap(mw.CreateMembership)).Post("/{personId}/memberships", h.CreateMembership)
	r.Patch("/{personId}/memberships/{membershipId}", h.UpdateMembership)
}

// CatalogRoutes mounts /party/catalogs, which is not under a person.
func (h *Handler) CatalogRoutes(r chi.Router) { r.Get("/catalogs", h.Catalogs) }

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

// writeError maps application and domain errors to the problem codes of section 3.3.
// canSeeOwner decides whether a 409 may name the person already holding the identifier.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error, canSeeOwner bool) {
	var ve *domain.ValidationError
	var taken *application.IdentifierTakenError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, r, ve.Fields)
	case errors.As(err, &taken):
		detail := ""
		if canSeeOwner && taken.ExistingPersonID != uuid.Nil {
			detail = "Tanımlayıcı şu kişide kayıtlı: " + taken.ExistingPersonID.String()
		}
		problem(w, r, http.StatusConflict, "party/identifier-taken", "PERSON_IDENTIFIER_TAKEN",
			"Tanımlayıcı başka bir kişiye kayıtlı", detail)
	case errors.Is(err, application.ErrPersonNotFound):
		problem(w, r, http.StatusNotFound, "party/person-not-found", "PERSON_NOT_FOUND", "Kişi bulunamadı", "")
	case errors.Is(err, application.ErrNotFound):
		problem(w, r, http.StatusNotFound, "generic/not-found", "RESOURCE_NOT_FOUND", "Kaynak bulunamadı", "")
	case errors.Is(err, application.ErrRelationshipOverlap):
		problem(w, r, http.StatusConflict, "party/relationship-overlap", "RELATIONSHIP_OVERLAP",
			"Bu ilişki için çakışan bir dönem var", "")
	case errors.Is(err, application.ErrMembershipOverlap):
		problem(w, r, http.StatusConflict, "party/membership-overlap", "MEMBERSHIP_OVERLAP",
			"Bu üyelik için çakışan bir dönem var", "")
	case errors.Is(err, application.ErrMemberNoTaken):
		problem(w, r, http.StatusConflict, "party/member-no-taken", "MEMBER_NO_TAKEN",
			"Bu üye numarası sponsorda zaten kullanılıyor", "")
	case errors.Is(err, application.ErrVersionMismatch):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH",
			"Kayıt bu arada değişti", "Güncel sürümü alıp değişikliğinizi yeniden uygulayın.")
	case errors.Is(err, httpx.ErrInvalidCursor):
		problem(w, r, http.StatusBadRequest, "generic/cursor-invalid", "CURSOR_INVALID", "Sayfa imleci geçersiz", "")
	default:
		h.logger.Error("party request failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR", "Beklenmeyen hata", "")
	}
}

// pathUUID reads a path parameter; a malformed id is indistinguishable from an unknown one.
func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		problem(w, r, http.StatusNotFound, "party/person-not-found", "PERSON_NOT_FOUND", "Kişi bulunamadı", "")
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
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY", "İstek gövdesi geçersiz", "")
		return false
	}
	return true
}

// isJSONNull recognises an explicit null (merge-patch: "remove this field").
func isJSONNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
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
	httpx.WriteProblem(w, r, httpx.Problem{Type: httpx.ProblemTypeBase + typ, Title: title, Status: status, Code: code, Detail: detail})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// dateOnly strips the clock from a contract date so comparisons stay day-based.
func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
