# Runtimes

A **runtime** is the part that does the real sandbox work.

In this project, the runtime is the engine under the hood.

`or3-sandbox` supports three runtime backends:

- `docker`
- `kata` (containerd + Kata Containers)
- `qemu`

Operators choose which runtimes to enable using **runtime selections**:

| Runtime selection              | Backend  | Runtime class    | Production eligible     |
| ------------------------------ | -------- | ---------------- | ----------------------- |
| `docker-dev`                   | `docker` | `trusted-docker` | No – shared-kernel only |
| `containerd-kata-professional` | `kata`   | `vm`             | Yes – microVM isolation |
| `qemu-professional`            | `qemu`   | `vm`             | Yes – full VM isolation |

Multiple runtime selections can be enabled simultaneously. The operator sets `SANDBOX_ENABLED_RUNTIMES` and `SANDBOX_DEFAULT_RUNTIME` in the daemon config. Callers can request a specific runtime selection at sandbox creation time.

## Runtime classes

Each backend maps to a **runtime class** that describes its isolation posture:

| Backend  | Runtime class    | Production eligible            |
| -------- | ---------------- | ------------------------------ |
| `docker` | `trusted-docker` | No – shared-kernel only        |
| `kata`   | `vm`             | Yes – microVM-backed isolation |
| `qemu`   | `vm`             | Yes – VM-backed isolation      |

The runtime class is used by policy decisions throughout the system. Setting `SANDBOX_MODE=production` fails closed: it rejects any backend whose runtime class is not `vm`.

Docker is intentionally documented and validated as `trusted-docker`. It is not the hostile multi-tenant production boundary. It remains available for local development, image compatibility checks, and explicitly trusted operator environments.

Future VM-compatible backends would also map to the `vm` class when they are supported.

The current runtime info including enabled selections is visible through `GET /v1/runtime/info`:

```json
{
    "backend": "kata",
    "class": "vm",
    "default_runtime_selection": "containerd-kata-professional",
    "enabled_runtime_selections": ["docker-dev", "containerd-kata-professional"]
}
```

The `sandboxctl doctor --production-qemu` command also reports the resolved class and flags non-VM production posture as blocking.

For the recommended production posture, use `SANDBOX_DEPLOYMENT_PROFILE=production-qemu-core`. That profile keeps QEMU on the safe `core` / `runtime` guest profile set. Add browser capability only with `production-qemu-browser`, and reserve `exception-container` for audited dangerous-profile exceptions.

## Quick comparison

| Runtime  | Best for                                              | Runtime class    | Main control method |
| -------- | ----------------------------------------------------- | ---------------- | ------------------- |
| `docker` | local development and trusted setups                  | `trusted-docker` | Docker CLI          |
| `kata`   | professional hosted sandboxes                         | `vm`             | containerd ctr CLI  |
| `qemu`   | production isolation and security-sensitive workloads | `vm`             | QEMU + guest agent  |

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

Its default trusted posture now aims for least privilege:

- explicit non-root execution via `SANDBOX_DOCKER_USER` (default: the daemon's current numeric `uid:gid`, overrideable when operators need a fixed identity)
- `--cap-drop=ALL`
- `--security-opt no-new-privileges:true`
- read-only root filesystem
- writable `/workspace` and optional `/cache` only
- bounded tmpfs-backed `/tmp`

Where the host can enforce them, the Docker runtime can also apply:

- `SANDBOX_DOCKER_SECCOMP_PROFILE`
- `SANDBOX_DOCKER_APPARMOR_PROFILE`
- `SANDBOX_DOCKER_SELINUX_LABEL`

On macOS and other non-Linux developer hosts, those Linux kernel controls are best-effort only. The runtime warns instead of pretending they were enforced.

### How it works

For each sandbox, the Docker runtime:

- creates a container
- mounts a persistent `/workspace`
- optionally mounts `/cache`
- keeps the root filesystem read-only and uses tmpfs for `/tmp`
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

Dangerous Docker behaviors are denied by default:

- privileged mode equivalents
- host namespace sharing
- Docker socket mounts
- elevated-user and `cap-add` overrides unless `SANDBOX_DOCKER_ALLOW_DANGEROUS_OVERRIDES=true`

If you do allow an explicit override, model it through the small capability set carried on sandbox metadata:

- `docker.elevated-user`
- `docker.extra-cap:NET_BIND_SERVICE`

Those overrides are intended for trusted operator workflows only and are audit-visible.

## Kata runtime

### Why it exists

The Kata runtime is a supported VM-backed runtime selection for Linux hosts that already use containerd + Kata Containers.

It uses containerd with Kata Containers to run each sandbox inside a lightweight microVM, giving VM-class isolation with container-like ergonomics.

Kata resolves to the `vm` runtime class and is production-eligible.

### How it works

The Kata adapter shells out to `ctr` (the containerd CLI) rather than linking the containerd client library, keeping the dependency surface small.

For each sandbox, the Kata runtime:

- pulls the base image if not already present
- creates a container via `ctr run` with the configured Kata runtime class
- bind-mounts `/workspace`, `/cache`, `/scratch`, and `/secrets` from host-side storage
- passes CPU and memory limits to containerd
- applies a network label (`or3.network.loopback-only=true`) for internet-disabled sandboxes
- tracks task PIDs and persists local state under `.kata/state.json`

### Exec and TTY

- Non-interactive exec uses `ctr task exec`
- Interactive TTY uses `ctr task exec --tty` with a PTY
- Detached exec is supported

### Snapshots

Kata snapshots use the same model as Docker:

- workspace directory is archived as a `.tar.gz`
- the base image reference is preserved alongside the archive
- restore clears the workspace and extracts the archive with safety limits

### Limitations

- **Suspend / Resume**: not supported (Kata does not expose pause today)
- **Disk limits**: not enforced at create time; containerd + Kata manage root filesystem sizing via the guest kernel
- **Linux only**: the Kata runtime requires a Linux host with KVM

### Host prerequisites

- containerd must be installed and running
- Kata Containers runtime must be installed (e.g. `kata-qemu` or `kata-clh`)
- The containerd socket must be accessible to the daemon
- `ctr` CLI must be available

There is no Kata-only doctor subcommand today. Use `sandboxctl config-lint` for daemon config validation, and use `sandboxctl doctor --production-qemu` when you want the full production-host report; it also surfaces enabled Kata prerequisite failures such as missing `ctr` or an unreachable containerd socket.

### Configuration

```bash
SANDBOX_KATA_BINARY=/usr/local/bin/ctr
SANDBOX_KATA_RUNTIME_CLASS=kata-qemu
SANDBOX_KATA_CONTAINERD_SOCKET=/run/containerd/containerd.sock
```

## QEMU runtime

### Why it exists

The QEMU runtime is the production-oriented direction in this repo.

It is the higher-isolation option when security matters more than density.

Instead of a container, each sandbox becomes a guest machine.

That gives a clearer isolation boundary than Docker's shared-kernel model, but it still needs host-specific verification before you call a deployment production-ready.

QEMU resolves to the `vm` runtime class and is the only currently supported production boundary.

For the normal production posture in this repo, treat one sandbox as one
VM-backed workload boundary. The control plane can manage many sandboxes for a
tenant, but it should not imply that many tenant sandboxes share one guest VM
as the default steady state.

### How it works

The QEMU backend:

- prepares a writable root disk
- prepares a separate workspace disk
- mounts that workspace disk inside the guest at `/workspace` during guest bootstrap
- boots a guest image with QEMU
- waits for the guest agent to report readiness on agent-first images
- checks for a readiness marker at `/var/lib/or3/bootstrap.ready`
- runs commands and PTY sessions through the guest agent as the unprivileged `sandbox` workload user by default
- manages guest files through the guest boundary with a daemon-configured transfer limit that defaults to 64 MiB
- rejects unpromoted production images until `sandboxctl image promote --image <path>` records a verified promotion in SQLite

If a QEMU node raises the daemon transfer limit above 64 MiB, guest images built with the older agent keep the legacy 64 MiB ceiling until they are rebuilt and promoted again.

SSH still exists, but only as the explicit compatibility/debug path for `ssh-compat` images.

### Current limitations

Right now, the first-pass QEMU backend still has some practical limits.

The main ones are:

- setup is more involved than Docker
- operator-owned guest image prep matters a lot
- Linux/KVM is the supported hostile-production target and still needs host-specific validation before production claims
- production release claims should be backed by `sandboxctl release-gate` plus the host-gated verification scripts

### Runtime state signals

The QEMU runtime now exposes more honest status values:

- `booting` means the guest process exists but readiness has not finished yet
- `running` means the guest is reachable and ready
- `suspended` means the guest was intentionally paused
- `degraded` means the guest process is still alive but readiness checks are failing after the boot window
- `error` means the daemon found a clearer failure signal, such as a guest boot failure marker

That makes it easier for operators to tell the difference between "still starting" and "needs attention."

For agent-mode guests, periodic health reporting uses the serial-log bootstrap marker instead of repeatedly probing the guest-agent chardev. Actual guest operations such as exec, files, PTY, and sandbox-local tunnels still use the guest agent.

If you are new to the project, do **not** start here.

## Which runtime should you pick?

Choose `docker` if:

- you are learning the project
- you want the shortest setup path
- you are testing API and CLI behavior
- you are working on a trusted local machine
- you want the cheaper, denser option for trusted internal work

Choose `kata` if:

- you need professional-grade hosted sandboxes with VM isolation
- you want container-like ergonomics (containerd images, bind mounts)
- you are running on a Linux host with KVM and containerd
- you want the primary professional runtime for v1

Choose `qemu` if:

- you need the strongest production isolation boundary
- you are working on untrusted or security-sensitive workloads
- you are comfortable preparing guest images and validating profile contracts
- you need suspend/resume or full guest-agent control

## Storage behavior

### Docker

- workspace is a host-mounted directory
- snapshots combine a committed image and a workspace archive

### Kata

- workspace is a host-mounted directory (same as Docker)
- snapshots combine a base image reference and a workspace archive

### QEMU

- root filesystem lives in a writable disk image
- workspace lives in a separate disk image
- `/workspace` is the mounted guest view of that separate workspace disk
- snapshots copy both disk artifacts, but the sandbox must be stopped first
- restart reconciliation keeps incomplete snapshots conservative instead of pretending they finished cleanly

Troubleshooting missing workspace mounts:

- if snapshot restore appears to succeed but files do not roll back, verify `/workspace` is actually a mount inside the guest instead of a directory on the root filesystem
- the fastest in-guest check is `python3 -c "import os; print(os.path.ismount('/workspace'))"` or `mount | grep ' /workspace '
- if the mount is missing, inspect the serial log for bootstrap failures around the workspace disk attach or `or3-bootstrap` mount step before investigating snapshot copy logic
- a healthy guest should log `or3-bootstrap: ready` only after the workspace disk is mounted and persisted in `fstab`

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
- Kata (`vm` class): the primary professional hosted runtime, combining container ergonomics with microVM isolation
- QEMU (`vm` class): the advanced professional path for full VM isolation with guest-agent control
