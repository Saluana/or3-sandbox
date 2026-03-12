# Requirements

## Overview

This plan turns the current “promising serious sandbox project” into a production-hardening program for the existing single-node Go control plane. It keeps the current architecture intact: `sandboxd` remains the single process, SQLite remains the only database, and the runtime model continues to prefer `qemu` for hostile-production isolation while preserving the trusted Docker path for development and explicitly trusted workloads.

Scope:

- lock `production` mode to one safe default story
- replace coarse auth and transport posture with explicit production policy
- formalize guest-image promotion and release evidence
- prove runtime enforcement, restore/recovery, and browser tunnel posture
- reduce operator error by shipping bootstrap, linting, and safer deployment profiles

Assumptions:

- no new distributed control plane, PKI service, or external policy engine is introduced
- new persistence remains additive and SQLite-compatible
- current runtime selection names, snapshot metadata, and tenant/session behavior remain backward-compatible unless an explicit migration path is defined
- “mandatory TLS in production” means no plaintext production serving path; direct TLS and explicitly trusted TLS termination are both acceptable only when the daemon can prove it is behind a TLS boundary

## Requirements

1. **Production mode must fail closed onto a VM-backed runtime class.**
   - Acceptance criteria:
     - `SANDBOX_MODE=production` rejects `docker-dev` as the default runtime selection unless an explicit break-glass flag is set.
     - the production default runtime selection resolves to a VM-backed runtime class, with `qemu-professional` as the default supported path.
     - production config validation rejects dangerous default profile sets that include `container` or `debug`.
     - runtime info and sandbox inspect responses expose the resolved runtime selection and runtime class clearly enough for operators to verify the chosen isolation boundary.

2. **Production defaults must prefer the smallest safe guest profile set.**
   - Acceptance criteria:
     - the production-default allowed guest profiles are limited to safe profiles such as `core`, `runtime`, and optionally `browser` when explicitly enabled.
     - `container` and `debug` remain blocked by default policy in production.
     - dangerous profile enablement requires explicit operator opt-in and is recorded in audit events.
     - shipped operator docs and deployment examples use the safe production profile set rather than the permissive development defaults.

3. **Authorization must move from wildcard roles to an explicit reviewed permission map.**
   - Acceptance criteria:
     - `internal/auth` no longer uses wildcard role grants for normal production roles.
     - the supported production role set includes at least `tenant-admin`, `tenant-developer`, `tenant-viewer`, `operator`, and `service-account`.
     - each API route is mapped to one or more explicit permissions, and the route-to-permission matrix is documented or test-covered.
     - a regression test fails if a new production endpoint is added without a reviewed permission requirement.

4. **Production credentials must support scoped service accounts and TLS-backed transport only.**
   - Acceptance criteria:
     - production startup fails unless the daemon is configured for direct TLS or an explicitly trusted TLS-terminated deployment mode.
     - service accounts support explicit scopes, tenant binding, expiration, and revocation.
     - production credentials are short-lived by default, and non-expiring service-account credentials require an explicit break-glass path.
     - secrets and signing material remain file-backed or operator-supplied and are never printed in logs, doctor output, or readiness reports.

5. **Guest-image selection must be driven by a formal promotion workflow.**
   - Acceptance criteria:
     - production-eligible guest images have recorded image hash, sidecar contract validation result, provenance fields, verification status, and promotion status.
     - production policy refuses unpromoted QEMU images even if they exist on disk and have a syntactically valid sidecar contract.
     - image promotion records are stored in one operator-facing source of truth that can be queried deterministically.
     - image promotion and rejection events are auditable.

6. **Release claims must be gated by repeatable smoke, verification, and recovery evidence.**
   - Acceptance criteria:
     - the release gate runs the existing fast smoke path plus `qemu-host-verification`, `qemu-production-smoke`, and `qemu-recovery-drill` before a build is called production-ready.
     - gate results are captured as bounded artifacts that include command, host identity, image/profile under test, start/end time, and pass/fail outcome.
     - operator docs publish a supported host matrix derived from actual gate evidence rather than assumptions.
     - a production tag or release note template cannot claim production-ready status without fresh gate evidence.

7. **Admission limits must line up with real runtime enforcement and hardened defaults.**
   - Acceptance criteria:
     - CPU, memory, disk, temp-space, PID, stdout, and file-count abuse cases have runtime-level enforcement or explicit conservative failure behavior, not only admission denial.
     - trusted Docker mode applies seccomp, AppArmor, and SELinux posture automatically when supported, with clear warnings when a host cannot supply them.
     - production presets turn hardening defaults on automatically instead of relying on ad hoc environment variables.
     - abuse tests prove that constrained sandboxes degrade, stop, or are denied predictably under pressure.

8. **Browser tunnel capabilities must support stricter issuance, revocation, and session controls.**
   - Acceptance criteria:
     - signed tunnel URL defaults are shorter than today and remain bounded by a server-side maximum.
     - the browser bootstrap flow supports optional one-time or nonce-backed capabilities without breaking the current deterministic proxy model.
     - tunnel revoke operations reliably invalidate outstanding browser capabilities at the intended risk level.
     - cookie flags, origin checks, and path scoping are documented and regression-tested for both HTTP and WebSocket flows.

9. **Snapshot, backup, and restore behavior must be proven end to end.**
   - Acceptance criteria:
     - recurring restore drills validate representative snapshot restore on a clean host.
     - snapshot bundles and promoted images have integrity verification that is checked before restore or promotion.
     - restore behavior across version upgrades is tested for the supported upgrade window.
     - operator docs define modest but explicit RPO and RTO expectations for the shipped backup and restore flow.

10. **Operators must have one supported bootstrap and readiness workflow.**
    - Acceptance criteria:
      - a single supported doctor/bootstrap flow validates runtime posture, directory posture, image policy posture, auth/TLS posture, and host prerequisites.
      - config linting detects unsafe combinations before `sandboxd` starts.
      - the bootstrap flow emits a bounded red/green report suitable for operator handoff.
      - production docs describe one recommended installation path instead of requiring operators to stitch together multiple docs manually.

11. **Operational visibility must cover the control-plane and runtime failure modes that matter for launch.**
    - Acceptance criteria:
      - existing health, capacity, metrics, and audit surfaces expose degraded sandboxes, failed runtime inspections, storage pressure, tunnel churn, noisy tenants, and image/promotion posture.
      - launch-critical alert thresholds and runbook links are documented for those signals.
      - common degraded conditions have reconcile or operator-repair guidance.
      - audit retention and query expectations are defined for operator investigations.

12. **The repo must publish a credible production test matrix and safer deployment profiles.**
    - Acceptance criteria:
      - docs identify which claims are covered by unit tests, integration tests, host-gated verification, abuse drills, recovery drills, and restore drills.
      - deployment profiles reduce config sprawl to a few supported combinations such as trusted Docker development, QEMU production core, QEMU production browser, and explicit dangerous-profile exception mode.
      - dangerous profiles require explicit tenant-level approval and audit reason when they are used.
      - most operators can deploy the supported production path from a preset/profile without reading every environment variable.

## Non-functional constraints

- Keep all persistence single-process and SQLite-compatible with additive migrations only.
- Favor deterministic, low-RAM designs over external services, dynamic policy engines, or large background workers.
- Preserve bounded command execution, bounded tunnel/session capability lifetime, bounded output, and bounded release-evidence artifacts.
- Keep production-safe defaults strict while preserving backward-compatible development mode behavior where practical.
- Treat files, tunnels, secrets, and network access as security-sensitive paths that should fail closed in production.
- Prefer extending existing packages such as `internal/config`, `internal/auth`, `internal/service`, `internal/api`, `internal/db`, `internal/guestimage`, and `cmd/sandboxctl` instead of adding new subsystems.
