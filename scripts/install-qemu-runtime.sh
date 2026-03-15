#!/usr/bin/env bash
set -euo pipefail

SCRIPT_NAME="install-qemu-runtime"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./install-runtime-common.sh
. "$SCRIPT_DIR/install-runtime-common.sh"

PROFILE="${PROFILE:-core}"
SKIP_IMAGE_BUILD="${SKIP_IMAGE_BUILD:-0}"

require_linux
require_apt
require_sudo
require_cmd go

install_packages ca-certificates curl jq qemu-system qemu-utils cloud-image-utils openssh-client cpu-checker
bootstrap_go_modules

qemu_binary="qemu-system-x86_64"
case "$(uname -m)" in
  aarch64|arm64) qemu_binary="qemu-system-aarch64" ;;
esac

if [ -e /dev/kvm ]; then
  add_user_to_group kvm
else
  warn "/dev/kvm is not present; QEMU can be installed, but the production-oriented Linux KVM path is not usable yet"
fi

if command -v kvm-ok >/dev/null 2>&1; then
  log "checking KVM availability"
  kvm-ok || warn "kvm-ok reported that KVM acceleration is not currently available"
fi

if [ "$SKIP_IMAGE_BUILD" != "1" ]; then
  log "building the default ${PROFILE} guest image"
  (
    cd "$REPO_ROOT"
    PROFILE="$PROFILE" QEMU_BINARY="$qemu_binary" QEMU_ACCEL="kvm" images/guest/build-base-image.sh
  )
fi

image_path="$REPO_ROOT/images/guest/or3-guest-${PROFILE}.qcow2"
if [ -f "$image_path" ]; then
  log "verifying repo QEMU config with the built image"
  (
    cd "$REPO_ROOT"
    SANDBOX_RUNTIME=qemu \
    SANDBOX_QEMU_BINARY="$qemu_binary" \
    SANDBOX_QEMU_ACCEL=kvm \
    SANDBOX_QEMU_BASE_IMAGE_PATH="$image_path" \
    go run ./cmd/sandboxctl config-lint
  )
else
  warn "guest image $image_path was not found; skipping qemu config-lint verification"
fi

printf '\n'
printf 'QEMU runtime install completed.\n'
printf 'Verify host tools with: %s --version && qemu-img --version && cloud-localds --help >/dev/null\n' "$qemu_binary"
if [ -f "$image_path" ]; then
  printf 'Built guest image: %s\n' "$image_path"
fi
printf 'Repo daemon command:\n'
printf '  SANDBOX_RUNTIME=qemu SANDBOX_QEMU_BINARY=%s SANDBOX_QEMU_ACCEL=kvm SANDBOX_QEMU_BASE_IMAGE_PATH=%s go run ./cmd/sandboxd -listen :8080 -db ./data/sandbox.db -storage-root ./data/storage -snapshot-root ./data/snapshots\n' "$qemu_binary" "$image_path"
print_group_membership_note