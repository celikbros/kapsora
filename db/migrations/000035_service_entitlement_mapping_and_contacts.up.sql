-- 000035: the five things M4's screens found the platform had not decided (WP-I5-05,
-- v1.2 9.12, 11.10, 12.4, 16.14).
--
-- Two tables and three permissions. Each of them closes a place where the product had to
-- lie to the person in front of it.
--
-- `benefit.service_entitlement_mapping` is the missing sentence between the catalogue and
-- the plan: "one of these services draws this much of that entitlement". Without it every
-- eligibility answer is REVIEW_REQUIRED with SERVICE_MAPPING_PENDING, which is the honest
-- answer to a question nobody had configured — and it made the whole check useless in
-- practice, because it was the answer to every question.
--
-- `party.person_contact` is the reason an SMS could not work at all: `iam.actor.email` was
-- the only contact column in the schema, so a member who is not a user of the product had
-- no address on any channel. The value is envelope-encrypted exactly as an identifier is
-- (migration 000003, WP-I2-01): the plaintext exists in `value_enc` and nowhere else, and
-- `value_masked` is what a screen, a log and an audit row are allowed to see. There is
-- deliberately no blind index: nothing searches people by e-mail address, and an index
-- that made it possible would be a second, equality-searchable copy of the same personal
-- data.
--
-- The rule the mapping table exists inside is WP-I2-02's and is not restated in Go: a
-- published plan version is immutable, so a mapping that belongs to one is immutable too.
-- It is enforced here, by a trigger, because a rule the application could forget is not a
-- rule.

-- ---------------------------------------------------------------------------
-- Permissions (the other half lives in internal/identity/application/roles.go)
-- ---------------------------------------------------------------------------
--
-- Contact details are SENSITIVE for the same reason an identifier is: an e-mail address
-- and a mobile number are how a person is reached and how a person is correlated across
-- systems. Reading them and writing them are separate grants, because a desk that has to
-- correct a wrong number is not thereby entitled to export the tenant's address book.
INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('entitlement.mapping.manage', 'Hizmet tanımı ile hak kodu eşleşmesini yönetme',   'NORMAL'),
    ('member.contact.read',        'Hak sahibi iletişim bilgisini görme (maskeli)',     'SENSITIVE'),
    ('member.contact.manage',      'Hak sahibi iletişim bilgisini ekleme ve güncelleme','SENSITIVE')
ON CONFLICT (code) DO NOTHING;

-- ---------------------------------------------------------------------------
-- benefit.service_entitlement_mapping
-- ---------------------------------------------------------------------------

-- The mapping points at one of *this version's* definitions, and the composite foreign key
-- below is what says so. It needs a key to point at: (tenant, plan_version, id) is already
-- unique by construction, but a foreign key cannot rely on "by construction".
ALTER TABLE benefit.entitlement_definition
    ADD CONSTRAINT uq_entitlement_definition_version_id
    UNIQUE (tenant_id, plan_version_id, id);

CREATE TABLE benefit.service_entitlement_mapping (
    id                        uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                 uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    plan_version_id           uuid NOT NULL,
    service_definition_id     uuid NOT NULL,
    entitlement_definition_id uuid NOT NULL,
    -- One session of physiotherapy may draw two units of the SESSION entitlement, and a
    -- night of accommodation may draw one. It is a factor rather than a quantity because
    -- the quantity is on the request; numeric(20,6) is the same exactness every quantity
    -- in this schema has, and a float here would make a balance depend on rounding.
    unit_factor               numeric(20,6) NOT NULL DEFAULT 1 CHECK (unit_factor > 0),
    -- Both bounds are optional: a mapping with no window is the mapping for the whole
    -- life of the version, which is what almost every mapping is. A window is for the
    -- case a version covers a change of practice part-way through its own validity.
    valid_from                date,
    valid_to                  date,
    created_at                timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by                uuid,
    updated_at                timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by                uuid,
    row_version               bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_service_entitlement_mapping_id_tenant UNIQUE (tenant_id, id),
    -- One service draws from one entitlement inside one version. Two rows would make
    -- "which balance does this service spend" a question with two answers, and the
    -- eligibility resolver would have to guess.
    CONSTRAINT uq_service_entitlement_mapping
        UNIQUE (tenant_id, plan_version_id, service_definition_id),
    CONSTRAINT fk_service_entitlement_mapping_plan_version
        FOREIGN KEY (tenant_id, plan_version_id)
        REFERENCES benefit.plan_version(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_service_entitlement_mapping_service
        FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT,
    -- The plan version is part of this key, so a mapping cannot name a definition that
    -- belongs to a different version of a different plan.
    CONSTRAINT fk_service_entitlement_mapping_entitlement
        FOREIGN KEY (tenant_id, plan_version_id, entitlement_definition_id)
        REFERENCES benefit.entitlement_definition(tenant_id, plan_version_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_service_entitlement_mapping_period CHECK (
        valid_from IS NULL OR valid_to IS NULL OR valid_to > valid_from
    )
);

CREATE INDEX ix_service_entitlement_mapping_version
    ON benefit.service_entitlement_mapping (tenant_id, plan_version_id, service_definition_id);
SELECT platform.attach_touch_row('benefit.service_entitlement_mapping'::regclass);
SELECT platform.enable_tenant_rls('benefit.service_entitlement_mapping'::regclass);

-- A mapping is part of the plan version, so it follows the version's publishing rule:
-- editable on a draft, frozen everywhere else. This is the same shape as
-- service.tg_request_item_guard (migration 000006) — the parent's status decides what may
-- happen to the child — and it is here rather than in Go because the application service
-- that forgets it would leave no trace of having done so.
CREATE OR REPLACE FUNCTION benefit.tg_service_entitlement_mapping_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    v_version_id uuid := COALESCE(NEW.plan_version_id, OLD.plan_version_id);
    v_status     text;
BEGIN
    SELECT status INTO v_status
      FROM benefit.plan_version
     WHERE id = v_version_id;

    IF v_status IS DISTINCT FROM 'DRAFT' THEN
        RAISE EXCEPTION 'plan version % is not a draft; its entitlement mappings are frozen', v_version_id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tg_service_entitlement_mapping_guard
    BEFORE INSERT OR UPDATE OR DELETE ON benefit.service_entitlement_mapping
    FOR EACH ROW EXECUTE FUNCTION benefit.tg_service_entitlement_mapping_guard();

-- ---------------------------------------------------------------------------
-- party.person_contact
-- ---------------------------------------------------------------------------

-- One way of reaching one person. The plaintext lives in `value_enc` and nowhere else:
-- no column, no index, no audit detail and no log holds the address itself, exactly as
-- `party.person_identifier` holds a TCKN.
--
-- `verified_at` is NULL until somebody proves the address is theirs, which is M10's
-- onboarding. An unverified contact is still an address — a member who has not clicked a
-- link is still the member whose number the sponsor supplied — and the notification
-- pipeline uses it. What verification will buy is the right to *trust* it, not the right
-- to use it.
--
-- The column is `is_primary` rather than the `primary` of the work package: `primary` is
-- a reserved word, and `party.person_identifier` already spells the same idea this way.
CREATE TABLE party.person_contact (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id     uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    person_id     uuid NOT NULL,
    channel       text NOT NULL CHECK (channel IN ('EMAIL','SMS')),
    value_enc     bytea NOT NULL,
    value_masked  text NOT NULL,
    verified_at   timestamptz,
    is_primary    boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by    uuid,
    updated_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by    uuid,
    row_version   bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_person_contact_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_person_contact_person
        FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_person_contact_cipher CHECK (octet_length(value_enc) > 0),
    CONSTRAINT ck_person_contact_masked CHECK (length(btrim(value_masked)) BETWEEN 1 AND 120)
);

-- One primary per person and channel. Two primaries is not richer data: every sender asks
-- "where does this person read their mail" and expects one answer.
CREATE UNIQUE INDEX uq_person_contact_primary
    ON party.person_contact (tenant_id, person_id, channel)
 WHERE is_primary;
CREATE INDEX ix_person_contact_person
    ON party.person_contact (tenant_id, person_id, channel);
SELECT platform.attach_touch_row('party.person_contact'::regclass);
SELECT platform.enable_tenant_rls('party.person_contact'::regclass);

SELECT platform.grant_app_schema_usage('benefit');
SELECT platform.grant_app_schema_usage('party');
