# Dangerous Profile Misuse

This runbook also covers dangerous trusted-Docker override usage.

## Symptoms

- a `debug` or other dangerous profile is present on a host that was meant to be production-default only
- `sandboxctl doctor --production-qemu` reports dangerous-profile findings
- audit or incident review shows SSH-bearing images in routine production traffic
- audit review shows `policy.create.override` events for trusted-Docker capability overrides
- sandboxes were created with `docker.elevated-user` or `docker.extra-cap:*`

## Inspect

1. approved image list and sidecar contracts
2. `SANDBOX_QEMU_ALLOW_DANGEROUS_PROFILES` and `SANDBOX_QEMU_ALLOW_SSH_COMPAT`
3. sandbox inventory by profile and image
4. recent deployment or image-rollout changes
5. `SANDBOX_DOCKER_ALLOW_DANGEROUS_OVERRIDES`
6. audit events containing `docker_capabilities=` or `docker_features=` details

## Immediate actions

- stop admitting new dangerous-profile sandboxes unless there is a break-glass exception
- identify all active dangerous-profile sandboxes and confirm business justification
- preserve audit events for the approval path and operator actions
- disable `SANDBOX_DOCKER_ALLOW_DANGEROUS_OVERRIDES` unless an active break-glass workflow requires it
- confirm no workflow is attempting host namespaces, privileged mode, or Docker socket mounts

## Recovery

- migrate affected workloads back to approved non-dangerous profiles where possible
- remove the allow flags after the break-glass window closes
- update the approval record and post-incident notes so the exception is auditable
- rotate or rebuild any image that depended on running as root by default; the trusted Docker path assumes an explicit non-root user
- on Linux hosts, verify seccomp/AppArmor/SELinux settings match the deployment intent; on macOS developer hosts, treat those controls as best-effort only
