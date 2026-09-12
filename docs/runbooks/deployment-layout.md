# Deployment (single server, no containers)

Reference layout for one Linux server (Ubuntu 24.04 LTS or Debian 12); larger setups run
the same units on more hosts behind the same reverse proxy (ADR-021). Turkish operator
guide: [docs/runbooks/deploy-single-server.md](deploy-single-server.md).

## Layout

| Path | Owner / mode | Content |
|---|---|---|
| `/opt/kapsora/bin/` | root 0755 | `api`, `worker`, `scheduler`, `migrate`, `seed`, `keygen` (static binaries from the CI artefact) |
| `/opt/kapsora/web/{backoffice,provider,member}/` | root 0755 | `pnpm build` output, served by nginx/Caddy |
| `/etc/kapsora/api.env` | root:kapsora 0640 | runtime configuration, `KAPSORA_*` (app role) |
| `/etc/kapsora/migrate.env` | root:kapsora 0640 | owner-role URL for `kapsora-migrate` only |
| `/var/lib/kapsora/` | kapsora 0750 | state directory (currently unused; reserved for local spool) |
| `/var/lib/minio/` | minio | object store data when MinIO runs here |
| `/etc/systemd/system/kapsora-*.service` | root 0644 | units from `deploy/systemd/` |

## Ports and firewall

| Port | Process | Exposure |
|---|---|---|
| 443 / 80 | nginx or Caddy | public (80 only redirects) |
| 8080 | kapsora-api | localhost only |
| 5432 | PostgreSQL 18 | localhost (or private network to a managed instance) |
| 9000 / 9001 | MinIO S3 / console | localhost; console via SSH tunnel |
| 3310 | clamd | localhost |
| 587 | outgoing SMTP relay | egress to the customer's mail service |

`ufw default deny incoming; ufw allow 22/tcp; ufw allow 80,443/tcp`. The health endpoints
are answered only to 127.0.0.1 by the proxy configuration.

## Install and upgrade

```sh
sudo deploy/install.sh kapsora-binaries-<version>.tar.gz
```

First run: creates the `kapsora` user, directories, unit files and env templates, then
stops with a message until `CHANGE_ME` values are filled (`/opt/kapsora/bin/keygen` prints
keys). Every later run is an upgrade: stop api/worker/scheduler → run `kapsora-migrate`
(oneshot, owner role) → start. Migrations are forward-only (ADR-016); a bad release is
fixed forward, never rolled back at the schema level. Keep the previous archive to
redeploy binaries if needed.

## Reverse proxy

- nginx: `deploy/nginx/kapsora.conf` + `deploy/nginx/snippets/kapsora-proxy.conf`
  (TLS, HSTS, CSP, request-id passthrough, rate-limit zones, health not public).
  Validate with `nginx -t`.
- Caddy: `deploy/caddy/Caddyfile` (automatic TLS). Rate limiting needs the
  `caddy-ratelimit` module; without it the API's own limiter still applies.

## Dependencies

- PostgreSQL 18 from the PGDG repository (`postgresql-18`), or the customer's managed
  PostgreSQL. Roles: `kapsora_owner` (migrations), `kapsora_app` (runtime, NOBYPASSRLS);
  see `scripts/db-init.sql`.
- ClamAV from the distribution (`clamav-daemon`, `clamav-freshclam`), `TCPSocket 3310`.
- MinIO only when no S3-compatible store exists: `deploy/systemd/minio.service`, binary
  pinned in `scripts/native/versions.json`.
- Outgoing e-mail: the customer's SMTP relay (Mailpit is local development only).

## Backups

PostgreSQL PITR with pgBackRest, MinIO versioning + replication or `mc mirror` to a second
site; procedure and restore drill in
[docs/runbooks/backup-restore.md](backup-restore.md).

## Observability

Logs go to the journal (`journalctl -u kapsora-api`). JSON lines with `request_id`,
`tenant_id` where known, never personal data. Metrics/tracing stack (OTel Collector,
Prometheus, Grafana) arrives in I10 as native services.
