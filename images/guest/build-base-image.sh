#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLOUD_INIT_DIR="$ROOT_DIR/cloud-init"
SYSTEMD_DIR="$ROOT_DIR/systemd"

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd cloud-localds
require_cmd ssh
QEMU_BINARY="${QEMU_BINARY:-qemu-system-x86_64}"
QEMU_IMG_BINARY="${QEMU_IMG_BINARY:-qemu-img}"
QEMU_ACCEL="${QEMU_ACCEL:-kvm}"
SSH_PORT="${SSH_PORT:-2222}"
SSH_PRIVATE_KEY_PATH="${SSH_PRIVATE_KEY_PATH:?set SSH_PRIVATE_KEY_PATH to the operator private key path}"
require_cmd "$QEMU_BINARY"
require_cmd "$QEMU_IMG_BINARY"

BASE_IMAGE_URL="${BASE_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"
DOWNLOAD_PATH="${DOWNLOAD_PATH:-$ROOT_DIR/.cache/base-cloudimg.qcow2}"
OUTPUT_IMAGE="${OUTPUT_IMAGE:-$ROOT_DIR/or3-guest-base.qcow2}"
SSH_USER="${SSH_USER:-sandbox}"
SSH_PUBLIC_KEY_PATH="${SSH_PUBLIC_KEY_PATH:?set SSH_PUBLIC_KEY_PATH to the operator public key path}"
WORK_DIR="$(mktemp -d)"
trap 'if [ -f "$WORK_DIR/qemu.pid" ]; then kill "$(cat "$WORK_DIR/qemu.pid")" >/dev/null 2>&1 || true; fi; rm -rf "$WORK_DIR"' EXIT

mkdir -p "$(dirname "$DOWNLOAD_PATH")"

if [ ! -f "$DOWNLOAD_PATH" ]; then
  require_cmd curl
  curl -L "$BASE_IMAGE_URL" -o "$DOWNLOAD_PATH"
fi

cp "$DOWNLOAD_PATH" "$WORK_DIR/base.qcow2"
cp "$SYSTEMD_DIR/or3-bootstrap.sh" "$WORK_DIR/or3-bootstrap.sh"
cp "$SYSTEMD_DIR/or3-bootstrap.service" "$WORK_DIR/or3-bootstrap.service"

SSH_PUBLIC_KEY="$(tr -d '\n' < "$SSH_PUBLIC_KEY_PATH")"

sed \
  -e "s/__SSH_USER__/$SSH_USER/g" \
  -e "s/__SSH_PUBLIC_KEY__/$SSH_PUBLIC_KEY/g" \
  -e "/__BOOTSTRAP_SCRIPT__/{
r $WORK_DIR/or3-bootstrap.sh
d
}" \
  -e "/__BOOTSTRAP_SERVICE__/{
r $WORK_DIR/or3-bootstrap.service
d
}" \
  "$CLOUD_INIT_DIR/user-data.tpl" > "$WORK_DIR/user-data"

sed \
  -e "s/__INSTANCE_ID__/or3-build/g" \
  -e "s/__HOSTNAME__/or3-build/g" \
  "$CLOUD_INIT_DIR/meta-data.tpl" > "$WORK_DIR/meta-data"

cloud-localds "$WORK_DIR/seed.img" "$WORK_DIR/user-data" "$WORK_DIR/meta-data"
"$QEMU_IMG_BINARY" create -f qcow2 -F qcow2 -b "$WORK_DIR/base.qcow2" "$OUTPUT_IMAGE" 20G >/dev/null
"$QEMU_IMG_BINARY" create -f raw "$WORK_DIR/workspace.img" 10G >/dev/null

net_device="virtio-net-pci"
if [[ "$QEMU_BINARY" == *aarch64* ]]; then
  net_device="virtio-net-device"
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
  -drive "if=virtio,file=$OUTPUT_IMAGE,format=qcow2" \
  -drive "if=virtio,file=$WORK_DIR/workspace.img,format=raw" \
  -drive "if=virtio,file=$WORK_DIR/seed.img,format=raw,readonly=on" \
  -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22" \
  -device "$net_device,netdev=net0"

ready=0
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
    sh -lc 'test -f /var/lib/or3/bootstrap.ready'
  then
    ssh \
      -o BatchMode=yes \
      -o IdentitiesOnly=yes \
      -o StrictHostKeyChecking=no \
      -o UserKnownHostsFile="$WORK_DIR/known_hosts" \
      -i "$SSH_PRIVATE_KEY_PATH" \
      -p "$SSH_PORT" \
      "$SSH_USER@127.0.0.1" \
      sudo poweroff || true
    sleep 5
    ready=1
    break
  fi
  sleep 2
done

if [ "$ready" != "1" ]; then
  echo "guest image bootstrap did not reach readiness" >&2
  if [ -f "$WORK_DIR/serial.log" ]; then
    tail -n 50 "$WORK_DIR/serial.log" >&2 || true
  fi
  exit 1
fi

cat <<EOF
Prepared guest base image:
  $OUTPUT_IMAGE

The image has been bootstrapped once with cloud-init and the guest bootstrap service.

Next step:
  Run images/guest/smoke-ssh.sh against this image before using it with SANDBOX_QEMU_BASE_IMAGE_PATH.
EOF
