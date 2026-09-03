package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/domain"
)

// Service runs the authentication flows. It is safe for concurrent use.
type Service struct {
	credentials CredentialRepository
	sessions    SessionRepository
	audit       AuditSink
	policy      domain.Policy
	lockout     domain.Lockout
	hashParams  domain.PasswordParams
	now         func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Credentials CredentialRepository
	Sessions    SessionRepository
	Audit       AuditSink
	Policy      domain.Policy
	Lockout     domain.Lockout
	HashParams  domain.PasswordParams
	Now         func() time.Time // nil means time.Now
}

// New validates the dependencies and returns the service.
func New(d Deps) (*Service, error) {
	if d.Credentials == nil || d.Sessions == nil {
		return nil, errors.New("identity: credential repository and session store are required")
	}
	if err := d.Policy.Validate(); err != nil {
		return nil, err
	}
	if err := d.Lockout.Validate(); err != nil {
		return nil, err
	}
	if d.HashParams.MemoryKiB == 0 {
		d.HashParams = domain.DefaultPasswordParams()
	}
	if d.Audit == nil {
		d.Audit = func(context.Context, audit.Event) error { return nil }
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{
		credentials: d.Credentials, sessions: d.Sessions, audit: d.Audit,
		policy: d.Policy, lockout: d.Lockout, hashParams: d.HashParams, now: d.Now,
	}, nil
}

// Policy exposes the timing rules to the transport layer.
func (s *Service) Policy() domain.Policy { return s.policy }

// LoginInput is what the login endpoint received.
type LoginInput struct {
	Username string
	Password string
	Meta     SessionMeta
}

// SessionView is a session plus the profile fields the frontend displays. Login and
// Describe both return one, so a page load reports exactly what the login did.
type SessionView struct {
	Session            identity.Session
	DisplayName        string
	Email              string
	MustChangePassword bool
}

// Login verifies the credential and opens a session. Every failure path returns
// ErrInvalidCredentials, ErrAccountLocked or ErrActorSuspended and spends roughly the
// same time, so an attacker cannot enumerate user names.
func (s *Service) Login(ctx context.Context, in LoginInput) (SessionView, error) {
	username := domain.NormalizeUsername(in.Username)
	now := s.now()

	account, err := s.credentials.FindByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			domain.BurnPasswordTime(in.Password)
			s.auditLogin(ctx, uuid.Nil, audit.OutcomeFailure, "UNKNOWN_USER")
			return SessionView{}, ErrInvalidCredentials
		}
		return SessionView{}, fmt.Errorf("identity: look up account: %w", err)
	}

	if s.lockout.IsLocked(account.LockedUntil, now) {
		domain.BurnPasswordTime(in.Password)
		s.auditLogin(ctx, account.ActorID, audit.OutcomeDenied, "ACCOUNT_LOCKED")
		return SessionView{}, ErrAccountLocked
	}

	ok, needsRehash, err := domain.VerifyPassword(in.Password, account.PasswordHash, s.hashParams)
	if err != nil {
		// A malformed stored hash must not reveal itself to the caller.
		s.auditLogin(ctx, account.ActorID, audit.OutcomeFailure, "HASH_UNREADABLE")
		return SessionView{}, ErrInvalidCredentials
	}
	if !ok {
		if err := s.credentials.RegisterFailure(ctx, account.ActorID, s.lockout, now); err != nil {
			return SessionView{}, fmt.Errorf("identity: register failure: %w", err)
		}
		s.auditLogin(ctx, account.ActorID, audit.OutcomeFailure, "BAD_PASSWORD")
		return SessionView{}, ErrInvalidCredentials
	}

	// The password was correct; only now does the account status matter.
	if account.Status != ActorActive && account.Status != ActorInvited {
		s.auditLogin(ctx, account.ActorID, audit.OutcomeDenied, "ACTOR_"+string(account.Status))
		return SessionView{}, ErrActorSuspended
	}
	if err := s.credentials.RegisterSuccess(ctx, account.ActorID, now); err != nil {
		return SessionView{}, fmt.Errorf("identity: register success: %w", err)
	}
	if needsRehash {
		if hash, hErr := domain.HashPassword(in.Password, s.hashParams); hErr == nil {
			_ = s.credentials.SetPassword(ctx, account.ActorID, hash, now)
		}
	}

	session, err := s.openSession(ctx, account.ActorID, in.Meta, now)
	if err != nil {
		return SessionView{}, err
	}
	s.auditLogin(ctx, account.ActorID, audit.OutcomeSuccess, "")
	return SessionView{
		Session:            session,
		DisplayName:        account.DisplayName,
		Email:              account.Email,
		MustChangePassword: account.MustChangePassword,
	}, nil
}

// Describe loads a session together with the account profile. The frontend calls this on
// every page load, so it must report the same fields the login did.
func (s *Service) Describe(ctx context.Context, sessionID string) (SessionView, error) {
	session, err := s.LoadSession(ctx, sessionID)
	if err != nil {
		return SessionView{}, err
	}
	account, err := s.credentials.FindByActorID(ctx, session.ActorID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			// The account was deleted while the session lived; treat it as signed out.
			_ = s.sessions.Delete(ctx, sessionID)
			return SessionView{}, identity.ErrSessionNotFound
		}
		return SessionView{}, fmt.Errorf("identity: look up account: %w", err)
	}
	if account.Status != ActorActive && account.Status != ActorInvited {
		_ = s.sessions.Delete(ctx, sessionID)
		return SessionView{}, ErrActorSuspended
	}
	return SessionView{
		Session:            session,
		DisplayName:        account.DisplayName,
		Email:              account.Email,
		MustChangePassword: account.MustChangePassword,
	}, nil
}

func (s *Service) openSession(ctx context.Context, actorID uuid.UUID, meta SessionMeta, now time.Time) (identity.Session, error) {
	id, err := domain.NewToken()
	if err != nil {
		return identity.Session{}, err
	}
	session := identity.Session{
		ID:         id,
		ActorID:    actorID,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  s.policy.ExpiresAt(now),
	}
	if err := s.sessions.CreateWithMeta(ctx, session, meta); err != nil {
		return identity.Session{}, fmt.Errorf("identity: create session: %w", err)
	}
	return session, nil
}

// Logout deletes the session. It is idempotent: logging out twice is not an error.
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return identity.ErrUnauthenticated
	}
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil && !errors.Is(err, identity.ErrSessionNotFound) {
		return err
	}
	if err := s.sessions.Delete(ctx, sessionID); err != nil {
		return fmt.Errorf("identity: delete session: %w", err)
	}
	if session.ActorID != uuid.Nil {
		s.record(ctx, audit.Event{
			ActorID:    nullUUID(session.ActorID),
			Category:   audit.CategoryAuthentication,
			ActionCode: "session.logout",
			Outcome:    audit.OutcomeSuccess,
		})
	}
	return nil
}

// StepUp re-verifies the password and opens the step-up window (v1.2 section 18.3).
// Failures count towards the same lockout as a login.
func (s *Service) StepUp(ctx context.Context, sessionID, password string) (until time.Time, err error) {
	session, err := s.LoadSession(ctx, sessionID)
	if err != nil {
		return time.Time{}, err
	}
	now := s.now()
	account, err := s.credentials.FindByActorID(ctx, session.ActorID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return time.Time{}, ErrInvalidCredentials
		}
		return time.Time{}, fmt.Errorf("identity: look up account: %w", err)
	}
	if s.lockout.IsLocked(account.LockedUntil, now) {
		domain.BurnPasswordTime(password)
		s.auditStepUp(ctx, account.ActorID, audit.OutcomeDenied, "ACCOUNT_LOCKED")
		return time.Time{}, ErrAccountLocked
	}
	ok, _, err := domain.VerifyPassword(password, account.PasswordHash, s.hashParams)
	if err != nil || !ok {
		if regErr := s.credentials.RegisterFailure(ctx, account.ActorID, s.lockout, now); regErr != nil {
			return time.Time{}, fmt.Errorf("identity: register failure: %w", regErr)
		}
		s.auditStepUp(ctx, account.ActorID, audit.OutcomeFailure, "BAD_PASSWORD")
		return time.Time{}, ErrInvalidCredentials
	}
	if err := s.credentials.RegisterSuccess(ctx, account.ActorID, now); err != nil {
		return time.Time{}, fmt.Errorf("identity: register success: %w", err)
	}

	until = s.policy.StepUpUntil(now)
	if err := s.sessions.SetStepUp(ctx, sessionID, until); err != nil {
		return time.Time{}, fmt.Errorf("identity: set step-up: %w", err)
	}
	s.auditStepUp(ctx, account.ActorID, audit.OutcomeSuccess, "")
	return until, nil
}

// ChangePassword replaces the caller's own password after verifying the current one. All
// other sessions of the actor are ended; the calling session survives.
func (s *Service) ChangePassword(ctx context.Context, sessionID, current, next string) error {
	session, err := s.LoadSession(ctx, sessionID)
	if err != nil {
		return err
	}
	now := s.now()
	account, err := s.credentials.FindByActorID(ctx, session.ActorID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return ErrInvalidCredentials
		}
		return fmt.Errorf("identity: look up account: %w", err)
	}
	if s.lockout.IsLocked(account.LockedUntil, now) {
		domain.BurnPasswordTime(current)
		return ErrAccountLocked
	}
	ok, _, err := domain.VerifyPassword(current, account.PasswordHash, s.hashParams)
	if err != nil || !ok {
		if regErr := s.credentials.RegisterFailure(ctx, account.ActorID, s.lockout, now); regErr != nil {
			return fmt.Errorf("identity: register failure: %w", regErr)
		}
		s.auditPassword(ctx, account.ActorID, audit.OutcomeFailure, "BAD_CURRENT_PASSWORD")
		return ErrInvalidCredentials
	}
	if err := domain.ValidatePassword(next, account.Username); err != nil {
		return err
	}
	if next == current {
		return domain.ErrPasswordTooCommon
	}
	hash, err := domain.HashPassword(next, s.hashParams)
	if err != nil {
		return err
	}
	if err := s.credentials.SetPassword(ctx, account.ActorID, hash, now); err != nil {
		return fmt.Errorf("identity: set password: %w", err)
	}

	// End every other session of this actor, then restore the caller's own.
	if _, err := s.sessions.DeleteByActor(ctx, account.ActorID); err != nil {
		return fmt.Errorf("identity: revoke sessions: %w", err)
	}
	session.LastSeenAt = now
	if err := s.sessions.CreateWithMeta(ctx, session, SessionMeta{}); err != nil {
		return fmt.Errorf("identity: restore session: %w", err)
	}
	s.auditPassword(ctx, account.ActorID, audit.OutcomeSuccess, "")
	return nil
}

// LoadSession returns a usable session or identity.ErrSessionNotFound. Sessions past the
// idle timeout or the absolute limit are deleted so they cannot be resurrected.
func (s *Service) LoadSession(ctx context.Context, sessionID string) (identity.Session, error) {
	if sessionID == "" {
		return identity.Session{}, identity.ErrSessionNotFound
	}
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return identity.Session{}, err
	}
	now := s.now()
	if s.policy.Evaluate(session, now) != domain.StatusActive {
		_ = s.sessions.Delete(ctx, sessionID)
		return identity.Session{}, identity.ErrSessionNotFound
	}
	if s.policy.NeedsTouch(session, now) {
		if err := s.sessions.Touch(ctx, sessionID, now); err != nil {
			return identity.Session{}, fmt.Errorf("identity: touch session: %w", err)
		}
		session.LastSeenAt = now
	}
	return session, nil
}

// SetActiveTenant records the tenant the user selected. Membership is verified by the
// caller (WP-I1-02) before this is called.
func (s *Service) SetActiveTenant(ctx context.Context, sessionID string, tenantID uuid.UUID) error {
	return s.sessions.SetActiveTenant(ctx, sessionID, tenantID)
}

// RevokeAllForActor ends every session of an actor (account suspended, password reset by
// an administrator, security incident).
func (s *Service) RevokeAllForActor(ctx context.Context, actorID uuid.UUID) (int64, error) {
	return s.sessions.DeleteByActor(ctx, actorID)
}

// CreateAccount hashes the password and creates a login-capable actor. Used by seeding
// and by administrative user creation.
func (s *Service) CreateAccount(ctx context.Context, username, displayName, email, password string, mustChange bool) (uuid.UUID, error) {
	username = domain.NormalizeUsername(username)
	if username == "" {
		return uuid.Nil, errors.New("identity: user name is required")
	}
	if err := domain.ValidatePassword(password, username); err != nil {
		return uuid.Nil, err
	}
	hash, err := domain.HashPassword(password, s.hashParams)
	if err != nil {
		return uuid.Nil, err
	}
	return s.credentials.CreateHumanAccount(ctx, NewAccount{
		Username:           username,
		DisplayName:        displayName,
		Email:              email,
		PasswordHash:       hash,
		MustChangePassword: mustChange,
	})
}

func (s *Service) auditLogin(ctx context.Context, actorID uuid.UUID, outcome audit.Outcome, reason string) {
	s.record(ctx, audit.Event{
		ActorID:    nullUUID(actorID),
		Category:   audit.CategoryAuthentication,
		ActionCode: "session.login",
		Outcome:    outcome,
		ReasonCode: reason,
	})
}

func (s *Service) auditStepUp(ctx context.Context, actorID uuid.UUID, outcome audit.Outcome, reason string) {
	s.record(ctx, audit.Event{
		ActorID:    nullUUID(actorID),
		Category:   audit.CategoryAuthentication,
		ActionCode: "session.step_up",
		Outcome:    outcome,
		ReasonCode: reason,
	})
}

func (s *Service) auditPassword(ctx context.Context, actorID uuid.UUID, outcome audit.Outcome, reason string) {
	s.record(ctx, audit.Event{
		ActorID:    nullUUID(actorID),
		Category:   audit.CategoryAuthentication,
		ActionCode: "session.password_change",
		Outcome:    outcome,
		ReasonCode: reason,
	})
}

// record never fails the flow: a login must not break because the audit write did. The
// sink logs its own errors.
func (s *Service) record(ctx context.Context, ev audit.Event) {
	_ = s.audit(ctx, ev)
}
