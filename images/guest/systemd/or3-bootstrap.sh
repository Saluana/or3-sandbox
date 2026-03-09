#!/usr/bin/env bash
set -euo pipefail

WORKSPACE_DEVICE="${WORKSPACE_DEVICE:-/dev/vdb}"
WORKSPACE_MOUNT="${WORKSPACE_MOUNT:-/workspace}"
READY_MARKER="${READY_MARKER:-/var/lib/or3/bootstrap.ready}"
WORKSPACE_OWNER="${WORKSPACE_OWNER:-sandbox}"
WORKSPACE_GROUP="${WORKSPACE_GROUP:-$WORKSPACE_OWNER}"

log_bootstrap() {
  local message="$1"
  echo "$message" >&2
  if [ -w /dev/ttyS0 ]; then
    echo "$message" >/dev/ttyS0 || true
  fi
}

mkdir -p "$(dirname "$READY_MARKER")"
mkdir -p "$WORKSPACE_MOUNT"

log_bootstrap "or3-bootstrap: starting"

if [ -b "$WORKSPACE_DEVICE" ]; then
  if ! blkid "$WORKSPACE_DEVICE" >/dev/null 2>&1; then
    mkfs.ext4 -F "$WORKSPACE_DEVICE"
  fi

  uuid="$(blkid -s UUID -o value "$WORKSPACE_DEVICE")"
  if [ -n "$uuid" ] && ! grep -q "$uuid" /etc/fstab; then
    echo "UUID=$uuid $WORKSPACE_MOUNT ext4 defaults,nofail 0 2" >> /etc/fstab
  fi

  mountpoint -q "$WORKSPACE_MOUNT" || mount "$WORKSPACE_MOUNT"
fi

if id "$WORKSPACE_OWNER" >/dev/null 2>&1; then
  chown "$WORKSPACE_OWNER:$WORKSPACE_GROUP" "$WORKSPACE_MOUNT"
  chmod 0755 "$WORKSPACE_MOUNT"
fi

install -d -o root -g root -m 0755 /var/lib/or3
touch "$READY_MARKER"
sync
log_bootstrap "or3-bootstrap: ready"
