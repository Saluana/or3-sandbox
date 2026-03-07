# What Is Left For Sandbox v1

## 1. Current State

The repository is usable today as a small single-node control plane with a Docker-backed sandbox runtime for trusted or development use.

It is not complete relative to the v1 architecture, requirements, and design documents.

The biggest reason is simple:

- the design calls for a guest-backed production runtime with a VM-style boundary
- the implementation only ships a Docker backend
- the config explicitly treats that Docker backend as trusted-only

This means "sandbox v1 is complete" is only true if "v1" means the current trusted Docker implementation, not the full production design written in the planning docs.

## 2. What Is Already In Place

These areas are substantially implemented:

- Go daemon, HTTP API, SQLite schema, auth, tenancy, and rate limiting
- sandbox CRUD lifecycle APIs
- exec streaming and PTY attach
- file operations inside the workspace
- tunnel creation and revocation
- local snapshot and restore
- quota checks based on requested resources
- restart reconciliation and cleanup flows
- CLI coverage for the main user paths

## 3. Major Gaps Versus The Design

### 3.1 Production multi-tenant runtime is missing

The design requires a guest-backed runtime for hostile multi-tenant workloads.

Evidence:

- `planning/architecture_v1.md` says production mode must use a strong guest boundary and a VM-style runtime.
- `planning/design_v1.md` says the design removes earlier container-per-tenant assumptions.
- `planning/runtime_decisions_v1.md` says the current Docker runtime is for trusted single-operator or development deployments.
- `internal/config/config.go` only accepts `docker` as a runtime backend and requires `SANDBOX_TRUSTED_DOCKER_RUNTIME=true`.

Impact:

- the core production isolation requirement from the design is not implemented
- host-kernel container isolation is still the active boundary
- the current backend is not the production runtime described in the architecture

### 3.2 Guest machine boot and bootstrap flow is not implemented

The design assumes guest creation, guest boot, and guest bootstrap.

Current behavior is much simpler:

- the Docker runtime creates a container
- it starts the container with `sleep infinity`
- there is no guest agent bootstrap contract
- there is no first-boot workflow
- there is no guest health handshake before marking the sandbox ready

Impact:

- the implementation does not yet match the "durable tenant machine" boot model from the design
- task list items that mention guest boot and bootstrap are overstated

### 3.3 Guest-local container engine support is only partial

The design requires Docker-in-Docker or an equivalent engine inside the tenant environment.

Current state:

- `images/base/Dockerfile` installs `docker.io`
- there is no verified daemon lifecycle or bootstrap for the inner engine
- there are no smoke tests proving guest-local containers work
- inner-engine storage is not counted toward sandbox quota

Impact:

- the capability is not finished end-to-end
- the current task list marks more of this as complete than the implementation proves

### 3.4 Browser automation support is not verified

The design requires headless browser automation inside the sandbox.

Current state:

- the default base image is Playwright-oriented
- the base image includes browser dependencies
- there are no dedicated browser smoke tests in the sandbox runtime path
- persistence and restart behavior for browser workloads is not verified

Impact:

- the repository may support browser workloads in practice
- the design requirement is not yet proven

### 3.5 Storage quotas are bookkeeping, not hard enforcement

The design requires per-sandbox quotas and aggregate storage limits.

Current state:

- quota checks sum requested `disk_limit_mb` values from SQLite
- the Docker runtime does not enforce an actual filesystem quota for the sandbox writable layer or workspace
- there is no accounting for inner-engine image and layer storage

Impact:

- storage limits are advisory at create time, not true runtime enforcement
- real disk exhaustion and quota escape paths are still open

### 3.6 S3 snapshot export is not implemented

The design treats S3-compatible export as optional, not required for first ship.

Current state:

- config has an optional snapshot export URI field
- the export and restore flow is not implemented

Impact:

- optional backup/export scope remains open

### 3.7 Failure-mode and verification coverage is incomplete

The design expects hardening and verification for the real workload model.

Still missing from the existing task list:

- Git workload verification
- Python package persistence verification
- npm package persistence verification
- browser automation verification
- guest-local container engine verification
- control-plane restart during exec drill
- guest boot failure drill
- disk-full drill
- runtime health inspection commands

Impact:

- the happy path is reasonably covered
- several important operational and workload claims are still unproven

### 3.8 Background service restart semantics are not demonstrated

The design expects the sandbox to behave like a durable computer and mentions a guest init or supervision mechanism.

Current state:

- the base image uses `tini`
- the runtime starts the container with `sleep infinity`
- there is no guest init system or documented service restart contract

Impact:

- one-off long-running processes work
- "installed background services restart after boot" is not yet established

### 3.9 Some task status is inaccurate

The existing task list is useful, but a few checked items read as complete when they are only partially satisfied or are satisfied only for the trusted Docker implementation.

Examples:

- "Finalize the production runtime boundary" is checked, but no guest-backed production backend is implemented
- "Boot the guest and run bootstrap" is checked, but there is no guest boot/bootstrap flow
- "Install and configure Docker-in-Docker or an equivalent guest-local engine" is checked, but only package installation is visible

## 4. Bottom Line

The codebase is not empty or broken. A lot of the control plane is there.

What is missing is the part that most directly matters for the original v1 promise:

- the production runtime boundary
- the guest machine semantics behind that boundary
- proof that the key workloads actually function in that environment

## 5. Recommended Positioning Right Now

Until the missing work lands, the honest product statement is:

- trusted or development-ready Docker-backed sandbox control plane: mostly yes
- production-ready hostile multi-tenant sandbox platform as described by the design docs: not yet

Next-step planning references:

- `planning/tasks2.md`
- `planning/onwards/requirements.md`
- `planning/onwards/design.md`
- `planning/onwards/tasks.md`
