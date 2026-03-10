package model

import "time"

type SandboxStatus string

const (
	SandboxStatusCreating   SandboxStatus = "creating"
	SandboxStatusBooting    SandboxStatus = "booting"
	SandboxStatusDegraded   SandboxStatus = "degraded"
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
	ExecutionStatusDetached  ExecutionStatus = "detached"
	ExecutionStatusSucceeded ExecutionStatus = "succeeded"
	ExecutionStatusFailed    ExecutionStatus = "failed"
	ExecutionStatusTimedOut  ExecutionStatus = "timed_out"
	ExecutionStatusCanceled  ExecutionStatus = "canceled"
)

type GuestProfile string

const (
	GuestProfileCore      GuestProfile = "core"
	GuestProfileRuntime   GuestProfile = "runtime"
	GuestProfileBrowser   GuestProfile = "browser"
	GuestProfileContainer GuestProfile = "container"
	GuestProfileDebug     GuestProfile = "debug"
)

type GuestControlMode string

const (
	GuestControlModeAgent     GuestControlMode = "agent"
	GuestControlModeSSHCompat GuestControlMode = "ssh-compat"
)

func (p GuestProfile) IsValid() bool {
	switch p {
	case GuestProfileCore, GuestProfileRuntime, GuestProfileBrowser, GuestProfileContainer, GuestProfileDebug:
		return true
	default:
		return false
	}
}

func (m GuestControlMode) IsValid() bool {
	switch m {
	case GuestControlModeAgent, GuestControlModeSSHCompat:
		return true
	default:
		return false
	}
}

// Sandbox is the primary lifecycle resource returned by sandbox CRUD endpoints.
type Sandbox struct {
	ID                       string           `json:"id"`
	TenantID                 string           `json:"tenant_id"`
	Status                   SandboxStatus    `json:"status"`
	RuntimeSelection         RuntimeSelection `json:"runtime_selection,omitempty"`
	RuntimeBackend           string           `json:"runtime_backend"`
	RuntimeClass             RuntimeClass     `json:"runtime_class,omitempty"`
	BaseImageRef             string           `json:"base_image_ref"`
	Profile                  GuestProfile     `json:"profile,omitempty"`
	Features                 []string         `json:"features,omitempty"`
	Capabilities             []string         `json:"capabilities,omitempty"`
	ControlMode              GuestControlMode `json:"control_mode,omitempty"`
	ControlProtocolVersion   string           `json:"control_protocol_version,omitempty"`
	WorkspaceContractVersion string           `json:"workspace_contract_version,omitempty"`
	ImageContractVersion     string           `json:"image_contract_version,omitempty"`
	CPULimit                 CPUQuantity      `json:"cpu_limit"`
	MemoryLimitMB            int              `json:"memory_limit_mb"`
	PIDsLimit                int              `json:"pids_limit"`
	DiskLimitMB              int              `json:"disk_limit_mb"`
	NetworkMode              NetworkMode      `json:"network_mode"`
	AllowTunnels             bool             `json:"allow_tunnels"`
	StorageRoot              string           `json:"-"`
	WorkspaceRoot            string           `json:"-"`
	CacheRoot                string           `json:"-"`
	RuntimeID                string           `json:"runtime_id"`
	RuntimeStatus            string           `json:"runtime_status"`
	LastRuntimeError         string           `json:"last_runtime_error,omitempty"`
	CreatedAt                time.Time        `json:"created_at"`
	UpdatedAt                time.Time        `json:"updated_at"`
	LastActiveAt             time.Time        `json:"last_active_at"`
	DeletedAt                *time.Time       `json:"deleted_at,omitempty"`
}

// CreateSandboxRequest is the JSON payload accepted by POST /v1/sandboxes.
type CreateSandboxRequest struct {
	RuntimeSelection RuntimeSelection `json:"runtime_selection,omitempty"`
	BaseImageRef     string           `json:"base_image_ref"`
	Profile          GuestProfile     `json:"profile,omitempty"`
	Features         []string         `json:"features,omitempty"`
	Capabilities     []string         `json:"capabilities,omitempty"`
	CPULimit         CPUQuantity      `json:"cpu_limit"`
	MemoryLimitMB    int              `json:"memory_limit_mb"`
	PIDsLimit        int              `json:"pids_limit"`
	DiskLimitMB      int              `json:"disk_limit_mb"`
	NetworkMode      NetworkMode      `json:"network_mode"`
	AllowTunnels     *bool            `json:"allow_tunnels,omitempty"`
	Start            bool             `json:"start"`
}

// LifecycleRequest is the JSON payload used by lifecycle mutation endpoints.
type LifecycleRequest struct {
	Force bool `json:"force"`
}

// ErrorResponse is the normalized error envelope returned by API endpoints.
type ErrorResponse struct {
	Error  string `json:"error"`
	Code   string `json:"code"`
	Status int    `json:"status"`
}

// ExecRequest is the JSON payload accepted by POST /v1/sandboxes/{id}/exec.
type ExecRequest struct {
	Command  []string          `json:"command"`
	Env      map[string]string `json:"env"`
	Cwd      string            `json:"cwd"`
	Timeout  time.Duration     `json:"timeout"`
	Detached bool              `json:"detached"`
}

// Execution is the command result returned by sync exec and SSE terminal events.
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

// TTYRequest is the first WebSocket frame sent when opening a TTY session.
type TTYRequest struct {
	Command []string          `json:"command"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	Cols    int               `json:"cols"`
	Rows    int               `json:"rows"`
}

// TTYSession describes a persisted terminal session record.
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

// FileWriteRequest is the JSON payload for file writes.
type FileWriteRequest struct {
	Content       string `json:"content,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	Encoding      string `json:"encoding,omitempty"`
}

// FileReadResponse is the JSON payload returned by file reads.
type FileReadResponse struct {
	Path          string `json:"path"`
	Content       string `json:"content,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	Size          int64  `json:"size"`
	Encoding      string `json:"encoding"`
}

// MkdirRequest is the JSON payload for directory creation.
type MkdirRequest struct {
	Path string `json:"path"`
}

// CreateTunnelRequest is the JSON payload accepted by tunnel creation endpoints.
type CreateTunnelRequest struct {
	TargetPort int            `json:"target_port"`
	Protocol   TunnelProtocol `json:"protocol"`
	AuthMode   string         `json:"auth_mode"`
	Visibility string         `json:"visibility"`
}

// CreateTunnelSignedURLRequest is the JSON payload accepted by signed browser URL issuance.
type CreateTunnelSignedURLRequest struct {
	Path       string `json:"path,omitempty"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

// Tunnel is the HTTP tunnel resource returned by tunnel endpoints.
type Tunnel struct {
	ID             string         `json:"id"`
	SandboxID      string         `json:"sandbox_id"`
	TenantID       string         `json:"tenant_id"`
	TargetPort     int            `json:"target_port"`
	Protocol       TunnelProtocol `json:"protocol"`
	AuthMode       string         `json:"auth_mode"`
	Visibility     string         `json:"visibility"`
	Endpoint       string         `json:"endpoint"`
	AccessToken    string         `json:"access_token,omitempty"`
	AuthSecretHash string         `json:"-"`
	CreatedAt      time.Time      `json:"created_at"`
	RevokedAt      *time.Time     `json:"revoked_at,omitempty"`
}

// TunnelSignedURL is the browser-launch capability returned by POST /v1/tunnels/{id}/signed-url.
type TunnelSignedURL struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateSnapshotRequest is the JSON payload accepted by snapshot creation.
type CreateSnapshotRequest struct {
	Name string `json:"name"`
}

// Snapshot is the snapshot resource returned by snapshot endpoints.
type Snapshot struct {
	ID                       string           `json:"id"`
	SandboxID                string           `json:"sandbox_id"`
	TenantID                 string           `json:"tenant_id"`
	Name                     string           `json:"name"`
	Status                   SnapshotStatus   `json:"status"`
	ImageRef                 string           `json:"image_ref"`
	RuntimeSelection         RuntimeSelection `json:"runtime_selection,omitempty"`
	RuntimeBackend           string           `json:"runtime_backend,omitempty"`
	Profile                  GuestProfile     `json:"profile,omitempty"`
	ImageContractVersion     string           `json:"image_contract_version,omitempty"`
	ControlProtocolVersion   string           `json:"control_protocol_version,omitempty"`
	WorkspaceContractVersion string           `json:"workspace_contract_version,omitempty"`
	WorkspaceTar             string           `json:"-"`
	ExportLocation           string           `json:"export_location,omitempty"`
	CreatedAt                time.Time        `json:"created_at"`
	CompletedAt              *time.Time       `json:"completed_at,omitempty"`
}

// RestoreSnapshotRequest is the JSON payload accepted by snapshot restore.
type RestoreSnapshotRequest struct {
	TargetSandboxID string `json:"target_sandbox_id"`
}

// RuntimeHealth is the runtime health report returned by GET /v1/runtime/health.
type RuntimeHealth struct {
	DefaultRuntimeSelection  RuntimeSelection       `json:"default_runtime_selection,omitempty"`
	EnabledRuntimeSelections []RuntimeSelection     `json:"enabled_runtime_selections,omitempty"`
	Backend                  string                 `json:"backend"`
	Healthy                  bool                   `json:"healthy"`
	CheckedAt                time.Time              `json:"checked_at"`
	RuntimeSelectionCounts   map[string]int         `json:"runtime_selection_counts,omitempty"`
	StatusCounts             map[string]int         `json:"status_counts,omitempty"`
	Sandboxes                []RuntimeSandboxHealth `json:"sandboxes"`
}

// RuntimeInfo is the runtime summary returned by GET /v1/runtime/info.
type RuntimeInfo struct {
	Backend                  string             `json:"backend,omitempty"`
	Class                    string             `json:"class,omitempty"`
	DefaultRuntimeSelection  RuntimeSelection   `json:"default_runtime_selection,omitempty"`
	EnabledRuntimeSelections []RuntimeSelection `json:"enabled_runtime_selections,omitempty"`
}

// RuntimeSandboxHealth is one sandbox entry inside RuntimeHealth.
type RuntimeSandboxHealth struct {
	SandboxID        string           `json:"sandbox_id"`
	TenantID         string           `json:"tenant_id"`
	RuntimeSelection RuntimeSelection `json:"runtime_selection,omitempty"`
	PersistedStatus  SandboxStatus    `json:"persisted_status"`
	ObservedStatus   SandboxStatus    `json:"observed_status"`
	RuntimeID        string           `json:"runtime_id"`
	RuntimeStatus    string           `json:"runtime_status"`
	Pid              int              `json:"pid"`
	IPAddress        string           `json:"ip_address,omitempty"`
	Error            string           `json:"error,omitempty"`
}

type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// TenantQuota is the tenant quota configuration exposed through quota and capacity responses.
type TenantQuota struct {
	TenantID                string      `json:"tenant_id"`
	MaxSandboxes            int         `json:"max_sandboxes"`
	MaxRunningSandboxes     int         `json:"max_running_sandboxes"`
	MaxConcurrentExecs      int         `json:"max_concurrent_execs"`
	MaxTunnels              int         `json:"max_tunnels"`
	MaxCPUCores             CPUQuantity `json:"max_cpu_cores"`
	MaxMemoryMB             int         `json:"max_memory_mb"`
	MaxStorageMB            int         `json:"max_storage_mb"`
	AllowTunnels            bool        `json:"allow_tunnels"`
	DefaultTunnelAuthMode   string      `json:"default_tunnel_auth_mode"`
	DefaultTunnelVisibility string      `json:"default_tunnel_visibility"`
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
