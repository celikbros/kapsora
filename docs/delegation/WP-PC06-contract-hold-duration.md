# WP-PC06 · Apply the selected contract's lodging hold duration

| Field | Value |
| --- | --- |
| Status | ACTIVE; bounded implementation and isolated evidence in progress |
| Planned | 2026-10-09, gpt-6-astra |
| Outcome | A published contract's optional hold duration governs the room hold quoted under that contract |
| Migration | None |
| Permissions / grants | No changes |
| Live acceptance | Not authorized by this package; isolated synthetic tests only |

## 1. Defect and settled policy

`internal/accommodation/application/booking.go` currently sets `preparedHold.expiresAt`
from `settings.HoldMinutes` unconditionally. A selected contract whose lodging terms say
30 minutes therefore still produces a 15-minute hold when the tenant uses its default.
The later policy gateway copies `HoldMinutes` into the confirmation snapshot, which does
not repair the hold that was already created.

This is an existing implementation omission. Baseline v1.2 section 11.11 explicitly says
hold duration defaults to 15 minutes and may vary by tenant/provider policy. WP-I6-04
section 2.1 defines nullable `contract.lodging_terms.hold_minutes` as the provider override;
migration 000039 documents NULL as the tenant default and constrains a present value to
1–1440. `contract/domain/lodging.go` enforces the same interval. WP-I6-02 section 2.2's
tenant-only expiry formula describes the default and did not wire the companion override;
it is not a prohibition on the expressly documented provider override. No new duration,
privilege, approval or financial policy is introduced here.

Read those sources, the delegate handbook, ADR-004/008/015/016 and the current HANDOVER
PC-06 checkpoint before implementation. Preserve the current public hold endpoint and
booking representation. This document does not accept MGT-03B or authorize MGT-04's
proposed settings permissions.

## 2. Exact contract selection and effect

Choose the override from the **actual winning price candidate for the check-in night**
produced by the existing `quoteRoomType` selection during `prepareHold`:

- Property/room visibility, provider scope, active service, enrollment and eligibility
  remain enforced by the existing prepare flow.
- `ListAccommodationPriceCandidates` already loads tenant-owned price items joined by
  tenant to price list, published version and ACTIVE contract for the property's provider.
- `applicableCandidates` enforces the version's half-open validity per stay night.
- `contract/selection.Select` applies item validity, season, weekday, service definition /
  containing package / category specificity, location and item/list priority. A tied top
  price remains `PRICE_AMBIGUOUS`; missing price remains the existing refusal.
- Retain the selected first-night candidate's `ContractVersionID` and optional hold value
  from that exact candidate. A higher-ranked or later unrelated contract cannot supply the
  duration. Do not choose MAX/MIN duration, latest version, a caller-provided version,
  provider-wide terms or an arbitrary non-null override from another night.

Check-in-night policy is the bounded interpretation of the current booking model: the
confirmation selector's documented intent in `GetContractVersionForRoomType` is the first
night's price contract, and WP-I6-04 snapshots the version effective on check-in. A stay
can legitimately span price versions; later-night prices still participate in the total,
but do not change the one hold countdown. Add an explicit cross-version test.

Effective minutes = selected first-night override when present, otherwise the tenant's
validated `accommodation.hold_minutes`, whose missing/invalid fallback remains 15. Missing
lodging terms at hold time mean there is no override, preserving today's ability to hold;
confirmation continues to refuse missing terms under its existing policy. Do not pull
that confirmation refusal earlier as an incidental behavior change.

Compute the expiry once at the existing pre-lock preparation point using the injected
server clock, then carry that exact timestamp into both `booking.hold_expires_at` and the
entitlement reservation expiry. Preserve the existing timing boundary and lock order.
A 30-minute contract override is not capped at the tenant's 15 minutes or the quote TTL.
The quote-age check may independently expire sooner; this fix does not modify it.
Existing persisted holds are never resized, and confirmation's pending-approval extension,
release/expiry jobs, cancellation terms and ledger amounts are unchanged.

## 3. Minimal implementation

Prefer adding the optional value to the existing candidate read, avoiding a new lookup or
cross-module call during the inventory critical section:

1. In `db/queries/accommodation.sql`, LEFT JOIN `contract.lodging_terms` to the already
   selected candidate version using both tenant ID and contract-version ID. Project only
   nullable `hold_minutes` alongside the existing price metadata. Its unique
   `(tenant_id, contract_version_id)` constraint prevents multiplying price candidates.
   Preserve every existing selection predicate, ordering and cardinality.
2. Map that nullable integer into one additional field of accommodation's `PriceCandidate`
   application port in `ports.go` / `infrastructure/postgres/repository.go`. Regenerate sqlc.
   Do not add lodging fields to the shared `contract/selection.Candidate`: the pricing
   ladder does not need them and its rankings must stay identical.
3. Carry first-night selection metadata internally through `quoteRoomType` into the
   prepared hold. A private quote-selection result or internal fields with explicit
   transport projection are both acceptable; do not expose new contract or duration input
   in OpenAPI, return internal candidate details, or change money calculation.
4. A small checked resolver combines optional contract minutes and the already-validated
   tenant value. `prepareHold` uses its answer when computing expiry. Update the stale
   tenant-only `CreateHold` comment and any directly affected test-fixture documentation.

No new gateway or repository method is needed for this design. In particular, do not call
`BookingRepository.ContractVersionForProperty`: despite its comment, the actual query
chooses a provider contract by domain preference/latest validity/ID and does not join the
winning price item or service. Reusing it would replace this defect with the wrong
contract's duration. Do not call `SnapshotPolicy` during hold creation just to parse JSON;
that would add a transaction and introduce unrelated missing-policy behavior.

Contract values 0, negative or above 1440 are invalid, never absence. Normal persisted rows
cannot contain them under migration 000039, and public contract validation already refuses
them. A corrupted/fake selected value fails the hold preparation as an internal invariant
error through the existing safe 500 mapping; it must never be clamped, ignored or replaced
by the tenant duration. Reject only the selected value rather than letting an irrelevant
losing candidate change the outcome. No booking, inventory or reservation writes follow
this failure; the existing eligibility evaluation may already have been recorded.

No schema, new permission, role template, seed command, server control, UI form, settings API
or public contract change is needed. If implementation finds any necessary expansion,
report the concrete dependency to the integrator before taking it on.

## 4. Adjacent concerns recorded, not silently corrected

The existing confirmation policy selector is broader than the quote winner. This package
must not rewrite cancellation/no-show policy selection or reinterpret old booking snapshots.
Its proof covers the initial hold duration only; record the confirmation discrepancy for
separate review.

The full inspected accommodation quote path also has a separate payer-boundary concern:
`loadWorld` obtains member program payers and applies them to property visibility, but
`PriceCandidateQuery`/SQL do not carry them; the repository mapping, `applicableCandidates`,
`selection.Request`/`Candidate`/`score` and `quoteRoomType` contain no later payer filter.
Thus a same-provider different-payer price can enter selection after property visibility
is established. This is static evidence, not a reproduced multi-payer fixture or live
incident. The integrator should track/reproduce it separately. Do not claim this hold
fix certifies multi-payer quote correctness, and do not change quote prices or candidate
payer policy within this patch. Foreign-tenant/provider tests below cover their existing
boundaries and are not substitutes for that separate payer investigation.

## 5. Required evidence

Use the existing deterministic clock, `dbtest` harness and booking fixture under
`internal/accommodation/transport/http/booking_test.go` / `handler_test.go`; extend the
application availability tests for pure selection metadata. Prepare synthetic draft terms
before publishing them. Do not alter real published contracts, disable constraints or
reuse a live booking journey to obtain these fixtures.

| Case | Required assertion |
| --- | --- |
| Override 30 / tenant 15 | First-night winner yields exactly now + 30 minutes; persisted booking and reservation expiry match; HTTP countdown is 1800 at the frozen clock |
| NULL override / tenant 15 | Exactly 15 minutes; a losing candidate's non-null value is not borrowed |
| No lodging terms | Hold retains tenant fallback; existing confirmation refusal remains unchanged |
| Tenant override 20 | NULL contract uses 20, proving fallback is not hard-coded to 15 |
| Boundaries | Selected contract 1 and 1440 accepted; resolver rejects 0, negative and 1441; existing contract validation/database CHECK rejects invalid persisted values |
| Specificity | Exact service wins over category/package according to the unchanged ladder, and its duration wins with it; location/priority behavior remains unchanged |
| Version transition | First night uses version A's override; later nights price under B without replacing the hold duration; exclusive valid_to and next valid_from are honored |
| Unsupported candidates | DRAFT/inactive/expired/not-yet-effective, wrong service/location, different provider and foreign-tenant candidates cannot supply a hold duration |
| Ambiguous/unpriced | Same existing quote refusal, with no inventory/reservation/booking change |
| Lifecycle conservation | One hold produces one reservation and the same quantities as before; release or expiry releases once; confirmation still adopts the existing reservation |
| Existing hold immutability | A later tenant configuration change or other published version does not change stored expiry or add reservation movement |
| HTTP permissions | Existing member PERSON boundary and provider organization restriction remain; no new contract-read/manage permission is required merely to create a permitted hold |

Add a focused HTTP idempotency regression with the actual create-hold middleware used by
`cmd/api`: the current `newServer` fixture mounts handlers without command middleware, so
two direct service calls are not proof of Idempotency-Key behavior. An identical successful
request replay returns the same booking and original expiry/countdown response and creates
no second reservation, booking or inventory movement, including after the test clock
advances. Same key/different body remains refused. Also retain the active duplicate-booking
conflict under a fresh key. Do not redesign global idempotency or claim a new post-commit
process-crash guarantee that this patch does not implement.

Run focused application/selection, PostgreSQL-backed HTTP booking and contract lodging
validation tests; verify generated sqlc is current and run Go formatting/vet/lint and
required isolated CI checks. Run existing oversell/expiry/adoption cases when they are part
of the scoped booking suite rather than replacing them with mocked arithmetic. Report any
skips explicitly. No browser/payment/end-to-end booking rerun is required: the public shape
and UI have not changed, and the isolated HTTP/database proof exercises the actual expiry
and ledger behavior.

## 6. Completion and integration

Deliver the bounded diff, exact test commands/results and code head to the integrator.
Completion requires the 30-vs-15 regression, NULL fallback, exact winner/version binding,
foreign/invalid candidate refusal and no duplicate reservation proof. No migration number
is allocated and no running database has to be modified. The integrator owns roadmap,
handover and MGT-04 updates; this delegate may write only this work-package file during
planning. A later live confirmation, if separately requested, follows operator reload and
a bounded synthetic plan rather than repeating settled financial journeys.
