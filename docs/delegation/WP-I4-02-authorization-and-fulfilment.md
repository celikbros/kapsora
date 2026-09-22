# WP-I4-02 · Authorization, fulfilment and vouchers

| Field                      | Value                                                                                                                                                                                                                                            |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Milestone                  | M4 (plan increment I4)                                                                                                                                                                                                                           |
| Size                       | L                                                                                                                                                                                                                                                |
| Depends on                 | WP-I4-01, WP-I2-03 (the entitlement ledger and its reservations), WP-I3-05 (pricing)                                                                                                                                                             |
| Runs in parallel with      | WP-I4-03                                                                                                                                                                                                                                         |
| Migration numbers assigned | `000026_authorization_and_fulfilment.up.sql`                                                                                                                                                                                                     |
| OpenAPI operations owned   | `createAuthorization`, `getAuthorization`, `listAuthorizations`, `extendAuthorization`, `cancelAuthorization`, `createFulfilment`, `getFulfilment`, `listFulfilments`, `completeFulfilment`, `cancelFulfilment`, `issueVoucher`, `redeemVoucher` |
| Read first                 | v1.2 9.11, 11.4, 16.7; **WP-I2-03 §reservations** — the reserve/release/consume semantics this package drives; ADR-015                                                                                                                           |

## 1. Goal

An approval is a promise, and this package is where the promise costs something. An
authorization reserves entitlement so the same balance cannot be promised twice; a
fulfilment records what was actually delivered and turns the reservation into consumption;
a voucher is the token the member shows at the counter.

The ledger already knows how to reserve, release and consume without double spending
(WP-I2-03, proven under 100 concurrent reserves). This package must use it and must never
write a balance itself.

### PC-02 clarification (2026-09-22)

Generic request approval changes the decision; the explicit authorization command reserves
entitlement. The request UI now exposes that command to `authorization.manage` and shows its
reference/status to the provider. Migration 000051 adds internal `entitlement_unit_factor`
(positive numeric(20,6), default 1). New holds resolve the request enrollment's published plan
on its service date, using explicit mappings before the legacy service-code convention.
Authorization counters keep service quantities; ledger reserve/consume/release use the stored
factor. Cumulative rounding preserves fractional holds across split delivery/release, and
cancel/expiry release the ledger's actual remainder after a prior partial release. Historical
rows keep factor 1, matching the units actually reserved; no retrospective balance rewrite.
Replay reads enforce provider scope. See the current roadmap checkpoint for test evidence and
the still-pending restarted real-system handoff; the original acceptance checklist below is
historical, not current PC-02 certification.

## 2. Scope

### 2.1 Schema (migration 000026)

`service.authorization`: id, tenant_id, request_id (composite FK), `authorization_reference`
(unique per tenant), `valid_from`, `valid_to`, `status`
(`ACTIVE`,`PARTIALLY_USED`,`USED`,`EXPIRED`,`CANCELLED`), `reserved_total numeric(20,6)`,
`consumed_total numeric(20,6)`, `currency_code`, `price_quote_id` (nullable composite FK to
`contract.price_quote`), `approved_by`, `approved_at`, `cancel_reason_code`, row_version.
CHECK `consumed_total <= reserved_total`. Partial index on `(tenant_id, status, valid_to)`
for the expiry sweep.

`service.authorization_item`: id, tenant_id, authorization_id, `request_item_id`,
`service_definition_id`, `approved_quantity numeric(20,6)` CHECK > 0,
`approved_amount numeric(20,6)`, `member_amount numeric(20,6)`,
`entitlement_reservation_id` (composite FK into the ledger's reservation table),
`consumed_quantity numeric(20,6)` NOT NULL DEFAULT 0 CHECK
`consumed_quantity <= approved_quantity`. Unique `(tenant_id, authorization_id, request_item_id)`.

`service.fulfilment`: id, tenant_id, `fulfilment_reference` (unique), authorization_id,
`provider_profile_id`, `location_id`, `practitioner_id` (all nullable composite FKs),
`performed_at`, `status` (`RECORDED`,`COMPLETED`,`CANCELLED`), `recorded_by`, row_version.

`service.fulfilment_item`: id, tenant_id, fulfilment_id, authorization_item_id,
`service_definition_id`, `actual_quantity numeric(20,6)` CHECK > 0, `actual_amount`.

`service.voucher`: id, tenant_id, authorization_id, `token_hash bytea` (32 bytes),
`token_masked text`, `valid_from`, `valid_to`, `status` (`ISSUED`,`REDEEMED`,`EXPIRED`,`REVOKED`),
`redeemed_at`, `redeemed_by_actor_id`. Unique `(tenant_id, token_hash)`. **The plaintext
token is never stored**: it is returned once on issue and only its digest is kept, exactly
as the session cookie is handled in WP-I1-01.

RLS, touch triggers, composite keys throughout.

### 2.2 Creating an authorization reserves

`createAuthorization` runs in one transaction:

1. The request must be APPROVED or PARTIALLY_APPROVED; anything else answers 409
   `REQUEST_NOT_APPROVED`.
2. For each approved item, call the ledger's **reserve** with the item's quantity. A
   refusal (`BALANCE_INSUFFICIENT`, `ENTITLEMENT_ACCOUNT_FROZEN`) fails the whole
   authorization: a half-reserved approval is a promise nobody can keep.
3. Store the reservation id on each authorization item, so release and consume later act
   on the exact hold this authorization took.
4. Optionally attach the `price_quote_id` the approval was made against, so what was
   quoted and what was authorized can be compared later.

`Idempotency-Key` is required. Creating twice with the same key returns the same
authorization and takes **one** set of reservations.

### 2.3 Fulfilment consumes, cancellation releases

- `completeFulfilment` consumes from the reservation, item by item, for the actual
  quantities. Consuming more than was approved answers 422 `OVER_FULFILMENT`; consuming
  less leaves the remainder reserved until the authorization expires or is cancelled.
- `cancelAuthorization` releases every outstanding reservation and sets CANCELLED. It
  needs a reason code.
- **Expiry is a scheduler job**, idempotent, that releases the reservations of
  authorizations past `valid_to` and marks them EXPIRED. Running it twice releases
  nothing twice; there is a test.
- `extendAuthorization` moves `valid_to` forward only, needs `service_request.review`, and
  refuses once the authorization is not ACTIVE.

### 2.4 Vouchers

`issueVoucher` generates a token with `crypto/rand`, returns it **once** in the response
body, and stores only `sha256(token)` plus a masked form. `redeemVoucher` takes the token,
hashes it, and marks the voucher REDEEMED inside the same transaction that records the
fulfilment. A second redemption answers 409 `VOUCHER_ALREADY_REDEEMED`. An expired or
revoked voucher answers 409 with its own code. The token never appears in a URL, a query
key, a log or an audit row.

### 2.5 Permissions this package adds

The permission catalogue (migration 000008) and the role templates
(`internal/identity/application/roles.go`) are two separate places. A permission seeded
into the first but missing from the second is a permission nobody can ever hold — that
already happened once, with `pricing.quote`. Add both, in this package's migration and in
the same commit.

New: `authorization.manage` (create, extend, cancel), `fulfilment.record`,
`voucher.redeem`. Grant `authorization.manage` to PROGRAM_MANAGER and MEDICAL_REVIEWER,
`fulfilment.record` and `voucher.redeem` to PROVIDER_STAFF and PROGRAM_MANAGER.

## 3. Tests required

- **No double promise**: 50 concurrent `createAuthorization` calls against a balance that
  covers 20 of them leave exactly 20 authorizations and 20 reservations, and the account's
  reserved quantity equals the sum. This is the WP-I2-03 concurrency test one level up.
- A failed reserve on the second item leaves no authorization and no reservation at all.
- Idempotent replay takes one set of reservations, not two.
- Consume, partial consume, over-consume refused; release on cancel; the expiry job run
  twice releases once.
- Voucher: the plaintext appears in no table, no log and no audit row (scan the columns
  and the captured log buffer); redeeming twice is refused; redemption and fulfilment are
  one transaction, so a failed fulfilment leaves the voucher unredeemed.
- Ledger conservation: after every flow, `reserved + consumed + available + expired`
  equals `total_granted` for every touched account.

## 4. Acceptance criteria

- [ ] An approval reserves entitlement, and the same balance can never be promised twice.
- [ ] A half-reserved authorization cannot exist.
- [ ] Fulfilment consumes exactly what it delivered; the rest stays reserved until it
      expires or is cancelled, and the expiry job is safe to run repeatedly.
- [ ] The voucher token is shown once and stored only as a digest.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; schema version 26.
