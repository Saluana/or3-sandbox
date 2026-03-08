# Backup And Restore

This guide covers the durable state that matters for `or3-sandbox` and the restore order that avoids partial recovery.

## Data that must be protected

The production data set is:

- the SQLite database at `SANDBOX_DB_PATH`
- the snapshot root at `SANDBOX_SNAPSHOT_ROOT`
- the storage root at `SANDBOX_STORAGE_ROOT`
- the optional export bundle root referenced by `SANDBOX_S3_EXPORT_URI`
- the guest base image inventory and the production secret files needed to boot guests and validate auth

## Backup strategy

Recommended baseline:

- take regular cold backups of SQLite by stopping `sandboxd` briefly or quiescing writes
- back up the snapshot root and optional export bundles on the same cadence as the database
- back up the storage root when you need sandbox-local workspace persistence beyond snapshots
- version and protect the guest base images separately from sandbox state

Important truth:

- SQLite plus snapshot artifacts are the minimum durable restore set for snapshot recovery
- SQLite alone is not enough if you expect local snapshots to be restorable
- export bundles are optional but useful when local snapshot artifacts are lost

## Cold backup procedure

1. Stop `sandboxd` or otherwise ensure writes are paused.
2. Copy `SANDBOX_DB_PATH`.
3. Copy `SANDBOX_SNAPSHOT_ROOT`.
4. Copy `SANDBOX_STORAGE_ROOT` if you need live sandbox workspaces, not only snapshots.
5. Copy the optional export root if configured.
6. Record the guest base image version and the exact daemon build you are backing up.

## Hot backup expectations

This repo does not currently ship a dedicated hot-backup coordinator.

If you need hot backups:

- use an external SQLite-safe backup mechanism rather than a raw file copy taken mid-write
- understand that snapshot creation and restore activity can still change the snapshot root while the daemon is live

Cold backups are the simplest supported operator path.

## Restore ordering

Restore in this order:

1. Restore the guest base images and secret files required by the deployment.
2. Restore `SANDBOX_DB_PATH`.
3. Restore `SANDBOX_SNAPSHOT_ROOT`.
4. Restore `SANDBOX_STORAGE_ROOT` if you expect existing non-snapshot sandbox workspaces to survive.
5. Restore the optional export bundle root.
6. Start `sandboxd`.
7. Run `sandboxctl runtime-health` and `GET /v1/runtime/capacity`.
8. Test one snapshot restore on a non-critical sandbox before resuming full traffic.

## Snapshot-specific recovery

If local snapshot artifacts are missing but `ExportLocation` is populated, the service can rehydrate the local artifacts from the exported bundle during restore. That makes export bundles useful as a secondary recovery path for snapshot corruption or accidental local deletion.

## Post-restore checks

Inspect:

- `GET /v1/runtime/health` for degraded sandboxes
- `GET /v1/runtime/capacity` for snapshot counts and storage pressure
- `sqlite3 "$SANDBOX_DB_PATH" 'select snapshot_id,status,export_location from snapshots order by created_at desc limit 20;'`
- the snapshot root for missing or truncated artifact files

If restore succeeds but sandboxes fail to boot, switch to the guest boot failure runbook in [Incident Runbooks](incidents.md).
