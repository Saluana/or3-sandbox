# Missing Functionality Closure Design

## 1. Overview

The smallest repo-aligned approach is to extend the current control plane with missing snapshot read paths and matching CLI commands, then refresh docs to match the new user-facing behavior.

This fits the current architecture because:

- snapshot create and restore already exist in `internal/service`, `internal/repository`, `internal/api`, and both runtimes
- `sandboxctl` already uses a simple HTTP JSON client model that can be extended without adding new packages or frameworks
- snapshot metadata already lives in SQLite, so snapshot discovery can be implemented by reading existing rows rather than inventing new storage
- the remaining QEMU lifecycle gap can be closed with a minimal process-based implementation rather than a large new control channel

## 2. Affected areas

- `cmd/sandboxctl/main.go`
  - add snapshot-related commands and force-stop flag handling
  - update top-level usage text
- `internal/api/router.go`
  - add snapshot read routes for list and inspect
  - preserve current create and restore behavior
- `internal/service/service.go`
  - add service methods for listing and fetching snapshots
  - reuse existing tenant-scoped snapshot lookup behavior
- `internal/repository/store.go`
  - add snapshot query helpers for listing snapshots by sandbox and tenant
  - reuse existing snapshot row shape and scanner
- `internal/model/model.go`
  - likely no schema or JSON model changes are required; confirm whether any small request helper type is needed
- `internal/api/integration_test.go`
  - extend snapshot flow coverage to include list and inspect routes
- `cmd/sandboxctl/`
  - add the first CLI tests in this package or add focused request-shape tests around the new commands
- `docs/project-overview.md`
  - remove the stale statement that snapshot support is API-only
- `docs/usage.md`
  - add snapshot command examples
- `docs/api-reference.md`
  - add snapshot list and inspect endpoints if implemented
- `docs/tutorials/`
  - optionally add one short snapshot example if it improves discoverability
- `planning/whats_left.md`
  - refresh stale statements after the implementation lands
- `planning/onwards/status_matrix.md`
  - refresh user-facing parity status after the implementation lands

## 3. Control flow / architecture

### 3.1 Snapshot list flow

1. User runs `sandboxctl snapshot-list <sandbox-id>`.
2. The CLI performs `GET /v1/sandboxes/{sandbox-id}/snapshots`.
3. Auth middleware resolves tenant identity as it does for all protected routes.
4. The router calls a new service method such as `ListSnapshots`.
5. The service delegates to a new repository query scoped by tenant and sandbox ID.
6. The repository returns snapshot rows ordered for operator readability, ideally newest first.
7. The router returns JSON to the CLI.

### 3.2 Snapshot inspect flow

1. User runs `sandboxctl snapshot-inspect <snapshot-id>`.
2. The CLI performs `GET /v1/snapshots/{snapshot-id}`.
3. Auth middleware resolves tenant identity.
4. The router calls a new service method such as `GetSnapshot`.
5. The service reuses the existing tenant-scoped repository lookup.
6. The router returns the snapshot JSON payload.

### 3.3 Snapshot create and restore flow

The current flow can remain as-is with CLI wrappers added on top:

1. `snapshot-create` calls `POST /v1/sandboxes/{id}/snapshots`.
2. `snapshot-restore` calls `POST /v1/snapshots/{id}/restore` with `target_sandbox_id`.
3. The existing service logic continues to manage snapshot persistence, optional export fetch, and runtime restore behavior.

### 3.4 Force stop flow

1. User runs `sandboxctl stop --force <sandbox-id>`.
2. The CLI sends `POST /v1/sandboxes/{id}/stop` with `{ "force": true }`.
3. The existing API and service code already support this payload.
4. The returned sandbox state is printed as normal JSON.

## 4. Data and persistence

### 4.1 SQLite

No schema migration should be required.

The existing `snapshots` table already stores:

- snapshot ID
- sandbox ID
- tenant ID
- name
- status
- image reference
- workspace artifact path
- export location
- created and completed timestamps

The main missing behavior is query exposure, not data modeling.

### 4.2 Config

No config change is required for snapshot CLI parity.

Existing snapshot export behavior through `SANDBOX_S3_EXPORT_URI` should remain unchanged.

### 4.3 Session and memory implications

No new session layer or long-lived background worker is needed.

Snapshot list and inspect are simple read paths over existing metadata.

## 5. Interfaces and types

### 5.1 Repository additions

Add small query helpers instead of reshaping the storage model. Likely forms:

```go
func (s *Store) ListSnapshots(ctx context.Context, tenantID, sandboxID string) ([]model.Snapshot, error)
func (s *Store) GetSnapshot(ctx context.Context, tenantID, snapshotID string) (model.Snapshot, error)
```

`GetSnapshot` already exists and can be reused.

### 5.2 Service additions

Add thin service wrappers matching current style:

```go
func (s *Service) ListSnapshots(ctx context.Context, tenantID, sandboxID string) ([]model.Snapshot, error)
func (s *Service) GetSnapshot(ctx context.Context, tenantID, snapshotID string) (model.Snapshot, error)
```

These should not introduce new business logic beyond tenant scoping and sandbox ownership checks as needed.

### 5.3 API routes

Keep routing close to current patterns:

- `GET /v1/sandboxes/{id}/snapshots`
- `GET /v1/snapshots/{id}`
- existing `POST /v1/sandboxes/{id}/snapshots`
- existing `POST /v1/snapshots/{id}/restore`

This keeps snapshot read and write paths consistent with the existing URL scheme.

### 5.4 CLI commands

A minimal command surface that fits the current style:

```text
sandboxctl snapshot-create [--name <name>] <sandbox-id>
sandboxctl snapshot-list <sandbox-id>
sandboxctl snapshot-inspect <snapshot-id>
sandboxctl snapshot-restore <snapshot-id> <target-sandbox-id>
```

For force stop:

```text
sandboxctl stop [--force] <sandbox-id>
```

## 6. Failure modes and safeguards

- **Unknown snapshot ID**: return `404`, not empty success.
- **Cross-tenant snapshot access**: continue returning `404` through tenant-scoped repository lookups.
- **Restore into missing sandbox**: preserve current error behavior.
- **CLI misuse**: return short usage errors consistent with current commands.
- **Docs drift**: update docs in the same implementation pass so the repo does not keep contradicting itself.
- **QEMU runtime expectations**: keep the implementation small and make sure inspect, stop, and reconciliation can observe suspended guests correctly.

## 7. Testing strategy

- Extend `internal/api/integration_test.go` to cover snapshot create, list, inspect, and restore as one tenant-scoped flow.
- Add negative API coverage for cross-tenant snapshot reads.
- Add focused repository or service tests only if query ordering or ownership checks need extra coverage beyond API tests.
- Add CLI-level tests for the new snapshot commands and force-stop request behavior; if no CLI tests exist yet, add a small `httptest`-backed suite instead of overengineering.
- Refresh docs after code changes and review them manually for consistency with the implemented commands and routes.

## 8. Follow-on work after closure

With QEMU suspend and resume implemented, the remaining follow-on work is now more about long-term hardening than missing lifecycle surface area.
