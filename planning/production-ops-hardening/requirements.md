# Requirements

## Overview

This plan covers the operational bottlenecks that remain after the runtime, image, and storage/security boundaries are tightened.

Scope:
- add lightweight admission control and abuse resistance
- define sandbox-versus-tenant isolation expectations clearly
- harden recovery, reconciliation, and cleanup behavior
- make telemetry and image supply-chain policy first-class

Assumptions:
- the repo remains a single-node, single-process control plane backed by SQLite
- the first wave should not introduce a distributed scheduler, external metrics service, or separate image-policy service
- production isolation in this repo should remain one sandbox per VM-backed workload, not multi-container pods inside one guest

## Requirements

1. **Define tenant isolation separately from sandbox isolation.**
   - Acceptance criteria:
     - docs and policy clearly state that the production isolation unit is one sandbox per VM-backed workload.
     - the control plane does not imply that multiple tenant sandboxes share one guest VM for normal production operation.
     - restart, snapshot, and quota behavior are documented in terms of sandbox-level isolation and tenant-level quotas.

2. **Add bounded admission control for node pressure.**
   - Acceptance criteria:
     - sandbox create/start paths can deny or delay work when CPU, memory, storage, or running-sandbox pressure crosses configured limits.
     - admission decisions remain deterministic and local to the current node.
     - the first implementation does not require a distributed queue or scheduler.

3. **Enforce per-tenant fairness and concurrency limits more explicitly.**
   - Acceptance criteria:
     - the system can bound per-tenant concurrent starts or heavy operations in addition to existing quota counts.
     - abusive tenants cannot monopolize startup capacity through bursty create/start loops.
     - audit or metrics output makes admission denials visible.

4. **Harden recovery and reconciliation for ugly failure cases.**
   - Acceptance criteria:
     - reconciliation after daemon restart is tested for running sandboxes, partially started sandboxes, and partially deleted sandboxes.
     - orphaned runtime artifacts are cleaned up conservatively.
     - snapshot failure, tunnel recovery, and runtime crash scenarios have explicit operator handling or automated cleanup.

5. **Make abuse testing a first-class release gate.**
   - Acceptance criteria:
     - host-gated tests or scripts cover OOM, PID exhaustion, disk pressure, stdout floods, and repeated startup abuse.
     - release guidance requires running those checks for production-targeted runtime changes.
     - the checks are bounded, self-cleaning, and documented.

6. **Promote security telemetry and auditability to first-class behavior.**
   - Acceptance criteria:
     - metrics or audit surfaces cover sandbox lifecycle failures, storage pressure, OOM, PID exhaustion, snapshot import/export, exec and TTY attach, and tunnel exposure changes.
     - dangerous profile or elevated-capability use is visible in audit logs.
     - incidents can be investigated from existing health, capacity, metrics, and audit endpoints without adding a new telemetry service.

7. **Tighten image supply-chain policy in the control plane.**
   - Acceptance criteria:
     - production policy can restrict sandbox creation to approved image refs or artifacts.
     - operator guidance prefers digest-pinned curated images and documented rebuild cadence.
     - image metadata validation failures are surfaced before runtime create.

8. **Keep the operational model lightweight.**
   - Acceptance criteria:
     - no external scheduler, queue broker, or compliance platform is required for the first wave.
     - new control-plane state is kept small and SQLite-compatible.
     - the design uses existing service, audit, metrics, doctor, and scripts surfaces wherever possible.

## Non-functional constraints

- Favor deterministic local admission checks over opaque heuristics.
- Preserve reconcile correctness across daemon restarts.
- Keep telemetry bounded; do not log secrets or unbounded command output.
- Do not add long-lived background workers outside the existing daemon and reconcile loop.
