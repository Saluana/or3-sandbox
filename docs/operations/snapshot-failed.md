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

## Restore validation behavior

- restore bundles are treated as untrusted input
- the control plane rejects path traversal, symlinks, hard links, device nodes, FIFOs, and oversized expansion ratios during bundle extraction
- snapshot metadata now records runtime backend plus workspace contract compatibility so cross-runtime restores can be denied before files are applied
- restored files are written with normalized permissions instead of replaying arbitrary host modes

## Cleanup guidance

- if restore fails after fetching an exported bundle, remove the temporary `snapshot.restore.tar.gz` artifact from the snapshot directory
- if compatibility validation fails, keep the snapshot row for audit and investigation but do not retry against a different runtime class unless the snapshot was recreated there
- if a partial restore left files behind in the target sandbox workspace, delete and recreate the target sandbox before retrying with a trusted snapshot
