# or3-sandbox

Single-node sandbox control plane for durable tenant environments.

Current status:

- shipped today: trusted Docker-backed control plane for development or internal trusted use
- shipped today: guest-backed `qemu` runtime for the intended higher-isolation path, with real host validation still required before calling a deployment production-ready

Runtime rule of thumb:

- use `docker` when cost and density matter more than isolation and the workload is trusted
- use `qemu` when isolation strength matters more than density and you have validated the guest image, suspend/resume behavior, and recovery drills on your hosts

Production default posture:

- set `SANDBOX_DEPLOYMENT_PROFILE=production-qemu-core` for the smallest supported production footprint
- use `SANDBOX_DEPLOYMENT_PROFILE=production-qemu-browser` only when browser tooling is explicitly required
- use `SANDBOX_DEPLOYMENT_PROFILE=exception-container` only for time-bounded dangerous-profile exceptions with an audit reason

The current Docker backend is not the hostile multi-tenant production boundary described by the architecture docs.

The repository ships:

- `sandboxd`: Go HTTP daemon with SQLite metadata, static-token or JWT tenancy, quotas, lifecycle orchestration, file APIs, exec streaming, PTY attach, tunnels, snapshots, and restart reconciliation
- `sandboxctl`: CLI for lifecycle, exec, TTY, file transfer, and tunnel management
- Docker-backed runtime implementation for durable per-sandbox environments with isolated networks and persistent workspace mounts in trusted or development mode
- Kata-backed runtime implementation for Linux hosts that have containerd plus Kata Containers available, exposed through the `containerd-kata-professional` runtime selection
- QEMU-backed runtime implementation with booting, suspended, degraded, and failed guest visibility, plus opt-in host-prepared guest verification
- integration tests that exercise the main control-plane flows, with opt-in host-prepared QEMU verification for the real guest path

See also:

- `planning/qemu-production-readiness/requirements.md`
- `planning/qemu-production-readiness/design.md`
- `planning/qemu-production-readiness/tasks.md`
- `planning/runtime-proof/requirements.md`
- `planning/runtime-proof/design.md`
- `planning/runtime-proof/tasks.md`
- `docs/README.md`
- `docs/operations/README.md`

## Documentation

For a beginner-friendly walkthrough of the project, see:

- `docs/README.md`
- `docs/setup.md`
- `docs/usage.md`
- `docs/tutorials/first-sandbox.md`
- `docs/tutorials/kata-runtime.md`

## Quick Start

Requirements for the shipped trusted Docker path:

- Go 1.26+
- Docker

On Linux, your shell also needs access to Docker without `sudo`.
If `docker ps` fails with a permission error on `/var/run/docker.sock`, run:

```bash
sudo usermod -aG docker "$USER"
newgrp docker
docker ps
```

Run the daemon:

```bash
SANDBOX_DEPLOYMENT_PROFILE=dev-trusted-docker \
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

Production bootstrap path:

```bash
export SANDBOX_DEPLOYMENT_PROFILE=production-qemu-core
export SANDBOX_PRODUCTION_TRANSPORT=terminated-proxy
export SANDBOX_OPERATOR_HOST=https://sandbox.example.com
export SANDBOX_TRUST_PROXY_HEADERS=true
export SANDBOX_AUTH_MODE=jwt-hs256
export SANDBOX_AUTH_JWT_ISSUER=https://issuer.example
export SANDBOX_AUTH_JWT_AUDIENCE=sandbox-api
export SANDBOX_AUTH_JWT_SECRET_PATHS=/run/secrets/or3-jwt-hmac
export SANDBOX_TUNNEL_SIGNING_KEY_PATH=/run/secrets/or3-tunnel-signing-key

go run ./cmd/sandboxctl config-lint
go run ./cmd/sandboxctl doctor --production-qemu
go run ./cmd/sandboxctl image promote --image /var/lib/or3-images/or3-guest-core.qcow2
go run ./cmd/sandboxctl release-gate
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

## Platform Boundary Notes

`sandboxd` is a raw tenant-scoped control-plane API. Its persisted resources and direct API responses may include `tenant_id` because `tenant_id` is the daemon's canonical internal ownership key.

When `sandboxd` sits behind `or3-net`, the boundary contract is:

- `or3-net` maps upstream `workspace_id` context into sandbox tenant auth before calling `sandboxd`
- `tenant_id` stays internal to the sandbox/provider boundary and must not become a UI-facing identifier
- browser and app clients should rely on upstream `workspace_id` contracts, not raw `tenant_id` fields from `sandboxd`

Service-account guidance for `or3-net` callers:

- use the smallest stored scope set needed for the workflow: typically `sandbox.read`, `sandbox.lifecycle`, `exec.run`, `files.read`, `files.write`, `tunnels.read`, `tunnels.write`, `snapshots.read`, and `snapshots.write`
- reserve `admin.inspect` for explicit operator tooling, not routine workspace flows
- treat service-account credentials and tunnel access tokens as control-plane secrets

Browser launch guidance:

- tunnel create responses may contain `access_token`; treat it as a control-plane capability that must not be relayed to browser clients
- browser clients should use `POST /v1/tunnels/{id}/signed-url`, which returns a short-lived, path-scoped launch capability and bootstrap cookie flow instead of a reusable admin credential

## Config alignment

- Native sandbox runtime variables remain `SANDBOX_*`; cross-repo deployment tooling should reserve `OR3_SANDBOX_*` and translate those values into the daemon's native env or secret-file inputs before startup.
- Secret precedence should be launch-time env or mounted secret paths → instance-local config/profile material → checked-in defaults.
- Shared-key mapping for sandbox auth material and tunnel-signing keys is documented in [../or3-net/planning/platform-standardization/config-alignment.md](../or3-net/planning/platform-standardization/config-alignment.md).
- Frozen sandbox fixture coverage is enforced via [.github/workflows/contracts.yml](.github/workflows/contracts.yml).

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

Production test matrix:

| Claim                                                  | Evidence                                                                        |
| ------------------------------------------------------ | ------------------------------------------------------------------------------- | ----------------------------------- | -------------------------- |
| Production runtime boundary defaults to VM-backed QEMU | `go test ./internal/config ./internal/service`                                  |
| Explicit RBAC and service-account scope checks         | `go test ./internal/auth`                                                       |
| Runtime info / operator inspection surfaces            | `go test ./internal/api -run 'Test(StartAdmissionDenialAppearsInMetrics         | RuntimeHealthEndpoint)'`            |
| Promoted image enforcement                             | `go test ./internal/service -run TestProductionQEMUCreateRequiresPromotedImage` |
| SQLite migrations for hardening state                  | `go test ./internal/db ./internal/repository`                                   |
| Config lint and doctor bootstrap path                  | `go test ./cmd/sandboxctl -run 'Test(RunConfigLint                              | RunDoctorRequiresProductionQEMUFlag | ProductionQEMUDoctor.\*)'` |
| Host-gated QEMU posture                                | `./scripts/qemu-host-verification.sh --profile core --control-mode agent`       |
| Production smoke                                       | `./scripts/qemu-production-smoke.sh`                                            |
| Abuse-path behavior                                    | `./scripts/qemu-resource-abuse.sh`                                              |
| Recovery / restore drill                               | `./scripts/qemu-recovery-drill.sh`                                              |

For host-prepared QEMU verification, backup or restore procedures, and incident drills, use the operator docs under `docs/operations/`.

Production-facing deployment language should be gated on the smoke path above plus the documented drills in `docs/operations/verification.md`.
