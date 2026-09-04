# WP-I3-03 · Contracts, versions, price lists, packages, quotas and payment terms

| Field                      | Value                                                                                                                                                                                                                                                                                                                                                                                |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Milestone                  | M3 (plan increment I3)                                                                                                                                                                                                                                                                                                                                                               |
| Size                       | L                                                                                                                                                                                                                                                                                                                                                                                    |
| Depends on                 | WP-I3-01 (services), WP-I3-02 (providers)                                                                                                                                                                                                                                                                                                                                            |
| Runs in parallel with      | WP-I3-04                                                                                                                                                                                                                                                                                                                                                                             |
| Migration numbers assigned | `000021_contract_and_pricing.up.sql`                                                                                                                                                                                                                                                                                                                                                 |
| OpenAPI operations owned   | `listContracts`, `createContract`, `getContract`, `patchContract`, `listContractVersions`, `createContractVersion`, `getContractVersion`, `patchContractVersion`, `submitContractVersion`, `publishContractVersion`, `retireContractVersion`, `putPriceLists`, `listPriceItems`, `putPriceItems`, `putPackageDefinition`, `listProviderQuotas`, `putProviderQuota`, `putPaymentTerm` |
| Read first                 | v1.2 9.8, 11.5, 11.6, 16.6 (contract rows); WP-I2-02 for the plan version maker-checker this mirrors; ADR-015                                                                                                                                                                                                                                                                        |

## 1. Goal

What the tenant pays a provider for a service on a given date, and how much of that the
member carries. The contract is the agreement, the version is the immutable thing that was
agreed, and the price item is the row that a quote finally lands on. Everything downstream
— quotes (WP-I3-05), authorizations (I4), claims (I7) — reads a _published_ contract
version and nothing else, so this package's real product is a set of rows that cannot
change under a claim that was priced from them.

## 2. Scope

### 2.1 Schema (migration 000021, new `contract` schema)

`contract.contract`: id, tenant_id, `code`, `name`, `payer_organization_id` and
`provider_profile_id` (composite FKs), `sponsor_organization_id` NULL, `domain_code`
(same closed list as `catalog.service_category`), `status`
(`DRAFT`,`ACTIVE`,`SUSPENDED`,`CLOSED`), row_version, timestamps. Unique
`(tenant_id, code)`. Index on the two parties.

`contract.contract_version`: id, tenant_id, contract_id, `version_no` int, `status`
(`DRAFT`,`UNDER_REVIEW`,`PUBLISHED`,`RETIRED`), `valid_from` date, `valid_to` date NULL,
`currency_code` char(3), `notes`, `configuration_hash` text NULL, `submitted_at/_by`,
`published_at/_by`, `retire_reason_code`, `review_comment`, row_version. Unique
`(tenant_id, contract_id, version_no)`. **Exclusion constraint: two PUBLISHED versions of
the same contract may not overlap in `daterange(valid_from, valid_to, '[)')`** (409
`CONTRACT_VERSION_OVERLAP`).

`contract.price_list`: id, tenant_id, contract_version_id, `code`, `name`, `priority` int
NOT NULL DEFAULT 100, `season_from` date NULL, `season_to` date NULL, `weekday_mask`
smallint NULL (bit 0 = Monday; NULL means every day). Unique
`(tenant_id, contract_version_id, code)`. Season and weekday exist because accommodation
(I6) prices by date and day; a health contract simply leaves them NULL.

`contract.price_item`: id, tenant_id, price_list_id, exactly one of
`service_definition_id` / `service_category_id` / `package_definition_id` (CHECK
`num_nonnulls = 1`), `location_id` NULL (composite FK — a price that applies only at one
location), `unit_type`, `pricing_method`
(`FIXED`,`UNIT`,`PERCENT_OF_LIST`,`FORMULA`), `amount numeric(20,6)` NULL,
`percent numeric(9,6)` NULL, `formula_key` text NULL, `min_amount`, `max_amount`
`numeric(20,6)` NULL, `member_share_method` (`NONE`,`FIXED`,`PERCENT`),
`member_share_amount`, `member_share_percent`, `valid_from`, `valid_to` NULL,
`priority` int NOT NULL DEFAULT 100. A CHECK ties the method to its fields (FIXED needs
`amount`, PERCENT_OF_LIST needs `percent`, FORMULA needs `formula_key`). Index
`(tenant_id, price_list_id, service_definition_id, valid_from)`.

`contract.package_definition` + `contract.package_line`: a package is a named bundle
(`code`, `name`, `inclusion_rule` `ALL`/`ANY_OF_N` with `min_lines`), its lines name a
service definition and an included quantity. A price item pointing at a package prices the
bundle as a whole.

`contract.provider_quota`: id, tenant_id, contract_version_id, optional `location_id` and
`service_definition_id`, `period_type` (`DAY`,`WEEK`,`MONTH`,`YEAR`,`CONTRACT`),
`period_from`, `period_to`, `capacity numeric(20,6)` CHECK > 0, `consumed numeric(20,6)`
NOT NULL DEFAULT 0 CHECK >= 0, CHECK `consumed <= capacity` when overdraft is not allowed.
Unique on the scope plus period. This package only creates and reads quotas; consuming
them belongs to authorization (I4), which will take the row lock.

`contract.payment_term`: one row per contract version — `due_days` int, `settlement_method`
(`BANK_TRANSFER`,`OFFSET`,`OTHER`), `tax_behaviour` (`EXCLUSIVE`,`INCLUSIVE`,`EXEMPT`),
`vat_rate numeric(5,2)` NULL, `late_fee_percent` NULL. Unique `(tenant_id, contract_version_id)`.

All tables: composite `(tenant_id, id)`, composite FKs, RLS, touch triggers.

### 2.2 Maker-checker, exactly as plan versions

`submit` (`contract.manage`) → UNDER_REVIEW; `publish` (`contract.publish`, step-up, a
different actor, 403 `MAKER_CHECKER_SAME_ACTOR`) → PUBLISHED with a
`configuration_hash` over the version's price content; `retire` (`contract.publish`,
step-up, reason required) → RETIRED. A version that is not DRAFT refuses every write to
itself, its price lists, its items, its packages, its quotas and its payment term with 409
`CONTRACT_VERSION_IMMUTABLE`. Submitting refuses when the version has no price list with
at least one item (422, field `priceLists`).

Reuse the WP-I2-02 pattern rather than inventing a second one; if the two diverge, that is
a bug in this package.

### 2.3 Deterministic selection (the part that matters)

`internal/contract/selection` is a pure function over loaded rows. Given tenant, service
date, provider profile, optional location, and a service definition (or package), it
returns the single winning price item or a refusal:

1. Candidate versions: PUBLISHED, of an ACTIVE contract, whose period contains the service
   date, whose provider matches.
2. Candidate price lists inside those: season contains the date (or NULL), weekday mask
   matches (or NULL).
3. Candidate items: period contains the date; location matches exactly or is NULL; the
   item names the definition, or a category that is an ancestor of the definition's
   category, or a package containing it.
4. **Specificity score**, highest wins: definition match 4, package match 3, category match
   `2 - depth_distance/100` (a nearer ancestor beats a further one), plus 1 when
   `location_id` is not NULL.
5. Tie on specificity → higher `priority` on the item, then on the price list.
6. Still tied → return `REVIEW_REQUIRED` with explanation `PRICE_AMBIGUOUS` naming every
   tied item id. **Never pick one.** This is v1.2 11.5 and it is not negotiable: a random
   winner is a silent financial error.
7. No candidate → `PRICE_NOT_FOUND`.

The function returns the winner _and_ the full candidate list with each one's score, so a
screen can explain the choice rather than assert it.

### 2.4 API notes

Price lists, items, packages and quotas are written as sets under their version
(`PUT`), for the same reason capabilities are: the meaningful unit is the whole price
sheet, and a set write makes overlap and consistency one decision. Reads are paged.
A published version's price sheet is readable by `contract.read`; drafts need
`contract.manage`.

## 3. Tests required

- Pure selection table tests: every rung of the ladder, including a category price beaten
  by a definition price, a location price beating a tenant-wide one, an expired item
  ignored, a weekday mask miss, and a genuine tie producing `PRICE_AMBIGUOUS` with both ids.
- dbtest: published overlap refused; every write to a non-draft version refused; submit
  without a price item refused; publish by the submitter refused; hash stable for the same
  content and different for changed content; quota CHECK; RLS on all seven tables.
- Money: amounts round-trip as `numeric(20,6)` decimal strings; no float appears anywhere
  in the Go path (assert with a query on the column types and a decimal-string test).

## 4. Acceptance criteria

- [ ] For a service date the system selects exactly one contract and price, and can show
      why; two equally specific prices answer `REVIEW_REQUIRED`, never a coin flip.
- [ ] A published contract version cannot be changed in any of its parts.
- [ ] Publishing requires a second person and a fresh password.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; schema version 21.
