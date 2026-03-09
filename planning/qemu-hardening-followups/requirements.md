# Requirements

## Overview

Plan a focused hardening pass for the production-oriented QEMU path after the first architecture cleanup. The remaining work is centered on the guest-agent boundary, the sandbox-local tunnel bridge, the production doctor, and guest-image reproducibility.

Scope assumptions:

- this plan covers the remaining issues called out in the review: guest-agent limits, bounded file transfer, PTY/session validation, tunnel bridge cleanup, stronger doctor checks, and stronger image-build provenance
- the WebSocket origin hardening item is already covered by `planning/websocket-origin-browser-tunnel-hardening/` and is intentionally excluded here
- keep the current single-host, single-process daemon model and agent-first QEMU architecture
- prefer localized changes in existing Go packages and guest-image assets over adding new services or a new control plane

## Requirements

1. **Harden the guest-agent wire boundary**
   - Acceptance criteria:
     - the guest-agent protocol has explicit message-size limits for inbound and outbound frames instead of accepting arbitrary payload sizes
     - each request carries a request identifier that is echoed in the matching response, and mismatched or missing IDs are rejected in the agent/host flow rather than treated as best-effort metadata
     - the guest agent rejects unknown, malformed, or disallowed operations with deterministic errors and without leaving the connection in an ambiguous state
     - host and guest continue to negotiate an explicit control protocol version, with rollout guidance for images that still advertise the older protocol

2. **Enforce capability checks at the guest-agent boundary**
   - Acceptance criteria:
     - the guest agent derives its allowed operations from a fixed in-guest contract source already produced by the image build, rather than exposing the full operation set unconditionally
     - operations such as `exec`, `pty`, `file_*`, and `shutdown` are rejected by the guest agent when the active image profile does not declare the corresponding capability
     - the runtime handshake verifies that the guest-reported capability set still matches the image contract expected by the host
     - tests cover both allowed and denied operations for at least one minimal profile

3. **Bound and stream guest-agent file operations**
   - Acceptance criteria:
     - `file_read` no longer uses whole-file reads for arbitrary workspace paths; large reads are chunked, bounded, or rejected above a defined limit
     - `file_write` enforces explicit per-request and total-size limits and does not require the agent to hold arbitrarily large payloads in memory
     - path validation continues to confine access to `/workspace`, with regression coverage for traversal attempts and oversize requests
     - host-side workspace helpers continue to expose the current user-facing file API while internally handling chunking or bounded rejection deterministically

4. **Tighten PTY session ownership and state validation**
   - Acceptance criteria:
     - the guest agent maintains explicit PTY session state and rejects `pty_data`, `pty_resize`, and `pty_close` messages that omit or mismatch the active session ID
     - invalid PTY operations do not mutate terminal state or write to the PTY device
     - host-side PTY handling validates session IDs on inbound PTY frames and closes cleanly on protocol misuse or stale-session traffic
     - tests cover wrong-session, missing-session, double-close, and post-exit PTY behavior

5. **Replace helper-probing tunnel bridging with a substrate-owned path**
   - Acceptance criteria:
     - sandbox-local TCP bridging no longer depends on probing guest-side `python3`, `python`, `node`, `nc`, or `busybox nc`
     - the bridge is implemented either as a guest-agent operation or a tiny fixed guest helper that ships as part of the supported image substrate; the chosen path is uniform across supported QEMU profiles
     - the default `core` profile can satisfy local tunnel bridging without additional language runtimes or ad hoc guest tooling assumptions
     - service/runtime tests cover successful bridging, target-port validation, connection shutdown, and deterministic failure when the target port is unavailable

6. **Expand production doctor into a more credible host gate**
   - Acceptance criteria:
     - `sandboxctl doctor --production-qemu` checks disk free-space thresholds in addition to path existence for the database, storage, and snapshot roots
     - the doctor validates restrictive permissions or ownership posture for operator-sensitive material where applicable, including JWT secret files, tunnel signing key material, and QEMU runtime-control directories or sockets
     - the doctor checks relevant host-runtime prerequisites for the supported Linux/KVM posture, including cgroup/controller availability and other host assumptions needed by the daemon/runtime path
     - the doctor reports findings as `PASS`, `WARN`, or `FAIL` with actionable details, and includes regression coverage for the new checks

7. **Strengthen guest-image reproducibility and promotion metadata**
   - Acceptance criteria:
     - the guest-image build pipeline records stronger provenance for each built image, including the resolved profile manifest, base image identity, image checksum, and package/source metadata sufficient to promote a release artifact bundle deliberately
     - the build can optionally validate pinned inputs such as expected base-image checksum or explicit package selections without requiring a new external build system
     - emitted image-contract metadata distinguishes between “recorded provenance” and “bit-for-bit reproducibility” so docs do not overclaim current guarantees
     - documentation defines the minimum release bundle that must move together for promotion: qcow2 image, sidecar contract, resolved manifest, package inventory, and provenance metadata

## Non-functional constraints

- Keep the design low-complexity and low-RAM:
  - avoid unbounded buffering in the guest agent
  - prefer chunked reads/writes and conservative defaults
  - keep the control loop single-connection and deterministic where possible
- Preserve the current SQLite model:
  - no new service or distributed state layer
  - no SQLite schema change unless a persistence need is unavoidable; if none is needed, state that explicitly
- Maintain safe-by-default runtime behavior:
  - workspace-confined file access only
  - bounded command/file/tunnel operations
  - deterministic failure on malformed protocol messages
  - no secret values printed in doctor output, smoke logs, or build metadata
- Preserve compatibility intentionally:
  - coordinate any guest-agent protocol version bump with image-contract updates and image rebuild expectations
  - keep existing user-facing CLI and API semantics stable unless a change is required for hardening
- Keep this plan scoped:
  - do not add a frontend, a new daemon, a new auth system, or generic browser-session infrastructure
  - do not duplicate the WebSocket origin work already planned elsewhere
