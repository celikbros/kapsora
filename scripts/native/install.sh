#!/usr/bin/env bash
# Downloads the pinned native dependencies (MinIO, mc, Mailpit) into tools/ with SHA-256
# verification and checks PostgreSQL 18, Go and the ClamAV packages. No root needed except
# for the distribution packages, which are only reported, never installed silently.
# Usage: scripts/native/install.sh [--force]

# shellcheck source=_common.sh
. "$(dirname "$0")/_common.sh"

FORCE=0
[[ "${1:-}" == "--force" ]] && FORCE=1

ARCH="linux-amd64"
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) ARCH="linux-amd64" ;;
  *) warn "only linux-amd64 binaries are pinned; $(uname -s)-$(uname -m) needs manual installation" ;;
esac

mkdir -p "$BIN_DIR" "$DOWNLOAD_DIR" "$DATA_DIR" "$RUN_DIR"

verified_download() { # verified_download NAME KEY -> prints local file path
  local name="$1" key="$2" url sha file actual
  url="$(json_get "$key.url")"
  sha="$(json_get "$key.sha256")"
  file="$DOWNLOAD_DIR/${url##*/}"
  if [[ -f "$file" && $FORCE -eq 0 ]] && [[ "$(sha256_of "$file")" == "$sha" ]]; then
    ok "$name download already verified" >&2
    echo "$file"; return 0
  fi
  step "downloading $name $url" >&2
  curl -fsSL --retry 3 -o "$file.part" "$url"
  actual="$(sha256_of "$file.part")"
  if [[ "$actual" != "$sha" ]]; then
    rm -f "$file.part"
    fail "$name checksum mismatch: expected $sha, got $actual. Refusing to install." >&2
    exit 1
  fi
  mv -f "$file.part" "$file"
  ok "$name checksum verified" >&2
  echo "$file"
}

install_binary() { # install_binary NAME KEY
  local file target
  file="$(verified_download "$1" "$2")"
  target="$TOOLS_DIR/$(json_get "$2.target")"
  cp -f "$file" "$target"
  chmod 0755 "$target"
  ok "$1 -> tools/${target#"$TOOLS_DIR"/}"
}

install_tar_member() { # install_tar_member NAME KEY
  local file target member stage
  file="$(verified_download "$1" "$2")"
  target="$TOOLS_DIR/$(json_get "$2.target")"
  member="$(json_get "$2.member")"
  stage="$DOWNLOAD_DIR/$1-extract"
  rm -rf "$stage"; mkdir -p "$stage"
  tar -xzf "$file" -C "$stage"
  local found
  found="$(find "$stage" -type f -name "$member" | head -n 1)"
  [[ -n "$found" ]] || { fail "$1 archive does not contain $member"; exit 1; }
  cp -f "$found" "$target"
  chmod 0755 "$target"
  rm -rf "$stage"
  ok "$1 -> tools/${target#"$TOOLS_DIR"/}"
}

step "checking prerequisites"
if command -v go >/dev/null 2>&1; then ok "$(go version)"; else warn "Go not found on PATH: https://go.dev/dl/"; fi
PG_WANT="$(json_get postgresql.version)"
if command -v psql >/dev/null 2>&1; then
  PG_VER="$(psql --version | sed -E 's/.* ([0-9]+)\..*/\1/')"
  if [[ "$PG_VER" -ge "$PG_WANT" ]]; then ok "$(psql --version)"; else warn "PostgreSQL $PG_VER found; KAPSORA needs major version $PG_WANT"; fi
else
  warn "PostgreSQL $PG_WANT not found. Debian/Ubuntu: https://www.postgresql.org/download/linux/ubuntu/ (package postgresql-$PG_WANT)"
fi
if command -v clamd >/dev/null 2>&1 || [[ -x /usr/sbin/clamd ]]; then
  ok "ClamAV: $(clamd --version 2>/dev/null || /usr/sbin/clamd --version)"
else
  warn "ClamAV not installed. Debian/Ubuntu: sudo apt install $(json_get clamav.linux.packages | tr -d '[]",') ; Fedora/RHEL: sudo dnf install clamav clamd clamav-update"
fi

install_binary minio "minio.$ARCH"
install_binary mc "mc.$ARCH"
install_tar_member mailpit "mailpit.$ARCH"

step "done"
echo "Next: cp .env.example .env (fill CHANGE_ME values), then scripts/native/up.sh"
