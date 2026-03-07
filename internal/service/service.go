package service

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
)

const previewLimit = 64 * 1024

type Service struct {
	cfg     config.Config
	store   *repository.Store
	runtime model.RuntimeManager
}

type workspaceFileRuntime interface {
	ReadWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string) (model.FileReadResponse, error)
	WriteWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string, content string) error
	DeleteWorkspacePath(ctx context.Context, sandbox model.Sandbox, relativePath string) error
	MkdirWorkspace(ctx context.Context, sandbox model.Sandbox, relativePath string) error
}

type storageMeasurer interface {
	MeasureStorage(ctx context.Context, sandbox model.Sandbox) (model.StorageUsage, error)
}

type TenantQuotaView struct {
	Quota model.TenantQuota      `json:"quota"`
	Usage repository.TenantUsage `json:"usage"`
}

func New(cfg config.Config, store *repository.Store, runtime model.RuntimeManager) *Service {
	return &Service{cfg: cfg, store: store, runtime: runtime}
}

func (s *Service) CreateSandbox(ctx context.Context, tenant model.Tenant, quota model.TenantQuota, req model.CreateSandboxRequest) (model.Sandbox, error) {
	req = s.applyCreateDefaults(req)
	if err := validateCreate(req); err != nil {
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
	storageRoot := filepath.Join(s.cfg.StorageRoot, id, "rootfs")
	workspaceRoot := filepath.Join(s.cfg.StorageRoot, id, "workspace")
	cacheRoot := filepath.Join(s.cfg.StorageRoot, id, "cache")
	for _, dir := range []string{storageRoot, workspaceRoot, cacheRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return model.Sandbox{}, err
		}
	}
	now := time.Now().UTC()
	sandbox := model.Sandbox{
		ID:             id,
		TenantID:       tenant.ID,
		Status:         model.SandboxStatusCreating,
		RuntimeBackend: s.cfg.RuntimeBackend,
		BaseImageRef:   req.BaseImageRef,
		CPULimit:       req.CPULimit,
		MemoryLimitMB:  req.MemoryLimitMB,
		PIDsLimit:      req.PIDsLimit,
		DiskLimitMB:    req.DiskLimitMB,
		NetworkMode:    req.NetworkMode,
		AllowTunnels:   *req.AllowTunnels,
		StorageRoot:    storageRoot,
		WorkspaceRoot:  workspaceRoot,
		CacheRoot:      cacheRoot,
		RuntimeID:      id,
		RuntimeStatus:  string(model.SandboxStatusCreating),
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActiveAt:   now,
	}
	if err := s.store.CreateSandbox(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	spec := model.SandboxSpec{
		SandboxID:     sandbox.ID,
		TenantID:      sandbox.TenantID,
		BaseImageRef:  sandbox.BaseImageRef,
		CPULimit:      sandbox.CPULimit,
		MemoryLimitMB: sandbox.MemoryLimitMB,
		PIDsLimit:     sandbox.PIDsLimit,
		DiskLimitMB:   sandbox.DiskLimitMB,
		NetworkMode:   sandbox.NetworkMode,
		AllowTunnels:  sandbox.AllowTunnels,
		StorageRoot:   sandbox.StorageRoot,
		WorkspaceRoot: sandbox.WorkspaceRoot,
		CacheRoot:     sandbox.CacheRoot,
	}
	state, err := s.runtime.Create(ctx, spec)
	if err != nil {
		sandbox.Status = model.SandboxStatusError
		sandbox.RuntimeStatus = string(model.SandboxStatusError)
		sandbox.LastRuntimeError = err.Error()
		sandbox.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdateSandboxState(ctx, sandbox)
		return sandbox, err
	}
	if req.Start {
		state, err = s.runtime.Start(ctx, sandbox)
		if err != nil {
			sandbox.Status = model.SandboxStatusError
			sandbox.RuntimeStatus = string(model.SandboxStatusError)
			sandbox.LastRuntimeError = err.Error()
			sandbox.UpdatedAt = time.Now().UTC()
			_ = s.store.UpdateSandboxState(ctx, sandbox)
			return sandbox, err
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
	s.recordAudit(ctx, tenant.ID, sandbox.ID, "sandbox.create", sandbox.ID, "ok", "sandbox created")
	return s.store.GetSandbox(ctx, tenant.ID, sandbox.ID)
}

func (s *Service) GetSandbox(ctx context.Context, tenantID, sandboxID string) (model.Sandbox, error) {
	return s.store.GetSandbox(ctx, tenantID, sandboxID)
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
	return TenantQuotaView{Quota: quota, Usage: usage}, nil
}

func (s *Service) StartSandbox(ctx context.Context, tenantID, sandboxID string, quota model.TenantQuota) (model.Sandbox, error) {
	if err := s.checkRunningQuota(ctx, tenantID, quota); err != nil {
		return model.Sandbox{}, err
	}
	return s.transitionSandbox(ctx, tenantID, sandboxID, model.SandboxStatusStarting, func(sandbox model.Sandbox) (model.RuntimeState, model.SandboxStatus, error) {
		state, err := s.runtime.Start(ctx, sandbox)
		return state, model.SandboxStatusRunning, err
	}, model.SandboxStatusRunning)
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
	s.recordAudit(ctx, tenantID, sandbox.ID, "sandbox.stop", sandbox.ID, "ok", "sandbox stopped")
	return s.store.GetSandbox(ctx, tenantID, sandbox.ID)
}

func (s *Service) SuspendSandbox(ctx context.Context, tenantID, sandboxID string) (model.Sandbox, error) {
	return s.transitionSandbox(ctx, tenantID, sandboxID, model.SandboxStatusSuspending, func(sandbox model.Sandbox) (model.RuntimeState, model.SandboxStatus, error) {
		state, err := s.runtime.Suspend(ctx, sandbox)
		return state, model.SandboxStatusSuspended, err
	}, model.SandboxStatusSuspended)
}

func (s *Service) ResumeSandbox(ctx context.Context, tenantID, sandboxID string, quota model.TenantQuota) (model.Sandbox, error) {
	if err := s.checkRunningQuota(ctx, tenantID, quota); err != nil {
		return model.Sandbox{}, err
	}
	return s.transitionSandbox(ctx, tenantID, sandboxID, model.SandboxStatusStarting, func(sandbox model.Sandbox) (model.RuntimeState, model.SandboxStatus, error) {
		state, err := s.runtime.Resume(ctx, sandbox)
		return state, model.SandboxStatusRunning, err
	}, model.SandboxStatusRunning)
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
	s.recordAudit(ctx, tenantID, sandbox.ID, "sandbox.delete", sandbox.ID, "ok", "sandbox deleted")
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
		now := time.Now().UTC()
		exitCode := 1
		durationMS := now.Sub(started).Milliseconds()
		execution.Status = model.ExecutionStatusFailed
		execution.ExitCode = &exitCode
		execution.StderrPreview = err.Error()
		execution.CompletedAt = &now
		execution.DurationMS = &durationMS
		_ = s.store.UpdateExecution(ctx, execution)
		return model.Execution{}, err
	}
	if req.Detached {
		now := time.Now().UTC()
		exitCode := 0
		durationMS := now.Sub(started).Milliseconds()
		execution.Status = model.ExecutionStatusSucceeded
		execution.ExitCode = &exitCode
		execution.CompletedAt = &now
		execution.DurationMS = &durationMS
		if err := s.store.UpdateExecution(ctx, execution); err != nil {
			return model.Execution{}, err
		}
		s.recordAudit(ctx, tenant.ID, sandbox.ID, "sandbox.exec.detached", execution.ID, "ok", execution.Command)
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
	if err := s.store.UpdateExecution(ctx, execution); err != nil {
		return model.Execution{}, err
	}
	s.recordAudit(ctx, tenant.ID, sandbox.ID, "sandbox.exec", execution.ID, string(execution.Status), execution.Command)
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
	return sandbox, session, handle, nil
}

func (s *Service) CloseTTYSession(ctx context.Context, tenantID, sessionID string) error {
	return s.store.CloseTTYSession(ctx, tenantID, sessionID)
}

func (s *Service) UpdateTTYResize(ctx context.Context, tenantID, sessionID string, rows, cols int) error {
	return s.store.UpdateTTYResize(ctx, tenantID, sessionID, fmt.Sprintf("%dx%d", cols, rows))
}

func (s *Service) ReadFile(ctx context.Context, tenantID, sandboxID, path string) (model.FileReadResponse, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	relativePath, err := cleanWorkspaceRelativePath(path)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	if runtime, ok := s.runtime.(workspaceFileRuntime); ok {
		return runtime.ReadWorkspaceFile(ctx, sandbox, relativePath)
	}
	target, err := resolveWorkspacePath(sandbox.WorkspaceRoot, relativePath)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	return model.FileReadResponse{Path: relativePath, Content: string(data), Size: int64(len(data)), Encoding: "utf-8"}, nil
}

func (s *Service) WriteFile(ctx context.Context, tenantID, sandboxID, path string, content string) error {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return err
	}
	relativePath, err := cleanWorkspaceRelativePath(path)
	if err != nil {
		return err
	}
	if runtime, ok := s.runtime.(workspaceFileRuntime); ok {
		if err := runtime.WriteWorkspaceFile(ctx, sandbox, relativePath, content); err != nil {
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
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return err
		}
	}
	s.recordAudit(ctx, tenantID, sandboxID, "file.write", relativePath, "ok", "file written")
	return s.refreshStorage(ctx, sandbox)
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
	return s.refreshStorage(ctx, sandbox)
}

func (s *Service) CreateTunnel(ctx context.Context, tenantID, sandboxID string, req model.CreateTunnelRequest) (model.Tunnel, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Tunnel{}, err
	}
	if !sandbox.AllowTunnels {
		s.recordAudit(ctx, tenantID, sandboxID, "tunnel.create", sandboxID, "denied", "sandbox tunnel policy denied")
		return model.Tunnel{}, fmt.Errorf("sandbox does not allow tunnels")
	}
	usage, err := s.store.TenantUsage(ctx, tenantID)
	if err != nil {
		return model.Tunnel{}, err
	}
	quota, err := s.store.GetQuota(ctx, tenantID)
	if err == nil && !quota.AllowTunnels {
		s.recordAudit(ctx, tenantID, sandboxID, "tunnel.create", sandboxID, "denied", "tenant tunnel policy denied")
		return model.Tunnel{}, fmt.Errorf("tenant tunnel policy denied")
	}
	if err == nil && usage.ActiveTunnels >= quota.MaxTunnels {
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
	s.recordAudit(ctx, tenantID, sandboxID, "tunnel.create", tunnel.ID, "ok", tunnel.Endpoint)
	return tunnel, nil
}

func (s *Service) ListTunnels(ctx context.Context, tenantID, sandboxID string) ([]model.Tunnel, error) {
	return s.store.ListTunnels(ctx, tenantID, sandboxID)
}

func (s *Service) RevokeTunnel(ctx context.Context, tenantID, tunnelID string) error {
	if _, err := s.store.GetTunnel(ctx, tenantID, tunnelID); err != nil {
		return err
	}
	if err := s.store.RevokeTunnel(ctx, tenantID, tunnelID); err != nil {
		return err
	}
	s.recordAudit(ctx, tenantID, "", "tunnel.revoke", tunnelID, "ok", "tunnel revoked")
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
	snapshot := model.Snapshot{
		ID:        newID("snap-"),
		SandboxID: sandbox.ID,
		TenantID:  tenantID,
		Name:      req.Name,
		Status:    model.SnapshotStatusCreating,
		CreatedAt: time.Now().UTC(),
	}
	if snapshot.Name == "" {
		snapshot.Name = snapshot.ID
	}
	if err := s.store.CreateSnapshot(ctx, snapshot); err != nil {
		return model.Snapshot{}, err
	}
	info, err := s.runtime.CreateSnapshot(ctx, sandbox, snapshot.ID)
	if err != nil {
		snapshot.Status = model.SnapshotStatusError
		_ = s.store.UpdateSnapshot(ctx, snapshot)
		return model.Snapshot{}, err
	}
	snapshotDir := filepath.Join(s.cfg.SnapshotRoot, sandbox.ID, snapshot.ID)
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return model.Snapshot{}, err
	}
	if info.ImageRef != "" {
		targetImage := filepath.Join(snapshotDir, "rootfs.img")
		if info.ImageRef != targetImage {
			if err := copyFile(targetImage, info.ImageRef); err != nil {
				return model.Snapshot{}, err
			}
		}
		snapshot.ImageRef = targetImage
		if info.ImageRef != targetImage {
			_ = os.Remove(info.ImageRef)
		}
	} else {
		snapshot.ImageRef = info.ImageRef
	}
	if info.WorkspaceTar != "" {
		targetTar := filepath.Join(snapshotDir, "workspace.img")
		if info.WorkspaceTar != targetTar {
			if err := copyFile(targetTar, info.WorkspaceTar); err != nil {
				return model.Snapshot{}, err
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
		exportPath := filepath.Join(s.cfg.OptionalSnapshotExport, snapshot.ID+".tar.gz")
		if err := os.MkdirAll(filepath.Dir(exportPath), 0o755); err == nil {
			_ = copyFile(exportPath, snapshot.WorkspaceTar)
			snapshot.ExportLocation = exportPath
		}
	}
	if err := s.store.UpdateSnapshot(ctx, snapshot); err != nil {
		return model.Snapshot{}, err
	}
	if err := s.refreshStorage(ctx, sandbox); err != nil {
		return model.Snapshot{}, err
	}
	s.recordAudit(ctx, tenantID, sandboxID, "snapshot.create", snapshot.ID, "ok", snapshot.Name)
	return snapshot, nil
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
	if sandbox.Status == model.SandboxStatusRunning || sandbox.Status == model.SandboxStatusSuspended {
		if _, err := s.StopSandbox(ctx, tenantID, sandbox.ID, true); err != nil {
			return model.Sandbox{}, err
		}
		sandbox, _ = s.store.GetSandbox(ctx, tenantID, sandbox.ID)
	}
	state, err := s.runtime.RestoreSnapshot(ctx, sandbox, snapshot)
	if err != nil {
		return model.Sandbox{}, err
	}
	sandbox.Status = model.SandboxStatusStopped
	sandbox.RuntimeStatus = string(state.Status)
	sandbox.BaseImageRef = snapshot.ImageRef
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
	s.recordAudit(ctx, tenantID, sandbox.ID, "snapshot.restore", snapshot.ID, "ok", snapshot.Name)
	return s.store.GetSandbox(ctx, tenantID, sandbox.ID)
}

func (s *Service) Reconcile(ctx context.Context) error {
	sandboxes, err := s.store.ListNonDeletedSandboxes(ctx)
	if err != nil {
		return err
	}
	for _, sandbox := range sandboxes {
		state, err := s.runtime.Inspect(ctx, sandbox)
		if err != nil {
			sandbox.Status = model.SandboxStatusError
			sandbox.RuntimeStatus = string(model.SandboxStatusError)
			sandbox.LastRuntimeError = err.Error()
			sandbox.UpdatedAt = time.Now().UTC()
			_ = s.store.UpdateSandboxState(ctx, sandbox)
			continue
		}
		switch {
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
		sandbox.LastRuntimeError = state.Error
		sandbox.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdateSandboxState(ctx, sandbox)
		_ = s.store.UpdateRuntimeState(ctx, sandbox.ID, state)
		_ = s.refreshStorage(ctx, sandbox)
	}
	return nil
}

func (s *Service) refreshStorage(ctx context.Context, sandbox model.Sandbox) error {
	if runtime, ok := s.runtime.(storageMeasurer); ok {
		usage, err := runtime.MeasureStorage(ctx, sandbox)
		if err != nil {
			return err
		}
		snapshotExportBytes, _ := dirSize(filepath.Join(s.cfg.SnapshotRoot, sandbox.ID))
		usage.SnapshotBytes += snapshotExportBytes
		return s.store.UpdateStorageUsage(ctx, sandbox.ID, usage.RootfsBytes, usage.WorkspaceBytes, usage.CacheBytes, usage.SnapshotBytes)
	}
	rootfsBytes, _ := dirSize(sandbox.StorageRoot)
	workspaceBytes, _ := dirSize(sandbox.WorkspaceRoot)
	cacheBytes, _ := dirSize(sandbox.CacheRoot)
	snapshotBytes, _ := dirSize(filepath.Join(s.cfg.SnapshotRoot, sandbox.ID))
	return s.store.UpdateStorageUsage(ctx, sandbox.ID, rootfsBytes, workspaceBytes, cacheBytes, snapshotBytes)
}

func (s *Service) applyCreateDefaults(req model.CreateSandboxRequest) model.CreateSandboxRequest {
	if req.BaseImageRef == "" {
		req.BaseImageRef = s.cfg.BaseImageRef
	}
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
	if req.CPULimit <= 0 || req.MemoryLimitMB <= 0 || req.PIDsLimit <= 0 || req.DiskLimitMB <= 0 {
		return errors.New("cpu, memory, pids, and disk limits must be positive")
	}
	if req.NetworkMode != model.NetworkModeInternetEnabled && req.NetworkMode != model.NetworkModeInternetDisabled {
		return fmt.Errorf("invalid network mode %q", req.NetworkMode)
	}
	return nil
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

func (s *Service) transitionSandbox(ctx context.Context, tenantID, sandboxID string, transitional model.SandboxStatus, action func(model.Sandbox) (model.RuntimeState, model.SandboxStatus, error), finalStatus model.SandboxStatus) (model.Sandbox, error) {
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
	s.recordAudit(ctx, tenantID, sandbox.ID, "sandbox.transition", sandbox.ID, "ok", fmt.Sprintf("%s->%s", transitional, finalStatus))
	return s.store.GetSandbox(ctx, tenantID, sandbox.ID)
}

func (s *Service) recordAudit(ctx context.Context, tenantID, sandboxID, action, resourceID, outcome, message string) {
	_ = s.store.AddAuditEvent(ctx, model.AuditEvent{
		ID:         newID("audit-"),
		TenantID:   tenantID,
		SandboxID:  sandboxID,
		Action:     action,
		ResourceID: resourceID,
		Outcome:    outcome,
		Message:    message,
		CreatedAt:  time.Now().UTC(),
	})
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
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	gr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(destination, header.Name)
		if header.FileInfo().IsDir() {
			if err := os.MkdirAll(target, header.FileInfo().Mode()); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, header.FileInfo().Mode())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	}
}
