#!/usr/bin/env bash
# Health of PostgreSQL, MinIO, ClamAV, Mailpit and the KAPSORA API. Exit 1 when a required
# service (PostgreSQL) is down; the others are optional for the current modules.

# shellcheck source=_common.sh
. "$(dirname "$0")/_common.sh"
import_dotenv

MINIO_ADDR="$(env_or KAPSORA_MINIO_ADDR 127.0.0.1:9000)"
CLAM_ADDR="$(env_or KAPSORA_CLAMAV_ADDR 127.0.0.1:3310)"
MAIL_UI="$(env_or KAPSORA_MAILPIT_UI_ADDR 127.0.0.1:8025)"
API_ADDR="$(env_or KAPSORA_HTTP_ADDR :8080)"
[[ "$API_ADDR" == :* ]] && API_ADDR="127.0.0.1$API_ADDR"
PG_HOST=127.0.0.1; PG_PORT=5432
if [[ "${KAPSORA_DATABASE_URL:-}" =~ @([^:/]+):([0-9]+)/ ]]; then PG_HOST="${BASH_REMATCH[1]}"; PG_PORT="${BASH_REMATCH[2]}"; fi

failed=0
row() { # row NAME REQUIRED(0/1) WHERE UP(0/1)
  local color state
  if [[ "$4" -eq 1 ]]; then state=UP; color='\033[32m'
  elif [[ "$2" -eq 1 ]]; then state=DOWN; color='\033[31m'; failed=1
  else state=DOWN; color='\033[33m'; fi
  printf "  ${color}%-12s %-5s %s\033[0m\n" "$1" "$state" "$3"
}

up=0; tcp_up "$PG_HOST" "$PG_PORT" && up=1; row PostgreSQL 1 "$PG_HOST:$PG_PORT" $up
up=0; http_up "http://$MINIO_ADDR/minio/health/live" && up=1; row MinIO 0 "http://$MINIO_ADDR" $up
up=0; clamd_ping "${CLAM_ADDR%%:*}" "${CLAM_ADDR##*:}" && up=1; row ClamAV 0 "tcp://$CLAM_ADDR" $up
up=0; http_up "http://$MAIL_UI/livez" && up=1; row Mailpit 0 "http://$MAIL_UI" $up
up=0; http_up "http://$API_ADDR/health/ready" && up=1; row "KAPSORA API" 0 "http://$API_ADDR" $up

for name in minio clamd mailpit; do
  pid="$(running_pid "$name")"
  [[ -n "$pid" ]] && printf '  \033[90m%-12s pid %s\033[0m\n' "$name" "$pid"
done
exit $failed
