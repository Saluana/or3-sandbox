# Production Verification

Production-readiness claims in this repo require both automated checks and operator evidence. Start with the threat model in [docs/operations/qemu-production-threat-model.md](docs/operations/qemu-production-threat-model.md): if a verification step does not support that boundary, it is not enough to justify a production claim.

## Fast smoke gate

Use the shipped package sanity gate first:

```bash
./scripts/production-smoke.sh
```

That command runs the production-critical non-host-specific packages:

- `./internal/config`
- `./internal/auth`
- `./internal/service`
- `./internal/api`
- `./cmd/sandboxctl`

This is the intended CI-friendly smoke path. It is fast, host-agnostic, and useful for catching control-plane regressions early, but it is **not** sufficient proof that a Linux/KVM deployment is production-ready.

If you prefer not to use the script:

```bash
/usr/local/go/bin/go test ./internal/config ./internal/auth ./internal/service ./internal/api ./cmd/sandboxctl
```

## Gated QEMU host verification

Run the host-gated wrapper on a prepared Linux/KVM host:

```bash
./scripts/qemu-host-verification.sh --profile core --control-mode agent
```

Required environment:

- always: `SANDBOX_QEMU_BINARY`, `SANDBOX_QEMU_BASE_IMAGE_PATH`
- optional: `SANDBOX_QEMU_ACCEL` (defaults to `auto`), `SANDBOX_QEMU_CONTROL_MODE`
- `ssh-compat` only: `SANDBOX_QEMU_SSH_USER`, `SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH`, `SANDBOX_QEMU_SSH_HOST_KEY_PATH`

Wrapper behavior:

- prints the requested profile label and control mode so operator evidence is tied to the intended runtime contract
- exits with an explicit skip message when the required QEMU environment is missing
- runs the existing `go test ./internal/runtime/qemu` host entry points without introducing a second harness
- leaves pass/fail detail to the underlying Go tests so host misconfiguration and isolation regressions stay visible

The guest image sidecar contract remains authoritative. The wrapper surfaces the operator’s intended profile/control-mode inputs, but the host tests still validate the contract actually attached to `SANDBOX_QEMU_BASE_IMAGE_PATH`.

## Verification tiers

Treat the host verification suite as three distinct layers:

### Core substrate verification

The `core` gate verifies the production-default substrate without relying on SSH/debug assumptions:

- boot and readiness
- guest-agent protocol negotiation
- exec, PTY, and file transfer boundaries
- absence of host docker socket exposure and obvious host path leakage
- conservative stop/start behavior and workspace persistence
- disk-pressure recovery

This is the minimum host verification layer needed before using “production-ready” language for hostile-workload QEMU deployments.

### Browser and container capability verification

Heavier profiles add profile-specific checks only when the selected guest image advertises those capabilities. Examples include:

- runtime-style toolchain persistence such as Git, Python, and npm
- headless browser execution for `browser`/debug-style images
- guest container engine execution for `container`/debug-style images

These checks confirm workload claims for those profiles. They do not replace the `core` substrate gate.

### SSH/debug compatibility checks

`ssh-compat` and debug images are compatibility or break-glass verification paths. They are useful for migration, debugging, or explicit operator exceptions, but they are not the default hostile-production verification gate.

## What the suite proves

The combined smoke path, host wrapper, and operator drills give evidence that:

- the control plane packages behave as expected
- the prepared Linux/KVM host can boot the selected guest image and satisfy its control contract
- the production-default guest-agent path is healthy
- obvious repo-specific isolation assumptions still hold, such as no host docker socket exposure or host workspace bind leakage into the guest
- restart, snapshot, and storage-pressure behavior remains conservative enough for operator recovery workflows

## What the suite does not prove

The verification suite does **not** provide:

- a formal VM-escape proof
- proof that the host kernel, hypervisor, firmware, or hardware posture is independently hardened
- a blanket claim that Docker-backed hostile multi-tenancy is production-safe
- a reason to treat SSH/debug compatibility checks as equivalent to the production-default guest-agent gate

Host hardening, kernel posture, hypervisor patching, and operational isolation outside the sandbox process remain external production dependencies.

## Production doctor

Before calling a Linux/KVM host production-ready, run:

```bash
go run ./cmd/sandboxctl doctor --production-qemu
```

Output classes:

- `PASS` — expected production posture is present
- `WARN` — usable for development or pre-production, but review before making a production claim
- `FAIL` — blocking issue; do not treat the host as production-ready until it is fixed

The doctor verifies runtime/auth posture, KVM/QEMU availability, free-space posture for the database/storage/snapshot roots, tunnel-signing-key posture, basic cgroup-controller posture, and approved guest image sidecar contracts.

## Guest image verification

Use the image-local smoke scripts after building or updating guest profiles:

```bash
images/guest/smoke-agent.sh
PROFILE=debug images/guest/build-base-image.sh
BASE_IMAGE=$PWD/images/guest/or3-guest-debug.qcow2 images/guest/smoke-ssh.sh
./scripts/qemu-production-smoke.sh
```

Use `smoke-agent.sh` for production-default profiles and reserve `smoke-ssh.sh` for explicit compatibility/debug images.

For release promotion, retain the full guest-image bundle together:

- the qcow2 image
- the `*.or3.json` sidecar
- the resolved profile manifest copy
- the package inventory text file

The sidecar provenance fields are a promotion record, not a proof of bit-for-bit reproducibility.

## Operator drill flow

Existing `sandboxctl` commands and API endpoints are sufficient for the read-only verification flow; no extra CLI helper is required today.

### Read-only inspection drill

Run and record:

```bash
go run ./cmd/sandboxctl runtime-health
go run ./cmd/sandboxctl quota
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/v1/runtime/capacity"
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/metrics"
```

Expected evidence:

- `runtime-health` shows the intended backend and current sandbox posture
- `quota` and `/v1/runtime/capacity` show storage/running pressure and profile mix
- `/metrics` is reachable only to authorized callers and includes the expected counters

Cleanup: none.

### Disposable runtime-contract drill

Create a disposable sandbox with the intended contract, record the contract fields, then delete it:

```bash
sandbox_json="$(go run ./cmd/sandboxctl create --image "$SANDBOX_QEMU_BASE_IMAGE_PATH" --profile core --cpu 1 --memory-mb 1024 --disk-mb 2048 --network internet-disabled --allow-tunnels=false --start=true)"
sandbox_id="$(printf '%s' "$sandbox_json" | jq -r '.id')"
go run ./cmd/sandboxctl inspect "$sandbox_id" | jq '{id, profile, control_mode, control_protocol_version, workspace_contract_version}'
go run ./cmd/sandboxctl exec "$sandbox_id" -- sh -lc 'printf verification-ok'
go run ./cmd/sandboxctl delete "$sandbox_id"
```

Expected evidence:

- the inspect output records `profile`, `control_mode`, and contract versions
- the exec output confirms the selected runtime contract is usable

Cleanup: delete the disposable sandbox even on failure.

### Disposable preset drill

Run a shipped preset with automatic cleanup when the preset fits the release scope:

```bash
go run ./cmd/sandboxctl preset run playwright --cleanup on-success
```

Expected evidence:

- the preset reports readiness and artifact locations without manual sandbox bookkeeping

Cleanup:

- `--cleanup on-success` removes the sandbox on success
- rerun with `--cleanup always` for stricter hygiene, or `--cleanup never` only when you need to inspect failures manually

### Host-gated smoke, abuse, and recovery drills

The repository also includes host-gated operator drill scripts:

- `./scripts/qemu-production-smoke.sh`
  - runs `sandboxctl doctor --production-qemu`
  - exercises core-profile exec, file transfer, suspend/resume, snapshot create/restore, and optional daemon restart reconciliation
- `./scripts/qemu-resource-abuse.sh`
  - runs bounded memory/disk/file-count/PID/stdout abuse scenarios against a core-profile sandbox
  - captures runtime health, quota, and optional metrics evidence
- `./scripts/qemu-recovery-drill.sh`
  - is disruptive and guarded by `OR3_ALLOW_DISRUPTIVE=1`
  - verifies restart durability when `SANDBOXD_RESTART_COMMAND` is supplied and checks conservative stopped-state restore behavior, partial restore failure handling, and owned-root cleanup

Expected evidence:

- successful core smoke output
- explicit denial or degradation when abuse limits are reached
- conservative post-restart and post-restore state

Cleanup:

- the smoke and abuse scripts clean up disposable sandboxes automatically
- the recovery drill is disruptive; run it only when the change window allows it

## Required production gate

Before using “production-ready” language for a deployment, run at least:

1. `./scripts/production-smoke.sh`
2. `go run ./cmd/sandboxctl doctor --production-qemu`
3. `./scripts/qemu-host-verification.sh --profile core --control-mode agent`
4. `./scripts/qemu-production-smoke.sh`
5. `./scripts/qemu-resource-abuse.sh`
6. one restart/recovery drill via `./scripts/qemu-recovery-drill.sh` during an approved window

For runtime-affecting changes, treat these as the release gate rather than optional extra coverage.
