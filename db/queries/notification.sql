-- Notification template, message, delivery and preference queries (WP-I4-05, v1.2 9.16,
-- 16.9, 19.x).
--
-- Four properties shape this file.
--
-- There is no boundary parameter. Templates are the tenant's own configuration and the
-- message log is a record of the tenant's dealings with its members; neither is scoped to
-- a provider organization, and the two permissions guarding them — notification.manage
-- and notification.read — are held by nobody outside the tenant. RLS is the whole of the
-- isolation here.
--
-- Publishing is two statements in one transaction, in this order: retire whatever is
-- published for the slot, then publish the draft. The partial unique index that keeps one
-- published template per (event_code, channel, locale) is therefore never momentarily
-- violated, and a message rendered on either side of the pair finds exactly one template.
--
-- CreateNotificationMessage inserts ON CONFLICT DO NOTHING against the deduplication key.
-- No rows returned is not an error: it is the answer "this event has already notified
-- somebody", which is what makes the same event delivered twice one message.
--
-- The delivery attempt number is computed here rather than handed in. Two workers that
-- both counted the rows they could see would agree on a number neither of them checked;
-- computing it inside the insert makes them collide on the unique constraint instead.

-- name: CreateNotificationTemplate :one
INSERT INTO notification.template (
    tenant_id, event_code, channel, locale, version_no, subject, body,
    declared_variables, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('event_code'), sqlc.arg('channel'),
        sqlc.arg('locale'), sqlc.arg('version_no'), sqlc.narg('subject'), sqlc.arg('body'),
        sqlc.arg('declared_variables')::text[], sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, event_code, channel, locale, version_no, status, subject, body,
          declared_variables, published_at, published_by, created_at, row_version;

-- name: GetNotificationTemplate :one
SELECT t.id, t.event_code, t.channel, t.locale, t.version_no, t.status, t.subject, t.body,
       t.declared_variables, t.published_at, t.published_by, t.created_at, t.row_version
  FROM notification.template t
 WHERE t.tenant_id = sqlc.arg('tenant_id')
   AND t.id = sqlc.arg('id');

-- name: ListNotificationTemplates :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows to
-- learn whether a next page exists.
SELECT t.id, t.event_code, t.channel, t.locale, t.version_no, t.status, t.subject, t.body,
       t.declared_variables, t.published_at, t.published_by, t.created_at, t.row_version
  FROM notification.template t
 WHERE t.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('event_code')::text IS NULL OR t.event_code = sqlc.narg('event_code')::text)
   AND (sqlc.narg('channel')::text IS NULL OR t.channel = sqlc.narg('channel')::text)
   AND (sqlc.narg('locale')::text IS NULL OR t.locale = sqlc.narg('locale')::text)
   AND (sqlc.narg('status')::text IS NULL OR t.status = sqlc.narg('status')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (t.created_at, t.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY t.created_at DESC, t.id DESC
 LIMIT sqlc.arg('page_size');

-- name: NextNotificationTemplateVersion :one
-- The version a new draft takes. Computed here rather than handed in: a number the caller
-- chose is a number two callers can choose at once, and the unique constraint would then
-- turn a race into an error the second one cannot act on.
SELECT COALESCE(MAX(t.version_no), 0) + 1
  FROM notification.template t
 WHERE t.tenant_id = sqlc.arg('tenant_id')
   AND t.event_code = sqlc.arg('event_code')
   AND t.channel = sqlc.arg('channel')
   AND t.locale = sqlc.arg('locale');

-- name: RetirePublishedNotificationTemplate :one
-- Retires whatever is published for one slot and says which row it was. No row is the
-- normal answer the first time an event gets a template.
UPDATE notification.template
   SET status = 'RETIRED', updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND event_code = sqlc.arg('event_code')
   AND channel = sqlc.arg('channel')
   AND locale = sqlc.arg('locale')
   AND status = 'PUBLISHED'
RETURNING id;

-- name: PublishNotificationTemplate :execrows
-- The expected row_version is part of the predicate, so a stale If-Match publishes
-- nothing. Only a DRAFT can be published: the trigger on the table refuses to bring a
-- retired template back, and this predicate says the same thing before the trigger has to.
UPDATE notification.template
   SET status       = 'PUBLISHED',
       published_at = sqlc.arg('published_at'),
       published_by = sqlc.narg('actor_id'),
       updated_by   = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT'
   AND row_version = sqlc.arg('row_version');

-- name: FindPublishedNotificationTemplate :one
-- What a send renders from. The partial unique index guarantees there is at most one.
SELECT t.id, t.event_code, t.channel, t.locale, t.version_no, t.status, t.subject, t.body,
       t.declared_variables, t.published_at, t.published_by, t.created_at, t.row_version
  FROM notification.template t
 WHERE t.tenant_id = sqlc.arg('tenant_id')
   AND t.event_code = sqlc.arg('event_code')
   AND t.channel = sqlc.arg('channel')
   AND t.locale = sqlc.arg('locale')
   AND t.status = 'PUBLISHED';

-- name: CreateNotificationMessage :one
-- No row returned means the deduplication key already named a message: the same event was
-- delivered twice and the first delivery already told somebody.
INSERT INTO notification.message (
    tenant_id, event_code, recipient_type, recipient_id, channel, locale,
    template_id, template_version_no, subject_rendered, body_rendered, safe_variables,
    status, suppressed_reason, dedupe_key, resent_from_message_id, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('event_code'), sqlc.arg('recipient_type'),
        sqlc.arg('recipient_id'), sqlc.arg('channel'), sqlc.arg('locale'),
        sqlc.narg('template_id'), sqlc.narg('template_version_no'),
        sqlc.narg('subject_rendered'), sqlc.narg('body_rendered'),
        sqlc.arg('safe_variables'), sqlc.arg('status'), sqlc.narg('suppressed_reason'),
        sqlc.narg('dedupe_key'), sqlc.narg('resent_from_message_id'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
ON CONFLICT (tenant_id, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING
RETURNING id, event_code, recipient_type, recipient_id, channel, locale, template_id,
          template_version_no, subject_rendered, body_rendered, safe_variables, status,
          suppressed_reason, dedupe_key, resent_from_message_id, created_at, sent_at,
          row_version;

-- name: GetNotificationMessage :one
SELECT m.id, m.event_code, m.recipient_type, m.recipient_id, m.channel, m.locale,
       m.template_id, m.template_version_no, m.subject_rendered, m.body_rendered,
       m.safe_variables, m.status, m.suppressed_reason, m.dedupe_key,
       m.resent_from_message_id, m.created_at, m.sent_at, m.row_version
  FROM notification.message m
 WHERE m.tenant_id = sqlc.arg('tenant_id')
   AND m.id = sqlc.arg('id');

-- name: FindNotificationMessageByDedupeKey :one
SELECT m.id, m.event_code, m.recipient_type, m.recipient_id, m.channel, m.locale,
       m.template_id, m.template_version_no, m.subject_rendered, m.body_rendered,
       m.safe_variables, m.status, m.suppressed_reason, m.dedupe_key,
       m.resent_from_message_id, m.created_at, m.sent_at, m.row_version
  FROM notification.message m
 WHERE m.tenant_id = sqlc.arg('tenant_id')
   AND m.dedupe_key = sqlc.arg('dedupe_key');

-- name: ListNotificationMessages :many
SELECT m.id, m.event_code, m.recipient_type, m.recipient_id, m.channel, m.locale,
       m.template_id, m.template_version_no, m.subject_rendered, m.body_rendered,
       m.safe_variables, m.status, m.suppressed_reason, m.dedupe_key,
       m.resent_from_message_id, m.created_at, m.sent_at, m.row_version
  FROM notification.message m
 WHERE m.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('event_code')::text IS NULL OR m.event_code = sqlc.narg('event_code')::text)
   AND (sqlc.narg('channel')::text IS NULL OR m.channel = sqlc.narg('channel')::text)
   AND (sqlc.narg('status')::text IS NULL OR m.status = sqlc.narg('status')::text)
   AND (sqlc.narg('recipient_type')::text IS NULL
        OR m.recipient_type = sqlc.narg('recipient_type')::text)
   AND (sqlc.narg('recipient_id')::uuid IS NULL OR m.recipient_id = sqlc.narg('recipient_id')::uuid)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (m.created_at, m.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY m.created_at DESC, m.id DESC
 LIMIT sqlc.arg('page_size');

-- name: SetNotificationMessageStatus :exec
-- The only write the schema allows on a message once it exists. sent_at is set exactly
-- when the status becomes SENT, which the table CHECKs as well. suppressed_reason is
-- coalesced rather than assigned: a message suppressed at creation keeps the reason it was
-- given, and a send that discovers a channel has no provider behind it can set one on a
-- message that had none. The trigger refuses any other movement of that column.
UPDATE notification.message
   SET status            = sqlc.arg('status'),
       sent_at           = sqlc.narg('sent_at'),
       suppressed_reason = coalesce(suppressed_reason, sqlc.narg('suppressed_reason')),
       updated_by        = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id');

-- name: CreateNotificationDelivery :one
-- One attempt. The attempt number is the count of what is already there plus one,
-- computed inside the insert so two workers cannot agree on the same number.
INSERT INTO notification.delivery (
    tenant_id, message_id, attempt_no, provider_code, provider_message_id, outcome, detail)
SELECT sqlc.arg('tenant_id'), sqlc.arg('message_id'),
       COALESCE(MAX(d.attempt_no), 0) + 1, sqlc.arg('provider_code'),
       sqlc.narg('provider_message_id'), sqlc.arg('outcome'), sqlc.narg('detail')
  FROM notification.delivery d
 WHERE d.tenant_id = sqlc.arg('tenant_id')
   AND d.message_id = sqlc.arg('message_id')
RETURNING id, message_id, attempt_no, provider_code, provider_message_id, outcome, detail,
          attempted_at;

-- name: ListNotificationDeliveries :many
SELECT d.id, d.message_id, d.attempt_no, d.provider_code, d.provider_message_id,
       d.outcome, d.detail, d.attempted_at
  FROM notification.delivery d
 WHERE d.tenant_id = sqlc.arg('tenant_id')
   AND d.message_id = sqlc.arg('message_id')
 ORDER BY d.attempt_no;

-- name: ListNotificationPreferences :many
SELECT p.id, p.recipient_type, p.recipient_id, p.event_code, p.channel, p.enabled,
       p.quiet_hours_start, p.quiet_hours_end, p.timezone, p.created_at, p.row_version
  FROM notification.preference p
 WHERE p.tenant_id = sqlc.arg('tenant_id')
   AND p.recipient_type = sqlc.arg('recipient_type')
   AND p.recipient_id = sqlc.arg('recipient_id')
 ORDER BY p.channel, p.event_code NULLS FIRST;

-- name: DeleteNotificationPreferences :exec
-- The first half of a replace. A merge would leave behind a channel the person believed
-- they had turned off, so the set is cleared and rewritten in one transaction.
DELETE FROM notification.preference
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND recipient_type = sqlc.arg('recipient_type')
   AND recipient_id = sqlc.arg('recipient_id');

-- name: CreateNotificationPreference :one
INSERT INTO notification.preference (
    tenant_id, recipient_type, recipient_id, event_code, channel, enabled,
    quiet_hours_start, quiet_hours_end, timezone, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('recipient_type'), sqlc.arg('recipient_id'),
        sqlc.narg('event_code'), sqlc.arg('channel'), sqlc.arg('enabled'),
        sqlc.narg('quiet_hours_start'), sqlc.narg('quiet_hours_end'),
        sqlc.arg('timezone'), sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, recipient_type, recipient_id, event_code, channel, enabled,
          quiet_hours_start, quiet_hours_end, timezone, created_at, row_version;

-- name: ResolveNotificationPreference :one
-- Which row governs one event on one channel. The row that names the event wins over the
-- row that names none, which is what makes "turn this channel off" and "turn this one
-- event back on" both expressible.
SELECT p.id, p.recipient_type, p.recipient_id, p.event_code, p.channel, p.enabled,
       p.quiet_hours_start, p.quiet_hours_end, p.timezone, p.created_at, p.row_version
  FROM notification.preference p
 WHERE p.tenant_id = sqlc.arg('tenant_id')
   AND p.recipient_type = sqlc.arg('recipient_type')
   AND p.recipient_id = sqlc.arg('recipient_id')
   AND p.channel = sqlc.arg('channel')
   AND (p.event_code IS NULL OR p.event_code = sqlc.arg('event_code'))
 ORDER BY p.event_code NULLS LAST
 LIMIT 1;

-- name: NotificationActorEmail :one
-- The only address the platform holds today. A person has no contact row of their own
-- yet, so a PERSON or ORGANIZATION recipient has no address on any channel and the send
-- writes a SUPPRESSED message saying exactly that rather than pretending.
--
-- The membership is part of the predicate: an actor with no active membership of this
-- tenant is not this tenant's to write to, whatever else is true about them.
SELECT (COALESCE(a.email::text, ''))::text AS address
  FROM iam.actor a
  JOIN iam.tenant_membership m ON m.actor_id = a.id
 WHERE a.id = sqlc.arg('actor_id')
   AND m.tenant_id = sqlc.arg('tenant_id')
   AND m.membership_status = 'ACTIVE'
   AND a.status = 'ACTIVE'
 LIMIT 1;
