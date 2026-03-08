# QEMU Preset Parity Tasks

## 1. Lock the shared preset contract first [R1, R2, R10]

- [x] Reuse the shared `internal/presets` manifest schema from the Docker track instead of creating a QEMU-specific schema.
- [x] Add runtime-selection and optional guest-profile hints to the shared manifest only if needed for honest QEMU compatibility.
- [x] Document that shared manifest semantics must remain aligned across Docker and QEMU.

## 2. Add a QEMU runtime adapter, not a second runner [R4, R10]

- [x] Add a QEMU capability/compatibility adapter in shared preset-runner code.
- [x] Keep manifest loading, env resolution, file uploads, step sequencing, readiness scaffolding, artifact handling, and cleanup in shared code.
- [x] Add tests that verify QEMU reuses the shared runner rather than a separate orchestration path.

## 3. Define guest-profile or image compatibility rules [R3]

- [x] Define how a preset identifies a compatible QEMU guest base image or guest profile.
- [x] Add early validation for presets that assume arbitrary container-image behavior incompatible with the current guest-image path.
- [x] Document how operators prepare or select example-ready guest images for QEMU examples.

## 4. Stage guest readiness separately from app readiness [R6, R8]

- [x] Reuse current QEMU runtime boot/readiness signals to detect guest-ready state.
- [x] Add runner behavior that waits for guest-ready before bootstrap/app steps begin.
- [x] Reuse the shared readiness framework for app/service readiness after guest-ready is achieved.
- [x] Add QEMU-focused tests for guest boot failure, guest-ready/app-not-ready, and successful readiness.

## 5. Reuse shared env and workspace seeding behavior [R5]

- [x] Reuse the shared env input handling and secret redaction logic from the Docker track.
- [x] Reuse the shared file-upload logic for repo-local and inline assets.
- [x] Add only QEMU-specific tests where guest compatibility affects those steps.

## 6. Reuse shared binary artifact transfer [R7]

- [x] Reuse the shared binary-safe file transfer support added in the common API/service/model path.
- [x] Validate artifact retrieval from QEMU guests with a focused integration test once a compatible guest example exists.
- [x] Avoid introducing a second artifact-transfer implementation for QEMU.

## 7. Reuse shared tunnel and endpoint behavior [R6]

- [x] Reuse the same tunnel APIs and user-facing endpoint printing as the Docker track.
- [x] Add QEMU-specific tests only for guest/service startup interactions that affect tunnel readiness.
- [x] Keep tunnel orchestration logic shared unless a true guest-specific difference requires a small adapter hook.

## 8. Ship QEMU-appropriate example presets [R9]

- [x] Create at least one QEMU example preset that demonstrates bootstrap/tooling behavior using a documented guest image or profile.
- [x] Create a QEMU service/tunnel example only when the guest profile reliably supports it.
- [x] Create an artifact example only when the guest/browser prerequisites are real and documented.
- [x] Ensure each QEMU example README clearly lists guest image prerequisites and host requirements.

## 9. Document parity and honest differences [R2, R3, R10]

- [x] Update preset docs to explain that Docker and QEMU share the same preset UX but may differ in packaging and startup latency.
- [x] Document guest-profile expectations for QEMU examples in `examples/README.md` or runtime-specific example READMEs.
- [x] Document that QEMU uses a stronger isolation boundary, with different image assumptions than Docker.

## 10. Validate the QEMU track [R2, R6, R7, R8]

- [x] Add unit coverage for QEMU compatibility checks and guest-ready/app-ready staging.
- [x] Add host-dependent integration coverage for at least one QEMU preset path.
- [x] Add more QEMU integration coverage only where it tests QEMU-specific behavior not already covered by the shared runner tests.
- [x] Document the host prerequisites for running QEMU preset integration coverage.

## 11. Out of scope for this phase

- [ ] Duplicate the Docker runner logic for QEMU.
- [ ] Pretend arbitrary Docker images map directly onto the current QEMU guest-image path.
- [ ] Add a separate preset schema just for QEMU.
- [ ] Add daemon-side job persistence before the shared CLI runner is proven.
