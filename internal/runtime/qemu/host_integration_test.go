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
		Binary:         cfg.binary,
		Accel:          cfg.accel,
		BaseImagePath:  cfg.baseImagePath,
		SSHUser:        cfg.sshUser,
		SSHKeyPath:     cfg.sshKeyPath,
		SSHHostKeyPath: cfg.sshHostKeyPath,
		BootTimeout:    2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	root := t.TempDir()
	spec := model.SandboxSpec{
		SandboxID:     "sbx-host-coverage",
		TenantID:      "tenant-host-coverage",
		BaseImageRef:  cfg.baseImagePath,
		CPULimit:      model.CPUCores(1),
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

func TestHostWorkloadClaimsAndRestartDurability(t *testing.T) {
	cfg := requireHostIntegrationConfig(t)
	ctx := context.Background()

	runtime, err := New(Options{
		Binary:         cfg.binary,
		Accel:          cfg.accel,
		BaseImagePath:  cfg.baseImagePath,
		SSHUser:        cfg.sshUser,
		SSHKeyPath:     cfg.sshKeyPath,
		SSHHostKeyPath: cfg.sshHostKeyPath,
		BootTimeout:    2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	root := t.TempDir()
	spec := model.SandboxSpec{
		SandboxID:     "sbx-workload-coverage",
		TenantID:      "tenant-workload-coverage",
		BaseImageRef:  cfg.baseImagePath,
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 2048,
		PIDsLimit:     512,
		DiskLimitMB:   2048,
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

	t.Run("git", func(t *testing.T) {
		mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `
set -eu
rm -rf /workspace/git-smoke
mkdir -p /workspace/git-smoke
cd /workspace/git-smoke
git init >/dev/null
git config user.email smoke@example.com
git config user.name "OR3 Smoke"
echo git-ok > README.md
git add README.md
git commit -m init >/dev/null
git rev-parse --is-inside-work-tree
`}, "true")
	})

	t.Run("python persists after restart", func(t *testing.T) {
		mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `
set -eu
python3 -m venv /workspace/venv
/workspace/venv/bin/pip install colorama >/dev/null
/workspace/venv/bin/python -c 'import colorama; print(colorama.__version__)'
`}, ".")
		restartSandbox(t, ctx, runtime, sandbox)
		mustExecContain(t, runtime, sandbox, []string{"/workspace/venv/bin/python", "-c", "import colorama; print(colorama.__version__)"}, ".")
	})

	t.Run("npm persists after restart", func(t *testing.T) {
		mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `
set -eu
rm -rf /workspace/npm-smoke
mkdir -p /workspace/npm-smoke
cd /workspace/npm-smoke
npm init -y >/dev/null
npm install left-pad >/dev/null
node -e "process.stdout.write(require('left-pad')('7', 3, '0'))"
`}, "007")
		restartSandbox(t, ctx, runtime, sandbox)
		mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `cd /workspace/npm-smoke && node -e "process.stdout.write(require('left-pad')('7', 3, '0'))"`}, "007")
	})

	t.Run("headless browser", func(t *testing.T) {
		mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `
set -eu
for browser in chromium chromium-browser google-chrome google-chrome-stable; do
  if command -v "$browser" >/dev/null 2>&1; then
    "$browser" --headless --disable-gpu --no-sandbox --dump-dom 'data:text/html,<h1>browser-ok</h1>'
    exit 0
  fi
done
echo "no browser binary found" >&2
exit 1
`}, "browser-ok")
	})

	t.Run("guest container engine", func(t *testing.T) {
		mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `
set -eu
if command -v docker >/dev/null 2>&1; then
  sudo systemctl start docker || true
  docker run --rm alpine:3.20 sh -lc 'echo container-ok'
  exit 0
fi
if command -v podman >/dev/null 2>&1; then
  podman run --rm alpine:3.20 sh -lc 'echo container-ok'
  exit 0
fi
echo "no guest container engine found" >&2
exit 1
`}, "container-ok")
	})

	t.Run("supervised background service survives restart", func(t *testing.T) {
		mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `
set -eu
cat >/tmp/or3-workload-smoke.sh <<'SH'
#!/bin/sh
while true; do
  date +%s >> /workspace/service.log
  sleep 1
done
SH
chmod +x /tmp/or3-workload-smoke.sh
cat >/tmp/or3-workload-smoke.service <<'UNIT'
[Unit]
Description=OR3 workload smoke
After=network.target

[Service]
ExecStart=/usr/local/bin/or3-workload-smoke.sh
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
UNIT
sudo mv /tmp/or3-workload-smoke.sh /usr/local/bin/or3-workload-smoke.sh
sudo mv /tmp/or3-workload-smoke.service /etc/systemd/system/or3-workload-smoke.service
sudo systemctl daemon-reload
sudo systemctl enable --now or3-workload-smoke.service
sleep 3
systemctl is-active or3-workload-smoke.service
`}, "active")
		restartSandbox(t, ctx, runtime, sandbox)
		mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `systemctl is-active or3-workload-smoke.service && test -s /workspace/service.log && echo service-ok`}, "service-ok")
	})
}

type hostIntegrationConfig struct {
	binary        string
	accel         string
	baseImagePath string
	sshUser       string
	sshKeyPath    string
	sshHostKeyPath string
}

func requireHostIntegrationConfig(t *testing.T) hostIntegrationConfig {
	t.Helper()
	cfg := hostIntegrationConfig{
		binary:        firstEnv("SANDBOX_QEMU_BINARY", "OR3_QEMU_BINARY"),
		accel:         firstEnv("SANDBOX_QEMU_ACCEL", "OR3_QEMU_ACCEL"),
		baseImagePath: firstEnv("SANDBOX_QEMU_BASE_IMAGE_PATH", "OR3_QEMU_BASE_IMAGE_PATH"),
		sshUser:       firstEnv("SANDBOX_QEMU_SSH_USER", "OR3_QEMU_SSH_USER"),
		sshKeyPath:    firstEnv("SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH", "OR3_QEMU_SSH_PRIVATE_KEY_PATH"),
		sshHostKeyPath: firstEnv("SANDBOX_QEMU_SSH_HOST_KEY_PATH", "OR3_QEMU_SSH_HOST_KEY_PATH"),
	}
	if cfg.binary == "" || cfg.baseImagePath == "" || cfg.sshUser == "" || cfg.sshKeyPath == "" || cfg.sshHostKeyPath == "" {
		t.Skip("host QEMU coverage requires SANDBOX_QEMU_BINARY, SANDBOX_QEMU_BASE_IMAGE_PATH, SANDBOX_QEMU_SSH_USER, SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH, and SANDBOX_QEMU_SSH_HOST_KEY_PATH")
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

func restartSandbox(t *testing.T, ctx context.Context, runtime *Runtime, sandbox model.Sandbox) {
	t.Helper()
	if _, err := runtime.Stop(ctx, sandbox, false); err != nil {
		t.Fatalf("stop sandbox: %v", err)
	}
	if _, err := runtime.Start(ctx, sandbox); err != nil {
		t.Fatalf("restart sandbox: %v", err)
	}
}

func mustExecContain(t *testing.T, runtime *Runtime, sandbox model.Sandbox, command []string, want string) {
	t.Helper()
	handle, err := runtime.Exec(context.Background(), sandbox, model.ExecRequest{
		Command: command,
		Cwd:     "/workspace",
		Timeout: 5 * time.Minute,
	}, model.ExecStreams{})
	if err != nil {
		t.Fatalf("start exec %v: %v", command, err)
	}
	result := handle.Wait()
	if result.Status != model.ExecutionStatusSucceeded {
		t.Fatalf("exec %v failed: status=%s stdout=%q stderr=%q", command, result.Status, result.StdoutPreview, result.StderrPreview)
	}
	if !strings.Contains(result.StdoutPreview, want) {
		t.Fatalf("exec %v missing %q in stdout %q", command, want, result.StdoutPreview)
	}
}
