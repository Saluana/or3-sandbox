# Runtimes

A **runtime** is the part that does the real sandbox work.

In this project, the runtime is the engine under the hood.

`or3-sandbox` supports two runtime backends:

- `docker`
- `qemu`

## Runtime classes

Each backend maps to a **runtime class** that describes its isolation posture:

| Backend | Runtime class | Production eligible |
| --- | --- | --- |
| `docker` | `trusted-docker` | No – shared-kernel only |
| `qemu` | `vm` | Yes – VM-backed isolation |

The runtime class is used by policy decisions throughout the system. Setting `SANDBOX_MODE=production` fails closed: it rejects any backend whose runtime class is not `vm`.

Docker is intentionally documented and validated as `trusted-docker`. It is not the hostile multi-tenant production boundary. It remains available for local development, image compatibility checks, and explicitly trusted operator environments.

Future VM-compatible backends (such as Kata Containers) would also map to the `vm` class when they are supported.

The current backend and its resolved class are visible through `GET /v1/runtime/info`:

```json
{
  "backend": "qemu",
  "class": "vm"
}
```

The `sandboxctl doctor --production-qemu` command also reports the resolved class and flags non-VM production posture as blocking.

## Quick comparison

| Runtime | Best for | Runtime class | Main control method |
| --- | --- | --- | --- |
| `docker` | local development and trusted setups | `trusted-docker` | Docker CLI |
| `qemu` | production isolation and security-sensitive workloads | `vm` | QEMU + guest agent |

## Docker runtime

### What it does well

The Docker runtime is the easiest place to start.

It is also the lower-cost option when the workload is trusted.

It currently supports:

- sandbox creation
- start and stop
- suspend and resume
- exec
- interactive TTY
- file operations
- tunnels
- snapshots
- restart reconciliation

### How it works

For each sandbox, the Docker runtime:

- creates a container
- mounts a persistent `/workspace`
- optionally mounts `/cache`
- creates a dedicated network for internet-enabled sandboxes
- uses `none` networking for internet-disabled sandboxes

### Important warning

Docker uses the host kernel.

Because of that, this project requires:

```bash
SANDBOX_TRUSTED_DOCKER_RUNTIME=true
```

That setting is the project's way of saying:

> "Yes, I understand this is trusted mode."

Docker resolves to the `trusted-docker` runtime class. `SANDBOX_MODE=production` will reject it at startup because `trusted-docker` is not VM-backed.

## QEMU runtime

### Why it exists

The QEMU runtime is the production-oriented direction in this repo.

It is the higher-isolation option when security matters more than density.

Instead of a container, each sandbox becomes a guest machine.

That gives a clearer isolation boundary than Docker's shared-kernel model, but it still needs host-specific verification before you call a deployment production-ready.

QEMU resolves to the `vm` runtime class and is the only currently supported production boundary.

### How it works

The QEMU backend:

- prepares a writable root disk
- prepares a separate workspace disk
- boots a guest image with QEMU
- waits for the guest agent to report readiness on agent-first images
- checks for a readiness marker at `/var/lib/or3/bootstrap.ready`
- runs commands through the guest agent by default
- manages guest files through the guest boundary

SSH still exists, but only as the explicit compatibility/debug path for `ssh-compat` images.

### Current limitations

Right now, the first-pass QEMU backend still has some practical limits.

The main ones are:

- setup is more involved than Docker
- operator-owned guest image prep matters a lot
- Linux/KVM is the supported hostile-production target and still needs host-specific validation before production claims

### Runtime state signals

The QEMU runtime now exposes more honest status values:

- `booting` means the guest process exists but readiness has not finished yet
- `running` means the guest is reachable and ready
- `suspended` means the guest was intentionally paused
- `degraded` means the guest process is still alive but readiness checks are failing after the boot window
- `error` means the daemon found a clearer failure signal, such as a guest boot failure marker

That makes it easier for operators to tell the difference between "still starting" and "needs attention."

If you are new to the project, do **not** start here.

## Which runtime should you pick?

Choose `docker` if:

- you are learning the project
- you want the shortest setup path
- you are testing API and CLI behavior
- you are working on a trusted local machine
- you want the cheaper, denser option for trusted internal work

Choose `qemu` if:

- you need the stronger production isolation boundary
- you are working on untrusted or security-sensitive workloads
- you are comfortable preparing guest images and validating profile contracts

## Storage behavior

### Docker

- workspace is a host-mounted directory
- snapshots combine a committed image and a workspace archive

### QEMU

- root filesystem lives in a writable disk image
- workspace lives in a separate disk image
- snapshots copy both disk artifacts, but the sandbox must be stopped first
- restart reconciliation keeps incomplete snapshots conservative instead of pretending they finished cleanly

## Network behavior

### Docker

- `internet-enabled` gets a dedicated Docker network
- `internet-disabled` uses Docker `none`

### QEMU

- guest access is agent-first via virtio-serial; SSH remains explicit compatibility/debug only
- tunnel exposure remains explicit
- no direct host exposure by default

## Beginner recommendation

Use Docker first.

Then, once the project makes sense to you, read `images/guest/README.md` and try the QEMU path.

For production planning, remember the simple rule:

- Docker (`trusted-docker` class): choose when the workload is trusted and density matters
- QEMU (`vm` class): required when the security boundary matters more than density, and the only class eligible for `SANDBOX_MODE=production`
