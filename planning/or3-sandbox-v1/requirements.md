# Requirements

## Overview

This plan turns the v1 product direction into a repo-aligned implementation path for `or3-sandbox`.

The current codebase already has the right architectural center:

- a single-process Go daemon in `cmd/sandboxd`
- a clean `internal/api` → `internal/service` → `internal/runtime` split
- SQLite-backed persistence and reconciliation
- existing Docker and QEMU runtime implementations
- explicit runtime metadata, quota, audit, snapshot, and observability surfaces

The main v1 work is therefore not a rewrite. It is:

1. make runtime selection explicit per sandbox instead of per daemon only
2. preserve the unified control plane while dispatching to multiple backends
3. add a professional `containerd + Kata` runtime adapter under the existing runtime boundary
4. keep Docker clearly scoped to personal/trusted use
5. keep QEMU in scope because it is already an active VM-backed backend in this repo

Assumptions:

- v1 remains a single Go control plane with SQLite and no Kubernetes dependency
- Docker remains the simplest local/self-hosted path
- `containerd + Kata` becomes the primary professional hosted runtime once it reaches lifecycle and exec parity
- QEMU remains a supported professional/advanced backend because it already exists and its current feature set should not be regressed
- because this repo already uses `runtime_class` as a persisted isolation concept, implementation may need a lightweight compatibility layer so explicit operator-facing runtime selection does not break existing rows, config, or policy behavior

## Requirements

1. **Support explicit per-sandbox runtime selection under one control plane.**
   - Acceptance criteria:
     - sandbox creation accepts an explicit runtime selection value that maps to the requested product modes: Docker development/trusted, Kata professional, and QEMU professional.
     - when the request omits runtime selection, the daemon applies a configured default.
     - runtime selection is persisted with sandbox metadata and survives daemon restart, reconciliation, snapshot creation, and restore validation.
     - the control plane dispatches sandbox operations by persisted runtime selection instead of assuming one daemon-wide backend.

2. **Keep Docker as the low-friction personal and trusted mode.**
   - Acceptance criteria:
     - Docker remains easy to run locally with the existing low-friction setup path.
     - documentation and API metadata clearly communicate that Docker is for personal, local, or trusted self-hosted use.
     - production policy continues to reject Docker as the hostile multi-tenant boundary.
     - Docker-specific behavior remains contained inside the Docker runtime package and related policy/config helpers.

3. **Add `containerd + Kata` as the primary professional hosted runtime.**
   - Acceptance criteria:
     - a new runtime adapter exists for containerd-backed Kata workloads and plugs into the existing runtime abstraction.
     - the adapter supports, at minimum, create, start, stop, destroy, inspect status, exec, attach terminal, and resource/network application.
     - the adapter exposes clear structured errors for unsupported features during its first implementation wave rather than silently degrading.
     - operator-facing config can enable or disable this runtime independently from Docker and QEMU.

4. **Keep QEMU supported as an advanced VM-backed runtime.**
   - Acceptance criteria:
     - the existing QEMU runtime remains compatible with the unified runtime-selection flow.
     - existing QEMU snapshot, reconciliation, and guest-control behavior continue to work for sandboxes that target QEMU.
     - professional-mode policy treats QEMU as VM-backed and eligible when enabled by config.
     - the plan does not require removing or rewriting QEMU in order to add Kata.

5. **Preserve a runtime-agnostic control plane contract.**
   - Acceptance criteria:
     - the service layer and API continue to work through shared runtime interfaces and shared sandbox metadata rather than backend-specific CLI assumptions.
     - the shared contract covers create, start, stop, destroy, exec, interactive attach, output capture, status inspection, snapshot, restore, network mode, resource limits, and health/capability reporting.
     - backend-specific implementation differences are hidden behind runtime adapters and surfaced only as explicit capability differences or structured unsupported errors.
     - no new external scheduler, queue, or control-plane service is introduced.

6. **Make runtime policy explicit and enforceable.**
   - Acceptance criteria:
     - config exposes enabled runtime selections and a default runtime selection.
     - sandbox creation fails if a tenant requests a runtime that is disabled or not allowed by policy.
     - production mode defaults to a VM-backed runtime selection and fails closed when only trusted/shared-kernel modes are enabled.
     - runtime-specific image/template allowlists can be enforced without leaking backend-specific assumptions into unrelated packages.

7. **Persist enough metadata to support reconciliation, audit, and restore safely.**
   - Acceptance criteria:
     - sandbox metadata records tenant, runtime selection, backend family, isolation posture, template/image reference, profile, resource limits, and network posture.
     - snapshot metadata records the originating runtime selection or equivalent restore-compatibility information.
     - audit events for create/start/restore/expose/delete include the runtime used.
     - legacy rows created before the new runtime-selection data exists continue to reconcile deterministically through additive migration behavior.

8. **Keep lifecycle and execution behavior consistent across backends where practical.**
   - Acceptance criteria:
     - create/start/stop/delete/status work through the same API routes for Docker, Kata, and QEMU.
     - exec returns stdout/stderr preview, exit code, timeout, and cancellation behavior consistently across supported backends.
     - interactive terminal attach remains available through the same WebSocket path when the backend supports it.
     - when a backend lacks parity for a feature during v1, the API returns a clear structured error and docs list the limitation.

9. **Preserve the current filesystem and snapshot safety model.**
   - Acceptance criteria:
     - each sandbox still gets persistent workspace storage plus bounded scratch/cache behavior according to the selected runtime.
     - snapshot import and restore continue to defend against path traversal, special files, abusive expansion, and oversized archives.
     - snapshot/restore compatibility checks validate runtime and template compatibility before mutating an existing sandbox.
     - the design keeps writable surface minimal by default.

10. **Preserve and extend resource enforcement and quota behavior.**
    - Acceptance criteria:
      - CPU, memory, PID, disk, exec timeout, and network mode remain part of sandbox creation and enforcement.
      - backends enforce limits where the runtime can actually enforce them; unsupported limits are surfaced explicitly.
      - per-tenant quotas for sandbox count, running sandboxes, CPU, memory, storage, and concurrent execs continue to work regardless of runtime selection.
      - node-level admission checks remain bounded and deterministic.

11. **Keep recovery, reconciliation, and observability first-class.**
    - Acceptance criteria:
      - daemon restart reconciliation dispatches inspect/update logic to the correct runtime for each persisted sandbox.
      - runtime health and capacity reporting include counts grouped by runtime selection in addition to current profile and status summaries where useful.
      - logs, metrics, and audit trails cover lifecycle failure, exec failure, snapshot failure, crash/degraded state, quota denial, and runtime selection.
      - orphan cleanup remains bounded and does not assume Docker-specific artifacts only.

12. **Document operator-facing trust guidance and runtime tradeoffs clearly.**
    - Acceptance criteria:
      - docs explain when to use Docker, Kata, and QEMU.
      - docs state that Docker, Kata, and QEMU do not provide identical internals or operational tradeoffs.
      - setup docs describe how to enable each runtime and what host prerequisites are required.
      - API and CLI examples show explicit runtime selection where relevant.

## Non-functional constraints

- Keep the design single-process, SQLite-compatible, and deterministic.
- Prefer small changes to existing packages over introducing a new service layer or orchestration framework.
- Keep control-plane RAM and background activity low; avoid watchers, agents, or daemons beyond what the selected runtime already requires.
- Preserve current config loading behavior through additive changes and backward-compatible defaults.
- Preserve current SQLite migration style: additive columns/tables, safe backfill, deterministic startup migration.
- Keep file, network, tunnel, and snapshot behavior safe by default.
- Do not assume a frontend, Kubernetes, or multi-node scheduler.
- Keep runtime-specific complexity inside runtime packages and minimal dispatch code rather than spreading it across API handlers or repository code.
