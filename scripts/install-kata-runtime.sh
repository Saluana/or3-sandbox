#!/usr/bin/env bash
set -euo pipefail

SCRIPT_NAME="install-kata-runtime"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./install-runtime-common.sh
. "$SCRIPT_DIR/install-runtime-common.sh"

require_linux
require_apt
require_sudo
require_glibc_at_least 2.34

require_cmd curl
install_packages ca-certificates curl jq zstd containerd
ensure_service_started containerd

arch="$(host_arch)"
release_json="$(mktemp)"
trap 'rm -f "$release_json"' EXIT

log "resolving latest Kata Containers release"
curl -fsSL https://api.github.com/repos/kata-containers/kata-containers/releases/latest -o "$release_json"
version="$(jq -r '.tag_name' "$release_json")"
asset_name="kata-static-${version}-${arch}.tar.zst"
asset_url="$(jq -r --arg name "$asset_name" '.assets[] | select(.name == $name) | .browser_download_url' "$release_json")"

[ -n "$asset_url" ] && [ "$asset_url" != "null" ] || fail "could not find a Kata static archive for ${arch} in release ${version}"

archive_path="/tmp/${asset_name}"
log "downloading $asset_name"
curl -fL "$asset_url" -o "$archive_path"

log "extracting Kata runtime artifacts into /"
run_sudo tar --zstd -C / -xf "$archive_path"

shim_path=""
for candidate in \
  /usr/local/bin/containerd-shim-kata-v2 \
  /usr/bin/containerd-shim-kata-v2 \
  /opt/kata/bin/containerd-shim-kata-v2 \
  /opt/kata/runtime-rs/bin/containerd-shim-kata-v2
do
  if [ -x "$candidate" ]; then
    shim_path="$candidate"
    break
  fi
done

[ -n "$shim_path" ] || fail "containerd-shim-kata-v2 was not found after extraction"

if ! command -v containerd-shim-kata-v2 >/dev/null 2>&1; then
  log "linking containerd-shim-kata-v2 into /usr/local/bin"
  run_sudo ln -sf "$shim_path" /usr/local/bin/containerd-shim-kata-v2
fi

ensure_service_started containerd
bootstrap_go_modules

if [ -e /dev/kvm ]; then
  add_user_to_group kvm
else
  warn "/dev/kvm is not present; Kata installation completed, but the runtime will not be usable until hardware virtualization is available"
fi

log "verifying containerd and Kata runtime shim"
run_sudo ctr version >/dev/null
command -v containerd-shim-kata-v2 >/dev/null 2>&1 || fail "containerd-shim-kata-v2 is still not on PATH"
containerd-shim-kata-v2 --version >/dev/null 2>&1 || fail "installed containerd-shim-kata-v2 is not runnable on this host; verify glibc compatibility or use Ubuntu 22.04+"

printf '\n'
printf 'Kata runtime install completed.\n'
printf 'Installed release: %s\n' "$version"
printf 'Verify with: sudo ctr version && containerd-shim-kata-v2 --version\n'
printf 'Note: direct ctr access often requires sudo because /run/containerd/containerd.sock is root-owned by default.\n'
printf 'Repo config-lint command:\n'
printf '  SANDBOX_ENABLED_RUNTIMES=containerd-kata-professional SANDBOX_DEFAULT_RUNTIME=containerd-kata-professional SANDBOX_KATA_BINARY=ctr SANDBOX_KATA_RUNTIME_CLASS=io.containerd.kata.v2 SANDBOX_KATA_CONTAINERD_SOCKET=/run/containerd/containerd.sock go run ./cmd/sandboxctl config-lint\n'
print_group_membership_note