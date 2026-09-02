// Package identity holds the types every module uses to know who is calling and for
// which tenant (v1.2 section 14.4). The OIDC/BFF implementation (WP-I1-01) and the
// authorization middleware (WP-I1-02) populate these; other modules only read them
// through FromContext and Require.
package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ActorType mirrors iam.actor.actor_type.
type ActorType string

const (
	ActorHuman          ActorType = "HUMAN"
	ActorServiceAccount ActorType = "SERVICE_ACCOUNT"
	ActorSystem         ActorType = "SYSTEM"
)

// Principal is the authenticated identity mapped to iam.actor.
type Principal struct {
	ActorID     uuid.UUID
	ActorType   ActorType
	Issuer      string
	Subject     string
	DisplayName string
	Email       string
}

// Session is the opaque BFF session referenced by the __Host-kapsora_session cookie.
// The cookie carries a random id; the store persists only a hash of it.
type Session struct {
	ID             string
	ActorID        uuid.UUID
	ActiveTenantID uuid.NullUUID
	CSRFToken      string
	IdPSessionID   string
	CreatedAt      time.Time
	LastSeenAt     time.Time
	ExpiresAt      time.Time // absolute limit (v1.2 18.2)
	StepUpUntil    time.Time // zero when no step-up is active
}

// SessionStore persists sessions. The PostgreSQL implementation is delivered by
// WP-I1-01 (ADR-021: no Valkey in the container-free runtime).
type SessionStore interface {
	Create(ctx context.Context, s Session) error
	Get(ctx context.Context, id string) (Session, error)
	Touch(ctx context.Context, id string, lastSeen time.Time) error
	SetActiveTenant(ctx context.Context, id string, tenantID uuid.UUID) error
	SetStepUp(ctx context.Context, id string, until time.Time) error
	Delete(ctx context.Context, id string) error
	DeleteByActor(ctx context.Context, actorID uuid.UUID) (int64, error)
}

// Scope narrows a role grant (v1.2 6.3): ORGANIZATION, PROGRAM, PROVIDER_LOCATION, WORK_QUEUE.
type Scope struct {
	Type string
	ID   uuid.NullUUID
}

// ClientType distinguishes browser sessions from machine clients.
type ClientType string

const (
	ClientBrowser ClientType = "BROWSER"
	ClientService ClientType = "SERVICE"
)

// RequestContext is resolved once per request after authentication and tenant checks.
type RequestContext struct {
	RequestID    string
	TraceID      string
	Principal    Principal
	SessionID    string
	ClientType   ClientType
	TenantID     uuid.UUID
	MembershipID uuid.UUID
	Permissions  map[string]struct{}
	Scopes       []Scope
	Locale       string
	TimeZone     string
	StepUpValid  bool
}

// Has reports whether the permission code is granted.
func (rc RequestContext) Has(permission string) bool {
	_, ok := rc.Permissions[permission]
	return ok
}

// Errors returned by Require and FromContext; transports map them to problem+json
// (401 UNAUTHENTICATED, 403 PERMISSION_DENIED, 403 STEP_UP_REQUIRED).
var (
	ErrUnauthenticated  = errors.New("identity: no authenticated request context")
	ErrPermissionDenied = errors.New("identity: permission denied")
	ErrStepUpRequired   = errors.New("identity: step-up authentication required")
	ErrSessionNotFound  = errors.New("identity: session not found")
)

type ctxKey struct{}
type sessionCtxKey struct{}

// WithSession attaches the loaded BFF session (set by the session middleware, WP-I1-01).
func WithSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionCtxKey{}, s)
}

// SessionFromContext returns the session loaded for this request, if any.
func SessionFromContext(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionCtxKey{}).(Session)
	return s, ok
}

// WithRequestContext attaches rc to ctx.
func WithRequestContext(ctx context.Context, rc RequestContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, rc)
}

// FromContext returns the resolved request context, if any.
func FromContext(ctx context.Context) (RequestContext, bool) {
	rc, ok := ctx.Value(ctxKey{}).(RequestContext)
	return rc, ok
}

// Require fails unless the caller is authenticated and holds the permission. The UI may
// hide buttons; the backend always re-validates through this call.
func Require(ctx context.Context, permission string) (RequestContext, error) {
	rc, ok := FromContext(ctx)
	if !ok {
		return RequestContext{}, ErrUnauthenticated
	}
	if !rc.Has(permission) {
		return rc, ErrPermissionDenied
	}
	return rc, nil
}

// RequireStepUp is Require plus a valid step-up window (v1.2 18.3).
func RequireStepUp(ctx context.Context, permission string) (RequestContext, error) {
	rc, err := Require(ctx, permission)
	if err != nil {
		return rc, err
	}
	if !rc.StepUpValid {
		return rc, ErrStepUpRequired
	}
	return rc, nil
}
