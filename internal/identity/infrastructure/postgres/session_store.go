// Package identitypg implements the identity repositories on PostgreSQL.
package identitypg

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// SessionStore persists BFF sessions in iam.session. Sessions are not tenant data, so the
// queries run outside a tenant transaction; rows are addressed only by the hash of the
// unguessable session id.
type SessionStore struct {
	pool *pgxpool.Pool
}

// NewSessionStore returns a store backed by pool.
func NewSessionStore(pool *pgxpool.Pool) *SessionStore { return &SessionStore{pool: pool} }

var (
	_ identity.SessionStore         = (*SessionStore)(nil)
	_ application.SessionRepository = (*SessionStore)(nil)
)

// Create stores a session without request metadata.
func (s *SessionStore) Create(ctx context.Context, sess identity.Session) error {
	return s.CreateWithMeta(ctx, sess, application.SessionMeta{})
}

// CreateWithMeta stores a session together with the user agent hash and client address.
func (s *SessionStore) CreateWithMeta(ctx context.Context, sess identity.Session, meta application.SessionMeta) error {
	if sess.ID == "" || sess.ActorID == uuid.Nil {
		return errors.New("identity: session id and actor id are required")
	}
	err := sqlcgen.New(s.pool).CreateSession(ctx, sqlcgen.CreateSessionParams{
		IDHash:         domain.TokenHash(sess.ID),
		ActorID:        sess.ActorID,
		ActiveTenantID: sess.ActiveTenantID,
		UserAgentHash:  meta.UserAgentHash,
		SourceIp:       optAddr(meta.SourceIP),
		CreatedAt:      sess.CreatedAt,
		LastSeenAt:     sess.LastSeenAt,
		ExpiresAt:      sess.ExpiresAt,
		StepUpUntil:    optTime(sess.StepUpUntil),
	})
	if err != nil {
		return fmt.Errorf("identity: insert session: %w", err)
	}
	return nil
}

// Get returns the session for the plaintext id, or identity.ErrSessionNotFound.
func (s *SessionStore) Get(ctx context.Context, id string) (identity.Session, error) {
	row, err := sqlcgen.New(s.pool).GetSession(ctx, domain.TokenHash(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Session{}, identity.ErrSessionNotFound
	}
	if err != nil {
		return identity.Session{}, fmt.Errorf("identity: read session: %w", err)
	}
	sess := identity.Session{
		ID:             id,
		ActorID:        row.ActorID,
		ActiveTenantID: row.ActiveTenantID,
		CreatedAt:      row.CreatedAt,
		LastSeenAt:     row.LastSeenAt,
		ExpiresAt:      row.ExpiresAt,
	}
	if row.StepUpUntil != nil {
		sess.StepUpUntil = *row.StepUpUntil
	}
	return sess, nil
}

// Touch updates last_seen_at.
func (s *SessionStore) Touch(ctx context.Context, id string, lastSeen time.Time) error {
	err := sqlcgen.New(s.pool).TouchSession(ctx, sqlcgen.TouchSessionParams{
		IDHash:     domain.TokenHash(id),
		LastSeenAt: lastSeen,
	})
	if err != nil {
		return fmt.Errorf("identity: touch session: %w", err)
	}
	return nil
}

// SetActiveTenant records the selected tenant.
func (s *SessionStore) SetActiveTenant(ctx context.Context, id string, tenantID uuid.UUID) error {
	n, err := sqlcgen.New(s.pool).SetSessionActiveTenant(ctx, sqlcgen.SetSessionActiveTenantParams{
		IDHash:         domain.TokenHash(id),
		ActiveTenantID: uuid.NullUUID{UUID: tenantID, Valid: tenantID != uuid.Nil},
	})
	if err != nil {
		return fmt.Errorf("identity: set active tenant: %w", err)
	}
	if n == 0 {
		return identity.ErrSessionNotFound
	}
	return nil
}

// SetStepUp opens or extends the step-up window.
func (s *SessionStore) SetStepUp(ctx context.Context, id string, until time.Time) error {
	n, err := sqlcgen.New(s.pool).SetSessionStepUp(ctx, sqlcgen.SetSessionStepUpParams{
		IDHash:      domain.TokenHash(id),
		StepUpUntil: optTime(until),
	})
	if err != nil {
		return fmt.Errorf("identity: set step-up: %w", err)
	}
	if n == 0 {
		return identity.ErrSessionNotFound
	}
	return nil
}

// Delete removes the session; deleting an unknown session is not an error.
func (s *SessionStore) Delete(ctx context.Context, id string) error {
	if _, err := sqlcgen.New(s.pool).DeleteSession(ctx, domain.TokenHash(id)); err != nil {
		return fmt.Errorf("identity: delete session: %w", err)
	}
	return nil
}

// DeleteByActor ends every session of an actor.
func (s *SessionStore) DeleteByActor(ctx context.Context, actorID uuid.UUID) (int64, error) {
	n, err := sqlcgen.New(s.pool).DeleteSessionsByActor(ctx, actorID)
	if err != nil {
		return 0, fmt.Errorf("identity: delete sessions of actor: %w", err)
	}
	return n, nil
}

// DeleteExpired removes sessions past their absolute expiry (scheduler job session.cleanup).
func (s *SessionStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	n, err := sqlcgen.New(s.pool).DeleteExpiredSessions(ctx, before)
	if err != nil {
		return 0, fmt.Errorf("identity: delete expired sessions: %w", err)
	}
	return n, nil
}

func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func optAddr(a netip.Addr) *netip.Addr {
	if !a.IsValid() {
		return nil
	}
	return &a
}
