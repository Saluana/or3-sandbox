# Example Presets

These presets are examples for the shared preset runner used by both Docker and QEMU.

Use them with:

```bash
go run ./cmd/sandboxctl preset list
go run ./cmd/sandboxctl preset inspect playwright
go run ./cmd/sandboxctl preset run playwright
```

Important truths:

- these presets are orchestrated by `sandboxctl`, not by a separate SDK
- Docker and QEMU share the same manifest format, CLI commands, artifact flow, and cleanup policies
- Docker is the cheaper trusted path for image-shaped examples
- QEMU is the stronger isolation path, but it uses guest images or documented guest profiles instead of arbitrary container image references
- Docker-backed integration tests require a reachable Docker daemon/socket
- QEMU-backed integration tests require `SANDBOX_QEMU_BINARY`, `SANDBOX_QEMU_BASE_IMAGE_PATH`, `SANDBOX_QEMU_SSH_USER`, and `SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH`

Shared manifest notes:

- `runtime.allowed` keeps one manifest honest across runtimes
- `runtime.profile` is an optional guest-profile hint used by the QEMU path
- if a QEMU preset uses `${QEMU_GUEST_IMAGE}`, pass it with `--env QEMU_GUEST_IMAGE=/path/to/guest.qcow2` or override with `--set image=/path/to/guest.qcow2`

Shipped presets:

- `claude-code` — installs a coding CLI in a Node-capable sandbox and runs a sample prompt
- `playwright` — captures a screenshot and downloads it as a binary artifact
- `openclaw` — starts a detached service and exposes it through a tunnel
- `qemu-bootstrap` — runs bootstrap steps inside a documented QEMU guest profile and downloads text artifacts
- `qemu-service` — starts a guest-hosted HTTP service and publishes it through the shared tunnel flow
- `qemu-browser-artifact` — captures a screenshot-like artifact from a browser-capable guest profile

Common flags:

```bash
go run ./cmd/sandboxctl preset run claude-code --env ANTHROPIC_AUTH_TOKEN=... --cleanup never
go run ./cmd/sandboxctl preset run playwright --env TARGET_URL=https://example.com
go run ./cmd/sandboxctl preset run openclaw --env OPENCLAW_GATEWAY_TOKEN=... --keep
go run ./cmd/sandboxctl preset run qemu-bootstrap --env QEMU_GUEST_IMAGE="$SANDBOX_QEMU_BASE_IMAGE_PATH"
```

Useful overrides:

- `--set image=...`
- `--set cpu=2`
- `--set memory-mb=2048`
- `--set disk-mb=8192`
- `--set allow-tunnels=true`
- `--cleanup always|never|on-success`

The preset runner always prints the sandbox ID, and when tunnels are created it also prints the endpoint.