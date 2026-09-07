-- 000040: the three tables the accommodation vertical is built on (WP-I6-01, v1.2 9.13,
-- 10.6, 16.7).
--
-- A provider says what it has -- properties, room types, and how many rooms of each it
-- will give this payer on each night -- and a member asks what is free between two dates.
-- Two sentences carry the whole package and both of them are here rather than in Go:
--
--   * `accommodation.inventory_day` is the ONLY place a room is counted. Not a column on
--     a room type, not a derived count over bookings, not a cache. One row per room type
--     per night, holding the three numbers, and every question about availability is a
--     read of those rows.
--   * `held + confirmed <= capacity` is a CHECK. WP-I6-02 will lock these rows FOR UPDATE
--     in stay_date order to take a hold; what makes that safe is not the lock alone but
--     this constraint, which also refuses the other direction -- a provider lowering a
--     season's capacity under rooms it has already promised. A service can forget to look;
--     the database cannot.
--
-- The tenant settings the vertical reads (accommodation.max_nights and the rest) are keys
-- of platform.tenant_setting with their defaults in Go (internal/accommodation/settings),
-- for the reason migration 000039 states at length. Nothing about them is repeated here.

CREATE SCHEMA IF NOT EXISTS accommodation;

-- ---------------------------------------------------------------------------
-- Permission (the other half lives in internal/identity/application/roles.go)
-- ---------------------------------------------------------------------------
--
-- The three accommodation permissions of migration 000008 are all writes:
-- inventory.manage, booking.create, booking.manage. Nobody could read a property without
-- one of them, which meant a member could book a room they were never allowed to look at
-- and a program manager could not open the list at all.
--
-- NORMAL rather than SENSITIVE: a hotel's name, its town and how many rooms it has free
-- on a Tuesday are facts about a building, not about a person. Marking it SENSITIVE would
-- put every member account into the access reviews that exist to list the grants over
-- personal data, and drown the ones that are.
INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('accommodation.property.read', 'Konaklama tesisi, oda tipi ve müsaitlik okuma', 'NORMAL')
ON CONFLICT (code) DO NOTHING;

-- ---------------------------------------------------------------------------
-- accommodation.property
-- ---------------------------------------------------------------------------
--
-- One building. It belongs to a provider organization (directory.tenant_organization),
-- which is the same id an ORGANIZATION-scoped grant carries, so "is this my property" is
-- a comparison rather than a join through three tables.
--
-- `location_id` is optional and deliberately so. A hotel that is already a
-- provider.location of the network reuses that row -- address, coordinates, phone -- and
-- an internal social facility (v1.2 9.13) has no provider location at all, because the
-- tenant runs it itself.
--
-- `cost_center` is the other half of that sentence: a facility the tenant owns does not
-- send an invoice, it charges an internal cost centre. It is a code, never an amount, and
-- it is NULL for every commercial hotel.
--
-- `timezone` is the property's own IANA zone, and it is a column rather than a tenant
-- setting because a night begins and ends where the building is. A Turkish payer booking
-- a room in Berlin has a stay whose nights are Berlin nights; a tenant-wide zone would
-- make the last night of a stay wrong by a day twice a year for every property outside
-- Europe/Istanbul. The CHECK is only a shape: whether the string is a zone PostgreSQL
-- knows is not an immutable question and cannot live in a constraint, so the domain
-- validates it with time.LoadLocation before the row is written.
--
-- `amenities` is a jsonb array of keys from a closed list held in
-- internal/accommodation/domain. The list is in Go rather than in a CHECK because it will
-- grow -- an amenity added to the catalogue must not need a migration -- and because an
-- unknown key is a 422 naming the field, which a constraint violation is not.
CREATE TABLE accommodation.property (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    provider_organization_id uuid NOT NULL,
    location_id              uuid,
    code                     text NOT NULL,
    name                     text NOT NULL,
    property_type            text NOT NULL
                             CHECK (property_type IN ('HOTEL','RESORT','GUESTHOUSE','SOCIAL_FACILITY','OTHER')),
    timezone                 text NOT NULL DEFAULT 'Europe/Istanbul',
    city                     text,
    region_code              text,
    amenities                jsonb NOT NULL DEFAULT '[]'::jsonb,
    cost_center              text,
    status                   text NOT NULL DEFAULT 'ACTIVE'
                             CHECK (status IN ('ACTIVE','INACTIVE')),
    created_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by               uuid,
    updated_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by               uuid,
    row_version              bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_property_id_tenant UNIQUE (tenant_id, id),
    -- The provider's own code for the building. Unique inside the provider rather than
    -- inside the tenant, because two hotel chains both calling their flagship 'MERKEZ' is
    -- ordinary and refusing the second one would be this table's problem, not theirs.
    CONSTRAINT uq_property_code UNIQUE (tenant_id, provider_organization_id, code),
    CONSTRAINT fk_property_organization
        FOREIGN KEY (tenant_id, provider_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_property_location
        FOREIGN KEY (tenant_id, location_id)
        REFERENCES provider.location(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_property_code CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,39}$'),
    CONSTRAINT ck_property_name CHECK (length(name) BETWEEN 1 AND 200),
    CONSTRAINT ck_property_timezone CHECK (timezone ~ '^[A-Za-z][A-Za-z0-9_+/-]{1,63}$'),
    CONSTRAINT ck_property_city CHECK (city IS NULL OR length(city) BETWEEN 1 AND 100),
    CONSTRAINT ck_property_region CHECK (region_code IS NULL OR region_code ~ '^[A-Z0-9][A-Z0-9_-]{0,15}$'),
    CONSTRAINT ck_property_cost_center
        CHECK (cost_center IS NULL OR cost_center ~ '^[A-Z0-9][A-Z0-9_.-]{0,31}$'),
    -- An array, and never an object: the domain reads it as a set of keys, and a shape
    -- the reader has to guess at is a shape that will eventually hold a free-text note.
    CONSTRAINT ck_property_amenities CHECK (jsonb_typeof(amenities) = 'array')
);
-- The two searches this table answers: a provider opening its own list, and a member
-- asking what is free in a region.
CREATE INDEX ix_property_organization ON accommodation.property (tenant_id, provider_organization_id, status);
CREATE INDEX ix_property_region ON accommodation.property (tenant_id, region_code) WHERE status = 'ACTIVE';

SELECT platform.attach_touch_row('accommodation.property'::regclass);
SELECT platform.enable_tenant_rls('accommodation.property'::regclass);

-- ---------------------------------------------------------------------------
-- accommodation.room_type
-- ---------------------------------------------------------------------------
--
-- One sellable kind of room. `service_definition_id` is the joint with the rest of the
-- platform and the reason this vertical needs almost no machinery of its own: the room
-- type IS a catalogue service, so it is priced by the contract prices of WP-I3-03,
-- entitled through the NIGHT-unit mapping of WP-I5-05, and claimed like anything else.
-- One room type, one service: a room type priced as two services would be a room with two
-- answers to "what does a night cost", and there is only ever one right one.
--
-- max_occupancy >= max_adults is a CHECK because the pair is read by the availability
-- search to refuse a party too large for the room, and a room type saying it sleeps two
-- adults and three people in total is a typo that would quietly sell a family a room they
-- cannot all fit in.
CREATE TABLE accommodation.room_type (
    id                    uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id             uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    property_id           uuid NOT NULL,
    code                  text NOT NULL,
    name                  text NOT NULL,
    max_adults            integer NOT NULL CHECK (max_adults BETWEEN 1 AND 20),
    max_children          integer NOT NULL DEFAULT 0 CHECK (max_children BETWEEN 0 AND 20),
    max_occupancy         integer NOT NULL CHECK (max_occupancy BETWEEN 1 AND 40),
    attributes            jsonb NOT NULL DEFAULT '{}'::jsonb,
    service_definition_id uuid NOT NULL,
    status                text NOT NULL DEFAULT 'ACTIVE'
                          CHECK (status IN ('ACTIVE','INACTIVE')),
    created_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by            uuid,
    updated_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by            uuid,
    row_version           bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_room_type_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_room_type_code UNIQUE (tenant_id, property_id, code),
    CONSTRAINT fk_room_type_property
        FOREIGN KEY (tenant_id, property_id)
        REFERENCES accommodation.property(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_room_type_service_definition
        FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_room_type_code CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,39}$'),
    CONSTRAINT ck_room_type_name CHECK (length(name) BETWEEN 1 AND 200),
    CONSTRAINT ck_room_type_occupancy CHECK (max_occupancy >= max_adults),
    CONSTRAINT ck_room_type_attributes CHECK (jsonb_typeof(attributes) = 'object')
);
CREATE INDEX ix_room_type_property ON accommodation.room_type (tenant_id, property_id, status);
CREATE INDEX ix_room_type_service ON accommodation.room_type (tenant_id, service_definition_id);

SELECT platform.attach_touch_row('accommodation.room_type'::regclass);
SELECT platform.enable_tenant_rls('accommodation.room_type'::regclass);

-- ---------------------------------------------------------------------------
-- accommodation.inventory_day
-- ---------------------------------------------------------------------------
--
-- The hot row of the vertical, and the smallest table in the schema on purpose: three
-- integers, keyed by the only three things that identify a night's allotment.
--
-- There is no `id`. The primary key IS (tenant_id, room_type_id, stay_date), because the
-- row for a given room type on a given night is the same row however it was reached, and a
-- surrogate key would allow two of them -- which is to say, two answers to how many rooms
-- are free, and a hold taken against the one nobody else is looking at.
--
-- `capacity` is an allotment, not a count of rooms the hotel owns: it is how many of that
-- room type this provider gives THIS payer on THIS night. A missing row therefore means
-- no allotment and no availability -- never "unlimited" and never "ask somebody". The
-- search relies on that: a room type with capacity on every night but one is unavailable
-- for the whole range, and it is the absence of the row that says so.
--
-- The CHECK is the package. `held + confirmed <= capacity` is what makes the FOR UPDATE
-- of WP-I6-02 safe, and it is also what refuses a provider lowering a season's capacity
-- under rooms already promised -- a refusal no service performs and none can skip.
CREATE TABLE accommodation.inventory_day (
    tenant_id    uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    room_type_id uuid NOT NULL,
    stay_date    date NOT NULL,
    capacity     integer NOT NULL DEFAULT 0 CHECK (capacity >= 0),
    held         integer NOT NULL DEFAULT 0 CHECK (held >= 0),
    confirmed    integer NOT NULL DEFAULT 0 CHECK (confirmed >= 0),
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version  bigint NOT NULL DEFAULT 1,
    CONSTRAINT pk_inventory_day PRIMARY KEY (tenant_id, room_type_id, stay_date),
    CONSTRAINT fk_inventory_day_room_type
        FOREIGN KEY (tenant_id, room_type_id)
        REFERENCES accommodation.room_type(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_inventory_day_commitment CHECK (held + confirmed <= capacity)
);
-- The range read the search and the allotment writer both make, in the order the rows are
-- locked: by room type, then by date.
CREATE INDEX ix_inventory_day_range ON accommodation.inventory_day (tenant_id, stay_date, room_type_id);

SELECT platform.attach_touch_row('accommodation.inventory_day'::regclass);
SELECT platform.enable_tenant_rls('accommodation.inventory_day'::regclass);

SELECT platform.grant_app_schema_usage('accommodation');
