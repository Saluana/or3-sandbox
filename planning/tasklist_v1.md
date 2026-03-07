# Sandbox v1 Task List

## 1. Goal

This task list turns the architecture into an implementation sequence for a small single-node control plane that manages persistent guest-backed tenant machines.

Priority labels:

- `P0`: required for a credible v1
- `P1`: important but can land after the core path is working
- `P2`: useful follow-up after v1 is stable

## 2. Phase 0: lock product and runtime decisions

### P0-1. Finalize the production runtime boundary

- [ ] Select the guest-backed runtime technology for hostile multi-tenant workloads.
- [ ] Document required Linux host capabilities and operational assumptions for that runtime.
- [ ] Mark host-native sandbox execution as trusted-development-only or explicitly deferred from v1.

### P0-2. Finalize the sandbox contract

- [ ] Freeze lifecycle states: `creating`, `stopped`, `starting`, `running`, `suspending`, `suspended`, `stopping`, `deleting`, `deleted`, and `error`.
- [ ] Freeze the sandbox create request schema, including image reference, limits, network mode, and tunnel policy.
- [ ] Freeze which lifecycle operations are supported in v1: create, inspect, start, stop, suspend, resume, delete, exec, terminal attach, snapshots, and tunnels.

### P0-3. Finalize storage and ingress scope

- [ ] Confirm local persistent writable system and workspace volumes as the required active storage model.
- [ ] Confirm snapshot export to S3-compatible storage is optional and not required for first ship.
- [ ] Confirm explicit tunnel creation as the only inbound access mechanism in v1.

## 3. Phase 1: control-plane foundation

### P0-4. Create the Go daemon skeleton

- [ ] Define config loading and validation.
- [ ] Define structured logging.
- [ ] Define the HTTP router and middleware chain.
- [ ] Add a health endpoint.
- [ ] Add graceful shutdown behavior.

### P0-5. Add SQLite schema and data access

- [ ] Create migrations for `tenants`, `sandboxes`, `sandbox_runtime_state`, `sandbox_storage`, `tunnels`, `snapshots`, `executions`, `tty_sessions`, `quotas`, and `audit_events`.
- [ ] Add repository interfaces for lifecycle, execution, tunnel, snapshot, and quota data.
- [ ] Add transaction helpers for multi-step lifecycle updates.
- [ ] Add schema validation and migration startup checks.

### P0-6. Add auth, tenancy, and rate limiting

- [ ] Implement bearer token authentication.
- [ ] Resolve every request to one tenant identity.
- [ ] Add tenant ownership checks to all sandbox, tunnel, snapshot, execution, and terminal lookups.
- [ ] Add basic per-tenant rate limiting hooks.

## 4. Phase 2: runtime abstraction and lifecycle orchestration

### P0-7. Define the runtime manager interface

- [ ] Define runtime methods for create, start, stop, suspend, resume, destroy, inspect, exec, terminal attach, snapshot create, and snapshot restore.
- [ ] Keep the API independent of guest implementation details.
- [ ] Define explicit runtime status and error mapping for reconciliation.

### P0-8. Implement lifecycle handlers

- [ ] Implement `POST /v1/sandboxes`.
- [ ] Implement `GET /v1/sandboxes/:id`.
- [ ] Implement `POST /v1/sandboxes/:id/start`.
- [ ] Implement `POST /v1/sandboxes/:id/stop`.
- [ ] Implement `POST /v1/sandboxes/:id/suspend`.
- [ ] Implement `POST /v1/sandboxes/:id/resume`.
- [ ] Implement `DELETE /v1/sandboxes/:id`.

### P0-9. Implement runtime-backed create and boot flow

- [ ] Allocate sandbox ID and initial metadata.
- [ ] Allocate persistent storage and network resources.
- [ ] Materialize the guest from the base image.
- [ ] Apply limits and network policy.
- [ ] Boot the guest and run bootstrap.
- [ ] Persist status transitions and last-known runtime state.

## 5. Phase 3: storage, files, and snapshots

### P0-10. Implement local volume management

- [ ] Create per-sandbox writable system storage.
- [ ] Create per-sandbox workspace storage.
- [ ] Add optional cache volume support if it stays operationally small.
- [ ] Support quota accounting where practical.
- [ ] Clean up storage on sandbox deletion.

### P0-11. Implement the file API

- [ ] Implement file read.
- [ ] Implement file write and overwrite.
- [ ] Implement file delete.
- [ ] Implement directory creation.
- [ ] Add path validation and traversal rejection.
- [ ] Ensure host paths are never exposed directly.

### P1-12. Implement snapshot support

- [ ] Implement `POST /v1/sandboxes/:id/snapshots`.
- [ ] Persist snapshot metadata and status.
- [ ] Implement `POST /v1/snapshots/:id/restore`.
- [ ] Validate restore safety for writable system and workspace state.

### P1-13. Add optional S3-compatible snapshot export

- [ ] Add operator config for S3-compatible snapshot export.
- [ ] Export snapshot artifacts and metadata.
- [ ] Restore from exported snapshot metadata when configured.

## 6. Phase 4: runtime interaction

### P0-14. Implement one-off exec sessions

- [ ] Implement `POST /v1/sandboxes/:id/exec`.
- [ ] Stream stdout and stderr.
- [ ] Enforce per-exec timeout.
- [ ] Kill the full guest-side process tree on timeout or cancellation.
- [ ] Record execution metadata and bounded output bookkeeping.

### P0-15. Implement interactive terminal sessions

- [ ] Implement `POST /v1/sandboxes/:id/tty`.
- [ ] Allocate PTYs inside the guest.
- [ ] Bridge terminal streams over WebSocket.
- [ ] Support resize events.
- [ ] Support multiple concurrent terminal attachments per sandbox.

### P0-16. Support long-running guest processes

- [ ] Allow detached commands and guest service starts.
- [ ] Ensure client disconnects do not terminate unrelated guest workloads.
- [ ] Expose enough runtime metadata to reconnect safely.

## 7. Phase 5: network isolation and tunnels

### P0-17. Implement per-sandbox network policy

- [ ] Support `internet-enabled` mode.
- [ ] Support `internet-disabled` mode.
- [ ] Block east-west tenant connectivity.
- [ ] Prevent direct host network exposure by default.

### P0-18. Implement tunnel management

- [ ] Implement `POST /v1/sandboxes/:id/tunnels`.
- [ ] Implement `GET /v1/sandboxes/:id/tunnels`.
- [ ] Implement `DELETE /v1/sandboxes/:id/tunnels/:tunnelId`.
- [ ] Publish sandbox ports only through the managed tunnel path.
- [ ] Log tunnel create and revoke events.

### P1-19. Add operator defaults and per-tenant tunnel policy

- [ ] Add allow or deny tunnel policy per tenant.
- [ ] Add default tunnel visibility and auth settings.
- [ ] Add audit fields for tunnel policy denials.

## 8. Phase 6: guest image and usability

### P0-20. Build the base guest image

- [ ] Choose the Linux distribution base.
- [ ] Install shell and core Unix tooling.
- [ ] Install Git.
- [ ] Install Python.
- [ ] Install Node and npm.
- [ ] Install browser automation dependencies.
- [ ] Install a lightweight init or supervision layer.

### P0-21. Support a guest-local container engine

- [ ] Install and configure Docker-in-Docker or an equivalent guest-local engine.
- [ ] Ensure the host container runtime socket is never exposed.
- [ ] Ensure inner-engine storage counts toward sandbox quota.

### P0-22. Verify browser automation support

- [ ] Run headless browser smoke tests in a sandbox.
- [ ] Verify filesystem and network behavior for browser workloads.
- [ ] Verify browser dependencies survive restart in the persistent image model.

## 9. Phase 7: quotas, cleanup, and recovery

### P0-23. Implement quota checks

- [ ] Enforce maximum sandboxes per tenant.
- [ ] Enforce maximum running sandboxes per tenant.
- [ ] Enforce aggregate CPU, memory, and storage limits.
- [ ] Enforce maximum concurrent exec sessions.
- [ ] Enforce maximum active tunnels.

### P0-24. Implement cleanup flows

- [ ] Make sandbox deletion idempotent.
- [ ] Revoke tunnels before final delete.
- [ ] Force-stop stuck runtime resources when needed.
- [ ] Remove storage volumes on delete.
- [ ] Preserve snapshots only when policy allows.

### P0-25. Implement restart recovery

- [ ] Load SQLite state at daemon startup.
- [ ] Inspect runtime state for every non-deleted sandbox.
- [ ] Reconcile missing and orphaned guest resources.
- [ ] Recover sandbox and tunnel status.
- [ ] Resume cleanup loops.

## 10. Phase 8: CLI and operator experience

### P0-26. Build the CLI

- [ ] Add sandbox create.
- [ ] Add sandbox start, stop, suspend, and resume.
- [ ] Add exec command support.
- [ ] Add terminal attach support.
- [ ] Add upload and download file commands.
- [ ] Add tunnel create, list, and revoke commands.

### P1-27. Add admin and debugging commands

- [ ] Add quota inspection commands.
- [ ] Add sandbox listing commands.
- [ ] Add tunnel inspection commands.
- [ ] Add runtime health inspection commands.

## 11. Phase 9: hardening and verification

### P0-28. Add tenant isolation integration tests

- [ ] Test cross-tenant file access denial.
- [ ] Test cross-tenant network denial.
- [ ] Test tunnel ownership checks.
- [ ] Test sandbox deletion cleanup.
- [ ] Test auth and ownership checks.

### P0-29. Add workload verification tests

- [ ] Test shell exec.
- [ ] Test Git usage.
- [ ] Test Python package installation persistence.
- [ ] Test npm package installation persistence.
- [ ] Test headless browser automation.
- [ ] Test guest-local container engine behavior.
- [ ] Test long-running agent reconnect behavior.

### P0-30. Run failure-mode drills

- [ ] Drill control-plane restart during exec.
- [ ] Drill guest boot failure.
- [ ] Drill quota exceed handling.
- [ ] Drill live tunnel revoke behavior.
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
