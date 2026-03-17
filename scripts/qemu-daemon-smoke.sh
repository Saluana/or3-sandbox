#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
port=${SANDBOX_SMOKE_PORT:-18090}
token=${SANDBOX_SMOKE_TOKEN:-dev-token}
tenant=${SANDBOX_SMOKE_TENANT:-tenant-dev}
auth_header="Authorization: Bearer ${token}"
image_path=${SANDBOX_QEMU_BASE_IMAGE_PATH:-"$repo_root/images/guest/or3-guest-core.qcow2"}
state_dir=${SANDBOX_SMOKE_STATE_DIR:-"$(mktemp -d /tmp/or3-qemu-smoke.XXXXXX)"}
daemon_log="$state_dir/daemon.log"
api_base="http://127.0.0.1:${port}"

cleanup() {
  local exit_code=$?
  if [[ -n "${daemon_pid:-}" ]] && kill -0 "$daemon_pid" 2>/dev/null; then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  if [[ -z "${SANDBOX_SMOKE_KEEP_STATE:-}" ]]; then
    rm -rf "$state_dir"
  else
    printf 'state preserved at %s\n' "$state_dir"
  fi
  exit "$exit_code"
}
trap cleanup EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    printf 'missing required command: %s\n' "$1" >&2
    exit 1
  }
}

require_cmd curl
require_cmd go
require_cmd python3

mkdir -p "$state_dir/storage" "$state_dir/snapshots"

printf 'starting sandboxd on %s using %s\n' "$api_base" "$image_path"
(
  cd "$repo_root"
  SANDBOX_ENABLED_RUNTIMES=qemu-professional \
  SANDBOX_DEFAULT_RUNTIME=qemu-professional \
  SANDBOX_QEMU_BASE_IMAGE_PATH="$image_path" \
  SANDBOX_AUTH_MODE=static \
  SANDBOX_STATIC_TOKEN="${token}=${tenant}" \
  go run ./cmd/sandboxd -listen ":${port}" -db "$state_dir/sandbox.db" -storage-root "$state_dir/storage" -snapshot-root "$state_dir/snapshots" >"$daemon_log" 2>&1
) &
daemon_pid=$!

for _ in $(seq 1 60); do
  if curl -sf -H "$auth_header" "$api_base/v1/sandboxes" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -sf -H "$auth_header" "$api_base/v1/sandboxes" >/dev/null

create_payload=$(cat <<JSON
{"base_image_ref":"$image_path","cpu_limit":"2","memory_limit_mb":2048,"disk_limit_mb":4096,"network_mode":"internet-disabled","allow_tunnels":true,"start":true}
JSON
)
create_response=$(curl -sf -X POST -H "$auth_header" -H "Content-Type: application/json" -d "$create_payload" "$api_base/v1/sandboxes")
sandbox_id=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$create_response")
printf 'sandbox: %s\n' "$sandbox_id"

for i in $(seq 1 10); do
  exec_payload=$(printf '{"command":["echo","hello-%s"]}' "$i")
  exec_output=$(curl -sf -X POST -H "$auth_header" -H "Content-Type: application/json" -d "$exec_payload" "$api_base/v1/sandboxes/$sandbox_id/exec?stream=1")
  grep -q "hello-$i" <<<"$exec_output"
done
printf 'exec: 10/10 pass\n'

curl -sf -X PUT -H "$auth_header" -H "Content-Type: application/json" -d '{"content":"smoke-file-ok"}' "$api_base/v1/sandboxes/$sandbox_id/files/test-file.txt" >/dev/null
file_response=$(curl -sf -H "$auth_header" "$api_base/v1/sandboxes/$sandbox_id/files/test-file.txt")
file_content=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["content"])' <<<"$file_response")
[[ "$file_content" == "smoke-file-ok" ]]
curl -sf -X DELETE -H "$auth_header" "$api_base/v1/sandboxes/$sandbox_id/files/test-file.txt" >/dev/null
printf 'files: write/read/delete pass\n'

curl -sf -X PUT -H "$auth_header" -H "Content-Type: application/json" -d '{"content":"smoke tunnel ok"}' "$api_base/v1/sandboxes/$sandbox_id/files/index.html" >/dev/null
curl -sf -X POST -H "$auth_header" -H "Content-Type: application/json" -d '{"command":["sh","-lc","nohup python3 -m http.server 8080 -d /workspace >/workspace/http.log 2>&1 &"]}' "$api_base/v1/sandboxes/$sandbox_id/exec" >/dev/null

for _ in $(seq 1 20); do
  if curl -sf -X POST -H "$auth_header" -H "Content-Type: application/json" -d '{"command":["python3","-c","import urllib.request; print(urllib.request.urlopen(\"http://127.0.0.1:8080\").read().decode())"]}' "$api_base/v1/sandboxes/$sandbox_id/exec" | grep -q "smoke tunnel ok"; then
    break
  fi
  sleep 1
done

tunnel_response=$(curl -sf -X POST -H "$auth_header" -H "Content-Type: application/json" -d '{"target_port":8080,"protocol":"http"}' "$api_base/v1/sandboxes/$sandbox_id/tunnels")
tunnel_id=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$tunnel_response")
tunnel_token=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])' <<<"$tunnel_response")
tunnel_body=$(curl -sf -H "$auth_header" -H "X-Tunnel-Token: $tunnel_token" "$api_base/v1/tunnels/$tunnel_id/proxy/")
grep -q "smoke tunnel ok" <<<"$tunnel_body"
printf 'tunnels: pass (%s)\n' "$tunnel_id"

printf 'smoke complete\n'
