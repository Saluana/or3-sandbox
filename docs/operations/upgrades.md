# Upgrade Guide

This guide describes the supported expectations for upgrading the single-node control plane.

## Before upgrading

Do this every time:

1. Run the CI-friendly smoke path from [Production Verification](verification.md).
2. If the deployment is a production Linux/KVM host, run `./scripts/qemu-host-verification.sh --profile core --control-mode agent` before the upgrade window and resolve any failures first.
3. Take a fresh backup of SQLite, the snapshot root, and any optional export bundles.
4. Record the current daemon version and guest base image version.
5. Confirm there are no active incident conditions such as disk pressure, auth failures, or snapshot corruption.

## Database compatibility

The current repo keeps SQLite as the system of record and favors additive schema changes. Treat that as an operational expectation, not a license to skip backups.

Upgrade rule:

- do not assume downgrade safety after a newer daemon has started against the database
- stage upgrades on a backup copy first if the new release changes schema behavior

## Snapshot compatibility

Snapshot restore assumes:

- the target daemon still understands the stored snapshot metadata
- the snapshot artifacts or export bundles still exist
- the target runtime backend is still compatible with the snapshot format

Operationally:

- keep the snapshot root and export bundles together with the database backup
- avoid changing runtime backend, host architecture, or guest image family mid-upgrade without testing snapshot restore first

## Guest image compatibility

QEMU upgrades are only as good as the guest contract they preserve.

Keep these stable unless you are deliberately migrating:

- guest architecture
- SSH bootstrap user
- SSH key trust model
- cloud-init or bootstrap expectations used by the runtime

If the guest image changes materially, validate:

- new sandbox boot
- exec reachability
- file upload and download
- snapshot create and restore
- any preset or workload type you depend on

## Upgrade procedure

1. Run backups.
2. Stop `sandboxd`.
3. Deploy the new binary and any updated config or secret files.
4. If the guest image changed, deploy it before restarting the daemon.
5. Start `sandboxd`.
6. Check `/healthz`, `/v1/runtime/health`, `/v1/runtime/capacity`, and `/metrics`.
7. Run the fast smoke path again.
8. Run `go run ./cmd/sandboxctl doctor --production-qemu`.
9. Run `./scripts/qemu-host-verification.sh --profile core --control-mode agent` on the prepared Linux/KVM host.
10. Perform one documented operator drill, such as snapshot restore or daemon restart recovery, before restoring production-ready language.

## Rollback expectations

Rollback is safest when:

- you have not yet started the newer daemon against production data, or
- you restore the pre-upgrade backup set in full

Do not mix:

- old SQLite state with new snapshot artifacts
- new SQLite state with old snapshot artifacts
- changed guest image families with unverified snapshot restore expectations
