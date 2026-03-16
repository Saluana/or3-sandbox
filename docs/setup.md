# Setup

This guide shows the easiest way to get `or3-sandbox` running.

If you are brand new to the project, use the **Docker runtime selection** first.

If you are deploying for production, stop after the local setup walkthrough and switch to the operator docs in [Production Deployment](operations/production-deployment.md).

The supported bootstrap shortcut for production is now:

```bash
export SANDBOX_DEPLOYMENT_PROFILE=production-qemu-core
go run ./cmd/sandboxctl config-lint
go run ./cmd/sandboxctl doctor --production-qemu
```

## What you need

### Required for the easy path

- macOS or Linux
- Go 1.26 or newer
- Docker

### Optional for the Kata path

- Linux host
- containerd
- Kata Containers runtime installed
- `ctr` available in `PATH`

### Optional for the QEMU path

- QEMU installed
- a prepared guest base image
- an SSH private key that matches the guest image setup

## Step 1: Clone the project

```bash
git clone <your-repo-url>
cd or3-sandbox
```

If you already have the repo, just `cd` into it.

## Bootstrap helper scripts

If you want a runtime-specific host bootstrap instead of installing packages by hand,
the repo now ships separate installer scripts:

```bash
./scripts/install-docker-runtime.sh
./scripts/install-kata-runtime.sh
./scripts/install-qemu-runtime.sh
```

Notes:

- these scripts currently target apt-based Linux hosts
- the Docker script installs Docker Engine, starts the daemon, and downloads Go modules for the repo
- the Kata script installs containerd plus the latest upstream Kata static release, verifies the Kata shim is runnable, and fails early on older hosts whose glibc is too old for the current upstream release
- the QEMU script installs the host packages needed by the repo's QEMU path and, by default, builds the repo's `core` guest image

Kata-specific note:

- `ctr` usually needs `sudo` on a default Ubuntu host because `/run/containerd/containerd.sock` is root-owned
- the current upstream static Kata builds are a better fit for Ubuntu 22.04+ than Ubuntu 20.04

QEMU-specific options:

```bash
PROFILE=runtime ./scripts/install-qemu-runtime.sh
SKIP_IMAGE_BUILD=1 ./scripts/install-qemu-runtime.sh
```

## Step 2: Make sure Docker works

Run:

```bash
docker version
```

If Docker is not installed or not running, fix that first.

On Linux, also make sure your shell can reach `/var/run/docker.sock` without `sudo`.
If `docker version` or `docker ps` fails with a socket permission error, add your user to the `docker` group and refresh your login session before continuing:

```bash
sudo usermod -aG docker "$USER"
newgrp docker
docker ps
```

## Step 3: Start the daemon with Docker runtime selection

The Docker path is treated as a **trusted** mode, so you must say that on purpose.

Run:

```bash
SANDBOX_ENABLED_RUNTIMES=docker-dev \
SANDBOX_DEFAULT_RUNTIME=docker-dev \
SANDBOX_TRUSTED_DOCKER_RUNTIME=true \
go run ./cmd/sandboxd \
  -listen :8080 \
  -db ./data/sandbox.db \
  -storage-root ./data/storage \
  -snapshot-root ./data/snapshots
```

What this does:

- starts the API server on port `8080`
- creates a SQLite database in `./data`
- stores sandbox files under `./data/storage`
- stores snapshot files under `./data/snapshots`
- enables the `docker-dev` runtime selection as the default for new sandboxes

Legacy compatibility note:

```bash
SANDBOX_RUNTIME=docker
```

still works, but the current explicit runtime-selection variables are `SANDBOX_ENABLED_RUNTIMES` and `SANDBOX_DEFAULT_RUNTIME`.

## Step 4: Set client environment variables

Open a second terminal and run:

```bash
export SANDBOX_API=http://127.0.0.1:8080
export SANDBOX_TOKEN=dev-token
```

These defaults match the project's built-in development token setup.

## Step 5: Check that the server is alive

You can test with either `curl` or the CLI.

### Health check

```bash
curl http://127.0.0.1:8080/healthz
```

Expected result:

```json
{ "ok": true }
```

### Runtime health check

```bash
go run ./cmd/sandboxctl runtime-health
```

## Step 6: Create your first sandbox

```bash
go run ./cmd/sandboxctl create --image alpine:3.20 --start
```

That uses the daemon's default runtime selection. To request Docker explicitly:

```bash
go run ./cmd/sandboxctl create --image alpine:3.20 --runtime docker-dev --start
```

Then list sandboxes:

```bash
go run ./cmd/sandboxctl list
```

If that works, your setup is good.

## Optional: Start with Docker and Kata enabled together

On a Linux host with containerd and Kata installed, you can enable both the
trusted Docker path and the professional Kata path in one daemon:

```bash
SANDBOX_ENABLED_RUNTIMES=docker-dev,containerd-kata-professional \
SANDBOX_DEFAULT_RUNTIME=containerd-kata-professional \
SANDBOX_TRUSTED_DOCKER_RUNTIME=true \
SANDBOX_KATA_BINARY=ctr \
SANDBOX_KATA_RUNTIME_CLASS=io.containerd.kata.v2 \
SANDBOX_KATA_CONTAINERD_SOCKET=/run/containerd/containerd.sock \
go run ./cmd/sandboxd
```

Then create sandboxes with either:

```bash
go run ./cmd/sandboxctl create --image alpine:3.20 --runtime docker-dev --start
go run ./cmd/sandboxctl create --image alpine:3.20 --runtime containerd-kata-professional --start
```

Use `go run ./cmd/sandboxctl config-lint` to confirm the daemon config before
turning on Kata or QEMU.

For production, prefer:

```bash
go run ./cmd/sandboxctl config-lint
go run ./cmd/sandboxctl doctor --production-qemu
```

## Optional: Start with Kata only

If you are on a Linux host with containerd and Kata installed, and you want a
VM-backed runtime without switching all the way to QEMU guest images yet, you
can start `sandboxd` with Kata as the only enabled runtime selection:

```bash
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

Then create a sandbox with a container image as usual:

```bash
go run ./cmd/sandboxctl create --image alpine:3.20 --runtime containerd-kata-professional --start
```

Important Kata behavior to know up front:

- Kata is Linux-only in this codebase
- `suspend` and `resume` are not supported on Kata
- `disk-mb` is accepted in the API, but Kata does not enforce that limit at create time
- snapshots archive the workspace and preserve the base image reference, similar to Docker

## Common setup problems

## Problem: "unauthorized"

Cause:

- `SANDBOX_TOKEN` is missing or wrong

Fix:

```bash
export SANDBOX_TOKEN=dev-token
```

## Problem: Docker runtime error at startup

Cause:

- you forgot `SANDBOX_TRUSTED_DOCKER_RUNTIME=true`

Fix:

```bash
export SANDBOX_ENABLED_RUNTIMES=docker-dev
export SANDBOX_DEFAULT_RUNTIME=docker-dev
export SANDBOX_TRUSTED_DOCKER_RUNTIME=true
```

## Problem: Kata runtime error at startup

Cause:

- `ctr`, containerd, or the configured Kata runtime class is unavailable

Fix:

```bash
go run ./cmd/sandboxctl config-lint
```

On non-Linux hosts such as macOS, `config-lint` now fails early for Kata instead of letting the daemon reach a later create-time error.

Then verify:

- `SANDBOX_KATA_BINARY`
- `SANDBOX_KATA_RUNTIME_CLASS`
- `SANDBOX_KATA_CONTAINERD_SOCKET`
- containerd is running on the local host

## Problem: command not found

Cause:

- Go, Docker, or QEMU is not installed

Fix:

- install the missing tool
- reopen your terminal if needed
- run the check again

## Directory layout created during local use

When you run the daemon locally, you will usually see:

- `data/sandbox.db`
- `data/storage/`
- `data/snapshots/`

These are safe places to inspect while learning the project.

## Next step

After setup, continue with:

- [Usage Guide](usage.md)
- [Tutorial 1: Your First Sandbox](tutorials/first-sandbox.md)
