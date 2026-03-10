# Tasks

## 1. Tighten the guest-agent protocol boundary (Req 1, 2)

- [x] Update `internal/runtime/qemu/agentproto/protocol.go` to define bounded message-size constants, required request ID handling, and any additive request/response types needed for hardened file transfer and tunnel bridging.
- [x] Decide whether the wire changes fit as an additive protocol update or require bumping `ProtocolVersion`, then update the version/compatibility expectations consistently across `internal/model/guest.go`, image contracts, and runtime handshake behavior.
- [x] Refactor `cmd/or3-guest-agent/main.go` so message parsing rejects oversize or malformed frames deterministically before dispatch.
- [x] Update `internal/runtime/qemu/agent_client.go` so round-trips validate echoed request IDs and fail closed on protocol mismatches.
- [x] Add focused protocol regression tests in `internal/runtime/qemu/runtime_test.go` for oversize messages, missing IDs, and mismatched IDs.

## 2. Enforce guest-agent capability checks (Req 2)

- [x] Identify the fixed in-guest source of profile capabilities produced by `images/guest/build-base-image.sh` and wire `cmd/or3-guest-agent/main.go` to load or derive its allowed operation set from that source.
- [x] Replace the current unconditional hello capability list in `cmd/or3-guest-agent/main.go` with a capability set derived from the image/profile contract.
- [x] Reject disallowed ops at the guest-agent dispatch boundary with explicit errors rather than letting unsupported behavior reach exec/files/PTY handlers.
- [x] Update `internal/runtime/qemu/agent_client.go` handshake validation so guest-reported capabilities are checked against the host-side image contract expectations.
- [x] Add tests in `internal/runtime/qemu/runtime_test.go` and, if needed, `internal/guestimage/contract_test.go` for capability mismatch and minimal-profile denied operations.

## 3. Bound and stream file operations (Req 3)

- [x] Replace whole-file `os.ReadFile` usage in `cmd/or3-guest-agent/main.go` with bounded chunked reads for workspace files.
- [x] Add per-request and total-size enforcement for `file_write` in `cmd/or3-guest-agent/main.go`, keeping writes confined to `/workspace`.
- [x] Update `internal/runtime/qemu/agent_client.go` and `internal/runtime/qemu/workspace.go` to assemble/disassemble chunked transfers while preserving existing runtime/service file APIs.
- [x] Add regression tests for traversal attempts, oversize reads/writes, and large-file behavior in `internal/runtime/qemu/runtime_test.go`.

## 4. Tighten PTY session validation (Req 4)

- [x] Refactor PTY session handling in `cmd/or3-guest-agent/main.go` so active session state is explicit and `pty_data`, `pty_resize`, and `pty_close` require the active session ID.
- [x] Update host-side PTY handling in `internal/runtime/qemu/agent_client.go` to verify inbound PTY frames belong to the opened session and to close cleanly on stale/mismatched traffic.
- [x] Add PTY misuse regression tests in `internal/runtime/qemu/runtime_test.go` covering wrong-session, missing-session, double-close, and post-exit behavior.

## 5. Move sandbox-local tunnel bridging into the substrate (Req 5)

- [x] Design and implement an agent-backed local TCP bridge path in `internal/runtime/qemu/agentproto/protocol.go`, `cmd/or3-guest-agent/main.go`, and `internal/runtime/qemu/agent_client.go`.
- [x] Replace the helper-probing script in `internal/service/tunnel_tcp.go` with the new runtime-backed bridge path.
- [x] Keep `OpenSandboxLocalConn` semantics stable at the service layer while removing assumptions about `python3`, `python`, `node`, `nc`, or `busybox` in guest images.
- [x] Add or update tests in `internal/service/service_test.go` and `internal/runtime/qemu/runtime_test.go` for bridge success, target-port validation, EOF handling, and deterministic failure when the guest port is unavailable.
- [x] Update host-gated smoke coverage in `scripts/qemu-production-smoke.sh` and/or `internal/runtime/qemu/host_integration_test.go` so `core` proves local tunnel bridging without opportunistic helper runtimes.

## 6. Deepen production doctor checks (Req 6)

- [x] Extend `cmd/sandboxctl/doctor.go` to check free-space thresholds for the database, storage, and snapshot filesystems in addition to basic path existence.
- [x] Add read-only posture checks in `cmd/sandboxctl/doctor.go` for tunnel signing key presence/readability/permissions and for restrictive parent-directory posture around runtime-control roots where practical.
- [x] Add Linux/KVM host-prerequisite checks in `cmd/sandboxctl/doctor.go` for cgroup/controller availability and any other runtime assumption already required by the daemon/runtime path.
- [x] Keep doctor output actionable and conservative: use `WARN` for posture concerns and `FAIL` only when the host clearly cannot satisfy the supported production model.
- [x] Add focused tests in `cmd/sandboxctl/main_test.go` for the new fail/warn/pass cases.
- [x] Update `docs/operations/verification.md` and related ops docs if the meaning of production doctor output becomes materially stronger.

## 7. Strengthen image provenance and promotion guidance (Req 7)

- [x] Extend `images/guest/build-base-image.sh` to record stronger provenance such as base-image identity/checksum, resolved-manifest checksum, and any package/source metadata chosen for release promotion.
- [x] Add optional pinned-input validation in `images/guest/build-base-image.sh` for cases like expected base-image checksum, keeping the default workflow lightweight.
- [x] Extend `internal/guestimage/contract.go` and `internal/guestimage/contract_test.go` if additive sidecar fields are needed to carry the new provenance metadata.
- [x] Update `images/guest/README.md` to distinguish recorded provenance from full reproducibility and define the required release bundle contents.
- [x] Update `docs/operations/verification.md` and/or `docs/operations/qemu-production-threat-model.md` so production claims and release-promotion guidance match the stronger-but-not-perfect reproducibility posture.

## 8. Out of scope

- [ ] Do not duplicate the WebSocket origin hardening already planned in `planning/websocket-origin-browser-tunnel-hardening/`.
- [ ] Do not introduce a new daemon, general browser-session auth layer, distributed control plane, or frontend work as part of this hardening pass.
- [ ] Do not add SQLite schema changes unless implementation uncovers a concrete persistence need that cannot be solved with runtime-local state or file-backed image metadata.
