# or3-sandbox

Single-node sandbox control plane for durable tenant environments.

The repository ships:

- `sandboxd`: Go HTTP daemon with SQLite metadata, bearer-token tenancy, quotas, lifecycle orchestration, file APIs, exec streaming, PTY attach, tunnels, snapshots, and restart reconciliation
- `sandboxctl`: CLI for lifecycle, exec, TTY, file transfer, and tunnel management
- Docker-backed runtime implementation for durable per-sandbox environments with isolated networks and persistent workspace mounts
- integration tests that exercise lifecycle, ownership, snapshots, tunnels, detached workloads, and quota enforcement

## Quick Start

Requirements:

- Go 1.26+
- Docker

Run the daemon:

```bash
go run ./cmd/sandboxd \
  -listen :8080 \
  -db ./data/sandbox.db \
  -storage-root ./data/storage \
  -snapshot-root ./data/snapshots
```

Use the CLI:

```bash
export SANDBOX_API=http://127.0.0.1:8080
export SANDBOX_TOKEN=dev-token

go run ./cmd/sandboxctl create --image alpine:3.20 --start
go run ./cmd/sandboxctl list
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc 'echo hello > /workspace/hello.txt && cat /workspace/hello.txt'
go run ./cmd/sandboxctl tty <sandbox-id>
```

## Default Auth

The daemon seeds bearer tokens from `SANDBOX_TOKENS`.

Default:

- token: `dev-token`
- tenant: `tenant-dev`

Format:

```text
SANDBOX_TOKENS=token-a=tenant-a,token-b=tenant-b
```

## Runtime Notes

- Each sandbox maps to a durable Docker container with a persistent `/workspace` mount.
- `internet-enabled` sandboxes receive a dedicated Docker bridge network.
- `internet-disabled` sandboxes run with Docker `--network none`.
- Tunnels are explicit daemon-managed proxy endpoints; containers do not publish host ports directly.
- Snapshots combine a committed container image with a workspace tarball.

## Tests

```bash
go test ./...
```
