# Setup

This guide shows the easiest way to get `or3-sandbox` running.

If you are brand new to the project, use the **Docker runtime** first.

If you are deploying for production, stop after the local setup walkthrough and switch to the operator docs in [Production Deployment](operations/production-deployment.md).

## What you need

### Required for the easy path

- macOS or Linux
- Go 1.26 or newer
- Docker

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

## Step 2: Make sure Docker works

Run:

```bash
docker version
```

If Docker is not installed or not running, fix that first.

## Step 3: Start the daemon with Docker runtime

The Docker runtime is treated as a **trusted** mode, so you must say that on purpose.

Run:

```bash
SANDBOX_RUNTIME=docker \
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
{"ok":true}
```

### Runtime health check

```bash
go run ./cmd/sandboxctl runtime-health
```

## Step 6: Create your first sandbox

```bash
go run ./cmd/sandboxctl create --image alpine:3.20 --start
```

Then list sandboxes:

```bash
go run ./cmd/sandboxctl list
```

If that works, your setup is good.

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
export SANDBOX_RUNTIME=docker
export SANDBOX_TRUSTED_DOCKER_RUNTIME=true
```

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
