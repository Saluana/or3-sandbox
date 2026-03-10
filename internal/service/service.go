package service

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"or3-sandbox/internal/archiveutil"
	"or3-sandbox/internal/config"
	"or3-sandbox/internal/dockerimage"
	"or3-sandbox/internal/guestimage"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
)

const (
	previewLimit                           = 64 * 1024
	defaultReconcileStorageRefreshInterval = 5 * time.Minute
)

type Service struct {
	cfg         config.Config
	store       *repository.Store
	runtime     model.RuntimeManager
	log         *slog.Logger
	admissionMu sync.Mutex
}

type workspaceFileRuntime interface {
	ReadWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string) (model.FileReadResponse, error)
	WriteWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string, content string) error
	DeleteWorkspacePath(ctx context.Context, sandbox model.Sandbox, relativePath string) error
	MkdirWorkspace(ctx context.Context, sandbox model.Sandbox, relativePath string) error
}

type workspaceBinaryFileRuntime interface {
	ReadWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string) ([]byte, error)
	WriteWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string, content []byte) error
}

type storageMeasurer interface {
	MeasureStorage(ctx context.Context, sandbox model.Sandbox) (model.StorageUsage, error)
}

func New(cfg config.Config, store *repository.Store, runtime model.RuntimeManager, logs ...*slog.Logger) *Service {
	log := slog.Default()
	if len(logs) > 0 && logs[0] != nil {
		log = logs[0]
	}
	return &Service{cfg: cfg, store: store, runtime: runtime, log: log}
}

func (s *Service) CreateSandbox(ctx context.Context, tenant model.Tenant, quota model.TenantQuota, req model.CreateSandboxRequest) (model.Sandbox, error) {
	req = s.applyCreateDefaults(req)
	if err := validateCreate(req); err != nil {
		return model.Sandbox{}, err
	}
	var err error
	req, contract, err := s.validateRuntimeCreate(ctx, req)
	if err != nil {
		return model.Sandbox{}, err
	}
	if err := s.enforceCreatePolicy(ctx, tenant.ID, req); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.checkQuota(ctx, tenant.ID, quota, req); err != nil {
		return model.Sandbox{}, err
	}
	if req.Start {
		if err := s.checkRunningQuota(ctx, tenant.ID, quota); err != nil {
			return model.Sandbox{}, err
		}
	}
	id := newID("sbx-")
	baseDir := filepath.Join(s.cfg.StorageRoot, id)
	storageRoot := filepath.Join(baseDir, "rootfs")
	workspaceRoot := storageClassRoot(baseDir, model.StorageClassWorkspace)
	cacheRoot := storageClassRoot(baseDir, model.StorageClassCache)
	scratchRoot := storageClassRoot(baseDir, model.StorageClassScratch)
	secretsRoot := storageClassRoot(baseDir, model.StorageClassSecrets)
	for _, dir := range []string{storageRoot, workspaceRoot, cacheRoot, scratchRoot, secretsRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return model.Sandbox{}, err
		}
	}
	now := time.Now().UTC()
	initialStatus := model.SandboxStatusCreating
	if req.Start {
		initialStatus = model.SandboxStatusStarting
	}
	sandbox := model.Sandbox{
		ID:                       id,
		TenantID:                 tenant.ID,
		Status:                   initialStatus,
		RuntimeBackend:           s.cfg.RuntimeBackend,
		RuntimeClass:             model.BackendToRuntimeClass(s.cfg.RuntimeBackend),
		BaseImageRef:             req.BaseImageRef,
		Profile:                  req.Profile,
		Features:                 model.NormalizeFeatures(req.Features),
		Capabilities:             s.resolvedCapabilities(req, contract),
		ControlMode:              contract.Control.Mode,
		ControlProtocolVersion:   contract.Control.ProtocolVersion,
		WorkspaceContractVersion: contract.WorkspaceContractVersion,
		ImageContractVersion:     contract.ContractVersion,
		CPULimit:                 req.CPULimit,
		MemoryLimitMB:            req.MemoryLimitMB,
		PIDsLimit:                req.PIDsLimit,
		DiskLimitMB:              req.DiskLimitMB,
		NetworkMode:              req.NetworkMode,
		AllowTunnels:             *req.AllowTunnels,
		StorageRoot:              storageRoot,
		WorkspaceRoot:            workspaceRoot,
		CacheRoot:                cacheRoot,
		RuntimeID:                id,
		RuntimeStatus:            string(initialStatus),
		CreatedAt:                now,
		UpdatedAt:                now,
		LastActiveAt:             now,
	}
	if err := s.reserveSandboxCreate(ctx, tenant.ID, sandbox, req); err != nil {
		_ = os.RemoveAll(filepath.Join(s.cfg.StorageRoot, id))
		return model.Sandbox{}, err
	}
	spec := model.SandboxSpec{
		SandboxID:                sandbox.ID,
		TenantID:                 sandbox.TenantID,
		BaseImageRef:             sandbox.BaseImageRef,
		Profile:                  sandbox.Profile,
		Features:                 append([]string(nil), sandbox.Features...),
		Capabilities:             append([]string(nil), sandbox.Capabilities...),
		ControlMode:              sandbox.ControlMode,
		ControlProtocolVersion:   sandbox.ControlProtocolVersion,
		WorkspaceContractVersion: sandbox.WorkspaceContractVersion,
		ImageContractVersion:     sandbox.ImageContractVersion,
		CPULimit:                 sandbox.CPULimit,
		MemoryLimitMB:            sandbox.MemoryLimitMB,
		PIDsLimit:                sandbox.PIDsLimit,
		DiskLimitMB:              sandbox.DiskLimitMB,
		NetworkMode:              sandbox.NetworkMode,
		AllowTunnels:             sandbox.AllowTunnels,
		StorageRoot:              sandbox.StorageRoot,
		WorkspaceRoot:            sandbox.WorkspaceRoot,
		CacheRoot:                sandbox.CacheRoot,
		ScratchRoot:              scratchRoot,
		SecretsRoot:              secretsRoot,
		NetworkPolicy:            buildNetworkPolicy(sandbox.NetworkMode, sandbox.AllowTunnels),
	}
	state, err := s.runtime.Create(ctx, spec)
	if err != nil {
		return model.Sandbox{}, s.rollbackFailedCreate(ctx, tenant.ID, sandbox, "runtime_create", req.Start, err)
	}
	if req.Start {
		state, err = s.runtime.Start(ctx, sandbox)
		if err != nil {
			return model.Sandbox{}, s.rollbackFailedCreate(ctx, tenant.ID, sandbox, "runtime_start", true, err)
		}
		sandbox.Status = model.SandboxStatusRunning
	} else {
		sandbox.Status = model.SandboxStatusStopped
	}
	sandbox.RuntimeStatus = string(state.Status)
	sandbox.UpdatedAt = time.Now().UTC()
	sandbox.LastActiveAt = sandbox.UpdatedAt
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.store.UpdateRuntimeState(ctx, sandbox.ID, state); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.refreshStorage(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	s.recordAudit(ctx, tenant.ID, sandbox.ID, "sandbox.create", sandbox.ID, "ok", auditDetail(
		auditKV("runtime", sandbox.RuntimeBackend),
		auditKV("base_image_ref", sandbox.BaseImageRef),
		auditKV("start_requested", req.Start),
		auditKV("allow_tunnels", sandbox.AllowTunnels),
		networkPolicyAuditDetail(buildNetworkPolicy(sandbox.NetworkMode, sandbox.AllowTunnels)),
	))
	return s.store.GetSandbox(ctx, tenant.ID, sandbox.ID)
}

func (s *Service) rollbackFailedCreate(ctx context.Context, tenantID string, sandbox model.Sandbox, stage string, startRequested bool, cause error) error {
	persistCtx := context.WithoutCancel(ctx)
	cleanupErr := s.runtime.Destroy(persistCtx, sandbox)
	if cleanupErr == nil {
		cleanupErr = os.RemoveAll(filepath.Join(s.cfg.StorageRoot, sandbox.ID))
	}
	now := time.Now().UTC()
	sandbox.LastRuntimeError = cause.Error()
	sandbox.UpdatedAt = now
	sandbox.LastActiveAt = now
	if cleanupErr == nil {
		sandbox.Status = model.SandboxStatusDeleted
		sandbox.RuntimeStatus = string(model.SandboxStatusDeleted)
		sandbox.DeletedAt = &now
	} else {
		sandbox.Status = model.SandboxStatusError
		sandbox.RuntimeStatus = string(model.SandboxStatusError)
	}
	_ = s.store.UpdateSandboxState(persistCtx, sandbox)
	s.recordAudit(persistCtx, tenantID, sandbox.ID, "sandbox.create", sandbox.ID, "error", auditDetail(
		auditKV("stage", stage),
		auditKV("start_requested", startRequested),
		auditKV("rollback", cleanupErr == nil),
	), "error", cause)
	if cleanupErr != nil {
		s.log.Error("failed create cleanup", "event", "sandbox.create.rollback", "sandbox_id", sandbox.ID, "stage", stage, "error", cleanupErr)
	}
	return cause
}

func (s *Service) GetSandbox(ctx context.Context, tenantID, sandboxID string) (model.Sandbox, error) {
	return s.store.GetSandbox(ctx, tenantID, sandboxID)
}

func (s *Service) RuntimeBackend() string {
	return s.cfg.RuntimeBackend
}

func (s *Service) RuntimeClass() model.RuntimeClass {
	return s.cfg.RuntimeClass()
}

func (s *Service) ListSandboxes(ctx context.Context, tenantID string) ([]model.Sandbox, error) {
	return s.store.ListSandboxes(ctx, tenantID)
}

func (s *Service) GetTenantQuotaView(ctx context.Context, tenantID string) (TenantQuotaView, error) {
	quota, err := s.store.GetQuota(ctx, tenantID)
	if err != nil {
		return TenantQuotaView{}, err
	}
	usage, err := s.store.TenantUsage(ctx, tenantID)
	if err != nil {
		return TenantQuotaView{}, err
	}
	return buildTenantQuotaView(s.cfg, quota, usage), nil
}

func (s *Service) RuntimeHealth(ctx context.Context, tenantID string) (model.RuntimeHealth, error) {
	if err := s.enforceAdminInspectionPolicy(ctx, tenantID, "runtime.inspect"); err != nil {
		return model.RuntimeHealth{}, err
	}
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
		entry := model.RuntimeSandboxHealth{
			SandboxID:       sandbox.ID,
			TenantID:        sandbox.TenantID,
			PersistedStatus: sandbox.Status,
			ObservedStatus:  sandbox.Status,
			RuntimeID:       sandbox.RuntimeID,
			RuntimeStatus:   sandbox.RuntimeStatus,
			Error:           sandbox.LastRuntimeError,
		}
		state, err := s.runtime.Inspect(ctx, sandbox)
		if err != nil {
			entry.ObservedStatus = model.SandboxStatusDegraded
			entry.RuntimeStatus = string(model.SandboxStatusDegraded)
			entry.Error = err.Error()
			health.Healthy = false
		} else {
			entry.ObservedStatus = state.Status
			entry.RuntimeID = state.RuntimeID
			entry.RuntimeStatus = string(state.Status)
			entry.Pid = state.Pid
			entry.IPAddress = state.IPAddress
			entry.Error = state.Error
			if state.Status == model.SandboxStatusError || state.Status == model.SandboxStatusDegraded {
				health.Healthy = false
			}
		}
		health.StatusCounts[string(entry.ObservedStatus)]++
		health.Sandboxes = append(health.Sandboxes, entry)
	}
	return health, nil
}

func (s *Service) StartSandbox(ctx context.Context, tenantID, sandboxID string, quota model.TenantQuota) (model.Sandbox, error) {
	var err error
	if err := s.checkRunningQuota(ctx, tenantID, quota); err != nil {
		return model.Sandbox{}, err
	}
	sandbox, err := s.reserveLifecycleTransition(ctx, tenantID, sandboxID, "start", model.SandboxStatusStarting, admissionDelta{
		nodeRunning:  1,
		tenantStarts: 1,
		tenantHeavy:  1,
	})
	if err != nil {
		return model.Sandbox{}, err
	}
	return s.executeReservedTransition(ctx, tenantID, sandbox, "sandbox.start", model.SandboxStatusStarting, model.SandboxStatusRunning, func(sandbox model.Sandbox) (model.RuntimeState, error) {
		return s.runtime.Start(ctx, sandbox)
	})
}

func (s *Service) StopSandbox(ctx context.Context, tenantID, sandboxID string, force bool) (model.Sandbox, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Sandbox{}, err
	}
	sandbox.Status = model.SandboxStatusStopping
	sandbox.RuntimeStatus = string(model.SandboxStatusStopping)
	sandbox.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	state, err := s.runtime.Stop(ctx, sandbox, force)
	if err != nil {
		sandbox.Status = model.SandboxStatusError
		sandbox.RuntimeStatus = string(model.SandboxStatusError)
		sandbox.LastRuntimeError = err.Error()
		sandbox.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdateSandboxState(ctx, sandbox)
		s.recordAudit(ctx, tenantID, sandbox.ID, "sandbox.stop", sandbox.ID, "error", auditDetail(
			auditKV("force", force),
			auditKV("requested_status", model.SandboxStatusStopped),
		), "error", err)
		return model.Sandbox{}, err
	}
	sandbox.Status = model.SandboxStatusStopped
	sandbox.RuntimeStatus = string(state.Status)
	sandbox.UpdatedAt = time.Now().UTC()
	sandbox.LastActiveAt = sandbox.UpdatedAt
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.store.UpdateRuntimeState(ctx, sandbox.ID, state); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.refreshStorage(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	s.recordAudit(ctx, tenantID, sandbox.ID, "sandbox.stop", sandbox.ID, "ok", auditDetail(
		auditKV("force", force),
		auditKV("result_status", sandbox.Status),
	))
	return s.store.GetSandbox(ctx, tenantID, sandbox.ID)
}

func (s *Service) SuspendSandbox(ctx context.Context, tenantID, sandboxID string) (model.Sandbox, error) {
	return s.transitionSandbox(ctx, tenantID, sandboxID, "sandbox.suspend", model.SandboxStatusSuspending, func(sandbox model.Sandbox) (model.RuntimeState, model.SandboxStatus, error) {
		state, err := s.runtime.Suspend(ctx, sandbox)
		return state, model.SandboxStatusSuspended, err
	}, model.SandboxStatusSuspended)
}

func (s *Service) ResumeSandbox(ctx context.Context, tenantID, sandboxID string, quota model.TenantQuota) (model.Sandbox, error) {
	var err error
	if err := s.checkRunningQuota(ctx, tenantID, quota); err != nil {
		return model.Sandbox{}, err
	}
	sandbox, err := s.reserveLifecycleTransition(ctx, tenantID, sandboxID, "resume", model.SandboxStatusStarting, admissionDelta{
		nodeRunning:  1,
		tenantStarts: 1,
		tenantHeavy:  1,
	})
	if err != nil {
		return model.Sandbox{}, err
	}
	return s.executeReservedTransition(ctx, tenantID, sandbox, "sandbox.resume", model.SandboxStatusStarting, model.SandboxStatusRunning, func(sandbox model.Sandbox) (model.RuntimeState, error) {
		return s.runtime.Resume(ctx, sandbox)
	})
}

func (s *Service) DeleteSandbox(ctx context.Context, tenantID, sandboxID string, preserveSnapshots bool) error {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return err
	}
	tunnels, err := s.store.ListTunnels(ctx, tenantID, sandboxID)
	if err != nil {
		return err
	}
	for _, tunnel := range tunnels {
		if tunnel.RevokedAt == nil {
			if err := s.store.RevokeTunnel(ctx, tenantID, tunnel.ID); err != nil {
				return err
			}
			s.recordAudit(ctx, tenantID, sandbox.ID, "tunnel.revoke", tunnel.ID, "ok", auditDetail(
				auditKV("reason", "sandbox_delete"),
				tunnelAuditDetail(tunnel),
			))
		}
	}
	if sandbox.Status == model.SandboxStatusRunning || sandbox.Status == model.SandboxStatusSuspended {
		if _, err := s.StopSandbox(ctx, tenantID, sandboxID, true); err != nil {
			return err
		}
		sandbox, _ = s.store.GetSandbox(ctx, tenantID, sandboxID)
	}
	sandbox.Status = model.SandboxStatusDeleting
	sandbox.RuntimeStatus = string(model.SandboxStatusDeleting)
	sandbox.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return err
	}
	if err := s.runtime.Destroy(ctx, sandbox); err != nil {
		sandbox.Status = model.SandboxStatusError
		sandbox.RuntimeStatus = string(model.SandboxStatusError)
		sandbox.LastRuntimeError = err.Error()
		sandbox.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdateSandboxState(ctx, sandbox)
		s.recordAudit(ctx, tenantID, sandbox.ID, "sandbox.delete", sandbox.ID, "error", auditDetail(
			auditKV("preserve_snapshots", preserveSnapshots),
		), "error", err)
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.cfg.StorageRoot, sandbox.ID)); err != nil {
		return err
	}
	if !preserveSnapshots {
		_ = os.RemoveAll(filepath.Join(s.cfg.SnapshotRoot, sandbox.ID))
	}
	now := time.Now().UTC()
	sandbox.Status = model.SandboxStatusDeleted
	sandbox.RuntimeStatus = string(model.SandboxStatusDeleted)
	sandbox.UpdatedAt = now
	sandbox.LastActiveAt = now
	sandbox.DeletedAt = &now
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return err
	}
	s.recordAudit(ctx, tenantID, sandbox.ID, "sandbox.delete", sandbox.ID, "ok", auditDetail(
		auditKV("preserve_snapshots", preserveSnapshots),
	))
	return nil
}

func (s *Service) ExecSandbox(ctx context.Context, tenant model.Tenant, quota model.TenantQuota, sandboxID string, req model.ExecRequest, stdout, stderr io.Writer) (model.Execution, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenant.ID, sandboxID)
	if err != nil {
		return model.Execution{}, err
	}
	if sandbox.Status != model.SandboxStatusRunning {
		return model.Execution{}, fmt.Errorf("sandbox %s is not running", sandbox.ID)
	}
	if err := s.enforceLifecyclePolicy(ctx, sandbox, "exec"); err != nil {
		return model.Execution{}, err
	}
	usage, err := s.store.TenantUsage(ctx, tenant.ID)
	if err != nil {
		return model.Execution{}, err
	}
	if usage.ConcurrentExecs >= quota.MaxConcurrentExecs {
		return model.Execution{}, fmt.Errorf("tenant exec quota exceeded")
	}
	id := newID("exec-")
	started := time.Now().UTC()
	execution := model.Execution{
		ID:             id,
		SandboxID:      sandbox.ID,
		TenantID:       tenant.ID,
		Command:        strings.Join(req.Command, " "),
		Cwd:            req.Cwd,
		TimeoutSeconds: int(req.Timeout.Seconds()),
		Status:         model.ExecutionStatusRunning,
		StartedAt:      started,
	}
	if execution.TimeoutSeconds == 0 && req.Timeout > 0 {
		execution.TimeoutSeconds = 1
	}
	if err := s.store.CreateExecution(ctx, execution); err != nil {
		return model.Execution{}, err
	}
	stdoutCapture := &boundedBuffer{limit: previewLimit}
	stderrCapture := &boundedBuffer{limit: previewLimit}
	streams := model.ExecStreams{
		Stdout: io.MultiWriter(stdoutCapture, stdout),
		Stderr: io.MultiWriter(stderrCapture, stderr),
	}
	handle, err := s.runtime.Exec(ctx, sandbox, req, streams)
	if err != nil {
		persistCtx := context.WithoutCancel(ctx)
		now := time.Now().UTC()
		exitCode := 1
		durationMS := now.Sub(started).Milliseconds()
		execution.Status = model.ExecutionStatusFailed
		execution.ExitCode = &exitCode
		execution.StderrPreview = err.Error()
		execution.CompletedAt = &now
		execution.DurationMS = &durationMS
		_ = s.store.UpdateExecution(persistCtx, execution)
		s.recordAudit(persistCtx, tenant.ID, sandbox.ID, "sandbox.exec", execution.ID, "error", execAuditDetail(req), "error", err)
		return model.Execution{}, err
	}
	persistCtx := context.WithoutCancel(ctx)
	if req.Detached {
		now := time.Now().UTC()
		durationMS := now.Sub(started).Milliseconds()
		execution.Status = model.ExecutionStatusDetached
		execution.CompletedAt = &now
		execution.DurationMS = &durationMS
		if err := s.store.UpdateExecution(persistCtx, execution); err != nil {
			return model.Execution{}, err
		}
		s.recordAudit(persistCtx, tenant.ID, sandbox.ID, "sandbox.exec.detached", execution.ID, "ok", execAuditDetail(req))
		return execution, nil
	}
	result := handle.Wait()
	execution.Status = result.Status
	exitCode := result.ExitCode
	execution.ExitCode = &exitCode
	execution.StdoutPreview = stdoutCapture.String()
	execution.StderrPreview = stderrCapture.String()
	execution.StdoutTruncated = stdoutCapture.truncated || result.StdoutTruncated
	execution.StderrTruncated = stderrCapture.truncated || result.StderrTruncated
	completed := result.CompletedAt.UTC()
	durationMS := result.Duration.Milliseconds()
	execution.CompletedAt = &completed
	execution.DurationMS = &durationMS
	if err := s.store.UpdateExecution(persistCtx, execution); err != nil {
		return model.Execution{}, err
	}
	_ = s.touchSandboxActivity(persistCtx, sandbox)
	s.recordAudit(persistCtx, tenant.ID, sandbox.ID, "sandbox.exec", execution.ID, string(execution.Status), execAuditDetail(req))
	return execution, nil
}

func (s *Service) CreateTTYSession(ctx context.Context, tenantID, sandboxID string, req model.TTYRequest) (model.Sandbox, model.TTYSession, model.TTYHandle, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Sandbox{}, model.TTYSession{}, nil, err
	}
	if sandbox.Status != model.SandboxStatusRunning {
		return model.Sandbox{}, model.TTYSession{}, nil, fmt.Errorf("sandbox %s is not running", sandbox.ID)
	}
	if err := s.enforceLifecyclePolicy(ctx, sandbox, "tty"); err != nil {
		return model.Sandbox{}, model.TTYSession{}, nil, err
	}
	handle, err := s.runtime.AttachTTY(ctx, sandbox, req)
	if err != nil {
		return model.Sandbox{}, model.TTYSession{}, nil, err
	}
	session := model.TTYSession{
		ID:         newID("tty-"),
		SandboxID:  sandbox.ID,
		TenantID:   tenantID,
		Command:    strings.Join(req.Command, " "),
		Connected:  true,
		LastResize: fmt.Sprintf("%dx%d", req.Cols, req.Rows),
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.store.CreateTTYSession(ctx, session); err != nil {
		_ = handle.Close()
		return model.Sandbox{}, model.TTYSession{}, nil, err
	}
	_ = s.touchSandboxActivity(ctx, sandbox)
	s.recordAudit(ctx, tenantID, sandbox.ID, "sandbox.tty.attach", session.ID, "ok", auditDetail(
		auditKV("command", session.Command),
		auditKV("connected", session.Connected),
		auditKV("last_resize", session.LastResize),
	))
	return sandbox, session, handle, nil
}

func (s *Service) CloseTTYSession(ctx context.Context, tenantID, sessionID string) error {
	if err := s.store.CloseTTYSession(ctx, tenantID, sessionID); err != nil {
		return err
	}
	s.recordAudit(ctx, tenantID, "", "sandbox.tty.detach", sessionID, "ok", auditKV("session_id", sessionID))
	return nil
}

func (s *Service) UpdateTTYResize(ctx context.Context, tenantID, sessionID string, rows, cols int) error {
	return s.store.UpdateTTYResize(ctx, tenantID, sessionID, fmt.Sprintf("%dx%d", cols, rows))
}

func (s *Service) ReadFile(ctx context.Context, tenantID, sandboxID, path, encoding string) (model.FileReadResponse, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	relativePath, err := cleanWorkspaceRelativePath(path)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	if strings.EqualFold(strings.TrimSpace(encoding), "base64") {
		data, err := s.readWorkspaceBytes(ctx, sandbox, relativePath)
		if err != nil {
			return model.FileReadResponse{}, err
		}
		_ = s.touchSandboxActivity(ctx, sandbox)
		return model.FileReadResponse{Path: relativePath, ContentBase64: base64.StdEncoding.EncodeToString(data), Size: int64(len(data)), Encoding: "base64"}, nil
	}
	if runtime, ok := s.runtime.(workspaceFileRuntime); ok {
		file, err := runtime.ReadWorkspaceFile(ctx, sandbox, relativePath)
		if err == nil {
			_ = s.touchSandboxActivity(ctx, sandbox)
		}
		return file, err
	}
	target, err := resolveWorkspacePath(sandbox.WorkspaceRoot, relativePath)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	_ = s.touchSandboxActivity(ctx, sandbox)
	return model.FileReadResponse{Path: relativePath, Content: string(data), Size: int64(len(data)), Encoding: "utf-8"}, nil
}

func (s *Service) WriteFile(ctx context.Context, tenantID, sandboxID, path string, content string) error {
	return s.WriteFileBytes(ctx, tenantID, sandboxID, path, []byte(content))
}

func (s *Service) WriteFileBytes(ctx context.Context, tenantID, sandboxID, path string, content []byte) error {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return err
	}
	relativePath, err := cleanWorkspaceRelativePath(path)
	if err != nil {
		return err
	}
	if runtime, ok := s.runtime.(workspaceBinaryFileRuntime); ok {
		if err := runtime.WriteWorkspaceFileBytes(ctx, sandbox, relativePath, content); err != nil {
			return err
		}
	} else if runtime, ok := s.runtime.(workspaceFileRuntime); ok {
		if !utf8.Valid(content) {
			return fmt.Errorf("binary file write is unsupported for runtime %q", sandbox.RuntimeBackend)
		}
		if err := runtime.WriteWorkspaceFile(ctx, sandbox, relativePath, string(content)); err != nil {
			return err
		}
	} else {
		target, err := resolveWorkspacePath(sandbox.WorkspaceRoot, relativePath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return err
		}
	}
	s.recordAudit(ctx, tenantID, sandboxID, "file.write", relativePath, "ok", "file written")
	_ = s.touchSandboxActivity(ctx, sandbox)
	return s.refreshStorage(ctx, sandbox)
}

func (s *Service) readWorkspaceBytes(ctx context.Context, sandbox model.Sandbox, relativePath string) ([]byte, error) {
	if runtime, ok := s.runtime.(workspaceBinaryFileRuntime); ok {
		return runtime.ReadWorkspaceFileBytes(ctx, sandbox, relativePath)
	}
	if runtime, ok := s.runtime.(workspaceFileRuntime); ok {
		file, err := runtime.ReadWorkspaceFile(ctx, sandbox, relativePath)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(file.Encoding, "base64") && strings.TrimSpace(file.ContentBase64) != "" {
			return base64.StdEncoding.DecodeString(file.ContentBase64)
		}
		return []byte(file.Content), nil
	}
	target, err := resolveWorkspacePath(sandbox.WorkspaceRoot, relativePath)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(target)
}

func (s *Service) DeleteFile(ctx context.Context, tenantID, sandboxID, path string) error {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return err
	}
	relativePath, err := cleanWorkspaceRelativePath(path)
	if err != nil {
		return err
	}
	if runtime, ok := s.runtime.(workspaceFileRuntime); ok {
		if err := runtime.DeleteWorkspacePath(ctx, sandbox, relativePath); err != nil {
			return err
		}
	} else {
		target, err := resolveWorkspacePath(sandbox.WorkspaceRoot, relativePath)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	s.recordAudit(ctx, tenantID, sandboxID, "file.delete", relativePath, "ok", "path deleted")
	_ = s.touchSandboxActivity(ctx, sandbox)
	return s.refreshStorage(ctx, sandbox)
}

func (s *Service) Mkdir(ctx context.Context, tenantID, sandboxID, path string) error {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return err
	}
	relativePath, err := cleanWorkspaceRelativePath(path)
	if err != nil {
		return err
	}
	if runtime, ok := s.runtime.(workspaceFileRuntime); ok {
		if err := runtime.MkdirWorkspace(ctx, sandbox, relativePath); err != nil {
			return err
		}
	} else {
		target, err := resolveWorkspacePath(sandbox.WorkspaceRoot, relativePath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
	}
	s.recordAudit(ctx, tenantID, sandboxID, "file.mkdir", relativePath, "ok", "directory created")
	_ = s.touchSandboxActivity(ctx, sandbox)
	return s.refreshStorage(ctx, sandbox)
}

func (s *Service) CreateTunnel(ctx context.Context, tenantID, sandboxID string, req model.CreateTunnelRequest) (model.Tunnel, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Tunnel{}, err
	}
	if !sandbox.AllowTunnels {
		s.recordAudit(ctx, tenantID, sandboxID, "tunnel.create", sandboxID, "denied", auditKV("reason", "sandbox_tunnel_policy_denied"))
		return model.Tunnel{}, fmt.Errorf("sandbox does not allow tunnels")
	}
	usage, err := s.store.TenantUsage(ctx, tenantID)
	if err != nil {
		return model.Tunnel{}, err
	}
	quota, err := s.store.GetQuota(ctx, tenantID)
	if err == nil && !quota.AllowTunnels {
		s.recordAudit(ctx, tenantID, sandboxID, "tunnel.create", sandboxID, "denied", auditKV("reason", "tenant_tunnel_policy_denied"))
		return model.Tunnel{}, fmt.Errorf("tenant tunnel policy denied")
	}
	if err == nil && usage.ActiveTunnels >= quota.MaxTunnels {
		s.recordAudit(ctx, tenantID, sandboxID, "tunnel.create", sandboxID, "denied", auditKV("reason", "tunnel_quota_exceeded"))
		return model.Tunnel{}, fmt.Errorf("tunnel quota exceeded")
	}
	id := newID("tun-")
	if req.TargetPort < 1 || req.TargetPort > 65535 {
		return model.Tunnel{}, fmt.Errorf("target_port must be between 1 and 65535")
	}
	if req.Protocol == "" {
		req.Protocol = model.TunnelProtocolHTTP
	}
	if req.Protocol != model.TunnelProtocolHTTP {
		return model.Tunnel{}, fmt.Errorf("unsupported tunnel protocol %q", req.Protocol)
	}
	if req.AuthMode == "" && err == nil {
		req.AuthMode = quota.DefaultTunnelAuthMode
	}
	if req.AuthMode == "" {
		req.AuthMode = "token"
	}
	if req.AuthMode != "token" && req.AuthMode != "none" {
		return model.Tunnel{}, fmt.Errorf("unsupported auth_mode %q", req.AuthMode)
	}
	if req.Visibility == "" && err == nil {
		req.Visibility = quota.DefaultTunnelVisibility
	}
	if req.Visibility == "" {
		req.Visibility = "private"
	}
	if req.Visibility != "private" && req.Visibility != "public" {
		return model.Tunnel{}, fmt.Errorf("unsupported visibility %q", req.Visibility)
	}
	if err := s.enforceTunnelPolicy(ctx, sandbox, req); err != nil {
		return model.Tunnel{}, err
	}
	policy := buildNetworkPolicy(sandbox.NetworkMode, sandbox.AllowTunnels)
	accessToken := ""
	tunnel := model.Tunnel{
		ID:         id,
		SandboxID:  sandbox.ID,
		TenantID:   tenantID,
		TargetPort: req.TargetPort,
		Protocol:   req.Protocol,
		AuthMode:   req.AuthMode,
		Visibility: req.Visibility,
		Endpoint:   strings.TrimRight(s.cfg.OperatorHost, "/") + "/v1/tunnels/" + id + "/proxy",
		CreatedAt:  time.Now().UTC(),
	}
	if tunnel.AuthMode == "token" {
		accessToken = newID("ttok-")
		tunnel.AccessToken = accessToken
		tunnel.AuthSecretHash = config.HashToken(accessToken)
	}
	if err := s.store.CreateTunnel(ctx, tunnel); err != nil {
		return model.Tunnel{}, err
	}
	_ = s.touchSandboxActivity(ctx, sandbox)
	s.recordAudit(ctx, tenantID, sandboxID, "tunnel.create", tunnel.ID, "ok", auditDetail(tunnelAuditDetail(tunnel), networkPolicyAuditDetail(policy)))
	return tunnel, nil
}

func (s *Service) ListTunnels(ctx context.Context, tenantID, sandboxID string) ([]model.Tunnel, error) {
	return s.store.ListTunnels(ctx, tenantID, sandboxID)
}

func (s *Service) RevokeTunnel(ctx context.Context, tenantID, tunnelID string) error {
	tunnel, err := s.store.GetTunnel(ctx, tenantID, tunnelID)
	if err != nil {
		return err
	}
	if err := s.store.RevokeTunnel(ctx, tenantID, tunnelID); err != nil {
		return err
	}
	s.recordAudit(ctx, tenantID, tunnel.SandboxID, "tunnel.revoke", tunnelID, "ok", tunnelAuditDetail(tunnel))
	return nil
}

func (s *Service) GetTunnel(ctx context.Context, tenantID, tunnelID string) (model.Tunnel, model.Sandbox, error) {
	tunnel, err := s.store.GetTunnel(ctx, tenantID, tunnelID)
	if err != nil {
		return model.Tunnel{}, model.Sandbox{}, err
	}
	sandbox, err := s.store.GetSandbox(ctx, tenantID, tunnel.SandboxID)
	if err != nil {
		return model.Tunnel{}, model.Sandbox{}, err
	}
	return tunnel, sandbox, nil
}

func (s *Service) GetTunnelForProxy(ctx context.Context, tunnelID string) (model.Tunnel, model.Sandbox, error) {
	tunnel, err := s.store.GetTunnelByID(ctx, tunnelID)
	if err != nil {
		return model.Tunnel{}, model.Sandbox{}, err
	}
	sandbox, err := s.store.GetSandbox(ctx, tunnel.TenantID, tunnel.SandboxID)
	if err != nil {
		return model.Tunnel{}, model.Sandbox{}, err
	}
	return tunnel, sandbox, nil
}

func (s *Service) CreateSnapshot(ctx context.Context, tenantID, sandboxID string, req model.CreateSnapshotRequest) (model.Snapshot, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Snapshot{}, err
	}
	if err := s.enforceAdmission(ctx, tenantID, sandbox.ID, "snapshot_create", admissionDelta{tenantHeavy: 1}); err != nil {
		return model.Snapshot{}, err
	}
	if sandbox.RuntimeBackend == "qemu" && sandbox.Status != model.SandboxStatusStopped {
		return model.Snapshot{}, fmt.Errorf("qemu snapshots require the sandbox to be stopped")
	}
	snapshot := model.Snapshot{
		ID:                       newID("snap-"),
		SandboxID:                sandbox.ID,
		TenantID:                 tenantID,
		Name:                     req.Name,
		Status:                   model.SnapshotStatusCreating,
		RuntimeBackend:           sandbox.RuntimeBackend,
		Profile:                  sandbox.Profile,
		ImageContractVersion:     sandbox.ImageContractVersion,
		ControlProtocolVersion:   sandbox.ControlProtocolVersion,
		WorkspaceContractVersion: sandbox.WorkspaceContractVersion,
		CreatedAt:                time.Now().UTC(),
	}
	if snapshot.Name == "" {
		snapshot.Name = snapshot.ID
	}
	if err := s.store.CreateSnapshot(ctx, snapshot); err != nil {
		return model.Snapshot{}, err
	}
	snapshotDir := filepath.Join(s.cfg.SnapshotRoot, sandbox.ID, snapshot.ID)
	stage := "persist"
	failSnapshot := func(cause error) (model.Snapshot, error) {
		snapshot.Status = model.SnapshotStatusError
		_ = os.RemoveAll(snapshotDir)
		_ = s.store.UpdateSnapshot(ctx, snapshot)
		s.recordAudit(ctx, tenantID, sandboxID, "snapshot.create", snapshot.ID, "error", auditDetail(
			auditKV("stage", stage),
			auditKV("name", snapshot.Name),
		), "error", cause)
		return snapshot, cause
	}
	stage = "runtime_create"
	info, err := s.runtime.CreateSnapshot(ctx, sandbox, snapshot.ID)
	if err != nil {
		return failSnapshot(err)
	}
	stage = "mkdir_snapshot_dir"
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return failSnapshot(err)
	}
	if info.ImageRef != "" {
		if looksLikeFilesystemPath(info.ImageRef) {
			stage = "validate_rootfs_artifact"
			if !isReadableFile(info.ImageRef) {
				return failSnapshot(fmt.Errorf("snapshot image artifact is not readable: %s", info.ImageRef))
			}
			targetImage := filepath.Join(snapshotDir, "rootfs.img")
			if info.ImageRef != targetImage {
				stage = "copy_rootfs_artifact"
				if err := copyFile(targetImage, info.ImageRef); err != nil {
					return failSnapshot(err)
				}
			}
			snapshot.ImageRef = targetImage
			if info.ImageRef != targetImage {
				_ = os.Remove(info.ImageRef)
			}
		} else {
			snapshot.ImageRef = info.ImageRef
		}
	} else {
		snapshot.ImageRef = info.ImageRef
	}
	if info.WorkspaceTar != "" {
		targetTar := filepath.Join(snapshotDir, "workspace.tar.gz")
		if info.WorkspaceTar != targetTar {
			stage = "copy_workspace_artifact"
			if err := copyFile(targetTar, info.WorkspaceTar); err != nil {
				return failSnapshot(err)
			}
		}
		snapshot.WorkspaceTar = targetTar
		if info.WorkspaceTar != targetTar {
			_ = os.Remove(info.WorkspaceTar)
		}
	} else {
		snapshot.WorkspaceTar = info.WorkspaceTar
	}
	snapshot.Status = model.SnapshotStatusReady
	completed := time.Now().UTC()
	snapshot.CompletedAt = &completed
	if s.cfg.OptionalSnapshotExport != "" {
		stage = "export_bundle"
		exportLocation, err := s.exportSnapshotBundle(ctx, sandbox, snapshot)
		if err != nil {
			return failSnapshot(err)
		}
		snapshot.ExportLocation = exportLocation
	}
	stage = "persist_ready"
	if err := s.store.UpdateSnapshot(ctx, snapshot); err != nil {
		return failSnapshot(err)
	}
	stage = "refresh_storage"
	if err := s.refreshStorage(ctx, sandbox); err != nil {
		return failSnapshot(err)
	}
	s.recordAudit(ctx, tenantID, sandboxID, "snapshot.create", snapshot.ID, "ok", snapshotAuditDetail(snapshot))
	return snapshot, nil
}

func (s *Service) ListSnapshots(ctx context.Context, tenantID, sandboxID string) ([]model.Snapshot, error) {
	if _, err := s.store.GetSandbox(ctx, tenantID, sandboxID); err != nil {
		return nil, err
	}
	return s.store.ListSnapshots(ctx, tenantID, sandboxID)
}

func (s *Service) GetSnapshot(ctx context.Context, tenantID, snapshotID string) (model.Snapshot, error) {
	return s.store.GetSnapshot(ctx, tenantID, snapshotID)
}

func (s *Service) RestoreSnapshot(ctx context.Context, tenantID, snapshotID string, req model.RestoreSnapshotRequest) (model.Sandbox, error) {
	snapshot, err := s.store.GetSnapshot(ctx, tenantID, snapshotID)
	if err != nil {
		return model.Sandbox{}, err
	}
	sandbox, err := s.store.GetSandbox(ctx, tenantID, req.TargetSandboxID)
	if err != nil {
		return model.Sandbox{}, err
	}
	if err := s.enforceAdmission(ctx, tenantID, sandbox.ID, "snapshot_restore", admissionDelta{tenantHeavy: 1}); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.validateSnapshotCompatibility(snapshot, sandbox); err != nil {
		s.recordAudit(ctx, tenantID, sandbox.ID, "snapshot.restore", snapshot.ID, "denied", auditDetail(
			auditKV("stage", "compatibility_check"),
			auditKV("target_sandbox_id", sandbox.ID),
			auditKV("forced_stop", false),
		), "error", err)
		return model.Sandbox{}, err
	}
	snapshot, err = s.ensureSnapshotArtifacts(ctx, sandbox, snapshot)
	if err != nil {
		s.recordAudit(ctx, tenantID, sandbox.ID, "snapshot.restore", snapshot.ID, "error", auditDetail(
			auditKV("stage", "ensure_artifacts"),
			auditKV("target_sandbox_id", sandbox.ID),
			auditKV("forced_stop", false),
		), "error", err)
		return model.Sandbox{}, err
	}
	forcedStop := false
	if sandbox.Status == model.SandboxStatusRunning || sandbox.Status == model.SandboxStatusSuspended {
		forcedStop = true
		if _, err := s.StopSandbox(ctx, tenantID, sandbox.ID, true); err != nil {
			s.recordAudit(ctx, tenantID, sandbox.ID, "snapshot.restore", snapshot.ID, "error", auditDetail(
				auditKV("stage", "stop_target"),
				auditKV("target_sandbox_id", sandbox.ID),
				auditKV("forced_stop", true),
			), "error", err)
			return model.Sandbox{}, err
		}
		sandbox, _ = s.store.GetSandbox(ctx, tenantID, sandbox.ID)
	}
	state, err := s.runtime.RestoreSnapshot(ctx, sandbox, snapshot)
	if err != nil {
		s.recordAudit(ctx, tenantID, sandbox.ID, "snapshot.restore", snapshot.ID, "error", auditDetail(
			auditKV("stage", "runtime_restore"),
			auditKV("target_sandbox_id", sandbox.ID),
			auditKV("forced_stop", forcedStop),
		), "error", err)
		return model.Sandbox{}, err
	}
	sandbox.Status = model.SandboxStatusStopped
	sandbox.RuntimeStatus = string(state.Status)
	sandbox.Profile = snapshot.Profile
	sandbox.ImageContractVersion = snapshot.ImageContractVersion
	sandbox.ControlProtocolVersion = snapshot.ControlProtocolVersion
	sandbox.WorkspaceContractVersion = snapshot.WorkspaceContractVersion
	if sandbox.RuntimeBackend != "qemu" {
		sandbox.BaseImageRef = snapshot.ImageRef
	}
	sandbox.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.store.UpdateRuntimeState(ctx, sandbox.ID, state); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.refreshStorage(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	s.recordAudit(ctx, tenantID, sandbox.ID, "snapshot.restore", snapshot.ID, "ok", auditDetail(
		auditKV("target_sandbox_id", sandbox.ID),
		auditKV("forced_stop", forcedStop),
		snapshotAuditDetail(snapshot),
	))
	return s.store.GetSandbox(ctx, tenantID, sandbox.ID)
}

func (s *Service) Reconcile(ctx context.Context) error {
	var reconcileErr error
	if err := s.reconcileOrphanedExecutions(ctx); err != nil {
		return err
	}
	if err := s.reconcileIncompleteSnapshots(ctx); err != nil {
		return err
	}
	sandboxes, err := s.store.ListNonDeletedSandboxes(ctx)
	if err != nil {
		return err
	}
	for _, sandbox := range sandboxes {
		previousStatus := sandbox.Status
		previousRuntimeStatus := sandbox.RuntimeStatus
		previousRuntimeID := sandbox.RuntimeID
		previousRuntimeError := sandbox.LastRuntimeError
		state, err := s.runtime.Inspect(ctx, sandbox)
		if err != nil {
			sandbox.Status = model.SandboxStatusDegraded
			sandbox.RuntimeStatus = string(model.SandboxStatusDegraded)
			sandbox.LastRuntimeError = err.Error()
			sandbox.UpdatedAt = time.Now().UTC()
			if updateErr := s.store.UpdateSandboxState(ctx, sandbox); updateErr != nil {
				reconcileErr = errors.Join(reconcileErr, updateErr)
			}
			s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "sandbox.reconcile", sandbox.ID, "error", auditDetail(
				auditKV("previous_status", previousStatus),
				auditKV("result_status", sandbox.Status),
				auditKV("reason", "inspect_failed"),
			), "error", err)
			continue
		}
		switch {
		case state.Status == model.SandboxStatusBooting:
			sandbox.Status = model.SandboxStatusBooting
		case state.Status == model.SandboxStatusDegraded:
			sandbox.Status = model.SandboxStatusDegraded
		case state.Status == model.SandboxStatusRunning:
			sandbox.Status = model.SandboxStatusRunning
		case state.Status == model.SandboxStatusStopped:
			sandbox.Status = model.SandboxStatusStopped
		case state.Status == model.SandboxStatusSuspended:
			sandbox.Status = model.SandboxStatusSuspended
		default:
			sandbox.Status = state.Status
		}
		sandbox.RuntimeStatus = string(state.Status)
		sandbox.RuntimeID = state.RuntimeID
		sandbox.LastRuntimeError = state.Error
		sandbox.UpdatedAt = time.Now().UTC()
		if updateErr := s.store.UpdateSandboxState(ctx, sandbox); updateErr != nil {
			reconcileErr = errors.Join(reconcileErr, updateErr)
		}
		if updateErr := s.store.UpdateRuntimeState(ctx, sandbox.ID, state); updateErr != nil {
			reconcileErr = errors.Join(reconcileErr, updateErr)
		}
		stateChanged := sandbox.Status != previousStatus || sandbox.RuntimeStatus != previousRuntimeStatus || sandbox.RuntimeID != previousRuntimeID || sandbox.LastRuntimeError != previousRuntimeError
		if stateChanged {
			s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "sandbox.reconcile", sandbox.ID, "ok", auditDetail(
				auditKV("previous_status", previousStatus),
				auditKV("result_status", sandbox.Status),
				auditKV("runtime_status", sandbox.RuntimeStatus),
			))
		}
		if updateErr := s.refreshStorageIfStale(ctx, sandbox, stateChanged); updateErr != nil {
			reconcileErr = errors.Join(reconcileErr, updateErr)
		}
	}
	return reconcileErr
}

func (s *Service) reconcileOrphanedExecutions(ctx context.Context) error {
	executions, err := s.store.ListRunningExecutions(ctx)
	if err != nil {
		return err
	}
	for _, execution := range executions {
		now := time.Now().UTC()
		exitCode := 1
		durationMS := now.Sub(execution.StartedAt).Milliseconds()
		execution.Status = model.ExecutionStatusCanceled
		execution.ExitCode = &exitCode
		execution.StderrPreview = "control plane restarted during execution"
		execution.CompletedAt = &now
		execution.DurationMS = &durationMS
		if err := s.store.UpdateExecution(ctx, execution); err != nil {
			return err
		}
		s.recordAudit(ctx, execution.TenantID, execution.SandboxID, "sandbox.exec.reconcile", execution.ID, "canceled", executionAuditDetail(execution))
	}
	return nil
}

func (s *Service) reconcileIncompleteSnapshots(ctx context.Context) error {
	snapshots, err := s.store.ListSnapshotsByStatus(ctx, model.SnapshotStatusCreating)
	if err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		snapshot.Status = model.SnapshotStatusError
		if err := s.store.UpdateSnapshot(ctx, snapshot); err != nil {
			return err
		}
		s.recordAudit(ctx, snapshot.TenantID, snapshot.SandboxID, "snapshot.reconcile", snapshot.ID, "error", "control plane restarted during snapshot creation")
	}
	return nil
}

func (s *Service) touchSandboxActivity(ctx context.Context, sandbox model.Sandbox) error {
	now := time.Now().UTC()
	sandbox.LastActiveAt = now
	sandbox.UpdatedAt = now
	return s.store.UpdateSandboxState(ctx, sandbox)
}

func (s *Service) refreshStorage(ctx context.Context, sandbox model.Sandbox) error {
	if runtime, ok := s.runtime.(storageMeasurer); ok {
		usage, err := runtime.MeasureStorage(ctx, sandbox)
		if err != nil {
			return err
		}
		snapshotExportBytes, snapshotExportEntries, _ := dirUsage(filepath.Join(s.cfg.SnapshotRoot, sandbox.ID))
		usage.SnapshotBytes += snapshotExportBytes
		usage.SnapshotEntries += snapshotExportEntries
		if err := s.auditStoragePressure(ctx, sandbox, usage); err != nil {
			return err
		}
		return s.store.UpdateStorageUsage(ctx, sandbox.ID, usage.RootfsBytes, usage.WorkspaceBytes, usage.CacheBytes, usage.SnapshotBytes, usage.RootfsEntries, usage.WorkspaceEntries, usage.CacheEntries, usage.SnapshotEntries)
	}
	rootfsBytes, rootfsEntries, _ := dirUsage(sandbox.StorageRoot)
	workspaceBytes, workspaceEntries, _ := dirUsage(sandbox.WorkspaceRoot)
	cacheBytes, cacheEntries, _ := dirUsage(sandbox.CacheRoot)
	scratchBytes, scratchEntries, _ := dirUsage(scratchRootFromStorageRoot(sandbox.StorageRoot))
	snapshotBytes, snapshotEntries, _ := dirUsage(filepath.Join(s.cfg.SnapshotRoot, sandbox.ID))
	usage := model.StorageUsage{
		RootfsBytes:      rootfsBytes,
		WorkspaceBytes:   workspaceBytes,
		CacheBytes:       cacheBytes + scratchBytes,
		SnapshotBytes:    snapshotBytes,
		RootfsEntries:    rootfsEntries,
		WorkspaceEntries: workspaceEntries,
		CacheEntries:     cacheEntries + scratchEntries,
		SnapshotEntries:  snapshotEntries,
	}
	if err := s.auditStoragePressure(ctx, sandbox, usage); err != nil {
		return err
	}
	return s.store.UpdateStorageUsage(ctx, sandbox.ID, usage.RootfsBytes, usage.WorkspaceBytes, usage.CacheBytes, usage.SnapshotBytes, usage.RootfsEntries, usage.WorkspaceEntries, usage.CacheEntries, usage.SnapshotEntries)
}

func (s *Service) refreshStorageIfStale(ctx context.Context, sandbox model.Sandbox, force bool) error {
	if !force {
		updatedAt, err := s.store.StorageUsageUpdatedAt(ctx, sandbox.ID)
		switch {
		case err == nil && time.Since(updatedAt) < s.reconcileStorageRefreshInterval():
			return nil
		case err != nil && !errors.Is(err, repository.ErrNotFound):
			return err
		}
	}
	return s.refreshStorage(ctx, sandbox)
}

func (s *Service) reconcileStorageRefreshInterval() time.Duration {
	if s.cfg.CleanupInterval > 0 {
		return s.cfg.CleanupInterval
	}
	return defaultReconcileStorageRefreshInterval
}

func (s *Service) applyCreateDefaults(req model.CreateSandboxRequest) model.CreateSandboxRequest {
	if req.BaseImageRef == "" {
		if s.cfg.RuntimeBackend == "qemu" {
			req.BaseImageRef = s.cfg.QEMUBaseImagePath
		} else {
			req.BaseImageRef = s.cfg.BaseImageRef
		}
	}
	if req.Profile == "" && s.cfg.RuntimeBackend == "qemu" {
		req.Profile = model.GuestProfileCore
	}
	req.Features = model.NormalizeFeatures(req.Features)
	req.Capabilities = model.NormalizeCapabilities(req.Capabilities)
	if req.CPULimit == 0 {
		req.CPULimit = s.cfg.DefaultCPULimit
	}
	if req.MemoryLimitMB == 0 {
		req.MemoryLimitMB = s.cfg.DefaultMemoryLimitMB
	}
	if req.PIDsLimit == 0 {
		req.PIDsLimit = s.cfg.DefaultPIDsLimit
	}
	if req.DiskLimitMB == 0 {
		req.DiskLimitMB = s.cfg.DefaultDiskLimitMB
	}
	if req.NetworkMode == "" {
		req.NetworkMode = s.cfg.DefaultNetworkMode
	}
	if req.AllowTunnels == nil {
		value := s.cfg.DefaultAllowTunnels
		req.AllowTunnels = &value
	}
	return req
}

func validateCreate(req model.CreateSandboxRequest) error {
	if req.BaseImageRef == "" {
		return errors.New("base_image_ref is required")
	}
	if req.Profile != "" && !req.Profile.IsValid() {
		return fmt.Errorf("invalid guest profile %q", req.Profile)
	}
	if req.CPULimit <= 0 || req.MemoryLimitMB <= 0 || req.PIDsLimit <= 0 || req.DiskLimitMB <= 0 {
		return errors.New("cpu, memory, pids, and disk limits must be positive")
	}
	if req.NetworkMode != model.NetworkModeInternetEnabled && req.NetworkMode != model.NetworkModeInternetDisabled {
		return fmt.Errorf("invalid network mode %q", req.NetworkMode)
	}
	return nil
}

func (s *Service) validateRuntimeCreate(ctx context.Context, req model.CreateSandboxRequest) (model.CreateSandboxRequest, guestimage.Contract, error) {
	req.Capabilities = model.NormalizeCapabilities(req.Capabilities)
	if s.cfg.RuntimeBackend == "qemu" && req.CPULimit.MilliValue()%1000 != 0 {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("qemu runtime requires whole CPU cores until fractional throttling is implemented")
	}
	if s.cfg.RuntimeBackend == "docker" {
		metadata, err := dockerimage.ResolveWithDockerLabels(ctx, req.BaseImageRef)
		profile := req.Profile
		switch {
		case err == nil && profile == "":
			profile = metadata.Profile
		case err == nil && profile != metadata.Profile:
			return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("docker image profile %q does not match requested profile %q", metadata.Profile, profile)
		case err != nil && !errors.Is(err, dockerimage.ErrMetadataUnavailable):
			return model.CreateSandboxRequest{}, guestimage.Contract{}, err
		}
		if err != nil && profile == "" {
			return model.CreateSandboxRequest{}, guestimage.Contract{}, err
		}
		if !profile.IsValid() {
			return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("docker runtime requires a valid guest profile")
		}
		req.Profile = profile
		req.Features = model.NormalizeFeatures(req.Features)
		return req, guestimage.Contract{}, nil
	}
	if s.cfg.RuntimeBackend != "qemu" {
		return req, guestimage.Contract{}, nil
	}
	if len(req.Capabilities) > 0 {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("qemu runtime does not accept requested capability overrides")
	}
	resolved, err := s.resolveQEMUBaseImageRef(req.BaseImageRef)
	if err != nil {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, err
	}
	contract, err := guestimage.Load(resolved)
	if err != nil {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, err
	}
	if err := guestimage.Validate(resolved, contract); err != nil {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, err
	}
	profile := req.Profile
	if profile == "" {
		profile = contract.Profile
	}
	if !profile.IsValid() {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("qemu runtime requires a valid guest profile")
	}
	if profile != contract.Profile {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("guest image profile %q does not match requested profile %q", contract.Profile, profile)
	}
	if contract.Control.Mode == model.GuestControlModeSSHCompat && !s.cfg.QEMUAllowSSHCompat {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("ssh-compat guest images are blocked by policy until SANDBOX_QEMU_ALLOW_SSH_COMPAT=true")
	}
	if err := guestimage.RequestedFeaturesAllowed(contract, req.Features); err != nil {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, err
	}
	req.BaseImageRef = resolved
	req.Profile = profile
	req.Features = model.NormalizeFeatures(req.Features)
	return req, contract, nil
}

func (s *Service) resolvedCapabilities(req model.CreateSandboxRequest, contract guestimage.Contract) []string {
	if s.cfg.RuntimeBackend == "qemu" {
		return append([]string(nil), contract.Capabilities...)
	}
	return append([]string(nil), model.NormalizeCapabilities(req.Capabilities)...)
}

func (s *Service) resolveQEMUBaseImageRef(value string) (string, error) {
	normalized := s.normalizeQEMUBaseImageRef(value)
	if normalized == "" {
		return "", fmt.Errorf("qemu runtime requires a guest image path")
	}
	if !isReadableFile(normalized) {
		return "", fmt.Errorf("qemu guest image path %q is not readable", normalized)
	}
	for _, allowed := range s.cfg.EffectiveQEMUAllowedBaseImagePaths() {
		if normalized == allowed {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("qemu guest image path %q is not allowed", normalized)
}

func (s *Service) normalizeQEMUBaseImageRef(value string) string {
	normalized := config.NormalizeQEMUBaseImagePath(value)
	if normalized == "" {
		return ""
	}
	if normalized == config.NormalizeQEMUBaseImagePath(s.cfg.BaseImageRef) {
		return config.NormalizeQEMUBaseImagePath(s.cfg.QEMUBaseImagePath)
	}
	return normalized
}

func (s *Service) checkQuota(ctx context.Context, tenantID string, quota model.TenantQuota, req model.CreateSandboxRequest) error {
	usage, err := s.store.TenantUsage(ctx, tenantID)
	if err != nil {
		return err
	}
	switch {
	case usage.Sandboxes >= quota.MaxSandboxes:
		return fmt.Errorf("sandbox quota exceeded")
	case usage.RequestedCPU+req.CPULimit > quota.MaxCPUCores:
		return fmt.Errorf("cpu quota exceeded")
	case usage.RequestedMemory+req.MemoryLimitMB > quota.MaxMemoryMB:
		return fmt.Errorf("memory quota exceeded")
	case usage.RequestedStorage+req.DiskLimitMB > quota.MaxStorageMB:
		return fmt.Errorf("storage quota exceeded")
	case req.AllowTunnels != nil && *req.AllowTunnels && !quota.AllowTunnels:
		return fmt.Errorf("tenant tunnel policy denied")
	}
	return nil
}

func (s *Service) checkRunningQuota(ctx context.Context, tenantID string, quota model.TenantQuota) error {
	usage, err := s.store.TenantUsage(ctx, tenantID)
	if err != nil {
		return err
	}
	if usage.RunningSandboxes >= quota.MaxRunningSandboxes {
		return fmt.Errorf("running sandbox quota exceeded")
	}
	return nil
}

func (s *Service) transitionSandbox(ctx context.Context, tenantID, sandboxID, auditAction string, transitional model.SandboxStatus, action func(model.Sandbox) (model.RuntimeState, model.SandboxStatus, error), finalStatus model.SandboxStatus) (model.Sandbox, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Sandbox{}, err
	}
	sandbox.Status = transitional
	sandbox.RuntimeStatus = string(transitional)
	sandbox.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	state, nextStatus, err := action(sandbox)
	if err != nil {
		sandbox.Status = model.SandboxStatusError
		sandbox.RuntimeStatus = string(model.SandboxStatusError)
		sandbox.LastRuntimeError = err.Error()
		sandbox.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdateSandboxState(ctx, sandbox)
		s.recordAudit(ctx, tenantID, sandbox.ID, auditAction, sandbox.ID, "error", auditDetail(
			auditKV("transitional_status", transitional),
			auditKV("requested_status", finalStatus),
		), "error", err)
		return model.Sandbox{}, err
	}
	sandbox.Status = nextStatus
	sandbox.RuntimeStatus = string(state.Status)
	sandbox.UpdatedAt = time.Now().UTC()
	sandbox.LastActiveAt = sandbox.UpdatedAt
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.store.UpdateRuntimeState(ctx, sandbox.ID, state); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.refreshStorage(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	s.recordAudit(ctx, tenantID, sandbox.ID, auditAction, sandbox.ID, "ok", auditDetail(
		auditKV("transitional_status", transitional),
		auditKV("result_status", finalStatus),
	))
	return s.store.GetSandbox(ctx, tenantID, sandbox.ID)
}

func (s *Service) reserveSandboxCreate(ctx context.Context, tenantID string, sandbox model.Sandbox, req model.CreateSandboxRequest) error {
	createDelta := admissionDelta{nodeSandboxes: 1, tenantHeavy: 1}
	if req.Start {
		createDelta.nodeRunning = 1
		createDelta.runningCPU = req.CPULimit
		createDelta.runningMemory = req.MemoryLimitMB
		createDelta.tenantStarts = 1
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if err := s.enforceAdmission(ctx, tenantID, sandbox.ID, "create", createDelta); err != nil {
		return err
	}
	return s.store.CreateSandbox(ctx, sandbox)
}

func (s *Service) reserveLifecycleTransition(ctx context.Context, tenantID, sandboxID, action string, transitional model.SandboxStatus, delta admissionDelta) (model.Sandbox, error) {
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Sandbox{}, err
	}
	if delta.runningCPU == 0 {
		delta.runningCPU = sandbox.CPULimit
	}
	if delta.runningMemory == 0 {
		delta.runningMemory = sandbox.MemoryLimitMB
	}
	if err := s.enforceLifecyclePolicy(ctx, sandbox, action); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.enforceAdmission(ctx, tenantID, sandbox.ID, action, delta); err != nil {
		return model.Sandbox{}, err
	}
	sandbox.Status = transitional
	sandbox.RuntimeStatus = string(transitional)
	sandbox.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	return sandbox, nil
}

func (s *Service) executeReservedTransition(ctx context.Context, tenantID string, sandbox model.Sandbox, auditAction string, transitional, finalStatus model.SandboxStatus, action func(model.Sandbox) (model.RuntimeState, error)) (model.Sandbox, error) {
	state, err := action(sandbox)
	if err != nil {
		sandbox.Status = model.SandboxStatusError
		sandbox.RuntimeStatus = string(model.SandboxStatusError)
		sandbox.LastRuntimeError = err.Error()
		sandbox.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdateSandboxState(ctx, sandbox)
		s.recordAudit(ctx, tenantID, sandbox.ID, auditAction, sandbox.ID, "error", auditDetail(
			auditKV("transitional_status", transitional),
			auditKV("requested_status", finalStatus),
		), "error", err)
		return model.Sandbox{}, err
	}
	sandbox.Status = finalStatus
	sandbox.RuntimeStatus = string(state.Status)
	sandbox.UpdatedAt = time.Now().UTC()
	sandbox.LastActiveAt = sandbox.UpdatedAt
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.store.UpdateRuntimeState(ctx, sandbox.ID, state); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.refreshStorage(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	s.recordAudit(ctx, tenantID, sandbox.ID, auditAction, sandbox.ID, "ok", auditDetail(
		auditKV("transitional_status", transitional),
		auditKV("result_status", finalStatus),
	))
	return s.store.GetSandbox(ctx, tenantID, sandbox.ID)
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func (s *Service) snapshotExtractLimits() archiveutil.Limits {
	return archiveutil.Limits{
		MaxBytes:          s.cfg.SnapshotMaxBytes,
		MaxFiles:          s.cfg.SnapshotMaxFiles,
		MaxExpansionRatio: s.cfg.SnapshotMaxExpansionRatio,
	}
}

func (s *Service) validateSnapshotCompatibility(snapshot model.Snapshot, sandbox model.Sandbox) error {
	if snapshot.RuntimeBackend != "" && snapshot.RuntimeBackend != sandbox.RuntimeBackend {
		return fmt.Errorf("snapshot runtime %q does not match target sandbox runtime %q", snapshot.RuntimeBackend, sandbox.RuntimeBackend)
	}
	if snapshot.Profile != "" && sandbox.Profile != "" && snapshot.Profile != sandbox.Profile {
		return fmt.Errorf("snapshot profile %q does not match target sandbox profile %q", snapshot.Profile, sandbox.Profile)
	}
	if snapshot.WorkspaceContractVersion != "" && sandbox.WorkspaceContractVersion != "" && snapshot.WorkspaceContractVersion != sandbox.WorkspaceContractVersion {
		return fmt.Errorf("snapshot workspace contract version %q does not match target sandbox workspace contract version %q", snapshot.WorkspaceContractVersion, sandbox.WorkspaceContractVersion)
	}
	return nil
}

func (s *Service) auditStoragePressure(ctx context.Context, sandbox model.Sandbox, usage model.StorageUsage) error {
	if s.cfg.StorageWarningFileCount <= 0 {
		return nil
	}
	entries := usage.RootfsEntries + usage.WorkspaceEntries + usage.CacheEntries + usage.SnapshotEntries
	if entries <= int64(s.cfg.StorageWarningFileCount) {
		return nil
	}
	s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "sandbox.storage_pressure", sandbox.ID, "ok", auditDetail(
		auditKV("entries", entries),
		auditKV("warning_threshold", s.cfg.StorageWarningFileCount),
		auditKV("workspace_entries", usage.WorkspaceEntries),
		auditKV("cache_entries", usage.CacheEntries),
		auditKV("snapshot_entries", usage.SnapshotEntries),
	))
	return nil
}

func (s *Service) exportSnapshotBundle(ctx context.Context, sandbox model.Sandbox, snapshot model.Snapshot) (string, error) {
	snapshotDir := filepath.Join(s.cfg.SnapshotRoot, sandbox.ID, snapshot.ID)
	bundle, err := os.CreateTemp(filepath.Dir(snapshotDir), snapshot.ID+"-*.tar.gz")
	if err != nil {
		return "", err
	}
	bundlePath := bundle.Name()
	_ = bundle.Close()
	defer os.Remove(bundlePath)
	if err := writeTarGz(bundlePath, snapshotDir); err != nil {
		return "", err
	}
	return putSnapshotBundle(ctx, s.cfg.OptionalSnapshotExport, sandbox.ID, snapshot.ID, bundlePath)
}

func (s *Service) ensureSnapshotArtifacts(ctx context.Context, sandbox model.Sandbox, snapshot model.Snapshot) (model.Snapshot, error) {
	haveLocal := true
	for _, path := range []string{snapshot.ImageRef, snapshot.WorkspaceTar} {
		if path == "" {
			continue
		}
		if !looksLikeFilesystemPath(path) {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			haveLocal = false
			break
		}
	}
	if haveLocal {
		return snapshot, nil
	}
	if snapshot.ExportLocation == "" {
		return snapshot, nil
	}
	targetDir := filepath.Join(s.cfg.SnapshotRoot, sandbox.ID, snapshot.ID)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return model.Snapshot{}, err
	}
	tempBundle := filepath.Join(targetDir, "snapshot.restore.tar.gz")
	if err := fetchSnapshotBundle(ctx, snapshot.ExportLocation, tempBundle); err != nil {
		return model.Snapshot{}, err
	}
	defer os.Remove(tempBundle)
	if _, err := archiveutil.ExtractTarGz(tempBundle, targetDir, s.snapshotExtractLimits()); err != nil {
		return model.Snapshot{}, err
	}
	snapshot.ImageRef = rebindSnapshotArtifactPath(targetDir, snapshot.ImageRef)
	snapshot.WorkspaceTar = rebindSnapshotArtifactPath(targetDir, snapshot.WorkspaceTar)
	return snapshot, nil
}

func rebindSnapshotArtifactPath(targetDir, original string) string {
	if !looksLikeFilesystemPath(original) {
		return original
	}
	return filepath.Join(targetDir, filepath.Base(original))
}

func putSnapshotBundle(ctx context.Context, exportRoot, sandboxID, snapshotID, localBundle string) (string, error) {
	switch {
	case strings.HasPrefix(exportRoot, "s3://"):
		target := strings.TrimRight(exportRoot, "/") + "/" + sandboxID + "/" + snapshotID + ".tar.gz"
		cmd := exec.CommandContext(ctx, "aws", "s3", "cp", localBundle, target)
		if output, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("export snapshot bundle: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return target, nil
	case strings.HasPrefix(exportRoot, "file://"):
		exportRoot = strings.TrimPrefix(exportRoot, "file://")
		fallthrough
	default:
		target := filepath.Join(exportRoot, sandboxID, snapshotID+".tar.gz")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := copyFile(target, localBundle); err != nil {
			return "", err
		}
		return target, nil
	}
}

func fetchSnapshotBundle(ctx context.Context, exportLocation, localPath string) error {
	switch {
	case strings.HasPrefix(exportLocation, "s3://"):
		cmd := exec.CommandContext(ctx, "aws", "s3", "cp", exportLocation, localPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("restore snapshot bundle: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	case strings.HasPrefix(exportLocation, "file://"):
		exportLocation = strings.TrimPrefix(exportLocation, "file://")
		fallthrough
	default:
		return copyFile(localPath, exportLocation)
	}
}

func writeTarGz(destination, root string) error {
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer file.Close()
	gw := gzip.NewWriter(file)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()
	return filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tw, file)
		return err
	})
}

func extractTarGz(source, destination string) error {
	_, err := archiveutil.ExtractTarGz(source, destination, archiveutil.Limits{
		MaxBytes:          256 * 1024 * 1024,
		MaxFiles:          4096,
		MaxExpansionRatio: 32,
	})
	return err
}
