package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"or3-sandbox/internal/model"
)

func TestArchiveRoundTrip(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "value.txt"), []byte("42"), 0o644); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "workspace.tar.gz")
	if err := archiveDirectory(src, archive); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := extractArchive(archive, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "nested", "value.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "42" {
		t.Fatalf("unexpected extracted content: %q", string(data))
	}
}

func TestCreateUsesAbsoluteHostPathsForBindMounts(t *testing.T) {
	root := t.TempDir()
	argsLog := filepath.Join(root, "docker-args.txt")
	fakeDocker := filepath.Join(root, "docker")
	script := "#!/bin/sh\nprintf '%s\n' \"$@\" > \"$DOCKER_ARGS_LOG\"\nprintf 'container-id\\n'\n"
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_ARGS_LOG", argsLog)
	runtime := NewWithBinary(fakeDocker)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relWorkspace := filepath.Join("testdata", "workspace")
	relCache := filepath.Join("testdata", "cache")
	if err := os.MkdirAll(relWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(relCache, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(wd, "testdata"))
	})

	_, err = runtime.Create(context.Background(), model.SandboxSpec{
		SandboxID:     "sbx-relative",
		TenantID:      "tenant-test",
		BaseImageRef:  "alpine:3.20",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		StorageRoot:   filepath.Join("testdata", "rootfs"),
		WorkspaceRoot: relWorkspace,
		CacheRoot:     relCache,
	})
	if err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	workspaceMount := filepath.Clean(filepath.Join(wd, relWorkspace)) + ":/workspace"
	cacheMount := filepath.Clean(filepath.Join(wd, relCache)) + ":/cache"
	text := string(logged)
	if !strings.Contains(text, workspaceMount) {
		t.Fatalf("expected absolute workspace mount %q in args %q", workspaceMount, text)
	}
	if !strings.Contains(text, cacheMount) {
		t.Fatalf("expected absolute cache mount %q in args %q", cacheMount, text)
	}
}

func TestPreviewWriterTracksTruncation(t *testing.T) {
	writer := newPreviewWriter(nil, 4)
	if _, err := writer.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if got := writer.String(); got != "abcd" {
		t.Fatalf("unexpected preview: %q", got)
	}
	if !writer.Truncated() {
		t.Fatal("expected truncation")
	}
}

func TestDockerRuntimeLifecycle(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("docker socket not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	runtime := New()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	cache := filepath.Join(root, "cache")
	spec := model.SandboxSpec{
		SandboxID:     "rt-smoke-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000"), ".", ""),
		TenantID:      "tenant-test",
		BaseImageRef:  "alpine:3.20",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  false,
		StorageRoot:   root,
		WorkspaceRoot: workspace,
		CacheRoot:     cache,
	}
	state, err := runtime.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != model.SandboxStatusStopped {
		t.Fatalf("unexpected create status: %s", state.Status)
	}

	sandbox := model.Sandbox{
		ID:            spec.SandboxID,
		TenantID:      spec.TenantID,
		BaseImageRef:  spec.BaseImageRef,
		CPULimit:      spec.CPULimit,
		MemoryLimitMB: spec.MemoryLimitMB,
		PIDsLimit:     spec.PIDsLimit,
		DiskLimitMB:   spec.DiskLimitMB,
		NetworkMode:   spec.NetworkMode,
		AllowTunnels:  spec.AllowTunnels,
		StorageRoot:   spec.StorageRoot,
		WorkspaceRoot: spec.WorkspaceRoot,
		CacheRoot:     spec.CacheRoot,
	}
	defer runtime.Destroy(context.Background(), sandbox)

	state, err = runtime.Start(ctx, sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != model.SandboxStatusRunning {
		t.Fatalf("unexpected start status: %s", state.Status)
	}

	handle, err := runtime.Exec(ctx, sandbox, model.ExecRequest{
		Command: []string{"sh", "-lc", "echo ok > /workspace/health && cat /workspace/health"},
		Cwd:     "/workspace",
		Timeout: 10 * time.Second,
	}, model.ExecStreams{})
	if err != nil {
		t.Fatal(err)
	}
	result := handle.Wait()
	if result.Status != model.ExecutionStatusSucceeded {
		t.Fatalf("unexpected exec status: %+v", result)
	}

	snapshot, err := runtime.CreateSnapshot(ctx, sandbox, "snap-"+spec.SandboxID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ImageRef == "" || snapshot.WorkspaceTar == "" {
		t.Fatalf("invalid snapshot info: %+v", snapshot)
	}
}
