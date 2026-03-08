# Runtimes

A **runtime** is the part that does the real sandbox work.

In this project, the runtime is the engine under the hood.

`or3-sandbox` supports two runtime backends:

- `docker`
- `qemu`

## Quick comparison

| Runtime | Best for | Current status | Main control method |
| --- | --- | --- | --- |
| `docker` | local development and trusted setups | most complete today | Docker CLI |
| `qemu` | production-like isolation work | newer and still being hardened | QEMU + SSH |

## Docker runtime

### What it does well

The Docker runtime is the easiest place to start.

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

## QEMU runtime

### Why it exists

The QEMU runtime is the production-oriented direction.

Instead of a container, each sandbox becomes a guest machine.

That gives a stronger isolation story than Docker's shared-kernel model.

### How it works

The QEMU backend:

- prepares a writable root disk
- prepares a separate workspace disk
- boots a guest image with QEMU
- waits for SSH to become reachable
- checks for a readiness marker at `/var/lib/or3/bootstrap.ready`
- runs commands through SSH
- manages guest files through the guest boundary

### Current limitations

Right now, the first-pass QEMU backend has some intentional limits.

Most importantly:

- `Suspend` is not supported
- `Resume` is not supported
- setup is more involved than Docker
- operator-owned guest image prep matters a lot

If you are new to the project, do **not** start here.

## Which runtime should you pick?

Choose `docker` if:

- you are learning the project
- you want the shortest setup path
- you are testing API and CLI behavior
- you are working on a trusted local machine

Choose `qemu` if:

- you want to study the guest-backed design
- you are working on production-like isolation ideas
- you are comfortable preparing guest images and SSH access

## Storage behavior

### Docker

- workspace is a host-mounted directory
- snapshots combine a committed image and a workspace archive

### QEMU

- root filesystem lives in a writable disk image
- workspace lives in a separate disk image
- snapshots copy both disk artifacts

## Network behavior

### Docker

- `internet-enabled` gets a dedicated Docker network
- `internet-disabled` uses Docker `none`

### QEMU

- guest access is built around loopback forwarding and SSH
- tunnel exposure remains explicit
- no direct host exposure by default

## Beginner recommendation

Use Docker first.

Then, once the project makes sense to you, read `images/guest/README.md` and try the QEMU path.
