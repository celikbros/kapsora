package identitypg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/mail"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

type InvitationRepository struct {
	pool            *pgxpool.Pool
	cipher          crypto.FieldCipher
	index           crypto.BlindIndexer
	audit           audit.Recorder
	deliveryEnabled bool
}

func NewInvitationRepository(pool *pgxpool.Pool, cipher crypto.FieldCipher, index crypto.BlindIndexer, recorder ...audit.Recorder) *InvitationRepository {
	selected := audit.Recorder(auditpg.New())
	if len(recorder) > 0 && recorder[0] != nil {
		selected = recorder[0]
	}
	return &InvitationRepository{pool: pool, cipher: cipher, index: index, audit: selected}
}

func (r *InvitationRepository) WithDeliveryEnabled(enabled bool) *InvitationRepository {
	r.deliveryEnabled = enabled
	return r
}

var _ application.InvitationRepository = (*InvitationRepository)(nil)

func invitationSummary(id uuid.UUID, masked *string, status string, createdAt, expiresAt time.Time, version int64, deliveryStatus string) application.TenantInvitation {
	out := application.TenantInvitation{ID: id, Status: status, CreatedAt: createdAt,
		ExpiresAt: expiresAt, RowVersion: version, DeliveryStatus: deliveryStatus}
	if masked != nil {
		out.MaskedRecipient = *masked
	}
	return out
}

func loadInvitation(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.TenantInvitation, error) {
	row, err := sqlcgen.New(tx).GetTenantInvitationSummary(ctx, sqlcgen.GetTenantInvitationSummaryParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TenantInvitation{}, application.ErrInvitationNotFound
	}
	if err != nil {
		return application.TenantInvitation{}, err
	}
	return invitationSummary(row.ID, row.MaskedRecipient, row.Status, row.CreatedAt, row.ExpiresAt, row.RowVersion, row.DeliveryStatus), nil
}

func (r *InvitationRepository) ListInvitations(ctx context.Context, rc identity.RequestContext, filter application.InvitationFilter) ([]application.TenantInvitation, error) {
	if filter.Limit < 1 || filter.Limit > 101 {
		return nil, errors.New("identity: invalid invitation page limit")
	}
	items := make([]application.TenantInvitation, 0)
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		if err := authorizeDirectory(ctx, sqlcgen.New(tx), rc); err != nil {
			return err
		}
		var afterID uuid.NullUUID
		if filter.AfterAt != nil {
			afterID = uuid.NullUUID{UUID: filter.AfterID, Valid: true}
		}
		rows, err := sqlcgen.New(tx).ListTenantInvitationSummaries(ctx, sqlcgen.ListTenantInvitationSummariesParams{
			TenantID: rc.TenantID, InvitationStatus: nullableStatus(filter.Status),
			AfterAt: filter.AfterAt, AfterID: afterID, PageLimit: int64(filter.Limit)})
		if err != nil {
			return fmt.Errorf("identity: list invitations: %w", err)
		}
		for _, row := range rows {
			items = append(items, invitationSummary(row.ID, row.MaskedRecipient, row.Status, row.CreatedAt, row.ExpiresAt, row.RowVersion, row.DeliveryStatus))
		}
		return nil
	})
	return items, err
}

func (r *InvitationRepository) GetInvitation(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (application.TenantInvitation, error) {
	var item application.TenantInvitation
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		if err := authorizeDirectory(ctx, sqlcgen.New(tx), rc); err != nil {
			return err
		}
		var err error
		item, err = loadInvitation(ctx, tx, rc.TenantID, id)
		return err
	})
	return item, err
}

func (r *InvitationRepository) AuthorizeInvitationManage(ctx context.Context, rc identity.RequestContext) error {
	return db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		return authorizeManageDirectory(ctx, sqlcgen.New(tx), rc)
	})
}

func (r *InvitationRepository) CreateInvitation(ctx context.Context, rc identity.RequestContext, email, key string) (application.TenantInvitation, error) {
	if !r.deliveryEnabled {
		return application.TenantInvitation{}, application.ErrInvitationDeliveryDisabled
	}
	var out application.TenantInvitation
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if _, err := q.LockActiveInvitationTenant(ctx, rc.TenantID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return identity.ErrPermissionDenied
			}
			return err
		}
		if err := authorizeManageDirectory(ctx, q, rc); err != nil {
			return err
		}
		fingerprint, err := r.index.TenantIndex(ctx, rc.TenantID, crypto.PurposeInvitationCommand, "v1|create|"+email)
		if err != nil {
			return err
		}
		receipt, err := q.GetInvitationCreateReceipt(ctx, sqlcgen.GetInvitationCreateReceiptParams{
			TenantID: rc.TenantID, ActorID: rc.Principal.ActorID, IdempotencyKey: key})
		if err == nil {
			if !time.Now().Before(receipt.CreatedAt.Add(24 * time.Hour)) {
				if err := q.DeleteInvitationCreateReceipt(ctx, sqlcgen.DeleteInvitationCreateReceiptParams{
					TenantID: rc.TenantID, ActorID: rc.Principal.ActorID, IdempotencyKey: key}); err != nil {
					return err
				}
			} else {
				if !bytes.Equal(fingerprint, receipt.Fingerprint) {
					return application.ErrInvitationKeyReused
				}
				return json.Unmarshal(receipt.ResponseJson, &out)
			}
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		contactHash, err := r.index.TenantIndex(ctx, rc.TenantID, crypto.PurposeInvitationContact, email)
		if err != nil {
			return err
		}
		// A real-time partial index is impossible. Expire the matching stale row while
		// holding the tenant management lock, then rely on the unique pending index.
		err = q.ExpireMatchingPendingInvitation(ctx, sqlcgen.ExpireMatchingPendingInvitationParams{TenantID: rc.TenantID, ContactHash: contactHash})
		if err != nil {
			return err
		}
		exists, err := q.PendingInvitationExists(ctx, sqlcgen.PendingInvitationExistsParams{TenantID: rc.TenantID, ContactHash: contactHash})
		if err != nil {
			return err
		}
		if exists {
			return application.ErrInvitationPendingExists
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		code, digest, err := application.GenerateInvitationCode(rc.TenantID, id)
		if err != nil {
			return err
		}
		contactCipher, err := r.cipher.Encrypt(ctx, rc.TenantID, crypto.PurposeInvitationContact, []byte(email))
		if err != nil {
			return err
		}
		payload, err := json.Marshal(struct {
			To   string `json:"to"`
			Code string `json:"code"`
		}{email, code})
		if err != nil {
			return err
		}
		deliveryCipher, err := r.cipher.Encrypt(ctx, rc.TenantID, crypto.PurposeInvitationDelivery, payload)
		if err != nil {
			return err
		}
		masked := application.MaskInvitationEmail(email)
		inserted, err := q.InsertTenantInvitation(ctx, sqlcgen.InsertTenantInvitationParams{
			ID: id, TenantID: rc.TenantID, ContactCipher: contactCipher, ContactHash: contactHash,
			MaskedRecipient: &masked, ProofDigest: digest, DeliveryCipher: deliveryCipher})
		if err != nil {
			return err
		}
		out = invitationSummary(inserted.ID, inserted.MaskedRecipient, inserted.Status, inserted.CreatedAt, inserted.ExpiresAt, inserted.RowVersion, inserted.DeliveryStatus)
		responseJSON, err := json.Marshal(out)
		if err != nil {
			return err
		}
		err = q.InsertInvitationCreateReceipt(ctx, sqlcgen.InsertInvitationCreateReceiptParams{
			TenantID: rc.TenantID, ActorID: rc.Principal.ActorID, IdempotencyKey: key,
			Fingerprint: fingerprint, InvitationID: id, ResponseJson: responseJSON})
		if err != nil {
			return err
		}
		_, _, err = outbox.Publish(ctx, tx, outbox.Event{TenantID: uuid.NullUUID{UUID: rc.TenantID, Valid: true},
			AggregateType: "tenant_invitation", AggregateID: id, Type: application.InvitationDeliveryEvent,
			Payload: struct {
				InvitationID uuid.UUID `json:"invitationId"`
				Generation   int       `json:"generation"`
			}{id, 1},
			DeduplicationKey: id.String() + ":1"})
		if err != nil {
			return err
		}
		return r.audit.Record(ctx, tx, audit.Event{TenantID: uuid.NullUUID{UUID: rc.TenantID, Valid: true},
			ActorID: uuid.NullUUID{UUID: rc.Principal.ActorID, Valid: true}, MembershipID: uuid.NullUUID{UUID: rc.MembershipID, Valid: true},
			Category: audit.CategoryAdmin, ActionCode: "tenant_invitation.create", ResourceType: "tenant_invitation",
			ResourceID: uuid.NullUUID{UUID: id, Valid: true}, Outcome: audit.OutcomeSuccess,
			Detail: map[string]any{"status": "PENDING"}})
	})
	return out, err
}

func (r *InvitationRepository) CancelInvitation(ctx context.Context, rc identity.RequestContext, id uuid.UUID, version int64) (application.TenantInvitation, error) {
	var out application.TenantInvitation
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if _, err := q.LockActiveInvitationTenant(ctx, rc.TenantID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return identity.ErrPermissionDenied
			}
			return err
		}
		if err := authorizeManageDirectory(ctx, q, rc); err != nil {
			return err
		}
		locked, err := q.LockInvitationForCancel(ctx, sqlcgen.LockInvitationForCancelParams{TenantID: rc.TenantID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrInvitationNotFound
		}
		if err != nil {
			return err
		}
		if locked.RowVersion != version {
			return application.ErrInvitationVersionConflict
		}
		if locked.Status != "PENDING" {
			return application.ErrInvitationStateConflict
		}
		updated, err := q.CancelTenantInvitation(ctx, sqlcgen.CancelTenantInvitationParams{TenantID: rc.TenantID, ID: id})
		if err != nil {
			return err
		}
		out = invitationSummary(updated.ID, updated.MaskedRecipient, updated.Status, updated.CreatedAt, updated.ExpiresAt, updated.RowVersion, updated.DeliveryStatus)
		return r.audit.Record(ctx, tx, audit.Event{TenantID: uuid.NullUUID{UUID: rc.TenantID, Valid: true},
			ActorID: uuid.NullUUID{UUID: rc.Principal.ActorID, Valid: true}, MembershipID: uuid.NullUUID{UUID: rc.MembershipID, Valid: true},
			Category: audit.CategoryAdmin, ActionCode: "tenant_invitation.cancel", ResourceType: "tenant_invitation",
			ResourceID: uuid.NullUUID{UUID: id, Valid: true}, Outcome: audit.OutcomeSuccess,
			Detail: map[string]any{"old_status": "PENDING", "new_status": "CANCELLED"}})
	})
	return out, err
}

func invitationActorActive(ctx context.Context, tx pgx.Tx, actorID uuid.UUID) (bool, error) {
	return sqlcgen.New(tx).ActiveInvitationActor(ctx, actorID)
}

func (r *InvitationRepository) InspectInvitation(ctx context.Context, actorID uuid.UUID, proof application.InvitationProof) (application.InspectedInvitation, error) {
	var out application.InspectedInvitation
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: proof.TenantID, ActorID: actorID}, func(ctx context.Context, tx pgx.Tx) error {
		active, err := invitationActorActive(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !active {
			return application.ErrInvitationUnavailable
		}
		row, err := sqlcgen.New(tx).InspectInvitationProof(ctx, sqlcgen.InspectInvitationProofParams{TenantID: proof.TenantID, ID: proof.InvitationID})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrInvitationUnavailable
		}
		if err != nil {
			return err
		}
		if !application.VerifyInvitationProof(proof.Code, row.ProofDigest) {
			return application.ErrInvitationUnavailable
		}
		out.TenantDisplayName = row.DisplayName
		out.ExpiresAt = row.ExpiresAt
		if row.Status == "ACCEPTED" && row.AcceptedActorID.Valid && row.AcceptedActorID.UUID == actorID && row.TerminalAt != nil && time.Now().Before(row.TerminalAt.Add(24*time.Hour)) {
			out.Status = row.Status
			return r.audit.Record(ctx, tx, audit.Event{TenantID: uuid.NullUUID{UUID: proof.TenantID, Valid: true},
				ActorID: uuid.NullUUID{UUID: actorID, Valid: true}, Category: audit.CategorySecurity,
				ActionCode: "tenant_invitation.inspect", ResourceType: "tenant_invitation",
				ResourceID: uuid.NullUUID{UUID: proof.InvitationID, Valid: true}, Outcome: audit.OutcomeSuccess,
				Detail: map[string]any{"status": row.Status}})
		}
		if row.Status != "PENDING" || !time.Now().Before(out.ExpiresAt) {
			return application.ErrInvitationUnavailable
		}
		out.Status = row.Status
		return r.audit.Record(ctx, tx, audit.Event{TenantID: uuid.NullUUID{UUID: proof.TenantID, Valid: true},
			ActorID: uuid.NullUUID{UUID: actorID, Valid: true}, Category: audit.CategorySecurity,
			ActionCode: "tenant_invitation.inspect", ResourceType: "tenant_invitation",
			ResourceID: uuid.NullUUID{UUID: proof.InvitationID, Valid: true}, Outcome: audit.OutcomeSuccess,
			Detail: map[string]any{"status": row.Status}})
	})
	return out, err
}

func (r *InvitationRepository) AcceptExistingInvitation(ctx context.Context, actorID uuid.UUID, proof application.InvitationProof, key string) (application.AcceptedInvitation, error) {
	out := application.AcceptedInvitation{TenantID: proof.TenantID}
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: proof.TenantID, ActorID: actorID}, func(ctx context.Context, tx pgx.Tx) error {
		// Serializes concurrent joins and membership state changes in this tenant.
		q := sqlcgen.New(tx)
		if _, err := q.LockActiveInvitationTenant(ctx, proof.TenantID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return application.ErrInvitationUnavailable
			}
			return err
		}
		active, err := invitationActorActive(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !active {
			return application.ErrInvitationUnavailable
		}
		row, err := q.LockInvitationForAccept(ctx, sqlcgen.LockInvitationForAcceptParams{TenantID: proof.TenantID, ID: proof.InvitationID})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrInvitationUnavailable
		}
		if err != nil {
			return err
		}
		if !application.VerifyInvitationProof(proof.Code, row.ProofDigest) {
			return application.ErrInvitationUnavailable
		}
		out.TenantDisplayName = row.DisplayName
		if row.Status == "ACCEPTED" && row.AcceptedActorID.Valid && row.AcceptedActorID.UUID == actorID && row.TerminalAt != nil && time.Now().Before(row.TerminalAt.Add(24*time.Hour)) {
			if row.AcceptKey != key {
				return application.ErrInvitationKeyReused
			}
			out.MembershipID = row.AcceptedMembershipID.UUID
			out.AccessPending, err = invitationAccessPending(ctx, tx, proof.TenantID, out.MembershipID)
			return err
		}
		if row.Status != "PENDING" || !time.Now().Before(row.ExpiresAt) {
			return application.ErrInvitationUnavailable
		}
		member, err := q.FindInvitationMembership(ctx, sqlcgen.FindInvitationMembershipParams{TenantID: proof.TenantID, ActorID: actorID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		valid, ok := member.ValidToday.(bool)
		if err == nil && (member.MembershipStatus != "ACTIVE" || !ok || !valid) {
			return application.ErrInvitationMembershipConflict
		}
		if errors.Is(err, pgx.ErrNoRows) {
			member.ID, err = q.CreateInvitationMembership(ctx, sqlcgen.CreateInvitationMembershipParams{TenantID: proof.TenantID, ActorID: actorID})
			if err != nil {
				return err
			}
		}
		out.MembershipID = member.ID
		out.AccessPending, err = invitationAccessPending(ctx, tx, proof.TenantID, member.ID)
		if err != nil {
			return err
		}
		err = q.AcceptTenantInvitation(ctx, sqlcgen.AcceptTenantInvitationParams{
			TenantID: proof.TenantID, ID: proof.InvitationID, AcceptedActorID: uuid.NullUUID{UUID: actorID, Valid: true},
			AcceptedMembershipID: uuid.NullUUID{UUID: member.ID, Valid: true}, AcceptKey: &key})
		if err != nil {
			return err
		}
		return r.audit.Record(ctx, tx, audit.Event{TenantID: uuid.NullUUID{UUID: proof.TenantID, Valid: true},
			ActorID: uuid.NullUUID{UUID: actorID, Valid: true}, Category: audit.CategoryAdmin,
			ActionCode: "tenant_invitation.accept_existing", ResourceType: "tenant_invitation",
			ResourceID: uuid.NullUUID{UUID: proof.InvitationID, Valid: true}, Outcome: audit.OutcomeSuccess,
			Detail: map[string]any{"old_status": "PENDING", "new_status": "ACCEPTED"}})
	})
	return out, err
}

func invitationAccessPending(ctx context.Context, tx pgx.Tx, tenantID, memberID uuid.UUID) (bool, error) {
	granted, err := sqlcgen.New(tx).InvitationHasAccess(ctx, sqlcgen.InvitationHasAccessParams{TenantID: tenantID, TenantMembershipID: memberID})
	return !granted, err
}

// DeliverInvitation is identity-owned: the ordinary notification pipeline never
// receives the secret. A sender is injected so tests can use a fake SMTP endpoint.
func (r *InvitationRepository) DeliverInvitation(ctx context.Context, delivery outbox.Delivery, sender mail.Sender, linkBase string, enabled bool) error {
	if !enabled {
		return outbox.Transient(errors.New("invitation delivery disabled"))
	}
	if sender == nil {
		return outbox.Transient(errors.New("invitation sender unavailable"))
	}
	if !delivery.TenantID.Valid || delivery.TenantID.UUID == uuid.Nil {
		return outbox.Permanent(errors.New("invitation tenant absent"))
	}
	var payload struct {
		InvitationID uuid.UUID `json:"invitationId"`
		Generation   int       `json:"generation"`
	}
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil || payload.InvitationID != delivery.AggregateID || payload.Generation < 1 {
		return outbox.Permanent(errors.New("invitation delivery selector invalid"))
	}
	tenantID := delivery.TenantID.UUID
	return db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, err := q.LockInvitationForDelivery(ctx, sqlcgen.LockInvitationForDeliveryParams{TenantID: tenantID, ID: payload.InvitationID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.Status != "PENDING" || !time.Now().Before(row.ExpiresAt) || int64(row.DeliveryGeneration) != int64(payload.Generation) || row.DeliveryStatus == "SENT" || len(row.DeliveryCipher) == 0 {
			return nil
		}
		plain, err := r.cipher.Decrypt(ctx, tenantID, crypto.PurposeInvitationDelivery, row.DeliveryCipher)
		if err != nil {
			return outbox.Security(errors.New("invitation delivery envelope invalid"))
		}
		var message struct {
			To   string `json:"to"`
			Code string `json:"code"`
		}
		if err := json.Unmarshal(plain, &message); err != nil {
			return outbox.Security(errors.New("invitation delivery envelope invalid"))
		}
		if message.To == "" || message.Code == "" {
			return outbox.Security(errors.New("invitation delivery envelope invalid"))
		}
		_, err = sender.Send(ctx, mail.Message{To: message.To,
			Subject: "KAPSORA kurum daveti",
			Body:    "KAPSORA kurum davetinizi incelemek için " + linkBase + "/invitation bağlantısını açın ve bu kodu girin:\n\n" + message.Code + "\n\nKod 48 saat geçerlidir."})
		if err != nil {
			if errors.Is(err, mail.ErrRejected) {
				return q.FailInvitationDelivery(ctx, sqlcgen.FailInvitationDeliveryParams{TenantID: tenantID, ID: payload.InvitationID})
			}
			return outbox.Transient(errors.New("invitation delivery temporarily unavailable"))
		}
		return q.CompleteInvitationDelivery(ctx, sqlcgen.CompleteInvitationDeliveryParams{TenantID: tenantID, ID: payload.InvitationID})
	})
}

// CleanupInvitations uses bounded tenant transactions because invitation rows are
// FORCE RLS protected. It removes secrets on expiry and masked displays after 30 days.
func (r *InvitationRepository) CleanupInvitations(ctx context.Context) (int64, error) {
	tenantIDs, err := sqlcgen.New(r.pool).ListInvitationTenantIDs(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, tenantID := range tenantIDs {
		err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
			q := sqlcgen.New(tx)
			n, err := q.ExpireInvitationBatch(ctx, tenantID)
			if err != nil {
				return err
			}
			total += n
			n, err = q.PurgeInvitationMaskBatch(ctx, tenantID)
			if err != nil {
				return err
			}
			total += n
			n, err = q.PurgeInvitationProofBatch(ctx, tenantID)
			if err != nil {
				return err
			}
			total += n
			n, err = q.PurgeInvitationCreateReceiptBatch(ctx, tenantID)
			if err != nil {
				return err
			}
			total += n
			return nil
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
