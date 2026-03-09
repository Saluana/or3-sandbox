# Requirements

## Overview

This plan shifts the sandbox runtime model away from treating plain Docker as the production isolation boundary.

Scope:
- define lightweight runtime classes that capture trust and isolation posture
- keep Docker as a trusted/local-development compatibility path
- make VM-backed runtimes the only supported production isolation mode
- introduce a runtime adapter model that is not shaped around `docker create/start/exec`
- preserve the current single-process Go daemon and SQLite-backed control plane

Assumptions:
- the repo already has `docker` and `qemu` runtime backends, so the first implementation wave should build on those instead of introducing a new orchestrator
- `qemu` is the first supported VM-backed production class in this repo
- a future Kata/containerd adapter may use the same abstraction, but the first plan should not block on shipping Kata support

## Requirements

1. **Define explicit runtime classes for trust and policy decisions.**
   - Acceptance criteria:
     - the system defines runtime classes that separate trust posture from implementation detail, at minimum covering `trusted-docker` and `vm`.
     - the selected runtime class is visible through config, persisted sandbox metadata, and runtime inspection output.
     - policy decisions about production eligibility are based on runtime class, not on ad hoc checks spread across unrelated packages.

2. **Make VM-backed isolation the only supported production boundary.**
   - Acceptance criteria:
     - `SANDBOX_MODE=production` rejects any runtime selection whose class is not VM-backed.
     - Docker remains available for development and explicitly trusted operator environments only.
     - operator-facing docs and doctor output consistently state that plain Docker is not the hostile multi-tenant boundary.

3. **Keep Docker as a compatibility path, not the default security product.**
   - Acceptance criteria:
     - the Docker backend is documented and validated as `trusted-docker` only.
     - the Docker backend can still support local development, image compatibility checks, and optional trusted control-surface use.
     - production runbooks and config validation do not present Docker as an equivalent alternative to VM-backed isolation.

4. **Introduce a runtime adapter layer that models sandbox concepts directly.**
   - Acceptance criteria:
     - the runtime layer uses request/response types that model sandbox lifecycle, storage attachments, and network attachments rather than raw Docker CLI verbs.
     - existing service code can keep using a bounded Go interface, while adapters translate that into backend-specific behavior.
     - adding a future VM-backed adapter does not require threading Docker-specific assumptions through service or API code.

5. **Represent sandbox, workload, storage, and network separately in the adapter contract.**
   - Acceptance criteria:
     - the adapter contract distinguishes the sandbox instance from container or guest workload details.
     - storage and network attachments are represented as typed configuration in the adapter layer.
     - the adapter contract can express both the existing Docker backend and the existing QEMU backend without lossy backend-specific escape hatches.

6. **Preserve backward compatibility for the current API and persisted sandboxes.**
   - Acceptance criteria:
     - the public HTTP API does not require breaking request changes for current sandbox create/start/stop flows.
     - persisted sandbox rows remain readable after any additive schema changes.
     - existing Docker and QEMU sandboxes created before the change reconcile safely, using sensible runtime-class defaults when explicit metadata is missing.

7. **Keep the abstraction lightweight and deterministic.**
   - Acceptance criteria:
     - the first implementation does not add Kubernetes, containerd as a mandatory dependency, or a separate scheduler service.
     - the adapter layer stays inside the existing Go process and preserves bounded command execution and bounded output behavior.
     - the refactor does not increase steady-state memory use materially for the common single-node case.

8. **Prepare, but do not overcommit to, a future Kata/containerd adapter.**
   - Acceptance criteria:
     - the adapter model and naming can accommodate a future Kata/containerd-backed VM runtime class.
     - the first implementation wave does not require Kata/containerd packages, daemons, or migrations to ship the boundary fix.
     - docs clearly distinguish what is implemented now (`qemu` as the VM-backed production class) from what is a future extension point.

## Non-functional constraints

- Keep the design compatible with the current single-process daemon and SQLite model.
- Favor small, additive schema and config changes over a large control-plane rewrite.
- Preserve deterministic reconciliation after daemon restart.
- Avoid increasing hot-path latency for sandbox lifecycle calls beyond what the existing backends already incur.
- Keep runtime selection and policy decisions safe by default: production must fail closed to VM-backed classes only.
