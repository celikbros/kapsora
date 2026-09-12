# WP-I1-06 · Native environment and ops: install/run scripts, systemd units, runbooks

> **Delivered in-house on 2026-09-03.** Scope adapted to ADR-022: there is no Keycloak and
> no JDK anywhere, so the scripts manage PostgreSQL (preinstalled), MinIO + mc, ClamAV and
> Mailpit only, and `keycloak-local.md` became [local-accounts.md](../runbooks/local-accounts.md).
> On Linux, ClamAV comes from the distribution packages rather than a download. Windows run
> verified on this machine; the Ubuntu VM run is still open (see the report).
> Report: [WP-I1-06-report.md](WP-I1-06-report.md).

| Field | Value |
|---|---|
| Milestone | M1 (plan increment I1) |
| Size | M |
| Depends on | none (WP-I1-01 supplies the Keycloak realm file; use a placeholder realm until it lands) |
| Runs in parallel with | all M1 packages |
| Migration numbers assigned | none |
| OpenAPI operations owned | none |
| Read first | Handbook; ADR-021; v1.2 sections 22.1, 30.1, 31, 33.4 (for the security intent, not the container mechanics) |

## 1. Goal

A developer on Windows or Linux can install and run every dependency as a native process
with one script, and an operator can deploy KAPSORA to a Linux server with systemd units
and a reverse proxy, without Docker or Kubernetes.

## 2. Scope

### 2.1 Local developer scripts (`scripts/native/`)

- `install.ps1` / `install.sh`: download pinned versions into `tools/` (git-ignored) with
  checksum verification: Keycloak 26.7.x (zip), MinIO server + `mc` client, ClamAV 1.4.x,
  Mailpit; verify JDK 21 is present (print install instructions for Eclipse Temurin 21 if
  not; do not install system-wide software silently). PostgreSQL is assumed installed;
  check version 18 and print guidance otherwise.
- `up.ps1` / `up.sh`: start Keycloak (`kc start-dev --http-port=8081 --import-realm` with
  `deploy/keycloak/`), MinIO (`:9000`, console `:9001`, root credentials from `.env`),
  create buckets `quarantine`, `secure`, `exports`, `immutable`, `fiscal` with `mc`,
  ClamAV (`clamd` on 3310 after `freshclam`), Mailpit (`:8025` UI, `:1025` SMTP); write
  logs and pid files under `tools/run/`; wait for each health endpoint; print a summary.
- `down.ps1` / `down.sh`: stop everything started by `up`.
- `status.ps1` / `status.sh`: health of PostgreSQL, Keycloak, MinIO, ClamAV, Mailpit, API.
- Keep scripts idempotent and readable; no admin rights required after JDK/PostgreSQL.

### 2.2 Production deployment (`deploy/`)

- `deploy/systemd/`: `kapsora-api.service`, `kapsora-worker.service`,
  `kapsora-scheduler.service`, `kapsora-migrate.service` (oneshot, runs before api),
  hardened: `DynamicUser=yes` or dedicated user, `ProtectSystem=strict`,
  `ProtectHome=yes`, `NoNewPrivileges=yes`, `PrivateTmp=yes`, `EnvironmentFile=/etc/kapsora/api.env`
  (0600, owned by root, read via `LoadCredential` where possible), restart policies,
  `LimitNOFILE`. Also unit templates for Keycloak, MinIO and clamd, and notes for
  PostgreSQL 18 from the distribution packages.
- `deploy/nginx/kapsora.conf`: TLS termination, HSTS, security headers (v1.2 19.2), proxy
  to api on 8080 with `X-Request-ID` passthrough, rate limit zone as a second line of
  defence, upload size limits, health endpoints not exposed publicly. Provide the Caddy
  equivalent as `deploy/caddy/Caddyfile`.
- `deploy/install.sh`: places binaries from the CI artefact (`kapsora-binaries-*`) under
  `/opt/kapsora/bin`, config under `/etc/kapsora`, enables units, runs migrate.
- `docs/runbooks/deployment-layout.md`: single-server reference layout, ports, firewall rules, backup hooks
  (pgBackRest and MinIO replication pointers), upgrade procedure (stop api → migrate →
  start), rollback = forward-fix (ADR-016).

### 2.3 Runbooks (`docs/runbooks/`)

- `local-native-environment.md`: rewrite the current stub into a complete guide
  (prerequisites, `install`, `.env`, `db-init`, `migrate-up`, `up`, verifying each service,
  common failures).
- `keycloak-local.md`: how the realm import works, admin console, resetting demo users.
- `deploy-single-server.md`: from a blank Ubuntu LTS server to a running system.
- `backup-restore.md`: PostgreSQL PITR with pgBackRest and MinIO versioning, restore drill
  checklist (v1.2 31.3).

## 3. Tests and verification

- Scripts pass `shellcheck` (bash) and `PSScriptAnalyzer` (PowerShell) with no errors.
- A fresh Windows 11 machine and a fresh Ubuntu 24.04 VM: `install` then `up` then
  `status` shows every service healthy; document the actual run with timings in the report.
- systemd units validated with `systemd-analyze verify`; nginx config with `nginx -t`.

## 4. Acceptance criteria

- [ ] One command brings up all native dependencies on both platforms; one stops them.
- [ ] Pinned versions and checksums recorded in a single `scripts/native/versions.json`.
- [ ] No script stores credentials outside `.env`; example values only.
- [ ] Deployment guide followed by the integrator on a clean VM works without asking
      questions (that is the bar).
- [ ] Runbooks written in Turkish for operators, scripts and comments in English.
