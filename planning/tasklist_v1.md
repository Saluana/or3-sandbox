# Sandbox v1 Task List

## 1. Goal

This task list turns the architecture into an implementation sequence for a small single-node control plane that manages persistent guest-backed tenant machines.

Priority labels:

- `P0`: required for a credible v1
- `P1`: important but can land after the core path is working
- `P2`: useful follow-up after v1 is stable

## 2. Phase 0: lock product and runtime decisions

### P0-1. Finalize the production runtime boundary

- [x] Select the guest-backed runtime technology for hostile multi-tenant workloads.
- [x] Document required Linux host capabilities and operational assumptions for that runtime.
- [x] Mark host-native sandbox execution as trusted-development-only or explicitly deferred from v1.

Status note:
The design decision was made, but the guest-backed production backend is not implemented in the current repository. The shipped runtime is still Docker-backed trusted mode.

### P0-2. Finalize the sandbox contract

- [x] Freeze lifecycle states: `creating`, `stopped`, `starting`, `running`, `suspending`, `suspended`, `stopping`, `deleting`, `deleted`, and `error`.
- [x] Freeze the sandbox create request schema, including image reference, limits, network mode, and tunnel policy.
- [x] Freeze which lifecycle operations are supported in v1: create, inspect, start, stop, suspend, resume, delete, exec, terminal attach, snapshots, and tunnels.

### P0-3. Finalize storage and ingress scope

- [x] Confirm local persistent writable system and workspace volumes as the required active storage model.
- [x] Confirm snapshot export to S3-compatible storage is optional and not required for first ship.
- [x] Confirm explicit tunnel creation as the only inbound access mechanism in v1.

## 3. Phase 1: control-plane foundation

### P0-4. Create the Go daemon skeleton

- [x] Define config loading and validation.
- [x] Define structured logging.
- [x] Define the HTTP router and middleware chain.
- [x] Add a health endpoint.
- [x] Add graceful shutdown behavior.

### P0-5. Add SQLite schema and data access

- [x] Create migrations for `tenants`, `sandboxes`, `sandbox_runtime_state`, `sandbox_storage`, `tunnels`, `snapshots`, `executions`, `tty_sessions`, `quotas`, and `audit_events`.
- [x] Add repository interfaces for lifecycle, execution, tunnel, snapshot, and quota data.
- [x] Add transaction helpers for multi-step lifecycle updates.
- [x] Add schema validation and migration startup checks.

### P0-6. Add auth, tenancy, and rate limiting

- [x] Implement bearer token authentication.
- [x] Resolve every request to one tenant identity.
- [x] Add tenant ownership checks to all sandbox, tunnel, snapshot, execution, and terminal lookups.
- [x] Add basic per-tenant rate limiting hooks.

## 4. Phase 2: runtime abstraction and lifecycle orchestration

### P0-7. Define the runtime manager interface

- [x] Define runtime methods for create, start, stop, suspend, resume, destroy, inspect, exec, terminal attach, snapshot create, and snapshot restore.
- [x] Keep the API independent of guest implementation details.
- [x] Define explicit runtime status and error mapping for reconciliation.

### P0-8. Implement lifecycle handlers

- [x] Implement `POST /v1/sandboxes`.
- [x] Implement `GET /v1/sandboxes/:id`.
- [x] Implement `POST /v1/sandboxes/:id/start`.
- [x] Implement `POST /v1/sandboxes/:id/stop`.
- [x] Implement `POST /v1/sandboxes/:id/suspend`.
- [x] Implement `POST /v1/sandboxes/:id/resume`.
- [x] Implement `DELETE /v1/sandboxes/:id`.

### P0-9. Implement runtime-backed create and boot flow

- [x] Allocate sandbox ID and initial metadata.
- [x] Allocate persistent storage and network resources.
- [x] Materialize the guest from the base image.
- [x] Apply limits and network policy.
- [ ] Boot the guest and run bootstrap.
- [x] Persist status transitions and last-known runtime state.

## 5. Phase 3: storage, files, and snapshots

### P0-10. Implement local volume management

- [x] Create per-sandbox writable system storage.
- [x] Create per-sandbox workspace storage.
- [x] Add optional cache volume support if it stays operationally small.
- [x] Support quota accounting where practical.
- [x] Clean up storage on sandbox deletion.

### P0-11. Implement the file API

- [x] Implement file read.
- [x] Implement file write and overwrite.
- [x] Implement file delete.
- [x] Implement directory creation.
- [x] Add path validation and traversal rejection.
- [x] Ensure host paths are never exposed directly.

### P1-12. Implement snapshot support

- [x] Implement `POST /v1/sandboxes/:id/snapshots`.
- [x] Persist snapshot metadata and status.
- [x] Implement `POST /v1/snapshots/:id/restore`.
- [x] Validate restore safety for writable system and workspace state.

### P1-13. Add optional S3-compatible snapshot export

- [ ] Add operator config for S3-compatible snapshot export.
- [ ] Export snapshot artifacts and metadata.
- [ ] Restore from exported snapshot metadata when configured.

## 6. Phase 4: runtime interaction

### P0-14. Implement one-off exec sessions

- [x] Implement `POST /v1/sandboxes/:id/exec`.
- [x] Stream stdout and stderr.
- [x] Enforce per-exec timeout.
- [x] Kill the full guest-side process tree on timeout or cancellation.
- [x] Record execution metadata and bounded output bookkeeping.

### P0-15. Implement interactive terminal sessions

- [x] Implement `POST /v1/sandboxes/:id/tty`.
- [x] Allocate PTYs inside the guest.
- [x] Bridge terminal streams over WebSocket.
- [x] Support resize events.
- [x] Support multiple concurrent terminal attachments per sandbox.

### P0-16. Support long-running guest processes

- [x] Allow detached commands and guest service starts.
- [x] Ensure client disconnects do not terminate unrelated guest workloads.
- [x] Expose enough runtime metadata to reconnect safely.

## 7. Phase 5: network isolation and tunnels

### P0-17. Implement per-sandbox network policy

- [x] Support `internet-enabled` mode.
- [x] Support `internet-disabled` mode.
- [x] Block east-west tenant connectivity.
- [x] Prevent direct host network exposure by default.

### P0-18. Implement tunnel management

- [x] Implement `POST /v1/sandboxes/:id/tunnels`.
- [x] Implement `GET /v1/sandboxes/:id/tunnels`.
- [x] Implement `DELETE /v1/sandboxes/:id/tunnels/:tunnelId`.
- [x] Publish sandbox ports only through the managed tunnel path.
- [x] Log tunnel create and revoke events.

### P1-19. Add operator defaults and per-tenant tunnel policy

- [x] Add allow or deny tunnel policy per tenant.
- [x] Add default tunnel visibility and auth settings.
- [x] Add audit fields for tunnel policy denials.

## 8. Phase 6: guest image and usability

### P0-20. Build the base guest image

- [x] Choose the Linux distribution base.
- [x] Install shell and core Unix tooling.
- [x] Install Git.
- [x] Install Python.
- [x] Install Node and npm.
- [x] Install browser automation dependencies.
- [x] Install a lightweight init or supervision layer.

### P0-21. Support a guest-local container engine

- [ ] Install and configure Docker-in-Docker or an equivalent guest-local engine.
- [x] Ensure the host container runtime socket is never exposed.
- [ ] Ensure inner-engine storage counts toward sandbox quota.

Status note:
The current base image installs `docker.io`, but the guest-backed runtime path, inner-engine bootstrap, and end-to-end guest-local engine verification are still open.

### P0-22. Verify browser automation support

- [ ] Run headless browser smoke tests in a sandbox.
- [ ] Verify filesystem and network behavior for browser workloads.
- [ ] Verify browser dependencies survive restart in the persistent image model.

## 9. Phase 7: quotas, cleanup, and recovery

### P0-23. Implement quota checks

- [x] Enforce maximum sandboxes per tenant.
- [x] Enforce maximum running sandboxes per tenant.
- [x] Enforce aggregate CPU, memory, and storage limits.
- [x] Enforce maximum concurrent exec sessions.
- [x] Enforce maximum active tunnels.

### P0-24. Implement cleanup flows

- [x] Make sandbox deletion idempotent.
- [x] Revoke tunnels before final delete.
- [x] Force-stop stuck runtime resources when needed.
- [x] Remove storage volumes on delete.
- [x] Preserve snapshots only when policy allows.

### P0-25. Implement restart recovery

- [x] Load SQLite state at daemon startup.
- [x] Inspect runtime state for every non-deleted sandbox.
- [x] Reconcile missing and orphaned guest resources.
- [x] Recover sandbox and tunnel status.
- [x] Resume cleanup loops.

## 10. Phase 8: CLI and operator experience

### P0-26. Build the CLI

- [x] Add sandbox create.
- [x] Add sandbox start, stop, suspend, and resume.
- [x] Add exec command support.
- [x] Add terminal attach support.
- [x] Add upload and download file commands.
- [x] Add tunnel create, list, and revoke commands.

### P1-27. Add admin and debugging commands

- [x] Add quota inspection commands.
- [x] Add sandbox listing commands.
- [x] Add tunnel inspection commands.
- [ ] Add runtime health inspection commands.

## 11. Phase 9: hardening and verification

### P0-28. Add tenant isolation integration tests

- [x] Test cross-tenant file access denial.
- [x] Test cross-tenant network denial.
- [x] Test tunnel ownership checks.
- [x] Test sandbox deletion cleanup.
- [x] Test auth and ownership checks.

### P0-29. Add workload verification tests

- [x] Test shell exec.
- [ ] Test Git usage.
- [ ] Test Python package installation persistence.
- [ ] Test npm package installation persistence.
- [ ] Test headless browser automation.
- [ ] Test guest-local container engine behavior.
- [x] Test long-running agent reconnect behavior.

### P0-30. Run failure-mode drills

- [ ] Drill control-plane restart during exec.
- [ ] Drill guest boot failure.
- [x] Drill quota exceed handling.
- [x] Drill live tunnel revoke behavior.
- [ ] Drill disk-full handling.

## 12. Recommended implementation order

- [ ] Complete Phase 0 decision lock.
- [ ] Complete Phase 1 control-plane foundation.
- [ ] Complete Phase 2 runtime abstraction and lifecycle.
- [ ] Complete Phase 3 storage, files, and snapshots.
- [ ] Complete Phase 4 runtime interaction.
- [ ] Complete Phase 5 network isolation and tunnels.
- [ ] Complete Phase 6 guest image and usability.
- [ ] Complete Phase 7 quotas, cleanup, and recovery.
- [ ] Complete Phase 8 CLI and operator experience.
- [ ] Complete Phase 9 hardening and verification.

## 13. Explicit cuts to protect v1

- [ ] Do not add Kubernetes backend work to the v1 critical path.
- [ ] Do not add multi-node scheduling to the v1 critical path.
- [ ] Do not add full remote desktop UX to the v1 critical path.
- [ ] Do not add FQDN-level egress filtering to the v1 critical path unless it becomes unexpectedly simple.
- [ ] Do not treat host-native shared-kernel runtime as a production multi-tenant story.
- [ ] Do not add large observability stacks or rich browser UI before the core system works.

## 14. v1 exit criteria

- [ ] A tenant can create a persistent sandbox machine through the API.
- [ ] A tenant can start, stop, suspend, resume, and delete that machine safely.
- [ ] Shells, files, network policy, package installs, background services, browser automation, and guest-local containers work inside the sandbox.
- [ ] Tunnels are explicit, auditable, and revocable.
- [ ] The host and other tenants remain isolated.
- [ ] The system is usable through both the HTTP API and CLI.

See also:

- `planning/whats_left.md`
- `planning/tasks2.md`
- `planning/onwards/status_matrix.md`
- `planning/onwards/tasks.md`
