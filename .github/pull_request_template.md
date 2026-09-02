<!--
Title format: "WP-I1-0X: short title" for delegated work packages, or a Conventional
Commit style title for integrator changes.
Paste your completed report (docs/delegation/REPORT_TEMPLATE.md) below. Every section is
required; write "none" instead of deleting a heading.
-->

## Work package

- WP: 
- Issue: closes #

## Report

<!-- paste REPORT.md here -->

## Checklist (author)

- [ ] `make ci` green locally (fmt, vet, lint, unit tests, generated code current)
- [ ] `make test-db` green on PostgreSQL 18
- [ ] Negative authorization / RLS tests added for new tables and endpoints
- [ ] No PII, secrets, `.env`, binaries or `node_modules` in the diff
- [ ] OpenAPI changed only for operations this WP owns; Spectral zero errors
- [ ] Migrations use only the numbers assigned in the WP
- [ ] New dependencies justified in the report
