package identitypg

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

func (r *InvitationRepository) InspectNewInvitation(ctx context.Context, proof application.InvitationProof) (application.InspectedInvitation, error) {
	var out application.InspectedInvitation
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: proof.TenantID}, func(ctx context.Context, tx pgx.Tx) error {
		row, err := sqlcgen.New(tx).InspectInvitationProof(ctx, sqlcgen.InspectInvitationProofParams{TenantID: proof.TenantID, ID: proof.InvitationID})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrInvitationUnavailable
		}
		if err != nil {
			return err
		}
		if !application.VerifyInvitationProof(proof.Code, row.ProofDigest) || row.Status != "PENDING" || !time.Now().Before(row.ExpiresAt) {
			return application.ErrInvitationUnavailable
		}
		out = application.InspectedInvitation{TenantDisplayName: row.DisplayName, Status: "PENDING", ExpiresAt: row.ExpiresAt}
		return nil
	})
	return out, err
}

func (r *InvitationRepository) AcceptNewInvitation(ctx context.Context, proof application.InvitationProof, name, password, key, fingerprintInput string) (application.AcceptedNewInvitation, error) {
	var out application.AcceptedNewInvitation
	var refusal error
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: proof.TenantID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if _, err := q.LockActiveInvitationTenant(ctx, proof.TenantID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				domain.BurnPasswordTime(password)
				return application.ErrInvitationUnavailable
			}
			return err
		}
		row, err := q.LockInvitationForAccept(ctx, sqlcgen.LockInvitationForAcceptParams{TenantID: proof.TenantID, ID: proof.InvitationID})
		if errors.Is(err, pgx.ErrNoRows) {
			domain.BurnPasswordTime(password)
			return application.ErrInvitationUnavailable
		}
		if err != nil {
			return err
		}
		if !application.VerifyInvitationProof(proof.Code, row.ProofDigest) {
			domain.BurnPasswordTime(password)
			return application.ErrInvitationUnavailable
		}
		if row.Status == "ACCEPTED" {
			out, refusal, err = r.recoverAcceptedNew(ctx, tx, proof, row, password)
			if err != nil {
				return err
			}
			if refusal != nil {
				return nil
			}
			fingerprint, err := r.index.TenantIndex(ctx, proof.TenantID, crypto.PurposeInvitationCommand, fingerprintInput)
			if err != nil {
				return err
			}
			if row.AcceptKey != key || !bytes.Equal(row.AcceptNewFingerprint, fingerprint) {
				return application.ErrInvitationKeyReused
			}
			return nil
		}
		if row.Status != "PENDING" || !time.Now().Before(row.ExpiresAt) {
			domain.BurnPasswordTime(password)
			return application.ErrInvitationUnavailable
		}
		hash, err := domain.HashPassword(password, domain.DefaultPasswordParams())
		if err != nil {
			return err
		}
		actorID, handle, err := createInvitationAccountTx(ctx, q, name, hash)
		if err != nil {
			return err
		}
		memberID, err := q.CreateInvitationMembership(ctx, sqlcgen.CreateInvitationMembershipParams{TenantID: proof.TenantID, ActorID: actorID})
		if err != nil {
			return err
		}
		fingerprint, err := r.index.TenantIndex(ctx, proof.TenantID, crypto.PurposeInvitationCommand, fingerprintInput)
		if err != nil {
			return err
		}
		terminalAt, err := q.AcceptNewTenantInvitation(ctx, sqlcgen.AcceptNewTenantInvitationParams{
			TenantID: proof.TenantID, ID: proof.InvitationID,
			AcceptedActorID:      uuid.NullUUID{UUID: actorID, Valid: true},
			AcceptedMembershipID: uuid.NullUUID{UUID: memberID, Valid: true},
			AcceptKey:            &key, AcceptNewFingerprint: fingerprint,
		})
		if err != nil {
			return err
		}
		if terminalAt == nil {
			return application.ErrInvitationUnavailable
		}
		out = application.AcceptedNewInvitation{AcceptedInvitation: application.AcceptedInvitation{
			TenantID: proof.TenantID, TenantDisplayName: row.DisplayName, MembershipID: memberID, AccessPending: true,
		}, LoginHandle: handle, RecoveryExpiresAt: terminalAt.Add(24 * time.Hour)}
		return r.audit.Record(ctx, tx, audit.Event{TenantID: uuid.NullUUID{UUID: proof.TenantID, Valid: true},
			ActorID: uuid.NullUUID{UUID: actorID, Valid: true}, Category: audit.CategoryAdmin,
			ActionCode: "tenant_invitation.accept_new", ResourceType: "tenant_invitation",
			ResourceID: uuid.NullUUID{UUID: proof.InvitationID, Valid: true}, Outcome: audit.OutcomeSuccess,
			Detail: map[string]any{"old_status": "PENDING", "new_status": "ACCEPTED"}})
	})
	if err != nil {
		return application.AcceptedNewInvitation{}, err
	}
	if refusal != nil {
		return application.AcceptedNewInvitation{}, refusal
	}
	return out, nil
}

func createInvitationAccountTx(ctx context.Context, q *sqlcgen.Queries, name, hash string) (uuid.UUID, string, error) {
	for range 5 {
		secret := make([]byte, 16)
		if _, err := rand.Read(secret); err != nil {
			return uuid.Nil, "", err
		}
		handle := domain.NormalizeUsername("k_" + hex.EncodeToString(secret))
		actorID, err := q.CreateLocalActorIfAvailable(ctx, sqlcgen.CreateLocalActorIfAvailableParams{
			IdentityIssuer: identity.LocalIssuer, IdentitySubject: handle, DisplayName: name,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return uuid.Nil, "", err
		}
		if err := createCredentialTx(ctx, q, actorID, application.NewAccount{
			Username: handle, DisplayName: name, PasswordHash: hash, MustChangePassword: false,
		}); err != nil {
			return uuid.Nil, "", err
		}
		return actorID, handle, nil
	}
	return uuid.Nil, "", errors.New("identity: login handle collision limit")
}

func (r *InvitationRepository) RecoverNewInvitation(ctx context.Context, proof application.InvitationProof, password string) (application.AcceptedNewInvitation, error) {
	var out application.AcceptedNewInvitation
	var refusal error
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: proof.TenantID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if _, err := q.LockActiveInvitationTenant(ctx, proof.TenantID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				domain.BurnPasswordTime(password)
				return application.ErrInvitationUnavailable
			}
			return err
		}
		row, err := q.LockInvitationForAccept(ctx, sqlcgen.LockInvitationForAcceptParams{TenantID: proof.TenantID, ID: proof.InvitationID})
		if errors.Is(err, pgx.ErrNoRows) {
			domain.BurnPasswordTime(password)
			return application.ErrInvitationUnavailable
		}
		if err != nil {
			return err
		}
		if !application.VerifyInvitationProof(proof.Code, row.ProofDigest) {
			domain.BurnPasswordTime(password)
			return application.ErrInvitationUnavailable
		}
		out, refusal, err = r.recoverAcceptedNew(ctx, tx, proof, row, password)
		return err
	})
	if err != nil {
		return application.AcceptedNewInvitation{}, err
	}
	if refusal != nil {
		return application.AcceptedNewInvitation{}, refusal
	}
	return out, nil
}

func (r *InvitationRepository) recoverAcceptedNew(ctx context.Context, tx pgx.Tx, proof application.InvitationProof, row sqlcgen.LockInvitationForAcceptRow, password string) (application.AcceptedNewInvitation, error, error) {
	if row.Status != "ACCEPTED" || row.AcceptedMode == nil || *row.AcceptedMode != "NEW" ||
		row.TerminalAt == nil || !time.Now().Before(row.TerminalAt.Add(24*time.Hour)) ||
		!row.AcceptedActorID.Valid || !row.AcceptedMembershipID.Valid {
		domain.BurnPasswordTime(password)
		return application.AcceptedNewInvitation{}, application.ErrInvitationUnavailable, nil
	}
	q := sqlcgen.New(tx)
	cred, err := q.LockInvitationRecoveryCredential(ctx, row.AcceptedActorID.UUID)
	if errors.Is(err, pgx.ErrNoRows) {
		domain.BurnPasswordTime(password)
		return application.AcceptedNewInvitation{}, application.ErrInvitationUnavailable, nil
	}
	if err != nil {
		return application.AcceptedNewInvitation{}, nil, err
	}
	if cred.Status != "ACTIVE" || (cred.LockedUntil != nil && time.Now().Before(*cred.LockedUntil)) {
		domain.BurnPasswordTime(password)
		return application.AcceptedNewInvitation{}, application.ErrInvitationUnavailable, nil
	}
	member, err := q.ValidateInvitationAcceptedMembership(ctx, sqlcgen.ValidateInvitationAcceptedMembershipParams{
		TenantID: proof.TenantID, ID: row.AcceptedMembershipID.UUID, ActorID: row.AcceptedActorID.UUID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		domain.BurnPasswordTime(password)
		return application.AcceptedNewInvitation{}, application.ErrInvitationUnavailable, nil
	}
	if err != nil {
		return application.AcceptedNewInvitation{}, nil, err
	}
	valid, ok := member.ValidToday.(bool)
	if member.MembershipStatus != "ACTIVE" || !ok || !valid {
		domain.BurnPasswordTime(password)
		return application.AcceptedNewInvitation{}, application.ErrInvitationUnavailable, nil
	}
	verified, _, err := domain.VerifyPassword(password, cred.PasswordHash, domain.DefaultPasswordParams())
	if err != nil {
		domain.BurnPasswordTime(password)
		return application.AcceptedNewInvitation{}, application.ErrInvitationUnavailable, nil
	}
	if !verified {
		lockedUntil := time.Now().Add(domain.DefaultLockout().Duration)
		if err := q.RegisterLoginFailure(ctx, sqlcgen.RegisterLoginFailureParams{
			ActorID: row.AcceptedActorID.UUID, FailedAttempts: int32(domain.DefaultLockout().MaxFailedAttempts), LockedUntil: &lockedUntil, //nolint:gosec // DefaultLockout is a fixed 10-attempt policy.
		}); err != nil {
			return application.AcceptedNewInvitation{}, nil, err
		}
		return application.AcceptedNewInvitation{}, application.ErrInvitationUnavailable, nil
	}
	if err := q.ClearInvitationCredentialFailures(ctx, row.AcceptedActorID.UUID); err != nil {
		return application.AcceptedNewInvitation{}, nil, err
	}
	pending, err := invitationAccessPending(ctx, tx, proof.TenantID, row.AcceptedMembershipID.UUID)
	if err != nil {
		return application.AcceptedNewInvitation{}, nil, err
	}
	return application.AcceptedNewInvitation{AcceptedInvitation: application.AcceptedInvitation{
		TenantID: proof.TenantID, TenantDisplayName: row.DisplayName,
		MembershipID: row.AcceptedMembershipID.UUID, AccessPending: pending,
	}, LoginHandle: cred.IdentitySubject, RecoveryExpiresAt: row.TerminalAt.Add(24 * time.Hour)}, nil, nil
}
