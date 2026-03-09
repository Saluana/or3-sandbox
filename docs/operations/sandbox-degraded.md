# Sandbox Degraded

## Symptoms

- `runtime-health` marks a sandbox `degraded`
- the guest process is still alive but readiness or control checks are failing
- user operations begin timing out or partially failing

## Inspect

1. `go run ./cmd/sandboxctl runtime-health`
2. `go run ./cmd/sandboxctl inspect <sandbox-id>`
3. recent `sandbox.reconcile` and lifecycle audit events
4. host resource pressure and storage metrics

## Immediate actions

- determine whether the sandbox is still salvageable or should be snapshotted and replaced
- avoid forcing deletion until you understand whether the workspace must be preserved
- check whether the issue is isolated to one profile or one host image

## Recovery

- if the guest becomes healthy again after restart, record the incident and continue monitoring
- if the sandbox remains degraded, stop it, snapshot if possible, and restore to a fresh sandbox or approved image
- run the bounded abuse or recovery scripts if the degradation appears resource-related or restart-related
