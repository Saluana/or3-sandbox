# Requirements

## Overview

Tighten two browser-facing security gaps in the tunnel/TTY surface:

- make WebSocket origin policy explicit in `internal/api/router.go`
- harden browser tunnel capability auth without introducing a full user-session system

Assumption: keep the existing signed URL + tunnel cookie model, but make its security boundaries clearer and narrower.

## Requirements

1. **Explicit WebSocket origin policy**
   - Acceptance criteria:
     - `internal/api` defines an explicit `CheckOrigin` policy instead of relying on Gorilla defaults.
     - the policy allows same-origin browser use for the configured operator host.
     - cross-origin WebSocket upgrades are rejected with regression coverage.

2. **Minimal browser tunnel hardening**
   - Acceptance criteria:
     - the plan keeps capability-style tunnel auth and does not introduce a general user session layer.
     - signed URL bootstrap and cookie reuse remain scoped to a specific tunnel path.
     - cookie settings are reviewed and tightened where low-risk improvements exist, such as `Secure`, `HttpOnly`, `SameSite`, and bounded lifetime behavior.

3. **Clear security model and limits**
   - Acceptance criteria:
     - docs or code comments clearly state that browser tunnel auth is capability-based, not a full session model.
     - revocation behavior remains tunnel-scoped and test-covered.
     - the plan avoids claims about device binding, CSRF-wide session controls, or global browser session management that the repo does not implement.

## Non-functional constraints

- Keep changes localized to existing `internal/api` code and tests.
- Do not add new persistence, DB schema, or auth subsystems.
- Preserve existing CLI and tunnel flows unless a change directly improves security with minimal compatibility risk.
