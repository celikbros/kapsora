# WP-PC06 · Return unused fractional authorization at checkout

| Field | Value |
| --- | --- |
| Status | ISOLATED ACCEPTANCE COMPLETE; formal owner/runtime acceptance pending |
| Planned | 2026-10-10, gpt-6-astra |
| Baseline | NIGHT conversion isolated acceptance on 89521c4; all six CI jobs passed |
| Scope | Exact unused service quantity at lodging checkout; no new approval policy |
| Migration / permissions / public API | None planned |

## 1. Existing requirement and source concern

Read the delegate handbook and current HANDOVER/ROADMAP, then the enrollment coherence
and NIGHT conversion packages. Their acceptance remains valid within recorded scopes;
this package addresses the separate fractional-service-approval limitation.

WP-I6-03 section 2.3 requires checkout to consume used nights and release unused
entitlement across the booking's holds. Its acceptance says consumption/release happen
once. This establishes a terminal accounting requirement without deciding whether review
should permit fractional NIGHT service quantities.

Generic review currently permits positive fractional approved quantity. In
internal/accommodation/application/stay.go settleStay, promised sums exact approved
service quantity, but authorized = wholeNights(promised) then determines both integer
fulfilment and the release ceiling authorized-used. The fractional remainder is omitted.
internal/authorization/application/release.go already accepts an exact service-quantity
ceiling and converts it with persisted EntitlementUnitFactor before ledger release.

The reproduction scenario is: a hold for 2 service nights at factor 2 reserves
4 units, partial approval of 0.5 adopts it and returns 3 units under the accepted conversion
patch, leaving 1 unit reserved. After a one-calendar-night checkout, current whole-night
fulfilment is 0; release is skipped because floor(0.5)-0 is zero. The terminal booking can
therefore retain 1 unused unit until expiry. This is a source prediction, not live proof.

## 2. Bounded correction after reproduction

Keep current whole-night fulfilment, approved service quantity, actual-night count,
overstaying flag, frozen quote/mapping factor and partial-approval behavior unchanged.
Compute the checkout release ceiling from exact promised service quantity minus actual
service quantity fulfilled by this checkout (an integer converted to exact Quantity).
When positive, pass that exact decimal to the existing ReleaseUnused boundary. Do not
floor the release ceiling, multiply twice, spend a fraction as a full night, round approval,
reject a fractional decision or change generic health/request rules.

Preserve transactional checkout, booking/authorization locks, inventory release, existing
reason/idempotency keys, and the original reservation. No second RESERVE, authorization
creation or background compensation. The authorization layer applies the stored factor
and protects already consumed units. If the synthetic test exposes a deeper ledger or
multiple-item problem, report the precise reproduction before generalizing the resolver.
Do not hide a remainder error or bypass account/reservation ownership checks.

This applies prospectively to an ordinary checkout command, including a previously
confirmed booking whose persisted authorization factor is 1. It does not rewrite historical
snapshots, factors or ledger entries; completed bookings are not scanned or reopened.
Repeated completed checkout remains an ordinary no-op/replay according to current contract.
Use the original stored factor; never infer a replacement from a newer plan version.

Primary implementation scope: accommodation/application/stay.go and focused pure/HTTP
tests. Gateway changes should be unnecessary because BookingReleaseInput.Nights is already
an exact-decimal string. If needed, clarify private comments rather than add a public field.

## 3. Reproduction-first acceptance

Use synthetic published plan/contract fixtures, frozen clock, real review/authorization
adoption and real checkout in isolated CI. Do not mutate immutable published mappings.

| Case | Required evidence |
| --- | --- |
| Approved 0.5, factor 2, actual 1 | Approval leaves 1 reserved; checkout consumes 0, releases 1, ends reserved 0; actual/authorized/overstay behavior unchanged |
| Approved 1.5, factor 2, actual 1 | Approval leaves 3 reserved; checkout consumes 2 and releases 1, reserved 0 |
| Approved 1.5, factor 0.5, actual 1 | Consume 0.5 units, release 0.25 units, reserved 0, exact decimal conservation |
| Factor 1 compatibility | Fractional approved service remainder releases using stored 1, without rewriting historical metadata |
| Integer regression | Existing whole-night checkout, early checkout and overstay remain unchanged |
| Retry and rollback | Exactly one fulfilment/CONSUME and one terminal RELEASE where applicable; retry does not duplicate; failed checkout rolls back inventory/status/ledger effects |

Record original reservation ID/quantity, prior adoption surplus release separately from
checkout release, authorization item factor/approved/consumed service quantities and
account available/reserved/consumed conservation. No unrelated enrollment/account changes.
Run the new targeted regression against old settleStay in an isolated source negative
control; require its specific retained-reserved-unit assertion, not setup/compile failure.
Use one final full CI run for this bounded correction: corrected normal SQL cases pass, and a restored-source negative-control step proves the old residual failure in that same run; no separate old-only full CI is required. Restore source and pass new cases plus existing conversion, accommodation lifecycle and
authorization quantity/release tests and required CI. Local Go tests blank
KAPSORA_TEST_ADMIN_DATABASE_URL; local PostgreSQL tests remain disabled.

## 4. Exclusions and completion

Fractional cancellation/no-show penalty policy, fractional approval admission rules,
MONEY entitlement policy, generic multi-item allocation, historical recovery, live writes,
operator restart, local migration 58 and new permissions are excluded. A retained penalty
failure needs its own source/reproduction assessment; this checkout correction does not
certify it. Isolated acceptance is complete below; report formal owner and live/runtime
acceptance separately.

### Isolated acceptance evidence

[CI run 37993738359](https://github.com/celikbros/kapsora/actions/runs/37993738359)
completed all six jobs successfully against source
`03bfa6c00724c4f99542f396ca356e0b917e0960`. The fractional checkout target completed in
6.322 seconds with no skips: four exact-release subcases passed (0.5 service at factor 2,
1.5 at factor 2, 1.5 at factor 0.5, and stored factor 1), as did legacy v2 factor-one
compatibility and status-failure rollback followed by retry. The restored old-release-
ceiling negative control compiled and failed each of the four expected retained-reserve
assertions, demonstrating the prior defect rather than a setup or compilation failure.

The same run also passed the full accommodation suite (94 passed, one deliberate gated
handoff-race skip; 140.332 seconds), eligibility (20), authorization (31), schema (191),
and directory (70). Web checks passed 756 tests in 92 files and 25 mock smoke cases; 106 opt-in browser
cases were skipped. NIGHT fractional mapping acceptance remains the prior 89521c4 result.
No local SQL tests, database writes, server lifecycle, grants or migration changes were
performed. Isolated SQL evidence is separate from live acceptance; generic approval and
penalty policy remain unchanged.

The targeted command was `go test -count=1 -v -timeout 10m ./internal/accommodation/transport/http -run '^TestFractionalCheckout'`.
Its three functions are `TestFractionalCheckoutReleasesExactUnusedAuthorization`,
`TestFractionalCheckoutLegacyV2FactorOneUsesStoredAuthorization` and
`TestFractionalCheckoutStatusFailureRollsBackAndRetrySettles`.

## 5. Sustained work queue and decision gates

1. Checkout correction and its isolated old-source negative control are complete as
   recorded above. Existing whole-night conversion acceptance is not reopened; retain its
   regressions.
2. The authorization/status handoff diagnostic passed its deterministic fault-injection
   investigation; see [WP-PC06 booking authorization handoff](WP-PC06-booking-authorization-handoff.md)
   for the evidence. Its nine normal functions and original-source negative control
   passed in final all-six-job CI 37995074957 on bda7984. Checkout's three functions
   passed again there in 6.468 seconds, including successful-retry outbox uniqueness.
3. Fractional cancellation/no-show service ceilings are separately reproduced in final
   CI 37995074957: 0.5 approval versus one-night cancellation and 1.5 versus two-night
   no-show refuse with transaction rollback. See the concrete
   [penalty decision](PC06-fractional-penalty-decision.md). Exact cap/reporting and unchanged
   monetary terms await the requested owner decision; no penalty policy was changed.
4. Continue other demonstrably ungated defects with the same evidence-first sequence.
   Do not treat a completed feature as all-product completion, and do not fill waiting time
   by inventing settings authority, integrations or broad refactors.

External/decision gates remain:

- MGT-04 needs explicit approval of proposed platform.tenant_settings.read/manage grants
  to system TENANT_ADMIN, tenant-correlated read/manage enforcement, direct stepped-up
  changes rather than maker-checker, admitted holdMinutes/quoteTtlMinutes allowlist and
  acknowledgement that quote TTL changes affect pending confirmations. No read-only
  settings API is permission-free. Contract hold precedence prerequisite is already fixed;
  permission/impact acceptance and migration allocation still remain. The integrator has requested the concrete permission/impact decision asynchronously; a pending question is not approval.
- Local migration 58 requires its retained explicit approval; current local schema remains
  57. MGT-02B anonymous reads and MGT-03 live reads require operator reload (and relevant
  schema) rather than further source work. Do not restart the user's stack autonomously.
- PC-05/PC-06 formal owner acceptance and retained October 15/30 calendar checks are not
  closed by synthetic CI. Keep dates and opt-in tests as recorded; no fake clock proof may
  substitute for the required real scheduled observation.
- External SMTP/integrations, production deployment, fiscal/bank-transfer acceptance,
  other-tenant legacy recovery and draft PR merge remain their separate authorized gates.

These gates do not prevent other bounded investigations above. They do
prevent declaring all remaining product work complete without the required decisions or
observations. Record exact completed scopes and continue useful independent work.
