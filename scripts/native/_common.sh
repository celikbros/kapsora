#!/usr/bin/env bash
# Shared helpers for the native environment scripts (bash 4+, Linux/macOS/Git Bash).
# Source from install/up/down/status: . "$(dirname "$0")/_common.sh"
# shellcheck disable=SC2034  # variables are consumed by the sourcing scripts

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TOOLS_DIR="$REPO_ROOT/tools"
BIN_DIR="$TOOLS_DIR/bin"
DATA_DIR="$TOOLS_DIR/data"
RUN_DIR="$TOOLS_DIR/run"
DOWNLOAD_DIR="$TOOLS_DIR/downloads"
VERSIONS_FILE="$SCRIPT_DIR/versions.json"

step() { printf '\033[36m==> %s\033[0m\n' "$*"; }
ok() { printf '\033[32m    ok  %s\033[0m\n' "$*"; }
warn() { printf '\033[33m    !!  %s\033[0m\n' "$*"; }
fail() { printf '\033[31m    XX  %s\033[0m\n' "$*"; }

import_dotenv() {
  # Loads KEY=VALUE lines from .env; never prints values.
  local env_file="$REPO_ROOT/.env"
  if [[ ! -f "$env_file" ]]; then
    warn ".env not found; copy .env.example to .env first"
    return 0
  fi
  local line key value
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ "$line" =~ ^[[:space:]]*# ]] && continue
    [[ "$line" =~ ^[[:space:]]*$ ]] && continue
    key="${line%%=*}"
    value="${line#*=}"
    key="$(echo "$key" | tr -d '[:space:]')"
    export "$key=$value"
  done <"$env_file"
}

env_or() { # env_or NAME DEFAULT
  local v="${!1:-}"
  if [[ -z "$v" ]]; then echo "$2"; else echo "$v"; fi
}

json_get() { # json_get '<jq-like dotted path>' e.g. minio.linux-amd64.url
  python3 - "$VERSIONS_FILE" "$1" <<'PY'
import json, sys
doc = json.load(open(sys.argv[1]))
node = doc
for part in sys.argv[2].split('.'):
    node = node[part]
print(node if not isinstance(node, (list, dict)) else json.dumps(node))
PY
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

pid_file() { echo "$RUN_DIR/$1.pid"; }

running_pid() { # prints the pid if alive, nothing otherwise
  local f
  f="$(pid_file "$1")"
  [[ -f "$f" ]] || return 0
  local pid
  pid="$(tr -d '[:space:]' <"$f")"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then echo "$pid"; fi
}

tcp_up() { # tcp_up HOST PORT
  (exec 3<>"/dev/tcp/$1/$2") 2>/dev/null && { exec 3>&- 3<&-; return 0; }
  return 1
}

http_up() { curl -fsS --max-time 3 -o /dev/null "$1" 2>/dev/null; }

clamd_ping() { # clamd_ping HOST PORT
  local reply
  reply="$( (printf 'zPING\0'; sleep 1) | timeout 5 bash -c "exec 3<>/dev/tcp/$1/$2; cat >&3 & head -c 16 <&3" 2>/dev/null || true)"
  [[ "$reply" == *PONG* ]]
}

wait_until() { # wait_until "<command>" "what" [timeout]
  local cmd="$1" what="$2" timeout="${3:-60}" i=0
  while (( i < timeout )); do
    if eval "$cmd"; then ok "$what is up"; return 0; fi
    sleep 1; i=$((i + 1))
  done
  fail "$what did not become healthy within ${timeout}s (see tools/run/*.log)"
  return 1
}

start_background() { # start_background NAME CMD [ARGS...]  (env vars set by the caller)
  local name="$1"; shift
  mkdir -p "$RUN_DIR"
  local pid
  pid="$(running_pid "$name")"
  if [[ -n "$pid" ]]; then ok "$name already running (pid $pid)"; return 0; fi
  nohup "$@" >"$RUN_DIR/$name.log" 2>&1 &
  echo $! >"$(pid_file "$name")"
  ok "$name started (pid $!, log tools/run/$name.log)"
}

stop_background() {
  local name="$1" pid
  pid="$(running_pid "$name")"
  if [[ -n "$pid" ]]; then
    kill "$pid" 2>/dev/null || true
    for _ in 1 2 3 4 5; do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
    kill -9 "$pid" 2>/dev/null || true
    ok "$name stopped (pid $pid)"
  else
    ok "$name not running"
  fi
  rm -f "$(pid_file "$name")"
}
