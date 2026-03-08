# Configuration

This page explains the most important settings in plain language.

You can set configuration in two ways:

- command-line flags for `sandboxd`
- environment variables

In most cases, flags and env vars describe the same setting.

## A simple way to think about config

There are four big groups:

1. where the server listens
2. where data is stored
3. which runtime is used
4. how auth and transport are secured
5. what defaults and limits apply

## Most important settings

## Server and storage

| Setting | Default | What it means |
| --- | --- | --- |
| `SANDBOX_LISTEN` | `:8080` | HTTP address for `sandboxd` |
| `SANDBOX_DB_PATH` | `./data/sandbox.db` | SQLite database file |
| `SANDBOX_STORAGE_ROOT` | `./data/storage` | sandbox runtime files |
| `SANDBOX_SNAPSHOT_ROOT` | `./data/snapshots` | snapshot files |
| `SANDBOX_OPERATOR_HOST` | `http://127.0.0.1:8080` | public host used in generated endpoints |

## Deployment mode

| Setting | Default | What it means |
| --- | --- | --- |
| `SANDBOX_MODE` | `development` | choose `development` or `production` |

Important truth:

- `development` mode allows the trusted Docker path and static tokens
- `production` mode rejects Docker and requires the `qemu` runtime plus secure auth and transport settings

These settings answer the question:

> "Where does the daemon listen, and where does it put its files?"

## Runtime selection

| Setting | Default | What it means |
| --- | --- | --- |
| `SANDBOX_RUNTIME` | `docker` | choose `docker` or `qemu` |
| `SANDBOX_TRUSTED_DOCKER_RUNTIME` | `false` | must be `true` when using Docker |

Important truth:

- If `SANDBOX_RUNTIME=docker`, startup fails unless `SANDBOX_TRUSTED_DOCKER_RUNTIME=true`
- This is on purpose, so the operator must clearly opt in to Docker's shared-kernel model
- In production mode, startup also fails unless `SANDBOX_RUNTIME=qemu`

## Sandbox defaults

These values are used when a request does not provide its own values.

| Setting | Default |
| --- | --- |
| `SANDBOX_BASE_IMAGE` | `mcr.microsoft.com/playwright:v1.51.1-noble` |
| `SANDBOX_DEFAULT_CPU` | `2` |
| `SANDBOX_DEFAULT_MEMORY_MB` | `2048` |
| `SANDBOX_DEFAULT_PIDS` | `512` |
| `SANDBOX_DEFAULT_DISK_MB` | `10240` |
| `SANDBOX_DEFAULT_NETWORK_MODE` | `internet-enabled` |
| `SANDBOX_DEFAULT_ALLOW_TUNNELS` | `true` |

## Rate limiting

| Setting | Default | Meaning |
| --- | --- | --- |
| `SANDBOX_RATE_LIMIT_PER_MIN` | `120` | requests per tenant per minute |
| `SANDBOX_RATE_LIMIT_BURST` | `30` | short burst allowance |

## Auth settings

| Setting | Default | Meaning |
| --- | --- | --- |
| `SANDBOX_AUTH_MODE` | `static` | choose `static` or `jwt-hs256` |
| `SANDBOX_TOKENS` | `dev-token=tenant-dev` | token-to-tenant mapping |
| `SANDBOX_AUTH_JWT_ISSUER` | empty | required JWT issuer in production JWT mode |
| `SANDBOX_AUTH_JWT_AUDIENCE` | empty | required JWT audience in production JWT mode |
| `SANDBOX_AUTH_JWT_SECRET_PATHS` | empty | comma-separated HMAC secret file paths |

### Static auth

Format:

```text
SANDBOX_TOKENS=token-a=tenant-a,token-b=tenant-b
```

This means:

- token `token-a` belongs to tenant `tenant-a`
- token `token-b` belongs to tenant `tenant-b`

This is the simple development path.

### JWT auth

JWT mode is the production-ready auth path in this repo.

- tokens must match the configured issuer and audience
- secrets are loaded from files, not written directly into logs or code
- service identities are supported with a `service=true` claim
- roles control what an identity may do

Useful role examples:

- `admin` or `operator`: full access
- `developer`: lifecycle, exec, file, snapshot, and tunnel actions
- `viewer`: read-only sandbox, file, snapshot, and tunnel inspection
- `service`: automation-friendly access including admin inspection

## Transport security

| Setting | Default | Meaning |
| --- | --- | --- |
| `SANDBOX_TLS_CERT_PATH` | empty | TLS certificate file for in-process HTTPS |
| `SANDBOX_TLS_KEY_PATH` | empty | TLS private key file for in-process HTTPS |
| `SANDBOX_TRUST_PROXY_HEADERS` | `false` | trust a reverse proxy to handle HTTPS in front of `sandboxd` |
| `SANDBOX_TUNNEL_SIGNING_KEY` | empty | shared secret for signed tunnel URLs and browser bootstrap cookies |
| `SANDBOX_TUNNEL_SIGNING_KEY_PATH` | empty | file path containing the shared tunnel signing secret |

Important truth:

- you must provide both TLS files together or neither
- if you use trusted proxy mode, set `SANDBOX_OPERATOR_HOST` to an `https://` address
- production mode requires either in-process TLS or trusted-proxy mode
- set one of `SANDBOX_TUNNEL_SIGNING_KEY` or `SANDBOX_TUNNEL_SIGNING_KEY_PATH` for rolling restarts or multiple replicas so signed browser tunnel URLs stay valid across instances

Tunnel signing note:

- signed browser tunnel URLs and the bootstrap cookie are HMAC-protected
- if you do not set an explicit tunnel signing secret, the daemon derives a stable fallback from the current auth configuration
- an explicit shared secret is still the recommended operator choice for multi-replica deployments because it makes signing independent from auth-secret rotation

## Quota settings

These limit what one tenant can do.

| Setting | Default |
| --- | --- |
| `SANDBOX_QUOTA_MAX_SANDBOXES` | `10` |
| `SANDBOX_QUOTA_MAX_RUNNING` | `5` |
| `SANDBOX_QUOTA_MAX_EXECS` | `8` |
| `SANDBOX_QUOTA_MAX_TUNNELS` | `8` |
| `SANDBOX_QUOTA_MAX_CPU` | `16` |
| `SANDBOX_QUOTA_MAX_MEMORY_MB` | `16384` |
| `SANDBOX_QUOTA_MAX_STORAGE_MB` | `51200` |
| `SANDBOX_QUOTA_ALLOW_TUNNELS` | `true` |
| `SANDBOX_DEFAULT_TUNNEL_AUTH` | `token` |
| `SANDBOX_DEFAULT_TUNNEL_VISIBILITY` | `private` |

## Policy guardrails

These settings add operator rules on top of tenant quotas.

| Setting | Default | Meaning |
| --- | --- | --- |
| `SANDBOX_POLICY_ALLOWED_IMAGES` | empty | comma-separated allowlist of exact image refs or prefixes ending in `*` |
| `SANDBOX_POLICY_ALLOW_PUBLIC_TUNNELS` | `false` | allows or blocks `public` tunnel visibility |
| `SANDBOX_POLICY_MAX_SANDBOX_LIFETIME` | `0` | maximum total sandbox lifetime; `0` means disabled |
| `SANDBOX_POLICY_MAX_IDLE_TIMEOUT` | `0` | maximum idle time before start, resume, exec, or tty is denied; `0` means disabled |

Important truth:

- quotas answer "how much can this tenant use?"
- policy answers "what is the operator willing to allow at all?"
- policy denials happen before risky actions continue, and the API returns a clear error message instead of silently changing the request

## Timing settings

| Setting | Default | Meaning |
| --- | --- | --- |
| `SANDBOX_SHUTDOWN_TIMEOUT` | `15s` | graceful stop time for the daemon |
| `SANDBOX_RECONCILE_INTERVAL` | `30s` | how often the daemon re-checks runtime state |
| `SANDBOX_CLEANUP_INTERVAL` | `5m` | cleanup loop timing |

The reconcile loop also helps clean up orphaned exec records and incomplete snapshots after a daemon restart.

## QEMU-specific settings

Only use these when `SANDBOX_RUNTIME=qemu`.

| Setting | Default | Meaning |
| --- | --- | --- |
| `SANDBOX_QEMU_BINARY` | auto | qemu system binary name |
| `SANDBOX_QEMU_ACCEL` | `auto` | accelerator selection |
| `SANDBOX_QEMU_BASE_IMAGE_PATH` | empty | guest base image path |
| `SANDBOX_QEMU_SSH_USER` | empty | guest SSH user |
| `SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH` | empty | SSH private key path |
| `SANDBOX_QEMU_BOOT_TIMEOUT` | `2m` | max guest boot wait |

The daemon validates these at startup.

That means it will fail fast if:

- the QEMU binary is missing
- the base image file is missing
- the SSH key file is missing
- the host accelerator is not available

## Observability and capacity

The daemon now exposes two operator-facing views for production monitoring:

- `GET /v1/runtime/capacity` returns tenant-aware counts for sandboxes, running sandboxes, execs, snapshots, storage, and quota pressure
- `GET /metrics` returns scrape-friendly text counters for sandbox states, runtime health, snapshot counts, exec counts, and quota pressure

Important truth:

- the stronger runtime boundary is `qemu`, but capacity cost is higher because each guest carries its own memory overhead
- Docker remains the cheaper path for trusted work, but resource boundaries there are still more best-effort because it shares the host kernel
- QEMU gives the clearer production boundary, while Docker stays the cost-saving trusted mode

## CPU values

CPU can be written in a few ways:

- `2`
- `1.5`
- `1500m`

Examples:

- `2` means 2 full CPU cores
- `1.5` means 1 and a half cores
- `1500m` means 1500 millicores, which is also 1.5 cores

Note:

- the QEMU runtime currently requires a **whole-core default CPU limit**

## Example: safe local development config

```bash
export SANDBOX_RUNTIME=docker
export SANDBOX_TRUSTED_DOCKER_RUNTIME=true
export SANDBOX_MODE=development
export SANDBOX_AUTH_MODE=static
export SANDBOX_TOKENS=dev-token=tenant-dev
export SANDBOX_DB_PATH=./data/sandbox.db
export SANDBOX_STORAGE_ROOT=./data/storage
export SANDBOX_SNAPSHOT_ROOT=./data/snapshots
```

## Example: QEMU config sketch

```bash
export SANDBOX_MODE=production
export SANDBOX_RUNTIME=qemu
export SANDBOX_AUTH_MODE=jwt-hs256
export SANDBOX_AUTH_JWT_ISSUER=https://issuer.example
export SANDBOX_AUTH_JWT_AUDIENCE=sandbox-api
export SANDBOX_AUTH_JWT_SECRET_PATHS=/run/secrets/or3-jwt-hmac
export SANDBOX_TUNNEL_SIGNING_KEY_PATH=/run/secrets/or3-tunnel-signing-key
export SANDBOX_TRUST_PROXY_HEADERS=true
export SANDBOX_OPERATOR_HOST=https://sandbox.example.com
export SANDBOX_POLICY_ALLOWED_IMAGES=ghcr.io/acme/runner:*,alpine:3.20
export SANDBOX_POLICY_ALLOW_PUBLIC_TUNNELS=false
export SANDBOX_POLICY_MAX_SANDBOX_LIFETIME=24h
export SANDBOX_POLICY_MAX_IDLE_TIMEOUT=2h
export SANDBOX_QEMU_BINARY=qemu-system-aarch64
export SANDBOX_QEMU_ACCEL=hvf
export SANDBOX_QEMU_BASE_IMAGE_PATH=$PWD/images/guest/base.qcow2
export SANDBOX_QEMU_SSH_USER=or3
export SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH=$HOME/.ssh/or3-sandbox
```

Pick the binary and accelerator that match your host.

## Good beginner advice

Start small.

- keep the defaults at first
- use the Docker runtime first if the workload is trusted and you want the cheapest path
- use QEMU in production or when isolation matters more than density
- only change one setting at a time
- if startup fails, read the full error text because validation is designed to be direct and helpful
