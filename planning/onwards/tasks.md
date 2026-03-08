# Sandbox Onwards Implementation Tasks

## 1. Re-baseline shipped scope

- [x] (R1) Update `README.md` to describe the current product as a trusted Docker-backed control plane and to reserve production multi-tenant claims for the guest-backed runtime.
- [x] (R1) Add a short status matrix under `planning/onwards/` mapping the major v1 claims to one of: shipped in trusted Docker mode, planned for guest-backed mode, or deferred.
- [x] (R1) Audit `planning/tasklist_v1.md` and re-open or annotate items that currently overstate guest runtime, bootstrap, guest-local engine, browser verification, or hard quota enforcement.
- [x] (R1) Cross-link `planning/whats_left.md`, `planning/tasks2.md`, and the new onwards plan so there is one clear source of truth for next steps.

## 2. Lock the minimal production backend

- [x] (R2, R6) Extend `internal/config/config.go` validation to accept `qemu` in addition to `docker`.
- [x] (R2, R6) Add only the minimum new config fields for QEMU startup, guest image path, SSH access, boot timeout, and cross-platform accelerator selection.
- [x] (R2, R6) Update `cmd/sandboxd/main.go` runtime wiring to construct either the existing Docker runtime or a new QEMU runtime.
- [x] (R2, R6) Fail fast at daemon startup when `docker` lacks trusted mode or when `qemu` lacks required host prerequisites on the current OS.
- [x] (R2) Add unit tests in `internal/config/` covering valid and invalid runtime selections on Linux and macOS host paths.

## 3. Add the guest-backed runtime package

- [x] (R2, R3, R4) Create `internal/runtime/qemu/runtime.go` implementing `model.RuntimeManager`.
- [x] (R2, R3) Add host-acceleration selection inside the QEMU runtime so one backend works on Linux and macOS without OS-specific forks.
- [x] (R3) Add helper code in `internal/runtime/qemu/` for sandbox filesystem layout, writable system-disk creation, workspace-disk creation, and runtime-artifact paths.
- [x] (R3, R4) Add bounded boot and readiness logic that waits for SSH connectivity and a guest bootstrap-ready marker before reporting `running`.
- [x] (R3) Implement `Create`, `Start`, `Stop`, `Destroy`, and `Inspect` first, reusing the same lifecycle semantics already used by `internal/service/service.go`.
- [x] (R3) Decide and document first-pass `Suspend` and `Resume` behavior; either implement safely or return an explicit unsupported error with tests.
- [x] (R3) Add focused unit tests in `internal/runtime/qemu/` for command assembly, readiness timeout, and runtime-state parsing.

## 4. Bootstrap a durable guest image

- [x] (R3, R7) Add a minimal guest-image build or preparation path under `images/guest/` instead of expanding the existing Docker-only image path.
- [x] (R4, R7) Ensure the guest image or first-boot bootstrap provides: SSH server, mounted `/workspace`, Git, Python, Node or npm, browser dependencies, and a guest-local container engine.
- [x] (R4, R7) Choose and document a lightweight guest init or supervision mechanism that is sufficient for restart semantics without adding a large system-management stack.
- [x] (R4) Keep guest access key material operator-provided and outside SQLite; document where it lives and how the daemon reads it.
- [x] (R7) Add a smoke script under `images/guest/` or `internal/runtime/qemu/testdata/` that proves the image can boot and accept SSH before it is used by integration tests.

## 5. Route runtime interactions through the guest boundary

- [x] (R4) Implement `Exec` in `internal/runtime/qemu/` via SSH with the same timeout and bounded-output expectations already enforced in the service layer.
- [x] (R4) Implement `AttachTTY` via SSH PTY allocation and verify resize handling remains compatible with the current WebSocket bridge.
- [x] (R4) Refactor file operations in `internal/service/service.go` and any helper code they use so the QEMU backend performs guest-scoped reads and writes over SSH-based transfer instead of direct host filesystem access.
- [x] (R4) Keep Docker file behavior unchanged for the trusted backend while avoiding backend-specific leaks into API handlers.
- [x] (R4) Add regression tests that prove cross-tenant file isolation and path validation still hold for both backends.

## 6. Make storage limits real

- [x] (R5) Define a simple disk split policy for the guest backend, for example root disk plus workspace disk derived from `disk_limit_mb`, and document the operator-visible behavior.
- [x] (R5) Ensure guest-local container engine storage lives inside quota-bounded guest disks rather than in separate untracked host storage.
- [x] (R5) Add storage measurement helpers in `internal/runtime/qemu/` and call `repository.UpdateStorageUsage` with actual measured bytes after create, stop, snapshot, restore, and reconciliation.
- [x] (R5) Update quota-related logic in `internal/service/service.go` and `internal/repository/store.go` only as needed so operator views can show measured storage in addition to requested storage.
- [x] (R5) Add Linux and macOS host coverage for disk-full behavior and for workspace persistence after stop and start.

## 7. Preserve the network and tunnel contract

- [x] (R6) Implement `internet-enabled` and `internet-disabled` behavior for QEMU using the smallest viable networking setup.
- [x] (R6) Keep sandbox-access ports loopback-only on the host unless explicitly published by the tunnel subsystem.
- [x] (R6) Update tunnel activation logic so it can reach guest services through the QEMU backend without exposing arbitrary host ports directly.
- [x] (R6, R8) Add integration tests proving no east-west access by default, no direct host exposure by default, and successful explicit tunnel create and revoke behavior.

## 8. Snapshots, recovery, and operator inspection

- [x] (R3, R8) Implement guest-aware snapshot create and restore for the writable system disk plus workspace disk while keeping the existing snapshot metadata flow in `internal/service/service.go` and `internal/repository/store.go`.
- [x] (R3, R8) Add optional S3-compatible snapshot export and restore on top of local snapshot artifacts once local capture and restore are stable.
- [x] (R8) Extend startup reconciliation in `internal/service/service.go` or adjacent runtime-recovery helpers to distinguish booting, ready, stopped, absent, and failed QEMU guests conservatively.
- [x] (R8) Add operator-visible runtime health inspection, either as a small new API endpoint or a CLI command backed by existing health plumbing, without introducing a broad admin subsystem.
- [x] (R8) Add regression coverage for control-plane restart during exec, guest boot failure, and snapshot partial failure.

## 9. Verify the workloads the project claims

- [ ] (R7, R8) Extend `internal/api/integration_test.go` or add guest-runtime integration tests for Git usage inside a guest sandbox on both Linux and macOS host paths.
- [ ] (R7, R8) Add restart-persistence tests for Python package installation.
- [ ] (R7, R8) Add restart-persistence tests for npm package installation.
- [ ] (R7, R8) Add headless browser end-to-end smoke coverage against the guest backend.
- [ ] (R7, R8) Add guest-local container engine smoke coverage that proves the host runtime socket is not required or exposed.
- [ ] (R7, R8) Add a restart-durability test for a supervised background service inside the guest.
- [ ] (R8) Run lifecycle and exec smoke coverage on both Linux and macOS using the same QEMU backend with OS-appropriate acceleration.

## 10. Low-cost follow-ons after the guest runtime lands

- [ ] (R5) Extend CPU input, validation, persistence, and quota math from integer cores to fractional or millicore values while preserving backward compatibility.
- [ ] (R4) Re-evaluate the SSH-only control channel after workload tests; add a small guest agent only if it removes a measured limitation rather than adding protocol churn.

## 11. Out of scope for this phase

- [ ] Additional guest runtime backends beyond `qemu`.
- [ ] Clustered or multi-node control-plane behavior.
