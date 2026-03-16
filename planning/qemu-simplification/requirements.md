# Requirements

## Overview

This plan simplifies the `or3-sandbox` QEMU runtime around one production control model: long-lived guest-agent sessions over the existing QEMU agent transport, with SSH demoted to `debug` and explicit rescue workflows.

The scope is deliberately narrow and repo-aligned:

- keep the existing Go daemon, SQLite persistence, HTTP API, and `sandboxctl` lifecycle/file/snapshot verbs
- simplify the QEMU implementation inside the current `internal/runtime/qemu`, `internal/service`, `internal/config`, `cmd/sandboxctl`, and guest-image assets
- replace per-operation reconnects and chunk-style file transfer with a session-oriented agent protocol
- simplify guest boot so workspace provisioning is deterministic and outside the hot path
- keep `debug` SSH compatibility available, but remove SSH as a normal production branch

Assumptions:

- no SQLite migration is required for this wave
- `agentproto.ProtocolVersion` advances to `3` and all agent-capable guest images are rebuilt with the daemon change
- public sandbox status names stay unchanged; internal “failed” behavior maps to the existing terminal `error` state
- the workspace simplification uses the existing block-image model, not `virtiofs`
- workspace-first snapshot restore becomes the default QEMU snapshot model; full-disk/forensic snapshots are explicitly out of scope for this wave

## Requirements

1. **Make guest-agent control the only normal production QEMU path.**
   - Acceptance criteria:
     - `core`, `runtime`, `browser`, and `container` images validate only with `control.mode=agent`.
     - `debug` remains the only supported `ssh-compat` image profile for normal repo workflows.
     - production config, doctor, and image validation fail early for non-debug SSH combinations instead of silently falling back.
     - normal QEMU runtime operations no longer branch between SSH and agent for readiness, exec, file transfer, archive export, or lifecycle actions.

2. **Persist one long-lived control session per running VM.**
   - Acceptance criteria:
     - the runtime opens a single reusable session per running sandbox and performs `hello`/capability negotiation once per connection.
     - readiness checks, exec, file transfer, tunnel helpers, archive export, and shutdown use the existing session rather than opening a fresh connection per request.
     - the runtime reconnects only when the control socket breaks or the VM identity changes due to restart/recreate.
     - after a daemon restart, the first QEMU operation lazily reconstructs the session from persisted runtime state without requiring sandbox recreation.

3. **Define a versioned session-oriented agent protocol for production sandboxes.**
   - Acceptance criteria:
     - protocol version `3` is declared in the host and guest implementations and validated during handshake.
     - the protocol includes explicit operations for `ping`, streaming file transfer, streaming exec, and archive export in addition to existing PTY/TCP bridge support.
     - unsupported protocol versions, missing capabilities, or control-mode mismatches are rejected before the sandbox is treated as ready.
     - the normal production path no longer depends on legacy `file_read`/`file_write` request-response chunk loops.

4. **Replace chunk-style workspace file transfer with streaming operations.**
   - Acceptance criteria:
     - workspace upload and download use open/data/close semantics over the live agent session.
     - workspace path validation remains rooted at `/workspace` and rejects escapes before guest access.
     - file transfer limits remain bounded by the configured host limit and the negotiated guest limit.
     - the runtime no longer shells out to guest helper subprocesses for normal agent-mode file reads and writes.

5. **Make readiness and health come from the live control session.**
   - Acceptance criteria:
     - QEMU runtime state derivation uses PID/process state plus session `ping`/ready results as the primary control-plane signal.
     - serial logs are used only for boot failure context and diagnostics, not as the happy-path readiness source.
     - `Start`, `Resume`, `Inspect`, and daemon reconciliation use the same state-derivation rules for `booting`, `running`, `stopped`, `suspended`, `degraded`, and terminal `error`.
     - a running sandbox can be health-checked cheaply without creating a new control connection.

6. **Provide real streaming exec behavior on the QEMU path.**
   - Acceptance criteria:
     - stdout and stderr are emitted incrementally to `model.ExecStreams` during command execution.
     - terminal result metadata is delivered as a distinct final event with exit code, status, and timestamps.
     - exec cancellation works through the agent protocol and does not require SSH-only process tracking.
     - PTY and TCP bridge traffic continue to function while sharing the same per-VM session infrastructure.

7. **Harden lifecycle handling around one canonical QEMU state model.**
   - Acceptance criteria:
     - stop, suspend, resume, restart reconciliation, and delete all derive state from the same runtime model instead of separate best-effort branches.
     - repeated stop and delete calls are safe when the monitor socket, PID file, agent session, or overlay state is partially missing.
     - graceful stop prefers agent-driven shutdown when available, then falls back conservatively to process/monitor cleanup.
     - daemon reconciliation produces predictable persisted runtime state after daemon bounce or partial cleanup.

8. **Move workspace provisioning out of the guest boot hot path.**
   - Acceptance criteria:
     - the workspace block image is created and formatted once at sandbox creation time on the host.
     - guest bootstrap only waits for the device, mounts it, fixes ownership if needed, and writes the ready marker.
     - boot no longer decides whether to format the workspace during normal start/resume.
     - guest bootstrap no longer edits `/etc/fstab` on each boot.

9. **Replace guest temp-tar export with host-owned archive construction.**
   - Acceptance criteria:
     - workspace export no longer requires creating a temporary tarball inside `/workspace`.
     - host-side export continues to enforce safe path normalization and bounded archive handling.
     - archive export is performed through the agent session as a streaming operation.
     - medium-sized workspace exports are predictable and do not require many tiny request-response file cycles.

10. **Make workspace-first restore the default QEMU snapshot model.**
    - Acceptance criteria:
      - default QEMU snapshot creation stores the immutable base image reference plus a workspace archive rather than a copied root overlay image.
      - restore recreates the root overlay from the base image and then restores workspace contents.
      - snapshot compatibility checks continue to validate runtime selection and relevant image/contract metadata before restore.
      - existing snapshot persistence tables and API shapes are reused without a schema migration.

11. **Simplify operator onboarding and verification for the production QEMU path.**
    - Acceptance criteria:
      - `sandboxctl doctor --production-qemu` remains the main host readiness entrypoint.
      - `sandboxctl qemu init` and `sandboxctl qemu smoke` exist as first-class CLI flows for setup and verification.
      - `scripts/install-qemu-runtime.sh` no longer builds a guest image by default and instead acts as a lighter host bootstrap helper.
      - `scripts/qemu-host-verification.sh` defaults to the agent path and only performs SSH checks when explicitly requested for debug/rescue verification.

12. **Tighten docs, contracts, and validation around the simplified product surface.**
    - Acceptance criteria:
      - package docs, image contracts, and config validation no longer describe SSH and agent as equal QEMU production citizens.
      - unsupported control-mode/profile combinations fail at config load, doctor, or image validation time instead of midway through sandbox lifecycle operations.
      - docs and hardening guidance recommend promoted prebuilt `core` images as the default path and document local guest builds as advanced.
      - dead compatibility branches left behind by the previous dual-mode design are removed or explicitly isolated to debug-only workflows.

## Non-functional constraints

- Favor deterministic, bounded behavior over opportunistic fallback logic.
- Keep the implementation inside existing Go packages and scripts instead of adding new daemons or stores.
- Preserve current SQLite compatibility, persisted sandbox/snapshot metadata, and API route shapes unless an additive change is strictly required.
- Keep workspace/file/archive handling safe by default: bounded bytes, normalized paths, no host-side path traversal, and no secret leakage in logs or CLI output.
- Keep host-gated QEMU tests and smoke scripts opt-in and bounded so local/default CI paths remain fast.
- Treat SSH material, promoted image references, and operator secrets as explicit file-backed inputs; do not make them implicit production prerequisites for agent-default sandboxes.
