# OpenClaw Preset

This preset starts an OpenClaw gateway inside a Docker sandbox, waits for it to become healthy, and publishes the Control UI through an OR3 tunnel.

It is tuned for the browser flow we debugged in this repo:

- the gateway binds to loopback inside the sandbox
- the tunnel is private and token-protected
- `sandboxctl preset run openclaw` prints a browser-ready `dashboard_url`
- OpenRouter settings can come from shell env, `--env`, or a repo-local `.env`

## Requirements

- `sandboxd` running in Docker mode
- `OPENCLAW_GATEWAY_TOKEN` set in your shell
- `OPENROUTER_API_KEY` set in your shell or in a local `.env` file if you want hosted model access through OpenRouter
- optional `OPENCLAW_MODEL` set in your shell or local `.env`
- operator policy must allow the chosen image and tunnel configuration

## Configuration inputs

The preset currently understands these inputs:

- `OPENCLAW_GATEWAY_TOKEN` — required gateway auth token for the Control UI and API
- `OPENROUTER_API_KEY` — optional OpenRouter key for hosted model access
- `OPENCLAW_MODEL` — optional default model, defaulting to `minimax/minimax-m2.5`

Input precedence for `sandboxctl preset run` is:

1. explicit `--env KEY=VALUE`
2. existing process environment
3. local `.env` in the current working directory
4. preset defaults

The local `.env` load is non-overriding, so checked-in defaults or shell exports still win.

## Run

```bash
export OPENCLAW_GATEWAY_TOKEN=$(openssl rand -hex 32)
export OPENROUTER_API_KEY=sk-or-v1-...
export OPENCLAW_MODEL=minimax/minimax-m2.5
go run ./cmd/sandboxctl preset run openclaw --cleanup never
```

You can also keep these in a repo-local `.env` and run the preset without repeating `--env` flags:

```dotenv
OPENCLAW_GATEWAY_TOKEN=...
OPENROUTER_API_KEY=sk-or-v1-...
OPENCLAW_MODEL=minimax/minimax-m2.5
```

When `OPENROUTER_API_KEY` is present, the preset seeds OpenClaw with:

- `agents.defaults.model.primary`
- `agents.defaults.models`
- `~/.openclaw/.env` containing `OPENROUTER_API_KEY`
- `env.shellEnv.enabled=true` so expected provider keys can still be imported safely when missing

If `OPENCLAW_MODEL` is given without an explicit `openrouter/` prefix, the preset normalizes it to `openrouter/<provider>/<model>` so values like `minimax/minimax-m2.5` work as expected with OpenRouter.

For example:

- `OPENCLAW_MODEL=minimax/minimax-m2.5` becomes `openrouter/minimax/minimax-m2.5`
- `OPENCLAW_MODEL=openrouter/anthropic/claude-sonnet-4-5` is used as-is

The runner prints:

- the sandbox ID
- the tunnel endpoint
- the tunnel access token for this private token-protected tunnel
- a signed `tunnel_browser_url`
- a browser-ready `dashboard_url` that already includes the gateway token fragment

Typical output looks like:

```text
sandbox_id=sbx-...
tunnel_endpoint=http://127.0.0.1:8080/v1/tunnels/tun-.../proxy
tunnel_access_token=ttok-...
tunnel_browser_url=http://127.0.0.1:8080/v1/tunnels/tun-.../proxy/?or3_exp=...&or3_sig=...
dashboard_url=http://127.0.0.1:8080/v1/tunnels/tun-.../proxy/?or3_exp=...&or3_sig=...#token=<gateway-token>
```

Use `dashboard_url` for browser testing. It is the most reliable path because it bootstraps both the tunnel browser session and the OpenClaw gateway token.

## Verify

Call the tunnel with your API token plus the printed tunnel token:

```bash
curl -i \
	-H 'Authorization: Bearer dev-token' \
	-H 'X-Tunnel-Token: REAL_TUNNEL_TOKEN' \
	'http://127.0.0.1:8080/v1/tunnels/<tunnel-id>/proxy/healthz'
```

You can also sanity-check the service from inside the sandbox:

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc 'curl -i http://127.0.0.1:18789/healthz'
```

Inspect the configured default model inside the sandbox:

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc \
	'HOME=/home/node openclaw config get agents.defaults.model.primary'
```

Inspect OpenClaw model/provider status:

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc \
	'HOME=/home/node openclaw models status | head -40'
```

With OpenRouter configured, you should see a default model like `openrouter/minimax/minimax-m2.5` and an `OPENROUTER_API_KEY` source in the auth section.

## Browser notes

- Use the printed `dashboard_url`, not just the raw tunnel endpoint.
- The browser tunnel session and the OpenClaw gateway token are separate things:
	- the tunnel session is bootstrapped by the signed URL
	- the OpenClaw gateway auth is bootstrapped by `#token=<gateway-token>`
- OR3 serves a small bootstrap page before redirecting into the UI. That page clears a stale stored `gatewayUrl` from browser storage so the OpenClaw UI reconnects to the current tunnel instead of an older revoked one.

## Troubleshooting

### UI says `Health Offline` or shows WebSocket `1006`

Most likely causes:

- you opened the raw tunnel URL instead of the printed `dashboard_url`
- the signed browser URL expired
- the browser has stale OpenClaw settings from a previous tunnel

Try this first:

- rerun `sandboxctl preset run openclaw --cleanup never`
- open the newly printed `dashboard_url`
- if needed, use a fresh incognito window

### OpenClaw still thinks the default provider is Anthropic

That usually means the gateway started without OpenRouter config being applied.

Check:

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc \
	'HOME=/home/node openclaw models status | head -20'
```

If the default is still `anthropic/claude-opus-4-6`, verify that:

- `OPENROUTER_API_KEY` was present in shell env, `--env`, or local `.env`
- `OPENCLAW_MODEL` is set to either `minimax/minimax-m2.5` or an explicit `openrouter/...` model id
- the preset run you used includes the updated OpenClaw preset in this repo

### Agent says `No API key found for provider "anthropic"`

That specific message means the active model still points at Anthropic.

Confirm the configured model:

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc \
	'HOME=/home/node openclaw config get agents.defaults.model.primary'
```

It should be an OpenRouter-prefixed model such as `openrouter/minimax/minimax-m2.5`, not `anthropic/...`.

## Notes

- This preset demonstrates detached startup plus HTTP readiness plus tunnel exposure.
- The preset also seeds OpenClaw's local config with the same gateway token, so the dashboard can authenticate without manual Control UI token entry.
- `sandboxctl preset run` also reads a non-overriding `.env` file from the current working directory for preset inputs, so repo-local secrets work without repeating `--env KEY=VALUE`.
- OpenClaw provider credentials and default model selection are separate concerns. Supplying `OPENROUTER_API_KEY` alone is not enough; the preset also sets `agents.defaults.model.primary`.
- The startup command runs from `/app`, which matches the image's configured working directory.
- The gateway binds to `loopback`, which works with OR3's in-sandbox tunnel proxy and avoids OpenClaw's non-loopback Control UI origin requirement.
- The future QEMU version should keep the same UX, but it will rely on a prepared guest profile plus startup scripts instead of a direct container image.