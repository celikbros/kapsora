-- Invitation projections are intentionally restricted to manager-safe fields.
-- name: GetTenantInvitationSummary :one
SELECT id, masked_recipient, status, created_at, expires_at, row_version, delivery_status
  FROM iam.tenant_invitation
 WHERE tenant_id = $1 AND id = $2;

-- name: ListTenantInvitationSummaries :many
SELECT id, masked_recipient, status, created_at, expires_at, row_version, delivery_status
  FROM iam.tenant_invitation
 WHERE tenant_id = $1
   AND (sqlc.narg('invitation_status')::text IS NULL OR status = sqlc.narg('invitation_status'))
   AND (sqlc.narg('after_at')::timestamptz IS NULL
        OR (created_at,id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY created_at DESC, id DESC
 LIMIT sqlc.arg('page_limit')::bigint;

-- name: GetInvitationCreateReceipt :one
SELECT fingerprint,response_json,created_at FROM iam.tenant_invitation_create_receipt
 WHERE tenant_id=$1 AND actor_id=$2 AND idempotency_key=$3;

-- name: InsertInvitationCreateReceipt :exec
INSERT INTO iam.tenant_invitation_create_receipt
 (tenant_id,actor_id,idempotency_key,fingerprint,invitation_id,response_json)
 VALUES ($1,$2,$3,$4,$5,$6);

-- name: DeleteInvitationCreateReceipt :exec
DELETE FROM iam.tenant_invitation_create_receipt
 WHERE tenant_id=$1 AND actor_id=$2 AND idempotency_key=$3;

-- name: ExpireMatchingPendingInvitation :exec
UPDATE iam.tenant_invitation AS target SET status='EXPIRED',terminal_at=clock_timestamp(),
 contact_cipher=NULL,contact_hash=NULL,proof_digest=NULL,delivery_cipher=NULL,
 delivery_status=CASE WHEN delivery_status='SENT' THEN 'SENT' ELSE 'CANCELLED' END
 WHERE tenant_id=$1 AND contact_hash=$2 AND status='PENDING' AND expires_at<=clock_timestamp();

-- name: PendingInvitationExists :one
SELECT EXISTS(SELECT 1 FROM iam.tenant_invitation
 WHERE tenant_id=$1 AND contact_hash=$2 AND status='PENDING');

-- name: InsertTenantInvitation :one
INSERT INTO iam.tenant_invitation
 (id,tenant_id,contact_cipher,contact_hash,masked_recipient,proof_digest,delivery_cipher,expires_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,clock_timestamp()+interval '48 hours')
 RETURNING id, masked_recipient, status, created_at, expires_at, row_version, delivery_status;

-- name: LockInvitationForCancel :one
SELECT status,row_version FROM iam.tenant_invitation
 WHERE tenant_id=$1 AND id=$2 FOR UPDATE;

-- name: CancelTenantInvitation :one
UPDATE iam.tenant_invitation SET status='CANCELLED',terminal_at=clock_timestamp(),
 contact_cipher=NULL,contact_hash=NULL,proof_digest=NULL,delivery_cipher=NULL,
 delivery_status=CASE WHEN delivery_status='SENT' THEN 'SENT' ELSE 'CANCELLED' END
 WHERE tenant_id=$1 AND id=$2
 RETURNING id,masked_recipient,status,created_at,expires_at,row_version,delivery_status;

-- name: ActiveInvitationActor :one
SELECT EXISTS(SELECT 1 FROM iam.actor WHERE id=$1 AND status='ACTIVE');

-- name: InspectInvitationProof :one
SELECT i.proof_digest,i.status,i.expires_at,t.display_name,i.accepted_actor_id,i.terminal_at,i.accepted_mode
 FROM iam.tenant_invitation i JOIN platform.tenant t ON t.id=i.tenant_id
 WHERE i.tenant_id=$1 AND i.id=$2 AND t.status='ACTIVE';

-- name: LockActiveInvitationTenant :one
SELECT id FROM platform.tenant WHERE id=$1 AND status='ACTIVE' FOR UPDATE;

-- name: LockInvitationForAccept :one
SELECT i.proof_digest,i.status,i.expires_at,COALESCE(i.accept_key,'') AS accept_key,
 i.accepted_actor_id,i.accepted_membership_id,t.display_name,i.terminal_at,
 i.accepted_mode,i.accept_new_fingerprint
 FROM iam.tenant_invitation i JOIN platform.tenant t ON t.id=i.tenant_id
 WHERE i.tenant_id=$1 AND i.id=$2 FOR UPDATE OF i;

-- name: FindInvitationMembership :one
SELECT id,membership_status,valid_period @> CURRENT_DATE AS valid_today
 FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2
 ORDER BY created_at DESC,id DESC LIMIT 1 FOR UPDATE;

-- name: CreateInvitationMembership :one
INSERT INTO iam.tenant_membership (tenant_id,actor_id,membership_status,created_by)
 VALUES ($1,$2,'ACTIVE',$2) RETURNING id;

-- name: InvitationHasAccess :one
SELECT EXISTS (
 SELECT 1 FROM iam.access_grant g JOIN iam.role_permission rp
 ON rp.tenant_id=g.tenant_id AND rp.role_id=g.role_id
 WHERE g.tenant_id=$1 AND g.tenant_membership_id=$2
 AND g.valid_period @> clock_timestamp());

-- name: AcceptTenantInvitation :exec
UPDATE iam.tenant_invitation SET status='ACCEPTED',
 accepted_actor_id=$3,accepted_membership_id=$4,accept_key=$5,accepted_mode='EXISTING',terminal_at=clock_timestamp(),
 contact_cipher=NULL,contact_hash=NULL,delivery_cipher=NULL,
 delivery_status=CASE WHEN delivery_status='SENT' THEN 'SENT' ELSE 'CANCELLED' END
 WHERE tenant_id=$1 AND id=$2;

-- name: LockInvitationForDelivery :one
SELECT status,delivery_status,delivery_generation,expires_at,delivery_cipher
 FROM iam.tenant_invitation WHERE tenant_id=$1 AND id=$2 FOR UPDATE;

-- name: FailInvitationDelivery :exec
UPDATE iam.tenant_invitation SET delivery_status='FAILED',delivery_cipher=NULL WHERE tenant_id=$1 AND id=$2;

-- name: CompleteInvitationDelivery :exec
UPDATE iam.tenant_invitation SET delivery_status='SENT',delivery_cipher=NULL WHERE tenant_id=$1 AND id=$2;

-- name: ListInvitationTenantIDs :many
SELECT id FROM platform.tenant ORDER BY id;

-- name: ExpireInvitationBatch :execrows
UPDATE iam.tenant_invitation AS target SET status='EXPIRED',terminal_at=clock_timestamp(),
 contact_cipher=NULL,contact_hash=NULL,proof_digest=NULL,delivery_cipher=NULL,
 delivery_status=CASE WHEN delivery_status='SENT' THEN 'SENT' ELSE 'CANCELLED' END
 WHERE target.id IN (SELECT i.id FROM iam.tenant_invitation i WHERE i.tenant_id=$1 AND i.status='PENDING'
 AND i.expires_at<=clock_timestamp() ORDER BY i.expires_at,i.id LIMIT 100 FOR UPDATE SKIP LOCKED);

-- name: PurgeInvitationMaskBatch :execrows
UPDATE iam.tenant_invitation AS target SET masked_recipient=NULL
 WHERE target.id IN (SELECT i.id FROM iam.tenant_invitation i WHERE i.tenant_id=$1 AND i.status<>'PENDING'
 AND i.masked_recipient IS NOT NULL AND i.terminal_at<clock_timestamp()-interval '30 days'
 ORDER BY i.terminal_at,i.id LIMIT 100 FOR UPDATE SKIP LOCKED);

-- name: PurgeInvitationProofBatch :execrows
UPDATE iam.tenant_invitation AS target SET proof_digest=NULL,accept_key=NULL,accept_new_fingerprint=NULL
 WHERE target.id IN (SELECT i.id FROM iam.tenant_invitation i WHERE i.tenant_id=$1 AND i.status='ACCEPTED'
 AND i.proof_digest IS NOT NULL AND i.terminal_at<clock_timestamp()-interval '24 hours'
 ORDER BY i.terminal_at,i.id LIMIT 100 FOR UPDATE SKIP LOCKED);

-- name: PurgeInvitationCreateReceiptBatch :execrows
DELETE FROM iam.tenant_invitation_create_receipt AS target
 WHERE (target.tenant_id,target.actor_id,target.idempotency_key) IN (
 SELECT i.tenant_id,i.actor_id,i.idempotency_key FROM iam.tenant_invitation_create_receipt i
 WHERE i.tenant_id=$1 AND i.created_at<clock_timestamp()-interval '24 hours'
 ORDER BY i.created_at,i.invitation_id LIMIT 100 FOR UPDATE SKIP LOCKED);

-- name: AcceptNewTenantInvitation :one
UPDATE iam.tenant_invitation SET status='ACCEPTED', accepted_mode='NEW',
 accepted_actor_id=$3,accepted_membership_id=$4,accept_key=$5,
 accept_new_fingerprint=$6,terminal_at=clock_timestamp(),
 contact_cipher=NULL,contact_hash=NULL,delivery_cipher=NULL,
 delivery_status=CASE WHEN delivery_status='SENT' THEN 'SENT' ELSE 'CANCELLED' END
 WHERE tenant_id=$1 AND id=$2
 RETURNING terminal_at;

-- name: LockInvitationRecoveryCredential :one
SELECT a.identity_subject,a.status,c.password_hash,c.failed_attempts,c.locked_until
 FROM iam.actor a JOIN iam.credential c ON c.actor_id=a.id
 WHERE a.id=$1 FOR UPDATE OF a,c;

-- name: ClearInvitationCredentialFailures :exec
UPDATE iam.credential SET failed_attempts=0,locked_until=NULL WHERE actor_id=$1;

-- name: ValidateInvitationAcceptedMembership :one
SELECT membership_status,valid_period @> CURRENT_DATE AS valid_today
 FROM iam.tenant_membership
 WHERE tenant_id=$1 AND id=$2 AND actor_id=$3 FOR UPDATE;
