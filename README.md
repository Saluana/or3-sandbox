# or3-sandbox

Single-node sandbox control plane for durable tenant environments.

Current status:

- shipped today: trusted Docker-backed control plane for development or internal trusted use
- shipped today: guest-backed `qemu` runtime for the stronger production isolation path

Runtime rule of thumb:

- use `docker` when cost and density matter more than isolation and the workload is trusted
- use `qemu` when isolation strength matters more than density and the workload is untrusted or security-sensitive

The current Docker backend is not the hostile multi-tenant production boundary described by the architecture docs.

The repository ships:

- `sandboxd`: Go HTTP daemon with SQLite metadata, static-token or JWT tenancy, quotas, lifecycle orchestration, file APIs, exec streaming, PTY attach, tunnels, snapshots, and restart reconciliation
- `sandboxctl`: CLI for lifecycle, exec, TTY, file transfer, and tunnel management
- Docker-backed runtime implementation for durable per-sandbox environments with isolated networks and persistent workspace mounts in trusted or development mode
- QEMU-backed runtime implementation with booting, suspended, degraded, and failed guest visibility for the stronger production boundary
- integration tests that exercise lifecycle, ownership, snapshots, tunnels, detached workloads, and quota enforcement

See also:

- `planning/whats_left.md`
- `planning/tasks2.md`
- `planning/onwards/requirements.md`
- `planning/onwards/design.md`
- `planning/onwards/tasks.md`
- `planning/onwards/status_matrix.md`
- `docs/README.md`
- `docs/operations/README.md`

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

Development mode seeds bearer tokens from `SANDBOX_TOKENS`.

Default:

- token: `dev-token`
- tenant: `tenant-dev`

Format:

```text
SANDBOX_TOKENS=token-a=tenant-a,token-b=tenant-b
```

For production mode, use JWT auth instead:

```bash
export SANDBOX_MODE=production
export SANDBOX_AUTH_MODE=jwt-hs256
export SANDBOX_AUTH_JWT_ISSUER=https://issuer.example
export SANDBOX_AUTH_JWT_AUDIENCE=sandbox-api
export SANDBOX_AUTH_JWT_SECRET_PATHS=/run/secrets/or3-jwt-hmac
export SANDBOX_TUNNEL_SIGNING_KEY_PATH=/run/secrets/or3-tunnel-signing-key
```

For browser-facing tunnel flows behind rolling restarts or multiple replicas, configure a shared tunnel signing secret so signed URLs and bootstrap cookies validate consistently across instances.

## Runtime Notes

- Each sandbox maps to a durable Docker container with a persistent `/workspace` mount.
- `internet-enabled` sandboxes receive a dedicated Docker bridge network.
- `internet-disabled` sandboxes run with Docker `--network none`.
- Tunnels are explicit daemon-managed proxy endpoints; containers do not publish host ports directly.
- Snapshots combine a committed container image with a workspace tarball.
- The daemon requires `SANDBOX_TRUSTED_DOCKER_RUNTIME=true` when `SANDBOX_RUNTIME=docker` because Docker is treated as a shared-kernel trusted mode, not a production hostile multi-tenant boundary.
- Policy guardrails can restrict allowed base images, public tunnels, maximum sandbox lifetime, and idle time.
- `GET /v1/runtime/capacity` and `GET /metrics` expose production-oriented capacity and pressure views for operators.
- Use `sandboxctl inspect <sandbox-id>` or `sandboxctl runtime-health` to confirm whether a sandbox is running on `docker` or `qemu`.

## Production Roadmap Notes

The active next-step design work is focused on:

- enterprise identity, authorization, TLS, and policy hardening around the shipped `qemu` production boundary
- stronger workload verification, failure drills, and operator runbooks
- resource enforcement, observability, and backup or recovery confidence for production deployments

## Tests

```bash
./scripts/production-smoke.sh
```

For host-prepared QEMU verification, backup or restore procedures, and incident drills, use the operator docs under `docs/operations/`.

Production-facing deployment language should be gated on the smoke path above plus the documented drills in `docs/operations/verification.md`.
