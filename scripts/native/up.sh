#!/usr/bin/env bash
# Starts the native dependencies for local development: MinIO (+ buckets), ClamAV clamd
# (after freshclam), Mailpit. PostgreSQL is expected to be running as a system service.
# Idempotent; credentials come from .env only. Usage: scripts/native/up.sh [--skip-freshclam]

# shellcheck source=_common.sh
. "$(dirname "$0")/_common.sh"
import_dotenv

SKIP_FRESHCLAM=0
[[ "${1:-}" == "--skip-freshclam" ]] && SKIP_FRESHCLAM=1

MINIO_ADDR="$(env_or KAPSORA_MINIO_ADDR 127.0.0.1:9000)"
MINIO_CONSOLE="$(env_or KAPSORA_MINIO_CONSOLE_ADDR 127.0.0.1:9001)"
MINIO_USER="$(env_or KAPSORA_MINIO_ROOT_USER kapsora)"
MINIO_PASS="$(env_or KAPSORA_MINIO_ROOT_PASSWORD kapsora_local_minio)"
CLAM_ADDR="$(env_or KAPSORA_CLAMAV_ADDR 127.0.0.1:3310)"
MAIL_UI="$(env_or KAPSORA_MAILPIT_UI_ADDR 127.0.0.1:8025)"
SMTP_ADDR="$(env_or KAPSORA_SMTP_ADDR 127.0.0.1:1025)"
BUCKETS=(quarantine secure exports immutable fiscal)

for exe in minio mc mailpit; do
  [[ -x "$BIN_DIR/$exe" ]] || { fail "tools/bin/$exe missing; run scripts/native/install.sh first"; exit 1; }
done
mkdir -p "$RUN_DIR" "$DATA_DIR/minio" "$DATA_DIR/mailpit" "$DATA_DIR/clamav-db" "$DATA_DIR/mc"

step "PostgreSQL"
PG_HOST=127.0.0.1; PG_PORT=5432
if [[ "${KAPSORA_DATABASE_URL:-}" =~ @([^:/]+):([0-9]+)/ ]]; then PG_HOST="${BASH_REMATCH[1]}"; PG_PORT="${BASH_REMATCH[2]}"; fi
if tcp_up "$PG_HOST" "$PG_PORT"; then ok "PostgreSQL listening on $PG_HOST:$PG_PORT"; else warn "PostgreSQL not reachable on $PG_HOST:$PG_PORT; sudo systemctl start postgresql"; fi

step "MinIO"
MINIO_ROOT_USER="$MINIO_USER" MINIO_ROOT_PASSWORD="$MINIO_PASS" MINIO_BROWSER_REDIRECT=false \
  start_background minio "$BIN_DIR/minio" server "$DATA_DIR/minio" --address "$MINIO_ADDR" --console-address "$MINIO_CONSOLE"
if wait_until "http_up http://$MINIO_ADDR/minio/health/live" MinIO 60; then
  export MC_CONFIG_DIR="$DATA_DIR/mc"
  "$BIN_DIR/mc" alias set kapsora-local "http://$MINIO_ADDR" "$MINIO_USER" "$MINIO_PASS" --api S3v4 >/dev/null 2>&1
  for b in "${BUCKETS[@]}"; do "$BIN_DIR/mc" mb --ignore-existing "kapsora-local/$b" >/dev/null 2>&1; done
  "$BIN_DIR/mc" version enable kapsora-local/immutable >/dev/null 2>&1 || true
  "$BIN_DIR/mc" version enable kapsora-local/fiscal >/dev/null 2>&1 || true
  ok "buckets: ${BUCKETS[*]} (versioning on immutable, fiscal)"
fi

step "ClamAV"
CLAM_HOST="${CLAM_ADDR%%:*}"; CLAM_PORT="${CLAM_ADDR##*:}"
if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files clamav-daemon.service >/dev/null 2>&1 && systemctl list-unit-files clamav-daemon.service | grep -q clamav-daemon; then
  # Distribution package: the daemon and freshclam are systemd services (Debian/Ubuntu names).
  if systemctl is-active --quiet clamav-daemon; then ok "clamav-daemon active (system service)"
  else warn "clamav-daemon inactive; sudo systemctl enable --now clamav-freshclam clamav-daemon (first signature download takes minutes). Make sure /etc/clamav/clamd.conf has 'TCPSocket 3310' and 'TCPAddr 127.0.0.1'."; fi
elif command -v clamd >/dev/null 2>&1; then
  CLAM_CONF="$TOOLS_DIR/clamav/clamd.conf"; FRESH_CONF="$TOOLS_DIR/clamav/freshclam.conf"
  mkdir -p "$TOOLS_DIR/clamav"
  [[ -f "$CLAM_CONF" ]] || printf 'DatabaseDirectory %s\nLogFile %s\nTCPSocket %s\nTCPAddr %s\nForeground yes\n' "$DATA_DIR/clamav-db" "$RUN_DIR/clamd.log" "$CLAM_PORT" "$CLAM_HOST" >"$CLAM_CONF"
  [[ -f "$FRESH_CONF" ]] || printf 'DatabaseDirectory %s\nUpdateLogFile %s\nDatabaseMirror database.clamav.net\n' "$DATA_DIR/clamav-db" "$RUN_DIR/freshclam.log" >"$FRESH_CONF"
  if [[ $SKIP_FRESHCLAM -eq 0 ]] && ! find "$DATA_DIR/clamav-db" -name 'main.c*' -mtime -1 | grep -q .; then
    step "freshclam: updating signatures (first run downloads ~300 MB)"
    freshclam --config-file="$FRESH_CONF" 2>&1 | tail -n 3 || true
  fi
  start_background clamd clamd --config-file="$CLAM_CONF"
  wait_until "clamd_ping $CLAM_HOST $CLAM_PORT" clamd 180 || true
else
  warn "ClamAV not installed (see install.sh); skipping"
fi

step "Mailpit"
start_background mailpit "$BIN_DIR/mailpit" --listen "$MAIL_UI" --smtp "$SMTP_ADDR" --database "$DATA_DIR/mailpit/mailpit.db"
wait_until "http_up http://$MAIL_UI/livez" Mailpit 30 || true

step "summary"
printf '  MinIO      S3 http://%s   console http://%s   user %s\n' "$MINIO_ADDR" "$MINIO_CONSOLE" "$MINIO_USER"
printf '  ClamAV     clamd tcp://%s\n' "$CLAM_ADDR"
printf '  Mailpit    UI http://%s   SMTP %s\n' "$MAIL_UI" "$SMTP_ADDR"
printf '  PostgreSQL %s:%s\n' "$PG_HOST" "$PG_PORT"
echo "  Logs and pid files: tools/run/   Stop everything: scripts/native/down.sh"
