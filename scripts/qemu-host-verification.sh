#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROFILE_LABEL="core"
CONTROL_MODE="${SANDBOX_QEMU_CONTROL_MODE:-agent}"
GO_BIN="${GO_BIN:-}"

usage() {
  cat <<'EOF'
usage: ./scripts/qemu-host-verification.sh [--profile <core|runtime|browser|container|debug>] [--control-mode <agent|ssh-compat>]

Runs the host-gated QEMU integration verification entry points when the required
QEMU environment is present. Agent mode is the normal production path; use
ssh-compat only for explicit debug or rescue verification. If the environment is
incomplete, the wrapper prints a skip message and exits successfully.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile)
      PROFILE_LABEL="${2:-}"
      shift 2
      ;;
    --control-mode)
      CONTROL_MODE="${2:-}"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$GO_BIN" ]]; then
  if command -v go >/dev/null 2>&1; then
    GO_BIN="$(command -v go)"
  elif [[ -x /usr/local/go/bin/go ]]; then
    GO_BIN=/usr/local/go/bin/go
  else
    echo "go binary not found; set GO_BIN or install Go" >&2
    exit 127
  fi
fi

export SANDBOX_QEMU_CONTROL_MODE="$CONTROL_MODE"
: "${SANDBOX_QEMU_ACCEL:=auto}"
export SANDBOX_QEMU_ACCEL

missing=()
for name in SANDBOX_QEMU_BINARY SANDBOX_QEMU_BASE_IMAGE_PATH; do
  if [[ -z "${!name:-}" ]]; then
    missing+=("$name")
  fi
done
if [[ "$CONTROL_MODE" == "ssh-compat" ]]; then
  for name in SANDBOX_QEMU_SSH_USER SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH SANDBOX_QEMU_SSH_HOST_KEY_PATH; do
    if [[ -z "${!name:-}" ]]; then
      missing+=("$name")
    fi
  done
fi

if [[ ${#missing[@]} -gt 0 ]]; then
  printf '[qemu-host-verification] skipping: set %s\n' "$(IFS=', '; echo "${missing[*]}")"
  exit 0
fi

for name in qemu-img mkfs.ext4; do
  if ! command -v "$name" >/dev/null 2>&1; then
    printf '[qemu-host-verification] skipping: required host command %s is missing\n' "$name"
    exit 0
  fi
done

printf '[qemu-host-verification] requested_profile=%s control_mode=%s\n' "$PROFILE_LABEL" "$CONTROL_MODE"
printf '[qemu-host-verification] base_image=%s accel=%s\n' "$SANDBOX_QEMU_BASE_IMAGE_PATH" "$SANDBOX_QEMU_ACCEL"
if [[ "$CONTROL_MODE" == "ssh-compat" ]]; then
  echo '[qemu-host-verification] note: ssh-compat verification is debug-only; agent-default substrate checks may skip and the guest image contract remains authoritative.'
fi

test_regex='TestHost(CoreSubstrateAndAgentProtocol|DiskFullAndWorkspacePersistence|IsolationBoundaries|RestartRecoveryAndProfileWorkloads|SandboxLocalBridge)$'
cd "$ROOT_DIR"
exec "$GO_BIN" test ./internal/runtime/qemu -run "$test_regex"
