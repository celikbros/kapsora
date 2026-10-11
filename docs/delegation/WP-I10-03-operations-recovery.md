# WP-I10-03 · Deployment verification and disaster recovery

| Field | Value |
| --- | --- |
| Milestone | M10 |
| Status | DEFERRED by owner (2026-09-13): working product takes priority |
| Depends on | Existing native deployment, document storage and encrypted fields |
| Migration numbers | None |
| API ownership | Existing health probes; no new operation planned |
| Read first | `README.md`, `../runbooks/deploy-single-server.md`, `../runbooks/backup-restore.md`, baseline §§30.4–31 |

## Goal

An operator can deploy and restore a consistent, usable system on a clean target with
measured data loss and elapsed recovery time.

## Scope

- Complete the deferred Ubuntu VM installation and `systemd-analyze verify` checks for
  API, worker, scheduler and migration units. Verify proxy routing for all three apps,
  secure cookies, restart behavior and operator-owned startup commands. No containers.
- Verify readiness reflects PostgreSQL, both authenticated document buckets and clamd;
  liveness remains a process check. Monitor outbox age and scan backlog separately.
- Prepare encrypted PostgreSQL PITR and a separate object-storage copy with versioning.
  Inventory encryption-key dependencies without recording their values in evidence.
- Restore into an isolated target. Never overwrite the source installation or delete its
  backups as part of a drill. Check database/object references, clean/quarantine boundaries,
  access to encryption keys and tenant isolation after recovery.
- Test login, eligibility, document access, one booking and one financial reconciliation
  using synthetic restored data. Account for jobs that may be redelivered after restore.

## Targets and known document discrepancy

Baseline §30.4 specifies RPO ≤5 minutes and RTO ≤2 hours. The existing backup runbook says
15 minutes /4 hours while attributing them to §31, which does not state those values.
Use the stricter normative targets for acceptance and correct that runbook in this package.
A different contractual target requires an explicit recorded owner decision.

## Required evidence and acceptance

- Deployment commands, unit verification output and service/proxy checks from the named VM.
- Backup identifier, source checkpoint, last recovered transaction and timestamps that
  demonstrate actual RPO/RTO; a successful backup command alone is insufficient.
- Database/object consistency, reconciliation and restored-user smoke results.
- Operator-reviewed runbook and unresolved findings. Store captures and raw logs outside
  tracked documentation; keep only concise reports and reproducible commands in git.
- Execution requires designated source/restore environments and backup destinations.
