# Tasks

## 1. Clarify sandbox-versus-tenant isolation policy (Req 1, 8)

- [ ] Update `docs/operations/production-deployment.md` and `docs/architecture.md` to state that the production isolation unit is one sandbox per VM-backed workload.
- [ ] Ensure runtime docs do not imply multi-sandbox sharing of one VM as a normal production shape.
- [ ] Align quota and restart documentation with sandbox-level isolation and tenant-level limits.

## 2. Add lightweight admission control in service code (Req 2, 3, 8)

- [ ] Add deterministic admission checks in `internal/service/service.go` for create/start operations based on current usage and pressure.
- [ ] Extend existing capacity or usage calculations to include node-pressure signals that matter for this repo.
- [ ] Add clear, auditable denial reasons rather than generic capacity failures.
- [ ] Keep the first implementation synchronous and local; do not add a distributed queue.

## 3. Tighten per-tenant fairness (Req 2, 3, 8)

- [ ] Add small per-tenant concurrent-start or heavy-operation limits in config and service policy if current quotas are insufficient.
- [ ] Record admission denials and retryable pressure events in `internal/service/audit.go`.
- [ ] Add service tests proving a bursty tenant cannot monopolize starts when limits are configured.

## 4. Harden restart recovery and orphan cleanup (Req 4, 8)

- [ ] Review and extend reconcile behavior in `internal/service/service.go` for partial create, partial delete, and runtime-crash cases.
- [ ] Add conservative orphan cleanup rules for runtime artifacts and storage roots where ownership is clear.
- [ ] Extend `scripts/qemu-recovery-drill.sh` to cover daemon restart, orphan cleanup, tunnel recovery, and partial snapshot failures.
- [ ] Add regression tests for reconcile behavior after interrupted lifecycle operations.

## 5. Expand abuse testing and release gates (Req 5, 8)

- [ ] Extend `scripts/qemu-resource-abuse.sh` with startup-spam and fairness scenarios in addition to OOM, PID, disk, and stdout pressure.
- [ ] Update release guidance so runtime-affecting changes run the bounded abuse and recovery scripts before production signoff.
- [ ] Keep all scripts self-cleaning and explicit about host prerequisites.

## 6. Promote security telemetry and audit coverage (Req 5, 6, 8)

- [ ] Review `internal/service/observability.go` and add only the counters needed for lifecycle failures, storage pressure, OOM, PID exhaustion, snapshot operations, exec/TTY attach, and tunnel exposure changes.
- [ ] Extend `internal/service/audit.go` so dangerous profile use, elevated capabilities, and admission denials are easy to query.
- [ ] Update API integration tests for metrics, health, and audit-relevant denial behavior where appropriate.

## 7. Tighten image supply-chain policy in the control plane (Req 6, 7, 8)

- [ ] Strengthen allowed-image policy handling in `internal/config/config.go` and `internal/service/policy.go` so production flows prefer curated pinned images.
- [ ] Surface image metadata validation failures before runtime create.
- [ ] Update operator docs with rebuild cadence, digest pinning expectations, and approval workflow for curated images.

## 8. Update operator docs and drills (Req 1, 4, 5, 6, 7)

- [ ] Update `docs/operations/verification.md`, `docs/operations/daemon-restart-recovery.md`, `docs/operations/host-disk-full.md`, and `docs/operations/incidents.md` with the new recovery and admission behavior.
- [ ] Ensure the runbooks point to actual metrics, health endpoints, and audit evidence already exposed by the repo.
- [ ] Document what remains manual versus automatic during host or daemon recovery.

## 9. Out of scope for this plan

- [ ] Do not introduce a distributed scheduler or external queue broker.
- [ ] Do not add a separate telemetry platform as a prerequisite.
- [ ] Do not turn one VM into a general multi-tenant pod host.
- [ ] Do not rely on manual operator knowledge instead of explicit audit and health signals.
