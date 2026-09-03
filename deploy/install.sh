#!/usr/bin/env bash
# Installs or upgrades KAPSORA on a Linux server from a CI artefact (kapsora-binaries-*).
# Layout: /opt/kapsora/bin (binaries), /opt/kapsora/web (static apps), /etc/kapsora (env),
# /var/lib/kapsora (state). Run as root. Idempotent; re-run for every release.
#
#   sudo deploy/install.sh /path/to/kapsora-binaries-<version>.tar.gz [--no-restart]
#
# Upgrade order (ADR-016, forward-only): stop api/worker/scheduler → migrate → start.
set -euo pipefail

ARCHIVE="${1:-}"
NO_RESTART=0
[[ "${2:-}" == "--no-restart" ]] && NO_RESTART=1
[[ -n "$ARCHIVE" && -f "$ARCHIVE" ]] || { echo "usage: $0 <kapsora-binaries.tar.gz> [--no-restart]" >&2; exit 1; }
[[ "$(id -u)" -eq 0 ]] || { echo "run as root" >&2; exit 1; }

HERE="$(cd "$(dirname "$0")" && pwd)"
PREFIX=/opt/kapsora
ETC=/etc/kapsora
STATE=/var/lib/kapsora
UNITS=(kapsora-migrate kapsora-api kapsora-worker kapsora-scheduler)

echo "==> user and directories"
id -u kapsora >/dev/null 2>&1 || useradd --system --home-dir "$STATE" --shell /usr/sbin/nologin kapsora
install -d -m 0755 "$PREFIX" "$PREFIX/bin" "$PREFIX/web"
install -d -m 0750 -o root -g kapsora "$ETC"
install -d -m 0750 -o kapsora -g kapsora "$STATE"

echo "==> binaries from $ARCHIVE"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
tar -xzf "$ARCHIVE" -C "$STAGE"
for bin in api worker scheduler migrate seed keygen; do
  src="$(find "$STAGE" -type f -name "$bin" | head -n 1)"
  [[ -n "$src" ]] || { echo "archive lacks $bin" >&2; exit 1; }
  install -m 0755 -o root -g root "$src" "$PREFIX/bin/$bin.new"
  mv -f "$PREFIX/bin/$bin.new" "$PREFIX/bin/$bin"
done
if [[ -d "$STAGE/web" ]]; then
  echo "==> static web apps"
  for app in backoffice provider member; do
    [[ -d "$STAGE/web/$app" ]] || continue
    rm -rf "$PREFIX/web/$app.new"
    cp -r "$STAGE/web/$app" "$PREFIX/web/$app.new"
    rm -rf "$PREFIX/web/$app"
    mv "$PREFIX/web/$app.new" "$PREFIX/web/$app"
  done
fi

echo "==> configuration"
if [[ ! -f "$ETC/api.env" ]]; then
  cat >"$ETC/api.env" <<'EOF'
# KAPSORA runtime configuration (read by the systemd units). Keep 0640 root:kapsora.
KAPSORA_ENV=production
KAPSORA_LOG_LEVEL=info
KAPSORA_HTTP_ADDR=127.0.0.1:8080
KAPSORA_DB_MAX_CONNS=20
KAPSORA_DATABASE_URL=postgres://kapsora_app:CHANGE_ME@127.0.0.1:5432/kapsora?sslmode=disable
KAPSORA_LOCAL_MASTER_KEY=CHANGE_ME_run_keygen
KAPSORA_COOKIE_SIGNING_KEY=CHANGE_ME_run_keygen
KAPSORA_COOKIE_SECURE=true
KAPSORA_SESSION_IDLE_MINUTES=30
KAPSORA_SESSION_ABSOLUTE_HOURS=8
KAPSORA_STEP_UP_MINUTES=10
EOF
  chmod 0640 "$ETC/api.env"; chown root:kapsora "$ETC/api.env"
  echo "    wrote $ETC/api.env — fill the CHANGE_ME values ($PREFIX/bin/keygen prints keys)"
fi
if [[ ! -f "$ETC/migrate.env" ]]; then
  cat >"$ETC/migrate.env" <<'EOF'
# Owner role for migrations only (kapsora-migrate.service). Keep 0640 root:kapsora.
KAPSORA_DATABASE_URL=postgres://kapsora_owner:CHANGE_ME@127.0.0.1:5432/kapsora?sslmode=disable
EOF
  chmod 0640 "$ETC/migrate.env"; chown root:kapsora "$ETC/migrate.env"
  echo "    wrote $ETC/migrate.env — fill the CHANGE_ME value"
fi

echo "==> systemd units"
for u in "${UNITS[@]}"; do
  install -m 0644 "$HERE/systemd/$u.service" "/etc/systemd/system/$u.service"
done
systemctl daemon-reload
systemd-analyze verify /etc/systemd/system/kapsora-*.service || true

if grep -q CHANGE_ME "$ETC/api.env" "$ETC/migrate.env"; then
  echo "!! $ETC/*.env still contain CHANGE_ME; finish the configuration, then:"
  echo "   systemctl enable --now kapsora-migrate kapsora-api kapsora-worker kapsora-scheduler"
  exit 0
fi

if [[ $NO_RESTART -eq 1 ]]; then
  echo "==> installed; services not restarted (--no-restart)"
  exit 0
fi

echo "==> upgrade: stop → migrate → start"
systemctl stop kapsora-api kapsora-worker kapsora-scheduler 2>/dev/null || true
systemctl start kapsora-migrate
systemctl enable --now kapsora-api kapsora-worker kapsora-scheduler
sleep 2
if curl -fsS http://127.0.0.1:8080/health/ready >/dev/null; then
  echo "==> KAPSORA API ready"
else
  echo "!! API not ready; journalctl -u kapsora-api -n 100" >&2
  exit 1
fi
