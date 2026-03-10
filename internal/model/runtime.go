package model

import (
	"context"
	"io"
	"time"
)

type SandboxSpec struct {
	SandboxID                string
	TenantID                 string
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

type ExecStreams struct {
	Stdout io.Writer
	Stderr io.Writer
}

type ExecHandle interface {
	Wait() ExecResult
	Cancel() error
}

type ResizeRequest struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

type TTYHandle interface {
	Reader() io.Reader
	Writer() io.Writer
	Resize(ResizeRequest) error
	Close() error
}

type SnapshotInfo struct {
	ImageRef     string
	WorkspaceTar string
}

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

type StorageClass string

const (
	StorageClassWorkspace StorageClass = "workspace"
	StorageClassCache     StorageClass = "cache"
	StorageClassScratch   StorageClass = "scratch"
	StorageClassSecrets   StorageClass = "secrets"
	StorageClassSnapshot  StorageClass = "snapshot"
)

type NetworkPolicy struct {
	Internet     bool
	LoopbackOnly bool
	AllowTunnels bool
}

func ResolveNetworkPolicy(mode NetworkMode, allowTunnels bool) NetworkPolicy {
	policy := NetworkPolicy{LoopbackOnly: true, AllowTunnels: allowTunnels}
	if mode == NetworkModeInternetEnabled {
		policy.Internet = true
	}
	return policy
}

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
