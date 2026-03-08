# Operations Docs

These guides cover the supported production and operator workflow for `or3-sandbox`.

Read them in this order:

1. [Production Deployment](production-deployment.md)
2. [Backup And Restore](backup-and-restore.md)
3. [Upgrade Guide](upgrades.md)
4. [Incident Runbooks](incidents.md)
5. [Production Verification](verification.md)

Important truth:

- `qemu` is the intended higher-isolation path once the documented verification drills pass on your hosts
- `docker` stays a trusted or development runtime
- production claims should be tied to passing tests and documented drills, not aspirational wording
