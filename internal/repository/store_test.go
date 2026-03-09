package repository

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func newStoreTestHarness(t *testing.T) (*Store, func()) {
	t.Helper()
	root := t.TempDir()
	sqlDB, err := db.Open(context.Background(), filepath.Join(root, "sandbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	store := New(sqlDB)
	quota := model.TenantQuota{
		MaxSandboxes:            10,
		MaxRunningSandboxes:     5,
		MaxConcurrentExecs:      4,
		MaxTunnels:              4,
		MaxCPUCores:             model.CPUCores(8),
		MaxMemoryMB:             4096,
		MaxStorageMB:            10240,
		AllowTunnels:            true,
		DefaultTunnelAuthMode:   "token",
		DefaultTunnelVisibility: "private",
	}
	if err := store.SeedTenants(context.Background(), []config.TenantConfig{{ID: "tenant-a", Name: "Tenant A", Token: "token-a"}}, quota); err != nil {
		t.Fatal(err)
	}
	return store, func() { sqlDB.Close() }
}

// TestRuntimeClassRoundTrip verifies that a sandbox created with an explicit
// runtime class stores and retrieves the class without loss.
func TestRuntimeClassRoundTrip(t *testing.T) {
	store, close := newStoreTestHarness(t)
	defer close()
	root := t.TempDir()
	now := time.Now().UTC()

	sb := model.Sandbox{
		ID:             "sbx-rt-class",
		TenantID:       "tenant-a",
		Status:         model.SandboxStatusStopped,
		RuntimeBackend: "qemu",
		RuntimeClass:   model.RuntimeClassVM,
		BaseImageRef:   "guest-base.qcow2",
		CPULimit:       model.CPUCores(1),
		MemoryLimitMB:  512,
		PIDsLimit:      64,
		DiskLimitMB:    1024,
		NetworkMode:    model.NetworkModeInternetDisabled,
		StorageRoot:    filepath.Join(root, "rootfs"),
		WorkspaceRoot:  filepath.Join(root, "workspace"),
		CacheRoot:      filepath.Join(root, "cache"),
		RuntimeID:      "sbx-rt-class",
		RuntimeStatus:  string(model.SandboxStatusStopped),
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActiveAt:   now,
	}
	if err := store.CreateSandbox(context.Background(), sb); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	got, err := store.GetSandbox(context.Background(), "tenant-a", "sbx-rt-class")
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if got.RuntimeClass != model.RuntimeClassVM {
		t.Errorf("runtime class round-trip: got %q, want %q", got.RuntimeClass, model.RuntimeClassVM)
	}
	if got.RuntimeBackend != "qemu" {
		t.Errorf("runtime backend round-trip: got %q, want %q", got.RuntimeBackend, "qemu")
	}
}

// TestRuntimeClassDerivedFromLegacyRow verifies that sandboxes created before
// the runtime_class column was added are reconciled using BackendToRuntimeClass.
func TestRuntimeClassDerivedFromLegacyRow(t *testing.T) {
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
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Insert a sandbox row without runtime_class (simulating a pre-migration row
	// by explicitly setting the column to empty string after the fact).
	if _, err := sqlDB.ExecContext(context.Background(), `
		INSERT INTO sandboxes(
			sandbox_id, tenant_id, status, runtime_backend, base_image_ref,
			cpu_limit, cpu_limit_millis, memory_limit_mb, pids_limit, disk_limit_mb,
			network_mode, allow_tunnels, storage_root, workspace_root, cache_root,
			created_at, updated_at, last_active_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`, "sbx-legacy", "tenant-a", string(model.SandboxStatusStopped), "docker", "alpine:3.20",
		1, 1000, 256, 64, 512, string(model.NetworkModeInternetDisabled), 0,
		filepath.Join(root, "rootfs"), filepath.Join(root, "workspace"), filepath.Join(root, "cache"),
		now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), `
		INSERT INTO sandbox_runtime_state(sandbox_id, runtime_id, runtime_status, last_runtime_error, ip_address, pid, started_at)
		VALUES (?, ?, ?, '', '', 0, NULL)
	`, "sbx-legacy", "sbx-legacy", string(model.SandboxStatusStopped)); err != nil {
		t.Fatal(err)
	}
	// Explicitly clear runtime_class to simulate a legacy row.
	if _, err := sqlDB.ExecContext(context.Background(), `UPDATE sandboxes SET runtime_class='' WHERE sandbox_id=?`, "sbx-legacy"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetSandbox(context.Background(), "tenant-a", "sbx-legacy")
	if err != nil {
		t.Fatalf("get legacy sandbox: %v", err)
	}
	// When runtime_class is empty the store must derive it from the backend.
	if got.RuntimeClass != model.RuntimeClassTrustedDocker {
		t.Errorf("legacy docker row: expected RuntimeClass %q, got %q", model.RuntimeClassTrustedDocker, got.RuntimeClass)
	}
}