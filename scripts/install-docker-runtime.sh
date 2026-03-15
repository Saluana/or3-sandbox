#!/usr/bin/env bash
set -euo pipefail

SCRIPT_NAME="install-docker-runtime"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./install-runtime-common.sh
. "$SCRIPT_DIR/install-runtime-common.sh"

require_linux
require_apt
require_sudo

install_packages ca-certificates curl jq docker.io
ensure_service_started docker
add_user_to_group docker
bootstrap_go_modules

log "verifying Docker daemon reachability"
run_sudo docker version >/dev/null

printf '\n'
printf 'Docker runtime install completed.\n'
printf 'Verify with: sudo docker version\n'
printf 'Local dev daemon command:\n'
printf '  SANDBOX_ENABLED_RUNTIMES=docker-dev SANDBOX_DEFAULT_RUNTIME=docker-dev SANDBOX_TRUSTED_DOCKER_RUNTIME=true go run ./cmd/sandboxd -listen :8080 -db ./data/sandbox.db -storage-root ./data/storage -snapshot-root ./data/snapshots\n'
print_group_membership_note