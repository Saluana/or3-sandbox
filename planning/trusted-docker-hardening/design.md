# Design

## Overview

The current Docker runtime in `internal/runtime/docker/runtime.go` already centralizes container creation. That makes it the right place for a surgical hardening pass.

The design goal is to harden the existing trusted Docker path without pretending it is equivalent to VM-backed isolation:
- keep Docker CLI shell-outs for now
- tighten the default `docker create` arguments materially
- express any elevated or dangerous behavior through the existing service policy and capability model

This fits the repo because it uses existing packages and keeps the runtime behavior bounded.

## Affected areas

- `internal/runtime/docker/runtime.go`
  - add default non-root, capability drop, security opts, read-only rootfs, and tighter mounts
- `internal/runtime/docker/runtime_test.go`
  - verify the resulting Docker argument construction and fail-closed cases
- `internal/service/service.go`
  - persist any explicit elevated capability request only if supported
- `internal/service/policy.go`
  - deny dangerous Docker capabilities in normal trusted mode
- `internal/model/model.go`
  - use existing `Features`/`Capabilities` to model any explicit dangerous opt-ins, if needed
- `internal/config/config.go`
  - add only minimal config knobs for Linux security profiles or dangerous Docker overrides if necessary
- `docs/runtimes.md`
  - explain the hardened-but-trusted Docker posture
- `docs/operations/dangerous-profile-misuse.md`
  - extend operator guidance for elevated Docker requests if any are allowed at all

## Control flow / architecture

The hardened Docker path should still follow the current service flow:

1. `service.CreateSandbox` validates the request.
2. policy code denies dangerous capability requests unless explicitly allowed.
3. `docker.Runtime.Create` builds a hardened `docker create` command.
4. the runtime starts the container with a narrow writable surface and minimal privileges.
5. runtime health and audit events expose when a sandbox used a dangerous override.

No separate security controller is needed.

### Default Docker create posture

The normal trusted Docker command should add or tighten arguments in these categories:
- `--user <uid:gid>`
- `--cap-drop ALL`
- `--security-opt no-new-privileges:true`
- `--read-only`
- `--tmpfs /tmp:rw,nosuid,nodev,noexec`
- explicit writable mounts only for service-owned paths
- existing CPU, memory, and PID limits remain in place

Where supported on Linux, the runtime may also add:
- a seccomp profile via `--security-opt seccomp=...`
- an AppArmor profile via `--security-opt apparmor=...`
- SELinux label options where the host is configured for it

### Dangerous override model

To stay lightweight, do not add a large privilege matrix.

If an override is needed, express it through a tiny set of explicit capabilities such as:
- `docker.elevated-user`
- `docker.extra-cap:<name>`

The service policy should reject these by default and only allow them in explicit trusted admin scenarios. The normal tenant API should never silently request them.

### Filesystem layout

The service already creates:
- sandbox storage root
- workspace root
- cache root

The Docker runtime should map those to a tighter in-container layout:
- rootfs: read-only
- `/workspace`: writable bind mount
- `/cache`: writable bind mount when configured
- `/tmp`: tmpfs

Avoid broad writable root or extra host mounts.

## Data and persistence

### SQLite changes

No schema change is required for the basic hardening pass.

If dangerous overrides are modeled through existing `capability_set`, those already persist on `sandboxes`.

### Config and environment

Keep config changes minimal and Linux-focused.

Possible additive knobs:
- default Docker UID/GID or username mapping for curated images
- optional seccomp profile path
- optional AppArmor profile name
- explicit flag to allow dangerous Docker overrides in trusted environments only

Do not add a large per-container security policy language.

### Session and memory implications

None for session state. Runtime memory impact should be neutral or slightly better due to read-only rootfs and smaller writable surfaces.

## Interfaces and types

Likely internal helpers inside `internal/runtime/docker/runtime.go`:

```go
type dockerSecurityOptions struct {
    User          string
    ReadOnlyRoot  bool
    TmpfsMounts   []string
    SecurityOpts  []string
    CapDrop       []string
    CapAdd        []string
}
```

This can remain internal to keep the public model small.

## Failure modes and safeguards

- **image/user mismatch**
  - fail create with a clear error if the configured non-root user cannot execute the workload
- **platform lacks a Linux primitive**
  - log a warning and expose it in docs/doctor for trusted development, but do not claim the control was applied
- **dangerous override abuse**
  - deny by default in service policy and audit any approved usage
- **read-only root breaks a workflow**
  - only add back narrow writable paths, not a general writable rootfs escape hatch
- **path traversal in bind mounts**
  - continue canonicalizing service-owned mount roots and reject empty or invalid paths

## Testing strategy

- unit tests in `internal/runtime/docker/runtime_test.go`
  - command args include `--user`, `--cap-drop=ALL`, `--read-only`, and security opts
  - dangerous capability requests are denied or translated correctly
  - mount list stays limited to service-owned roots
- service tests in `internal/service/service_test.go`
  - dangerous Docker overrides are blocked by default
  - allowed overrides are audited when explicitly enabled
- config tests in `internal/config/config_test.go`
  - Linux-specific security profile paths validate cleanly
- documentation verification
  - ensure runtime docs and dangerous-profile docs match the actual flags and limitations
