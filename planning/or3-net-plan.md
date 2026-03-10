# OR3 Net Plan for `or3-sandbox`

> This file describes the work that belongs **inside the `or3-sandbox` repo** as part of the broader OR3 Network initiative.
> For the full network plan see `or3-net/plan.md` and `or3-net/planning/`.

---

## Context — How does `or3-sandbox` fit into OR3 Network?

`or3-sandbox` is a Go daemon (`sandboxd`) that manages isolated execution environments (Docker containers or QEMU VMs). It already exposes a complete REST API for sandbox lifecycle, command execution, file transfer, TTY sessions, tunnels, snapshots, quotas, and metrics.

In OR3 Network, `or3-sandbox` becomes the **first concrete node backend**. `or3-net` will run a "sandbox-backed node adapter" that wraps `or3-sandbox`'s API to implement the OR3 node protocol. This adapter lives in `or3-net`, not in `or3-sandbox`.

**`or3-sandbox` itself does not need to know about OR3 Network concepts** (nodes, manifests, leases, job routing). It just needs to keep its existing API stable, well-documented, and complete enough that a TypeScript SDK can consume it reliably.

---

## What changes in `or3-sandbox`

### 1. API contract audit for SDK completeness

`or3-net` will build a TypeScript SDK (`@or3/sandbox-sdk`) that wraps every `sandboxd` endpoint. The SDK needs complete, predictable contracts for:

| API area | Key endpoints | What the SDK needs |
|---|---|---|
| **Lifecycle** | `POST /v1/sandboxes`, `DELETE`, start/stop/suspend/resume | Typed request/response for `CreateSandboxRequest`, `Sandbox`, lifecycle state transitions |
| **Exec** | `POST /v1/sandboxes/:id/exec`, `?stream=1` | Typed `ExecRequest`, `Execution` result, SSE event format (`stdout`, `stderr`, `result` events) |
| **TTY** | `GET /v1/sandboxes/:id/tty` (WebSocket) | WebSocket upgrade handshake, `TTYRequest` init payload, binary frame format |
| **Files** | `GET/PUT/DELETE /v1/sandboxes/:id/files/*` | File read/write/delete/mkdir contracts, content types, error shapes |
| **Tunnels** | `/v1/sandboxes/:id/tunnels`, `/v1/tunnels/:id` | `CreateTunnelRequest`, `Tunnel`, signed URL generation, revocation |
| **Snapshots** | `/v1/sandboxes/:id/snapshots`, `/v1/snapshots/:id`, restore | `CreateSnapshotRequest`, `Snapshot`, `RestoreSnapshotRequest`, status tracking |
| **Runtime** | `/v1/runtime/info`, `/v1/runtime/health`, `/v1/runtime/capacity` | `RuntimeInfo`, `RuntimeHealth`, capacity report shape |
| **Quotas** | `/v1/quotas/me` | `TenantQuota` shape |
| **Metrics** | `/metrics` | Prometheus text format (consumed as raw text) |

**Action needed:** Review each endpoint and confirm the request/response types in `internal/model/model.go` are fully documented and that error responses follow a consistent shape. Fill any gaps.

### 2. SSE exec streaming contract

The current SSE exec implementation (in `router.go`, `streamExec` function) emits three event types:

```
event: stdout
data: <raw output bytes>

event: stderr
data: <raw output bytes>

event: result
data: <JSON Execution object>
```

The TypeScript SDK needs to know:
- Whether `stdout`/`stderr` data is newline-delimited or can contain arbitrary bytes.
- Whether the `result` event is always the last event in the stream.
- What happens if the exec times out or the sandbox crashes mid-stream (does the connection close cleanly? is there an error event?).
- Whether the SSE stream includes keep-alive comments for long-running commands.

**Action needed:** Document the exact SSE contract. If edge cases (timeout, crash, disconnect) are not handled consistently, add handling and tests.

### 3. TTY WebSocket contract

The TTY handler (in `router.go`, `handleTTY` function) uses:
- WebSocket upgrade on `GET /v1/sandboxes/:id/tty`.
- Client sends a `TTYRequest` JSON message as the first frame.
- Server sends binary frames with PTY output.
- Client sends binary frames with PTY input.

The SDK needs to know:
- The exact `TTYRequest` fields and defaults.
- Whether resize messages are supported (and their format).
- How the session ends (server close, client close, timeout).
- Whether `CloseTTYSession` is called automatically on disconnect.

**Action needed:** Document the WebSocket protocol. Confirm resize support if it exists.

### 4. Warm-pool reset hooks

When `or3-net` uses sandbox-backed nodes, it will maintain a "warm pool" of pre-reset sandboxes for fast job startup. After each job, the adapter needs to:

1. Kill all processes in the sandbox.
2. Scrub the workspace filesystem.
3. Rotate any sandbox-specific credentials.
4. Verify the sandbox is healthy before returning it to the pool.

The adapter will call existing `sandboxd` endpoints to do this (stop, start, exec cleanup commands, health check). But the reliability of this flow depends on:

- **Stop/start being idempotent and fast** — If a sandbox is already stopped, does `stop` succeed or error?
- **Exec working immediately after start** — Is there a readiness delay the adapter must account for?
- **Health check availability** — Can the adapter call `GET /v1/runtime/health` or per-sandbox health to verify a specific sandbox is ready?

**Action needed:** Document the stop→start→exec readiness sequence and any idempotency guarantees. If per-sandbox health checks don't exist, note that as a gap for the adapter to work around.

### 5. Error response consistency

The TypeScript SDK needs a predictable error shape across all endpoints. Currently, some handlers use `http.Error()` (plain text) and others use `handleError()` (which may or may not produce JSON).

**Action needed:** Audit error responses and ensure they follow a consistent JSON shape, at least for 4xx and 5xx responses. A minimal shape like `{ "error": "message", "code": "..." }` is sufficient.

---

## What does NOT change

- **`sandboxd` architecture** — single-process Go daemon with SQLite, runtime abstraction, auth middleware, quota enforcement. No architectural changes.
- **Runtime backends** — Docker and QEMU runtimes stay as they are.
- **Auth model** — static-token and JWT auth. No new auth providers for OR3 Network. `or3-net` authenticates as a regular tenant.
- **Quota enforcement** — `or3-net` is subject to the same tenant quotas as any other API consumer.
- **Tunnel, snapshot, file APIs** — no functional changes unless the SDK audit finds missing contracts.

---

## Design decisions

| Decision | Rationale |
|---|---|
| No OR3 Network concepts in `sandboxd` | `or3-sandbox` is a generic sandbox daemon. Node protocol, leases, and manifests belong in `or3-net`. |
| SDK lives in `or3-net`, not here | The TypeScript SDK is consumed by `or3-net` and shouldn't create a build dependency in the opposite direction. |
| `or3-net` authenticates as a tenant | Simplest integration path — `or3-net` gets a static token or JWT, subject to the same quota/auth rules as `sandboxctl`. |
| Focus on contract documentation | Most of the API surface already works. The main risk is undocumented behavior that the SDK will mishandle. |

---

## Affected files and areas

| Area | Likely files | Notes |
|---|---|---|
| API docs | `docs/api-reference.md` | Expand with typed request/response examples for every endpoint |
| SSE contract | `internal/api/router.go` (streamExec) | Document event format, edge cases, add error events if missing |
| TTY contract | `internal/api/router.go` (handleTTY) | Document WebSocket protocol, resize, session lifecycle |
| Error responses | `internal/api/router.go`, `internal/api/errors.go` | Audit and normalize to consistent JSON error shape |
| Model types | `internal/model/model.go` | Ensure all request/response structs have doc comments and JSON tags |
| Warm-pool readiness | `docs/` or inline in `internal/service/` | Document stop/start idempotency and exec readiness sequence |
| Tests | `internal/api/*_test.go` | Add/extend coverage for SSE event ordering, TTY lifecycle, error shapes |

---

## Tasks

- [ ] **API contract audit** — Walk through every endpoint in `internal/api/router.go` and confirm the request/response types in `internal/model/model.go` are complete, have doc comments, and have consistent JSON tags. Record any gaps.
- [ ] **SSE exec documentation** — Document the exact SSE event format for `?stream=1` exec: event names, data encoding, terminal event guarantees, timeout/crash behavior. Add an error event type if one doesn't exist.
- [ ] **TTY WebSocket documentation** — Document the WebSocket handshake, `TTYRequest` init frame, binary I/O frame format, resize support (if any), and session teardown behavior.
- [ ] **Error response normalization** — Audit all `http.Error()` and `handleError()` call sites. Ensure 4xx/5xx responses return a consistent JSON shape. Add a shared error response helper if needed.
- [ ] **Warm-pool readiness documentation** — Document the stop→start→exec readiness sequence, idempotency of lifecycle operations, and how the adapter should verify a sandbox is healthy before reuse.
- [ ] **Model type completeness** — Ensure all structs in `internal/model/model.go` that appear in API responses have exported fields with JSON tags and doc comments. Add any missing fields that the API actually returns but the model doesn't declare.
- [ ] **Regression tests** — Add or extend tests for: SSE event ordering and terminal event, TTY session lifecycle, error response shapes across endpoints, lifecycle idempotency (stop an already-stopped sandbox, start an already-running one).
- [ ] **API reference doc** — Expand `docs/api-reference.md` with typed request/response examples for the endpoints the SDK will consume most heavily: create, exec (sync + stream), files, snapshots, and runtime health.