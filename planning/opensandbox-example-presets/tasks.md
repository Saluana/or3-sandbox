# OpenSandbox-Style Example Presets Tasks

## 1. Confirm scope and preset contract [R1, R2, R10]

- [ ] Define the initial preset manifest schema in a new `internal/presets` package.
- [ ] Choose YAML or JSON for manifest storage and document the reason in code comments and docs.
- [ ] Decide and document that initial shipped presets are Docker-oriented and do not promise arbitrary-image parity on the current QEMU path.
- [ ] Add an "Out of scope" note covering OpenSandbox features not included in the first pass, such as network policy parity and SDK-level sandbox objects.

## 2. Add preset parsing and validation [R1, R5, R6, R7]

- [ ] Create `internal/presets/manifest.go` with Go types for sandbox defaults, inputs, files, steps, readiness checks, tunnels, artifacts, and cleanup policy.
- [ ] Create `internal/presets/load.go` to discover and load preset manifests from a top-level `examples/` directory.
- [ ] Add validation for invalid durations, duplicate input names, unsupported readiness types, missing startup/tunnel pairings, and illegal artifact definitions.
- [ ] Add unit tests for manifest parsing and validation errors.

## 3. Build a CLI-side preset runner [R2, R3, R4, R5, R7, R9]

- [ ] Add `sandboxctl preset list`, `sandboxctl preset inspect`, and `sandboxctl preset run` in [cmd/sandboxctl/main.go](cmd/sandboxctl/main.go).
- [ ] Add a runner package or helper in `cmd/sandboxctl` that sequences sandbox creation, file uploads, exec/bootstrap steps, detached startup, readiness checks, tunnel creation, artifact downloads, and cleanup.
- [ ] Add flags for cleanup policy, sandbox preservation, preset override values, and optional env injection.
- [ ] Ensure the runner prints bounded progress output and always surfaces the sandbox ID.
- [ ] Add CLI tests covering successful sequencing and early validation failures.

## 4. Add env input handling for preset commands [R5]

- [ ] Implement required and optional preset input resolution from process env and CLI overrides.
- [ ] Support secret-marked inputs without echoing values in logs or progress output.
- [ ] Thread resolved env values into exec and detached startup requests.
- [ ] Add tests for missing required env, defaulted optional env, and secret redaction behavior.

## 5. Add workspace seeding support [R6]

- [ ] Add support for preset-managed text assets from inline content and repo-local files.
- [ ] Reuse existing upload/file APIs instead of adding a new bulk file service.
- [ ] Add path normalization and deterministic file application order.
- [ ] Add tests for inline asset writes, repo-local asset writes, and traversal rejection.

## 6. Add binary-safe artifact transfer [R8]

- [ ] Extend [internal/model/model.go](internal/model/model.go) file transfer models to support a backward-compatible binary mode.
- [ ] Update [internal/service/service.go](internal/service/service.go) and [internal/api/router.go](internal/api/router.go) to serve binary-safe file reads and writes.
- [ ] Extend `sandboxctl download` and the preset runner to decode and write binary artifacts correctly.
- [ ] Add regression tests for PNG or other non-UTF-8 binary transfers while keeping current text flows unchanged.

## 7. Implement readiness and tunnel orchestration [R4, R7]

- [ ] Add runner support for command-based readiness probes with timeout and retry interval.
- [ ] Add runner support for HTTP readiness probes using created tunnels.
- [ ] Reuse existing tunnel APIs for endpoint creation instead of adding a new daemon-side gateway abstraction.
- [ ] Add tests for successful readiness, timeout behavior, and tunnel endpoint reporting.

## 8. Ship the first preset set [R10]

- [ ] Create `examples/claude-code/` with a manifest, README, and any helper assets needed for a CLI-oriented coding example.
- [ ] Create `examples/playwright/` with a manifest, README, and any helper assets needed for a screenshot/artifact example.
- [ ] Create `examples/openclaw/` or a repo-appropriate service/gateway example with a manifest, README, and readiness/tunnel configuration.
- [ ] Keep each preset small and explicit about required env vars, produced outputs, and cleanup behavior.

## 9. Document repo-native differences from OpenSandbox [R10]

- [ ] Update [docs/usage.md](docs/usage.md) with preset-runner commands and examples.
- [ ] Update [docs/api-reference.md](docs/api-reference.md) if binary transfer semantics or related file endpoints change.
- [ ] Add a top-level `examples/README.md` explaining the preset catalog, prerequisites, and runtime assumptions.
- [ ] Document where `or3-sandbox` intentionally differs from OpenSandbox: CLI orchestration instead of SDK orchestration, Docker-focused initial presets, and no first-pass network policy parity.

## 10. Validate with focused integration coverage [R2, R4, R8, R10]

- [ ] Add a Docker-backed smoke or integration test for one CLI example preset.
- [ ] Add a Docker-backed smoke or integration test for one artifact-download preset.
- [ ] Add a Docker-backed smoke or integration test for one readiness-plus-tunnel preset.
- [ ] Document prerequisites for any host-dependent tests so CI and local runs can distinguish unit coverage from Docker integration coverage.

## 11. Out of scope for this phase

- [ ] Add a Python SDK that mirrors OpenSandbox's `Sandbox.create(...)` API directly.
- [ ] Add Kubernetes or pooled sandbox orchestration.
- [ ] Add network policy parity with OpenSandbox examples in the first pass.
- [ ] Add persistent daemon-side workflow jobs or SQLite-backed preset-run history before the CLI-first flow is proven.
