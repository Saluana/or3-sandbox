#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$ROOT_DIR/../.." && pwd)"
CLOUD_INIT_DIR="$ROOT_DIR/cloud-init"
SYSTEMD_DIR="$ROOT_DIR/systemd"
PROFILES_DIR="$ROOT_DIR/profiles"

log_step() {
  printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd cloud-localds
require_cmd go
require_cmd jq
require_cmd mkfs.ext4
require_cmd python3
QEMU_BINARY="${QEMU_BINARY:-qemu-system-x86_64}"
QEMU_IMG_BINARY="${QEMU_IMG_BINARY:-qemu-img}"
QEMU_ACCEL="${QEMU_ACCEL:-kvm}"
require_cmd "$QEMU_BINARY"
require_cmd "$QEMU_IMG_BINARY"

PROFILE="${PROFILE:-core}"
PROFILE_MANIFEST="${PROFILE_MANIFEST:-$PROFILES_DIR/$PROFILE.json}"
if [ ! -f "$PROFILE_MANIFEST" ]; then
	echo "missing guest profile manifest: $PROFILE_MANIFEST" >&2
	exit 1
fi

BASE_IMAGE_URL="${BASE_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"
DOWNLOAD_PATH="${DOWNLOAD_PATH:-$ROOT_DIR/.cache/base-cloudimg.qcow2}"
EXPECTED_BASE_IMAGE_SHA256="${EXPECTED_BASE_IMAGE_SHA256:-}"
OUTPUT_IMAGE="${OUTPUT_IMAGE:-$ROOT_DIR/or3-guest-$PROFILE.qcow2}"
PROFILE_RESOLVED_OUTPUT="${PROFILE_RESOLVED_OUTPUT:-$OUTPUT_IMAGE.profile.json}"
PACKAGE_INVENTORY_OUTPUT="${PACKAGE_INVENTORY_OUTPUT:-$OUTPUT_IMAGE.packages.txt}"
CONTRACT_OUTPUT="${CONTRACT_OUTPUT:-$OUTPUT_IMAGE.or3.json}"
SSH_HOST_PUBLIC_KEY_OUTPUT="${SSH_HOST_PUBLIC_KEY_OUTPUT:-$OUTPUT_IMAGE.ssh-host-key.pub}"
SANDBOX_USER="${SANDBOX_USER:-sandbox}"
AGENT_USER="${AGENT_USER:-or3-agent}"
GUEST_AGENT_GOARCH="${GUEST_AGENT_GOARCH:-}"
BUILD_VERSION="${BUILD_VERSION:-$(date -u +%Y%m%dT%H%M%SZ)}"
GIT_SHA="${GIT_SHA:-$(git -C "$REPO_ROOT" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)}"
WORK_DIR="$(mktemp -d)"
trap 'if [ -f "$WORK_DIR/qemu.pid" ]; then kill "$(cat "$WORK_DIR/qemu.pid")" >/dev/null 2>&1 || true; fi; rm -rf "$WORK_DIR"' EXIT

mkdir -p "$(dirname "$DOWNLOAD_PATH")"

if [ -z "$GUEST_AGENT_GOARCH" ]; then
	case "$QEMU_BINARY" in
		*aarch64*) GUEST_AGENT_GOARCH="arm64" ;;
		*) GUEST_AGENT_GOARCH="amd64" ;;
	esac
fi

log_step "Resolving guest profile: $PROFILE_MANIFEST"

python3 - "$PROFILE_MANIFEST" > "$WORK_DIR/profile.json" <<'PY'
import json
import pathlib
import sys

ARRAY_FIELDS = {"allowed_features", "capabilities", "enable_services", "packages", "sandbox_groups"}

def unique(values):
    seen = []
    for value in values:
        if value not in seen:
            seen.append(value)
    return seen

def merge(base, child):
    merged = dict(base)
    for key, value in child.items():
        if key == "extends":
            continue
        if key == "control":
            next_value = dict(base.get("control", {}))
            next_value.update(value or {})
            merged[key] = next_value
            continue
        if key in ARRAY_FIELDS:
            merged[key] = unique(list(base.get(key, [])) + list(value or []))
            continue
        merged[key] = value
    return merged

def load(path):
    data = json.loads(path.read_text())
    parent = data.get("extends")
    if not parent:
        return merge({}, data)
    parent_path = path.parent / f"{parent}.json"
    return merge(load(parent_path), data)

manifest_path = pathlib.Path(sys.argv[1]).resolve()
resolved = load(manifest_path)
if resolved.get("profile") != manifest_path.stem:
    raise SystemExit(f"resolved profile name mismatch: expected {manifest_path.stem!r}, got {resolved.get('profile')!r}")
print(json.dumps(resolved, indent=2))
PY

profile_name="$(jq -r '.profile' "$WORK_DIR/profile.json")"
ssh_present="$(jq -r '.ssh_present // false' "$WORK_DIR/profile.json")"
profile_packages="$(jq -r '.packages[] | "  - " + .' "$WORK_DIR/profile.json")"
sandbox_groups="$(jq -r '(.sandbox_groups // []) | if length == 0 then "[]" else "[" + (join(", ")) + "]" end' "$WORK_DIR/profile.json")"
profile_enable_commands="$(jq -r '(.enable_services // []) | map("  - systemctl enable " + . + "\n  - systemctl start " + .) | join("\n")' "$WORK_DIR/profile.json")"
profile_manifest_json="$(cat "$WORK_DIR/profile.json")"

sandbox_sudo_line=""
if [ "$(jq -r '.sandbox_passwordless_sudo // false' "$WORK_DIR/profile.json")" = "true" ]; then
	sandbox_sudo_line=$'    sudo: ALL=(ALL) NOPASSWD:ALL\n'
fi

ssh_authorized_keys_block=""
ssh_enable_commands=""
if [ "$ssh_present" = "true" ]; then
	SSH_PUBLIC_KEY_PATH="${SSH_PUBLIC_KEY_PATH:?set SSH_PUBLIC_KEY_PATH for ssh-compat/debug guest profiles}"
	ssh_public_key="$(tr -d '\n' < "$SSH_PUBLIC_KEY_PATH")"
	ssh_authorized_keys_block=$'    ssh_authorized_keys:\n'
	ssh_authorized_keys_block+="      - $ssh_public_key"
	ssh_authorized_keys_block+=$'\n'
	ssh_enable_commands=$'  - systemctl enable ssh\n  - systemctl start ssh\n'
else
  ssh_enable_commands=$'  - systemctl disable --now ssh >/dev/null 2>&1 || true\n  - apt-get purge -y openssh-server >/dev/null 2>&1 || true\n  - apt-get autoremove -y >/dev/null 2>&1 || true\n'
fi

cp "$SYSTEMD_DIR/or3-bootstrap.sh" "$WORK_DIR/or3-bootstrap.sh"
cp "$SYSTEMD_DIR/or3-bootstrap.service" "$WORK_DIR/or3-bootstrap.service"
cp "$SYSTEMD_DIR/or3-guest-agent.service" "$WORK_DIR/or3-guest-agent.service"

log_step "Building guest agent binary for linux/$GUEST_AGENT_GOARCH"
(
	cd "$REPO_ROOT"
	CGO_ENABLED=0 GOOS=linux GOARCH="$GUEST_AGENT_GOARCH" go build -o "$WORK_DIR/or3-guest-agent" ./cmd/or3-guest-agent
)
base64 < "$WORK_DIR/or3-guest-agent" | tr -d '\n' > "$WORK_DIR/or3-guest-agent.b64"

log_step "Rendering cloud-init user-data"
AGENT_USER="$AGENT_USER" \
BOOTSTRAP_SCRIPT_FILE="$WORK_DIR/or3-bootstrap.sh" \
BOOTSTRAP_SERVICE_FILE="$WORK_DIR/or3-bootstrap.service" \
GUEST_AGENT_BINARY_BASE64_FILE="$WORK_DIR/or3-guest-agent.b64" \
GUEST_AGENT_SERVICE_FILE="$WORK_DIR/or3-guest-agent.service" \
PROFILE_ENABLE_COMMANDS="$profile_enable_commands" \
PROFILE_MANIFEST_JSON_FILE="$WORK_DIR/profile.json" \
PROFILE_NAME="$profile_name" \
PROFILE_PACKAGES="$profile_packages" \
SANDBOX_GROUPS="$sandbox_groups" \
SANDBOX_SUDO_LINE="$sandbox_sudo_line" \
SANDBOX_USER="$SANDBOX_USER" \
SSH_AUTHORIZED_KEYS_BLOCK="$ssh_authorized_keys_block" \
SSH_ENABLE_COMMANDS="$ssh_enable_commands" \
python3 - "$CLOUD_INIT_DIR/user-data.tpl" > "$WORK_DIR/user-data" <<'PY'
import os
import sys

template = open(sys.argv[1], 'r', encoding='utf-8').read()

def read_text(path, strip_newlines=False):
  value = open(path, 'r', encoding='utf-8').read()
  if strip_newlines:
    value = value.replace("\n", "")
  return value

def indent_multiline(value, prefix="      "):
  return value.replace("\n", f"\n{prefix}")

replacements = {
  "AGENT_USER": os.environ.get("AGENT_USER", ""),
  "BOOTSTRAP_SCRIPT": indent_multiline(read_text(os.environ["BOOTSTRAP_SCRIPT_FILE"])),
  "BOOTSTRAP_SERVICE": indent_multiline(read_text(os.environ["BOOTSTRAP_SERVICE_FILE"])),
  "GUEST_AGENT_BINARY_BASE64": read_text(os.environ["GUEST_AGENT_BINARY_BASE64_FILE"], strip_newlines=True),
  "GUEST_AGENT_SERVICE": indent_multiline(read_text(os.environ["GUEST_AGENT_SERVICE_FILE"])),
  "PROFILE_ENABLE_COMMANDS": os.environ.get("PROFILE_ENABLE_COMMANDS", ""),
  "PROFILE_MANIFEST_JSON": indent_multiline(read_text(os.environ["PROFILE_MANIFEST_JSON_FILE"])),
  "PROFILE_NAME": os.environ.get("PROFILE_NAME", ""),
  "PROFILE_PACKAGES": os.environ.get("PROFILE_PACKAGES", ""),
  "SANDBOX_GROUPS": os.environ.get("SANDBOX_GROUPS", ""),
  "SANDBOX_SUDO_LINE": os.environ.get("SANDBOX_SUDO_LINE", ""),
  "SANDBOX_USER": os.environ.get("SANDBOX_USER", ""),
  "SSH_AUTHORIZED_KEYS_BLOCK": os.environ.get("SSH_AUTHORIZED_KEYS_BLOCK", ""),
  "SSH_ENABLE_COMMANDS": os.environ.get("SSH_ENABLE_COMMANDS", ""),
}
for key, value in replacements.items():
  template = template.replace(f"__{key}__", value)

template = template.replace(
  "    # __SANDBOX_OPTIONAL_USER_FIELDS__",
  (os.environ.get("SANDBOX_SUDO_LINE", "") + os.environ.get("SSH_AUTHORIZED_KEYS_BLOCK", "")).rstrip("\n"),
)
template = template.replace("  # __PROFILE_PACKAGES__", os.environ.get("PROFILE_PACKAGES", ""))
template = template.replace(
  "  # __OPTIONAL_ENABLE_COMMANDS__",
  (os.environ.get("SSH_ENABLE_COMMANDS", "") + os.environ.get("PROFILE_ENABLE_COMMANDS", "")).rstrip("\n"),
)
sys.stdout.write(template)
PY

if [ ! -f "$DOWNLOAD_PATH" ]; then
  log_step "Downloading Ubuntu base image to $DOWNLOAD_PATH"
  require_cmd curl
  curl -L "$BASE_IMAGE_URL" -o "$DOWNLOAD_PATH"
else
  log_step "Using cached Ubuntu base image: $DOWNLOAD_PATH"
fi

python3 - "$DOWNLOAD_PATH" <<'PY' > "$WORK_DIR/base-image.sha256"
import hashlib
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
sha = hashlib.sha256(path.read_bytes()).hexdigest()
print(sha)
PY
base_image_sha="$(tr -d '\n' < "$WORK_DIR/base-image.sha256")"
if [ -n "$EXPECTED_BASE_IMAGE_SHA256" ] && [ "$base_image_sha" != "$EXPECTED_BASE_IMAGE_SHA256" ]; then
  echo "base image checksum mismatch: expected $EXPECTED_BASE_IMAGE_SHA256 got $base_image_sha" >&2
  exit 1
fi

cp "$DOWNLOAD_PATH" "$WORK_DIR/base.qcow2"

log_step "Preparing cloud-init seed image"
sed \
  -e "s/__INSTANCE_ID__/or3-build/g" \
  -e "s/__HOSTNAME__/or3-build/g" \
  "$CLOUD_INIT_DIR/meta-data.tpl" > "$WORK_DIR/meta-data"

cloud-localds "$WORK_DIR/seed.img" "$WORK_DIR/user-data" "$WORK_DIR/meta-data"
log_step "Creating guest root and workspace disks"
"$QEMU_IMG_BINARY" create -f qcow2 -F qcow2 -b "$WORK_DIR/base.qcow2" "$OUTPUT_IMAGE" 20G >/dev/null
"$QEMU_IMG_BINARY" create -f raw "$WORK_DIR/workspace.img" 10G >/dev/null
"$(command -v mkfs.ext4)" -F -L or3-build-ws "$WORK_DIR/workspace.img" >/dev/null

net_device="virtio-net-pci"
if [[ "$QEMU_BINARY" == *aarch64* ]]; then
  net_device="virtio-net-device"
fi

log_step "Booting guest under QEMU"
"$QEMU_BINARY" \
  -daemonize \
  -pidfile "$WORK_DIR/qemu.pid" \
  -monitor "unix:$WORK_DIR/monitor.sock,server,nowait" \
  -serial "file:$WORK_DIR/serial.log" \
  -display none \
  -accel "$QEMU_ACCEL" \
  -m 2048 \
  -smp 2 \
  -device virtio-serial \
  -chardev "socket,id=agent0,path=$WORK_DIR/agent.sock,server=on,wait=off" \
  -device "virtserialport,chardev=agent0,name=org.or3.guest_agent" \
  -drive "if=virtio,file=$OUTPUT_IMAGE,format=qcow2" \
  -drive "if=virtio,file=$WORK_DIR/workspace.img,format=raw" \
  -drive "if=virtio,file=$WORK_DIR/seed.img,format=raw,readonly=on" \
  -netdev "user,id=net0" \
  -device "$net_device,netdev=net0"

agent_rpc() {
	local op="$1"
	local payload="${2:-null}"
	OR3_AGENT_OP="$op" OR3_AGENT_PAYLOAD="$payload" OR3_AGENT_SOCKET_PATH="$WORK_DIR/agent.sock" python3 - <<'PY'
import json
import os
import socket
import struct
import sys
import time

sock_path = os.environ["OR3_AGENT_SOCKET_PATH"]
op = os.environ["OR3_AGENT_OP"]
payload = os.environ.get("OR3_AGENT_PAYLOAD", "null")
message = {"op": op}
message["id"] = f"build-{time.time_ns()}"
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

agent_exec_stdout() {
  local command_json="$1"
  OR3_AGENT_PAYLOAD="$command_json" OR3_AGENT_SOCKET_PATH="$WORK_DIR/agent.sock" python3 - <<'PY'
import base64
import json
import os
import socket
import struct
import sys
import time

sock_path = os.environ["OR3_AGENT_SOCKET_PATH"]
payload = json.loads(os.environ["OR3_AGENT_PAYLOAD"])

def send(conn, message):
  raw = json.dumps(message).encode("utf-8")
  conn.sendall(struct.pack(">I", len(raw)) + raw)

def recv(conn):
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
  return json.loads(data.decode("utf-8"))

with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
  conn.settimeout(30)
  conn.connect(sock_path)
  send(conn, {"op": "exec_start", "id": f"build-{time.time_ns()}", "result": payload})
  reply = recv(conn)
  if not reply.get("ok", False):
    raise SystemExit(reply.get("error") or "guest exec_start failed")
  exec_id = (reply.get("result") or {}).get("exec_id", "").strip()
  if not exec_id:
    raise SystemExit("guest agent returned empty exec id")

  stdout_parts = []
  stderr_parts = []
  while True:
    message = recv(conn)
    if not message.get("ok", False):
      raise SystemExit(message.get("error") or "guest exec_event failed")
    if message.get("op") != "exec_event":
      raise SystemExit(f"unexpected guest exec stream op: {message.get('op')!r}")
    event = message.get("result") or {}
    if event.get("exec_id") != exec_id:
      raise SystemExit(f"unexpected guest exec id: {event.get('exec_id')!r}")
    stream = event.get("stream", "")
    data = event.get("data", "")
    if data:
      decoded = base64.b64decode(data)
      if stream == "stdout":
        stdout_parts.append(decoded)
      elif stream == "stderr":
        stderr_parts.append(decoded)
    terminal = event.get("result")
    if terminal is None:
      continue
    status = str(terminal.get("status", ""))
    if status != "succeeded":
      stderr_preview = terminal.get("stderr_preview", "")
      if stderr_preview:
        print(stderr_preview, file=sys.stderr)
      elif stderr_parts:
        print(b"".join(stderr_parts).decode("utf-8", errors="replace"), file=sys.stderr)
      raise SystemExit(f"guest exec failed with status {status!r}")
    sys.stdout.write(b"".join(stdout_parts).decode("utf-8", errors="replace"))
    break
PY
}

wait_for_agent_ready() {
  OR3_AGENT_SOCKET_PATH="$WORK_DIR/agent.sock" python3 - <<'PY'
import json
import os
import socket
import struct
import sys
import time

sock_path = os.environ["OR3_AGENT_SOCKET_PATH"]
deadline = time.time() + (120 * 2)
attempt = 0

while time.time() < deadline:
    attempt += 1
    try:
        print(f"[{time.strftime('%H:%M:%S', time.gmtime())}] Waiting for guest readiness: attempt {attempt}/120", file=sys.stderr)
        request = {"op": "ping", "id": f"build-{time.time_ns()}"}
        raw = json.dumps(request).encode("utf-8")
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
            conn.settimeout(5)
            conn.connect(sock_path)
            conn.sendall(struct.pack(">I", len(raw)) + raw)
            header = conn.recv(4)
            if len(header) != 4:
                raise RuntimeError("short guest-agent header")
            length = struct.unpack(">I", header)[0]
            data = b""
            while len(data) < length:
                chunk = conn.recv(length - len(data))
                if not chunk:
                    raise RuntimeError("short guest-agent body")
                data += chunk
        reply = json.loads(data.decode("utf-8"))
        if reply.get("ok"):
            result = reply.get("result") or {}
            if result.get("ready") is True:
                print(f"[{time.strftime('%H:%M:%S', time.gmtime())}] Guest agent reported ready", file=sys.stderr)
                sys.exit(0)
    except (OSError, RuntimeError, ValueError, json.JSONDecodeError):
        pass
    time.sleep(2)

print(f"[{time.strftime('%H:%M:%S', time.gmtime())}] Guest agent did not report ready before timeout", file=sys.stderr)
sys.exit(1)
PY
}

verify_profile_artifacts() {
  local packages_json ssh_present_json capabilities_json allowed_features_json
  packages_json="$(jq -c '.packages // []' "$WORK_DIR/profile.json")"
  ssh_present_json="$(jq -c '.ssh_present // false' "$WORK_DIR/profile.json")"
  capabilities_json="$(jq -c '.capabilities // []' "$WORK_DIR/profile.json")"
  allowed_features_json="$(jq -c '.allowed_features // []' "$WORK_DIR/profile.json")"

  OR3_PACKAGES_JSON="$packages_json" \
  OR3_SSH_PRESENT_JSON="$ssh_present_json" \
  OR3_CAPABILITIES_JSON="$capabilities_json" \
  OR3_ALLOWED_FEATURES_JSON="$allowed_features_json" \
  python3 - <<'PY' > "$WORK_DIR/verify-profile.sh"
import json
import os
from shlex import quote

packages = json.loads(os.environ.get("OR3_PACKAGES_JSON", "[]"))
ssh_present = json.loads(os.environ.get("OR3_SSH_PRESENT_JSON", "false"))
capabilities = set(json.loads(os.environ.get("OR3_CAPABILITIES_JSON", "[]")))
allowed_features = set(json.loads(os.environ.get("OR3_ALLOWED_FEATURES_JSON", "[]")))

lines = ["set -eu"]
if packages:
    lines.append("dpkg-query -W -f='${Package}\t${Version}\\n' " + " ".join(quote(pkg) for pkg in packages) + " | sort")
else:
    lines.append(":")
if ssh_present:
    lines.append("command -v sshd >/dev/null")
    lines.append("systemctl is-enabled ssh >/dev/null")
else:
    lines.append("if command -v sshd >/dev/null 2>&1; then echo 'unexpected sshd present for non-ssh profile' >&2; exit 1; fi")
if "container" in capabilities or "docker" in allowed_features:
    lines.append("command -v docker >/dev/null")
    lines.append("systemctl is-enabled docker >/dev/null")
print("\n".join(lines))
PY
  chmod +x "$WORK_DIR/verify-profile.sh"

  local verify_result
  verify_result="$(agent_exec_stdout "$(jq -cn --arg script "$(cat "$WORK_DIR/verify-profile.sh")" '{command:["sh","-lc",$script],cwd:"/"}')")"
  printf '%s\n' "$verify_result" > "$PACKAGE_INVENTORY_OUTPUT"
  if [ -s "$PACKAGE_INVENTORY_OUTPUT" ]; then
    sed -i.bak '/^$/d' "$PACKAGE_INVENTORY_OUTPUT" && rm -f "$PACKAGE_INVENTORY_OUTPUT.bak"
  fi
}

if ! wait_for_agent_ready; then
  echo "guest image bootstrap did not reach readiness" >&2
  if [ -f "$WORK_DIR/serial.log" ]; then
    tail -n 50 "$WORK_DIR/serial.log" >&2 || true
  fi
  exit 1
fi

# Allow the guest agent to fully re-open the virtio port after the
# readiness-probe connection closes.  Without this pause the next
# host-side connect can race with the guest-side fd re-open, causing
# the exec_start message to be silently dropped by the virtio buffer.
sleep 3

log_step "Verifying guest profile contents and package inventory"
verify_profile_artifacts

python3 - "$WORK_DIR/profile.json" <<'PY' > "$WORK_DIR/profile.sha256"
import hashlib
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
sha = hashlib.sha256(path.read_bytes()).hexdigest()
print(sha)
PY
resolved_profile_sha="$(tr -d '\n' < "$WORK_DIR/profile.sha256")"

python3 - "$PACKAGE_INVENTORY_OUTPUT" <<'PY' > "$WORK_DIR/package-inventory.sha256"
import hashlib
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
sha = hashlib.sha256(path.read_bytes()).hexdigest()
print(sha)
PY
package_inventory_sha="$(tr -d '\n' < "$WORK_DIR/package-inventory.sha256")"

if [ "$ssh_present" = "true" ]; then
  log_step "Capturing guest SSH host public key"
  printf '%s\n' "$(agent_exec_stdout "$(jq -cn '{command:["sh","-lc","cat /etc/ssh/ssh_host_ed25519_key.pub"],cwd:"/"}')" | tr -d '\r')" > "$SSH_HOST_PUBLIC_KEY_OUTPUT"
	if [ ! -s "$SSH_HOST_PUBLIC_KEY_OUTPUT" ]; then
		echo "failed to capture guest SSH host public key" >&2
		exit 1
	fi
fi

log_step "Shutting down guest"
agent_rpc shutdown '{"reboot":false}' >/dev/null 2>&1 || true
if [ -f "$WORK_DIR/qemu.pid" ]; then
	pid="$(cat "$WORK_DIR/qemu.pid")"
	for _ in $(seq 1 30); do
		if ! kill -0 "$pid" >/dev/null 2>&1; then
			break
		fi
		sleep 1
	done
	if kill -0 "$pid" >/dev/null 2>&1; then
		kill "$pid" >/dev/null 2>&1 || true
		sleep 2
	fi
fi

log_step "Flattening final qcow2 image"
"$QEMU_IMG_BINARY" rebase -f qcow2 -b "" "$OUTPUT_IMAGE" >/dev/null

log_step "Computing checksums and writing image contract"
python3 - "$OUTPUT_IMAGE" <<'PY' > "$WORK_DIR/image.sha256"
import hashlib
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
sha = hashlib.sha256(path.read_bytes()).hexdigest()
print(sha)
PY
image_sha="$(tr -d '\n' < "$WORK_DIR/image.sha256")"

cp "$WORK_DIR/profile.json" "$PROFILE_RESOLVED_OUTPUT"

jq -n \
	--slurpfile manifest "$WORK_DIR/profile.json" \
	--arg image_path "$OUTPUT_IMAGE" \
	--arg image_sha "$image_sha" \
	--arg build_version "$BUILD_VERSION" \
	--arg git_sha "$GIT_SHA" \
	--arg base_image_source "$BASE_IMAGE_URL" \
	--arg base_image_sha "$base_image_sha" \
	--arg base_image_expected_sha "$EXPECTED_BASE_IMAGE_SHA256" \
	--arg resolved_profile_sha "$resolved_profile_sha" \
	--arg package_inventory_sha "$package_inventory_sha" \
	'{
		contract_version: "1",
		image_path: $image_path,
		image_sha256: $image_sha,
		build_version: $build_version,
		git_sha: $git_sha,
		profile: $manifest[0].profile,
		capabilities: ($manifest[0].capabilities // []),
		allowed_features: ($manifest[0].allowed_features // []),
		control: ($manifest[0].control // {}),
		workspace_contract_version: ($manifest[0].workspace_contract_version // "1"),
		ssh_present: ($manifest[0].ssh_present // false),
		dangerous: ($manifest[0].dangerous // false),
		debug: ($manifest[0].debug // false),
		package_inventory: ($manifest[0].packages // []),
		provenance: {
			base_image_source: $base_image_source,
			base_image_sha256: $base_image_sha,
			base_image_expected_sha256: $base_image_expected_sha,
			resolved_profile_sha256: $resolved_profile_sha,
			package_inventory_sha256: $package_inventory_sha
		}
	}' > "$CONTRACT_OUTPUT"

cat <<EOF
Prepared guest image:
  $OUTPUT_IMAGE

Resolved profile manifest:
  $PROFILE_RESOLVED_OUTPUT

Package inventory (actual guest package versions):
  $PACKAGE_INVENTORY_OUTPUT

Host-side image contract:
  $CONTRACT_OUTPUT

Selected profile:
  $profile_name

Control mode:
  $(jq -r '.control.mode' "$WORK_DIR/profile.json")

Workspace contract version:
  $(jq -r '.workspace_contract_version' "$WORK_DIR/profile.json")

Declared capabilities:
  $(jq -r '(.capabilities // []) | join(", ")' "$WORK_DIR/profile.json")

The image has been bootstrapped once with cloud-init, the guest agent, and the guest bootstrap service.

EOF

if [ "$ssh_present" = "true" ]; then
	cat <<EOF

Guest SSH host public key:
  $SSH_HOST_PUBLIC_KEY_OUTPUT

EOF
fi

cat <<EOF

Next step:
  The build already ran a guest-agent smoke/verification pass against the selected profile.
  Run images/guest/smoke-ssh.sh only for debug/ssh-compat images.
  Use the generated sidecar contract with SANDBOX_QEMU_BASE_IMAGE_PATH and SANDBOX_QEMU_ALLOWED_BASE_IMAGE_PATHS.
EOF
