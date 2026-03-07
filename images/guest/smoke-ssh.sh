#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_IMAGE="${BASE_IMAGE:-$ROOT_DIR/or3-guest-base.qcow2}"
SSH_USER="${SSH_USER:-sandbox}"
SSH_PRIVATE_KEY_PATH="${SSH_PRIVATE_KEY_PATH:?set SSH_PRIVATE_KEY_PATH to the operator private key path}"
QEMU_BINARY="${QEMU_BINARY:-qemu-system-x86_64}"
QEMU_IMG_BINARY="${QEMU_IMG_BINARY:-qemu-img}"
QEMU_ACCEL="${QEMU_ACCEL:-kvm}"
SSH_PORT="${SSH_PORT:-2222}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"; if [ -f "$WORK_DIR/qemu.pid" ]; then kill "$(cat "$WORK_DIR/qemu.pid")" >/dev/null 2>&1 || true; fi' EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd "$QEMU_BINARY"
require_cmd "$QEMU_IMG_BINARY"
require_cmd ssh

"$QEMU_IMG_BINARY" create -f qcow2 -F qcow2 -b "$BASE_IMAGE" "$WORK_DIR/root-overlay.qcow2" 20G >/dev/null
"$QEMU_IMG_BINARY" create -f raw "$WORK_DIR/workspace.img" 10G >/dev/null

QEMU_NET_DEVICE="${QEMU_NET_DEVICE:-virtio-net-pci}"
if [[ "$QEMU_BINARY" == *aarch64* ]] && [[ "${QEMU_NET_DEVICE:-}" == "virtio-net-pci" ]]; then
  QEMU_NET_DEVICE="virtio-net-device"
fi

"$QEMU_BINARY" \
  -daemonize \
  -pidfile "$WORK_DIR/qemu.pid" \
  -monitor "unix:$WORK_DIR/monitor.sock,server,nowait" \
  -serial "file:$WORK_DIR/serial.log" \
  -display none \
  -accel "$QEMU_ACCEL" \
  -m 2048 \
  -smp 2 \
  -drive "if=virtio,file=$WORK_DIR/root-overlay.qcow2,format=qcow2" \
  -drive "if=virtio,file=$WORK_DIR/workspace.img,format=raw" \
  -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22" \
  -device "$QEMU_NET_DEVICE,netdev=net0"

for _ in $(seq 1 90); do
  if ssh \
    -o BatchMode=yes \
    -o IdentitiesOnly=yes \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile="$WORK_DIR/known_hosts" \
    -o ConnectTimeout=2 \
    -i "$SSH_PRIVATE_KEY_PATH" \
    -p "$SSH_PORT" \
    "$SSH_USER@127.0.0.1" \
    sh -lc 'test -f /var/lib/or3/bootstrap.ready && test -d /workspace'
  then
    echo "guest image is SSH reachable and bootstrap-ready"
    exit 0
  fi
  sleep 2
done

echo "guest image did not become ready" >&2
if [ -f "$WORK_DIR/serial.log" ]; then
  tail -n 50 "$WORK_DIR/serial.log" >&2 || true
fi
exit 1
