package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"or3-sandbox/internal/api"
	"or3-sandbox/internal/auth"
	"or3-sandbox/internal/config"
	"or3-sandbox/internal/db"
	"or3-sandbox/internal/logging"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
	runtimedocker "or3-sandbox/internal/runtime/docker"
	"or3-sandbox/internal/service"
)

func TestAPILifecycleOwnershipFilesAndSnapshots(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		CPULimit:      1,
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})

	execution := h.exec(t, "token-a", sandbox.ID, model.ExecRequest{
		Command: []string{"sh", "-lc", "echo hello > /workspace/hello.txt && cat /workspace/hello.txt"},
		Cwd:     "/workspace",
		Timeout: 20 * time.Second,
	})
	if execution.Status != model.ExecutionStatusSucceeded {
		t.Fatalf("unexpected execution result: %+v", execution)
	}

	file := h.readFile(t, "token-a", sandbox.ID, "hello.txt")
	if strings.TrimSpace(file.Content) != "hello" {
		t.Fatalf("unexpected file content: %q", file.Content)
	}

	h.expectStatus(t, "token-b", http.MethodGet, "/v1/sandboxes/"+sandbox.ID, nil, http.StatusNotFound)
	h.expectStatus(t, "token-b", http.MethodGet, "/v1/sandboxes/"+sandbox.ID+"/files/hello.txt", nil, http.StatusNotFound)
	h.expectStatus(t, "token-a", http.MethodGet, "/v1/sandboxes/"+sandbox.ID+"/files/%2e%2e/%2e%2e/etc/passwd", nil, http.StatusBadRequest)

	snapshot := h.createSnapshot(t, "token-a", sandbox.ID)
	h.writeFile(t, "token-a", sandbox.ID, "hello.txt", "changed")
	restored := h.restoreSnapshot(t, "token-a", snapshot.ID, sandbox.ID)
	if restored.Status != model.SandboxStatusStopped {
		t.Fatalf("expected stopped sandbox after restore, got %s", restored.Status)
	}
	restored = h.startSandbox(t, "token-a", sandbox.ID)
	file = h.readFile(t, "token-a", sandbox.ID, "hello.txt")
	if strings.TrimSpace(file.Content) != "hello" {
		t.Fatalf("snapshot restore did not recover file content: %q", file.Content)
	}

	h.deleteSandbox(t, "token-a", sandbox.ID)
	inspected := h.inspectSandbox(t, "token-a", sandbox.ID)
	if inspected.Status != model.SandboxStatusDeleted {
		t.Fatalf("expected deleted sandbox, got %s", inspected.Status)
	}
}

func TestTunnelDetachedExecAndNetworkIsolation(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	alpha := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "python:3.12-alpine",
		CPULimit:      1,
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	beta := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "python:3.12-alpine",
		CPULimit:      1,
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})

	h.exec(t, "token-a", alpha.ID, model.ExecRequest{
		Command:  []string{"sh", "-lc", "echo tunnel-ok > /workspace/index.html && python3 -m http.server 8090 -d /workspace"},
		Cwd:      "/workspace",
		Timeout:  30 * time.Second,
		Detached: true,
	})
	h.waitForExecContains(t, "token-a", alpha.ID, model.ExecRequest{
		Command: []string{"python3", "-c", "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:8090').read().decode())"},
		Cwd:     "/workspace",
		Timeout: 5 * time.Second,
	}, "tunnel-ok", 10*time.Second)

	tunnel := h.createTunnel(t, "token-a", alpha.ID, 8090)
	body := h.tunnelGET(t, "token-a", tunnel.ID, tunnel.AccessToken)
	if !strings.Contains(body, "tunnel-ok") {
		t.Fatalf("unexpected tunnel body: %q", body)
	}
	h.expectStatus(t, "token-b", http.MethodDelete, "/v1/tunnels/"+tunnel.ID, nil, http.StatusNotFound)
	h.expectStatus(t, "token-a", http.MethodDelete, "/v1/tunnels/"+tunnel.ID, nil, http.StatusNoContent)
	h.expectStatusWithHeaders(t, "token-a", http.MethodGet, "/v1/tunnels/"+tunnel.ID+"/proxy", nil, http.StatusGone, map[string]string{"X-Tunnel-Token": tunnel.AccessToken})

	tunnel = h.createTunnel(t, "token-a", alpha.ID, 8090)

	h.exec(t, "token-a", alpha.ID, model.ExecRequest{
		Command:  []string{"sh", "-lc", "sleep 2 && echo detached-done > /workspace/done.txt"},
		Cwd:      "/workspace",
		Timeout:  10 * time.Second,
		Detached: true,
	})
	h.waitForFile(t, "token-a", alpha.ID, "done.txt", 10*time.Second)

	alphaState, err := h.runtime.Inspect(context.Background(), alpha)
	if err != nil {
		t.Fatal(err)
	}
	if alphaState.IPAddress == "" {
		t.Fatal("expected alpha sandbox IP address")
	}
	networkProbe := fmt.Sprintf(`import urllib.request
url = "http://%s:8090"
try:
    print(urllib.request.urlopen(url, timeout=3).read().decode())
except Exception:
    pass
`, alphaState.IPAddress)
	result := h.exec(t, "token-a", beta.ID, model.ExecRequest{
		Command: []string{"python3", "-c", networkProbe},
		Cwd:     "/workspace",
		Timeout: 10 * time.Second,
	})
	if strings.Contains(result.StdoutPreview, "tunnel-ok") {
		t.Fatalf("expected east-west network denial, got output %q", result.StdoutPreview)
	}
}

func TestQuotaEnforcement(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	quota := h.cfg.DefaultQuota
	quota.MaxSandboxes = 1
	if err := h.store.SeedTenants(context.Background(), h.cfg.Tenants, quota); err != nil {
		t.Fatal(err)
	}
	_ = h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		CPULimit:      1,
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	h.expectStatus(t, "token-a", http.MethodPost, "/v1/sandboxes", model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		CPULimit:      1,
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	}, http.StatusBadRequest)
}

func TestAllowTunnelsFalseIsRespected(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		CPULimit:      1,
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	if sandbox.AllowTunnels {
		t.Fatal("expected allow_tunnels=false to be preserved")
	}
	h.expectStatus(t, "token-a", http.MethodPost, "/v1/sandboxes/"+sandbox.ID+"/tunnels", model.CreateTunnelRequest{
		TargetPort: 8080,
		Protocol:   model.TunnelProtocolHTTP,
		AuthMode:   "none",
		Visibility: "private",
	}, http.StatusBadRequest)
}

func TestDetachedExecDoesNotConsumeExecQuotaForever(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	quota := h.cfg.DefaultQuota
	quota.MaxConcurrentExecs = 1
	if err := h.store.SeedTenants(context.Background(), h.cfg.Tenants, quota); err != nil {
		t.Fatal(err)
	}
	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "python:3.12-alpine",
		CPULimit:      1,
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	})
	first := h.exec(t, "token-a", sandbox.ID, model.ExecRequest{
		Command:  []string{"sh", "-lc", "sleep 1 && echo detached > /workspace/detached.txt"},
		Cwd:      "/workspace",
		Timeout:  10 * time.Second,
		Detached: true,
	})
	if first.Status != model.ExecutionStatusSucceeded {
		t.Fatalf("unexpected detached exec status: %+v", first)
	}
	second := h.exec(t, "token-a", sandbox.ID, model.ExecRequest{
		Command: []string{"sh", "-lc", "echo ok"},
		Cwd:     "/workspace",
		Timeout: 10 * time.Second,
	})
	if second.Status != model.ExecutionStatusSucceeded {
		t.Fatalf("unexpected second exec status: %+v", second)
	}
}


type harness struct {
	t       *testing.T
	cfg     config.Config
	db      *sql.DB
	store   *repository.Store
	service *service.Service
	runtime *runtimedocker.Runtime
	server  *httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("docker socket not available")
	}
	root := t.TempDir()
	cfg := config.Config{
		ListenAddress:        "127.0.0.1:0",
		DatabasePath:         filepath.Join(root, "sandbox.db"),
		StorageRoot:          filepath.Join(root, "storage"),
		SnapshotRoot:         filepath.Join(root, "snapshots"),
		BaseImageRef:         "alpine:3.20",
		RuntimeBackend:       "docker",
		TrustedDockerRuntime: true,
		DefaultCPULimit:      1,
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
		Tenants: []config.TenantConfig{
			{ID: "tenant-a", Name: "Tenant A", Token: "token-a"},
			{ID: "tenant-b", Name: "Tenant B", Token: "token-b"},
		},
		DefaultQuota: model.TenantQuota{
			MaxSandboxes:            8,
			MaxRunningSandboxes:     8,
			MaxConcurrentExecs:      8,
			MaxTunnels:              8,
			MaxCPUCores:             16,
			MaxMemoryMB:             8192,
			MaxStorageMB:            16384,
			AllowTunnels:            true,
			DefaultTunnelAuthMode:   "token",
			DefaultTunnelVisibility: "private",
		},
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
	server := httptest.NewServer(auth.New(store, cfg).Wrap(api.New(logging.New(), svc)))
	cfg.OperatorHost = server.URL
	svc = service.New(cfg, store, runtime)
	server.Config.Handler = auth.New(store, cfg).Wrap(api.New(logging.New(), svc))
	return &harness{t: t, cfg: cfg, db: sqlDB, store: store, service: svc, runtime: runtime, server: server}
}

func (h *harness) close() {
	sandboxes, _ := h.store.ListNonDeletedSandboxes(context.Background())
	for _, sandbox := range sandboxes {
		_ = h.runtime.Destroy(context.Background(), sandbox)
	}
	h.server.Close()
	_ = h.db.Close()
}

func (h *harness) createSandbox(t *testing.T, token string, req model.CreateSandboxRequest) model.Sandbox {
	t.Helper()
	var sandbox model.Sandbox
	h.mustDoJSON(t, token, http.MethodPost, "/v1/sandboxes", req, &sandbox, http.StatusCreated)
	return sandbox
}

func (h *harness) inspectSandbox(t *testing.T, token, sandboxID string) model.Sandbox {
	t.Helper()
	var sandbox model.Sandbox
	h.mustDoJSON(t, token, http.MethodGet, "/v1/sandboxes/"+sandboxID, nil, &sandbox, http.StatusOK)
	return sandbox
}

func (h *harness) startSandbox(t *testing.T, token, sandboxID string) model.Sandbox {
	t.Helper()
	var sandbox model.Sandbox
	h.mustDoJSON(t, token, http.MethodPost, "/v1/sandboxes/"+sandboxID+"/start", map[string]any{}, &sandbox, http.StatusOK)
	return sandbox
}

func (h *harness) deleteSandbox(t *testing.T, token, sandboxID string) {
	t.Helper()
	h.mustDoJSON(t, token, http.MethodDelete, "/v1/sandboxes/"+sandboxID, nil, nil, http.StatusNoContent)
}

func (h *harness) exec(t *testing.T, token, sandboxID string, req model.ExecRequest) model.Execution {
	t.Helper()
	var execution model.Execution
	h.mustDoJSON(t, token, http.MethodPost, "/v1/sandboxes/"+sandboxID+"/exec", req, &execution, http.StatusOK)
	return execution
}

func (h *harness) readFile(t *testing.T, token, sandboxID, path string) model.FileReadResponse {
	t.Helper()
	var file model.FileReadResponse
	h.mustDoJSON(t, token, http.MethodGet, "/v1/sandboxes/"+sandboxID+"/files/"+path, nil, &file, http.StatusOK)
	return file
}

func (h *harness) writeFile(t *testing.T, token, sandboxID, path, content string) {
	t.Helper()
	h.mustDoJSON(t, token, http.MethodPut, "/v1/sandboxes/"+sandboxID+"/files/"+path, model.FileWriteRequest{Content: content}, nil, http.StatusNoContent)
}

func (h *harness) createSnapshot(t *testing.T, token, sandboxID string) model.Snapshot {
	t.Helper()
	var snapshot model.Snapshot
	h.mustDoJSON(t, token, http.MethodPost, "/v1/sandboxes/"+sandboxID+"/snapshots", model.CreateSnapshotRequest{Name: "test-snap"}, &snapshot, http.StatusCreated)
	return snapshot
}

func (h *harness) restoreSnapshot(t *testing.T, token, snapshotID, sandboxID string) model.Sandbox {
	t.Helper()
	var sandbox model.Sandbox
	h.mustDoJSON(t, token, http.MethodPost, "/v1/snapshots/"+snapshotID+"/restore", model.RestoreSnapshotRequest{TargetSandboxID: sandboxID}, &sandbox, http.StatusOK)
	return sandbox
}

func (h *harness) createTunnel(t *testing.T, token, sandboxID string, port int) model.Tunnel {
	t.Helper()
	var tunnel model.Tunnel
	h.mustDoJSON(t, token, http.MethodPost, "/v1/sandboxes/"+sandboxID+"/tunnels", model.CreateTunnelRequest{
		TargetPort: port,
		Protocol:   model.TunnelProtocolHTTP,
		AuthMode:   "token",
		Visibility: "private",
	}, &tunnel, http.StatusCreated)
	return tunnel
}

func (h *harness) tunnelGET(t *testing.T, token, tunnelID, accessToken string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, h.server.URL+"/v1/tunnels/"+tunnelID+"/proxy", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Tunnel-Token", accessToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected tunnel status %d: %s", response.StatusCode, string(data))
	}
	return string(data)
}

func boolPtr(value bool) *bool {
	return &value
}

func (h *harness) waitForFile(t *testing.T, token, sandboxID, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequest(http.MethodGet, h.server.URL+"/v1/sandboxes/"+sandboxID+"/files/"+path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for file %s", path)
}

func (h *harness) waitForExecContains(t *testing.T, token, sandboxID string, req model.ExecRequest, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result := h.exec(t, token, sandboxID, req)
		if strings.Contains(result.StdoutPreview, want) {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for exec output %q", want)
}

func (h *harness) expectStatus(t *testing.T, token, method, endpoint string, payload any, want int) {
	h.expectStatusWithHeaders(t, token, method, endpoint, payload, want, nil)
}

func (h *harness) expectStatusWithHeaders(t *testing.T, token, method, endpoint string, payload any, want int, headers map[string]string) {
	t.Helper()
	response, body := h.do(t, token, method, endpoint, payload, headers)
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("%s %s returned %d, want %d: %s", method, endpoint, response.StatusCode, want, body)
	}
}

func (h *harness) mustDoJSON(t *testing.T, token, method, endpoint string, payload any, out any, want int) {
	t.Helper()
	response, body := h.do(t, token, method, endpoint, payload, nil)
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("%s %s returned %d, want %d: %s", method, endpoint, response.StatusCode, want, body)
	}
	if out != nil && response.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *harness) do(t *testing.T, token, method, endpoint string, payload any, headers map[string]string) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, h.server.URL+endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	return response, string(data)
}
