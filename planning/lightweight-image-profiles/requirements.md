# Requirements

## Overview

This plan reduces image weight and attack surface by replacing the current broad default image posture with a small curated profile set.

Scope:
- split heavyweight default images into smaller curated profiles
- make the smallest secure profile the default for new sandboxes
- remove Docker-in-container and similar high-risk tooling from the normal tenant path
- keep profile selection aligned with the existing `GuestProfile` model already present in the repo

Assumptions:
- the repo already has `core`, `runtime`, `browser`, `container`, and `debug` profile vocabulary on the QEMU side
- the same profile vocabulary can be used to describe curated Docker images without introducing a second profile system
- images should remain simple enough to build and validate on a single node without a full registry-control service

## Requirements

1. **Make the smallest useful profile the default sandbox image path.**
   - Acceptance criteria:
     - the default image for new sandboxes maps to a minimal `core` or `runtime` profile rather than a browser-heavy image.
     - a freshly created default sandbox can boot, exec commands, and use its workspace without requiring browser tooling or inner Docker.
     - browser dependencies are not pulled into the default profile implicitly.

2. **Split broad images into a small curated profile set.**
   - Acceptance criteria:
     - curated image profiles cover at least `core`, `runtime`, `browser`, `container`, and `debug`.
     - each profile has a clear purpose and additive contents.
     - profile selection is explicit in presets, examples, and policy validation.

3. **Remove Docker-in-container from the normal tenant path.**
   - Acceptance criteria:
     - the default image path does not install the Docker engine or Docker CLI unless the selected profile is `container`.
     - the `container` profile is explicitly marked as heavier and riskier than the default path.
     - docs and policy make clear that inner Docker is an opt-in compatibility mode, not a baseline feature.

4. **Keep browser tooling isolated to browser-focused profiles.**
   - Acceptance criteria:
     - Playwright or other browser stacks live only in `browser` images.
     - the default non-browser image is materially smaller than the current Playwright-based default.
     - examples and presets request browser images only when they actually need that capability.

5. **Make image capabilities machine-readable and enforceable.**
   - Acceptance criteria:
     - curated images publish lightweight metadata describing profile, capabilities, and any dangerous features.
     - service-side validation rejects mismatches between requested profile and actual image metadata.
     - production policy can block `debug` and `container` profiles unless explicitly allowed.

6. **Prefer digest-pinned curated images for production selection.**
   - Acceptance criteria:
     - config and examples prefer pinned image references or local artifacts that resolve deterministically.
     - operator docs explain how to update curated images without silently drifting to different contents.
     - the allowed-image policy can restrict sandbox creation to the curated profile set.

7. **Keep the profile system lightweight.**
   - Acceptance criteria:
     - the plan does not create dozens of feature combinations or a second package-management control plane.
     - profile metadata is simple enough to validate in Go tests and at sandbox-create time.
     - build and validation steps remain feasible on a single host without an external artifact catalog service.

## Non-functional constraints

- Favor fewer, smaller curated images over an open-ended toggle matrix.
- Keep default boot time and image pull size lower than the current broad image path.
- Preserve existing preset and example flows through additive changes where possible.
- Keep policy safe by default: risky profiles must require explicit operator approval.
