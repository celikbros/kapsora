# WP-I2-03 · Entitlement accounts, ledger movements, reservations (no double spend)

| Field | Value |
|---|---|
| Milestone | M2 (plan increment I2) |
| Size | L |
| Depends on | WP-I2-02 (plan versions, entitlement definitions, enrollments) |
| Runs in parallel with | WP-I2-01 |
| Migration numbers assigned | `000015_benefit_entitlement_reservation.up.sql` (D8) |
| OpenAPI operations owned | `listPersonEntitlements`, `getEntitlementAccount`, `listEntitlementLedger`, `createEntitlementAdjustment`, `approveEntitlementAdjustment`; internal Go API for reserve/release/consume/reverse used by later increments |
| Read first | v1.2 11.4, 13.5 (transaction sketch), 16.5 (`entitlement_*`), 31.2 (reconciliation); migration 000005 constraints (`ck_entitlement_account_balance`, `ck_entitlement_ledger_conservation`, append-only ledger) |

## 1. Goal

The ledger is the record; account balances are materialised in the same transaction.
Reserving an entitlement for a request or booking can never exceed the available balance
under concurrency, every movement is idempotent, history is never edited, and a daily job
proves ledger sums equal account balances.

## 2. Scope

### 2.1 Migration 000015

```sql
CREATE TABLE benefit.entitlement_reservation (
  id uuid PK, tenant_id, entitlement_account_id (composite FK),
  reference_type text NOT NULL,      -- SERVICE_REQUEST | BOOKING | AUTHORIZATION | MANUAL
  reference_id uuid NOT NULL,
  quantity numeric(20,6) NOT NULL CHECK (quantity > 0),
  consumed_quantity numeric(20,6) NOT NULL DEFAULT 0 CHECK (consumed_quantity >= 0 AND consumed_quantity <= quantity),
  status text NOT NULL CHECK (status IN ('HELD','PARTIALLY_CONSUMED','CONSUMED','RELEASED','EXPIRED')),
  expires_at timestamptz,
  idempotency_key text NOT NULL,
  created_by uuid, created_at, updated_at, row_version,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, entitlement_account_id, idempotency_key),
  UNIQUE (tenant_id, reference_type, reference_id, entitlement_account_id)
);
-- partial index for the expiry job: (tenant_id, expires_at) WHERE status IN ('HELD','PARTIALLY_CONSUMED')
-- RLS, touch_row, grants like the other benefit tables.
ALTER TABLE benefit.entitlement_ledger ADD COLUMN reservation_id uuid;  -- composite FK, nullable
-- CHECK: movement_type IN ('RESERVE','RELEASE','CONSUME') requires reservation_id IS NOT NULL
--        (CONSUME without reservation allowed only with reason_code = 'DIRECT_CONSUME').
```

### 2.2 Accounts

- Opened by the outbox consumer of `benefit.enrollment.created` (worker) and, for
  robustness, lazily by `EnsureAccounts(ctx, tx, enrollmentID, asOf)` when a reserve or
  eligibility check finds none: one account per entitlement definition of the plan
  version valid at `asOf`, with `benefit_period` computed from `period_type`
  (`CALENDAR_YEAR` → Jan 1..Jan 1 next year; `PLAN_YEAR` → version `valid_period` lower
  bound anniversaries; `ROLLING_DAYS` → `[asOf, asOf + period_length)`; `LIFETIME` →
  `[enrollment start, infinity)`; `CUSTOM` → definition metadata, else reject), and an
  initial `GRANT` movement of `initial_quantity`. Opening is idempotent on
  `uq_entitlement_account_period`.
- `family_shared` definitions open one account on the principal's enrollment; dependants
  resolve to it through `principal_membership_id` (`ResolveAccount`).

### 2.3 Movements (Go API, package `internal/benefit/ledger`)

```go
type Ledger interface {
  Reserve(ctx, tx, in ReserveInput) (Reservation, error)   // FOR UPDATE on account; available >= qty unless allow_overdraft; idempotent on (account, key)
  Release(ctx, tx, reservationID, qty, key, reason) error  // up to the un-consumed remainder
  Consume(ctx, tx, reservationID, qty, key, reason) error  // from reserved; partial allowed
  Reverse(ctx, tx, ledgerEntryID, key, reason) error       // opposite delta, never edits
  Adjust(ctx, tx, accountID, delta, key, reason) error     // maker-checker: see 2.4
  Expire(ctx, tx, now) (int, error)                        // scheduler: HELD past expires_at → RELEASE
}
```

Rules: every call runs `SELECT ... FROM benefit.entitlement_account WHERE tenant_id=$1
AND id=$2 FOR UPDATE` first; the ledger row is inserted with the delta vector, the
account row updated with the same deltas in the same statement batch; `ErrInsufficient`
when `available - qty < 0` and `allow_overdraft = false`; `ErrIdempotentReplay` returns
the existing reservation when the key was seen with the same quantity, 409
`IDEMPOTENCY_KEY_REUSED` when the quantity differs.

### 2.4 Manual adjustments (maker-checker)

- `POST /entitlement-accounts/{id}/adjustments` (`entitlement.adjust`): creates a
  pending adjustment (`benefit.entitlement_adjustment` — add to migration 000015:
  quantity delta, reason, requested_by, status PENDING|APPROVED|REJECTED, approved_by).
- `POST /entitlement-adjustments/{id}/approve|reject` (`entitlement.adjust`, step-up,
  approver ≠ requester): approve writes the `ADJUST` movement.

### 2.5 Read endpoints

- `GET /people/{personId}/entitlements` (`entitlement.read`): accounts of the person's
  enrollments (including family-shared accounts resolved through the principal) with
  balances and the definition summary.
- `GET /entitlement-accounts/{id}` and `GET /entitlement-accounts/{id}/ledger`
  (paged, newest first) with reservation summaries.

### 2.6 Scheduler jobs

- `entitlement.reservation.expire` every minute: releases expired holds in batches of 200.
- `entitlement.reconcile` daily: for every account compare `sum(delta_*)` from the ledger
  with the account columns; drift → audit event `entitlement.drift` with SYSTEM category,
  outbox event for alerting, and the account is set FROZEN.

## 3. Tests required

- **Concurrency (dbtest):** one account with 100 available; 100 goroutines each reserve 2
  with distinct keys through separate connections; exactly 50 succeed, 50 get
  `ErrInsufficient`; `available = 0`, `reserved = 100`, ledger sum equals account,
  `ck_entitlement_account_balance` never violated. Repeat with `allow_overdraft = true`
  (all succeed, available = -100). Repeat with 100 goroutines using the SAME key: one row,
  99 replays.
- Release/consume partials, reverse of a consume, expiry job, reconciliation detects an
  injected drift (admin UPDATE of the account) and freezes the account.
- Family-shared account resolution for a dependant.
- Adjustment maker-checker: same actor rejected; approved adjustment appears in ledger.
- db/tests: `expectedSchemaVersion` 15; new tables RLS; ledger still append-only.

## 4. Acceptance criteria

- [ ] 100 concurrent reserves: no double spend (test in CI, `-race`).
- [ ] Every movement idempotent by key; replays return the original result.
- [ ] Ledger rows never updated or deleted (append-only trigger) and always reference a reservation for RESERVE/RELEASE/CONSUME.
- [ ] Reconciliation job proves ledger = balances and freezes on drift.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean.
