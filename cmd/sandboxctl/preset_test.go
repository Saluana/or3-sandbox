package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"or3-sandbox/internal/model"
	"or3-sandbox/internal/presets"
)

func respondRuntimeBackend(w http.ResponseWriter, r *http.Request, backend string) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/v1/runtime/info":
		_ = json.NewEncoder(w).Encode(model.RuntimeInfo{Backend: backend})
		return true
	case "/v1/runtime/health":
		_ = json.NewEncoder(w).Encode(model.RuntimeHealth{Backend: backend})
		return true
	default:
		return false
	}
}

func TestRunPresetListAndInspect(t *testing.T) {
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "alpha", "name: alpha\ndescription: Alpha preset\nsandbox:\n  image: alpine:3.20\n")
	writePresetFixture(t, examplesDir, "beta", "name: beta\ndescription: Beta preset\nsandbox:\n  image: alpine:3.20\n")

	listOutput := captureStdout(t, func() {
		if err := runPresetList([]string{"--examples-dir", examplesDir}); err != nil {
			t.Fatalf("runPresetList: %v", err)
		}
	})
	if !strings.Contains(listOutput, "alpha") || !strings.Contains(listOutput, "beta") {
		t.Fatalf("unexpected list output: %s", listOutput)
	}

	inspectOutput := captureStdout(t, func() {
		if err := runPresetInspect([]string{"--examples-dir", examplesDir, "alpha"}); err != nil {
			t.Fatalf("runPresetInspect: %v", err)
		}
	})
	if !strings.Contains(inspectOutput, `"name": "alpha"`) {
		t.Fatalf("unexpected inspect output: %s", inspectOutput)
	}
}

func TestRunPresetRunSequencesRequestsAndRedactsSecrets(t *testing.T) {
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "demo", `
name: demo
description: demo preset
runtime:
  allowed: [docker]
  profile: runtime
sandbox:
  image: alpine:3.20
  cpu: "1"
  memory_mb: 512
inputs:
  - name: SECRET_TOKEN
    required: true
    secret: true
files:
  - path: notes.txt
    content: "hello ${SECRET_TOKEN}"
bootstrap:
  - name: bootstrap
    command: ["sh", "-lc", "echo ready"]
    env:
      SECRET_TOKEN: "${SECRET_TOKEN}"
artifacts:
  - remote_path: notes.txt
    local_path: outputs/notes.txt
cleanup: always
`)

	var sawCreate bool
	var sawWrite bool
	var sawExec bool
	var sawDelete bool
	var createReq model.CreateSandboxRequest
	var writeReq model.FileWriteRequest
	var execReq model.ExecRequest
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case respondRuntimeBackend(w, r, "docker"):
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			sawCreate = true
			if err := json.NewDecoder(r.Body).Decode(&createReq); err != nil {
				t.Fatalf("decode create: %v", err)
			}
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-demo", RuntimeBackend: "docker", Status: model.SandboxStatusRunning})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/sandboxes/sbx-demo/files/notes.txt":
			sawWrite = true
			if err := json.NewDecoder(r.Body).Decode(&writeReq); err != nil {
				t.Fatalf("decode write: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/sbx-demo/exec":
			sawExec = true
			if err := json.NewDecoder(r.Body).Decode(&execReq); err != nil {
				t.Fatalf("decode exec: %v", err)
			}
			_ = json.NewEncoder(w).Encode(model.Execution{ID: "exec-1", Status: model.ExecutionStatusSucceeded, StdoutPreview: "ok\n"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/sbx-demo/files/notes.txt":
			_ = json.NewEncoder(w).Encode(model.FileReadResponse{Path: "notes.txt", Content: "artifact text", Size: 13, Encoding: "utf-8"})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/sbx-demo":
			sawDelete = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	output := captureStdout(t, func() {
		if err := runPresetRun(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "--env", "SECRET_TOKEN=super-secret", "--set", "image=busybox:1.36", "demo"}); err != nil {
			t.Fatalf("runPresetRun: %v", err)
		}
	})

	if !sawCreate || !sawWrite || !sawExec || !sawDelete {
		t.Fatalf("unexpected request sequence create=%v write=%v exec=%v delete=%v", sawCreate, sawWrite, sawExec, sawDelete)
	}
	if createReq.BaseImageRef != "busybox:1.36" {
		t.Fatalf("expected image override, got %+v", createReq)
	}
	if createReq.Profile != model.GuestProfileRuntime {
		t.Fatalf("expected runtime profile to be forwarded, got %+v", createReq)
	}
	if writeReq.Content != "hello super-secret" {
		t.Fatalf("unexpected write payload: %+v", writeReq)
	}
	if execReq.Env["SECRET_TOKEN"] != "super-secret" {
		t.Fatalf("expected injected env, got %+v", execReq.Env)
	}
	if strings.Contains(output, "super-secret") {
		t.Fatalf("secret leaked in output: %s", output)
	}
	artifactPath := filepath.Join(examplesDir, "demo", "outputs", "notes.txt")
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(data) != "artifact text" {
		t.Fatalf("unexpected artifact: %s", data)
	}
}

func TestRunPresetRunRequiresInputs(t *testing.T) {
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "required", "name: required\nsandbox:\n  image: alpine:3.20\ninputs:\n  - name: API_TOKEN\n    required: true\n")
	err := runPresetRun(clientConfig{baseURL: "http://example.invalid", token: "dev-token"}, []string{"--examples-dir", examplesDir, "required"})
	if err == nil || !strings.Contains(err.Error(), "API_TOKEN") {
		t.Fatalf("expected required input error, got %v", err)
	}
}

func TestRunPresetRunDoesNotExposeUndeclaredHostEnv(t *testing.T) {
	examplesDir := t.TempDir()
	t.Setenv("UNDECLARED_SECRET", "host-secret")
	writePresetFixture(t, examplesDir, "scoped", `
name: scoped
runtime:
  allowed: [docker]
sandbox:
  image: alpine:3.20
files:
  - path: note.txt
    content: "declared=${DECLARED_SECRET}; undeclared=${UNDECLARED_SECRET}"
cleanup: always
`)

	var writeReq model.FileWriteRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case respondRuntimeBackend(w, r, "docker"):
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-scoped", RuntimeBackend: "docker", Status: model.SandboxStatusRunning})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/sandboxes/sbx-scoped/files/note.txt":
			if err := json.NewDecoder(r.Body).Decode(&writeReq); err != nil {
				t.Fatalf("decode write: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/sbx-scoped":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	if err := runPresetRun(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "scoped"}); err != nil {
		t.Fatalf("runPresetRun: %v", err)
	}
	if writeReq.Content != "declared=; undeclared=" {
		t.Fatalf("expected undeclared host env to be hidden, got %+v", writeReq)
	}
}

func TestRunPresetRunAcceptsFlagsAfterPresetName(t *testing.T) {
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "ordered", `
name: ordered
runtime:
  allowed: [docker]
sandbox:
  image: alpine:3.20
cleanup: always
`)

	var deleted bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case respondRuntimeBackend(w, r, "docker"):
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-ordered", RuntimeBackend: "docker", Status: model.SandboxStatusRunning})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/sbx-ordered":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	if err := runPresetRun(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "ordered", "--cleanup", "never"}); err != nil {
		t.Fatalf("runPresetRun: %v", err)
	}
	if deleted {
		t.Fatal("expected cleanup override to be honored even when flag comes after preset name")
	}
}

func TestRunPresetRunPrintsTunnelTokenForPrivateTokenTunnels(t *testing.T) {
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "tunnel-token", `
name: tunnel-token
runtime:
  allowed: [docker]
sandbox:
  image: alpine:3.20
  allow_tunnels: true
startup:
  name: sleep
  command: ["sh", "-lc", "sleep 60"]
  detached: true
readiness:
  type: http
  path: /
  timeout: 1s
  interval: 100ms
tunnel:
  port: 8080
  protocol: http
  auth_mode: token
  visibility: private
cleanup: always
`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case respondRuntimeBackend(w, r, "docker"):
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-tunnel", RuntimeBackend: "docker", Status: model.SandboxStatusRunning})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/sbx-tunnel/exec":
			_ = json.NewEncoder(w).Encode(model.Execution{ID: "exec-1", Status: model.ExecutionStatusRunning})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/sbx-tunnel/tunnels":
			_ = json.NewEncoder(w).Encode(model.Tunnel{ID: "tun-1", Endpoint: "http://" + r.Host + "/proxy", AuthMode: "token", AccessToken: "tunnel-secret"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tunnels/tun-1/signed-url":
			_ = json.NewEncoder(w).Encode(model.TunnelSignedURL{URL: "http://" + r.Host + "/proxy?or3_exp=123&or3_sig=sig", ExpiresAt: time.Unix(123, 0).UTC()})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/sbx-tunnel":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/proxy/":
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	output := captureStdout(t, func() {
		if err := runPresetRun(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "tunnel-token"}); err != nil {
			t.Fatalf("runPresetRun: %v", err)
		}
	})
	if !strings.Contains(output, "tunnel_access_token=tunnel-secret") {
		t.Fatalf("expected tunnel access token in output, got %s", output)
	}
}

func TestRunPresetRunRunsBootstrapBeforeStartup(t *testing.T) {
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "openclaw-like", `
name: openclaw-like
runtime:
  allowed: [docker]
sandbox:
  image: ghcr.io/openclaw/openclaw:latest
inputs:
  - name: OPENCLAW_GATEWAY_TOKEN
    required: true
    secret: true
bootstrap:
  - name: seed-openclaw-config
    command: ["sh", "-lc", "openclaw config set gateway.auth.token \"$OPENCLAW_GATEWAY_TOKEN\" >/dev/null"]
    env:
      OPENCLAW_GATEWAY_TOKEN: "${OPENCLAW_GATEWAY_TOKEN}"
startup:
  name: start-openclaw
  command: ["node", "openclaw.mjs", "gateway"]
  env:
    OPENCLAW_GATEWAY_TOKEN: "${OPENCLAW_GATEWAY_TOKEN}"
  detached: true
cleanup: always
`)

	var execReqs []model.ExecRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case respondRuntimeBackend(w, r, "docker"):
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-openclaw", RuntimeBackend: "docker", Status: model.SandboxStatusRunning})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/sbx-openclaw/exec":
			var req model.ExecRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode exec: %v", err)
			}
			execReqs = append(execReqs, req)
			status := model.ExecutionStatusSucceeded
			if req.Detached {
				status = model.ExecutionStatusRunning
			}
			_ = json.NewEncoder(w).Encode(model.Execution{ID: "exec-1", Status: status})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/sbx-openclaw":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	if err := runPresetRun(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "--env", "OPENCLAW_GATEWAY_TOKEN=test-gateway-token", "openclaw-like"}); err != nil {
		t.Fatalf("runPresetRun: %v", err)
	}
	if len(execReqs) != 2 {
		t.Fatalf("expected bootstrap and startup execs, got %d", len(execReqs))
	}
	if execReqs[0].Detached {
		t.Fatal("expected bootstrap step to run before detached startup")
	}
	if execReqs[0].Env["OPENCLAW_GATEWAY_TOKEN"] != "test-gateway-token" {
		t.Fatalf("expected bootstrap env injection, got %+v", execReqs[0].Env)
	}
	if !execReqs[1].Detached {
		t.Fatal("expected startup step to be detached")
	}
	if execReqs[1].Env["OPENCLAW_GATEWAY_TOKEN"] != "test-gateway-token" {
		t.Fatalf("expected startup env injection, got %+v", execReqs[1].Env)
	}
}

func TestRunPresetRunLoadsInputsFromDotEnv(t *testing.T) {
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "openclaw-like", `
name: openclaw-like
runtime:
  allowed: [docker]
sandbox:
  image: ghcr.io/openclaw/openclaw:latest
inputs:
  - name: OPENCLAW_GATEWAY_TOKEN
    required: true
    secret: true
  - name: OPENROUTER_API_KEY
    secret: true
  - name: OPENCLAW_MODEL
bootstrap:
  - name: seed-openclaw-config
    command: ["sh", "-lc", "echo bootstrap >/dev/null"]
    env:
      OPENCLAW_GATEWAY_TOKEN: "${OPENCLAW_GATEWAY_TOKEN}"
      OPENROUTER_API_KEY: "${OPENROUTER_API_KEY}"
      OPENCLAW_MODEL: "${OPENCLAW_MODEL}"
startup:
  name: start-openclaw
  command: ["node", "openclaw.mjs", "gateway"]
  env:
    OPENCLAW_GATEWAY_TOKEN: "${OPENCLAW_GATEWAY_TOKEN}"
    OPENROUTER_API_KEY: "${OPENROUTER_API_KEY}"
    OPENCLAW_MODEL: "${OPENCLAW_MODEL}"
  detached: true
cleanup: always
`)

	workDir := t.TempDir()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(originalWD); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte(strings.Join([]string{
		"OPENCLAW_GATEWAY_TOKEN=dotenv-gateway-token",
		"OPENROUTER_API_KEY=dotenv-openrouter-key",
		"OPENCLAW_MODEL=minimax/minimax-m2.5",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	var execReqs []model.ExecRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case respondRuntimeBackend(w, r, "docker"):
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-openclaw", RuntimeBackend: "docker", Status: model.SandboxStatusRunning})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/sbx-openclaw/exec":
			var req model.ExecRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode exec: %v", err)
			}
			execReqs = append(execReqs, req)
			status := model.ExecutionStatusSucceeded
			if req.Detached {
				status = model.ExecutionStatusRunning
			}
			_ = json.NewEncoder(w).Encode(model.Execution{ID: "exec-1", Status: status})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/sbx-openclaw":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	if err := runPresetRun(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "openclaw-like"}); err != nil {
		t.Fatalf("runPresetRun: %v", err)
	}
	if len(execReqs) != 2 {
		t.Fatalf("expected bootstrap and startup execs, got %d", len(execReqs))
	}
	for _, req := range execReqs {
		if req.Env["OPENCLAW_GATEWAY_TOKEN"] != "dotenv-gateway-token" {
			t.Fatalf("expected .env gateway token injection, got %+v", req.Env)
		}
		if req.Env["OPENROUTER_API_KEY"] != "dotenv-openrouter-key" {
			t.Fatalf("expected .env OpenRouter key injection, got %+v", req.Env)
		}
		if req.Env["OPENCLAW_MODEL"] != "minimax/minimax-m2.5" {
			t.Fatalf("expected .env model injection, got %+v", req.Env)
		}
	}
}

func TestRunPresetRunPrintsOpenClawDashboardURL(t *testing.T) {
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "openclaw", `
name: openclaw
runtime:
  allowed: [docker]
sandbox:
  image: ghcr.io/openclaw/openclaw:latest
  allow_tunnels: true
inputs:
  - name: OPENCLAW_GATEWAY_TOKEN
    required: true
    secret: true
startup:
  name: start-openclaw
  command: ["node", "openclaw.mjs", "gateway"]
  detached: true
readiness:
  type: http
  path: /healthz
  timeout: 1s
  interval: 100ms
tunnel:
  port: 18789
  protocol: http
  auth_mode: token
  visibility: private
cleanup: always
`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case respondRuntimeBackend(w, r, "docker"):
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-openclaw", RuntimeBackend: "docker", Status: model.SandboxStatusRunning})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/sbx-openclaw/exec":
			_ = json.NewEncoder(w).Encode(model.Execution{ID: "exec-1", Status: model.ExecutionStatusRunning})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/sbx-openclaw/tunnels":
			_ = json.NewEncoder(w).Encode(model.Tunnel{ID: "tun-openclaw", Endpoint: "http://" + r.Host + "/v1/tunnels/tun-openclaw/proxy", AuthMode: "token", AccessToken: "tunnel-secret"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tunnels/tun-openclaw/signed-url":
			_ = json.NewEncoder(w).Encode(model.TunnelSignedURL{URL: "http://127.0.0.1:8080/v1/tunnels/tun-openclaw/proxy?or3_exp=123&or3_sig=sig", ExpiresAt: time.Unix(123, 0).UTC()})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tunnels/tun-openclaw/proxy/healthz":
			if got := r.Header.Get("X-Tunnel-Token"); got != "tunnel-secret" {
				t.Fatalf("expected readiness tunnel token header, got %q", got)
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/sbx-openclaw":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	output := captureStdout(t, func() {
		if err := runPresetRun(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "--env", "OPENCLAW_GATEWAY_TOKEN=test-gateway-token", "openclaw"}); err != nil {
			t.Fatalf("runPresetRun: %v", err)
		}
	})
	if !strings.Contains(output, "tunnel_browser_url=http://127.0.0.1:8080/v1/tunnels/tun-openclaw/proxy?or3_exp=123&or3_sig=sig") {
		t.Fatalf("expected signed tunnel browser url in output, got %s", output)
	}
	if !strings.Contains(output, "dashboard_url=http://127.0.0.1:8080/v1/tunnels/tun-openclaw/proxy?or3_exp=123&or3_sig=sig#token=test-gateway-token") {
		t.Fatalf("expected OpenClaw dashboard url in output, got %s", output)
	}
}

func TestResolvePresetRuntimeAdapterRejectsDockerStyleImageOnQEMU(t *testing.T) {
	manifest := writeLoadedPreset(t, `
name: qemu-bad
runtime:
  allowed: [qemu]
sandbox:
  image: alpine:3.20
`)
	_, err := resolvePresetRuntimeAdapter(clientConfig{}, manifest, model.CreateSandboxRequest{BaseImageRef: "alpine:3.20", Start: true})
	if err == nil || !strings.Contains(err.Error(), "qemu guest packaging") {
		t.Fatalf("expected qemu packaging error, got %v", err)
	}
}

func TestRunPresetRunWaitsForQEMUGuestReadyBeforeBootstrapping(t *testing.T) {
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "qemu-tooling", "\nname: qemu-tooling\nruntime:\n  allowed: [qemu]\n  profile: core\nsandbox:\n  image: ${QEMU_GUEST_IMAGE}\nbootstrap:\n  - name: bootstrap\n    command: [\"sh\", \"-lc\", \"cp /workspace/input.txt /workspace/output.txt\"]\nreadiness:\n  type: command\n  command: [\"sh\", \"-lc\", \"test -f /workspace/output.txt\"]\n  timeout: 2s\n  interval: 200ms\nfiles:\n  - path: input.txt\n    content: \"qemu-ok\"\nartifacts:\n  - remote_path: output.txt\n    local_path: outputs/output.txt\ncleanup: always\n")

	inspectCalls := 0
	guestReady := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case respondRuntimeBackend(w, r, "qemu"):
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			var req model.CreateSandboxRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create sandbox request: %v", err)
			}
			if req.BaseImageRef != "/images/base.qcow2" {
				t.Fatalf("expected QEMU guest image override to populate sandbox.image, got %q", req.BaseImageRef)
			}
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-qemu", RuntimeBackend: "qemu", Status: model.SandboxStatusBooting})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/sbx-qemu":
			inspectCalls++
			status := model.SandboxStatusBooting
			if inspectCalls >= 2 {
				status = model.SandboxStatusRunning
				guestReady = true
			}
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-qemu", RuntimeBackend: "qemu", Status: status, RuntimeStatus: string(status)})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/sandboxes/sbx-qemu/files/input.txt":
			if !guestReady {
				t.Fatalf("file upload happened before qemu guest-ready")
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/sbx-qemu/exec":
			if !guestReady {
				t.Fatalf("exec happened before qemu guest-ready")
			}
			_ = json.NewEncoder(w).Encode(model.Execution{ID: "exec-1", Status: model.ExecutionStatusSucceeded})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/sbx-qemu/files/output.txt":
			_ = json.NewEncoder(w).Encode(model.FileReadResponse{Path: "output.txt", Content: "qemu-ok", Size: 7, Encoding: "utf-8"})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/sbx-qemu":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	if err := runPresetRun(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "--env", "QEMU_GUEST_IMAGE=/images/base.qcow2", "qemu-tooling"}); err != nil {
		t.Fatalf("runPresetRun: %v", err)
	}
	if inspectCalls < 2 {
		t.Fatalf("expected guest-ready polling, got %d polls", inspectCalls)
	}
	data, err := os.ReadFile(filepath.Join(examplesDir, "qemu-tooling", "outputs", "output.txt"))
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(data) != "qemu-ok" {
		t.Fatalf("unexpected artifact content: %q", data)
	}
}

func TestRunPresetRunFailsWhenQEMUGuestBootFails(t *testing.T) {
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "qemu-fail", "\nname: qemu-fail\nruntime:\n  allowed: [qemu]\n  profile: core\nsandbox:\n  image: ${QEMU_GUEST_IMAGE}\ncleanup: always\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case respondRuntimeBackend(w, r, "qemu"):
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-qemu", RuntimeBackend: "qemu", Status: model.SandboxStatusBooting})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/sbx-qemu":
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-qemu", RuntimeBackend: "qemu", Status: model.SandboxStatusError, RuntimeStatus: string(model.SandboxStatusError), LastRuntimeError: "kernel panic"})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/sbx-qemu":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	err := runPresetRun(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "--env", "QEMU_GUEST_IMAGE=/images/base.qcow2", "qemu-fail"})
	if err == nil || !strings.Contains(err.Error(), "guest did not become ready") {
		t.Fatalf("expected guest-ready failure, got %v", err)
	}
}

func TestRunPresetRunSeparatesQEMUGuestAndAppReadiness(t *testing.T) {
	examplesDir := t.TempDir()
	writePresetFixture(t, examplesDir, "qemu-app-readiness", "\nname: qemu-app-readiness\nruntime:\n  allowed: [qemu]\n  profile: core\nsandbox:\n  image: ${QEMU_GUEST_IMAGE}\nreadiness:\n  type: command\n  command: [\"sh\", \"-lc\", \"test -f /workspace/ready.txt\"]\n  timeout: 600ms\n  interval: 200ms\ncleanup: always\n")

	inspectCalls := 0
	readinessCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case respondRuntimeBackend(w, r, "qemu"):
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-qemu", RuntimeBackend: "qemu", Status: model.SandboxStatusBooting})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/sbx-qemu":
			inspectCalls++
			_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-qemu", RuntimeBackend: "qemu", Status: model.SandboxStatusRunning, RuntimeStatus: string(model.SandboxStatusRunning)})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/sbx-qemu/exec":
			readinessCalls++
			_ = json.NewEncoder(w).Encode(model.Execution{ID: "exec-1", Status: model.ExecutionStatusFailed})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/sbx-qemu":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	err := runPresetRun(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"--examples-dir", examplesDir, "--env", "QEMU_GUEST_IMAGE=/images/base.qcow2", "qemu-app-readiness"})
	if err == nil || !strings.Contains(err.Error(), "timed out waiting for preset readiness") {
		t.Fatalf("expected app readiness timeout, got %v", err)
	}
	if inspectCalls == 0 || readinessCalls == 0 {
		t.Fatalf("expected both guest-ready and app-ready polling, got inspect=%d readiness=%d", inspectCalls, readinessCalls)
	}
}

func TestRunDownloadDecodesBinaryPayload(t *testing.T) {
	pngData := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "encoding=base64" {
			t.Fatalf("expected base64 query, got %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(model.FileReadResponse{Path: "pixel.png", ContentBase64: base64.StdEncoding.EncodeToString(pngData), Size: int64(len(pngData)), Encoding: "base64"})
	}))
	defer server.Close()
	outputPath := filepath.Join(t.TempDir(), "pixel.png")
	if err := runDownload(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"sbx-1", "pixel.png", outputPath}); err != nil {
		t.Fatalf("runDownload: %v", err)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != string(pngData) {
		t.Fatalf("unexpected binary payload: %v", data)
	}
}

func writePresetFixture(t *testing.T, examplesDir, name, content string) {
	t.Helper()
	dir := filepath.Join(examplesDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "preset.yaml"), []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeLoadedPreset(t *testing.T, content string) presets.Manifest {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "preset.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := presets.LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}
