#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE_IMAGE="${CORE_IMAGE:-${SANDBOX_QEMU_BASE_IMAGE_PATH:-}}"
RUNTIME_IMAGE="${RUNTIME_IMAGE:-}"
BROWSER_IMAGE="${BROWSER_IMAGE:-}"
CONTAINER_IMAGE="${CONTAINER_IMAGE:-}"
WORK_DIR="$(mktemp -d)"
SANDBOX_IDS=()
trap 'for id in "${SANDBOX_IDS[@]:-}"; do sandboxctl delete "$id" >/dev/null 2>&1 || true; done; rm -rf "$WORK_DIR"' EXIT

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
require_cmd curl

if [ -z "$CORE_IMAGE" ]; then
  echo "set CORE_IMAGE or SANDBOX_QEMU_BASE_IMAGE_PATH before running this smoke script" >&2
  exit 1
fi

log() {
  printf '[qemu-smoke] %s\n' "$*"
}

run_restart_command() {
  python3 - <<'PY'
import os
import shlex
import subprocess
import sys

command = os.environ.get("SANDBOXD_RESTART_COMMAND", "").strip()
if not command:
    sys.exit(0)
subprocess.run(shlex.split(command), check=True)
PY
}

create_qemu_sandbox() {
  local image="$1"
  local profile="$2"
  local features="${3:-}"
  local allow_tunnels="${4:-false}"
  local json
  if [ -n "$features" ]; then
    json="$(sandboxctl create --image "$image" --profile "$profile" --features "$features" --cpu 1 --memory-mb 1024 --disk-mb 2048 --network internet-disabled --allow-tunnels="$allow_tunnels" --start=true)"
  else
    json="$(sandboxctl create --image "$image" --profile "$profile" --cpu 1 --memory-mb 1024 --disk-mb 2048 --network internet-disabled --allow-tunnels="$allow_tunnels" --start=true)"
  fi
  local sandbox_id
  sandbox_id="$(printf '%s' "$json" | jq -r '.id')"
  if [ -z "$sandbox_id" ] || [ "$sandbox_id" = "null" ]; then
    echo "failed to parse sandbox id from create response" >&2
    printf '%s\n' "$json" >&2
    exit 1
  fi
  SANDBOX_IDS+=("$sandbox_id")
  printf '%s\n' "$sandbox_id"
}

inspect_status() {
  sandboxctl inspect "$1" | jq -r '.status'
}

wait_for_status() {
  local sandbox_id="$1"
  local want="$2"
  local attempts="${3:-60}"
  local status
  for _ in $(seq 1 "$attempts"); do
    status="$(inspect_status "$sandbox_id")"
    if [ "$status" = "$want" ]; then
      return 0
    fi
    sleep 2
  done
  echo "sandbox $sandbox_id did not reach status $want (last=$status)" >&2
  return 1
}

assert_core_substrate() {
  local sandbox_id="$1"
  sandboxctl exec "$sandbox_id" sh -lc 'for cmd in python3 node docker; do if command -v "$cmd" >/dev/null 2>&1; then echo "unexpected command present: $cmd"; exit 1; fi; done; test -d /workspace; test -f /var/lib/or3/bootstrap.ready'
}

assert_runtime_profile() {
  local sandbox_id="$1"
  sandboxctl exec "$sandbox_id" sh -lc 'command -v python3 >/dev/null 2>&1 && command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1'
}

assert_browser_profile() {
  local sandbox_id="$1"
  sandboxctl exec "$sandbox_id" sh -lc 'command -v Xvfb >/dev/null 2>&1'
}

assert_container_profile() {
  local sandbox_id="$1"
  sandboxctl exec "$sandbox_id" sh -lc 'command -v docker >/dev/null 2>&1 && getent group docker >/dev/null 2>&1'
}

assert_core_tunnel_bridge() {
  local sandbox_id="$1"
  sandboxctl exec "$sandbox_id" sh -lc '
set -eu
command -v systemd-socket-activate >/dev/null 2>&1
nohup systemd-socket-activate -l 127.0.0.1:8080 /bin/sh -lc '"'"'printf "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"'"'"' >/tmp/or3-smoke-http.log 2>&1 &
'
  local tunnel_json tunnel_id endpoint token
  tunnel_json="$(sandboxctl tunnel-create --port 8080 "$sandbox_id")"
  tunnel_id="$(printf '%s' "$tunnel_json" | jq -r '.id')"
  endpoint="$(printf '%s' "$tunnel_json" | jq -r '.endpoint')"
  token="$(printf '%s' "$tunnel_json" | jq -r '.access_token')"
  if [ -z "$tunnel_id" ] || [ "$tunnel_id" = "null" ] || [ -z "$endpoint" ] || [ "$endpoint" = "null" ]; then
    echo "failed to parse tunnel metadata" >&2
    printf '%s\n' "$tunnel_json" >&2
    exit 1
  fi
  trap 'sandboxctl tunnel-revoke "$tunnel_id" >/dev/null 2>&1 || true' RETURN
  local response
  for _ in $(seq 1 20); do
    if response="$(curl -fsS -H "X-Tunnel-Token: $token" "$endpoint/" 2>/dev/null)"; then
      break
    fi
    sleep 1
  done
  if [ "${response:-}" != "ok" ]; then
    echo "core tunnel bridge response mismatch: ${response:-<empty>}" >&2
    exit 1
  fi
  sandboxctl tunnel-revoke "$tunnel_id" >/dev/null
  trap - RETURN
}

log 'running production doctor'
sandboxctl doctor --production-qemu >/dev/null

log 'creating core sandbox'
core_id="$(create_qemu_sandbox "$CORE_IMAGE" core "" true)"
wait_for_status "$core_id" running

printf 'uploaded-from-host\n' > "$WORK_DIR/input.txt"
log 'verifying guest exec, file upload, and download'
sandboxctl upload "$core_id" "$WORK_DIR/input.txt" input.txt
sandboxctl exec "$core_id" sh -lc 'cat /workspace/input.txt > /workspace/output.txt && printf restored > /workspace/restore-marker.txt && id -un > /workspace/user.txt'
sandboxctl download "$core_id" output.txt "$WORK_DIR/output.txt"
if [ "$(cat "$WORK_DIR/output.txt")" != 'uploaded-from-host' ]; then
  echo 'downloaded artifact content mismatch' >&2
  exit 1
fi
assert_core_substrate "$core_id"

log 'verifying core local tunnel bridge'
assert_core_tunnel_bridge "$core_id"

log 'verifying suspend/resume'
sandboxctl suspend "$core_id" >/dev/null
wait_for_status "$core_id" suspended
sandboxctl resume "$core_id" >/dev/null
wait_for_status "$core_id" running

log 'verifying snapshot create/restore'
sandboxctl stop "$core_id" >/dev/null
wait_for_status "$core_id" stopped
snapshot_json="$(sandboxctl snapshot-create --name qemu-smoke "$core_id")"
snapshot_id="$(printf '%s' "$snapshot_json" | jq -r '.id')"
if [ -z "$snapshot_id" ] || [ "$snapshot_id" = 'null' ]; then
  echo 'failed to parse snapshot id' >&2
  printf '%s\n' "$snapshot_json" >&2
  exit 1
fi
sandboxctl start "$core_id" >/dev/null
wait_for_status "$core_id" running
sandboxctl exec "$core_id" sh -lc 'rm -f /workspace/output.txt /workspace/restore-marker.txt'
sandboxctl stop "$core_id" >/dev/null
wait_for_status "$core_id" stopped
sandboxctl snapshot-restore "$snapshot_id" "$core_id" >/dev/null
sandboxctl start "$core_id" >/dev/null
wait_for_status "$core_id" running
sandboxctl download "$core_id" restore-marker.txt "$WORK_DIR/restore-marker.txt"
if [ "$(cat "$WORK_DIR/restore-marker.txt")" != 'restored' ]; then
  echo 'snapshot restore marker mismatch' >&2
  exit 1
fi

if [ -n "${SANDBOXD_RESTART_COMMAND:-}" ]; then
  log 'running optional daemon restart reconciliation step'
  if [ "${OR3_ALLOW_DISRUPTIVE:-0}" != '1' ]; then
    echo 'set OR3_ALLOW_DISRUPTIVE=1 to run SANDBOXD_RESTART_COMMAND during smoke' >&2
    exit 1
  fi
  run_restart_command
  wait_for_status "$core_id" running 90
else
  log 'skipping daemon restart reconciliation step (set SANDBOXD_RESTART_COMMAND and OR3_ALLOW_DISRUPTIVE=1 to enable)'
fi

if [ -n "$RUNTIME_IMAGE" ]; then
  log 'verifying runtime profile capabilities'
  runtime_id="$(create_qemu_sandbox "$RUNTIME_IMAGE" runtime)"
  wait_for_status "$runtime_id" running
  assert_runtime_profile "$runtime_id"
fi

if [ -n "$BROWSER_IMAGE" ]; then
  log 'verifying browser profile capabilities'
  browser_id="$(create_qemu_sandbox "$BROWSER_IMAGE" browser)"
  wait_for_status "$browser_id" running
  assert_browser_profile "$browser_id"
fi

if [ -n "$CONTAINER_IMAGE" ]; then
  log 'verifying container profile capabilities'
  container_id="$(create_qemu_sandbox "$CONTAINER_IMAGE" container docker)"
  wait_for_status "$container_id" running
  assert_container_profile "$container_id"
fi

log 'qemu production smoke completed successfully'
