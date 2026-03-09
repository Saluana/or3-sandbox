# Requirements

## Overview

This plan hardens the existing Docker runtime for the roles it should still serve: local development, compatibility validation, and explicitly trusted operator environments.

Scope:
- force least-privilege defaults in the Docker create path
- make writable filesystem and privilege surfaces explicit and minimal
- deny obviously unsafe modes by default
- preserve a lightweight implementation that still shells out to Docker for now

Assumptions:
- this plan does not try to make plain Docker the hostile multi-tenant production boundary
- Docker remains a supported `trusted-docker` path after the runtime-boundary plan lands
- platform-specific Linux hardening features may degrade gracefully on macOS development hosts, but Linux behavior must be strict and testable

## Requirements

1. **Run Docker sandboxes as non-root by default.**
   - Acceptance criteria:
     - the Docker runtime passes an explicit non-root user to `docker create` by default.
     - base images used for the trusted Docker path define a matching non-root user or predictable UID/GID mapping.
     - any elevated execution path requires explicit operator opt-in and is recorded in audit output.

2. **Drop capabilities and block privilege escalation by default.**
   - Acceptance criteria:
     - the Docker runtime applies `cap-drop=ALL` by default.
     - the runtime sets `no-new-privileges` for standard containers.
     - privileged containers are rejected by policy unless a narrowly scoped explicit override exists.
     - default capability additions are empty; any add-back path is small, explicit, and test-covered.

3. **Apply kernel-surface hardening where the platform supports it.**
   - Acceptance criteria:
     - the runtime applies seccomp configuration by default on supported Linux hosts.
     - AppArmor or SELinux integration is used when available without introducing a heavyweight new dependency.
     - unsupported host features produce clear warnings or skip behavior in trusted development mode instead of silently implying full protection.

4. **Make the filesystem model read-mostly by default.**
   - Acceptance criteria:
     - the container root filesystem is read-only by default.
     - writable paths are limited to explicit mounts such as `/workspace`, `/cache`, and bounded temp locations.
     - writable mount options use the most restrictive settings practical for each path, including `nodev`, `nosuid`, and `noexec` where compatible.

5. **Keep writable host mounts narrow and explicit.**
   - Acceptance criteria:
     - the Docker runtime does not expose arbitrary host bind mounts to tenants.
     - only the service-created sandbox workspace, cache, and runtime storage paths are mounted.
     - mount construction validates and canonicalizes host paths before passing them to Docker.

6. **Deny Docker modes that collapse the trust boundary.**
   - Acceptance criteria:
     - the runtime rejects privileged mode.
     - host PID, host network, and host IPC sharing are not used by default and require a separate, clearly dangerous path if ever supported.
     - the normal trusted Docker path does not mount the Docker socket into tenant sandboxes.

7. **Keep the Docker hardening path lightweight and deterministic.**
   - Acceptance criteria:
     - the first implementation remains within `internal/runtime/docker` and existing policy/config packages.
     - no new sidecar daemons, admission webhooks, or kernel agents are introduced.
     - hardening behavior is visible in unit tests and command construction without depending on an always-on integration environment.

8. **Document the trust limitations honestly.**
   - Acceptance criteria:
     - docs clearly state that these measures improve the trusted Docker path but do not turn it into the hostile production boundary.
     - operator guidance explains which hardening controls are mandatory on Linux and which are best-effort on developer hosts.

## Non-functional constraints

- Preserve the current Docker CLI-based runtime for this wave; do not replace it with a new Docker SDK layer unless needed for correctness.
- Keep changes focused on the create/start/exec lifecycle and policy checks.
- Avoid hardcoding Linux-only behavior in a way that breaks local macOS development.
- Fail closed for clearly unsafe Docker options; warn, rather than pretend, when a platform cannot supply a Linux hardening primitive.
