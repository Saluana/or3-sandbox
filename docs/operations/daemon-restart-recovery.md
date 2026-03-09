# Daemon Restart Recovery

## Symptoms

- `sandboxd` exited or was restarted while sandboxes or snapshots were active
- operators need to confirm conservative reconcile behavior before resuming normal traffic

## Inspect

1. recent daemon logs before and after restart
2. `go run ./cmd/sandboxctl runtime-health`
3. recent `sandbox.reconcile`, `sandbox.exec.reconcile`, and `snapshot.reconcile` audit events
4. the SQLite `sandboxes` and `snapshots` rows most recently updated

## Immediate actions

- confirm the daemon is healthy before taking new traffic
- verify that running, stopped, and suspended sandboxes were not silently reclassified in an unsafe way
- preserve logs if reconcile behavior is suspicious

## Recovery

- run `./scripts/qemu-recovery-drill.sh` on a prepared host after fixing the restart issue
- if reconcile state looks wrong, stop and preserve affected sandboxes before manual correction
- do not broaden production claims again until restart durability is revalidated
