# Missing Functionality Closure Tasks

## 1. Add snapshot read paths to the server [R1]

- [x] Add `ListSnapshots` query support in `internal/repository/store.go` scoped by `tenant_id` and `sandbox_id`, ordered for operator readability.
- [x] Add `ListSnapshots` and `GetSnapshot` wrappers in `internal/service/service.go` using the existing tenant-scoped snapshot model.
- [x] Extend `internal/api/router.go` to support `GET /v1/sandboxes/{id}/snapshots` and `GET /v1/snapshots/{id}` while preserving existing create and restore routes.
- [x] Reuse the existing `model.Snapshot` JSON shape and avoid schema changes unless implementation proves they are required.

## 2. Add snapshot commands to `sandboxctl` [R2]

- [x] Update `cmd/sandboxctl/main.go` command dispatch to add `snapshot-create`, `snapshot-list`, `snapshot-inspect`, and `snapshot-restore`.
- [x] Implement `snapshot-create` to call `POST /v1/sandboxes/{id}/snapshots` with an optional snapshot name.
- [x] Implement `snapshot-list` to call `GET /v1/sandboxes/{id}/snapshots`.
- [x] Implement `snapshot-inspect` to call `GET /v1/snapshots/{id}`.
- [x] Implement `snapshot-restore` to call `POST /v1/snapshots/{id}/restore` with `target_sandbox_id`.
- [x] Update CLI usage output so the new snapshot commands appear in help text.

## 3. Close the existing CLI parity gap for force stop [R3]

- [x] Refactor `cmd/sandboxctl/main.go` so `stop` can parse `--force` without breaking `start`, `suspend`, or `resume`.
- [x] Send `model.LifecycleRequest{Force: true}` only for `sandboxctl stop --force <sandbox-id>`.
- [x] Preserve the existing default `sandboxctl stop <sandbox-id>` behavior.
- [x] Update command usage text for `stop` so the force option is discoverable.

## 4. Add regression coverage [R1, R2, R3]

- [x] Extend `internal/api/integration_test.go` to verify snapshot create, list, inspect, and restore in one tenant-owned flow.
- [x] Add API coverage proving another tenant cannot list or inspect someone else’s snapshots.
- [x] Add focused tests for the new CLI commands and force-stop request body shape, likely under a new `cmd/sandboxctl` test file.
- [x] Keep tests narrow and local to the new behavior instead of broad unrelated refactors.

## 5. Refresh docs after the code lands [R4]

- [x] Update `docs/project-overview.md` to remove the stale statement that snapshots are API-only.
- [x] Update `docs/usage.md` with snapshot CLI examples for create, list, inspect, and restore.
- [x] Update `docs/api-reference.md` to include the snapshot list and inspect routes.
- [x] Add one short snapshot example to a tutorial only if it helps beginners without making the docs noisy.
- [x] Update `docs/runtimes.md` to describe QEMU suspend and resume support accurately.

## 6. Refresh planning and status docs [R5]

- [x] Update `planning/whats_left.md` so it no longer treats snapshot user access as missing once the parity work lands.
- [x] Update `planning/onwards/status_matrix.md` so snapshot support status reflects the shipped server-plus-CLI workflow.
- [x] Refresh planning docs so they no longer describe QEMU `suspend` and `resume` as missing.

## 7. Out of scope for this closure pass

- [x] Implement QEMU `Suspend` and `Resume` support inside `internal/runtime/qemu/runtime.go`.
- [ ] Add brand-new runtime backends or broaden the architecture beyond the current single-node Go and SQLite design.
