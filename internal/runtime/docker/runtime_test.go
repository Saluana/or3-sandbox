package docker

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"or3-sandbox/internal/model"
	"or3-sandbox/internal/testutil"
)

func newFakeDocker(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	argsLog := filepath.Join(root, "docker-args.txt")
	fakeDocker := filepath.Join(root, "docker")
	script := "#!/bin/sh\nprintf '%s\n' \"$@\" > \"$DOCKER_ARGS_LOG\"\nprintf 'container-id\\n'\n"
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_ARGS_LOG", argsLog)
	return fakeDocker, argsLog
}

func newFakeDockerScript(t *testing.T, script string) (string, string) {
	t.Helper()
	root := t.TempDir()
	argsLog := filepath.Join(root, "docker-args.txt")
	fakeDocker := filepath.Join(root, "docker")
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_ARGS_LOG", argsLog)
	return fakeDocker, argsLog
}

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
	fakeDocker, argsLog := newFakeDocker(t)
	runtime := NewWithBinary(fakeDocker)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relWorkspace := filepath.Join("testdata", "workspace")
	relCache := filepath.Join("testdata", "cache")
	relScratch := filepath.Join("testdata", "scratch")
	relSecrets := filepath.Join("testdata", "secrets")
	if err := os.MkdirAll(relWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(relCache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(relScratch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(relSecrets, 0o755); err != nil {
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
		ScratchRoot:   relScratch,
		SecretsRoot:   relSecrets,
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
	scratchMount := filepath.Clean(filepath.Join(wd, relScratch)) + ":/scratch"
	secretsMount := filepath.Clean(filepath.Join(wd, relSecrets)) + ":/secrets:ro"
	text := string(logged)
	if !strings.Contains(text, workspaceMount) {
		t.Fatalf("expected absolute workspace mount %q in args %q", workspaceMount, text)
	}
	if !strings.Contains(text, cacheMount) {
		t.Fatalf("expected absolute cache mount %q in args %q", cacheMount, text)
	}
	if !strings.Contains(text, scratchMount) {
		t.Fatalf("expected absolute scratch mount %q in args %q", scratchMount, text)
	}
	if !strings.Contains(text, secretsMount) {
		t.Fatalf("expected absolute secrets mount %q in args %q", secretsMount, text)
	}
	for _, expected := range []string{"--user", defaultUser, "--cap-drop", "ALL", "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m", "--security-opt", "no-new-privileges:true"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected hardened docker arg %q in args %q", expected, text)
		}
	}
}

func TestResolveSecurityOptionsAddsLinuxProfilesAndOverrides(t *testing.T) {
	runtime := New(Options{
		HostOS:                  "linux",
		User:                    defaultUser,
		TmpfsSizeMB:             32,
		SeccompProfile:          "/profiles/seccomp.json",
		AppArmorProfile:         "or3-sandbox",
		SELinuxLabel:            "type:or3_t",
		AllowDangerousOverrides: true,
	})
	options, warnings, err := runtime.resolveSecurityOptions(model.SandboxSpec{
		Capabilities: []string{"docker.elevated-user", "docker.extra-cap:net_bind_service"},
	})
	if err != nil {
		t.Fatalf("resolve security options: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings on linux, got %#v", warnings)
	}
	if options.User != "0:0" {
		t.Fatalf("expected elevated user override, got %q", options.User)
	}
	if options.TmpfsMount != "/tmp:rw,nosuid,nodev,noexec,size=32m" {
		t.Fatalf("unexpected tmpfs mount %q", options.TmpfsMount)
	}
	for _, expected := range []string{"seccomp=/profiles/seccomp.json", "apparmor=or3-sandbox", "label=type:or3_t"} {
		if !slices.Contains(options.SecurityOpts, expected) {
			t.Fatalf("expected security option %q in %#v", expected, options.SecurityOpts)
		}
	}
	if !slices.Contains(options.CapAdd, "NET_BIND_SERVICE") {
		t.Fatalf("expected NET_BIND_SERVICE cap add, got %#v", options.CapAdd)
	}
}

func TestResolveSecurityOptionsWarnsWhenLinuxProfilesUnsupported(t *testing.T) {
	runtime := New(Options{
		HostOS:          "darwin",
		SeccompProfile:  "/profiles/seccomp.json",
		AppArmorProfile: "or3-sandbox",
		SELinuxLabel:    "type:or3_t",
	})
	options, warnings, err := runtime.resolveSecurityOptions(model.SandboxSpec{})
	if err != nil {
		t.Fatalf("resolve security options: %v", err)
	}
	if len(options.SecurityOpts) != 0 {
		t.Fatalf("expected no linux security opts on darwin, got %#v", options.SecurityOpts)
	}
	if len(warnings) != 3 {
		t.Fatalf("expected 3 warnings, got %#v", warnings)
	}
}

func TestResolveSecurityOptionsRejectsDangerousOverridesWithoutSupport(t *testing.T) {
	runtime := New(Options{HostOS: "linux"})
	_, _, err := runtime.resolveSecurityOptions(model.SandboxSpec{Capabilities: []string{"docker.extra-cap:net_bind_service"}})
	if err == nil || !strings.Contains(err.Error(), "dangerous override support") {
		t.Fatalf("expected dangerous override rejection, got %v", err)
	}
}

func TestCreateAddsLinuxStorageOptForDiskLimit(t *testing.T) {
	fakeDocker, argsLog := newFakeDocker(t)
	runtime := New(Options{Binary: fakeDocker, HostOS: "linux"})
	root := t.TempDir()
	_, err := runtime.Create(context.Background(), model.SandboxSpec{
		SandboxID:     "sbx-storage-opt",
		TenantID:      "tenant-test",
		BaseImageRef:  "alpine:3.20",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		StorageRoot:   filepath.Join(root, "rootfs"),
		WorkspaceRoot: filepath.Join(root, "workspace"),
		CacheRoot:     filepath.Join(root, "cache"),
	})
	if err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "--storage-opt\nsize=512m") {
		t.Fatalf("expected linux storage-opt in args %q", string(logged))
	}
}

func TestCreateFallsBackWhenLinuxStorageOptIsUnsupported(t *testing.T) {
	fakeDocker, argsLog := newFakeDockerScript(t, "#!/bin/sh\nprintf '%s\n' \"$@\" >> \"$DOCKER_ARGS_LOG\"\nprintf '%s\n' '---' >> \"$DOCKER_ARGS_LOG\"\nfor arg in \"$@\"; do\n  if [ \"$arg\" = \"--storage-opt\" ]; then\n    printf '%s\n' \"storage-opt is supported only for overlay over xfs with 'pquota' mount option\" >&2\n    exit 1\n  fi\ndone\nprintf 'container-id\\n'\n")
	runtime := New(Options{Binary: fakeDocker, HostOS: "linux"})
	root := t.TempDir()
	_, err := runtime.Create(context.Background(), model.SandboxSpec{
		SandboxID:     "sbx-storage-opt-fallback",
		TenantID:      "tenant-test",
		BaseImageRef:  "alpine:3.20",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		StorageRoot:   filepath.Join(root, "rootfs"),
		WorkspaceRoot: filepath.Join(root, "workspace"),
		CacheRoot:     filepath.Join(root, "cache"),
	})
	if err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimSpace(string(logged)), "---")
	if len(parts) < 2 {
		t.Fatalf("expected retry without storage-opt, got log %q", string(logged))
	}
	if !strings.Contains(parts[0], "--storage-opt\nsize=512m") {
		t.Fatalf("expected first attempt to include storage-opt, got %q", parts[0])
	}
	if strings.Contains(parts[len(parts)-1], "--storage-opt") {
		t.Fatalf("expected retry without storage-opt, got %q", parts[len(parts)-1])
	}
}

func TestRestoreSnapshotUsesConfiguredArchiveLimits(t *testing.T) {
	fakeDocker, _ := newFakeDocker(t)
	runtime := New(Options{Binary: fakeDocker, SnapshotMaxFiles: 1, SnapshotMaxBytes: 1024 * 1024, SnapshotMaxExpansionRatio: 32})
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	source := t.TempDir()
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "workspace.tar.gz")
	if err := archiveDirectory(source, archive); err != nil {
		t.Fatal(err)
	}
	_, err := runtime.RestoreSnapshot(context.Background(), model.Sandbox{
		ID:            "sbx-restore-limits",
		TenantID:      "tenant-test",
		BaseImageRef:  "alpine:3.20",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   256,
		NetworkMode:   model.NetworkModeInternetDisabled,
		StorageRoot:   filepath.Join(t.TempDir(), "rootfs"),
		WorkspaceRoot: workspaceRoot,
		CacheRoot:     filepath.Join(t.TempDir(), "cache"),
	}, model.Snapshot{ImageRef: "alpine:3.20", WorkspaceTar: archive})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "file count") {
		t.Fatalf("expected configured archive file-count limit error, got %v", err)
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
	if err := testutil.DockerAvailable(context.Background()); err != nil {
		t.Skipf("docker unavailable: %v", err)
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
		Command: []string{"sh", "-lc", "uid=$(id -u); gid=$(id -g); printf '%s:%s\n' \"$uid\" \"$gid\" > /workspace/identity && echo ok > /workspace/health && if touch /etc/or3-readonly 2>/dev/null; then exit 90; fi; cat /workspace/health"},
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
	identity, err := os.ReadFile(filepath.Join(workspace, "identity"))
	if err != nil {
		t.Fatalf("read identity file: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(identity)), "0:") {
		t.Fatalf("expected non-root execution identity, got %q", string(identity))
	}

	snapshot, err := runtime.CreateSnapshot(ctx, sandbox, "snap-"+spec.SandboxID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ImageRef == "" || snapshot.WorkspaceTar == "" {
		t.Fatalf("invalid snapshot info: %+v", snapshot)
	}
}
