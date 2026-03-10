# Tasks

## 1. Finalize the runtime-selection model and compatibility rules (Req 1, 5, 6, 7)

- [x] Define the explicit operator-facing runtime selection values for v1 and document how they map to backend family and VM-backed policy.
- [x] Decide whether to preserve the existing `runtime_class` field as isolation posture and add a new runtime-selection field, or to evolve the existing field with a compatibility layer.
- [ ] Update `internal/model/runtime_class.go` and related model files with stable helpers for backend mapping, VM-backed checks, and legacy fallback.
- [ ] Add focused unit tests for runtime-selection parsing, backend mapping, and VM-backed policy helpers.

Decision recorded in `planning/or3-sandbox-v1/design.md`: preserve `runtime_class` as isolation posture, add explicit persisted `runtime_selection`, and use deterministic fallback from legacy `runtime_backend` values.

## 2. Add additive config for enabled/default runtime selection (Req 1, 3, 4, 6, 12)

- [ ] Extend `internal/config/config.go` to parse enabled runtime selections and a default runtime selection while preserving legacy `SANDBOX_RUNTIME` behavior.
- [ ] Reuse existing Docker and QEMU config fields; add only the Kata-specific host/runtime options that are actually required.
- [ ] Fail closed in production mode when the configured default runtime selection is not VM-backed.
- [ ] Add config tests covering legacy fallback, mixed-runtime config, disabled runtime selection, and production-mode validation.

## 3. Introduce a lightweight runtime registry/dispatcher (Req 1, 3, 4, 5, 8, 11)

- [ ] Add a small package such as `internal/runtime/registry` that implements `model.RuntimeManager` by dispatching operations to concrete runtimes by runtime selection.
- [ ] Extend `internal/model/runtime.go` so `SandboxSpec` carries explicit runtime-selection metadata for create-time dispatch.
- [ ] Ensure non-create operations dispatch by persisted sandbox metadata rather than daemon-wide config.
- [ ] Add unit tests for correct dispatch, missing-runtime errors, and disabled-runtime reconcile behavior.

## 4. Rewire daemon startup around the runtime registry (Req 1, 3, 4, 5, 6)

- [ ] Update `cmd/sandboxd/main.go` to build all enabled runtimes and register them instead of constructing a single global backend.
- [ ] Keep startup logs and health reporting explicit about enabled and default runtime selections.
- [ ] Preserve backward compatibility for Docker-only and QEMU-only installs.
- [ ] Add or update startup-focused tests where the current test surface allows it.

## 5. Extend API and CLI request models for explicit runtime selection (Req 1, 6, 8, 12)

- [ ] Add explicit runtime selection to `internal/model.CreateSandboxRequest` and any related response models.
- [ ] Update `internal/api/router.go` to accept and validate the new field on sandbox creation.
- [ ] Update `cmd/sandboxctl` create/preset flows to send runtime selection when requested and to display default/available runtime selections from runtime info.
- [ ] Add API and CLI tests for explicit selection, omitted-selection defaulting, and disabled-runtime errors.

## 6. Make service-layer create/policy flows runtime-selection aware (Req 1, 2, 3, 4, 6, 8, 10)

- [ ] Update `internal/service/service.go` so sandbox creation resolves runtime selection, backend family, and isolation posture before persisting and dispatching.
- [ ] Update `internal/service/policy.go` so image allowlists, dangerous-profile rules, public exposure policy, and production checks key off the resolved runtime selection instead of a daemon-wide backend only.
- [ ] Ensure lifecycle, exec, snapshot, and restore flows use persisted sandbox runtime metadata consistently.
- [ ] Add service tests for create denial, mixed-runtime reconciliation, and runtime-stamped audit behavior.

## 7. Extend persistence and migrations for explicit runtime selection (Req 1, 7, 9, 11)

- [ ] Add additive SQLite migration(s) in `internal/db/db.go` for explicit runtime selection on sandboxes and snapshots if compatibility analysis says a new column is safer.
- [ ] Backfill legacy rows deterministically from `runtime_backend`.
- [ ] Update `internal/repository/store.go` scan/insert/update paths to read and write the new metadata while preserving legacy fallback behavior.
- [ ] Add DB and repository tests covering migration, backfill, snapshot metadata, and legacy-row reconciliation.

## 8. Extend runtime info, health, capacity, and audit surfaces (Req 5, 6, 7, 11, 12)

- [ ] Update `internal/api/router.go` and related model types so runtime info exposes enabled/default runtime selections alongside existing compatibility fields.
- [ ] Update `internal/service/observability.go` to include counts and alerts grouped by runtime selection where useful.
- [ ] Ensure audit events for create, restore, delete, and exposure include the selected runtime.
- [ ] Add tests for runtime-info output and runtime-selection observability summaries.

## 9. Keep Docker localized and explicitly trusted-only (Req 2, 5, 6, 8, 12)

- [ ] Review `internal/runtime/docker` and adjacent policy/config paths to ensure Docker remains fully localized behind the adapter boundary.
- [ ] Preserve the current least-privilege Docker defaults and trusted-runtime opt-in behavior.
- [ ] Ensure docs and runtime info continue to mark Docker as non-production for hostile multi-tenant use.
- [ ] Add or update regression coverage only where dispatch or metadata changes affect Docker behavior.

## 10. Adapt QEMU to the multi-runtime control plane without regression (Req 4, 5, 7, 8, 9, 11)

- [ ] Update `internal/runtime/qemu` to work under the registry path with persisted runtime selection and explicit capability reporting if needed.
- [ ] Preserve current QEMU lifecycle, snapshot, restore, and reconciliation behavior.
- [ ] Ensure QEMU remains production-eligible when enabled by config.
- [ ] Add regression coverage for QEMU dispatch under the registry and mixed-runtime reconcile flows.

## 11. Add the Kata runtime adapter as a new bounded package (Req 3, 5, 8, 9, 10, 11)

- [ ] Add `internal/runtime/kata` implementing the shared runtime contract for create, start, stop, destroy, inspect, exec, and attach.
- [ ] Reuse the service-owned workspace/cache/scratch layout instead of inventing a new storage subsystem.
- [ ] Enforce CPU, memory, PID, disk, and network settings where containerd + Kata can enforce them; return structured unsupported errors where first-wave parity is incomplete.
- [ ] Add unit tests for containerd/Kata command or client wiring, inspect parsing, and unsupported-feature behavior.

## 12. Define snapshot and restore behavior for Kata explicitly (Req 5, 8, 9, 11)

- [ ] Decide and document whether Kata v1 snapshots use the existing image-ref + workspace-archive model, a runtime-native snapshot path, or a staged subset of both.
- [ ] Implement only the bounded snapshot/restore behavior that can be validated safely in this repo’s current architecture.
- [ ] Ensure restore compatibility checks reject mismatched runtime selection or template/image metadata before workspace mutation.
- [ ] Add snapshot and restore tests for Docker, QEMU, and Kata compatibility/error behavior.

## 13. Keep network and tunnel behavior common at the API level (Req 5, 8, 9, 10, 12)

- [ ] Verify Docker, QEMU, and Kata all accept the existing common network mode model and tunnel policy flow.
- [ ] Add structured unsupported errors for any backend that cannot support a tunnel or network mode combination during the first wave.
- [ ] Keep public exposure opt-in and policy-gated regardless of runtime selection.
- [ ] Extend API/service tests if runtime-selection-aware tunnel behavior needs coverage.

## 14. Document runtime tradeoffs and operator guidance (Req 2, 3, 4, 6, 12)

- [ ] Update `docs/runtimes.md` with the v1 positioning: Docker for personal/trusted use, Kata as the primary professional hosted runtime, and QEMU as the advanced/professional path already active in this repo.
- [ ] Update `docs/architecture.md` to describe the runtime registry/dispatcher and per-sandbox runtime selection.
- [ ] Update `docs/setup.md` and `docs/usage.md` with explicit runtime-selection examples and host prerequisites.
- [ ] Ensure docs clearly state that the backends share one API but do not promise identical internals.

## 15. Add runtime-selection-aware doctor coverage (Req 3, 4, 6, 11, 12)

- [ ] Extend `cmd/sandboxctl/doctor.go` to report enabled/default runtime selections and host prerequisite failures for Docker, QEMU, and Kata.
- [ ] Keep doctor output bounded, operator-readable, and explicit about blocking failures versus warnings.
- [ ] Add tests for mixed-runtime doctor output and production posture failures.
- [ ] Document how operators use doctor before enabling professional runtime selections.

## 16. Add regression and integration coverage for mixed-runtime operation (Req 1, 5, 8, 9, 10, 11)

- [ ] Add service and API tests that create, list, inspect, and reconcile sandboxes across more than one enabled runtime.
- [ ] Add repository tests ensuring mixed-runtime rows can be listed and scanned deterministically.
- [ ] Add host-gated integration coverage for Kata and keep existing Docker/QEMU integration coverage working.
- [ ] Verify unsupported-feature responses are explicit and stable enough for CLI/operator use.

## 17. Roll out in phases to keep the codebase clean (Req 1-12)

- [ ] Phase 1: land runtime-selection metadata, config, registry dispatch, and mixed-runtime reconciliation while preserving Docker and QEMU behavior.
- [ ] Phase 2: add Kata lifecycle, exec, and inspect parity plus doctor/config support.
- [ ] Phase 3: add or finalize snapshot/restore parity, observability polish, and documentation before calling Kata the primary professional default.
- [ ] Keep each phase shippable without requiring a full architectural rewrite.

## 18. Out of scope

- [ ] Do not add Kubernetes or multi-node orchestration.
- [ ] Do not add a new external control-plane service, queue, or scheduler.
- [ ] Do not attempt to make all runtime internals identical.
- [ ] Do not remove QEMU just to introduce Kata.
- [ ] Do not market Docker as the hostile multi-tenant boundary.
