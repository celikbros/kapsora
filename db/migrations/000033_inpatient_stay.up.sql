-- 000033: the inpatient stay — admitting somebody, extending it, and settling up
-- (WP-I5-03, v1.2 10.4, 12.2, 16.7).
--
-- Admitting somebody costs the plan something before anybody knows how much. The whole
-- schema below exists to make three sentences true in the database rather than only in a
-- service, because a service can be bypassed by the next caller and a constraint cannot:
--
--   * there is **one open stay per case and provider** — the partial unique index on
--     health.inpatient_stay;
--   * a new extension **cannot be opened while an earlier one is undecided** — the partial
--     unique index on health.stay_extension;
--   * two segments of the same stay **cannot claim the same hours** — the exclusion
--     constraint on health.stay_segment, with the one deliberate hole a companion needs.
--
-- The money is not here. A stay reserves entitlement through a PREAUTHORIZATION service
-- request and the authorization that request's approval produces (WP-I4-01, WP-I4-02), and
-- there is no column in this migration that touches a balance. What these tables hold are
-- the two foreign keys that say which request and which authorization, and the day counts
-- the reconciliation compares — as numeric(20,6) like every other quantity in the schema,
-- because a day count that depended on binary rounding would be a day count two systems
-- disagree about.
--
-- The backdating window a stay is created inside is configuration rather than a column:
-- `platform.tenant_setting` already holds one jsonb value per key per tenant, so the keys
-- `health.inpatient.backdate_days` and `health.inpatient.future_days` live there and the
-- application falls back to 3 and 30 when a tenant has set neither. No column and no table
-- is added for them: a second place to state the same fact is a second place for it to be
-- wrong, and a tenant that has never thought about it should not need a row to be admitted.

-- ---------------------------------------------------------------------------
-- health.inpatient_stay
-- ---------------------------------------------------------------------------
--
-- One admission. It hangs off a case (WP-I5-01), which is what makes the admission
-- diagnosis a `health.diagnosis` of one of that case's encounters rather than a second
-- clinical column here: the whole point of the projection WP-I5-01 built is that there is
-- one place a diagnosis lives and one function that decides who may read it.
--
-- `expected_discharge_at` is derived at write from `admission_at` and `estimated_days`. It
-- is stored rather than computed on read because it is what the authorization's validity is
-- set from, and a derived value the authorization was built against has to still be the
-- value anybody reads back afterwards.
--
-- `authorized_days`, `actual_days`, `released_days` and `over_authorization` are the
-- reconciliation of 2.4, written once at discharge and never recomputed. They are columns
-- rather than a view over the authorization for two reasons: the claim (WP-I5-04) raises an
-- exception from `over_authorization` and needs a row it can join to, and the figures have
-- to keep saying what was true on the day of discharge even after the authorization has
-- expired, been cancelled, or had its holds swept.
CREATE TABLE health.inpatient_stay (
    id                        uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                 uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    case_id                   uuid NOT NULL,
    provider_organization_id  uuid NOT NULL,
    location_id               uuid,
    attending_practitioner_id uuid,
    admission_at              timestamptz NOT NULL,
    estimated_days            integer NOT NULL CHECK (estimated_days > 0),
    expected_discharge_at     timestamptz NOT NULL,
    discharge_at              timestamptz,
    status                    text NOT NULL DEFAULT 'REQUESTED'
                              CHECK (status IN ('REQUESTED','AUTHORIZED','ADMITTED',
                                                'DISCHARGED','CANCELLED','REJECTED')),
    -- The PREAUTHORIZATION request the admission was asked for with. NOT NULL: a stay
    -- nobody asked for is a stay nobody can decide, and the reviewer decides it on the
    -- request page without ever learning that a stay exists.
    service_request_id        uuid NOT NULL,
    -- The hold the approval produced (WP-I4-02). NULL until the request is decided, and
    -- NULL forever on a stay that was refused.
    authorization_id          uuid,
    admission_diagnosis_id    uuid,
    -- The reconciliation. All four are NULL until discharge, and the CHECK below keeps
    -- them that way: a stay carrying an actual day count and no discharge would be a
    -- reconciliation of something that has not happened.
    authorized_days           numeric(20,6),
    actual_days               numeric(20,6),
    released_days             numeric(20,6),
    over_authorization        boolean NOT NULL DEFAULT false,
    cancel_reason_code        text,
    created_at                timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by                uuid,
    updated_at                timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by                uuid,
    row_version               bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_inpatient_stay_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_inpatient_stay_case FOREIGN KEY (tenant_id, case_id)
        REFERENCES health.health_case(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_inpatient_stay_provider FOREIGN KEY (tenant_id, provider_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_inpatient_stay_location FOREIGN KEY (tenant_id, location_id)
        REFERENCES provider.location(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_inpatient_stay_practitioner FOREIGN KEY (tenant_id, attending_practitioner_id)
        REFERENCES provider.practitioner(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_inpatient_stay_request FOREIGN KEY (tenant_id, service_request_id)
        REFERENCES service.service_request(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_inpatient_stay_authorization FOREIGN KEY (tenant_id, authorization_id)
        REFERENCES service.authorization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_inpatient_stay_diagnosis FOREIGN KEY (tenant_id, admission_diagnosis_id)
        REFERENCES health.diagnosis(tenant_id, id) ON DELETE RESTRICT,
    -- A discharge is one fact with two halves: the status and the moment. Half of it is a
    -- row nobody can answer "when did this person go home" from.
    CONSTRAINT ck_inpatient_stay_discharge CHECK (
        (status = 'DISCHARGED') = (discharge_at IS NOT NULL)
    ),
    CONSTRAINT ck_inpatient_stay_period
        CHECK (discharge_at IS NULL OR discharge_at >= admission_at),
    CONSTRAINT ck_inpatient_stay_expected
        CHECK (expected_discharge_at > admission_at),
    -- The reconciliation exists exactly when the discharge does. `authorized_days` is the
    -- exception: it is written when the authorization is created and survives everything
    -- afterwards, because "what was promised" is answerable for a stay still running.
    CONSTRAINT ck_inpatient_stay_reconciliation CHECK (
        (discharge_at IS NOT NULL)
        OR (actual_days IS NULL AND released_days IS NULL AND over_authorization = false)
    ),
    CONSTRAINT ck_inpatient_stay_days CHECK (
        (authorized_days IS NULL OR authorized_days > 0)
        AND (actual_days IS NULL OR actual_days > 0)
        AND (released_days IS NULL OR released_days >= 0)
    ),
    -- An authorized stay has an authorization, and a refused one has none. Both halves are
    -- asserted because both are the thing the outbox subscriber writes, and a subscriber
    -- that half-wrote its answer is exactly what a redelivery would leave behind.
    CONSTRAINT ck_inpatient_stay_authorized CHECK (
        status <> 'AUTHORIZED' OR authorization_id IS NOT NULL
    ),
    CONSTRAINT ck_inpatient_stay_rejected CHECK (
        status <> 'REJECTED' OR authorization_id IS NULL
    ),
    CONSTRAINT ck_inpatient_stay_cancel_reason CHECK (
        cancel_reason_code IS NULL OR cancel_reason_code ~ '^[A-Z][A-Z0-9_]{1,63}$'
    )
);

-- **One open stay per case and provider** (v1.2 10.4 step 3). This index is the rule; the
-- 409 the service answers is only the polite version of it. A second admission of the same
-- person to the same hospital while the first has not been discharged is either a mistake
-- or a duplicate, and both of them would reserve entitlement twice.
--
-- REJECTED, CANCELLED and DISCHARGED are outside the index on purpose: a refused admission
-- must not block the corrected one, and a person readmitted next month is a new stay.
CREATE UNIQUE INDEX uq_inpatient_stay_open
    ON health.inpatient_stay (tenant_id, case_id, provider_organization_id)
 WHERE status IN ('REQUESTED','AUTHORIZED','ADMITTED');

-- The stay behind a request, which is how the outbox subscriber finds what a decision moved.
CREATE UNIQUE INDEX uq_inpatient_stay_request
    ON health.inpatient_stay (tenant_id, service_request_id);
CREATE INDEX ix_inpatient_stay_case
    ON health.inpatient_stay (tenant_id, case_id, admission_at DESC);
CREATE INDEX ix_inpatient_stay_provider
    ON health.inpatient_stay (tenant_id, provider_organization_id, status);
-- The keyset the list endpoint pages by.
CREATE INDEX ix_inpatient_stay_admission
    ON health.inpatient_stay (tenant_id, admission_at DESC, id DESC);
SELECT platform.attach_touch_row('health.inpatient_stay'::regclass);
SELECT platform.enable_tenant_rls('health.inpatient_stay'::regclass);

-- ---------------------------------------------------------------------------
-- health.stay_extension
-- ---------------------------------------------------------------------------
--
-- "Three more days." It is a request of its own, so the review is the request's review and
-- a medical reviewer extends an admission where they decide everything else. That is why
-- there is no `decided_by` here: the decision belongs to the request, and a second copy of
-- it would be a second answer to "who approved the extra days".
--
-- `authorization_id` is the extension's own hold. WP-I4-02's extend moves the end of a
-- promise forward and takes no further reserve — it cannot, because the approved quantities
-- of an authorization are the ones its request was decided for. So the added days are
-- reserved by the extension's own authorization, and the stay's original authorization has
-- its validity moved forward beside it. Two rows, one promise: the days are held once and
-- the window covers all of them.
CREATE TABLE health.stay_extension (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id          uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    stay_id            uuid NOT NULL,
    sequence_no        integer NOT NULL CHECK (sequence_no > 0),
    additional_days    integer NOT NULL CHECK (additional_days > 0),
    reason_code        text NOT NULL,
    reason_text        text,
    service_request_id uuid NOT NULL,
    authorization_id   uuid,
    status             text NOT NULL DEFAULT 'REQUESTED'
                       CHECK (status IN ('REQUESTED','APPROVED','REJECTED','CANCELLED')),
    created_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by         uuid,
    updated_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by         uuid,
    row_version        bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_stay_extension_id_tenant UNIQUE (tenant_id, id),
    -- The extensions of one stay are numbered, and the numbers do not repeat: "the second
    -- extension" has to name one row.
    CONSTRAINT uq_stay_extension_sequence UNIQUE (tenant_id, stay_id, sequence_no),
    CONSTRAINT uq_stay_extension_request UNIQUE (tenant_id, service_request_id),
    CONSTRAINT fk_stay_extension_stay FOREIGN KEY (tenant_id, stay_id)
        REFERENCES health.inpatient_stay(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_stay_extension_request FOREIGN KEY (tenant_id, service_request_id)
        REFERENCES service.service_request(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_stay_extension_authorization FOREIGN KEY (tenant_id, authorization_id)
        REFERENCES service.authorization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_stay_extension_reason_code CHECK (reason_code ~ '^[A-Z][A-Z0-9_.:-]{1,79}$'),
    -- `reason_text` is why the doctor wants more days, in the doctor's words. It is
    -- clinical, so it is served only in the clinical projection and never reaches a
    -- notification variable, an audit detail or a work-item title. The length is the one
    -- the rest of the schema uses for a reason.
    CONSTRAINT ck_stay_extension_reason_text
        CHECK (reason_text IS NULL OR length(reason_text) <= 1000),
    CONSTRAINT ck_stay_extension_approved CHECK (
        status <> 'REJECTED' OR authorization_id IS NULL
    )
);

-- **A new extension cannot be opened while one is undecided** (v1.2 10.4 step 6), enforced
-- by the database rather than only by the service. Two extensions in flight would be two
-- reviewers reserving different numbers of days for the same admission, and whichever
-- landed second would silently win.
CREATE UNIQUE INDEX uq_stay_extension_pending
    ON health.stay_extension (tenant_id, stay_id)
 WHERE status = 'REQUESTED';
CREATE INDEX ix_stay_extension_stay
    ON health.stay_extension (tenant_id, stay_id, sequence_no);
SELECT platform.attach_touch_row('health.stay_extension'::regclass);
SELECT platform.enable_tenant_rls('health.stay_extension'::regclass);

-- ---------------------------------------------------------------------------
-- health.stay_segment
-- ---------------------------------------------------------------------------
--
-- Where the patient actually was, hour by hour: a ward, then intensive care, then a ward
-- again. It is what a claim is priced from, because a night in intensive care is not a
-- night in a ward.
--
-- The exclusion constraint says a patient is in one place at a time — **except for a
-- COMPANION**. A companion is the relative sleeping in the room, and their segment overlaps
-- the patient's own by definition; putting them inside the constraint would make the true
-- record unrecordable. So the constraint is partial and COMPANION rows sit outside it,
-- which is a hole with a name rather than a rule quietly not applied.
CREATE TABLE health.stay_segment (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id    uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    stay_id      uuid NOT NULL,
    segment_type text NOT NULL
                 CHECK (segment_type IN ('WARD','ICU','SURGERY','OBSERVATION','COMPANION')),
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz,
    room_code    text,
    bed_code     text,
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by   uuid,
    updated_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by   uuid,
    row_version  bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_stay_segment_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_stay_segment_stay FOREIGN KEY (tenant_id, stay_id)
        REFERENCES health.inpatient_stay(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_stay_segment_period CHECK (ends_at IS NULL OR ends_at > starts_at),
    CONSTRAINT ck_stay_segment_room
        CHECK (room_code IS NULL OR room_code ~ '^[A-Za-z0-9][A-Za-z0-9 ._/-]{0,31}$'),
    CONSTRAINT ck_stay_segment_bed
        CHECK (bed_code IS NULL OR bed_code ~ '^[A-Za-z0-9][A-Za-z0-9 ._/-]{0,31}$'),
    -- One patient, one place, one moment. The range is half-open, so a ward segment that
    -- ends at 14:00 and an intensive care segment that begins at 14:00 are a transfer
    -- rather than an overlap. An open-ended segment is `[starts_at, ∞)`, so a second open
    -- segment of the same stay is refused as well.
    CONSTRAINT ex_stay_segment_overlap EXCLUDE USING gist (
        tenant_id WITH =,
        stay_id WITH =,
        tstzrange(starts_at, ends_at, '[)') WITH &&
    ) WHERE (segment_type <> 'COMPANION')
);

CREATE INDEX ix_stay_segment_stay
    ON health.stay_segment (tenant_id, stay_id, starts_at);
SELECT platform.attach_touch_row('health.stay_segment'::regclass);
SELECT platform.enable_tenant_rls('health.stay_segment'::regclass);

-- Idempotent, and repeated for the same reason migration 000032 repeats it: the grant is
-- over the tables that exist when it runs, so a schema that has just gained three of them
-- has to ask again.
SELECT platform.grant_app_schema_usage('health');
