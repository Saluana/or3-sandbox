# 1. Tell the truth at startup and in docs

- [ ] [Req 1] Tighten `internal/config/config.go` and related tests so hostile-production posture refuses Docker-backed or otherwise unsafe runtime/auth combinations.
- [ ] [Req 1] Update `README.md`, `docs/runtimes.md`, `docs/project-overview.md`, and `docs/operations/production-deployment.md` so Docker is consistently described as trusted-only and QEMU verification is the production gate.
- [ ] [Req 1, 2] Keep `cmd/sandboxctl doctor` and `docs/operations/verification.md` as the single operator entrypoint for production claims.

# 2. Prove the QEMU path, do not just describe it

- [ ] [Req 2] Extend `internal/runtime/qemu/*` tests and host-gated scripts to verify guest readiness, suspend/resume, snapshot restore, daemon restart reconciliation, and guest-agent contract version checks.
- [ ] [Req 2] Ensure release qualification scripts in `scripts/*` clearly fail when required host verification or recovery drills do not pass.
- [ ] [Req 2] Surface guest image contract version and guest-agent protocol version in existing inspect/runtime paths if they are not already visible enough for operators.

# 3. Freeze the daemon contract

- [ ] [Req 3] Update `docs/api-reference.md` so exec, streaming exec, files, tunnels, snapshots, lifecycle, and runtime inspection are explicit and authoritative.
- [ ] [Req 3] Add or tighten conformance coverage in `internal/api/integration_test.go` and nearby client-facing tests for documented request/response and stream framing behavior.
- [ ] [Req 3] Add a lightweight compatibility check for the supported `or3-net` integration path so contract drift fails fast.

# 4. Narrow browser/tunnel capabilities

- [ ] [Req 4] Verify `internal/api/router.go`, `internal/service/*`, and `cmd/sandboxctl/*` expose minimal tunnel inspection and revoke flows without adding a new admin subsystem.
- [ ] [Req 4] Add tests for shared signing secret requirements, header stripping, TTL, replay, revocation, and cookie scope.
- [ ] [Req 4] Keep active tunnel visibility and revoke actions discoverable through existing CLI and API surfaces.

# 5. Prove quota behavior under abuse

- [ ] [Req 5] Add bounded abuse scenarios in `scripts/*` and relevant integration tests for CPU, memory, disk, process count, upload/download, PTY or exec flood, and tunnel abuse.
- [ ] [Req 5] Make over-limit behavior explicit in `internal/service/*` and `docs/api-reference.md` so clients see stable reject, throttle, or terminate semantics.
- [ ] [Req 5] Add restart-focused regression coverage proving quota/accounting recovery remains conservative after daemon restart.

# 6. Out of scope

- [ ] Do not introduce a new distributed control plane or external quota service.
- [ ] Do not broaden Docker into the hostile production story.
- [ ] Do not add speculative API surfaces that current clients do not need.
