-- 000029: notifications — what a person is told, what they were told instead, and why
-- they were told nothing (WP-I4-05, v1.2 9.16, 16.9, 19.x).
--
-- The one property that shapes every table below: **a notification carries only safe
-- variables**. A body is rendered from a published template and a closed list of declared
-- variables, and that list may only name values from a catalogue this migration writes
-- down as a CHECK. There is no slot for a diagnosis, an identity number, a document body
-- or anything an operator typed into a comment, so a template cannot declare one and a
-- render cannot supply one.
--
-- The catalogue is duplicated here and in internal/notification/domain. That is
-- deliberate: the Go side refuses an unsafe variable with a message a caller can act on,
-- and the CHECK below refuses it even if that code is deleted. Adding a variable to the
-- catalogue therefore costs a migration, which is the right price for widening what may
-- leave the system in an e-mail.
--
-- A notification leaves the system and cannot be recalled. That is why suppression is a
-- row rather than an absence: a member who was not told can be shown to have not been
-- told, and why.

CREATE SCHEMA IF NOT EXISTS notification;

-- notification.manage is already in the catalogue (migration 000008): who may write and
-- publish a template. This one is new and is a different question — who may read the log
-- of what was actually sent to whom, which is a record of people's dealings with the
-- payer and is read by an auditor rather than by whoever writes the templates.
INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('notification.read', 'Gönderilen bildirimleri ve gönderim denemelerini görme', 'NORMAL')
ON CONFLICT (code) DO NOTHING;

-- One version of one message, for one event, one channel and one language.
--
-- `declared_variables` is the closed list the renderer works from: a variable that is not
-- in it is refused at render time rather than dropped, because a body that silently loses
-- half its sentence is worse than a body that was never sent. The CHECK bounds that list
-- to the safe catalogue.
--
-- A published template is immutable — the trigger below refuses an edit to anything but
-- its status — and there is at most one published template per (event_code, channel,
-- locale), which the partial unique index enforces. Publishing a new version retires the
-- one it replaces, in the same transaction, so the index is never momentarily violated
-- and a message rendered a second later cannot find two answers to "which template".
CREATE TABLE notification.template (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id          uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    event_code         text NOT NULL,
    channel            text NOT NULL CHECK (channel IN ('EMAIL','SMS','PUSH','INAPP')),
    locale             text NOT NULL,
    version_no         integer NOT NULL CHECK (version_no > 0),
    status             text NOT NULL DEFAULT 'DRAFT'
                       CHECK (status IN ('DRAFT','PUBLISHED','RETIRED')),
    subject            text,
    body               text NOT NULL,
    declared_variables text[] NOT NULL DEFAULT '{}',
    published_at       timestamptz,
    published_by       uuid REFERENCES iam.actor(id),
    created_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by         uuid,
    updated_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by         uuid,
    row_version        bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_notification_template_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_notification_template_version
        UNIQUE (tenant_id, event_code, channel, locale, version_no),
    CONSTRAINT ck_notification_template_event_code
        CHECK (event_code ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,4}$'),
    CONSTRAINT ck_notification_template_locale CHECK (locale ~ '^[a-z]{2}(-[A-Z]{2})?$'),
    CONSTRAINT ck_notification_template_subject
        CHECK (subject IS NULL OR length(btrim(subject)) BETWEEN 1 AND 200),
    CONSTRAINT ck_notification_template_body CHECK (length(btrim(body)) BETWEEN 1 AND 5000),
    -- An e-mail has a subject line and the other three channels have nowhere to put one.
    CONSTRAINT ck_notification_template_subject_channel
        CHECK ((channel = 'EMAIL') = (subject IS NOT NULL)),
    -- A draft has not been published; anything else has, and says when.
    CONSTRAINT ck_notification_template_published
        CHECK ((status = 'DRAFT') = (published_at IS NULL)),
    -- The safe variable catalogue. Everything a notification may carry is here: the
    -- person's given name, a reference number, a status word, two dates, an amount with
    -- its currency, a provider or program name, and a link into the product. Nothing in
    -- this list can hold a diagnosis, an identity number or a free-text comment, and a
    -- template cannot declare a name that is not in it.
    CONSTRAINT ck_notification_template_variables CHECK (
        cardinality(declared_variables) <= 10
        AND declared_variables <@ ARRAY[
            'given_name','reference_no','status_code','event_date','expires_at',
            'amount','currency','provider_name','program_name','deep_link']::text[]
    ),
    -- The template text itself is written by an operator rather than derived from a
    -- person's record, so it cannot carry *somebody's* diagnosis — but it can carry a
    -- bare identity number or an IBAN somebody pasted, and every recipient of the event
    -- would then get it. A run of eight digits is refused for the same reason the
    -- rendered message refuses one.
    CONSTRAINT ck_notification_template_no_identity_number
        CHECK (body !~ '[0-9]{8}' AND (subject IS NULL OR subject !~ '[0-9]{8}'))
);

-- At most one published template per event, channel and language. This is what makes
-- "which template rendered this" a question with one answer.
CREATE UNIQUE INDEX uq_notification_template_published
    ON notification.template (tenant_id, event_code, channel, locale)
 WHERE status = 'PUBLISHED';
CREATE INDEX ix_notification_template_event
    ON notification.template (tenant_id, event_code, channel, locale, version_no DESC);
CREATE INDEX ix_notification_template_created
    ON notification.template (tenant_id, created_at DESC, id DESC);

-- A published template is immutable. Editing the body of a template that has already sent
-- messages would rewrite what those messages said: the stored message keeps its own
-- rendered text, but the template it names would no longer be the thing that produced it.
-- A change is a new version, and a retirement is the only status move a published row has.
CREATE OR REPLACE FUNCTION notification.tg_template_immutable()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status = 'DRAFT' THEN
        RETURN NEW;
    END IF;
    IF NEW.event_code IS DISTINCT FROM OLD.event_code
       OR NEW.channel IS DISTINCT FROM OLD.channel
       OR NEW.locale IS DISTINCT FROM OLD.locale
       OR NEW.version_no IS DISTINCT FROM OLD.version_no
       OR NEW.subject IS DISTINCT FROM OLD.subject
       OR NEW.body IS DISTINCT FROM OLD.body
       OR NEW.declared_variables IS DISTINCT FROM OLD.declared_variables
       OR NEW.published_at IS DISTINCT FROM OLD.published_at
       OR NEW.published_by IS DISTINCT FROM OLD.published_by THEN
        RAISE EXCEPTION 'notification.template %: a published template is immutable; publish a new version instead', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.status = 'RETIRED' AND NEW.status <> 'RETIRED' THEN
        RAISE EXCEPTION 'notification.template %: a retired template cannot be published again', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER tg_template_immutable BEFORE UPDATE ON notification.template
    FOR EACH ROW EXECUTE FUNCTION notification.tg_template_immutable();
-- tg_template_immutable sorts before tg_touch_row, so it compares the row the caller
-- wrote rather than one the touch trigger has already stamped.
SELECT platform.attach_touch_row('notification.template'::regclass);
SELECT platform.enable_tenant_rls('notification.template'::regclass);

-- One message: what was rendered, for whom, from which template version, and what became
-- of it. It is the operator's answer to "was the member told, and what exactly did they
-- get" — which is why the rendered text is stored rather than recomputed. A template that
-- is retired tomorrow must not change what an e-mail sent today said.
--
-- Append-only except for `status` and `sent_at`: the trigger below refuses an edit to
-- anything else and refuses a delete outright.
--
-- `dedupe_key` is what makes the same event notify once however many times the outbox
-- delivers it. It is unique per tenant where it is set; the send handler inserts with
-- ON CONFLICT DO NOTHING and treats "no row" as "already told".
--
-- A SUPPRESSED message is the point of the table. Quiet hours, a preference the person
-- turned off, no published template, no address to send to — each of them writes a row
-- saying so instead of quietly doing nothing.
CREATE TABLE notification.message (
    id                     uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id              uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    event_code             text NOT NULL,
    recipient_type         text NOT NULL CHECK (recipient_type IN ('PERSON','ACTOR','ORGANIZATION')),
    recipient_id           uuid NOT NULL,
    channel                text NOT NULL CHECK (channel IN ('EMAIL','SMS','PUSH','INAPP')),
    locale                 text NOT NULL,
    -- The snapshot of what was used. Both are NULL on a message that was suppressed
    -- before anything was rendered.
    template_id            uuid,
    template_version_no    integer,
    subject_rendered       text,
    body_rendered          text,
    safe_variables         jsonb NOT NULL DEFAULT '{}'::jsonb,
    status                 text NOT NULL DEFAULT 'QUEUED'
                           CHECK (status IN ('QUEUED','SENDING','SENT','FAILED','SUPPRESSED')),
    -- CHANNEL_NOT_DELIVERABLE is the one reason discovered at send time rather than before
    -- it: a channel whose adapter records the attempt and hands the message to nobody.
    suppressed_reason      text CHECK (suppressed_reason IN
                               ('PREFERENCE_DISABLED','QUIET_HOURS','NO_TEMPLATE','NO_ADDRESS',
                                'CHANNEL_NOT_DELIVERABLE')),
    dedupe_key             text,
    -- Set when an operator asked for this message to be sent again. A resend is a new
    -- message rather than a second attempt at the old one: the original stays exactly as
    -- it was, and the log shows both.
    resent_from_message_id uuid,
    created_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by             uuid,
    sent_at                timestamptz,
    updated_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by             uuid,
    row_version            bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_notification_message_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_notification_message_template
        FOREIGN KEY (tenant_id, template_id)
        REFERENCES notification.template(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_notification_message_resent_from
        FOREIGN KEY (tenant_id, resent_from_message_id)
        REFERENCES notification.message(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_notification_message_event_code
        CHECK (event_code ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,4}$'),
    CONSTRAINT ck_notification_message_locale CHECK (locale ~ '^[a-z]{2}(-[A-Z]{2})?$'),
    CONSTRAINT ck_notification_message_subject
        CHECK (subject_rendered IS NULL OR length(btrim(subject_rendered)) BETWEEN 1 AND 200),
    CONSTRAINT ck_notification_message_body
        CHECK (body_rendered IS NULL OR length(btrim(body_rendered)) BETWEEN 1 AND 10000),
    CONSTRAINT ck_notification_message_dedupe
        CHECK (dedupe_key IS NULL OR length(dedupe_key) BETWEEN 1 AND 200),
    CONSTRAINT ck_notification_message_not_self_resend
        CHECK (resent_from_message_id IS NULL OR resent_from_message_id <> id),
    -- A subject belongs to an e-mail and nowhere else.
    CONSTRAINT ck_notification_message_subject_channel
        CHECK (subject_rendered IS NULL OR channel = 'EMAIL'),
    -- The template snapshot is a pair or is absent.
    CONSTRAINT ck_notification_message_template_pair
        CHECK ((template_id IS NULL) = (template_version_no IS NULL)),
    CONSTRAINT ck_notification_message_rendered_needs_template
        CHECK ((body_rendered IS NULL) = (template_id IS NULL)),
    -- Suppression names its reason, and only a suppressed message has one. This is the
    -- whole difference between "not told" and "nothing happened".
    CONSTRAINT ck_notification_message_suppressed
        CHECK ((status = 'SUPPRESSED') = (suppressed_reason IS NOT NULL)),
    -- Anything that is going to be sent has already been rendered: there is no way to
    -- queue a message whose body nobody produced.
    CONSTRAINT ck_notification_message_queued_is_rendered
        CHECK (status = 'SUPPRESSED' OR body_rendered IS NOT NULL),
    CONSTRAINT ck_notification_message_sent_at
        CHECK ((status = 'SENT') = (sent_at IS NOT NULL)),
    CONSTRAINT ck_notification_message_variables
        CHECK (jsonb_typeof(safe_variables) = 'object'
               AND length(safe_variables::text) <= 2000),
    -- The safe variable rule, as a constraint. `deep_link` is excluded because it is the
    -- one variable that may carry a record id, and a uuid is indistinguishable from a run
    -- of digits to a regular expression. Everything else a message carries is a name, a
    -- reference, a status word, a date or an amount, and none of those has eight digits
    -- in a row — a TCKN has eleven and a VKN has ten.
    CONSTRAINT ck_notification_message_variables_no_identity_number
        CHECK ((safe_variables - 'deep_link')::text !~ '[0-9]{8}'),
    -- The same rule over the text that actually leaves the system, with record ids
    -- removed first so a link into the product does not look like an identity number.
    CONSTRAINT ck_notification_message_body_no_identity_number CHECK (
        (subject_rendered IS NULL
         OR regexp_replace(subject_rendered,
                '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}',
                ' ', 'g') !~ '[0-9]{8}')
        AND (body_rendered IS NULL
         OR regexp_replace(body_rendered,
                '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}',
                ' ', 'g') !~ '[0-9]{8}')
    )
);

-- The same event must not notify twice because a retry ran.
CREATE UNIQUE INDEX uq_notification_message_dedupe
    ON notification.message (tenant_id, dedupe_key)
 WHERE dedupe_key IS NOT NULL;
-- The operator's log, newest first.
CREATE INDEX ix_notification_message_log
    ON notification.message (tenant_id, created_at DESC, id DESC);
-- "What was this person told?"
CREATE INDEX ix_notification_message_recipient
    ON notification.message (tenant_id, recipient_type, recipient_id, created_at DESC, id DESC);
-- What is still in flight. Partial, so a tenant whose messages have all been decided
-- costs the sweep one empty scan.
CREATE INDEX ix_notification_message_open
    ON notification.message (tenant_id, created_at)
 WHERE status IN ('QUEUED','SENDING');

-- Append-only except for the columns a send moves. Everything a message says about
-- itself — who it was for, what it said, which template produced it — is fixed the moment
-- it is written, because it is evidence of what left the system rather than a working copy.
--
-- suppressed_reason is the one exception, and only in one direction: NULL to a value. A
-- channel with no provider behind it is only discovered when the send is attempted, and
-- the attempt has to be recorded (the SMS stub exists for exactly that), so the message
-- learns why it was suppressed after it was written. Once set the reason is fixed like
-- everything else; it can never be changed or cleared, so a suppression cannot be
-- rewritten into a different one.
CREATE OR REPLACE FUNCTION notification.tg_message_status_only()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'notification.message is append-only: DELETE is not allowed on %', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
       OR NEW.event_code IS DISTINCT FROM OLD.event_code
       OR NEW.recipient_type IS DISTINCT FROM OLD.recipient_type
       OR NEW.recipient_id IS DISTINCT FROM OLD.recipient_id
       OR NEW.channel IS DISTINCT FROM OLD.channel
       OR NEW.locale IS DISTINCT FROM OLD.locale
       OR NEW.template_id IS DISTINCT FROM OLD.template_id
       OR NEW.template_version_no IS DISTINCT FROM OLD.template_version_no
       OR NEW.subject_rendered IS DISTINCT FROM OLD.subject_rendered
       OR NEW.body_rendered IS DISTINCT FROM OLD.body_rendered
       OR NEW.safe_variables IS DISTINCT FROM OLD.safe_variables
       OR (OLD.suppressed_reason IS NOT NULL
           AND NEW.suppressed_reason IS DISTINCT FROM OLD.suppressed_reason)
       OR NEW.dedupe_key IS DISTINCT FROM OLD.dedupe_key
       OR NEW.resent_from_message_id IS DISTINCT FROM OLD.resent_from_message_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'notification.message %: only status and sent_at may change once a message is written', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER tg_message_status_only BEFORE UPDATE OR DELETE ON notification.message
    FOR EACH ROW EXECUTE FUNCTION notification.tg_message_status_only();
SELECT platform.attach_touch_row('notification.message'::regclass);
SELECT platform.enable_tenant_rls('notification.message'::regclass);

-- One attempt to hand one message to one provider. Attempts accumulate rather than
-- overwrite: "it failed twice and then went" is a different fact from "it went", and only
-- the row per attempt can tell them apart. Append-only for the same reason a scan verdict
-- is: an attempt that could be rewritten afterwards is not evidence of anything.
--
-- `detail` carries the provider's answer, bounded and with addresses redacted by the
-- adapter before it gets here: a bounce message that quotes the recipient's e-mail would
-- put an address into a table an auditor reads.
CREATE TABLE notification.delivery (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    message_id          uuid NOT NULL,
    attempt_no          integer NOT NULL CHECK (attempt_no > 0),
    provider_code       text NOT NULL,
    provider_message_id text,
    outcome             text NOT NULL CHECK (outcome IN ('ACCEPTED','REJECTED','BOUNCED','ERROR')),
    detail              text,
    attempted_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by          uuid,
    CONSTRAINT uq_notification_delivery_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_notification_delivery_attempt UNIQUE (tenant_id, message_id, attempt_no),
    CONSTRAINT fk_notification_delivery_message
        FOREIGN KEY (tenant_id, message_id)
        REFERENCES notification.message(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_notification_delivery_provider
        CHECK (provider_code ~ '^[A-Z][A-Z0-9_]{1,39}$'),
    CONSTRAINT ck_notification_delivery_provider_message_id
        CHECK (provider_message_id IS NULL OR length(provider_message_id) BETWEEN 1 AND 200),
    CONSTRAINT ck_notification_delivery_detail
        CHECK (detail IS NULL OR length(detail) BETWEEN 1 AND 500)
);

CREATE INDEX ix_notification_delivery_message
    ON notification.delivery (tenant_id, message_id, attempt_no DESC);
SELECT platform.make_append_only('notification.delivery'::regclass);
SELECT platform.enable_tenant_rls('notification.delivery'::regclass);

-- What somebody has asked not to be sent, and when they would rather not hear from us.
--
-- `event_code` NULL means "every event on this channel", so a person can turn a channel
-- off once instead of once per event. The unique constraint is NULLS NOT DISTINCT so that
-- "every event" is one row rather than a row per attempt to write it, and the lookup
-- prefers the row that names the event over the one that does not.
--
-- Quiet hours are a pair or are absent, and they are read in `timezone` rather than in the
-- server's: a member in Berlin and one in Istanbul do not share a night.
CREATE TABLE notification.preference (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id         uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    recipient_type    text NOT NULL CHECK (recipient_type IN ('PERSON','ACTOR','ORGANIZATION')),
    recipient_id      uuid NOT NULL,
    event_code        text,
    channel           text NOT NULL CHECK (channel IN ('EMAIL','SMS','PUSH','INAPP')),
    enabled           boolean NOT NULL DEFAULT true,
    quiet_hours_start time,
    quiet_hours_end   time,
    timezone          text NOT NULL DEFAULT 'Europe/Istanbul',
    created_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by        uuid,
    updated_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by        uuid,
    row_version       bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_notification_preference_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_notification_preference_target UNIQUE NULLS NOT DISTINCT
        (tenant_id, recipient_type, recipient_id, event_code, channel),
    CONSTRAINT ck_notification_preference_event_code
        CHECK (event_code IS NULL OR event_code ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,4}$'),
    CONSTRAINT ck_notification_preference_quiet_pair
        CHECK ((quiet_hours_start IS NULL) = (quiet_hours_end IS NULL)),
    -- A window that starts and ends at the same instant is either every hour of the day
    -- or none of them, depending on who reads it. Neither is what anybody meant.
    CONSTRAINT ck_notification_preference_quiet_distinct
        CHECK (quiet_hours_start IS NULL OR quiet_hours_start <> quiet_hours_end),
    CONSTRAINT ck_notification_preference_timezone
        CHECK (timezone ~ '^[A-Za-z][A-Za-z0-9_+/-]{0,59}$')
);

CREATE INDEX ix_notification_preference_recipient
    ON notification.preference (tenant_id, recipient_type, recipient_id, channel);
SELECT platform.attach_touch_row('notification.preference'::regclass);
SELECT platform.enable_tenant_rls('notification.preference'::regclass);

SELECT platform.grant_app_schema_usage('notification');
