# Tasks

## 1. Tighten the production control contract (Req 1, 11, 12)

- [ ] Update `images/guest/profiles/core.json`, `runtime.json`, `browser.json`, and `container.json` so their control contract remains explicitly agent-only, and keep `debug.json` as the only `ssh-compat` profile.
- [ ] Update `internal/guestimage/contract.go` and `internal/runtime/qemu/doc.go` so non-debug SSH combinations are rejected or described as compatibility-only rather than normal production behavior.
- [ ] Tighten `internal/config/config.go` and `internal/config/config_test.go` so production validation rejects unsupported SSH/profile combinations early while preserving backward-compatible config parsing where practical.
- [ ] Update `cmd/sandboxctl/doctor.go`, `cmd/sandboxctl/main_test.go`, and `scripts/qemu-host-verification.sh` so agent-default verification is the normal path and SSH checks are explicit debug/rescue actions.

## 2. Add a persistent per-VM agent session manager (Req 2, 5, 7)

- [ ] Refactor `internal/runtime/qemu/agent_client.go` to add an in-memory session registry keyed by sandbox/runtime identity, with one long-lived connection per running VM.
- [ ] Implement handshake caching, request correlation, stream registration, ping-based health, and reconnect-on-break behavior in the session manager.
- [ ] Update `internal/runtime/qemu/runtime.go` to resolve sessions through the manager for QEMU control-path operations and to invalidate sessions when PID/runtime identity changes.
- [ ] Add focused unit tests for session reuse, reconnect behavior, stale-session invalidation, and post-daemon-restart lazy reconstruction.

## 3. Bump the agent protocol to version 3 and add stream-oriented operations (Req 2, 3, 4, 6, 9)

- [ ] Update `internal/runtime/qemu/agentproto/protocol.go` with protocol version `3` and new payloads for `ping`, `file_open`, `file_data`, `file_close`, `exec_start`, `exec_event`, `exec_cancel`, and `archive_stream`.
- [ ] Refactor `cmd/or3-guest-agent/main.go` to serve the new request/stream model over a single long-lived connection while preserving PTY and TCP bridge support.
- [ ] Update guest-image profile/contract metadata so agent-capable images advertise the new protocol version.
- [ ] Add or update protocol-focused tests in `cmd/or3-guest-agent/main_test.go` and `internal/runtime/qemu/runtime_test.go` for version mismatches, malformed messages, and stream framing.

## 4. Convert workspace file operations to streaming (Req 3, 4)

- [ ] Rework `internal/runtime/qemu/workspace.go` and agent-side file handlers so agent-mode file upload/download uses open/data/close semantics instead of repeated chunk RPCs.
- [ ] Keep the existing runtime-facing file methods unchanged while removing guest helper subprocesses from the normal agent path.
- [ ] Enforce host-side workspace path normalization and negotiated transfer limits during streaming reads and writes.
- [ ] Add regression tests for EOF handling, size-limit enforcement, canceled streams, and path-escape rejection.

## 5. Add real streaming exec on the QEMU path (Req 3, 6)

- [ ] Refactor `internal/runtime/qemu/exec.go` to drive `model.ExecStreams` from `exec_event` messages and return terminal result metadata only after the final result event.
- [ ] Update guest-side exec handling in `cmd/or3-guest-agent/main.go` to emit stdout, stderr, and final result events incrementally, plus support `exec_cancel`.
- [ ] Keep PTY and TCP bridge operations functioning through the shared session manager instead of independent one-off control sockets.
- [ ] Add tests for live output ordering, cancellation, timeout behavior, and detached execution semantics.

## 6. Unify readiness, health, and lifecycle state (Req 5, 7)

- [ ] Add a shared state-derivation helper in `internal/runtime/qemu/runtime.go` and use it from `Start`, `Resume`, `Inspect`, `Stop`, and related lifecycle helpers.
- [ ] Replace per-call readiness probing with session `ping`/ready checks and keep serial logs diagnostic-only for boot failure context.
- [ ] Rework stop/delete cleanup in `internal/runtime/qemu/runtime.go` so missing monitor sockets, stale PID files, broken sessions, and already-exited VMs are safe retry cases.
- [ ] Add unit tests for booting-to-running transitions, degraded/error resolution after boot timeout, suspended/resumed behavior, and repeated stop/delete calls.

## 7. Simplify workspace provisioning and guest bootstrap (Req 8, 11)

- [ ] Move workspace image formatting into `internal/runtime/qemu/runtime.go` sandbox creation, using host-side ext4 formatting with a deterministic label.
- [ ] Update `images/guest/systemd/or3-bootstrap.sh`, `images/guest/systemd/or3-bootstrap.service`, and any related guest assets so bootstrap only mounts, fixes ownership, and writes the ready marker.
- [ ] Remove boot-time formatting logic and `/etc/fstab` mutation from the guest bootstrap path.
- [ ] Update doctor/install verification so missing host formatting dependencies are surfaced clearly before QEMU sandbox creation.

## 8. Replace guest temp archive export and switch snapshots to workspace-first restore (Req 9, 10)

- [ ] Rework `internal/runtime/qemu/archive.go` and guest-agent archive handling so workspace export is streamed to a host-built tar.gz rather than created as a temp archive inside the guest.
- [ ] Keep host-side archive safety checks by reusing the existing archive utilities and bounds enforcement.
- [ ] Update `internal/runtime/qemu/runtime.go`, `internal/service/service.go`, and related tests so default QEMU snapshots store base image reference plus workspace archive and restore by recreating the overlay from base image.
- [ ] Add regression tests for export path safety, workspace-first snapshot create/restore, and compatibility rejection for invalid snapshot/image metadata.

## 9. Update onboarding, CLI flows, and verification scripts (Req 1, 11, 12)

- [ ] Add `sandboxctl qemu init` and `sandboxctl qemu smoke` in `cmd/sandboxctl/main.go` and supporting command files while preserving `sandboxctl doctor --production-qemu`.
- [ ] Lighten `scripts/install-qemu-runtime.sh` so it bootstraps host prerequisites without building a guest image unless explicitly requested.
- [ ] Update `scripts/qemu-production-smoke.sh`, `scripts/qemu-host-verification.sh`, and any related smoke helpers so the agent path is the default and debug SSH coverage is separate.
- [ ] Add CLI and script-focused tests where practical, plus docs for the new recommended flow.

## 10. Refresh docs and recommended image workflow (Req 11, 12)

- [ ] Update `README.md`, `docs/operations/*`, `cmd/sandboxctl/hardening.go`, and related operator guidance so prebuilt promoted `core` images are the recommended default and local guest building is documented as advanced.
- [ ] Update QEMU runtime docs and image-contract docs to describe protocol version `3`, session-based control, debug-only SSH, and workspace-first snapshots.
- [ ] Ensure all docs reference the actual command names, env vars, control modes, and guest profiles used by the implementation.

## 11. Finish focused regression coverage (Req 1-12)

- [ ] Extend `internal/runtime/qemu/runtime_test.go`, `archive_test.go`, and `host_integration_test.go` with session-manager, lifecycle, archive, and workspace-first snapshot coverage.
- [ ] Extend `cmd/or3-guest-agent/main_test.go` for streaming file, exec, ping, and archive behavior.
- [ ] Extend `internal/service/service_test.go` and `cmd/sandboxctl/main_test.go` for snapshot semantics, doctor behavior, and new CLI commands.
- [ ] Document any remaining host-gated/manual validation steps under `or3-sandbox/planning/` or `docs/operations/verification.md`.

## 12. Out of scope for this wave

- [ ] Do not introduce `virtiofs`; keep the existing workspace block-image model.
- [ ] Do not add a new SQLite schema or second metadata store for agent sessions.
- [ ] Do not keep SSH as a normal production fallback for non-debug QEMU sandboxes.
- [ ] Do not make full-disk/forensic snapshots the default snapshot model.
