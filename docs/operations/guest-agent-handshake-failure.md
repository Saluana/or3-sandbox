# Guest-Agent Handshake Failure

## Symptoms

- `runtime-health` shows QEMU sandboxes in `booting`, `degraded`, or `error`
- `sandboxctl doctor --production-qemu` passes host checks, but the guest never reaches ready
- logs or serial output point at agent protocol or readiness failures

## Inspect

1. `go run ./cmd/sandboxctl runtime-health`
2. `go run ./cmd/sandboxctl inspect <sandbox-id>`
3. host serial logs and QEMU runtime artifacts under `SANDBOX_STORAGE_ROOT/<tenant>/<sandbox>/.runtime/`
4. the image sidecar contract for control mode and protocol version
5. whether the guest image was rebuilt without updating the host-side `*.or3.json` file

## Immediate actions

- stop and preserve the affected sandbox before repeated retries
- verify the image contract checksum still matches the qcow2 image
- verify the image profile still declares the expected `control.mode` and `control.protocol_version`
- if the image is `ssh-compat`, confirm the SSH key and host-key material still match

## Recovery

- rebuild or replace the guest image if the checksum or contract drifted
- restart the sandbox only after the contract and runtime config agree again
- if multiple sandboxes fail after a rollout, roll back to the previous approved image set
