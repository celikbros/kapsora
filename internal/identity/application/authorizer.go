package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
)

// TenantSummary is the tenant as shown to its members.
type TenantSummary struct {
	ID              uuid.UUID
	Code            string
	DisplayName     string
	Status          string
	DefaultLocale   string
	DefaultTimeZone string
}

// Membership is an actor's active membership in a tenant.
type Membership struct {
	ID     uuid.UUID
	Tenant TenantSummary
}

// Grant is one access grant valid now: the scope it carries and the permissions of its role.
// A tenant-wide grant has the scope type TENANT and no id.
type Grant struct {
	Scope       identity.Scope
	Permissions []string
}

// Grants are the access grants a membership currently holds, kept apart rather than merged,
// because which of them apply depends on the app a request comes from (identity.App).
type Grants struct {
	Items []Grant
}

// For is what the grants give a request from app: the union of the permissions of the grants
// that belong to it, and the narrowing scopes those grants carry. identity.AppAny takes every
// grant, which is what an account had before the apps were told apart.
//
// It is the one place the choice is made, so the request context, /me and switch-tenant
// cannot disagree about what an app may do.
func (g Grants) For(app identity.App) ([]string, []identity.Scope) {
	perms := map[string]struct{}{}
	seen := map[identity.Scope]struct{}{}
	scopes := []identity.Scope{}
	for _, it := range g.Items {
		if app != identity.AppAny && identity.AppOfScope(it.Scope.Type) != app {
			continue
		}
		for _, p := range it.Permissions {
			perms[p] = struct{}{}
		}
		if it.Scope.Type == ScopeTenant {
			continue
		}
		if _, dup := seen[it.Scope]; dup {
			continue
		}
		seen[it.Scope] = struct{}{}
		scopes = append(scopes, it.Scope)
	}
	permissions := make([]string, 0, len(perms))
	for p := range perms {
		permissions = append(permissions, p)
	}
	sort.Strings(permissions)
	sort.Slice(scopes, func(i, j int) bool {
		if scopes[i].Type != scopes[j].Type {
			return scopes[i].Type < scopes[j].Type
		}
		return scopes[i].ID.UUID.String() < scopes[j].ID.UUID.String()
	})
	return permissions, scopes
}

// Apps are the apps the account has work in: each app one of its grants belongs to, provided
// that grant carries a permission. They are listed whatever app is asking, in the fixed order
// of identity.Apps.
func (g Grants) Apps() []identity.App {
	out := []identity.App{}
	for _, app := range identity.Apps {
		for _, it := range g.Items {
			if identity.AppOfScope(it.Scope.Type) == app && len(it.Permissions) > 0 {
				out = append(out, app)
				break
			}
		}
	}
	return out
}

// SelfPerson is the person this account is in the tenant, from its PERSON grant, whichever
// app is asking. It is what lets a reviewer's own file be recognised as theirs while the
// reviewer is working as staff and acting for nobody.
func (g Grants) SelfPerson() uuid.NullUUID {
	scopes := make([]identity.Scope, 0, len(g.Items))
	for _, it := range g.Items {
		scopes = append(scopes, it.Scope)
	}
	return identity.PersonFromScopes(scopes)
}

// TenantContext is what the frontend needs to render one tenant for the user.
type TenantContext struct {
	Membership  Membership
	Permissions []string
	Scopes      []identity.Scope
	// PersonID is the person this account acts for in this tenant, resolved from its
	// PERSON scope (migration 000039). It is null for every actor that is not a member, and
	// for a member account asked about by any app but the member app. The frontend reads it
	// to know a member client is bound; the server never trusts it back and resolves the
	// person from the grant on every call.
	PersonID uuid.NullUUID
	// Apps are the apps the account has work in here, whichever app asked.
	Apps []identity.App
	// SelfPersonID is the person the account is here, even while it acts as staff.
	SelfPersonID uuid.NullUUID
}

// tenantContext builds the view, resolving the person once so /me, switch-tenant and the
// request context cannot disagree about who the caller acts for.
func tenantContext(m Membership, g Grants, app identity.App) TenantContext {
	perms, scopes := g.For(app)
	return TenantContext{
		Membership:   m,
		Permissions:  perms,
		Scopes:       scopes,
		PersonID:     identity.PersonFromScopes(scopes),
		Apps:         g.Apps(),
		SelfPersonID: g.SelfPerson(),
	}
}

// AuthorizationRepository reads memberships and grants. Membership lookups run in an
// actor-bound transaction (an actor may see only its own memberships); grant resolution
// runs in the tenant's transaction.
type AuthorizationRepository interface {
	FindActiveMembership(ctx context.Context, actorID, tenantID uuid.UUID) (Membership, error)
	ListMemberships(ctx context.Context, actorID uuid.UUID) ([]Membership, error)
	ResolveGrants(ctx context.Context, tenantID, membershipID uuid.UUID) (Grants, error)
}

// Errors raised while resolving a tenant context. They are deliberately silent about
// whether the tenant exists.
var (
	ErrNoMembership   = errors.New("identity: no active membership in this tenant")
	ErrTenantMismatch = errors.New("identity: requested tenant is not the session's active tenant")
)

// Authorizer turns a session into an authorized identity.RequestContext.
type Authorizer struct {
	repo     AuthorizationRepository
	sessions identity.SessionStore
	audit    AuditSink
	now      func() time.Time
}

// NewAuthorizer wires the authorizer; audit and now may be nil.
func NewAuthorizer(repo AuthorizationRepository, sessions identity.SessionStore, sink AuditSink, now func() time.Time) *Authorizer {
	if sink == nil {
		sink = func(context.Context, audit.Event) error { return nil }
	}
	if now == nil {
		now = time.Now
	}
	return &Authorizer{repo: repo, sessions: sessions, audit: sink, now: now}
}

// ResolveTenantContext builds the request context for a tenant-scoped request from app. The
// requested tenant must be the session's active tenant and the actor must hold an active
// membership in it; the header alone is never trusted (v1.2 section 14.4). Only the grants
// that belong to app apply (identity.AppAny: all of them).
func (a *Authorizer) ResolveTenantContext(ctx context.Context, session identity.Session, requested uuid.UUID, app identity.App, requestID string) (identity.RequestContext, error) {
	if requested == uuid.Nil || !session.ActiveTenantID.Valid || session.ActiveTenantID.UUID != requested {
		return identity.RequestContext{}, ErrTenantMismatch
	}
	membership, err := a.repo.FindActiveMembership(ctx, session.ActorID, requested)
	if err != nil {
		return identity.RequestContext{}, err
	}
	grants, err := a.repo.ResolveGrants(ctx, membership.Tenant.ID, membership.ID)
	if err != nil {
		return identity.RequestContext{}, err
	}
	return a.requestContext(session, membership, grants, app, requestID), nil
}

func (a *Authorizer) requestContext(session identity.Session, m Membership, g Grants, app identity.App, requestID string) identity.RequestContext {
	permissions, scopes := g.For(app)
	perms := make(map[string]struct{}, len(permissions))
	for _, p := range permissions {
		perms[p] = struct{}{}
	}
	return identity.RequestContext{
		RequestID:    requestID,
		Principal:    identity.Principal{ActorID: session.ActorID, ActorType: identity.ActorHuman},
		SessionID:    session.ID,
		ClientType:   identity.ClientBrowser,
		TenantID:     m.Tenant.ID,
		MembershipID: m.ID,
		App:          app,
		PersonID:     identity.PersonFromScopes(scopes),
		SelfPersonID: g.SelfPerson(),
		Permissions:  perms,
		Scopes:       scopes,
		Locale:       m.Tenant.DefaultLocale,
		TimeZone:     m.Tenant.DefaultTimeZone,
		StepUpValid:  !session.StepUpUntil.IsZero() && a.now().Before(session.StepUpUntil),
	}
}

// TenantContexts lists every tenant the actor may work in, with what app may do there (/me).
func (a *Authorizer) TenantContexts(ctx context.Context, actorID uuid.UUID, app identity.App) ([]TenantContext, error) {
	memberships, err := a.repo.ListMemberships(ctx, actorID)
	if err != nil {
		return nil, err
	}
	out := make([]TenantContext, 0, len(memberships))
	for _, m := range memberships {
		grants, err := a.repo.ResolveGrants(ctx, m.Tenant.ID, m.ID)
		if err != nil {
			return nil, fmt.Errorf("identity: resolve grants in %s: %w", m.Tenant.Code, err)
		}
		out = append(out, tenantContext(m, grants, app))
	}
	return out, nil
}

// ListTenants returns the tenants of the actor's active memberships (/tenants).
func (a *Authorizer) ListTenants(ctx context.Context, actorID uuid.UUID) ([]TenantSummary, error) {
	memberships, err := a.repo.ListMemberships(ctx, actorID)
	if err != nil {
		return nil, err
	}
	out := make([]TenantSummary, 0, len(memberships))
	for _, m := range memberships {
		out = append(out, m.Tenant)
	}
	return out, nil
}

// SwitchTenant makes tenantID the session's active tenant after verifying membership, and
// answers with what app may do there.
func (a *Authorizer) SwitchTenant(ctx context.Context, session identity.Session, tenantID uuid.UUID, app identity.App) (TenantContext, error) {
	if tenantID == uuid.Nil {
		return TenantContext{}, ErrNoMembership
	}
	membership, err := a.repo.FindActiveMembership(ctx, session.ActorID, tenantID)
	if err != nil {
		if errors.Is(err, ErrNoMembership) {
			a.record(ctx, audit.Event{
				ActorID:    nullUUID(session.ActorID),
				Category:   audit.CategoryAuthentication,
				ActionCode: "session.tenant_switch",
				Outcome:    audit.OutcomeDenied,
				ReasonCode: "NO_MEMBERSHIP",
			})
		}
		return TenantContext{}, err
	}
	if err := a.sessions.SetActiveTenant(ctx, session.ID, tenantID); err != nil {
		return TenantContext{}, fmt.Errorf("identity: set active tenant: %w", err)
	}
	grants, err := a.repo.ResolveGrants(ctx, tenantID, membership.ID)
	if err != nil {
		return TenantContext{}, err
	}
	a.record(ctx, audit.Event{
		TenantID:     nullUUID(tenantID),
		ActorID:      nullUUID(session.ActorID),
		MembershipID: nullUUID(membership.ID),
		Category:     audit.CategoryAuthentication,
		ActionCode:   "session.tenant_switch",
		Outcome:      audit.OutcomeSuccess,
	})
	return tenantContext(membership, grants, app), nil
}

// RecordDenied audits a failed identity.Require or RequireStepUp on a route (SECURITY,
// DENIED). The resource is named by type only; ids would leak existence.
func (a *Authorizer) RecordDenied(ctx context.Context, rc identity.RequestContext, permission, reason string) {
	a.record(ctx, audit.Event{
		TenantID:     nullUUID(rc.TenantID),
		ActorID:      nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID),
		Category:     audit.CategorySecurity,
		ActionCode:   "authorization.denied",
		Outcome:      audit.OutcomeDenied,
		ReasonCode:   reason,
		Detail:       map[string]any{"permission": permission},
	})
}

func (a *Authorizer) record(ctx context.Context, ev audit.Event) { _ = a.audit(ctx, ev) }
