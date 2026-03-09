# Requirements

## Overview

This plan tightens the sandbox storage, networking, and snapshot model without adding heavyweight infrastructure.

Scope:
- define explicit storage classes for sandbox data
- enforce real storage limits where the backend can support them and conservative guardrails where it cannot
- move networking from a raw `bridge-or-none` implementation toward a small policy-driven model
- harden snapshot import/export and restore paths as attacker-controlled input

Assumptions:
- the repo already tracks storage usage in SQLite and already has snapshot and tunnel APIs
- the current API surface should remain compatible; policy and enforcement can become stricter behind the existing create/snapshot/tunnel flows
- Linux host features may be stronger than macOS development behavior, but operator docs must be explicit about that

## Requirements

1. **Define explicit storage classes for sandbox data.**
   - Acceptance criteria:
     - the sandbox runtime distinguishes at least `workspace`, `cache`, `scratch`, `snapshot`, and `secrets` classes, even if not all are exposed in the first API wave.
     - runtime implementations mount or map those classes with clear durability and mutability expectations.
     - docs describe what survives restart, what survives restore, and what is intentionally ephemeral.

2. **Keep the writable surface minimal and class-specific.**
   - Acceptance criteria:
     - the default runtime layout keeps only the required storage classes writable.
     - cache and scratch are distinct from durable workspace state.
     - secrets are not mixed into the workspace tarball or normal snapshot payloads.

3. **Enforce storage limits with real backend-aware controls.**
   - Acceptance criteria:
     - VM-backed runtimes enforce disk ceilings through virtual-disk sizing or an equivalent hard mechanism.
     - Docker trusted mode uses the strongest supported host/runtime enforcement available and otherwise falls back to conservative admission and reconcile-time enforcement with explicit operator documentation.
     - storage tracking covers workspace bytes, snapshot bytes, and cache growth, not just API-level requested limits.

4. **Track inode and file-count abuse as well as byte growth.**
   - Acceptance criteria:
     - the system can detect and report file-count or inode-heavy abuse patterns during reconcile or measurement.
     - bounded tests cover both byte growth and file-count explosion scenarios.
     - storage-pressure reporting becomes visible in runtime health, capacity, or metrics output.

5. **Move networking to a small policy-driven model.**
   - Acceptance criteria:
     - the implementation can represent at least loopback-only, no-internet, and controlled-egress behavior without exposing a large firewall DSL.
     - tunnel exposure remains loopback-only inside the sandbox unless explicitly published through the existing tunnel control plane.
     - exposure changes and policy denials are recorded in audit output.

6. **Keep DNS and egress behavior explicit.**
   - Acceptance criteria:
     - internet-enabled sandboxes use a documented DNS and egress policy rather than an implicit “whatever the bridge allows” posture.
     - no-internet sandboxes cannot bypass policy through broad host networking shortcuts.
     - the design leaves room for future allowlists or rate limits without requiring them in the first wave.

7. **Harden snapshot export/import and restore as attacker-controlled input.**
   - Acceptance criteria:
     - restore rejects or normalizes dangerous tar entries including path traversal, unsupported special files, and disallowed ownership metadata.
     - restore enforces bounded total size, bounded file count, and bounded expansion ratio.
     - snapshot metadata records enough profile/runtime information to reject incompatible restores.

8. **Keep the implementation lightweight and single-node friendly.**
   - Acceptance criteria:
     - no external storage gateway, SDN controller, or snapshot-metadata service is introduced.
     - the first implementation uses existing storage roots, SQLite metadata, runtime code, and audit surfaces.
     - any new policy model is small enough to express in current config and Go types.

## Non-functional constraints

- Favor additive use of existing `sandbox_storage`, `snapshots`, and audit tables over new subsystems.
- Keep snapshot and restore behavior deterministic and bounded.
- Preserve existing API paths for sandboxes, tunnels, and snapshots wherever possible.
- Fail closed on malformed snapshot data or incompatible restore metadata.
