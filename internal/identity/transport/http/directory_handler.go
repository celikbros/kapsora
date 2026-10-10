package identityhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

type DirectoryHandler struct {
	svc        *application.DirectoryService
	middleware *Middleware
	logger     *slog.Logger
}

func NewDirectoryHandler(svc *application.DirectoryService, middleware *Middleware, logger *slog.Logger) *DirectoryHandler {
	return &DirectoryHandler{svc: svc, middleware: middleware, logger: logger}
}

func (h *DirectoryHandler) Routes(r chi.Router, suspendMiddleware func(http.Handler) http.Handler) {
	r.Get("/", h.List)
	r.Get("/{membershipId}", h.Get)
	r.With(h.SuspendAuthorization, suspendMiddleware).Post("/{membershipId}/suspend", h.Suspend)
}

// SuspendAuthorization runs outside idempotency so even stored replays require a
// currently valid tenant management grant and fresh password step-up.
func (h *DirectoryHandler) SuspendAuthorization(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc, err := identity.RequireStepUp(r.Context(), "identity.user.manage")
		if err == nil {
			err = h.svc.AuthorizeManage(r.Context(), rc)
		}
		if err != nil {
			if errors.Is(err, identity.ErrPermissionDenied) || errors.Is(err, identity.ErrStepUpRequired) || errors.Is(err, identity.ErrUnauthenticated) {
				h.middleware.Deny(w, r, err, "identity.user.manage")
			} else {
				h.logger.Error("identity directory authorization failed", "error", err)
				problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR", "Beklenmeyen hata", "")
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *DirectoryHandler) require(w http.ResponseWriter, r *http.Request) (identity.RequestContext, bool) {
	rc, err := identity.Require(r.Context(), "identity.user.read")
	if err != nil {
		h.middleware.Deny(w, r, err, "identity.user.read")
		return identity.RequestContext{}, false
	}
	return rc, true
}

type membershipJSON struct {
	ID               string  `json:"id"`
	DisplayName      string  `json:"displayName"`
	ActorType        string  `json:"actorType"`
	ActorStatus      string  `json:"actorStatus"`
	MembershipStatus string  `json:"membershipStatus"`
	ValidFrom        *string `json:"validFrom"`
	ValidTo          *string `json:"validTo"`
	ValidityEmpty    bool    `json:"validityEmpty"`
	RowVersion       int64   `json:"rowVersion"`
}

type roleJSON struct {
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	System        bool    `json:"isSystemRole"`
	ScopeType     string  `json:"scopeType"`
	ValidFrom     *string `json:"validFrom"`
	ValidTo       *string `json:"validTo"`
	ValidityEmpty bool    `json:"validityEmpty"`
}

func membershipBody(m application.DirectoryMembership) membershipJSON {
	return membershipJSON{ID: m.ID.String(), DisplayName: m.DisplayName, ActorType: m.ActorType,
		ActorStatus: m.ActorStatus, MembershipStatus: m.MembershipStatus,
		ValidFrom: m.ValidFrom, ValidTo: m.ValidTo, ValidityEmpty: m.ValidityEmpty, RowVersion: m.RowVersion}
}

func (h *DirectoryHandler) List(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "PENDING", "ACTIVE", "SUSPENDED", "REVOKED":
	default:
		h.badQuery(w, r)
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 {
			h.badQuery(w, r)
			return
		}
	}
	page, err := h.svc.List(r.Context(), rc, status, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]membershipJSON, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, membershipBody(item))
	}
	var next *string
	if page.NextCursor != "" {
		next = &page.NextCursor
	}
	writeJSON(w, struct {
		Items      []membershipJSON `json:"items"`
		NextCursor *string          `json:"nextCursor"`
	}{items, next})
}

func (h *DirectoryHandler) Get(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "membershipId"))
	if err != nil {
		h.notFound(w, r)
		return
	}
	detail, err := h.svc.Get(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", directoryETag(detail.Membership.RowVersion))
	h.writeDetail(w, detail)
}

func (h *DirectoryHandler) writeDetail(w http.ResponseWriter, detail application.DirectoryDetail) {
	roles := make([]roleJSON, 0, len(detail.AssignedRoles))
	for _, role := range detail.AssignedRoles {
		roles = append(roles, roleJSON{Code: role.Code, Name: role.Name, System: role.System,
			ScopeType: role.ScopeType, ValidFrom: role.ValidFrom, ValidTo: role.ValidTo,
			ValidityEmpty: role.ValidityEmpty})
	}
	writeJSON(w, struct {
		Membership    membershipJSON `json:"membership"`
		AssignedRoles []roleJSON     `json:"assignedRoles"`
	}{membershipBody(detail.Membership), roles})
}

func directoryETag(version int64) string { return fmt.Sprintf(`"%d"`, version) }

func (h *DirectoryHandler) Suspend(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.RequireStepUp(r.Context(), "identity.user.manage")
	if err != nil {
		h.middleware.Deny(w, r, err, "identity.user.manage")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "membershipId"))
	if err != nil {
		h.notFound(w, r)
		return
	}
	rawVersion := strings.TrimSpace(r.Header.Get("If-Match"))
	version, err := strconv.ParseInt(strings.Trim(rawVersion, `"`), 10, 64)
	if err != nil || version < 1 || rawVersion != directoryETag(version) {
		problem(w, r, http.StatusPreconditionRequired, "generic/precondition-required", "IF_MATCH_REQUIRED", "If-Match başlığı gerekli", "")
		return
	}
	var body struct {
		ReasonCode string `json:"reasonCode"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY", "Geçersiz istek gövdesi", "")
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY", "Geçersiz istek gövdesi", "")
		return
	}
	detail, err := h.svc.Suspend(r.Context(), rc, id, version, body.ReasonCode)
	if err != nil {
		h.writeSuspendError(w, r, err)
		return
	}
	w.Header().Set("ETag", directoryETag(detail.Membership.RowVersion))
	h.writeDetail(w, detail)
}

func (h *DirectoryHandler) writeSuspendError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied):
		h.middleware.Deny(w, r, err, "identity.user.manage")
	case errors.Is(err, application.ErrMembershipNotFound):
		h.notFound(w, r)
	case errors.Is(err, application.ErrDirectoryReasonInvalid):
		problem(w, r, http.StatusBadRequest, "identity/suspension-reason-invalid", "SUSPENSION_REASON_INVALID", "Geçersiz askıya alma nedeni", "")
	case errors.Is(err, application.ErrDirectorySelfSuspension):
		problem(w, r, http.StatusConflict, "identity/self-suspension-forbidden", "SELF_SUSPENSION_FORBIDDEN", "Kendi üyeliğiniz askıya alınamaz", "")
	case errors.Is(err, application.ErrDirectoryLastManager):
		problem(w, r, http.StatusConflict, "identity/last-tenant-manager", "LAST_TENANT_MANAGER", "Son yönetici askıya alınamaz", "")
	case errors.Is(err, application.ErrLastTenantRoleManager):
		problem(w, r, http.StatusConflict, "identity/last-tenant-role-manager", "LAST_TENANT_ROLE_MANAGER", "Son rol yöneticisi askıya alınamaz", "")
	case errors.Is(err, application.ErrDirectoryStateConflict):
		problem(w, r, http.StatusConflict, "identity/membership-state-conflict", "MEMBERSHIP_STATE_CONFLICT", "Üyelik etkin değil", "")
	case errors.Is(err, application.ErrDirectoryVersionConflict):
		problem(w, r, http.StatusPreconditionFailed, "generic/etag-mismatch", "ETAG_MISMATCH", "Üyelik değişti", "")
	default:
		h.logger.Error("identity directory suspension failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR", "Beklenmeyen hata", "")
	}
}

func (h *DirectoryHandler) badQuery(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, httpx.Problem{Type: httpx.ProblemTypeBase + "identity/directory-query-invalid",
		Title: "Geçersiz liste isteği", Status: http.StatusBadRequest, Code: "DIRECTORY_QUERY_INVALID"})
}

func (h *DirectoryHandler) notFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, httpx.Problem{Type: httpx.ProblemTypeBase + "identity/membership-not-found",
		Title: "Üyelik bulunamadı", Status: http.StatusNotFound, Code: "MEMBERSHIP_NOT_FOUND"})
}

func (h *DirectoryHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied):
		h.middleware.Deny(w, r, err, "identity.user.read")
	case errors.Is(err, application.ErrMembershipNotFound):
		h.notFound(w, r)
	case errors.Is(err, httpx.ErrInvalidCursor):
		h.badQuery(w, r)
	default:
		h.logger.Error("identity directory read failed", "error", err)
		problem(w, r, http.StatusInternalServerError, "generic/internal-error", "INTERNAL_ERROR", "Beklenmeyen hata", "")
	}
}
