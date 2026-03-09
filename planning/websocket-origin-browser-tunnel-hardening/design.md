# Design

## Overview

Make a small, explicit hardening pass in `internal/api`:

- replace the implicit `websocket.Upgrader{}` behavior with a router-owned origin check derived from `OperatorHost`
- tighten the tunnel bootstrap cookie and document the browser tunnel flow as capability auth, not a full session model

This stays proportional to the issue and avoids adding a new auth architecture.

## Affected areas

- `internal/api/router.go`
  - add an explicit WebSocket origin validator
  - review tunnel cookie attributes and issuance logic
- `internal/api/integration_test.go`
  - keep/add focused coverage for allowed same-origin upgrades, rejected cross-origin upgrades, cookie bootstrap, expiry, and revocation behavior
- `docs/operations` or nearby API docs
  - clarify the browser tunnel security model and its limits if current docs overstate it

## Control flow / architecture

1. Browser requests a signed tunnel URL.
2. Server validates the signed URL and issues a tunnel-scoped cookie.
3. Browser follows the clean proxy URL or WebSocket URL.
4. Proxy/WebSocket authorization accepts the tunnel capability only when:
   - the tunnel is still valid
   - cookie or signed URL material is valid
   - the WebSocket `Origin` matches the configured operator origin policy

## Data and persistence

- No SQLite or migration changes.
- No new server-side session store.
- Continue using tunnel-scoped signed material and cookie state only.

## Interfaces and types

Likely small additions only:

```go
func newWebSocketCheckOrigin(operatorHost string) func(*http.Request) bool
func sameOriginOperatorRequest(r *http.Request, operatorHost string) bool
```

No new public API surface is required unless a doc-facing config knob is truly needed.

## Failure modes and safeguards

- If `OperatorHost` is unset or malformed, fail closed for browser WebSocket origin checks where practical.
- Keep non-browser token-based access behavior unchanged unless it depends on cross-origin browser assumptions.
- Avoid loosening compatibility beyond the configured operator origin.

## Testing strategy

- Add or tighten `internal/api/integration_test.go` coverage for:
  - same-origin WebSocket success
  - cross-origin WebSocket rejection
  - signed URL bootstrap cookie attributes and path scoping
  - tunnel revocation invalidating follow-up browser access
- Keep tests local to the existing API harness.
