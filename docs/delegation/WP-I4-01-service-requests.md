# WP-I4-01 · Service requests: versions, items, explicit transitions and the eligibility gate

| Field                      | Value                                                                                                                                                                                                                                                                                                                                         |
| -------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M4 (plan increment I4)                                                                                                                                                                                                                                                                                                                        |
| Size                       | L                                                                                                                                                                                                                                                                                                                                             |
| Depends on                 | M2 (persons, plans, eligibility), M3 (catalog, providers, contracts, rules)                                                                                                                                                                                                                                                                   |
| Runs in parallel with      | WP-I4-04                                                                                                                                                                                                                                                                                                                                      |
| Migration numbers assigned | `000025_service_request_lifecycle.up.sql`                                                                                                                                                                                                                                                                                                     |
| OpenAPI operations owned   | `listServiceRequests`, `createServiceRequest`, `getServiceRequest`, `patchServiceRequestDraft`, `putServiceRequestItems`, `submitServiceRequest`, `returnServiceRequest`, `rejectServiceRequest`, `approveServiceRequest`, `partiallyApproveServiceRequest`, `cancelServiceRequest`, `listServiceRequestVersions`, `getServiceRequestVersion` |
| Read first                 | v1.2 9.11, 11.8, 11.9, 16.7; migration `000006_catalog_and_service_request.up.sql` (the three tables already exist); WP-I2-04 eligibility; ADR-015                                                                                                                                                                                            |

## 1. Goal

A request is how anything gets asked for: a member wants a service, a provider wants it
approved, an operator records one on the phone. Everything after this package — the
authorization that reserves entitlement, the work item somebody picks up, the claim that
gets paid — hangs off a request and the version of it that was actually submitted.

`service.service_request`, `service_request_version` and `service_request_item` exist
since migration 000006 and are unused. This package gives them their lifecycle.

## 2. Scope

### 2.1 The rule that shapes everything: no status field

**There is no endpoint that writes `status`.** Every move is its own command with its own
precondition, permission and reason code (v1.2 11.8). A `PATCH` carrying `status` answers
422 with field code `IMMUTABLE`, and there is a test for it.

The transitions, and nothing else:

| From                             | Command            | To                                                     | Who                      |
| -------------------------------- | ------------------ | ------------------------------------------------------ | ------------------------ |
| DRAFT                            | `submit`           | SUBMITTED, then the gate below                         | `service_request.submit` |
| SUBMITTED                        | (automatic)        | ELIGIBILITY_FAILED / PENDING_DOCUMENT / PENDING_REVIEW | —                        |
| PENDING_REVIEW, PENDING_DOCUMENT | `return`           | DRAFT                                                  | `service_request.review` |
| PENDING_REVIEW                   | `reject`           | REJECTED                                               | `service_request.review` |
| PENDING_REVIEW                   | `approve`          | APPROVED                                               | `service_request.review` |
| PENDING_REVIEW                   | `partiallyApprove` | PARTIALLY_APPROVED                                     | `service_request.review` |
| DRAFT, SUBMITTED, PENDING_*      | `cancel`           | CANCELLED                                              | `service_request.cancel` |

**Return and reject are different things and must not be collapsed** (v1.2 11.8). A
returned request goes back to DRAFT, keeps its number, and the requester fixes it and
submits again — the reason is shown to them. A rejected request is finished; a new
attempt is a new request that names the old one in `supersedes_request_id`. Getting this
wrong makes a correctable mistake look like a refusal, which is the difference between a
member being served and a member giving up.

### 2.2 Versions

A DRAFT version is editable; submitting freezes it into `snapshot_json` and increments
`current_version_no`. A returned request opens the **next** version rather than reopening
the frozen one, so what was submitted the first time is still readable. Only one DRAFT
version may exist per request (the existing unique index enforces it).

### 2.3 The submit gate

`submit` runs, in one transaction:

1. Validate the version: at least one item, a service date, a provider when the request
   type needs one.
2. **Eligibility** — call the pure resolver of WP-I2-04 (never the service: it opens
   accounts and that posts a ledger movement). `INELIGIBLE` → ELIGIBILITY_FAILED with the
   explanations stored on the version. `REVIEW_REQUIRED` → PENDING_REVIEW.
3. **Rules** — evaluate the tenant's published DOCUMENT and PREAUTH rule sets for the
   service date (WP-I3-04). `REQUIRE_DOCUMENT` → PENDING_DOCUMENT with the required
   document types recorded; `REQUIRE_PREAUTH` or any other `REQUIRE_*` → PENDING_REVIEW.
4. Nothing objected → PENDING_REVIEW anyway when the program requires review, else
   APPROVED. Whether a program auto-approves is a plan setting, not a hard-coded rule.

Every outcome writes a `workflow.status_event` row with the from/to status, the actor and
the reason. That table exists since migration 000007 and is append-only.

### 2.4 Schema (migration 000025)

Additions rather than a new schema:

- `service.service_request`: add `eligibility_evaluation_id` and `rule_evaluation_id`
  (nullable composite FKs), `required_document_types text[]`, `return_reason_code`,
  `reject_reason_code`, `review_comment`.
- `service.cancellation`: id, tenant_id, aggregate type and id, `policy_snapshot jsonb`,
  `fee_amount numeric(20,6)`, `released_amount numeric(20,6)`, reason code and text,
  actor, created_at. One active cancellation per aggregate version (partial unique).
- `service.appeal`: id, tenant_id, request_id, `decision_reference`, appellant actor,
  reason, status (`OPEN`,`UPHELD`,`OVERTURNED`,`WITHDRAWN`), SLA due date. Unique one
  open appeal per request.

RLS, touch triggers and composite `(tenant_id, id)` on both new tables.

### 2.5 Reading

`GET /api/v1/service-requests` pages with the standard cursor and filters by status,
person, provider, program, service date range and channel. Provider-scoped actors see
only their own requests, filtered in the repository. Members (once the portal exists in
WP-I4-06) see only their own; that filter is the same one, keyed on person.

## 3. Tests required

- Transition table tests: every legal move accepted, every illegal one refused with
  `REQUEST_TRANSITION_INVALID`, and a `PATCH` carrying `status` refused with `IMMUTABLE`.
- **Return is not reject**: a returned request is editable again, keeps its reference, and
  its next submit produces version 2 while version 1 stays readable exactly as submitted.
- Submit gate: ineligible → ELIGIBILITY_FAILED with the explanation codes stored; a
  DOCUMENT rule → PENDING_DOCUMENT naming the document types; a PREAUTH rule →
  PENDING_REVIEW.
- **The ledger is untouched by anything in this package.** Reserving entitlement is
  WP-I4-02's job; assert ledger row counts and balances are unchanged across a submit.
- A submitted version is immutable: every write to it answers 409.
- Provider scope: another provider's request answers 404, not 403.
- Every transition wrote a `status_event` with its reason, and those rows reject UPDATE.

## 4. Acceptance criteria

- [ ] No endpoint anywhere writes `status`; the transitions are the only way through.
- [ ] A returned request can be corrected and resubmitted; a rejected one cannot.
- [ ] What was submitted is readable unchanged after any number of later versions.
- [ ] The submit gate records which eligibility evaluation and which rule versions decided
      the outcome.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; schema version 25.
