package identityhttp

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
)

// ContextHandler serves /api/v1/me, /api/v1/tenants and /api/v1/session/switch-tenant.
type ContextHandler struct {
	svc    *application.Service
	authz  *application.Authorizer
	logger *slog.Logger
}

// NewContextHandler wires the handler.
func NewContextHandler(svc *application.Service, authz *application.Authorizer, logger *slog.Logger) *ContextHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ContextHandler{svc: svc, authz: authz, logger: logger}
}

// JSON shapes follow the OpenAPI schemas UserContext, TenantContext and TenantSummary.
type tenantSummaryJSON struct {
	ID              string `json:"id"`
	Code            string `json:"code"`
	DisplayName     string `json:"displayName"`
	Status          string `json:"status"`
	DefaultLocale   string `json:"defaultLocale,omitempty"`
	DefaultTimeZone string `json:"defaultTimeZone,omitempty"`
}

type scopeJSON struct {
	Type string  `json:"type"`
	ID   *string `json:"id"`
}

type tenantContextJSON struct {
	Tenant      tenantSummaryJSON `json:"tenant"`
	Permissions []string          `json:"permissions"`
	Scopes      []scopeJSON       `json:"scopes"`
}

type userContextJSON struct {
	ActorID     string              `json:"actorId"`
	DisplayName string              `json:"displayName"`
	Email       string              `json:"email,omitempty"`
	Tenants     []tenantContextJSON `json:"tenants"`
}

// GetMe returns the actor and one context per active membership.
func (h *ContextHandler) GetMe(w http.ResponseWriter, r *http.Request) {
	session, ok := identity.SessionFromContext(r.Context())
	if !ok {
		WriteAuthError(w, r, identity.ErrUnauthenticated, h.logger)
		return
	}
	view, err := h.svc.Describe(r.Context(), session.ID)
	if err != nil {
		WriteAuthError(w, r, err, h.logger)
		return
	}
	contexts, err := h.authz.TenantContexts(r.Context(), session.ActorID)
	if err != nil {
		WriteAuthError(w, r, err, h.logger)
		return
	}
	body := userContextJSON{
		ActorID:     session.ActorID.String(),
		DisplayName: view.DisplayName,
		Email:       view.Email,
		Tenants:     make([]tenantContextJSON, 0, len(contexts)),
	}
	for _, c := range contexts {
		body.Tenants = append(body.Tenants, tenantContextBody(c))
	}
	writeJSON(w, body)
}

// ListTenants returns the tenants the actor may switch to.
func (h *ContextHandler) ListTenants(w http.ResponseWriter, r *http.Request) {
	session, ok := identity.SessionFromContext(r.Context())
	if !ok {
		WriteAuthError(w, r, identity.ErrUnauthenticated, h.logger)
		return
	}
	tenants, err := h.authz.ListTenants(r.Context(), session.ActorID)
	if err != nil {
		WriteAuthError(w, r, err, h.logger)
		return
	}
	items := make([]tenantSummaryJSON, 0, len(tenants))
	for _, t := range tenants {
		items = append(items, tenantSummaryBody(t))
	}
	writeJSON(w, map[string]any{"items": items})
}

type switchTenantRequest struct {
	TenantID string `json:"tenantId"`
}

// SwitchTenant makes the given tenant the session's active tenant.
func (h *ContextHandler) SwitchTenant(w http.ResponseWriter, r *http.Request) {
	var in switchTenantRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	session, ok := identity.SessionFromContext(r.Context())
	if !ok {
		WriteAuthError(w, r, identity.ErrUnauthenticated, h.logger)
		return
	}
	tenantID, err := uuid.Parse(in.TenantID)
	if err != nil {
		// An unparseable id is treated like an unknown tenant: no membership.
		WriteAuthError(w, r, application.ErrNoMembership, h.logger)
		return
	}
	tc, err := h.authz.SwitchTenant(r.Context(), session, tenantID)
	if err != nil {
		WriteAuthError(w, r, err, h.logger)
		return
	}
	writeJSON(w, tenantContextBody(tc))
}

func tenantSummaryBody(t application.TenantSummary) tenantSummaryJSON {
	return tenantSummaryJSON{
		ID: t.ID.String(), Code: t.Code, DisplayName: t.DisplayName, Status: t.Status,
		DefaultLocale: t.DefaultLocale, DefaultTimeZone: t.DefaultTimeZone,
	}
}

func tenantContextBody(c application.TenantContext) tenantContextJSON {
	out := tenantContextJSON{
		Tenant:      tenantSummaryBody(c.Membership.Tenant),
		Permissions: append([]string{}, c.Permissions...),
		Scopes:      make([]scopeJSON, 0, len(c.Scopes)),
	}
	for _, s := range c.Scopes {
		var id *string
		if s.ID.Valid {
			v := s.ID.UUID.String()
			id = &v
		}
		out.Scopes = append(out.Scopes, scopeJSON{Type: s.Type, ID: id})
	}
	return out
}
