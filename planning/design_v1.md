# Sandbox v1 Design

## 1. Overview

Sandbox v1 is a single-node control plane for persistent tenant machines backed by a guest runtime.

This design fits the current architecture because it keeps the system small while preserving a strong tenant boundary:

- one Go control-plane daemon
- one SQLite metadata store
- one production-grade guest-backed runtime abstraction
- persistent writable system and workspace storage per sandbox
- explicit tunnel-based ingress instead of direct host exposure

The design deliberately removes earlier native-process and container-per-tenant assumptions because they conflict with the stated hostile multi-tenant threat model.

## 2. Affected areas

### 2.1 Control-plane daemon

Owns API auth, tenant authorization, lifecycle orchestration, quota checks, and recovery.

### 2.2 Runtime manager

Owns guest creation, boot, stop, suspend, resume, exec, terminal attach, runtime inspection, and teardown.

### 2.3 Storage manager

Owns per-sandbox writable system layers, persistent workspace volumes, optional cache volumes, snapshots, and local quota accounting.

### 2.4 Network and tunnel manager

Owns sandbox network policy, tenant isolation, outbound internet enablement, and explicit tunnel lifecycle.

### 2.5 Image and bootstrap pipeline

Owns the guest base image, bootstrap agent or first-boot setup, runtime tooling, and reproducible versioning.

### 2.6 CLI and API surface

Owns lifecycle, exec, terminal, file, tunnel, and snapshot operations against the same HTTP API.

## 3. Control flow and architecture

### 3.1 Sandbox creation

1. Authenticate the request and resolve the tenant.
2. Validate quotas, requested limits, and network policy.
3. Persist initial metadata in SQLite with status `creating`.
4. Allocate storage layers and runtime network resources.
5. Materialize the sandbox from the selected base image.
6. Boot the guest and run bootstrap inside the guest if required.
7. Persist final state as `running` or `stopped`, depending on create semantics.

### 3.2 Start, stop, suspend, and resume

Lifecycle operations always go through SQLite state first, then the runtime manager, then final reconciliation:

1. verify tenant ownership and current status
2. update status to transitional state
3. invoke runtime action
4. reconcile observed runtime state
5. persist final status or `error`

### 3.3 One-off exec flow

1. Verify sandbox ownership and `running` status.
2. Validate command, environment, cwd, and timeout.
3. Open a bounded stream for stdout and stderr.
4. Execute in the guest through the runtime manager.
5. Record exit metadata and bounded output bookkeeping.

### 3.4 Interactive terminal flow

1. Verify sandbox ownership and `running` status.
2. Allocate a PTY in the guest.
3. Bridge the PTY through WebSocket transport.
4. Forward resize events and disconnect signals.
5. Record terminal session metadata for audit and cleanup.

### 3.5 Tunnel flow

1. Verify sandbox ownership, tunnel policy, and target port.
2. Create the tunnel metadata record in SQLite.
3. Ask the tunnel manager or gateway to activate an outbound-first mapping.
4. Return the tunnel descriptor to the caller.
5. On revoke, disable the route first and then mark `revoked_at`.

### 3.6 Recovery flow

1. Load sandbox, tunnel, and snapshot metadata from SQLite.
2. Inspect guest runtime state for each non-deleted sandbox.
3. Reconcile missing, stopped, and orphaned guests.
4. Rebuild active tunnel state.
5. Resume cleanup loops and mark inconsistent resources for repair.

## 4. Data and persistence

### 4.1 SQLite tables

Core tables:

- `tenants`
- `sandboxes`
- `sandbox_runtime_state`
- `sandbox_storage`
- `tunnels`
- `snapshots`
- `executions`
- `tty_sessions`
- `quotas`
- `audit_events`

Representative sandbox fields:

- `sandbox_id`
- `tenant_id`
- `status`
- `runtime_backend`
- `base_image_ref`
- `cpu_limit`
- `memory_limit_mb`
- `pids_limit`
- `disk_limit_mb`
- `network_mode`
- `allow_tunnels`
- `rootfs_path_or_volume_ref`
- `workspace_volume_ref`
- `cache_volume_ref`
- `created_at`
- `updated_at`
- `last_active_at`

### 4.2 Storage model

Each sandbox has:

1. a shared read-only base image
2. a writable system layer
3. a persistent workspace volume
4. an optional persistent cache volume

Snapshots capture at least the writable system layer and workspace volume. Optional S3 export is metadata-driven and does not replace local active storage.

### 4.3 Config model

Operator configuration should include:

- SQLite path
- sandbox storage roots
- default base image reference
- default CPU, memory, PID, and disk limits
- default network policy
- auth settings
- tunnel defaults and allow or deny behavior
- cleanup and reconciliation intervals

### 4.4 Session and memory scope

No separate language-kernel session manager is required in v1. Long-lived state belongs inside the durable guest machine itself, and terminal sessions are attachment records rather than application-level interpreter sessions.

## 5. Interfaces and types

### 5.1 Runtime manager interface

```go
type RuntimeManager interface {
	Create(ctx context.Context, spec SandboxSpec) (RuntimeHandle, error)
	Start(ctx context.Context, sandboxID string) error
	Stop(ctx context.Context, sandboxID string, force bool) error
	Suspend(ctx context.Context, sandboxID string) error
	Resume(ctx context.Context, sandboxID string) error
	Destroy(ctx context.Context, sandboxID string) error
	Inspect(ctx context.Context, sandboxID string) (RuntimeState, error)
	Exec(ctx context.Context, sandboxID string, req ExecRequest) (ExecHandle, error)
	AttachTTY(ctx context.Context, sandboxID string, req TTYRequest) (TTYHandle, error)
	CreateSnapshot(ctx context.Context, sandboxID string, req SnapshotRequest) (SnapshotInfo, error)
	RestoreSnapshot(ctx context.Context, snapshotID string, targetSandboxID string) error
}
```

### 5.2 Request and record shapes

```go
type SandboxSpec struct {
	SandboxID     string
	TenantID      string
	BaseImageRef  string
	CPULimit      int
	MemoryLimitMB int
	PIDsLimit     int
	DiskLimitMB   int
	NetworkMode   string
	AllowTunnels  bool
}

type ExecRequest struct {
	Command []string
	Env     map[string]string
	Cwd     string
	Timeout time.Duration
}
```

These types stay intentionally small. The control plane should not become a language-specific execution broker.

### 5.3 API surface

v1 HTTP endpoints:

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

`tty` should use WebSocket. `exec` streaming may use SSE or WebSocket, but the implementation should choose one transport and keep it consistent.

## 6. Failure modes and safeguards

### 6.1 Invalid config

- fail fast on invalid SQLite path, missing storage roots, or unknown base image reference
- reject startup if the selected runtime backend cannot satisfy the required guest isolation guarantees

### 6.2 Runtime and boot failures

- mark the sandbox `error` when guest creation or bootstrapping fails
- ensure partially allocated volumes and network resources are cleaned up or clearly marked for cleanup

### 6.3 Exec and terminal failures

- enforce timeout and bounded output on exec requests
- kill the full process tree on timeout or explicit cancellation
- clean up PTYs and terminal session records on disconnect or runtime loss

### 6.4 Tunnel failures

- do not publish a tunnel record as active until the gateway confirms activation
- revoke tunnel access before deleting the database record
- log create, revoke, and policy-denied events

### 6.5 Snapshot and restore failures

- keep snapshot metadata separate from active sandbox state until capture completes
- make restore idempotent where possible and fail without partially swapping in corrupt state

### 6.6 Session isolation mistakes

- never reuse runtime handles, network identity, or storage references across tenant sandboxes
- require tenant ownership checks on every sandbox, tunnel, snapshot, execution, and terminal lookup

## 7. Testing strategy

### 7.1 Unit coverage

- lifecycle state validation
- path normalization and traversal rejection
- quota enforcement
- tunnel policy checks
- config validation and defaulting

### 7.2 SQLite-backed integration coverage

- sandbox create, start, stop, suspend, resume, and delete flows
- snapshot metadata creation and restore bookkeeping
- restart recovery and orphan reconciliation
- tunnel lifecycle persistence and revoke behavior

### 7.3 Runtime integration coverage

- guest boot and bootstrap success path
- one-off exec with timeout and process-tree cleanup
- terminal attach and reconnect behavior
- durable package install persistence across restart
- guest-local container engine smoke tests
- headless browser automation smoke tests

### 7.4 Security regression coverage

- cross-tenant authorization denial
- no default east-west connectivity
- no direct host socket exposure
- explicit tunnel-only ingress behavior

## 8. Deliberate exclusions

- host-native multi-tenant production mode
- Kubernetes scheduling or multi-node orchestration
- full remote desktop UX
- enterprise IAM integrations
- large observability stacks or mandatory external databases

## 9. Summary

Sandbox v1 stays small by keeping the control plane simple and moving durable workload state into a strongly isolated guest per tenant sandbox. That model matches the architecture, preserves multi-tenant safety, and avoids rebuilding language-specific session systems inside the daemon.
