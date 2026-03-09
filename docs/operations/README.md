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
- production claims should be tied to passing tests and documented drills, not aspirational wording
