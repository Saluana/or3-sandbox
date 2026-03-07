package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`PRAGMA foreign_keys = ON;`,
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
			runtime_backend TEXT NOT NULL,
			base_image_ref TEXT NOT NULL,
			cpu_limit INTEGER NOT NULL,
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
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS tunnels (
			tunnel_id TEXT PRIMARY KEY,
			sandbox_id TEXT NOT NULL REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			target_port INTEGER NOT NULL,
			protocol TEXT NOT NULL,
			auth_mode TEXT NOT NULL,
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
			workspace_tar TEXT NOT NULL,
			export_location TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			completed_at TEXT
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
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO schema_migrations(version, applied_at) VALUES (?, ?)`, schemaVersion, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}
