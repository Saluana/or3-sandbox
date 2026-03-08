# Production Deployment

This guide describes the supported single-node production deployment shape for `or3-sandbox`.

## Supported production boundary

- use `SANDBOX_MODE=production`
- use `SANDBOX_RUNTIME=qemu`
- use JWT auth, not static bearer tokens
- terminate TLS either inside `sandboxd` or at a trusted reverse proxy

`docker` is not the hostile multi-tenant production boundary in this repo.

## Host prerequisites

The production host needs:

- Linux or macOS with the matching supported QEMU accelerator
- the QEMU system binary and `qemu-img`
- the prepared guest base image referenced by `SANDBOX_QEMU_BASE_IMAGE_PATH`
- an SSH private key that matches the guest image bootstrap user
- the matching guest SSH host public key referenced by `SANDBOX_QEMU_SSH_HOST_KEY_PATH`
- enough disk for the SQLite database, sandbox storage root, snapshot root, and optional export bundles
- enough RAM for the daemon plus the guest memory assigned to each concurrent QEMU sandbox

The daemon already validates the critical QEMU settings at startup and fails fast if the binary, guest image, SSH user, SSH key, or accelerator support is missing.

## Directory layout

Use explicit persistent paths owned by the service user.

Recommended layout:

```text
/var/lib/or3-sandbox/
  sandbox.db
  storage/
  snapshots/
  exports/        # optional snapshot bundle export root

/etc/or3-sandbox/
  sandboxd.env

/run/secrets/or3-sandbox/
  jwt-hmac
  tunnel-signing-key
  tls.crt         # if using in-process TLS
  tls.key         # if using in-process TLS
  qemu-ssh-key
  qemu-ssh-host-key.pub
```

Map those paths to:

- `SANDBOX_DB_PATH=/var/lib/or3-sandbox/sandbox.db`
- `SANDBOX_STORAGE_ROOT=/var/lib/or3-sandbox/storage`
- `SANDBOX_SNAPSHOT_ROOT=/var/lib/or3-sandbox/snapshots`
- `SANDBOX_S3_EXPORT_URI=/var/lib/or3-sandbox/exports` if you want local export bundles

## Secrets model

Use file-backed secrets for production:

- `SANDBOX_AUTH_JWT_SECRET_PATHS=/run/secrets/or3-sandbox/jwt-hmac`
- `SANDBOX_TUNNEL_SIGNING_KEY_PATH=/run/secrets/or3-sandbox/tunnel-signing-key`
- `SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH=/run/secrets/or3-sandbox/qemu-ssh-key`
- `SANDBOX_QEMU_SSH_HOST_KEY_PATH=/run/secrets/or3-sandbox/qemu-ssh-host-key.pub`
- `SANDBOX_TLS_CERT_PATH` and `SANDBOX_TLS_KEY_PATH` if TLS terminates inside `sandboxd`

Do not place raw secrets in command lines, shell history, or ad hoc logs.

## Runtime setup

Baseline production configuration:

```bash
export SANDBOX_MODE=production
export SANDBOX_RUNTIME=qemu
export SANDBOX_AUTH_MODE=jwt-hs256
export SANDBOX_AUTH_JWT_ISSUER=https://issuer.example
export SANDBOX_AUTH_JWT_AUDIENCE=sandbox-api
export SANDBOX_AUTH_JWT_SECRET_PATHS=/run/secrets/or3-sandbox/jwt-hmac
export SANDBOX_TUNNEL_SIGNING_KEY_PATH=/run/secrets/or3-sandbox/tunnel-signing-key
export SANDBOX_DB_PATH=/var/lib/or3-sandbox/sandbox.db
export SANDBOX_STORAGE_ROOT=/var/lib/or3-sandbox/storage
export SANDBOX_SNAPSHOT_ROOT=/var/lib/or3-sandbox/snapshots
export SANDBOX_OPERATOR_HOST=https://sandbox.example.com
export SANDBOX_QEMU_BINARY=qemu-system-x86_64
export SANDBOX_QEMU_ACCEL=auto
export SANDBOX_QEMU_BASE_IMAGE_PATH=/var/lib/or3-images/base.qcow2
export SANDBOX_QEMU_SSH_USER=or3
export SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH=/run/secrets/or3-sandbox/qemu-ssh-key
export SANDBOX_QEMU_SSH_HOST_KEY_PATH=/run/secrets/or3-sandbox/qemu-ssh-host-key.pub
```

Transport options:

- in-process TLS: set both `SANDBOX_TLS_CERT_PATH` and `SANDBOX_TLS_KEY_PATH`
- reverse proxy TLS termination: set `SANDBOX_TRUST_PROXY_HEADERS=true` and keep `SANDBOX_OPERATOR_HOST` on the final `https://` origin

## Startup ordering

Bring the system up in this order:

1. Ensure the persistent directories exist with correct ownership.
2. Ensure the QEMU binary, guest image, guest SSH host key, SSH private key, and auth secrets are present on disk.
3. Start `sandboxd`.
4. Wait for `GET /healthz` to return `{"ok":true}`.
5. Confirm runtime posture with `sandboxctl runtime-health` or `GET /v1/runtime/health`.
6. Confirm capacity and storage visibility with `sandboxctl quota`, `GET /v1/runtime/capacity`, or `GET /metrics`.

If the daemon is supervised by `systemd`, make secret mounts and persistent storage available before the unit starts.

## What to inspect after startup

Check these first:

- `/healthz` for basic daemon reachability
- `/v1/runtime/health` for runtime backend and per-sandbox state
- `/v1/runtime/capacity` for quota pressure, snapshot counts, and degraded sandboxes
- `/metrics` for scrape-friendly counters and ratios
- the JSON logs from `sandboxd` for `component=daemon`, `component=auth`, `component=api`, and `component=service`

## Startup failures

Common production startup failures are:

- missing or unreadable JWT secret files
- missing TLS key or certificate when using in-process TLS
- unsupported runtime choice in production mode
- missing QEMU binary, guest image, or SSH key
- unreadable storage or snapshot roots

The daemon is designed to fail fast on those errors instead of silently starting in a weaker mode.
