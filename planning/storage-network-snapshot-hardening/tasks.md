# Tasks

## 1. Define and wire storage classes (Req 1, 2, 8)

- [x] Add small internal storage-class definitions in `internal/model` or `internal/service`.
- [x] Update `internal/service/service.go` to create distinct roots for durable workspace, cache, scratch, and any operator-managed secrets material.
- [x] Update runtime specs so Docker and QEMU can map those roots consistently.
- [x] Document durability and snapshot inclusion rules in `docs/tutorials/files-and-tunnels.md` and related docs.

## 2. Tighten writable layout and secrets handling (Req 1, 2, 7)

- [x] Ensure secrets material is mounted or exposed separately from workspace and is excluded from snapshot/export flows.
- [x] Keep cache and scratch out of portable workspace snapshot payloads unless explicitly intended.
- [x] Review workspace file APIs in `internal/service/service.go` so they remain rooted to the allowed workspace class only.
- [x] Add service tests covering class separation and snapshot exclusion behavior.

## 3. Improve storage-limit enforcement (Req 3, 4, 8)

- [x] Review QEMU disk sizing in `internal/runtime/qemu` and document it as the hard storage boundary for VM-backed sandboxes.
- [x] Add the strongest supported Docker-side quota enforcement available in `internal/runtime/docker/runtime.go` without breaking trusted local dev workflows.
- [x] Extend storage measurement and reconcile logic in `internal/service/service.go` and `internal/repository/store.go` to detect both byte growth and file-count/inode pressure.
- [x] Surface storage-pressure signals in runtime health, capacity, or metrics output using existing observability surfaces.

## 4. Add a lightweight network-policy model (Req 5, 6, 8)

- [x] Add a small internal network-policy resolver that maps existing `NetworkMode` values to concrete behavior.
- [x] Update Docker and QEMU runtime wiring to keep sandbox services loopback-only unless published via the tunnel control plane.
- [x] Keep current API request shapes stable while making egress and exposure policy more explicit under the hood.
- [x] Add audit coverage for exposure changes and policy denials in `internal/service/audit.go` and related tests.

## 5. Harden snapshot import/export and restore (Req 7, 8)

- [x] Review the snapshot create/restore paths in `internal/service/service.go` and add bounded tar validation before extraction.
- [x] Reject path traversal, unsupported special files, dangerous links, and oversized expansion ratios.
- [x] Normalize ownership and permissions on restore.
- [x] Persist enough metadata on snapshots to reject incompatible profile/runtime restores where needed.
- [x] Add regression tests for malformed and hostile archive inputs.

## 6. Add abuse and recovery validation (Req 3, 4, 5, 7)

- [x] Extend `scripts/qemu-resource-abuse.sh` with file-count and storage-pressure scenarios.
- [x] Add targeted tests or scripts for Docker trusted-mode storage pressure where hard quotas are not available.
- [x] Verify tunnel exposure remains loopback-only unless explicitly published.
- [x] Add snapshot failure and partial-restore drills to the operations scripts or docs.

## 7. Update docs and operator guidance (Req 1, 3, 5, 6, 7)

- [x] Update `docs/operations/snapshot-failed.md` with restore validation and cleanup behavior.
- [x] Update `docs/tutorials/files-and-tunnels.md` with storage classes, durability rules, and tunnel exposure rules.
- [x] Update `docs/configuration.md` with any new snapshot or storage-pressure limits.
- [x] Make platform-specific limits explicit, especially for Docker on non-Linux hosts.

## 8. Out of scope for this plan

- [ ] Do not add a full SDN controller or external firewall service.
- [ ] Do not add a distributed snapshot catalog or object-store control plane.
- [ ] Do not expose a large new networking DSL in the public API.
- [ ] Do not rely on secrets being stored inside workspace or snapshot tarballs.
