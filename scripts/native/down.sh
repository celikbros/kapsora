#!/usr/bin/env bash
# Stops everything started by up.sh (Mailpit, clamd when started here, MinIO).
# PostgreSQL and a packaged clamav-daemon are system services and are left running.

# shellcheck source=_common.sh
. "$(dirname "$0")/_common.sh"

step "stopping native services"
for name in mailpit clamd minio; do
  stop_background "$name"
done
ok "done (data kept under tools/data)"
