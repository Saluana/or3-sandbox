# Production Readiness Requirements

## 1. Overview

This plan defines the work needed to move `or3-sandbox` from a credible single-node sandbox platform into a production-grade, enterprise-ready system for running untrusted code.

The plan is grounded in the current repository state:

- Go daemon and CLI entry points already exist in `cmd/sandboxd` and `cmd/sandboxctl`
- SQLite is the current system of record
- Docker remains an explicitly trusted or development-only backend
- QEMU is the real production-oriented isolation path
- the core lifecycle, files, exec, TTY, tunnels, snapshots, and reconciliation flows already exist

Scope for this phase:

- strengthen the guest-backed production path rather than broadening the Docker path
- add enterprise security, auth, policy, and operations controls that fit the current Go service
- preserve the current single-node control-plane shape unless the work clearly requires an explicit architectural expansion
- tighten deployment, recovery, audit, and test confidence to the level expected for production use

Assumptions:

- “production-grade untrusted-code sandbox” means QEMU-backed operation is the primary supported production path
- Docker remains available only as a trusted or development mode
- Docker is the lower-cost density option for trusted internal workloads, while QEMU is the higher-cost isolation option for untrusted or security-sensitive workloads
- enterprise readiness does not automatically imply a Kubernetes or multi-node rewrite in this phase

## 2. Requirements

### 2.1 Requirement 1: Production runtime mode must have an explicit supported boundary

The product must clearly define which runtime is supported for untrusted production workloads and enforce that boundary in configuration, docs, and operator flows.

Acceptance criteria:

1. Production guidance and docs state that QEMU is the supported production isolation path.
2. Docker remains supported only as an explicitly trusted mode.
3. Production deployment docs do not describe Docker as an acceptable hostile multi-tenant boundary.
4. Runtime health and startup validation fail fast when the selected production path is not correctly configured.
5. The control plane exposes enough runtime state for operators to confirm that a sandbox is using the intended backend.
6. Operator guidance explains the practical tradeoff clearly: use Docker when cost and density matter more than isolation, and use QEMU when security boundary strength matters more than density.

### 2.2 Requirement 2: Enterprise authentication and authorization must replace static-token-only posture

The control plane must support production identity and access controls beyond static bearer tokens configured directly in process environment.

Acceptance criteria:

1. The system supports an enterprise identity source suitable for operators and automation, such as OIDC-backed bearer validation or another repo-aligned equivalent.
2. The system supports service identities distinct from human operator identities.
3. The system supports role- or policy-based authorization for at least sandbox lifecycle, file access, snapshot actions, tunnel actions, and administrative inspection.
4. Token or credential rotation can occur without manual SQLite surgery or unsafe restarts.
5. Existing static-token flows remain available only as a compatibility or development mode if retained.

### 2.3 Requirement 3: Transport and secret handling must be secure by default

Production deployments must protect API traffic, tunnel secrets, guest SSH material, and operator credentials with an explicit secure handling model.

Acceptance criteria:

1. The daemon can be deployed with TLS in a supported production configuration.
2. Production docs define whether TLS terminates in-process or at a trusted reverse proxy and state the required headers and trust boundaries.
3. Guest SSH private keys, tunnel access material, and enterprise auth secrets are not logged or exposed in API responses.
4. Secret storage and loading have a documented production path, such as file-based secrets, OS keychain integration, or external secret manager integration appropriate to this repo.
5. Rotating secrets does not require modifying historical sandbox records in SQLite.

### 2.4 Requirement 4: Policy controls must exist for safe multi-tenant operation

The system must support operator-defined policy controls for what tenants can run, expose, and persist.

Acceptance criteria:

1. Operators can restrict allowed base images or guest image families.
2. Operators can restrict network exposure, tunnel use, or both by policy in addition to per-tenant quota values.
3. Operators can set maximum sandbox lifetime, idle timeout, or similar bounded-runtime policy.
4. Operators can restrict dangerous capabilities in a repo-aligned way without introducing a large policy engine unrelated to the current architecture.
5. Policy violations are surfaced as clear API and CLI errors and are audit-visible.

### 2.5 Requirement 5: Runtime reliability and recovery behavior must be production-safe

The guest-backed path must behave predictably during failures, restarts, and partial operations.

Acceptance criteria:

1. Control-plane restart during running sandboxes, active execs, active TTY sessions, and snapshot operations has a documented and tested outcome.
2. Guest boot failure, readiness timeout, disk-full, and partial snapshot failure all have deterministic recovery behavior.
3. Reconciliation distinguishes stopped, suspended, booting, running, failed, and missing guests conservatively.
4. Recovery actions avoid silent data loss and avoid deleting runtime artifacts automatically outside explicit delete flows.
5. Operators can inspect degraded state through the existing API or a small extension of it.

### 2.6 Requirement 6: Resource boundaries must be enforceable and observable

Production-grade operation must provide stronger resource enforcement and more reliable operator visibility into real usage.

Acceptance criteria:

1. Guest-backed sandboxes enforce real disk boundaries for root and workspace storage.
2. CPU, memory, process count, storage, and tunnel usage are visible through operator and tenant inspection paths.
3. Actual storage measurements remain persisted in SQLite and reflected in quota views.
4. Policies and quotas clearly describe what is enforced in QEMU mode versus what remains best-effort in trusted Docker mode.
5. Disk-full and quota-exceeded behaviors are regression-tested.

### 2.7 Requirement 7: Enterprise observability and audit must be first-class

Production use must provide reliable audit trails and actionable system health signals.

Acceptance criteria:

1. All privileged actions and tenant-facing state changes are audit-recorded with stable fields and timestamps.
2. Logs include enough structured metadata for incident response without leaking secrets.
3. Health inspection covers daemon health, runtime backend health, and guest readiness state.
4. Metrics or metric-friendly counters are exposed through a repo-aligned mechanism suitable for production scraping.
5. Docs explain how operators monitor capacity, quota pressure, snapshot growth, and runtime failures.

### 2.8 Requirement 8: Enterprise operations must have a supported runbook

Operators need a clear, repeatable path to deploy, upgrade, back up, restore, and recover the service.

Acceptance criteria:

1. Production deployment docs cover host prerequisites, directory layout, secrets, runtime selection, and safe startup ordering.
2. Upgrade docs cover database compatibility, snapshot compatibility, and guest image compatibility expectations.
3. Backup and restore docs cover SQLite, snapshot artifacts, and optional exported snapshot bundles.
4. Failure recovery docs cover the highest-risk incidents: lost daemon process, broken guest image, expired credentials, snapshot corruption, and disk exhaustion.
5. The project ships at least one production-oriented runbook or operations guide written for real operators, not only developers.

### 2.9 Requirement 9: Test confidence must support production claims

The system must not claim enterprise readiness without a test and verification story that covers the real production path.

Acceptance criteria:

1. The directly affected package tests and core API tests pass in supported development environments.
2. The QEMU production path has automated coverage for lifecycle, files, exec, TTY, suspend or resume, snapshots, recovery, and workload claims.
3. At least one stable CI-friendly verification path exists for production-critical flows, even if host integration tests remain gated by environment.
4. Known failing tests are either fixed or clearly isolated from production claims.
5. README and production docs only claim what the tests and drills actually prove.

## 3. Non-functional constraints

- Keep the design simple, bounded, and low-RAM.
- Preserve the current Go and SQLite control-plane shape unless the plan explicitly calls out an expansion.
- Prefer small extensions to existing packages over adding large new frameworks or services.
- Preserve compatibility for existing sandbox records, snapshots, quotas, and CLI usage where practical.
- Maintain secure-by-default behavior for file access, network exposure, tunnels, and secrets.
- Keep enterprise features comprehensible to operators without turning the repo into a platform that requires a large control-plane rewrite.
