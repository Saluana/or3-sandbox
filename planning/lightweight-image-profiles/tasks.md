# Tasks

## 1. Standardize curated profile semantics across runtimes (Req 1, 2, 5, 7)

- [x] Confirm `core`, `runtime`, `browser`, `container`, and `debug` as the only curated profile names in code and docs.
- [x] Reuse `internal/model.GuestProfile` and avoid introducing a second image-profile vocabulary.
- [ ] Update `internal/service/service.go` and related helpers so profile validation follows the same policy semantics for Docker and QEMU.

## 2. Shrink the default image path (Req 1, 4, 6)

- [ ] Change the default `SANDBOX_BASE_IMAGE` posture in `internal/config/config.go` away from the broad Playwright image.
- [ ] Build or document a smaller curated `core` or `runtime` Docker image in `images/base/Dockerfile` or adjacent build assets.
- [ ] Verify a default sandbox can still create, start, and exec using the smaller image.
- [ ] Update docs in `docs/configuration.md` and `docs/usage.md` to reflect the new default.

## 3. Split heavyweight capabilities into explicit profiles (Req 2, 3, 4, 5)

- [ ] Keep browser tooling only in `browser` images and examples.
- [ ] Remove Docker-in-container from normal curated images and keep it only in the `container` profile.
- [x] Ensure `debug` remains a separate non-default troubleshooting profile.
- [ ] Update `examples/openclaw`, `examples/playwright`, and any other presets to request the smallest required profile explicitly.

## 4. Add lightweight image metadata validation (Req 2, 5, 6, 7)

- [ ] Define lightweight Docker image metadata using OCI labels or a small curated mapping in Go.
- [ ] Extend the Docker runtime or service validation path to load that metadata before sandbox creation.
- [x] Reuse existing guest-image contract validation for QEMU-backed profiles.
- [ ] Reject sandbox-create requests when requested profile, image metadata, and dangerous-profile policy do not align.

## 5. Tighten policy and allowed-image handling (Req 5, 6, 7)

- [ ] Update `internal/service/policy.go` so dangerous profiles such as `container` and `debug` are blocked by default in production.
- [ ] Strengthen `SANDBOX_POLICY_ALLOWED_IMAGES` usage in docs and tests so production examples prefer pinned curated image refs.
- [ ] Add config tests that cover minimal default images and allowed-image restriction behavior.

## 6. Add regression coverage (Req 1, 2, 3, 4, 5, 6)

- [ ] Add service tests for profile mismatch, missing metadata, and dangerous-profile denial.
- [ ] Add Docker runtime tests for image metadata parsing or curated mapping.
- [ ] Add preset tests ensuring browser and container examples do not fall back to the default lightweight image by mistake.
- [x] Add API integration tests for create-time validation errors.

## 7. Update documentation and examples (Req 2, 3, 4, 6)

- [ ] Update `docs/project-overview.md`, `docs/configuration.md`, and `docs/usage.md` with the curated image/profile story.
- [ ] Update example READMEs so they explain why a given example needs `browser` or `container`.
- [ ] Document the operator process for rebuilding and rolling curated images without silent drift.

## 8. Out of scope for this plan

- [ ] Do not build a full registry catalog service.
- [ ] Do not support arbitrary package-combination feature matrices.
- [ ] Do not add Docker-in-container back to the default path.
- [ ] Do not make browser tooling the default for all sandboxes.
