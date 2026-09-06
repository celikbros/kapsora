-- 000039: the four things the accommodation vertical leans on that the platform did not
-- have a place for (WP-I6-04, v1.2 9.13, 10.7, 11.11, 17.5).
--
-- None of them is large. Each of them is the sentence a screen of WP-I6-01..03 cannot be
-- written without:
--
--   * `contract.lodging_terms` is the contract saying what a cancellation costs, what a
--     no-show costs and how long a hold may stand. Without it a booking would have to be
--     confirmed under a default nobody agreed to, and the fee charged three months later
--     would be a number the provider had never seen.
--   * `iam.access_grant.scope_type = 'PERSON'` is a member account knowing which person it
--     is. Without it "my bookings" is a query with no subject, and the only way to write
--     the member screens would be to let the request body name the person -- which is to
--     say, to let a member hold a room for their neighbour.
--   * `property_name` joins the notification safe-variable catalogue, because a booking
--     message that cannot name the hotel is a message nobody can act on.
--   * `contract.lodging_terms.manage` is the grant that writing the first of these takes.
--
-- The tenant settings the vertical reads (`accommodation.hold_minutes`,
-- `accommodation.quote_ttl_minutes`, `accommodation.checkin_early_hours`,
-- `accommodation.checkin_late_hours`, `accommodation.max_nights`,
-- `accommodation.stepup_member_amount`) are deliberately *not* here. They are keys of
-- `platform.tenant_setting`, which already holds one jsonb value per key per tenant, and
-- their defaults live in Go (internal/accommodation/settings). A tenant that has never
-- thought about a hold's length should need no row to be admitted, and a second place to
-- state the same fact is a second place for it to be wrong. Migration 000033 made the same
-- decision for the inpatient backdating window.

-- ---------------------------------------------------------------------------
-- Permission (the other half lives in internal/identity/application/roles.go)
-- ---------------------------------------------------------------------------
--
-- NORMAL rather than SENSITIVE: a cancellation policy is a commercial term of an agreement
-- between two organizations, not a fact about a person. It is granted with contract.manage
-- because it is written on the same draft, by the same desk, in the same sitting -- and a
-- role that may set a price but may not say what cancelling it costs would be a role that
-- can only publish half an agreement.
INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('contract.lodging_terms.manage', 'Sözleşme sürümünün konaklama koşullarını yönetme', 'NORMAL')
ON CONFLICT (code) DO NOTHING;

-- ---------------------------------------------------------------------------
-- contract.lodging_terms
-- ---------------------------------------------------------------------------
--
-- One row per contract version, and the whole of what that version promises about a stay
-- that is cut short or never begun.
--
-- The penalty is one of two kinds and never both: a number of nights or a percentage of
-- the member amount. Two nullable columns with a CHECK rather than one column and a unit,
-- because "3" meaning three nights and "3" meaning three percent are two different numbers
-- and a column that could hold either would eventually hold the wrong one. The CHECK ties
-- each to its kind, so a row cannot say PERCENT and carry nights.
--
-- Every percentage is numeric rather than a float, for the reason every money column in
-- this schema is: a fee of 42.5% of 1.234,56 TRY has one exact answer, and a fee two
-- systems disagree about by a kuruş is a fee nobody can invoice.
--
-- `hold_minutes` is NULL for almost every provider: the tenant's
-- `accommodation.hold_minutes` is the answer, and this column exists for the one hotel
-- that negotiated its own. Reading NULL as "the tenant's default" is what keeps the
-- setting the single place the ordinary answer is stated.
CREATE TABLE contract.lodging_terms (
    id                             uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                      uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    contract_version_id            uuid NOT NULL,
    -- How many hours before check-in a cancellation is still free. 0 is a policy too: it
    -- says the free window closes at check-in, not that there is no policy.
    free_cancellation_hours_before integer NOT NULL DEFAULT 0
                                   CHECK (free_cancellation_hours_before BETWEEN 0 AND 8760),
    penalty_kind                   text NOT NULL CHECK (penalty_kind IN ('NIGHTS','PERCENT')),
    penalty_nights                 integer CHECK (penalty_nights IS NULL OR penalty_nights BETWEEN 0 AND 365),
    penalty_percent                numeric(7,4)
                                   CHECK (penalty_percent IS NULL OR (penalty_percent >= 0 AND penalty_percent <= 100)),
    -- What a member who simply does not arrive owes, as a percentage of their own share of
    -- the stay. 100 means the whole stay; it is not a default but what a great many hotels
    -- actually charge, and the tenant has to write it down either way.
    no_show_percent                numeric(7,4) NOT NULL
                                   CHECK (no_show_percent >= 0 AND no_show_percent <= 100),
    hold_minutes                   integer CHECK (hold_minutes IS NULL OR hold_minutes BETWEEN 1 AND 1440),
    min_nights                     integer NOT NULL DEFAULT 1 CHECK (min_nights >= 1),
    max_nights                     integer CHECK (max_nights IS NULL OR max_nights >= 1),
    -- A child below this age stays free. NULL is not "zero": it is a contract that says
    -- nothing about children, and a booking under it charges for all of them.
    child_free_under_age           integer
                                   CHECK (child_free_under_age IS NULL OR child_free_under_age BETWEEN 0 AND 18),
    created_at                     timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by                     uuid,
    updated_at                     timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by                     uuid,
    row_version                    bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_lodging_terms_id_tenant UNIQUE (tenant_id, id),
    -- One answer per version. A second row would make "what does cancelling cost" a
    -- question with two answers, and the snapshot copied onto a booking would have to
    -- guess which one the member agreed to.
    CONSTRAINT uq_lodging_terms_version UNIQUE (tenant_id, contract_version_id),
    CONSTRAINT fk_lodging_terms_version
        FOREIGN KEY (tenant_id, contract_version_id)
        REFERENCES contract.contract_version(tenant_id, id) ON DELETE CASCADE,
    -- Exactly one of the two, and the one the kind names.
    CONSTRAINT ck_lodging_terms_penalty CHECK (
        (penalty_kind = 'NIGHTS'  AND penalty_nights  IS NOT NULL AND penalty_percent IS NULL)
        OR (penalty_kind = 'PERCENT' AND penalty_percent IS NOT NULL AND penalty_nights IS NULL)
    ),
    CONSTRAINT ck_lodging_terms_nights CHECK (max_nights IS NULL OR max_nights >= min_nights)
);

SELECT platform.attach_touch_row('contract.lodging_terms'::regclass);
SELECT platform.enable_tenant_rls('contract.lodging_terms'::regclass);

-- The lodging terms are part of the contract version, so they follow the version's
-- publishing rule: written on a draft, frozen everywhere else. This is the same shape as
-- benefit.tg_service_entitlement_mapping_guard (migration 000035) and it is here rather
-- than in Go for the same reason: a rule the application service can forget is not a rule,
-- and a booking's fee is judged months later against what this row said on the day.
--
-- The one deliberate hole is a version that no longer exists. The foreign key above
-- cascades, so deleting a draft version deletes its terms, and by the time this trigger
-- sees that DELETE the parent row is already gone.
CREATE OR REPLACE FUNCTION contract.tg_lodging_terms_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    v_version_id uuid := COALESCE(NEW.contract_version_id, OLD.contract_version_id);
    v_status     text;
BEGIN
    SELECT status INTO v_status
      FROM contract.contract_version
     WHERE id = v_version_id;

    IF v_status IS NULL AND TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;

    IF v_status IS DISTINCT FROM 'DRAFT' THEN
        RAISE EXCEPTION 'contract version % is not a draft; its lodging terms are frozen', v_version_id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tg_lodging_terms_guard
    BEFORE INSERT OR UPDATE OR DELETE ON contract.lodging_terms
    FOR EACH ROW EXECUTE FUNCTION contract.tg_lodging_terms_guard();

-- ---------------------------------------------------------------------------
-- iam.access_grant: the PERSON scope
-- ---------------------------------------------------------------------------
--
-- A member account acts for exactly one person. The scope says which, in the column every
-- other narrowing already uses, so nothing about grant resolution changes: a membership's
-- scopes come back as they always did and one of them now names a person.
--
-- `scope_id` cannot carry a plain foreign key, because the same column points at an
-- organization, a program, a provider location or a work queue depending on the row. What
-- it can carry is a *generated* column that is the scope id only when the scope is a
-- person, and a composite foreign key on that -- so a PERSON grant naming a person who
-- does not exist, or one belonging to another tenant, is refused by the database rather
-- than by whichever code path happened to write it. The column is STORED because a foreign
-- key needs a real one.
ALTER TABLE iam.access_grant
    DROP CONSTRAINT ck_access_grant_scope;

ALTER TABLE iam.access_grant
    DROP CONSTRAINT access_grant_scope_type_check;

ALTER TABLE iam.access_grant
    ADD CONSTRAINT ck_access_grant_scope_type
        CHECK (scope_type IN ('TENANT','ORGANIZATION','PROGRAM','PROVIDER_LOCATION','WORK_QUEUE','PERSON')),
    ADD CONSTRAINT ck_access_grant_scope CHECK (
        (scope_type = 'TENANT' AND scope_id IS NULL)
        OR (scope_type <> 'TENANT' AND scope_id IS NOT NULL)
    );

ALTER TABLE iam.access_grant
    ADD COLUMN person_scope_id uuid
        GENERATED ALWAYS AS (CASE WHEN scope_type = 'PERSON' THEN scope_id END) STORED;

ALTER TABLE iam.access_grant
    ADD CONSTRAINT fk_access_grant_person
        FOREIGN KEY (tenant_id, person_scope_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT;

-- A person is acted for by at most one membership, and a membership acts for at most one
-- person. Both halves matter: two accounts bound to one member is an account somebody
-- forgot to revoke, and one account bound to two members is the neighbour's room this
-- whole scope exists to prevent.
CREATE UNIQUE INDEX uq_access_grant_person_scope
    ON iam.access_grant (tenant_id, person_scope_id)
 WHERE person_scope_id IS NOT NULL;
CREATE UNIQUE INDEX uq_access_grant_membership_person
    ON iam.access_grant (tenant_id, tenant_membership_id)
 WHERE person_scope_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- notification: property_name joins the safe-variable catalogue
-- ---------------------------------------------------------------------------
--
-- A booking message that cannot name the hotel is a message the member has to open the
-- product to understand, which is most of the reason for sending it. `property_name` has
-- exactly the limits `provider_name` has -- a label, not a sentence -- and the Go side
-- (internal/notification/domain) states the same shape as a value rule. The CHECK is
-- repeated here rather than relaxed, because the catalogue is what makes "there is no slot
-- a diagnosis could be supplied under" a fact about the schema and not only about the code.
ALTER TABLE notification.template
    DROP CONSTRAINT ck_notification_template_variables,
    ADD CONSTRAINT ck_notification_template_variables CHECK (
        cardinality(declared_variables) <= 11
        AND declared_variables <@ ARRAY[
            'given_name','reference_no','status_code','event_date','expires_at',
            'amount','currency','provider_name','program_name','property_name','deep_link']::text[]
    );

-- ---------------------------------------------------------------------------
-- notification: the digit run in a rendered body is ten, as it already is everywhere else
-- ---------------------------------------------------------------------------
--
-- Migration 000030 raised the "this looks like an identity number" run from eight digits
-- to ten, on the grounds that eight refused the one value a notification exists to carry:
-- a reference is minted as PREFIX-YYYYMMDD-XXXXXXXX and the date in the middle is exactly
-- eight digits. It changed the template constraint and the message's safe_variables
-- constraint -- and missed ck_notification_message_body_no_identity_number, which is the
-- one over the text that actually leaves the system.
--
-- The result was a message that could be composed, validated and published, and then
-- refused by the column at the moment of writing, with the outbox event failing and the
-- member simply never told. Nothing surfaced it until an accommodation template put a
-- reference in its body: WP-I6-04's booking.confirmed does exactly that, so the omission
-- is repaired here rather than left for the next module to trip over.
--
-- This widens nothing that matters. What a notification may carry is the closed variable
-- catalogue in internal/notification/domain; this constraint is the crude second line, and
-- ten still catches both shapes it exists for (a VKN has ten digits, a TCKN eleven).
ALTER TABLE notification.message
    DROP CONSTRAINT ck_notification_message_body_no_identity_number,
    ADD CONSTRAINT ck_notification_message_body_no_identity_number CHECK (
        (subject_rendered IS NULL
         OR regexp_replace(subject_rendered,
                '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}',
                ' ', 'g') !~ '[0-9]{10}')
        AND (body_rendered IS NULL
         OR regexp_replace(body_rendered,
                '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}',
                ' ', 'g') !~ '[0-9]{10}')
    );

SELECT platform.grant_app_schema_usage('contract');
