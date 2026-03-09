# QEMU Production Threat Model

This document defines the production security claims for the hostile-workload `qemu` path in `or3-sandbox`.

## Supported claim

The supported hostile-production target is:

- a single Linux host
- KVM-backed QEMU guests
- agent-based guest control on approved guest profiles
- JWT-authenticated control-plane access

`docker` is not the hostile multi-tenant production boundary in this repository.

## Adversaries

The production threat model assumes the operator may face:

- untrusted sandbox workloads running inside guest operating systems
- tenants attempting cross-tenant access through control-plane or tunnel misuse
- guest workloads trying to escape the guest boundary or interfere with host state
- guest workloads trying to consume excessive CPU, memory, disk, PID, or tunnel resources
- accidental operator mistakes such as running a debug image without realizing it is dangerous

## Trust boundaries

Primary boundaries:

- host control plane: `sandboxd`, SQLite metadata, filesystem roots, host secrets
- guest boundary: each QEMU VM and its attached workspace disk
- tenant boundary: per-tenant authorization, quotas, and audit scopes
- tunnel boundary: explicit tunnel creation and revocation, not implicit host exposure

The guest agent is inside the guest boundary. It is trusted only to the same degree as the guest image profile and must not cause the host to trust a guest more than the host-side image contract allows.

## In-scope protections

The current production claims rely on these controls:

- immutable approved guest image paths with sidecar contracts and checksums
- fixed guest profiles with explicit capability declarations
- agent-first control for production-default images
- explicit opt-in for SSH compatibility and dangerous debug profiles
- JWT auth for production control-plane access
- per-tenant quotas, audit events, and explicit tunnel policy
- bounded host-facing exposure: loopback-only guest control paths and explicit tunnels

## Out-of-scope claims

This project does not currently claim:

- that Docker is a hostile multi-tenant production boundary
- that arbitrary self-built guest images are safe without sidecar contracts and profile review
- that debug or SSH-bearing images are production-safe by default
- that a distributed control plane or multi-host scheduler exists
- that every host kernel / hypervisor combination is production-supported

## Guest-agent role

The guest agent exists to reduce the long-term reliance on SSH for production control.

It is responsible for:

- readiness probing
- command execution
- PTY sessions
- workspace file operations
- shutdown requests

It is not a trust anchor by itself. The host still makes policy decisions using:

- approved image paths
- sidecar contracts
- profile policy
- control-mode validation

## Dangerous profiles

`debug` and any other future dangerous profiles are outside the production-default posture.

They require explicit operator approval because they may include:

- SSH servers
- passwordless sudo
- extra troubleshooting tooling
- broader guest convenience features that reduce density or hardening

## Operational gates

Before using production language, operators should run:

- the CI-friendly package smoke path
- `sandboxctl doctor --production-qemu`
- guest image smoke validation for the selected profile
- restart, snapshot, and recovery drills on a prepared Linux/KVM host

If those gates are not met, the environment should be treated as development or pre-production.
