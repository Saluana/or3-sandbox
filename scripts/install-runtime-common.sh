#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APT_UPDATED=0
GROUP_MEMBERSHIP_CHANGED=0
INSTALL_USER="${SUDO_USER:-${USER:-$(id -un)}}"

log() {
  printf '[%s] %s\n' "$SCRIPT_NAME" "$*"
}

warn() {
  printf '[%s] warning: %s\n' "$SCRIPT_NAME" "$*" >&2
}

fail() {
  printf '[%s] error: %s\n' "$SCRIPT_NAME" "$*" >&2
  exit 1
}

run_sudo() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
    return
  fi
  sudo "$@"
}

require_linux() {
  [ "$(uname -s)" = "Linux" ] || fail "this installer only supports Linux hosts"
}

require_apt() {
  command -v apt-get >/dev/null 2>&1 || fail "this installer currently supports apt-based Linux distributions only"
}

require_sudo() {
  command -v sudo >/dev/null 2>&1 || fail "sudo is required for package installation"
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

apt_update() {
  if [ "$APT_UPDATED" -eq 0 ]; then
    log "updating apt package metadata"
    run_sudo apt-get update
    APT_UPDATED=1
  fi
}

install_packages() {
  apt_update
  log "installing packages: $*"
  run_sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@"
}

ensure_service_started() {
  command -v systemctl >/dev/null 2>&1 || fail "systemctl is required to manage $1"
  log "enabling and starting service: $1"
  run_sudo systemctl enable --now "$1"
}

add_user_to_group() {
  local group="$1"

  if ! getent group "$group" >/dev/null 2>&1; then
    warn "group $group does not exist yet; skipping user membership"
    return
  fi

  if id -nG "$INSTALL_USER" | tr ' ' '\n' | grep -qx "$group"; then
    return
  fi

  log "adding $INSTALL_USER to group $group"
  run_sudo usermod -aG "$group" "$INSTALL_USER"
  GROUP_MEMBERSHIP_CHANGED=1
}

bootstrap_go_modules() {
  if ! command -v go >/dev/null 2>&1; then
    warn "go is not installed; skipping go module bootstrap"
    return
  fi

  if [ ! -f "$REPO_ROOT/go.mod" ]; then
    return
  fi

  log "downloading Go modules for the repo"
  (
    cd "$REPO_ROOT"
    go mod download
  )
}

host_arch() {
  case "$(uname -m)" in
    x86_64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    ppc64le) echo ppc64le ;;
    s390x) echo s390x ;;
    *) fail "unsupported CPU architecture: $(uname -m)" ;;
  esac
}

glibc_version() {
  getconf GNU_LIBC_VERSION 2>/dev/null | awk '{print $2}'
}

require_glibc_at_least() {
  local minimum="$1"
  local current

  current="$(glibc_version)"
  [ -n "$current" ] || fail "could not determine glibc version on this host"

  if ! dpkg --compare-versions "$current" ge "$minimum"; then
    fail "glibc $minimum or newer is required for the latest upstream Kata static release; this host has glibc $current"
  fi
}

print_group_membership_note() {
  if [ "$GROUP_MEMBERSHIP_CHANGED" -eq 1 ]; then
    printf '\n'
    printf 'Group membership changed for %s. Open a new login shell before using the runtime without sudo.\n' "$INSTALL_USER"
  fi
}