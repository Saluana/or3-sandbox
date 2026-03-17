# API Reference

This document is the canonical HTTP, SSE, and WebSocket contract for `sandboxd`.

All JSON fields use `snake_case`. Protected endpoints require:

```text
Authorization: Bearer <token>
```

Boundary notes:

- `sandboxd` is the raw tenant-scoped provider API. Fields such as `tenant_id` are part of the daemon's internal/operator contract.
- When a higher-level service such as `or3-net` fronts this API, that adapter is responsible for mapping upstream `workspace_id` context into sandbox auth and for preventing raw `tenant_id` from becoming a public UI contract.
- Service-account callers should use stored minimal scopes for the specific workflow. Routine workspace automation should not rely on operator-only scopes such as `admin.inspect`.

## Error format

All non-streaming `4xx` and `5xx` API responses use the same JSON envelope:

```json
{
  "error": "not found",
  "code": "not_found",
  "status": 404
}
```

Common codes:

- `unauthorized`
- `forbidden`
- `not_found`
- `method_not_allowed`
- `invalid_request`
- `conflict`
- `rate_limited`
- `bad_gateway`

## Health and runtime

### `GET /healthz`

Unauthenticated process health check.

Response:

```json
{ "ok": true }
```

### `GET /v1/runtime/info`

Returns the configured runtime backend and enabled runtime selections for the current deployment.

For QEMU deployments, this response also includes the configured guest-image identity when the daemon can read the image sidecar contract.

Example response:

```json
{
  "backend": "qemu",
  "class": "vm",
  "default_runtime_selection": "qemu-professional",
  "enabled_runtime_selections": ["qemu-professional"],
  "guest_image": {
    "path": "/var/lib/or3/images/or3-guest-core.qcow2",
    "sidecar_path": "/var/lib/or3/images/or3-guest-core.qcow2.or3.json",
    "contract_version": "v1",
    "build_version": "2025.03.17",
    "git_sha": "abc1234",
    "image_sha256": "...",
    "profile": "core",
    "capabilities": ["exec", "files", "pty"],
    "allowed_features": ["exec", "files", "pty"],
    "control_mode": "agent",
    "control_protocol_version": "1",
    "workspace_contract_version": "1"
  }
}
```

Notes:

- `guest_image_error` is returned instead of `guest_image` when the daemon has a configured QEMU base image but the sidecar contract cannot be loaded.

### `GET /v1/runtime/health`

Admin/service-readable runtime health report including observed per-sandbox status.

Example response:

```json
{
  "backend": "docker",
  "healthy": true,
  "checked_at": "2025-01-01T00:00:00Z",
  "runtime_selection_counts": { "docker-dev": 1 },
  "status_counts": { "running": 1 },
  "agent_sessions": {
    "sessions_opened": 3,
    "sessions_reused": 12,
    "sessions_invalidated": 0,
    "sessions_closed": 2,
    "buffered_exec_events": 1,
    "buffered_file_events": 1,
    "dropped_exec_events": 0,
    "dropped_file_events": 0
  },
  "sandboxes": [
    {
      "sandbox_id": "sbx-123",
      "tenant_id": "tenant-a",
      "runtime_selection": "docker-dev",
      "persisted_status": "running",
      "observed_status": "running",
      "runtime_id": "sbx-123",
      "runtime_status": "running",
      "pid": 1234,
      "ip_address": "172.18.0.2"
    }
  ]
}
```

Notes:

- `agent_sessions` is present for runtimes that expose guest-agent session telemetry, such as QEMU agent mode.
- The buffered counters are useful for spotting early async guest events that arrived before the host registered the matching exec or file stream.

### `GET /v1/runtime/capacity`

Returns runtime/operator capacity data. Treat this as an operator-facing report rather than a stable end-user UI payload.

Operationally, this report is where operators should look for degraded sandbox counts, runtime-selection mix, guest-profile mix, snapshot counts, and audit-derived pressure signals.

### `GET /v1/quotas/me`

Returns the caller's effective quota and current usage view.

### `GET /metrics`

Returns Prometheus text format. This endpoint is intentionally raw text, not JSON.

The metrics surface includes launch-critical counters such as runtime health, storage pressure, tunnel events, dangerous-profile exceptions, promoted-image totals, and release-gate freshness.

## Sandboxes

### `POST /v1/sandboxes`

Create a sandbox.

Example request:

```json
{
  "base_image_ref": "alpine:3.20",
  "cpu_limit": 1,
  "memory_limit_mb": 512,
  "pids_limit": 128,
  "disk_limit_mb": 2048,
  "network_mode": "internet-enabled",
  "allow_tunnels": true,
  "start": true
}
```

Example response:

```json
{
  "id": "sbx-123",
  "tenant_id": "tenant-a",
  "status": "running",
  "runtime_selection": "docker-dev",
  "runtime_backend": "docker",
  "runtime_class": "trusted-docker",
  "base_image_ref": "alpine:3.20",
  "cpu_limit": 1,
  "memory_limit_mb": 512,
  "pids_limit": 128,
  "disk_limit_mb": 2048,
  "network_mode": "internet-enabled",
  "allow_tunnels": true,
  "runtime_id": "sbx-123",
  "runtime_status": "running",
  "created_at": "2025-01-01T00:00:00Z",
  "updated_at": "2025-01-01T00:00:00Z",
  "last_active_at": "2025-01-01T00:00:00Z"
}
```

Notes:

- If `runtime_selection` is omitted, the daemon default is used.
- Fractional CPU values are accepted only on backends that support them.
- `allow_tunnels` defaults from server policy if omitted.
- The returned `tenant_id` is a raw provider ownership field. Upstream adapters should normalize ownership to their public workspace contract instead of forwarding `tenant_id` directly to UI clients.

### `GET /v1/sandboxes`

List non-deleted sandboxes visible to the current tenant.

### `GET /v1/sandboxes/{id}`

Return one sandbox.

### `DELETE /v1/sandboxes/{id}`

Delete a sandbox. Running or suspended sandboxes are stopped first.

### Lifecycle actions

- `POST /v1/sandboxes/{id}/start`
- `POST /v1/sandboxes/{id}/stop`
- `POST /v1/sandboxes/{id}/suspend`
- `POST /v1/sandboxes/{id}/resume`

Lifecycle responses return the updated `Sandbox` object.

`stop` accepts:

```json
{ "force": true }
```

Warm-pool and reuse notes:

- `stop -> start -> exec` is the supported reset sequence for higher-level adapters.
- There is no dedicated per-sandbox readiness endpoint today; adapters should treat a successful `start` response as the control-plane transition and then perform a lightweight `exec` probe if they require guest readiness confirmation.
- `GET /v1/runtime/health` can be used to inspect observed per-sandbox runtime state, but it is a tenant/runtime report, not a low-latency readiness RPC.
- Callers should not assume lifecycle actions are idempotent across all backends; if an adapter wants “already stopped” or “already running” semantics, it should inspect the sandbox first and only issue the transition when needed.

## Exec

### `POST /v1/sandboxes/{id}/exec`

Run a command and return a single `Execution` result.

Example request:

```json
{
  "command": ["sh", "-lc", "echo hello"],
  "cwd": "/workspace",
  "timeout": 30000000000,
  "detached": false
}
```

Notes:

- `timeout` is encoded as a Go `time.Duration` in JSON, so callers should send nanoseconds when using raw JSON. Many clients will prefer their SDK to handle this serialization.
- If `timeout` is omitted or `0`, the API uses a default of five minutes.

Example response:

```json
{
  "id": "exec-123",
  "sandbox_id": "sbx-123",
  "tenant_id": "tenant-a",
  "command": "sh -lc echo hello",
  "cwd": "/workspace",
  "timeout_seconds": 30,
  "status": "succeeded",
  "exit_code": 0,
  "stdout_preview": "hello\n",
  "stderr_truncated": false,
  "stdout_truncated": false,
  "started_at": "2025-01-01T00:00:00Z",
  "completed_at": "2025-01-01T00:00:01Z",
  "duration_ms": 1000
}
```

### `POST /v1/sandboxes/{id}/exec?stream=1`

Run a command and stream output as Server-Sent Events.

Headers:

```text
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
```

Event types:

- `stdout` — streamed stdout chunk
- `stderr` — streamed stderr chunk
- `result` — final serialized `Execution` payload on success
- `error` — terminal serialized `ErrorResponse` if the exec cannot start or is rejected before a result is available

Example success stream:

```text
event: stdout
data: hello\n

event: result
data: {"id":"exec-123","status":"succeeded"}
```

Example failure stream:

```text
event: error
data: {"error":"sandbox sbx-123 is not running","code":"invalid_request","status":400}
```

Contract details:

- `stdout` and `stderr` payloads are text chunks. Newlines are escaped as `\n` in the SSE `data:` line.
- The implementation does not emit keep-alive comments today.
- On successful completion, `result` is the final event.
- If the command cannot be started or is rejected before completion, the stream emits `error` and does not emit `result`.

## TTY WebSocket

### `GET /v1/sandboxes/{id}/tty`

Upgrade to a WebSocket-backed interactive terminal.

Handshake:

- HTTP method must be `GET`
- Authorization is still required on the upgrade request
- The first client frame must be a JSON `TTYRequest`

Example init frame:

```json
{
  "command": ["sh"],
  "cwd": "/workspace",
  "cols": 120,
  "rows": 40,
  "env": {"TERM": "xterm-256color"}
}
```

Frame behavior:

- Server -> client: binary frames carrying PTY output bytes
- Client -> server: binary frames carrying PTY input bytes
- Client -> server: optional JSON text resize frame

Resize frame format:

```json
{ "type": "resize", "rows": 40, "cols": 120 }
```

Session lifecycle:

- If the initial JSON frame is invalid, the server sends a text error frame and closes.
- When the socket disconnects, `CloseTTYSession` is called automatically.
- Resize updates are persisted through `UpdateTTYResize` when accepted.

## Files

### `GET /v1/sandboxes/{id}/files/{path}`

Read a workspace file.

Limits and safety:

- Paths stay inside the workspace root; traversal and symlink escapes are rejected.
- The daemon returns `413 payload_too_large` when the requested file exceeds the configured workspace file transfer limit.
- The daemon limit defaults to 64 MiB and can be changed with `SANDBOX_WORKSPACE_FILE_TRANSFER_MAX_MB`.
- These workspace files are staged runtime state inside the sandbox; the canonical host workspace remains outside `sandboxd` ownership.

Text response example:

```json
{
  "path": "notes.txt",
  "content": "hello",
  "size": 5,
  "encoding": "utf-8"
}
```

Binary-safe response example:

```json
{
  "path": "pixel.png",
  "content_base64": "iVBORw0KGgo=",
  "size": 10,
  "encoding": "base64"
}
```

Use `?encoding=base64` when the file may contain arbitrary bytes.

### `PUT /v1/sandboxes/{id}/files/{path}`

Write a file.

Limits and safety:

- Paths stay inside the workspace root; traversal and symlink escapes are rejected.
- File writes are capped at the configured workspace file transfer limit.
- The daemon limit defaults to 64 MiB and can be changed with `SANDBOX_WORKSPACE_FILE_TRANSFER_MAX_MB`.
- Oversized upload bodies fail with `413 payload_too_large`.

Text upload:

```json
{ "content": "hello" }
```

Binary-safe upload:

```json
{
  "encoding": "base64",
  "content_base64": "iVBORw0KGgo="
}
```

### `DELETE /v1/sandboxes/{id}/files/{path}`

Delete a file or directory path.

### `POST /v1/sandboxes/{id}/mkdir`

Create a directory.

Example request:

```json
{ "path": "dist/assets" }
```

### `POST /v1/sandboxes/{id}/workspace-import`

Import a gzip-compressed tar archive into the sandbox workspace.

Limits and safety:

- Paths in the archive stay inside the workspace root; traversal entries are rejected.
- Symlink, hard-link, device, FIFO, and extended-header tar entries are rejected.
- Extracted bytes are capped at the configured workspace transfer limit and oversized imports fail with `413 payload_too_large`.
- Imports merge into the existing workspace tree; they do not delete paths that are absent from the archive.

Request body:

- Raw `application/gzip` or `application/octet-stream` tar.gz payload.

### `POST /v1/sandboxes/{id}/workspace-export`

Export selected workspace paths as a gzip-compressed tar archive.

Example request:

```json
{
  "paths": ["README.md", "src"]
}
```

Notes:

- When `paths` is omitted or empty, the daemon exports the full workspace root.
- Export rejects workspace symlinks rather than following them.
- Successful responses stream binary tar.gz content with `Content-Type: application/gzip`.

Static preview guidance:

- `sandboxd` does not expose a dedicated static-site hosting API.
- Higher-level systems can use file reads for artifact retrieval and tunnels for live preview services.
- The stable distinction is: files are for workspace/static output access, tunnels are for live in-sandbox services.

## Tunnels

### `POST /v1/sandboxes/{id}/tunnels`

Create a tunnel.

Example request:

```json
{
  "target_port": 3000,
  "protocol": "http",
  "auth_mode": "token",
  "visibility": "private"
}
```

Example response:

```json
{
  "id": "tun-123",
  "sandbox_id": "sbx-123",
  "tenant_id": "tenant-a",
  "target_port": 3000,
  "protocol": "http",
  "auth_mode": "token",
  "visibility": "private",
  "endpoint": "http://127.0.0.1:8080/v1/tunnels/tun-123/proxy",
  "access_token": "ttok-abc",
  "access": {
    "requires_tenant_token": true,
    "tunnel_token_header": "X-Tunnel-Token",
    "tunnel_token_query": "token",
    "example_curl": "curl -H 'Authorization: Bearer <tenant-token>' -H 'X-Tunnel-Token: ttok-abc' 'http://127.0.0.1:8080/v1/tunnels/tun-123/proxy'"
  },
  "created_at": "2025-01-01T00:00:00Z"
}
```

Notes:

- `access_token` is returned on create and should be treated as a secret capability.
- `access` describes how to call the tunnel correctly from non-browser clients.
- Private tunnels normally require both the tenant bearer token and the tunnel token.
- `access_token` is intended for control-plane or trusted service use. Browser-facing clients should use the signed browser launch flow instead of receiving raw tunnel tokens.
- Revoked tunnels return `410 Gone` when accessed later.

### `GET /v1/sandboxes/{id}/tunnels`

List tunnels for a sandbox.

### `DELETE /v1/tunnels/{id}`

Revoke a tunnel.

### `POST /v1/tunnels/{id}/signed-url`

Create a short-lived browser-launch URL for one tunnel proxy path.

Example request:

```json
{
  "path": "/",
  "ttl_seconds": 300
}
```

Example response:

```json
{
  "url": "http://127.0.0.1:8080/v1/tunnels/tun-123/proxy/?or3_exp=...&or3_sig=...",
  "expires_at": "2025-01-01T00:05:00Z"
}
```

Signed browser launch contract:

- TTL defaults to five minutes and is capped at fifteen minutes.
- `path` must begin with `/` and is capability-scoped, including query string when present.
- Visiting the signed URL sets a narrow bootstrap cookie scoped to that tunnel proxy path and then returns an HTML bootstrap page that redirects into the proxied app.
- The bootstrap page is the supported browser-launch mechanism for dashboard-style apps.
- One-time launch capabilities return `capability_id` and are consumed on first successful bootstrap.
- This browser capability is distinct from tunnel-token auth:
  - tunnel token auth is a direct request capability for HTTP/WebSocket clients
  - signed browser launch auth is a browser-friendly bootstrap flow that installs a scoped cookie

### `GET /v1/tunnels/{id}/proxy...`

Proxy traffic into the sandbox service bound on `target_port`.

Auth options:

- owner tenant bearer auth
- `X-Tunnel-Token` header
- `?token=` query token
- signed browser cookie established by the signed-url flow
- public visibility, if enabled by policy

The proxy strips control-plane auth headers and the tunnel auth cookie/token before forwarding upstream.

For higher-level browser clients, signed browser launch is the supported capability surface. Avoid forwarding raw tunnel tokens or other control-plane credentials into user-visible pages.

## Snapshots

### `POST /v1/sandboxes/{id}/snapshots`

Create a snapshot.

Example request:

```json
{ "name": "before-upgrade" }
```

### `GET /v1/sandboxes/{id}/snapshots`

List snapshots for one sandbox.

### `GET /v1/snapshots/{id}`

Inspect one snapshot.

### `POST /v1/snapshots/{id}/restore`

Restore a snapshot into a target sandbox.

Example request:

```json
{ "target_sandbox_id": "sbx-123" }
```

Restore returns the updated target `Sandbox` object.

## Model summary

The main transport types live in `internal/model/model.go`:

- `Sandbox`
- `CreateSandboxRequest`
- `LifecycleRequest`
- `ExecRequest`
- `Execution`
- `TTYRequest`
- `TTYSession`
- `FileWriteRequest`
- `FileReadResponse`
- `CreateTunnelRequest`
- `CreateTunnelSignedURLRequest`
- `Tunnel`
- `TunnelSignedURL`
- `CreateSnapshotRequest`
- `Snapshot`
- `RestoreSnapshotRequest`
- `RuntimeInfo`
- `RuntimeHealth`
- `TenantQuota`
- `ErrorResponse`
