# Overview

This plan turns the current `or3-sandbox` state into a short, production-facing proof wave: tell the truth about the hostile boundary, make QEMU verification non-optional for production claims, freeze the API contract that downstream clients depend on, and prove that tunnel and quota controls hold under stress.

Scope assumptions:

- `sandboxd` remains the single-node Go daemon with SQLite metadata.
- Docker stays available for trusted and development use only.
- QEMU remains the intended higher-isolation path for hostile or multi-tenant workloads.
- The plan adds proof, gates, and conformance around existing surfaces rather than inventing a new control plane.

# Requirements

## 1. Truthful deployment posture

Production posture must clearly distinguish trusted Docker use from hostile-workload QEMU use.

Acceptance criteria:

- Production startup rejects unsafe runtime/auth combinations for hostile or multi-tenant deployments.
- Docs and operator checks consistently state that Docker is trusted-only and not the hostile production boundary.
- Production-ready language is gated on the documented QEMU verification flow.

## 2. Proved QEMU recovery path

QEMU support must be repeatedly survivable, not just implemented.

Acceptance criteria:

- Host verification is part of release qualification for supported production hosts.
- Release qualification covers guest readiness, exec, suspend/resume, snapshot restore, and daemon restart reconciliation.
- Guest image contract version and guest-agent contract version are recorded and checked.
- Failed verification blocks production claims for the release.

## 3. Stable sandbox API contract

The daemon must publish one stable HTTP and streaming contract for the subset of sandbox behavior clients depend on.

Acceptance criteria:

- Exec, exec stream, files, tunnels, lifecycle, snapshots, and runtime inspection have explicit documented request/response and stream framing rules.
- Conformance tests fail when daemon behavior drifts from the documented contract.
- Downstream compatibility tests exist for the currently supported `or3-net` integration path.

## 4. Hardened browser and tunnel capabilities

Browser-facing access must stay narrow, short-lived, and revocable.

Acceptance criteria:

- Replicated or rolling deployments require a shared tunnel-signing secret.
- Proxy paths strip inbound auth headers and tunnel-only cookies before forwarding upstream.
- TTL, replay, revocation, and cookie-scope behavior are covered by tests.
- Operators can inspect and revoke active tunnel capabilities.

## 5. Quotas that resist abuse

Quota controls must behave predictably under hostile usage rather than only existing in config.

Acceptance criteria:

- Stress coverage exists for CPU, memory, disk, process count, exec stream flood, upload/download abuse, and tunnel abuse.
- Over-limit behavior is stable and explicit: reject, throttle, kill, or degrade with documented errors.
- Recovery after restart does not lose track of quota pressure or leave the runtime in an unsafe state.

# Non-functional constraints

- Keep the daemon single-node and SQLite-backed.
- Do not broaden Docker into a hostile multi-tenant claim.
- Prefer small additive validation, tests, and operator tooling over new subsystems.
- Keep API error envelopes stable and `snake_case`.
- Keep tunnel and exec capabilities bounded by TTL, scope, and revocation semantics.
