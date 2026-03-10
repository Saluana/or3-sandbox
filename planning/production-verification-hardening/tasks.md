# Tasks

## 1. Reframe the existing smoke path (Req 1, 2, 7, 8)

- [ ] Update `docs/operations/verification.md` to describe `scripts/production-smoke.sh` as the fast package sanity gate rather than sufficient proof of production readiness.
- [ ] Update `docs/operations/production-deployment.md` and `docs/operations/upgrades.md` so “production-ready” language requires QEMU host verification plus operator drills.
- [ ] Review `scripts/production-smoke.sh` output and inline comments; add brief messaging only if needed to clarify scope without slowing the script.

## 2. Add a gated QEMU host verification wrapper (Req 2, 3, 5, 6, 8, 9)

- [ ] Add a small shell wrapper under `scripts/` that runs the existing QEMU host integration tests when required env vars are present and prints a clear skip message otherwise.
- [ ] Keep the wrapper limited to existing `go test` entry points and current QEMU env names, while allowing the QEMU plan’s profile/control-mode inputs to be surfaced explicitly.
- [ ] Document the wrapper command and prerequisites in `docs/operations/verification.md`.

## 3. Extend QEMU host integration coverage for production boundary assumptions (Req 2, 3, 5, 6, 7, 9)

- [ ] Expand `internal/runtime/qemu/host_integration_test.go` with a `core` substrate test that validates boot, readiness, exec, PTY/files, and guest-agent protocol negotiation without relying on production SSH assumptions.
- [ ] Add an isolation-focused test that validates repo-specific boundary assumptions such as absence of host docker socket exposure, absence of mounted host secret paths, and no host workspace bind leakage into the guest.
- [ ] Add or expand host-gated restart/recovery tests that validate conservative behavior after stop/start cycles and profile-specific workload persistence only where the selected profile declares those capabilities.
- [ ] Keep all new host tests self-cleaning and rooted in `t.TempDir()` to avoid persistent host state.
- [ ] Ensure failures are specific enough for operators to distinguish host misconfiguration from sandbox isolation regressions.

## 4. Strengthen service-layer tunnel and recovery regression tests (Req 4, 5, 6, 8, 9)

- [ ] Add `internal/service` tests for tunnel denial paths: sandbox disallows tunnels, tenant quota disallows tunnels, tunnel quota exceeded, unsupported protocol/auth/visibility, and public tunnel rejection by policy.
- [x] Add `internal/service` tests for dangerous-profile denial or immutable-profile violations if the QEMU profile system exposes those policy checks through service APIs.
- [ ] Add `internal/service` tests covering tunnel revoke behavior triggered by lifecycle transitions that should invalidate active access.
- [x] Add or expand tests for storage pressure and partial snapshot failure persistence, reusing existing capacity and snapshot behavior where possible.
- [x] Add a targeted reconcile or restart regression test in `internal/service/service_test.go` that validates persisted state remains conservative after runtime inspection or recovery.

## 5. Strengthen API-level verification surfaces (Req 4, 5, 8, 9)

- [x] Add `internal/api/integration_test.go` coverage for tunnel auth requirements, bounded signed URL TTL behavior, and relevant denial responses.
- [x] Add API-level verification for admin inspection endpoints used in operator drills, including `runtime-health`, capacity, and metrics access control.
- [ ] Reuse existing auth and repository harnesses rather than introducing a new integration framework.

## 6. Add an operator drill path using existing CLI and API flows (Req 2, 4, 5, 6, 8, 9)

- [ ] Decide whether existing `sandboxctl` commands are sufficient for the documented drills; only add a thin CLI helper if the shell flow is too repetitive or error-prone.
- [ ] If needed, add a minimal helper in `cmd/sandboxctl` that bundles read-only verification steps such as health, capacity, metrics, and disposable preset execution.
- [ ] Make the documented drill path print or record the selected guest profile and control mode so evidence is tied to the right runtime contract.
- [ ] Keep restart or restore actions opt-in and explicitly labeled as disruptive.
- [ ] Document the expected output and cleanup steps for each operator drill.

## 7. Tighten production-boundary and residual-risk documentation (Req 3, 6, 7, 8)

- [ ] Update `docs/operations/verification.md` with a dedicated section on what the verification suite does and does not prove about hostile workload isolation.
- [ ] Update the same docs to distinguish `core` substrate verification from `browser`/`container` capability verification and from any SSH/debug compatibility checks.
- [ ] Update `docs/operations/production-deployment.md` to restate that QEMU is the supported hostile boundary and that host/kernel/hypervisor hardening remains an external dependency.
- [ ] Update `docs/operations/incidents.md` with follow-up steps for failed tunnel-abuse checks, restart-recovery failures, and storage-pressure verification findings.

## 8. Validate the new verification flow (Req 1, 2, 3, 4, 5, 6, 8, 9)

- [ ] Run the fast package smoke path and confirm it still works in a generic CI-like environment.
- [ ] Run the new or updated QEMU host verification wrapper in a prepared environment and confirm skip/pass/fail behavior is explicit.
- [ ] Run any new service, API, and `sandboxctl` tests added by this work.
- [ ] Verify the docs’ command examples match the actual script and test entry points.

## 9. Out of scope

- [ ] Do not attempt to build a formal VM-escape proof framework.
- [ ] Do not add a new external orchestrator, SaaS test harness, or frontend dashboard for verification.
- [ ] Do not broaden production claims to Docker-based hostile multi-tenancy.
- [ ] Do not let transitional SSH/debug verification stand in for the production-default guest-agent verification gate.
- [ ] Do not introduce destructive live-environment disk-fill automation as the default operator path.
