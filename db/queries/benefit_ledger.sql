-- Entitlement ledger queries (WP-I2-03): accounts with materialised balances, the
-- append-only movement ledger, reservations and manual adjustments.
--
-- The ledger is the record and the account columns are its materialisation. Every
-- movement therefore runs the same three statements in one transaction:
--   1. LockEntitlementAccount (SELECT ... FOR UPDATE) serialises the account,
--   2. InsertEntitlementLedgerEntry appends the delta vector,
--   3. ApplyEntitlementAccountDeltas adds exactly the same deltas to the account.
-- ck_entitlement_ledger_conservation and ck_entitlement_account_balance are the
-- database's own proof that the three stay in step.
--
-- Quantities are numeric(20,6) and cross the boundary as exact decimal text
-- (domain.Quantity); no float ever holds a balance.

-- name: LockEntitlementAccount :one
-- The serialisation point of every movement. Only the account row is locked; the
-- definition is joined read-only for allow_overdraft and the unit metadata.
SELECT a.id, a.enrollment_id, a.entitlement_definition_id,
       lower(a.benefit_period)::date AS period_from,
       upper(a.benefit_period)::date AS period_to,
       a.total_granted::text      AS total_granted,
       a.available_quantity::text AS available_quantity,
       a.reserved_quantity::text  AS reserved_quantity,
       a.consumed_quantity::text  AS consumed_quantity,
       a.expired_quantity::text   AS expired_quantity,
       a.status, a.created_at, a.row_version,
       d.id AS definition_id, d.code AS definition_code, d.name AS definition_name,
       d.unit_type, d.currency_code, d.allow_overdraft, d.family_shared
  FROM benefit.entitlement_account a
  JOIN benefit.entitlement_definition d
       ON d.tenant_id = a.tenant_id AND d.id = a.entitlement_definition_id
 WHERE a.tenant_id = $1 AND a.id = $2
   FOR UPDATE OF a;

-- name: GetEntitlementAccountDetail :one
SELECT a.id, a.enrollment_id, a.entitlement_definition_id,
       lower(a.benefit_period)::date AS period_from,
       upper(a.benefit_period)::date AS period_to,
       a.total_granted::text      AS total_granted,
       a.available_quantity::text AS available_quantity,
       a.reserved_quantity::text  AS reserved_quantity,
       a.consumed_quantity::text  AS consumed_quantity,
       a.expired_quantity::text   AS expired_quantity,
       a.status, a.created_at, a.row_version,
       d.id AS definition_id, d.code AS definition_code, d.name AS definition_name,
       d.unit_type, d.currency_code, d.allow_overdraft, d.family_shared,
       m.person_id
  FROM benefit.entitlement_account a
  JOIN benefit.entitlement_definition d
       ON d.tenant_id = a.tenant_id AND d.id = a.entitlement_definition_id
  JOIN benefit.enrollment e ON e.tenant_id = a.tenant_id AND e.id = a.enrollment_id
  JOIN party.sponsor_membership m ON m.tenant_id = e.tenant_id AND m.id = e.sponsor_membership_id
 WHERE a.tenant_id = $1 AND a.id = $2;

-- name: ListPersonEntitlementAccounts :many
-- Accounts the person can spend from on as_of: the accounts of their own enrollments,
-- plus the family-shared accounts opened on the enrollment of their principal
-- membership (party.sponsor_membership.principal_membership_id).
SELECT a.id, a.enrollment_id, a.entitlement_definition_id,
       lower(a.benefit_period)::date AS period_from,
       upper(a.benefit_period)::date AS period_to,
       a.total_granted::text      AS total_granted,
       a.available_quantity::text AS available_quantity,
       a.reserved_quantity::text  AS reserved_quantity,
       a.consumed_quantity::text  AS consumed_quantity,
       a.expired_quantity::text   AS expired_quantity,
       a.status, a.created_at, a.row_version,
       d.id AS definition_id, d.code AS definition_code, d.name AS definition_name,
       d.unit_type, d.currency_code, d.allow_overdraft, d.family_shared,
       m.person_id, (m.person_id <> sqlc.arg('person_id')) AS shared
  FROM benefit.entitlement_account a
  JOIN benefit.entitlement_definition d
       ON d.tenant_id = a.tenant_id AND d.id = a.entitlement_definition_id
  JOIN benefit.enrollment e ON e.tenant_id = a.tenant_id AND e.id = a.enrollment_id
  JOIN party.sponsor_membership m ON m.tenant_id = e.tenant_id AND m.id = e.sponsor_membership_id
 WHERE a.tenant_id = sqlc.arg('tenant_id')
   AND a.benefit_period @> sqlc.arg('as_of')::date
   AND (m.person_id = sqlc.arg('person_id')
        OR (d.family_shared AND EXISTS (
              SELECT 1 FROM party.sponsor_membership own
               WHERE own.tenant_id = a.tenant_id
                 AND own.person_id = sqlc.arg('person_id')
                 AND own.principal_membership_id = m.id)))
 ORDER BY shared, d.code, a.id;

-- name: CreateEntitlementAccount :one
-- Opening an account is idempotent on uq_entitlement_account_period: a concurrent
-- opener wins and this statement returns no row. Balances stay zero until the GRANT
-- movement writes them, so the ledger explains every unit on the account.
INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id, entitlement_definition_id, benefit_period)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('enrollment_id'), sqlc.arg('definition_id'),
        daterange(sqlc.arg('period_from')::date, sqlc.narg('period_to')::date, '[)'))
ON CONFLICT ON CONSTRAINT uq_entitlement_account_period DO NOTHING
RETURNING id;

-- name: FindEntitlementAccountForPeriod :one
SELECT id
  FROM benefit.entitlement_account
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND enrollment_id = sqlc.arg('enrollment_id')
   AND entitlement_definition_id = sqlc.arg('definition_id')
   AND benefit_period = daterange(sqlc.arg('period_from')::date, sqlc.narg('period_to')::date, '[)');

-- name: ApplyEntitlementAccountDeltas :execrows
-- The account side of a movement: exactly the deltas that were just appended to the
-- ledger. platform.tg_touch_row owns updated_at and row_version.
UPDATE benefit.entitlement_account
   SET total_granted      = total_granted      + sqlc.arg('delta_total')::text::numeric,
       available_quantity = available_quantity + sqlc.arg('delta_available')::text::numeric,
       reserved_quantity  = reserved_quantity  + sqlc.arg('delta_reserved')::text::numeric,
       consumed_quantity  = consumed_quantity  + sqlc.arg('delta_consumed')::text::numeric,
       expired_quantity   = expired_quantity   + sqlc.arg('delta_expired')::text::numeric
 WHERE tenant_id = sqlc.arg('tenant_id') AND id = sqlc.arg('id');

-- name: SetEntitlementAccountStatus :execrows
UPDATE benefit.entitlement_account
   SET status = sqlc.arg('status')
 WHERE tenant_id = sqlc.arg('tenant_id') AND id = sqlc.arg('id') AND status <> sqlc.arg('status');

-- name: InsertEntitlementLedgerEntry :one
INSERT INTO benefit.entitlement_ledger (
    tenant_id, entitlement_account_id, movement_type, effective_at,
    delta_total, delta_available, delta_reserved, delta_consumed, delta_expired,
    reference_type, reference_id, idempotency_key, reason_code, reason_text,
    reservation_id, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('account_id'), sqlc.arg('movement_type'), sqlc.arg('effective_at'),
        sqlc.arg('delta_total')::text::numeric,
        sqlc.arg('delta_available')::text::numeric,
        sqlc.arg('delta_reserved')::text::numeric,
        sqlc.arg('delta_consumed')::text::numeric,
        sqlc.arg('delta_expired')::text::numeric,
        sqlc.arg('reference_type'), sqlc.arg('reference_id'), sqlc.arg('idempotency_key'),
        sqlc.narg('reason_code'), sqlc.narg('reason_text'),
        sqlc.narg('reservation_id'), sqlc.narg('created_by'))
RETURNING id, effective_at, created_at;

-- name: GetEntitlementLedgerEntry :one
SELECT id, entitlement_account_id, movement_type, effective_at,
       delta_total::text     AS delta_total,
       delta_available::text AS delta_available,
       delta_reserved::text  AS delta_reserved,
       delta_consumed::text  AS delta_consumed,
       delta_expired::text   AS delta_expired,
       reference_type, reference_id, idempotency_key, reason_code, reason_text,
       reservation_id, created_by, created_at
  FROM benefit.entitlement_ledger
 WHERE tenant_id = $1 AND id = $2;

-- name: GetEntitlementLedgerEntryByKey :one
-- The idempotency lookup of the movements that are not reservations: it runs after the
-- account lock, so a replay always sees the entry the winning transaction committed.
SELECT id, entitlement_account_id, movement_type, effective_at,
       delta_total::text     AS delta_total,
       delta_available::text AS delta_available,
       delta_reserved::text  AS delta_reserved,
       delta_consumed::text  AS delta_consumed,
       delta_expired::text   AS delta_expired,
       reference_type, reference_id, idempotency_key, reason_code, reason_text,
       reservation_id, created_by, created_at
  FROM benefit.entitlement_ledger
 WHERE tenant_id = $1 AND entitlement_account_id = $2 AND idempotency_key = $3;

-- name: ListEntitlementLedgerEntries :many
-- Newest first with a keyset on (effective_at DESC, id DESC); the caller asks for
-- limit+1 rows to learn whether a next page exists.
SELECT id, entitlement_account_id, movement_type, effective_at,
       delta_total::text     AS delta_total,
       delta_available::text AS delta_available,
       delta_reserved::text  AS delta_reserved,
       delta_consumed::text  AS delta_consumed,
       delta_expired::text   AS delta_expired,
       reference_type, reference_id, idempotency_key, reason_code, reason_text,
       reservation_id, created_by, created_at
  FROM benefit.entitlement_ledger
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND entitlement_account_id = sqlc.arg('account_id')
   AND (sqlc.narg('cursor_effective_at')::timestamptz IS NULL
        OR (effective_at, id) < (sqlc.narg('cursor_effective_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY effective_at DESC, id DESC
 LIMIT sqlc.arg('page_size');

-- name: CountEntitlementLedgerReversals :one
-- A ledger entry is reversed at most once; the reversal points back at it by reference.
SELECT count(*)
  FROM benefit.entitlement_ledger
 WHERE tenant_id = $1 AND movement_type = 'REVERSE'
   AND reference_type = 'LEDGER_ENTRY' AND reference_id = $2;

-- name: CreateEntitlementReservation :one
INSERT INTO benefit.entitlement_reservation (
    tenant_id, entitlement_account_id, reference_type, reference_id,
    quantity, expires_at, idempotency_key, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('account_id'), sqlc.arg('reference_type'), sqlc.arg('reference_id'),
        sqlc.arg('quantity')::text::numeric, sqlc.narg('expires_at'),
        sqlc.arg('idempotency_key'), sqlc.narg('created_by'))
RETURNING id, status, created_at, row_version;

-- name: GetEntitlementReservation :one
SELECT id, entitlement_account_id, reference_type, reference_id,
       quantity::text          AS quantity,
       consumed_quantity::text AS consumed_quantity,
       released_quantity::text AS released_quantity,
       status, expires_at, idempotency_key, created_by, created_at, row_version
  FROM benefit.entitlement_reservation
 WHERE tenant_id = $1 AND id = $2;

-- name: GetEntitlementReservationForUpdate :one
SELECT id, entitlement_account_id, reference_type, reference_id,
       quantity::text          AS quantity,
       consumed_quantity::text AS consumed_quantity,
       released_quantity::text AS released_quantity,
       status, expires_at, idempotency_key, created_by, created_at, row_version
  FROM benefit.entitlement_reservation
 WHERE tenant_id = $1 AND id = $2
   FOR UPDATE;

-- name: GetEntitlementReservationByKey :one
-- The idempotency lookup of Reserve; it runs after the account lock, so a replay always
-- sees the reservation the winning transaction committed.
SELECT id, entitlement_account_id, reference_type, reference_id,
       quantity::text          AS quantity,
       consumed_quantity::text AS consumed_quantity,
       released_quantity::text AS released_quantity,
       status, expires_at, idempotency_key, created_by, created_at, row_version
  FROM benefit.entitlement_reservation
 WHERE tenant_id = $1 AND entitlement_account_id = $2 AND idempotency_key = $3;

-- name: ListOpenEntitlementReservations :many
SELECT id, entitlement_account_id, reference_type, reference_id,
       quantity::text          AS quantity,
       consumed_quantity::text AS consumed_quantity,
       released_quantity::text AS released_quantity,
       status, expires_at, idempotency_key, created_by, created_at, row_version
  FROM benefit.entitlement_reservation
 WHERE tenant_id = $1 AND entitlement_account_id = $2
   AND status IN ('HELD','PARTIALLY_CONSUMED')
 ORDER BY created_at, id;

-- name: ListExpiredEntitlementReservations :many
-- Feeds the entitlement.reservation.expire job; ix_entitlement_reservation_expiry serves
-- it. Only identifiers are read: each hold is then released through the normal
-- account-lock-first path.
SELECT id, entitlement_account_id
  FROM benefit.entitlement_reservation
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND status IN ('HELD','PARTIALLY_CONSUMED')
   AND expires_at IS NOT NULL
   AND expires_at <= sqlc.arg('expired_before')
 ORDER BY expires_at, id
 LIMIT sqlc.arg('page_size');

-- name: UpdateEntitlementReservationProgress :execrows
UPDATE benefit.entitlement_reservation
   SET consumed_quantity = sqlc.arg('consumed_quantity')::text::numeric,
       released_quantity = sqlc.arg('released_quantity')::text::numeric,
       status = sqlc.arg('status')
 WHERE tenant_id = sqlc.arg('tenant_id') AND id = sqlc.arg('id');

-- name: CreateEntitlementAdjustment :one
INSERT INTO benefit.entitlement_adjustment (
    tenant_id, entitlement_account_id, delta_quantity, reason_code, reason_text, requested_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('account_id'), sqlc.arg('delta_quantity')::text::numeric,
        sqlc.arg('reason_code'), sqlc.narg('reason_text'), sqlc.arg('requested_by'))
RETURNING id;

-- name: GetEntitlementAdjustment :one
SELECT id, entitlement_account_id,
       delta_quantity::text AS delta_quantity,
       reason_code, reason_text, status, requested_by, requested_at,
       decided_by, decided_at, decision_comment, ledger_entry_id, created_at, row_version
  FROM benefit.entitlement_adjustment
 WHERE tenant_id = $1 AND id = $2;

-- name: GetEntitlementAdjustmentForUpdate :one
SELECT id, entitlement_account_id,
       delta_quantity::text AS delta_quantity,
       reason_code, reason_text, status, requested_by, requested_at,
       decided_by, decided_at, decision_comment, ledger_entry_id, created_at, row_version
  FROM benefit.entitlement_adjustment
 WHERE tenant_id = $1 AND id = $2
   FOR UPDATE;

-- name: ListEntitlementAdjustments :many
SELECT id, entitlement_account_id,
       delta_quantity::text AS delta_quantity,
       reason_code, reason_text, status, requested_by, requested_at,
       decided_by, decided_at, decision_comment, ledger_entry_id, created_at, row_version
  FROM benefit.entitlement_adjustment
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY created_at DESC, id DESC
 LIMIT sqlc.arg('page_size');

-- name: DecideEntitlementAdjustment :execrows
-- ck_adjustment_maker_checker and ck_adjustment_decision are the database's guards
-- behind the service-side maker-checker check.
UPDATE benefit.entitlement_adjustment
   SET status = sqlc.arg('status'),
       decided_by = sqlc.arg('decided_by'),
       decided_at = clock_timestamp(),
       decision_comment = sqlc.narg('decision_comment'),
       ledger_entry_id = sqlc.narg('ledger_entry_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'PENDING'
   AND row_version = sqlc.arg('row_version');

-- name: ListEntitlementAccountDrift :many
-- The daily reconciliation: every account whose materialised balances disagree with the
-- sum of its ledger deltas. An account with no movements must be all zeros.
SELECT a.id,
       a.enrollment_id,
       a.status,
       a.total_granted::text      AS account_total,
       a.available_quantity::text AS account_available,
       a.reserved_quantity::text  AS account_reserved,
       a.consumed_quantity::text  AS account_consumed,
       a.expired_quantity::text   AS account_expired,
       coalesce(l.delta_total, 0)::text     AS ledger_total,
       coalesce(l.delta_available, 0)::text AS ledger_available,
       coalesce(l.delta_reserved, 0)::text  AS ledger_reserved,
       coalesce(l.delta_consumed, 0)::text  AS ledger_consumed,
       coalesce(l.delta_expired, 0)::text   AS ledger_expired
  FROM benefit.entitlement_account a
  LEFT JOIN LATERAL (
       SELECT sum(le.delta_total)     AS delta_total,
              sum(le.delta_available) AS delta_available,
              sum(le.delta_reserved)  AS delta_reserved,
              sum(le.delta_consumed)  AS delta_consumed,
              sum(le.delta_expired)   AS delta_expired
         FROM benefit.entitlement_ledger le
        WHERE le.tenant_id = a.tenant_id AND le.entitlement_account_id = a.id) l ON true
 WHERE a.tenant_id = sqlc.arg('tenant_id')
   AND (a.total_granted      <> coalesce(l.delta_total, 0)
     OR a.available_quantity <> coalesce(l.delta_available, 0)
     OR a.reserved_quantity  <> coalesce(l.delta_reserved, 0)
     OR a.consumed_quantity  <> coalesce(l.delta_consumed, 0)
     OR a.expired_quantity   <> coalesce(l.delta_expired, 0))
 ORDER BY a.id
 LIMIT sqlc.arg('page_size');

-- name: CountEntitlementAccounts :one
SELECT count(*) FROM benefit.entitlement_account WHERE tenant_id = $1;

-- name: GetEnrollmentForEntitlement :one
-- Everything EnsureAccounts needs about an enrollment, including the principal
-- membership that family-shared definitions resolve through.
SELECT e.id, e.plan_id, e.sponsor_membership_id, e.status,
       lower(e.valid_period)::date AS valid_from,
       upper(e.valid_period)::date AS valid_to,
       m.person_id, m.principal_membership_id
  FROM benefit.enrollment e
  JOIN party.sponsor_membership m ON m.tenant_id = e.tenant_id AND m.id = e.sponsor_membership_id
 WHERE e.tenant_id = $1 AND e.id = $2;

-- name: FindPrincipalEnrollment :one
-- The principal's enrollment in the same plan on as_of; family-shared accounts are
-- opened there and the dependant reaches them through ResolveAccounts.
SELECT e.id, lower(e.valid_period)::date AS valid_from
  FROM benefit.enrollment e
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.sponsor_membership_id = sqlc.arg('sponsor_membership_id')
   AND e.plan_id = sqlc.arg('plan_id')
   AND e.status IN ('PENDING','ACTIVE')
   AND e.valid_period @> sqlc.arg('as_of')::date
 ORDER BY lower(e.valid_period) DESC
 LIMIT 1;

-- name: ExtendEntitlementReservationExpiry :execrows
-- Move an open hold's deadline later (WP-I6-02's adoption: a booking's hold expires with
-- the fifteen-minute countdown, and the authorization that confirms it moves that deadline
-- out to the end of the stay).
--
-- It only ever moves the deadline forward, and the predicate says so rather than the
-- caller: a statement that could pull it back would be a way to expire somebody's hold
-- early, and a redelivered confirmation that ran this twice must be the same as running it
-- once. A hold with no deadline at all is left alone -- it already outlives every date this
-- would set.
UPDATE benefit.entitlement_reservation
   SET expires_at = sqlc.arg('expires_at')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('HELD','PARTIALLY_CONSUMED')
   AND expires_at IS NOT NULL
   AND expires_at < sqlc.arg('expires_at');
