# Tasks

## 1. Add runtime-class metadata and policy vocabulary (Req 1, 2, 3)

- [x] Add a small runtime-class type to `internal/model` and use it to distinguish `trusted-docker` from VM-backed runtimes.
- [x] Extend `internal/model/model.go` and `internal/model/runtime.go` with additive runtime-class fields where they improve persistence and inspection clarity.
- [x] Update `internal/api/router.go` response shapes only additively so runtime info/health can expose backend and class together.

## 2. Persist runtime-class data without breaking existing rows (Req 1, 6)

- [x] Add backward-compatible schema columns in `internal/db/db.go` for sandbox and snapshot runtime-class metadata if explicit persistence is chosen.
- [x] Update `internal/repository/store.go` reads and writes to round-trip runtime-class values.
- [x] Add legacy-read behavior that derives runtime class from `runtime_backend` when older rows have empty metadata.
- [x] Add repository regression tests in `internal/repository/store_test.go` for both migrated and legacy rows.

## 3. Centralize backend-to-class resolution (Req 1, 2, 3, 7)

- [x] Add a small resolver in `internal/config` or `internal/runtime` that maps backend names to runtime classes.
- [x] Update `cmd/sandboxd/main.go` startup wiring to resolve backend/class together.
- [x] Update `internal/service/service.go` sandbox creation to stamp the resolved runtime class once, not recompute it ad hoc in multiple places.
- [x] Update `internal/service/policy.go` to enforce production-only VM boundary using runtime class.

## 4. Introduce a lightweight adapter request model (Req 4, 5, 7, 8)

- [x] Add an internal adapter package or shared runtime request types that model sandbox lifecycle, storage attachments, and network attachments directly.
- [x] Refactor `internal/runtime/docker/runtime.go` to consume the shared request model without changing the public `RuntimeManager` contract.
- [x] Refactor `internal/runtime/qemu/runtime.go` to consume the same shared request model.
- [x] Keep the shared model small and lifecycle-focused; do not add container-orchestrator features not used by this repo.

## 5. Make production fail closed to VM-backed classes (Req 2, 3)

- [x] Update `internal/config/config.go` validation so `SANDBOX_MODE=production` rejects any runtime that does not resolve to a VM-backed class.
- [x] Keep current local-development Docker flows working without requiring a large config migration.
- [x] Update `cmd/sandboxctl/doctor.go` so doctor output reports both backend and class and flags non-VM production posture as blocking.
- [x] Add config and CLI tests in `internal/config/config_test.go` and `cmd/sandboxctl/main_test.go` for pass/fail coverage.

## 6. Document the new boundary clearly (Req 2, 3, 8)

- [x] Update `docs/runtimes.md` to explain backend versus runtime class and to state that Docker is trusted/local-dev only.
- [x] Update `docs/operations/production-deployment.md` and `docs/project-overview.md` so production language consistently points to VM-backed isolation.
- [x] Add a short architecture note in `docs/architecture.md` describing the adapter layer and why it is intentionally lightweight.

## 7. Add regression coverage and migration checks (Req 6, 7, 8)

- [x] Add service tests in `internal/service/service_test.go` for legacy sandboxes that have a backend but no explicit runtime class.
- [x] Add API tests in `internal/api/integration_test.go` for runtime info/health output.
- [x] Add runtime tests proving Docker resolves to `trusted-docker` and QEMU resolves to `vm`.
- [x] Verify reconcile behavior after daemon restart with mixed legacy and new metadata.

## 8. Out of scope for this plan

- [ ] Do not require containerd or Kata to ship the first boundary fix.
- [ ] Do not add Kubernetes-style pod orchestration or multi-node scheduling.
- [ ] Do not break the existing HTTP lifecycle API.
- [ ] Do not rewrite both runtimes around a new framework; keep the refactor additive and bounded.
