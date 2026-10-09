-- B2 keeps the accepted B1 outcome while distinguishing its replay authority.
ALTER TABLE iam.tenant_invitation
    ADD COLUMN accepted_mode text,
    ADD COLUMN accept_new_fingerprint bytea;

UPDATE iam.tenant_invitation SET accepted_mode = 'EXISTING' WHERE status = 'ACCEPTED';

ALTER TABLE iam.tenant_invitation
    ADD CONSTRAINT ck_tenant_invitation_accepted_mode CHECK (
        (status = 'ACCEPTED' AND accepted_mode IS NOT NULL AND accepted_mode IN ('EXISTING', 'NEW')) OR
        (status <> 'ACCEPTED' AND accepted_mode IS NULL)
    ),
    ADD CONSTRAINT ck_tenant_invitation_new_fingerprint CHECK (
        (accepted_mode = 'NEW' AND (accept_new_fingerprint IS NULL OR octet_length(accept_new_fingerprint) = 32)) OR
        (accepted_mode IS DISTINCT FROM 'NEW' AND accept_new_fingerprint IS NULL)
    );
