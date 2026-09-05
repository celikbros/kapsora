-- Person contact details (WP-I5-05 section 2.5).
--
-- `value_enc` is an envelope and never leaves this file as anything else: the list query
-- selects the mask, and only the notification pipeline's address lookup selects the
-- envelope, because it is the one caller that has to send to the address rather than show
-- it. There is no query here that searches by value, and no index that would make one
-- possible.

-- name: ListPersonContacts :many
-- What a screen is allowed to see: the channel, the mask, whether it is the primary and
-- whether anybody has proved it. Never the value.
SELECT id, person_id, channel, value_masked, verified_at, is_primary,
       created_at, row_version
  FROM party.person_contact
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND person_id = sqlc.arg('person_id')
 ORDER BY channel, is_primary DESC, created_at, id;

-- name: DeletePersonContacts :execrows
-- The whole set of one person. A replace deletes and re-inserts: merging would leave
-- behind a number the caller believed they had removed.
DELETE FROM party.person_contact
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND person_id = sqlc.arg('person_id');

-- name: CreatePersonContact :one
INSERT INTO party.person_contact (
    tenant_id, person_id, channel, value_enc, value_masked, verified_at, is_primary,
    created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('person_id'), sqlc.arg('channel'),
        sqlc.arg('value_enc'), sqlc.arg('value_masked'), sqlc.narg('verified_at'),
        sqlc.arg('is_primary'), sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, created_at, row_version;

-- name: FindPersonContactEnvelope :one
-- The address the notification pipeline sends to. The primary contact wins, then a
-- verified one, then the oldest: a person with two numbers is written to at one of them,
-- deterministically, rather than at whichever row the planner happened to return.
SELECT value_enc
  FROM party.person_contact
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND person_id = sqlc.arg('person_id')
   AND channel = sqlc.arg('channel')
 ORDER BY is_primary DESC, (verified_at IS NOT NULL) DESC, created_at, id
 LIMIT 1;

-- name: TouchPersonForContacts :one
-- Contacts are child rows of a person; replacing them has to move the person's ETag, or a
-- caller holding the old one could follow the write with a command written against a
-- record that no longer says what they think it says. The row_version predicate is the
-- If-Match: a stale one matches nothing.
UPDATE party.person
   SET updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version')
RETURNING row_version;
