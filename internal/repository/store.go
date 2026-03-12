package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
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

// DB returns the underlying *sql.DB. This is intended for use in tests that
// need direct database access (e.g. to simulate legacy rows without a
// runtime_class column value).
func (s *Store) DB() *sql.DB {
	return s.db
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
					max_cpu_cores, max_cpu_millis, max_memory_mb, max_storage_mb, allow_tunnels,
					default_tunnel_auth_mode, default_tunnel_visibility
				)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(tenant_id) DO UPDATE SET
					max_sandboxes=excluded.max_sandboxes,
					max_running_sandboxes=excluded.max_running_sandboxes,
					max_concurrent_execs=excluded.max_concurrent_execs,
					max_tunnels=excluded.max_tunnels,
					max_cpu_cores=excluded.max_cpu_cores,
					max_cpu_millis=excluded.max_cpu_millis,
					max_memory_mb=excluded.max_memory_mb,
					max_storage_mb=excluded.max_storage_mb,
					allow_tunnels=excluded.allow_tunnels,
					default_tunnel_auth_mode=excluded.default_tunnel_auth_mode,
					default_tunnel_visibility=excluded.default_tunnel_visibility
			`, tenant.ID, quota.MaxSandboxes, quota.MaxRunningSandboxes, quota.MaxConcurrentExecs, quota.MaxTunnels, quota.MaxCPUCores.VCPUCount(), quota.MaxCPUCores.MilliValue(), quota.MaxMemoryMB, quota.MaxStorageMB, boolToInt(quota.AllowTunnels), quota.DefaultTunnelAuthMode, quota.DefaultTunnelVisibility); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) EnsureTenantQuota(ctx context.Context, tenant model.Tenant, quota model.TenantQuota, tokenHash string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		name := tenant.Name
		if name == "" {
			name = tenant.ID
		}
		if tokenHash == "" {
			tokenHash = config.HashToken("jwt:" + tenant.ID)
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tenants(tenant_id, name, token_hash, created_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(tenant_id) DO UPDATE SET name=excluded.name
		`, tenant.ID, name, tokenHash, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO quotas(
				tenant_id, max_sandboxes, max_running_sandboxes, max_concurrent_execs, max_tunnels,
				max_cpu_cores, max_cpu_millis, max_memory_mb, max_storage_mb, allow_tunnels,
				default_tunnel_auth_mode, default_tunnel_visibility
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(tenant_id) DO NOTHING
		`, tenant.ID, quota.MaxSandboxes, quota.MaxRunningSandboxes, quota.MaxConcurrentExecs, quota.MaxTunnels, quota.MaxCPUCores.VCPUCount(), quota.MaxCPUCores.MilliValue(), quota.MaxMemoryMB, quota.MaxStorageMB, boolToInt(quota.AllowTunnels), quota.DefaultTunnelAuthMode, quota.DefaultTunnelVisibility); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) AuthenticateTenant(ctx context.Context, tokenHash string) (model.Tenant, model.TenantQuota, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT t.tenant_id, t.name, t.token_hash, t.created_at,
		       q.max_sandboxes, q.max_running_sandboxes, q.max_concurrent_execs, q.max_tunnels,
		       q.max_cpu_millis, q.max_memory_mb, q.max_storage_mb, q.allow_tunnels,
		       q.default_tunnel_auth_mode, q.default_tunnel_visibility
		FROM tenants t
		JOIN quotas q ON q.tenant_id = t.tenant_id
		WHERE t.token_hash = ?
	`, tokenHash)
	var tenant model.Tenant
	var quota model.TenantQuota
	var created string
	var allowTunnels int
	var maxCPUMillis int64
	if err := row.Scan(
		&tenant.ID, &tenant.Name, &tenant.TokenHash, &created,
		&quota.MaxSandboxes, &quota.MaxRunningSandboxes, &quota.MaxConcurrentExecs, &quota.MaxTunnels,
		&maxCPUMillis, &quota.MaxMemoryMB, &quota.MaxStorageMB, &allowTunnels,
		&quota.DefaultTunnelAuthMode, &quota.DefaultTunnelVisibility,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Tenant{}, model.TenantQuota{}, ErrNotFound
		}
		return model.Tenant{}, model.TenantQuota{}, err
	}
	parsedCreatedAt, err := parseTime(created)
	if err != nil {
		return model.Tenant{}, model.TenantQuota{}, err
	}
	tenant.CreatedAt = parsedCreatedAt
	quota.TenantID = tenant.ID
	quota.MaxCPUCores = model.CPUQuantity(maxCPUMillis)
	quota.AllowTunnels = allowTunnels == 1
	return tenant, quota, nil
}

func (s *Store) GetQuota(ctx context.Context, tenantID string) (model.TenantQuota, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT max_sandboxes, max_running_sandboxes, max_concurrent_execs, max_tunnels,
		       max_cpu_millis, max_memory_mb, max_storage_mb, allow_tunnels,
		       default_tunnel_auth_mode, default_tunnel_visibility
		FROM quotas
		WHERE tenant_id = ?
	`, tenantID)
	var quota model.TenantQuota
	var allowTunnels int
	var maxCPUMillis int64
	if err := row.Scan(
		&quota.MaxSandboxes, &quota.MaxRunningSandboxes, &quota.MaxConcurrentExecs, &quota.MaxTunnels,
		&maxCPUMillis, &quota.MaxMemoryMB, &quota.MaxStorageMB, &allowTunnels,
		&quota.DefaultTunnelAuthMode, &quota.DefaultTunnelVisibility,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.TenantQuota{}, ErrNotFound
		}
		return model.TenantQuota{}, err
	}
	quota.TenantID = tenantID
	quota.MaxCPUCores = model.CPUQuantity(maxCPUMillis)
	quota.AllowTunnels = allowTunnels == 1
	return quota, nil
}

func (s *Store) CreateSandbox(ctx context.Context, sandbox model.Sandbox) error {
	now := sandbox.CreatedAt.UTC().Format(time.RFC3339Nano)
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sandboxes(
				sandbox_id, tenant_id, status, runtime_selection, runtime_backend, runtime_class, base_image_ref,
				profile, feature_set, capability_set, control_mode, control_protocol_version, workspace_contract_version, image_contract_version,
				cpu_limit, cpu_limit_millis, memory_limit_mb, pids_limit, disk_limit_mb,
				network_mode, allow_tunnels, storage_root, workspace_root, cache_root,
				created_at, updated_at, last_active_at, deleted_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
		`, sandbox.ID, sandbox.TenantID, string(sandbox.Status), string(sandbox.RuntimeSelection), sandbox.RuntimeBackend, string(sandbox.RuntimeClass), sandbox.BaseImageRef,
			string(sandbox.Profile), joinStringList(sandbox.Features), joinStringList(sandbox.Capabilities), string(sandbox.ControlMode), sandbox.ControlProtocolVersion, sandbox.WorkspaceContractVersion, sandbox.ImageContractVersion,
			sandbox.CPULimit.VCPUCount(), sandbox.CPULimit.MilliValue(), sandbox.MemoryLimitMB, sandbox.PIDsLimit, sandbox.DiskLimitMB,
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
			INSERT INTO sandbox_storage(sandbox_id, rootfs_bytes, workspace_bytes, cache_bytes, snapshot_bytes, rootfs_entries, workspace_entries, cache_entries, snapshot_entries, updated_at)
			VALUES (?, 0, 0, 0, 0, 0, 0, 0, 0, ?)
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
			SET status=?, base_image_ref=?, profile=?, feature_set=?, capability_set=?, control_mode=?, control_protocol_version=?, workspace_contract_version=?, image_contract_version=?, cpu_limit=?, cpu_limit_millis=?, memory_limit_mb=?, pids_limit=?, disk_limit_mb=?, network_mode=?, allow_tunnels=?,
			    updated_at=?, last_active_at=?, deleted_at=?
			WHERE sandbox_id=? AND tenant_id=?
		`, string(sandbox.Status), sandbox.BaseImageRef, string(sandbox.Profile), joinStringList(sandbox.Features), joinStringList(sandbox.Capabilities), string(sandbox.ControlMode), sandbox.ControlProtocolVersion, sandbox.WorkspaceContractVersion, sandbox.ImageContractVersion, sandbox.CPULimit.VCPUCount(), sandbox.CPULimit.MilliValue(), sandbox.MemoryLimitMB, sandbox.PIDsLimit, sandbox.DiskLimitMB,
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
		SELECT s.sandbox_id, s.tenant_id, s.status, s.runtime_selection, s.runtime_backend, s.runtime_class, s.base_image_ref, s.profile, s.feature_set, s.capability_set, s.control_mode, s.control_protocol_version, s.workspace_contract_version, s.image_contract_version, s.cpu_limit_millis,
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
		SELECT s.sandbox_id, s.tenant_id, s.status, s.runtime_selection, s.runtime_backend, s.runtime_class, s.base_image_ref, s.profile, s.feature_set, s.capability_set, s.control_mode, s.control_protocol_version, s.workspace_contract_version, s.image_contract_version, s.cpu_limit_millis,
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
	return s.listNonDeletedSandboxes(ctx, "")
}

func (s *Store) ListNonDeletedSandboxesByTenant(ctx context.Context, tenantID string) ([]model.Sandbox, error) {
	return s.listNonDeletedSandboxes(ctx, tenantID)
}

func (s *Store) listNonDeletedSandboxes(ctx context.Context, tenantID string) ([]model.Sandbox, error) {
	query := `
		SELECT s.sandbox_id, s.tenant_id, s.status, s.runtime_selection, s.runtime_backend, s.runtime_class, s.base_image_ref, s.profile, s.feature_set, s.capability_set, s.control_mode, s.control_protocol_version, s.workspace_contract_version, s.image_contract_version, s.cpu_limit_millis,
		       s.memory_limit_mb, s.pids_limit, s.disk_limit_mb, s.network_mode, s.allow_tunnels,
		       s.storage_root, s.workspace_root, s.cache_root,
		       s.created_at, s.updated_at, s.last_active_at, s.deleted_at,
		       r.runtime_id, r.runtime_status, r.last_runtime_error
		FROM sandboxes s
		JOIN sandbox_runtime_state r ON r.sandbox_id = s.sandbox_id
		WHERE s.status != ?`
	args := []any{string(model.SandboxStatusDeleted)}
	if tenantID != "" {
		query += ` AND s.tenant_id = ?`
		args = append(args, tenantID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
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

func (s *Store) StorageUsageUpdatedAt(ctx context.Context, sandboxID string) (time.Time, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT updated_at
		FROM sandbox_storage
		WHERE sandbox_id = ?
	`, sandboxID)
	var updated string
	if err := row.Scan(&updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, ErrNotFound
		}
		return time.Time{}, err
	}
	return parseTime(updated)
}

func (s *Store) UpdateStorageUsage(ctx context.Context, sandboxID string, rootfsBytes, workspaceBytes, cacheBytes, snapshotBytes, rootfsEntries, workspaceEntries, cacheEntries, snapshotEntries int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sandbox_storage
		SET rootfs_bytes=?, workspace_bytes=?, cache_bytes=?, snapshot_bytes=?, rootfs_entries=?, workspace_entries=?, cache_entries=?, snapshot_entries=?, updated_at=?
		WHERE sandbox_id=?
	`, rootfsBytes, workspaceBytes, cacheBytes, snapshotBytes, rootfsEntries, workspaceEntries, cacheEntries, snapshotEntries, time.Now().UTC().Format(time.RFC3339Nano), sandboxID)
	return err
}

type TenantUsage struct {
	Sandboxes            int               `json:"sandboxes"`
	RunningSandboxes     int               `json:"running_sandboxes"`
	ConcurrentExecs      int               `json:"concurrent_execs"`
	ActiveTunnels        int               `json:"active_tunnels"`
	RequestedCPU         model.CPUQuantity `json:"requested_cpu"`
	RequestedMemory      int               `json:"requested_memory_mb"`
	RequestedStorage     int               `json:"requested_storage_mb"`
	ActualStorageBytes   int64             `json:"actual_storage_bytes"`
	ActualStorageEntries int64             `json:"actual_storage_entries"`
}

func (s *Store) TenantUsage(ctx context.Context, tenantID string) (TenantUsage, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) AS sandboxes,
			SUM(CASE WHEN s.status = ? THEN 1 ELSE 0 END) AS running,
			SUM(s.cpu_limit_millis) AS cpu_total,
			SUM(s.memory_limit_mb) AS memory_total,
			SUM(s.disk_limit_mb) AS storage_total,
			SUM(COALESCE(ss.rootfs_bytes, 0) + COALESCE(ss.workspace_bytes, 0) + COALESCE(ss.cache_bytes, 0) + COALESCE(ss.snapshot_bytes, 0)) AS actual_storage_bytes,
			SUM(COALESCE(ss.rootfs_entries, 0) + COALESCE(ss.workspace_entries, 0) + COALESCE(ss.cache_entries, 0) + COALESCE(ss.snapshot_entries, 0)) AS actual_storage_entries,
			COALESCE((SELECT COUNT(*) FROM executions e WHERE e.tenant_id = ? AND e.status = ?), 0) AS concurrent_execs,
			COALESCE((SELECT COUNT(*) FROM tunnels t WHERE t.tenant_id = ? AND t.revoked_at IS NULL), 0) AS active_tunnels
		FROM sandboxes s
		LEFT JOIN sandbox_storage ss ON ss.sandbox_id = s.sandbox_id
		WHERE s.tenant_id = ? AND s.status != ?
	`, string(model.SandboxStatusRunning), tenantID, string(model.ExecutionStatusRunning), tenantID, tenantID, string(model.SandboxStatusDeleted))
	var usage TenantUsage
	var running, cpuTotal, memTotal, storageTotal, actualStorageBytes, actualStorageEntries sql.NullInt64
	if err := row.Scan(&usage.Sandboxes, &running, &cpuTotal, &memTotal, &storageTotal, &actualStorageBytes, &actualStorageEntries, &usage.ConcurrentExecs, &usage.ActiveTunnels); err != nil {
		return usage, err
	}
	usage.RunningSandboxes = int(running.Int64)
	usage.RequestedCPU = model.CPUQuantity(cpuTotal.Int64)
	usage.RequestedMemory = int(memTotal.Int64)
	usage.RequestedStorage = int(storageTotal.Int64)
	usage.ActualStorageBytes = actualStorageBytes.Int64
	usage.ActualStorageEntries = actualStorageEntries.Int64
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

func (s *Store) CreateTunnelCapability(ctx context.Context, capability model.TunnelCapability) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tunnel_capabilities(capability_id, tunnel_id, nonce_hash, path, expires_at, consumed_at, revoked_at, created_at)
		VALUES (?, ?, ?, ?, ?, NULL, NULL, ?)
	`, capability.ID, capability.TunnelID, capability.NonceHash, capability.Path, capability.ExpiresAt.UTC().Format(time.RFC3339Nano), capability.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetTunnelCapability(ctx context.Context, capabilityID string) (model.TunnelCapability, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT capability_id, tunnel_id, nonce_hash, path, expires_at, consumed_at, revoked_at, created_at
		FROM tunnel_capabilities WHERE capability_id=?
	`, capabilityID)
	return scanTunnelCapability(row)
}

func (s *Store) ConsumeTunnelCapability(ctx context.Context, capabilityID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tunnel_capabilities SET consumed_at=? WHERE capability_id=? AND consumed_at IS NULL AND revoked_at IS NULL
	`, time.Now().UTC().Format(time.RFC3339Nano), capabilityID)
	return err
}

func (s *Store) RevokeTunnelCapabilities(ctx context.Context, tunnelID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tunnel_capabilities SET revoked_at=? WHERE tunnel_id=? AND revoked_at IS NULL
	`, time.Now().UTC().Format(time.RFC3339Nano), tunnelID)
	return err
}

func (s *Store) CreateSnapshot(ctx context.Context, snapshot model.Snapshot) error {
	var completed interface{}
	if snapshot.CompletedAt != nil {
		completed = snapshot.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO snapshots(snapshot_id, sandbox_id, tenant_id, name, status, image_ref, runtime_selection, runtime_backend, profile, image_contract_version, control_protocol_version, workspace_contract_version, workspace_tar, bundle_sha256, export_location, created_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, snapshot.ID, snapshot.SandboxID, snapshot.TenantID, snapshot.Name, string(snapshot.Status), snapshot.ImageRef, string(snapshot.RuntimeSelection), snapshot.RuntimeBackend, string(snapshot.Profile), snapshot.ImageContractVersion, snapshot.ControlProtocolVersion, snapshot.WorkspaceContractVersion, snapshot.WorkspaceTar, snapshot.BundleSHA256, snapshot.ExportLocation, snapshot.CreatedAt.UTC().Format(time.RFC3339Nano), completed)
	return err
}

func (s *Store) UpdateSnapshot(ctx context.Context, snapshot model.Snapshot) error {
	var completed interface{}
	if snapshot.CompletedAt != nil {
		completed = snapshot.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE snapshots
		SET status=?, image_ref=?, runtime_selection=?, runtime_backend=?, profile=?, image_contract_version=?, control_protocol_version=?, workspace_contract_version=?, workspace_tar=?, bundle_sha256=?, export_location=?, completed_at=?
		WHERE snapshot_id=? AND tenant_id=?
	`, string(snapshot.Status), snapshot.ImageRef, string(snapshot.RuntimeSelection), snapshot.RuntimeBackend, string(snapshot.Profile), snapshot.ImageContractVersion, snapshot.ControlProtocolVersion, snapshot.WorkspaceContractVersion, snapshot.WorkspaceTar, snapshot.BundleSHA256, snapshot.ExportLocation, completed, snapshot.ID, snapshot.TenantID)
	return err
}

func (s *Store) GetSnapshot(ctx context.Context, tenantID, snapshotID string) (model.Snapshot, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT snapshot_id, sandbox_id, tenant_id, name, status, image_ref, runtime_selection, runtime_backend, profile, image_contract_version, control_protocol_version, workspace_contract_version, workspace_tar, bundle_sha256, export_location, created_at, completed_at
		FROM snapshots WHERE tenant_id=? AND snapshot_id=?
	`, tenantID, snapshotID)
	return scanSnapshot(row)
}

func (s *Store) ListSnapshots(ctx context.Context, tenantID, sandboxID string) ([]model.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT snapshot_id, sandbox_id, tenant_id, name, status, image_ref, runtime_selection, runtime_backend, profile, image_contract_version, control_protocol_version, workspace_contract_version, workspace_tar, bundle_sha256, export_location, created_at, completed_at
		FROM snapshots
		WHERE tenant_id=? AND sandbox_id=?
		ORDER BY created_at DESC
	`, tenantID, sandboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []model.Snapshot
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

func (s *Store) AddAuditEvent(ctx context.Context, event model.AuditEvent) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audit_events(audit_event_id, tenant_id, sandbox_id, action, resource_id, outcome, message, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, event.ID, event.TenantID, event.SandboxID, event.Action, event.ResourceID, event.Outcome, event.Message, event.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO audit_event_counts(tenant_id, action, outcome, total)
			VALUES (?, ?, ?, 1)
			ON CONFLICT(tenant_id, action, outcome) DO UPDATE SET total = total + 1
		`, event.TenantID, event.Action, event.Outcome)
		return err
	})
}

func (s *Store) ListRunningExecutions(ctx context.Context) ([]model.Execution, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT execution_id, sandbox_id, tenant_id, command, cwd, timeout_seconds, status, started_at
		FROM executions
		WHERE status = ?
		ORDER BY started_at
	`, string(model.ExecutionStatusRunning))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var executions []model.Execution
	for rows.Next() {
		var execution model.Execution
		var started string
		if err := rows.Scan(&execution.ID, &execution.SandboxID, &execution.TenantID, &execution.Command, &execution.Cwd, &execution.TimeoutSeconds, &execution.Status, &started); err != nil {
			return nil, err
		}
		execution.StartedAt, err = parseTime(started)
		if err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	return executions, rows.Err()
}

func (s *Store) ListSnapshotsByStatus(ctx context.Context, status model.SnapshotStatus) ([]model.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT snapshot_id, sandbox_id, tenant_id, name, status, image_ref, runtime_selection, runtime_backend, profile, image_contract_version, control_protocol_version, workspace_contract_version, workspace_tar, bundle_sha256, export_location, created_at, completed_at
		FROM snapshots
		WHERE status = ?
		ORDER BY created_at
	`, string(status))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []model.Snapshot
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

func (s *Store) ExecutionCounts(ctx context.Context, tenantID string) (map[model.ExecutionStatus]int, error) {
	query := `SELECT status, COUNT(*) FROM executions`
	args := []any{}
	if tenantID != "" {
		query += ` WHERE tenant_id = ?`
		args = append(args, tenantID)
	}
	query += ` GROUP BY status`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[model.ExecutionStatus]int)
	for rows.Next() {
		var status model.ExecutionStatus
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (s *Store) SnapshotCounts(ctx context.Context, tenantID string) (map[model.SnapshotStatus]int, error) {
	query := `SELECT status, COUNT(*) FROM snapshots`
	args := []any{}
	if tenantID != "" {
		query += ` WHERE tenant_id = ?`
		args = append(args, tenantID)
	}
	query += ` GROUP BY status`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[model.SnapshotStatus]int)
	for rows.Next() {
		var status model.SnapshotStatus
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (s *Store) ListAuditEvents(ctx context.Context, tenantID string) ([]model.AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT audit_event_id, tenant_id, sandbox_id, action, resource_id, outcome, message, created_at
		FROM audit_events
		WHERE tenant_id = ?
		ORDER BY created_at
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []model.AuditEvent
	for rows.Next() {
		var event model.AuditEvent
		var created string
		if err := rows.Scan(&event.ID, &event.TenantID, &event.SandboxID, &event.Action, &event.ResourceID, &event.Outcome, &event.Message, &created); err != nil {
			return nil, err
		}
		event.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) AuditEventCounts(ctx context.Context, tenantID string) (map[string]map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT action, outcome, total
		FROM audit_event_counts
		WHERE tenant_id = ?
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]map[string]int)
	for rows.Next() {
		var action, outcome string
		var count int
		if err := rows.Scan(&action, &outcome, &count); err != nil {
			return nil, err
		}
		if counts[action] == nil {
			counts[action] = make(map[string]int)
		}
		counts[action][outcome] = count
	}
	return counts, rows.Err()
}

func (s *Store) UpsertServiceAccount(ctx context.Context, account model.ServiceAccount) error {
	scopeJSON, err := json.Marshal(account.Scopes)
	if err != nil {
		return err
	}
	var expiresAt, revokedAt any
	if account.ExpiresAt != nil {
		expiresAt = account.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if account.RevokedAt != nil {
		revokedAt = account.RevokedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO service_accounts(service_account_id, tenant_id, name, scope_json, disabled, expires_at, created_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(service_account_id) DO UPDATE SET tenant_id=excluded.tenant_id, name=excluded.name, scope_json=excluded.scope_json, disabled=excluded.disabled, expires_at=excluded.expires_at, revoked_at=excluded.revoked_at
	`, account.ID, account.TenantID, account.Name, string(scopeJSON), boolToInt(account.Disabled), expiresAt, account.CreatedAt.UTC().Format(time.RFC3339Nano), revokedAt)
	return err
}

func (s *Store) GetServiceAccount(ctx context.Context, serviceAccountID string) (model.ServiceAccount, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT service_account_id, tenant_id, name, scope_json, disabled, expires_at, created_at, revoked_at
		FROM service_accounts WHERE service_account_id=?
	`, serviceAccountID)
	return scanServiceAccount(row)
}

func (s *Store) UpsertPromotedGuestImage(ctx context.Context, image model.PromotedGuestImage) error {
	var promotedAt any
	if image.PromotedAt != nil {
		promotedAt = image.PromotedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO promoted_guest_images(image_ref, image_sha256, profile, control_mode, control_protocol_version, contract_version, provenance_json, verification_status, promotion_status, promoted_at, promoted_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(image_ref) DO UPDATE SET image_sha256=excluded.image_sha256, profile=excluded.profile, control_mode=excluded.control_mode, control_protocol_version=excluded.control_protocol_version, contract_version=excluded.contract_version, provenance_json=excluded.provenance_json, verification_status=excluded.verification_status, promotion_status=excluded.promotion_status, promoted_at=excluded.promoted_at, promoted_by=excluded.promoted_by
	`, image.ImageRef, image.ImageSHA256, string(image.Profile), string(image.ControlMode), image.ControlProtocolVersion, image.ContractVersion, image.ProvenanceJSON, image.VerificationStatus, image.PromotionStatus, promotedAt, image.PromotedBy)
	return err
}

func (s *Store) GetPromotedGuestImage(ctx context.Context, imageRef string) (model.PromotedGuestImage, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT image_ref, image_sha256, profile, control_mode, control_protocol_version, contract_version, provenance_json, verification_status, promotion_status, promoted_at, promoted_by
		FROM promoted_guest_images WHERE image_ref=?
	`, imageRef)
	return scanPromotedGuestImage(row)
}

func (s *Store) ListPromotedGuestImages(ctx context.Context) ([]model.PromotedGuestImage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT image_ref, image_sha256, profile, control_mode, control_protocol_version, contract_version, provenance_json, verification_status, promotion_status, promoted_at, promoted_by
		FROM promoted_guest_images ORDER BY image_ref
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var images []model.PromotedGuestImage
	for rows.Next() {
		image, err := scanPromotedGuestImage(rows)
		if err != nil {
			return nil, err
		}
		images = append(images, image)
	}
	return images, rows.Err()
}

func (s *Store) CreateReleaseEvidence(ctx context.Context, evidence model.ReleaseEvidence) error {
	var completedAt any
	if evidence.CompletedAt != nil {
		completedAt = evidence.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO release_evidence(evidence_id, gate_name, host_fingerprint, runtime_selection, image_ref, profile, outcome, artifact_path, started_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, evidence.ID, evidence.GateName, evidence.HostFingerprint, string(evidence.RuntimeSelection), evidence.ImageRef, string(evidence.Profile), evidence.Outcome, evidence.ArtifactPath, evidence.StartedAt.UTC().Format(time.RFC3339Nano), completedAt)
	return err
}

func (s *Store) ListReleaseEvidence(ctx context.Context, gateName string) ([]model.ReleaseEvidence, error) {
	query := `
		SELECT evidence_id, gate_name, host_fingerprint, runtime_selection, image_ref, profile, outcome, artifact_path, started_at, completed_at
		FROM release_evidence`
	args := []any{}
	if strings.TrimSpace(gateName) != "" {
		query += ` WHERE gate_name=?`
		args = append(args, gateName)
	}
	query += ` ORDER BY started_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var evidence []model.ReleaseEvidence
	for rows.Next() {
		entry, err := scanReleaseEvidence(rows)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, entry)
	}
	return evidence, rows.Err()
}

func scanSandbox(scanner interface{ Scan(...any) error }) (model.Sandbox, error) {
	var sandbox model.Sandbox
	var created, updated, lastActive string
	var deleted sql.NullString
	var allowTunnels int
	var runtimeSelection string
	var profile, featureSet, capabilitySet, controlMode, controlProtocolVersion, workspaceContractVersion, imageContractVersion string
	var cpuLimitMillis int64
	var runtimeClass string
	if err := scanner.Scan(
		&sandbox.ID, &sandbox.TenantID, &sandbox.Status, &runtimeSelection, &sandbox.RuntimeBackend, &runtimeClass, &sandbox.BaseImageRef, &profile, &featureSet, &capabilitySet, &controlMode, &controlProtocolVersion, &workspaceContractVersion, &imageContractVersion,
		&cpuLimitMillis, &sandbox.MemoryLimitMB, &sandbox.PIDsLimit, &sandbox.DiskLimitMB, &sandbox.NetworkMode,
		&allowTunnels, &sandbox.StorageRoot, &sandbox.WorkspaceRoot, &sandbox.CacheRoot,
		&created, &updated, &lastActive, &deleted,
		&sandbox.RuntimeID, &sandbox.RuntimeStatus, &sandbox.LastRuntimeError,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Sandbox{}, ErrNotFound
		}
		return model.Sandbox{}, err
	}
	sandbox.CPULimit = model.CPUQuantity(cpuLimitMillis)
	sandbox.Profile = model.GuestProfile(profile)
	sandbox.Features = splitStringList(featureSet)
	sandbox.Capabilities = splitStringList(capabilitySet)
	sandbox.ControlMode = model.GuestControlMode(controlMode)
	sandbox.ControlProtocolVersion = controlProtocolVersion
	sandbox.WorkspaceContractVersion = workspaceContractVersion
	sandbox.ImageContractVersion = imageContractVersion
	sandbox.AllowTunnels = allowTunnels == 1
	sandbox.RuntimeSelection = model.ResolveRuntimeSelection(model.ParseRuntimeSelection(runtimeSelection), sandbox.RuntimeBackend)
	// Derive runtime class from backend when the stored value is empty (legacy rows).
	if runtimeClass == "" {
		sandbox.RuntimeClass = sandbox.RuntimeSelection.RuntimeClass()
	} else {
		sandbox.RuntimeClass = model.RuntimeClass(runtimeClass)
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return model.Sandbox{}, err
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return model.Sandbox{}, err
	}
	lastActiveAt, err := parseTime(lastActive)
	if err != nil {
		return model.Sandbox{}, err
	}
	sandbox.CreatedAt = createdAt
	sandbox.UpdatedAt = updatedAt
	sandbox.LastActiveAt = lastActiveAt
	if deleted.Valid {
		t, err := parseTime(deleted.String)
		if err != nil {
			return model.Sandbox{}, err
		}
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
	createdAt, err := parseTime(created)
	if err != nil {
		return model.Tunnel{}, err
	}
	tunnel.CreatedAt = createdAt
	if revoked.Valid {
		t, err := parseTime(revoked.String)
		if err != nil {
			return model.Tunnel{}, err
		}
		tunnel.RevokedAt = &t
	}
	return tunnel, nil
}

func scanSnapshot(scanner interface{ Scan(...any) error }) (model.Snapshot, error) {
	var snapshot model.Snapshot
	var created string
	var completed sql.NullString
	var runtimeSelection, runtimeBackend, profile, imageContractVersion, controlProtocolVersion, workspaceContractVersion string
	if err := scanner.Scan(&snapshot.ID, &snapshot.SandboxID, &snapshot.TenantID, &snapshot.Name, &snapshot.Status, &snapshot.ImageRef, &runtimeSelection, &runtimeBackend, &profile, &imageContractVersion, &controlProtocolVersion, &workspaceContractVersion, &snapshot.WorkspaceTar, &snapshot.BundleSHA256, &snapshot.ExportLocation, &created, &completed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Snapshot{}, ErrNotFound
		}
		return model.Snapshot{}, err
	}
	snapshot.RuntimeSelection = model.ResolveRuntimeSelection(model.ParseRuntimeSelection(runtimeSelection), runtimeBackend)
	snapshot.RuntimeBackend = runtimeBackend
	snapshot.Profile = model.GuestProfile(profile)
	snapshot.ImageContractVersion = imageContractVersion
	snapshot.ControlProtocolVersion = controlProtocolVersion
	snapshot.WorkspaceContractVersion = workspaceContractVersion
	createdAt, err := parseTime(created)
	if err != nil {
		return model.Snapshot{}, err
	}
	snapshot.CreatedAt = createdAt
	if completed.Valid {
		t, err := parseTime(completed.String)
		if err != nil {
			return model.Snapshot{}, err
		}
		snapshot.CompletedAt = &t
	}
	return snapshot, nil
}

func scanServiceAccount(scanner interface{ Scan(...any) error }) (model.ServiceAccount, error) {
	var account model.ServiceAccount
	var scopeJSON string
	var expiresAt, revokedAt sql.NullString
	var createdAt string
	var disabled int
	if err := scanner.Scan(&account.ID, &account.TenantID, &account.Name, &scopeJSON, &disabled, &expiresAt, &createdAt, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ServiceAccount{}, ErrNotFound
		}
		return model.ServiceAccount{}, err
	}
	account.Disabled = disabled == 1
	if err := json.Unmarshal([]byte(scopeJSON), &account.Scopes); err != nil {
		return model.ServiceAccount{}, err
	}
	created, err := parseTime(createdAt)
	if err != nil {
		return model.ServiceAccount{}, err
	}
	account.CreatedAt = created
	if expiresAt.Valid {
		parsed, err := parseTime(expiresAt.String)
		if err != nil {
			return model.ServiceAccount{}, err
		}
		account.ExpiresAt = &parsed
	}
	if revokedAt.Valid {
		parsed, err := parseTime(revokedAt.String)
		if err != nil {
			return model.ServiceAccount{}, err
		}
		account.RevokedAt = &parsed
	}
	return account, nil
}

func scanPromotedGuestImage(scanner interface{ Scan(...any) error }) (model.PromotedGuestImage, error) {
	var image model.PromotedGuestImage
	var profile, controlMode string
	var promotedAt sql.NullString
	if err := scanner.Scan(&image.ImageRef, &image.ImageSHA256, &profile, &controlMode, &image.ControlProtocolVersion, &image.ContractVersion, &image.ProvenanceJSON, &image.VerificationStatus, &image.PromotionStatus, &promotedAt, &image.PromotedBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.PromotedGuestImage{}, ErrNotFound
		}
		return model.PromotedGuestImage{}, err
	}
	image.Profile = model.GuestProfile(profile)
	image.ControlMode = model.GuestControlMode(controlMode)
	if promotedAt.Valid {
		parsed, err := parseTime(promotedAt.String)
		if err != nil {
			return model.PromotedGuestImage{}, err
		}
		image.PromotedAt = &parsed
	}
	return image, nil
}

func scanReleaseEvidence(scanner interface{ Scan(...any) error }) (model.ReleaseEvidence, error) {
	var evidence model.ReleaseEvidence
	var runtimeSelection, profile, started string
	var completed sql.NullString
	if err := scanner.Scan(&evidence.ID, &evidence.GateName, &evidence.HostFingerprint, &runtimeSelection, &evidence.ImageRef, &profile, &evidence.Outcome, &evidence.ArtifactPath, &started, &completed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ReleaseEvidence{}, ErrNotFound
		}
		return model.ReleaseEvidence{}, err
	}
	evidence.RuntimeSelection = model.ParseRuntimeSelection(runtimeSelection)
	evidence.Profile = model.GuestProfile(profile)
	startedAt, err := parseTime(started)
	if err != nil {
		return model.ReleaseEvidence{}, err
	}
	evidence.StartedAt = startedAt
	if completed.Valid {
		parsed, err := parseTime(completed.String)
		if err != nil {
			return model.ReleaseEvidence{}, err
		}
		evidence.CompletedAt = &parsed
	}
	return evidence, nil
}

func scanTunnelCapability(scanner interface{ Scan(...any) error }) (model.TunnelCapability, error) {
	var capability model.TunnelCapability
	var expiresAt, createdAt string
	var consumedAt, revokedAt sql.NullString
	if err := scanner.Scan(&capability.ID, &capability.TunnelID, &capability.NonceHash, &capability.Path, &expiresAt, &consumedAt, &revokedAt, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.TunnelCapability{}, ErrNotFound
		}
		return model.TunnelCapability{}, err
	}
	parsedExpiresAt, err := parseTime(expiresAt)
	if err != nil {
		return model.TunnelCapability{}, err
	}
	capability.ExpiresAt = parsedExpiresAt
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return model.TunnelCapability{}, err
	}
	capability.CreatedAt = parsedCreatedAt
	if consumedAt.Valid {
		parsed, err := parseTime(consumedAt.String)
		if err != nil {
			return model.TunnelCapability{}, err
		}
		capability.ConsumedAt = &parsed
	}
	if revokedAt.Valid {
		parsed, err := parseTime(revokedAt.String)
		if err != nil {
			return model.TunnelCapability{}, err
		}
		capability.RevokedAt = &parsed
	}
	return capability, nil
}

func parseTime(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func joinStringList(values []string) string {
	return strings.Join(values, ",")
}

func splitStringList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		result = append(result, part)
	}
	return result
}
