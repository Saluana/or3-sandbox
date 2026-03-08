# Tutorial 3: Trying the QEMU Runtime

This tutorial is for people who already understand the Docker path.

If you are new to the project, stop here and do the first two tutorials first.

## What makes QEMU different?

With Docker, each sandbox is a container.

With QEMU, each sandbox is a guest machine.

That means setup is slower, but the isolation story is closer to a real VM.

## Important warning

The QEMU path is more advanced and still being hardened.

That means:

- setup is less beginner-friendly
- you need a prepared guest image
- you need SSH access set up correctly
- suspend and resume are intentionally not supported in the first pass

## Step 1: Prepare a guest image

Read the guest image notes:

- `images/guest/README.md`

That directory includes scripts and templates for preparing a guest image and checking SSH readiness.

The guest is expected to support:

- your chosen SSH user
- your SSH key
- the readiness marker `/var/lib/or3/bootstrap.ready`
- a mounted `/workspace`

## Step 2: Set QEMU environment variables

Example for macOS on Apple Silicon:

```bash
export SANDBOX_RUNTIME=qemu
export SANDBOX_QEMU_BINARY=qemu-system-aarch64
export SANDBOX_QEMU_ACCEL=hvf
export SANDBOX_QEMU_BASE_IMAGE_PATH=$PWD/images/guest/base.qcow2
export SANDBOX_QEMU_SSH_USER=or3
export SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH=$HOME/.ssh/or3-sandbox
```

Example for Linux may use:

```bash
export SANDBOX_QEMU_ACCEL=kvm
```

Use values that match your host and image.

## Step 3: Start the daemon

```bash
go run ./cmd/sandboxd \
  -listen :8080 \
  -db ./data/sandbox.db \
  -storage-root ./data/storage \
  -snapshot-root ./data/snapshots
```

If validation fails, the startup error is usually telling you exactly what is missing.

Common causes are:

- bad QEMU binary name
- missing base image file
- unreadable SSH key
- unavailable accelerator

## Step 4: Set client variables

```bash
export SANDBOX_API=http://127.0.0.1:8080
export SANDBOX_TOKEN=dev-token
```

## Step 5: Create a sandbox

```bash
go run ./cmd/sandboxctl create --start
```

The daemon will:

- create sandbox disk files
- boot the guest
- wait for SSH
- wait for readiness
- then mark the sandbox as running

## Step 6: Run a command

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc 'echo hello from qemu'
```

## Step 7: Check runtime health

```bash
go run ./cmd/sandboxctl runtime-health
```

This is especially useful on QEMU because it helps show whether the guest is reachable and healthy.

## Step 8: Know the current limit

These commands are expected to return a clear error with the current first-pass backend:

```bash
go run ./cmd/sandboxctl suspend <sandbox-id>
go run ./cmd/sandboxctl resume <sandbox-id>
```

That is normal for the current QEMU implementation.

## Final advice

Treat QEMU as the "study the future architecture" path.

Treat Docker as the "get real work done quickly while learning" path.
