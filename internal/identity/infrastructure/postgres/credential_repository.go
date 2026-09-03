package identitypg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// CredentialRepository stores login-capable actors and their password credentials.
type CredentialRepository struct {
	pool *pgxpool.Pool
}

// NewCredentialRepository returns a repository backed by pool.
func NewCredentialRepository(pool *pgxpool.Pool) *CredentialRepository {
	return &CredentialRepository{pool: pool}
}

var _ application.CredentialRepository = (*CredentialRepository)(nil)

// FindByUsername looks up a local account by its normalised user name.
func (r *CredentialRepository) FindByUsername(ctx context.Context, username string) (application.Account, error) {
	row, err := sqlcgen.New(r.pool).FindAccountByUsername(ctx, sqlcgen.FindAccountByUsernameParams{
		IdentityIssuer:  identity.LocalIssuer,
		IdentitySubject: username,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Account{}, application.ErrAccountNotFound
	}
	if err != nil {
		return application.Account{}, fmt.Errorf("identity: find account: %w", err)
	}
	return application.Account{
		ActorID:            row.ID,
		Username:           row.IdentitySubject,
		DisplayName:        row.DisplayName,
		Email:              derefString(row.Email),
		Status:             application.ActorStatus(row.Status),
		PasswordHash:       row.PasswordHash,
		MustChangePassword: row.MustChangePassword,
		FailedAttempts:     int(row.FailedAttempts),
		LockedUntil:        derefTime(row.LockedUntil),
	}, nil
}

// FindByActorID looks up the account of an authenticated actor.
func (r *CredentialRepository) FindByActorID(ctx context.Context, actorID uuid.UUID) (application.Account, error) {
	row, err := sqlcgen.New(r.pool).FindAccountByActorID(ctx, actorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Account{}, application.ErrAccountNotFound
	}
	if err != nil {
		return application.Account{}, fmt.Errorf("identity: find account: %w", err)
	}
	return application.Account{
		ActorID:            row.ID,
		Username:           row.IdentitySubject,
		DisplayName:        row.DisplayName,
		Email:              derefString(row.Email),
		Status:             application.ActorStatus(row.Status),
		PasswordHash:       row.PasswordHash,
		MustChangePassword: row.MustChangePassword,
		FailedAttempts:     int(row.FailedAttempts),
		LockedUntil:        derefTime(row.LockedUntil),
	}, nil
}

// RegisterFailure increments the failure counter and applies the lockout threshold in one
// statement, so concurrent attempts cannot overshoot it.
func (r *CredentialRepository) RegisterFailure(ctx context.Context, actorID uuid.UUID, l domain.Lockout, now time.Time) error {
	lockedUntil := now.Add(l.Duration)
	err := sqlcgen.New(r.pool).RegisterLoginFailure(ctx, sqlcgen.RegisterLoginFailureParams{
		ActorID:        actorID,
		FailedAttempts: int32(l.MaxFailedAttempts), //nolint:gosec // Lockout.Validate caps this at 100
		LockedUntil:    &lockedUntil,
	})
	if err != nil {
		return fmt.Errorf("identity: register login failure: %w", err)
	}
	return nil
}

// RegisterSuccess clears the failure counter and records the login instant.
func (r *CredentialRepository) RegisterSuccess(ctx context.Context, actorID uuid.UUID, now time.Time) error {
	err := sqlcgen.New(r.pool).RegisterLoginSuccess(ctx, sqlcgen.RegisterLoginSuccessParams{
		ActorID:     actorID,
		LastLoginAt: &now,
	})
	if err != nil {
		return fmt.Errorf("identity: register login success: %w", err)
	}
	return nil
}

// SetPassword replaces the hash, clears must-change and unlocks the account.
func (r *CredentialRepository) SetPassword(ctx context.Context, actorID uuid.UUID, hash string, now time.Time) error {
	err := sqlcgen.New(r.pool).SetCredentialPassword(ctx, sqlcgen.SetCredentialPasswordParams{
		ActorID:           actorID,
		PasswordHash:      hash,
		PasswordUpdatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("identity: set password: %w", err)
	}
	return nil
}

// CreateHumanAccount creates the actor and its credential in one transaction.
func (r *CredentialRepository) CreateHumanAccount(ctx context.Context, in application.NewAccount) (uuid.UUID, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("identity: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := sqlcgen.New(tx)
	actorID, err := q.CreateLocalActor(ctx, sqlcgen.CreateLocalActorParams{
		IdentityIssuer:  identity.LocalIssuer,
		IdentitySubject: in.Username,
		DisplayName:     in.DisplayName,
		Email:           optString(in.Email),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("identity: create actor: %w", err)
	}
	if err := q.CreateCredential(ctx, sqlcgen.CreateCredentialParams{
		ActorID:            actorID,
		PasswordHash:       in.PasswordHash,
		MustChangePassword: in.MustChangePassword,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("identity: create credential: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("identity: commit: %w", err)
	}
	return actorID, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
