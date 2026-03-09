# Snapshot Failed

## Symptoms

- snapshot status remains `error`
- restore attempts fail or the local/exported artifacts are missing
- audit events show `snapshot.create`, `snapshot.restore`, or `snapshot.reconcile` errors

## Inspect

1. `go run ./cmd/sandboxctl snapshot-list <sandbox-id>`
2. `go run ./cmd/sandboxctl snapshot-inspect <snapshot-id>`
3. local artifacts under `SANDBOX_SNAPSHOT_ROOT`
4. export bundles if `export_location` is set
5. host free disk space and runtime-health output

## Immediate actions

- do not overwrite partial artifacts until you know whether they are recoverable
- preserve the failing snapshot row and host logs
- verify the sandbox was stopped when the snapshot was attempted

## Recovery

- retry only after restoring host disk headroom and confirming the sandbox is stopped
- restore to a non-critical target first when testing a questionable snapshot
- if both local and exported artifacts are unusable, fall back to backup and restore procedures
