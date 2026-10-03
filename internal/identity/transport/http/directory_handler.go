package identityhttp

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

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

func (h *DirectoryHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Get("/{membershipId}", h.Get)
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
		ValidFrom: m.ValidFrom, ValidTo: m.ValidTo, ValidityEmpty: m.ValidityEmpty}
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
