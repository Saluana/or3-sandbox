# Dangerous Profile Misuse

## Symptoms

- a `debug` or other dangerous profile is present on a host that was meant to be production-default only
- `sandboxctl doctor --production-qemu` reports dangerous-profile findings
- audit or incident review shows SSH-bearing images in routine production traffic

## Inspect

1. approved image list and sidecar contracts
2. `SANDBOX_QEMU_ALLOW_DANGEROUS_PROFILES` and `SANDBOX_QEMU_ALLOW_SSH_COMPAT`
3. sandbox inventory by profile and image
4. recent deployment or image-rollout changes

## Immediate actions

- stop admitting new dangerous-profile sandboxes unless there is a break-glass exception
- identify all active dangerous-profile sandboxes and confirm business justification
- preserve audit events for the approval path and operator actions

## Recovery

- migrate affected workloads back to approved non-dangerous profiles where possible
- remove the allow flags after the break-glass window closes
- update the approval record and post-incident notes so the exception is auditable
