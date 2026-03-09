#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE_IMAGE="${CORE_IMAGE:-${SANDBOX_QEMU_BASE_IMAGE_PATH:-}}"
DISK_FILL_MB="${DISK_FILL_MB:-64}"
FILE_COUNT="${FILE_COUNT:-2000}"
PID_FANOUT="${PID_FANOUT:-32}"
STDOUT_LINES="${STDOUT_LINES:-4000}"
WORK_DIR="$(mktemp -d)"
SANDBOX_ID=""
trap 'if [ -n "$SANDBOX_ID" ]; then sandboxctl delete "$SANDBOX_ID" >/dev/null 2>&1 || true; fi; rm -rf "$WORK_DIR"' EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

sandboxctl() {
  if [ -n "${SANDBOXCTL_BIN:-}" ]; then
    "$SANDBOXCTL_BIN" "$@"
  else
    (cd "$ROOT_DIR" && go run ./cmd/sandboxctl "$@")
  fi
}

require_cmd jq
require_cmd mktemp
require_cmd go

if [ -z "$CORE_IMAGE" ]; then
  echo 'set CORE_IMAGE or SANDBOX_QEMU_BASE_IMAGE_PATH before running this abuse drill' >&2
  exit 1
fi

log() {
  printf '[qemu-abuse] %s\n' "$*"
}

wait_for_status() {
  local sandbox_id="$1"
  local want="$2"
  local attempts="${3:-60}"
  local status
  for _ in $(seq 1 "$attempts"); do
    status="$(sandboxctl inspect "$sandbox_id" | jq -r '.status')"
    if [ "$status" = "$want" ]; then
      return 0
    fi
    sleep 2
  done
  echo "sandbox $sandbox_id did not reach status $want (last=$status)" >&2
  return 1
}

create_json="$(sandboxctl create --image "$CORE_IMAGE" --profile core --cpu 1 --memory-mb 1024 --disk-mb 2048 --network internet-disabled --allow-tunnels=false --start=true)"
SANDBOX_ID="$(printf '%s' "$create_json" | jq -r '.id')"
if [ -z "$SANDBOX_ID" ] || [ "$SANDBOX_ID" = 'null' ]; then
  echo 'failed to create abuse sandbox' >&2
  printf '%s\n' "$create_json" >&2
  exit 1
fi
wait_for_status "$SANDBOX_ID" running

log 'running bounded stdout flood'
sandboxctl exec "$SANDBOX_ID" sh -lc "i=0; while [ \"\$i\" -lt $STDOUT_LINES ]; do echo abuse-line-\$i; i=\$((i+1)); done" >/dev/null

log 'running bounded file-count abuse'
sandboxctl exec "$SANDBOX_ID" sh -lc "mkdir -p /workspace/flood && i=0; while [ \"\$i\" -lt $FILE_COUNT ]; do : > /workspace/flood/file-\$i.txt; i=\$((i+1)); done"

log 'running bounded disk pressure drill'
sandboxctl exec "$SANDBOX_ID" sh -lc "dd if=/dev/zero of=/workspace/fill.bin bs=1M count=$DISK_FILL_MB status=none"

log 'running bounded pid fan-out drill'
sandboxctl exec "$SANDBOX_ID" sh -lc "children=''; i=0; while [ \"\$i\" -lt $PID_FANOUT ]; do sleep 2 & children=\"\$children \$!\"; i=\$((i+1)); done; wait \$children"

log 'capturing runtime health and quota views'
sandboxctl runtime-health > "$WORK_DIR/runtime-health.json"
sandboxctl quota > "$WORK_DIR/quota.json"
status="$(sandboxctl inspect "$SANDBOX_ID" | jq -r '.status')"
case "$status" in
  running|degraded|stopped)
    ;;
  *)
    echo "unexpected sandbox status after abuse drill: $status" >&2
    exit 1
    ;;
esac

log 'resource abuse drill completed successfully'
log "artifacts written to $WORK_DIR during execution (removed on exit)"
