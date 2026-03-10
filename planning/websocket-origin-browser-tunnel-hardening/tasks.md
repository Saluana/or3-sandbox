# Tasks

## 1. WebSocket origin policy (Req 1)

- [x] Add an explicit `CheckOrigin` function in `internal/api/router.go` based on `OperatorHost`.
- [x] Allow same-origin browser upgrades and reject mismatched origins.
- [x] Add or tighten integration tests in `internal/api/integration_test.go` for allowed same-origin and denied cross-origin WebSocket upgrades.

## 2. Browser tunnel capability hardening (Req 2, 3)

- [x] Review tunnel bootstrap cookie issuance in `internal/api/router.go` and tighten low-risk attributes like `HttpOnly`, `SameSite`, `Secure`, path scoping, and expiry handling.
- [x] Keep the current signed URL + tunnel cookie capability model; do not add a general browser session system.
- [x] Add focused tests for cookie bootstrap, tunnel-scoped reuse, and access failure after revoke/expiry.

## 3. Clarify the security model (Req 3)

- [x] Update the relevant docs or API comments to state that browser tunnel auth is capability-based, not a full user session model.
- [x] Keep the wording narrow and avoid overclaiming device binding, CSRF-wide session protection, or global revocation semantics.
