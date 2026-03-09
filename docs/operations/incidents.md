# Incident Runbooks

This guide covers what to inspect during runtime, auth, storage, and snapshot incidents, followed by focused runbooks for the highest-risk failures.

## First inspection pass

For any incident, collect these first:

1. `curl -fsS "$SANDBOX_API/healthz"`
2. `go run ./cmd/sandboxctl runtime-health`
3. `curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/v1/runtime/capacity"`
4. `curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/metrics"`
5. recent daemon logs filtered by `component=daemon`, `component=auth`, `component=api`, and `component=service`

Useful database checks:

```bash
sqlite3 "$SANDBOX_DB_PATH" 'select sandbox_id,status,updated_at from sandboxes order by updated_at desc limit 20;'
sqlite3 "$SANDBOX_DB_PATH" 'select snapshot_id,status,export_location,created_at from snapshots order by created_at desc limit 20;'
sqlite3 "$SANDBOX_DB_PATH" 'select action,resource_id,outcome,message,created_at from audit_events order by created_at desc limit 50;'
```

## Runtime incidents

Inspect:

- `/v1/runtime/health` for `booting`, `degraded`, and `error` sandboxes
- service audit events such as `sandbox.start`, `sandbox.stop`, `sandbox.resume`, `snapshot.restore`, and `sandbox.reconcile`
- host disk and memory pressure
- guest image reachability, guest-agent handshake state, and SSH bootstrap material only when using `ssh-compat`

## Auth incidents

Inspect:

- `component=auth` logs for `auth.reject` and `auth.rate_limit`
- JWT issuer, audience, and secret file paths
- secret file timestamps and rotation changes
- tunnel-related audit events like `tunnel.signed_url` and `tunnel.proxy`

Do not log or paste raw bearer tokens into tickets or shells.

## Storage incidents

Inspect:

- `/v1/runtime/capacity` alerts and storage pressure ratios
- `/metrics` values such as `or3_sandbox_actual_storage_bytes` and `or3_sandbox_storage_pressure_ratio`
- free disk space on the database, storage, and snapshot volumes
- the snapshot root for missing or partial artifacts

## Snapshot incidents

Inspect:

- `snapshots` table state in SQLite
- `snapshot.create`, `snapshot.restore`, and `snapshot.reconcile` audit events
- local snapshot artifacts under `SANDBOX_SNAPSHOT_ROOT/<sandbox-id>/<snapshot-id>/`
- optional export bundles referenced by `export_location`

## Runbook: daemon crash

1. Confirm the process is gone and capture the last daemon logs.
2. Restart `sandboxd`.
3. Run `runtime-health` immediately after restart.
4. Check for `sandbox.exec.reconcile`, `snapshot.reconcile`, and `sandbox.reconcile` audit events.
5. Verify that running or suspended sandboxes were reconciled conservatively instead of silently deleted.

If the daemon cannot restart cleanly, restore from backup and follow the restore guide.

See also [Daemon Restart Recovery](daemon-restart-recovery.md).

## Runbook: guest boot failure

1. Check `/v1/runtime/health` for sandboxes stuck in `booting`, `degraded`, or `error`.
2. Inspect recent `sandbox.create`, `sandbox.start`, `sandbox.resume`, or `sandbox.reconcile` audit events.
3. Verify the QEMU binary, accelerator, guest image path, SSH user, and SSH key still match the deployment.
4. Confirm host disk and memory are sufficient for the requested guest size.
5. Recreate the sandbox only after preserving any needed snapshot artifacts.

See also [Guest Won't Boot](guest-wont-boot.md) and [Guest-Agent Handshake Failure](guest-agent-handshake-failure.md).

## Runbook: disk exhaustion

1. Confirm host free space on the database, storage, and snapshot volumes.
2. Inspect `/v1/runtime/capacity` and `/metrics` for storage pressure.
3. Identify the largest snapshot roots and workspace roots.
4. Revoke unnecessary tunnels or stop non-critical sandboxes only if that helps preserve data for critical workloads.
5. Free disk, then retry the blocked operation.

Host integration coverage already exercises disk-full behavior for the QEMU guest path; use that test as the reference drill described in [Production Verification](verification.md).

See also [Host Disk Full](host-disk-full.md).

## Runbook: expired credentials

1. Inspect `component=auth` logs for `auth.reject`.
2. Verify the current JWT secret file, issuer, and audience values.
3. Check whether tunnel signed URL or tunnel token failures are isolated to one tenant or are systemic.
4. Rotate the relevant secret file and restart or redeploy the daemon if the operational model requires it.
5. Re-run the smoke path after rotation.

## Runbook: snapshot corruption

1. Inspect the snapshot row in SQLite and confirm whether the snapshot is `ready` or already `error`.
2. Check the local snapshot files under `SANDBOX_SNAPSHOT_ROOT`.
3. If local files are missing, check whether `export_location` is populated and whether the export bundle still exists.
4. Attempt restore on a non-critical target first.
5. If both local and exported artifacts are missing or corrupt, restore from backup.

See also [Snapshot Failed](snapshot-failed.md), [Sandbox Degraded](sandbox-degraded.md), [Tunnel Abuse](tunnel-abuse.md), and [Dangerous Profile Misuse](dangerous-profile-misuse.md).
