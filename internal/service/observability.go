package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
)

type TenantQuotaView struct {
	Quota             model.TenantQuota      `json:"quota"`
	Usage             repository.TenantUsage `json:"usage"`
	StorageQuotaBytes int64                  `json:"storage_quota_bytes"`
	StoragePressure   float64                `json:"storage_pressure"`
	StorageEntries    int64                  `json:"storage_entries"`
	EntryPressure     float64                `json:"entry_pressure"`
	RunningPressure   float64                `json:"running_pressure"`
	CPUPressure       float64                `json:"cpu_pressure"`
	MemoryPressure    float64                `json:"memory_pressure"`
	Alerts            []string               `json:"alerts,omitempty"`
}

type CapacityReport struct {
	Backend          string                        `json:"backend"`
	CheckedAt        time.Time                     `json:"checked_at"`
	QuotaView        TenantQuotaView               `json:"quota_view"`
	StatusCounts     map[string]int                `json:"status_counts"`
	ProfileCounts    map[string]int                `json:"profile_counts,omitempty"`
	CapabilityCounts map[string]int                `json:"capability_counts,omitempty"`
	SnapshotCounts   map[model.SnapshotStatus]int  `json:"snapshot_counts,omitempty"`
	ExecutionCounts  map[model.ExecutionStatus]int `json:"execution_counts,omitempty"`
	Alerts           []string                      `json:"alerts,omitempty"`
}

func buildTenantQuotaView(cfg config.Config, quota model.TenantQuota, usage repository.TenantUsage) TenantQuotaView {
	storageQuotaBytes := int64(quota.MaxStorageMB) * 1024 * 1024
	view := TenantQuotaView{
		Quota:             quota,
		Usage:             usage,
		StorageQuotaBytes: storageQuotaBytes,
		StoragePressure:   ratioInt64(usage.ActualStorageBytes, storageQuotaBytes),
		StorageEntries:    usage.ActualStorageEntries,
		EntryPressure:     ratioInt64(usage.ActualStorageEntries, int64(cfg.StorageWarningFileCount)),
		RunningPressure:   ratioInt(usage.RunningSandboxes, quota.MaxRunningSandboxes),
		CPUPressure:       ratioInt64(usage.RequestedCPU.MilliValue(), quota.MaxCPUCores.MilliValue()),
		MemoryPressure:    ratioInt(usage.RequestedMemory, quota.MaxMemoryMB),
	}
	if view.StoragePressure >= 1 {
		view.Alerts = append(view.Alerts, "storage quota pressure exceeded")
	} else if view.StoragePressure >= 0.8 {
		view.Alerts = append(view.Alerts, "storage quota pressure above 80%")
	}
	if view.EntryPressure >= 1 {
		view.Alerts = append(view.Alerts, "storage file-count pressure exceeded")
	} else if view.EntryPressure >= 0.8 {
		view.Alerts = append(view.Alerts, "storage file-count pressure above 80%")
	}
	if view.RunningPressure >= 1 {
		view.Alerts = append(view.Alerts, "running sandbox quota exceeded")
	}
	if view.CPUPressure >= 1 {
		view.Alerts = append(view.Alerts, "cpu quota pressure exceeded")
	}
	if view.MemoryPressure >= 1 {
		view.Alerts = append(view.Alerts, "memory quota pressure exceeded")
	}
	return view
}

func (s *Service) CapacityReport(ctx context.Context, tenantID string) (CapacityReport, error) {
	if err := s.enforceAdminInspectionPolicy(ctx, tenantID, "capacity.inspect"); err != nil {
		return CapacityReport{}, err
	}
	quota, err := s.store.GetQuota(ctx, tenantID)
	if err != nil {
		return CapacityReport{}, err
	}
	usage, err := s.store.TenantUsage(ctx, tenantID)
	if err != nil {
		return CapacityReport{}, err
	}
	sandboxes, err := s.store.ListSandboxes(ctx, tenantID)
	if err != nil {
		return CapacityReport{}, err
	}
	statusCounts := make(map[string]int)
	profileCounts := make(map[string]int)
	capabilityCounts := make(map[string]int)
	for _, sandbox := range sandboxes {
		statusCounts[string(sandbox.Status)]++
		if profile := strings.TrimSpace(string(sandbox.Profile)); profile != "" {
			profileCounts[profile]++
		}
		for _, capability := range sandbox.Capabilities {
			if trimmed := strings.TrimSpace(capability); trimmed != "" {
				capabilityCounts[trimmed]++
			}
		}
	}
	snapshotCounts, err := s.store.SnapshotCounts(ctx, tenantID)
	if err != nil {
		return CapacityReport{}, err
	}
	executionCounts, err := s.store.ExecutionCounts(ctx, tenantID)
	if err != nil {
		return CapacityReport{}, err
	}
	quotaView := buildTenantQuotaView(s.cfg, quota, usage)
	report := CapacityReport{
		Backend:          s.cfg.RuntimeBackend,
		CheckedAt:        time.Now().UTC(),
		QuotaView:        quotaView,
		StatusCounts:     statusCounts,
		ProfileCounts:    profileCounts,
		CapabilityCounts: capabilityCounts,
		SnapshotCounts:   snapshotCounts,
		ExecutionCounts:  executionCounts,
		Alerts:           append([]string(nil), quotaView.Alerts...),
	}
	if statusCounts[string(model.SandboxStatusDegraded)] > 0 {
		report.Alerts = append(report.Alerts, "one or more sandboxes are degraded")
	}
	if snapshotCounts[model.SnapshotStatusCreating] > 0 {
		report.Alerts = append(report.Alerts, "one or more snapshots are incomplete")
	}
	return report, nil
}

func (s *Service) MetricsReport(ctx context.Context, tenantID string) (string, error) {
	if err := s.enforceAdminInspectionPolicy(ctx, tenantID, "metrics.inspect"); err != nil {
		return "", err
	}
	report, err := s.CapacityReport(ctx, tenantID)
	if err != nil {
		return "", err
	}
	health, err := s.persistedRuntimeHealth(ctx, tenantID)
	if err != nil {
		return "", err
	}
	var lines []string
	lines = append(lines,
		"# TYPE or3_sandbox_sandboxes_total gauge",
		fmt.Sprintf("or3_sandbox_sandboxes_total %d", report.QuotaView.Usage.Sandboxes),
		fmt.Sprintf("or3_sandbox_running_sandboxes %d", report.QuotaView.Usage.RunningSandboxes),
		fmt.Sprintf("or3_sandbox_exec_running %d", report.QuotaView.Usage.ConcurrentExecs),
		fmt.Sprintf("or3_sandbox_tunnels_active %d", report.QuotaView.Usage.ActiveTunnels),
		fmt.Sprintf("or3_sandbox_actual_storage_bytes %d", report.QuotaView.Usage.ActualStorageBytes),
		fmt.Sprintf("or3_sandbox_actual_storage_entries %d", report.QuotaView.Usage.ActualStorageEntries),
		fmt.Sprintf("or3_sandbox_storage_pressure_ratio %.6f", report.QuotaView.StoragePressure),
		fmt.Sprintf("or3_sandbox_storage_entry_pressure_ratio %.6f", report.QuotaView.EntryPressure),
		fmt.Sprintf("or3_sandbox_running_pressure_ratio %.6f", report.QuotaView.RunningPressure),
		fmt.Sprintf("or3_sandbox_runtime_healthy %d", boolMetric(health.Healthy)),
	)
	for _, status := range sortedStringKeys(health.StatusCounts) {
		lines = append(lines, fmt.Sprintf("or3_sandbox_runtime_status_count{status=%q} %d", status, health.StatusCounts[status]))
	}
	for _, profile := range sortedStringKeys(report.ProfileCounts) {
		lines = append(lines, fmt.Sprintf("or3_sandbox_profile_count{profile=%q} %d", profile, report.ProfileCounts[profile]))
	}
	for _, capability := range sortedStringKeys(report.CapabilityCounts) {
		lines = append(lines, fmt.Sprintf("or3_sandbox_capability_count{capability=%q} %d", capability, report.CapabilityCounts[capability]))
	}
	for _, status := range sortedSnapshotStatuses(report.SnapshotCounts) {
		lines = append(lines, fmt.Sprintf("or3_sandbox_snapshots_count{status=%q} %d", status, report.SnapshotCounts[status]))
	}
	for _, status := range sortedExecutionStatuses(report.ExecutionCounts) {
		lines = append(lines, fmt.Sprintf("or3_sandbox_executions_count{status=%q} %d", status, report.ExecutionCounts[status]))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func (s *Service) persistedRuntimeHealth(ctx context.Context, tenantID string) (model.RuntimeHealth, error) {
	health := model.RuntimeHealth{
		Backend:      s.cfg.RuntimeBackend,
		Healthy:      true,
		CheckedAt:    time.Now().UTC(),
		StatusCounts: make(map[string]int),
	}
	var sandboxes []model.Sandbox
	var err error
	if tenantID != "" {
		sandboxes, err = s.store.ListNonDeletedSandboxesByTenant(ctx, tenantID)
	} else {
		sandboxes, err = s.store.ListNonDeletedSandboxes(ctx)
	}
	if err != nil {
		return health, err
	}
	for _, sandbox := range sandboxes {
		observedStatus := sandbox.Status
		if sandbox.RuntimeStatus != "" {
			observedStatus = model.SandboxStatus(sandbox.RuntimeStatus)
		}
		entry := model.RuntimeSandboxHealth{
			SandboxID:       sandbox.ID,
			TenantID:        sandbox.TenantID,
			PersistedStatus: sandbox.Status,
			ObservedStatus:  observedStatus,
			RuntimeID:       sandbox.RuntimeID,
			RuntimeStatus:   sandbox.RuntimeStatus,
			Error:           sandbox.LastRuntimeError,
		}
		health.StatusCounts[string(entry.ObservedStatus)]++
		health.Sandboxes = append(health.Sandboxes, entry)
		if entry.ObservedStatus == model.SandboxStatusError || entry.ObservedStatus == model.SandboxStatusDegraded {
			health.Healthy = false
		}
	}
	return health, nil
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
}

func ratioInt(value, limit int) float64 {
	if limit <= 0 {
		return 0
	}
	return float64(value) / float64(limit)
}

func ratioInt64(value, limit int64) float64 {
	if limit <= 0 {
		return 0
	}
	return float64(value) / float64(limit)
}

func sortedStringKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSnapshotStatuses(values map[model.SnapshotStatus]int) []model.SnapshotStatus {
	keys := make([]model.SnapshotStatus, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func sortedExecutionStatuses(values map[model.ExecutionStatus]int) []model.ExecutionStatus {
	keys := make([]model.ExecutionStatus, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
