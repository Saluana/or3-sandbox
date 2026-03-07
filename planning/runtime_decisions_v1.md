# Sandbox v1 Runtime Decisions

## Selected implementation in this repository

The current codebase implements the sandbox runtime on top of Docker.

Rationale:

- durable writable layer per sandbox through a persistent container
- simple stop, start, pause, resume, exec, and attach primitives
- per-sandbox bridge networks for east-west isolation
- workspace and cache mounts for explicit persistent storage
- a runtime surface that can be exercised end-to-end in integration tests

## Operational assumptions

Operators need:

- Docker installed and reachable by the control-plane process
- local filesystem roots for SQLite, sandbox storage, and snapshots
- Linux-oriented guest images for tenant environments

## Security posture

This repository’s runtime is Docker-backed and intended for trusted single-operator or development deployments.

For stricter hostile multi-tenant production isolation, the control-plane surface is already separated behind the `RuntimeManager` abstraction so a stronger guest backend can replace the Docker runtime without changing the HTTP or CLI contract.

## Scope decisions frozen for v1

- lifecycle states are fixed to `creating`, `stopped`, `starting`, `running`, `suspending`, `suspended`, `stopping`, `deleting`, `deleted`, and `error`
- create requests include image reference, limits, network mode, and tunnel policy
- supported lifecycle operations are create, inspect, start, stop, suspend, resume, delete, exec, terminal attach, snapshots, and tunnels
- active storage is local persistent writable system state plus persistent workspace storage
- snapshot export is optional and additive
- inbound access is explicit tunnel creation only
