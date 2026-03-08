# Usage Guide

This guide shows how to use the project day to day.

## Before you start

Make sure:

- `sandboxd` is running
- `SANDBOX_API` is set
- `SANDBOX_TOKEN` is set

Example:

```bash
export SANDBOX_API=http://127.0.0.1:8080
export SANDBOX_TOKEN=dev-token
```

## Core CLI commands

The CLI supports these top-level commands:

- `create`
- `list`
- `inspect`
- `start`
- `stop`
- `suspend`
- `resume`
- `delete`
- `exec`
- `tty`
- `upload`
- `download`
- `mkdir`
- `tunnel-create`
- `tunnel-list`
- `tunnel-revoke`
- `quota`
- `runtime-health`
- `snapshot-create`
- `snapshot-list`
- `snapshot-inspect`
- `snapshot-restore`
- `preset list`
- `preset inspect`
- `preset run`

## 1. Create a sandbox

```bash
go run ./cmd/sandboxctl create --image alpine:3.20 --start
```

Useful flags:

- `--image`
- `--cpu`
- `--memory-mb`
- `--pids`
- `--disk-mb`
- `--network`
- `--allow-tunnels`
- `--start`

Example with more settings:

```bash
go run ./cmd/sandboxctl create \
  --image alpine:3.20 \
  --cpu 2 \
  --memory-mb 1024 \
  --disk-mb 4096 \
  --network internet-enabled \
  --allow-tunnels=true \
  --start
```

## 2. List sandboxes

```bash
go run ./cmd/sandboxctl list
```

This prints JSON for the current tenant's sandboxes.

## 3. Inspect one sandbox

```bash
go run ./cmd/sandboxctl inspect <sandbox-id>
```

Use this when you want detailed status and limits.

## 4. Start and stop a sandbox

Start:

```bash
go run ./cmd/sandboxctl start <sandbox-id>
```

Stop:

```bash
go run ./cmd/sandboxctl stop <sandbox-id>
```

Force stop:

```bash
go run ./cmd/sandboxctl stop --force <sandbox-id>
```

Delete:

```bash
go run ./cmd/sandboxctl delete <sandbox-id>
```

## 5. Run a command inside a sandbox

Simple command:

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc 'echo hello'
```

Run in `/workspace` by default:

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc 'pwd && ls -la'
```

Useful flags:

- `--stream`
- `--timeout`
- `--cwd`
- `--detached`

Example:

```bash
go run ./cmd/sandboxctl exec --timeout 30s --cwd /workspace <sandbox-id> sh -lc 'echo hi > note.txt && cat note.txt'
```

## 6. Open an interactive terminal

```bash
go run ./cmd/sandboxctl tty <sandbox-id>
```

You can also pass a command:

```bash
go run ./cmd/sandboxctl tty <sandbox-id> sh
```

This is useful when you want to explore manually.

## 7. Upload and download files

Upload a local file into the sandbox:

```bash
go run ./cmd/sandboxctl upload <sandbox-id> ./hello.txt /workspace/hello.txt
```

Download it back:

```bash
go run ./cmd/sandboxctl download <sandbox-id> /workspace/hello.txt ./hello-copy.txt
```

Make a directory:

```bash
go run ./cmd/sandboxctl mkdir <sandbox-id> /workspace/demo
```

## 8. Work with tunnels

Create a tunnel:

```bash
go run ./cmd/sandboxctl tunnel-create --port 3000 <sandbox-id>
```

Optional flags:

- `--protocol` (`http` or `tcp`)
- `--auth-mode`
- `--visibility`

List tunnels:

```bash
go run ./cmd/sandboxctl tunnel-list <sandbox-id>
```

Revoke a tunnel:

```bash
go run ./cmd/sandboxctl tunnel-revoke <tunnel-id>
```

## 9. Check quota and runtime health

Quota:

```bash
go run ./cmd/sandboxctl quota
```

Runtime health:

```bash
go run ./cmd/sandboxctl runtime-health
```

These are good first commands when something seems wrong.

## 10. Work with snapshots

Create a snapshot:

```bash
go run ./cmd/sandboxctl snapshot-create --name before-change <sandbox-id>
```

List snapshots for a sandbox:

```bash
go run ./cmd/sandboxctl snapshot-list <sandbox-id>
```

Inspect one snapshot:

```bash
go run ./cmd/sandboxctl snapshot-inspect <snapshot-id>
```

Restore a snapshot into a target sandbox:

```bash
go run ./cmd/sandboxctl snapshot-restore <snapshot-id> <sandbox-id>
```

This is useful when you want to save a known-good state before making changes.

## 11. Run example presets

List presets:

```bash
go run ./cmd/sandboxctl preset list
```

Inspect one preset:

```bash
go run ./cmd/sandboxctl preset inspect playwright
```

Run a preset:

```bash
go run ./cmd/sandboxctl preset run playwright
```

The command stays the same for Docker and QEMU. The difference is packaging:

- Docker presets usually point at container images directly.
- QEMU presets usually point at a guest image path or use a documented `runtime.profile` plus `--env QEMU_GUEST_IMAGE=...`.
- QEMU runs also have an extra guest-ready phase before bootstrap and app readiness begin.

Useful preset flags:

- `--env KEY=VALUE` for preset inputs like tokens or target URLs
- `--set image=...` or `--set memory-mb=2048` for sandbox overrides
- `--cleanup always|never|on-success`
- `--keep` to preserve the sandbox for inspection

Examples:

```bash
go run ./cmd/sandboxctl preset run claude-code --env ANTHROPIC_AUTH_TOKEN=... --cleanup never
go run ./cmd/sandboxctl preset run playwright --env TARGET_URL=https://example.com
go run ./cmd/sandboxctl preset run openclaw --env OPENCLAW_GATEWAY_TOKEN=secret --keep
go run ./cmd/sandboxctl preset run qemu-bootstrap --env QEMU_GUEST_IMAGE="$SANDBOX_QEMU_BASE_IMAGE_PATH"
go run ./cmd/sandboxctl preset run qemu-service --env QEMU_GUEST_IMAGE="$SANDBOX_QEMU_BASE_IMAGE_PATH" --keep
```

Preset artifacts download into the example directory by default, and the runner always prints the sandbox ID.

QEMU notes:

- Use a guest image that matches the preset README or `runtime.profile` hint.
- Expect slower startup because QEMU must boot a guest before running bootstrap steps.
- HTTP readiness, tunnel creation, and artifact downloads still use the same shared preset UX as Docker.

## API basics

You do not have to use the CLI. You can also call the HTTP API directly.

Remember the auth header:

```text
Authorization: Bearer dev-token
```

### Create a sandbox with `curl`

```bash
curl -X POST http://127.0.0.1:8080/v1/sandboxes \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{
    "base_image_ref": "alpine:3.20",
    "cpu_limit": 2,
    "memory_limit_mb": 512,
    "pids_limit": 256,
    "disk_limit_mb": 2048,
    "network_mode": "internet-enabled",
    "start": true
  }'
```

### Stream exec output

```bash
curl -N -X POST 'http://127.0.0.1:8080/v1/sandboxes/<sandbox-id>/exec?stream=1' \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{
    "command": ["sh", "-lc", "echo hello && uname -a"],
    "cwd": "/workspace"
  }'
```

The stream uses server-sent events, so the output arrives in pieces.

If you leave out `timeout`, the server uses its normal default.

## Snapshot note

Snapshots are now available from both the API and `sandboxctl`.

Docker and QEMU still store snapshot data differently under the hood, but the user-facing create, list, inspect, and restore workflow is the same.

## Good habits

- inspect before debugging
- stop or delete sandboxes you no longer need
- use `runtime-health` when runtime behavior looks strange
- keep tutorials simple until you trust your setup
