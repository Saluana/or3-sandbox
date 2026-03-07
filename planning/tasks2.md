# Sandbox v1 Follow-On Task List

## 1. Goal

Close the gap between the current trusted Docker-backed implementation and the production-oriented v1 design.

This task list assumes two truths:

- the current repository already has a working control plane
- the missing work is mainly runtime boundary, guest semantics, quota enforcement, and workload proof

Priority labels:

- `P0`: required to honestly claim alignment with the v1 design
- `P1`: important follow-up needed for a production-ready release
- `P2`: useful polish after the core platform is credible

## 2. Phase A: Re-baseline Scope And Truth

### P0-A1. Re-state the current product status

- [ ] Update README and planning docs to distinguish "trusted Docker deployment" from "production guest-backed v1".
- [ ] Remove or reword any claim that implies the full architecture is already complete.
- [ ] Mark incomplete design areas explicitly: guest runtime, bootstrap, browser verification, guest-local engine verification, and hard quota enforcement.

### P0-A2. Reconcile task status drift

- [ ] Audit `planning/tasklist_v1.md` for checked items that do not satisfy the design as written.
- [ ] Re-open or annotate items for guest boot/bootstrap, guest-local engine support, and production runtime selection.
- [ ] Add a short status matrix mapping requirements to actual implementation state.

## 3. Phase B: Lock The Production Runtime Direction

### P0-B1. Choose the guest-backed runtime technology

- [ ] Select one concrete production backend: QEMU/KVM, Firecracker, Cloud Hypervisor, or equivalent.
- [ ] Document Linux host prerequisites, privilege model, networking model, and storage model.
- [ ] Define what "guest-backed" means operationally for this project.

### P0-B2. Define the guest control channel

- [ ] Choose how exec, PTY, file operations, and health checks reach the guest.
- [ ] Pick one transport: guest agent over vsock, SSH, serial console bridge, or equivalent.
- [ ] Define the bootstrap handshake required before a sandbox is marked ready.

### P0-B3. Extend runtime selection in config

- [ ] Add real runtime backend selection beyond `docker`.
- [ ] Reject startup when the selected production backend cannot satisfy the design isolation guarantees.
- [ ] Keep Docker available only as an explicitly trusted mode.

## 4. Phase C: Implement The Guest-Backed Runtime

### P0-C1. Add a new runtime backend package

- [ ] Create a new runtime package implementing `model.RuntimeManager`.
- [ ] Implement create, start, stop, suspend, resume, destroy, inspect, exec, attach TTY, snapshot create, and snapshot restore.
- [ ] Persist runtime identifiers and status in the same control-plane model used today.

### P0-C2. Implement guest storage

- [ ] Create a writable system disk per sandbox.
- [ ] Keep the persistent workspace volume separate from the writable system disk.
- [ ] Support snapshotting both layers in a guest-aware way.

### P0-C3. Implement guest networking

- [ ] Create an isolated network boundary per sandbox.
- [ ] Preserve `internet-enabled` and `internet-disabled` policy.
- [ ] Prevent east-west tenant traffic and direct host exposure by default.

### P0-C4. Implement guest readiness and bootstrap

- [ ] Boot the guest from the selected base image.
- [ ] Run first-boot bootstrap or guest agent setup.
- [ ] Refuse to mark the sandbox ready until guest readiness is confirmed.
- [ ] Surface bootstrap failure as sandbox `error`.

## 5. Phase D: Complete The Guest Environment Contract

### P0-D1. Make the sandbox behave like a durable machine

- [ ] Add a real guest init or supervision story.
- [ ] Verify that package installs persist across restart.
- [ ] Verify that configured background services restart after boot.

### P0-D2. Finish guest-local container engine support

- [ ] Decide whether the guest engine is Docker, rootless Docker, Podman, or equivalent.
- [ ] Configure and bootstrap it inside the guest.
- [ ] Prove that the host runtime socket is never exposed.
- [ ] Add smoke tests for guest-local container creation and image pulls.

### P1-D3. Harden the guest image pipeline

- [ ] Version the guest image build more explicitly.
- [ ] Define base image compatibility expectations for exec, PTY, browser, and guest engine support.
- [ ] Document upgrade and snapshot compatibility expectations.

## 6. Phase E: Enforce Real Resource Boundaries

### P0-E1. Implement actual storage enforcement

- [ ] Replace requested-size bookkeeping with real per-sandbox storage enforcement.
- [ ] Include writable system disk, workspace volume, cache volume, and snapshot staging where applicable.
- [ ] Count guest-local container engine storage toward sandbox usage.

### P1-E2. Improve CPU model

- [ ] Replace integer-only CPU limits with fractional CPU or millicore-style limits.
- [ ] Update validation, persistence, quota accounting, and CLI input handling.
- [ ] Preserve backward compatibility for existing stored sandboxes if practical.

### P1-E3. Add runtime health inspection

- [ ] Expose backend health and guest readiness inspection commands for operators.
- [ ] Surface degraded runtime state clearly in CLI and logs.

## 7. Phase F: Verify Real Workloads

### P0-F1. Add workload smoke tests

- [ ] Test Git usage inside a sandbox.
- [ ] Test Python package installation persistence across restart.
- [ ] Test npm package installation persistence across restart.
- [ ] Test headless browser automation end-to-end.
- [ ] Test guest-local container engine behavior end-to-end.

### P0-F2. Add environment durability tests

- [ ] Test that writable system changes persist after stop and start.
- [ ] Test that workspace changes persist after stop and start.
- [ ] Test that supervised background services recover after restart.

## 8. Phase G: Harden Recovery And Failure Behavior

### P0-G1. Add failure-mode drills

- [ ] Drill control-plane restart during exec.
- [ ] Drill guest boot failure.
- [ ] Drill disk-full behavior.
- [ ] Drill partial snapshot failure and restore failure.

### P1-G2. Tighten reconciliation

- [ ] Reconcile partially bootstrapped guests safely.
- [ ] Detect leaked runtime resources with stronger backend-specific checks.
- [ ] Make orphan cleanup safe when runtime inspection is degraded.

## 9. Phase H: Optional But Already Planned Follow-Up

### P1-H1. Add S3-compatible snapshot export

- [ ] Implement snapshot export artifact upload.
- [ ] Persist export metadata and recovery metadata.
- [ ] Support restore from exported snapshot metadata when configured.

## 10. Exit Criteria

Sandbox v1 should only be called complete against the design docs when all of the following are true:

- [ ] a guest-backed production runtime exists and is selectable
- [ ] Docker remains clearly marked as trusted-only
- [ ] guest boot and bootstrap are real, observable flows
- [ ] exec, PTY, files, tunnels, and snapshots work against the guest runtime
- [ ] guest-local container engine support works and is tested
- [ ] browser automation works and is tested
- [ ] storage enforcement is real, not just requested-value accounting
- [ ] failure-mode drills pass
- [ ] README and planning docs describe the shipped system accurately

## 11. Suggested Implementation Order

1. Re-baseline the docs and task status.
2. Lock the guest-backed runtime choice and control channel.
3. Implement the new runtime backend with guest boot and readiness.
4. Finish the guest environment contract: init, service restart, guest-local engine.
5. Add real storage enforcement and quota accounting.
6. Add workload verification and failure-mode drills.
7. Land optional S3 export only after the core platform is credible.

Active execution checklist:

- `planning/onwards/tasks.md`
