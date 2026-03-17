#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_IMAGE="${BASE_IMAGE:-$ROOT_DIR/or3-guest-core.qcow2}"
QEMU_BINARY="${QEMU_BINARY:-qemu-system-x86_64}"
QEMU_IMG_BINARY="${QEMU_IMG_BINARY:-qemu-img}"
QEMU_ACCEL="${QEMU_ACCEL:-kvm}"
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
require_cmd mkfs.ext4
require_cmd jq
require_cmd python3

RESOLVED_QEMU_ACCEL="$QEMU_ACCEL"
if [[ "$RESOLVED_QEMU_ACCEL" == "auto" ]]; then
  if [[ -e /dev/kvm ]]; then
    RESOLVED_QEMU_ACCEL="kvm"
  else
    RESOLVED_QEMU_ACCEL="tcg"
  fi
fi

"$QEMU_IMG_BINARY" create -f qcow2 -F qcow2 -b "$BASE_IMAGE" "$WORK_DIR/root-overlay.qcow2" 20G >/dev/null
"$QEMU_IMG_BINARY" create -f raw "$WORK_DIR/workspace.img" 10G >/dev/null
mkfs.ext4 -F -L or3-smoke-ws "$WORK_DIR/workspace.img" >/dev/null

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
  -accel "$RESOLVED_QEMU_ACCEL" \
  -m 2048 \
  -smp 2 \
  -device virtio-serial \
  -chardev "socket,id=agent0,path=$WORK_DIR/agent.sock,server=on,wait=off" \
  -device "virtserialport,chardev=agent0,name=org.or3.guest_agent" \
  -drive "if=virtio,file=$WORK_DIR/root-overlay.qcow2,format=qcow2" \
  -drive "if=virtio,file=$WORK_DIR/workspace.img,format=raw" \
  -netdev "user,id=net0" \
  -device "$QEMU_NET_DEVICE,netdev=net0"

agent_rpc() {
  local op="$1"
  local payload="${2:-null}"
  OR3_AGENT_OP="$op" OR3_AGENT_PAYLOAD="$payload" OR3_AGENT_SOCKET_PATH="$WORK_DIR/agent.sock" python3 - <<'PY'
import json
import os
import socket
import struct
import time

sock_path = os.environ["OR3_AGENT_SOCKET_PATH"]
op = os.environ["OR3_AGENT_OP"]
payload = os.environ.get("OR3_AGENT_PAYLOAD", "null")
message = {"op": op, "id": f"smoke-{time.time_ns()}"}
if payload and payload != "null":
    message["result"] = json.loads(payload)
raw = json.dumps(message).encode("utf-8")
with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
    conn.settimeout(5)
    conn.connect(sock_path)
    conn.sendall(struct.pack(">I", len(raw)) + raw)
    header = conn.recv(4)
    if len(header) != 4:
        raise SystemExit("short guest-agent header")
    length = struct.unpack(">I", header)[0]
    data = b""
    while len(data) < length:
        chunk = conn.recv(length - len(data))
        if not chunk:
            raise SystemExit("short guest-agent body")
        data += chunk
reply = json.loads(data.decode("utf-8"))
if not reply.get("ok", False):
    raise SystemExit(reply.get("error") or "guest agent request failed")
result = reply.get("result")
if result is None:
    result = {}
print(json.dumps(result))
PY
}

wait_for_agent_ready() {
  local ready_json=""
  local attempt
  for attempt in $(seq 1 120); do
    if ready_json="$(agent_rpc ping 2>/dev/null)" && jq -e '.ready == true' >/dev/null <<<"$ready_json"; then
      return 0
    fi
    sleep 2
  done
  return 1
}

if wait_for_agent_ready; then
  echo "guest image is agent reachable and bootstrap-ready"
  agent_rpc shutdown '{"reboot":false}' >/dev/null 2>&1 || true
  exit 0
fi

echo "guest image did not become agent-ready" >&2
if [ -f "$WORK_DIR/serial.log" ]; then
  tail -n 50 "$WORK_DIR/serial.log" >&2 || true
fi
exit 1
