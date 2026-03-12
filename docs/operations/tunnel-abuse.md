# Tunnel Abuse

## Symptoms

- unexpected tunnel creation volume or repeated signed-URL creation
- repeated one-time capability issuance or high tunnel churn in metrics
- reports of cross-tenant access attempts or suspicious public exposure
- audit events show repeated tunnel revocation, proxy, or signed-URL activity

## Inspect

1. tunnel-related audit events in SQLite
2. current tunnel list and tenant ownership
3. operator logs around tunnel policy denials and revocations
4. whether any dangerous or debug profile was involved

## Immediate actions

- revoke the affected tunnels immediately
- remember that tunnel revoke now also invalidates outstanding stored browser capabilities
- confirm tenant ownership and scope of the exposed service
- rotate tunnel signing material if misuse or leakage is suspected
- preserve the audit trail before broad cleanup

## Recovery

- tighten tenant tunnel policy or visibility defaults if needed
- reissue signed URLs only after the misuse path is understood; prefer the shorter production-default TTL and one-time signed URLs for browser bootstrap flows
- document whether the issue came from policy drift, application behavior, or operator error
