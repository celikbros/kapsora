# WP-I1-06 delivery report · Native environment and ops

Delivered in-house on 2026-09-03. Format follows `REPORT_TEMPLATE.md`.

## 1. Summary

Developers get one command per platform to install and one to start every native
dependency; operators get hardened systemd units, a reverse-proxy configuration for nginx
or Caddy, an install/upgrade script and Turkish runbooks. Scope was adapted to ADR-022:
no Keycloak, no JDK, so the dependency set is PostgreSQL 18 (preinstalled), MinIO + `mc`,
ClamAV and Mailpit.

## 2. What is where

| Path | Content |
|---|---|
| `scripts/native/versions.json` | Pinned versions, URLs and SHA-256 for MinIO `RELEASE.2025-09-07T16-13-09Z`, mc `RELEASE.2025-08-13T08-35-41Z`, Mailpit `v1.31.0`, ClamAV `1.5.4` (Windows zip); Linux ClamAV from distribution packages; PostgreSQL 18 preinstalled |
| `scripts/native/install.{ps1,sh}` | Download into `tools/downloads`, verify checksum (refuse on mismatch), place binaries under `tools/bin`, extract ClamAV under `tools/clamav`, generate `clamd.conf`/`freshclam.conf`, check Go and PostgreSQL versions; no admin rights, nothing system-wide |
| `scripts/native/up.{ps1,sh}` | Start MinIO (data `tools/data/minio`, console 9001) and create `quarantine`, `secure`, `exports`, `immutable`, `fiscal` (versioning on the last two), run freshclam when the database is missing or older than a day, start clamd on 127.0.0.1:3310, start Mailpit (UI 8025, SMTP 1025); pid and log files under `tools/run`; health waits; summary |
| `scripts/native/down.{ps1,sh}`, `status.{ps1,sh}` | Stop what `up` started; health table for PostgreSQL, MinIO, ClamAV (`PING`/`PONG`), Mailpit, API |
| `scripts/native/_common.{ps1,sh}` | Shared helpers: `.env` loading (values never printed), pid handling, TCP/HTTP/clamd probes |
| `scripts/PSScriptAnalyzerSettings.psd1` | Analyzer settings (Write-Host allowed for CLI output) |
| `deploy/systemd/*.service` + `README.md` | `kapsora-migrate` (oneshot), `kapsora-api`, `kapsora-worker`, `kapsora-scheduler`, `minio`; dedicated user, `ProtectSystem=strict`, `ProtectHome`, `NoNewPrivileges`, `PrivateTmp`, empty capabilities, syscall filter, `UMask=0077`, `LimitNOFILE=65536`, `EnvironmentFile=/etc/kapsora/api.env` with a `LoadCredential` example |
| `deploy/nginx/kapsora.conf` + `snippets/kapsora-proxy.conf` | TLS 1.2/1.3, HSTS, CSP (no inline scripts), frame/referrer/permissions headers, `X-Request-ID` passthrough, rate-limit zones (API 30 r/s, login 5 r/min), 25 MB body limit, health endpoints localhost-only, SPA + immutable assets caching |
| `deploy/caddy/Caddyfile` | Same policy for Caddy with automatic TLS |
| `deploy/install.sh` | Creates the `kapsora` user and layout, installs binaries and web builds from the CI artefact, writes env templates on first run, installs units, `systemd-analyze verify`, upgrade order stop → migrate → start, readiness check |
| `docs/runbooks/deployment-layout.md` | Layout, ports and firewall, install/upgrade, dependencies, backups |
| `docs/runbooks/local-native-environment.md` | Rewritten complete guide (Turkish) |
| `docs/runbooks/local-accounts.md` | Replaces `keycloak-local.md`: keys, `seed demo`, single accounts, unlock |
| `docs/runbooks/deploy-single-server.md` | Blank Ubuntu 24.04 to running system |
| `docs/runbooks/backup-restore.md` | pgBackRest PITR, MinIO versioning/replication, key backup, quarterly drill checklist |
| `.env.example`, `Makefile` (`native-*`), `scripts/dev.ps1` | Local service addresses and credentials (examples only), targets |

## 3. Verification

| Check | Result |
|---|---|
| `shellcheck -x -P SCRIPTDIR scripts/native/*.sh deploy/install.sh` (v0.11.0) | 0 findings |
| `bash -n` on every `.sh` | ok |
| `Invoke-ScriptAnalyzer -Path scripts -Recurse -Settings scripts/PSScriptAnalyzerSettings.psd1` (Windows PowerShell 5.1) | 0 errors, 0 warnings after renaming helpers to approved verbs |
| Windows 11, this machine: `install.ps1` | 11 s with the downloads already cached (first download ≈ 480 MB: MinIO 113 MB, mc 31 MB, Mailpit 11 MB, ClamAV 226 MB); every checksum verified, MinIO/mc checksums also match the publisher's `.sha256sum` files |
| Windows: first `up.ps1` | 227 s, of which freshclam ≈ 200 s (main/daily/bytecode ≈ 113 MB); MinIO and buckets ok |
| Windows: `up.ps1` after fixes | 16 s; PostgreSQL, MinIO, ClamAV (`PONG`), Mailpit all UP; buckets listed with `mc ls`, versioning confirmed on `fiscal` |
| Windows: second `up.ps1` | idempotent ("already running" for all three) |
| Windows: `down.ps1` then `status.ps1` | three services DOWN, PostgreSQL still UP, pid files removed |
| Ubuntu 24.04 VM: `install.sh` → `up.sh` → `status.sh` | **not run** — no Linux VM on this machine. The bash scripts are shellcheck-clean and mirror the PowerShell logic; the first real run is scheduled for the pilot server setup and its timings will be added here. |
| `systemd-analyze verify`, `nginx -t` | **not run** for the same reason; `deploy/install.sh` runs `systemd-analyze verify` on the target host. |

Two defects found and fixed during the Windows run: `Start-Process` needs quoted
arguments when the repository path contains a space (this one does), and in PowerShell
the comma binds tighter than `+`, which had merged two clamd arguments into one.

## 4. Acceptance criteria

- [x] One command brings up all native dependencies, one stops them — verified on Windows; Linux scripts written and linted, run pending (see 3).
- [x] Pinned versions and checksums in `scripts/native/versions.json`.
- [x] No script stores credentials outside `.env`; `.env.example` carries example values only; `.env` is never printed.
- [ ] Deployment guide followed on a clean VM — open until the pilot server exists.
- [x] Runbooks in Turkish for operators; scripts and comments in English.

## 5. Deviations

- Keycloak/JDK removed from scope (ADR-022). `keycloak-local.md` → `local-accounts.md`.
- ClamAV on Linux from distribution packages instead of a pinned download: the packages
  carry the signature updater, the `clamav` user and the systemd unit, which a manual
  install would have to recreate.
- MinIO community binaries stopped being published after 2025-10; the pinned release is
  the last one with published checksums. The object-store decision (customer S3 vs MinIO
  vs another self-hosted store) is due in I5 with the document module.
- The Windows `up` skips `freshclam` when the database is younger than a day; `-SkipFreshclam`
  forces the skip for offline work.
