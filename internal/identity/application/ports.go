// Package application orchestrates login, logout, step-up and password changes. It
// depends only on the ports declared here, so it is testable without a database.
//
// KAPSORA authenticates its own users (ADR-022): there is no external identity provider
// in the MVP. Credentials live in iam.credential, sessions in iam.session; the browser
// only ever holds an opaque session cookie.
package application

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/domain"
)

// ActorStatus mirrors iam.actor.status.
type ActorStatus string

const (
	ActorInvited   ActorStatus = "INVITED"
	ActorActive    ActorStatus = "ACTIVE"
	ActorSuspended ActorStatus = "SUSPENDED"
	ActorClosed    ActorStatus = "CLOSED"
)

// Account is one login-capable actor with its credential state.
type Account struct {
	ActorID            uuid.UUID
	Username           string
	DisplayName        string
	Email              string
	Status             ActorStatus
	PasswordHash       string
	MustChangePassword bool
	FailedAttempts     int
	LockedUntil        time.Time
}

// NewAccount creates a login-capable actor (used by seeding and, from WP-I1-02, by the
// user invitation flow).
type NewAccount struct {
	Username     string
	DisplayName  string
	Email        string
	PasswordHash string
	// MustChangePassword marks a temporary password issued by an administrator.
	MustChangePassword bool
}

// ErrAccountNotFound is returned by the repository; the service never surfaces it,
// so a wrong user name and a wrong password look identical to the caller.
var ErrAccountNotFound = errors.New("identity: account not found")

// CredentialRepository stores actors and their password credentials.
type CredentialRepository interface {
	FindByUsername(ctx context.Context, username string) (Account, error)
	FindByActorID(ctx context.Context, actorID uuid.UUID) (Account, error)
	// RegisterFailure increments the failure counter atomically and applies the lockout
	// threshold in the same statement, so parallel attempts cannot overshoot it.
	RegisterFailure(ctx context.Context, actorID uuid.UUID, l domain.Lockout, now time.Time) error
	// RegisterSuccess clears the counter and records the login instant.
	RegisterSuccess(ctx context.Context, actorID uuid.UUID, now time.Time) error
	// SetPassword replaces the hash, clears must-change and unlocks the account.
	SetPassword(ctx context.Context, actorID uuid.UUID, hash string, now time.Time) error
	CreateHumanAccount(ctx context.Context, in NewAccount) (uuid.UUID, error)
}

// SessionMeta is the non-identifying request context stored with a session.
type SessionMeta struct {
	UserAgentHash []byte
	SourceIP      netip.Addr
}

// SessionRepository is identity.SessionStore plus creation with request metadata.
type SessionRepository interface {
	identity.SessionStore
	CreateWithMeta(ctx context.Context, s identity.Session, meta SessionMeta) error
}

// AuditSink records an authentication event. The wiring supplies an implementation that
// opens its own short transaction, keeping pgx out of this package.
type AuditSink func(ctx context.Context, ev audit.Event) error

// Errors surfaced to the transport layer. Login failures are deliberately coarse: the
// caller must not learn whether the user name exists.
var (
	ErrInvalidCredentials = errors.New("identity: invalid user name or password")
	ErrAccountLocked      = errors.New("identity: account is temporarily locked")
	ErrActorSuspended     = errors.New("identity: actor is suspended or closed")
	ErrPasswordChange     = errors.New("identity: password must be changed before continuing")
)

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}
