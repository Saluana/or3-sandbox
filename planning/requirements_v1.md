# Sandbox v1 Requirements

## 1. Overview

Sandbox v1 is a lightweight multi-tenant system for hosting persistent tenant machines on a single Linux node.

Scope assumptions for v1:

- production multi-tenant isolation uses a guest-backed runtime per sandbox
- each sandbox is a durable machine, not an ephemeral job container
- the control plane is a single Go daemon with SQLite metadata
- access is provided through an HTTP API and CLI
- trusted native host-process mode may exist later, but it is not part of the v1 production requirement set

## 2. Requirements

### 2.1 Requirement 1: Persistent sandbox lifecycle

The system must manage each sandbox as a long-lived tenant machine with explicit lifecycle states.

Acceptance criteria:

- the system supports `creating`, `stopped`, `starting`, `running`, `suspending`, `suspended`, `stopping`, `deleting`, `deleted`, and `error` states
- the API supports create, inspect, start, stop, suspend, resume, and delete operations
- each sandbox record includes sandbox ID, tenant ID, runtime backend, base image reference, resource limits, network mode, tunnel policy, and timestamps
- deleting a sandbox destroys local state unless a snapshot or backup is retained by policy

### 2.2 Requirement 2: Durable environment behavior

The sandbox must behave like a durable computer across restarts.

Acceptance criteria:

- the writable system layer persists across stop and start cycles
- the workspace volume persists across stop and start cycles
- package installs and configuration changes persist across restarts
- background services can restart after boot through the guest init or supervision mechanism

### 2.3 Requirement 3: One-off command execution

The system must support bounded one-off command execution inside a running sandbox.

Acceptance criteria:

- the API accepts command, args, env, cwd, and timeout fields
- stdout and stderr can be streamed to the caller
- the system returns exit status and execution timing metadata
- timeout or cancellation kills the full process tree in the guest
- execution output is bounded and oversized output can be truncated or spilled into an artifact flow without exhausting daemon memory

### 2.4 Requirement 4: Interactive terminal attachment

The system must support interactive shell attachment to a running sandbox.

Acceptance criteria:

- PTY-backed sessions are supported through the API and CLI
- stdin, stdout, and stderr are streamed in real time
- terminal resize events are supported
- multiple concurrent terminal sessions can attach to the same sandbox without corrupting state

### 2.5 Requirement 5: Long-running agents and services

The platform must support long-lived workloads inside the sandbox.

Acceptance criteria:

- user processes can continue running after the client disconnects
- the control plane distinguishes between one-off exec sessions, attached terminals, and persistent guest services
- reconnecting to a sandbox does not require recreating the sandbox environment

### 2.6 Requirement 6: File operations and workspace safety

The system must expose minimal file operations without exposing host paths.

Acceptance criteria:

- the API supports read, write, delete, and directory creation inside the sandbox-visible workspace
- import and export paths are validated before transfer
- path traversal outside the sandbox-visible filesystem is rejected
- host filesystem paths are never exposed directly to the tenant

### 2.7 Requirement 7: Network policy and tunnel ingress

Each sandbox must have an isolated network policy with explicit inbound access.

Acceptance criteria:

- v1 supports `internet-enabled` and `internet-disabled` modes
- tenant sandboxes cannot reach one another over the internal network
- sandboxes do not bind directly to the host network by default
- inbound access is available only through explicit tunnel creation
- tunnel records include tunnel ID, sandbox ID, target port, protocol, auth mode, visibility, `created_at`, and `revoked_at`

### 2.8 Requirement 8: Snapshot and restore support

The system must support local snapshot and restore flows for persistent sandboxes.

Acceptance criteria:

- the API supports snapshot creation and restore
- snapshot metadata is stored in SQLite
- snapshot restore recreates the persistent workspace and writable system state for the target sandbox
- optional S3-compatible export remains additive and is not required for the base v1 release

### 2.9 Requirement 9: Tenant isolation, auth, and quotas

The control plane must enforce strict tenant ownership and quota boundaries.

Acceptance criteria:

- every request is authenticated with a bearer token or equivalent operator-selected auth mode
- every sandbox, tunnel, snapshot, execution, and terminal lookup verifies tenant ownership
- per-tenant quotas cover maximum sandboxes, maximum running sandboxes, maximum concurrent exec sessions, maximum tunnels, and aggregate CPU, memory, and storage limits
- per-tenant rate limiting is supported at the API boundary

### 2.10 Requirement 10: Runtime security boundary

The production sandbox runtime must rely on a guest boundary rather than shared-kernel process isolation alone.

Acceptance criteria:

- production multi-tenant mode uses a guest-backed sandbox runtime per tenant sandbox
- host root or privileged host capabilities are never exposed to tenants
- root or passwordless `sudo` inside the guest is permitted only within the guest boundary
- Docker-in-Docker or an equivalent guest-local engine never exposes the host container runtime socket

### 2.11 Requirement 11: Recovery and cleanup

The control plane must recover safely after restart and clean up idempotently.

Acceptance criteria:

- on daemon restart, the system reloads SQLite state, inspects runtime state, reconciles orphaned guests, rebuilds tunnel state, and resumes cleanup loops
- sandbox deletion blocks new sessions before stopping the guest and revoking tunnels
- deletion remains safe to retry after partial failure
- stopped sandboxes consume disk only, while suspended sandboxes may preserve memory state only if the implementation remains operationally simple

### 2.12 Requirement 12: Packaging and operator experience

The system must stay operationally small.

Acceptance criteria:

- the control plane ships as one Go daemon for Linux `amd64` and `arm64`
- SQLite is the default and only required metadata store for v1
- the operator can configure SQLite path, sandbox storage roots, default limits, default network policy, base image reference, auth settings, and cleanup intervals
- the platform can expose the same control-plane API whether the daemon itself runs directly on Linux or inside a minimal container image

## 3. Non-functional constraints

- deterministic behavior: lifecycle transitions, cleanup flows, and tunnel revocation must behave predictably across restarts
- low memory usage: the control plane must keep bounded in-memory state and avoid per-sandbox heavyweight sidecars
- bounded execution: command timeouts, terminal session handling, streamed output, and reconnect bookkeeping must be bounded to prevent host exhaustion
- SQLite safety: schema migrations must be additive or otherwise backward-compatible for existing metadata whenever practical, and must not require an external database
- secure files and network access: file APIs must validate paths, tunnel creation must be explicit and revocable, and no default east-west connectivity is allowed
- secret handling: auth credentials, operator keys, and tunnel secrets must not be logged or returned to unauthorized tenants
- scope discipline: Kubernetes, multi-node scheduling, enterprise IAM, rich browser IDE UX, and host-native production multi-tenancy are out of scope for v1
