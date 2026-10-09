# WP-PC06 · Convert lodging nights to mapped entitlement units

| Field | Value |
| --- | --- |
| Status | REPRODUCTION IN PROGRESS; isolated CI evidence and implementation pending |
| Planned | 2026-10-09, gpt-6-astra |
| Prerequisite | Bounded factor-1 enrollment/account coherence accepted on f94c269 in CI 37984416048 |
| Outcome | NIGHT service quantities and mapped ledger quantities remain distinct through the booking lifecycle |
| Migration / public API / grants | None presumed; stop and report if implementation requires a schema or public contract expansion |
| Evidence boundary | Synthetic isolated tests; no certified live defect, deadline or runtime acceptance claim |

## 1. Source-backed scope

Read the delegate handbook (README.md), current HANDOVER/ROADMAP checkpoints and
WP-PC06-hold-enrollment-coherence.md. This is a separate follow-up to its factor-1 NIGHT
acceptance, not an expansion of that package's completed claims.

WP-I5-05 section 2.1 defines the exact positive service_entitlement_mapping.unit_factor
and requires eligibility to apply it. WP-I6-01 sections 2.1 and 2.3 explicitly depend on
that mapping and describe NIGHT service/entitlement coverage. Mapping validation in
internal/benefit/application/mapping.go accepts every positive exact decimal; migration
000035 constrains unit_factor only to greater than zero. No lodging factor-1 restriction
prevents factors such as 2, 0.5 or 0.333333.

internal/benefit/eligibility/resolver.go resolveItem applies the factor to requested service
quantity, but returns AvailableQuantity in actual account units. Accommodation's
application/availability.go checkEligibility currently truncates that raw balance into
remainingNights. quoteRoomTypeWithSelection uses it as a number of covered service nights;
booking.go reserveNights reserves CoveredNights without conversion. The confirmation
request similarly carries CoveredNights as its already-held allowance.

Authorization's application/authorization.go approvedLines initializes factor to 1.
accountsFor populates the actual factor only on the non-adoption path. holdFor's adoption
branch compares reserved ledger quantity to unconverted service quantity. Existing
application/quantity.go and fulfilment/consume paths can convert through the recorded
EntitlementUnitFactor, but adopted lodging items currently retain the default factor.
These source findings require isolated reproduction; they do not establish affected live
bookings or authorize changes to historical rows.

This package covers NIGHT entitlements with positive exact factors. MONEY lodging appears
in current quote code but its complete reservation/consumption semantics are not established
by these sources. Monetary entitlement coverage, other entitlement units and their policy
are explicitly outside this plan. Do not claim this package certifies them or silently
reinterpret money as nights. Preserve their current behavior unless a concrete safety
conflict requires a separately reviewed refusal policy.

## 2. Required semantics and private interfaces

Service quantities remain nights: quoted nightly quantity, CoveredNights, request quantity,
approved quantity and actual fulfilled/penalty nights. Ledger reserve/consume/release
quantities are entitlement units. Use existing exact Quantity/decimal operations and
six-decimal ledger conventions, never floats.

For a selected NIGHT mapping with factor f and available account quantity a, the plan
covers min(stay nights, floor(a / f)) whole nights, subject to existing eligibility rules.
Avoid rounded division that would promote an incomplete night; exact integer/decimal
comparison must prove covered*f <= a. The remaining nights stay member-paid, using the
existing per-night pricing and sharing calculation. A contract with 100 percent member
share still consumes covered service nights under the existing coverage rule; do not infer
coverage from payerAmount. Do not introduce overdraft policy or change current behavior
outside the mapped conversion.

1. Resolve the selected mapping's definition, unit and factor alongside the existing
   pinned enrollment/account scope. Carry that factor into availability's coverage
   calculation and hold preparation without a new public eligibility field. Prefer an
   existing internal result/port or a bounded internal mapping read; do not duplicate
   eligibility's account choice or broaden person-wide account access.
2. Preserve public quote quantities and monetary totals. A hold reserves CoveredNights*f
   on the exact selected account. Retain contract identity, expiry, inventory locks,
   reservation idempotency and partial coverage. No extra reserve during confirmation.
3. Freeze a private conversion record in the quote JSON: selected mapping/definition
   identity and plan version where needed to validate it, NIGHT unit, exact factor,
   covered service nights, actual reserved entitlement units and selected account identity.
   Reuse already stored identities rather than proliferating copies. A versioned internal
   struct should validate positivity and exact quantity agreement. New data must never
   appear in the public v1 quote projection. Existing JSON storage should suffice.
4. Replace the misleading internal HeldNights allowance with explicitly named entitlement
   units in BookingRequestInput/gateway wiring (or document and safely migrate the private
   field). The submission gate compares serviceQuantity*f to available+alreadyHeldUnits.
   Pass only units belonging to the referenced booking reservation, not a caller estimate.
   The booking-only SubmitInTxHolding path must pin request.EnrollmentID and the original
   selected plan version/account before adding held units. Current servicerequest
   application/submit.go resolveEligibility delegates to LoadEligibility(person, program,
   day), whose SelectEnrollments/person-wide accounts can choose another enrollment;
   held quantities keyed only by service definition cannot establish ownership. Bind the
   allowance to the exact A reservation account and mapping, including the authorized
   family-shared holder. Do not credit A's units to B's same-code account. Limit this
   change to internal booking-held submission; preserve general unpinned request behavior.
5. Carry validated private conversion data through BookingAuthorizationInput and the
   internal NewAuthorizationInput adoption path as needed. Verify reservation identity,
   account and units against the booked conversion. Set EntitlementUnitFactor on the
   adopted authorization item without creating/resolving another funded account.
   Compare reservation remaining units with approvedServiceQuantity*f, not raw nights.
   Keep approved quantity in nights; reject malformed/inconsistent conversion metadata.
6. Existing authorization item factor storage from migration 51 and cumulative
   entitlementConsumption should handle later conversion. Inspect fulfilment, cancellation,
   no-show and release callers before relying on that claim. Preserve their service-unit
   inputs and ensure conversion occurs exactly once. Where release callers currently pass
   nights straight to a ledger operation, convert with the frozen factor at that boundary.
   Freeze the originally authorized factor; do not re-read a later mapping to consume it.

Touch only required accommodation application/ports/gateway, relevant mapping queries and
sqlc outputs, eligibility internal plumbing if required, authorization adoption handling,
the booking-held servicerequest submission/repository path, and focused tests. General authorization account selection, pricing ranking, public
eligibility ordering, family-sharing policy and enrollment selection remain unchanged.

## 3. Snapshot and historical compatibility decision

Current private QuoteSnapshotVersion is 2 and already carries exact first-night contract
identity. Advance the private version for conversion-aware holds; update version checks
so contract binding remains valid. Keep old snapshot display/read compatibility and the
existing version-1 missing-contract safeguards. Do not serialize new metadata into public
responses or rewrite old quote/policy JSON. Update transport quoteSnapshotView to project
all supported internal versions (1, 2 and the new conversion version) as public wire
version 1. Its current check handles only 1 and QuoteSnapshotVersion; merely bumping that
constant would accidentally expose historical internal version 2. Add old/new snapshot
projection assertions, not only decoder tests.

For old HOLD/PENDING_APPROVAL snapshots without conversion metadata, do not assume their
intended mapped units from the current factor, from monetary totals, or from a reservation
amount that happens to divide evenly. Their old hold was actually made using one ledger
unit per covered night, regardless of configuration. Automatically multiplying it would
change historical financial effects; interpreting it as correctly mapped would invent proof.

Permit the existing factor-1 confirmation path only when the ORIGINAL eligibility
evaluation identifies the immutable plan version whose booked-service mapping is
demonstrably NIGHT/factor 1, and existing reservation account/quantity and contract-identity
checks agree. Resolve QuoteSnapshot.EvaluationID to the evaluation under tenant scope;
validate its person, enrollment and service date against the booking, then use its stored
plan_version_id for the service mapping and entitlement definition. Do not substitute
whichever version is currently PUBLISHED for the enrollment/date: later publication or
retirement must not change this historical compatibility decision. Missing or inconsistent
evaluation/version/mapping evidence is unprovable, not permission to guess. Do not write
conversion metadata back to that booking. If these facts cannot be established, or mapping is
nonunit, refuse the new confirmation/approval with the existing stale-quote failure path
before new service-request/authorization effects. Apply the same decision to synchronous
confirmation and asynchronous approval. Existing release/expiry must remain possible;
no recovery command or automatic compensation is added.

Already authorized/confirmed historical records retain their persisted authorization item
factor and actual reservation units. Do not retroactively change that factor to the plan's
mapping or reinterpret existing consume/release movements. New lifecycle code must use
the historical stored factor (including the legacy default 1) and must not require new
quote metadata merely to display, release or finish those records. Do not claim such
preservation repairs historically undercharged nonunit bookings. Any recovery/audit of
those bookings is separate work requiring its own scope and evidence.

Test compatibility explicitly. If safely preserving existing authorized lifecycle behavior
requires more than this bounded dual-version handling, report the concrete issue before
expanding the implementation or adding migration/recovery machinery.

## 4. Tests-first reproduction and acceptance

Use one synthetic enrolled person, one known NIGHT account and one priced room service so
selection ambiguity does not mask conversion. Build mappings while the version is DRAFT
and publish normally. Run production SQL/HTTP tests only in isolated CI. The first three
regressions must fail for their intended reason against old production behavior:

| Fixture | Correct result | Old-source prediction |
| --- | --- | --- |
| Balance 3, factor 2, stay 2 nights | Cover 1 night, member pays 1; reserve 2 units | Covers both nights, reserves 2 |
| Balance 4, factor 2, stay 2 nights | Cover 2; reserve 4 units | Covers 2, reserves 2 |
| Balance 1.5, factor 0.5, stay 3 nights | Cover 3; reserve 1.5 units | Covers 1, reserves 1 |

Require focused pure boundary tests for exact floor arithmetic, insufficient-for-one-night,
fractional residual balance and six-decimal factors, including 0.333333. Distinguish raw
account units in EntitlementView from covered service nights; do not change the existing
public field's unit silently. Include factor-1 regression and independent family-shared
account regression using the accepted exact account scope.

For each successful representative factor above/below 1, prove search and hold coverage,
exact per-night payer/member totals, private conversion snapshot, correct account and
reserved units, one reservation/RESERVE, unchanged inventory behavior, and request
submission using the converted held allowance when available balance is now zero.
Add a regression that creates A's hold and then adds active B in the same program with
an identical entitlement code and a different balance before confirmation. Submission
must remain pinned to A's original plan version/account, apply only A's actual held units,
and neither become ambiguous nor borrow B's balance. Cover the asynchronous approval
path where it invokes the same evidence. Confirmation and approval must adopt the same
reservation, set the item factor, retain
service-night approved quantity and perform no second RESERVE.

Exercise full checkout, partial checkout plus unused release, free cancellation, penalized
cancellation and no-show consumption. Assert service counters separately from ledger
quantities; exact available/reserved/consumed conservation must hold at every boundary.
Include repeated calls/idempotency and cumulative decimal consumption where applicable.
Old snapshots with proven factor 1 must retain their supported confirmation; old nonunit
or unprovable snapshots must refuse without new authorization effects and still expire or
release. Existing authorized legacy factor-1 items must keep their actual historical
consumption semantics. Malformed new conversion fields must fail closed.

Run the three reproductions and at least one adoption-factor regression against restored
old source in an isolated temporary checkout/negative control. Compile/setup failures are
not evidence. Restore final source/generated files and require green required CI, current
accommodation payer/enrollment/contract regressions, and relevant authorization mapping,
adoption and quantity tests. Record exact commands, test names, no-skip results and code
head. Local checks may cover pure tests, compilation, regeneration, format, scoped vet/lint
and unchanged public contract; blank KAPSORA_TEST_ADMIN_DATABASE_URL for local Go tests.

## 5. Exclusions and gates

No local database tests/writes, schema migration, grants, settings, server lifecycle,
live booking, historical rewrite, compensation, unrelated financial journeys or MONEY
conversion design. No new public field or UI flow is planned. Allocation/price formulas,
contract ranking and owner/calendar gates remain separate. An unexpected schema/public
interface requirement is a concrete review point, not permission to silently broaden scope.

Enrollment/account acceptance is complete. Four focused SQL/HTTP reproduction tests
and an early isolated CI diagnostic are prepared; production conversion code is unchanged.
The tests are deliberately opt-in (`KAPSORA_TEST_NIGHT_CONVERSION_REPRODUCTION=1`) until
implementation: the diagnostic requires four compiled, unskipped failures at the stated
coverage/reserved-unit and authorization-factor assertions. Their default skips are not functional acceptance.
Fixture mappings are set while DRAFT, then published; the superseded same-code account
is frozen so it cannot mask the fractional-balance search. Isolated reproduction results
must be recorded before implementation. Runtime verification remains operator
controlled and separate from CI; no affected live row or rollout deadline is asserted.

The three-case diagnostic on `e261d60` completed successfully in
[CI run 37986149082](https://github.com/celikbros/kapsora/actions/runs/37986149082),
meaning each deliberately failing regression reached its intended defect assertion.
This is old-behavior reproduction, not passing conversion functionality or final CI
acceptance. A fourth dedicated adoption regression is added next: it carries the old
two-unit hold through real confirmation/approval, then requires factor 2 on the item,
two approved service nights, the original reservation and one RESERVE. It does not
assert converted hold units early, which would hide the independent adoption defect.



