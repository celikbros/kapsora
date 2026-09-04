# WP-I3-05 · Pricing quote: what is charged, who pays which part, and why

| Field                      | Value                                                                                                      |
| -------------------------- | ---------------------------------------------------------------------------------------------------------- |
| Milestone                  | M3 (plan increment I3)                                                                                     |
| Size                       | M                                                                                                          |
| Depends on                 | WP-I3-01, WP-I3-02, WP-I3-03, WP-I3-04, WP-I2-04 (eligibility)                                             |
| Runs in parallel with      | WP-I3-06                                                                                                   |
| Migration numbers assigned | `000023_pricing_quote.up.sql` (also seeds the one permission M3 adds, `pricing.quote`)                      |
| OpenAPI operations owned   | `createPriceQuote`, `getPriceQuote`                                                                        |
| Read first                 | v1.2 11.5, 11.6, 9.8; WP-I3-03 selection; WP-I2-04 eligibility snapshots (this mirrors their immutability) |

## 1. Goal

Before anything is booked, approved or claimed, somebody needs a number: what this service
costs at this provider on this date, how much the plan covers, and what the member will pay
out of pocket. A quote answers that, explains every line of the arithmetic, and is stored
so that the number quoted and the number later claimed can be compared.

## 2. Scope

### 2.1 The calculation (pure, `internal/pricing`)

Input: tenant, `personId`, `serviceDate`, `providerProfileId`, optional `locationId`,
optional `programId`, and `items[]` of `{serviceDefinitionId | packageDefinitionId,
quantity}`. Quantities and money are decimal strings end to end.

Per item, in this order, each step producing explanation codes:

1. **Eligibility** — call WP-I2-04's resolver for the person, date and items. An
   `INELIGIBLE` person still gets a quote (the member may pay privately) but every
   coverage figure is zero and the outcome carries `NOT_ELIGIBLE`.
2. **Price selection** — WP-I3-03's deterministic selection. `PRICE_AMBIGUOUS` or
   `PRICE_NOT_FOUND` makes the item `REVIEW_REQUIRED`; the quote as a whole is then
   `REVIEW_REQUIRED` and carries no member figure, because a guess here is worse than an
   answer of "we do not know".
3. **Contract amount** — `listAmount` from the price item by its method
   (FIXED / UNIT × quantity / PERCENT_OF_LIST of a supplied `requestedAmount` / FORMULA),
   clamped by `min_amount` and `max_amount`. `FORMULA` resolves through the rule engine
   with action `ADJUST_PRICE`; an unknown `formula_key` is an ERROR, never a fallback to
   the raw amount.
4. **Rules** — evaluate the tenant's published PRICE rule set for the date, if any.
   `SET_LIMIT` caps the covered amount; `ADJUST_PRICE` adjusts it; `REQUIRE_*` marks the
   item `REVIEW_REQUIRED`. Every applied action is recorded with its rule code.
5. **Entitlement cover** — the balance available on the matching entitlement account caps
   what the plan can carry. Nothing is reserved here: **a quote never touches the ledger.**
6. **Split** — `payerAmount = min(coveredAmount, availableBalance)`;
   `memberAmount = contractAmount − payerAmount + memberShare`, where `memberShare` comes
   from the price item's `member_share_method`. Both are non-negative by construction; a
   negative intermediate is a bug and panics in tests rather than silently clamping.

Rounding happens **once**, at the end, per item, to the currency's minor unit, using
half-up; every earlier step keeps full `numeric(20,6)` precision (v1.2 11.6). The rounding
step is recorded as its own explanation line so a one-kuruş difference is never mysterious.

The result carries, per item and in total: `requestedAmount`, `contractAmount`,
`coveredAmount`, `payerAmount`, `memberAmount`, `currencyCode`, plus `explanations[]` and
the ids of everything used — `contractVersionId`, `priceListId`, `priceItemId`,
`planVersionId`, `ruleSetVersionIds[]`, `eligibilityEvaluationId`.

### 2.2 Persistence (migration 000023)

`contract.price_quote`: id, tenant_id, person_id, program_id NULL, provider_profile_id,
location_id NULL, service_date, `currency_code`, `outcome`
(`QUOTED`,`PARTIAL`,`REVIEW_REQUIRED`,`NOT_ELIGIBLE`), total columns
`requested_amount`/`contract_amount`/`covered_amount`/`payer_amount`/`member_amount`
`numeric(20,6)`, `request_hash` bytea, `request_snapshot` jsonb, `result_snapshot` jsonb,
`eligibility_evaluation_id` NULL, `expires_at` timestamptz, `quoted_at`, `quoted_by`,
`idempotency_key` text NULL. Append-only; unique `(tenant_id, idempotency_key)` where not
null; index `(tenant_id, person_id, service_date desc)`. RLS.

Snapshots hold ids and quantities only — never an identity number, never a name. A quote
expires (`expires_at`, default the tenant setting `pricing.quote_ttl_hours`, fallback 72 h)
because prices move; an expired quote still reads back, and the response says it expired
rather than hiding it.

### 2.3 API

- `POST /api/v1/pricing/quotes` — permission `pricing.quote`, optional `Idempotency-Key`
  replaying the stored quote. Runs in one `REPEATABLE READ` transaction. Writes an
  `audit.access_event` (classification PERSONAL, or HEALTH when the domain is health).
- `GET /api/v1/pricing/quotes/{id}` — permission `pricing.quote`; provider-scoped actors
  see only quotes for their own provider.
- A quote is not an authorization and grants nothing. The response says so in its
  `disclaimer` field, carried through to the UI, so nobody at a counter mistakes one for
  the other.

## 3. Tests required

- Pure calculation table tests for every method, both share methods, the min/max clamp, the
  balance cap, and the single rounding step (assert an example where rounding earlier would
  give a different total).
- `REVIEW_REQUIRED` propagation from an ambiguous price and from a `REQUIRE_*` rule action.
- dbtest end to end with fixtures from WP-I2-01..03 and WP-I3-01..04: a member with a
  balance of 300 TRY quoted for a 500 TRY service with a 20 % member share resolves to
  exactly the expected payer and member figures, as decimal strings.
- **The ledger is untouched**: account balances and ledger row counts are identical before
  and after a quote.
- Idempotent replay returns the same quote id; expiry is reported, not hidden; no
  identifier appears in either snapshot (assert on the JSON).

## 4. Acceptance criteria

- [ ] A quote explains requested, contract, covered, payer and member amounts, and names
      the contract version, price item, plan version and rule versions behind them.
- [ ] An ambiguous price produces `REVIEW_REQUIRED` and no member figure.
- [ ] Quoting reserves nothing and moves no balance.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; schema version 23.
