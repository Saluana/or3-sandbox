# Sandbox v1 Architecture

## 1. Purpose

Sandbox v1 is a lightweight multi-tenant system for hosting persistent tenant machines on a single Linux node.

The product goal is not "run one command safely." The product goal is "give each tenant a durable cloud computer that agents can use without affecting other tenants or the host."

v1 must support:

- multi-tenant isolation on one Linux machine
- persistent per-tenant environments
- interactive shells and long-running agents
- package installation and background daemons
- outbound internet access when allowed
- optional reverse tunnels for inbound access
- Docker-in-Docker or an equivalent container engine inside the tenant environment
- headless browser automation
- CLI and HTTP API access

The design must stay operationally small:

- one control-plane daemon
- SQLite metadata store
- low idle overhead
- no Kubernetes requirement
- no mandatory external database

## 2. Core product model

Each sandbox is a long-lived tenant machine, not an ephemeral job container.

That machine has:

- a stable sandbox ID
- a durable root environment
- a durable workspace volume
- one or more attached user sessions
- a network identity and policy
- an optional tunnel capability

Clients interact with the machine through:

- lifecycle APIs
- command execution
- interactive terminal attachment
- file operations
- tunnel creation and revocation
- snapshot and restore operations

Stateless command execution still exists, but it runs against a persistent tenant machine.

## 3. Security stance

### 3.1 Threat model

v1 must assume that tenant code may be buggy, destructive, or actively malicious.

Security goals:

- a tenant must not access another tenant's files, processes, sockets, memory, or network namespace
- a tenant must not modify the host environment
- a tenant must not interfere with the control plane
- a tenant must not reach another tenant over the internal network unless an explicit future feature allows it

### 3.2 Consequence for runtime choice

Shared-kernel process isolation is not a sufficient primary security boundary for hostile multi-tenant workloads.

Because of that:

- production multi-tenant mode must use a strong guest boundary per sandbox
- native host-isolated process mode, if implemented later, must be treated as trusted or development-only

v1 should be designed around a guest-backed sandbox runtime. The exact guest technology can be selected during implementation, but the architecture assumes a VM-style boundary rather than plain host namespaces alone.

### 3.3 Root and privilege model

The host must never expose root or privileged host capabilities to tenants.

Inside the guest sandbox:

- root or passwordless `sudo` is acceptable
- package installation is allowed
- background daemons are allowed
- Docker-in-Docker or an equivalent engine is allowed when the guest profile supports it

This is intentional. Usability depends on tenants feeling like they have a real machine. The safety boundary must sit outside that machine.

## 4. High-level architecture

The system consists of five main parts:

1. control-plane daemon
2. runtime manager
3. storage manager
4. network and tunnel manager
5. image and guest bootstrap pipeline

## 4.1 Control-plane daemon

The control plane is a single Go daemon.

Responsibilities:

- authenticate API and CLI requests
- authorize tenant access
- create, start, stop, suspend, resume, and delete sandboxes
- attach to terminal and command sessions
- track quotas and policy
- store metadata in SQLite
- coordinate cleanup and recovery
- manage snapshots and optional S3 sync metadata
- broker tunnel requests

The control plane never runs tenant code directly.

## 4.2 Runtime manager

The runtime manager is an internal interface that owns sandbox execution backends.

v1 should optimize for one production backend:

- guest-backed runtime for hostile multi-tenant workloads

Optional later backend:

- trusted native runtime for local development or single-tenant installs

Runtime responsibilities:

- create sandbox machine from a base image
- attach persistent volumes
- configure limits
- configure network
- start and stop the guest
- exec commands in the guest
- attach an interactive terminal
- collect runtime status
- destroy runtime resources

## 4.3 Storage manager

Each sandbox has three storage layers:

1. shared read-only base image
2. sandbox writable system layer
3. persistent workspace volume

Optional fourth layer:

4. persistent cache volume for package or build caches

The storage manager must support:

- local filesystem backed volumes by default
- per-sandbox quotas
- snapshot creation
- snapshot restore
- optional snapshot export to S3-compatible storage

S3 is not the live root filesystem. S3 is an optional persistence and backup target.

## 4.4 Network and tunnel manager

Each sandbox gets an isolated network boundary.

v1 network goals:

- no east-west tenant traffic
- outbound internet allowed or denied per sandbox
- no direct host network exposure by default
- explicit tunnel-based ingress for inbound access

The tunnel model should be outbound-first:

- sandbox initiates or is proxied through an operator-managed gateway
- users receive a public or private endpoint mapped to a sandbox service

This avoids exposing arbitrary host ports and keeps tenant ingress explicit and revocable.

## 4.5 Image and bootstrap pipeline

The system needs a base image pipeline for guest environments.

v1 base image should include:

- Linux userland
- shell tooling
- Git
- Python
- Node and npm
- a container engine or guest-compatible DinD setup
- browser automation dependencies
- a lightweight init or process supervisor

The image pipeline must produce a small, reproducible base image with clear versioning.

## 5. Sandbox lifecycle

Each sandbox moves through these states:

1. `creating`
2. `stopped`
3. `starting`
4. `running`
5. `suspending`
6. `suspended`
7. `stopping`
8. `deleting`
9. `deleted`
10. `error`

Expected lifecycle:

1. create sandbox metadata
2. allocate storage and network resources
3. prepare guest from base image
4. boot guest
5. run bootstrap agent in guest
6. mark sandbox `running`

Idle optimization is important for footprint:

- stopped sandboxes consume only disk
- suspended sandboxes may preserve state with lower resource use if implementation remains simple enough

v1 does not need live migration or clustering.

## 6. Persistent environment model

The tenant machine must behave like a durable computer.

v1 persistence rules:

- the root environment persists across restarts
- the workspace persists across restarts
- installed packages persist across restarts
- background services may be restarted by a guest init system after boot
- deleting a sandbox destroys all local state unless a snapshot or backup exists

This is a major change from the earlier stateless-first design. The sandbox itself is durable; individual command executions are not.

## 7. Execution model

## 7.1 Stateless exec

The API must support running one-off commands in a running sandbox.

Requirements:

- specify command, args, env, cwd, timeout
- stream stdout and stderr
- return exit status and timing
- kill the full process tree on timeout or cancellation

## 7.2 Interactive terminal

The API and CLI must support interactive shell attachment.

Requirements:

- PTY allocation
- resize events
- stdin, stdout, and stderr streaming
- multiple terminal sessions per sandbox

## 7.3 Long-running agents and services

Sandboxes must support:

- long-running user processes
- background daemons
- reconnecting to existing environments after client disconnect

The control plane must distinguish between:

- transient exec sessions
- attached terminal sessions
- sandbox services that continue without an attached client

## 8. Filesystem design

Each sandbox filesystem should be structured like this:

- system layer for the guest OS
- tenant home directory
- workspace directory for project files
- temp directory with eviction policy
- optional cache mounts

Rules:

- tenant-visible paths stay inside the sandbox
- host paths are never exposed directly
- workspace import and export goes through validated file APIs
- snapshots cover at least the persistent workspace and writable system state

For v1, active storage should remain local to the node for simplicity and performance.

## 9. Network model

v1 network modes:

- `internet-enabled`
- `internet-disabled`

Future mode:

- `restricted-egress`

Rules for v1:

- no tenant can directly discover or reach another tenant sandbox
- no tenant can bind directly on the host network
- sandbox ingress happens only through an explicit publish or tunnel capability
- operator can set a default network policy

Per-sandbox network implementation must support:

- isolated interface or namespace
- egress NAT when enabled
- explicit deny when disabled

## 10. Tunnel and service exposure model

Tunnels are first-class resources attached to sandboxes.

Each tunnel record should include:

- tunnel ID
- sandbox ID
- target port in sandbox
- protocol
- auth mode
- public or private visibility
- created_at
- revoked_at

v1 should support:

- creating a tunnel to a sandbox service
- listing active tunnels
- revoking a tunnel

The control plane or gateway is responsible for policy and auditability.

## 11. Container engine inside sandbox

The system must support Docker-in-Docker or an equivalent guest-local container engine.

Rules:

- never expose the host Docker socket
- the inner engine runs only inside the sandbox boundary
- inner engine storage counts toward sandbox quota
- inner engine network must remain nested inside sandbox policy

This capability is easier and safer in a guest-backed sandbox than in a plain shared container.

## 12. Browser automation

v1 must support headless browser automation inside the sandbox.

Requirements:

- browser dependencies available in the base image or installable by tenant
- sandbox resource limits sized for browser workloads
- terminal and file APIs sufficient for Playwright or similar tools

Full remote desktop UX is not required for v1, but the architecture should not block it later.

## 13. API surface

v1 HTTP API should include:

- `POST /v1/sandboxes`
- `GET /v1/sandboxes/:id`
- `POST /v1/sandboxes/:id/start`
- `POST /v1/sandboxes/:id/stop`
- `POST /v1/sandboxes/:id/suspend`
- `POST /v1/sandboxes/:id/resume`
- `DELETE /v1/sandboxes/:id`
- `POST /v1/sandboxes/:id/exec`
- `POST /v1/sandboxes/:id/tty`
- `GET /v1/sandboxes/:id/files/*path`
- `PUT /v1/sandboxes/:id/files/*path`
- `DELETE /v1/sandboxes/:id/files/*path`
- `POST /v1/sandboxes/:id/tunnels`
- `GET /v1/sandboxes/:id/tunnels`
- `DELETE /v1/sandboxes/:id/tunnels/:tunnelId`
- `POST /v1/sandboxes/:id/snapshots`
- `POST /v1/snapshots/:id/restore`

Streaming may use WebSocket or SSE depending on endpoint type:

- `exec` output may use SSE or WebSocket
- `tty` should use WebSocket

## 14. Data model

Core tables:

- tenants
- sandboxes
- sandbox_runtime_state
- sandbox_storage
- tunnels
- snapshots
- executions
- tty_sessions
- quotas
- audit_events

Suggested sandbox fields:

- sandbox_id
- tenant_id
- status
- runtime_backend
- base_image_ref
- cpu_limit
- memory_limit_mb
- pids_limit
- disk_limit_mb
- network_mode
- allow_tunnels
- rootfs_path or volume_ref
- workspace_volume_ref
- cache_volume_ref
- created_at
- updated_at
- last_active_at

## 15. Auth and tenancy

Minimum v1 requirements:

- bearer token authentication
- one tenant per request
- strict tenant ownership checks on every sandbox and tunnel lookup
- per-tenant quotas
- per-tenant rate limiting

The CLI should consume the same HTTP API and auth model as other clients.

## 16. Quotas and limits

Quota enforcement must happen at two layers.

Control-plane quotas:

- max sandboxes
- max running sandboxes
- max concurrent exec sessions
- max tunnels
- max aggregate CPU, memory, and storage

Runtime enforcement:

- CPU limit
- memory limit
- PIDs limit
- disk quota where practical
- network mode
- execution timeout

## 17. Recovery and cleanup

On daemon restart:

1. load metadata from SQLite
2. inspect runtime state for each sandbox
3. reconcile missing or orphaned guests
4. reattach to or mark down active sandboxes
5. rebuild tunnel state
6. resume cleanup loops

Deletion must be idempotent:

1. block new sessions
2. revoke tunnels
3. terminate exec and terminal sessions
4. stop guest
5. delete local volumes
6. keep or delete snapshots per policy
7. mark sandbox deleted

## 18. Observability

v1 observability should stay small but useful.

Required:

- structured logs
- health endpoint
- sandbox lifecycle events
- exec and terminal session events
- tunnel create and revoke events
- policy denials
- runtime failure events

Optional:

- lightweight counters
- basic per-sandbox resource telemetry

## 19. Packaging

v1 packaging targets:

- Linux `amd64`
- Linux `arm64`

Shipping model:

- one Go control-plane binary
- one runtime helper package if needed
- versioned base image artifacts
- optional minimal control-plane container image

## 20. Deliberate v1 exclusions

The following are out of scope for initial v1 unless implementation proves much easier than expected:

- Kubernetes backend
- multi-node scheduling
- live migration
- FQDN-level egress policy
- full remote desktop UX
- enterprise IAM integrations
- host-native multi-tenant mode as a production security story

## 21. Acceptance criteria

v1 is complete when all of the following are true:

- a tenant can create a persistent sandbox machine through the API
- the machine can be stopped and started without losing its environment
- the tenant can attach an interactive shell and run long-lived agents
- package installs persist across restarts
- Docker-in-Docker or an equivalent guest-local engine works inside the sandbox
- headless browser automation works inside the sandbox
- tenants cannot access each other's files, runtime state, or network
- inbound access occurs only through explicit tunnel creation
- local storage works by default and snapshot export to S3 is optional
- the control plane runs as a small single-node service with SQLite
