# Design

## Overview

The repo already has many of the right operational surfaces:
- quota enforcement in `internal/service`
- runtime health, capacity, and metrics endpoints
- audit logging
- reconcile loops in `cmd/sandboxd`
- operator scripts for QEMU smoke, abuse, and recovery

The lightweight design is to strengthen those existing surfaces rather than introducing a new scheduler or observability stack.

## Affected areas

- `internal/service/service.go`
  - add deterministic admission checks for create/start pressure and better recovery handling
- `internal/service/policy.go`
  - centralize fairness and dangerous-operation denials
- `internal/service/observability.go`
  - extend metrics and reporting for security/abuse signals
- `internal/service/audit.go`
  - record elevated capability use, tunnel exposure changes, and admission denials
- `internal/api/router.go`
  - continue exposing health, capacity, metrics, and audit-relevant errors cleanly
- `internal/repository/store.go`
  - persist any small additive state needed for admission counts or recovery markers
- `internal/db/db.go`
  - add schema only if a tiny amount of durable admission or recovery state is justified
- `cmd/sandboxd/main.go`
  - keep reconcile loop behavior but extend restart recovery drills and logging expectations
- `cmd/sandboxctl/doctor.go`
  - add operator-facing checks relevant to image policy and runtime recovery posture where useful
- `scripts/qemu-production-smoke.sh`
  - include release-gate checks for recovery and abuse readiness
- `scripts/qemu-recovery-drill.sh`
  - expand restart, orphan cleanup, and partial-failure drills
- `scripts/qemu-resource-abuse.sh`
  - expand admission and abuse scenarios
- `docs/operations/*`
  - update runbooks and release guidance

## Control flow / architecture

### Admission control

Keep admission local and synchronous.

Suggested flow:
1. sandbox create/start request enters `service.Service`
2. service reads quota and current usage from SQLite
3. service gathers current runtime capacity or storage pressure view
4. a small admission function returns allow or deny, optionally with a retryable signal
5. denial is audited and surfaced through existing API errors

No external queue is required for the first wave.

### Tenant fairness

The current quota model already limits counts. Extend it slightly with pressure-sensitive checks such as:
- current concurrent starts per tenant
- current running sandboxes versus headroom
- storage-pressure-based denial when a tenant is already near limits

These can be derived from existing persisted state plus small additive counters if needed.

### Recovery model

Reconciliation should be explicitly tested for:
- daemon restart while sandboxes are running
- create/start interrupted after persistence but before runtime success
- stop/delete interrupted after runtime change but before DB update
- orphaned storage or runtime artifacts

The service should prefer conservative cleanup and degraded state reporting over guessing success.

### Telemetry model

Reuse existing endpoints:
- `/v1/runtime/health`
- `/v1/runtime/capacity`
- `/metrics`
- audit events

Extend them with a small number of signals tied to real decisions and failures, not verbose event spam.

## Data and persistence

### SQLite changes

Try to avoid new tables.

Possible small additions if needed:
- transient-operation counters or timestamps on sandboxes
- last admission denial reason for debugging

Prefer derivation from existing state first.

### Config and environment

Possible additive config values:
- max concurrent starts per tenant
- node-level storage-pressure denial threshold
- optional startup queue limit if a tiny in-process queue is justified

Keep defaults conservative and bounded.

### Session and memory implications

None for chat/session data. In-process admission tracking must remain small and reset safely after daemon restart.

## Interfaces and types

Possible internal types:

```go
type AdmissionDecision struct {
    Allowed bool
    Reason  string
    Retry   bool
}
```

```go
type PressureSnapshot struct {
    RunningSandboxes int
    StoragePressure  bool
    MemoryPressure   bool
}
```

These should remain internal to `internal/service`.

## Failure modes and safeguards

- **restart during create/start**
  - reconcile should mark sandboxes degraded or complete cleanup, not leave ambiguous state silently
- **admission false positives**
  - keep checks simple and deterministic, with clear denial messages
- **metric spam**
  - record counters for meaningful events only
- **secret leakage in telemetry**
  - continue limiting previews and never emit raw secret values
- **image policy bypass**
  - validate before runtime create, not after a guest has already started

## Testing strategy

- service tests in `internal/service/service_test.go`
  - admission denials for pressure and per-tenant fairness
  - restart reconciliation behavior for partial lifecycle failures
- repository tests in `internal/repository/store_test.go`
  - any additive admission-related state round-trips cleanly
- API integration tests in `internal/api/integration_test.go`
  - health/capacity/metrics and denial paths remain consistent
- script validation
  - update `scripts/qemu-resource-abuse.sh` and `scripts/qemu-recovery-drill.sh` as release gates
- operator-doc verification
  - ensure runbooks match actual error messages, metrics, and audit events
