# Tasks

## 1. Freeze the threat model and production claims (Req 1, 8, 12)

- [x] Add a dedicated QEMU production threat model document under `docs/operations/` that defines adversaries, trust boundaries, guest-agent role, in-scope protections, and explicit out-of-scope claims.
- [x] Update `docs/operations/production-deployment.md`, `docs/runtimes.md`, and `docs/project-overview.md` so production language consistently says Docker is not the hostile boundary, the production-default control path is agent-based, and Linux/KVM-backed QEMU is the supported hostile-production target.
- [x] Update `docs/operations/verification.md` to make the threat model, profile policy, and production gates the primary reference for any “production-ready” claim.

## 2. Introduce a strict guest profile system (Req 4, 5, 6, 7, 12)

- [x] Define the fixed profile set under `images/guest/profiles/`: `core`, `runtime`, `browser`, `container`, and optional `debug`.
- [x] Define the substrate contract and separate it clearly from additive tooling layers in `images/guest/README.md` and related docs.
- [x] Add profile manifests describing declared capabilities, control protocol/version, workspace contract version, and whether SSH is present.
- [x] Add service/runtime validation so unsupported profiles, disallowed features, or random package combinations are rejected early.
- [x] Make `core` the default production profile and document that `browser`, `container`, and `debug` are explicit opt-ins.

## 3. Rework the image build pipeline around shared base + overlays (Req 4, 5, 6, 7)

- [x] Refactor `images/guest/build-base-image.sh` so it builds a shared minimal base and then applies profile overlays rather than maintaining one broad package list.
- [ ] Minimize the role of `images/guest/cloud-init/user-data.tpl` so profile composition is build-time rather than one giant runtime package install step.
- [x] Generate per-profile artifacts: checksum, manifest, package inventory/SBOM-like summary, and any required host-side contract files.
- [ ] Pin package versions where practical and document reproducibility expectations in `images/guest/README.md`.
- [x] Add build-time smoke checks for each profile to ensure declared capabilities match actual contents.

## 4. Add the guest-agent control path (Req 2, 3, 5, 6)

- [x] Add a tiny guest-side agent binary, preferably under `cmd/or3-guest-agent`, implemented in Go.
- [x] Define a small versioned protocol for readiness, heartbeat, exec, PTY, file transfer, and shutdown/reboot.
- [x] Add a transport abstraction in `internal/runtime/qemu` that supports the chosen production transport and portability fallback.
- [x] Update QEMU boot wiring in `internal/runtime/qemu/runtime.go` to attach the required control device(s) and stop depending on localhost SSH forwarding for production-default images.
- [x] Add unit tests in `internal/runtime/qemu/runtime_test.go` for protocol negotiation, transport selection, and handshake failures.

## 5. Make agent-based control the production default while containing SSH to compatibility/debug (Req 2, 3, 6, 7)

- [x] Add production runtime validation in `internal/config/config.go` and `internal/runtime/qemu` so production-default images no longer require `SANDBOX_QEMU_SSH_*`.
- [x] Keep an explicit compatibility/debug path for SSH-based images only where still needed during migration.
- [x] Mark SSH-bearing images clearly in their manifests and make them production-ineligible by default policy unless explicitly allowed.
- [x] Update docs so SSH is described as compatibility/debug only, not the long-term runtime contract.
- [x] Add regression tests covering agent-default and SSH-compat behavior without silently falling back in production mode.

## 6. Tighten privilege and identity inside the guest (Req 7)

- [x] Introduce separate guest identities such as `or3-agent` and `sandbox` in the image recipes and bootstrap assets.
- [x] Remove passwordless sudo, default `sudo` membership, and default `docker` membership from the workload user in `core`, `runtime`, and `browser`.
- [x] Restrict inner Docker privileges to `container` only and document the extra risk and density cost.
- [x] Place SSH, troubleshooting tools, and similar conveniences in `debug` rather than polluting the production profiles.
- [ ] Add host-gated QEMU tests that verify user/group posture inside each supported profile.

## 7. Enforce image/profile/capability contracts at runtime (Req 3, 4, 5, 6, 11, 12)

- [x] Add host-side image-contract loading and validation in `internal/runtime/qemu`, using sidecar manifests and checksums.
- [x] Extend `internal/model`, `internal/service`, and `internal/presets` as needed so sandbox creation can request a profile and any tightly scoped allowed features.
- [x] Add policy validation so profile requests, dangerous features, and capability mismatches fail before guest boot.
- [x] Record profile and relevant contract version metadata in sandbox and snapshot persistence using additive, backward-compatible changes.
- [x] Add tests in `internal/runtime/qemu/runtime_test.go`, `internal/service/service_test.go`, and `internal/presets/manifest_test.go` for profile mismatch, forbidden capabilities, and immutable profile behavior.

## 8. Add production host validation with profile awareness (Req 8, 12)

- [x] Add a `doctor` subcommand in `cmd/sandboxctl`, likely in `cmd/sandboxctl/doctor.go`, with a `--production-qemu` mode.
- [x] Implement read-only host checks for KVM availability, Linux production posture, QEMU/`qemu-img` presence, version policy, disk headroom, required path permissions, secret readability, and time-sync/firewall sanity.
- [x] Add checks that approved profile images and sidecar manifests are present, intact, and production-eligible.
- [x] Add CLI tests in `cmd/sandboxctl/main_test.go` and config-focused regression tests in `internal/config/config_test.go` for pass/fail/warn cases.
- [x] Document the command, output classes, and operator actions in `docs/operations/verification.md` and `docs/operations/production-deployment.md`.

## 9. Add real QEMU production smoke coverage for the substrate contract (Req 9, 10, 12)

- [x] Add `scripts/qemu-production-smoke.sh` that verifies guest-agent readiness, exec, PTY, file transfer, suspend/resume, snapshot create/restore, and restart reconciliation on `core`.
- [x] Extend the smoke path to verify additive capabilities only on the profiles that declare them, such as browser-specific smoke on `browser` and inner-Docker checks on `container`.
- [x] Keep the script host-gated, bounded, and self-cleaning, with explicit skip/fail messaging when prerequisites are missing.
- [x] Update `docs/operations/verification.md` with exact commands, prerequisites, and expected evidence from the profile-aware smoke script.

## 10. Add bounded recovery and abuse drills with per-profile expectations (Req 9, 10, 11, 12)

- [ ] Add `scripts/qemu-recovery-drill.sh` for the smallest set of disruptive drills that matter before launch: daemon restart with sandboxes present, guest-agent handshake failure, boot failure detection, stale runtime artifact cleanup, and interrupted snapshot handling.
- [x] Add `scripts/qemu-resource-abuse.sh` with repeatable memory, disk, file-count, PID/fork-pressure, and stdout-flood scenarios plus explicit pass/fail thresholds.
- [ ] Extend `internal/runtime/qemu/host_integration_test.go` with recovery, abuse, and snapshot-integrity coverage across `core` and at least one heavier profile.
- [x] Add service-level checks in `internal/service/service_test.go` for storage-pressure reporting, degraded/error visibility, and conservative post-restart state.
- [x] Document which drill steps are read-only versus disruptive and require explicit operator opt-in.

## 11. Finish tunnel/browser hardening and immutable profile policy (Req 10, 12)

- [x] Extend `internal/api/integration_test.go` for signed-URL TTL bounds, revocation correctness, cross-tenant denial, path validation, WebSocket/browser access, and cookie flag expectations.
- [x] Extend `internal/service/service_test.go` for tunnel policy denial, quota denial, dangerous-profile denial, and audit-event behavior around misuse and revocation.
- [x] Review `internal/api/router.go` and `internal/service/policy.go` against the threat model and make only targeted fixes needed for cookie flags, origin behavior, logging, bounds, and dangerous-profile enforcement.
- [x] Update `docs/operations/incidents.md` with tunnel abuse and dangerous-profile incident workflows tied to the existing audit, metrics, and revoke flows.

## 12. Improve observability, cost reporting, and runbooks with existing surfaces (Req 10, 12)

- [x] Review `internal/service/observability.go` and existing metrics output to ensure production docs can monitor boot failures, degraded sandboxes, tunnel failures, snapshot failures, storage pressure, and profile/capability mix.
- [x] Add only narrowly scoped metrics or counters if the current health/capacity/metrics surfaces cannot express a launch-critical signal.
- [ ] Measure and document per-profile idle RAM, boot time, and disk footprint, with `core` explicitly optimized for density and faster startup.
- [x] Add runbooks under `docs/operations/` for: guest-agent handshake failure, guest won’t boot, sandbox degraded, snapshot failed, host disk full, tunnel abuse, dangerous-profile misuse, and daemon/host restart recovery.

## 13. Lock production auth and operator boundaries (Req 8, 12)

- [x] Tighten `internal/config/config.go` and tests so production QEMU posture continues to require JWT auth and does not silently allow weaker static-token production shortcuts.
- [x] Review admin inspection, profile-selection, and tunnel-related permission checks in `internal/api/router.go`, `internal/service/policy.go`, and related auth tests for least-privilege alignment.
- [x] Document JWT key rotation, break-glass handling, and the policy for dangerous-profile approval in `docs/operations/production-deployment.md` or a focused operations runbook.

## 14. Validate the full production-readiness path (Req 1-12)

- [x] Run the fast package smoke path and confirm it remains CI-friendly.
- [ ] Run `sandboxctl doctor --production-qemu` on a prepared Linux/KVM host and confirm fail/warn/pass output is actionable.
- [ ] Run `scripts/qemu-production-smoke.sh`, `scripts/qemu-recovery-drill.sh`, and `scripts/qemu-resource-abuse.sh` in a prepared environment and confirm cleanup and operator guidance are correct.
- [ ] Validate that `core` works with no assumptions about Git, Python, Node, browsers, or Docker, and that additive capabilities only appear in the correct profiles.
- [x] Verify all updated docs reference the actual command names, profile names, env vars, and package tests.

## 15. Out of scope for the first implementation wave

- [ ] Do not build a giant open-ended feature-toggle matrix.
- [ ] Do not add a new distributed control plane, queueing system, or external verification service.
- [ ] Do not support dozens of guest profiles beyond the fixed approved set.
- [ ] Do not broaden production claims to Docker-based hostile multi-tenancy.

## Remaining open items and why

- `images/guest/cloud-init/user-data.tpl` minimization is still open because the current image pipeline still relies on cloud-init for package installation and first-boot composition; finishing that well requires moving more package layering into the offline image build path rather than another small patch.
- Package version pinning is still only partial: the build now records resolved guest package versions and supports exact apt selections in manifests, but the repo does not yet fully pin or vendor the upstream Ubuntu package sources needed for stronger reproducibility claims.
- The user/group posture tests, extended recovery/integration tests, doctor validation, smoke/recovery/abuse script execution, and per-profile performance measurements all require a prepared Linux/KVM host or other host-gated environment that is not available in this macOS workspace.
- The `core` profile validation item remains open for the same reason: the strongest proof is the prepared-host QEMU validation path, not just local unit coverage.
- The unchecked items in section 15 are intentional out-of-scope guardrails, not missing implementation work; they stay unchecked to document the boundaries of this first production-readiness wave.
