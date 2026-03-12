package service

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"or3-sandbox/internal/auth"
	"or3-sandbox/internal/config"
	"or3-sandbox/internal/db"
	"or3-sandbox/internal/dockerimage"
	"or3-sandbox/internal/guestimage"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
	runtimeregistry "or3-sandbox/internal/runtime/registry"
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
		CPULimit:      model.CPUCores(1),
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
	file, err := svc.ReadFile(ctx, tenantA.ID, sandbox.ID, "notes/hello.txt", "")
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

	if _, err := svc.ReadFile(ctx, tenantB.ID, sandbox.ID, "notes/hello.txt", ""); !errors.Is(err, repository.ErrNotFound) {
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
		CPULimit:      model.CPUCores(1),
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
	file, err := svc.ReadFile(ctx, tenantA.ID, sandbox.ID, "nested/value.txt", "")
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
	if _, err := svc.ReadFile(ctx, tenantB.ID, sandbox.ID, "nested/value.txt", ""); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected cross-tenant read denial, got %v", err)
	}
}

func TestLocalFileOpsRejectWorkspaceSymlinkEscapes(t *testing.T) {
	ctx := context.Background()
	runtime := newRuntimeOnlyStub()
	svc, _, quota, tenant, _ := newServiceHarness(t, runtime, "docker")

	sandbox, err := svc.CreateSandbox(ctx, tenant, quota, model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		CPULimit:      model.CPUCores(1),
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

	outsideRoot := t.TempDir()
	outsideFile := filepath.Join(outsideRoot, "host.txt")
	if err := os.WriteFile(outsideFile, []byte("host"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	linkPath := filepath.Join(sandbox.WorkspaceRoot, "escape")
	if err := os.Symlink(outsideRoot, linkPath); err != nil {
		t.Fatalf("symlink outside root: %v", err)
	}

	if _, err := svc.ReadFile(ctx, tenant.ID, sandbox.ID, "escape/host.txt", ""); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink read rejection, got %v", err)
	}
	if err := svc.WriteFile(ctx, tenant.ID, sandbox.ID, "escape/new.txt", "nope"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink write rejection, got %v", err)
	}
	if err := svc.Mkdir(ctx, tenant.ID, sandbox.ID, "escape/nested"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink mkdir rejection, got %v", err)
	}
	if err := svc.DeleteFile(ctx, tenant.ID, sandbox.ID, "escape/host.txt"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink delete rejection, got %v", err)
	}
	if data, err := os.ReadFile(outsideFile); err != nil || string(data) != "host" {
		t.Fatalf("expected outside file to remain unchanged, got %q err=%v", string(data), err)
	}
}

func TestLocalFileReadsRejectOversizeTransfers(t *testing.T) {
	ctx := context.Background()
	runtime := newRuntimeOnlyStub()
	svc, _, quota, tenant, _ := newServiceHarness(t, runtime, "docker")

	sandbox, err := svc.CreateSandbox(ctx, tenant, quota, model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		CPULimit:      model.CPUCores(1),
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

	target := filepath.Join(sandbox.WorkspaceRoot, "large.bin")
	file, err := os.Create(target)
	if err != nil {
		t.Fatalf("create oversize file: %v", err)
	}
	if err := file.Truncate(model.MaxWorkspaceFileTransferBytes + 1); err != nil {
		file.Close()
		t.Fatalf("truncate oversize file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close oversize file: %v", err)
	}

	if _, err := svc.ReadFile(ctx, tenant.ID, sandbox.ID, "large.bin", ""); !errors.Is(err, model.ErrFileTransferTooLarge) {
		t.Fatalf("expected oversize read rejection, got %v", err)
	}
}

func TestOpenSandboxLocalConnUsesRuntimeBridge(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})
	runtime.localConnFactory = func(context.Context, model.Sandbox, int) (net.Conn, error) {
		return clientConn, nil
	}
	runtime.startState = model.RuntimeState{RuntimeID: "rt-bridge", Status: model.SandboxStatusRunning, Running: true}
	svc, _, quota, tenant, _ := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenant, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	conn, err := svc.OpenSandboxLocalConn(ctx, sandbox, 8080)
	if err != nil {
		t.Fatalf("open sandbox local conn: %v", err)
	}
	defer conn.Close()

	go func() {
		_, _ = serverConn.Write([]byte("hello"))
		buf := make([]byte, 5)
		_, _ = io.ReadFull(serverConn, buf)
	}()

	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read bridged bytes: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("unexpected bridged payload %q", string(buf))
	}
	if _, err := conn.Write([]byte("world")); err != nil {
		t.Fatalf("write bridged bytes: %v", err)
	}
}

func TestOpenSandboxLocalConnRejectsInvalidTargetPort(t *testing.T) {
	runtime := newStubRuntime()
	svc, _, _, _, _ := newServiceHarness(t, runtime, "qemu")
	_, err := svc.OpenSandboxLocalConn(context.Background(), model.Sandbox{ID: "sbx-port", Status: model.SandboxStatusRunning}, 70000)
	if err == nil || !strings.Contains(err.Error(), "target port must be between 1 and 65535") {
		t.Fatalf("expected target port validation error, got %v", err)
	}
}

func TestOpenSandboxLocalConnPropagatesEOF(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	serverConn, clientConn := net.Pipe()
	runtime.localConnFactory = func(context.Context, model.Sandbox, int) (net.Conn, error) {
		return clientConn, nil
	}
	runtime.startState = model.RuntimeState{RuntimeID: "rt-bridge-eof", Status: model.SandboxStatusRunning, Running: true}
	svc, _, quota, tenant, _ := newServiceHarness(t, runtime, "qemu")
	sandbox, err := svc.CreateSandbox(ctx, tenant, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	conn, err := svc.OpenSandboxLocalConn(ctx, sandbox, 9090)
	if err != nil {
		t.Fatalf("open sandbox local conn: %v", err)
	}
	defer conn.Close()
	go func() {
		_, _ = serverConn.Write([]byte("ok"))
		_ = serverConn.Close()
	}()
	data := make([]byte, 2)
	if _, err := io.ReadFull(conn, data); err != nil {
		t.Fatalf("read bridged bytes: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("unexpected bridged bytes %q", string(data))
	}
	buf := make([]byte, 1)
	if n, err := conn.Read(buf); err != io.EOF || n != 0 {
		t.Fatalf("expected EOF after bridged peer close, got n=%d err=%v", n, err)
	}
}

func TestOpenSandboxLocalConnUnavailablePortFailsDeterministically(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	runtime.localConnErr = errors.New("sandbox-local tunnel bridge failed to connect: connection refused")
	runtime.startState = model.RuntimeState{RuntimeID: "rt-bridge-fail", Status: model.SandboxStatusRunning, Running: true}
	svc, _, quota, tenant, _ := newServiceHarness(t, runtime, "qemu")
	sandbox, err := svc.CreateSandbox(ctx, tenant, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	_, err = svc.OpenSandboxLocalConn(ctx, sandbox, 1234)
	if err == nil || !strings.Contains(err.Error(), "failed to connect") {
		t.Fatalf("expected deterministic bridge failure, got %v", err)
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
		CPULimit:      model.CPUCores(1),
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

	runtime.storageUsage = model.StorageUsage{RootfsBytes: 95, WorkspaceBytes: 200, CacheBytes: 30, SnapshotBytes: 0}
	if _, err := svc.StopSandbox(ctx, tenantA.ID, sandbox.ID, false); err != nil {
		t.Fatalf("stop sandbox before snapshot: %v", err)
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
	restored, err := svc.GetSandbox(ctx, tenantA.ID, sandbox.ID)
	if err != nil {
		t.Fatalf("get restored sandbox: %v", err)
	}
	if restored.BaseImageRef != svc.cfg.QEMUBaseImagePath {
		t.Fatalf("expected restored qemu sandbox to keep canonical base image ref, got %q", restored.BaseImageRef)
	}
	assertActualStorage(t, store, tenantA.ID, 425)

	runtime.storageUsage = model.StorageUsage{RootfsBytes: 120, WorkspaceBytes: 220, CacheBytes: 30, SnapshotBytes: 80}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	assertActualStorage(t, store, tenantA.ID, 450)
}

func TestCreateSandboxBuildsScratchSecretsRootsAndNetworkPolicy(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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
	if got := filepath.Base(runtime.createdSpec.ScratchRoot); got != "scratch" {
		t.Fatalf("expected scratch root, got %q", runtime.createdSpec.ScratchRoot)
	}
	if got := filepath.Base(runtime.createdSpec.SecretsRoot); got != "secrets" {
		t.Fatalf("expected secrets root, got %q", runtime.createdSpec.SecretsRoot)
	}
	if runtime.createdSpec.NetworkPolicy.Internet {
		t.Fatal("expected internet-disabled network policy")
	}
	if !runtime.createdSpec.NetworkPolicy.LoopbackOnly {
		t.Fatal("expected loopback-only network policy")
	}
	for _, dir := range []string{runtime.createdSpec.ScratchRoot, runtime.createdSpec.SecretsRoot} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("expected storage root %q to exist, got %v", dir, err)
		}
	}
}

func TestCreateSnapshotPersistsArtifactsOutsideSandboxRoot(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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

func TestCreateSnapshotMarksErrorOnPartialFailure(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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
	validRootfs := filepath.Join(sandbox.StorageRoot, ".snapshots", "snap-raw", "rootfs.img")
	if err := os.MkdirAll(filepath.Dir(validRootfs), 0o755); err != nil {
		t.Fatalf("mkdir snapshot dir: %v", err)
	}
	if err := os.WriteFile(validRootfs, []byte("rootfs"), 0o644); err != nil {
		t.Fatalf("write rootfs snapshot: %v", err)
	}
	runtime.snapshotInfo = model.SnapshotInfo{
		ImageRef:     validRootfs,
		WorkspaceTar: filepath.Join(sandbox.StorageRoot, ".snapshots", "snap-raw", "missing.img"),
	}

	snapshot, err := svc.CreateSnapshot(ctx, tenantA.ID, sandbox.ID, model.CreateSnapshotRequest{Name: "broken"})
	if err == nil {
		t.Fatal("expected snapshot create to fail")
	}
	stored, err := store.GetSnapshot(ctx, tenantA.ID, snapshot.ID)
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	if stored.Status != model.SnapshotStatusError {
		t.Fatalf("expected snapshot error status, got %#v", stored)
	}
}

func TestQEMUCreateRejectsUnallowedGuestImagePath(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	rogueImage := filepath.Join(t.TempDir(), "rogue.qcow2")
	if err := os.WriteFile(rogueImage, []byte("qcow2"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  rogueImage,
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected qemu image allowlist rejection, got %v", err)
	}
}

func TestFailedQEMUCreateRollsBackSandboxAndStorage(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	runtime.createErr = errors.New("guest image broken")
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	if err == nil {
		t.Fatal("expected create to fail")
	}
	usage, err := store.TenantUsage(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("tenant usage: %v", err)
	}
	if usage.Sandboxes != 0 || usage.RequestedStorage != 0 {
		t.Fatalf("expected failed create rollback to release quota usage, got %+v", usage)
	}
	entries, err := svc.ListSandboxes(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if len(entries) != 1 || entries[0].Status != model.SandboxStatusDeleted {
		t.Fatalf("expected rolled back sandbox to be recorded as deleted, got %+v", entries)
	}
	if _, err := os.Stat(filepath.Join(svc.cfg.StorageRoot, entries[0].ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected rolled back storage to be removed, got %v", err)
	}
}

func TestQEMUSnapshotRequiresStoppedSandbox(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	runtime.startState = model.RuntimeState{RuntimeID: "rt-running", Status: model.SandboxStatusRunning, Running: true}

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	if _, err := svc.CreateSnapshot(ctx, tenantA.ID, sandbox.ID, model.CreateSnapshotRequest{Name: "live"}); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("expected qemu stopped-snapshot guard, got %v", err)
	}
}

func TestRestoreSnapshotFetchesExportBundleWhenLocalArtifactsAreMissing(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	svc.cfg.OptionalSnapshotExport = filepath.Join(filepath.Dir(svc.cfg.SnapshotRoot), "exports")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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
	originalRootfs := filepath.Join(sandbox.StorageRoot, ".snapshots", "snap-export", "rootfs.img")
	originalWorkspace := filepath.Join(sandbox.StorageRoot, ".snapshots", "snap-export", "workspace.img")
	for path, content := range map[string]string{
		originalRootfs:    "rootfs-export",
		originalWorkspace: "workspace-export",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir snapshot fixture: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write snapshot fixture %s: %v", path, err)
		}
	}
	runtime.snapshotInfo = model.SnapshotInfo{ImageRef: originalRootfs, WorkspaceTar: originalWorkspace}

	snapshot, err := svc.CreateSnapshot(ctx, tenantA.ID, sandbox.ID, model.CreateSnapshotRequest{Name: "exported"})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	if snapshot.ExportLocation == "" {
		t.Fatal("expected export location to be recorded")
	}
	if err := os.Remove(snapshot.ImageRef); err != nil {
		t.Fatalf("remove local rootfs snapshot: %v", err)
	}
	if err := os.Remove(snapshot.WorkspaceTar); err != nil {
		t.Fatalf("remove local workspace snapshot: %v", err)
	}

	if _, err := svc.RestoreSnapshot(ctx, tenantA.ID, snapshot.ID, model.RestoreSnapshotRequest{TargetSandboxID: sandbox.ID}); err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}
	for _, path := range []string{snapshot.ImageRef, snapshot.WorkspaceTar} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected restored snapshot artifact %s: %v", path, err)
		}
	}
}

func TestRestoreSnapshotRebindsExportedArtifactsForDifferentTargetSandbox(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	svc.cfg.OptionalSnapshotExport = filepath.Join(filepath.Dir(svc.cfg.SnapshotRoot), "exports")

	source, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	if err != nil {
		t.Fatalf("create source sandbox: %v", err)
	}
	target, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	if err != nil {
		t.Fatalf("create target sandbox: %v", err)
	}
	originalRootfs := filepath.Join(source.StorageRoot, ".snapshots", "snap-export", "rootfs.img")
	originalWorkspace := filepath.Join(source.StorageRoot, ".snapshots", "snap-export", "workspace.img")
	for path, content := range map[string]string{
		originalRootfs:    "rootfs-export",
		originalWorkspace: "workspace-export",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir snapshot fixture: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write snapshot fixture %s: %v", path, err)
		}
	}
	runtime.snapshotInfo = model.SnapshotInfo{ImageRef: originalRootfs, WorkspaceTar: originalWorkspace}

	snapshot, err := svc.CreateSnapshot(ctx, tenantA.ID, source.ID, model.CreateSnapshotRequest{Name: "exported-cross-restore"})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	if err := os.Remove(snapshot.ImageRef); err != nil {
		t.Fatalf("remove local rootfs snapshot: %v", err)
	}
	if err := os.Remove(snapshot.WorkspaceTar); err != nil {
		t.Fatalf("remove local workspace snapshot: %v", err)
	}

	if _, err := svc.RestoreSnapshot(ctx, tenantA.ID, snapshot.ID, model.RestoreSnapshotRequest{TargetSandboxID: target.ID}); err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}
	expectedRootfs := filepath.Join(svc.cfg.SnapshotRoot, target.ID, snapshot.ID, filepath.Base(snapshot.ImageRef))
	expectedWorkspace := filepath.Join(svc.cfg.SnapshotRoot, target.ID, snapshot.ID, filepath.Base(snapshot.WorkspaceTar))
	if runtime.restoredSnapshot.ImageRef != expectedRootfs {
		t.Fatalf("expected rebound image ref %q, got %q", expectedRootfs, runtime.restoredSnapshot.ImageRef)
	}
	if runtime.restoredSnapshot.WorkspaceTar != expectedWorkspace {
		t.Fatalf("expected rebound workspace tar %q, got %q", expectedWorkspace, runtime.restoredSnapshot.WorkspaceTar)
	}
	for _, path := range []string{expectedRootfs, expectedWorkspace} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected rebound snapshot artifact %s: %v", path, err)
		}
	}
}

func TestRestoreSnapshotRejectsRuntimeCompatibilityMismatch(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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

	snapshot, err := svc.CreateSnapshot(ctx, tenantA.ID, sandbox.ID, model.CreateSnapshotRequest{Name: "snap"})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	snapshot.RuntimeSelection = model.RuntimeSelectionDockerDev
	snapshot.RuntimeBackend = sandbox.RuntimeBackend
	if err := store.UpdateSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("update snapshot: %v", err)
	}

	if _, err := svc.RestoreSnapshot(ctx, tenantA.ID, snapshot.ID, model.RestoreSnapshotRequest{TargetSandboxID: sandbox.ID}); err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatalf("expected runtime compatibility error, got %v", err)
	}
}

func TestRestoreSnapshotRejectsIncompatibleSnapshotBeforeStoppingRunningTarget(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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
	snapshot, err := svc.CreateSnapshot(ctx, tenantA.ID, sandbox.ID, model.CreateSnapshotRequest{Name: "snap"})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	snapshot.RuntimeSelection = model.RuntimeSelectionDockerDev
	snapshot.RuntimeBackend = sandbox.RuntimeBackend
	if err := store.UpdateSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("update snapshot: %v", err)
	}
	runtime.startState = model.RuntimeState{RuntimeID: "rt-start", Status: model.SandboxStatusRunning, Running: true}
	runtime.stopErr = errors.New("stop should not be called")
	if _, err := svc.StartSandbox(ctx, tenantA.ID, sandbox.ID, quota); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}

	if _, err := svc.RestoreSnapshot(ctx, tenantA.ID, snapshot.ID, model.RestoreSnapshotRequest{TargetSandboxID: sandbox.ID}); err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatalf("expected runtime compatibility error, got %v", err)
	}
}

func TestCreateSnapshotPersistsRuntimeSelection(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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

	snapshot, err := svc.CreateSnapshot(ctx, tenantA.ID, sandbox.ID, model.CreateSnapshotRequest{Name: "snap"})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	if snapshot.RuntimeSelection != model.RuntimeSelectionQEMUProfessional {
		t.Fatalf("expected snapshot runtime selection to be stamped, got %q", snapshot.RuntimeSelection)
	}
	persisted, err := store.GetSnapshot(ctx, tenantA.ID, snapshot.ID)
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	if persisted.RuntimeSelection != model.RuntimeSelectionQEMUProfessional {
		t.Fatalf("expected persisted snapshot runtime selection, got %q", persisted.RuntimeSelection)
	}
}

func TestExtractTarGzRejectsEscapingEntries(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "escape.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	payload := []byte("owned")
	header := &tar.Header{Name: "../escape.txt", Mode: 0o644, Size: int64(len(payload))}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	destination := t.TempDir()
	if err := extractTarGz(archivePath, destination); err == nil || !strings.Contains(err.Error(), "escapes destination") {
		t.Fatalf("expected escape rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(destination), "escape.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no file outside destination, got %v", err)
	}
}

func TestRuntimeHealthReportsBootingState(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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
	runtime.inspectState = model.RuntimeState{
		RuntimeID: "rt-booting",
		Status:    model.SandboxStatusBooting,
		Pid:       4242,
		Error:     "still booting",
	}

	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	reconciled, err := svc.GetSandbox(ctx, tenantA.ID, sandbox.ID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if reconciled.Status != model.SandboxStatusBooting {
		t.Fatalf("expected booting status after reconcile, got %s", reconciled.Status)
	}

	health, err := svc.RuntimeHealth(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("runtime health: %v", err)
	}
	if len(health.Sandboxes) != 1 || health.Sandboxes[0].ObservedStatus != model.SandboxStatusBooting {
		t.Fatalf("unexpected runtime health: %+v", health)
	}
	if health.RuntimeSelectionCounts[string(model.RuntimeSelectionQEMUProfessional)] != 1 {
		t.Fatalf("expected qemu runtime selection count in runtime health, got %+v", health.RuntimeSelectionCounts)
	}
}

func TestRuntimeHealthMarksDegradedGuestsUnhealthy(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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
	runtime.inspectState = model.RuntimeState{
		RuntimeID: "rt-degraded",
		Status:    model.SandboxStatusDegraded,
		Pid:       4242,
		Error:     "ssh readiness failed",
	}

	health, err := svc.RuntimeHealth(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("runtime health: %v", err)
	}
	if health.Healthy {
		t.Fatalf("expected degraded runtime health to be unhealthy, got %+v", health)
	}
	if len(health.Sandboxes) != 1 || health.Sandboxes[0].SandboxID != sandbox.ID || health.Sandboxes[0].ObservedStatus != model.SandboxStatusDegraded {
		t.Fatalf("unexpected runtime health payload: %+v", health)
	}
	if health.Sandboxes[0].RuntimeSelection != model.RuntimeSelectionQEMUProfessional {
		t.Fatalf("expected runtime selection in runtime health entry, got %+v", health.Sandboxes[0])
	}
}

func TestStartSandboxPolicyDenialPreservesStoppedState(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	svc.cfg.PolicyMaxIdleTimeout = time.Minute

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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
	sandbox.LastActiveAt = time.Now().UTC().Add(-2 * time.Minute)
	sandbox.UpdatedAt = time.Now().UTC()
	if err := store.UpdateSandboxState(ctx, sandbox); err != nil {
		t.Fatalf("age sandbox activity: %v", err)
	}

	if _, err := svc.StartSandbox(ctx, tenantA.ID, sandbox.ID, quota); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("expected policy denial, got %v", err)
	}

	stored, err := svc.GetSandbox(ctx, tenantA.ID, sandbox.ID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if stored.Status != model.SandboxStatusStopped {
		t.Fatalf("expected stopped sandbox after denied start, got %+v", stored)
	}
}

func TestCreateSandboxPolicyAllowsAndDeniesImages(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "docker")
	svc.cfg.PolicyAllowedImages = []string{"ghcr.io/acme/*", "guest-base.qcow2"}

	if _, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "ghcr.io/acme/app:1",
		Profile:       model.GuestProfileCore,
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	}); err != nil {
		t.Fatalf("expected allowed image to succeed, got %v", err)
	}

	if _, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "docker.io/library/alpine:3.20",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	}); err == nil || !strings.Contains(err.Error(), "not allowed by policy") {
		t.Fatalf("expected policy denial, got %v", err)
	}

	events, err := store.ListAuditEvents(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Action != "policy.create" || events[len(events)-1].Outcome != "denied" {
		t.Fatalf("expected policy denial audit event, got %+v", events)
	}
}

func TestLifecyclePolicyRejectsDisabledPersistedRuntimeSelection(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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

	svc.cfg.EnabledRuntimeSelections = []model.RuntimeSelection{model.RuntimeSelectionDockerDev}
	if _, err := svc.StartSandbox(ctx, tenantA.ID, sandbox.ID, quota); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("expected disabled runtime selection denial, got %v", err)
	}
}

func TestCreateSandboxRejectsDangerousDockerFeaturesByDefault(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "docker")

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		Features:      []string{"docker.host-network"},
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	})
	if !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("expected docker feature denial, got %v", err)
	}
	events, err := store.ListAuditEvents(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Action != "policy.create" || events[len(events)-1].Outcome != "denied" {
		t.Fatalf("expected docker policy denial audit event, got %+v", events)
	}
}

func TestCreateSandboxRejectsDangerousDockerCapabilityOverrideByDefault(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "docker")

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		Capabilities:  []string{"docker.elevated-user"},
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	})
	if !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("expected docker capability denial, got %v", err)
	}
}

func TestCreateSandboxAuditsAllowedDockerCapabilityOverride(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "docker")
	svc.cfg.DockerAllowDangerousOverrides = true

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		Capabilities:  []string{"docker.elevated-user", "docker.extra-cap:net_bind_service"},
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	})
	if err != nil {
		t.Fatalf("create sandbox with allowed docker override: %v", err)
	}
	if got := runtime.createdSpec.Capabilities; len(got) != 2 || got[0] != "docker.elevated-user" {
		t.Fatalf("expected normalized docker capabilities in runtime spec, got %#v", got)
	}
	if len(sandbox.Capabilities) != 2 {
		t.Fatalf("expected persisted docker capabilities, got %#v", sandbox.Capabilities)
	}
	events, err := store.ListAuditEvents(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Action == "policy.create.override" && event.Outcome == "ok" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected policy.create.override audit event, got %+v", events)
	}
}

func TestCreateSandboxRejectsDockerProfileMismatch(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "docker")

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		Profile:       model.GuestProfileBrowser,
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	})
	if err == nil || !strings.Contains(err.Error(), "does not match requested profile") {
		t.Fatalf("expected docker profile mismatch, got %v", err)
	}
}

func TestCreateSandboxRejectsDockerImageMissingMetadata(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "docker")

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "registry.example.com/custom/app:1",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	})
	if err == nil || !strings.Contains(err.Error(), "missing curated profile metadata") {
		t.Fatalf("expected missing metadata error, got %v", err)
	}
}

func TestCreateSandboxAcceptsCustomDockerImageWithExplicitProfile(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "docker")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "registry.example.com/custom/app:1",
		Profile:       model.GuestProfileRuntime,
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	})
	if err != nil {
		t.Fatalf("expected explicit profile to allow custom docker image, got %v", err)
	}
	if sandbox.Profile != model.GuestProfileRuntime {
		t.Fatalf("expected runtime profile to persist, got %+v", sandbox)
	}
}

func TestCreateSandboxRejectsDangerousDockerProfileByDefault(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "docker")
	svc.cfg.AllowedGuestProfiles = []model.GuestProfile{model.GuestProfileCore, model.GuestProfileContainer, model.GuestProfileDebug}
	svc.cfg.DangerousGuestProfiles = []model.GuestProfile{model.GuestProfileContainer, model.GuestProfileDebug}

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "or3-sandbox/base-container:latest",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	})
	if !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("expected dangerous profile denial, got %v", err)
	}
	events, err := store.ListAuditEvents(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) == 0 || !strings.Contains(events[len(events)-1].Message, "dangerous-profile") {
		t.Fatalf("expected dangerous profile audit detail, got %+v", events)
	}
}

func TestCreateSandboxAuditsAllowedDangerousProfileOverride(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "docker")
	svc.cfg.AllowedGuestProfiles = []model.GuestProfile{model.GuestProfileCore, model.GuestProfileContainer}
	svc.cfg.DangerousGuestProfiles = []model.GuestProfile{model.GuestProfileContainer}
	svc.cfg.AllowDangerousProfiles = true

	if _, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "or3-sandbox/base-container:latest",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	}); err != nil {
		t.Fatalf("expected dangerous profile create to succeed when explicitly allowed, got %v", err)
	}
	events, err := store.ListAuditEvents(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Action == "policy.profile.override" && event.Outcome == "ok" && strings.Contains(event.Message, "dangerous_profile_explicitly_allowed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected dangerous profile override audit event, got %+v", events)
	}
}

func TestCreateSandboxAdmissionRejectsNodeRunningPressure(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	runtime.startState = model.RuntimeState{RuntimeID: "rt-running", Status: model.SandboxStatusRunning, Running: true}
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	svc.cfg.AdmissionMaxNodeRunning = 1

	if _, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	}); err != nil {
		t.Fatalf("create first sandbox: %v", err)
	}

	if _, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	}); err == nil || !strings.Contains(err.Error(), "node running admission limit reached") {
		t.Fatalf("expected node running admission denial, got %v", err)
	}
	events, err := store.ListAuditEvents(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Action != "admission.create" {
		t.Fatalf("expected admission.create audit event, got %+v", events)
	}
}

func TestStartSandboxAdmissionRejectsBurstyTenantWhenConfigured(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	runtime.startState = model.RuntimeState{RuntimeID: "rt-start", Status: model.SandboxStatusRunning, Running: true}
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	svc.cfg.AdmissionMaxTenantStarts = 1

	first, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	if err != nil {
		t.Fatalf("create first sandbox: %v", err)
	}
	second, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	if err != nil {
		t.Fatalf("create second sandbox: %v", err)
	}
	first.Status = model.SandboxStatusStarting
	first.RuntimeStatus = string(model.SandboxStatusStarting)
	first.UpdatedAt = time.Now().UTC()
	if err := store.UpdateSandboxState(ctx, first); err != nil {
		t.Fatalf("update first sandbox state: %v", err)
	}

	if _, err := svc.StartSandbox(ctx, tenantA.ID, second.ID, quota); err == nil || !strings.Contains(err.Error(), "tenant concurrent start limit reached") {
		t.Fatalf("expected tenant start admission denial, got %v", err)
	}
	metrics, err := svc.MetricsReport(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("metrics report: %v", err)
	}
	if !strings.Contains(metrics, `or3_sandbox_admission_denials_total{action="admission.start"} 1`) {
		t.Fatalf("expected admission denial metric, got %s", metrics)
	}
}

func TestCreateSandboxAdmissionIsAtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	startBlock := make(chan struct{})
	startEntered := make(chan struct{}, 1)
	runtime.startBlock = startBlock
	runtime.startEntered = startEntered
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	svc.cfg.AdmissionMaxNodeRunning = 1

	result := make(chan error, 1)
	go func() {
		_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
			BaseImageRef:  "guest-base.qcow2",
			CPULimit:      model.CPUCores(1),
			MemoryLimitMB: 512,
			PIDsLimit:     128,
			DiskLimitMB:   512,
			NetworkMode:   model.NetworkModeInternetDisabled,
			AllowTunnels:  boolPtr(false),
			Start:         true,
		})
		result <- err
	}()

	select {
	case <-startEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first sandbox start reservation")
	}

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	})
	if err == nil || !strings.Contains(err.Error(), "node running admission limit reached") {
		t.Fatalf("expected concurrent create admission denial, got %v", err)
	}
	close(startBlock)
	if err := <-result; err != nil {
		t.Fatalf("first create failed: %v", err)
	}
}

func TestDeleteSandboxBypassesHeavyAdmissionLimit(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	svc.cfg.AdmissionMaxTenantHeavyOps = 1

	target, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	})
	if err != nil {
		t.Fatalf("create target sandbox: %v", err)
	}
	busy, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	})
	if err != nil {
		t.Fatalf("create busy sandbox: %v", err)
	}
	busy.Status = model.SandboxStatusCreating
	busy.RuntimeStatus = string(model.SandboxStatusCreating)
	busy.UpdatedAt = time.Now().UTC()
	if err := store.UpdateSandboxState(ctx, busy); err != nil {
		t.Fatalf("mark busy sandbox creating: %v", err)
	}

	if err := svc.DeleteSandbox(ctx, tenantA.ID, target.ID, false); err != nil {
		t.Fatalf("delete should bypass heavy admission pressure: %v", err)
	}
}

func TestDockerCuratedMetadataFixtureStaysInSync(t *testing.T) {
	metadata, err := dockerimage.Resolve("or3-sandbox/base-container:latest")
	if err != nil {
		t.Fatalf("resolve container metadata: %v", err)
	}
	if metadata.Profile != model.GuestProfileContainer || !metadata.Dangerous {
		t.Fatalf("unexpected metadata %+v", metadata)
	}
}

func TestQEMULifecyclePolicyAllowsLegacyDefaultImageAlias(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	runtime.startState = model.RuntimeState{RuntimeID: "rt-start", Status: model.SandboxStatusRunning, Running: true}

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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

	sandbox.BaseImageRef = svc.cfg.BaseImageRef
	sandbox.UpdatedAt = time.Now().UTC()
	if err := store.UpdateSandboxState(ctx, sandbox); err != nil {
		t.Fatalf("persist legacy image ref: %v", err)
	}

	started, err := svc.StartSandbox(ctx, tenantA.ID, sandbox.ID, quota)
	if err != nil {
		t.Fatalf("start sandbox with legacy qemu image alias: %v", err)
	}
	if started.Status != model.SandboxStatusRunning {
		t.Fatalf("unexpected sandbox after start: %+v", started)
	}
}

func TestTunnelPolicyRejectsPublicVisibility(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	svc.cfg.PolicyAllowPublicTunnels = false
	quota.AllowTunnels = true
	if err := store.SeedTenants(ctx, []config.TenantConfig{{ID: "tenant-a", Name: "Tenant A", Token: "token-a"}, {ID: "tenant-b", Name: "Tenant B", Token: "token-b"}}, quota); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(true),
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	if _, err := svc.CreateTunnel(ctx, tenantA.ID, sandbox.ID, model.CreateTunnelRequest{
		TargetPort: 8080,
		Protocol:   model.TunnelProtocolHTTP,
		AuthMode:   "token",
		Visibility: "public",
	}); err == nil || !strings.Contains(err.Error(), "disabled by policy") {
		t.Fatalf("expected public tunnel denial, got %v", err)
	}

	events, err := store.ListAuditEvents(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Action != "policy.tunnel" {
		t.Fatalf("expected tunnel policy audit event, got %+v", events)
	}
}

func TestTunnelPolicyRejectsDefaultPublicVisibility(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	svc.cfg.PolicyAllowPublicTunnels = false
	quota.AllowTunnels = true
	quota.DefaultTunnelVisibility = "public"
	if err := store.SeedTenants(ctx, []config.TenantConfig{{ID: "tenant-a", Name: "Tenant A", Token: "token-a"}}, quota); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(true),
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	if _, err := svc.CreateTunnel(ctx, tenantA.ID, sandbox.ID, model.CreateTunnelRequest{
		TargetPort: 8080,
		Protocol:   model.TunnelProtocolHTTP,
		AuthMode:   "token",
	}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("expected default public tunnel denial, got %v", err)
	}
}

func TestCreateTunnelMintsSecretForDefaultTokenAuth(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	quota.AllowTunnels = true
	quota.DefaultTunnelAuthMode = "token"
	quota.DefaultTunnelVisibility = "private"
	if err := store.SeedTenants(ctx, []config.TenantConfig{{ID: tenantA.ID, Name: tenantA.Name, Token: "token-a"}}, quota); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(true),
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	tunnel, err := svc.CreateTunnel(ctx, tenantA.ID, sandbox.ID, model.CreateTunnelRequest{TargetPort: 8080})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	if tunnel.AuthMode != "token" {
		t.Fatalf("expected default token auth mode, got %q", tunnel.AuthMode)
	}
	if tunnel.AccessToken == "" {
		t.Fatalf("expected access token to be minted")
	}
	if tunnel.AuthSecretHash == "" {
		t.Fatalf("expected auth secret hash to be persisted")
	}
	if got, want := tunnel.AuthSecretHash, config.HashToken(tunnel.AccessToken); got != want {
		t.Fatalf("expected auth secret hash %q, got %q", want, got)
	}
}

func TestCreateTunnelRejectsDenialPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("sandbox disallows tunnels", func(t *testing.T) {
		runtime := newStubRuntime()
		svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
		quota.AllowTunnels = true
		if err := store.SeedTenants(ctx, []config.TenantConfig{{ID: tenantA.ID, Name: tenantA.Name, Token: "token-a"}}, quota); err != nil {
			t.Fatalf("seed tenants: %v", err)
		}
		sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
			BaseImageRef:  "guest-base.qcow2",
			CPULimit:      model.CPUCores(1),
			MemoryLimitMB: 512,
			PIDsLimit:     128,
			DiskLimitMB:   512,
			NetworkMode:   model.NetworkModeInternetDisabled,
			AllowTunnels:  boolPtr(false),
		})
		if err != nil {
			t.Fatalf("create sandbox: %v", err)
		}
		if _, err := svc.CreateTunnel(ctx, tenantA.ID, sandbox.ID, model.CreateTunnelRequest{TargetPort: 8080}); err == nil || !strings.Contains(err.Error(), "does not allow tunnels") {
			t.Fatalf("expected sandbox tunnel denial, got %v", err)
		}
	})

	t.Run("tenant quota disallows tunnels", func(t *testing.T) {
		runtime := newStubRuntime()
		svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
		quota.AllowTunnels = true
		if err := store.SeedTenants(ctx, []config.TenantConfig{{ID: tenantA.ID, Name: tenantA.Name, Token: "token-a"}}, quota); err != nil {
			t.Fatalf("seed tenants: %v", err)
		}
		sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
			BaseImageRef:  "guest-base.qcow2",
			CPULimit:      model.CPUCores(1),
			MemoryLimitMB: 512,
			PIDsLimit:     128,
			DiskLimitMB:   512,
			NetworkMode:   model.NetworkModeInternetDisabled,
			AllowTunnels:  boolPtr(true),
		})
		if err != nil {
			t.Fatalf("create sandbox: %v", err)
		}
		quota.AllowTunnels = false
		if err := store.SeedTenants(ctx, []config.TenantConfig{{ID: tenantA.ID, Name: tenantA.Name, Token: "token-a"}}, quota); err != nil {
			t.Fatalf("update tenant quota: %v", err)
		}
		if _, err := svc.CreateTunnel(ctx, tenantA.ID, sandbox.ID, model.CreateTunnelRequest{TargetPort: 8080}); err == nil || !strings.Contains(err.Error(), "tenant tunnel policy denied") {
			t.Fatalf("expected tenant tunnel denial, got %v", err)
		}
	})

	t.Run("tunnel quota exceeded", func(t *testing.T) {
		runtime := newStubRuntime()
		svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
		quota.AllowTunnels = true
		quota.MaxTunnels = 1
		if err := store.SeedTenants(ctx, []config.TenantConfig{{ID: tenantA.ID, Name: tenantA.Name, Token: "token-a"}}, quota); err != nil {
			t.Fatalf("seed tenants: %v", err)
		}
		sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
			BaseImageRef:  "guest-base.qcow2",
			CPULimit:      model.CPUCores(1),
			MemoryLimitMB: 512,
			PIDsLimit:     128,
			DiskLimitMB:   512,
			NetworkMode:   model.NetworkModeInternetDisabled,
			AllowTunnels:  boolPtr(true),
		})
		if err != nil {
			t.Fatalf("create sandbox: %v", err)
		}
		if _, err := svc.CreateTunnel(ctx, tenantA.ID, sandbox.ID, model.CreateTunnelRequest{TargetPort: 8080}); err != nil {
			t.Fatalf("create first tunnel: %v", err)
		}
		if _, err := svc.CreateTunnel(ctx, tenantA.ID, sandbox.ID, model.CreateTunnelRequest{TargetPort: 8081}); err == nil || !strings.Contains(err.Error(), "tunnel quota exceeded") {
			t.Fatalf("expected tunnel quota denial, got %v", err)
		}
	})

	t.Run("unsupported protocol auth and visibility", func(t *testing.T) {
		runtime := newStubRuntime()
		svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
		quota.AllowTunnels = true
		if err := store.SeedTenants(ctx, []config.TenantConfig{{ID: tenantA.ID, Name: tenantA.Name, Token: "token-a"}}, quota); err != nil {
			t.Fatalf("seed tenants: %v", err)
		}
		sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
			BaseImageRef:  "guest-base.qcow2",
			CPULimit:      model.CPUCores(1),
			MemoryLimitMB: 512,
			PIDsLimit:     128,
			DiskLimitMB:   512,
			NetworkMode:   model.NetworkModeInternetDisabled,
			AllowTunnels:  boolPtr(true),
		})
		if err != nil {
			t.Fatalf("create sandbox: %v", err)
		}
		cases := []struct {
			name string
			req  model.CreateTunnelRequest
			want string
		}{
			{name: "protocol", req: model.CreateTunnelRequest{TargetPort: 8080, Protocol: "tcp"}, want: "unsupported tunnel protocol"},
			{name: "auth", req: model.CreateTunnelRequest{TargetPort: 8080, Protocol: model.TunnelProtocolHTTP, AuthMode: "mtls"}, want: "unsupported auth_mode"},
			{name: "visibility", req: model.CreateTunnelRequest{TargetPort: 8080, Protocol: model.TunnelProtocolHTTP, AuthMode: "token", Visibility: "cluster"}, want: "unsupported visibility"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := svc.CreateTunnel(ctx, tenantA.ID, sandbox.ID, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("expected %q denial, got %v", tc.want, err)
				}
			})
		}
	})
}

func TestLifecycleTransitionsRevokeActiveTunnels(t *testing.T) {
	ctx := context.Background()

	assertRevoked := func(t *testing.T, store *repository.Store, tenantID, tunnelID, wantReason string) {
		t.Helper()
		tunnel, err := store.GetTunnel(ctx, tenantID, tunnelID)
		if err != nil {
			t.Fatalf("get tunnel: %v", err)
		}
		if tunnel.RevokedAt == nil {
			t.Fatalf("expected tunnel %s to be revoked", tunnelID)
		}
		events, err := store.ListAuditEvents(ctx, tenantID)
		if err != nil {
			t.Fatalf("list audit events: %v", err)
		}
		found := false
		for _, event := range events {
			if event.Action == "tunnel.revoke" && event.ResourceID == tunnelID && strings.Contains(event.Message, wantReason) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected tunnel revoke audit for %s with reason %q, got %+v", tunnelID, wantReason, events)
		}
	}

	t.Run("stop revokes active tunnels", func(t *testing.T) {
		runtime := newStubRuntime()
		runtime.startState = model.RuntimeState{RuntimeID: "rt-stop-revoke", Status: model.SandboxStatusRunning, Running: true}
		runtime.stopState = model.RuntimeState{RuntimeID: "rt-stop-revoke", Status: model.SandboxStatusStopped}
		svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
		quota.AllowTunnels = true
		if err := store.SeedTenants(ctx, []config.TenantConfig{{ID: tenantA.ID, Name: tenantA.Name, Token: "token-a"}}, quota); err != nil {
			t.Fatalf("seed tenants: %v", err)
		}
		sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
			BaseImageRef:  "guest-base.qcow2",
			CPULimit:      model.CPUCores(1),
			MemoryLimitMB: 512,
			PIDsLimit:     128,
			DiskLimitMB:   512,
			NetworkMode:   model.NetworkModeInternetDisabled,
			AllowTunnels:  boolPtr(true),
			Start:         true,
		})
		if err != nil {
			t.Fatalf("create sandbox: %v", err)
		}
		tunnel, err := svc.CreateTunnel(ctx, tenantA.ID, sandbox.ID, model.CreateTunnelRequest{TargetPort: 8080})
		if err != nil {
			t.Fatalf("create tunnel: %v", err)
		}
		if _, err := svc.StopSandbox(ctx, tenantA.ID, sandbox.ID, false); err != nil {
			t.Fatalf("stop sandbox: %v", err)
		}
		assertRevoked(t, store, tenantA.ID, tunnel.ID, "sandbox_stop")
	})

	t.Run("suspend revokes active tunnels", func(t *testing.T) {
		runtime := newStubRuntime()
		runtime.startState = model.RuntimeState{RuntimeID: "rt-suspend-revoke", Status: model.SandboxStatusRunning, Running: true}
		svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
		quota.AllowTunnels = true
		if err := store.SeedTenants(ctx, []config.TenantConfig{{ID: tenantA.ID, Name: tenantA.Name, Token: "token-a"}}, quota); err != nil {
			t.Fatalf("seed tenants: %v", err)
		}
		sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
			BaseImageRef:  "guest-base.qcow2",
			CPULimit:      model.CPUCores(1),
			MemoryLimitMB: 512,
			PIDsLimit:     128,
			DiskLimitMB:   512,
			NetworkMode:   model.NetworkModeInternetDisabled,
			AllowTunnels:  boolPtr(true),
			Start:         true,
		})
		if err != nil {
			t.Fatalf("create sandbox: %v", err)
		}
		tunnel, err := svc.CreateTunnel(ctx, tenantA.ID, sandbox.ID, model.CreateTunnelRequest{TargetPort: 8080})
		if err != nil {
			t.Fatalf("create tunnel: %v", err)
		}
		if _, err := svc.SuspendSandbox(ctx, tenantA.ID, sandbox.ID); err != nil {
			t.Fatalf("suspend sandbox: %v", err)
		}
		assertRevoked(t, store, tenantA.ID, tunnel.ID, "sandbox_suspend")
	})

	t.Run("snapshot restore revokes active tunnels", func(t *testing.T) {
		runtime := newStubRuntime()
		runtime.snapshotInfo = model.SnapshotInfo{ImageRef: "snapshot-image"}
		runtime.restoreState = model.RuntimeState{RuntimeID: "rt-restore-revoke", Status: model.SandboxStatusStopped}
		svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
		quota.AllowTunnels = true
		if err := store.SeedTenants(ctx, []config.TenantConfig{{ID: tenantA.ID, Name: tenantA.Name, Token: "token-a"}}, quota); err != nil {
			t.Fatalf("seed tenants: %v", err)
		}
		sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
			BaseImageRef:  "guest-base.qcow2",
			CPULimit:      model.CPUCores(1),
			MemoryLimitMB: 512,
			PIDsLimit:     128,
			DiskLimitMB:   512,
			NetworkMode:   model.NetworkModeInternetDisabled,
			AllowTunnels:  boolPtr(true),
			Start:         false,
		})
		if err != nil {
			t.Fatalf("create sandbox: %v", err)
		}
		tunnel, err := svc.CreateTunnel(ctx, tenantA.ID, sandbox.ID, model.CreateTunnelRequest{TargetPort: 8080})
		if err != nil {
			t.Fatalf("create tunnel: %v", err)
		}
		snapshot, err := svc.CreateSnapshot(ctx, tenantA.ID, sandbox.ID, model.CreateSnapshotRequest{Name: "restore-revoke"})
		if err != nil {
			t.Fatalf("create snapshot: %v", err)
		}
		if _, err := svc.RestoreSnapshot(ctx, tenantA.ID, snapshot.ID, model.RestoreSnapshotRequest{TargetSandboxID: sandbox.ID}); err != nil {
			t.Fatalf("restore snapshot: %v", err)
		}
		assertRevoked(t, store, tenantA.ID, tunnel.ID, "snapshot_restore")
	})
}

func TestCapacityReportAndMetricsShowQuotaPressure(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	quota.MaxStorageMB = 1024
	runtime.storageUsage = model.StorageUsage{}
	if err := store.SeedTenants(ctx, []config.TenantConfig{{ID: tenantA.ID, Name: tenantA.Name, Token: "token-a"}}, quota); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	runtime.storageUsage = model.StorageUsage{RootfsBytes: 900 * 1024 * 1024, WorkspaceBytes: 32 * 1024 * 1024}
	if err := svc.WriteFile(ctx, tenantA.ID, sandbox.ID, "note.txt", "hello"); err != nil {
		t.Fatalf("write file: %v", err)
	}

	report, err := svc.CapacityReport(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("capacity report: %v", err)
	}
	if report.QuotaView.StoragePressure < 0.8 {
		t.Fatalf("expected storage pressure >= 0.8, got %f", report.QuotaView.StoragePressure)
	}
	if len(report.Alerts) == 0 {
		t.Fatalf("expected capacity alerts, got %+v", report)
	}
	if report.ProfileCounts["core"] != 1 {
		t.Fatalf("expected core profile count in capacity report, got %+v", report.ProfileCounts)
	}
	if report.RuntimeSelectionCounts[string(model.RuntimeSelectionQEMUProfessional)] != 1 {
		t.Fatalf("expected qemu runtime selection count in capacity report, got %+v", report.RuntimeSelectionCounts)
	}
	if report.CapabilityCounts["exec"] == 0 {
		t.Fatalf("expected exec capability count in capacity report, got %+v", report.CapabilityCounts)
	}
	metrics, err := svc.MetricsReport(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("metrics report: %v", err)
	}
	if !strings.Contains(metrics, "or3_sandbox_storage_pressure_ratio") || !strings.Contains(metrics, "or3_sandbox_runtime_status_count") || !strings.Contains(metrics, `or3_sandbox_runtime_selection_count{runtime_selection="qemu-professional"} 1`) || !strings.Contains(metrics, `or3_sandbox_profile_count{profile="core"} 1`) || !strings.Contains(metrics, `or3_sandbox_capability_count{capability="exec"} 1`) {
		t.Fatalf("unexpected metrics output: %s", metrics)
	}
}

func TestAuditEventsIncludeRuntimeSelectionForCreateRestoreDeleteAndExposure(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	quota.AllowTunnels = true
	if _, err := store.DB().ExecContext(ctx, `UPDATE quotas SET allow_tunnels=1 WHERE tenant_id=?`, tenantA.ID); err != nil {
		t.Fatalf("enable tunnels: %v", err)
	}

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(true),
		Start:         false,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	snapshot, err := svc.CreateSnapshot(ctx, tenantA.ID, sandbox.ID, model.CreateSnapshotRequest{Name: "snap"})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	if _, err := svc.CreateTunnel(ctx, tenantA.ID, sandbox.ID, model.CreateTunnelRequest{TargetPort: 8080}); err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	if _, err := svc.RestoreSnapshot(ctx, tenantA.ID, snapshot.ID, model.RestoreSnapshotRequest{TargetSandboxID: sandbox.ID}); err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}
	if err := svc.DeleteSandbox(ctx, tenantA.ID, sandbox.ID, true); err != nil {
		t.Fatalf("delete sandbox: %v", err)
	}

	events, err := store.ListAuditEvents(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	required := map[string]bool{
		"sandbox.create":   false,
		"snapshot.restore": false,
		"sandbox.delete":   false,
		"tunnel.create":    false,
	}
	for _, event := range events {
		if _, ok := required[event.Action]; ok && strings.Contains(event.Message, "runtime_selection=qemu-professional") {
			required[event.Action] = true
		}
	}
	for action, seen := range required {
		if !seen {
			t.Fatalf("expected runtime_selection in audit event %q, got %+v", action, events)
		}
	}
}

func TestMetricsReportUsesPersistedRuntimeHealth(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	if _, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	}); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	runtime.inspectCalls = 0
	runtime.inspectState = model.RuntimeState{RuntimeID: "rt-degraded", Status: model.SandboxStatusDegraded, Error: "should not be used"}

	metrics, err := svc.MetricsReport(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("metrics report: %v", err)
	}
	if runtime.inspectCalls != 0 {
		t.Fatalf("expected metrics to avoid live runtime inspect, got %d calls", runtime.inspectCalls)
	}
	if !strings.Contains(metrics, `or3_sandbox_runtime_status_count{status="creating"} 1`) && !strings.Contains(metrics, `or3_sandbox_runtime_status_count{status="stopped"} 1`) {
		t.Fatalf("expected persisted runtime status in metrics, got %s", metrics)
	}
}

func TestReconcileAcrossMixedRuntimeSelections(t *testing.T) {
	ctx := context.Background()
	qemuRuntime := newStubRuntime()
	dockerRuntime := newStubRuntime()
	runtime := runtimeregistry.New(map[model.RuntimeSelection]model.RuntimeManager{
		model.RuntimeSelectionQEMUProfessional: qemuRuntime,
		model.RuntimeSelectionDockerDev:        dockerRuntime,
	})
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	svc.cfg.EnabledRuntimeSelections = []model.RuntimeSelection{model.RuntimeSelectionQEMUProfessional, model.RuntimeSelectionDockerDev}
	svc.cfg.DefaultRuntimeSelection = model.RuntimeSelectionQEMUProfessional

	qemuRuntime.inspectState = model.RuntimeState{RuntimeID: "rt-qemu", Status: model.SandboxStatusRunning, Running: true}
	dockerRuntime.inspectState = model.RuntimeState{RuntimeID: "rt-docker", Status: model.SandboxStatusStopped}

	qemuSandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		RuntimeSelection: model.RuntimeSelectionQEMUProfessional,
		BaseImageRef:     "guest-base.qcow2",
		CPULimit:         model.CPUCores(1),
		MemoryLimitMB:    512,
		PIDsLimit:        128,
		DiskLimitMB:      512,
		NetworkMode:      model.NetworkModeInternetDisabled,
		AllowTunnels:     boolPtr(false),
		Start:            false,
	})
	if err != nil {
		t.Fatalf("create qemu sandbox: %v", err)
	}
	dockerSandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		RuntimeSelection: model.RuntimeSelectionDockerDev,
		BaseImageRef:     "docker.io/library/alpine:3.20",
		CPULimit:         model.CPUCores(1),
		MemoryLimitMB:    256,
		PIDsLimit:        64,
		DiskLimitMB:      256,
		NetworkMode:      model.NetworkModeInternetDisabled,
		AllowTunnels:     boolPtr(false),
		Start:            false,
	})
	if err != nil {
		t.Fatalf("create docker sandbox: %v", err)
	}

	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if qemuRuntime.inspectCalls == 0 {
		t.Fatal("expected qemu runtime inspect during reconcile")
	}
	if dockerRuntime.inspectCalls == 0 {
		t.Fatal("expected docker runtime inspect during reconcile")
	}
	gotQEMU, err := svc.GetSandbox(ctx, tenantA.ID, qemuSandbox.ID)
	if err != nil {
		t.Fatalf("get qemu sandbox: %v", err)
	}
	if gotQEMU.Status != model.SandboxStatusRunning {
		t.Fatalf("expected qemu sandbox to reconcile to running, got %q", gotQEMU.Status)
	}
	gotDocker, err := svc.GetSandbox(ctx, tenantA.ID, dockerSandbox.ID)
	if err != nil {
		t.Fatalf("get docker sandbox: %v", err)
	}
	if gotDocker.RuntimeSelection != model.RuntimeSelectionDockerDev {
		t.Fatalf("expected docker sandbox selection to persist, got %q", gotDocker.RuntimeSelection)
	}
}

func TestReconcileSkipsFreshStorageRefreshWhenStateIsUnchanged(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	svc.cfg.CleanupInterval = time.Hour

	runtime.startState = model.RuntimeState{RuntimeID: "rt-start", Status: model.SandboxStatusRunning, Running: true}
	runtime.inspectState = model.RuntimeState{RuntimeID: "rt-start", Status: model.SandboxStatusRunning, Running: true}
	runtime.storageUsage = model.StorageUsage{RootfsBytes: 100, WorkspaceBytes: 200, CacheBytes: 30}
	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	assertActualStorage(t, store, tenantA.ID, 330)

	runtime.storageUsage = model.StorageUsage{RootfsBytes: 999, WorkspaceBytes: 999, CacheBytes: 999}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	reconciled, err := svc.GetSandbox(ctx, tenantA.ID, sandbox.ID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if reconciled.Status != model.SandboxStatusRunning {
		t.Fatalf("expected sandbox to stay running, got %+v", reconciled)
	}
	assertActualStorage(t, store, tenantA.ID, 330)
}

func TestExecSandboxCancellationRecordsCanceledResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	runtime.startState = model.RuntimeState{RuntimeID: "rt-start", Status: model.SandboxStatusRunning, Running: true}
	runtime.execHandleFactory = func(ctx context.Context, _ model.ExecRequest) model.ExecHandle {
		ch := make(chan model.ExecResult, 1)
		go func() {
			<-ctx.Done()
			now := time.Now().UTC()
			ch <- model.ExecResult{
				ExitCode:    1,
				Status:      model.ExecutionStatusCanceled,
				StartedAt:   now,
				CompletedAt: now,
			}
			close(ch)
		}()
		return stubExecHandle{ch: ch}
	}

	sandbox, err := svc.CreateSandbox(context.Background(), tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
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
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	execution, err := svc.ExecSandbox(ctx, tenantA, quota, sandbox.ID, model.ExecRequest{
		Command: []string{"sh", "-lc", "sleep 30"},
		Cwd:     "/workspace",
		Timeout: 30 * time.Second,
	}, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("exec sandbox: %v", err)
	}
	if execution.Status != model.ExecutionStatusCanceled {
		t.Fatalf("expected canceled execution, got %+v", execution)
	}
	usage, err := store.TenantUsage(context.Background(), tenantA.ID)
	if err != nil {
		t.Fatalf("tenant usage: %v", err)
	}
	if usage.ConcurrentExecs != 0 {
		t.Fatalf("expected no running execs after cancellation, got %d", usage.ConcurrentExecs)
	}
}

func TestCreateSandboxAppliesFractionalCPUDefault(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "docker")
	svc.cfg.DefaultCPULimit = model.MustParseCPUQuantity("1500m")
	quota.MaxCPUCores = model.MustParseCPUQuantity("2500m")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		Profile:       model.GuestProfileCore,
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(false),
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if sandbox.CPULimit != model.MustParseCPUQuantity("1500m") {
		t.Fatalf("unexpected sandbox cpu %v", sandbox.CPULimit)
	}
	if runtime.createdSpec.CPULimit != model.MustParseCPUQuantity("1500m") {
		t.Fatalf("unexpected runtime spec cpu %v", runtime.createdSpec.CPULimit)
	}
}

func TestCreateSandboxRejectsFractionalCPUQuotaOverflow(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "docker")
	quota.MaxCPUCores = model.MustParseCPUQuantity("2")

	if _, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		Profile:       model.GuestProfileCore,
		CPULimit:      model.MustParseCPUQuantity("1500m"),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(false),
	}); err != nil {
		t.Fatalf("seed sandbox: %v", err)
	}

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		Profile:       model.GuestProfileCore,
		CPULimit:      model.MustParseCPUQuantity("600m"),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(false),
	})
	if err == nil || !strings.Contains(err.Error(), "cpu quota exceeded") {
		t.Fatalf("expected cpu quota error, got %v", err)
	}
}

func TestCreateSandboxRejectsFractionalCPUOnQEMU(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	quota.MaxCPUCores = model.MustParseCPUQuantity("2")

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.MustParseCPUQuantity("500m"),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(false),
	})
	if err == nil || !strings.Contains(err.Error(), "whole CPU cores") {
		t.Fatalf("expected qemu fractional cpu rejection, got %v", err)
	}
}

func TestCreateSandboxRejectsDangerousQEMUProfileByDefault(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	svc.cfg.QEMUAllowedProfiles = []model.GuestProfile{model.GuestProfileDebug}
	svc.cfg.QEMUDangerousProfiles = []model.GuestProfile{model.GuestProfileDebug}

	imagePath := filepath.Join(t.TempDir(), "guest-debug.qcow2")
	if err := os.WriteFile(imagePath, []byte("debug-image"), 0o644); err != nil {
		t.Fatalf("write debug guest image fixture: %v", err)
	}
	writeGuestImageContract(t, imagePath, guestimage.Contract{
		ContractVersion:          model.DefaultImageContractVersion,
		ImagePath:                imagePath,
		BuildVersion:             "test",
		Profile:                  model.GuestProfileDebug,
		Control:                  guestimage.ControlContract{Mode: model.GuestControlModeAgent, ProtocolVersion: model.DefaultGuestControlProtocolVersion},
		WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
		Dangerous:                true,
		Debug:                    true,
	})
	svc.cfg.QEMUBaseImagePath = imagePath
	svc.cfg.QEMUAllowedBaseImagePaths = []string{imagePath}

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  imagePath,
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Profile:       model.GuestProfileDebug,
	})
	if err == nil || !strings.Contains(err.Error(), "dangerous-profile policy") {
		t.Fatalf("expected dangerous-profile rejection, got %v", err)
	}
}

func TestCreateSandboxRejectsFeatureNotAllowedByImageProfile(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	imagePath := filepath.Join(t.TempDir(), "guest-container.qcow2")
	if err := os.WriteFile(imagePath, []byte("container-image"), 0o644); err != nil {
		t.Fatalf("write container guest image fixture: %v", err)
	}
	writeGuestImageContract(t, imagePath, guestimage.Contract{
		ContractVersion:          model.DefaultImageContractVersion,
		ImagePath:                imagePath,
		BuildVersion:             "test",
		Profile:                  model.GuestProfileContainer,
		AllowedFeatures:          []string{"docker"},
		Control:                  guestimage.ControlContract{Mode: model.GuestControlModeAgent, ProtocolVersion: model.DefaultGuestControlProtocolVersion},
		WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
	})
	svc.cfg.QEMUBaseImagePath = imagePath
	svc.cfg.QEMUAllowedBaseImagePaths = []string{imagePath}
	svc.cfg.QEMUAllowedProfiles = []model.GuestProfile{model.GuestProfileContainer}

	_, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  imagePath,
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Profile:       model.GuestProfileContainer,
		Features:      []string{"gpu"},
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed by image profile") {
		t.Fatalf("expected forbidden feature rejection, got %v", err)
	}
}

func TestRestoreSnapshotPreservesProfileContractMetadata(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	runtime.restoreState = model.RuntimeState{RuntimeID: "rt-restore", Status: model.SandboxStatusStopped}
	runtime.snapshotInfo = model.SnapshotInfo{ImageRef: "snapshot-image"}

	imagePath := filepath.Join(t.TempDir(), "guest-browser.qcow2")
	if err := os.WriteFile(imagePath, []byte("browser-image"), 0o644); err != nil {
		t.Fatalf("write browser guest image fixture: %v", err)
	}
	writeGuestImageContract(t, imagePath, guestimage.Contract{
		ContractVersion:          model.DefaultImageContractVersion,
		ImagePath:                imagePath,
		BuildVersion:             "test",
		Profile:                  model.GuestProfileBrowser,
		Control:                  guestimage.ControlContract{Mode: model.GuestControlModeAgent, ProtocolVersion: model.DefaultGuestControlProtocolVersion},
		WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
	})
	svc.cfg.QEMUBaseImagePath = imagePath
	svc.cfg.QEMUAllowedBaseImagePaths = []string{imagePath}
	svc.cfg.QEMUAllowedProfiles = []model.GuestProfile{model.GuestProfileBrowser}

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  imagePath,
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Profile:       model.GuestProfileBrowser,
		Start:         false,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	snapshot, err := svc.CreateSnapshot(ctx, tenantA.ID, sandbox.ID, model.CreateSnapshotRequest{Name: "snap"})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	if _, err := svc.RestoreSnapshot(ctx, tenantA.ID, snapshot.ID, model.RestoreSnapshotRequest{TargetSandboxID: sandbox.ID}); err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}
	restored, err := svc.GetSandbox(ctx, tenantA.ID, sandbox.ID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if restored.Profile != model.GuestProfileBrowser {
		t.Fatalf("expected restored profile to remain browser, got %q", restored.Profile)
	}
	if restored.ControlProtocolVersion != model.DefaultGuestControlProtocolVersion {
		t.Fatalf("expected restored control protocol version %q, got %q", model.DefaultGuestControlProtocolVersion, restored.ControlProtocolVersion)
	}
	if restored.ImageContractVersion != model.DefaultImageContractVersion {
		t.Fatalf("expected restored image contract version %q, got %q", model.DefaultImageContractVersion, restored.ImageContractVersion)
	}
}

func TestLifecycleAuditsUseSpecificActionsAndFailureOutcomes(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := svc.StartSandbox(ctx, tenantA.ID, sandbox.ID, quota); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	runtime.stopErr = errors.New("forced stop failure")
	if _, err := svc.StopSandbox(ctx, tenantA.ID, sandbox.ID, true); err == nil {
		t.Fatal("expected stop failure")
	}

	events, err := store.ListAuditEvents(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	var sawStart, sawStopFailure bool
	for _, event := range events {
		if event.Action == "sandbox.start" && event.Outcome == "ok" {
			sawStart = true
		}
		if event.Action == "sandbox.stop" && event.Outcome == "error" && strings.Contains(event.Message, "force=true") {
			sawStopFailure = true
		}
		if event.Action == "sandbox.transition" {
			t.Fatalf("unexpected generic transition audit event: %+v", event)
		}
	}
	if !sawStart {
		t.Fatalf("expected sandbox.start audit event, got %+v", events)
	}
	if !sawStopFailure {
		t.Fatalf("expected sandbox.stop failure audit event, got %+v", events)
	}
}

func TestExecAuditSanitizesCommandArguments(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	runtime.startState = model.RuntimeState{RuntimeID: "rt-start", Status: model.SandboxStatusRunning, Running: true}
	runtime.execHandleFactory = func(context.Context, model.ExecRequest) model.ExecHandle {
		ch := make(chan model.ExecResult, 1)
		now := time.Now().UTC()
		ch <- model.ExecResult{
			ExitCode:    0,
			Status:      model.ExecutionStatusSucceeded,
			StartedAt:   now,
			CompletedAt: now,
		}
		close(ch)
		return stubExecHandle{ch: ch}
	}

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	secret := "super-secret-token"
	if _, err := svc.ExecSandbox(ctx, tenantA, quota, sandbox.ID, model.ExecRequest{
		Command: []string{"sh", "-lc", "echo " + secret},
		Cwd:     "/workspace",
		Timeout: 10 * time.Second,
	}, io.Discard, io.Discard); err != nil {
		t.Fatalf("exec sandbox: %v", err)
	}

	events, err := store.ListAuditEvents(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	last := events[len(events)-1]
	if last.Action != "sandbox.exec" {
		t.Fatalf("unexpected last audit event: %+v", last)
	}
	if strings.Contains(last.Message, secret) {
		t.Fatalf("expected exec audit to omit secret-bearing arguments, got %+v", last)
	}
	if !strings.Contains(last.Message, `entrypoint=sh`) {
		t.Fatalf("expected sanitized exec summary, got %+v", last)
	}
}

func TestReconcileAuditsDegradedTransitions(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")
	runtime.startState = model.RuntimeState{RuntimeID: "rt-start", Status: model.SandboxStatusRunning, Running: true}

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	runtime.inspectErr = errors.New("guest unreachable")
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	events, err := store.ListAuditEvents(ctx, tenantA.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	var sawDegraded bool
	for _, event := range events {
		if event.Action == "sandbox.reconcile" && event.Outcome == "error" && strings.Contains(event.Message, "reason=inspect_failed") {
			sawDegraded = true
		}
	}
	if !sawDegraded {
		t.Fatalf("expected reconcile degradation audit event, got %+v", events)
	}
	stored, err := svc.GetSandbox(ctx, tenantA.ID, sandbox.ID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if stored.Status != model.SandboxStatusDegraded {
		t.Fatalf("expected degraded sandbox after reconcile, got %+v", stored)
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

// TestCreateSandboxStampsRuntimeClass verifies that the service stamps the
// correct RuntimeClass on sandbox creation for each backend.
func TestCreateSandboxStampsRuntimeClass(t *testing.T) {
	tests := []struct {
		backend   string
		wantClass model.RuntimeClass
	}{
		{"docker", model.RuntimeClassTrustedDocker},
		{"qemu", model.RuntimeClassVM},
	}
	for _, tt := range tests {
		t.Run(tt.backend, func(t *testing.T) {
			ctx := context.Background()
			runtime := newStubRuntime()
			svc, _, quota, tenantA, _ := newServiceHarness(t, runtime, tt.backend)
			var imageRef string
			if tt.backend == "qemu" {
				imageRef = "guest-base.qcow2"
			} else {
				imageRef = "alpine:3.20"
			}
			sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
				BaseImageRef:  imageRef,
				CPULimit:      model.CPUCores(1),
				MemoryLimitMB: 512,
				PIDsLimit:     128,
				DiskLimitMB:   512,
				NetworkMode:   model.NetworkModeInternetDisabled,
				AllowTunnels:  boolPtr(false),
			})
			if err != nil {
				t.Fatalf("create sandbox: %v", err)
			}
			if sandbox.RuntimeClass != tt.wantClass {
				t.Errorf("RuntimeClass: got %q, want %q", sandbox.RuntimeClass, tt.wantClass)
			}
			if sandbox.RuntimeSelection == "" {
				t.Error("expected runtime selection to be stamped")
			}
		})
	}
}

// TestReconcileToleatesLegacySandboxWithNoRuntimeClass verifies that sandboxes
// with an empty runtime_class (i.e. created before the column was added) are
// reconciled successfully without errors, and that the class is derived from
// the backend on subsequent reads.
func TestReconcileToleratesLegacySandboxWithNoRuntimeClass(t *testing.T) {
	ctx := context.Background()
	runtime := newStubRuntime()
	svc, store, quota, tenantA, _ := newServiceHarness(t, runtime, "qemu")

	sandbox, err := svc.CreateSandbox(ctx, tenantA, quota, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 512,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	// Simulate a legacy row by clearing the runtime_class column.
	sqlDB := store.DB()
	if _, err := sqlDB.ExecContext(ctx, `UPDATE sandboxes SET runtime_class='' WHERE sandbox_id=?`, sandbox.ID); err != nil {
		t.Fatalf("clear runtime_class: %v", err)
	}

	// Reconcile must succeed even when runtime_class is empty.
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile with legacy sandbox: %v", err)
	}

	// After reconcile the sandbox should still be readable with a derived class.
	got, err := svc.GetSandbox(ctx, tenantA.ID, sandbox.ID)
	if err != nil {
		t.Fatalf("get sandbox after reconcile: %v", err)
	}
	if got.RuntimeClass != model.RuntimeClassVM {
		t.Errorf("expected derived RuntimeClassVM for qemu backend, got %q", got.RuntimeClass)
	}
	if got.RuntimeSelection != model.RuntimeSelectionQEMUProfessional {
		t.Errorf("expected derived runtime selection for qemu legacy row, got %q", got.RuntimeSelection)
	}
}

func newServiceHarness(t *testing.T, runtime model.RuntimeManager, backend string) (*Service, *repository.Store, model.TenantQuota, model.Tenant, model.Tenant) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		DatabasePath:             filepath.Join(root, "sandbox.db"),
		StorageRoot:              filepath.Join(root, "storage"),
		SnapshotRoot:             filepath.Join(root, "snapshots"),
		BaseImageRef:             "base-image",
		RuntimeBackend:           backend,
		EnabledRuntimeSelections: []model.RuntimeSelection{model.RuntimeSelectionFromBackend(backend)},
		DefaultRuntimeSelection:  model.RuntimeSelectionFromBackend(backend),
		DefaultCPULimit:          model.CPUCores(1),
		DefaultMemoryLimitMB:     256,
		DefaultPIDsLimit:         64,
		DefaultDiskLimitMB:       256,
		DefaultNetworkMode:       model.NetworkModeInternetDisabled,
		DefaultAllowTunnels:      false,
		OperatorHost:             "http://operator.invalid",
	}
	if backend == "qemu" {
		qemuImage := filepath.Join(root, "guest-base.qcow2")
		if err := os.WriteFile(qemuImage, []byte("qcow2"), 0o644); err != nil {
			t.Fatalf("write qemu guest image fixture: %v", err)
		}
		writeGuestImageContract(t, qemuImage, guestimage.Contract{
			ContractVersion:          model.DefaultImageContractVersion,
			ImagePath:                qemuImage,
			BuildVersion:             "test",
			Profile:                  model.GuestProfileCore,
			Capabilities:             []string{"exec", "files", "pty", "tcp_bridge"},
			Control:                  guestimage.ControlContract{Mode: model.GuestControlModeAgent, ProtocolVersion: model.DefaultGuestControlProtocolVersion},
			WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
		})
		cfg.BaseImageRef = "guest-base.qcow2"
		cfg.QEMUBaseImagePath = qemuImage
		cfg.QEMUAllowedBaseImagePaths = []string{qemuImage}
		cfg.QEMUAllowedProfiles = []model.GuestProfile{model.GuestProfileCore}
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
		MaxCPUCores:             model.CPUCores(8),
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

func writeGuestImageContract(t *testing.T, imagePath string, contract guestimage.Contract) {
	t.Helper()
	sha, err := guestimage.ComputeSHA256(imagePath)
	if err != nil {
		t.Fatalf("compute guest image sha: %v", err)
	}
	contract.ImageSHA256 = sha
	if contract.ImagePath == "" {
		contract.ImagePath = imagePath
	}
	data, err := json.Marshal(contract)
	if err != nil {
		t.Fatalf("marshal guest image contract: %v", err)
	}
	if err := os.WriteFile(guestimage.SidecarPath(imagePath), data, 0o644); err != nil {
		t.Fatalf("write guest image contract: %v", err)
	}
}

func boolPtr(value bool) *bool {
	return &value
}

type stubRuntime struct {
	createState       model.RuntimeState
	startState        model.RuntimeState
	stopState         model.RuntimeState
	restoreState      model.RuntimeState
	inspectState      model.RuntimeState
	snapshotInfo      model.SnapshotInfo
	storageUsage      model.StorageUsage
	createErr         error
	startErr          error
	stopErr           error
	suspendErr        error
	resumeErr         error
	destroyErr        error
	restoreErr        error
	inspectErr        error
	localConnErr      error
	inspectCalls      int
	execHandleFactory func(context.Context, model.ExecRequest) model.ExecHandle
	localConnFactory  func(context.Context, model.Sandbox, int) (net.Conn, error)
	createdSpec       model.SandboxSpec
	restoredSnapshot  model.Snapshot

	reads         map[string]string
	writes        []stubWrite
	deletes       []string
	mkdirs        []string
	createBlock   chan struct{}
	createEntered chan struct{}
	startBlock    chan struct{}
	startEntered  chan struct{}
	mu            sync.Mutex
}

type stubWrite struct {
	path    string
	content string
}

type stubExecHandle struct {
	ch chan model.ExecResult
}

func (h stubExecHandle) Wait() model.ExecResult {
	return <-h.ch
}

func (h stubExecHandle) Cancel() error {
	return nil
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

func (r *stubRuntime) Create(_ context.Context, spec model.SandboxSpec) (model.RuntimeState, error) {
	if r.createEntered != nil {
		select {
		case r.createEntered <- struct{}{}:
		default:
		}
	}
	if r.createBlock != nil {
		<-r.createBlock
	}
	r.mu.Lock()
	r.createdSpec = spec
	r.mu.Unlock()
	if r.createErr != nil {
		return model.RuntimeState{}, r.createErr
	}
	return withDefaultRuntimeState(r.createState, model.SandboxStatusStopped, false), nil
}

func (r *stubRuntime) Start(context.Context, model.Sandbox) (model.RuntimeState, error) {
	if r.startEntered != nil {
		select {
		case r.startEntered <- struct{}{}:
		default:
		}
	}
	if r.startBlock != nil {
		<-r.startBlock
	}
	if r.startErr != nil {
		return model.RuntimeState{}, r.startErr
	}
	return withDefaultRuntimeState(r.startState, model.SandboxStatusRunning, true), nil
}

func (r *stubRuntime) Stop(context.Context, model.Sandbox, bool) (model.RuntimeState, error) {
	if r.stopErr != nil {
		return model.RuntimeState{}, r.stopErr
	}
	return withDefaultRuntimeState(r.stopState, model.SandboxStatusStopped, false), nil
}

func (r *stubRuntime) Suspend(context.Context, model.Sandbox) (model.RuntimeState, error) {
	if r.suspendErr != nil {
		return model.RuntimeState{}, r.suspendErr
	}
	return withDefaultRuntimeState(model.RuntimeState{}, model.SandboxStatusSuspended, false), nil
}

func (r *stubRuntime) Resume(context.Context, model.Sandbox) (model.RuntimeState, error) {
	if r.resumeErr != nil {
		return model.RuntimeState{}, r.resumeErr
	}
	return withDefaultRuntimeState(model.RuntimeState{}, model.SandboxStatusRunning, true), nil
}

func (r *stubRuntime) Destroy(context.Context, model.Sandbox) error {
	return r.destroyErr
}

func (r *stubRuntime) Inspect(context.Context, model.Sandbox) (model.RuntimeState, error) {
	r.inspectCalls++
	if r.inspectErr != nil {
		return model.RuntimeState{}, r.inspectErr
	}
	return withDefaultRuntimeState(r.inspectState, model.SandboxStatusStopped, false), nil
}

func (r *stubRuntime) Exec(ctx context.Context, _ model.Sandbox, req model.ExecRequest, _ model.ExecStreams) (model.ExecHandle, error) {
	if r.execHandleFactory != nil {
		return r.execHandleFactory(ctx, req), nil
	}
	return nil, errors.New("not implemented")
}

func (r *stubRuntime) AttachTTY(context.Context, model.Sandbox, model.TTYRequest) (model.TTYHandle, error) {
	return nil, errors.New("not implemented")
}

func (r *stubRuntime) OpenSandboxLocalConn(ctx context.Context, sandbox model.Sandbox, targetPort int) (net.Conn, error) {
	if r.localConnFactory != nil {
		return r.localConnFactory(ctx, sandbox, targetPort)
	}
	if r.localConnErr != nil {
		return nil, r.localConnErr
	}
	return nil, errors.New("not implemented")
}

func (r *stubRuntime) CreateSnapshot(context.Context, model.Sandbox, string) (model.SnapshotInfo, error) {
	if r.snapshotInfo.ImageRef == "" {
		return model.SnapshotInfo{ImageRef: "snapshot-image"}, nil
	}
	return r.snapshotInfo, nil
}

func (r *stubRuntime) RestoreSnapshot(_ context.Context, _ model.Sandbox, snapshot model.Snapshot) (model.RuntimeState, error) {
	r.restoredSnapshot = snapshot
	if r.restoreErr != nil {
		return model.RuntimeState{}, r.restoreErr
	}
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
