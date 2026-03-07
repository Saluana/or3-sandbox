package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/db"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
)

func TestGuestFileOpsUseRuntimeBoundaryAndTenantIsolation(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	runtime.storageUsage = model.StorageUsage{
		RootfsBytes:    10,
		WorkspaceBytes: 20,
		CacheBytes:     30,
		SnapshotBytes:  40,
	}
	svc, store, quota, tenantA, tenantB := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      1,
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	runtime.reads["notes/hello.txt"] = "hello"
	if err := svc.Mkdir(ctx, tenantA.ID, sandbox.ID, "notes"); err != nil {
		t.Fatalf("mkdir via runtime: %v", err)
	}
	if err := svc.WriteFile(ctx, tenantA.ID, sandbox.ID, "notes/hello.txt", "hello"); err != nil {
		t.Fatalf("write via runtime: %v", err)
	}
	file, err := svc.ReadFile(ctx, tenantA.ID, sandbox.ID, "notes/hello.txt")
	if err != nil {
		t.Fatalf("read via runtime: %v", err)
	}
	if file.Content != "hello" {
		t.Fatalf("unexpected file content %q", file.Content)
	}
	if err := svc.DeleteFile(ctx, tenantA.ID, sandbox.ID, "notes/hello.txt"); err != nil {
		t.Fatalf("delete via runtime: %v", err)
	}

	if len(runtime.mkdirs) != 1 || runtime.mkdirs[0] != "notes" {
		t.Fatalf("expected mkdir call for notes, got %#v", runtime.mkdirs)
	}
	if len(runtime.writes) != 1 || runtime.writes[0].path != "notes/hello.txt" || runtime.writes[0].content != "hello" {
		t.Fatalf("unexpected runtime writes: %#v", runtime.writes)
	}
	if len(runtime.deletes) != 1 || runtime.deletes[0] != "notes/hello.txt" {
		t.Fatalf("unexpected runtime deletes: %#v", runtime.deletes)
	}
	if _, err := os.Stat(filepath.Join(sandbox.WorkspaceRoot, "notes", "hello.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected guest backend to avoid host workspace writes, got %v", err)
	}

	usage, err := store.TenantUsage(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("tenant usage: %v", err)
	}
	if usage.ActualStorageBytes != 100 {
		t.Fatalf("unexpected measured storage: got %d want 100", usage.ActualStorageBytes)
	}

	if _, err := svc.ReadFile(ctx, tenantB.ID, sandbox.ID, "notes/hello.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected cross-tenant read denial, got %v", err)
	}
	if err := svc.WriteFile(ctx, tenantA.ID, sandbox.ID, "../escape.txt", "nope"); err == nil {
		t.Fatal("expected traversal write to fail")
	}
	if len(runtime.writes) != 1 {
		t.Fatalf("expected no traversal write to reach runtime, got %#v", runtime.writes)
	}
}

func TestLocalFileOpsStayScopedToWorkspaceAndMeasuredStorage(t *testing.T) {
	ctx := context.Background()
	runtime := newRuntimeOnlyStub()
	svc, store, quota, tenantA, tenantB := newServiceHarness(t, runtime, "docker")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		CPULimit:      1,
		MemoryLimitMB: 256,
		PIDsLimit:     64,
		DiskLimitMB:   256,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	if err := svc.Mkdir(ctx, tenantA.ID, sandbox.ID, "nested"); err != nil {
		t.Fatalf("mkdir fallback: %v", err)
	}
	if err := svc.WriteFile(ctx, tenantA.ID, sandbox.ID, "nested/value.txt", "42"); err != nil {
		t.Fatalf("write fallback: %v", err)
	}
	file, err := svc.ReadFile(ctx, tenantA.ID, sandbox.ID, "nested/value.txt")
	if err != nil {
		t.Fatalf("read fallback: %v", err)
	}
	if file.Content != "42" {
		t.Fatalf("unexpected file content %q", file.Content)
	}
	if _, err := os.Stat(filepath.Join(sandbox.WorkspaceRoot, "nested", "value.txt")); err != nil {
		t.Fatalf("expected host workspace file: %v", err)
	}
	usage, err := store.TenantUsage(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("tenant usage: %v", err)
	}
	if usage.ActualStorageBytes < 2 {
		t.Fatalf("expected actual storage to include workspace file, got %d", usage.ActualStorageBytes)
	}
	if err := svc.WriteFile(ctx, tenantA.ID, sandbox.ID, "../escape.txt", "nope"); err == nil {
		t.Fatal("expected traversal write to fail")
	}
	if _, err := svc.ReadFile(ctx, tenantB.ID, sandbox.ID, "nested/value.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected cross-tenant read denial, got %v", err)
	}
}

func TestLifecycleRefreshStorageUsesRuntimeMeasurement(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	runtime.startState = model.RuntimeState{RuntimeID: "rt-start", Status: model.SandboxStatusRunning, Running: true}
	runtime.stopState = model.RuntimeState{RuntimeID: "rt-stop", Status: model.SandboxStatusStopped}
	runtime.restoreState = model.RuntimeState{RuntimeID: "rt-restore", Status: model.SandboxStatusStopped}
	runtime.inspectState = model.RuntimeState{RuntimeID: "rt-inspect", Status: model.SandboxStatusRunning, Running: true}
	runtime.snapshotInfo = model.SnapshotInfo{ImageRef: "snapshot-image"}

	runtime.storageUsage = model.StorageUsage{RootfsBytes: 100, WorkspaceBytes: 200, CacheBytes: 30, SnapshotBytes: 0}
	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      1,
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	assertActualStorage(t, store, tenantA.ID, 330)

	runtime.storageUsage = model.StorageUsage{RootfsBytes: 90, WorkspaceBytes: 200, CacheBytes: 30, SnapshotBytes: 0}
	if _, err := svc.StopSandbox(ctx, tenantA.ID, sandbox.ID, false); err != nil {
		t.Fatalf("stop sandbox: %v", err)
	}
	assertActualStorage(t, store, tenantA.ID, 320)

	runtime.storageUsage = model.StorageUsage{RootfsBytes: 95, WorkspaceBytes: 200, CacheBytes: 30, SnapshotBytes: 0}
	if _, err := svc.StartSandbox(ctx, tenantA.ID, sandbox.ID, quota); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	assertActualStorage(t, store, tenantA.ID, 325)

	runtime.storageUsage = model.StorageUsage{RootfsBytes: 95, WorkspaceBytes: 200, CacheBytes: 30, SnapshotBytes: 75}
	snapshot, err := svc.CreateSnapshot(ctx, tenantA.ID, sandbox.ID, model.CreateSnapshotRequest{Name: "snap"})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	assertActualStorage(t, store, tenantA.ID, 400)

	runtime.storageUsage = model.StorageUsage{RootfsBytes: 110, WorkspaceBytes: 210, CacheBytes: 30, SnapshotBytes: 75}
	if _, err := svc.RestoreSnapshot(ctx, tenantA.ID, snapshot.ID, model.RestoreSnapshotRequest{TargetSandboxID: sandbox.ID}); err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}
	assertActualStorage(t, store, tenantA.ID, 425)

	runtime.storageUsage = model.StorageUsage{RootfsBytes: 120, WorkspaceBytes: 220, CacheBytes: 30, SnapshotBytes: 80}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	assertActualStorage(t, store, tenantA.ID, 450)
}

func TestCreateSnapshotPersistsArtifactsOutsideSandboxRoot(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      1,
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	originalRootfs := filepath.Join(sandbox.StorageRoot, ".snapshots", "snap-raw", "rootfs.img")
	originalWorkspace := filepath.Join(sandbox.StorageRoot, ".snapshots", "snap-raw", "workspace.img")
	for path, content := range map[string]string{
		originalRootfs:    "rootfs-snapshot",
		originalWorkspace: "workspace-snapshot",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir snapshot fixture: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write snapshot fixture %s: %v", path, err)
		}
	}
	runtime.snapshotInfo = model.SnapshotInfo{
		ImageRef:     originalRootfs,
		WorkspaceTar: originalWorkspace,
	}

	snapshot, err := svc.CreateSnapshot(ctx, tenantA.ID, sandbox.ID, model.CreateSnapshotRequest{Name: "snap"})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	if !strings.HasPrefix(snapshot.ImageRef, filepath.Join(svc.cfg.SnapshotRoot, sandbox.ID)) {
		t.Fatalf("expected rootfs snapshot under snapshot root, got %q", snapshot.ImageRef)
	}
	if !strings.HasPrefix(snapshot.WorkspaceTar, filepath.Join(svc.cfg.SnapshotRoot, sandbox.ID)) {
		t.Fatalf("expected workspace snapshot under snapshot root, got %q", snapshot.WorkspaceTar)
	}
	if _, err := os.Stat(originalRootfs); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected original rootfs snapshot to be removed, got %v", err)
	}
	if _, err := os.Stat(originalWorkspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected original workspace snapshot to be removed, got %v", err)
	}
	usage, err := store.TenantUsage(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("tenant usage: %v", err)
	}
	if usage.ActualStorageBytes < int64(len("rootfs-snapshot")+len("workspace-snapshot")) {
		t.Fatalf("expected storage accounting to include durable snapshots, got %d", usage.ActualStorageBytes)
	}
}

func assertActualStorage(t *testing.T, store *repository.Store, tenantID string, want int64) {
	t.Helper()
	usage, err := store.TenantUsage(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("tenant usage: %v", err)
	}
	if usage.ActualStorageBytes != want {
		t.Fatalf("unexpected actual storage: got %d want %d", usage.ActualStorageBytes, want)
	}
}

func newServiceHarness(t *testing.T, runtime model.RuntimeManager, backend string) (*Service, *repository.Store, model.TenantQuota, model.Tenant, model.Tenant) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		DatabasePath:         filepath.Join(root, "sandbox.db"),
		StorageRoot:          filepath.Join(root, "storage"),
		SnapshotRoot:         filepath.Join(root, "snapshots"),
		BaseImageRef:         "base-image",
		RuntimeBackend:       backend,
		DefaultCPULimit:      1,
		DefaultMemoryLimitMB: 256,
		DefaultPIDsLimit:     64,
		DefaultDiskLimitMB:   256,
		DefaultNetworkMode:   model.NetworkModeInternetDisabled,
		DefaultAllowTunnels:  false,
		OperatorHost:         "http://operator.invalid",
	}
	sqlDB, err := db.Open(context.Background(), cfg.DatabasePath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	store := repository.New(sqlDB)
	quota := model.TenantQuota{
		MaxSandboxes:            8,
		MaxRunningSandboxes:     8,
		MaxConcurrentExecs:      8,
		MaxTunnels:              8,
		MaxCPUCores:             8,
		MaxMemoryMB:             8192,
		MaxStorageMB:            8192,
		AllowTunnels:            false,
		DefaultTunnelAuthMode:   "token",
		DefaultTunnelVisibility: "private",
	}
	if err := store.SeedTenants(context.Background(), []config.TenantConfig{
		{ID: "tenant-a", Name: "Tenant A", Token: "token-a"},
		{ID: "tenant-b", Name: "Tenant B", Token: "token-b"},
	}, quota); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	return New(cfg, store, runtime), store, quota, model.Tenant{ID: "tenant-a", Name: "Tenant A"}, model.Tenant{ID: "tenant-b", Name: "Tenant B"}
}

func boolPtr(value bool) *bool {
	return &value
}

type stubRuntime struct {
	createState  model.RuntimeState
	startState   model.RuntimeState
	stopState    model.RuntimeState
	restoreState model.RuntimeState
	inspectState model.RuntimeState
	snapshotInfo model.SnapshotInfo
	storageUsage model.StorageUsage

	reads   map[string]string
	writes  []stubWrite
	deletes []string
	mkdirs  []string
}

type stubWrite struct {
	path    string
	content string
}

func newStubRuntime() *stubRuntime {
	return &stubRuntime{
		reads: make(map[string]string),
	}
}

type runtimeOnlyStub struct {
	createState  model.RuntimeState
	startState   model.RuntimeState
	stopState    model.RuntimeState
	restoreState model.RuntimeState
	inspectState model.RuntimeState
	snapshotInfo model.SnapshotInfo
}

func newRuntimeOnlyStub() *runtimeOnlyStub {
	return &runtimeOnlyStub{}
}

func (r *stubRuntime) Create(context.Context, model.SandboxSpec) (model.RuntimeState, error) {
	return withDefaultRuntimeState(r.createState, model.SandboxStatusStopped, false), nil
}

func (r *stubRuntime) Start(context.Context, model.Sandbox) (model.RuntimeState, error) {
	return withDefaultRuntimeState(r.startState, model.SandboxStatusRunning, true), nil
}

func (r *stubRuntime) Stop(context.Context, model.Sandbox, bool) (model.RuntimeState, error) {
	return withDefaultRuntimeState(r.stopState, model.SandboxStatusStopped, false), nil
}

func (r *stubRuntime) Suspend(context.Context, model.Sandbox) (model.RuntimeState, error) {
	return model.RuntimeState{}, errors.New("not implemented")
}

func (r *stubRuntime) Resume(context.Context, model.Sandbox) (model.RuntimeState, error) {
	return model.RuntimeState{}, errors.New("not implemented")
}

func (r *stubRuntime) Destroy(context.Context, model.Sandbox) error {
	return nil
}

func (r *stubRuntime) Inspect(context.Context, model.Sandbox) (model.RuntimeState, error) {
	return withDefaultRuntimeState(r.inspectState, model.SandboxStatusStopped, false), nil
}

func (r *stubRuntime) Exec(context.Context, model.Sandbox, model.ExecRequest, model.ExecStreams) (model.ExecHandle, error) {
	return nil, errors.New("not implemented")
}

func (r *stubRuntime) AttachTTY(context.Context, model.Sandbox, model.TTYRequest) (model.TTYHandle, error) {
	return nil, errors.New("not implemented")
}

func (r *stubRuntime) CreateSnapshot(context.Context, model.Sandbox, string) (model.SnapshotInfo, error) {
	if r.snapshotInfo.ImageRef == "" {
		return model.SnapshotInfo{ImageRef: "snapshot-image"}, nil
	}
	return r.snapshotInfo, nil
}

func (r *stubRuntime) RestoreSnapshot(context.Context, model.Sandbox, model.Snapshot) (model.RuntimeState, error) {
	return withDefaultRuntimeState(r.restoreState, model.SandboxStatusStopped, false), nil
}

func (r *stubRuntime) ReadWorkspaceFile(_ context.Context, _ model.Sandbox, relativePath string) (model.FileReadResponse, error) {
	content, ok := r.reads[relativePath]
	if !ok {
		return model.FileReadResponse{}, os.ErrNotExist
	}
	return model.FileReadResponse{
		Path:     relativePath,
		Content:  content,
		Size:     int64(len(content)),
		Encoding: "utf-8",
	}, nil
}

func (r *stubRuntime) WriteWorkspaceFile(_ context.Context, _ model.Sandbox, relativePath string, content string) error {
	r.writes = append(r.writes, stubWrite{path: relativePath, content: content})
	r.reads[relativePath] = content
	return nil
}

func (r *stubRuntime) DeleteWorkspacePath(_ context.Context, _ model.Sandbox, relativePath string) error {
	r.deletes = append(r.deletes, relativePath)
	delete(r.reads, relativePath)
	return nil
}

func (r *stubRuntime) MkdirWorkspace(_ context.Context, _ model.Sandbox, relativePath string) error {
	r.mkdirs = append(r.mkdirs, relativePath)
	return nil
}

func (r *stubRuntime) MeasureStorage(context.Context, model.Sandbox) (model.StorageUsage, error) {
	return r.storageUsage, nil
}

func withDefaultRuntimeState(state model.RuntimeState, status model.SandboxStatus, running bool) model.RuntimeState {
	if state.RuntimeID == "" {
		state.RuntimeID = "runtime-id"
	}
	if state.Status == "" {
		state.Status = status
	}
	if state.Status == model.SandboxStatusRunning {
		state.Running = true
	}
	if !running && state.Status != model.SandboxStatusRunning {
		state.Running = false
	}
	return state
}

var _ model.RuntimeManager = (*stubRuntime)(nil)
var _ workspaceFileRuntime = (*stubRuntime)(nil)
var _ storageMeasurer = (*stubRuntime)(nil)

func (r *runtimeOnlyStub) Create(context.Context, model.SandboxSpec) (model.RuntimeState, error) {
	return withDefaultRuntimeState(r.createState, model.SandboxStatusStopped, false), nil
}

func (r *runtimeOnlyStub) Start(context.Context, model.Sandbox) (model.RuntimeState, error) {
	return withDefaultRuntimeState(r.startState, model.SandboxStatusRunning, true), nil
}

func (r *runtimeOnlyStub) Stop(context.Context, model.Sandbox, bool) (model.RuntimeState, error) {
	return withDefaultRuntimeState(r.stopState, model.SandboxStatusStopped, false), nil
}

func (r *runtimeOnlyStub) Suspend(context.Context, model.Sandbox) (model.RuntimeState, error) {
	return model.RuntimeState{}, errors.New("not implemented")
}

func (r *runtimeOnlyStub) Resume(context.Context, model.Sandbox) (model.RuntimeState, error) {
	return model.RuntimeState{}, errors.New("not implemented")
}

func (r *runtimeOnlyStub) Destroy(context.Context, model.Sandbox) error {
	return nil
}

func (r *runtimeOnlyStub) Inspect(context.Context, model.Sandbox) (model.RuntimeState, error) {
	return withDefaultRuntimeState(r.inspectState, model.SandboxStatusStopped, false), nil
}

func (r *runtimeOnlyStub) Exec(context.Context, model.Sandbox, model.ExecRequest, model.ExecStreams) (model.ExecHandle, error) {
	return nil, errors.New("not implemented")
}

func (r *runtimeOnlyStub) AttachTTY(context.Context, model.Sandbox, model.TTYRequest) (model.TTYHandle, error) {
	return nil, errors.New("not implemented")
}

func (r *runtimeOnlyStub) CreateSnapshot(context.Context, model.Sandbox, string) (model.SnapshotInfo, error) {
	if r.snapshotInfo.ImageRef == "" {
		return model.SnapshotInfo{ImageRef: "snapshot-image"}, nil
	}
	return r.snapshotInfo, nil
}

func (r *runtimeOnlyStub) RestoreSnapshot(context.Context, model.Sandbox, model.Snapshot) (model.RuntimeState, error) {
	return withDefaultRuntimeState(r.restoreState, model.SandboxStatusStopped, false), nil
}

var _ model.RuntimeManager = (*runtimeOnlyStub)(nil)
