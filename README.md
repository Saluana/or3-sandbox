# or3-sandbox

Single-node sandbox control plane for durable tenant environments.

Current status:

- shipped today: trusted Docker-backed control plane for development or single-operator use
- planned next: guest-backed `qemu` runtime for production-aligned multi-tenant isolation

The current Docker backend is not the hostile multi-tenant production boundary described by the architecture docs. That production path is still being implemented behind the existing runtime abstraction.

The repository ships:

- `sandboxd`: Go HTTP daemon with SQLite metadata, bearer-token tenancy, quotas, lifecycle orchestration, file APIs, exec streaming, PTY attach, tunnels, snapshots, and restart reconciliation
- `sandboxctl`: CLI for lifecycle, exec, TTY, file transfer, and tunnel management
- Docker-backed runtime implementation for durable per-sandbox environments with isolated networks and persistent workspace mounts in trusted or development mode
- integration tests that exercise lifecycle, ownership, snapshots, tunnels, detached workloads, and quota enforcement

See also:

- `planning/whats_left.md`
- `planning/tasks2.md`
- `planning/onwards/requirements.md`
- `planning/onwards/design.md`
- `planning/onwards/tasks.md`
- `planning/onwards/status_matrix.md`
- `docs/README.md`

## Documentation

For a beginner-friendly walkthrough of the project, see:

- `docs/README.md`
- `docs/setup.md`
- `docs/usage.md`
- `docs/tutorials/first-sandbox.md`

## Quick Start

Requirements for the shipped trusted Docker path:

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
- The daemon requires `SANDBOX_TRUSTED_DOCKER_RUNTIME=true` when `SANDBOX_RUNTIME=docker` because Docker is treated as a shared-kernel trusted mode, not a production hostile multi-tenant boundary.

## Production Roadmap Notes

The active next-step design work is focused on:

- a guest-backed `qemu` runtime selected through the existing runtime abstraction
- guest bootstrap and readiness checks before a sandbox is marked `running`
- real storage boundaries instead of requested-size bookkeeping alone
- workload verification for Git, package persistence, browser automation, and guest-local containers
- recovery drills for boot failure, disk-full behavior, snapshot failure, and restart during exec

## Tests

```bash
go test ./...
```
