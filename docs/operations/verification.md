# Production Verification

Production-facing docs and claims in this repo should be gated on passing tests and documented operator drills.

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
- `SANDBOX_QEMU_SSH_USER`
- `SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH`
- optional: `SANDBOX_QEMU_ACCEL`

Run them only on hosts prepared for QEMU guest execution:

```bash
/usr/local/go/bin/go test ./internal/runtime/qemu -run 'TestHost(DiskFullAndWorkspacePersistence|WorkloadClaimsAndRestartDurability)$'
```

These are operator drill and pre-release verification tests, not the default CI smoke path.

## Recommended operator drills

Before using “production-ready” language for a deployment, run at least:

1. the CI-friendly smoke path
2. one daemon restart and reconcile drill
3. one snapshot create and restore drill
4. one QEMU host integration drill in a prepared environment
