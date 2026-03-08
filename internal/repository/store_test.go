package repository

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/db"
	"or3-sandbox/internal/model"
)

func TestGetSandboxReturnsErrorForInvalidStoredTimestamp(t *testing.T) {
	root := t.TempDir()
	sqlDB, err := db.Open(context.Background(), filepath.Join(root, "sandbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	store := New(sqlDB)
	quota := model.TenantQuota{
		MaxSandboxes:            1,
		MaxRunningSandboxes:     1,
		MaxConcurrentExecs:      1,
		MaxTunnels:              1,
		MaxCPUCores:             model.CPUCores(1),
		MaxMemoryMB:             512,
		MaxStorageMB:            512,
		AllowTunnels:            true,
		DefaultTunnelAuthMode:   "token",
		DefaultTunnelVisibility: "private",
	}
	if err := store.SeedTenants(context.Background(), []config.TenantConfig{{ID: "tenant-a", Name: "Tenant A", Token: "token-a"}}, quota); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), `
		INSERT INTO sandboxes(
			sandbox_id, tenant_id, status, runtime_backend, base_image_ref,
			cpu_limit, cpu_limit_millis, memory_limit_mb, pids_limit, disk_limit_mb,
			network_mode, allow_tunnels, storage_root, workspace_root, cache_root,
			created_at, updated_at, last_active_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`, "sbx-bad", "tenant-a", string(model.SandboxStatusStopped), "qemu", "guest-base.qcow2", 1, 1000, 256, 64, 512, string(model.NetworkModeInternetDisabled), 0, filepath.Join(root, "rootfs"), filepath.Join(root, "workspace"), filepath.Join(root, "cache"), "not-a-time", "not-a-time", "not-a-time"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), `
		INSERT INTO sandbox_runtime_state(sandbox_id, runtime_id, runtime_status, last_runtime_error, ip_address, pid, started_at)
		VALUES (?, ?, ?, '', '', 0, NULL)
	`, "sbx-bad", "runtime-bad", string(model.SandboxStatusStopped)); err != nil {
		t.Fatal(err)
	}
	_, err = store.GetSandbox(context.Background(), "tenant-a", "sbx-bad")
	if err == nil || !strings.Contains(err.Error(), "cannot parse") {
		t.Fatalf("expected timestamp parse error, got %v", err)
	}
}