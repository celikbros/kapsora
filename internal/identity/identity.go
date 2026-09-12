// Package identity holds the types every module uses to know who is calling and for
// which tenant (v1.2 section 14.4). The OIDC/BFF implementation (WP-I1-01) and the
// authorization middleware (WP-I1-02) populate these; other modules only read them
// through FromContext and Require.
package identity

import (
	"context"
	"errors"
	"sort"
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

// Session is the opaque BFF session referenced by the session cookie. The cookie carries
// a random id; the store persists only a hash of it.
type Session struct {
	// ID is the plaintext session id. It exists in the cookie and in memory, never in
	// the database, and is empty on sessions loaded for anything but the current request.
	ID             string
	ActorID        uuid.UUID
	ActiveTenantID uuid.NullUUID
	// CSRFToken is derived from ID by the transport layer (domain.DeriveCSRFToken); it is
	// not stored and is empty unless the transport filled it in.
	CSRFToken   string
	CreatedAt   time.Time
	LastSeenAt  time.Time
	ExpiresAt   time.Time // absolute limit (v1.2 18.2)
	StepUpUntil time.Time // zero when no step-up is active
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
	// DeleteExpired removes sessions whose absolute expiry is before the given time; the
	// scheduler job session.cleanup calls it.
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// Scope narrows a role grant (v1.2 6.3): ORGANIZATION, PROGRAM, PROVIDER_LOCATION,
// WORK_QUEUE, PERSON.
type Scope struct {
	Type string
	ID   uuid.NullUUID
}

// ScopePerson is the scope a member account is bound to its own person by (migration
// 000039). It is spelled here, in the package every module reads its caller from, because
// the string is compared in three places -- the grant writer, the context resolver and the
// helper below -- and three spellings would be three different scopes.
//
// A PERSON grant is created by onboarding and by the seed, never by a member: the whole
// value of the binding is that the account cannot choose whose it is.
const ScopePerson = "PERSON"

// App is the KAPSORA app a browser request comes from, named in the X-Kapsora-App header.
//
// One account may hold grants of three kinds in one tenant -- a tenant-wide staff role, an
// ORGANIZATION grant at a hospital or hotel, a PERSON grant as a member -- and each kind
// belongs to one app. The app a request names picks which of them apply to it: the
// backoffice sees the staff roles and not the member binding, the member app sees the binding
// and nothing else. Naming an app can only narrow what the account holds, never add to it,
// which is why the header may be taken from the client.
//
// A request that names no app gets every grant at once. That is how every caller behaved
// before the apps were told apart, and how a service client still does.
type App string

const (
	AppAny        App = ""
	AppBackoffice App = "backoffice"
	AppProvider   App = "provider"
	AppMember     App = "member"
)

// AppHeader names the app of a browser request.
const AppHeader = "X-Kapsora-App"

// Apps are the three apps in the order they are offered to a person.
var Apps = []App{AppBackoffice, AppProvider, AppMember}

// ParseApp reads the header value. An empty value is AppAny; an unknown one is refused, so
// a client with a typo is told rather than silently handed every grant at once.
func ParseApp(raw string) (App, bool) {
	switch app := App(raw); app {
	case AppAny, AppBackoffice, AppProvider, AppMember:
		return app, true
	}
	return AppAny, false
}

// AppOfScope is the app a grant of this scope type belongs to. A PERSON grant is the member
// app's; an ORGANIZATION or PROVIDER_LOCATION grant is the provider portal's; a tenant-wide
// grant and the staff-side narrowing scopes (PROGRAM, WORK_QUEUE) are the backoffice's.
func AppOfScope(scopeType string) App {
	switch scopeType {
	case ScopePerson:
		return AppMember
	case "ORGANIZATION", "PROVIDER_LOCATION":
		return AppProvider
	default:
		return AppBackoffice
	}
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
	// App is the app the request came from; AppAny when it named none. Only the grants that
	// belong to it made the permissions and scopes below.
	App App
	// PersonID is the person this caller acts for, from its PERSON scope. It is set for a
	// member account and unset for every other actor: a reviewer, a provider clerk and an
	// administrator act for the tenant or for an organization, never for a person. A member
	// account that is also staff has it only in the member app.
	PersonID uuid.NullUUID
	// SelfPersonID is the person this account is in the tenant, from its PERSON grant,
	// whichever app the request came from. PersonID says whom the caller acts for; this says
	// who the caller is, and it is what refuses a reviewer a decision on their own file.
	SelfPersonID uuid.NullUUID
	Permissions  map[string]struct{}
	Scopes       []Scope
	Locale       string
	TimeZone     string
	StepUpValid  bool
}

// PermissionCodes are the permissions this caller holds, sorted. It is what a query that
// filters rows by the permission their work takes is given: the set is small, it is already
// resolved, and passing it is what keeps that decision in one place rather than in a join.
func (rc RequestContext) PermissionCodes() []string {
	out := make([]string, 0, len(rc.Permissions))
	for code := range rc.Permissions {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// Has reports whether the permission code is granted.
func (rc RequestContext) Has(permission string) bool {
	_, ok := rc.Permissions[permission]
	return ok
}

// PersonScope returns the person this caller acts for. It reads the resolved PersonID
// rather than walking Scopes again, so a caller cannot be bound by a scope the tenant
// context resolver did not accept.
func (rc RequestContext) PersonScope() (uuid.UUID, bool) {
	if !rc.PersonID.Valid || rc.PersonID.UUID == uuid.Nil {
		return uuid.Nil, false
	}
	return rc.PersonID.UUID, true
}

// PersonFromScopes finds the person of a grant set. It is the one place the PERSON scope
// is turned into an id, and it refuses an ambiguous answer rather than picking one: two
// PERSON grants on one membership would be an account acting for two people, which is
// exactly the confusion the scope exists to prevent. The database refuses that pair too
// (uq_access_grant_membership_person), so this is the second of two locks rather than the
// only one.
func PersonFromScopes(scopes []Scope) uuid.NullUUID {
	var found uuid.NullUUID
	for _, s := range scopes {
		if s.Type != ScopePerson || !s.ID.Valid || s.ID.UUID == uuid.Nil {
			continue
		}
		if found.Valid && found.UUID != s.ID.UUID {
			return uuid.NullUUID{}
		}
		found = uuid.NullUUID{UUID: s.ID.UUID, Valid: true}
	}
	return found
}

// Errors returned by Require and FromContext; transports map them to problem+json
// (401 UNAUTHENTICATED, 403 PERMISSION_DENIED, 403 STEP_UP_REQUIRED).
var (
	ErrUnauthenticated  = errors.New("identity: no authenticated request context")
	ErrPermissionDenied = errors.New("identity: permission denied")
	ErrStepUpRequired   = errors.New("identity: step-up authentication required")
	ErrSessionNotFound  = errors.New("identity: session not found")
	// ErrPersonBindingMissing is a caller with no PERSON scope asking for a member
	// command. It is its own error rather than a plain permission denial because the fix
	// is different: nobody has to grant this account anything, somebody has to finish
	// binding it to a person, and a member told "you do not have permission" would go
	// looking for the wrong help.
	ErrPersonBindingMissing = errors.New("identity: this account is not bound to a person")
	// ErrPersonScope is a member naming somebody else. It is the refusal that makes the
	// binding worth having: a member cannot hold a room for their neighbour by editing a
	// request body.
	ErrPersonScope = errors.New("identity: this caller may act only for its own person")
	// ErrOwnFile is a reviewer deciding a file that belongs to the person they are. A member
	// who also works as staff reviews everyone's files but their own: whoever decides a
	// claim, a request, a report or a refund must not be the person it pays or refuses.
	ErrOwnFile = errors.New("identity: a person may not decide their own file")
)

// RefuseOwnFile refuses a decision on a file whose person is the caller's own person. It
// reads SelfPersonID, which is set whichever app the request came from, so a reviewer who is
// also a member is recognised in the backoffice, where they act for nobody. It is called by
// every command that decides a person's file, after the file is locked and before anything
// is written.
func RefuseOwnFile(rc RequestContext, subject uuid.UUID) error {
	if subject != uuid.Nil && rc.SelfPersonID.Valid && rc.SelfPersonID.UUID == subject {
		return ErrOwnFile
	}
	return nil
}

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

// LocalIssuer is the identity_issuer value of accounts KAPSORA authenticates itself
// (ADR-022). identity_subject holds the normalised user name, so the existing
// UNIQUE (identity_issuer, identity_subject) enforces user-name uniqueness.
const LocalIssuer = "kapsora"

// RequirePerson resolves the person a member-side command acts for, and refuses any other.
//
// It is the single helper every member command in WP-I6-01..03 calls -- searchAvailability
// for oneself, createHold, confirmBooking, cancelBooking, joinWaitlist, listBookings --
// because "whose booking is this" must be answered the same way everywhere. The answer is
// the server's: it comes from the caller's PERSON grant and never from the request.
//
// `requested` is what the body named, or uuid.Nil when the body named nobody. A body that
// names the caller's own person is accepted, because a client that echoes back what it
// read from getMyPerson is doing nothing wrong; a body that names anybody else is refused
// with ErrPersonScope. That is the whole rule, and it is stated once.
func RequirePerson(ctx context.Context, requested uuid.UUID) (RequestContext, uuid.UUID, error) {
	rc, ok := FromContext(ctx)
	if !ok {
		return RequestContext{}, uuid.Nil, ErrUnauthenticated
	}
	personID, bound := rc.PersonScope()
	if !bound {
		return rc, uuid.Nil, ErrPersonBindingMissing
	}
	if requested != uuid.Nil && requested != personID {
		return rc, uuid.Nil, ErrPersonScope
	}
	return rc, personID, nil
}

// RequirePersonWith is RequirePerson plus a permission, for the member commands that also
// take one (accommodation.booking.create, service_request.create). The permission is
// checked first so an actor with neither is told the thing it can act on -- a member whose
// role was removed is a different problem from a member who was never bound.
func RequirePersonWith(ctx context.Context, permission string, requested uuid.UUID) (RequestContext, uuid.UUID, error) {
	if _, err := Require(ctx, permission); err != nil {
		return RequestContext{}, uuid.Nil, err
	}
	return RequirePerson(ctx, requested)
}
