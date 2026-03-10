package qemu

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"or3-sandbox/internal/guestimage"
	"or3-sandbox/internal/model"
)

func TestHostCoreSubstrateAndAgentProtocol(t *testing.T) {
	cfg := requireHostIntegrationConfig(t)
	if cfg.controlMode != model.GuestControlModeAgent {
		t.Skip("core substrate verification requires SANDBOX_QEMU_CONTROL_MODE=agent or an agent-default guest image")
	}
	ctx := context.Background()

	runtime, err := New(Options{
		Binary:        cfg.binary,
		Accel:         cfg.accel,
		BaseImagePath: cfg.baseImagePath,
		ControlMode:   cfg.controlMode,
		BootTimeout:   2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	root := t.TempDir()
	spec := model.SandboxSpec{
		SandboxID:                "sbx-core-substrate",
		TenantID:                 "tenant-core-substrate",
		BaseImageRef:             cfg.baseImagePath,
		Profile:                  cfg.contract.Profile,
		Capabilities:             append([]string(nil), cfg.contract.Capabilities...),
		ControlMode:              cfg.controlMode,
		ControlProtocolVersion:   cfg.contract.Control.ProtocolVersion,
		WorkspaceContractVersion: cfg.contract.WorkspaceContractVersion,
		ImageContractVersion:     cfg.contract.ContractVersion,
		CPULimit:                 model.CPUCores(1),
		MemoryLimitMB:            1024,
		PIDsLimit:                256,
		DiskLimitMB:              256,
		NetworkMode:              model.NetworkModeInternetDisabled,
		StorageRoot:              filepath.Join(root, "rootfs"),
		WorkspaceRoot:            filepath.Join(root, "workspace"),
		CacheRoot:                filepath.Join(root, "cache"),
	}
	state, err := runtime.Create(ctx, spec)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	sandbox := model.Sandbox{
		ID:                       spec.SandboxID,
		TenantID:                 spec.TenantID,
		RuntimeID:                state.RuntimeID,
		BaseImageRef:             spec.BaseImageRef,
		Profile:                  spec.Profile,
		Capabilities:             append([]string(nil), spec.Capabilities...),
		ControlMode:              spec.ControlMode,
		ControlProtocolVersion:   spec.ControlProtocolVersion,
		WorkspaceContractVersion: spec.WorkspaceContractVersion,
		ImageContractVersion:     spec.ImageContractVersion,
		CPULimit:                 spec.CPULimit,
		MemoryLimitMB:            spec.MemoryLimitMB,
		PIDsLimit:                spec.PIDsLimit,
		DiskLimitMB:              spec.DiskLimitMB,
		NetworkMode:              spec.NetworkMode,
		StorageRoot:              spec.StorageRoot,
		WorkspaceRoot:            spec.WorkspaceRoot,
		CacheRoot:                spec.CacheRoot,
	}
	defer runtime.Destroy(context.Background(), sandbox)

	if _, err := runtime.Start(ctx, sandbox); err != nil {
		t.Fatalf("start sandbox for core substrate verification: %v", err)
	}
	inspected, err := runtime.Inspect(ctx, sandbox)
	if err != nil {
		t.Fatalf("inspect running sandbox: %v", err)
	}
	if inspected.Status != model.SandboxStatusRunning {
		t.Fatalf("expected running sandbox after boot/readiness, got %s", inspected.Status)
	}
	if inspected.ControlMode != model.GuestControlModeAgent {
		t.Fatalf("expected agent control mode after readiness, got %s", inspected.ControlMode)
	}
	if _, err := runtime.agentHandshakeForSandbox(ctx, layoutForSandbox(sandbox), sandbox); err != nil {
		t.Fatalf("guest-agent protocol negotiation failed; this indicates host/guest control-contract drift: %v", err)
	}

	mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", "printf core-ready"}, "core-ready")
	if err := runtime.WriteWorkspaceFile(ctx, sandbox, "notes/hello.txt", "host-written"); err != nil {
		t.Fatalf("write workspace file through guest-agent boundary: %v", err)
	}
	file, err := runtime.ReadWorkspaceFile(ctx, sandbox, "notes/hello.txt")
	if err != nil {
		t.Fatalf("read workspace file through guest-agent boundary: %v", err)
	}
	if strings.TrimSpace(file.Content) != "host-written" {
		t.Fatalf("unexpected workspace file content %q", file.Content)
	}
	mustPTYContain(t, runtime, sandbox, model.TTYRequest{
		Command: []string{"sh", "-lc", "printf pty-ok"},
		Cwd:     "/workspace",
		Rows:    24,
		Cols:    80,
	}, "pty-ok")
}

func TestHostIsolationBoundaries(t *testing.T) {
	cfg := requireHostIntegrationConfig(t)
	ctx := context.Background()

	runtime, err := New(Options{
		Binary:         cfg.binary,
		Accel:          cfg.accel,
		BaseImagePath:  cfg.baseImagePath,
		ControlMode:    cfg.controlMode,
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
		SandboxID:                "sbx-isolation-boundaries",
		TenantID:                 "tenant-isolation-boundaries",
		BaseImageRef:             cfg.baseImagePath,
		Profile:                  cfg.contract.Profile,
		Capabilities:             append([]string(nil), cfg.contract.Capabilities...),
		ControlMode:              cfg.controlMode,
		ControlProtocolVersion:   cfg.contract.Control.ProtocolVersion,
		WorkspaceContractVersion: cfg.contract.WorkspaceContractVersion,
		ImageContractVersion:     cfg.contract.ContractVersion,
		CPULimit:                 model.CPUCores(1),
		MemoryLimitMB:            1024,
		PIDsLimit:                256,
		DiskLimitMB:              256,
		NetworkMode:              model.NetworkModeInternetDisabled,
		StorageRoot:              filepath.Join(root, "rootfs"),
		WorkspaceRoot:            filepath.Join(root, "workspace"),
		CacheRoot:                filepath.Join(root, "cache"),
	}
	state, err := runtime.Create(ctx, spec)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	sandbox := model.Sandbox{
		ID:                       spec.SandboxID,
		TenantID:                 spec.TenantID,
		RuntimeID:                state.RuntimeID,
		BaseImageRef:             spec.BaseImageRef,
		Profile:                  spec.Profile,
		Capabilities:             append([]string(nil), spec.Capabilities...),
		ControlMode:              spec.ControlMode,
		ControlProtocolVersion:   spec.ControlProtocolVersion,
		WorkspaceContractVersion: spec.WorkspaceContractVersion,
		ImageContractVersion:     spec.ImageContractVersion,
		CPULimit:                 spec.CPULimit,
		MemoryLimitMB:            spec.MemoryLimitMB,
		PIDsLimit:                spec.PIDsLimit,
		DiskLimitMB:              spec.DiskLimitMB,
		NetworkMode:              spec.NetworkMode,
		StorageRoot:              spec.StorageRoot,
		WorkspaceRoot:            spec.WorkspaceRoot,
		CacheRoot:                spec.CacheRoot,
	}
	defer runtime.Destroy(context.Background(), sandbox)

	hostSecretDir := t.TempDir()
	hostSecretPath := filepath.Join(hostSecretDir, "operator-secret.txt")
	if err := os.WriteFile(hostSecretPath, []byte("host-secret"), 0o600); err != nil {
		t.Fatalf("write host secret fixture: %v", err)
	}
	hostWorkspaceSentinel := filepath.Join(sandbox.WorkspaceRoot, "host-only.txt")
	if err := os.WriteFile(hostWorkspaceSentinel, []byte("host-only"), 0o644); err != nil {
		t.Fatalf("write host workspace sentinel: %v", err)
	}

	if _, err := runtime.Start(ctx, sandbox); err != nil {
		t.Fatalf("start sandbox for isolation verification: %v", err)
	}
	mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", strings.Join([]string{
		"set -eu",
		"test ! -S /var/run/docker.sock || { echo 'unexpected host docker socket exposure'; exit 1; }",
		"test ! -e /workspace/host-only.txt || { echo 'unexpected host workspace bind leakage into guest workspace'; exit 1; }",
		"test ! -e " + shellQuote(hostSecretPath) + " || { echo 'unexpected host secret path exposure'; exit 1; }",
		"test ! -e " + shellQuote(hostWorkspaceSentinel) + " || { echo 'unexpected host workspace path exposure'; exit 1; }",
		"printf isolation-ok",
	}, "\n")}, "isolation-ok")
}

func TestHostDiskFullAndWorkspacePersistence(t *testing.T) {
	cfg := requireHostIntegrationConfig(t)
	ctx := context.Background()

	runtime, err := New(Options{
		Binary:         cfg.binary,
		Accel:          cfg.accel,
		BaseImagePath:  cfg.baseImagePath,
		ControlMode:    cfg.controlMode,
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

func TestHostRestartRecoveryAndProfileWorkloads(t *testing.T) {
	cfg := requireHostIntegrationConfig(t)
	ctx := context.Background()

	runtime, err := New(Options{
		Binary:         cfg.binary,
		Accel:          cfg.accel,
		BaseImagePath:  cfg.baseImagePath,
		ControlMode:    cfg.controlMode,
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
		SandboxID:                "sbx-workload-coverage",
		TenantID:                 "tenant-workload-coverage",
		BaseImageRef:             cfg.baseImagePath,
		Profile:                  cfg.contract.Profile,
		Capabilities:             append([]string(nil), cfg.contract.Capabilities...),
		ControlMode:              cfg.controlMode,
		ControlProtocolVersion:   cfg.contract.Control.ProtocolVersion,
		WorkspaceContractVersion: cfg.contract.WorkspaceContractVersion,
		ImageContractVersion:     cfg.contract.ContractVersion,
		CPULimit:                 model.CPUCores(1),
		MemoryLimitMB:            2048,
		PIDsLimit:                512,
		DiskLimitMB:              2048,
		NetworkMode:              model.NetworkModeInternetEnabled,
		StorageRoot:              filepath.Join(root, "rootfs"),
		WorkspaceRoot:            filepath.Join(root, "workspace"),
		CacheRoot:                filepath.Join(root, "cache"),
	}
	state, err := runtime.Create(ctx, spec)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	sandbox := model.Sandbox{
		ID:                       spec.SandboxID,
		TenantID:                 spec.TenantID,
		RuntimeID:                state.RuntimeID,
		BaseImageRef:             spec.BaseImageRef,
		Profile:                  spec.Profile,
		Capabilities:             append([]string(nil), spec.Capabilities...),
		ControlMode:              spec.ControlMode,
		ControlProtocolVersion:   spec.ControlProtocolVersion,
		WorkspaceContractVersion: spec.WorkspaceContractVersion,
		ImageContractVersion:     spec.ImageContractVersion,
		CPULimit:                 spec.CPULimit,
		MemoryLimitMB:            spec.MemoryLimitMB,
		PIDsLimit:                spec.PIDsLimit,
		DiskLimitMB:              spec.DiskLimitMB,
		NetworkMode:              spec.NetworkMode,
		StorageRoot:              spec.StorageRoot,
		WorkspaceRoot:            spec.WorkspaceRoot,
		CacheRoot:                spec.CacheRoot,
	}
	defer runtime.Destroy(context.Background(), sandbox)
	if _, err := runtime.Start(ctx, sandbox); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	t.Logf("running profile-aware host verification for profile=%s control_mode=%s", cfg.contract.Profile, cfg.controlMode)

	t.Run("stop-start remains conservative and preserves workspace", func(t *testing.T) {
		if err := runtime.WriteWorkspaceFile(ctx, sandbox, "persist.txt", "persisted"); err != nil {
			t.Fatalf("seed persistent marker: %v", err)
		}
		restartSandbox(t, ctx, runtime, sandbox)
		inspected, err := runtime.Inspect(ctx, sandbox)
		if err != nil {
			t.Fatalf("inspect sandbox after restart: %v", err)
		}
		if inspected.Status != model.SandboxStatusRunning {
			t.Fatalf("expected running state after stop/start recovery, got %s", inspected.Status)
		}
		file, err := runtime.ReadWorkspaceFile(ctx, sandbox, "persist.txt")
		if err != nil {
			t.Fatalf("read persistent marker after restart: %v", err)
		}
		if strings.TrimSpace(file.Content) != "persisted" {
			t.Fatalf("expected persistent marker after restart, got %q", file.Content)
		}
	})

	if cfg.contract.Profile == model.GuestProfileCore {
		t.Run("core profile stays minimal", func(t *testing.T) {
			mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `
set -eu
for cmd in python3 node docker; do
  if command -v "$cmd" >/dev/null 2>&1; then
    echo "unexpected command present in core profile: $cmd"
    exit 1
  fi
done
printf core-minimal
`}, "core-minimal")
		})
		return
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

	if cfg.contract.Profile == model.GuestProfileRuntime || cfg.contract.Profile == model.GuestProfileBrowser || cfg.contract.Profile == model.GuestProfileContainer || cfg.contract.Profile == model.GuestProfileDebug {
		t.Run("python and npm persist after restart", func(t *testing.T) {
			mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `
set -eu
python3 -m venv /workspace/venv
/workspace/venv/bin/pip install colorama >/dev/null
/workspace/venv/bin/python -c 'import colorama; print(colorama.__version__)'
rm -rf /workspace/npm-smoke
mkdir -p /workspace/npm-smoke
cd /workspace/npm-smoke
npm init -y >/dev/null
npm install left-pad >/dev/null
node -e "process.stdout.write(require('left-pad')('7', 3, '0'))"
`}, "007")
			restartSandbox(t, ctx, runtime, sandbox)
			mustExecContain(t, runtime, sandbox, []string{"/workspace/venv/bin/python", "-c", "import colorama; print(colorama.__version__)"}, ".")
			mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `cd /workspace/npm-smoke && node -e "process.stdout.write(require('left-pad')('7', 3, '0'))"`}, "007")
		})
	}

	if cfg.contract.Profile == model.GuestProfileBrowser || cfg.contract.Profile == model.GuestProfileDebug {
		t.Run("headless browser", func(t *testing.T) {
			mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `
set -eu
for browser in chromium chromium-browser google-chrome google-chrome-stable; do
  if command -v "$browser" >/dev/null 2>&1; then
    "$browser" --headless --disable-gpu --no-sandbox --dump-dom 'data:text/html,<h1>browser-ok</h1>'
    exit 0
  fi
done
echo "no browser binary found for browser-profile verification" >&2
exit 1
`}, "browser-ok")
		})
	}

	if cfg.contract.Profile == model.GuestProfileContainer || cfg.contract.Profile == model.GuestProfileDebug {
		t.Run("guest container engine survives restart", func(t *testing.T) {
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
echo "no guest container engine found for container-profile verification" >&2
exit 1
`}, "container-ok")
			restartSandbox(t, ctx, runtime, sandbox)
			mustExecContain(t, runtime, sandbox, []string{"sh", "-lc", `
set -eu
if command -v docker >/dev/null 2>&1; then
  docker run --rm alpine:3.20 sh -lc 'echo container-ok'
  exit 0
fi
if command -v podman >/dev/null 2>&1; then
  podman run --rm alpine:3.20 sh -lc 'echo container-ok'
  exit 0
fi
exit 1
`}, "container-ok")
		})
	}
}

func TestHostSandboxLocalBridge(t *testing.T) {
	cfg := requireHostIntegrationConfig(t)
	if cfg.controlMode != model.GuestControlModeAgent {
		t.Skip("sandbox-local bridge verification requires SANDBOX_QEMU_CONTROL_MODE=agent or an agent-default guest image")
	}
	ctx := context.Background()

	runtime, err := New(Options{
		Binary:         cfg.binary,
		Accel:          cfg.accel,
		BaseImagePath:  cfg.baseImagePath,
		ControlMode:    cfg.controlMode,
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
		SandboxID:     "sbx-local-bridge",
		TenantID:      "tenant-local-bridge",
		BaseImageRef:  cfg.baseImagePath,
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 1024,
		PIDsLimit:     256,
		DiskLimitMB:   64,
		NetworkMode:   model.NetworkModeInternetDisabled,
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
	handle, err := runtime.Exec(ctx, sandbox, model.ExecRequest{
		Command:  []string{"sh", "-lc", `systemd-socket-activate -l 127.0.0.1:18080 /bin/sh -lc 'printf "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"'`},
		Cwd:      "/workspace",
		Timeout:  2 * time.Minute,
		Detached: true,
	}, model.ExecStreams{})
	if err != nil {
		t.Fatalf("start guest bridge server: %v", err)
	}
	_ = handle.Wait()

	var conn net.Conn
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = runtime.OpenSandboxLocalConn(ctx, sandbox, 18080)
		if err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("open sandbox local conn: %v", err)
	}
	defer conn.Close()

	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write request through bridge: %v", err)
	}
	response, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read response through bridge: %v", err)
	}
	if !strings.Contains(string(response), "\r\n\r\nok") {
		t.Fatalf("unexpected bridged response %q", string(response))
	}
}

type hostIntegrationConfig struct {
	binary         string
	accel          string
	baseImagePath  string
	controlMode    model.GuestControlMode
	contract       guestimage.Contract
	sshUser        string
	sshKeyPath     string
	sshHostKeyPath string
}

func requireHostIntegrationConfig(t *testing.T) hostIntegrationConfig {
	t.Helper()
	baseImagePath := firstEnv("SANDBOX_QEMU_BASE_IMAGE_PATH", "OR3_QEMU_BASE_IMAGE_PATH")
	if strings.TrimSpace(firstEnv("SANDBOX_QEMU_BINARY", "OR3_QEMU_BINARY")) == "" || strings.TrimSpace(baseImagePath) == "" {
		t.Skip("host QEMU verification requires SANDBOX_QEMU_BINARY and SANDBOX_QEMU_BASE_IMAGE_PATH")
	}
	contract, err := guestimage.Load(baseImagePath)
	if err != nil {
		t.Fatalf("load guest image contract for host verification: %v", err)
	}
	controlMode := model.GuestControlMode(strings.TrimSpace(firstEnv("SANDBOX_QEMU_CONTROL_MODE")))
	if !controlMode.IsValid() {
		controlMode = contract.Control.Mode
	}
	if !controlMode.IsValid() {
		controlMode = model.GuestControlModeAgent
	}
	cfg := hostIntegrationConfig{
		binary:         firstEnv("SANDBOX_QEMU_BINARY", "OR3_QEMU_BINARY"),
		accel:          firstEnv("SANDBOX_QEMU_ACCEL", "OR3_QEMU_ACCEL"),
		baseImagePath:  baseImagePath,
		controlMode:    controlMode,
		contract:       contract,
		sshUser:        firstEnv("SANDBOX_QEMU_SSH_USER", "OR3_QEMU_SSH_USER"),
		sshKeyPath:     firstEnv("SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH", "OR3_QEMU_SSH_PRIVATE_KEY_PATH"),
		sshHostKeyPath: firstEnv("SANDBOX_QEMU_SSH_HOST_KEY_PATH", "OR3_QEMU_SSH_HOST_KEY_PATH"),
	}
	if cfg.accel == "" {
		cfg.accel = "auto"
	}
	if cfg.controlMode == model.GuestControlModeSSHCompat && (cfg.sshUser == "" || cfg.sshKeyPath == "" || cfg.sshHostKeyPath == "") {
		t.Skip("ssh-compat host verification requires SANDBOX_QEMU_SSH_USER, SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH, and SANDBOX_QEMU_SSH_HOST_KEY_PATH")
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

func mustPTYContain(t *testing.T, runtime *Runtime, sandbox model.Sandbox, req model.TTYRequest, want string) {
	t.Helper()
	handle, err := runtime.AttachTTY(context.Background(), sandbox, req)
	if err != nil {
		t.Fatalf("attach PTY %v: %v", req.Command, err)
	}
	defer handle.Close()
	type readResult struct {
		data []byte
		err  error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(handle.Reader())
		resultCh <- readResult{data: data, err: err}
	}()
	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("read PTY output for %v: %v", req.Command, result.err)
		}
		if !strings.Contains(string(result.data), want) {
			t.Fatalf("PTY %v missing %q in output %q", req.Command, want, string(result.data))
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for PTY output from %v", req.Command)
	}
}
