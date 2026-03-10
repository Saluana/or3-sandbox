package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenCreatesMillicoreColumns(t *testing.T) {
	database, err := Open(context.Background(), t.TempDir()+"/sandbox.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	for _, tc := range []struct {
		table  string
		column string
	}{
		{table: "sandboxes", column: "cpu_limit_millis"},
		{table: "sandboxes", column: "runtime_selection"},
		{table: "quotas", column: "max_cpu_millis"},
		{table: "snapshots", column: "runtime_selection"},
	} {
		rows, err := database.QueryContext(context.Background(), "PRAGMA table_info("+tc.table+")")
		if err != nil {
			t.Fatalf("pragma %s: %v", tc.table, err)
		}
		found := false
		for rows.Next() {
			var cid int
			var name, dataType string
			var notNull int
			var defaultValue any
			var pk int
			if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				t.Fatalf("scan pragma row: %v", err)
			}
			if name == tc.column {
				found = true
				break
			}
		}
		rows.Close()
		if !found {
			t.Fatalf("missing %s column on %s", tc.column, tc.table)
		}
	}
}

func TestMigrateBackfillsRuntimeSelectionFromLegacyBackend(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatalf("sqlite dsn: %v", err)
	}
	legacyDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer legacyDB.Close()

	for _, stmt := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (5, CURRENT_TIMESTAMP);`,
		`CREATE TABLE sandboxes (
			sandbox_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
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
			deleted_at TEXT,
			runtime_class TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE TABLE snapshots (
			snapshot_id TEXT PRIMARY KEY,
			sandbox_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL,
			image_ref TEXT NOT NULL,
			runtime_backend TEXT NOT NULL DEFAULT '',
			profile TEXT NOT NULL DEFAULT '',
			image_contract_version TEXT NOT NULL DEFAULT '',
			control_protocol_version TEXT NOT NULL DEFAULT '',
			workspace_contract_version TEXT NOT NULL DEFAULT '',
			workspace_tar TEXT NOT NULL,
			export_location TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			completed_at TEXT
		);`,
	} {
		if _, err := legacyDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed legacy schema: %v", err)
		}
	}

	if _, err := legacyDB.ExecContext(ctx, `
		INSERT INTO sandboxes(
			sandbox_id, tenant_id, status, runtime_backend, base_image_ref,
			cpu_limit, memory_limit_mb, pids_limit, disk_limit_mb,
			network_mode, allow_tunnels, storage_root, workspace_root, cache_root,
			created_at, updated_at, last_active_at, deleted_at, runtime_class
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, NULL, '')
	`, "sbx-legacy", "tenant-a", "stopped", "qemu", "guest-base.qcow2", 1, 512, 64, 512, "internet-disabled", 0, "/tmp/rootfs", "/tmp/workspace", "/tmp/cache"); err != nil {
		t.Fatalf("insert legacy sandbox: %v", err)
	}
	if _, err := legacyDB.ExecContext(ctx, `
		INSERT INTO snapshots(
			snapshot_id, sandbox_id, tenant_id, name, status, image_ref,
			runtime_backend, profile, image_contract_version, control_protocol_version,
			workspace_contract_version, workspace_tar, export_location, created_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, '', '', '', '', '', '', CURRENT_TIMESTAMP, NULL)
	`, "snap-legacy", "sbx-legacy", "tenant-a", "legacy", "ready", "snapshot-image", "docker"); err != nil {
		t.Fatalf("insert legacy snapshot: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	defer migrated.Close()

	var sandboxSelection string
	if err := migrated.QueryRowContext(ctx, `SELECT runtime_selection FROM sandboxes WHERE sandbox_id=?`, "sbx-legacy").Scan(&sandboxSelection); err != nil {
		t.Fatalf("query migrated sandbox: %v", err)
	}
	if sandboxSelection != "qemu-professional" {
		t.Fatalf("unexpected sandbox runtime_selection %q", sandboxSelection)
	}

	var snapshotSelection string
	if err := migrated.QueryRowContext(ctx, `SELECT runtime_selection FROM snapshots WHERE snapshot_id=?`, "snap-legacy").Scan(&snapshotSelection); err != nil {
		t.Fatalf("query migrated snapshot: %v", err)
	}
	if snapshotSelection != "docker-dev" {
		t.Fatalf("unexpected snapshot runtime_selection %q", snapshotSelection)
	}
}
