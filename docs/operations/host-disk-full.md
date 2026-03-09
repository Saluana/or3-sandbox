# Host Disk Full

## Symptoms

- create, snapshot, restore, or exec operations begin failing with storage errors
- `/metrics` and runtime capacity show storage pressure
- sandbox snapshots or workspace writes stall or fail

## Inspect

1. host free space for database, storage, and snapshot volumes
2. `curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/v1/runtime/capacity"`
3. `curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/metrics"`
4. the largest sandbox and snapshot directories under the storage roots

## Immediate actions

- preserve critical workload data first
- stop non-critical work only if it helps protect critical sandboxes or snapshots
- avoid deleting snapshot artifacts until you know whether they are the only recovery path

## Recovery

- reclaim disk safely
- rerun `sandboxctl doctor --production-qemu`
- rerun `./scripts/qemu-production-smoke.sh` or the relevant snapshot drill before restoring production claims
