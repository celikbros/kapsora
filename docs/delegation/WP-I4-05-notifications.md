# WP-I4-05 · Notifications: templates, messages, delivery and preferences

| Field                      | Value                                                                                                                                                                                                                                                              |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Milestone                  | M4 (plan increment I4)                                                                                                                                                                                                                                             |
| Size                       | M                                                                                                                                                                                                                                                                  |
| Depends on                 | WP-I4-01 (the events worth telling somebody about), WP-I1-04 (outbox, scheduler)                                                                                                                                                                                   |
| Runs in parallel with      | WP-I4-03                                                                                                                                                                                                                                                           |
| Migration numbers assigned | `000029_notifications.up.sql`                                                                                                                                                                                                                                      |
| OpenAPI operations owned   | `listNotificationTemplates`, `createNotificationTemplate`, `getNotificationTemplate`, `publishNotificationTemplate`, `listNotificationMessages`, `getNotificationMessage`, `resendNotificationMessage`, `getNotificationPreferences`, `putNotificationPreferences` |
| Read first                 | v1.2 9.16, 16.9, 19.x; ADR-021 (Mailpit is the native local mail sink); WP-I1-04 outbox; ADR-015                                                                                                                                                                   |

## 1. Goal

Telling somebody what happened, without telling them — or anyone reading over their
shoulder, or the SMS gateway's logs — more than they should know. A notification leaves
the system and cannot be recalled, so what goes into one is a smaller question than what
goes into a screen.

## 2. Scope

### 2.1 The rule that shapes everything: safe variables only

**A message body is rendered from a published template and a closed set of safe
variables.** The renderer takes a map, and a variable that is not in the template's
declared list is refused at render time, not silently dropped. No template may declare a
variable whose value is a diagnosis, a report, a document body, an identity number or a
free-text comment.

What a notification may carry: the person's given name, a reference number, a status word,
a date, an amount with its currency, a provider or program name, and a deep link. What it
may not carry, ever: clinical detail, an identity number, or anything an operator typed
into a comment. The template validator enforces the variable list; a test feeds a
diagnosis into a render and asserts it is refused.

### 2.2 Schema (migration 000029, new `notification` schema)

`notification.template`: id, tenant_id, `event_code`, `channel` (`EMAIL`,`SMS`,`PUSH`,`INAPP`),
`locale`, `version_no`, `status` (`DRAFT`,`PUBLISHED`,`RETIRED`), `subject` text NULL,
`body` text, `declared_variables text[]`, `published_at`, `published_by`, row_version.
Unique `(tenant_id, event_code, channel, locale, version_no)`; a partial unique index
keeps **one PUBLISHED template per `(event_code, channel, locale)`**. A published template
is immutable — a change is a new version.

`notification.message`: id, tenant_id, `event_code`, `recipient_type`
(`PERSON`,`ACTOR`,`ORGANIZATION`), recipient id, `channel`, `locale`,
`template_id` and `template_version_no` (the snapshot of what was used),
`subject_rendered`, `body_rendered`, `safe_variables jsonb`, `status`
(`QUEUED`,`SENDING`,`SENT`,`FAILED`,`SUPPRESSED`), `dedupe_key` text,
`created_at`, `sent_at`. Unique `(tenant_id, dedupe_key)` where not null — the same event
must not notify twice because a retry ran. Append-only except for `status` and `sent_at`.

`notification.delivery`: id, tenant_id, message_id, `attempt_no`, `provider_code`,
`provider_message_id`, `outcome` (`ACCEPTED`,`REJECTED`,`BOUNCED`,`ERROR`), `detail`,
`attempted_at`. Append-only, unique `(tenant_id, message_id, attempt_no)`.

`notification.preference`: id, tenant_id, `recipient_type`, recipient id, `event_code`
NULL (null means every event), `channel`, `enabled` boolean, `quiet_hours_start` time NULL,
`quiet_hours_end` time NULL, `timezone`. Unique on
`(tenant_id, recipient_type, recipient_id, event_code, channel)` with NULLS NOT DISTINCT.

RLS, touch triggers, composite keys throughout.

### 2.3 Sending

- A domain event reaches notifications through the **outbox** (WP-I1-04), never through a
  direct call inside a business transaction. A notification must not be able to fail a
  request, and a rolled-back request must not have notified anybody.
- The worker picks the published template for `(event_code, channel, locale)`, renders it
  with the safe variables, writes the message, and hands it to the channel adapter.
- **Adapters**: `EMAIL` through SMTP (Mailpit locally per ADR-021, a real relay in
  production), `SMS` through a stub that records the attempt until a provider is chosen,
  `PUSH` and `INAPP` recorded but not delivered in this milestone.
- Quiet hours and a disabled preference produce a SUPPRESSED message with the reason, not
  a silent nothing: the operator has to be able to see that a member was not told.
- Retries are the outbox's, with backoff; each attempt writes a `delivery` row.

### 2.4 What a message must not do

- No message body, subject or `safe_variables` may contain an identity number, a
  diagnosis, a document body or a comment. A test scans the stored columns.
- No secret in a link: a deep link points at a screen the recipient must sign in to see.
  A voucher token is never mailed; the member sees it in the product.
- No unbounded fan-out: a bulk notification is a job with a recorded count, not a loop.

## 3. Tests required

- **Sensitive content is refused**: rendering with a diagnosis, a TCKN or a comment in the
  variables answers an error and writes nothing; a stored message column scan finds none
  of them.
- A published template is immutable; publishing a second one for the same
  `(event_code, channel, locale)` retires the first (or is refused — pick one, state it,
  and test it).
- The same event delivered twice through the outbox produces one message (dedupe key).
- A rolled-back business transaction notifies nobody.
- Quiet hours and a disabled preference produce SUPPRESSED with a reason, and the message
  is visible to an operator.
- Delivery attempts accumulate as rows; a permanent rejection stops the retries.

## 4. Acceptance criteria

- [ ] Nothing clinical, identifying or free-text can leave the system in a notification.
- [ ] A notification cannot fail or be caused by a transaction that did not commit.
- [ ] The same event notifies once, however many times it is delivered.
- [ ] A member who was not told can be shown to have not been told, and why.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; schema version 29.
