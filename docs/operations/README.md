# Operations Docs

These guides cover the supported production and operator workflow for `or3-sandbox`.

Read them in this order:

1. [Production Deployment](production-deployment.md)
2. [QEMU Production Threat Model](qemu-production-threat-model.md)
3. [Backup And Restore](backup-and-restore.md)
4. [Upgrade Guide](upgrades.md)
5. [Incident Runbooks](incidents.md)
6. [Production Verification](verification.md)

Focused runbooks:

- [Guest-Agent Handshake Failure](guest-agent-handshake-failure.md)
- [Guest Won't Boot](guest-wont-boot.md)
- [Sandbox Degraded](sandbox-degraded.md)
- [Snapshot Failed](snapshot-failed.md)
- [Host Disk Full](host-disk-full.md)
- [Tunnel Abuse](tunnel-abuse.md)
- [Dangerous Profile Misuse](dangerous-profile-misuse.md)
- [Daemon Restart Recovery](daemon-restart-recovery.md)

Important truth:

- `qemu` is the intended higher-isolation path once the documented verification drills pass on your hosts
- `docker` stays a trusted or development runtime
- the normal QEMU production contract is guest-agent protocol version `3`; `ssh-compat` remains debug-only compatibility posture
- production claims should be tied to passing tests and documented drills, not aspirational wording
- keep SQLite audit history long enough to span at least one release-gate window and one operator investigation window; the default expectation is to retain audit rows until they are exported or archived by operator policy
- common investigations should begin with `SELECT created_at, action, outcome, message FROM audit_events ORDER BY created_at DESC LIMIT 200;` plus the runtime-capacity and metrics endpoints documented in the API reference
