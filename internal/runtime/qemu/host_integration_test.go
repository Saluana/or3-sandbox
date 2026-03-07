package qemu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"or3-sandbox/internal/model"
)

func TestHostDiskFullAndWorkspacePersistence(t *testing.T) {
	cfg := requireHostIntegrationConfig(t)
	ctx := context.Background()

	runtime, err := New(Options{
		Binary:        cfg.binary,
		Accel:         cfg.accel,
		BaseImagePath: cfg.baseImagePath,
		SSHUser:       cfg.sshUser,
		SSHKeyPath:    cfg.sshKeyPath,
		BootTimeout:   2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	root := t.TempDir()
	spec := model.SandboxSpec{
		SandboxID:     "sbx-host-coverage",
		TenantID:      "tenant-host-coverage",
		BaseImageRef:  cfg.baseImagePath,
		CPULimit:      1,
		MemoryLimitMB: 1024,
		PIDsLimit:     256,
		DiskLimitMB:   64,
		NetworkMode:   model.NetworkModeInternetEnabled,
		StorageRoot:   filepath.Join(root, "rootfs"),
		WorkspaceRoot: filepath.Join(root, "workspace"),
		CacheRoot:     filepath.Join(root, "cache"),
	}

	state, err := runtime.Create(ctx, spec)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	sandbox := model.Sandbox{
		ID:            spec.SandboxID,
		TenantID:      spec.TenantID,
		RuntimeID:     state.RuntimeID,
		BaseImageRef:  spec.BaseImageRef,
		CPULimit:      spec.CPULimit,
		MemoryLimitMB: spec.MemoryLimitMB,
		PIDsLimit:     spec.PIDsLimit,
		DiskLimitMB:   spec.DiskLimitMB,
		NetworkMode:   spec.NetworkMode,
		StorageRoot:   spec.StorageRoot,
		WorkspaceRoot: spec.WorkspaceRoot,
		CacheRoot:     spec.CacheRoot,
	}
	defer runtime.Destroy(context.Background(), sandbox)

	if _, err := runtime.Start(ctx, sandbox); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	if err := runtime.WriteWorkspaceFile(ctx, sandbox, "persist.txt", "persisted"); err != nil {
		t.Fatalf("seed workspace file: %v", err)
	}

	handle, err := runtime.Exec(ctx, sandbox, model.ExecRequest{
		Command: []string{
			"sh", "-lc",
			"rm -f /workspace/fill.bin && dd if=/dev/zero of=/workspace/fill.bin bs=1M count=256 status=none",
		},
		Timeout: 2 * time.Minute,
	}, model.ExecStreams{})
	if err != nil {
		t.Fatalf("start disk fill exec: %v", err)
	}
	result := handle.Wait()
	if result.Status == model.ExecutionStatusSucceeded {
		t.Fatalf("expected disk fill to fail, got success with output: stdout=%q stderr=%q", result.StdoutPreview, result.StderrPreview)
	}

	if _, err := runtime.Stop(ctx, sandbox, false); err != nil {
		t.Fatalf("stop sandbox: %v", err)
	}
	if _, err := runtime.Start(ctx, sandbox); err != nil {
		t.Fatalf("restart sandbox: %v", err)
	}
	file, err := runtime.ReadWorkspaceFile(ctx, sandbox, "persist.txt")
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if strings.TrimSpace(file.Content) != "persisted" {
		t.Fatalf("unexpected persisted content %q", file.Content)
	}
}

type hostIntegrationConfig struct {
	binary        string
	accel         string
	baseImagePath string
	sshUser       string
	sshKeyPath    string
}

func requireHostIntegrationConfig(t *testing.T) hostIntegrationConfig {
	t.Helper()
	cfg := hostIntegrationConfig{
		binary:        firstEnv("SANDBOX_QEMU_BINARY", "OR3_QEMU_BINARY"),
		accel:         firstEnv("SANDBOX_QEMU_ACCEL", "OR3_QEMU_ACCEL"),
		baseImagePath: firstEnv("SANDBOX_QEMU_BASE_IMAGE_PATH", "OR3_QEMU_BASE_IMAGE_PATH"),
		sshUser:       firstEnv("SANDBOX_QEMU_SSH_USER", "OR3_QEMU_SSH_USER"),
		sshKeyPath:    firstEnv("SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH", "OR3_QEMU_SSH_PRIVATE_KEY_PATH"),
	}
	if cfg.binary == "" || cfg.baseImagePath == "" || cfg.sshUser == "" || cfg.sshKeyPath == "" {
		t.Skip("host QEMU coverage requires SANDBOX_QEMU_BINARY, SANDBOX_QEMU_BASE_IMAGE_PATH, SANDBOX_QEMU_SSH_USER, and SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH")
	}
	if cfg.accel == "" {
		cfg.accel = "auto"
	}
	return cfg
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
