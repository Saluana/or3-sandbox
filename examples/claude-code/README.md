# Claude Code Preset

This preset installs `@anthropic-ai/claude-code` in a Docker sandbox and runs a one-shot prompt.

It uses the shared `runtime` profile because it needs a general Node-capable image, but not browser tooling or inner Docker.

## Requirements

- `sandboxd` running with `SANDBOX_RUNTIME=docker`
- Docker available to the daemon
- `ANTHROPIC_AUTH_TOKEN` set in your shell

## Run

```bash
export ANTHROPIC_AUTH_TOKEN=your-token
go run ./cmd/sandboxctl preset run claude-code
```

Optional overrides:

```bash
go run ./cmd/sandboxctl preset run claude-code \
  --env ANTHROPIC_MODEL=claude_sonnet4 \
  --set memory-mb=2048 \
  --cleanup never
```

## Notes

- This is a Docker-first preset.
- Production setups should prefer a pinned curated image ref in `SANDBOX_POLICY_ALLOWED_IMAGES` instead of a broad mutable tag.
- The future QEMU version should offer the same preset UX, but through a guest profile plus startup/bootstrap steps.