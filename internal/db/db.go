package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 7

// Open opens the SQLite database at path, applies connection pragmas, and runs
// schema migrations before returning.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		return nil, err
	}
	return db, nil
}

func sqliteDSN(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	values := url.Values{}
	values.Add("_pragma", "foreign_keys(1)")
	values.Add("_pragma", "journal_mode(WAL)")
	values.Add("_pragma", "synchronous(NORMAL)")
	values.Add("_pragma", "busy_timeout(5000)")
	return (&url.URL{Scheme: "file", Path: absPath, RawQuery: values.Encode()}).String(), nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (0, CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS tenants (
			tenant_id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS quotas (
			tenant_id TEXT PRIMARY KEY REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			max_sandboxes INTEGER NOT NULL,
			max_running_sandboxes INTEGER NOT NULL,
			max_concurrent_execs INTEGER NOT NULL,
			max_tunnels INTEGER NOT NULL,
			max_cpu_cores INTEGER NOT NULL,
			max_cpu_millis INTEGER NOT NULL DEFAULT 0,
			max_memory_mb INTEGER NOT NULL,
			max_storage_mb INTEGER NOT NULL,
			allow_tunnels INTEGER NOT NULL,
			default_tunnel_auth_mode TEXT NOT NULL,
			default_tunnel_visibility TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS sandboxes (
			sandbox_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			status TEXT NOT NULL,
			runtime_selection TEXT NOT NULL DEFAULT '',
			runtime_backend TEXT NOT NULL,
			base_image_ref TEXT NOT NULL,
			profile TEXT NOT NULL DEFAULT '',
			feature_set TEXT NOT NULL DEFAULT '',
			capability_set TEXT NOT NULL DEFAULT '',
			control_mode TEXT NOT NULL DEFAULT '',
			control_protocol_version TEXT NOT NULL DEFAULT '',
			workspace_contract_version TEXT NOT NULL DEFAULT '',
			image_contract_version TEXT NOT NULL DEFAULT '',
			cpu_limit INTEGER NOT NULL,
			cpu_limit_millis INTEGER NOT NULL DEFAULT 0,
			memory_limit_mb INTEGER NOT NULL,
			pids_limit INTEGER NOT NULL,
			disk_limit_mb INTEGER NOT NULL,
			network_mode TEXT NOT NULL,
			allow_tunnels INTEGER NOT NULL,
			storage_root TEXT NOT NULL,
			workspace_root TEXT NOT NULL,
			cache_root TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_active_at TEXT NOT NULL,
			deleted_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS sandbox_runtime_state (
			sandbox_id TEXT PRIMARY KEY REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			runtime_id TEXT NOT NULL,
			runtime_status TEXT NOT NULL,
			last_runtime_error TEXT NOT NULL DEFAULT '',
			ip_address TEXT NOT NULL DEFAULT '',
			pid INTEGER NOT NULL DEFAULT 0,
			started_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS sandbox_storage (
			sandbox_id TEXT PRIMARY KEY REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			rootfs_bytes INTEGER NOT NULL DEFAULT 0,
			workspace_bytes INTEGER NOT NULL DEFAULT 0,
			cache_bytes INTEGER NOT NULL DEFAULT 0,
			snapshot_bytes INTEGER NOT NULL DEFAULT 0,
			rootfs_entries INTEGER NOT NULL DEFAULT 0,
			workspace_entries INTEGER NOT NULL DEFAULT 0,
			cache_entries INTEGER NOT NULL DEFAULT 0,
			snapshot_entries INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS tunnels (
			tunnel_id TEXT PRIMARY KEY,
			sandbox_id TEXT NOT NULL REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			target_port INTEGER NOT NULL,
			protocol TEXT NOT NULL,
			auth_mode TEXT NOT NULL,
			auth_secret_hash TEXT NOT NULL DEFAULT '',
			visibility TEXT NOT NULL,
			endpoint TEXT NOT NULL,
			created_at TEXT NOT NULL,
			revoked_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS snapshots (
			snapshot_id TEXT PRIMARY KEY,
			sandbox_id TEXT NOT NULL REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			status TEXT NOT NULL,
			image_ref TEXT NOT NULL,
			runtime_selection TEXT NOT NULL DEFAULT '',
			runtime_backend TEXT NOT NULL DEFAULT '',
			profile TEXT NOT NULL DEFAULT '',
			image_contract_version TEXT NOT NULL DEFAULT '',
			control_protocol_version TEXT NOT NULL DEFAULT '',
			workspace_contract_version TEXT NOT NULL DEFAULT '',
			workspace_tar TEXT NOT NULL,
			bundle_sha256 TEXT NOT NULL DEFAULT '',
			export_location TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			completed_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS service_accounts (
			service_account_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			scope_json TEXT NOT NULL DEFAULT '[]',
			disabled INTEGER NOT NULL DEFAULT 0,
			expires_at TEXT,
			created_at TEXT NOT NULL,
			revoked_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS promoted_guest_images (
			image_ref TEXT PRIMARY KEY,
			image_sha256 TEXT NOT NULL,
			profile TEXT NOT NULL,
			control_mode TEXT NOT NULL,
			control_protocol_version TEXT NOT NULL,
			contract_version TEXT NOT NULL,
			provenance_json TEXT NOT NULL DEFAULT '',
			verification_status TEXT NOT NULL,
			promotion_status TEXT NOT NULL,
			promoted_at TEXT,
			promoted_by TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE TABLE IF NOT EXISTS release_evidence (
			evidence_id TEXT PRIMARY KEY,
			gate_name TEXT NOT NULL,
			host_fingerprint TEXT NOT NULL,
			runtime_selection TEXT NOT NULL DEFAULT '',
			image_ref TEXT NOT NULL DEFAULT '',
			profile TEXT NOT NULL DEFAULT '',
			outcome TEXT NOT NULL,
			artifact_path TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL,
			completed_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS tunnel_capabilities (
			capability_id TEXT PRIMARY KEY,
			tunnel_id TEXT NOT NULL REFERENCES tunnels(tunnel_id) ON DELETE CASCADE,
			nonce_hash TEXT NOT NULL,
			path TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			consumed_at TEXT,
			revoked_at TEXT,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS executions (
			execution_id TEXT PRIMARY KEY,
			sandbox_id TEXT NOT NULL REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			command TEXT NOT NULL,
			cwd TEXT NOT NULL,
			timeout_seconds INTEGER NOT NULL,
			status TEXT NOT NULL,
			exit_code INTEGER,
			stdout_preview TEXT NOT NULL DEFAULT '',
			stderr_preview TEXT NOT NULL DEFAULT '',
			stdout_truncated INTEGER NOT NULL DEFAULT 0,
			stderr_truncated INTEGER NOT NULL DEFAULT 0,
			started_at TEXT NOT NULL,
			completed_at TEXT,
			duration_ms INTEGER
		);`,
		`CREATE TABLE IF NOT EXISTS tty_sessions (
			tty_session_id TEXT PRIMARY KEY,
			sandbox_id TEXT NOT NULL REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			command TEXT NOT NULL,
			connected INTEGER NOT NULL,
			last_resize TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			closed_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			audit_event_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			sandbox_id TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL,
			resource_id TEXT NOT NULL DEFAULT '',
			outcome TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS audit_event_counts (
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			action TEXT NOT NULL,
			outcome TEXT NOT NULL,
			total INTEGER NOT NULL,
			PRIMARY KEY (tenant_id, action, outcome)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_sandboxes_tenant_status_created ON sandboxes(tenant_id, status, created_at);`,
		`CREATE INDEX IF NOT EXISTS idx_sandboxes_status ON sandboxes(status);`,
		`CREATE INDEX IF NOT EXISTS idx_executions_tenant_status ON executions(tenant_id, status);`,
		`CREATE INDEX IF NOT EXISTS idx_tunnels_tenant_sandbox_revoked ON tunnels(tenant_id, sandbox_id, revoked_at);`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_tenant_status ON snapshots(tenant_id, status);`,
		`CREATE INDEX IF NOT EXISTS idx_service_accounts_tenant_id ON service_accounts(tenant_id);`,
		`CREATE INDEX IF NOT EXISTS idx_release_evidence_gate_started ON release_evidence(gate_name, started_at);`,
		`CREATE INDEX IF NOT EXISTS idx_tunnel_capabilities_tunnel_id ON tunnel_capabilities(tunnel_id);`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_tenant_created ON audit_events(tenant_id, created_at);`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_tenant_action_outcome ON audit_events(tenant_id, action, outcome);`,
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := ensureColumn(ctx, tx, "tunnels", "auth_secret_hash", `ALTER TABLE tunnels ADD COLUMN auth_secret_hash TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "cpu_limit_millis", `ALTER TABLE sandboxes ADD COLUMN cpu_limit_millis INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "profile", `ALTER TABLE sandboxes ADD COLUMN profile TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "feature_set", `ALTER TABLE sandboxes ADD COLUMN feature_set TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "capability_set", `ALTER TABLE sandboxes ADD COLUMN capability_set TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "control_mode", `ALTER TABLE sandboxes ADD COLUMN control_mode TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "control_protocol_version", `ALTER TABLE sandboxes ADD COLUMN control_protocol_version TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "workspace_contract_version", `ALTER TABLE sandboxes ADD COLUMN workspace_contract_version TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "image_contract_version", `ALTER TABLE sandboxes ADD COLUMN image_contract_version TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "quotas", "max_cpu_millis", `ALTER TABLE quotas ADD COLUMN max_cpu_millis INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "snapshots", "profile", `ALTER TABLE snapshots ADD COLUMN profile TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "snapshots", "runtime_backend", `ALTER TABLE snapshots ADD COLUMN runtime_backend TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "snapshots", "image_contract_version", `ALTER TABLE snapshots ADD COLUMN image_contract_version TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "snapshots", "control_protocol_version", `ALTER TABLE snapshots ADD COLUMN control_protocol_version TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "snapshots", "workspace_contract_version", `ALTER TABLE snapshots ADD COLUMN workspace_contract_version TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "snapshots", "bundle_sha256", `ALTER TABLE snapshots ADD COLUMN bundle_sha256 TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "runtime_class", `ALTER TABLE sandboxes ADD COLUMN runtime_class TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "runtime_selection", `ALTER TABLE sandboxes ADD COLUMN runtime_selection TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "snapshots", "runtime_selection", `ALTER TABLE snapshots ADD COLUMN runtime_selection TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandbox_storage", "rootfs_entries", `ALTER TABLE sandbox_storage ADD COLUMN rootfs_entries INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandbox_storage", "workspace_entries", `ALTER TABLE sandbox_storage ADD COLUMN workspace_entries INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandbox_storage", "cache_entries", `ALTER TABLE sandbox_storage ADD COLUMN cache_entries INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandbox_storage", "snapshot_entries", `ALTER TABLE sandbox_storage ADD COLUMN snapshot_entries INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sandboxes SET cpu_limit_millis = cpu_limit * 1000 WHERE cpu_limit_millis = 0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quotas SET max_cpu_millis = max_cpu_cores * 1000 WHERE max_cpu_millis = 0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sandboxes SET runtime_selection = CASE runtime_backend WHEN 'docker' THEN 'docker-dev' WHEN 'qemu' THEN 'qemu-professional' WHEN 'kata' THEN 'containerd-kata-professional' ELSE runtime_selection END WHERE runtime_selection = ''`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE snapshots SET runtime_selection = CASE runtime_backend WHEN 'docker' THEN 'docker-dev' WHEN 'qemu' THEN 'qemu-professional' WHEN 'kata' THEN 'containerd-kata-professional' ELSE runtime_selection END WHERE runtime_selection = ''`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM audit_event_counts`); err != nil {
		return fmt.Errorf("rebuild audit event counts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_event_counts(tenant_id, action, outcome, total)
		SELECT tenant_id, action, outcome, COUNT(*)
		FROM audit_events
		GROUP BY tenant_id, action, outcome
	`); err != nil {
		return fmt.Errorf("rebuild audit event counts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO schema_migrations(version, applied_at) VALUES (?, ?)`, schemaVersion, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureColumn(ctx context.Context, tx *sql.Tx, table, column, alterSQL string) error {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, alterSQL)
	return err
}
