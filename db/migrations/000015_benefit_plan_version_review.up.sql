-- 000014: maker-checker metadata for plan versions (WP-I2-02).
-- The submitter (maker) and the publisher (checker) must be different actors when both
-- are recorded (the service always records both; direct inserts in tests may omit the
-- maker); the review comment and the retire reason live on the row. The immutability guard from
-- 000004 is re-declared so that retiring may set the reason columns and nothing else.

ALTER TABLE benefit.plan_version
    ADD COLUMN submitted_by       uuid REFERENCES iam.actor(id),
    ADD COLUMN submitted_at       timestamptz,
    ADD COLUMN review_comment     text,
    ADD COLUMN retire_reason_code text,
    ADD COLUMN retire_reason_text text,
    ADD COLUMN notes              text;

ALTER TABLE benefit.plan_version
    ADD CONSTRAINT ck_plan_version_maker_checker
        CHECK (published_by IS NULL OR submitted_by IS NULL OR published_by <> submitted_by),
    ADD CONSTRAINT ck_plan_version_review_state
        CHECK (
            (status = 'DRAFT' AND submitted_at IS NULL AND published_at IS NULL)
            OR (status = 'UNDER_REVIEW' AND submitted_at IS NOT NULL AND submitted_by IS NOT NULL AND published_at IS NULL)
            OR (status IN ('PUBLISHED', 'RETIRED') AND published_at IS NOT NULL)
        ),
    ADD CONSTRAINT ck_plan_version_retire_reason
        CHECK (status <> 'RETIRED' OR retire_reason_code IS NOT NULL);

-- Published versions stay immutable; retiring may change status and the retire reason only.
CREATE OR REPLACE FUNCTION benefit.tg_plan_version_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    mutable text[] := ARRAY['status', 'retire_reason_code', 'retire_reason_text'];
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.status <> 'DRAFT' THEN
            RAISE EXCEPTION 'plan version % is not a draft and cannot be deleted', OLD.id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        RETURN OLD;
    END IF;

    IF OLD.status = 'PUBLISHED' THEN
        IF NOT (NEW.status = 'RETIRED' AND (to_jsonb(NEW) - mutable) = (to_jsonb(OLD) - mutable)) THEN
            RAISE EXCEPTION 'published plan version % is immutable', OLD.id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
    ELSIF OLD.status = 'RETIRED' THEN
        RAISE EXCEPTION 'retired plan version % is immutable', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END
$$;

COMMENT ON COLUMN benefit.plan_version.submitted_by IS 'Maker: actor who moved the draft to UNDER_REVIEW';
COMMENT ON COLUMN benefit.plan_version.review_comment IS 'Free text from the maker or checker; never personal data';
