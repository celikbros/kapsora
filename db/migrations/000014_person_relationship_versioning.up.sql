-- KAPSORA migration 000014: optimistic concurrency and an end reason for family
-- relationships (WP-I2-01).
--
-- party.person_relationship was created in 000003 without updated_at/row_version and
-- without a place for the reason a relationship was ended. The contract exposes
-- PersonRelationship.rowVersion as an ETag and POST .../end requires If-Match plus a
-- reason code, so the row needs the same trigger-owned versioning every other mutable
-- tenant table has.

ALTER TABLE party.person_relationship
    ADD COLUMN end_reason_code text,
    ADD COLUMN end_reason_text text,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    ADD COLUMN row_version bigint NOT NULL DEFAULT 1;

ALTER TABLE party.person_relationship
    ADD CONSTRAINT ck_person_relationship_end_reason
        CHECK (end_reason_code IS NULL OR end_reason_code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    ADD CONSTRAINT ck_person_relationship_end_reason_state
        CHECK (status = 'ENDED' OR end_reason_code IS NULL);

SELECT platform.attach_touch_row('party.person_relationship');
