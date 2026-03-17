package model

import (
	"context"
	"io"
	"time"
)

// SandboxSpec is the fully resolved runtime creation request passed from the
// service layer to a backend implementation.
type SandboxSpec struct {
	SandboxID                string
	TenantID                 string
	RuntimeSelection         RuntimeSelection
	RuntimeBackend           string
	RuntimeClass             RuntimeClass
	BaseImageRef             string
	Profile                  GuestProfile
	Features                 []string
	Capabilities             []string
	ControlMode              GuestControlMode
	ControlProtocolVersion   string
	WorkspaceContractVersion string
	ImageContractVersion     string
	CPULimit                 CPUQuantity
	MemoryLimitMB            int
	PIDsLimit                int
	DiskLimitMB              int
	NetworkMode              NetworkMode
	AllowTunnels             bool
	StorageRoot              string
	WorkspaceRoot            string
	CacheRoot                string
	ScratchRoot              string
	SecretsRoot              string
	NetworkPolicy            NetworkPolicy
}

// RuntimeState captures the backend-observed state of a sandbox runtime.
type RuntimeState struct {
	RuntimeID              string
	Status                 SandboxStatus
	Running                bool
	Pid                    int
	IPAddress              string
	ControlMode            GuestControlMode
	ControlProtocolVersion string
	StartedAt              *time.Time
	Error                  string
}

// ExecResult is the terminal result returned by an [ExecHandle].
type ExecResult struct {
	ExitCode        int
	Status          ExecutionStatus
	StartedAt       time.Time
	CompletedAt     time.Time
	Duration        time.Duration
	StdoutPreview   string
	StderrPreview   string
	StdoutTruncated bool
	StderrTruncated bool
}

// ExecStreams provides optional stdout and stderr sinks for runtime exec.
type ExecStreams struct {
	Stdout io.Writer
	Stderr io.Writer
}

// ExecHandle represents an in-flight or detached exec request.
//
// Wait blocks until the runtime reports a terminal result. Cancel requests
// termination of the underlying process when the backend supports it.
type ExecHandle interface {
	Wait() ExecResult
	Cancel() error
}

// ResizeRequest carries terminal dimensions for a TTY session.
type ResizeRequest struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// TTYHandle provides access to an attached interactive terminal session.
//
// The returned reader and writer stream the PTY byte stream. Resize updates the
// terminal geometry, and Close releases the underlying session.
type TTYHandle interface {
	Reader() io.Reader
	Writer() io.Writer
	Resize(ResizeRequest) error
	Close() error
}

// SnapshotInfo describes backend-produced snapshot artifacts.
type SnapshotInfo struct {
	ImageRef     string
	WorkspaceTar string
}

// StorageUsage summarizes sandbox storage consumption by storage class.
type StorageUsage struct {
	RootfsBytes      int64
	WorkspaceBytes   int64
	CacheBytes       int64
	SnapshotBytes    int64
	RootfsEntries    int64
	WorkspaceEntries int64
	CacheEntries     int64
	SnapshotEntries  int64
}

// StorageClass identifies a logical sandbox storage area.
type StorageClass string

const (
	// StorageClassWorkspace identifies workspace storage.
	StorageClassWorkspace StorageClass = "workspace"
	// StorageClassCache identifies reusable cache storage.
	StorageClassCache StorageClass = "cache"
	// StorageClassScratch identifies ephemeral scratch storage.
	StorageClassScratch StorageClass = "scratch"
	// StorageClassSecrets identifies secret material staged for the sandbox.
	StorageClassSecrets StorageClass = "secrets"
	// StorageClassSnapshot identifies snapshot artifact storage.
	StorageClassSnapshot StorageClass = "snapshot"
)

// NetworkPolicy is the normalized network posture enforced by a runtime.
type NetworkPolicy struct {
	Internet     bool
	LoopbackOnly bool
	AllowTunnels bool
}

// ResolveNetworkPolicy converts the API-level network settings into the
// runtime-level policy applied by backends.
func ResolveNetworkPolicy(mode NetworkMode, allowTunnels bool) NetworkPolicy {
	policy := NetworkPolicy{LoopbackOnly: true, AllowTunnels: allowTunnels}
	if mode == NetworkModeInternetEnabled {
		policy.Internet = true
	}
	return policy
}

// RuntimeManager defines the backend contract implemented by sandbox runtimes.
//
// Implementations are expected to honor [context.Context] cancellation for
// blocking operations when practical and to return a [RuntimeState] that is
// safe for persistence by the service layer.
type RuntimeManager interface {
	Create(ctx context.Context, spec SandboxSpec) (RuntimeState, error)
	Start(ctx context.Context, sandbox Sandbox) (RuntimeState, error)
	Stop(ctx context.Context, sandbox Sandbox, force bool) (RuntimeState, error)
	Suspend(ctx context.Context, sandbox Sandbox) (RuntimeState, error)
	Resume(ctx context.Context, sandbox Sandbox) (RuntimeState, error)
	Destroy(ctx context.Context, sandbox Sandbox) error
	Inspect(ctx context.Context, sandbox Sandbox) (RuntimeState, error)
	Exec(ctx context.Context, sandbox Sandbox, req ExecRequest, streams ExecStreams) (ExecHandle, error)
	AttachTTY(ctx context.Context, sandbox Sandbox, req TTYRequest) (TTYHandle, error)
	CreateSnapshot(ctx context.Context, sandbox Sandbox, snapshotID string) (SnapshotInfo, error)
	RestoreSnapshot(ctx context.Context, sandbox Sandbox, snapshot Snapshot) (RuntimeState, error)
}

// WorkspaceArchiveExporter is implemented by runtimes that can stream or stage
// workspace exports without going through the generic service-layer fallback.
type WorkspaceArchiveExporter interface {
	ExportWorkspaceArchive(ctx context.Context, sandbox Sandbox, paths []string, maxBytes int64) (string, error)
}

// UnsupportedRuntimeOperationError reports that an optional runtime operation
// is unavailable for the selected backend.
type UnsupportedRuntimeOperationError struct {
	Selection RuntimeSelection
	Operation string
}

// Error returns the formatted unsupported-operation message.
func (e UnsupportedRuntimeOperationError) Error() string {
	if e.Selection != "" {
		return "runtime " + `"` + string(e.Selection) + `"` + " does not support " + e.Operation
	}
	return "runtime does not support " + e.Operation
}
