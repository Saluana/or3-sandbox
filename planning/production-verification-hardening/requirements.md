# Requirements

## Overview

This plan upgrades the repo’s production-readiness verification from a narrow package-test smoke check into a layered verification program that matches the actual product boundary of `or3-sandbox`.

Scope:

- preserve the existing fast, CI-friendly package test smoke path
- add stronger host-gated and operator-gated verification for QEMU production deployments
- make verification profile-aware so the minimal substrate contract is verified separately from additive tooling profiles
- make verification control-path-aware so agent-based production guests are the default verification target and any SSH/debug compatibility path is tested separately
- explicitly cover hostile workload isolation assumptions, tunnel abuse resistance, recovery behavior, and disk exhaustion handling
- tighten docs so the repo does not imply that `./scripts/production-smoke.sh` alone proves production readiness

Assumptions:

- production posture in this repo remains `SANDBOX_MODE=production` with `SANDBOX_RUNTIME=qemu`
- Docker remains a development and convenience runtime, not the hostile multi-tenant production boundary
- the QEMU plan is moving toward a minimal guest-agent substrate contract and fixed guest profiles such as `core`, `runtime`, `browser`, and `container`
- formal proof of VM non-escape is out of scope; the implementation should instead verify enforceable boundary assumptions and document the limits of those claims

## Requirements

1. **Reframe the current smoke script as a base sanity gate, not a production proof.**
   - Acceptance criteria:
     - `scripts/production-smoke.sh` remains fast and CI-friendly.
     - operator-facing docs clearly describe it as a package-level sanity gate.
     - production-readiness language in docs requires additional verification layers beyond this script.

2. **Add a layered production verification flow that matches the repo’s existing runtime split.**
   - Acceptance criteria:
     - the verification plan defines at least three layers: package smoke, QEMU host integration, and live operator drills.
     - each layer has a clear purpose, expected environment, and pass/fail outcome.
     - non-QEMU production verification is explicitly unsupported.
     - the flow distinguishes production-default agent-based guests from any SSH/debug compatibility coverage.

3. **Verify the substrate contract on the minimal production profile independently of additive tooling profiles.**
   - Acceptance criteria:
     - host-gated verification explicitly proves the minimal substrate contract on the default `core` profile: boot, readiness, exec, PTY, file transfer, workspace behavior, and shutdown/restart handling.
     - browser or inner-Docker checks run only on the profiles that declare those capabilities.
     - verification docs explain which checks are substrate-wide versus profile-specific.

4. **Verify hostile workload isolation assumptions for the QEMU boundary.**
   - Acceptance criteria:
     - host-gated QEMU tests validate repo-specific isolation assumptions such as no host workspace bind mount leakage, no host secret mount exposure, no daemon socket access, and no privileged host device passthrough required for normal workloads.
     - tests verify that guest file operations stay inside the guest/runtime boundary and that tenant scoping still holds after restart or reconcile.
     - docs state that these checks validate assumptions and regressions, not a formal VM-escape proof.

5. **Verify control-protocol negotiation and compatibility boundaries.**
   - Acceptance criteria:
   - verification covers guest-agent readiness and protocol negotiation failures explicitly.
   - production verification requires the declared production-default control protocol to pass without silently falling back to SSH.
   - any SSH/debug compatibility coverage is clearly separated and does not count as passing the production-default control-path gate.

6. **Verify tunnel abuse resistance at the service and API layers.**
   - Acceptance criteria:
     - tests cover denial paths for disabled tunnels, exceeded tunnel quota, unsupported protocol/auth/visibility combinations, and public tunnel policy rejection in production posture.
     - tests cover token or signed-URL requirements, bounded TTL behavior, and revoke behavior after sandbox stop, delete, or reconcile where applicable.
     - production verification docs include a tunnel misuse drill using existing `sandboxctl` and API flows.

7. **Verify daemon recovery and conservative reconcile behavior.**
   - Acceptance criteria:
     - tests or drills validate behavior after daemon restart, including runtime health inspection, sandbox state reconciliation, and preservation of existing sandbox data.
     - verification covers restart behavior for at least one running QEMU workload and one snapshot-bearing sandbox.
     - docs define the minimum recovery checks operators must run after restart or upgrade.

8. **Verify disk exhaustion handling without silent corruption or unsafe cleanup.**
   - Acceptance criteria:
     - host-gated QEMU coverage validates guest-visible disk exhaustion and confirms workspace persistence after restart.
     - service- or API-level tests validate storage pressure reporting and error-state persistence for partial snapshot or storage failures.
     - operator docs define how to inspect capacity, metrics, and large storage consumers before and after remediation.

9. **Make production claims explicit about VM escape, hostile-tenant scope, and dangerous profiles.**
   - Acceptance criteria:
     - docs explicitly state that the supported hostile boundary is QEMU in production mode.
     - docs avoid language implying that unit tests or smoke tests alone prove absence of VM escape.
     - production verification guidance includes the expected residual risk statement for hypervisor/kernel vulnerabilities and host hardening dependencies.
       - docs clearly state that debug or SSH-bearing images do not satisfy the default hostile-production profile.

10. **Keep verification bounded, deterministic, and aligned with existing tooling.**
   - Acceptance criteria:
     - the plan prefers Go tests, existing scripts, and existing `sandboxctl` commands over new frameworks or external services.
     - host-gated tests remain opt-in through existing QEMU environment prerequisites.
     - any live-operator drill script defaults to non-destructive checks unless the operator explicitly opts into restart or recovery actions.

11. **Provide regression coverage for every newly claimed protection.**
   - Acceptance criteria:
     - new or expanded Go tests are identified for `internal/service`, `internal/api`, `internal/runtime/qemu`, and `cmd/sandboxctl` where relevant.
     - docs and scripts list the exact commands to run for CI smoke, host-gated coverage, and operator drills.
     - failures produce actionable output tied to an existing package, script, or runbook step.

## Non-functional constraints

- Verification must preserve deterministic behavior where possible and keep the default CI path fast.
- Host-gated QEMU coverage may be slower, but must remain bounded and opt-in.
- No new long-running services, SaaS dependencies, or frontend surfaces should be introduced.
- SQLite must remain the single-node system of record; this plan should not require incompatible schema changes unless a narrowly scoped observability need clearly demands one.
- File, network, and tunnel verification must remain safe by default and must not log raw secrets, bearer tokens, or private key material.
- Any restart, restore, or tunnel drill must preserve tenant isolation and avoid cross-tenant data exposure.
