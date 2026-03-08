# Production Readiness Design

## 1. Overview

The production-grade path for `or3-sandbox` should build on the existing Go daemon, SQLite state model, and QEMU runtime rather than replacing them with a large new orchestration stack.

The design goal is not “become a cluster platform.”

The design goal is:

- keep the current single-node control plane credible
- make QEMU the supported production isolation boundary
- preserve Docker as the cheaper trusted-mode option instead of stretching it into a false production-security claim
- add enterprise identity, policy, secret handling, audit, and operations capabilities in a way that fits the current architecture
- raise runtime confidence through better recovery behavior, observability, and verification

This approach fits the repo because:

- the daemon already centralizes lifecycle and policy decisions in `internal/service`
- auth is already mediated through `internal/auth`
- config validation already controls runtime safety at startup in `internal/config`
- runtime behavior is already abstracted behind `model.RuntimeManager`
- the current SQLite schema already tracks the major entities needed for audit, quotas, runtime state, and snapshots

## 2. Affected areas

- `internal/auth/`
  - replace static-token-only production posture with a pluggable enterprise auth path
  - keep development compatibility if static tokens remain
- `internal/config/config.go`
  - add production-oriented auth, TLS, secret-loading, and policy configuration with conservative defaults
- `internal/service/service.go`
  - enforce new policy checks, lifecycle guardrails, and more explicit recovery behavior
- `internal/repository/store.go`
  - extend storage for roles, service identities, auth metadata, or policy state only where necessary
- `internal/api/router.go`
  - expose small administrative inspection surfaces for health, auth, and policy visibility if needed
- `internal/runtime/qemu/`
  - continue hardening runtime reliability, suspend or resume behavior, recovery, and observability
- `internal/runtime/docker/`
  - remain trusted-mode only; avoid pretending it is the production isolation path
- `cmd/sandboxd/main.go`
  - wire the production auth, TLS, and runtime validation choices cleanly at startup
- `cmd/sandboxctl/main.go`
  - add any missing operator inspection or auth flows needed for enterprise use
- `images/guest/`
  - turn the guest image path into a more explicit, versioned, production-documented contract
- `docs/` and `planning/`
  - add operator docs, deployment runbooks, backup guidance, and truthful readiness statements

## 3. Control flow / architecture

### 3.1 Production request flow

1. A user or automation client authenticates through the configured production identity path.
2. `internal/auth` validates the identity and resolves tenant, roles, and policy context.
3. `internal/service` evaluates quota and policy rules before touching runtime state.
4. The selected runtime must be `qemu` for supported untrusted production execution.
5. Runtime operations proceed through the existing lifecycle methods and return observable runtime state.
6. Audit events and metrics are emitted for the action.

### 3.1.1 Runtime positioning

The runtime story should stay simple enough for operators to remember and accurate enough for production decisions:

- use `docker` for lower-cost, higher-density, trusted internal workloads
- use `qemu` for stronger isolation when running untrusted or security-sensitive workloads

That guidance must appear in operator-facing docs and startup validation so cost optimization does not quietly weaken the intended security boundary.

### 3.2 Auth model

The current static token model is good for development but weak for enterprise production.

A repo-aligned production auth model should be layered, not replaced wholesale:

- **Development mode**: existing static token map from config remains available
- **Production mode**: OIDC-backed JWT validation or equivalent bearer validation is added
- **Automation mode**: service identities use scoped credentials distinct from human users

Authorization should remain local to the daemon and should not require a separate policy microservice.

Likely model:

- identity resolves to tenant plus role set
- role set maps to permissions for lifecycle, files, snapshots, tunnels, and admin inspection
- policy checks happen inside `internal/service`

### 3.3 TLS and deployment model

The smallest production-ready model is to support both:

- in-process TLS for straightforward deployments
- documented reverse-proxy TLS termination for operators who prefer a front proxy

The plan should not require building a second daemon just for ingress.

Key expectation:

- all production guidance must define trusted headers and allowed proxy deployment shapes clearly

### 3.4 Policy enforcement

Use a small policy layer inside the existing service flow rather than a standalone engine.

Likely enforcement points:

- sandbox creation
- start or resume
- tunnel creation
- snapshot export if enabled
- any admin-only runtime health or inspection actions

Likely policy types:

- allowed guest images or image families
- max sandbox lifetime or idle timeout
- tunnel restrictions by tenant or role
- production mode restrictions that prevent Docker-backed untrusted operation

### 3.5 Runtime hardening model

Continue to harden QEMU in place instead of inventing a second production backend.

Key runtime areas:

- guest readiness reliability
- recovery from failed boots
- suspend or resume correctness
- runtime state reconciliation after daemon restart
- stronger visibility into guest health and runtime artifact state
- explicit handling for partial snapshot operations and disk pressure

### 3.6 Observability model

The daemon already emits structured logs through `slog`.

Production readiness should extend this with:

- stable event fields for tenant, sandbox, runtime backend, and outcome
- metrics or counters suitable for scrape-based monitoring
- clear operator health endpoints or admin inspection endpoints
- storage and capacity visibility tied to existing SQLite-backed usage views

### 3.7 Backup and recovery model

Stay local-first and operator-friendly.

Protected data in the current architecture includes:

- SQLite database
- sandbox storage root
- snapshot root
- optional exported snapshot bundles
- guest image artifacts and operator-provided secrets

A production runbook should define:

- cold backup
- hot backup expectations if supported
- restore ordering
- compatibility checks for snapshot and guest image versions

## 4. Data and persistence

### 4.1 SQLite changes

SQLite should remain the system of record in this phase.

Potential schema additions may be needed for:

- service identities
- roles or permission grants
- auth issuer metadata
- policy records if policy must be stored persistently rather than loaded from config
- more detailed audit metadata

Guiding rule:

- prefer small additive migrations over redesigning core tables
- preserve current sandboxes, snapshots, quotas, and runtime state rows

### 4.2 Config and secrets

Likely new config areas:

- auth mode selection
- OIDC issuer and audience settings or equivalent
- TLS certificate or key paths, or reverse-proxy trusted mode toggles
- allowed image families or production policy settings
- secret-loading locations for production deployments

All new config should validate at startup the same way runtime config already does.

### 4.3 Session implications

No new application-level session manager is needed.

The service should continue treating each request as authenticated, authorized, and tenant-scoped, with bounded operations and explicit runtime state persisted in SQLite.

## 5. Interfaces and types

### 5.1 Auth interfaces

A small abstraction is enough:

```go
type Identity struct {
    Subject   string
    TenantID  string
    Roles     []string
    IsService bool
}

type Authenticator interface {
    Authenticate(r *http.Request) (Identity, error)
}
```

This lets the current middleware support both static tokens and enterprise auth backends.

### 5.2 Policy surface

Policy evaluation can stay local to `internal/service`:

```go
type PolicyContext struct {
    Identity Identity
    Tenant   model.Tenant
    Quota    model.TenantQuota
}
```

Simple helpers should answer:

- may create sandbox?
- may create tunnel?
- may use image?
- may inspect runtime health?

### 5.3 Metrics and audit

Do not build a complex telemetry framework.

Instead:

- extend structured logging fields
- expose small counters or gauges in a scrape-friendly format
- keep audit events in SQLite with stable semantics

## 6. Failure modes and safeguards

- **Bad enterprise auth config**: fail fast at startup.
- **Expired or invalid credentials**: reject requests cleanly without falling back to insecure modes.
- **Secret leakage**: ensure logs and API responses never include private keys, auth secrets, or tunnel credentials beyond intended one-time visibility.
- **Proxy misconfiguration**: document and validate trusted proxy modes clearly.
- **Guest runtime degradation**: preserve state conservatively and avoid destructive cleanup on uncertain runtime inspection.
- **Policy ambiguity**: prefer deny-by-default for production-sensitive actions.
- **Storage exhaustion**: keep deterministic disk-full behavior and visible operator signals.
- **SQLite recovery risk**: document backup cadence and recovery ordering clearly for production use.

## 7. Testing strategy

- Add unit tests for enterprise auth config validation and role or policy enforcement.
- Add integration coverage for authorized versus unauthorized snapshot, file, tunnel, and admin operations.
- Keep extending QEMU runtime tests and host integration tests for recovery and failure drills.
- Add production smoke tests for TLS-enabled or reverse-proxy deployment mode if practical.
- Ensure the packages that define production readiness claims have stable passing test coverage.
- Track and resolve the current API integration failure before any enterprise-ready claim is made.

## 8. Rollout strategy

Recommended order:

1. fix failing production-critical tests and stabilize truth-in-docs
2. make QEMU the explicit supported production runtime path
3. add enterprise auth and authorization
4. add TLS and secret-loading support
5. add policy enforcement and audit improvements
6. add operator runbooks, backup guidance, and deployment docs
7. complete production validation and readiness signoff
