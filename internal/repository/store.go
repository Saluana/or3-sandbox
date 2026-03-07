package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/model"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SeedTenants(ctx context.Context, tenants []config.TenantConfig, quota model.TenantQuota) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, tenant := range tenants {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO tenants(tenant_id, name, token_hash, created_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT(tenant_id) DO UPDATE SET name=excluded.name, token_hash=excluded.token_hash
			`, tenant.ID, tenant.Name, config.HashToken(tenant.Token), now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO quotas(
					tenant_id, max_sandboxes, max_running_sandboxes, max_concurrent_execs, max_tunnels,
					max_cpu_cores, max_memory_mb, max_storage_mb, allow_tunnels,
					default_tunnel_auth_mode, default_tunnel_visibility
				)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(tenant_id) DO UPDATE SET
					max_sandboxes=excluded.max_sandboxes,
					max_running_sandboxes=excluded.max_running_sandboxes,
					max_concurrent_execs=excluded.max_concurrent_execs,
					max_tunnels=excluded.max_tunnels,
					max_cpu_cores=excluded.max_cpu_cores,
					max_memory_mb=excluded.max_memory_mb,
					max_storage_mb=excluded.max_storage_mb,
					allow_tunnels=excluded.allow_tunnels,
					default_tunnel_auth_mode=excluded.default_tunnel_auth_mode,
					default_tunnel_visibility=excluded.default_tunnel_visibility
			`, tenant.ID, quota.MaxSandboxes, quota.MaxRunningSandboxes, quota.MaxConcurrentExecs, quota.MaxTunnels, quota.MaxCPUCores, quota.MaxMemoryMB, quota.MaxStorageMB, boolToInt(quota.AllowTunnels), quota.DefaultTunnelAuthMode, quota.DefaultTunnelVisibility); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) AuthenticateTenant(ctx context.Context, tokenHash string) (model.Tenant, model.TenantQuota, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT t.tenant_id, t.name, t.token_hash, t.created_at,
		       q.max_sandboxes, q.max_running_sandboxes, q.max_concurrent_execs, q.max_tunnels,
		       q.max_cpu_cores, q.max_memory_mb, q.max_storage_mb, q.allow_tunnels,
		       q.default_tunnel_auth_mode, q.default_tunnel_visibility
		FROM tenants t
		JOIN quotas q ON q.tenant_id = t.tenant_id
		WHERE t.token_hash = ?
	`, tokenHash)
	var tenant model.Tenant
	var quota model.TenantQuota
	var created string
	var allowTunnels int
	if err := row.Scan(
		&tenant.ID, &tenant.Name, &tenant.TokenHash, &created,
		&quota.MaxSandboxes, &quota.MaxRunningSandboxes, &quota.MaxConcurrentExecs, &quota.MaxTunnels,
		&quota.MaxCPUCores, &quota.MaxMemoryMB, &quota.MaxStorageMB, &allowTunnels,
		&quota.DefaultTunnelAuthMode, &quota.DefaultTunnelVisibility,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Tenant{}, model.TenantQuota{}, ErrNotFound
		}
		return model.Tenant{}, model.TenantQuota{}, err
	}
	tenant.CreatedAt = mustTime(created)
	quota.TenantID = tenant.ID
	quota.AllowTunnels = allowTunnels == 1
	return tenant, quota, nil
}

func (s *Store) GetQuota(ctx context.Context, tenantID string) (model.TenantQuota, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT max_sandboxes, max_running_sandboxes, max_concurrent_execs, max_tunnels,
		       max_cpu_cores, max_memory_mb, max_storage_mb, allow_tunnels,
		       default_tunnel_auth_mode, default_tunnel_visibility
		FROM quotas
		WHERE tenant_id = ?
	`, tenantID)
	var quota model.TenantQuota
	var allowTunnels int
	if err := row.Scan(
		&quota.MaxSandboxes, &quota.MaxRunningSandboxes, &quota.MaxConcurrentExecs, &quota.MaxTunnels,
		&quota.MaxCPUCores, &quota.MaxMemoryMB, &quota.MaxStorageMB, &allowTunnels,
		&quota.DefaultTunnelAuthMode, &quota.DefaultTunnelVisibility,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.TenantQuota{}, ErrNotFound
		}
		return model.TenantQuota{}, err
	}
	quota.TenantID = tenantID
	quota.AllowTunnels = allowTunnels == 1
	return quota, nil
}

func (s *Store) CreateSandbox(ctx context.Context, sandbox model.Sandbox) error {
	now := sandbox.CreatedAt.UTC().Format(time.RFC3339Nano)
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sandboxes(
				sandbox_id, tenant_id, status, runtime_backend, base_image_ref,
				cpu_limit, memory_limit_mb, pids_limit, disk_limit_mb,
				network_mode, allow_tunnels, storage_root, workspace_root, cache_root,
				created_at, updated_at, last_active_at, deleted_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
		`, sandbox.ID, sandbox.TenantID, string(sandbox.Status), sandbox.RuntimeBackend, sandbox.BaseImageRef,
			sandbox.CPULimit, sandbox.MemoryLimitMB, sandbox.PIDsLimit, sandbox.DiskLimitMB,
			string(sandbox.NetworkMode), boolToInt(sandbox.AllowTunnels), sandbox.StorageRoot, sandbox.WorkspaceRoot, sandbox.CacheRoot,
			now, sandbox.UpdatedAt.UTC().Format(time.RFC3339Nano), sandbox.LastActiveAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sandbox_runtime_state(sandbox_id, runtime_id, runtime_status, last_runtime_error, ip_address, pid, started_at)
			VALUES (?, ?, ?, '', '', 0, NULL)
		`, sandbox.ID, sandbox.RuntimeID, sandbox.RuntimeStatus); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sandbox_storage(sandbox_id, rootfs_bytes, workspace_bytes, cache_bytes, snapshot_bytes, updated_at)
			VALUES (?, 0, 0, 0, 0, ?)
		`, sandbox.ID, now); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) UpdateSandboxState(ctx context.Context, sandbox model.Sandbox) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		var deletedAt interface{}
		if sandbox.DeletedAt != nil {
			deletedAt = sandbox.DeletedAt.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sandboxes
			SET status=?, base_image_ref=?, cpu_limit=?, memory_limit_mb=?, pids_limit=?, disk_limit_mb=?, network_mode=?, allow_tunnels=?,
			    updated_at=?, last_active_at=?, deleted_at=?
			WHERE sandbox_id=? AND tenant_id=?
		`, string(sandbox.Status), sandbox.BaseImageRef, sandbox.CPULimit, sandbox.MemoryLimitMB, sandbox.PIDsLimit, sandbox.DiskLimitMB,
			string(sandbox.NetworkMode), boolToInt(sandbox.AllowTunnels), sandbox.UpdatedAt.UTC().Format(time.RFC3339Nano),
			sandbox.LastActiveAt.UTC().Format(time.RFC3339Nano), deletedAt, sandbox.ID, sandbox.TenantID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sandbox_runtime_state
			SET runtime_id=?, runtime_status=?, last_runtime_error=?
			WHERE sandbox_id=?
		`, sandbox.RuntimeID, sandbox.RuntimeStatus, sandbox.LastRuntimeError, sandbox.ID); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) UpdateRuntimeState(ctx context.Context, sandboxID string, state model.RuntimeState) error {
	var startedAt interface{}
	if state.StartedAt != nil {
		startedAt = state.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE sandbox_runtime_state
		SET runtime_id=?, runtime_status=?, last_runtime_error=?, ip_address=?, pid=?, started_at=?
		WHERE sandbox_id=?
	`, state.RuntimeID, string(state.Status), state.Error, state.IPAddress, state.Pid, startedAt, sandboxID)
	return err
}

func (s *Store) GetSandbox(ctx context.Context, tenantID, sandboxID string) (model.Sandbox, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT s.sandbox_id, s.tenant_id, s.status, s.runtime_backend, s.base_image_ref, s.cpu_limit,
		       s.memory_limit_mb, s.pids_limit, s.disk_limit_mb, s.network_mode, s.allow_tunnels,
		       s.storage_root, s.workspace_root, s.cache_root,
		       s.created_at, s.updated_at, s.last_active_at, s.deleted_at,
		       r.runtime_id, r.runtime_status, r.last_runtime_error
		FROM sandboxes s
		JOIN sandbox_runtime_state r ON r.sandbox_id = s.sandbox_id
		WHERE s.sandbox_id = ? AND s.tenant_id = ?
	`, sandboxID, tenantID)
	sandbox, err := scanSandbox(row)
	if err != nil {
		return model.Sandbox{}, err
	}
	return sandbox, nil
}

func (s *Store) ListSandboxes(ctx context.Context, tenantID string) ([]model.Sandbox, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.sandbox_id, s.tenant_id, s.status, s.runtime_backend, s.base_image_ref, s.cpu_limit,
		       s.memory_limit_mb, s.pids_limit, s.disk_limit_mb, s.network_mode, s.allow_tunnels,
		       s.storage_root, s.workspace_root, s.cache_root,
		       s.created_at, s.updated_at, s.last_active_at, s.deleted_at,
		       r.runtime_id, r.runtime_status, r.last_runtime_error
		FROM sandboxes s
		JOIN sandbox_runtime_state r ON r.sandbox_id = s.sandbox_id
		WHERE s.tenant_id = ?
		ORDER BY s.created_at
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sandboxes []model.Sandbox
	for rows.Next() {
		sandbox, err := scanSandbox(rows)
		if err != nil {
			return nil, err
		}
		sandboxes = append(sandboxes, sandbox)
	}
	return sandboxes, rows.Err()
}

func (s *Store) ListNonDeletedSandboxes(ctx context.Context) ([]model.Sandbox, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.sandbox_id, s.tenant_id, s.status, s.runtime_backend, s.base_image_ref, s.cpu_limit,
		       s.memory_limit_mb, s.pids_limit, s.disk_limit_mb, s.network_mode, s.allow_tunnels,
		       s.storage_root, s.workspace_root, s.cache_root,
		       s.created_at, s.updated_at, s.last_active_at, s.deleted_at,
		       r.runtime_id, r.runtime_status, r.last_runtime_error
		FROM sandboxes s
		JOIN sandbox_runtime_state r ON r.sandbox_id = s.sandbox_id
		WHERE s.status != ?
	`, string(model.SandboxStatusDeleted))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sandboxes []model.Sandbox
	for rows.Next() {
		sandbox, err := scanSandbox(rows)
		if err != nil {
			return nil, err
		}
		sandboxes = append(sandboxes, sandbox)
	}
	return sandboxes, rows.Err()
}

func (s *Store) UpdateStorageUsage(ctx context.Context, sandboxID string, rootfsBytes, workspaceBytes, cacheBytes, snapshotBytes int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sandbox_storage
		SET rootfs_bytes=?, workspace_bytes=?, cache_bytes=?, snapshot_bytes=?, updated_at=?
		WHERE sandbox_id=?
	`, rootfsBytes, workspaceBytes, cacheBytes, snapshotBytes, time.Now().UTC().Format(time.RFC3339Nano), sandboxID)
	return err
}

type TenantUsage struct {
	Sandboxes          int
	RunningSandboxes   int
	ConcurrentExecs    int
	ActiveTunnels      int
	RequestedCPU       int
	RequestedMemory    int
	RequestedStorage   int
	ActualStorageBytes int64
}

func (s *Store) TenantUsage(ctx context.Context, tenantID string) (TenantUsage, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) AS sandboxes,
			SUM(CASE WHEN s.status = ? THEN 1 ELSE 0 END) AS running,
			SUM(s.cpu_limit) AS cpu_total,
			SUM(s.memory_limit_mb) AS memory_total,
			SUM(s.disk_limit_mb) AS storage_total,
			SUM(COALESCE(ss.rootfs_bytes, 0) + COALESCE(ss.workspace_bytes, 0) + COALESCE(ss.cache_bytes, 0) + COALESCE(ss.snapshot_bytes, 0)) AS actual_storage_bytes
		FROM sandboxes s
		LEFT JOIN sandbox_storage ss ON ss.sandbox_id = s.sandbox_id
		WHERE s.tenant_id = ? AND s.status != ?
	`, string(model.SandboxStatusRunning), tenantID, string(model.SandboxStatusDeleted))
	var usage TenantUsage
	var running, cpuTotal, memTotal, storageTotal, actualStorageBytes sql.NullInt64
	if err := row.Scan(&usage.Sandboxes, &running, &cpuTotal, &memTotal, &storageTotal, &actualStorageBytes); err != nil {
		return usage, err
	}
	usage.RunningSandboxes = int(running.Int64)
	usage.RequestedCPU = int(cpuTotal.Int64)
	usage.RequestedMemory = int(memTotal.Int64)
	usage.RequestedStorage = int(storageTotal.Int64)
	usage.ActualStorageBytes = actualStorageBytes.Int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM executions WHERE tenant_id = ? AND status = ?
	`, tenantID, string(model.ExecutionStatusRunning)).Scan(&usage.ConcurrentExecs); err != nil {
		return usage, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tunnels WHERE tenant_id = ? AND revoked_at IS NULL
	`, tenantID).Scan(&usage.ActiveTunnels); err != nil {
		return usage, err
	}
	return usage, nil
}

func (s *Store) CreateExecution(ctx context.Context, execution model.Execution) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO executions(
			execution_id, sandbox_id, tenant_id, command, cwd, timeout_seconds, status, exit_code,
			stdout_preview, stderr_preview, stdout_truncated, stderr_truncated, started_at, completed_at, duration_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)
	`, execution.ID, execution.SandboxID, execution.TenantID, execution.Command, execution.Cwd,
		execution.TimeoutSeconds, string(execution.Status), nil, "", "", 0, 0, execution.StartedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) UpdateExecution(ctx context.Context, execution model.Execution) error {
	var completed interface{}
	var duration interface{}
	if execution.CompletedAt != nil {
		completed = execution.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if execution.DurationMS != nil {
		duration = *execution.DurationMS
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE executions
		SET status=?, exit_code=?, stdout_preview=?, stderr_preview=?, stdout_truncated=?, stderr_truncated=?, completed_at=?, duration_ms=?
		WHERE execution_id=? AND tenant_id=?
	`, string(execution.Status), execution.ExitCode, execution.StdoutPreview, execution.StderrPreview,
		boolToInt(execution.StdoutTruncated), boolToInt(execution.StderrTruncated), completed, duration, execution.ID, execution.TenantID)
	return err
}

func (s *Store) CreateTTYSession(ctx context.Context, session model.TTYSession) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tty_sessions(tty_session_id, sandbox_id, tenant_id, command, connected, last_resize, created_at, closed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL)
	`, session.ID, session.SandboxID, session.TenantID, session.Command, boolToInt(session.Connected), session.LastResize, session.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) CloseTTYSession(ctx context.Context, tenantID, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tty_sessions SET connected=0, closed_at=? WHERE tty_session_id=? AND tenant_id=?
	`, time.Now().UTC().Format(time.RFC3339Nano), sessionID, tenantID)
	return err
}

func (s *Store) UpdateTTYResize(ctx context.Context, tenantID, sessionID, resize string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tty_sessions SET last_resize=? WHERE tty_session_id=? AND tenant_id=?
	`, resize, sessionID, tenantID)
	return err
}

func (s *Store) CreateTunnel(ctx context.Context, tunnel model.Tunnel) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tunnels(tunnel_id, sandbox_id, tenant_id, target_port, protocol, auth_mode, auth_secret_hash, visibility, endpoint, created_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`, tunnel.ID, tunnel.SandboxID, tunnel.TenantID, tunnel.TargetPort, string(tunnel.Protocol), tunnel.AuthMode, tunnel.AuthSecretHash, tunnel.Visibility, tunnel.Endpoint, tunnel.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListTunnels(ctx context.Context, tenantID, sandboxID string) ([]model.Tunnel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tunnel_id, sandbox_id, tenant_id, target_port, protocol, auth_mode, auth_secret_hash, visibility, endpoint, created_at, revoked_at
		FROM tunnels
		WHERE tenant_id=? AND sandbox_id=?
		ORDER BY created_at
	`, tenantID, sandboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tunnels []model.Tunnel
	for rows.Next() {
		tunnel, err := scanTunnel(rows)
		if err != nil {
			return nil, err
		}
		tunnels = append(tunnels, tunnel)
	}
	return tunnels, rows.Err()
}

func (s *Store) GetTunnel(ctx context.Context, tenantID, tunnelID string) (model.Tunnel, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT tunnel_id, sandbox_id, tenant_id, target_port, protocol, auth_mode, auth_secret_hash, visibility, endpoint, created_at, revoked_at
		FROM tunnels WHERE tenant_id=? AND tunnel_id=?
	`, tenantID, tunnelID)
	return scanTunnel(row)
}

func (s *Store) GetTunnelByID(ctx context.Context, tunnelID string) (model.Tunnel, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT tunnel_id, sandbox_id, tenant_id, target_port, protocol, auth_mode, auth_secret_hash, visibility, endpoint, created_at, revoked_at
		FROM tunnels WHERE tunnel_id=?
	`, tunnelID)
	return scanTunnel(row)
}

func (s *Store) RevokeTunnel(ctx context.Context, tenantID, tunnelID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tunnels SET revoked_at=? WHERE tenant_id=? AND tunnel_id=? AND revoked_at IS NULL
	`, time.Now().UTC().Format(time.RFC3339Nano), tenantID, tunnelID)
	return err
}

func (s *Store) CreateSnapshot(ctx context.Context, snapshot model.Snapshot) error {
	var completed interface{}
	if snapshot.CompletedAt != nil {
		completed = snapshot.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO snapshots(snapshot_id, sandbox_id, tenant_id, name, status, image_ref, workspace_tar, export_location, created_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, snapshot.ID, snapshot.SandboxID, snapshot.TenantID, snapshot.Name, string(snapshot.Status), snapshot.ImageRef, snapshot.WorkspaceTar, snapshot.ExportLocation, snapshot.CreatedAt.UTC().Format(time.RFC3339Nano), completed)
	return err
}

func (s *Store) UpdateSnapshot(ctx context.Context, snapshot model.Snapshot) error {
	var completed interface{}
	if snapshot.CompletedAt != nil {
		completed = snapshot.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE snapshots
		SET status=?, image_ref=?, workspace_tar=?, export_location=?, completed_at=?
		WHERE snapshot_id=? AND tenant_id=?
	`, string(snapshot.Status), snapshot.ImageRef, snapshot.WorkspaceTar, snapshot.ExportLocation, completed, snapshot.ID, snapshot.TenantID)
	return err
}

func (s *Store) GetSnapshot(ctx context.Context, tenantID, snapshotID string) (model.Snapshot, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT snapshot_id, sandbox_id, tenant_id, name, status, image_ref, workspace_tar, export_location, created_at, completed_at
		FROM snapshots WHERE tenant_id=? AND snapshot_id=?
	`, tenantID, snapshotID)
	return scanSnapshot(row)
}

func (s *Store) AddAuditEvent(ctx context.Context, event model.AuditEvent) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_events(audit_event_id, tenant_id, sandbox_id, action, resource_id, outcome, message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, event.ID, event.TenantID, event.SandboxID, event.Action, event.ResourceID, event.Outcome, event.Message, event.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func scanSandbox(scanner interface{ Scan(...any) error }) (model.Sandbox, error) {
	var sandbox model.Sandbox
	var created, updated, lastActive string
	var deleted sql.NullString
	var allowTunnels int
	if err := scanner.Scan(
		&sandbox.ID, &sandbox.TenantID, &sandbox.Status, &sandbox.RuntimeBackend, &sandbox.BaseImageRef,
		&sandbox.CPULimit, &sandbox.MemoryLimitMB, &sandbox.PIDsLimit, &sandbox.DiskLimitMB, &sandbox.NetworkMode,
		&allowTunnels, &sandbox.StorageRoot, &sandbox.WorkspaceRoot, &sandbox.CacheRoot,
		&created, &updated, &lastActive, &deleted,
		&sandbox.RuntimeID, &sandbox.RuntimeStatus, &sandbox.LastRuntimeError,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Sandbox{}, ErrNotFound
		}
		return model.Sandbox{}, err
	}
	sandbox.AllowTunnels = allowTunnels == 1
	sandbox.CreatedAt = mustTime(created)
	sandbox.UpdatedAt = mustTime(updated)
	sandbox.LastActiveAt = mustTime(lastActive)
	if deleted.Valid {
		t := mustTime(deleted.String)
		sandbox.DeletedAt = &t
	}
	return sandbox, nil
}

func scanTunnel(scanner interface{ Scan(...any) error }) (model.Tunnel, error) {
	var tunnel model.Tunnel
	var created string
	var revoked sql.NullString
	if err := scanner.Scan(&tunnel.ID, &tunnel.SandboxID, &tunnel.TenantID, &tunnel.TargetPort, &tunnel.Protocol, &tunnel.AuthMode, &tunnel.AuthSecretHash, &tunnel.Visibility, &tunnel.Endpoint, &created, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Tunnel{}, ErrNotFound
		}
		return model.Tunnel{}, err
	}
	tunnel.CreatedAt = mustTime(created)
	if revoked.Valid {
		t := mustTime(revoked.String)
		tunnel.RevokedAt = &t
	}
	return tunnel, nil
}

func scanSnapshot(scanner interface{ Scan(...any) error }) (model.Snapshot, error) {
	var snapshot model.Snapshot
	var created string
	var completed sql.NullString
	if err := scanner.Scan(&snapshot.ID, &snapshot.SandboxID, &snapshot.TenantID, &snapshot.Name, &snapshot.Status, &snapshot.ImageRef, &snapshot.WorkspaceTar, &snapshot.ExportLocation, &created, &completed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Snapshot{}, ErrNotFound
		}
		return model.Snapshot{}, err
	}
	snapshot.CreatedAt = mustTime(created)
	if completed.Valid {
		t := mustTime(completed.String)
		snapshot.CompletedAt = &t
	}
	return snapshot, nil
}

func mustTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(fmt.Sprintf("invalid time %q: %v", value, err))
	}
	return t
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
