# Requirements

## Overview

This plan takes the repo’s QEMU runtime from “higher-isolation beta” to a cleaner appliance-style production architecture built around two primary moves:

- a tiny guest-agent control channel over virtio-serial or vsock instead of SSH as the default runtime contract
- a small set of fixed, validated guest profiles built from a shared base rather than one broad “everything installed” image

Scope:

- formalize the hostile-workload security model and production claims
- define a strict substrate contract for boot, readiness, exec, PTY, files, and shutdown
- add a small guest-agent protocol and make it the production-default control path
- split guest images into fixed profiles: `core`, `runtime`, `browser`, `container`, and optional `debug`
- enforce strict image/profile/capability contracts and immutable profile policy
- harden privilege, verification, recovery, and observability around the new model

Assumptions:

- the supported production shape remains a single `sandboxd` process using SQLite
- the supported hostile boundary remains `SANDBOX_MODE=production` with `SANDBOX_RUNTIME=qemu`
- Linux with KVM is the production host target for hostile multi-tenant use; macOS/HVF remains useful for development and pre-release validation, not the primary sellable production profile
- the repo should prefer Go-native implementation choices, so the initial guest agent should be planned in Go unless a compelling repo-aligned reason emerges otherwise
- the transport should be abstracted so the runtime can prefer vsock on Linux/KVM while still supporting virtio-serial as the portability/fallback path during migration

## Requirements

1. **Freeze the QEMU production threat model and trust boundaries around the new control architecture.**
   - Acceptance criteria:
     - a dedicated operator-facing threat model document defines in-scope adversaries: malicious tenant code in guest, guest breakout attempts, tunnel abuse, noisy-neighbor resource abuse, and stolen API/JWT credentials.
     - the document defines trust boundaries for host, `sandboxd` control plane, hypervisor boundary, guest agent, guest OS, and workload inside the guest.
     - the document explicitly states that Docker is not the hostile multi-tenant boundary, and that QEMU on the supported host profile is the hostile boundary.
     - the document explicitly states whether root inside the guest is supported by design and in which profiles, and lists out-of-scope claims such as formal proof against all hypervisor/kernel escapes.

2. **Replace SSH as the default production control path with a minimal guest-agent transport.**
   - Acceptance criteria:
     - a versioned guest control protocol is defined for the minimum required operations: readiness, heartbeat, exec, PTY attach, file upload/download, and shutdown/reboot.
     - the runtime can boot a production guest, detect readiness, execute commands, transfer files, and attach PTY without relying on `openssh-server`.
     - the default production image contains no SSH server and the runtime no longer requires `SANDBOX_QEMU_SSH_*` for the default control path.
     - any transitional SSH support is limited to compatibility or debug workflows and is not the default production contract.

3. **Keep the control transport simple, portable, and explicitly negotiated.**
   - Acceptance criteria:
     - the runtime uses a small transport abstraction that supports at least one portability-safe control path and one Linux/KVM-preferred control path.
     - image manifests declare the supported control protocol and version.
     - runtime startup performs a version/capability handshake with the guest agent and refuses incompatible protocol versions.
     - the design avoids a broad RPC surface; only minimum sandbox lifecycle operations are in scope.

4. **Split the guest into a small set of fixed profiles with strict validation.**
   - Acceptance criteria:
     - the image system defines at least `core`, `runtime`, `browser`, and `container` profiles, with optional `debug` kept separate from the production baseline.
     - `core` is the default production profile and contains only the minimum substrate needed for the guest agent, workspace, readiness, and process execution.
     - `browser` and `container` are explicit opt-ins and are not silently implied by the default image.
     - random package combinations are not allowed; any feature flags are small in number, profile-scoped, and validated so unsupported combinations fail early.

5. **Separate substrate from tooling so the control plane depends only on the substrate contract.**
   - Acceptance criteria:
     - the runtime and service layers work against a minimal guest that does not assume Git, Python, Node, browsers, or inner Docker are present.
     - language runtimes, browser stacks, and inner Docker are additive tooling layers provided only by specific profiles or explicitly allowed feature flags.
     - the substrate contract is documented and includes boot, workspace mount, readiness, control channel, process execution, and file transfer.

6. **Enforce a strict, machine-readable image contract.**
   - Acceptance criteria:
     - the build flow emits sidecar contract artifacts with at least image checksum, build version, git SHA, profile, control protocol version, workspace contract version, and package inventory or SBOM-like summary.
     - runtime startup refuses to use images whose contract artifacts are missing, malformed, checksum-mismatched, profile-inconsistent, or protocol-incompatible.
     - production runtime can determine image profile and declared capabilities without relying on undocumented operator assumptions.
     - debug or transitional SSH images are clearly marked and can be rejected by production policy.

7. **Tighten guest privilege by default and make risky capability exposure explicit.**
   - Acceptance criteria:
     - the guest separates a control-path identity from the workload execution identity.
     - the default workload user is unprivileged: no passwordless sudo, no `sudo` group membership, and no `docker` group membership.
     - only the `container` profile can expose inner Docker by default, and that risk is documented as explicit operator choice.
     - `debug` conveniences such as SSH and troubleshooting tools are isolated to a non-default profile that is blocked from hostile production by policy.

8. **Define a production host profile and refuse “production QEMU” claims when it is not met.**
   - Acceptance criteria:
     - `sandboxctl doctor --production-qemu` validates the required host profile using existing config inputs plus the new control/image contract expectations.
     - the doctor command checks at least: KVM availability, supported Linux host posture, QEMU and `qemu-img` presence/version, disk space thresholds, storage/snapshot path permissions, secret readability and restrictive file modes, time sync visibility, and basic firewall/routing sanity relevant to the deployment.
     - the command validates the presence and integrity of approved profile images and flags production-ineligible debug or SSH-based images.
     - the command exits non-zero on blocking failures and prints a bounded summary distinguishing blocking failures from warnings.

9. **Add a bounded QEMU production verification suite that is profile-aware and control-path-aware.**
   - Acceptance criteria:
     - host-gated production smoke coverage verifies the substrate contract on `core` and verifies additive capabilities only on the profiles that declare them.
     - the verification path explicitly tests guest-agent readiness, protocol negotiation, exec, PTY, files, snapshot create/restore, and restart reconciliation without depending on SSH for the production path.
     - bounded compatibility coverage may exist for transitional SSH/debug images, but it is clearly separate from production-default verification.
     - operator docs clearly distinguish fast CI smoke, host-gated QEMU smoke, disruptive recovery drills, and profile-specific verification.

10. **Prove resource enforcement, cleanup, and profile cost behavior under adversarial workloads.**
    - Acceptance criteria:
      - host-gated tests or drill scripts validate CPU, memory, PIDs, disk, file-count pressure, workspace growth, and bounded stdout behavior using repeatable workloads.
      - the suite includes at least memory hog, disk flood, file-count explosion, stdout flood, and PID/fork pressure scenarios with explicit pass/fail thresholds.
      - each supported profile has documented idle RAM, boot-time, and disk-footprint measurements, with `core` explicitly optimized for density and faster boot.
      - tests verify cleanup and post-failure sandbox usability or conservative error reporting after forced stop, crash, or OOM conditions.

11. **Validate snapshot integrity and restore safety for the new profile/capability model.**
    - Acceptance criteria:
      - snapshot behavior is documented with explicit atomicity expectations for the current file-copy based implementation.
      - verification covers restore after daemon restart, interrupted snapshot creation, partial snapshot data, and corrupted snapshot metadata.
      - snapshot metadata records the source profile and relevant contract version so restore can reject incompatible or drifting images.
      - retention and cleanup expectations are documented so incomplete snapshots do not silently accumulate.

12. **Harden tunnel/browser access, auth, observability, and runbooks around immutable profiles.**
    - Acceptance criteria:
      - tunnel verification covers private/public policy enforcement, signed URL TTL bounds, revocation behavior, tenant isolation, path validation, and WebSocket/browser access behavior.
      - production documentation requires JWT auth for QEMU production mode and documents key rotation, break-glass handling, and dangerous-profile admin opt-in.
      - operator metrics and health checks cover boot latency, tunnel failures, snapshot failures, degraded sandboxes, recovery results, and profile/capability mix using existing health/capacity/metrics surfaces extended only where necessary.
      - policy prevents silent profile drift and blocks production use of disallowed profiles such as `debug` unless explicitly overridden by admin policy.

## Non-functional constraints

- Favor deterministic behavior, bounded execution, and low-RAM host-side and guest-side implementation.
- Keep the default CI path fast; host-gated QEMU checks may be slower but must remain opt-in and bounded.
- Preserve current config loading semantics, runtime selection, SQLite compatibility, and snapshot/history expectations unless an additive change is clearly justified.
- Treat secrets, tunnel credentials, and any transitional SSH material as file-backed operator material; do not print raw secret values in doctor, smoke, or drill output.
- Avoid introducing a frontend, distributed control plane, or background services not already used by this repo.
- Prefer profile manifests, immutable capability declarations, and strict validation over open-ended runtime feature toggles.
