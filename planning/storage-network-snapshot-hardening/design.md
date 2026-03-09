# Design

## Overview

The repo already has the right building blocks for a lightweight hardening pass:
- service-owned storage roots per sandbox
- `sandbox_storage` accounting in SQLite
- snapshot persistence and restore flows
- tunnel APIs and runtime network modes

The design should strengthen these pieces rather than replace them.

The main shift is to make storage and networking more explicit:
- storage becomes a small set of classes with distinct durability rules
- networking becomes a small policy object, not just a raw bridge toggle
- snapshots become bounded, validated archives instead of trusted filesystem state

## Affected areas

- `internal/model/runtime.go`
  - extend runtime spec with storage-class and network-policy metadata if needed
- `internal/model/model.go`
  - add additive metadata for network policy or snapshot compatibility only if necessary
- `internal/service/service.go`
  - create class-specific storage roots and validate snapshot import/export behavior
- `internal/service/policy.go`
  - enforce network policy and dangerous exposure decisions
- `internal/repository/store.go`
  - persist any additive storage/network metadata and improved storage usage reporting
- `internal/db/db.go`
  - add additive columns only if network-policy metadata must be stored explicitly
- `internal/runtime/docker/runtime.go`
  - apply tighter mount layout and backend-aware storage/network enforcement
- `internal/runtime/qemu/runtime.go`
  - preserve hard disk sizing and align guest layout with storage classes
- `internal/api/router.go`
  - surface policy errors and exposure audit behavior cleanly
- `scripts/qemu-resource-abuse.sh`
  - extend abuse coverage to file-count and storage-pressure cases
- `docs/operations/snapshot-failed.md`
  - update operator handling for restore validation failures and partial snapshots
- `docs/tutorials/files-and-tunnels.md`
  - explain the new durability and exposure rules

## Control flow / architecture

### Storage classes

Use a small internal model:
- `workspace`
  - durable user data, included in snapshot/export
- `cache`
  - durable across restarts if desired, excluded from portable snapshot unless explicitly designed otherwise
- `scratch`
  - ephemeral temp area, cleared on restart or restore
- `secrets`
  - operator-managed mounted material, excluded from snapshots and normal file APIs
- `snapshot`
  - archival output managed by the control plane, not a guest-writable class

The service can create these roots beneath the existing sandbox storage tree without changing the top-level storage model drastically.

### Network policy model

Keep the existing API-level `NetworkMode` stable for now, but resolve it internally to a small policy object.

Suggested shape:

```go
type NetworkPolicy struct {
    Internet    bool
    LoopbackOnly bool
    AllowTunnels bool
}
```

Initial mapping:
- `internet-disabled` -> loopback-only, no egress
- `internet-enabled` -> controlled egress, loopback-only service exposure unless tunneled

This is intentionally smaller than a full firewall DSL.

### Snapshot validation flow

Snapshot restore should validate archive contents before extraction:
1. read metadata and enforce compatibility with runtime/profile/image contract
2. scan tar entries and reject unsafe paths, device nodes, hardlinks, and oversized expansion
3. enforce file-count and byte ceilings
4. normalize ownership and permissions
5. extract only into the service-owned workspace target

### Backend-aware quota enforcement

- QEMU
  - continue using bounded virtual disk images as the hard storage boundary
- Docker
  - use the strongest supported runtime/host quota mechanism available
  - where hard quotas are unavailable, rely on conservative create-time checks plus reconcile-time stop/degrade behavior and clear docs

This is honest about platform variance while still improving safety.

## Data and persistence

### SQLite changes

Prefer using existing tables first.

Possible additive fields if needed:
- `sandboxes.network_policy TEXT NOT NULL DEFAULT ''`
- `snapshots.storage_contract_version TEXT NOT NULL DEFAULT ''`

Existing `sandbox_storage` can remain the main accounting table, potentially with improved semantics around cache and snapshot totals.

### Config and environment

Keep additions small:
- optional storage-pressure thresholds for warnings vs. hard denial
- optional snapshot size/file-count ceilings
- optional Linux-specific network policy helpers documented for production hosts

Do not add a large external policy engine.

### Session and memory implications

None for chat/session state. Runtime storage measurement should stay bounded and interval-based to avoid excessive host CPU or RAM use.

## Interfaces and types

Possible additive internal types:

```go
type StorageClass string

const (
    StorageClassWorkspace StorageClass = "workspace"
    StorageClassCache     StorageClass = "cache"
    StorageClassScratch   StorageClass = "scratch"
    StorageClassSecrets   StorageClass = "secrets"
)
```

```go
type SnapshotValidationLimits struct {
    MaxBytes          int64
    MaxFiles          int
    MaxExpansionRatio int
}
```

These can remain internal to service/runtime code unless the API truly needs them.

## Failure modes and safeguards

- **snapshot path traversal**
  - reject before extraction
- **special files or hardlinks in archives**
  - reject as unsupported input
- **storage exhaustion on Docker hosts**
  - stop or degrade sandbox conservatively and surface explicit storage-pressure errors
- **network policy drift**
  - keep policy resolution centralized in service/runtime code
- **secrets leakage into snapshots**
  - isolate secrets mounts from workspace/snapshot roots by construction
- **excessive measurement cost**
  - keep storage scanning bounded and reuse existing reconcile cadence

## Testing strategy

- service tests in `internal/service/service_test.go`
  - storage-class root creation and snapshot validation rules
  - rejection of unsafe restore archives
- repository tests in `internal/repository/store_test.go`
  - storage-usage updates and any additive metadata round-trip
- runtime tests in `internal/runtime/docker/runtime_test.go`
  - mount layout and policy-driven network argument construction
- runtime tests in `internal/runtime/qemu/runtime_test.go`
  - disk sizing and storage layout behavior
- integration tests in `internal/api/integration_test.go`
  - tunnel and snapshot policy errors surface cleanly
- host-gated scripts
  - file-count abuse, disk-fill abuse, and restart/restore drills remain bounded and self-cleaning
