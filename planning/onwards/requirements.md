# Sandbox Onwards Plan Requirements

## 1. Overview

This plan covers the next implementation phase for `or3-sandbox`: move from an honest, trusted Docker-backed control plane to a lightweight but credible guest-backed production path, without rewriting the control plane or adding new infrastructure.

Scope for this phase:

- keep the existing Go daemon, SQLite store, HTTP API, and CLI shape
- keep Docker available as an explicitly trusted or development backend
- add one production guest-backed backend
- make readiness, storage limits, and workload claims real and testable
- update project positioning so the shipped state is described accurately

Assumptions:

- the same guest-backed backend should run on both Linux and macOS hosts
- Linux uses KVM when available and macOS uses HVF, while the control plane keeps one backend name and one operator model
- optional S3 snapshot export, fractional CPU accounting, and any guest-agent work can follow the core guest-runtime milestone but should stay on the roadmap rather than being treated as unrelated work

## 2. Requirements

### 2.1 Requirement 1: Honest product status and scope

The repository must clearly distinguish the currently shipped trusted Docker path from the production-oriented guest-backed v1 path.

Acceptance criteria:

1. `README.md` and active planning docs state that Docker is a trusted or development backend, not the hostile multi-tenant production boundary.
2. The remaining gaps are listed explicitly: guest runtime, guest bootstrap, hard storage enforcement, workload verification, and recovery drills.
3. Any previously checked planning item that overstates current implementation status is reopened, annotated, or superseded by the new plan.

### 2.2 Requirement 2: Minimal production runtime selection

The daemon must support one concrete guest-backed production backend in addition to the current Docker backend, with startup validation that preserves the existing security stance.

Acceptance criteria:

1. Operator config supports at least `docker` and `qemu` runtime backend values.
2. Startup fails fast when `docker` is selected without the explicit trusted-runtime flag.
3. Startup fails fast when `qemu` is selected but required host prerequisites are missing, including the QEMU binary, host accelerator support for the current OS, guest image path, and SSH bootstrap material.
4. The backend choice does not require API changes to sandbox lifecycle, exec, TTY, file, tunnel, or snapshot endpoints.
5. The same `qemu` backend works on both Linux and macOS, selecting an OS-appropriate accelerator or returning a clear startup error.

### 2.3 Requirement 3: Guest-backed sandbox lifecycle and readiness

The production backend must create a durable guest machine per sandbox and only report it ready after guest bootstrap succeeds.

Acceptance criteria:

1. Sandbox create allocates a writable system disk and a separate persistent workspace disk for the guest-backed backend.
2. Sandbox start boots the guest and waits for a bounded readiness handshake before marking the sandbox `running`.
3. If guest boot or bootstrap fails, the sandbox is marked `error` and the failure reason is persisted in runtime state.
4. Stop, destroy, inspect, snapshot create, and snapshot restore work against the guest-backed backend using the same control-plane service flow used today.
5. Suspend and resume behavior is defined explicitly for the guest backend, either as implemented behavior or as an intentional, documented non-goal for this phase with clear operator-facing errors.

### 2.4 Requirement 4: Simple guest control channel

The guest-backed backend must use one lightweight control channel for exec, PTY, file operations, and health checks, without introducing a second always-on control-plane service.

Acceptance criteria:

1. The chosen control channel is documented and implemented as SSH-based guest access after first-boot bootstrap.
2. Exec requests run inside the guest over the control channel and preserve the existing timeout and bounded-output behavior.
3. TTY attach works against a guest shell, including resize handling.
4. File operations use the same guest boundary rather than reading or writing arbitrary host paths.
5. Secrets used for guest access are operator-controlled, stored outside SQLite, and never returned in API responses or logs.

### 2.5 Requirement 5: Real storage boundaries with minimal accounting complexity

The system must replace requested-storage bookkeeping with real per-sandbox storage limits while staying operationally simple.

Acceptance criteria:

1. The guest writable system layer has a hard maximum size derived from `disk_limit_mb` or a documented split of that limit.
2. The persistent workspace storage has a hard maximum size and survives stop or start and snapshot or restore.
3. Guest-local container engine storage counts toward sandbox usage by living inside quota-bounded guest disks rather than through separate host-side special cases.
4. Storage usage reported in SQLite reflects measured disk usage from actual sandbox artifacts, not only requested values.
5. Disk-full behavior is handled predictably and covered by a regression or integration drill.

### 2.6 Requirement 6: Network isolation and trusted-mode compatibility

The guest-backed runtime must preserve the current networking contract while keeping Docker available for trusted use.

Acceptance criteria:

1. `internet-enabled` and `internet-disabled` behavior both exist for the guest-backed backend.
2. Sandboxes do not have direct host network exposure by default.
3. East-west sandbox connectivity is blocked by default for the guest-backed backend.
4. Tunnel exposure remains explicit and revocable through the existing control-plane path.
5. Docker remains supported only as an explicitly trusted mode and is described that way in docs and config validation.

### 2.7 Requirement 7: Durable guest environment contract

The guest-backed sandbox must behave enough like a durable machine to support the workloads already claimed by the project.

Acceptance criteria:

1. Package installs in the guest persist across stop and start.
2. Workspace changes persist across stop and start.
3. A lightweight guest init or supervision strategy exists and is documented.
4. A guest-local container engine is available inside the guest without exposing the host runtime socket.
5. The default guest image includes or can reliably support Git, Python, Node or npm, browser automation dependencies, and the guest-local container engine.

### 2.8 Requirement 8: Verification and recovery confidence

The project must only claim production-aligned progress that is backed by targeted workload and failure verification.

Acceptance criteria:

1. Automated workload coverage exists for Git, Python package persistence, npm package persistence, browser automation, and guest-local container engine behavior.
2. Automated or scripted recovery drills exist for control-plane restart during exec, guest boot failure, disk-full handling, and snapshot failure handling.
3. Reconciliation logic handles partially bootstrapped or failed guest sandboxes without deleting healthy data silently.
4. Operator-visible health inspection exists for runtime backend availability and guest readiness state.
5. Host compatibility coverage exists for both Linux and macOS at least for backend startup validation, lifecycle smoke tests, and exec reachability.

## 3. Non-functional constraints

- Keep the implementation single-node and SQLite-backed.
- Keep the implementation usable on both Linux and macOS without splitting into separate runtime backends.
- Preserve deterministic single-process SQLite behavior and migration safety.
- Favor shelling out to existing host tools, as the Docker runtime already does, instead of adding heavy orchestration libraries.
- Keep loops, output previews, and readiness waits bounded.
- Keep file access restricted to sandbox-scoped resources; never reintroduce arbitrary host-path access.
- Keep runtime secrets, SSH keys, and any tunnel credentials out of SQLite and out of normal logs.
- Avoid schema changes unless they provide direct operational value for this phase.
- Preserve current API and CLI contracts unless a change is required to satisfy a safety or correctness gap.
