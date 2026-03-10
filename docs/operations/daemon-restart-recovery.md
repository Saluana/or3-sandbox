# Daemon Restart Recovery

## Symptoms

- `sandboxd` exited or was restarted while sandboxes or snapshots were active
- operators need to confirm conservative reconcile behavior before resuming normal traffic

## Inspect

1. recent daemon logs before and after restart
2. `go run ./cmd/sandboxctl runtime-health`
3. recent `sandbox.reconcile`, `sandbox.exec.reconcile`, and `snapshot.reconcile` audit events
4. the SQLite `sandboxes` and `snapshots` rows most recently updated
5. `/metrics` lines such as `or3_sandbox_audit_events_total`, `or3_sandbox_lifecycle_failures_total`, and `or3_sandbox_snapshot_operations_total`

## Immediate actions

- confirm the daemon is healthy before taking new traffic
- verify that running, stopped, and suspended sandboxes were not silently reclassified in an unsafe way
- preserve logs if reconcile behavior is suspicious
- note which steps were automatic versus which still require operator action before reopening traffic

## Recovery

- run `./scripts/qemu-recovery-drill.sh` on a prepared host after fixing the restart issue
- use the drill output plus audit/metrics evidence to confirm which recovery steps were automatic versus operator-driven
- if reconcile state looks wrong, stop and preserve affected sandboxes before manual correction
- do not broaden production claims again until restart durability is revalidated
