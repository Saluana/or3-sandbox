# Production Readiness Tasks

## 1. Stabilize the truth baseline [R1, R8, R9]

- [x] Audit [README.md](README.md) and `docs/` to ensure production guidance points to QEMU, not Docker, for untrusted workloads.
- [x] Make the runtime-positioning guidance explicit everywhere it matters: Docker for lower-cost trusted workloads, QEMU for stronger isolation.
- [x] Fix or isolate the current failing API integration path so production-readiness work starts from a trustworthy test baseline.
- [x] Update planning references so “enterprise-ready” claims are never made ahead of tests and drills.

## 2. Make QEMU the explicit production runtime path [R1, R5, R6]

- [x] Extend `internal/config/config.go` with a clear production mode or runtime policy that rejects unsafe production configurations.
- [x] Update `cmd/sandboxd/main.go` startup flow so production deployment clearly validates QEMU prerequisites and trusted-Docker exclusions.
- [x] Add operator-visible runtime inspection or status output showing whether a sandbox is running on `docker` or `qemu`.
- [x] Document the production-supported runtime boundary in `docs/` and [README.md](README.md).

## 3. Add enterprise authentication and authorization [R2]

- [x] Introduce an auth abstraction in `internal/auth/` that supports both development static tokens and a production identity backend.
- [x] Add production auth configuration in `internal/config/config.go` with strict startup validation.
- [x] Add support for service identities distinct from human operator identities.
- [x] Add authorization checks for lifecycle, file, snapshot, tunnel, and admin inspection actions.
- [x] Add tests for valid, invalid, and unauthorized access paths.

## 4. Add secure transport and secret handling [R3]

- [x] Add a supported TLS deployment path, either in-process or explicitly documented reverse-proxy mode, without weakening trust boundaries.
- [x] Add config validation for TLS or trusted-proxy settings in `internal/config/config.go`.
- [x] Review logging in `cmd/sandboxd`, `internal/auth`, `internal/service`, and runtime packages to ensure secrets are never logged.
- [x] Define a production secret-loading model for guest SSH material, auth credentials, and tunnel-sensitive data.
- [x] Add tests or smoke coverage for misconfigured TLS or secret paths.

## 5. Add policy controls for safe enterprise use [R4]

- [x] Add a small policy layer in `internal/service/service.go` or a nearby package for allowed images, tunnel restrictions, and production guardrails.
- [x] Add config or SQLite-backed policy storage only where it clearly improves operator control.
- [x] Enforce policy checks on create, start or resume, tunnel creation, and any sensitive admin operation.
- [x] Add clear audit events and user-visible error messages for policy denials.
- [x] Add focused tests for policy allow and deny flows.

## 6. Harden QEMU runtime reliability [R5, R6]

- [x] Add regression coverage for control-plane restart during running sandboxes, execs, and snapshots.
- [x] Strengthen `internal/runtime/qemu/` and `internal/service/service.go` handling for guest boot failure, degraded inspect state, and partial recovery.
- [x] Add stronger runtime-state visibility for booting, suspended, failed, and degraded guests.
- [x] Review suspend or resume, stop, and reconciliation interactions for long-running guest workloads.
- [x] Keep destructive cleanup conservative unless delete is explicit.

## 7. Tighten resource enforcement and observability [R6, R7]

- [x] Make the production documentation explicit about which resource boundaries are hard-enforced in QEMU mode and which are best-effort in Docker mode.
- [x] Add operator guidance for capacity planning so runtime choice reflects both memory cost and security needs.
- [x] Extend quota and health views to expose enough operator-facing storage, runtime, and capacity data for production operations.
- [x] Add metric-friendly counters or a scrape endpoint for sandbox counts, running state, exec failures, snapshots, and capacity pressure.
- [x] Verify disk-full and quota-pressure behavior with regression coverage.
- [x] Document capacity monitoring and quota interpretation in `docs/`.

## 8. Improve audit, logging, and incident response posture [R7]

- [x] Review audit event coverage in `internal/service/service.go` and ensure all privileged state transitions are recorded.
- [x] Standardize structured log fields across daemon, API, and service flows.
- [x] Add operator docs describing what to inspect during runtime, auth, storage, and snapshot incidents.
- [x] Ensure tunnel, snapshot, and auth-sensitive events include enough context without leaking secrets.

## 9. Ship enterprise operations runbooks [R8]

- [x] Add production deployment documentation covering host prerequisites, runtime setup, secrets, storage roots, and startup ordering.
- [x] Add backup and restore documentation covering SQLite, snapshot roots, and optional export bundles.
- [x] Add upgrade guidance covering database, snapshot, and guest image compatibility expectations.
- [x] Add incident runbooks for daemon crash, guest boot failure, disk exhaustion, expired credentials, and snapshot corruption.

## 10. Raise production test confidence [R9]

- [x] Ensure `go test` coverage for production-critical packages is stable and green in supported development environments.
- [x] Add a CI-friendly production smoke path for the most important non-host-specific flows.
- [x] Keep host integration coverage for QEMU workload claims and recovery drills, but document its prerequisites and intended environments.
- [x] Gate any future “enterprise-ready” docs language on passing tests and documented operator drills.

## 11. Out of scope for this phase

- [ ] Rewrite the control plane into a multi-service architecture.
- [ ] Replace SQLite with a distributed control-plane database by default.
- [ ] Add multiple new runtime backends before the QEMU production path is hardened.
- [ ] Turn the project into a generic cluster scheduler unrelated to the current single-node sandbox design.
