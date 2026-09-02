# Work Package Report

Copy this file as `REPORT.md` in your delivery (or paste it into the pull request). Fill every
section; write "none" rather than deleting a heading. Keep it factual: the integrator will run
your commands and compare them with what you claim.

## 1. Identification

| Field | Value |
|---|---|
| Work package | WP-I1-0X |
| Title | |
| Developer | name / handle |
| Branch | wp/I1-0X |
| Base commit on main | `git rev-parse --short main` |
| Delivery date | YYYY-MM-DD |
| Delivery form | pull request URL / patch zip name |

## 2. Summary (max 5 lines)

What was delivered and what state it is in.

## 3. Scope delivered vs. not delivered

| WP requirement (number from the WP) | Status (done / partial / not done) | Notes |
|---|---|---|
| | | |

## 4. Files changed

List every added, modified and deleted path grouped by area (migrations, queries, Go
packages, OpenAPI, frontend, docs, scripts).

## 5. Contract and data changes

- OpenAPI operations added/changed: (operationId list, and whether Spectral is clean)
- Migrations added: (file names, migration numbers assigned in the WP)
- sqlc query files added/changed:
- New permissions, events, jobs, config keys (`KAPSORA_*`):

## 6. Dependencies added

| Module | Version | Why it was needed and what alternative was rejected |
|---|---|---|
| | | |

## 7. How to verify

Exact commands from a clean checkout of your branch, in order. Example:

```sh
cp .env.example .env   # then set CHANGE_ME values
make db-init && make migrate-up
make ci
make test-db
go test -count=1 ./internal/identity/...
```

Any manual step (creating a Keycloak user, starting a native service) must be listed with
the exact commands and expected output.

## 8. Test evidence

Paste the final lines of the test runs (package, ok/FAIL, duration) and coverage figures for
the domain packages you own. Do not paste full logs.

## 9. Deviations from the WP or the handbook

Every deviation with the reason and the alternative you considered. If none, say "none".

## 10. Assumptions and questions

Every decision you made without an explicit instruction, and every question that still
needs an answer from the integrator or the owner.

## 11. Known limitations and risks

What does not work yet, performance concerns, security concerns you noticed while
implementing (including in existing code you touched).

## 12. Integrator checklist (filled by the integrator, leave empty)

- [ ] Report matches the diff
- [ ] `make ci` green on integrator machine
- [ ] `make test-db` green on PostgreSQL 18
- [ ] RLS / authorization negative tests present
- [ ] No PII, secrets or forbidden dependencies
- [ ] OpenAPI diff non-breaking or approved
- [ ] Merged / returned with change requests (date)
