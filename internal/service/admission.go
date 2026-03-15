package service

import (
	"context"
	"fmt"
	"syscall"

	"or3-sandbox/internal/model"
)

// AdmissionError reports that an operation was denied by admission control.
type AdmissionError struct {
	Message   string
	Retryable bool
}

// Error returns the human-readable admission failure message.
func (e AdmissionError) Error() string {
	return e.Message
}

type admissionDelta struct {
	nodeSandboxes int
	nodeRunning   int
	runningCPU    model.CPUQuantity
	runningMemory int
	tenantStarts  int
	tenantHeavy   int
}

type admissionSnapshot struct {
	nodeSandboxes int
	nodeRunning   int
	runningCPU    model.CPUQuantity
	runningMemory int
	freeStorage   int64
	tenantStarts  int
	tenantHeavy   int
}

func (s *Service) enforceAdmission(ctx context.Context, tenantID, sandboxID, action string, delta admissionDelta) error {
	if !s.admissionEnabled() {
		return nil
	}
	snapshot, err := s.admissionSnapshot(ctx, tenantID)
	if err != nil {
		return err
	}
	if err := s.evaluateAdmission(snapshot, delta); err != nil {
		admissionErr := err
		retryable := false
		if typed, ok := err.(AdmissionError); ok {
			admissionErr = typed
			retryable = typed.Retryable
		}
		s.recordAudit(ctx, tenantID, sandboxID, "admission."+action, sandboxID, "denied", auditDetail(
			auditKV("reason", admissionErr.Error()),
			auditKV("retryable", retryable),
			auditKV("node_sandboxes", snapshot.nodeSandboxes),
			auditKV("node_running", snapshot.nodeRunning),
			auditKV("tenant_starts", snapshot.tenantStarts),
			auditKV("tenant_heavy_ops", snapshot.tenantHeavy),
		))
		return admissionErr
	}
	return nil
}

func (s *Service) admissionEnabled() bool {
	return s.cfg.AdmissionMaxNodeSandboxes > 0 ||
		s.cfg.AdmissionMaxNodeRunning > 0 ||
		s.cfg.AdmissionMaxNodeCPU > 0 ||
		s.cfg.AdmissionMaxNodeMemoryMB > 0 ||
		s.cfg.AdmissionMinNodeFreeStorageMB > 0 ||
		s.cfg.AdmissionMaxTenantStarts > 0 ||
		s.cfg.AdmissionMaxTenantHeavyOps > 0
}

func (s *Service) admissionSnapshot(ctx context.Context, tenantID string) (admissionSnapshot, error) {
	sandboxes, err := s.store.ListNonDeletedSandboxes(ctx)
	if err != nil {
		return admissionSnapshot{}, err
	}
	snapshot := admissionSnapshot{freeStorage: -1}
	for _, sandbox := range sandboxes {
		snapshot.nodeSandboxes++
		if consumesNodeRuntimeCapacity(sandbox.Status) {
			snapshot.nodeRunning++
			snapshot.runningCPU += sandbox.CPULimit
			snapshot.runningMemory += sandbox.MemoryLimitMB
		}
		if sandbox.TenantID != tenantID {
			continue
		}
		if isConcurrentStartStatus(sandbox.Status) {
			snapshot.tenantStarts++
		}
		if isHeavyOperationStatus(sandbox.Status) {
			snapshot.tenantHeavy++
		}
	}
	if counts, err := s.store.SnapshotCounts(ctx, tenantID); err == nil {
		snapshot.tenantHeavy += counts[model.SnapshotStatusCreating]
	} else {
		return admissionSnapshot{}, err
	}
	if s.cfg.AdmissionMinNodeFreeStorageMB > 0 {
		freeStorage, err := minAvailableBytes(s.cfg.StorageRoot, s.cfg.SnapshotRoot)
		if err != nil {
			return admissionSnapshot{}, err
		}
		snapshot.freeStorage = freeStorage
	}
	return snapshot, nil
}

func (s *Service) evaluateAdmission(snapshot admissionSnapshot, delta admissionDelta) error {
	if s.cfg.AdmissionMaxNodeSandboxes > 0 && snapshot.nodeSandboxes+delta.nodeSandboxes > s.cfg.AdmissionMaxNodeSandboxes {
		return AdmissionError{Message: fmt.Sprintf("node sandbox admission limit reached (%d/%d)", snapshot.nodeSandboxes+delta.nodeSandboxes, s.cfg.AdmissionMaxNodeSandboxes), Retryable: true}
	}
	if s.cfg.AdmissionMaxNodeRunning > 0 && snapshot.nodeRunning+delta.nodeRunning > s.cfg.AdmissionMaxNodeRunning {
		return AdmissionError{Message: fmt.Sprintf("node running admission limit reached (%d/%d)", snapshot.nodeRunning+delta.nodeRunning, s.cfg.AdmissionMaxNodeRunning), Retryable: true}
	}
	if s.cfg.AdmissionMaxNodeCPU > 0 && snapshot.runningCPU.MilliValue()+delta.runningCPU.MilliValue() > s.cfg.AdmissionMaxNodeCPU.MilliValue() {
		return AdmissionError{Message: fmt.Sprintf("node cpu admission limit reached (%s/%s)", model.CPUQuantity(snapshot.runningCPU.MilliValue()+delta.runningCPU.MilliValue()).String(), s.cfg.AdmissionMaxNodeCPU.String()), Retryable: true}
	}
	if s.cfg.AdmissionMaxNodeMemoryMB > 0 && snapshot.runningMemory+delta.runningMemory > s.cfg.AdmissionMaxNodeMemoryMB {
		return AdmissionError{Message: fmt.Sprintf("node memory admission limit reached (%d/%d MB)", snapshot.runningMemory+delta.runningMemory, s.cfg.AdmissionMaxNodeMemoryMB), Retryable: true}
	}
	if s.cfg.AdmissionMinNodeFreeStorageMB > 0 {
		minFreeBytes := int64(s.cfg.AdmissionMinNodeFreeStorageMB) * 1024 * 1024
		if snapshot.freeStorage >= 0 && snapshot.freeStorage < minFreeBytes {
			return AdmissionError{Message: fmt.Sprintf("node free storage below admission floor (%d/%d bytes)", snapshot.freeStorage, minFreeBytes), Retryable: true}
		}
	}
	if s.cfg.AdmissionMaxTenantStarts > 0 && snapshot.tenantStarts+delta.tenantStarts > s.cfg.AdmissionMaxTenantStarts {
		return AdmissionError{Message: fmt.Sprintf("tenant concurrent start limit reached (%d/%d)", snapshot.tenantStarts+delta.tenantStarts, s.cfg.AdmissionMaxTenantStarts), Retryable: true}
	}
	if s.cfg.AdmissionMaxTenantHeavyOps > 0 && snapshot.tenantHeavy+delta.tenantHeavy > s.cfg.AdmissionMaxTenantHeavyOps {
		return AdmissionError{Message: fmt.Sprintf("tenant heavy-operation limit reached (%d/%d)", snapshot.tenantHeavy+delta.tenantHeavy, s.cfg.AdmissionMaxTenantHeavyOps), Retryable: true}
	}
	return nil
}

func consumesNodeRuntimeCapacity(status model.SandboxStatus) bool {
	switch status {
	case model.SandboxStatusBooting, model.SandboxStatusStarting, model.SandboxStatusRunning, model.SandboxStatusSuspending, model.SandboxStatusSuspended:
		return true
	default:
		return false
	}
}

func isConcurrentStartStatus(status model.SandboxStatus) bool {
	switch status {
	case model.SandboxStatusCreating, model.SandboxStatusBooting, model.SandboxStatusStarting:
		return true
	default:
		return false
	}
}

func isHeavyOperationStatus(status model.SandboxStatus) bool {
	switch status {
	case model.SandboxStatusCreating, model.SandboxStatusStarting, model.SandboxStatusStopping, model.SandboxStatusDeleting, model.SandboxStatusSuspending:
		return true
	default:
		return false
	}
}

func minAvailableBytes(paths ...string) (int64, error) {
	var result int64 = -1
	for _, path := range paths {
		if path == "" {
			continue
		}
		var stat syscall.Statfs_t
		if err := syscall.Statfs(path, &stat); err != nil {
			return 0, err
		}
		available := int64(stat.Bavail) * int64(stat.Bsize)
		if result < 0 || available < result {
			result = available
		}
	}
	if result < 0 {
		return 0, nil
	}
	return result, nil
}
