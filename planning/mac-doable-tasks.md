# Mac-doable task list

This checklist includes remaining implementation work that can be done from macOS. Some items, especially Linux hardening, QEMU, quota, and abuse/recovery work, should still get Linux validation later.

## Phase 1: boundary and immediate risk reduction

### Trusted Docker hardening

Design: [trusted-docker-hardening/design.md](trusted-docker-hardening/design.md)

- [x] Update `internal/runtime/docker/runtime.go` so the normal create path runs as an explicit non-root user.
- [x] Add `cap-drop=ALL`, `no-new-privileges`, and `read-only` rootfs defaults to the Docker create arguments.
- [x] Add a bounded tmpfs mount for `/tmp` and keep writable bind mounts limited to service-owned workspace and cache paths.
- [x] Keep canonical host-path validation for all bind mounts and reject invalid or empty mount roots.
- [x] Add optional seccomp profile support in `internal/runtime/docker/runtime.go` and `internal/config/config.go`.
- [x] Add optional AppArmor or SELinux integration using simple config values rather than a new policy engine.
- [x] Ensure the runtime emits clear warnings or test-visible behavior when a host cannot apply a requested Linux hardening primitive.
- [x] Update `internal/service/policy.go` to reject privileged-mode-equivalent requests and any host namespace sharing requests.
- [x] Ensure the trusted Docker path never mounts the Docker socket by default.
- [x] If any elevated Docker override is still needed, model it through a tiny explicit capability list and audit it in `internal/service/audit.go`.
- [x] Add service tests in `internal/service/service_test.go` covering default denial and explicit override audit behavior.
- [x] Review the curated Docker image path used by this repo and ensure it defines a predictable non-root user.
- [x] Update image docs or manifests so the runtime can rely on that user consistently.
- [x] Add regression tests or smoke checks that a default sandbox can still boot and exec under the non-root user.
- [x] Expand `internal/runtime/docker/runtime_test.go` to assert the hardened Docker arguments.
- [x] Add tests for read-only rootfs plus writable mount layout.
- [x] Add tests for capability add-back behavior if any explicit override path exists.
- [x] Add tests that privileged mode and host namespace requests are rejected.
- [x] Update `docs/runtimes.md` to describe the hardened `trusted-docker` posture and its limitations.
- [x] Update `docs/operations/dangerous-profile-misuse.md` or a Docker-focused runbook with operator guidance for elevated overrides.
- [x] Document which controls are Linux-enforced versus best-effort on local developer hosts.

## Phase 2: reduce weight and attack surface

### Lightweight image profiles

Design: [lightweight-image-profiles/design.md](lightweight-image-profiles/design.md)

- [x] Update `internal/service/service.go` and related helpers so profile validation follows the same policy semantics for Docker and QEMU.
- [x] Change the default `SANDBOX_BASE_IMAGE` posture in `internal/config/config.go` away from the broad Playwright image.
- [x] Build or document a smaller curated `core` or `runtime` Docker image in `images/base/Dockerfile` or adjacent build assets.
- [x] Verify a default sandbox can still create, start, and exec using the smaller image.
- [x] Update docs in `docs/configuration.md` and `docs/usage.md` to reflect the new default.
- [x] Keep browser tooling only in `browser` images and examples.
- [x] Remove Docker-in-container from normal curated images and keep it only in the `container` profile.
- [x] Update `examples/openclaw`, `examples/playwright`, and any other presets to request the smallest required profile explicitly.
- [x] Define lightweight Docker image metadata using OCI labels or a small curated mapping in Go.
- [x] Extend the Docker runtime or service validation path to load that metadata before sandbox creation.
- [x] Reject sandbox-create requests when requested profile, image metadata, and dangerous-profile policy do not align.
- [x] Update `internal/service/policy.go` so dangerous profiles such as `container` and `debug` are blocked by default in production.
- [x] Strengthen `SANDBOX_POLICY_ALLOWED_IMAGES` usage in docs and tests so production examples prefer pinned curated image refs.
- [x] Add config tests that cover minimal default images and allowed-image restriction behavior.
- [x] Add service tests for profile mismatch, missing metadata, and dangerous-profile denial.
- [x] Add Docker runtime tests for image metadata parsing or curated mapping.
- [x] Add preset tests ensuring browser and container examples do not fall back to the default lightweight image by mistake.
- [x] Update `docs/project-overview.md`, `docs/configuration.md`, and `docs/usage.md` with the curated image/profile story.
- [x] Update example READMEs so they explain why a given example needs `browser` or `container`.
- [x] Document the operator process for rebuilding and rolling curated images without silent drift.

## Phase 3: tighten persistence and exposure controls

### Storage, network, and snapshot hardening

Design: [storage-network-snapshot-hardening/design.md](storage-network-snapshot-hardening/design.md)

- [x] Add small internal storage-class definitions in `internal/model` or `internal/service`.
- [x] Update `internal/service/service.go` to create distinct roots for durable workspace, cache, scratch, and any operator-managed secrets material.
- [x] Update runtime specs so Docker and QEMU can map those roots consistently.
- [x] Document durability and snapshot inclusion rules in `docs/tutorials/files-and-tunnels.md` and related docs.
- [x] Ensure secrets material is mounted or exposed separately from workspace and is excluded from snapshot/export flows.
- [x] Add service tests covering class separation and snapshot exclusion behavior.
- [x] Review QEMU disk sizing in `internal/runtime/qemu` and document it as the hard storage boundary for VM-backed sandboxes.
- [x] Add the strongest supported Docker-side quota enforcement available in `internal/runtime/docker/runtime.go` without breaking trusted local dev workflows.
- [x] Extend storage measurement and reconcile logic in `internal/service/service.go` and `internal/repository/store.go` to detect both byte growth and file-count/inode pressure.
- [x] Add a small internal network-policy resolver that maps existing `NetworkMode` values to concrete behavior.
- [x] Update Docker and QEMU runtime wiring to keep sandbox services loopback-only unless published via the tunnel control plane.
- [x] Keep current API request shapes stable while making egress and exposure policy more explicit under the hood.
- [x] Add audit coverage for exposure changes and policy denials in `internal/service/audit.go` and related tests.
- [x] Reject path traversal, unsupported special files, dangerous links, and oversized expansion ratios.
- [x] Normalize ownership and permissions on restore.
- [x] Persist enough metadata on snapshots to reject incompatible profile/runtime restores where needed.
- [x] Add regression tests for malformed and hostile archive inputs.
- [x] Add targeted tests or scripts for Docker trusted-mode storage pressure where hard quotas are not available.
- [x] Verify tunnel exposure remains loopback-only unless explicitly published.
- [x] Add snapshot failure and partial-restore drills to the operations scripts or docs.
- [x] Update `docs/operations/snapshot-failed.md` with restore validation and cleanup behavior.
- [x] Update `docs/tutorials/files-and-tunnels.md` with storage classes, durability rules, and tunnel exposure rules.
- [x] Update `docs/configuration.md` with any new snapshot or storage-pressure limits.
- [x] Make platform-specific limits explicit, especially for Docker on non-Linux hosts.

## Phase 4: prove production behavior

### Production ops hardening

Design: [production-ops-hardening/design.md](production-ops-hardening/design.md)

- [ ] Update `docs/operations/production-deployment.md` and `docs/architecture.md` to state that the production isolation unit is one sandbox per VM-backed workload.
- [ ] Ensure runtime docs do not imply multi-sandbox sharing of one VM as a normal production shape.
- [ ] Align quota and restart documentation with sandbox-level isolation and tenant-level limits.
- [ ] Add deterministic admission checks in `internal/service/service.go` for create/start operations based on current usage and pressure.
- [ ] Extend existing capacity or usage calculations to include node-pressure signals that matter for this repo.
- [ ] Add small per-tenant concurrent-start or heavy-operation limits in config and service policy if current quotas are insufficient.
- [ ] Add service tests proving a bursty tenant cannot monopolize starts when limits are configured.
- [ ] Extend `scripts/qemu-recovery-drill.sh` to cover daemon restart, orphan cleanup, tunnel recovery, and partial snapshot failures.
- [ ] Extend `scripts/qemu-resource-abuse.sh` with startup-spam and fairness scenarios in addition to OOM, PID, disk, and stdout pressure.
- [ ] Update release guidance so runtime-affecting changes run the bounded abuse and recovery scripts before production signoff.
- [ ] Review `internal/service/observability.go` and add only the counters needed for lifecycle failures, storage pressure, OOM, PID exhaustion, snapshot operations, exec/TTY attach, and tunnel exposure changes.
- [ ] Extend `internal/service/audit.go` so dangerous profile use, elevated capabilities, and admission denials are easy to query.
- [ ] Update operator docs with rebuild cadence, digest pinning expectations, and approval workflow for curated images.
- [ ] Update `docs/operations/verification.md`, `docs/operations/daemon-restart-recovery.md`, `docs/operations/host-disk-full.md`, and `docs/operations/incidents.md` with the new recovery and admission behavior.
- [ ] Ensure the runbooks point to actual metrics, health endpoints, and audit evidence already exposed by the repo.
- [ ] Document what remains manual versus automatic during host or daemon recovery.
