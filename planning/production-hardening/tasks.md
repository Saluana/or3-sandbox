# Tasks

## 1. Lock production to one safe default boundary (Req 1, 2)

- [ ] Tighten `internal/config/config.go` so `SANDBOX_MODE=production` defaults to `qemu-professional`, rejects `docker-dev` unless an explicit break-glass flag is set, and rejects dangerous default profile sets.
- [ ] Update `internal/service/policy.go` to enforce the same runtime/profile rules on create, start, inspect, and admin-inspection paths.
- [ ] Extend `internal/api/router.go`, `internal/model/model.go`, and any relevant inspect/runtime-info responses so operators can see runtime selection and runtime class clearly on create and inspect.
- [ ] Add regression coverage in `internal/config/config_test.go`, `internal/service/service_test.go`, and `internal/api/integration_test.go` for production mode denial and runtime-class visibility.
- [ ] Update `README.md`, `docs/configuration.md`, and `docs/runtimes.md` so production examples use the safe boundary only.

## 2. Replace wildcard RBAC with explicit production roles (Req 3, 4)

- [ ] Refactor `internal/auth/identity.go` to remove wildcard permission grants and define explicit role-to-permission mappings for `tenant-admin`, `tenant-developer`, `tenant-viewer`, `operator`, and `service-account`.
- [ ] Review every `requirePermission` use in `internal/api/router.go` and align route coverage with the new production roles.
- [ ] Extend `internal/auth/authenticator.go` JWT claims handling for service-account identity, scope, expiry, and revocation-aware checks.
- [ ] Add additive persistence in `internal/db/db.go` and `internal/repository/store.go` for service-account records and lookups if runtime revocation cannot be represented from JWT claims alone.
- [ ] Add tests in `internal/auth/authenticator_test.go`, `internal/api/integration_test.go`, and repository/DB tests to prove least-privilege behavior and prevent endpoint-permission drift.

## 3. Make TLS mandatory for production transport (Req 4)

- [ ] Add an explicit production transport mode in `internal/config/config.go` to distinguish direct TLS from trusted TLS termination.
- [ ] Make `cmd/sandboxd/main.go` fail startup for plaintext production serving paths.
- [ ] Extend `cmd/sandboxctl/doctor.go` to validate direct-TLS and terminated-proxy production posture, including secret/file readability and HTTPS operator host requirements.
- [ ] Add tests in `internal/config/config_test.go` and CLI tests for production TLS pass/fail cases.
- [ ] Update `docs/operations/production-deployment.md` and `docs/configuration.md` with the supported production TLS patterns and break-glass expectations.

## 4. Add a promoted-image registry and enforcement path (Req 5)

- [ ] Add additive SQLite schema in `internal/db/db.go` for promoted guest images and any supporting indexes.
- [ ] Extend `internal/repository/store.go` with insert/list/get/update helpers for image verification and promotion status.
- [ ] Reuse `internal/guestimage/contract.go` to validate sidecars and hashes before a promotion record is written.
- [ ] Update `internal/service/policy.go` and relevant create/restore paths in `internal/service/service.go` so production QEMU workloads reject unpromoted images.
- [ ] Add CLI/operator flows in `cmd/sandboxctl` for image verification and promotion, with tests and audit events.

## 5. Turn the shipped drills into a release gate with evidence (Req 6)

- [ ] Add a small release-gate workflow in `cmd/sandboxctl` or `scripts/` that runs `scripts/production-smoke.sh`, `scripts/qemu-host-verification.sh`, `scripts/qemu-production-smoke.sh`, and `scripts/qemu-recovery-drill.sh` in a predictable order.
- [ ] Add additive SQLite or bounded artifact-manifest recording for release evidence in `internal/db/db.go` and `internal/repository/store.go`.
- [ ] Record gate metadata such as host fingerprint, runtime selection, image/profile, timestamps, outcome, and artifact location.
- [ ] Publish a supported host matrix in `docs/operations/verification.md` or a sibling doc sourced from actual evidence, not assumptions.
- [ ] Update release/readiness docs so “production-ready” claims depend on fresh gate evidence.

## 6. Close runtime enforcement gaps and automate hardened defaults (Req 7)

- [ ] Review `internal/service` admission policy and runtime manager behavior to confirm CPU, memory, disk, temp-space, PID, stdout, and file-count limits have real enforcement or conservative failure behavior.
- [ ] Make trusted Docker hardening defaults automatic in config/runtime wiring: seccomp, AppArmor, and SELinux when supported, with clear warnings when unavailable.
- [ ] Add or tighten abuse-path assertions in `scripts/qemu-resource-abuse.sh` and corresponding tests under `internal/service` and runtime packages.
- [ ] Add deployment-profile defaults in `internal/config/config.go` so production profiles enable hardening automatically.
- [ ] Update docs and runbooks for resource-abuse expectations and operator response.

## 7. Harden browser tunnel capability issuance and revocation (Req 8)

- [ ] Review `internal/api/router.go` tunnel signed-URL and browser-cookie flows and shorten production-default TTLs while preserving bounded max TTL enforcement.
- [ ] Add optional one-time or nonce-backed capability records using additive persistence in `internal/db/db.go` and `internal/repository/store.go` when stronger browser-session revocation is enabled.
- [ ] Extend `internal/service/service.go` revoke flows so tunnel revoke invalidates outstanding browser capabilities deterministically.
- [ ] Add integration coverage in `internal/api/integration_test.go` for TTL bounds, one-time use, revocation, origin/cookie behavior, and WebSocket access.
- [ ] Update `docs/operations/tunnel-abuse.md` and related incident docs with the new browser tunnel threat model and defaults.

## 8. Prove restore, recovery, and snapshot compatibility end to end (Req 9)

- [ ] Extend snapshot metadata in `internal/db/db.go` and `internal/repository/store.go` only as needed to record bundle integrity and restore compatibility information.
- [ ] Add integrity verification for snapshot bundles and promoted images before restore in `internal/service/service.go`.
- [ ] Expand `scripts/qemu-recovery-drill.sh` and related verification docs to cover clean-host restore, corrupted snapshot behavior, and upgrade-window restore checks.
- [ ] Add regression tests in `internal/service/service_test.go` and DB/repository tests for restore compatibility and conservative failure behavior.
- [ ] Document explicit RPO/RTO expectations and supported upgrade-restore windows in `docs/operations/backup-and-restore.md` and `docs/operations/upgrades.md`.

## 9. Ship one operator bootstrap and config-lint workflow (Req 10, 12)

- [ ] Extend `cmd/sandboxctl/doctor.go` with a single production readiness report that covers runtime, auth/TLS, directories, guest-image policy, and host prerequisites.
- [ ] Add a config-lint path that reuses `internal/config/config.go` validation without starting `sandboxd`.
- [ ] Introduce bounded deployment profiles in `internal/config/config.go`, such as `dev-trusted-docker`, `production-qemu-core`, `production-qemu-browser`, and `exception-container`.
- [ ] Document profile precedence and override rules so operators can still use env vars intentionally without stumbling into unsafe combinations.
- [ ] Update `docs/setup.md`, `docs/configuration.md`, and `docs/operations/production-deployment.md` to describe one supported bootstrap flow.

## 10. Raise observability from useful to operational (Req 11)

- [ ] Review `internal/service/observability.go`, `internal/service/audit.go`, and `internal/api/router.go` to ensure launch-critical signals exist for degraded sandboxes, failed runtime inspections, storage pressure, tunnel churn, noisy tenants, release-gate freshness, and image promotion posture.
- [ ] Add only the missing counters/fields needed for those signals, preserving the current health/capacity/metrics surfaces.
- [ ] Add operator runbooks under `docs/operations/` for each launch-critical alert or degraded state that lacks one.
- [ ] Define audit retention and query expectations for investigations using the current SQLite audit tables.
- [ ] Update `docs/api-reference.md` and operations docs so operators know which endpoints or commands back each operational signal.

## 11. Publish a visible test matrix and map claims to evidence (Req 6, 11, 12)

- [ ] Add a production test matrix to `README.md`, `docs/README.md`, or `docs/operations/verification.md` covering API flows, Docker trusted-runtime flows, QEMU host-gated flows, abuse drills, recovery drills, and restore drills.
- [ ] Map each production claim in docs to a test, host-gated verification, or operator drill.
- [ ] Add coverage goals or high-risk code ownership notes for `internal/config`, `internal/auth`, `internal/service`, `internal/api`, and QEMU runtime surfaces.
- [ ] Keep the matrix aligned with the actual scripts and test entry points already present in the repo.

## 12. Tighten dangerous-profile governance and simplify the config surface (Req 2, 12)

- [ ] Add explicit dangerous-profile approval controls in `internal/service/policy.go` and related request models so `container` and `debug` require tenant-level approval plus an audit reason.
- [ ] Record dangerous-profile exceptions in audit events and make them visible in observability/reporting surfaces.
- [ ] Ensure deployment profiles keep dangerous-profile usage in clearly named exception modes instead of ordinary defaults.
- [ ] Update shipped examples and docs so dangerous profiles are described as exception workflows, not ordinary profile choices.
- [ ] Add regression tests for accidental dangerous-profile enablement in production.

## 13. Out of scope

- [ ] Do not add a distributed policy service, external authz engine, or multi-node control plane.
- [ ] Do not replace SQLite with a server database for promotion records, release evidence, or service-account state.
- [ ] Do not broaden hostile-production claims to the Docker runtime.
- [ ] Do not create an open-ended matrix of deployment combinations when a bounded set of supported deployment profiles will do.
