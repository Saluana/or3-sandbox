# Architecture

This page explains how the project is put together.

If the codebase feels big, do not worry. The main idea is actually simple:

1. a user sends a request
2. the server checks who they are
3. the service layer decides what should happen
4. the runtime does the real machine or container work
5. the database remembers what happened

## High-level picture

```text
sandboxctl / curl
        |
        v
     sandboxd
        |
        +--> auth middleware
        |
        +--> API router
        |
        +--> service layer
        |        |
        |        +--> repository/store (SQLite)
        |        |
        |        +--> runtime manager
        |                  |
        |                  +--> docker runtime
        |                  +--> qemu runtime
        |
        +--> JSON / SSE / WebSocket responses
```

## Main layers

## 1. Entry points

The project has two main entry points:

- `cmd/sandboxd/main.go`
- `cmd/sandboxctl/main.go`

`sandboxd`:

- loads config
- opens SQLite
- seeds tenant records from tokens
- builds the selected runtime
- starts the HTTP server
- runs reconciliation in the background

`sandboxctl`:

- reads the API URL and token from environment variables
- parses commands
- sends requests to the daemon
- prints JSON results

## 2. Auth middleware

Auth is handled before most routes reach the service layer.

The middleware:

- reads the `Authorization: Bearer ...` header
- hashes the token
- looks up the tenant in SQLite
- loads that tenant's quota
- applies rate limiting
- puts tenant info in the request context

This means the rest of the app can trust that a request already belongs to a known tenant.

## 3. API router

The router lives in `internal/api/router.go`.

It maps URLs to actions such as:

- `GET /healthz`
- `GET /v1/runtime/health`
- `GET /v1/quotas/me`
- `POST /v1/sandboxes`
- `GET /v1/sandboxes`
- `POST /v1/sandboxes/{id}/exec`
- `GET /v1/sandboxes/{id}/tty`
- file routes under `/v1/sandboxes/{id}/files/...`
- tunnel routes
- snapshot routes

The API mostly speaks JSON.

Two special cases are:

- **SSE** for streamed exec output
- **WebSocket** for interactive terminal sessions

## 4. Service layer

The service layer is the brain of the system.

It:

- validates requests
- applies defaults
- checks quotas
- updates sandbox state
- calls the runtime
- records storage and audit data

This is important because it keeps business rules in one place instead of spreading them across handlers or runtimes.

### Example: create sandbox flow

When a sandbox is created, the service layer does roughly this:

1. apply default values
2. validate the request
3. check tenant quota
4. create workspace directories
5. write the sandbox record to SQLite
6. call `runtime.Create(...)`
7. optionally call `runtime.Start(...)`
8. update state and storage usage

That gives the project one central place to decide what "create" really means.

## 5. Runtime abstraction

The project uses a small interface called `RuntimeManager`.

That interface lets the rest of the system say things like:

- create a sandbox
- start it
- stop it
- inspect it
- run a command
- attach a terminal
- create or restore a snapshot

Because of this interface, the control plane code does not need to know the low-level details of Docker or QEMU.

### Runtime classes and the adapter layer

Each backend maps to a **runtime class** that expresses its isolation posture:

- `docker` → `trusted-docker` (shared-kernel, development/trusted only)
- `qemu` → `vm` (VM-backed, the only production-eligible class)

The `internal/runtime/adapter` package provides lightweight request types
(`AdapterCreateRequest`, `SandboxAttachment`, `NetworkAttachment`) that describe
sandbox intent in terms of lifecycle, storage, and network — rather than in
Docker CLI terms. This boundary means:

- adding a future VM-backed adapter does not require threading Docker-specific
  assumptions through service or API code
- the adapter layer is intentionally small: no Kubernetes, no containerd, no
  scheduler; it lives inside the existing Go process

The production policy gate is backed by runtime class, not by ad hoc backend
name checks. `SANDBOX_MODE=production` fails closed to VM-backed classes only.

## 6. Docker runtime

The Docker runtime is the easiest current backend.

It:

- creates a durable container per sandbox
- mounts `/workspace`
- can also mount `/cache`
- creates a dedicated Docker network for `internet-enabled`
- uses `--network none` for `internet-disabled`
- runs commands with `docker exec`
- opens terminals with a PTY
- creates snapshots with `docker commit` plus a workspace archive

This is the most complete path today.

## 7. QEMU runtime

The QEMU runtime is the more production-oriented path.

It:

- creates a writable root disk
- creates a separate workspace disk
- boots a guest image
- waits for SSH and a readiness marker
- runs commands over SSH
- copies files over the guest boundary
- stores VM artifacts in a predictable layout on disk

Right now, this path is still a little more operationally rough than Docker, but it now supports the full main lifecycle, including suspend and resume.

## 8. Repository and database layer

The repository layer hides direct SQL details from the rest of the app.

It is responsible for reading and writing records such as:

- tenants
- quotas
- sandboxes
- runtime state
- snapshots
- tunnels
- executions
- audit events

This makes the service layer easier to read and test.

## 9. Reconciliation

Reconciliation is a background check that helps the daemon recover after restarts.

It does things like:

- load non-deleted sandboxes from SQLite
- inspect the runtime state for each one
- update saved status if needed
- keep the control plane and real runtime closer together

In plain language, reconciliation asks:

> "What did we think was happening, and what is actually happening now?"

## 10. Data flow for common actions

### Exec command

1. user runs `sandboxctl exec`
2. CLI sends a request to the API
3. auth middleware loads tenant and quota
4. service checks the sandbox and exec quota
5. runtime runs the command
6. output is streamed or stored as a preview
7. result is saved and returned

### Interactive terminal

1. user runs `sandboxctl tty`
2. CLI opens a WebSocket
3. server opens a runtime PTY
4. stdin, stdout, and resize events are bridged both ways

### File upload

1. CLI reads a local file
2. CLI sends the file content to the daemon
3. service writes into the sandbox workspace through the runtime boundary

## Why this design is useful

This design keeps the code organized:

- API code handles HTTP details
- service code handles rules and workflows
- repository code handles SQLite
- runtime code handles containers or VMs

That separation makes the project easier to grow without turning into one giant file.
