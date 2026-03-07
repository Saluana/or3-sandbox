# Sandbox Onwards Status Matrix

This matrix is the short version of project status while the `planning/onwards/` work is in progress.

Legend:

- shipped in trusted Docker mode
- planned for guest-backed mode
- deferred

## Major v1 claims

| Claim | Status | Notes |
| --- | --- | --- |
| Single Go daemon with SQLite control plane | shipped in trusted Docker mode | Implemented today. |
| Authenticated tenant-scoped sandbox lifecycle API | shipped in trusted Docker mode | Implemented today. |
| Exec streaming and PTY attach | shipped in trusted Docker mode | Implemented today through the Docker backend. |
| File APIs scoped to sandbox workspace | shipped in trusted Docker mode | Implemented today for the Docker backend. |
| Local snapshots and restore | shipped in trusted Docker mode | Implemented today for Docker container plus workspace tarball. |
| Explicit tunnel creation and revoke path | shipped in trusted Docker mode | Implemented today. |
| Production hostile multi-tenant isolation boundary | planned for guest-backed mode | The design requires a guest-backed backend; Docker is trusted-only. |
| Guest boot and readiness handshake | planned for guest-backed mode | Not implemented in the current Docker runtime. |
| Guest-local container engine as a supported sandbox workload | planned for guest-backed mode | Base image installs tooling, but the end-to-end runtime path is not proven. |
| Headless browser automation as a verified workload | planned for guest-backed mode | Dependencies exist, but workload verification is still open. |
| Hard storage enforcement with measured usage | planned for guest-backed mode | Current quota flow is requested-size bookkeeping plus host-path size refresh. |
| Optional S3-compatible snapshot export | deferred | Still additive follow-up work. |
| Fractional CPU or millicore limits | deferred | Current model is integer-only. |

## Source of truth

- `planning/whats_left.md` summarizes the remaining design gaps.
- `planning/tasks2.md` gives a general follow-on execution plan.
- `planning/onwards/tasks.md` is the active implementation checklist for the next phase.
