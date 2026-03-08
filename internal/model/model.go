package model

import "time"

type SandboxStatus string

const (
	SandboxStatusCreating   SandboxStatus = "creating"
	SandboxStatusBooting    SandboxStatus = "booting"
	SandboxStatusStopped    SandboxStatus = "stopped"
	SandboxStatusStarting   SandboxStatus = "starting"
	SandboxStatusRunning    SandboxStatus = "running"
	SandboxStatusSuspending SandboxStatus = "suspending"
	SandboxStatusSuspended  SandboxStatus = "suspended"
	SandboxStatusStopping   SandboxStatus = "stopping"
	SandboxStatusDeleting   SandboxStatus = "deleting"
	SandboxStatusDeleted    SandboxStatus = "deleted"
	SandboxStatusError      SandboxStatus = "error"
)

type NetworkMode string

const (
	NetworkModeInternetEnabled  NetworkMode = "internet-enabled"
	NetworkModeInternetDisabled NetworkMode = "internet-disabled"
)

type TunnelProtocol string

const (
	TunnelProtocolHTTP TunnelProtocol = "http"
	TunnelProtocolTCP  TunnelProtocol = "tcp"
)

type SnapshotStatus string

const (
	SnapshotStatusCreating SnapshotStatus = "creating"
	SnapshotStatusReady    SnapshotStatus = "ready"
	SnapshotStatusError    SnapshotStatus = "error"
)

type ExecutionStatus string

const (
	ExecutionStatusRunning   ExecutionStatus = "running"
	ExecutionStatusSucceeded ExecutionStatus = "succeeded"
	ExecutionStatusFailed    ExecutionStatus = "failed"
	ExecutionStatusTimedOut  ExecutionStatus = "timed_out"
	ExecutionStatusCanceled  ExecutionStatus = "canceled"
)

type Sandbox struct {
	ID               string        `json:"id"`
	TenantID         string        `json:"tenant_id"`
	Status           SandboxStatus `json:"status"`
	RuntimeBackend   string        `json:"runtime_backend"`
	BaseImageRef     string        `json:"base_image_ref"`
	CPULimit         CPUQuantity   `json:"cpu_limit"`
	MemoryLimitMB    int           `json:"memory_limit_mb"`
	PIDsLimit        int           `json:"pids_limit"`
	DiskLimitMB      int           `json:"disk_limit_mb"`
	NetworkMode      NetworkMode   `json:"network_mode"`
	AllowTunnels     bool          `json:"allow_tunnels"`
	StorageRoot      string        `json:"-"`
	WorkspaceRoot    string        `json:"-"`
	CacheRoot        string        `json:"-"`
	RuntimeID        string        `json:"runtime_id"`
	RuntimeStatus    string        `json:"runtime_status"`
	LastRuntimeError string        `json:"last_runtime_error,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
	LastActiveAt     time.Time     `json:"last_active_at"`
	DeletedAt        *time.Time    `json:"deleted_at,omitempty"`
}

type CreateSandboxRequest struct {
	BaseImageRef  string      `json:"base_image_ref"`
	CPULimit      CPUQuantity `json:"cpu_limit"`
	MemoryLimitMB int         `json:"memory_limit_mb"`
	PIDsLimit     int         `json:"pids_limit"`
	DiskLimitMB   int         `json:"disk_limit_mb"`
	NetworkMode   NetworkMode `json:"network_mode"`
	AllowTunnels  *bool       `json:"allow_tunnels,omitempty"`
	Start         bool        `json:"start"`
}

type LifecycleRequest struct {
	Force bool `json:"force"`
}

type ExecRequest struct {
	Command  []string          `json:"command"`
	Env      map[string]string `json:"env"`
	Cwd      string            `json:"cwd"`
	Timeout  time.Duration     `json:"timeout"`
	Detached bool              `json:"detached"`
}

type Execution struct {
	ID              string          `json:"id"`
	SandboxID       string          `json:"sandbox_id"`
	TenantID        string          `json:"tenant_id"`
	Command         string          `json:"command"`
	Cwd             string          `json:"cwd"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
	Status          ExecutionStatus `json:"status"`
	ExitCode        *int            `json:"exit_code,omitempty"`
	StdoutPreview   string          `json:"stdout_preview,omitempty"`
	StderrPreview   string          `json:"stderr_preview,omitempty"`
	StdoutTruncated bool            `json:"stdout_truncated"`
	StderrTruncated bool            `json:"stderr_truncated"`
	StartedAt       time.Time       `json:"started_at"`
	CompletedAt     *time.Time      `json:"completed_at,omitempty"`
	DurationMS      *int64          `json:"duration_ms,omitempty"`
}

type TTYRequest struct {
	Command []string          `json:"command"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	Cols    int               `json:"cols"`
	Rows    int               `json:"rows"`
}

type TTYSession struct {
	ID         string     `json:"id"`
	SandboxID  string     `json:"sandbox_id"`
	TenantID   string     `json:"tenant_id"`
	Command    string     `json:"command"`
	Connected  bool       `json:"connected"`
	CreatedAt  time.Time  `json:"created_at"`
	ClosedAt   *time.Time `json:"closed_at,omitempty"`
	LastResize string     `json:"last_resize,omitempty"`
}

type FileWriteRequest struct {
	Content string `json:"content"`
}

type FileReadResponse struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Size     int64  `json:"size"`
	Encoding string `json:"encoding"`
}

type MkdirRequest struct {
	Path string `json:"path"`
}

type CreateTunnelRequest struct {
	TargetPort int            `json:"target_port"`
	Protocol   TunnelProtocol `json:"protocol"`
	AuthMode   string         `json:"auth_mode"`
	Visibility string         `json:"visibility"`
}

type Tunnel struct {
	ID         string         `json:"id"`
	SandboxID  string         `json:"sandbox_id"`
	TenantID   string         `json:"tenant_id"`
	TargetPort int            `json:"target_port"`
	Protocol   TunnelProtocol `json:"protocol"`
	AuthMode   string         `json:"auth_mode"`
	Visibility string         `json:"visibility"`
	Endpoint   string         `json:"endpoint"`
	AccessToken string        `json:"access_token,omitempty"`
	AuthSecretHash string     `json:"-"`
	CreatedAt  time.Time      `json:"created_at"`
	RevokedAt  *time.Time     `json:"revoked_at,omitempty"`
}

type CreateSnapshotRequest struct {
	Name string `json:"name"`
}

type Snapshot struct {
	ID             string         `json:"id"`
	SandboxID      string         `json:"sandbox_id"`
	TenantID       string         `json:"tenant_id"`
	Name           string         `json:"name"`
	Status         SnapshotStatus `json:"status"`
	ImageRef       string         `json:"image_ref"`
	WorkspaceTar   string         `json:"-"`
	ExportLocation string         `json:"export_location,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	CompletedAt    *time.Time     `json:"completed_at,omitempty"`
}

type RestoreSnapshotRequest struct {
	TargetSandboxID string `json:"target_sandbox_id"`
}

type RuntimeHealth struct {
	Backend   string                 `json:"backend"`
	Healthy   bool                   `json:"healthy"`
	CheckedAt time.Time              `json:"checked_at"`
	Sandboxes []RuntimeSandboxHealth `json:"sandboxes"`
}

type RuntimeSandboxHealth struct {
	SandboxID       string        `json:"sandbox_id"`
	TenantID        string        `json:"tenant_id"`
	PersistedStatus SandboxStatus `json:"persisted_status"`
	ObservedStatus  SandboxStatus `json:"observed_status"`
	RuntimeID       string        `json:"runtime_id"`
	RuntimeStatus   string        `json:"runtime_status"`
	Pid             int           `json:"pid"`
	IPAddress       string        `json:"ip_address,omitempty"`
	Error           string        `json:"error,omitempty"`
}

type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

type TenantQuota struct {
	TenantID               string `json:"tenant_id"`
	MaxSandboxes           int    `json:"max_sandboxes"`
	MaxRunningSandboxes    int    `json:"max_running_sandboxes"`
	MaxConcurrentExecs     int    `json:"max_concurrent_execs"`
	MaxTunnels             int    `json:"max_tunnels"`
	MaxCPUCores            CPUQuantity `json:"max_cpu_cores"`
	MaxMemoryMB            int    `json:"max_memory_mb"`
	MaxStorageMB           int    `json:"max_storage_mb"`
	AllowTunnels           bool   `json:"allow_tunnels"`
	DefaultTunnelAuthMode  string `json:"default_tunnel_auth_mode"`
	DefaultTunnelVisibility string `json:"default_tunnel_visibility"`
}

type AuditEvent struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	SandboxID  string    `json:"sandbox_id,omitempty"`
	Action     string    `json:"action"`
	ResourceID string    `json:"resource_id,omitempty"`
	Outcome    string    `json:"outcome"`
	Message    string    `json:"message"`
	CreatedAt  time.Time `json:"created_at"`
}
