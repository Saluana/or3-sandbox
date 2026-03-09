# Production Deployment

This guide describes the supported single-node production deployment shape for `or3-sandbox`.

## Supported production boundary

- use `SANDBOX_MODE=production`
- use `SANDBOX_RUNTIME=qemu` (the only backend whose runtime class is VM-backed)
- use the agent-based `core` guest profile unless there is an explicitly approved reason to use a heavier profile
- use JWT auth, not static bearer tokens
- terminate TLS either inside `sandboxd` or at a trusted reverse proxy

`docker` is not the hostile multi-tenant production boundary in this repo.

The production gate is enforced through **runtime classes**, not through ad-hoc backend name checks. `SANDBOX_MODE=production` rejects any backend that does not resolve to the `vm` runtime class. `docker` resolves to `trusted-docker` and is therefore always rejected in production mode.

| Backend | Runtime class | Production eligible |
| --- | --- | --- |
| `docker` | `trusted-docker` | No |
| `qemu` | `vm` | Yes |

The supported hostile-production target is Linux with KVM-backed QEMU. macOS remains useful for development and local validation, but it is not the production reference posture.

## Host prerequisites

The production host needs:

- Linux with KVM for the supported hostile-production posture
- the QEMU system binary and `qemu-img`
- the prepared guest image referenced by `SANDBOX_QEMU_BASE_IMAGE_PATH`
- the matching `*.or3.json` sidecar contract for every approved guest image
- enough disk for the SQLite database, sandbox storage root, snapshot root, and optional export bundles
- enough RAM for the daemon plus the guest memory assigned to each concurrent QEMU sandbox

The daemon already validates the critical QEMU settings at startup and fails fast if the binary, guest image, sidecar contract, profile policy, or accelerator support is missing.

SSH material is only required for explicit `ssh-compat` / `debug` guest images.

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
  qemu-ssh-key            # debug / ssh-compat only
  qemu-ssh-host-key.pub   # debug / ssh-compat only
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
- `SANDBOX_TLS_CERT_PATH` and `SANDBOX_TLS_KEY_PATH` if TLS terminates inside `sandboxd`

For production-default agent-based images, do not set the QEMU SSH paths. Reserve them for explicit compatibility/debug images only.

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
export SANDBOX_QEMU_CONTROL_MODE=agent
export SANDBOX_QEMU_ALLOWED_PROFILES=core,runtime,browser,container
export SANDBOX_QEMU_DANGEROUS_PROFILES=debug
export SANDBOX_QEMU_BASE_IMAGE_PATH=/var/lib/or3-images/or3-guest-core.qcow2
export SANDBOX_QEMU_ALLOWED_BASE_IMAGE_PATHS=/var/lib/or3-images/or3-guest-core.qcow2,/var/lib/or3-images/or3-guest-runtime.qcow2
```

If you intentionally run a compatibility/debug image, add:

```bash
export SANDBOX_QEMU_ALLOW_SSH_COMPAT=true
export SANDBOX_QEMU_ALLOW_DANGEROUS_PROFILES=true
export SANDBOX_QEMU_SSH_USER=sandbox
export SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH=/run/secrets/or3-sandbox/qemu-ssh-key
export SANDBOX_QEMU_SSH_HOST_KEY_PATH=/run/secrets/or3-sandbox/qemu-ssh-host-key.pub
```

Transport options:

- in-process TLS: set both `SANDBOX_TLS_CERT_PATH` and `SANDBOX_TLS_KEY_PATH`
- reverse proxy TLS termination: set `SANDBOX_TRUST_PROXY_HEADERS=true` and keep `SANDBOX_OPERATOR_HOST` on the final `https://` origin

## Startup ordering

Bring the system up in this order:

1. Ensure the persistent directories exist with correct ownership.
2. Ensure the QEMU binary, approved guest image(s), sidecar contract(s), and auth secrets are present on disk.
3. Start `sandboxd`.
4. Wait for `GET /healthz` to return `{"ok":true}`.
5. Confirm runtime posture with `sandboxctl runtime-health` or `GET /v1/runtime/health`.
6. Confirm capacity and storage visibility with `sandboxctl quota`, `GET /v1/runtime/capacity`, or `GET /metrics`.
7. Run `sandboxctl doctor --production-qemu` and resolve any blocking failures before admitting production traffic.

If the daemon is supervised by `systemd`, make secret mounts and persistent storage available before the unit starts.

## JWT rotation and break-glass policy

Normal production posture:

- rotate JWT HMAC material through the file paths listed in `SANDBOX_AUTH_JWT_SECRET_PATHS`
- reload or restart `sandboxd` using the standard deployment procedure after rotation
- rerun `sandboxctl doctor --production-qemu` and the fast smoke path after rotation

Break-glass guidance:

- keep dangerous-profile approval time-bounded and auditable
- enable `SANDBOX_QEMU_ALLOW_DANGEROUS_PROFILES` or `SANDBOX_QEMU_ALLOW_SSH_COMPAT` only for the smallest necessary window
- record who approved the exception, why it was needed, and which sandboxes/images were affected
- remove the allow flags once the emergency or migration window closes

## What to inspect after startup

Check these first:

- `/healthz` for basic daemon reachability
- `/v1/runtime/health` for runtime backend and per-sandbox state
- `/v1/runtime/capacity` for quota pressure, snapshot counts, degraded sandboxes, and guest profile/capability mix
- `/metrics` for scrape-friendly counters and ratios, including guest profile mix and declared capability counts
- the JSON logs from `sandboxd` for `component=daemon`, `component=auth`, `component=api`, and `component=service`

## Startup failures

Common production startup failures are:

- missing or unreadable JWT secret files
- missing TLS key or certificate when using in-process TLS
- unsupported runtime class in production mode (e.g. `docker` resolves to `trusted-docker`, which is not VM-backed)
- missing QEMU binary, guest image, sidecar contract, or KVM support
- use of `ssh-compat` or `debug` images without the explicit allow flags
- unreadable storage or snapshot roots

The daemon is designed to fail fast on those errors instead of silently starting in a weaker mode.
