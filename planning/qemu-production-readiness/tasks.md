# Tasks

## 1. Freeze the threat model and production claims (Req 1, 8, 12)

- [ ] Add a dedicated QEMU production threat model document under `docs/operations/` that defines adversaries, trust boundaries, guest-agent role, in-scope protections, and explicit out-of-scope claims.
- [ ] Update `docs/operations/production-deployment.md`, `docs/runtimes.md`, and `docs/project-overview.md` so production language consistently says Docker is not the hostile boundary, the production-default control path is agent-based, and Linux/KVM-backed QEMU is the supported hostile-production target.
- [ ] Update `docs/operations/verification.md` to make the threat model, profile policy, and production gates the primary reference for any “production-ready” claim.

## 2. Introduce a strict guest profile system (Req 4, 5, 6, 7, 12)

- [ ] Define the fixed profile set under `images/guest/profiles/`: `core`, `runtime`, `browser`, `container`, and optional `debug`.
- [ ] Define the substrate contract and separate it clearly from additive tooling layers in `images/guest/README.md` and related docs.
- [ ] Add profile manifests describing declared capabilities, control protocol/version, workspace contract version, and whether SSH is present.
- [ ] Add service/runtime validation so unsupported profiles, disallowed features, or random package combinations are rejected early.
- [ ] Make `core` the default production profile and document that `browser`, `container`, and `debug` are explicit opt-ins.

## 3. Rework the image build pipeline around shared base + overlays (Req 4, 5, 6, 7)

- [ ] Refactor `images/guest/build-base-image.sh` so it builds a shared minimal base and then applies profile overlays rather than maintaining one broad package list.
- [ ] Minimize the role of `images/guest/cloud-init/user-data.tpl` so profile composition is build-time rather than one giant runtime package install step.
- [ ] Generate per-profile artifacts: checksum, manifest, package inventory/SBOM-like summary, and any required host-side contract files.
- [ ] Pin package versions where practical and document reproducibility expectations in `images/guest/README.md`.
- [ ] Add build-time smoke checks for each profile to ensure declared capabilities match actual contents.

## 4. Add the guest-agent control path (Req 2, 3, 5, 6)

- [ ] Add a tiny guest-side agent binary, preferably under `cmd/or3-guest-agent`, implemented in Go.
- [ ] Define a small versioned protocol for readiness, heartbeat, exec, PTY, file transfer, and shutdown/reboot.
- [ ] Add a transport abstraction in `internal/runtime/qemu` that supports the chosen production transport and portability fallback.
- [ ] Update QEMU boot wiring in `internal/runtime/qemu/runtime.go` to attach the required control device(s) and stop depending on localhost SSH forwarding for production-default images.
- [ ] Add unit tests in `internal/runtime/qemu/runtime_test.go` for protocol negotiation, transport selection, and handshake failures.

## 5. Make agent-based control the production default while containing SSH to compatibility/debug (Req 2, 3, 6, 7)

- [ ] Add production runtime validation in `internal/config/config.go` and `internal/runtime/qemu` so production-default images no longer require `SANDBOX_QEMU_SSH_*`.
- [ ] Keep an explicit compatibility/debug path for SSH-based images only where still needed during migration.
- [ ] Mark SSH-bearing images clearly in their manifests and make them production-ineligible by default policy unless explicitly allowed.
- [ ] Update docs so SSH is described as compatibility/debug only, not the long-term runtime contract.
- [ ] Add regression tests covering agent-default and SSH-compat behavior without silently falling back in production mode.

## 6. Tighten privilege and identity inside the guest (Req 7)

- [ ] Introduce separate guest identities such as `or3-agent` and `sandbox` in the image recipes and bootstrap assets.
- [ ] Remove passwordless sudo, default `sudo` membership, and default `docker` membership from the workload user in `core`, `runtime`, and `browser`.
- [ ] Restrict inner Docker privileges to `container` only and document the extra risk and density cost.
- [ ] Place SSH, troubleshooting tools, and similar conveniences in `debug` rather than polluting the production profiles.
- [ ] Add host-gated QEMU tests that verify user/group posture inside each supported profile.

## 7. Enforce image/profile/capability contracts at runtime (Req 3, 4, 5, 6, 11, 12)

- [ ] Add host-side image-contract loading and validation in `internal/runtime/qemu`, using sidecar manifests and checksums.
- [ ] Extend `internal/model`, `internal/service`, and `internal/presets` as needed so sandbox creation can request a profile and any tightly scoped allowed features.
- [ ] Add policy validation so profile requests, dangerous features, and capability mismatches fail before guest boot.
- [ ] Record profile and relevant contract version metadata in sandbox and snapshot persistence using additive, backward-compatible changes.
- [ ] Add tests in `internal/runtime/qemu/runtime_test.go`, `internal/service/service_test.go`, and `internal/presets/manifest_test.go` for profile mismatch, forbidden capabilities, and immutable profile behavior.

## 8. Add production host validation with profile awareness (Req 8, 12)

- [ ] Add a `doctor` subcommand in `cmd/sandboxctl`, likely in `cmd/sandboxctl/doctor.go`, with a `--production-qemu` mode.
- [ ] Implement read-only host checks for KVM availability, Linux production posture, QEMU/`qemu-img` presence, version policy, disk headroom, required path permissions, secret readability, and time-sync/firewall sanity.
- [ ] Add checks that approved profile images and sidecar manifests are present, intact, and production-eligible.
- [ ] Add CLI tests in `cmd/sandboxctl/main_test.go` and config-focused regression tests in `internal/config/config_test.go` for pass/fail/warn cases.
- [ ] Document the command, output classes, and operator actions in `docs/operations/verification.md` and `docs/operations/production-deployment.md`.

## 9. Add real QEMU production smoke coverage for the substrate contract (Req 9, 10, 12)

- [ ] Add `scripts/qemu-production-smoke.sh` that verifies guest-agent readiness, exec, PTY, file transfer, suspend/resume, snapshot create/restore, and restart reconciliation on `core`.
- [ ] Extend the smoke path to verify additive capabilities only on the profiles that declare them, such as browser-specific smoke on `browser` and inner-Docker checks on `container`.
- [ ] Keep the script host-gated, bounded, and self-cleaning, with explicit skip/fail messaging when prerequisites are missing.
- [ ] Update `docs/operations/verification.md` with exact commands, prerequisites, and expected evidence from the profile-aware smoke script.

## 10. Add bounded recovery and abuse drills with per-profile expectations (Req 9, 10, 11, 12)

- [ ] Add `scripts/qemu-recovery-drill.sh` for the smallest set of disruptive drills that matter before launch: daemon restart with sandboxes present, guest-agent handshake failure, boot failure detection, stale runtime artifact cleanup, and interrupted snapshot handling.
- [ ] Add `scripts/qemu-resource-abuse.sh` with repeatable memory, disk, file-count, PID/fork-pressure, and stdout-flood scenarios plus explicit pass/fail thresholds.
- [ ] Extend `internal/runtime/qemu/host_integration_test.go` with recovery, abuse, and snapshot-integrity coverage across `core` and at least one heavier profile.
- [ ] Add service-level checks in `internal/service/service_test.go` for storage-pressure reporting, degraded/error visibility, and conservative post-restart state.
- [ ] Document which drill steps are read-only versus disruptive and require explicit operator opt-in.

## 11. Finish tunnel/browser hardening and immutable profile policy (Req 10, 12)

- [ ] Extend `internal/api/integration_test.go` for signed-URL TTL bounds, revocation correctness, cross-tenant denial, path validation, WebSocket/browser access, and cookie flag expectations.
- [ ] Extend `internal/service/service_test.go` for tunnel policy denial, quota denial, dangerous-profile denial, and audit-event behavior around misuse and revocation.
- [ ] Review `internal/api/router.go` and `internal/service/policy.go` against the threat model and make only targeted fixes needed for cookie flags, origin behavior, logging, bounds, and dangerous-profile enforcement.
- [ ] Update `docs/operations/incidents.md` with tunnel abuse and dangerous-profile incident workflows tied to the existing audit, metrics, and revoke flows.

## 12. Improve observability, cost reporting, and runbooks with existing surfaces (Req 10, 12)

- [ ] Review `internal/service/observability.go` and existing metrics output to ensure production docs can monitor boot failures, degraded sandboxes, tunnel failures, snapshot failures, storage pressure, and profile/capability mix.
- [ ] Add only narrowly scoped metrics or counters if the current health/capacity/metrics surfaces cannot express a launch-critical signal.
- [ ] Measure and document per-profile idle RAM, boot time, and disk footprint, with `core` explicitly optimized for density and faster startup.
- [ ] Add runbooks under `docs/operations/` for: guest-agent handshake failure, guest won’t boot, sandbox degraded, snapshot failed, host disk full, tunnel abuse, dangerous-profile misuse, and daemon/host restart recovery.

## 13. Lock production auth and operator boundaries (Req 8, 12)

- [ ] Tighten `internal/config/config.go` and tests so production QEMU posture continues to require JWT auth and does not silently allow weaker static-token production shortcuts.
- [ ] Review admin inspection, profile-selection, and tunnel-related permission checks in `internal/api/router.go`, `internal/service/policy.go`, and related auth tests for least-privilege alignment.
- [ ] Document JWT key rotation, break-glass handling, and the policy for dangerous-profile approval in `docs/operations/production-deployment.md` or a focused operations runbook.

## 14. Validate the full production-readiness path (Req 1-12)

- [ ] Run the fast package smoke path and confirm it remains CI-friendly.
- [ ] Run `sandboxctl doctor --production-qemu` on a prepared Linux/KVM host and confirm fail/warn/pass output is actionable.
- [ ] Run `scripts/qemu-production-smoke.sh`, `scripts/qemu-recovery-drill.sh`, and `scripts/qemu-resource-abuse.sh` in a prepared environment and confirm cleanup and operator guidance are correct.
- [ ] Validate that `core` works with no assumptions about Git, Python, Node, browsers, or Docker, and that additive capabilities only appear in the correct profiles.
- [ ] Verify all updated docs reference the actual command names, profile names, env vars, and package tests.

## 15. Out of scope for the first implementation wave

- [ ] Do not build a giant open-ended feature-toggle matrix.
- [ ] Do not add a new distributed control plane, queueing system, or external verification service.
- [ ] Do not support dozens of guest profiles beyond the fixed approved set.
- [ ] Do not broaden production claims to Docker-based hostile multi-tenancy.
