#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE_IMAGE="${CORE_IMAGE:-${SANDBOX_QEMU_BASE_IMAGE_PATH:-}}"
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

if [ "${OR3_ALLOW_DISRUPTIVE:-0}" != '1' ]; then
  echo 'qemu-recovery-drill.sh is disruptive; set OR3_ALLOW_DISRUPTIVE=1 to continue' >&2
  exit 1
fi
if [ -z "$CORE_IMAGE" ]; then
  echo 'set CORE_IMAGE or SANDBOX_QEMU_BASE_IMAGE_PATH before running this recovery drill' >&2
  exit 1
fi

log() {
  printf '[qemu-recovery] %s\n' "$*"
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
  echo 'failed to create drill sandbox' >&2
  printf '%s\n' "$create_json" >&2
  exit 1
fi
wait_for_status "$SANDBOX_ID" running
sandboxctl exec "$SANDBOX_ID" sh -lc 'printf recovery-ok > /workspace/recovery.txt'

if [ -n "${SANDBOXD_RESTART_COMMAND:-}" ]; then
  log 'running daemon restart drill'
  eval "$SANDBOXD_RESTART_COMMAND"
  wait_for_status "$SANDBOX_ID" running 90
  sandboxctl download "$SANDBOX_ID" recovery.txt "$WORK_DIR/recovery.txt"
  test "$(cat "$WORK_DIR/recovery.txt")" = 'recovery-ok'
else
  log 'skipping daemon restart drill (set SANDBOXD_RESTART_COMMAND to enable)'
fi

log 'running conservative stopped-state restore drill'
sandboxctl stop "$SANDBOX_ID" >/dev/null
wait_for_status "$SANDBOX_ID" stopped
snapshot_json="$(sandboxctl snapshot-create --name recovery-drill "$SANDBOX_ID")"
snapshot_id="$(printf '%s' "$snapshot_json" | jq -r '.id')"
if [ -z "$snapshot_id" ] || [ "$snapshot_id" = 'null' ]; then
  echo 'failed to create recovery snapshot' >&2
  exit 1
fi
sandboxctl snapshot-restore "$snapshot_id" "$SANDBOX_ID" >/dev/null
wait_for_status "$SANDBOX_ID" stopped 30

log 'recovery drill completed successfully'
log 'guest-agent handshake failure and interrupted snapshot subdrills still require host-level fault injection outside this script'
