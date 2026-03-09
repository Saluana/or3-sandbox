# Guest Won't Boot

## Symptoms

- sandbox remains in `booting` and later moves to `error` or `degraded`
- serial logs contain kernel panic or boot failure markers
- `sandboxctl start` or `resume` fails repeatedly on the same image/profile

## Inspect

1. `go run ./cmd/sandboxctl runtime-health`
2. `go run ./cmd/sandboxctl inspect <sandbox-id>`
3. serial log in the sandbox runtime directory
4. host free RAM, disk headroom, and KVM availability
5. the guest image sidecar contract and selected profile

## Immediate actions

- preserve the serial log and sandbox metadata before cleanup
- verify the host still has KVM access and enough free resources
- confirm the selected image matches the requested profile and control mode
- avoid repeated restart loops until the cause is understood

## Recovery

- move the sandbox to a known-good approved image if the failure is image-specific
- stop or delete the failed sandbox only after preserving needed data
- run `./scripts/qemu-recovery-drill.sh` or host integration tests after fixing the image or host issue
