package model

import (
	"errors"
	"fmt"
	"time"
)

const (
	// DefaultWorkspaceFileTransferMaxBytes is the default per-request limit for
	// workspace file read and write APIs.
	DefaultWorkspaceFileTransferMaxBytes = 64 * 1024 * 1024
	MaxWorkspaceFileTransferBytes        = DefaultWorkspaceFileTransferMaxBytes
	MaxWorkspaceFileTransferCeilingBytes = 1024 * 1024 * 1024
)

// ErrFileTransferTooLarge reports that a workspace file operation exceeded the
// configured transfer limit.
var ErrFileTransferTooLarge = errors.New("workspace file transfer too large")

// FileTransferTooLargeError wraps [ErrFileTransferTooLarge] with the effective
// byte limit for the rejected request.
func FileTransferTooLargeError(limit int64) error {
	return fmt.Errorf("%w: workspace file exceeds maximum transfer size of %d bytes", ErrFileTransferTooLarge, limit)
}

// SandboxStatus describes the lifecycle state of a sandbox.
type SandboxStatus string

const (
	// SandboxStatusCreating reports that sandbox provisioning has started.
	SandboxStatusCreating SandboxStatus = "creating"
	// SandboxStatusBooting reports that the guest is booting.
	SandboxStatusBooting SandboxStatus = "booting"
	// SandboxStatusDegraded reports that the sandbox exists but runtime health is degraded.
	SandboxStatusDegraded SandboxStatus = "degraded"
	// SandboxStatusStopped reports that the sandbox exists but is not running.
	SandboxStatusStopped SandboxStatus = "stopped"
	// SandboxStatusStarting reports that runtime start has been requested.
	SandboxStatusStarting SandboxStatus = "starting"
	// SandboxStatusRunning reports that the sandbox is actively running.
	SandboxStatusRunning SandboxStatus = "running"
	// SandboxStatusSuspending reports that runtime suspend is in progress.
	SandboxStatusSuspending SandboxStatus = "suspending"
	// SandboxStatusSuspended reports that the sandbox is paused in a suspended state.
	SandboxStatusSuspended SandboxStatus = "suspended"
	// SandboxStatusStopping reports that runtime stop is in progress.
	SandboxStatusStopping SandboxStatus = "stopping"
	// SandboxStatusDeleting reports that sandbox teardown is in progress.
	SandboxStatusDeleting SandboxStatus = "deleting"
	// SandboxStatusDeleted reports that the sandbox has been deleted.
	SandboxStatusDeleted SandboxStatus = "deleted"
	// SandboxStatusError reports that the sandbox entered a terminal error state.
	SandboxStatusError SandboxStatus = "error"
)

// NetworkMode describes the network posture requested for a sandbox.
type NetworkMode string

const (
	// NetworkModeInternetEnabled allows the sandbox to reach external networks.
	NetworkModeInternetEnabled NetworkMode = "internet-enabled"
	// NetworkModeInternetDisabled restricts the sandbox to loopback-only access.
	NetworkModeInternetDisabled NetworkMode = "internet-disabled"
)

// TunnelProtocol identifies the protocol exposed through a published tunnel.
type TunnelProtocol string

const (
	// TunnelProtocolHTTP exposes the tunnel as an HTTP endpoint.
	TunnelProtocolHTTP TunnelProtocol = "http"
	// TunnelProtocolTCP exposes the tunnel as a raw TCP endpoint.
	TunnelProtocolTCP TunnelProtocol = "tcp"
)

// SnapshotStatus describes the lifecycle state of a persisted snapshot.
type SnapshotStatus string

const (
	// SnapshotStatusCreating reports that snapshot export is still running.
	SnapshotStatusCreating SnapshotStatus = "creating"
	// SnapshotStatusReady reports that the snapshot is ready for use.
	SnapshotStatusReady SnapshotStatus = "ready"
	// SnapshotStatusError reports that snapshot creation failed.
	SnapshotStatusError SnapshotStatus = "error"
)

// ExecutionStatus describes the outcome of an exec request.
type ExecutionStatus string

const (
	// ExecutionStatusRunning reports that the command is still executing.
	ExecutionStatusRunning ExecutionStatus = "running"
	// ExecutionStatusDetached reports that the command was launched without waiting.
	ExecutionStatusDetached ExecutionStatus = "detached"
	// ExecutionStatusSucceeded reports that the command exited successfully.
	ExecutionStatusSucceeded ExecutionStatus = "succeeded"
	// ExecutionStatusFailed reports that the command exited unsuccessfully.
	ExecutionStatusFailed ExecutionStatus = "failed"
	// ExecutionStatusTimedOut reports that the command exceeded its timeout.
	ExecutionStatusTimedOut ExecutionStatus = "timed_out"
	// ExecutionStatusCanceled reports that the command was canceled by the caller.
	ExecutionStatusCanceled ExecutionStatus = "canceled"
)

// GuestProfile identifies the curated workload profile associated with an
// image or guest contract.
type GuestProfile string

const (
	// GuestProfileCore is the minimal shell-oriented guest profile.
	GuestProfileCore GuestProfile = "core"
	// GuestProfileRuntime is the general-purpose runtime workload profile.
	GuestProfileRuntime GuestProfile = "runtime"
	// GuestProfileBrowser is the browser-automation workload profile.
	GuestProfileBrowser GuestProfile = "browser"
	// GuestProfileContainer is the nested-container workload profile.
	GuestProfileContainer GuestProfile = "container"
	// GuestProfileDebug is the debugging-oriented guest profile.
	GuestProfileDebug GuestProfile = "debug"
)

// GuestControlMode identifies how the daemon communicates with a guest.
type GuestControlMode string

const (
	// GuestControlModeAgent uses the in-guest agent protocol.
	GuestControlModeAgent GuestControlMode = "agent"
	// GuestControlModeSSHCompat uses SSH-based compatibility shims.
	GuestControlModeSSHCompat GuestControlMode = "ssh-compat"
)

// IsValid reports whether p is a curated guest profile understood by the
// control plane.
func (p GuestProfile) IsValid() bool {
	switch p {
	case GuestProfileCore, GuestProfileRuntime, GuestProfileBrowser, GuestProfileContainer, GuestProfileDebug:
		return true
	default:
		return false
	}
}

// IsValid reports whether m is a supported guest control mode.
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
	RuntimeSelection       RuntimeSelection `json:"runtime_selection,omitempty"`
	BaseImageRef           string           `json:"base_image_ref"`
	Profile                GuestProfile     `json:"profile,omitempty"`
	Features               []string         `json:"features,omitempty"`
	Capabilities           []string         `json:"capabilities,omitempty"`
	DangerousProfileReason string           `json:"dangerous_profile_reason,omitempty"`
	CPULimit               CPUQuantity      `json:"cpu_limit"`
	MemoryLimitMB          int              `json:"memory_limit_mb"`
	PIDsLimit              int              `json:"pids_limit"`
	DiskLimitMB            int              `json:"disk_limit_mb"`
	NetworkMode            NetworkMode      `json:"network_mode"`
	AllowTunnels           *bool            `json:"allow_tunnels,omitempty"`
	Start                  bool             `json:"start"`
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

// WorkspaceExportRequest selects workspace paths to include in an archive export.
type WorkspaceExportRequest struct {
	Paths []string `json:"paths"`
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
	OneTime    bool   `json:"one_time,omitempty"`
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
	URL          string    `json:"url"`
	ExpiresAt    time.Time `json:"expires_at"`
	CapabilityID string    `json:"capability_id,omitempty"`
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
	BundleSHA256             string           `json:"bundle_sha256,omitempty"`
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

// Tenant identifies the caller's tenancy boundary and authentication record.
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

// AuditEvent records a policy or lifecycle event emitted by the service layer.
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

// ServiceAccount describes a named service principal bound to a tenant.
type ServiceAccount struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenant_id"`
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes,omitempty"`
	Disabled  bool       `json:"disabled"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// PromotedGuestImage records a guest image that has passed promotion checks
// for production use.
type PromotedGuestImage struct {
	ImageRef               string           `json:"image_ref"`
	ImageSHA256            string           `json:"image_sha256"`
	Profile                GuestProfile     `json:"profile"`
	ControlMode            GuestControlMode `json:"control_mode"`
	ControlProtocolVersion string           `json:"control_protocol_version"`
	ContractVersion        string           `json:"contract_version"`
	ProvenanceJSON         string           `json:"provenance_json,omitempty"`
	VerificationStatus     string           `json:"verification_status"`
	PromotionStatus        string           `json:"promotion_status"`
	PromotedAt             *time.Time       `json:"promoted_at,omitempty"`
	PromotedBy             string           `json:"promoted_by,omitempty"`
}

// ReleaseEvidence stores operator-supplied evidence for a named release gate.
type ReleaseEvidence struct {
	ID               string           `json:"id"`
	GateName         string           `json:"gate_name"`
	HostFingerprint  string           `json:"host_fingerprint"`
	RuntimeSelection RuntimeSelection `json:"runtime_selection,omitempty"`
	ImageRef         string           `json:"image_ref,omitempty"`
	Profile          GuestProfile     `json:"profile,omitempty"`
	Outcome          string           `json:"outcome"`
	ArtifactPath     string           `json:"artifact_path,omitempty"`
	StartedAt        time.Time        `json:"started_at"`
	CompletedAt      *time.Time       `json:"completed_at,omitempty"`
}

// TunnelCapability is a one-time capability used to authorize tunnel access
// flows such as signed bootstrap links.
type TunnelCapability struct {
	ID         string     `json:"id"`
	TunnelID   string     `json:"tunnel_id"`
	NonceHash  string     `json:"-"`
	Path       string     `json:"path"`
	ExpiresAt  time.Time  `json:"expires_at"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}
