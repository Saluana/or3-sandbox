# Tutorial 1: Your First Sandbox

This tutorial walks through the fastest successful path.

Goal:

- start the daemon
- create a sandbox
- run a command
- open a terminal
- clean up

## Step 1: Start the server

In terminal 1:

```bash
cd /Users/brendon/Documents/or3-sandbox

SANDBOX_RUNTIME=docker \
SANDBOX_TRUSTED_DOCKER_RUNTIME=true \
go run ./cmd/sandboxd \
  -listen :8080 \
  -db ./data/sandbox.db \
  -storage-root ./data/storage \
  -snapshot-root ./data/snapshots
```

Leave that terminal running.

## Step 2: Set client variables

In terminal 2:

```bash
cd /Users/brendon/Documents/or3-sandbox
export SANDBOX_API=http://127.0.0.1:8080
export SANDBOX_TOKEN=dev-token
```

## Step 3: Create a sandbox

```bash
go run ./cmd/sandboxctl create --image alpine:3.20 --start
```

You should see JSON output.

Look for:

- an `id`
- a `status`
- a `runtime_backend`

Copy the sandbox ID.

For the rest of this tutorial, replace `<sandbox-id>` with your real ID.

## Step 4: List your sandboxes

```bash
go run ./cmd/sandboxctl list
```

This helps you confirm the sandbox was saved.

## Step 5: Run a command inside the sandbox

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc 'echo hello from sandbox'
```

Now try writing a file:

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc 'echo sandbox-note > /workspace/note.txt && cat /workspace/note.txt'
```

Why `/workspace`?

Because that is the main persistent workspace path used by the project.

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

## Step 7: Inspect the sandbox

```bash
go run ./cmd/sandboxctl inspect <sandbox-id>
```

This shows the saved state and limits.

## Step 8: Create a snapshot

```bash
go run ./cmd/sandboxctl snapshot-create --name first-checkpoint <sandbox-id>
go run ./cmd/sandboxctl snapshot-list <sandbox-id>
```

This gives you a saved checkpoint before cleanup.

## Step 9: Stop the sandbox

```bash
go run ./cmd/sandboxctl stop <sandbox-id>
```

Then inspect it again if you want.

## Step 10: Delete the sandbox

```bash
go run ./cmd/sandboxctl delete <sandbox-id>
```

## What you learned

You just used the main workflow:

- start server
- create sandbox
- run commands
- use a terminal
- inspect state
- stop and delete

That is the heart of `or3-sandbox`.

## If something failed

Try these checks:

```bash
go run ./cmd/sandboxctl runtime-health
go run ./cmd/sandboxctl quota
go run ./cmd/sandboxctl list
```

Also make sure Docker is running and `SANDBOX_TOKEN` is correct.
