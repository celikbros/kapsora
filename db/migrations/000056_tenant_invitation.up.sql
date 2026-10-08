-- MGT-02b1: consent-based invitations for existing KAPSORA accounts.
-- Contact and delivery secrets are encrypted with separate tenant-bound purposes.
CREATE TABLE iam.tenant_invitation (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    contact_cipher           bytea,
    contact_hash             bytea CHECK (contact_hash IS NULL OR octet_length(contact_hash) = 32),
    masked_recipient         text,
    proof_digest             bytea CHECK (proof_digest IS NULL OR octet_length(proof_digest) = 32),
    delivery_cipher          bytea,
    delivery_generation      integer NOT NULL DEFAULT 1 CHECK (delivery_generation > 0),
    delivery_status          text NOT NULL DEFAULT 'QUEUED'
                             CHECK (delivery_status IN ('QUEUED','SENT','FAILED','CANCELLED')),
    status                   text NOT NULL DEFAULT 'PENDING'
                             CHECK (status IN ('PENDING','ACCEPTED','CANCELLED','EXPIRED')),
    accepted_actor_id        uuid REFERENCES iam.actor(id) ON DELETE RESTRICT,
    accepted_membership_id   uuid,
    accept_key               text CHECK (accept_key IS NULL OR char_length(accept_key) BETWEEN 16 AND 128),
    created_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at               timestamptz NOT NULL,
    terminal_at              timestamptz,
    row_version              bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_tenant_invitation_id UNIQUE (tenant_id, id),
    CONSTRAINT fk_tenant_invitation_accepted_membership
        FOREIGN KEY (tenant_id, accepted_membership_id)
        REFERENCES iam.tenant_membership(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_tenant_invitation_contact_state CHECK (
        (status = 'PENDING' AND contact_cipher IS NOT NULL AND contact_hash IS NOT NULL
         AND proof_digest IS NOT NULL AND terminal_at IS NULL)
        OR (status <> 'PENDING' AND contact_cipher IS NULL AND contact_hash IS NULL
            AND delivery_cipher IS NULL AND terminal_at IS NOT NULL)),
    CONSTRAINT ck_tenant_invitation_accept_state CHECK (
        (status = 'ACCEPTED' AND accepted_actor_id IS NOT NULL AND accepted_membership_id IS NOT NULL)
        OR (status <> 'ACCEPTED' AND accepted_actor_id IS NULL AND accepted_membership_id IS NULL))
);
CREATE UNIQUE INDEX uq_tenant_invitation_pending_contact
    ON iam.tenant_invitation (tenant_id, contact_hash) WHERE status = 'PENDING';
CREATE INDEX ix_tenant_invitation_page ON iam.tenant_invitation (tenant_id, created_at DESC, id DESC);
CREATE INDEX ix_tenant_invitation_expiry ON iam.tenant_invitation (tenant_id, expires_at)
    WHERE status = 'PENDING';
CREATE INDEX ix_tenant_invitation_terminal ON iam.tenant_invitation (tenant_id, terminal_at)
    WHERE status <> 'PENDING';
SELECT platform.attach_touch_row('iam.tenant_invitation');
SELECT platform.enable_tenant_rls('iam.tenant_invitation'::regclass);

-- This receipt has a tenant-keyed HMAC fingerprint; no raw or unkeyed email digest is stored.
CREATE TABLE iam.tenant_invitation_create_receipt (
    tenant_id       uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    actor_id        uuid NOT NULL REFERENCES iam.actor(id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 16 AND 128),
    fingerprint     bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    invitation_id   uuid NOT NULL,
    response_json   jsonb NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, actor_id, idempotency_key),
    CONSTRAINT fk_tenant_invitation_create_receipt_invitation
        FOREIGN KEY (tenant_id, invitation_id)
        REFERENCES iam.tenant_invitation(tenant_id, id) ON DELETE RESTRICT
);
SELECT platform.enable_tenant_rls('iam.tenant_invitation_create_receipt'::regclass);
SELECT platform.grant_app_schema_usage('iam');
