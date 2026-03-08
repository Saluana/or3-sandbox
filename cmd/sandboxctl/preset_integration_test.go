package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"net/http/httptest"

	"or3-sandbox/internal/api"
	"or3-sandbox/internal/auth"
	"or3-sandbox/internal/config"
	"or3-sandbox/internal/db"
	"or3-sandbox/internal/logging"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
	runtimedocker "or3-sandbox/internal/runtime/docker"
	runtimeqemu "or3-sandbox/internal/runtime/qemu"
	"or3-sandbox/internal/service"
)

func TestPresetRunToolingDocker(t *testing.T) {
	h := newPresetDockerHarness(t)
	defer h.close()
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "tooling", `
name: tooling
runtime:
  allowed: [docker]
sandbox:
  image: python:3.12-alpine
  cpu: "1"
  memory_mb: 512
files:
  - path: hello.py
    content: |
      print("tooling-ok")
bootstrap:
  - name: run-python
    command: ["python3", "/workspace/hello.py"]
readiness:
  type: command
  command: ["sh", "-lc", "test -f /workspace/hello.py"]
  timeout: 20s
  interval: 500ms
artifacts:
  - remote_path: hello.py
    local_path: outputs/hello.py
cleanup: always
`)
	output := captureStdout(t, func() {
		if err := runPresetRun(clientConfig{baseURL: h.server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "tooling"}); err != nil {
			t.Fatalf("runPresetRun: %v", err)
		}
	})
	if !strings.Contains(output, "sandbox_id=") {
		t.Fatalf("expected sandbox id in output, got %s", output)
	}
	data, err := os.ReadFile(filepath.Join(examplesDir, "tooling", "outputs", "hello.py"))
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !strings.Contains(string(data), "tooling-ok") {
		t.Fatalf("unexpected artifact content: %s", data)
	}
}

func TestPresetRunArtifactDocker(t *testing.T) {
	h := newPresetDockerHarness(t)
	defer h.close()
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "artifact", `
name: artifact
runtime:
  allowed: [docker]
sandbox:
  image: python:3.12-alpine
  cpu: "1"
  memory_mb: 512
bootstrap:
  - name: write-png
    command: ["python3", "-c", "import base64, pathlib; pathlib.Path('/workspace/pixel.png').write_bytes(base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO5Wg3sAAAAASUVORK5CYII='))"]
artifacts:
  - remote_path: pixel.png
    local_path: outputs/pixel.png
    binary: true
cleanup: always
`)
	if err := runPresetRun(clientConfig{baseURL: h.server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "artifact"}); err != nil {
		t.Fatalf("runPresetRun: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(examplesDir, "artifact", "outputs", "pixel.png"))
	if err != nil {
		t.Fatalf("read binary artifact: %v", err)
	}
	if len(data) < 8 || string(data[:4]) != "\x89PNG" {
		t.Fatalf("unexpected png artifact header: %v", data[:8])
	}
}

func TestPresetRunServiceTunnelDocker(t *testing.T) {
	h := newPresetDockerHarness(t)
	defer h.close()
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "service", `
name: service
runtime:
  allowed: [docker]
sandbox:
  image: python:3.12-alpine
  cpu: "1"
  memory_mb: 512
  allow_tunnels: true
files:
  - path: index.html
    content: "service-ok"
startup:
  name: start-http
  command: ["sh", "-lc", "python3 -m http.server 8080 -d /workspace"]
  detached: true
  timeout: 20s
readiness:
  type: http
  path: "/"
  timeout: 20s
  interval: 500ms
tunnel:
  port: 8080
  protocol: http
  auth_mode: token
  visibility: private
cleanup: always
`)
	output := captureStdout(t, func() {
		if err := runPresetRun(clientConfig{baseURL: h.server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "service"}); err != nil {
			t.Fatalf("runPresetRun: %v", err)
		}
	})
	if !strings.Contains(output, "tunnel_endpoint=") {
		t.Fatalf("expected tunnel endpoint output, got %s", output)
	}
}

func TestPresetRunArtifactQEMU(t *testing.T) {
	h := newPresetQEMUHarness(t)
	defer h.close()
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "qemu-artifact", `
name: qemu-artifact
runtime:
  allowed: [qemu]
  profile: base-guest
sandbox:
  image: ${QEMU_GUEST_IMAGE}
  cpu: "1"
  memory_mb: 1024
files:
  - path: hello.txt
    content: "qemu-ok"
bootstrap:
  - name: write-binary
    command: ["sh", "-lc", "cp /workspace/hello.txt /workspace/out.txt && printf '\\211PNG\\r\\n\\032\\n' > /workspace/pixel.bin"]
readiness:
  type: command
  command: ["sh", "-lc", "test -f /workspace/out.txt && grep -q qemu-ok /workspace/out.txt"]
  timeout: 30s
  interval: 500ms
artifacts:
  - remote_path: out.txt
    local_path: outputs/out.txt
  - remote_path: pixel.bin
    local_path: outputs/pixel.bin
    binary: true
cleanup: always
`)
	if err := runPresetRun(clientConfig{baseURL: h.server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "--env", "QEMU_GUEST_IMAGE=" + h.baseImagePath, "qemu-artifact"}); err != nil {
		t.Fatalf("runPresetRun: %v", err)
	}
	textData, err := os.ReadFile(filepath.Join(examplesDir, "qemu-artifact", "outputs", "out.txt"))
	if err != nil {
		t.Fatalf("read text artifact: %v", err)
	}
	if strings.TrimSpace(string(textData)) != "qemu-ok" {
		t.Fatalf("unexpected text artifact %q", textData)
	}
	binaryData, err := os.ReadFile(filepath.Join(examplesDir, "qemu-artifact", "outputs", "pixel.bin"))
	if err != nil {
		t.Fatalf("read binary artifact: %v", err)
	}
	if len(binaryData) < 8 || string(binaryData[:4]) != "\x89PNG" {
		t.Fatalf("unexpected qemu binary artifact header: %v", binaryData)
	}
}

type presetDockerHarness struct {
	server *httptest.Server
	db     *sql.DB
}

type presetQEMUHarness struct {
	server        *httptest.Server
	db            *sql.DB
	baseImagePath string
}

func newPresetDockerHarness(t *testing.T) *presetDockerHarness {
	t.Helper()
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("docker socket not available")
	}
	root := t.TempDir()
	cfg := config.Config{
		DeploymentMode:       "development",
		ListenAddress:        "127.0.0.1:0",
		DatabasePath:         filepath.Join(root, "sandbox.db"),
		StorageRoot:          filepath.Join(root, "storage"),
		SnapshotRoot:         filepath.Join(root, "snapshots"),
		BaseImageRef:         "alpine:3.20",
		RuntimeBackend:       "docker",
		AuthMode:             "static",
		TrustedDockerRuntime: true,
		DefaultCPULimit:      model.CPUCores(1),
		DefaultMemoryLimitMB: 256,
		DefaultPIDsLimit:     128,
		DefaultDiskLimitMB:   512,
		DefaultNetworkMode:   model.NetworkModeInternetDisabled,
		DefaultAllowTunnels:  true,
		RequestRatePerMinute: 600,
		RequestBurst:         120,
		GracefulShutdown:     5 * time.Second,
		ReconcileInterval:    30 * time.Second,
		CleanupInterval:      30 * time.Second,
		OperatorHost:         "http://example.invalid",
		Tenants:              []config.TenantConfig{{ID: "tenant-dev", Name: "Tenant Dev", Token: "dev-token"}},
		DefaultQuota:         model.TenantQuota{MaxSandboxes: 8, MaxRunningSandboxes: 8, MaxConcurrentExecs: 8, MaxTunnels: 8, MaxCPUCores: model.CPUCores(16), MaxMemoryMB: 8192, MaxStorageMB: 16384, AllowTunnels: true, DefaultTunnelAuthMode: "token", DefaultTunnelVisibility: "private"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.Open(context.Background(), cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	store := repository.New(sqlDB)
	if err := store.SeedTenants(context.Background(), cfg.Tenants, cfg.DefaultQuota); err != nil {
		t.Fatal(err)
	}
	runtime := runtimedocker.New()
	svc := service.New(cfg, store, runtime)
	server := httptest.NewServer(auth.New(store, cfg).Wrap(api.New(logging.New(), svc, cfg)))
	cfg.OperatorHost = server.URL
	svc = service.New(cfg, store, runtime)
	server.Config.Handler = auth.New(store, cfg).Wrap(api.New(logging.New(), svc, cfg))
	return &presetDockerHarness{server: server, db: sqlDB}
}

func (h *presetDockerHarness) close() {
	h.server.Close()
	_ = h.db.Close()
}

func newPresetQEMUHarness(t *testing.T) *presetQEMUHarness {
	t.Helper()
	qemuCfg := requirePresetQEMUConfig(t)
	root := t.TempDir()
	cfg := config.Config{
		DeploymentMode:        "development",
		ListenAddress:         "127.0.0.1:0",
		DatabasePath:          filepath.Join(root, "sandbox.db"),
		StorageRoot:           filepath.Join(root, "storage"),
		SnapshotRoot:          filepath.Join(root, "snapshots"),
		BaseImageRef:          qemuCfg.baseImagePath,
		RuntimeBackend:        "qemu",
		AuthMode:              "static",
		QEMUBinary:            qemuCfg.binary,
		QEMUAccel:             qemuCfg.accel,
		QEMUBaseImagePath:     qemuCfg.baseImagePath,
		QEMUSSHUser:           qemuCfg.sshUser,
		QEMUSSHPrivateKeyPath: qemuCfg.sshKeyPath,
		QEMUBootTimeout:       2 * time.Minute,
		DefaultCPULimit:       model.CPUCores(1),
		DefaultMemoryLimitMB:  1024,
		DefaultPIDsLimit:      256,
		DefaultDiskLimitMB:    2048,
		DefaultNetworkMode:    model.NetworkModeInternetEnabled,
		DefaultAllowTunnels:   true,
		RequestRatePerMinute:  600,
		RequestBurst:          120,
		GracefulShutdown:      5 * time.Second,
		ReconcileInterval:     30 * time.Second,
		CleanupInterval:       30 * time.Second,
		OperatorHost:          "http://example.invalid",
		Tenants:               []config.TenantConfig{{ID: "tenant-dev", Name: "Tenant Dev", Token: "dev-token"}},
		DefaultQuota:          model.TenantQuota{MaxSandboxes: 4, MaxRunningSandboxes: 4, MaxConcurrentExecs: 8, MaxTunnels: 4, MaxCPUCores: model.CPUCores(8), MaxMemoryMB: 8192, MaxStorageMB: 16384, AllowTunnels: true, DefaultTunnelAuthMode: "token", DefaultTunnelVisibility: "private"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.Open(context.Background(), cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	store := repository.New(sqlDB)
	if err := store.SeedTenants(context.Background(), cfg.Tenants, cfg.DefaultQuota); err != nil {
		t.Fatal(err)
	}
	runtime, err := runtimeqemu.New(runtimeqemu.Options{Binary: cfg.QEMUBinary, Accel: cfg.QEMUAccel, BaseImagePath: cfg.QEMUBaseImagePath, SSHUser: cfg.QEMUSSHUser, SSHKeyPath: cfg.QEMUSSHPrivateKeyPath, BootTimeout: cfg.QEMUBootTimeout})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(cfg, store, runtime)
	server := httptest.NewServer(auth.New(store, cfg).Wrap(api.New(logging.New(), svc, cfg)))
	cfg.OperatorHost = server.URL
	svc = service.New(cfg, store, runtime)
	server.Config.Handler = auth.New(store, cfg).Wrap(api.New(logging.New(), svc, cfg))
	return &presetQEMUHarness{server: server, db: sqlDB, baseImagePath: qemuCfg.baseImagePath}
}

func (h *presetQEMUHarness) close() {
	h.server.Close()
	_ = h.db.Close()
}

type presetQEMUConfig struct {
	binary        string
	accel         string
	baseImagePath string
	sshUser       string
	sshKeyPath    string
}

func requirePresetQEMUConfig(t *testing.T) presetQEMUConfig {
	t.Helper()
	cfg := presetQEMUConfig{
		binary:        firstPresetEnv("SANDBOX_QEMU_BINARY", "OR3_QEMU_BINARY"),
		accel:         firstPresetEnv("SANDBOX_QEMU_ACCEL", "OR3_QEMU_ACCEL"),
		baseImagePath: firstPresetEnv("SANDBOX_QEMU_BASE_IMAGE_PATH", "OR3_QEMU_BASE_IMAGE_PATH"),
		sshUser:       firstPresetEnv("SANDBOX_QEMU_SSH_USER", "OR3_QEMU_SSH_USER"),
		sshKeyPath:    firstPresetEnv("SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH", "OR3_QEMU_SSH_PRIVATE_KEY_PATH"),
	}
	if cfg.binary == "" || cfg.baseImagePath == "" || cfg.sshUser == "" || cfg.sshKeyPath == "" {
		t.Skip("qemu preset integration requires SANDBOX_QEMU_BINARY, SANDBOX_QEMU_BASE_IMAGE_PATH, SANDBOX_QEMU_SSH_USER, and SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH")
	}
	if cfg.accel == "" {
		cfg.accel = "auto"
	}
	return cfg
}

func firstPresetEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
