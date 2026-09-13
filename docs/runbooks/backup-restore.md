# Backup and isolated recovery

Acceptance targets are **RPO <=5 minutes and RTO <=2 hours**, from the frozen v1.2
baseline section 30.4. The former 15-minute /4-hour values in this guide were incorrect.
This is the preparation slice of [WP-I10-03](../delegation/WP-I10-03-operations-recovery.md);
no Ubuntu restore drill or measured RPO/RTO has been completed yet.

## 1. Name the recovery environment

Record the source host/database, isolated Ubuntu target, PostgreSQL 18 and pgBackRest
versions, application revision, schema version, backup repository and object-store copy.
Also record the operator, maintenance window, synthetic dataset and the target's CPU,
memory, disk space and network limits. Use UTC timestamps with explicit offsets.

The target must have its own data directory, database port, document buckets and service
configuration. Keep application services and outbound delivery stopped until validation.
Restrict network access so restored jobs cannot contact source databases, source buckets,
real mail recipients or integration consumers. Access to the backup repository is read-only
during the drill. Never run the restore commands on the source host.

Keep the original backup and its configuration available. Record identifiers and aggregate
results in the report; keep credentials, personal data, document keys and raw manifests in
restricted operator storage outside Git. Do not paste environment files into evidence.

## 2. PostgreSQL backup preparation

Install pgBackRest on the designated PostgreSQL host through the configured package source.
Use a repository on a separate failure domain, encryption, restricted repository credentials
and a secret-store copy of its cipher passphrase. A local directory alone is insufficient.
The following is a configuration outline, not a ready-to-use credential file:

```ini
[global]
repo1-path=/REPLACE_WITH_MOUNTED_OFF_HOST_REPOSITORY
repo1-retention-full=2
repo1-retention-diff=7
repo1-cipher-type=aes-256-cbc
repo1-cipher-pass=REPLACE_FROM_SECRET_STORE
process-max=2
log-level-console=info

[kapsora]
pg1-path=/var/lib/postgresql/18/main
pg1-port=5432
```

The operator must verify that the repository mount is present before backup; a missing
remote mount must not silently turn into a local backup. If using S3, configure and verify
the repository's S3 endpoint, bucket, region, TLS and credentials instead of this path.
Match source and repository-host pgBackRest versions. Restrict the configuration file to
the account that runs pgBackRest and its administrator.

Source PostgreSQL settings (an operator schedules the required restart):

```ini
wal_level = replica
archive_mode = on
archive_command = 'pgbackrest --stanza=kapsora archive-push %p'
archive_timeout = 60
```

```sh
sudo -u postgres pgbackrest --stanza=kapsora stanza-create
sudo -u postgres pgbackrest --stanza=kapsora check
sudo -u postgres pgbackrest --stanza=kapsora --type=full backup
sudo -u postgres pgbackrest --stanza=kapsora --output=json info
```

Schedule a weekly full and daily differential backup; monitor failures and retention.
WAL switching plus transfer and repository delay must fit the five-minute loss budget.
`archive_timeout` alone does not prove RPO. Record recoverable synthetic commit markers
and repository receipt times; monitor archive failures and free space. Restore also needs
the relevant WAL and all backups in the selected chain.

## 3. Document storage and keys

The current document pipeline uses logical buckets `quarantine` and `secure`, configured
by `KAPSORA_DOCUMENT_QUARANTINE_BUCKET` and `KAPSORA_DOCUMENT_SECURE_BUCKET`. Inventory
any additional deployment buckets separately. The old guide's `immutable`/`fiscal` pair
does not cover the current application documents.

Enable versioning and maintain a separately recoverable copy for both configured buckets.
Record version IDs, timestamps, deletion markers and copy lag in a restricted manifest.
Verify that retention covers the PostgreSQL recovery window. A latest-only mirror is not
proof that document bytes match a historical database checkpoint. Do not propagate a
source deletion into the only backup. Choose object versions consistent with the selected
database time and copy them into the target's independent buckets.

Use restored `document.object` metadata to reconcile required bytes: logical bucket,
object key, byte size and SHA-256. Resolve duplicate references; distinguish uncompleted
uploads, intentionally purged records and retained clean documents. Never promote
quarantined bytes merely to make a download work. Report missing objects and hash or
classification mismatches as failures. Counts alone do not prove byte consistency.

Escrow `KAPSORA_LOCAL_MASTER_KEY` securely: the current local/pilot provider derives both
field-encryption and blind-index keys from it. Generating a replacement does not decrypt
the restored fields. Also inventory database/object-store credentials, repository cipher
passphrase, cookie-signing key and any provider-side encryption keys. Record key references
and recoverability, never values. Rebind runtime endpoints to the isolated target.

## 4. Restore on the isolated target

1. Confirm the recorded source and target are different hosts. Confirm the target's data
   and tablespace directories are empty and dedicated to this drill. Do not use `--delta`
   to overwrite an existing installation. Keep the target's API, worker, scheduler and
   migration units stopped and temporarily masked during database recovery.
2. Install the matching application release without startup. Inspect its contents first:
   the current CI binary artifact is not yet a complete installer archive (see the
   [deployment findings](deploy-single-server.md#i10-03-preparation-findings)). Restore
   the recorded schema before considering any forward migration.
3. Create a target-only pgBackRest configuration pointing to the backup repository with
   read-only access. Remove source `pg1-host` connections, set the target `pg1-path` and
   port, and supply the original cipher passphrase through restricted configuration.
   Review source configuration symlinks and tablespaces before restoring.
4. Select the backup label and PITR timestamp explicitly. The following template requires
   those values and target paths to be filled by the operator:

```sh
sudo -u postgres pgbackrest --config=/etc/pgbackrest/kapsora-dr.conf \
  --stanza=kapsora --repo=1 --set=REPLACE_BACKUP_LABEL \
  --pg1-path=/srv/kapsora-dr/pgdata \
  --tablespace-map-all=/srv/kapsora-dr/tablespaces \
  --archive-mode=off --type=time --target="REPLACE_UTC_TIMESTAMP" \
  --target-action=pause restore
```

5. Before starting PostgreSQL, inspect restored recovery and external Ubuntu configuration:
   data directory and tablespace links stay on the target; listen address is local; port
   is dedicated; `archive_mode` remains off; no source replication connection or writable
   source archive command remains. Configure archive retrieval through the target's
   read-only pgBackRest configuration. Start only this isolated PostgreSQL instance using
   its recorded service/configuration, not a source-like default `postgresql` service.
6. Verify replay paused at the requested point. Record recovered synthetic markers,
   schema version, replay LSN and timestamps. If WAL is unavailable or the target was not
   reached, stop and report failure. Do not substitute a newer checkpoint silently.
7. Promote only the verified isolated cluster. Revoke restored sessions before enabling
   application access. With the operator's **target-only** administrative connection:

```sql
BEGIN;
UPDATE iam.session SET revoked_at = clock_timestamp() WHERE revoked_at IS NULL;
COMMIT;
SELECT count(*) AS unrevoked_sessions FROM iam.session WHERE revoked_at IS NULL;
```

Expect zero. Preserve the session rows and audit history. Do not truncate business,
idempotency or outbox records. Connect the restored release to the target database and
restored document buckets using recovered keys. Do not run `keygen` or seed over restored
data. Use a matching schema; hold the migration unit masked until an intentional upgrade.

## 5. Service validation and delayed jobs

On the named Ubuntu target, capture the return codes from:

```sh
systemd-analyze verify /etc/systemd/system/kapsora-api.service \
  /etc/systemd/system/kapsora-worker.service \
  /etc/systemd/system/kapsora-scheduler.service \
  /etc/systemd/system/kapsora-migrate.service
nginx -t
```

Fix errors before unmasking or starting the application. The installer currently ignores
unit-verification failure, so its exit code is not a substitute for this direct check.
Record target service names and exact startup commands in the drill evidence.

Start the API after PostgreSQL, both restored buckets and clamd are available. Verify
`/health/live` and `/health/ready` locally, including dependency failure/recovery. Test `/`,
`/portal/` and `/uye/` through the target proxy, secure cookies, restricted health access
and restart behavior. Readiness does not prove queue progress.

Keep the worker/scheduler stopped while reviewing counts and age by outbox status and
scan backlog. Events sent after the backup point can be replayed even when the source
already delivered them; application idempotency records alone do not prove external
exactly-once delivery. Reconcile receiver acknowledgements and deduplication keys. Then
start jobs only against the drill's test sinks, inspect retries/dead letters and prove no
duplicate financial effect. Do not publish restored notifications to real recipients.

Through ordinary tenant-scoped APIs, verify synthetic login/tenant selection, a positive
eligibility check, a clean document download with matching bytes, denial of a quarantined
download, hold/release with restored inventory/balances, and one financial reconciliation.
Include cross-tenant and own-file refusal checks. All unresolved mismatches remain findings.

## 6. Evidence and acceptance

Use this table in the work-package PR or operator's restricted drill report. Do not create
another permanent runbook for each attempt. Leave unmeasured values explicitly pending.

| Evidence | Required value |
|---|---|
| Operator, source/target and UTC window | Named and isolated |
| Application revision, schema and tool versions | Actual installed values |
| Backup label, WAL range and object manifest | Restricted evidence references |
| Incident/cutoff time | UTC timestamp chosen before recovery |
| Last recovered synthetic commit | Timestamp and non-personal marker |
| RPO | Cutoff minus last recovered commit, <=300 seconds; marker cadence bounds uncertainty |
| Recovery start | Incident declaration/start of recovery clock |
| Service acceptance time | After database, documents, keys, proxy and business checks pass |
| RTO | Acceptance minus recovery start, <=7200 seconds; include provisioning time |
| Document reconciliation | Required/missing/hash-mismatch counts and approved purge exclusions |
| Queues and financial reconciliation | Before/after totals, replay results and differences |
| Authorization and restored-user smoke | Test cases and results |
| Verdict | Pending, failed, or accepted with operator sign-off and evidence |

An empty workload, arbitrary last audit timestamp, successful backup command or HTTP 200
alone is not recovery acceptance. Run the drill quarterly and after material storage/key
changes. On completion, retain evidence and the recoverable backup; the operator decides
when to retire the isolated target.

## References

- [Frozen specification](../baseline-v1.2/KAPSORA_Teknik_Proje_Dokumani_v1.2.md), section 30.4.
- [pgBackRest guide](https://pgbackrest.org/user-guide.html), backup chains and isolated PITR.
- [PostgreSQL 18 continuous archiving](https://www.postgresql.org/docs/18/continuous-archiving.html), WAL recovery and configuration dependencies.
