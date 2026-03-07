#!/usr/bin/env bash
set -euo pipefail

WORKSPACE_DEVICE="${WORKSPACE_DEVICE:-/dev/vdb}"
WORKSPACE_MOUNT="${WORKSPACE_MOUNT:-/workspace}"
READY_MARKER="${READY_MARKER:-/var/lib/or3/bootstrap.ready}"

mkdir -p "$(dirname "$READY_MARKER")"
mkdir -p "$WORKSPACE_MOUNT"

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

install -d -o root -g root -m 0755 /var/lib/or3
touch "$READY_MARKER"
