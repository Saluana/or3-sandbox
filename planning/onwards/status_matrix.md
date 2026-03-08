# Sandbox Onwards Status Matrix

This matrix is the short version of project status while the `planning/onwards/` work is in progress.

Legend:

- shipped in trusted Docker mode
- shipped in first-pass guest-backed mode
- partially shipped in guest-backed mode
- deferred

## Major v1 claims

| Claim | Status | Notes |
| --- | --- | --- |
| Single Go daemon with SQLite control plane | shipped in trusted Docker mode | Implemented today. |
| Authenticated tenant-scoped sandbox lifecycle API | shipped in trusted Docker mode | Implemented today. |
| Exec streaming and PTY attach | shipped in first-pass guest-backed mode | Available through both runtimes. |
| File APIs scoped to sandbox workspace | shipped in first-pass guest-backed mode | Available through both runtimes. |
| Local snapshots and restore | shipped in first-pass guest-backed mode | Available through both runtimes. |
| Snapshot create, list, inspect, and restore in `sandboxctl` | shipped in first-pass guest-backed mode | CLI and API now expose the full snapshot workflow. |
| Explicit tunnel creation and revoke path | shipped in trusted Docker mode | Implemented today. |
| Production hostile multi-tenant isolation boundary | partially shipped in guest-backed mode | First-pass QEMU runtime exists; hardening and some lifecycle parity remain. |
| Guest boot and readiness handshake | shipped in first-pass guest-backed mode | QEMU waits for SSH reachability and readiness. |
| Guest-local container engine as a supported sandbox workload | shipped in first-pass guest-backed mode | Covered by host integration tests, with ongoing hardening still sensible. |
| Headless browser automation as a verified workload | shipped in first-pass guest-backed mode | Covered by host integration tests. |
| Hard storage enforcement with measured usage | partially shipped in guest-backed mode | Guest-backed coverage is stronger than trusted Docker mode. |
| Optional S3-compatible snapshot export | shipped in first-pass guest-backed mode | Snapshot bundle export and restore fetch paths exist. |
| Fractional CPU or millicore limits | shipped in trusted Docker mode | The model and CLI now accept fractional or millicore values. |
| QEMU suspend and resume parity | shipped in first-pass guest-backed mode | Implemented with process-level suspend and resume handling in the current backend. |

## Source of truth

- `planning/whats_left.md` summarizes the remaining real gaps.
- `planning/tasks2.md` gives a general follow-on execution plan.
- `planning/onwards/tasks.md` is the active implementation checklist for the next phase.
