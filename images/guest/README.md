# Guest Image Path

This directory contains the guest-image preparation assets for the production-oriented `qemu` runtime.

The image contract is now intentionally split into two layers:

- a small immutable substrate that every supported profile shares
- additive profiles that opt into extra tooling such as language runtimes, browser libraries, inner Docker, or debug-only SSH conveniences

The production-default control path is the guest agent over virtio-serial. SSH is kept only for explicit compatibility and debug profiles.

## Fixed profiles

The supported profiles live under `images/guest/profiles/`:

- `core`
  - minimal production profile
  - agent-based control
  - no SSH
  - no inner Docker
- `runtime`
  - `core` plus Git, Python, and Node tooling
- `browser`
  - `runtime` plus browser-supporting system libraries
- `container`
  - `core` plus inner Docker service and `docker` group membership
- `debug`
  - compatibility and troubleshooting profile
  - keeps SSH and elevated conveniences
  - marked dangerous and production-ineligible by default policy unless explicitly allowed

`core` is the default profile and the intended production baseline.

## Substrate contract

Every supported image is expected to provide the same substrate behavior:

- the guest agent reachable via virtio-serial at `org.or3.guest_agent`
- the readiness marker at `/var/lib/or3/bootstrap.ready`
- a persistent `/workspace` filesystem on the secondary disk
- a `sandbox` workload user
- a separate `or3-agent` identity reserved for future in-guest control split

Profile manifests declare what gets layered on top of that substrate:

- control mode and protocol version
- workspace contract version
- declared capabilities
- allowed additive features
- package inventory
- whether SSH is present

The host-side sidecar contract format is written next to each built image as `*.or3.json` and is consumed by runtime and policy validation.

## SSH material

The daemon never stores guest SSH keys in SQLite, but most supported production profiles do not need SSH at all.

Only `debug` / `ssh-compat` images need the operator to provide:

- `SANDBOX_QEMU_SSH_USER`
- `SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH`
- `SANDBOX_QEMU_SSH_HOST_KEY_PATH`

`build-base-image.sh` captures the guest SSH host public key only for SSH-bearing profiles.

## What the prepared image contains

The shared image substrate installs or enables:

- the guest agent binary and systemd unit
- a `systemd` oneshot bootstrap service that prepares `/workspace`
- the selected profile's declared package set and services

Profile-specific additions come from the manifest overlays under `images/guest/profiles/`.

Notably:

- `core`, `runtime`, and `browser` do not grant passwordless sudo or Docker group membership to the workload user
- `container` is the only profile that enables inner Docker by default
- `debug` is the only profile that includes SSH and troubleshooting tools by default

Operator-visible storage behavior:

- `disk_limit_mb` is split 50/50 between the writable guest system disk and the persistent workspace disk
- guest-local Docker data stays on the writable system disk, so it counts against the sandbox disk budget instead of using separate host-side storage

## Files

- `build-base-image.sh`
  - builds a profile-resolved qcow2 guest image from a cloud image
  - injects `cmd/or3-guest-agent`
  - boots the image once and verifies the selected profile against the guest via the guest agent
  - emits a resolved manifest, versioned package inventory, and `*.or3.json` sidecar contract
- `smoke-ssh.sh`
  - boots an SSH-bearing compatibility/debug image and verifies SSH reachability plus the readiness marker
- `cloud-init/user-data.tpl`
  - cloud-init template used for profile-aware first boot preparation
- `cloud-init/meta-data.tpl`
  - cloud-init metadata template
- `profiles/*.json`
  - fixed supported guest profile manifests
- `systemd/or3-bootstrap.sh`
  - guest-side bootstrap script that formats or mounts the workspace disk and writes the readiness marker
- `systemd/or3-bootstrap.service`
  - systemd unit that runs the bootstrap script at boot
- `systemd/or3-guest-agent.service`
  - systemd unit that keeps the guest agent running

## Lightweight init and supervision choice

The guest uses its existing `systemd` environment rather than adding another process manager.

That gives enough behavior for this phase:

- boot-time bootstrap
- service restart semantics
- guest-agent supervision
- optional Docker daemon management in the `container` profile

The control plane remains a single Go process outside the guest.

## Expected runtime contract

The `qemu` runtime assumes the selected guest image already supports the sidecar-declared contract for its profile.

For agent-based profiles, that means:

- the guest agent protocol version declared by the sidecar
- the readiness marker path created by guest bootstrap
- successful guest-agent handshake after boot

For `debug` / `ssh-compat` profiles, the sidecar also declares SSH presence and the operator must provide the SSH user and pinned host key material.

## Building images

Build the default production profile:

```bash
images/guest/build-base-image.sh
```

Build a heavier profile:

```bash
PROFILE=runtime images/guest/build-base-image.sh
PROFILE=browser images/guest/build-base-image.sh
PROFILE=container images/guest/build-base-image.sh
```

Build the compatibility/debug profile with SSH:

```bash
PROFILE=debug SSH_PUBLIC_KEY_PATH=$HOME/.ssh/id_ed25519.pub images/guest/build-base-image.sh
```

Each build produces:

- a qcow2 image
- a resolved profile manifest copy
- a package inventory text file with the guest-observed Debian package versions for the selected profile packages
- a host-side `*.or3.json` contract file

## Reproducibility expectations

This pipeline does not yet fully vendor or mirror the upstream Ubuntu package repositories, so exact bit-for-bit rebuilds are still constrained by the moving cloud image and apt repository state.

The current reproducibility contract is:

- profile manifests may carry exact apt selections when the operator needs to use `package=version` syntax
- every build records the resolved package versions observed inside the booted guest in the emitted package inventory file
- every build records the image checksum and build metadata in the sidecar contract
- release promotion should retain the qcow2 image, its `*.or3.json` contract, the resolved manifest copy, and the versioned package inventory together

For production release candidates, treat the package inventory as the authoritative record of what was actually admitted into the guest image.

## Build-time smoke checks

`build-base-image.sh` now performs a bounded smoke pass against the freshly booted guest before the image is handed off:

- verifies guest-agent readiness
- verifies every manifest-declared package is actually installed in the guest
- emits the observed package/version inventory from the guest itself
- verifies SSH-bearing profiles really expose `sshd` and the `ssh` service
- verifies `container`/Docker-enabled profiles really expose Docker and the `docker` service

That smoke pass is intended to catch profile/contract drift at build time rather than deferring discovery to host-side operator drills.

Use those artifacts with `sandboxctl doctor --production-qemu` and the QEMU runtime policy checks before treating an image as production-ready.
