-- Claim, version, line, line decision and adjustment queries (WP-I5-04, v1.2 9.14, 10.3
-- steps 5-7, 12.5, 16.8).
--
-- Three properties shape this file.
--
-- **No statement here decides what a caller may see.** Every read returns the whole row,
-- including `description`, `diagnosis_id`, `medical_report_id` and `review_comment_medical`,
-- and the application service drops what the caller may not have before the record reaches
-- the wire. The projection is one decision made in one place, and a second copy of it in SQL
-- would be a second place for it to disagree.
--
-- **The provider boundary is applied here rather than above.** `scope_ids` is a nullable
-- uuid[] of tenant organization ids: NULL means the caller sees every claim of the tenant,
-- and a non-null array binds it to the claims those organizations raised. It is applied in
-- every read including the single-row ones, so a claim outside it is answered 404 rather
-- than 403 — that such a claim exists at all is somebody else's business.
--
-- **Every precondition is a predicate.** A status a command may not run from, or a
-- `row_version` somebody else has moved, matches no row and updates nothing; the caller
-- reads the affected count and answers 409 or 412 without ever having half-written anything.

-- ---------------------------------------------------------------------------
-- claim.claim
-- ---------------------------------------------------------------------------

-- name: CreateClaim :one
INSERT INTO claim.claim (
    tenant_id, reference, person_id, program_id, enrollment_id, provider_organization_id,
    domain_code, source_type, source_id, case_id, fulfilment_id, authorization_id,
    service_date_from, service_date_to, channel, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('reference'), sqlc.arg('person_id'),
        sqlc.arg('program_id'), sqlc.arg('enrollment_id'),
        sqlc.arg('provider_organization_id'), sqlc.arg('domain_code'),
        sqlc.narg('source_type'), sqlc.narg('source_id'),
        sqlc.narg('case_id'), sqlc.narg('fulfilment_id'), sqlc.narg('authorization_id'),
        sqlc.arg('service_date_from'), sqlc.arg('service_date_to'), sqlc.arg('channel'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, reference, person_id, program_id, enrollment_id, provider_organization_id,
          domain_code, source_type, source_id, case_id, fulfilment_id, authorization_id,
          current_version_no, status,
          service_date_from, service_date_to, channel, reject_reason_code, return_reason_code,
          review_comment_medical, review_comment_financial, closed_at, created_at, row_version;

-- name: GetClaim :one
SELECT c.id, c.reference, c.person_id, c.program_id, c.enrollment_id,
       c.provider_organization_id, c.domain_code, c.source_type, c.source_id, c.case_id,
       c.fulfilment_id,
       c.authorization_id, c.current_version_no, c.status, c.service_date_from,
       c.service_date_to, c.channel, c.reject_reason_code, c.return_reason_code,
       c.review_comment_medical, c.review_comment_financial, c.closed_at, c.created_at,
       c.row_version
  FROM claim.claim c
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND c.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR c.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: LockClaim :one
-- The read every command makes before it writes, so two commands on one claim serialise.
SELECT c.id, c.reference, c.person_id, c.program_id, c.enrollment_id,
       c.provider_organization_id, c.domain_code, c.source_type, c.source_id, c.case_id,
       c.fulfilment_id,
       c.authorization_id, c.current_version_no, c.status, c.service_date_from,
       c.service_date_to, c.channel, c.reject_reason_code, c.return_reason_code,
       c.review_comment_medical, c.review_comment_financial, c.closed_at, c.created_at,
       c.row_version
  FROM claim.claim c
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND c.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR c.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   FOR UPDATE;

-- name: ListClaims :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows to learn
-- whether a next page exists.
SELECT c.id, c.reference, c.person_id, c.program_id, c.enrollment_id,
       c.provider_organization_id, c.domain_code, c.source_type, c.source_id, c.case_id,
       c.fulfilment_id,
       c.authorization_id, c.current_version_no, c.status, c.service_date_from,
       c.service_date_to, c.channel, c.reject_reason_code, c.return_reason_code,
       c.review_comment_medical, c.review_comment_financial, c.closed_at, c.created_at,
       c.row_version
  FROM claim.claim c
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR c.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('person_id')::uuid IS NULL OR c.person_id = sqlc.narg('person_id')::uuid)
   AND (sqlc.narg('case_id')::uuid IS NULL OR c.case_id = sqlc.narg('case_id')::uuid)
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR c.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
   AND (sqlc.narg('service_date_from')::date IS NULL
        OR c.service_date_to >= sqlc.narg('service_date_from')::date)
   AND (sqlc.narg('service_date_to')::date IS NULL
        OR c.service_date_from <= sqlc.narg('service_date_to')::date)
   AND (sqlc.narg('after_at')::timestamptz IS NULL
        OR (c.created_at, c.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY c.created_at DESC, c.id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdateClaimDraft :execrows
-- The header of a claim whose current version is still a draft. The status predicate is the
-- freeze: a claim that has been submitted matches no row here, and the service answers 409
-- CLAIM_VERSION_FROZEN rather than quietly editing a decided statement.
UPDATE claim.claim
   SET service_date_from = sqlc.arg('service_date_from'),
       service_date_to   = sqlc.arg('service_date_to'),
       channel           = sqlc.arg('channel'),
       case_id           = sqlc.narg('case_id'),
       -- The source pair follows the case rather than being patched beside it. Naming a
       -- case names the source; clearing it clears a source that was the case's and leaves
       -- a BOOKING or REIMBURSEMENT source exactly where it was, because a header patch on
       -- a lodging claim has nothing to say about where that claim came from.
       source_type       = CASE
                               WHEN sqlc.narg('case_id')::uuid IS NOT NULL THEN 'HEALTH_CASE'
                               WHEN source_type = 'HEALTH_CASE' THEN NULL
                               ELSE source_type
                           END,
       source_id         = CASE
                               WHEN sqlc.narg('case_id')::uuid IS NOT NULL THEN sqlc.narg('case_id')::uuid
                               WHEN source_type = 'HEALTH_CASE' THEN NULL
                               ELSE source_id
                           END,
       fulfilment_id     = sqlc.narg('fulfilment_id'),
       authorization_id  = sqlc.narg('authorization_id'),
       updated_by        = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('DRAFT', 'RETURNED')
   AND row_version = sqlc.arg('expected_row_version');

-- name: TouchClaim :execrows
-- Moves the claim's row_version without changing a field of it. It is what a line
-- replacement does: the lines belong to the version, the ETag belongs to the claim, and a
-- caller that replaced the lines has to be given a new token or its next If-Match is stale.
UPDATE claim.claim
   SET updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('expected_row_version');

-- name: SetClaimStatus :execrows
-- One move through the lifecycle. Every column the move owns is written, so clearing a
-- return reason on resubmission is expressible and a stale reason can never survive a
-- transition. `from_statuses` carries the whole precondition.
UPDATE claim.claim
   SET status             = sqlc.arg('status'),
       current_version_no = sqlc.arg('current_version_no'),
       reject_reason_code = sqlc.narg('reject_reason_code'),
       return_reason_code = sqlc.narg('return_reason_code'),
       closed_at          = sqlc.narg('closed_at'),
       updated_by         = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = ANY(sqlc.arg('from_statuses')::text[])
   AND row_version = sqlc.arg('expected_row_version');

-- name: SetClaimReviewComment :execrows
-- The reviewer's comment on the claim as a whole, written to the column its stage owns.
-- Two columns rather than one with a flag: `review_comment_medical` is a doctor's sentence
-- about a patient and is served only in the clinical projection, and a single column would
-- be a single column somebody eventually selects without the flag.
UPDATE claim.claim
   SET review_comment_medical = CASE WHEN sqlc.arg('stage')::text = 'MEDICAL'
                                     THEN sqlc.narg('comment')::text
                                     ELSE review_comment_medical END,
       review_comment_financial = CASE WHEN sqlc.arg('stage')::text = 'FINANCIAL'
                                       THEN sqlc.narg('comment')::text
                                       ELSE review_comment_financial END,
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id');

-- name: FindDuplicateClaim :one
-- The duplicate check of section 2.2 step 3: another claim, of the same person, carrying the
-- same service on a day this claim's service period covers, that nobody has rejected or
-- cancelled. It answers with the other claim's reference, because "we think you have already
-- billed this" is only actionable if the provider is told which one.
--
-- It looks at the *current* version of the other claim, not at every version it has ever
-- had: a line that was corrected away is not a duplicate of anything.
SELECT o.id, o.reference
  FROM claim.claim o
  JOIN claim.claim_version v
    ON v.tenant_id = o.tenant_id
   AND v.claim_id = o.id
   AND v.version_no = o.current_version_no
  JOIN claim.claim_line l
    ON l.tenant_id = v.tenant_id
   AND l.version_id = v.id
 WHERE o.tenant_id = sqlc.arg('tenant_id')
   AND o.id <> sqlc.arg('claim_id')
   AND o.person_id = sqlc.arg('person_id')
   AND l.service_definition_id = sqlc.arg('service_definition_id')
   AND o.status NOT IN ('REJECTED', 'CANCELLED')
   AND o.service_date_from <= sqlc.arg('service_date_to')
   AND o.service_date_to >= sqlc.arg('service_date_from')
 ORDER BY o.created_at, o.id
 LIMIT 1;

-- name: ProviderHasTaxIdentity :one
-- Whether the provider organization carries a tax identity an invoice can be raised
-- against. It answers a boolean and never the number: the value is envelope-encrypted and
-- the hash is a blind index, and neither of them belongs in a readiness response.
SELECT EXISTS (
    SELECT 1
      FROM directory.tenant_organization t
      JOIN directory.organization o ON o.id = t.organization_id
     WHERE t.tenant_id = sqlc.arg('tenant_id')
       AND t.id = sqlc.arg('tenant_organization_id')
       AND o.tax_number_hash IS NOT NULL
) AS has_tax_identity;

-- name: GetClaimCaseSensitivity :one
-- The sensitivity of the episode of care a claim hangs off. It is what decides the
-- projection, and it is read rather than copied onto the claim: WP-I5-01 recomputes it from
-- the diagnoses that are actually stored, and a second copy here would go stale the moment a
-- diagnosis was added.
SELECT hc.sensitivity
  FROM health.health_case hc
 WHERE hc.tenant_id = sqlc.arg('tenant_id')
   AND hc.id = sqlc.arg('case_id');

-- name: ClaimCaseOverAuthorization :one
-- Whether the case behind this claim has an admission that ran over what anybody approved
-- (WP-I5-03's reconciliation). It is the flag section 2.2 step 3 routes to medical review.
SELECT EXISTS (
    SELECT 1
      FROM health.inpatient_stay s
     WHERE s.tenant_id = sqlc.arg('tenant_id')
       AND s.case_id = sqlc.arg('case_id')
       AND s.over_authorization
) AS over_authorization;

-- name: ClaimProviderOrganizationExists :one
-- Whether the organization is a provider of this tenant. A claim raised against a sponsor or
-- a payer is a claim nobody can pay.
SELECT EXISTS (
    SELECT 1 FROM directory.tenant_organization t
     WHERE t.tenant_id = sqlc.arg('tenant_id')
       AND t.id = sqlc.arg('id')
       AND t.relationship_role = 'PROVIDER'
       AND t.status IN ('PENDING', 'ACTIVE')
) AS is_provider;

-- name: GetClaimEnrollment :one
-- The enrollment a claim is raised under, with the person and program it actually belongs
-- to, so a claim naming somebody else's plan is refused before anything is priced.
SELECT e.id, e.plan_id, p.program_id, e.status, m.person_id
  FROM benefit.enrollment e
  JOIN party.sponsor_membership m ON m.tenant_id = e.tenant_id
                                 AND m.id = e.sponsor_membership_id
  JOIN benefit.plan p ON p.tenant_id = e.tenant_id AND p.id = e.plan_id
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.id = sqlc.arg('id');

-- name: ResolveClaimServiceDefinitions :many
-- The catalogue definitions a line set names, with the code a screen shows and the unit the
-- definition is measured in. One read for the whole set rather than one per line.
SELECT d.id, d.code, d.name, d.default_unit_type, d.active
  FROM catalog.service_definition d
 WHERE d.tenant_id = sqlc.arg('tenant_id')
   AND d.id = ANY(sqlc.arg('ids')::uuid[]);

-- ---------------------------------------------------------------------------
-- claim.claim_version
-- ---------------------------------------------------------------------------

-- name: CreateClaimVersion :one
INSERT INTO claim.claim_version (tenant_id, claim_id, version_no, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('claim_id'), sqlc.arg('version_no'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, claim_id, version_no, status, submitted_at, submitted_by, returned_at,
          returned_by, return_reason_code, return_reason_text, snapshot, created_at,
          row_version;

-- name: GetClaimDraftVersion :one
SELECT v.id, v.claim_id, v.version_no, v.status, v.submitted_at, v.submitted_by,
       v.returned_at, v.returned_by, v.return_reason_code, v.return_reason_text, v.snapshot,
       v.created_at, v.row_version
  FROM claim.claim_version v
 WHERE v.tenant_id = sqlc.arg('tenant_id')
   AND v.claim_id = sqlc.arg('claim_id')
   AND v.status = 'DRAFT';

-- name: GetClaimVersionByNo :one
SELECT v.id, v.claim_id, v.version_no, v.status, v.submitted_at, v.submitted_by,
       v.returned_at, v.returned_by, v.return_reason_code, v.return_reason_text, v.snapshot,
       v.created_at, v.row_version
  FROM claim.claim_version v
 WHERE v.tenant_id = sqlc.arg('tenant_id')
   AND v.claim_id = sqlc.arg('claim_id')
   AND v.version_no = sqlc.arg('version_no');

-- name: ListClaimVersions :many
SELECT v.id, v.claim_id, v.version_no, v.status, v.submitted_at, v.submitted_by,
       v.returned_at, v.returned_by, v.return_reason_code, v.return_reason_text, v.snapshot,
       v.created_at, v.row_version
  FROM claim.claim_version v
 WHERE v.tenant_id = sqlc.arg('tenant_id')
   AND v.claim_id = sqlc.arg('claim_id')
 ORDER BY v.version_no DESC;

-- name: FreezeClaimVersion :execrows
-- The freeze. The DRAFT predicate is what makes it happen once: a redelivered submit finds a
-- version that has already been submitted and writes nothing over the snapshot it took.
UPDATE claim.claim_version
   SET status = 'SUBMITTED', snapshot = sqlc.arg('snapshot'),
       submitted_at = sqlc.arg('submitted_at'), submitted_by = sqlc.narg('actor_id'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT';

-- name: SupersedeClaimVersion :execrows
-- A version sent back to be corrected. It keeps its snapshot and every line decision it was
-- given; only the return reason is added, and only SUBMITTED matches.
UPDATE claim.claim_version
   SET status = 'SUPERSEDED', returned_at = sqlc.arg('returned_at'),
       returned_by = sqlc.narg('actor_id'),
       return_reason_code = sqlc.arg('return_reason_code'),
       return_reason_text = sqlc.narg('return_reason_text'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'SUBMITTED';

-- ---------------------------------------------------------------------------
-- claim.claim_line
-- ---------------------------------------------------------------------------

-- name: DeleteClaimLines :exec
-- The line set of a draft version is replaced whole. Nothing hangs off a draft line's id
-- that would be worth keeping: a decision can only exist against a submitted version.
DELETE FROM claim.claim_line
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND version_id = sqlc.arg('version_id');

-- name: CreateClaimLine :one
INSERT INTO claim.claim_line (
    tenant_id, version_id, line_no, service_definition_id, unit_type, quantity, unit_amount,
    line_amount, currency_code, diagnosis_id, medical_report_id, practitioner_id,
    description, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('version_id'), sqlc.arg('line_no'),
        sqlc.arg('service_definition_id'), sqlc.arg('unit_type'),
        sqlc.arg('quantity')::text::numeric, sqlc.narg('unit_amount')::text::numeric,
        sqlc.arg('line_amount')::text::numeric, sqlc.arg('currency_code'),
        sqlc.narg('diagnosis_id'), sqlc.narg('medical_report_id'),
        sqlc.narg('practitioner_id'), sqlc.narg('description'), sqlc.narg('actor_id'),
        sqlc.narg('actor_id'))
RETURNING id, version_id, line_no, service_definition_id, unit_type,
          trim_scale(quantity)::text AS quantity,
          COALESCE(trim_scale(unit_amount)::text, '')::text AS unit_amount,
          trim_scale(line_amount)::text AS line_amount, currency_code, diagnosis_id, medical_report_id,
          practitioner_id, description, created_at, row_version;

-- name: ListClaimLines :many
-- Every money and quantity column leaves as `::text`, which is the exact decimal the numeric
-- holds. A float on the way out would be a float in every figure computed from it.
SELECT l.id, l.version_id, l.line_no, l.service_definition_id, d.code AS service_code,
       l.unit_type, trim_scale(l.quantity)::text AS quantity,
       COALESCE(trim_scale(l.unit_amount)::text, '')::text AS unit_amount,
       trim_scale(l.line_amount)::text AS line_amount, l.currency_code, l.diagnosis_id,
       l.medical_report_id, l.practitioner_id, l.description, l.created_at, l.row_version
  FROM claim.claim_line l
  LEFT JOIN catalog.service_definition d
         ON d.tenant_id = l.tenant_id AND d.id = l.service_definition_id
 WHERE l.tenant_id = sqlc.arg('tenant_id')
   AND l.version_id = sqlc.arg('version_id')
 ORDER BY l.line_no;

-- ---------------------------------------------------------------------------
-- claim.line_decision
-- ---------------------------------------------------------------------------

-- name: CreateClaimLineDecision :one
INSERT INTO claim.line_decision (
    tenant_id, line_id, decided_in_version_no, decision, approved_quantity, approved_amount,
    contract_amount, payer_amount, member_amount, reason_code, reason_text, decided_by,
    decided_at, stage)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('line_id'), sqlc.arg('decided_in_version_no'),
        sqlc.arg('decision'), sqlc.arg('approved_quantity')::text::numeric,
        sqlc.arg('approved_amount')::text::numeric, sqlc.narg('contract_amount')::text::numeric,
        sqlc.arg('payer_amount')::text::numeric, sqlc.arg('member_amount')::text::numeric,
        sqlc.arg('reason_code'), sqlc.narg('reason_text'), sqlc.narg('decided_by'),
        sqlc.arg('decided_at'), sqlc.arg('stage'))
RETURNING id, line_id, decided_in_version_no, decision,
          trim_scale(approved_quantity)::text AS approved_quantity,
          trim_scale(approved_amount)::text AS approved_amount,
          COALESCE(trim_scale(contract_amount)::text, '')::text AS contract_amount,
          trim_scale(payer_amount)::text AS payer_amount, trim_scale(member_amount)::text AS member_amount,
          reason_code, reason_text, decided_by, decided_at, stage;

-- name: ListLatestClaimLineDecisions :many
-- The decision of every line of one version: the latest row per line, which is what
-- "append-only, and the latest is the decision" means when it is read rather than written.
-- DISTINCT ON does it in one pass over ix_line_decision_line.
SELECT DISTINCT ON (d.line_id)
       d.id, d.line_id, d.decided_in_version_no, d.decision,
       trim_scale(d.approved_quantity)::text AS approved_quantity,
       trim_scale(d.approved_amount)::text AS approved_amount,
       COALESCE(trim_scale(d.contract_amount)::text, '')::text AS contract_amount,
       trim_scale(d.payer_amount)::text AS payer_amount, trim_scale(d.member_amount)::text AS member_amount,
       d.reason_code, d.reason_text, d.decided_by, d.decided_at, d.stage
  FROM claim.line_decision d
  JOIN claim.claim_line l ON l.tenant_id = d.tenant_id AND l.id = d.line_id
 WHERE d.tenant_id = sqlc.arg('tenant_id')
   AND l.version_id = sqlc.arg('version_id')
 ORDER BY d.line_id, d.decided_at DESC, d.id DESC;

-- name: CountClaimLineDecisionHistory :one
-- How many decisions a line has ever carried. It is what proves the table is append-only
-- from the outside: a line decided twice answers two.
SELECT count(*) AS decision_count
  FROM claim.line_decision d
 WHERE d.tenant_id = sqlc.arg('tenant_id')
   AND d.line_id = sqlc.arg('line_id');

-- ---------------------------------------------------------------------------
-- claim.adjustment
-- ---------------------------------------------------------------------------

-- name: CreateClaimAdjustment :one
-- Every money column arrives and leaves as exact decimal text. The split is the database's
-- (ck_claim_adjustment_split), so a service that rounded the two halves independently is
-- refused here rather than publishing an invoice nobody can reconcile.
INSERT INTO claim.adjustment (
    tenant_id, claim_id, version_no, claim_line_id, adjustment_type, amount, payer_amount,
    member_amount, currency_code, reason_code, reason_text, source_type, source_id,
    reverses_adjustment_id, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('claim_id'), sqlc.arg('version_no'),
        sqlc.narg('claim_line_id'),
        sqlc.arg('adjustment_type'), sqlc.arg('amount')::text::numeric,
        sqlc.arg('payer_amount')::text::numeric, sqlc.arg('member_amount')::text::numeric,
        sqlc.arg('currency_code'), sqlc.arg('reason_code'), sqlc.narg('reason_text'),
        sqlc.arg('source_type'), sqlc.narg('source_id'),
        sqlc.narg('reverses_adjustment_id'), sqlc.narg('actor_id'))
RETURNING id, claim_id, version_no, claim_line_id, adjustment_type,
          trim_scale(amount)::text AS amount,
          trim_scale(payer_amount)::text AS payer_amount,
          trim_scale(member_amount)::text AS member_amount,
          currency_code, reason_code, reason_text, source_type, source_id,
          reverses_adjustment_id, created_by, created_at;

-- name: ListClaimAdjustments :many
-- Oldest first, which is the order the reversal chain reads in: a reversal always comes after
-- the row it reverses, so a screen rendering this list top to bottom shows the cut and then
-- the row that took it back.
SELECT a.id, a.claim_id, a.version_no, a.claim_line_id, a.adjustment_type,
       trim_scale(a.amount)::text AS amount,
       trim_scale(a.payer_amount)::text AS payer_amount,
       trim_scale(a.member_amount)::text AS member_amount,
       a.currency_code, a.reason_code, a.reason_text, a.source_type, a.source_id,
       a.reverses_adjustment_id, a.created_by, a.created_at
  FROM claim.adjustment a
 WHERE a.tenant_id = sqlc.arg('tenant_id')
   AND a.claim_id = sqlc.arg('claim_id')
 ORDER BY a.created_at, a.id;

-- name: GetClaimAdjustment :one
SELECT a.id, a.claim_id, a.version_no, a.claim_line_id, a.adjustment_type,
       trim_scale(a.amount)::text AS amount,
       trim_scale(a.payer_amount)::text AS payer_amount,
       trim_scale(a.member_amount)::text AS member_amount,
       a.currency_code, a.reason_code, a.reason_text, a.source_type, a.source_id,
       a.reverses_adjustment_id, a.created_by, a.created_at
  FROM claim.adjustment a
 WHERE a.tenant_id = sqlc.arg('tenant_id')
   AND a.id = sqlc.arg('id');

-- name: FindClaimBySource :one
-- The read every source-driven handler makes before it writes: is there already a live claim
-- for this stay. The outbox delivers at least once, so this is the first half of the
-- idempotency and `uq_claim_live_booking` is the second -- the half that holds when two
-- deliveries look at the same moment.
SELECT c.id, c.reference, c.status
  FROM claim.claim c
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND c.source_type = sqlc.arg('source_type')
   AND c.source_id = sqlc.arg('source_id')
   AND c.status NOT IN ('CANCELLED', 'REJECTED')
 ORDER BY c.created_at, c.id
 LIMIT 1;

-- name: GetClaimProviderProfile :one
-- The provider profile behind the claim's provider organization. The pricing ladder is
-- asked about a profile because that is what a contract is signed with; the claim names an
-- organization because that is what a provider is. This is the one place the two meet.
SELECT p.id, p.tenant_organization_id, p.status
  FROM provider.provider_profile p
 WHERE p.tenant_id = sqlc.arg('tenant_id')
   AND p.tenant_organization_id = sqlc.arg('tenant_organization_id');

-- name: ResolveClaimEntitlementCodes :many
-- Which entitlement each of these services draws on, under the plan version in force for this
-- enrollment on the service date (WP-I5-05's `benefit.service_entitlement_mapping`).
--
-- The claim needs it for the same reason the eligibility check does: the pricing ladder caps
-- what the payer carries at the balance behind the line, and a line whose entitlement nobody
-- named has no balance to be capped by. Without this the plan would appear to carry nothing
-- and every member would be told they owe the whole bill.
SELECT m.service_definition_id, d.code AS entitlement_code
  FROM benefit.enrollment e
  JOIN benefit.plan_version v
    ON v.tenant_id = e.tenant_id
   AND v.plan_id = e.plan_id
   AND v.status = 'PUBLISHED'
   AND v.valid_period @> sqlc.arg('service_date')::date
  JOIN benefit.service_entitlement_mapping m
    ON m.tenant_id = v.tenant_id
   AND m.plan_version_id = v.id
  JOIN benefit.entitlement_definition d
    ON d.tenant_id = m.tenant_id
   AND d.id = m.entitlement_definition_id
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.id = sqlc.arg('enrollment_id')
   AND m.service_definition_id = ANY(sqlc.arg('service_definition_ids')::uuid[])
   AND (m.valid_from IS NULL OR m.valid_from <= sqlc.arg('service_date')::date)
   AND (m.valid_to IS NULL OR m.valid_to > sqlc.arg('service_date')::date);

-- ---------------------------------------------------------------------------
-- The booking a lodging claim is raised from (WP-I7-01)
-- ---------------------------------------------------------------------------
--
-- These four reads cross into the accommodation schema, exactly as the reads above cross
-- into health, benefit, directory and catalog. The alternative would be a port back into the
-- accommodation service, and a port would mean the claim's own transaction waiting on
-- another module's transaction to answer a question about three columns.
--
-- **Nothing here re-prices anything.** Every amount comes out of `accommodation.booking_night`
-- and the two fee rows, which are the figures the booking froze when the member agreed to
-- them. A claim that recomputed a night from the contract would be a claim that charged the
-- member a price they were never shown.

-- name: GetBookingForClaim :one
-- The header of the lodging claim: who stayed, under which plan, in whose building, and what
-- the room type is in the catalogue. `actual_nights` is what the check-out counted on the
-- property's own clock and is the number of lines the claim gets; it is NULL until a stay is
-- checked out, which is why the handler reads it rather than the booked nights.
SELECT b.id, b.reference, b.person_id, b.program_id, b.enrollment_id, b.property_id,
       b.room_type_id, b.status, b.check_in, b.check_out, b.nights, b.actual_nights,
       b.over_booking, b.authorization_id, b.checked_out_at, b.cancelled_at,
       b.quote_snapshot,
       p.provider_organization_id, rt.service_definition_id
  FROM accommodation.booking b
  JOIN accommodation.property p ON p.tenant_id = b.tenant_id AND p.id = b.property_id
  JOIN accommodation.room_type rt ON rt.tenant_id = b.tenant_id AND rt.id = b.room_type_id
 WHERE b.tenant_id = sqlc.arg('tenant_id')
   AND b.id = sqlc.arg('id');

-- name: ListBookingNightsForClaim :many
-- One row per night the member agreed to, in stay order, with the split the booking froze.
-- `unit_amount` is what the claim line asks for and `payer_amount` is what the plan already
-- decided it carries; the two are copied and never recomputed.
SELECT n.stay_date, trim_scale(n.unit_amount)::text AS unit_amount,
       trim_scale(n.payer_amount)::text AS payer_amount,
       trim_scale(n.member_amount)::text AS member_amount, n.currency_code
  FROM accommodation.booking_night n
 WHERE n.tenant_id = sqlc.arg('tenant_id')
   AND n.booking_id = sqlc.arg('booking_id')
 ORDER BY n.stay_date;

-- name: GetBookingNoShowForClaim :one
-- The fee a confirmed no-show assessed, with the split the row already carries. The status is
-- returned rather than filtered on, so a handler reaching a report somebody rejected while
-- the event sat in a queue can say so instead of quietly writing nothing.
SELECT s.id, s.status, trim_scale(s.assessed_fee_amount)::text AS assessed_fee_amount,
       trim_scale(s.payer_amount)::text AS payer_amount,
       trim_scale(s.member_amount)::text AS member_amount, s.currency_code
  FROM accommodation.no_show s
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.booking_id = sqlc.arg('booking_id');

-- name: GetBookingCancellationForClaim :one
-- The fee a cancellation charged. `free` is returned because a free cancellation produces no
-- claim at all, and the handler has to be able to tell "nothing to bill" from "no row yet".
SELECT c.id, c.free, trim_scale(c.fee_amount)::text AS fee_amount,
       trim_scale(c.payer_fee)::text AS payer_fee,
       trim_scale(c.member_fee)::text AS member_fee, c.currency_code
  FROM accommodation.cancellation c
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND c.booking_id = sqlc.arg('booking_id');

-- ---------------------------------------------------------------------------
-- The provider's earnings (WP-I7-01 section 2.4)
-- ---------------------------------------------------------------------------

-- name: ListProviderEarningClaims :many
-- One row per decided claim of one provider, with everything the earnings view has to add up:
-- the currency, the moment the last line of the current version was decided, the line totals
-- and the adjustment totals.
--
-- It answers per claim rather than per currency because the sums belong in Go. Every figure
-- below is exact decimal text, `benefitdomain.Quantity` adds them, and a `sum()` grouped in
-- SQL would hand the same arithmetic to a numeric cast on the way out — one more place for a
-- total to be produced, which is one more place for two totals to disagree.
--
-- The period is measured against the decision, not against the service date: what a provider
-- earned in March is what was decided in March, and a stay in February decided in March is
-- money that arrives in March.
WITH latest AS (
    SELECT DISTINCT ON (d.line_id)
           d.line_id, d.approved_amount, d.payer_amount, d.member_amount, d.decided_at,
           l.version_id, l.currency_code
      FROM claim.line_decision d
      JOIN claim.claim_line l ON l.tenant_id = d.tenant_id AND l.id = d.line_id
     WHERE d.tenant_id = sqlc.arg('tenant_id')
     ORDER BY d.line_id, d.decided_at DESC, d.id DESC
), per_claim AS (
    SELECT c.id AS claim_id, c.reference, c.status, c.domain_code,
           min(latest.currency_code) AS currency_code,
           max(latest.decided_at)::timestamptz AS decided_at,
           sum(latest.approved_amount) AS line_total,
           sum(latest.payer_amount) AS line_payer_total,
           sum(latest.member_amount) AS line_member_total
      FROM claim.claim c
      JOIN claim.claim_version v ON v.tenant_id = c.tenant_id AND v.claim_id = c.id
                                AND v.version_no = c.current_version_no
      JOIN latest ON latest.version_id = v.id
     WHERE c.tenant_id = sqlc.arg('tenant_id')
       AND c.provider_organization_id = sqlc.arg('provider_organization_id')
       AND c.status IN ('APPROVED', 'PARTIALLY_APPROVED', 'INVOICED', 'BATCHED', 'SETTLED')
     GROUP BY c.id, c.reference, c.status, c.domain_code
), adjusted AS (
    SELECT a.claim_id,
           sum(a.amount) AS adjustment_total,
           sum(a.payer_amount) AS adjustment_payer_total,
           sum(a.member_amount) AS adjustment_member_total
      FROM claim.adjustment a
     WHERE a.tenant_id = sqlc.arg('tenant_id')
     GROUP BY a.claim_id
)
SELECT p.claim_id, p.reference, p.status, p.domain_code,
       COALESCE(p.currency_code, 'TRY')::text AS currency_code, p.decided_at,
       trim_scale(p.line_total)::text AS line_total,
       trim_scale(p.line_payer_total)::text AS line_payer_total,
       trim_scale(p.line_member_total)::text AS line_member_total,
       trim_scale(COALESCE(adjusted.adjustment_total, 0))::text AS adjustment_total,
       trim_scale(COALESCE(adjusted.adjustment_payer_total, 0))::text AS adjustment_payer_total,
       trim_scale(COALESCE(adjusted.adjustment_member_total, 0))::text AS adjustment_member_total,
       -- Whether the claim already sits on a live invoice (WP-I7-02). Until that package
       -- landed, "not yet invoiced" was read off the status alone; a claim allocated to a
       -- *draft* invoice is still APPROVED, and offering it again as invoiceable is how the
       -- same money ends up on two documents. The status is still checked in Go -- this is
       -- the half a status cannot answer.
       EXISTS (
           SELECT 1 FROM billing.invoice_claim ic
            WHERE ic.tenant_id = sqlc.arg('tenant_id')
              AND ic.claim_id = p.claim_id
              AND ic.active
       ) AS on_live_invoice
  FROM per_claim p
  LEFT JOIN adjusted ON adjusted.claim_id = p.claim_id
 WHERE (sqlc.narg('decided_from')::timestamptz IS NULL
        OR p.decided_at >= sqlc.narg('decided_from')::timestamptz)
   AND (sqlc.narg('decided_to')::timestamptz IS NULL
        OR p.decided_at < sqlc.narg('decided_to')::timestamptz)
   AND (sqlc.narg('currency_code')::text IS NULL
        OR p.currency_code = sqlc.narg('currency_code')::text)
 ORDER BY p.decided_at, p.claim_id;

-- name: ClaimProviderOrganizationName :one
-- The provider's own display name for the earnings header. It is a read of an organization,
-- not of a person, and it carries no identifier or contact value.
SELECT o.display_name
  FROM directory.tenant_organization t
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE t.tenant_id = sqlc.arg('tenant_id')
   AND t.id = sqlc.arg('id');

-- name: SetClaimInvoiceStatus :execrows
-- The claim moving onto an invoice (WP-I7-02), and back off one. The billing module owns
-- *when* it happens and this module owns *whether* it may, which is why the statement lives
-- here and is reached through the claim service's own command rather than from billing's
-- repository.
--
-- `from_statuses` is the whole precondition and there is no row_version: the concurrency
-- control of this transition is the invoice's If-Match and the row lock the invoice command
-- already holds, and demanding a claim's ETag as well would make an invoice covering fifty
-- claims unsubmittable whenever a reviewer had touched any one of them.
UPDATE claim.claim
   SET status     = sqlc.arg('status'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = ANY(sqlc.arg('from_statuses')::text[]);
