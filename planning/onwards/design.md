# Sandbox Onwards Technical Design

## 1. Overview

The smallest credible path forward is to keep the existing Go control plane and add one guest-backed backend that runs on both Linux and macOS: a QEMU runtime controlled over SSH after first-boot bootstrap.

Why this fits the current repository:

- the codebase already uses a narrow `model.RuntimeManager` abstraction
- the current Docker runtime already shells out to a host binary rather than relying on a large SDK
- SQLite already stores sandbox state, runtime state, and storage usage
- the service layer already owns lifecycle transitions, quota checks, reconciliation, and audit flow

This design intentionally avoids:

- adding Kubernetes, libvirt, or a second daemon
- inventing a custom guest agent for the first production backend
- broad API redesigns
- a large new configuration model

The design chooses these lightweight defaults:

- one production backend: `qemu`
- one cross-platform host strategy: KVM on Linux, HVF on macOS
- one control channel: SSH
- one writable system disk per sandbox
- one separate persistent workspace disk per sandbox
- host-side tunnel exposure remains explicit and loopback-based

## 2. Affected areas

- `internal/config/config.go`
  - extend runtime backend validation to support `qemu`
  - add only the operator settings needed to boot and reach the guest reliably
- `internal/model/runtime.go`
  - keep the existing `RuntimeManager` surface if possible
  - document any guest-backend limitations, especially around suspend or resume
- `internal/runtime/qemu/`
  - new backend package implementing the existing runtime interface
  - shell out to `qemu-system-*`, `ssh`, and `scp` or `sftp` rather than adding heavy dependencies
  - select host acceleration based on OS without creating separate Linux and macOS runtime packages
- `internal/service/service.go`
  - keep lifecycle orchestration centralized
  - add actual storage usage refreshes and guest-readiness-aware error handling where needed
- `internal/repository/store.go`
  - reuse existing runtime and storage tables
  - add helper methods only if the current store API is missing data needed for health inspection or reconciliation
- `internal/db/db.go`
  - no schema migration is required for the first pass unless implementation proves an operator-visible runtime metadata gap
- `cmd/sandboxd/main.go`
  - instantiate the selected runtime backend and fail fast on invalid production runtime config
- `cmd/sandboxctl/`
  - keep the current command surface
  - add health inspection commands only if they can be implemented without widening the API unnecessarily
- `images/`
  - add a lightweight guest-image build path or guest-image preparation script for the `qemu` backend
- `internal/api/` and `internal/runtime/*_test.go`
  - extend coverage for the guest backend, storage enforcement, failure drills, and Linux or macOS host compatibility

## 3. Control flow / architecture

### 3.1 Backend choice

The control plane keeps a single runtime selection at process startup:

- `docker`: trusted or development only
- `qemu`: production-oriented guest-backed backend

The service layer remains backend-agnostic after startup.

Within the `qemu` backend, host acceleration is selected automatically:

- Linux prefers KVM
- macOS prefers HVF
- unsupported host or accelerator combinations fail fast during daemon startup rather than degrading silently

### 3.2 Guest creation flow

1. `service.CreateSandbox` persists the sandbox in `creating` state exactly as it does today.
2. The `qemu` runtime creates a sandbox directory under the existing storage root.
3. The runtime creates:
   - a writable qcow2 overlay for the guest system disk
   - a separate raw or qcow2 workspace disk sized from the sandbox limit policy
   - optional cache storage only if it fits inside the same quota model
4. The runtime creates per-sandbox host artifacts derived from sandbox ID:
   - QEMU monitor socket
   - SSH known-hosts file
   - ephemeral loopback forwarding info
5. QEMU boots from the operator-configured base guest image plus sandbox-specific writable disks.
6. A per-sandbox cloud-init or first-boot script:
   - installs the authorized SSH key
   - mounts the workspace disk at `/workspace`
   - prepares a guest marker such as `/var/lib/or3/bootstrap.ready`
   - starts a lightweight guest init or service supervisor if required
7. The runtime waits for SSH reachability and the bootstrap-ready marker.
8. Only after readiness succeeds does the service transition the sandbox to `running`.
9. If readiness fails, the sandbox is marked `error` and the guest artifacts remain inspectable for debugging until delete.

### 3.3 Exec, TTY, and file flow

The first production backend uses SSH as the single control channel.

- `Exec` runs `ssh` with a bounded command wrapper and preserves timeout behavior from the service layer.
- `AttachTTY` allocates a PTY through `ssh -tt` and bridges stdin or stdout through the existing WebSocket path.
- File APIs operate through the guest boundary using `scp`, `sftp`, or streamed `tar` over SSH rather than direct host-path reads.
- Health checks use a lightweight SSH command to validate guest readiness after boot and during reconciliation.

This is intentionally simpler than introducing a long-lived guest agent and matches the repo's current pattern of shelling out to trusted host tools.

### 3.4 Network model

For the first guest backend, use QEMU user-mode networking to minimize host complexity and preserve cross-platform behavior.

- `internet-enabled` uses QEMU user networking with outbound access enabled.
- `internet-disabled` uses the same mode with outbound access restricted.
- No sandbox gets direct host exposure by default.
- Per-sandbox SSH access uses a loopback-only forwarded host port.
- Tunnel publication uses loopback-only forwarded ports on the host side and continues to be mediated by the existing control-plane tunnel path.

This keeps the first production backend small and avoids introducing bridge management, per-sandbox tap devices, or a firewall controller unless later testing proves they are required.

### 3.5 Snapshot and restore

Snapshots stay local-first.

- system state is captured from the writable system disk artifact
- workspace state is captured from the workspace disk artifact
- snapshot metadata remains in SQLite and existing snapshot APIs stay in place
- optional S3 export layers on after the local snapshot path is stable

A snapshot operation should not mutate active sandbox metadata until both disk artifacts have been captured successfully.

### 3.6 Reconciliation and health

On daemon startup:

1. load non-deleted sandboxes from SQLite
2. inspect runtime artifacts for the selected backend
3. determine whether each guest is absent, booting, ready, stopped, or failed
4. update runtime state and sandbox status conservatively
5. do not delete sandbox disks automatically unless the sandbox is already in a delete flow

A simple backend health check should confirm:

- QEMU binary exists
- the configured or auto-selected accelerator is available for the current host OS
- base guest image exists and is readable
- SSH bootstrap material exists and is readable

## 4. Data and persistence

### 4.1 SQLite changes

No SQLite schema change is required for the first pass.

Reasons:

- `sandboxes.runtime_backend` already records backend choice
- `sandbox_runtime_state` already stores runtime status, runtime ID, PID, IP address, and last error
- `sandbox_storage` already stores measured storage usage buckets

If later implementation shows an operator need for runtime-specific metadata, prefer deriving it from predictable sandbox filesystem layout before adding schema.

### 4.2 Storage layout

For a sandbox with ID `sbx-...`, the runtime should use a predictable layout under the existing storage root, for example:

- `rootfs/overlay.qcow2`
- `workspace/workspace.img`
- `cache/` only if still needed after the guest-disk split
- `.runtime/monitor.sock`
- `.runtime/ssh-known-hosts`

The first-pass disk policy is intentionally simple and operator-visible:

- split `disk_limit_mb` evenly between the writable system disk and the persistent workspace disk
- keep guest-local engine state on the writable system disk by default, so package layers and `/var/lib/docker` are counted inside the same sandbox budget
- measure both disks plus snapshots and cache artifacts when refreshing storage usage in SQLite

The key lightweight design choice is that guest-local container engine data must live inside quota-bounded guest disks. That removes the need for special host-side accounting for inner engine images and layers.

### 4.3 Config changes

Add only the minimum operator settings needed for `qemu`:

- `SANDBOX_RUNTIME=qemu|docker`
- `SANDBOX_QEMU_BINARY` with a sensible default
- `SANDBOX_QEMU_ACCEL=auto|kvm|hvf`
- `SANDBOX_QEMU_BASE_IMAGE_PATH`
- `SANDBOX_QEMU_SSH_USER`
- `SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH`
- `SANDBOX_QEMU_BOOT_TIMEOUT`

Optional follow-up settings such as explicit CPU model, machine type, or advanced networking should stay out of the initial implementation unless they are required to make the runtime work.

### 4.4 Session implications

No new application-level session manager is needed.

- long-lived process and package state belong inside the guest
- TTY sessions remain attachment records
- execution history remains bounded and stored the same way as today

## 5. Interfaces and types

### 5.1 Runtime interface

Keep the current `model.RuntimeManager` interface unchanged if practical.

That lets the service layer, API handlers, and CLI remain stable while the backend implementation changes behind the interface.

### 5.2 QEMU runtime shape

A small runtime package is enough:

```go
type Runtime struct {
    qemuBinary     string
    qemuImgBinary  string
    sshBinary      string
    scpBinary      string
    accelerator    string
    baseImagePath  string
    sshUser        string
    sshKeyPath     string
    bootTimeout    time.Duration
}
```

Helper responsibilities should stay local to the package:

- command construction for QEMU startup and shutdown
- SSH command execution wrappers
- readiness polling
- disk image creation and copy-on-write setup
- storage usage measurement
- monitor interaction only if required for controlled shutdown or port forwarding

### 5.3 Suspend and resume

Keep this area intentionally narrow.

Recommended first-pass behavior:

- implement `Suspend` and `Resume` only if QEMU monitor support can be added without destabilizing core flows
- otherwise return a clear runtime error such as `suspend not yet supported by qemu backend` and document that limitation in operator-facing docs

That is preferable to a fragile partial implementation.

Current first-pass implementation choice:

- the `qemu` backend returns an explicit unsupported error for `Suspend` and `Resume`
- operator-facing guest-image notes live under `images/guest/README.md`
- the first durable-guest boot path uses guest `systemd` plus `or3-bootstrap.service` to create `/workspace` and the readiness marker

### 5.4 Late-phase follow-ons

These are reasonable additions after the cross-platform guest runtime is stable, but they should not distort the initial runtime shape:

- fractional CPU or millicore parsing can extend the existing config, API, and quota model with limited blast radius
- S3-compatible snapshot export can layer on top of local snapshot artifacts without changing active runtime control flow
- a custom guest agent should only be introduced if SSH proves measurably insufficient for exec, PTY, file transfer, or health checks

Current outcome after workload coverage:

- fractional CPU and millicore support is implemented by storing CPU in exact millicores while preserving integer-compatible JSON and SQLite fields for older callers
- Docker accepts fractional CPU values directly, while QEMU currently rejects non-whole cores until it has a real fractional throttle instead of silently changing the requested limit
- SSH remains sufficient for exec, PTY, file transfer, health checks, and workload smoke coverage, so no guest agent is introduced in this phase

## 6. Failure modes and safeguards

- Invalid config
  - reject unknown runtime backends
  - reject `docker` without explicit trusted mode
  - reject `qemu` without base image, SSH key, required host binaries, or a supported accelerator for the current OS
- Guest boot failure
  - bound readiness wait
  - persist the failure reason
  - preserve artifacts for inspection until delete
- SSH control failure
  - surface command failures as runtime errors
  - avoid logging private key paths or command contents containing secrets
- Disk-full behavior
  - rely on bounded guest disks so writes fail inside the sandbox rather than exhausting the host storage silently
  - refresh measured storage usage after lifecycle and snapshot operations
- Snapshot failure
  - write snapshot artifacts into a temporary staging path
  - only mark snapshot ready after both system and workspace capture succeed
- Reconciliation mistakes
  - never auto-delete guest disks during inspection-only recovery
  - prefer marking sandboxes `error` over silently forcing recreation
- Tunnel mistakes
  - keep forwarded ports loopback-only unless the explicit tunnel layer publishes them
  - revoke forwarding before marking a tunnel inactive

## 7. Testing strategy

Use Go's `testing` package and keep the feedback loop split into small layers.

### 7.1 Unit coverage

- `internal/config`
  - runtime validation for `docker` vs `qemu`
  - required-qemu-setting validation
- `internal/runtime/qemu`
  - QEMU command generation
  - readiness timeout logic
  - storage usage measurement
  - loopback forwarding plan generation
- `internal/service`
  - sandbox state transitions when guest readiness fails
  - storage refresh and error propagation

### 7.2 Integration coverage

Add integration coverage for the `qemu` backend on both Linux and macOS host paths:

- lifecycle smoke tests on Linux and macOS
- exec and PTY attach through SSH on Linux and macOS
- file read or write through the guest boundary
- snapshot and restore
- tunnel create and revoke against loopback forwarding

Where CI cannot provide equivalent virtualization features on both hosts, keep the same test suite shape and run the missing host coverage through documented release-gating scripts.

### 7.3 Workload regression coverage

Add workload tests that prove the claims already made by the project:

- Git inside the guest
- Python package install persists after restart
- npm package install persists after restart
- browser automation runs end to end
- guest-local container engine can pull and run a small container

### 7.4 Failure drills

Add targeted drills for:

- control-plane restart during exec
- guest boot failure
- disk-full handling
- snapshot partial failure

These can be integration tests or operator scripts, but they must be runnable and documented.
