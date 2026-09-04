-- Contract queries (WP-I3-03): contracts, their versions, the price lists and items a
-- quote lands on, the packages and quotas a version grants, the payment term it settles
-- under, and the candidate load the deterministic price selection scores.
--
-- Every statement filters on tenant_id explicitly and runs inside db.WithTenantTx, so RLS
-- is the second line of defence. Money is numeric(20,6) and never touches a float: it is
-- read as `::text` and written as `::text::numeric`, so the exact decimal the tenant
-- agreed to is the exact decimal that is stored and returned.

-- name: CreateContract :one
INSERT INTO contract.contract (tenant_id, code, name, payer_organization_id,
                               provider_profile_id, sponsor_organization_id, domain_code)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('code'), sqlc.arg('name'),
        sqlc.arg('payer_organization_id'), sqlc.arg('provider_profile_id'),
        sqlc.narg('sponsor_organization_id'), sqlc.arg('domain_code'))
RETURNING id;

-- name: GetContract :one
-- The two party names are joined in because every contract screen shows "who with whom",
-- and a second round trip per row would make the list view N+1.
SELECT c.id, c.code, c.name, c.payer_organization_id, c.provider_profile_id,
       c.sponsor_organization_id, c.domain_code, c.status, c.created_at, c.row_version,
       payer.display_name AS payer_name,
       provider_org.display_name AS provider_name,
       sponsor.display_name AS sponsor_name
  FROM contract.contract c
  JOIN directory.tenant_organization payer_rel
    ON payer_rel.tenant_id = c.tenant_id AND payer_rel.id = c.payer_organization_id
  JOIN directory.organization payer ON payer.id = payer_rel.organization_id
  JOIN provider.provider_profile p
    ON p.tenant_id = c.tenant_id AND p.id = c.provider_profile_id
  JOIN directory.tenant_organization provider_rel
    ON provider_rel.tenant_id = p.tenant_id AND provider_rel.id = p.tenant_organization_id
  JOIN directory.organization provider_org ON provider_org.id = provider_rel.organization_id
  LEFT JOIN directory.tenant_organization sponsor_rel
    ON sponsor_rel.tenant_id = c.tenant_id AND sponsor_rel.id = c.sponsor_organization_id
  LEFT JOIN directory.organization sponsor ON sponsor.id = sponsor_rel.organization_id
 WHERE c.tenant_id = sqlc.arg('tenant_id') AND c.id = sqlc.arg('id');

-- name: ListContracts :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows to
-- learn whether a next page exists.
SELECT c.id, c.code, c.name, c.payer_organization_id, c.provider_profile_id,
       c.sponsor_organization_id, c.domain_code, c.status, c.created_at, c.row_version,
       payer.display_name AS payer_name,
       provider_org.display_name AS provider_name,
       sponsor.display_name AS sponsor_name
  FROM contract.contract c
  JOIN directory.tenant_organization payer_rel
    ON payer_rel.tenant_id = c.tenant_id AND payer_rel.id = c.payer_organization_id
  JOIN directory.organization payer ON payer.id = payer_rel.organization_id
  JOIN provider.provider_profile p
    ON p.tenant_id = c.tenant_id AND p.id = c.provider_profile_id
  JOIN directory.tenant_organization provider_rel
    ON provider_rel.tenant_id = p.tenant_id AND provider_rel.id = p.tenant_organization_id
  JOIN directory.organization provider_org ON provider_org.id = provider_rel.organization_id
  LEFT JOIN directory.tenant_organization sponsor_rel
    ON sponsor_rel.tenant_id = c.tenant_id AND sponsor_rel.id = c.sponsor_organization_id
  LEFT JOIN directory.organization sponsor ON sponsor.id = sponsor_rel.organization_id
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('provider_profile_id')::uuid IS NULL
        OR c.provider_profile_id = sqlc.narg('provider_profile_id')::uuid)
   AND (sqlc.narg('payer_organization_id')::uuid IS NULL
        OR c.payer_organization_id = sqlc.narg('payer_organization_id')::uuid)
   AND (sqlc.narg('domain_code')::text IS NULL OR c.domain_code = sqlc.narg('domain_code')::text)
   AND (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
   -- The caller escapes the user's own wildcards, so the default backslash escape
   -- character makes '%' and '_' literal characters here.
   AND (sqlc.narg('q')::text IS NULL
        OR c.code ILIKE sqlc.narg('q')::text OR c.name ILIKE sqlc.narg('q')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (c.created_at, c.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY c.created_at DESC, c.id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdateContract :one
-- platform.tg_touch_row bumps row_version and updated_at, so this statement never assigns
-- them. The code, the parties and the domain are absent: they are what the contract is,
-- and every published version was agreed under them.
UPDATE contract.contract
   SET name = sqlc.arg('name'),
       sponsor_organization_id = sqlc.narg('sponsor_organization_id'),
       status = sqlc.arg('status')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version')
RETURNING row_version;

-- name: NextContractVersionNo :one
SELECT (coalesce(max(version_no), 0) + 1)::int AS next_version_no
  FROM contract.contract_version
 WHERE tenant_id = $1 AND contract_id = $2;

-- name: CreateContractVersion :one
INSERT INTO contract.contract_version (tenant_id, contract_id, version_no, valid_from,
                                       valid_to, currency_code, notes)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('contract_id'), sqlc.arg('version_no'),
        sqlc.narg('valid_from'), sqlc.narg('valid_to'), sqlc.arg('currency_code'),
        sqlc.narg('notes'))
RETURNING id;

-- name: GetContractVersion :one
SELECT v.id, v.contract_id, v.version_no, v.status, v.valid_from, v.valid_to,
       v.currency_code, v.notes, v.configuration_hash, v.submitted_at, v.submitted_by,
       v.published_at, v.published_by, v.retire_reason_code, v.review_comment,
       v.created_at, v.row_version
  FROM contract.contract_version v
 WHERE v.tenant_id = $1 AND v.id = $2;

-- name: ListContractVersions :many
-- Highest version number first: a screen picking between "what we agreed then" and "what
-- we agreed now" wants the newest agreement at the top.
SELECT v.id, v.contract_id, v.version_no, v.status, v.valid_from, v.valid_to,
       v.currency_code, v.notes, v.configuration_hash, v.submitted_at, v.submitted_by,
       v.published_at, v.published_by, v.retire_reason_code, v.review_comment,
       v.created_at, v.row_version
  FROM contract.contract_version v
 WHERE v.tenant_id = $1 AND v.contract_id = $2
 ORDER BY v.version_no DESC;

-- name: GetContractVersionForUpdate :one
-- Every command on a version takes this row lock first, so two publishes of the same
-- version cannot both read UNDER_REVIEW and both proceed.
SELECT v.id, v.contract_id, v.version_no, v.status, v.valid_from, v.valid_to,
       v.currency_code, v.notes, v.configuration_hash, v.submitted_at, v.submitted_by,
       v.published_at, v.published_by, v.retire_reason_code, v.review_comment,
       v.created_at, v.row_version
  FROM contract.contract_version v
 WHERE v.tenant_id = $1 AND v.id = $2
   FOR UPDATE;

-- name: UpdateContractVersionDraft :execrows
UPDATE contract.contract_version
   SET valid_from = sqlc.narg('valid_from'),
       valid_to = sqlc.narg('valid_to'),
       currency_code = sqlc.arg('currency_code'),
       notes = sqlc.narg('notes')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT';

-- name: TouchContractVersion :execrows
-- Price lists, items, packages, quotas and the payment term are child rows of the version,
-- so writing any of them has to move the ETag the caller holds for the version itself.
UPDATE contract.contract_version
   SET notes = notes
 WHERE tenant_id = $1 AND id = $2;

-- name: SubmitContractVersion :execrows
UPDATE contract.contract_version
   SET status = 'UNDER_REVIEW',
       submitted_at = clock_timestamp(),
       submitted_by = sqlc.arg('actor_id'),
       review_comment = sqlc.narg('review_comment')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT';

-- name: PublishContractVersion :execrows
-- ex_contract_version_published_overlap is the authority on two published versions of one
-- contract covering the same date; it surfaces as SQLSTATE 23P01.
UPDATE contract.contract_version
   SET status = 'PUBLISHED',
       published_at = clock_timestamp(),
       published_by = sqlc.arg('actor_id'),
       configuration_hash = sqlc.arg('configuration_hash'),
       review_comment = coalesce(sqlc.narg('review_comment'), review_comment)
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'UNDER_REVIEW';

-- name: RetireContractVersion :execrows
UPDATE contract.contract_version
   SET status = 'RETIRED',
       retire_reason_code = sqlc.arg('reason_code'),
       review_comment = coalesce(sqlc.narg('reason_text'), review_comment)
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'PUBLISHED';

-- name: ListPriceLists :many
-- The item count comes along because a price sheet with an empty list in it is the one
-- thing submit refuses, and a screen should be able to show that before trying.
SELECT l.id, l.contract_version_id, l.code, l.name, l.priority, l.season_from, l.season_to,
       l.weekday_mask, l.created_at, l.row_version,
       (SELECT count(*) FROM contract.price_item i
         WHERE i.tenant_id = l.tenant_id AND i.price_list_id = l.id)::int AS item_count
  FROM contract.price_list l
 WHERE l.tenant_id = $1 AND l.contract_version_id = $2
 ORDER BY l.priority DESC, l.code;

-- name: GetPriceList :one
SELECT l.id, l.contract_version_id, l.code, l.name, l.priority, l.season_from, l.season_to,
       l.weekday_mask, l.created_at, l.row_version,
       (SELECT count(*) FROM contract.price_item i
         WHERE i.tenant_id = l.tenant_id AND i.price_list_id = l.id)::int AS item_count
  FROM contract.price_list l
 WHERE l.tenant_id = $1 AND l.id = $2;

-- name: UpsertPriceList :one
-- A list already present under the same code keeps its id, so the price items hanging off
-- it survive a set write that only meant to rename or re-season the list.
INSERT INTO contract.price_list (tenant_id, contract_version_id, code, name, priority,
                                 season_from, season_to, weekday_mask)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('contract_version_id'), sqlc.arg('code'),
        sqlc.arg('name'), sqlc.arg('priority'), sqlc.narg('season_from'),
        sqlc.narg('season_to'), sqlc.narg('weekday_mask'))
ON CONFLICT (tenant_id, contract_version_id, code) DO UPDATE
   SET name = excluded.name,
       priority = excluded.priority,
       season_from = excluded.season_from,
       season_to = excluded.season_to,
       weekday_mask = excluded.weekday_mask
RETURNING id;

-- name: DeletePriceListsExcept :execrows
-- The rows the set write did not name; their price items go with them by cascade.
DELETE FROM contract.price_list
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND contract_version_id = sqlc.arg('contract_version_id')
   AND NOT (id = ANY(sqlc.arg('keep_ids')::uuid[]));

-- name: TouchPriceList :execrows
UPDATE contract.price_list
   SET name = name
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version');

-- name: ListPriceItems :many
-- Oldest first: a tariff is read in the order it was entered, and the keyset walks the
-- same direction. Amounts leave as exact decimal text.
SELECT i.id, i.price_list_id, i.service_definition_id, i.service_category_id,
       i.package_definition_id, i.location_id, i.unit_type, i.pricing_method,
       coalesce(i.amount::text, '')::text AS amount,
       coalesce(i.percent::text, '')::text AS percent,
       i.formula_key,
       coalesce(i.min_amount::text, '')::text AS min_amount,
       coalesce(i.max_amount::text, '')::text AS max_amount,
       i.member_share_method,
       coalesce(i.member_share_amount::text, '')::text AS member_share_amount,
       coalesce(i.member_share_percent::text, '')::text AS member_share_percent,
       i.valid_from, i.valid_to, i.priority, i.created_at,
       d.code AS service_definition_code,
       g.code AS service_category_code,
       k.code AS package_definition_code
  FROM contract.price_item i
  LEFT JOIN catalog.service_definition d
    ON d.tenant_id = i.tenant_id AND d.id = i.service_definition_id
  LEFT JOIN catalog.service_category g
    ON g.tenant_id = i.tenant_id AND g.id = i.service_category_id
  LEFT JOIN contract.package_definition k
    ON k.tenant_id = i.tenant_id AND k.id = i.package_definition_id
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.price_list_id = sqlc.arg('price_list_id')
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (i.created_at, i.id) > (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY i.created_at, i.id
 LIMIT sqlc.arg('page_size');

-- name: CountPriceItemsInVersion :one
-- Submit refuses a version whose price sheet holds no item at all.
SELECT count(*)::int AS item_count
  FROM contract.price_item i
  JOIN contract.price_list l ON l.tenant_id = i.tenant_id AND l.id = i.price_list_id
 WHERE i.tenant_id = $1 AND l.contract_version_id = $2;

-- name: DeletePriceItems :execrows
DELETE FROM contract.price_item
 WHERE tenant_id = $1 AND price_list_id = $2;

-- name: CreatePriceItem :batchexec
INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id,
                                 service_category_id, package_definition_id, location_id,
                                 unit_type, pricing_method, amount, percent, formula_key,
                                 min_amount, max_amount, member_share_method,
                                 member_share_amount, member_share_percent,
                                 valid_from, valid_to, priority)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('price_list_id'), sqlc.narg('service_definition_id'),
        sqlc.narg('service_category_id'), sqlc.narg('package_definition_id'),
        sqlc.narg('location_id'), sqlc.arg('unit_type'), sqlc.arg('pricing_method'),
        sqlc.narg('amount')::text::numeric,
        sqlc.narg('percent')::text::numeric,
        sqlc.narg('formula_key'),
        sqlc.narg('min_amount')::text::numeric,
        sqlc.narg('max_amount')::text::numeric,
        sqlc.arg('member_share_method'),
        sqlc.narg('member_share_amount')::text::numeric,
        sqlc.narg('member_share_percent')::text::numeric,
        sqlc.arg('valid_from'), sqlc.narg('valid_to'), sqlc.arg('priority'));

-- name: ListPackageDefinitions :many
SELECT k.id, k.contract_version_id, k.code, k.name, k.inclusion_rule, k.min_lines,
       k.created_at, k.row_version
  FROM contract.package_definition k
 WHERE k.tenant_id = $1 AND k.contract_version_id = $2
 ORDER BY k.code;

-- name: ListPackageLinesInVersion :many
-- All lines of all packages of one version in a single read, so a package list is not
-- N+1 in the number of bundles.
SELECT pl.id, pl.package_definition_id, pl.service_definition_id,
       pl.included_quantity::text AS included_quantity,
       d.code AS service_definition_code
  FROM contract.package_line pl
  JOIN contract.package_definition k
    ON k.tenant_id = pl.tenant_id AND k.id = pl.package_definition_id
  LEFT JOIN catalog.service_definition d
    ON d.tenant_id = pl.tenant_id AND d.id = pl.service_definition_id
 WHERE pl.tenant_id = $1 AND k.contract_version_id = $2
 ORDER BY k.code, d.code;

-- name: UpsertPackageDefinition :one
-- Keeping the id of a package already known under this code is what lets the price items
-- pointing at it survive a set write of the packages.
INSERT INTO contract.package_definition (tenant_id, contract_version_id, code, name,
                                         inclusion_rule, min_lines)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('contract_version_id'), sqlc.arg('code'),
        sqlc.arg('name'), sqlc.arg('inclusion_rule'), sqlc.narg('min_lines'))
ON CONFLICT (tenant_id, contract_version_id, code) DO UPDATE
   SET name = excluded.name,
       inclusion_rule = excluded.inclusion_rule,
       min_lines = excluded.min_lines
RETURNING id;

-- name: DeletePackageDefinitionsExcept :execrows
-- A package the set write did not name goes, and so do the price items that priced it:
-- fk_price_item_package cascades on purpose, because a price for a bundle that no longer
-- exists could never be selected and would only sit there looking valid.
DELETE FROM contract.package_definition
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND contract_version_id = sqlc.arg('contract_version_id')
   AND NOT (id = ANY(sqlc.arg('keep_ids')::uuid[]));

-- name: DeletePackageLines :execrows
DELETE FROM contract.package_line
 WHERE tenant_id = $1 AND package_definition_id = $2;

-- name: CreatePackageLine :batchexec
INSERT INTO contract.package_line (tenant_id, package_definition_id, service_definition_id,
                                   included_quantity)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('package_definition_id'),
        sqlc.arg('service_definition_id'), sqlc.arg('included_quantity')::text::numeric);

-- name: ListProviderQuotas :many
SELECT q.id, q.contract_version_id, q.location_id, q.service_definition_id, q.period_type,
       q.period_from, q.period_to,
       q.capacity::text AS capacity,
       q.consumed::text AS consumed,
       q.allow_overdraft, q.created_at, q.row_version
  FROM contract.provider_quota q
 WHERE q.tenant_id = $1 AND q.contract_version_id = $2
 ORDER BY q.period_from, q.period_to, q.id;

-- name: UpsertProviderQuota :one
-- The scope index is NULLS NOT DISTINCT, so a tenant-wide quota for a period collides with
-- another tenant-wide one instead of both existing. Re-writing a known scope keeps the
-- consumed counter, which authorization owns and this module must never reset.
INSERT INTO contract.provider_quota (tenant_id, contract_version_id, location_id,
                                     service_definition_id, period_type, period_from,
                                     period_to, capacity, allow_overdraft)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('contract_version_id'), sqlc.narg('location_id'),
        sqlc.narg('service_definition_id'), sqlc.arg('period_type'), sqlc.arg('period_from'),
        sqlc.arg('period_to'), sqlc.arg('capacity')::text::numeric, sqlc.arg('allow_overdraft'))
ON CONFLICT (tenant_id, contract_version_id, location_id, service_definition_id,
             period_from, period_to) DO UPDATE
   SET period_type = excluded.period_type,
       capacity = excluded.capacity,
       allow_overdraft = excluded.allow_overdraft
RETURNING id;

-- name: DeleteProviderQuotasExcept :execrows
DELETE FROM contract.provider_quota
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND contract_version_id = sqlc.arg('contract_version_id')
   AND NOT (id = ANY(sqlc.arg('keep_ids')::uuid[]));

-- name: GetPaymentTerm :one
SELECT t.id, t.contract_version_id, t.due_days, t.settlement_method, t.tax_behaviour,
       coalesce(t.vat_rate::text, '')::text AS vat_rate,
       coalesce(t.late_fee_percent::text, '')::text AS late_fee_percent,
       t.created_at, t.row_version
  FROM contract.payment_term t
 WHERE t.tenant_id = $1 AND t.contract_version_id = $2;

-- name: UpsertPaymentTerm :one
INSERT INTO contract.payment_term (tenant_id, contract_version_id, due_days,
                                   settlement_method, tax_behaviour, vat_rate,
                                   late_fee_percent)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('contract_version_id'), sqlc.arg('due_days'),
        sqlc.arg('settlement_method'), sqlc.arg('tax_behaviour'),
        sqlc.narg('vat_rate')::text::numeric,
        sqlc.narg('late_fee_percent')::text::numeric)
ON CONFLICT (tenant_id, contract_version_id) DO UPDATE
   SET due_days = excluded.due_days,
       settlement_method = excluded.settlement_method,
       tax_behaviour = excluded.tax_behaviour,
       vat_rate = excluded.vat_rate,
       late_fee_percent = excluded.late_fee_percent
RETURNING id;

-- name: ListPriceCandidates :many
-- Everything the price selection may score for one request, with the cheap filters already
-- applied: a published version of an active contract of the named provider whose period
-- contains the service date, and an item naming the definition, a category on the chain
-- above it, or a package containing it. Season, weekday, item period, location and the
-- specificity ladder are judgement rather than filtering, so they are decided in
-- internal/contract/selection where they can be tested exhaustively without a database.
SELECT i.id AS price_item_id, i.price_list_id, l.contract_version_id,
       i.service_definition_id, i.service_category_id, i.package_definition_id,
       i.location_id, i.valid_from, i.valid_to, i.priority AS item_priority,
       i.unit_type, i.pricing_method,
       coalesce(i.amount::text, '')::text AS amount,
       coalesce(i.percent::text, '')::text AS percent,
       i.formula_key,
       coalesce(i.min_amount::text, '')::text AS min_amount,
       coalesce(i.max_amount::text, '')::text AS max_amount,
       i.member_share_method,
       coalesce(i.member_share_amount::text, '')::text AS member_share_amount,
       coalesce(i.member_share_percent::text, '')::text AS member_share_percent,
       l.code AS price_list_code, l.priority AS list_priority,
       l.season_from, l.season_to, l.weekday_mask,
       v.version_no, v.currency_code,
       c.id AS contract_id, c.code AS contract_code
  FROM contract.price_item i
  JOIN contract.price_list l ON l.tenant_id = i.tenant_id AND l.id = i.price_list_id
  JOIN contract.contract_version v ON v.tenant_id = l.tenant_id AND v.id = l.contract_version_id
  JOIN contract.contract c ON c.tenant_id = v.tenant_id AND c.id = v.contract_id
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND v.status = 'PUBLISHED'
   AND c.status = 'ACTIVE'
   AND c.provider_profile_id = sqlc.arg('provider_profile_id')
   AND v.valid_from <= sqlc.arg('service_date')::date
   AND (v.valid_to IS NULL OR v.valid_to > sqlc.arg('service_date')::date)
   AND (i.service_definition_id = sqlc.arg('service_definition_id')::uuid
        OR i.service_category_id = ANY(sqlc.arg('category_ids')::uuid[])
        OR i.package_definition_id = ANY(sqlc.arg('package_ids')::uuid[]))
 ORDER BY i.id;

-- name: ListPackagesContainingDefinition :many
-- The packages whose lines name this service definition; the selection scores a package
-- price only for a service the bundle actually contains.
SELECT DISTINCT pl.package_definition_id AS id
  FROM contract.package_line pl
 WHERE pl.tenant_id = $1 AND pl.service_definition_id = $2;

-- name: ContractServiceDefinitionExists :one
-- Probed before a price item or a package line names a definition, so a wrong id is a
-- field error on the request rather than a raw foreign key violation.
SELECT EXISTS (SELECT 1 FROM catalog.service_definition WHERE tenant_id = $1 AND id = $2) AS present;
