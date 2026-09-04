-- 000028: documents — where a file is, what it is, who may see it and what the scanner
-- said (WP-I4-04, v1.2 9.15, 19.x, 39.14, 11.10).
--
-- The one property that shapes every table below: **a file is never stored in the
-- database**. There is no bytea column here that could hold a body, and the only one that
-- exists at all is a 32-byte digest whose length is CHECKed, so a file cannot be smuggled
-- into it. The bytes live in the object store; these rows say which bucket and key they
-- are under, what the scanner decided about them, and who may reach them.
--
-- A file arrives in `quarantine` and is scanned there. Only a CLEAN verdict copies it to
-- `secure` and deletes the quarantine copy, so an infected file leaves no bytes anywhere
-- and the row survives to say the incident happened. `bucket` is therefore not decoration:
-- it is the single column that says which of those two worlds an object is currently in,
-- and the CHECK below is what makes "it is in secure but was never scanned" unrepresentable.

CREATE SCHEMA IF NOT EXISTS document;

-- document.upload, document.read and document.download.sensitive are already in the
-- catalogue (migration 000008). These two are new: binding a document to a record is a
-- different act from uploading one, and a legal hold is the one thing in the product that
-- overrides retention, so it is PRIVILEGED.
INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('document.link',              'Belgeyi bir kayda bağlama ve bağı kaldırma', 'NORMAL'),
    ('document.legal_hold.manage', 'Belgeye hukuki saklama koyma ve kaldırma',   'PRIVILEGED')
ON CONFLICT (code) DO NOTHING;

-- One uploaded file: where its bytes are, what they are, and what the scanner said.
--
-- `sha256` is the only bytea in the schema and is CHECKed to exactly 32 octets. It is
-- written twice: the client claims it at completeUpload, and the worker overwrites it with
-- the digest it computed itself while streaming the bytes to the scanner. Only the second
-- one is ever trusted, because a digest a client asserts about bytes the API never saw is
-- an assertion, not a fact.
--
-- `owner_tenant_organization_id` is the provider boundary (iam.access_grant.scope_type =
-- 'ORGANIZATION'). NULL means the document belongs to the tenant itself; a provider-scoped
-- actor sees only the documents of the organizations it is granted, and one outside that
-- set is not found rather than refused.
--
-- `duplicate_of_object_id` is the losing side of a race: two uploads of the same bytes
-- that both got past the dedupe check before either was promoted. The loser keeps no bytes
-- of its own — its quarantine copy is deleted and it carries the winner's key — so it is a
-- pointer at one stored file rather than a second copy of it. That is why the unique index
-- below excludes those rows: the invariant it protects is "one canonical CLEAN object per
-- digest per tenant", and a pointer is not a second canonical object.
CREATE TABLE document.object (
    id                           uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                    uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    object_key                   text NOT NULL,
    bucket                       text NOT NULL DEFAULT 'quarantine'
                                 CHECK (bucket IN ('quarantine','secure')),
    classification               text NOT NULL DEFAULT 'INTERNAL'
                                 CHECK (classification IN ('INTERNAL','CONFIDENTIAL','PERSONAL','HEALTH')),
    original_filename            text NOT NULL,
    content_type                 text NOT NULL,
    -- Both are NULL until completeUpload: at createUpload nothing has been uploaded yet,
    -- and a size the API invented would be a number nobody measured.
    byte_size                    bigint CHECK (byte_size IS NULL OR byte_size >= 0),
    sha256                       bytea CHECK (sha256 IS NULL OR octet_length(sha256) = 32),
    scan_status                  text NOT NULL DEFAULT 'PENDING'
                                 CHECK (scan_status IN ('PENDING','SCANNING','CLEAN','INFECTED','FAILED')),
    owner_tenant_organization_id uuid,
    duplicate_of_object_id       uuid,
    uploaded_by                  uuid REFERENCES iam.actor(id),
    uploaded_at                  timestamptz NOT NULL DEFAULT clock_timestamp(),
    -- When retention removed the bytes. The row outlives them: "this document existed and
    -- was purged on this day" is an answer a regulator can be given, and an empty table
    -- is not. It is also what makes the retention sweep idempotent — a second pass finds
    -- nothing left to purge rather than deleting a key twice.
    purged_at                    timestamptz,
    created_at                   timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by                   uuid,
    updated_at                   timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by                   uuid,
    row_version                  bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_document_object_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_document_object_owner_org
        FOREIGN KEY (tenant_id, owner_tenant_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_object_duplicate_of
        FOREIGN KEY (tenant_id, duplicate_of_object_id)
        REFERENCES document.object(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_document_object_key CHECK (length(object_key) BETWEEN 1 AND 512),
    CONSTRAINT ck_document_object_filename CHECK (length(btrim(original_filename)) BETWEEN 1 AND 255),
    CONSTRAINT ck_document_object_content_type CHECK (length(content_type) BETWEEN 3 AND 255),
    CONSTRAINT ck_document_object_not_self_duplicate
        CHECK (duplicate_of_object_id IS NULL OR duplicate_of_object_id <> id),
    -- The whole safety property of the package, written as a constraint: bytes reach the
    -- secure bucket only after a CLEAN verdict, and an object that has been decided about
    -- knows how big it was and what its digest is.
    CONSTRAINT ck_document_object_secure_is_clean
        CHECK (bucket <> 'secure' OR scan_status = 'CLEAN'),
    CONSTRAINT ck_document_object_decided
        CHECK (scan_status NOT IN ('CLEAN','INFECTED')
               OR (byte_size IS NOT NULL AND sha256 IS NOT NULL))
);

-- One canonical CLEAN object per digest per tenant. Uploading the same file twice is one
-- object, which also stops a member re-uploading a document being counted as a new one.
CREATE UNIQUE INDEX uq_document_object_sha256_clean
    ON document.object (tenant_id, sha256)
 WHERE scan_status = 'CLEAN' AND duplicate_of_object_id IS NULL;
-- One row owns a key. A duplicate is a pointer at somebody else's bytes and carries their
-- key, so it is deliberately outside this index: it names a file rather than owning one,
-- and two owners of one key is what this index exists to forbid.
CREATE UNIQUE INDEX uq_document_object_key
    ON document.object (tenant_id, object_key)
 WHERE duplicate_of_object_id IS NULL;
-- The worker's own queue: what is still waiting to be scanned, oldest first. Partial, so a
-- tenant whose documents have all been decided costs the sweep one empty scan.
CREATE INDEX ix_document_object_scanning
    ON document.object (tenant_id, uploaded_at)
 WHERE scan_status IN ('PENDING','SCANNING','FAILED');
CREATE INDEX ix_document_object_owner
    ON document.object (tenant_id, owner_tenant_organization_id, created_at DESC, id DESC);
-- The retention sweep: stored documents whose bytes are still there, oldest first.
CREATE INDEX ix_document_object_retention
    ON document.object (tenant_id, uploaded_at)
 WHERE purged_at IS NULL AND scan_status = 'CLEAN';
SELECT platform.attach_touch_row('document.object'::regclass);
SELECT platform.enable_tenant_rls('document.object'::regclass);

-- What the bytes were at one point in time. Append-only: a version that could be edited
-- after the scan that cleared it is not a record of what was scanned.
--
-- `encryption_key_ref` names the key the object store protects these bytes with. It is a
-- reference, never key material: the database holds nothing that could decrypt anything.
CREATE TABLE document.version (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id          uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    object_id          uuid NOT NULL,
    version_no         integer NOT NULL CHECK (version_no > 0),
    byte_size          bigint NOT NULL CHECK (byte_size >= 0),
    content_type       text NOT NULL,
    encryption_key_ref text,
    created_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by         uuid,
    CONSTRAINT uq_document_version_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_document_version_no UNIQUE (tenant_id, object_id, version_no),
    CONSTRAINT fk_document_version_object FOREIGN KEY (tenant_id, object_id)
        REFERENCES document.object(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_document_version_content_type CHECK (length(content_type) BETWEEN 3 AND 255),
    CONSTRAINT ck_document_version_key_ref
        CHECK (encryption_key_ref IS NULL OR length(encryption_key_ref) BETWEEN 1 AND 200)
);

CREATE INDEX ix_document_version_object
    ON document.version (tenant_id, object_id, version_no DESC);
SELECT platform.make_append_only('document.version'::regclass);
SELECT platform.enable_tenant_rls('document.version'::regclass);

-- How a record says "this is the invoice". `aggregate_type` and `aggregate_id` carry no
-- foreign key on purpose: a document is attached to a service request today and to a
-- claim, a booking or a medical report later, and a column per aggregate would mean a
-- migration every time a module starts holding documents.
--
-- `required_permission` is the link's own answer to who may download through it. NULL
-- falls back to document.read; a clinical attachment names health.clinical.read, and the
-- download then refuses a caller who may read documents in general but not that one.
CREATE TABLE document.link (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    object_id           uuid NOT NULL,
    aggregate_type      text NOT NULL,
    aggregate_id        uuid NOT NULL,
    document_type_code  text NOT NULL,
    purpose             text,
    required_permission text,
    created_by          uuid REFERENCES iam.actor(id),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_document_link_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_document_link_target
        UNIQUE (tenant_id, object_id, aggregate_type, aggregate_id, document_type_code),
    CONSTRAINT fk_document_link_object FOREIGN KEY (tenant_id, object_id)
        REFERENCES document.object(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_document_link_aggregate_type CHECK (aggregate_type ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    CONSTRAINT ck_document_link_type_code CHECK (document_type_code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    CONSTRAINT ck_document_link_purpose CHECK (purpose IS NULL OR length(btrim(purpose)) BETWEEN 1 AND 200),
    CONSTRAINT ck_document_link_permission
        CHECK (required_permission IS NULL
               OR required_permission ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$')
);

CREATE INDEX ix_document_link_aggregate
    ON document.link (tenant_id, aggregate_type, aggregate_id, created_at DESC, id DESC);
CREATE INDEX ix_document_link_object ON document.link (tenant_id, object_id);
-- No touch trigger and no row_version: a link is created and removed, never edited.
-- Pointing an existing link at a different document would rewrite what a record says its
-- invoice is without leaving a trace that it changed.
SELECT platform.enable_tenant_rls('document.link'::regclass);

-- What the scanner said, once per attempt. Append-only, because a verdict that could be
-- rewritten afterwards is not evidence of anything.
CREATE TABLE document.scan_result (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id         uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    object_id         uuid NOT NULL,
    version_id        uuid NOT NULL,
    engine            text NOT NULL,
    signature_version text,
    outcome           text NOT NULL CHECK (outcome IN ('CLEAN','INFECTED','ERROR')),
    finding           text,
    scanned_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_document_scan_result_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_document_scan_result_attempt
        UNIQUE (tenant_id, version_id, engine, scanned_at),
    CONSTRAINT fk_document_scan_result_object FOREIGN KEY (tenant_id, object_id)
        REFERENCES document.object(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_scan_result_version FOREIGN KEY (tenant_id, version_id)
        REFERENCES document.version(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_document_scan_result_engine CHECK (length(btrim(engine)) BETWEEN 1 AND 100),
    CONSTRAINT ck_document_scan_result_signature
        CHECK (signature_version IS NULL OR length(signature_version) BETWEEN 1 AND 100),
    -- An INFECTED verdict that does not name what was found is a row nobody can act on,
    -- and a CLEAN one that names a finding is a contradiction.
    CONSTRAINT ck_document_scan_result_finding CHECK (
        (outcome = 'INFECTED' AND finding IS NOT NULL AND length(btrim(finding)) BETWEEN 1 AND 500)
        OR (outcome = 'ERROR' AND (finding IS NULL OR length(finding) BETWEEN 1 AND 500))
        OR (outcome = 'CLEAN' AND finding IS NULL)
    )
);

CREATE INDEX ix_document_scan_result_object
    ON document.scan_result (tenant_id, object_id, scanned_at DESC);
SELECT platform.make_append_only('document.scan_result'::regclass);
SELECT platform.enable_tenant_rls('document.scan_result'::regclass);

-- **An object under legal hold is never deleted**, by retention or by anything else. A
-- hold names one of three things — an object, a person, or an aggregate — and the CHECK
-- forbids a row that names none, because a hold over nothing would silently protect
-- nothing while looking like protection.
CREATE TABLE document.legal_hold (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id      uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    object_id      uuid,
    person_id      uuid,
    aggregate_type text,
    aggregate_id   uuid,
    reason         text NOT NULL,
    placed_by      uuid REFERENCES iam.actor(id),
    placed_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    released_at    timestamptz,
    released_by    uuid REFERENCES iam.actor(id),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version    bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_document_legal_hold_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_document_legal_hold_object FOREIGN KEY (tenant_id, object_id)
        REFERENCES document.object(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_document_legal_hold_person FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_document_legal_hold_target CHECK (
        object_id IS NOT NULL OR person_id IS NOT NULL OR aggregate_id IS NOT NULL
    ),
    CONSTRAINT ck_document_legal_hold_aggregate CHECK (
        (aggregate_type IS NULL) = (aggregate_id IS NULL)
    ),
    CONSTRAINT ck_document_legal_hold_aggregate_type
        CHECK (aggregate_type IS NULL OR aggregate_type ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    CONSTRAINT ck_document_legal_hold_reason CHECK (length(btrim(reason)) BETWEEN 1 AND 1000),
    -- A release is one fact with two halves: when, and by whom. Half of it is a row
    -- nobody can answer "who lifted this" from.
    CONSTRAINT ck_document_legal_hold_release CHECK (
        (released_at IS NULL AND released_by IS NULL)
        OR (released_at IS NOT NULL AND released_by IS NOT NULL AND released_at >= placed_at)
    )
);

-- One active hold per target. A second hold over the same object would mean releasing one
-- of them looks like releasing the object while the other still protects it.
CREATE UNIQUE INDEX uq_document_legal_hold_active_object
    ON document.legal_hold (tenant_id, object_id)
 WHERE released_at IS NULL AND object_id IS NOT NULL;
CREATE UNIQUE INDEX uq_document_legal_hold_active_person
    ON document.legal_hold (tenant_id, person_id)
 WHERE released_at IS NULL AND person_id IS NOT NULL;
CREATE UNIQUE INDEX uq_document_legal_hold_active_aggregate
    ON document.legal_hold (tenant_id, aggregate_type, aggregate_id)
 WHERE released_at IS NULL AND aggregate_id IS NOT NULL;
SELECT platform.attach_touch_row('document.legal_hold'::regclass);
SELECT platform.enable_tenant_rls('document.legal_hold'::regclass);

SELECT platform.grant_app_schema_usage('document');
