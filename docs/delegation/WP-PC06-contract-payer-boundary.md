# WP-PC06 · Preserve the member's payer boundary in accommodation prices

| Field | Value |
| --- | --- |
| Status | ACTIVE; implementation pending |
| Planned | 2026-10-09, gpt-6-astra |
| Outcome | Only contracts of the member's enrollment-derived payers enter accommodation price selection |
| Migration / public API / permissions | None; no grants or role changes |
| Acceptance | Isolated synthetic evidence; no live action authorized |

## 1. Existing policy and concrete defect

Read the delegate handbook, current ROADMAP/HANDOVER PC-06 checkpoint and
WP-PC06-contract-hold-duration.md. This is a separate correction after that hold-duration
work, not an expansion of its acceptance claim. The integrator owns CI, roadmap and
handover updates. Do not apply migration 000058 or run local database tests; the local
schema remains 57 and the shared application-role password must be protected.

Baseline v1.2 section 11.5 orders selection by tenant, payer/sponsor program,
provider/location, service, date, contract status, then priority/specificity. WP-I6-01
section 2.3 requires quotes under the person's enrollment and provider contract. The
OpenAPI AvailabilitySearchRequest.programId description explicitly says an optional
program narrows a member enrolled in two programs, and a program without enrollment on
the first night selects nothing. This is existing policy, not a new authorization rule.
Migration 000021 makes contract.contract.payer_organization_id NOT NULL; there is no
global/null-payer contract fallback to preserve.

In application/availability.go, loadWorld obtains ListPersonProgramPayers using person,
check-in and optional program. It normalizes nil to an empty array and applies the result
to property visibility. However PriceCandidateQuery has no payer parameter, and
ListAccommodationPriceCandidates loads prices from every active contract of those
providers. The mapping and shared selector do not subsequently remove other payers.
One qualifying contract can therefore make a property visible while another payer's
contract supplies its price or creates a false ambiguity.

Reproduce with a single provider and property, an A-only member, a published A contract
and a published B contract for the same room service and dates. Give B higher item/list
priority or specificity: B incorrectly wins today. Equal ranking instead produces a
false PRICE_AMBIGUOUS. Merely making B cheaper is not a valid reproduction, because
amount is not a ranking criterion. This package requires an actual isolated SQL/HTTP
reproduction; static source inspection alone is not acceptance evidence.

## 2. Exact implementation

1. Add PayerOrganizationIDs []uuid.UUID to accommodation's PriceCandidateQuery. Document
   that it is required enrollment-derived scope: nil and empty both select no candidates.
2. Pass the existing normalized payers from loadWorld to this query. Preserve the current
   program argument and enrollment lookup. No payer identifier comes from the HTTP caller.
3. In db/queries/accommodation.sql add the unconditional predicate
   `c.payer_organization_id = ANY(sqlc.arg('payer_organization_ids')::uuid[])` to
   ListAccommodationPriceCandidates. Do not add an IS NULL bypass or reuse the property
   query's unrestricted-backoffice convention. Update its stale "narrowed in none" comment.
4. Pass the array in infrastructure/postgres/repository.go and regenerate sqlc; commit
   generated changes. Keep all existing tenant/provider/status/date/service predicates,
   ordering, joins and cardinality, including the exact version's nullable hold minutes.
5. Update affected fake/mock repository implementations and direct query fixtures so
   intended successful reads explicitly supply eligible payer IDs. Do not weaken production
   semantics to keep zero-value fixtures passing. Search scoped call sites and compile them.

No change to shared contract/selection Request, Candidate, scoring or ranking is needed:
the repository supplies only scoped candidates. No new query round trip, schema, index,
gateway, public field, permission or management setting is required. Accommodation query,
generated code, ports, availability loading, PostgreSQL mapping and focused tests are the
implementation scope. The hold and pricing calculations otherwise remain unchanged.

## 3. Program and enrollment boundary of this patch

Explicit program A must constrain property visibility and candidate prices to the payers
derived from active enrollment in A, even if the person also belongs to B. An omitted
program retains the current union of enrolled program payers for search; this patch does
not invent a preferred payer or program or choose the cheapest eligible contract.

Do not propagate prepareHold's resolved plan.ProgramID as an incidental change here.
GetPersonEnrollmentForStay currently chooses ORDER BY e.id LIMIT 1, whereas eligibility
orders latest enrollment start then ID and reports multiple enrollment. Both loadWorld
and checkEligibility currently receive the original optional ProgramID. Substituting the
resolved program would silently alter unspecified-program behavior without aligning the
enrollment used for eligibility, quote and reservation. That needs its own reproduction
and coherent enrollment design, including multiple enrollments within one program.
The narrow payer-filter fix is valid independently and must not be claimed as proof of
full multi-enrollment consistency. Explicit-program propagation is already present; test
it rather than changing the public selection contract.

## 4. Required evidence

Use synthetic fixtures in the existing accommodation availability/booking suites and
the existing dbtest harness in isolated CI. Prepare terms while versions are drafts and
publish normally; do not mutate real contracts or disable constraints. A local pure test
may assert that loadWorld passes the derived payer set, but it cannot replace SQL proof.

| Case | Required assertion |
| --- | --- |
| Query propagation | The exact enrollment-derived set reaches PriceCandidateQuery; explicit program reaches the payer lookup; nil lookup normalizes to empty scope |
| Actual SQL nil and empty | Direct repository calls with nil and empty arrays return zero candidates, even with matching provider/service contracts; do not satisfy this case solely with a mock or application early return |
| Actual SQL two payers | A-only returns A rows, B-only returns B rows, A+B returns both; unrelated payer rows never appear |
| Wrong-payer higher rank | HTTP availability quotes A and HTTP create-hold freezes A amounts although B would outrank A if loaded |
| Wrong-payer tie | Equal-ranked B does not cause ambiguity after filtering; the test demonstrably fails on the original query |
| Same-payer tie | Two equal-ranked A prices still produce PRICE_AMBIGUOUS and refuse a hold with no booking, inventory or reservation mutation |
| No own-payer service price | A contract makes the property visible but only B prices the requested room service; quote is unavailable and hold writes no booking/inventory/reservation |
| Program narrowing | A+B member explicitly choosing A excludes B; omitted-program search preserves the existing union; unenrolled program cannot broaden the set |
| Enrollment boundary | Missing/inactive/out-of-period enrollment selects nothing through the real payer lookup; no unrestricted fallback |
| First-night policy metadata | A's selected first-night hold duration controls exact persisted booking and reservation expiry; B's different duration cannot supply it |
| Existing boundaries | Tenant/provider restrictions, version dates and unsupported candidate behavior remain; no new contract-read/manage permission for allowed search/hold |

Use frozen time and exact decimal assertions. Verify unchanged reservation quantities and
single hold movement for the successful regression, and no mutation on unavailable/tied
holds. Existing scoped booking lifecycle tests remain required; do not replay settled live
payment, cancellation or owner-acceptance journeys. Test fixtures can share setup but must
exercise production SQL and HTTP wiring where specified.

Run focused application and selector unit tests, compile affected packages, verify sqlc
generation and formatting, and run scoped vet/lint. The integrator runs isolated
PostgreSQL-backed HTTP/repository proof and required CI gates; report local skips explicitly.
Provide exact test names, commands, results and code head. Mutation evidence should show
removing the payer predicate makes the high-rank and false-tie regressions fail.

## 5. Separate confirmation follow-up, not acceptance here

WP-I6-04 section 2.2 also requires confirmation policy for the person's program.
quoteRoomTypeWithSelection now retains the actual first-night ContractVersionID, but
QuoteSnapshot discards it. policySnapshot uses ContractVersionForProperty, whose SQL
chooses provider-wide domain/latest-date/ID without the winning price or payer. Both the
initial confirmation preflight and asynchronous booking decision call that helper.

A separate package should persist the actual first-night version in internal quote
metadata and use it for both paths, preserving the selected version's frozen terms and
missing-terms refusal. No table migration appears necessary. However old held/pending
bookings have no winning identity: a current price or matching amount cannot reliably
reconstruct it. Explicitly decide legacy snapshot compatibility before implementing that
package; do not silently reuse the broad latest-contract fallback or rewrite confirmed
policy snapshots. This package neither changes confirmation nor certifies that boundary.
