# systemd units

| Unit | Role |
|---|---|
| `kapsora-migrate.service` | oneshot; applies migrations with the owner role before the API starts |
| `kapsora-api.service` | HTTP API on 127.0.0.1:8080 behind nginx or Caddy |
| `kapsora-worker.service` | outbox dispatcher; several instances share work with SKIP LOCKED |
| `kapsora-scheduler.service` | periodic jobs; one leader via PostgreSQL advisory lock |
| `minio.service` | object store when the customer has no S3-compatible service |

PostgreSQL and ClamAV come from the distribution packages and bring their own units
(`postgresql@18-main`, `clamav-daemon`, `clamav-freshclam` on Debian/Ubuntu). Set
`TCPSocket 3310` and `TCPAddr 127.0.0.1` in `/etc/clamav/clamd.conf`.

Validate after copying: `systemd-analyze verify /etc/systemd/system/kapsora-*.service`.

Hardening in every KAPSORA unit: dedicated `kapsora` user, `ProtectSystem=strict`,
`ProtectHome`, `NoNewPrivileges`, `PrivateTmp`, empty capability set, syscall filter,
`UMask=0077`, `LimitNOFILE=65536`. Only `/var/lib/kapsora` is writable. Environment comes
from `/etc/kapsora/api.env` (0640 root:kapsora); the `LoadCredential=` comment shows how to
hand over secrets as files instead of environment variables.
