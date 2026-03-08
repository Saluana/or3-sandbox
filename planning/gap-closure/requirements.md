# Missing Functionality Closure Requirements

## 1. Overview

This plan is based on a code audit of the current `or3-sandbox` repository, not only on older planning notes.

Confirmed findings from the audit:

- snapshot **create** and **restore** are already implemented in the daemon, service layer, repository, Docker runtime, QEMU runtime, and API tests
- `sandboxctl` does **not** expose snapshot commands today
- the server does **not** currently expose snapshot **list** or **inspect** routes, even though snapshot records are stored in SQLite
- `sandboxctl stop` does not expose the server's existing `force` stop capability
- several docs and planning files still describe earlier gaps that are now partially or fully implemented
- QEMU `suspend` and `resume` were a real runtime limitation at audit time and are now part of the closure work

Scope of this plan:

- close the missing snapshot workflow from operator-facing CLI through API and service layers
- close small but real CLI parity gaps discovered during the audit
- update docs so they match the shipped implementation after the code lands
- refresh planning/status docs so they stop reporting already-implemented behavior as missing

Assumption:

- this closure pass should focus on missing user-visible functionality and documentation accuracy, not on broad runtime redesign

## 2. Requirements

### 2.1 Requirement 1: Snapshot discovery must be exposed through the API

The control plane must expose enough snapshot read paths for users and the CLI to work with snapshots without requiring direct database access or remembering IDs from earlier output.

Acceptance criteria:

1. The API supports listing snapshots for a sandbox.
2. The API supports inspecting a single snapshot by ID.
3. Snapshot reads remain tenant-scoped and return `404` for cross-tenant access.
4. Existing snapshot create and restore routes remain backward compatible.
5. API integration coverage proves create, list, inspect, and restore work together.

### 2.2 Requirement 2: `sandboxctl` must expose the snapshot workflow end-to-end

The CLI must expose the snapshot functionality already present in the server and any new snapshot read routes added by this plan.

Acceptance criteria:

1. `sandboxctl` supports a snapshot create command.
2. `sandboxctl` supports a snapshot list command.
3. `sandboxctl` supports a snapshot inspect command.
4. `sandboxctl` supports a snapshot restore command targeting an existing sandbox.
5. Each command uses the existing `SANDBOX_API` and `SANDBOX_TOKEN` flow and prints normal JSON output or direct errors consistent with the rest of the CLI.
6. The top-level CLI usage text includes the new commands.

### 2.3 Requirement 3: Existing server features exposed in the API should have reasonable CLI parity

Where a feature already exists in the API and fits the current CLI model, the CLI should expose it unless there is a deliberate reason not to.

Acceptance criteria:

1. `sandboxctl stop` supports the server's existing force-stop behavior.
2. Force-stop behavior is implemented without breaking the existing `sandboxctl stop <sandbox-id>` form.
3. CLI argument parsing remains simple and consistent with the current single-file command structure.
4. Tests cover the force-stop request payload.

### 2.4 Requirement 4: Docs must describe the actual shipped behavior after the implementation lands

The docs must stop telling users that snapshot functionality is API-only once the CLI supports it.

Acceptance criteria:

1. The docs no longer state that `sandboxctl` lacks snapshot commands.
2. Setup or usage docs include the new snapshot CLI workflow.
3. API docs reflect the new snapshot list and inspect routes if those routes are added.
4. Runtime docs are updated to describe QEMU `suspend` and `resume` as supported once the runtime work lands.
5. Documentation remains readable to a beginner audience.

### 2.5 Requirement 5: Planning docs must be corrected to distinguish real remaining gaps from stale ones

The planning material should remain a reliable source of truth for what is actually left.

Acceptance criteria:

1. Planning docs no longer describe snapshot support as missing once the CLI and API parity work lands.
2. Planning docs stop calling out QEMU `suspend` and `resume` as missing once that functionality is implemented.
3. The refreshed planning text clearly separates user-facing parity work from deeper runtime follow-ons.

## 3. Non-functional constraints

- Keep the implementation single-node and SQLite-backed.
- Preserve current auth, tenant scoping, and session isolation behavior.
- Avoid SQLite schema changes unless the audit proves they are necessary; the existing `snapshots` table should be reused if possible.
- Preserve backward compatibility for existing snapshot create and restore routes.
- Keep CLI behavior small, direct, and consistent with the current `cmd/sandboxctl/main.go` style.
- Keep docs honest and beginner-friendly.
- Keep the runtime change minimal and aligned with the current process-based QEMU backend.
