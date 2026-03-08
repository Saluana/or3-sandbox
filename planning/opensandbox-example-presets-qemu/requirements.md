# QEMU Preset Parity Requirements

## Overview

This plan covers the **QEMU runtime path** for OpenSandbox-style example presets.

The goal is to reach the **same preset feature surface** as the Docker plan from the user's point of view:

- named presets
- preset defaults
- env inputs
- workspace seeding
- bootstrap commands
- startup commands
- readiness checks
- tunnel exposure
- artifact downloads
- cleanup policy

However, the QEMU implementation must be honest about one major architectural difference:

- Docker presets can point directly at arbitrary container images.
- QEMU sandboxes currently boot a guest image and then work *inside that guest* through SSH and workspace/state disks.

So QEMU parity should target the same features, but it may need different packaging and bootstrap mechanics.

Assumptions:

- QEMU preset execution still uses the same shared preset manifest and shared CLI runner core as the Docker plan.
- QEMU-specific behavior is handled through adapter logic and guest-image conventions, not through a separate orchestration engine.
- The QEMU path should not duplicate the manifest parser, runner sequencing, readiness framework, or artifact-transfer logic.

## Requirements

1. **Shared preset contract with QEMU compatibility rules**
   - QEMU presets must use the same shared manifest schema as Docker presets.
   - Acceptance criteria:
     - Shared preset fields mean the same thing on both runtimes.
     - QEMU-specific validation explains when a preset is incompatible with the current guest-image path.
     - Manifest parsing remains shared code.

2. **Feature-parity goal with runtime-specific packaging**
   - QEMU must offer the same high-level preset features as Docker, even when the internal implementation differs.
   - Acceptance criteria:
     - The QEMU plan supports bootstrap commands, detached startup, readiness checks, tunnels, artifacts, and cleanup.
     - Differences in image packaging or guest preparation are documented explicitly.
     - The user-facing preset command structure is the same as the Docker track.

3. **Guest-image-compatible preset packaging**
   - QEMU presets must work with the current guest image model instead of pretending they can run arbitrary Docker images directly.
   - Acceptance criteria:
     - A QEMU preset can point at a compatible guest base image path or a prebuilt guest image profile.
     - The plan defines how example dependencies are provided: prebuilt guest images, first-boot provisioning, or bounded bootstrap installs.
     - The runner fails early when a preset requires unsupported guest capabilities.

4. **Shared CLI runner with QEMU adapter**
   - The same `sandboxctl preset` commands must drive QEMU preset runs.
   - Acceptance criteria:
     - The shared runner core is reused.
     - QEMU-specific checks and guest packaging logic live behind a runtime adapter.
     - No second workflow engine or duplicate runner implementation is introduced.

5. **Env injection and workspace seeding parity**
   - QEMU presets must support the same env and file-seeding features as Docker presets.
   - Acceptance criteria:
     - Required and optional env inputs work for QEMU bootstrap and startup commands.
     - Repo-local and inline text assets can be uploaded into the guest workspace.
     - Secret values remain redacted from logs.

6. **Readiness and service exposure parity**
   - QEMU presets must support the same readiness and endpoint exposure surface as Docker presets.
   - Acceptance criteria:
     - QEMU presets can run detached services when the guest image supports them.
     - Readiness checks can validate command success or HTTP service readiness.
     - Tunnel creation and endpoint printing work the same way from the user's point of view.

7. **Binary-safe artifact download parity**
   - QEMU presets must support the same artifact download features as Docker presets.
   - Acceptance criteria:
     - Binary-safe file transfer is shared across runtimes.
     - Screenshot-like artifacts can be produced inside the guest and downloaded locally.
     - Text file behavior remains backward compatible.

8. **QEMU-specific readiness and recovery honesty**
   - QEMU preset runs must account for guest boot time and guest readiness separately from application readiness.
   - Acceptance criteria:
     - The runner distinguishes guest boot failures from preset startup failures.
     - QEMU readiness can include guest-ready and app-ready phases.
     - Failure output clearly indicates whether the problem is guest boot, bootstrap, startup, or app readiness.

9. **Example set with QEMU-appropriate packaging**
   - The repo must ship QEMU-oriented example presets only when the required guest packaging path is real and documented.
   - Acceptance criteria:
     - At least one example demonstrates CLI/bootstrap behavior in a guest.
     - At least one example demonstrates service or browser behavior if the guest image supports it.
     - Each QEMU example README documents image prerequisites and expected host requirements.

10. **No duplicate orchestration code**
    - The QEMU feature track must reuse the same shared preset framework as Docker.
    - Acceptance criteria:
      - Shared code owns manifest parsing, input resolution, step orchestration, readiness loop scaffolding, artifact handling, and cleanup policy behavior.
      - QEMU-specific code is limited to runtime capability checks, image/profile resolution, and guest-specific readiness nuances.
      - The design explicitly rejects copy-pasted Docker runner logic.

## Non-functional constraints

- Keep runtime-neutral behavior in shared code.
- Keep QEMU-specific behavior localized to a small adapter/profile layer.
- Respect the existing low-RAM, single-node, deterministic model.
- Avoid new SQLite schema unless preset-run history is explicitly scoped later.
- Keep guest boot, app startup, and readiness loops bounded and diagnosable.
- Do not misrepresent arbitrary Docker-image parity on the current guest-image-based QEMU path.
