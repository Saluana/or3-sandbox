# Tutorial 3: Trying the Kata Runtime

This tutorial is for Linux hosts that already have containerd and Kata Containers available.

If you are new to the project, do [Tutorial 1](first-sandbox.md) first.

If you want the simplest VM-backed runtime path that still uses normal container images, Kata is the right next stop.

## What makes Kata different?

With Docker, each sandbox is a container sharing the host kernel.

With Kata, each sandbox still starts from a container image, but it runs inside a lightweight microVM through containerd + Kata Containers.

That means:

- the image workflow still feels container-like
- the isolation posture is VM-backed
- the host must be Linux
- the current implementation does **not** support `suspend` or `resume`

## Step 1: Check host prerequisites

Confirm these are available on the Linux host:

```bash
ctr version
test -S /run/containerd/containerd.sock && echo containerd-socket-ok
```

You also need a working Kata runtime class installed in containerd, commonly `io.containerd.kata.v2`.

## Step 2: Start the daemon with Kata as default

In terminal 1:

```bash
cd /Users/brendon/Documents/or3-sandbox

SANDBOX_ENABLED_RUNTIMES=containerd-kata-professional \
SANDBOX_DEFAULT_RUNTIME=containerd-kata-professional \
SANDBOX_KATA_BINARY=ctr \
SANDBOX_KATA_RUNTIME_CLASS=io.containerd.kata.v2 \
SANDBOX_KATA_CONTAINERD_SOCKET=/run/containerd/containerd.sock \
go run ./cmd/sandboxd \
  -listen :8080 \
  -db ./data/sandbox.db \
  -storage-root ./data/storage \
  -snapshot-root ./data/snapshots
```

Leave that terminal running.

If startup fails, the most common reasons are:

- `ctr` is missing
- the containerd socket path is wrong
- the configured Kata runtime class is unavailable
- you are not on a Linux host

## Step 3: Set client variables

In terminal 2:

```bash
cd /Users/brendon/Documents/or3-sandbox
export SANDBOX_API=http://127.0.0.1:8080
export SANDBOX_TOKEN=dev-token
```

Optional config check:

```bash
go run ./cmd/sandboxctl config-lint
```

If you run that on macOS or another non-Linux host, it should now fail immediately with a Kata host-OS error instead of waiting until sandbox creation.

## Step 4: Create a Kata-backed sandbox

```bash
go run ./cmd/sandboxctl create --image alpine:3.20 --runtime containerd-kata-professional --start
```

You should see JSON output.

Look for:

- `id`
- `runtime_selection` = `containerd-kata-professional`
- `runtime_backend` = `kata`
- `runtime_class` = `vm`

## Step 5: Run a command

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc 'echo hello from kata && uname -a'
```

Then try writing a file:

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc 'echo kata-note > /workspace/note.txt && cat /workspace/note.txt'
```

## Step 6: Open an interactive shell

```bash
go run ./cmd/sandboxctl tty <sandbox-id>
```

Inside the shell, try:

```bash
pwd
ls -la /workspace
cat /workspace/note.txt
exit
```

## Step 7: Inspect runtime details

```bash
go run ./cmd/sandboxctl inspect <sandbox-id>
go run ./cmd/sandboxctl runtime-health
```

This helps you confirm that the daemon sees the sandbox on the Kata backend.

## Step 8: Try a snapshot

```bash
go run ./cmd/sandboxctl snapshot-create --name kata-checkpoint <sandbox-id>
go run ./cmd/sandboxctl snapshot-list <sandbox-id>
```

In this backend, snapshot restore works by clearing the workspace and extracting the archived workspace contents back into place.

## Step 9: Know the current limitations

These commands are not supported on Kata in the current implementation:

```bash
go run ./cmd/sandboxctl suspend <sandbox-id>
go run ./cmd/sandboxctl resume <sandbox-id>
```

Also note:

- `disk-mb` is not enforced as a hard create-time limit on Kata
- the runtime is Linux-only
- container images are still the packaging model, unlike QEMU guest images

## Step 10: Clean up

```bash
go run ./cmd/sandboxctl stop <sandbox-id>
go run ./cmd/sandboxctl delete <sandbox-id>
```

## What you learned

You just used the Kata path with the same top-level CLI flow as Docker:

- create a sandbox from a container image
- run commands
- use a TTY
- snapshot the workspace
- inspect runtime state

The big difference is the isolation boundary: Kata gives you a VM-backed runtime selection while keeping a container-image workflow.

## Next step

After Kata, compare it with the guest-image path in [Tutorial 4: Trying the QEMU Runtime](qemu-runtime.md).