package model

import (
	"time"
)

// TenantUsageView is the transport-safe usage summary embedded in quota responses.
type TenantUsageView struct {
	Sandboxes            int         `json:"sandboxes"`
	RunningSandboxes     int         `json:"running_sandboxes"`
	ConcurrentExecs      int         `json:"concurrent_execs"`
	ActiveTunnels        int         `json:"active_tunnels"`
	RequestedCPU         CPUQuantity `json:"requested_cpu"`
	RequestedMemory      int         `json:"requested_memory_mb"`
	RequestedStorage     int         `json:"requested_storage_mb"`
	ActualStorageBytes   int64       `json:"actual_storage_bytes"`
	ActualStorageEntries int64       `json:"actual_storage_entries"`
}

// TenantQuotaView is the JSON payload returned by GET /v1/quotas/me.
type TenantQuotaView struct {
	Quota             TenantQuota     `json:"quota"`
	Usage             TenantUsageView `json:"usage"`
	StorageQuotaBytes int64           `json:"storage_quota_bytes"`
	StoragePressure   float64         `json:"storage_pressure"`
	StorageEntries    int64           `json:"storage_entries"`
	EntryPressure     float64         `json:"entry_pressure"`
	RunningPressure   float64         `json:"running_pressure"`
	CPUPressure       float64         `json:"cpu_pressure"`
	MemoryPressure    float64         `json:"memory_pressure"`
	Alerts            []string        `json:"alerts,omitempty"`
}

// NodePressureView summarizes node-local admission pressure for capacity reporting.
type NodePressureView struct {
	Sandboxes           int      `json:"sandboxes"`
	RunningSandboxes    int      `json:"running_sandboxes"`
	RunningCPUMillis    int64    `json:"running_cpu_millis"`
	RunningMemoryMB     int      `json:"running_memory_mb"`
	FreeStorageBytes    int64    `json:"free_storage_bytes,omitempty"`
	MaxSandboxes        int      `json:"max_sandboxes,omitempty"`
	MaxRunningSandboxes int      `json:"max_running_sandboxes,omitempty"`
	MaxCPUMillis        int64    `json:"max_cpu_millis,omitempty"`
	MaxMemoryMB         int      `json:"max_memory_mb,omitempty"`
	MinFreeStorageBytes int64    `json:"min_free_storage_bytes,omitempty"`
	Alerts              []string `json:"alerts,omitempty"`
}

// CapacityReport is the JSON payload returned by GET /v1/runtime/capacity.
type CapacityReport struct {
	Backend                  string                    `json:"backend"`
	DefaultRuntimeSelection  RuntimeSelection          `json:"default_runtime_selection,omitempty"`
	EnabledRuntimeSelections []RuntimeSelection        `json:"enabled_runtime_selections,omitempty"`
	CheckedAt                time.Time                 `json:"checked_at"`
	QuotaView                TenantQuotaView           `json:"quota_view"`
	NodePressure             NodePressureView          `json:"node_pressure"`
	StatusCounts             map[string]int            `json:"status_counts"`
	RuntimeSelectionCounts   map[string]int            `json:"runtime_selection_counts,omitempty"`
	ProfileCounts            map[string]int            `json:"profile_counts,omitempty"`
	CapabilityCounts         map[string]int            `json:"capability_counts,omitempty"`
	SnapshotCounts           map[SnapshotStatus]int    `json:"snapshot_counts,omitempty"`
	ExecutionCounts          map[ExecutionStatus]int   `json:"execution_counts,omitempty"`
	AuditCounts              map[string]map[string]int `json:"audit_counts,omitempty"`
	Alerts                   []string                  `json:"alerts,omitempty"`
}
