# API Reference

This is a short, practical API guide.

All protected endpoints require a bearer token:

```text
Authorization: Bearer <token>
```

## Health and status

- `GET /healthz` — basic health check
- `GET /v1/runtime/info` — authenticated runtime backend summary
- `GET /v1/runtime/health` — runtime and sandbox health view
- `GET /v1/runtime/capacity` — operator capacity summary with quota pressure
- `GET /v1/quotas/me` — current tenant quota and usage
- `GET /metrics` — scrape-friendly counters for operators and monitoring systems

## Sandboxes

- `POST /v1/sandboxes` — create sandbox
- `GET /v1/sandboxes` — list sandboxes
- `GET /v1/sandboxes/{id}` — inspect one sandbox
- `DELETE /v1/sandboxes/{id}` — delete sandbox
- `POST /v1/sandboxes/{id}/start` — start sandbox
- `POST /v1/sandboxes/{id}/stop` — stop sandbox
- `POST /v1/sandboxes/{id}/suspend` — suspend sandbox
- `POST /v1/sandboxes/{id}/resume` — resume sandbox

## Exec and terminal

- `POST /v1/sandboxes/{id}/exec` — run a command
- `POST /v1/sandboxes/{id}/exec?stream=1` — stream command output with SSE
- `GET /v1/sandboxes/{id}/tty` — open interactive terminal via WebSocket

## Files

- `PUT /v1/sandboxes/{id}/files/{path}` — write a file
- `GET /v1/sandboxes/{id}/files/{path}` — read a file
- `DELETE /v1/sandboxes/{id}/files/{path}` — delete a path
- `POST /v1/sandboxes/{id}/mkdir` — make a directory

Binary-safe transfer notes:

- `GET /v1/sandboxes/{id}/files/{path}?encoding=base64` returns `content_base64` with `encoding="base64"`
- `PUT /v1/sandboxes/{id}/files/{path}` accepts `{ "encoding": "base64", "content_base64": "..." }` for binary-safe uploads
- plain UTF-8 text uploads and downloads still work the same as before

## Tunnels

- `POST /v1/sandboxes/{id}/tunnels` — create tunnel
- `GET /v1/sandboxes/{id}/tunnels` — list tunnels for a sandbox
- `DELETE /v1/tunnels/{id}` — revoke tunnel

## Snapshots

- `GET /v1/sandboxes/{id}/snapshots` — list snapshots for a sandbox
- `POST /v1/sandboxes/{id}/snapshots` — create snapshot
- `GET /v1/snapshots/{id}` — inspect one snapshot
- `POST /v1/snapshots/{id}/restore` — restore snapshot

## Simple create example

```bash
curl -X POST http://127.0.0.1:8080/v1/sandboxes \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{
    "base_image_ref": "alpine:3.20",
    "cpu_limit": 1,
    "memory_limit_mb": 512,
    "pids_limit": 128,
    "disk_limit_mb": 2048,
    "network_mode": "internet-enabled",
    "start": true
  }'
```

## Notes for beginners

- JSON field names use `snake_case`
- if you are new, it is easiest to omit optional duration fields and let the server use defaults
- streamed exec output uses server-sent events, not plain JSON lines
- terminal sessions use WebSocket, not regular HTTP request-response
- some admin endpoints, such as runtime capacity and metrics, may require a stronger role than normal sandbox use
