# Production Verification

Production-facing docs and claims in this repo should be gated on passing tests and documented operator drills.

Start with the threat model in [docs/operations/qemu-production-threat-model.md](docs/operations/qemu-production-threat-model.md). If a verification step does not support the boundary described there, it should not be used to justify a production claim.

## CI-friendly smoke path

Use the shipped smoke entrypoint:

```bash
./scripts/production-smoke.sh
```

That command runs the production-critical non-host-specific package tests:

- `./internal/config`
- `./internal/auth`
- `./internal/service`
- `./internal/api`
- `./cmd/sandboxctl`

It is the intended CI-friendly verification path because it does not require a host QEMU guest image and it tolerates Docker-gated tests being skipped when `/var/run/docker.sock` is absent.

## Direct test command

If you prefer not to use the script:

```bash
/usr/local/go/bin/go test ./internal/config ./internal/auth ./internal/service ./internal/api ./cmd/sandboxctl
```

## Host QEMU integration coverage

Host QEMU coverage is still important, but it is intentionally environment-gated.

These tests cover:

- disk-full behavior and workspace persistence
- restart durability
- workload claims such as Git, Python, npm, headless browser use, guest container engine use, and supervised background services

Prerequisite environment variables:

- `SANDBOX_QEMU_BINARY`
- `SANDBOX_QEMU_BASE_IMAGE_PATH`
- optional: `SANDBOX_QEMU_ACCEL`

Add the SSH variables only when you are explicitly testing `ssh-compat` / `debug` images.

Run them only on hosts prepared for QEMU guest execution:

```bash
/usr/local/go/bin/go test ./internal/runtime/qemu -run 'TestHost(DiskFullAndWorkspacePersistence|WorkloadClaimsAndRestartDurability)$'
```

These are operator drill and pre-release verification tests, not the default CI smoke path.

## Production doctor

Before calling a Linux/KVM host production-ready, run:

```bash
go run ./cmd/sandboxctl doctor --production-qemu
```

Output classes:

- `PASS`
	- expected production posture is present
- `WARN`
	- the host may be usable for development or pre-production, but the finding should be reviewed before a production claim
- `FAIL`
	- blocking issue; do not treat the host as production-ready until it is fixed

The doctor verifies runtime/auth posture, KVM/QEMU availability, filesystem free-space posture for the database/storage/snapshot roots, tunnel-signing-key posture, basic cgroup-controller posture, and approved guest image sidecar contracts.

Capacity and metrics outputs should also show the currently admitted guest profile mix and declared capability mix so operators can spot accidental drift away from the intended `core`-heavy production posture.

## Guest image verification

Use the image-local smoke scripts after building or updating guest profiles:

```bash
images/guest/smoke-agent.sh
PROFILE=debug images/guest/build-base-image.sh
BASE_IMAGE=$PWD/images/guest/or3-guest-debug.qcow2 images/guest/smoke-ssh.sh
./scripts/qemu-production-smoke.sh
```

Use `smoke-agent.sh` for production-default profiles and reserve `smoke-ssh.sh` for the explicit debug/compatibility image.

For release promotion, retain the full guest-image bundle together:

- the qcow2 image
- the `*.or3.json` sidecar
- the resolved profile manifest copy
- the package inventory text file

The sidecar provenance fields are a promotion record, not a proof of bit-for-bit reproducibility.
Use `EXPECTED_BASE_IMAGE_SHA256` during release builds when the promotion process needs to pin the upstream cloud-image input explicitly.

The repository now also includes host-gated operator drill scripts:

- `./scripts/qemu-production-smoke.sh`
	- runs `sandboxctl doctor --production-qemu`
	- exercises core-profile exec, file transfer, suspend/resume, snapshot create/restore, and optional daemon restart reconciliation
- `./scripts/qemu-recovery-drill.sh`
	- disruptive drill guarded by `OR3_ALLOW_DISRUPTIVE=1`
	- verifies restart durability when `SANDBOXD_RESTART_COMMAND` is supplied and checks conservative stopped-state restore behavior, partial restore failure handling, and owned-root cleanup
- `./scripts/qemu-resource-abuse.sh`
	- bounded memory/disk/file-count/PID/stdout abuse scenarios against a core-profile sandbox
	- includes startup-spam and fairness probes; set `EXPECT_ADMISSION_LIMITS=1` when the host is expected to deny bursty starts

These scripts are intentionally host-gated and may skip or refuse destructive steps until the required environment variables are provided.

## Recommended operator drills

Before using “production-ready” language for a deployment, run at least:

1. the CI-friendly smoke path
2. `go run ./cmd/sandboxctl doctor --production-qemu` on the prepared Linux/KVM host
3. `./scripts/qemu-production-smoke.sh` on the prepared Linux/KVM host
4. one daemon restart and reconcile drill via `./scripts/qemu-recovery-drill.sh`
5. one bounded abuse drill via `./scripts/qemu-resource-abuse.sh`
6. one QEMU host integration drill in a prepared environment

For runtime-affecting changes, treat the smoke, recovery, and abuse scripts as a release gate rather than optional extra coverage.
