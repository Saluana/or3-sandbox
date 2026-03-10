package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	"or3-sandbox/internal/api"
	"or3-sandbox/internal/auth"
	"or3-sandbox/internal/config"
	"or3-sandbox/internal/db"
	"or3-sandbox/internal/guestimage"
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
		CPULimit:      model.CPUCores(1),
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
	listed := h.listSnapshots(t, "token-a", sandbox.ID)
	if len(listed) != 1 || listed[0].ID != snapshot.ID {
		t.Fatalf("unexpected snapshot list: %+v", listed)
	}
	inspectedSnapshot := h.inspectSnapshot(t, "token-a", snapshot.ID)
	if inspectedSnapshot.ID != snapshot.ID {
		t.Fatalf("unexpected snapshot inspect result: %+v", inspectedSnapshot)
	}
	h.expectStatus(t, "token-b", http.MethodGet, "/v1/sandboxes/"+sandbox.ID+"/snapshots", nil, http.StatusNotFound)
	h.expectStatus(t, "token-b", http.MethodGet, "/v1/snapshots/"+snapshot.ID, nil, http.StatusNotFound)
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
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	beta := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "python:3.12-alpine",
		CPULimit:      model.CPUCores(1),
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
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	h.expectStatus(t, "token-a", http.MethodPost, "/v1/sandboxes", model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	}, http.StatusBadRequest)
}

func TestCreateSandboxAcceptsFractionalCPUOnDocker(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	quota := h.cfg.DefaultQuota
	quota.MaxCPUCores = model.MustParseCPUQuantity("2")
	if err := h.store.SeedTenants(context.Background(), h.cfg.Tenants, quota); err != nil {
		t.Fatal(err)
	}

	var sandbox model.Sandbox
	h.mustDoRawJSON(t, "token-a", http.MethodPost, "/v1/sandboxes", `{
		"base_image_ref":"alpine:3.20",
		"cpu_limit":1.5,
		"memory_limit_mb":256,
		"pids_limit":128,
		"disk_limit_mb":512,
		"network_mode":"internet-disabled",
		"allow_tunnels":false,
		"start":false
	}`, &sandbox, http.StatusCreated)
	if sandbox.CPULimit != model.MustParseCPUQuantity("1500m") {
		t.Fatalf("unexpected fractional cpu %v", sandbox.CPULimit)
	}

	h.expectStatus(t, "token-a", http.MethodPost, "/v1/sandboxes", map[string]any{
		"base_image_ref":  "alpine:3.20",
		"cpu_limit":       "600m",
		"memory_limit_mb": 256,
		"pids_limit":      128,
		"disk_limit_mb":   512,
		"network_mode":    model.NetworkModeInternetDisabled,
		"allow_tunnels":   false,
		"start":           false,
	}, http.StatusBadRequest)
}

func TestCreateSandboxRejectsFractionalCPUOnQEMU(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	h.expectStatus(t, "token-a", http.MethodPost, "/v1/sandboxes", map[string]any{
		"base_image_ref":  "guest-base.qcow2",
		"cpu_limit":       "500m",
		"memory_limit_mb": 256,
		"pids_limit":      128,
		"disk_limit_mb":   512,
		"network_mode":    model.NetworkModeInternetDisabled,
		"allow_tunnels":   false,
		"start":           false,
	}, http.StatusBadRequest)
}

func TestStartAdmissionDenialAppearsInMetrics(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	h.cfg.AdmissionMaxTenantStarts = 1
	h.service = service.New(h.cfg, h.store, h.stubRuntime)
	h.server.Config.Handler = auth.New(h.store, h.cfg).Wrap(api.New(logging.New(), h.service, h.cfg))

	first := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	second := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})
	first.Status = model.SandboxStatusStarting
	first.RuntimeStatus = string(model.SandboxStatusStarting)
	first.UpdatedAt = time.Now().UTC()
	if err := h.store.UpdateSandboxState(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	h.expectStatus(t, "token-a", http.MethodPost, "/v1/sandboxes/"+second.ID+"/start", map[string]any{}, http.StatusTooManyRequests)

	request, err := http.NewRequest(http.MethodGet, h.server.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer token-a")
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
		t.Fatalf("unexpected metrics status %d: %s", response.StatusCode, string(data))
	}
	if !strings.Contains(string(data), `or3_sandbox_admission_denials_total{action="admission.start"} 1`) {
		t.Fatalf("expected admission denial metric, got %s", string(data))
	}
}

func TestCreateSandboxRejectsDockerProfileMismatch(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	h.expectStatus(t, "token-a", http.MethodPost, "/v1/sandboxes", map[string]any{
		"base_image_ref":  "alpine:3.20",
		"profile":         "browser",
		"cpu_limit":       1,
		"memory_limit_mb": 256,
		"pids_limit":      128,
		"disk_limit_mb":   512,
		"network_mode":    model.NetworkModeInternetDisabled,
		"allow_tunnels":   false,
		"start":           false,
	}, http.StatusBadRequest)
}

func TestAllowTunnelsFalseIsRespected(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		CPULimit:      model.CPUCores(1),
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
		CPULimit:      model.CPUCores(1),
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
	if first.Status != model.ExecutionStatusDetached {
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

func TestRuntimeInfoIsTenantReadable(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	var info model.RuntimeInfo
	h.mustDoJSON(t, "token-a", http.MethodGet, "/v1/runtime/info", nil, &info, http.StatusOK)
	if info.Backend != "qemu" {
		t.Fatalf("unexpected runtime info %+v", info)
	}
	if info.Class != string(model.RuntimeClassVM) {
		t.Fatalf("expected runtime class %q, got %q", model.RuntimeClassVM, info.Class)
	}
}

func TestRuntimeHealthEndpoint(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         true,
	})

	var health model.RuntimeHealth
	h.mustDoJSON(t, "token-a", http.MethodGet, "/v1/runtime/health", nil, &health, http.StatusOK)
	if health.Backend != "docker" {
		t.Fatalf("unexpected backend %q", health.Backend)
	}
	if len(health.Sandboxes) == 0 {
		t.Fatal("expected sandbox health entries")
	}
	if health.Sandboxes[0].SandboxID != sandbox.ID {
		t.Fatalf("unexpected sandbox health entry %+v", health.Sandboxes[0])
	}
}

func TestBinaryFileUploadAndDownload(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "alpine:3.20",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(false),
		Start:         false,
	})

	pngData := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01}
	h.mustDoJSON(t, "token-a", http.MethodPut, "/v1/sandboxes/"+sandbox.ID+"/files/pixel.png", model.FileWriteRequest{Encoding: "base64", ContentBase64: base64.StdEncoding.EncodeToString(pngData)}, nil, http.StatusNoContent)

	request, err := http.NewRequest(http.MethodGet, h.server.URL+"/v1/sandboxes/"+sandbox.ID+"/files/pixel.png?encoding=base64", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer token-a")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, body)
	}
	var file model.FileReadResponse
	if err := json.NewDecoder(response.Body).Decode(&file); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(file.ContentBase64)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(pngData) {
		t.Fatalf("unexpected decoded payload: %v", decoded)
	}
}

func TestJWTAuthorizationEnforcesRolePermissions(t *testing.T) {
	h := newJWTStubHarness(t)
	defer h.close()

	adminToken := h.jwtToken(t, "tenant-a", []string{"admin"}, false)
	viewerToken := h.jwtToken(t, "tenant-a", []string{"viewer"}, false)
	serviceToken := h.jwtToken(t, "tenant-a", nil, true)
	invalidToken := h.jwtTokenWithSecret(t, "wrong-secret", "tenant-a", []string{"admin"}, false)

	sandbox := h.createSandbox(t, adminToken, model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetDisabled,
		AllowTunnels:  boolPtr(true),
		Start:         false,
	})

	h.expectStatus(t, invalidToken, http.MethodGet, "/v1/sandboxes", nil, http.StatusUnauthorized)
	h.expectStatus(t, viewerToken, http.MethodGet, "/v1/sandboxes/"+sandbox.ID, nil, http.StatusOK)
	h.expectStatus(t, viewerToken, http.MethodPost, "/v1/sandboxes/"+sandbox.ID+"/start", map[string]any{}, http.StatusForbidden)
	h.expectStatus(t, viewerToken, http.MethodPut, "/v1/sandboxes/"+sandbox.ID+"/files/notes.txt", model.FileWriteRequest{Content: "nope"}, http.StatusForbidden)
	h.expectStatus(t, viewerToken, http.MethodPost, "/v1/sandboxes/"+sandbox.ID+"/snapshots", model.CreateSnapshotRequest{Name: "snap"}, http.StatusForbidden)
	h.expectStatus(t, viewerToken, http.MethodPost, "/v1/sandboxes/"+sandbox.ID+"/tunnels", model.CreateTunnelRequest{TargetPort: 8080, Protocol: model.TunnelProtocolHTTP, AuthMode: "token", Visibility: "private"}, http.StatusForbidden)
	h.expectStatus(t, viewerToken, http.MethodGet, "/v1/runtime/health", nil, http.StatusForbidden)
	h.expectStatus(t, viewerToken, http.MethodGet, "/v1/runtime/capacity", nil, http.StatusForbidden)
	h.expectStatus(t, viewerToken, http.MethodGet, "/metrics", nil, http.StatusForbidden)
	h.expectStatus(t, serviceToken, http.MethodGet, "/v1/runtime/health", nil, http.StatusOK)
	h.expectStatus(t, serviceToken, http.MethodGet, "/v1/runtime/capacity", nil, http.StatusOK)
	h.expectStatus(t, serviceToken, http.MethodGet, "/metrics", nil, http.StatusOK)
}

func TestTunnelProxyTargetsSandboxLocalhost(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	body := h.tunnelGET(t, "token-a", tunnel.ID, tunnel.AccessToken)
	if body != "proxy-ok" {
		t.Fatalf("unexpected tunnel body %q", body)
	}
	if h.stubRuntime.lastProxyPath != "/" {
		t.Fatalf("expected root tunnel path, got %q", h.stubRuntime.lastProxyPath)
	}
	h.expectStatus(t, "token-a", http.MethodDelete, "/v1/tunnels/"+tunnel.ID, nil, http.StatusNoContent)
	h.expectStatusWithHeaders(t, "token-a", http.MethodGet, "/v1/tunnels/"+tunnel.ID+"/proxy", nil, http.StatusGone, map[string]string{"X-Tunnel-Token": tunnel.AccessToken})
}

func TestTunnelProxyReturnsBadGatewayWhenSandboxRequestFails(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()
	h.stubRuntime.tunnelBridgeAddr = "127.0.0.1:1"

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	h.expectStatusWithHeaders(t, "token-a", http.MethodGet, "/v1/tunnels/"+tunnel.ID+"/proxy/healthz", nil, http.StatusBadGateway, map[string]string{"X-Tunnel-Token": tunnel.AccessToken})
}

func TestTunnelSignedURLBootstrapsBrowserSession(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	signed := h.createTunnelSignedURL(t, "token-a", tunnel.ID, model.CreateTunnelSignedURLRequest{Path: "/"})

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Step 1: visit the signed URL → expect an HTML bootstrap page that
	// sets the session cookie and clears stale localStorage.
	client := &http.Client{Jar: jar}
	response, err := client.Get(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected signed tunnel status %d: %s", response.StatusCode, string(body))
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "window.location.replace") {
		t.Fatalf("expected bootstrap page with JS redirect, got %q", string(body))
	}
	proxyURL, err := url.Parse(h.server.URL + "/v1/tunnels/" + tunnel.ID + "/proxy/")
	if err != nil {
		t.Fatal(err)
	}
	if len(jar.Cookies(proxyURL)) == 0 {
		t.Fatal("expected tunnel auth cookie to be set")
	}
	// Step 2: follow the redirect manually (the browser would do this via
	// the JS redirect in the bootstrap page) → proxy reaches upstream.
	cleanURL := h.server.URL + "/v1/tunnels/" + tunnel.ID + "/proxy/"
	response2, err := client.Get(cleanURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response2.Body.Close()
	body2, _ := io.ReadAll(response2.Body)
	if response2.StatusCode != http.StatusOK {
		t.Fatalf("unexpected proxied status %d: %s", response2.StatusCode, string(body2))
	}
	if string(body2) != "proxy-ok" {
		t.Fatalf("unexpected proxied body %q", string(body2))
	}
	if h.stubRuntime.lastProxyPath != "/" {
		t.Fatalf("expected signed tunnel proxy root path, got %q", h.stubRuntime.lastProxyPath)
	}
}

func TestTunnelSignedURLRejectsTampering(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	signed := h.createTunnelSignedURL(t, "token-a", tunnel.ID, model.CreateTunnelSignedURLRequest{Path: "/"})

	parsed, err := url.Parse(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("or3_sig", query.Get("or3_sig")+"tamper")
	parsed.RawQuery = query.Encode()

	response, err := http.Get(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected tampered tunnel status %d: %s", response.StatusCode, string(body))
	}
}

func TestTunnelSignedURLRejectsPathTampering(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	signed := h.createTunnelSignedURL(t, "token-a", tunnel.ID, model.CreateTunnelSignedURLRequest{Path: "/allowed"})

	parsed, err := url.Parse(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = strings.Replace(parsed.Path, "/allowed", "/other", 1)

	response, err := http.Get(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected tampered-path status %d: %s", response.StatusCode, string(body))
	}
}

func TestTunnelSignedURLCookieRejectsQueryTampering(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	signed := h.createTunnelSignedURL(t, "token-a", tunnel.ID, model.CreateTunnelSignedURLRequest{Path: "/app?mode=ro"})

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	response, err := client.Get(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "window.location.replace") {
		t.Fatalf("unexpected signed tunnel bootstrap status %d: %s", response.StatusCode, string(body))
	}

	response, err = client.Get(h.server.URL + "/v1/tunnels/" + tunnel.ID + "/proxy/app?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected signed tunnel proxied status %d: %s", response.StatusCode, string(body))
	}

	response, err = client.Get(h.server.URL + "/v1/tunnels/" + tunnel.ID + "/proxy/app?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unexpected tampered-query status %d: %s", response.StatusCode, string(body))
	}
}

func TestTunnelSignedURLBootstrapsEscapedPath(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	signed := h.createTunnelSignedURL(t, "token-a", tunnel.ID, model.CreateTunnelSignedURLRequest{Path: "/allowed%2Fsegment"})

	response, err := http.Get(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "window.location.replace") {
		t.Fatalf("unexpected escaped-path bootstrap status %d: %s", response.StatusCode, string(body))
	}
}

func TestTunnelSignedURLRejectsTTLAboveMaximum(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	h.expectStatus(t, "token-a", http.MethodPost, "/v1/tunnels/"+tunnel.ID+"/signed-url", model.CreateTunnelSignedURLRequest{Path: "/", TTLSeconds: 3600}, http.StatusBadRequest)
}

func TestTunnelSignedURLRejectsInvalidPath(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	h.expectStatus(t, "token-a", http.MethodPost, "/v1/tunnels/"+tunnel.ID+"/signed-url", model.CreateTunnelSignedURLRequest{Path: "relative/path"}, http.StatusBadRequest)
}

func TestTunnelSignedURLRejectsCrossTenantIssuance(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	h.expectStatus(t, "token-b", http.MethodPost, "/v1/tunnels/"+tunnel.ID+"/signed-url", model.CreateTunnelSignedURLRequest{Path: "/"}, http.StatusNotFound)
}

func TestTunnelSignedURLExpires(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	signed := h.createTunnelSignedURL(t, "token-a", tunnel.ID, model.CreateTunnelSignedURLRequest{Path: "/", TTLSeconds: 1})

	time.Sleep(1100 * time.Millisecond)
	response, err := http.Get(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected expired tunnel status %d: %s", response.StatusCode, string(body))
	}
}

func TestTunnelSignedURLSurvivesHandlerRestart(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	signed := h.createTunnelSignedURL(t, "token-a", tunnel.ID, model.CreateTunnelSignedURLRequest{Path: "/"})

	h.server.Config.Handler = auth.New(h.store, h.cfg).Wrap(api.New(logging.New(), h.service, h.cfg))

	response, err := http.Get(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected signed tunnel status after restart %d: %s", response.StatusCode, string(body))
	}
}

func TestTunnelSignedURLBootstrapSetsExpectedCookieFlags(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	h.cfg.OperatorHost = "https://operator.example"
	h.service = service.New(h.cfg, h.store, h.stubRuntime)
	h.server.Config.Handler = auth.New(h.store, h.cfg).Wrap(api.New(logging.New(), h.service, h.cfg))

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	signed := h.createTunnelSignedURL(t, "token-a", tunnel.ID, model.CreateTunnelSignedURLRequest{Path: "/"})
	parsed, err := url.Parse(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(h.server.URL + parsed.RequestURI())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected signed tunnel status %d: %s", response.StatusCode, string(body))
	}
	setCookie := response.Header.Get("Set-Cookie")
	if !strings.Contains(setCookie, "HttpOnly") {
		t.Fatalf("expected HttpOnly cookie, got %q", setCookie)
	}
	if !strings.Contains(setCookie, "Secure") {
		t.Fatalf("expected Secure cookie, got %q", setCookie)
	}
	if !strings.Contains(setCookie, "SameSite=Lax") {
		t.Fatalf("expected SameSite=Lax cookie, got %q", setCookie)
	}
	if !strings.Contains(setCookie, "Path=/v1/tunnels/"+tunnel.ID+"/proxy") {
		t.Fatalf("expected scoped tunnel cookie path, got %q", setCookie)
	}
}

func TestTunnelProxyMismatchDoesNotPolluteTargetTenantAudit(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)

	before, err := h.store.ListAuditEvents(context.Background(), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	h.expectStatus(t, "token-b", http.MethodGet, "/v1/tunnels/"+tunnel.ID+"/proxy", nil, http.StatusNotFound)
	after, err := h.store.ListAuditEvents(context.Background(), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("expected cross-tenant tunnel probe to avoid tenant-a audit log, before=%d after=%d events=%+v", len(before), len(after), after)
	}
}

func TestTunnelSignedURLRevokedCookieReturnsGone(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	signed := h.createTunnelSignedURL(t, "token-a", tunnel.ID, model.CreateTunnelSignedURLRequest{Path: "/"})

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	response, err := client.Get(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	h.expectStatus(t, "token-a", http.MethodDelete, "/v1/tunnels/"+tunnel.ID, nil, http.StatusNoContent)

	response, err = client.Get(h.server.URL + "/v1/tunnels/" + tunnel.ID + "/proxy/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusGone {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected revoked tunnel status %d: %s", response.StatusCode, string(body))
	}
}

func TestTunnelSignedURLAuditRedactsQuerySecrets(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	secret := "signed-url-secret"

	h.createTunnelSignedURL(t, "token-a", tunnel.ID, model.CreateTunnelSignedURLRequest{
		Path: "/oauth/callback?token=" + secret + "&mode=debug",
	})

	events, err := h.store.ListAuditEvents(context.Background(), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Action != "tunnel.signed_url" || event.Outcome != "ok" {
			continue
		}
		if strings.Contains(event.Message, secret) {
			t.Fatalf("expected signed-url audit to redact query secrets, got %+v", event)
		}
		if strings.Contains(event.Message, "?") {
			t.Fatalf("expected signed-url audit path to omit query string, got %+v", event)
		}
		return
	}
	t.Fatalf("expected signed-url audit event, got %+v", events)
}

func TestTunnelWebSocketProxyForwardsMessages(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()
	upstream := newTunnelWebSocketUpstream(t)
	defer upstream.close()
	h.stubRuntime.tunnelBridgeAddr = upstream.addr

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	wsURL := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/v1/tunnels/" + tunnel.ID + "/proxy/socket"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization":  []string{"Bearer token-a"},
		"X-Tunnel-Token": []string{tunnel.AccessToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.TextMessage || string(payload) != "echo:hello" {
		t.Fatalf("unexpected proxied websocket payload type=%d body=%q", messageType, string(payload))
	}
	if h.stubRuntime.lastTTY.Env["OR3_TUNNEL_TARGET_PORT"] != "8090" {
		t.Fatalf("expected tunnel bridge env port, got %q", h.stubRuntime.lastTTY.Env["OR3_TUNNEL_TARGET_PORT"])
	}
}

func TestTunnelWebSocketSignedCookieAllowsSameOrigin(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()
	upstream := newTunnelWebSocketUpstream(t)
	defer upstream.close()
	h.stubRuntime.tunnelBridgeAddr = upstream.addr

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	signed := h.createTunnelSignedURL(t, "token-a", tunnel.ID, model.CreateTunnelSignedURLRequest{Path: "/"})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Jar: jar}
	response, err := httpClient.Get(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	proxyURL := strings.TrimRight(h.server.URL, "/") + "/v1/tunnels/" + tunnel.ID + "/proxy/socket"
	parsedProxyURL, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	if len(jar.Cookies(parsedProxyURL)) == 0 {
		t.Fatal("expected signed tunnel cookie to be present for websocket dial")
	}
	dialer := websocket.Dialer{Jar: jar}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(proxyURL, "http"), http.Header{"Origin": []string{h.server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("signed")); err != nil {
		t.Fatal(err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "echo:signed" {
		t.Fatalf("unexpected signed websocket payload %q", string(payload))
	}
}

func TestTunnelProxyPreservesApplicationTokenQueryWhenHeaderAuthUsed(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)

	h.expectStatusWithHeaders(t, "token-a", http.MethodGet, "/v1/tunnels/"+tunnel.ID+"/proxy/app?token=upstream-token&mode=debug", nil, http.StatusOK, map[string]string{"X-Tunnel-Token": tunnel.AccessToken})
	if h.stubRuntime.lastProxyPath != "/app" || h.stubRuntime.lastProxyQuery != "mode=debug&token=upstream-token" {
		t.Fatalf("expected upstream app token query to be preserved, got path=%q query=%q", h.stubRuntime.lastProxyPath, h.stubRuntime.lastProxyQuery)
	}
}

func TestTunnelProxyRemovesTunnelTokenFromUpstreamQuery(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)

	h.expectStatus(t, "token-a", http.MethodGet, "/v1/tunnels/"+tunnel.ID+"/proxy/app?token="+url.QueryEscape(tunnel.AccessToken)+"&mode=debug", nil, http.StatusOK)
	if h.stubRuntime.lastProxyPath != "/app" || h.stubRuntime.lastProxyQuery != "mode=debug" {
		t.Fatalf("expected tunnel auth token to be stripped from upstream query, got path=%q query=%q", h.stubRuntime.lastProxyPath, h.stubRuntime.lastProxyQuery)
	}
}

func TestTunnelProxyForwardsBodyAndStripsControlPlaneAuth(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	body := bytes.NewBufferString("hello from client")
	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/v1/tunnels/"+tunnel.ID+"/proxy/api", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer token-a")
	req.Header.Set("X-Tunnel-Token", tunnel.AccessToken)
	req.Header.Set("Content-Type", "text/plain")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(payload))
	}
	if h.stubRuntime.lastProxyPath != "/api" || h.stubRuntime.lastProxyBody != "hello from client" {
		t.Fatalf("expected proxied body and path, got path=%q body=%q", h.stubRuntime.lastProxyPath, h.stubRuntime.lastProxyBody)
	}
	if got := h.stubRuntime.lastProxyHeaders.Get("Authorization"); got != "" {
		t.Fatalf("expected control-plane authorization to be stripped, got %q", got)
	}
	if got := h.stubRuntime.lastProxyHeaders.Get("X-Tunnel-Token"); got != "" {
		t.Fatalf("expected tunnel auth header to be stripped, got %q", got)
	}
	if got := h.stubRuntime.lastProxyHeaders.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("expected content type to be preserved, got %q", got)
	}
}

func TestTunnelWebSocketSignedCookieRejectsCrossOrigin(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()
	upstream := newTunnelWebSocketUpstream(t)
	defer upstream.close()
	h.stubRuntime.tunnelBridgeAddr = upstream.addr

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})
	tunnel := h.createTunnel(t, "token-a", sandbox.ID, 8090)
	signed := h.createTunnelSignedURL(t, "token-a", tunnel.ID, model.CreateTunnelSignedURLRequest{Path: "/"})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Jar: jar}
	response, err := httpClient.Get(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	proxyURL := strings.TrimRight(h.server.URL, "/") + "/v1/tunnels/" + tunnel.ID + "/proxy/socket"
	dialer := websocket.Dialer{Jar: jar}
	_, response, err = dialer.Dial("ws"+strings.TrimPrefix(proxyURL, "http"), http.Header{"Origin": []string{"http://evil.example"}})
	if err == nil {
		t.Fatal("expected cross-origin websocket upgrade to be rejected")
	}
	if response == nil {
		t.Fatal("expected websocket handshake response for rejected cross-origin request")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected cross-origin websocket upgrade status 403, got %d", response.StatusCode)
	}
}

func TestWriteFileRejectsMissingContentBase64WhenEncodingBase64(t *testing.T) {
	h := newStubHarness(t)
	defer h.close()

	sandbox := h.createSandbox(t, "token-a", model.CreateSandboxRequest{
		BaseImageRef:  "guest-base.qcow2",
		CPULimit:      model.CPUCores(1),
		MemoryLimitMB: 256,
		PIDsLimit:     128,
		DiskLimitMB:   512,
		NetworkMode:   model.NetworkModeInternetEnabled,
		AllowTunnels:  boolPtr(true),
		Start:         true,
	})

	h.expectStatus(t, "token-a", http.MethodPut, "/v1/sandboxes/"+sandbox.ID+"/files/notes.txt", model.FileWriteRequest{Encoding: "base64", Content: "hello"}, http.StatusBadRequest)
}

type harness struct {
	t           *testing.T
	cfg         config.Config
	db          *sql.DB
	store       *repository.Store
	service     *service.Service
	runtime     *runtimedocker.Runtime
	stubRuntime *apiStubRuntime
	server      *httptest.Server
	jwtSecret   string
	jwtIssuer   string
	jwtAudience string
}

func newHarness(t *testing.T) *harness {
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
		Tenants: []config.TenantConfig{
			{ID: "tenant-a", Name: "Tenant A", Token: "token-a"},
			{ID: "tenant-b", Name: "Tenant B", Token: "token-b"},
		},
		DefaultQuota: model.TenantQuota{
			MaxSandboxes:            8,
			MaxRunningSandboxes:     8,
			MaxConcurrentExecs:      8,
			MaxTunnels:              8,
			MaxCPUCores:             model.CPUCores(16),
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
	server := httptest.NewServer(auth.New(store, cfg).Wrap(api.New(logging.New(), svc, cfg)))
	cfg.OperatorHost = server.URL
	svc = service.New(cfg, store, runtime)
	server.Config.Handler = auth.New(store, cfg).Wrap(api.New(logging.New(), svc, cfg))
	return &harness{t: t, cfg: cfg, db: sqlDB, store: store, service: svc, runtime: runtime, server: server}
}

func newStubHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	qemuImage := filepath.Join(root, "guest-base.qcow2")
	if err := os.WriteFile(qemuImage, []byte("qcow2"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeGuestImageContract(t, qemuImage, guestimage.Contract{
		ContractVersion:          model.DefaultImageContractVersion,
		ImagePath:                qemuImage,
		Profile:                  model.GuestProfileCore,
		Control:                  guestimage.ControlContract{Mode: model.GuestControlModeAgent, ProtocolVersion: model.DefaultGuestControlProtocolVersion},
		WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
		Capabilities:             []string{"exec", "files", "pty"},
	})
	cfg := config.Config{
		DeploymentMode:            "development",
		ListenAddress:             "127.0.0.1:0",
		DatabasePath:              filepath.Join(root, "sandbox.db"),
		StorageRoot:               filepath.Join(root, "storage"),
		SnapshotRoot:              filepath.Join(root, "snapshots"),
		BaseImageRef:              "guest-base.qcow2",
		RuntimeBackend:            "qemu",
		QEMUBaseImagePath:         qemuImage,
		QEMUAllowedBaseImagePaths: []string{qemuImage},
		QEMUAllowedProfiles:       []model.GuestProfile{model.GuestProfileCore},
		AuthMode:                  "static",
		DefaultCPULimit:           model.CPUCores(1),
		DefaultMemoryLimitMB:      256,
		DefaultPIDsLimit:          128,
		DefaultDiskLimitMB:        512,
		DefaultNetworkMode:        model.NetworkModeInternetDisabled,
		DefaultAllowTunnels:       true,
		RequestRatePerMinute:      600,
		RequestBurst:              120,
		GracefulShutdown:          5 * time.Second,
		ReconcileInterval:         30 * time.Second,
		CleanupInterval:           30 * time.Second,
		OperatorHost:              "http://example.invalid",
		Tenants: []config.TenantConfig{
			{ID: "tenant-a", Name: "Tenant A", Token: "token-a"},
			{ID: "tenant-b", Name: "Tenant B", Token: "token-b"},
		},
		DefaultQuota: model.TenantQuota{
			MaxSandboxes:            8,
			MaxRunningSandboxes:     8,
			MaxConcurrentExecs:      8,
			MaxTunnels:              8,
			MaxCPUCores:             model.CPUCores(16),
			MaxMemoryMB:             8192,
			MaxStorageMB:            16384,
			AllowTunnels:            true,
			DefaultTunnelAuthMode:   "token",
			DefaultTunnelVisibility: "private",
		},
	}
	sqlDB, err := db.Open(context.Background(), cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	store := repository.New(sqlDB)
	if err := store.SeedTenants(context.Background(), cfg.Tenants, cfg.DefaultQuota); err != nil {
		t.Fatal(err)
	}
	runtime := &apiStubRuntime{}
	runtime.enableHTTPBridge()
	svc := service.New(cfg, store, runtime)
	server := httptest.NewServer(auth.New(store, cfg).Wrap(api.New(logging.New(), svc, cfg)))
	cfg.OperatorHost = server.URL
	svc = service.New(cfg, store, runtime)
	server.Config.Handler = auth.New(store, cfg).Wrap(api.New(logging.New(), svc, cfg))
	return &harness{t: t, cfg: cfg, db: sqlDB, store: store, service: svc, stubRuntime: runtime, server: server}
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

func newJWTStubHarness(t *testing.T) *harness {
	t.Helper()
	h := newStubHarness(t)
	secretPath := filepath.Join(t.TempDir(), "jwt.secret")
	secret := "jwt-test-secret"
	if err := os.WriteFile(secretPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	h.cfg.AuthMode = "jwt-hs256"
	h.cfg.AuthJWTIssuer = "issuer.example"
	h.cfg.AuthJWTAudience = "sandbox-api"
	h.cfg.AuthJWTSecretPaths = []string{secretPath}
	h.server.Config.Handler = auth.New(h.store, h.cfg).Wrap(api.New(logging.New(), h.service, h.cfg))
	h.jwtSecret = secret
	h.jwtIssuer = h.cfg.AuthJWTIssuer
	h.jwtAudience = h.cfg.AuthJWTAudience
	return h
}

func (h *harness) close() {
	sandboxes, _ := h.store.ListNonDeletedSandboxes(context.Background())
	for _, sandbox := range sandboxes {
		if h.runtime != nil {
			_ = h.runtime.Destroy(context.Background(), sandbox)
		}
		if h.stubRuntime != nil {
			h.stubRuntime.close()
			_ = h.stubRuntime.Destroy(context.Background(), sandbox)
		}
	}
	h.server.Close()
	_ = h.db.Close()
}

type apiStubRuntime struct {
	lastExec         model.ExecRequest
	lastTTY          model.TTYRequest
	execResult       model.ExecResult
	execStdout       string
	execStderr       string
	tunnelBridgeAddr string
	tunnelBridgeHTTP *httptest.Server
	lastProxyMethod  string
	lastProxyPath    string
	lastProxyQuery   string
	lastProxyBody    string
	lastProxyHeaders http.Header
}

func (r *apiStubRuntime) Create(context.Context, model.SandboxSpec) (model.RuntimeState, error) {
	return model.RuntimeState{RuntimeID: "qemu-stub", Status: model.SandboxStatusStopped}, nil
}

func (r *apiStubRuntime) Start(context.Context, model.Sandbox) (model.RuntimeState, error) {
	return model.RuntimeState{RuntimeID: "qemu-stub", Status: model.SandboxStatusRunning, Running: true, IPAddress: "127.0.0.1"}, nil
}

func (r *apiStubRuntime) Stop(context.Context, model.Sandbox, bool) (model.RuntimeState, error) {
	return model.RuntimeState{RuntimeID: "qemu-stub", Status: model.SandboxStatusStopped}, nil
}

func (r *apiStubRuntime) Suspend(context.Context, model.Sandbox) (model.RuntimeState, error) {
	return model.RuntimeState{}, errors.New("not implemented")
}

func (r *apiStubRuntime) Resume(context.Context, model.Sandbox) (model.RuntimeState, error) {
	return model.RuntimeState{}, errors.New("not implemented")
}

func (r *apiStubRuntime) Destroy(context.Context, model.Sandbox) error {
	return nil
}

func (r *apiStubRuntime) Inspect(context.Context, model.Sandbox) (model.RuntimeState, error) {
	return model.RuntimeState{RuntimeID: "qemu-stub", Status: model.SandboxStatusRunning, Running: true, IPAddress: "127.0.0.1"}, nil
}

func (r *apiStubRuntime) Exec(_ context.Context, _ model.Sandbox, req model.ExecRequest, streams model.ExecStreams) (model.ExecHandle, error) {
	r.lastExec = req
	stdout := r.execStdout
	if stdout == "" {
		stdout = "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nproxy-ok"
	}
	stderr := r.execStderr
	if streams.Stdout != nil {
		_, _ = io.WriteString(streams.Stdout, stdout)
	}
	if streams.Stderr != nil && stderr != "" {
		_, _ = io.WriteString(streams.Stderr, stderr)
	}
	result := r.execResult
	if result.Status == "" {
		result = model.ExecResult{
			ExitCode:    0,
			Status:      model.ExecutionStatusSucceeded,
			StartedAt:   time.Now().UTC(),
			CompletedAt: time.Now().UTC(),
		}
	}
	return &apiExecHandle{result: result}, nil
}

func (r *apiStubRuntime) enableHTTPBridge() {
	r.tunnelBridgeHTTP = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.lastProxyMethod = req.Method
		r.lastProxyPath = req.URL.Path
		r.lastProxyQuery = req.URL.RawQuery
		r.lastProxyBody = string(body)
		r.lastProxyHeaders = req.Header.Clone()
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "proxy-ok")
	}))
	parsed, _ := url.Parse(r.tunnelBridgeHTTP.URL)
	r.tunnelBridgeAddr = parsed.Host
}

func (r *apiStubRuntime) close() {
	if r.tunnelBridgeHTTP != nil {
		r.tunnelBridgeHTTP.Close()
	}
}

func (r *apiStubRuntime) AttachTTY(_ context.Context, _ model.Sandbox, req model.TTYRequest) (model.TTYHandle, error) {
	r.lastTTY = req
	if r.tunnelBridgeAddr == "" {
		return nil, errors.New("not implemented")
	}
	conn, err := net.Dial("tcp", r.tunnelBridgeAddr)
	if err != nil {
		return nil, err
	}
	reader := io.MultiReader(strings.NewReader("__OR3_TUNNEL_BRIDGE_READY__\n"), conn)
	return &stubTTYHandle{reader: reader, writer: conn, closer: conn}, nil
}

func (r *apiStubRuntime) CreateSnapshot(context.Context, model.Sandbox, string) (model.SnapshotInfo, error) {
	return model.SnapshotInfo{}, nil
}

func (r *apiStubRuntime) RestoreSnapshot(context.Context, model.Sandbox, model.Snapshot) (model.RuntimeState, error) {
	return model.RuntimeState{RuntimeID: "qemu-stub", Status: model.SandboxStatusStopped}, nil
}

type apiExecHandle struct {
	result model.ExecResult
}

type stubTTYHandle struct {
	reader io.Reader
	writer io.Writer
	closer io.Closer
}

func (h *stubTTYHandle) Reader() io.Reader {
	return h.reader
}

func (h *stubTTYHandle) Writer() io.Writer {
	return h.writer
}

func (h *stubTTYHandle) Resize(model.ResizeRequest) error {
	return nil
}

func (h *stubTTYHandle) Close() error {
	if h.closer != nil {
		return h.closer.Close()
	}
	return nil
}

func (h *apiExecHandle) Wait() model.ExecResult {
	return h.result
}

func (h *apiExecHandle) Cancel() error {
	return nil
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

func (h *harness) listSnapshots(t *testing.T, token, sandboxID string) []model.Snapshot {
	t.Helper()
	var snapshots []model.Snapshot
	h.mustDoJSON(t, token, http.MethodGet, "/v1/sandboxes/"+sandboxID+"/snapshots", nil, &snapshots, http.StatusOK)
	return snapshots
}

func (h *harness) inspectSnapshot(t *testing.T, token, snapshotID string) model.Snapshot {
	t.Helper()
	var snapshot model.Snapshot
	h.mustDoJSON(t, token, http.MethodGet, "/v1/snapshots/"+snapshotID, nil, &snapshot, http.StatusOK)
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

func (h *harness) createTunnelSignedURL(t *testing.T, token, tunnelID string, req model.CreateTunnelSignedURLRequest) model.TunnelSignedURL {
	t.Helper()
	var signed model.TunnelSignedURL
	h.mustDoJSON(t, token, http.MethodPost, "/v1/tunnels/"+tunnelID+"/signed-url", req, &signed, http.StatusOK)
	return signed
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

type tunnelWebSocketUpstream struct {
	listener net.Listener
	server   *http.Server
	addr     string
}

func newTunnelWebSocketUpstream(t *testing.T) *tunnelWebSocketUpstream {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				messageType, payload, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if err := conn.WriteMessage(messageType, []byte("echo:"+string(payload))); err != nil {
					return
				}
			}
		}
		_, _ = io.WriteString(w, "upstream-ok")
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	return &tunnelWebSocketUpstream{listener: listener, server: server, addr: listener.Addr().String()}
}

func (u *tunnelWebSocketUpstream) close() {
	if u.server != nil {
		_ = u.server.Close()
	}
	if u.listener != nil {
		_ = u.listener.Close()
	}
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

func (h *harness) mustDoRawJSON(t *testing.T, token, method, endpoint, payload string, out any, want int) {
	t.Helper()
	request, err := http.NewRequest(method, h.server.URL+endpoint, bytes.NewBufferString(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		body, _ := io.ReadAll(response.Body)
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

func (h *harness) jwtToken(t *testing.T, tenantID string, roles []string, service bool) string {
	t.Helper()
	return h.jwtTokenWithSecret(t, h.jwtSecret, tenantID, roles, service)
}

func (h *harness) jwtTokenWithSecret(t *testing.T, secret, tenantID string, roles []string, service bool) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":       h.jwtIssuer,
		"aud":       h.jwtAudience,
		"sub":       tenantID + "-subject",
		"tenant_id": tenantID,
		"service":   service,
		"exp":       time.Now().Add(time.Hour).Unix(),
	}
	if roles != nil {
		claims["roles"] = roles
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
