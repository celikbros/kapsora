# WP-I10-04 · Security review and pilot findings

| Field | Value |
| --- | --- |
| Milestone | M10 |
| Status | READY for threat review and local regression tests |
| Depends on | M1–M7; I10-01 and deployment configuration before final review |
| Migration numbers | None assigned |
| API ownership | Only reviewed fixes; contract changes precede implementation |
| Read first | `README.md`, `../HANDOVER.md` §6, applicable security/privacy ADRs |

## Goal

Close pilot security findings with reproducible evidence while preserving the established
authorization, privacy and document-handling boundaries.

## Scope

- Review authentication, session expiry, CSRF, password step-up, lockout, rate limiting,
  application-scoped grants, tenant boundaries and own-file/maker-checker refusals.
- Review encrypted identifiers, log/audit/export disclosure, presigned URL lifetime,
  document quarantine/scanning, download authorization and sensitive clinical access.
- Include I10-01 import/webhook attack surfaces and the actual deployment configuration.
- Run the existing dependency/secret checks and add focused negative regression tests for
  findings. Do not replace server/database authorization with UI-only restrictions.
- Track finding severity, affected operation, reproduction, fix and independent retest.
  Keep tokens, personal records and exploit payloads with sensitive contents out of git.

## Required verification and exit evidence

- Cross-tenant and cross-app attempts fail without revealing whether a foreign record exists.
- Sponsor HR receives no diagnosis or clinical narrative, even on a record with sensitive data.
- Expired/replayed requests, tampered downloads and unclean documents cannot bypass controls.
- New and changed refusal codes have Turkish messages and tests.
- No unresolved critical/high finding at pilot admission. Any proposed residual risk must
  be described and explicitly accepted by the responsible owner, not silently waived.
- The report distinguishes automated checks, internal review and an external penetration
  test. A local test suite is not a substitute for a completed penetration-test engagement.

An external test requires a named test owner, authorized target, scope and time window.
Prepare local review and regression coverage without contacting third parties or scanning
systems outside the designated project environment.
