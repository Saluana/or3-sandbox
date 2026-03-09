# Tasks

## 1. Tighten the Docker create path defaults (Req 1, 2, 3, 4, 5, 6)

- [x] Update `internal/runtime/docker/runtime.go` so the normal create path runs as an explicit non-root user.
- [x] Add `cap-drop=ALL`, `no-new-privileges`, and `read-only` rootfs defaults to the Docker create arguments.
- [x] Add a bounded tmpfs mount for `/tmp` and keep writable bind mounts limited to service-owned workspace and cache paths.
- [x] Keep canonical host-path validation for all bind mounts and reject invalid or empty mount roots.

## 2. Add Linux hardening primitives without overcomplicating the runtime (Req 2, 3, 7)

- [x] Add optional seccomp profile support in `internal/runtime/docker/runtime.go` and `internal/config/config.go`.
- [x] Add optional AppArmor or SELinux integration using simple config values rather than a new policy engine.
- [x] Ensure the runtime emits clear warnings or test-visible behavior when a host cannot apply a requested Linux hardening primitive.

## 3. Deny dangerous Docker modes by default (Req 2, 6, 8)

- [x] Update `internal/service/policy.go` to reject privileged-mode-equivalent requests and any host namespace sharing requests.
- [x] Ensure the trusted Docker path never mounts the Docker socket by default.
- [x] If any elevated Docker override is still needed, model it through a tiny explicit capability list and audit it in `internal/service/audit.go`.
- [x] Add service tests in `internal/service/service_test.go` covering default denial and explicit override audit behavior.

## 4. Align curated images with the non-root model (Req 1, 4, 8)

- [x] Review the curated Docker image path used by this repo and ensure it defines a predictable non-root user.
- [x] Update image docs or manifests so the runtime can rely on that user consistently.
- [x] Add regression tests or smoke checks that a default sandbox can still boot and exec under the non-root user.

## 5. Add runtime tests for command construction and failure cases (Req 1, 2, 3, 4, 5, 6, 7)

- [x] Expand `internal/runtime/docker/runtime_test.go` to assert the hardened Docker arguments.
- [x] Add tests for read-only rootfs plus writable mount layout.
- [x] Add tests for capability add-back behavior if any explicit override path exists.
- [x] Add tests that privileged mode and host namespace requests are rejected.

## 6. Update operator documentation (Req 3, 8)

- [x] Update `docs/runtimes.md` to describe the hardened `trusted-docker` posture and its limitations.
- [x] Update `docs/operations/dangerous-profile-misuse.md` or a Docker-focused runbook with operator guidance for elevated overrides.
- [x] Document which controls are Linux-enforced versus best-effort on local developer hosts.

## 7. Out of scope for this plan

- [ ] Do not claim hostile multi-tenant production isolation for plain Docker.
- [ ] Do not add a new Docker SDK integration layer unless the CLI path proves insufficient.
- [ ] Do not introduce a broad capability policy language.
- [ ] Do not solve real disk-quota enforcement here; track that in the storage/network/snapshot hardening plan.
