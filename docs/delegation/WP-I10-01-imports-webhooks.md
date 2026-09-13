# WP-I10-01 · HR/policy imports and integration webhooks

| Field | Value |
| --- | --- |
| Milestone | M10 — integrations, hardening, pilot |
| Status | PLANNED; source discovery is the first implementation step |
| Depends on | M2 member import, M4 outbox, the pilot source format |
| Migration numbers | None assigned; allocate in landing order after schema review |
| API ownership | Specify integration operations in OpenAPI before implementation |
| Read first | `README.md`, `../plan/KAPSORA_Master_Plan_v2.0.md` §5 I10, `../HANDOVER.md` |

Paths above are relative to this file; repository paths below are explicit.

## Goal

A pilot HR/policy source updates membership and enrollment through the existing validated,
audited import process. Approved integration events are delivered without duplicate business
effects or disclosure of identifiers, clinical data or secrets.

## Scope and sequence

1. Obtain a synthetic source example, stable source key, update/deletion semantics, field
   mapping and delivery frequency. Document the source contract inside this package before
   building a customer adapter. Do not infer a vendor schema from a demo file.
2. Reuse the M2 staging, validation, conflict review and idempotent apply pipeline. Define how
   policy coverage dates and status map to existing enrollment rules; ambiguous records go
   to review. Preserve encrypted identifiers and tenant isolation.
3. Specify webhook direction, consumers and event allowlist with the pilot integration.
   For outbound delivery, use the transactional outbox, authenticated HTTPS, signature and
   replay protection, bounded retries and an operator-visible failure/retry path. Endpoint
   registration must prevent requests to private/internal network targets. Inbound receivers,
   if needed, require their own authenticated, replay-safe contract before implementation.
4. Add configuration and operational instructions to the existing runbooks. Do not introduce
   a second import engine, an ERP connector, or a fiscal provider in this package.

## Required verification

- Replaying a file or event does not duplicate people, enrollments or business effects.
- Invalid dates, unknown source values, conflicting records and partial failures have
  explicit results; a retry resumes safely and has an audit trail.
- A second tenant cannot inspect or apply another tenant's import or delivery record.
- Tampered/expired webhook authentication is refused; retries and dead letters are proved
  against a local synthetic receiver, including network failures and endpoint restrictions.
- Source counts reconcile to accepted, rejected and pending rows with no silent loss.
- New API operations have generated clients, negative permission tests and localized errors.

## Exit evidence and external input

Deliver the source mapping, tests, reconciliation sample, OpenAPI/migration changes if any,
and a report using `REPORT_TEMPLATE.md`. The pilot source format and webhook consumer are
external inputs. Synthetic fixtures can prove the pipeline; they cannot close acceptance
against an unknown customer source.
