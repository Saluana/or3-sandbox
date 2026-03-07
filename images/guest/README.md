# Guest Image Path

This directory contains the first-pass guest image preparation assets for the `qemu` runtime.

The goal is narrow:

- prepare a reusable qcow2 guest base image for `or3-sandbox`
- keep SSH as the first control channel
- use `systemd` inside the guest as the lightweight init and supervision story
- make `/workspace` available from the secondary guest disk
- create the readiness marker expected by the QEMU runtime at `/var/lib/or3/bootstrap.ready`

## Operator-owned SSH material

The daemon never stores guest SSH keys in SQLite.

Instead it reads operator-provided paths:

- `SANDBOX_QEMU_SSH_USER`
- `SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH`

The build and smoke scripts in this directory expect the matching public key path to be provided on the host as well.

## What the prepared image contains

The guest bootstrap path installs or enables:

- OpenSSH server
- Git
- Python and `pip`
- Node and `npm`
- browser-related system packages needed for Playwright-style workloads
- Docker Engine inside the guest
- a `systemd` oneshot bootstrap service that prepares `/workspace`

Operator-visible storage behavior in the first pass:

- `disk_limit_mb` is split 50/50 between the writable guest system disk and the persistent workspace disk
- guest-local Docker data stays on the writable system disk, so it counts against the sandbox disk budget instead of using separate host-side storage

## Files

- `build-base-image.sh`
  - prepares an `or3`-ready qcow2 base image from a cloud image
- `smoke-ssh.sh`
  - boots a prepared image and verifies SSH reachability plus the readiness marker
- `cloud-init/user-data.tpl`
  - cloud-init template used for first boot preparation
- `cloud-init/meta-data.tpl`
  - cloud-init metadata template
- `systemd/or3-bootstrap.sh`
  - guest-side bootstrap script that formats or mounts the workspace disk and writes the readiness marker
- `systemd/or3-bootstrap.service`
  - systemd unit that runs the bootstrap script at boot

## Lightweight init and supervision choice

The first pass uses the guest's existing `systemd` environment rather than adding another process manager.

That gives enough behavior for this phase:

- boot-time bootstrap
- service restart semantics
- Docker daemon management inside the guest

The control plane remains a single Go process outside the guest.

## Expected runtime contract

The current `qemu` runtime assumes the operator-provided base image already supports:

- the configured SSH user
- the configured authorized SSH key
- successful boot to an SSH-reachable state
- the readiness marker path created by guest bootstrap

`build-base-image.sh` and `smoke-ssh.sh` are the intended way to produce and validate that image before using it in the daemon.
