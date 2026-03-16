# Design

## Overview

The current QEMU implementation already has most of the right pieces, but they are connected in a way that keeps the main path larger than it needs to be:

- production-capable images are already agent-based, but the runtime still keeps SSH and agent as equal control branches
- the guest agent already has a structured protocol, but the host treats it largely as a per-call socket instead of a long-lived session
- file transfer still uses repeated chunk RPCs
- workspace export still shells into the guest to build a tarball and copy it back
- guest bootstrap still owns one-time workspace formatting work that should happen at sandbox creation time

This design keeps the repo architecture intact and simplifies the QEMU backend in place:

1. make the image contract authoritative for control mode and narrow SSH to `debug`
2. add a persistent per-VM agent session manager in the host runtime
3. move to a streaming agent protocol for files, exec, and archive export
4. use that session as the primary readiness and health signal
5. simplify boot by formatting the workspace once on the host and leaving only mount/ready work in the guest
6. keep snapshots workspace-first and host-built without changing the existing API or SQLite schema

The result stays aligned with the current Go/SQLite/CLI architecture and avoids inventing any new service layer or persistence model.

## Affected areas

- `internal/runtime/qemu/agentproto/protocol.go`
  - define protocol version `3`, new streaming operations, and updated request/result payloads
- `cmd/or3-guest-agent/main.go`
  - keep the guest port open, multiplex requests and streams on one connection, and implement file/exec/archive streaming
- `internal/runtime/qemu/agent_client.go`
  - add the host-side session registry, handshake cache, ping/reconnect logic, and stream dispatch
- `internal/runtime/qemu/runtime.go`
  - make image-contract control mode authoritative, unify readiness/lifecycle state derivation, simplify stop/delete, and move workspace provisioning host-side
- `internal/runtime/qemu/exec.go`
  - switch agent-mode exec to true streaming and remove SSH as a production branch
- `internal/runtime/qemu/workspace.go`
  - replace request-response chunk loops with streaming open/data/close helpers
- `internal/runtime/qemu/archive.go`
  - replace guest temp-archive export with host-built tar.gz from streamed archive entries
- `internal/config/config.go`
  - reject unsupported production control-mode/profile combinations early while preserving backward-compatible config parsing where practical
- `internal/guestimage/contract.go`, `internal/runtime/qemu/doc.go`, `images/guest/profiles/*.json`
  - tighten contract language and profile/control-mode validation
- `images/guest/systemd/or3-bootstrap.sh` and related guest assets
  - reduce bootstrap to deterministic mount verification, ownership fixup, and ready marker creation
- `internal/service/service.go`
  - keep lifecycle and snapshot API shapes stable while relying on the simplified runtime state and snapshot semantics
- `cmd/sandboxctl/main.go`, `cmd/sandboxctl/doctor.go`, `cmd/sandboxctl/hardening.go`
  - add `qemu init`/`qemu smoke`, keep doctor as the operator gate, and update recommended image/setup guidance
- `scripts/qemu-host-verification.sh`, `scripts/install-qemu-runtime.sh`, `scripts/qemu-production-smoke.sh`
  - align verification and onboarding with the agent-default path

## Control flow / architecture

### 1. Control mode and image contract

The image contract becomes the source of truth for how a QEMU guest is controlled.

- `core`, `runtime`, `browser`, and `container` must declare `control.mode=agent`.
- `debug` may declare `control.mode=ssh-compat` and may continue shipping SSH-related tooling.
- the runtime still accepts older config fields for backward-compatible loading, but normal production flow resolves control mode from the selected image contract rather than a global runtime toggle
- production validation fails before boot when a non-debug image advertises SSH or when a production deployment explicitly requests `ssh-compat`

This keeps control-mode truth close to the image actually being booted and removes silent host-side fallback.

### 2. Persistent per-VM agent sessions

`Runtime` gains an in-memory session registry keyed by sandbox ID/runtime ID.

Suggested host-side shape:

```go
type agentSessionManager struct {
    mu       sync.Mutex
    sessions map[string]*agentSession
}

type agentSession struct {
    sandboxID  string
    runtimeID  string
    pid        int
    socketPath string

    conn      net.Conn
    handshake guestHandshake

    writeMu  sync.Mutex
    pending  map[string]chan agentproto.Message
    streams  map[string]agentStreamHandler
    closedCh chan struct{}
}
```

Behavior:

- `ensureSession(ctx, sandbox, layout)` dials the socket, performs `hello`, caches the handshake, and starts a read loop once
- regular RPC-style calls reuse the session and correlate replies by message ID
- long-running operations register stream handlers keyed by stream or exec session IDs
- `ping` is used for cheap health checks
- EOF, timeout, runtime ID change, or PID change invalidate the session and trigger reconnect on the next operation
- after daemon restart the registry is empty; the first post-restart QEMU operation simply recreates the session

### 3. Agent protocol version 3

The existing length-prefixed JSON protocol remains, but the normal data plane moves from chunk-by-chunk request-response to explicit streaming sessions.

New operations:

- `ping`
  - lightweight health/readiness probe on an existing session
- `file_open`
  - open a workspace file stream for read or write
- `file_data`
  - carry file bytes and EOF/error state
- `file_close`
  - finish a file transfer cleanly
- `exec_start`
  - start a command and return an exec session ID
- `exec_event`
  - carry stdout, stderr, and final result events
- `exec_cancel`
  - request cancellation for an active exec
- `archive_stream`
  - stream normalized workspace archive entries and file bytes for host-built export

Existing PTY and TCP bridge message patterns remain, but they now live under the same host-side session manager instead of being treated as separate one-off control connections.

Representative payloads:

```go
type PingResult struct {
    Ready  bool   `json:"ready"`
    Reason string `json:"reason,omitempty"`
}

type FileOpenRequest struct {
    Path     string `json:"path"`
    Mode     string `json:"mode"` // "read" or "write"
    Truncate bool   `json:"truncate,omitempty"`
}

type FileData struct {
    SessionID string `json:"session_id"`
    Data      string `json:"data,omitempty"`
    EOF       bool   `json:"eof,omitempty"`
    Error     string `json:"error,omitempty"`
}

type ExecStartRequest struct {
    Command []string          `json:"command"`
    Cwd     string            `json:"cwd,omitempty"`
    Env     map[string]string `json:"env,omitempty"`
    Timeout time.Duration     `json:"timeout,omitempty"`
    Detached bool             `json:"detached,omitempty"`
}

type ExecEvent struct {
    ExecID  string                 `json:"exec_id"`
    Stream  string                 `json:"stream,omitempty"` // stdout, stderr, result
    Data    string                 `json:"data,omitempty"`
    Result  *agentproto.ExecResult `json:"result,omitempty"`
}
```

### 4. Workspace files and archive export

`workspace.go` keeps the current runtime-facing methods:

- `ReadWorkspaceFile`
- `ReadWorkspaceFileBytes`
- `WriteWorkspaceFile`
- `WriteWorkspaceFileBytes`
- `DeleteWorkspacePath`
- `MkdirWorkspace`

Internally, agent-mode reads and writes are reimplemented as:

1. validate and normalize the workspace path on the host
2. open a file stream over the live session
3. move bytes incrementally using `file_data`
4. close the stream and surface any terminal error

This preserves current service/API behavior while making transfer behavior smoother and simpler to debug.

For exports, `archive.go` stops running `tar` in the guest. Instead:

1. host requests `archive_stream` for selected workspace paths
2. guest walks the requested tree, emitting normalized entry metadata and file data
3. host writes the final tar.gz using the existing safe archive path handling
4. host enforces total size bounds during write

This removes temp files in `/workspace`, guest-side tar dependencies, and the extra copy-back phase.

### 5. Readiness and lifecycle state

`runtime.go` gets one shared state-derivation helper used by `Start`, `Resume`, `Inspect`, and service reconciliation.

Suggested logic:

1. if the QEMU PID is missing, return `stopped`
2. if the suspended marker is present, return `suspended`
3. if still inside the boot window:
   - session unavailable or `ping` says not ready -> `booting`
4. if outside the boot window:
   - session healthy and ready -> `running`
   - session reachable but not ready -> `degraded`
   - boot failure markers in the serial log -> terminal `error`
   - process alive but session unavailable -> `degraded`

Serial logs remain important, but only for:

- early boot failure context
- degraded/error messaging
- operator troubleshooting

They stop being the normal source of truth for “ready.”

### 6. Stop/delete idempotence

Stop and delete are simplified to one conservative sequence:

1. resolve current state via the shared state helper
2. if already stopped, return success
3. if session exists, attempt guest shutdown
4. wait a bounded grace period for PID exit
5. if still alive, fall back to monitor or signal-based termination
6. remove stale runtime markers and tolerate missing files/sockets

`Destroy` continues to call `Stop(..., true)` and then remove the sandbox runtime directory. Missing monitor sockets, stale PID files, broken sessions, and already-exited VMs are all treated as recoverable cleanup states.

### 7. Workspace creation and guest bootstrap

The workspace block image stays in place, but one-time initialization moves to sandbox creation.

Host-side creation flow:

1. create the raw workspace image file
2. format it once with ext4 on the host
3. assign a deterministic label derived from the sandbox ID

Guest bootstrap responsibilities become:

- wait for the workspace block device
- mount it if not already mounted
- fix ownership/permissions if needed
- write the ready marker

Bootstrap no longer:

- decides whether to run `mkfs.ext4`
- appends entries to `/etc/fstab`
- owns any one-time storage-formatting behavior

This shortens boot and makes mount behavior easier to reason about.

### 8. Workspace-first snapshot model

The QEMU snapshot API remains the same, but the default artifact model changes.

Default snapshot create:

- require the sandbox to be stopped
- persist the base image reference already associated with the sandbox
- export workspace contents as a host-built tar.gz

Default snapshot restore:

- verify runtime selection and relevant image metadata
- recreate the root overlay from the base image
- restore the workspace archive into the workspace image or mounted workspace path

This keeps snapshot behavior aligned with what the smoke flows already care about: workspace state and a recreated root overlay, not long-lived copied overlay disks.

## Data and persistence

SQLite changes:

- none planned
- existing sandbox and snapshot tables remain the source of truth
- snapshot metadata reuses current fields for base image reference and workspace archive location

Config and env changes:

- `SANDBOX_QEMU_CONTROL_MODE` remains loadable for backward-compatible parsing but becomes effectively deprecated for production selection
- `SANDBOX_QEMU_ALLOW_SSH_COMPAT` gates debug/rescue validation only, not the normal production path
- doctor and install tooling add or tighten checks around `mkfs.ext4`/`e2fsprogs` because workspace formatting moves host-side

Session persistence:

- agent sessions are in-memory only and intentionally reconstructed after daemon restart
- no persistent session metadata is added to SQLite

Rollout/migration:

- protocol version `3` is a coordinated daemon + guest-image rollout boundary
- agent-capable images must be rebuilt/promoted with the new guest agent before production rollout
- debug SSH workflows can remain as a temporary operational escape hatch while the agent rollout completes

## Interfaces and types

Public API surface:

- no new HTTP endpoints for lifecycle, exec, files, or snapshots
- existing `sandboxctl` verbs remain
- new CLI entrypoints: `sandboxctl qemu init` and `sandboxctl qemu smoke`

Internal Go interfaces:

- `model.RuntimeManager` remains unchanged
- `model.WorkspaceArchiveExporter` remains unchanged
- most change is isolated to the QEMU runtime implementation and guest agent protocol

Key internal additions:

- session-manager types in `internal/runtime/qemu/agent_client.go`
- protocol v3 request/result types in `internal/runtime/qemu/agentproto/protocol.go`
- helper methods such as:

```go
func (r *Runtime) ensureAgentSession(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout) (*agentSession, error)
func (s *agentSession) Ping(ctx context.Context) (agentproto.PingResult, error)
func (s *agentSession) OpenFile(ctx context.Context, req agentproto.FileOpenRequest) (string, error)
func (s *agentSession) StartExec(ctx context.Context, req agentproto.ExecStartRequest) (string, error)
func (s *agentSession) StreamArchive(ctx context.Context, req agentproto.ArchiveStreamRequest, handler archiveStreamHandler) error
```

These are implementation details, not public contracts.

## Failure modes and safeguards

- **Invalid control-mode/profile combinations**
  - reject at config validation, doctor, or guest-image validation before boot
- **Protocol mismatch**
  - fail handshake immediately with a clear error including host and guest protocol versions
- **Broken or stale sessions**
  - invalidate the in-memory session and reconnect only when the runtime ID/PID still indicates the same VM
- **Workspace path escape**
  - reject on the host before any guest operation
- **Oversized file or archive transfers**
  - enforce configured and negotiated byte limits during streaming
- **Archive traversal or unsafe entry types**
  - normalize entry names on the guest and revalidate on the host before writing tar output
- **Boot never becomes ready**
  - return `booting` inside the timeout window and `degraded` or `error` afterward with serial failure context
- **Partial stop/delete cleanup**
  - treat missing monitor sockets, stale PID files, and already-gone sessions as recoverable cleanup cases
- **Missing host formatting tools**
  - doctor and install flows must surface `mkfs.ext4`/`e2fsprogs` absence clearly before operators attempt create/start

## Testing strategy

- Unit tests in `internal/config/config_test.go` and `cmd/sandboxctl/main_test.go`
  - invalid production SSH combinations
  - doctor pass/fail behavior
  - new `qemu init`/`qemu smoke` CLI wiring
- Unit tests in `internal/runtime/qemu/runtime_test.go`
  - shared state derivation
  - idempotent stop/delete cleanup
  - image contract validation and control-mode restrictions
- Unit tests in `internal/runtime/qemu/agent_client.go`-adjacent tests
  - handshake caching
  - request multiplexing
  - reconnect after EOF/timeout
  - session invalidation on runtime ID/PID change
- Unit tests in `cmd/or3-guest-agent/main_test.go`
  - `ping`
  - file streaming EOF/error handling
  - exec event ordering
  - archive stream framing
- Service and archive tests
  - snapshot create/restore semantics stay API-compatible
  - workspace export remains safe and bounded
- Host-gated QEMU integration coverage
  - `create --start`
  - upload -> exec -> download
  - live exec output
  - suspend/resume
  - restart reconciliation
  - workspace-first snapshot restore
  - explicit debug-profile SSH verification
