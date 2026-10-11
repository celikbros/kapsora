package identityhttp

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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

type RoleAssignmentHandler struct {
	svc        *application.RoleAssignmentService
	middleware *Middleware
	logger     *slog.Logger
}

func NewRoleAssignmentHandler(svc *application.RoleAssignmentService, middleware *Middleware, logger *slog.Logger) *RoleAssignmentHandler {
	return &RoleAssignmentHandler{svc: svc, middleware: middleware, logger: logger}
}

func roleAssignmentNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, req)
	})
}

func (h *RoleAssignmentHandler) CatalogRoutes(r chi.Router) {
	r.With(roleAssignmentNoStore).Get("/role-assignment-options", h.Options)
	r.With(roleAssignmentNoStore).Get("/role-assignment-organizations", h.Organizations)
}

func (h *RoleAssignmentHandler) UserRoutes(r chi.Router, assign, revoke func(http.Handler) http.Handler) {
	r.With(roleAssignmentNoStore).Get("/{membershipId}/role-grants", h.Grants)
	r.With(roleAssignmentNoStore, h.CommandAuthorization, assign).Post("/{membershipId}/role-grants", h.Assign)
	r.With(roleAssignmentNoStore, h.CommandAuthorization, revoke).Post("/{membershipId}/role-grants/{grantId}/revoke", h.Revoke)
}

// This gate precedes idempotency replay. The repository repeats it after locks.
func (h *RoleAssignmentHandler) CommandAuthorization(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc, err := identity.RequireStepUp(r.Context(), "identity.role.manage")
		if err == nil {
			err = h.svc.AuthorizeCommand(r.Context(), rc)
		}
		if err != nil {
			h.writeError(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *RoleAssignmentHandler) require(w http.ResponseWriter, r *http.Request) (identity.RequestContext, bool) {
	rc, err := identity.Require(r.Context(), "identity.role.manage")
	if err == nil {
		err = h.svc.Authorize(r.Context(), rc)
	}
	if err != nil {
		h.writeError(w, r, err)
		return identity.RequestContext{}, false
	}
	return rc, true
}

type roleOptionJSON struct {
	Code                    string   `json:"code"`
	Name                    string   `json:"name"`
	Description             string   `json:"description"`
	ScopeType               string   `json:"scopeType"`
	PermissionCodes         []string `json:"permissionCodes"`
	HasSensitivePermissions bool     `json:"hasSensitivePermissions"`
}

type roleOrganizationJSON struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"displayName"`
	TenantCode  *string `json:"tenantCode"`
}

type roleGrantJSON struct {
	ID                         string  `json:"id"`
	RoleCode                   string  `json:"roleCode"`
	RoleName                   string  `json:"roleName"`
	IsSystemRole               bool    `json:"isSystemRole"`
	ScopeType                  string  `json:"scopeType"`
	OrganizationRelationshipID *string `json:"organizationRelationshipId"`
	OrganizationDisplayName    *string `json:"organizationDisplayName"`
	ValidFrom                  *string `json:"validFrom"`
	ValidTo                    *string `json:"validTo"`
	ValidityEmpty              bool    `json:"validityEmpty"`
	CanRevoke                  bool    `json:"canRevoke"`
	RevocationRefusalCode      *string `json:"revocationRefusalCode"`
}

func grantBody(g application.TenantRoleGrant) roleGrantJSON {
	var orgID *string
	if g.OrganizationRelationshipID != nil {
		id := g.OrganizationRelationshipID.String()
		orgID = &id
	}
	return roleGrantJSON{ID: g.ID.String(), RoleCode: g.RoleCode, RoleName: g.RoleName, IsSystemRole: g.IsSystemRole,
		ScopeType: g.ScopeType, OrganizationRelationshipID: orgID, OrganizationDisplayName: g.OrganizationDisplayName,
		ValidFrom: g.ValidFrom, ValidTo: g.ValidTo, ValidityEmpty: g.ValidityEmpty,
		CanRevoke: g.CanRevoke, RevocationRefusalCode: g.RevocationRefusalCode}
}

func (h *RoleAssignmentHandler) Options(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	options, err := h.svc.Options(r.Context(), rc)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]roleOptionJSON, 0, len(options))
	for _, o := range options {
		items = append(items, roleOptionJSON{Code: o.Code, Name: o.Name,
			Description: o.Description, ScopeType: o.ScopeType, PermissionCodes: o.PermissionCodes,
			HasSensitivePermissions: o.HasSensitivePermissions})
	}
	writeJSON(w, struct {
		Items []roleOptionJSON `json:"items"`
	}{items})
}

func (h *RoleAssignmentHandler) pageParams(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 {
			h.badQuery(w, r)
			return "", 0, false
		}
	}
	return r.URL.Query().Get("cursor"), limit, true
}

func (h *RoleAssignmentHandler) Organizations(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	cursor, limit, ok := h.pageParams(w, r)
	if !ok {
		return
	}
	page, err := h.svc.Organizations(r.Context(), rc, cursor, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]roleOrganizationJSON, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, roleOrganizationJSON{ID: item.ID.String(), DisplayName: item.DisplayName, TenantCode: item.TenantCode})
	}
	var next *string
	if page.NextCursor != "" {
		next = &page.NextCursor
	}
	writeJSON(w, struct {
		Items      []roleOrganizationJSON `json:"items"`
		NextCursor *string                `json:"nextCursor"`
	}{items, next})
}

func (h *RoleAssignmentHandler) Grants(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "membershipId"))
	if err != nil {
		h.notFound(w, r)
		return
	}
	cursor, limit, ok := h.pageParams(w, r)
	if !ok {
		return
	}
	page, err := h.svc.Grants(r.Context(), rc, id, cursor, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]roleGrantJSON, 0, len(page.Items))
	for _, g := range page.Items {
		items = append(items, grantBody(g))
	}
	var next *string
	if page.NextCursor != "" {
		next = &page.NextCursor
	}
	w.Header().Set("ETag", directoryETag(page.MembershipRowVersion))
	writeJSON(w, struct {
		MembershipID          string          `json:"membershipId"`
		MembershipRowVersion  int64           `json:"membershipRowVersion"`
		CanAssign             bool            `json:"canAssign"`
		AssignmentRefusalCode *string         `json:"assignmentRefusalCode"`
		Items                 []roleGrantJSON `json:"items"`
		NextCursor            *string         `json:"nextCursor"`
	}{id.String(), page.MembershipRowVersion, page.CanAssign, page.AssignmentRefusalCode, items, next})
}

func (h *RoleAssignmentHandler) commandContext(w http.ResponseWriter, r *http.Request) (identity.RequestContext, uuid.UUID, int64, bool) {
	rc, err := identity.RequireStepUp(r.Context(), "identity.role.manage")
	if err != nil {
		h.writeError(w, r, err)
		return identity.RequestContext{}, uuid.Nil, 0, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "membershipId"))
	if err != nil {
		h.notFound(w, r)
		return identity.RequestContext{}, uuid.Nil, 0, false
	}
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	version, err := strconv.ParseInt(strings.Trim(raw, `"`), 10, 64)
	if err != nil || version < 1 || raw != fmt.Sprintf(`"%d"`, version) {
		problem(w, r, http.StatusPreconditionRequired, "generic/precondition-required", "IF_MATCH_REQUIRED", "If-Match başlığı gerekli", "")
		return identity.RequestContext{}, uuid.Nil, 0, false
	}
	return rc, id, version, true
}

func (h *RoleAssignmentHandler) decodeCommand(w http.ResponseWriter, r *http.Request, dst any) bool {
	if strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])) != "application/json" {
		problem(w, r, http.StatusUnsupportedMediaType, "generic/unsupported-media-type", "UNSUPPORTED_MEDIA_TYPE", "İstek biçimi desteklenmiyor", "")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		h.invalidBody(w, r)
		return false
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		h.invalidBody(w, r)
		return false
	}
	inner := json.NewDecoder(bytes.NewReader(raw))
	inner.DisallowUnknownFields()
	if err := inner.Decode(dst); err != nil {
		h.invalidBody(w, r)
		return false
	}
	return true
}

func (h *RoleAssignmentHandler) Assign(w http.ResponseWriter, r *http.Request) {
	rc, membershipID, version, ok := h.commandContext(w, r)
	if !ok {
		return
	}
	var body struct {
		RoleCode                   string          `json:"roleCode"`
		ScopeType                  string          `json:"scopeType"`
		OrganizationRelationshipID json.RawMessage `json:"organizationRelationshipId"`
		ReasonCode                 string          `json:"reasonCode"`
	}
	if !h.decodeCommand(w, r, &body) {
		return
	}
	if body.RoleCode == "" || body.ScopeType == "" || body.ReasonCode == "" {
		h.invalidBody(w, r)
		return
	}
	in := application.AssignRoleGrantInput{RoleCode: body.RoleCode, ScopeType: body.ScopeType, ReasonCode: body.ReasonCode}
	if len(body.OrganizationRelationshipID) > 0 {
		var rawID string
		if err := json.Unmarshal(body.OrganizationRelationshipID, &rawID); err != nil || rawID == "" {
			h.invalidBody(w, r)
			return
		}
		id, err := uuid.Parse(rawID)
		if err != nil {
			h.invalidBody(w, r)
			return
		}
		in.OrganizationRelationshipID = uuid.NullUUID{UUID: id, Valid: true}
	}
	result, err := h.svc.Assign(r.Context(), rc, membershipID, version, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeResult(w, result)
}

func (h *RoleAssignmentHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	rc, membershipID, version, ok := h.commandContext(w, r)
	if !ok {
		return
	}
	grantID, err := uuid.Parse(chi.URLParam(r, "grantId"))
	if err != nil {
		h.notFound(w, r)
		return
	}
	var body struct {
		ReasonCode string `json:"reasonCode"`
	}
	if !h.decodeCommand(w, r, &body) {
		return
	}
	if body.ReasonCode == "" {
		h.invalidBody(w, r)
		return
	}
	result, err := h.svc.Revoke(r.Context(), rc, membershipID, grantID, version, body.ReasonCode)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeResult(w, result)
}

func (h *RoleAssignmentHandler) writeResult(w http.ResponseWriter, result application.RoleGrantResult) {
	w.Header().Set("ETag", directoryETag(result.MembershipRowVersion))
	writeJSON(w, struct {
		MembershipID         string        `json:"membershipId"`
		MembershipRowVersion int64         `json:"membershipRowVersion"`
		Grant                roleGrantJSON `json:"grant"`
	}{result.MembershipID.String(), result.MembershipRowVersion, grantBody(result.Grant)})
}

func (h *RoleAssignmentHandler) badQuery(w http.ResponseWriter, r *http.Request) {
	problem(w, r, http.StatusBadRequest, "identity/role-assignment-query-invalid", "ROLE_ASSIGNMENT_QUERY_INVALID", "Geçersiz liste isteği", "")
}
func (h *RoleAssignmentHandler) invalidBody(w http.ResponseWriter, r *http.Request) {
	problem(w, r, http.StatusBadRequest, "generic/invalid-request-body", "INVALID_REQUEST_BODY", "Geçersiz istek gövdesi", "")
}
func (h *RoleAssignmentHandler) notFound(w http.ResponseWriter, r *http.Request) {
	problem(w, r, http.StatusNotFound, "generic/not-found", "RESOURCE_NOT_FOUND", "Kaynak bulunamadı", "")
}

func (h *RoleAssignmentHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied), errors.Is(err, identity.ErrUnauthenticated), errors.Is(err, identity.ErrStepUpRequired):
		h.middleware.Deny(w, r, err, "identity.role.manage")
	case errors.Is(err, application.ErrRoleAssignmentNotFound):
		h.notFound(w, r)
	case errors.Is(err, httpx.ErrInvalidCursor):
		h.badQuery(w, r)
	case errors.Is(err, application.ErrRoleAssignmentInvalid):
		h.invalidBody(w, r)
	case errors.Is(err, application.ErrRoleAssignmentSelf):
		problem(w, r, 409, "identity/self-role-change-forbidden", "SELF_ROLE_CHANGE_FORBIDDEN", "Kendi rolünüz değiştirilemez", "")
	case errors.Is(err, application.ErrRoleConfigurationUnsupported):
		problem(w, r, 409, "identity/role-configuration-unsupported", "ROLE_CONFIGURATION_UNSUPPORTED", "Rol yapılandırması desteklenmiyor", "")
	case errors.Is(err, application.ErrRoleAssignmentUnsupported):
		problem(w, r, 409, "identity/role-assignment-unsupported", "ROLE_ASSIGNMENT_UNSUPPORTED", "Rol ataması desteklenmiyor", "")
	case errors.Is(err, application.ErrRoleMembershipConflict):
		problem(w, r, 409, "identity/membership-state-conflict", "MEMBERSHIP_STATE_CONFLICT", "Üyelik etkin değil", "")
	case errors.Is(err, application.ErrExistingAccessConflict):
		problem(w, r, 409, "identity/existing-access-conflict", "EXISTING_ACCESS_CONFLICT", "Mevcut erişimle çakışıyor", "")
	case errors.Is(err, application.ErrRoleGrantConflict):
		problem(w, r, 409, "identity/grant-state-conflict", "GRANT_STATE_CONFLICT", "Rol erişimi güncel değil", "")
	case errors.Is(err, application.ErrDirectoryLastManager):
		problem(w, r, 409, "identity/last-tenant-manager", "LAST_TENANT_MANAGER", "Son yönetici kaldırılamaz", "")
	case errors.Is(err, application.ErrLastTenantRoleManager):
		problem(w, r, 409, "identity/last-tenant-role-manager", "LAST_TENANT_ROLE_MANAGER", "Son rol yöneticisi kaldırılamaz", "")
	case errors.Is(err, application.ErrRoleAssignmentVersion):
		problem(w, r, 412, "generic/etag-mismatch", "ETAG_MISMATCH", "Üyelik değişti", "")
	default:
		h.logger.Error("identity role assignment failed", "error", err)
		problem(w, r, 500, "generic/internal-error", "INTERNAL_ERROR", "Beklenmeyen hata", "")
	}
}
