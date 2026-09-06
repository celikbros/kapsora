package application

import (
	"context"
	"errors"
	"fmt"
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

// Grants are the permissions and scopes a membership currently holds.
type Grants struct {
	Permissions []string
	Scopes      []identity.Scope
}

// TenantContext is what the frontend needs to render one tenant for the user.
type TenantContext struct {
	Membership  Membership
	Permissions []string
	Scopes      []identity.Scope
	// PersonID is the person this account acts for in this tenant, resolved from its
	// PERSON scope (migration 000039). It is null for every actor that is not a member.
	// The frontend reads it to know a member client is bound; the server never trusts it
	// back and resolves the person from the grant on every call.
	PersonID uuid.NullUUID
}

// tenantContext builds the view, resolving the person once so /me, switch-tenant and the
// request context cannot disagree about who the caller acts for.
func tenantContext(m Membership, g Grants) TenantContext {
	return TenantContext{
		Membership:  m,
		Permissions: g.Permissions,
		Scopes:      g.Scopes,
		PersonID:    identity.PersonFromScopes(g.Scopes),
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

// ResolveTenantContext builds the request context for a tenant-scoped request. The
// requested tenant must be the session's active tenant and the actor must hold an active
// membership in it; the header alone is never trusted (v1.2 section 14.4).
func (a *Authorizer) ResolveTenantContext(ctx context.Context, session identity.Session, requested uuid.UUID, requestID string) (identity.RequestContext, error) {
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
	return a.requestContext(session, membership, grants, requestID), nil
}

func (a *Authorizer) requestContext(session identity.Session, m Membership, g Grants, requestID string) identity.RequestContext {
	perms := make(map[string]struct{}, len(g.Permissions))
	for _, p := range g.Permissions {
		perms[p] = struct{}{}
	}
	return identity.RequestContext{
		RequestID:    requestID,
		Principal:    identity.Principal{ActorID: session.ActorID, ActorType: identity.ActorHuman},
		SessionID:    session.ID,
		ClientType:   identity.ClientBrowser,
		TenantID:     m.Tenant.ID,
		MembershipID: m.ID,
		PersonID:     identity.PersonFromScopes(g.Scopes),
		Permissions:  perms,
		Scopes:       g.Scopes,
		Locale:       m.Tenant.DefaultLocale,
		TimeZone:     m.Tenant.DefaultTimeZone,
		StepUpValid:  !session.StepUpUntil.IsZero() && a.now().Before(session.StepUpUntil),
	}
}

// TenantContexts lists every tenant the actor may work in, with its permissions (/me).
func (a *Authorizer) TenantContexts(ctx context.Context, actorID uuid.UUID) ([]TenantContext, error) {
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
		out = append(out, tenantContext(m, grants))
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

// SwitchTenant makes tenantID the session's active tenant after verifying membership.
func (a *Authorizer) SwitchTenant(ctx context.Context, session identity.Session, tenantID uuid.UUID) (TenantContext, error) {
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
	return tenantContext(membership, grants), nil
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
