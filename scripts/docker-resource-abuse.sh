#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${IMAGE:-${SANDBOX_BASE_IMAGE:-alpine:3.20}}"
PROFILE="${PROFILE:-core}"
FILE_COUNT="${FILE_COUNT:-4000}"
FILL_MB="${FILL_MB:-128}"
SANDBOX_ID=""

trap 'if [ -n "$SANDBOX_ID" ]; then sandboxctl delete "$SANDBOX_ID" >/dev/null 2>&1 || true; fi' EXIT

sandboxctl() {
  if [ -n "${SANDBOXCTL_BIN:-}" ]; then
    "$SANDBOXCTL_BIN" "$@"
  else
    (cd "$ROOT_DIR" && go run ./cmd/sandboxctl "$@")
  fi
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd jq
require_cmd go

log() {
  printf '[docker-abuse] %s\n' "$*"
}

create_json="$(sandboxctl create --image "$IMAGE" --profile "$PROFILE" --cpu 1 --memory-mb 512 --disk-mb 512 --network internet-disabled --allow-tunnels=false --start=true)"
SANDBOX_ID="$(printf '%s' "$create_json" | jq -r '.id')"
if [ -z "$SANDBOX_ID" ] || [ "$SANDBOX_ID" = 'null' ]; then
  echo 'failed to create docker abuse sandbox' >&2
  printf '%s\n' "$create_json" >&2
  exit 1
fi

log 'creating high file-count workspace payload'
sandboxctl exec "$SANDBOX_ID" sh -lc "mkdir -p /workspace/flood && i=0; while [ \"\$i\" -lt $FILE_COUNT ]; do : > /workspace/flood/file-\$i.txt; i=\$((i+1)); done"

log 'creating bounded scratch and cache pressure'
sandboxctl exec "$SANDBOX_ID" sh -lc "mkdir -p /scratch/load /cache/load && dd if=/dev/zero of=/scratch/load/fill.bin bs=1M count=$FILL_MB status=none && dd if=/dev/zero of=/cache/load/fill.bin bs=1M count=$FILL_MB status=none"

log 'capturing quota and metrics snapshots'
sandboxctl quota | jq '.' >/dev/null

if sandboxctl metrics >/dev/null 2>&1; then
  log 'metrics endpoint returned successfully'
else
  log 'metrics command unavailable; inspect runtime-health and audit logs manually'
fi

log 'docker storage-pressure drill completed'
