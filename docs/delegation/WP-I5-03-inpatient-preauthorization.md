# WP-I5-03 · Inpatient stay: preauthorization, extended stay, segments, discharge

| Field                      | Value                                                                                                                                                                                                                                                                       |
| -------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M5 (plan increment I5)                                                                                                                                                                                                                                                      |
| Size                       | L                                                                                                                                                                                                                                                                           |
| Depends on                 | WP-I5-01 (case), WP-I4-01 (PREAUTHORIZATION requests), WP-I4-02 (authorization reserving entitlement, expiry, extension), WP-I4-04 (documents)                                                                                                                             |
| Runs in parallel with      | WP-I5-02                                                                                                                                                                                                                                                                    |
| Migration numbers assigned | `000033_inpatient_stay.up.sql`                                                                                                                                                                                                                                              |
| OpenAPI operations owned   | `listInpatientStays`, `createInpatientStay`, `getInpatientStay`, `extendInpatientStay`, `putStaySegments`, `dischargeInpatientStay`, `cancelInpatientStay`, `getInpatientStayReconciliation`                                                                                  |
| Read first                 | v1.2 10.4, 12.2, 16.7 (`inpatient_stay`, `stay_segment`); WP-I4-02 as delivered (`internal/authorization` — reserve, release, consume, `extendAuthorization`); WP-I4-01's submit gate; tenant configuration in `platform.tenant` (backdate/future-date days, add if missing) |

## 1. Goal

Admitting somebody costs the plan something before anybody knows how much. A
preauthorization reserves it; an extension reserves more against the same admission;
discharge and the real claim settle up. The rules the baseline states plainly: **an
extension cannot be opened while an earlier one is undecided, there is one open stay per
case and provider, and what was reserved but not used is released — never quietly kept.**

## 2. Scope

### 2.1 Schema (migration 000033)

`health.inpatient_stay`: id, tenant_id, case_id, `provider_organization_id`,
`location_id` NULL, `attending_practitioner_id` NULL, `admission_at` timestamptz,
`estimated_days` int CHECK > 0, `expected_discharge_at` (derived at write),
`discharge_at` NULL, `status` (`REQUESTED`,`AUTHORIZED`,`ADMITTED`,`DISCHARGED`,
`CANCELLED`,`REJECTED`), `service_request_id` (the PREAUTHORIZATION request; composite FK),
`authorization_id` NULL (WP-I4-02's; composite FK), `admission_diagnosis_id` NULL (into
`health.diagnosis`), row_version. **Partial unique `(tenant_id, case_id,
provider_organization_id) WHERE status IN ('REQUESTED','AUTHORIZED','ADMITTED')`**: one open
stay per case and provider (v1.2 10.4 step 3).

`health.stay_extension`: id, tenant_id, stay_id, `sequence_no`, `additional_days` CHECK > 0,
`reason_code`, `reason_text` NULL, `service_request_id` (its own PREAUTHORIZATION request,
so the review is the request's review), `authorization_id` NULL, `status`
(`REQUESTED`,`APPROVED`,`REJECTED`,`CANCELLED`), row_version. **Partial unique
`(tenant_id, stay_id) WHERE status = 'REQUESTED'`**: a new extension cannot be opened while
one is undecided (v1.2 10.4 step 6), enforced by the database, not only the service.

`health.stay_segment`: id, tenant_id, stay_id, `segment_type` (`WARD`,`ICU`,`SURGERY`,
`OBSERVATION`,`COMPANION`), `starts_at`, `ends_at` NULL, `room_code` NULL, `bed_code` NULL.
**Exclusion constraint** over `tstzrange(starts_at, ends_at, '[)')` per `(tenant_id,
stay_id)` unless `segment_type = 'COMPANION'` (a companion overlaps the patient's own
segment by definition — put COMPANION rows outside the constraint with a partial
exclusion, and say so in a comment).

RLS, touch triggers, composite keys; `grant_app_schema_usage('health')` again (idempotent).

### 2.2 Preauthorization is a request, then an authorization

`createInpatientStay` runs in one transaction: validates the backdate/future-date window
from tenant configuration (add `platform.tenant.settings jsonb` keys
`health.inpatient.backdate_days` / `future_days` with defaults 3 / 30 if no such thing
exists; refuse outside with 422 `ADMISSION_DATE_OUT_OF_WINDOW`), refuses a duplicate open
stay (409 `INPATIENT_STAY_ALREADY_OPEN`), checks the required documents through the
request's gate (WP-I4-01 decides `PENDING_DOCUMENT`), and creates the PREAUTHORIZATION
service request with one line per estimated day of the admission service. The stay is
`REQUESTED`. When that request is approved (WP-I4-01's approve/partially-approve), an
`authorization` is created through WP-I4-02 for the approved days and the stay becomes
`AUTHORIZED` — this package subscribes to the request's outbox event
`service_request.decided` rather than calling into WP-I4-01's command, so a decision made
on the request page moves the stay without the reviewer knowing a stay exists.

### 2.3 Extension

`extendInpatientStay` requires the stay `AUTHORIZED` or `ADMITTED` and no `REQUESTED`
extension; it creates the extension's own PREAUTHORIZATION request (`supersedesRequestId`
empty; `linkedStayId` in the request's metadata is not a thing — the extension row is the
link) for `additional_days`. On approval, `extendAuthorization` on the existing
authorization moves `valid_to` forward and a further reserve is taken for the added days;
on rejection the extension is `REJECTED` and the stay is unchanged.

### 2.4 Discharge and reconciliation

`dischargeInpatientStay` sets `discharge_at`, ends every open segment, and computes the
reconciliation: authorized days vs actual days (`ceil((discharge − admission) / 1 day)`,
never less than 1). **Days reserved and not used are released** on the authorization
(WP-I4-02's release), and a stay that ran over its authorization is flagged
`over_authorization = true` on the reconciliation for the claim to raise as an exception
(WP-I5-04). `getInpatientStayReconciliation` answers both figures and the release that
happened, as exact decimal strings.

### 2.5 Permissions

`health.case.manage` covers create/extend/segments/discharge for provider staff;
approvals are the request's (`service_request.review`) — a medical reviewer decides an
admission on the request page. No new permission.

### 2.6 The mock

`inpatient-handlers.ts` with the window check, the one-open-stay rule, the one-pending-
extension rule, discharge with reconciliation, and a world with an admitted stay carrying
two segments and one decided extension.

## 3. Tests required

- **One open stay per case and provider** — the second create is 409, and 20 concurrent
  creates against one case leave exactly one stay (constraint, not luck).
- **Extension gate** — a second extension while one is REQUESTED is 409; after the first is
  decided the second is accepted.
- **Window** — admission 4 days back with `backdate_days = 3` refused; 3 days back accepted.
- **Reconciliation releases** — authorize 5 days, discharge after 3: the authorization's
  reservation shows 2 days released, ledger conservation holds (WP-I4-02's assertion), and
  running discharge twice releases nothing twice.
- **Over-authorization** — discharge after 7 flags `over_authorization` and releases nothing.
- Segments: overlapping WARD/ICU refused by the exclusion; COMPANION may overlap.
- Decision through the outbox: approving the PREAUTHORIZATION request moves the stay to
  AUTHORIZED with an authorization attached; rejecting sets REJECTED with none.

## 4. Acceptance criteria

- [ ] An extension is always tied to an open, decided admission; two undecided extensions
      cannot exist.
- [ ] What was reserved and not used is released at discharge, once.
- [ ] A reviewer decides an admission where they decide every request, and the stay follows.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; Turkish for every problem
      code; schema version 33.
