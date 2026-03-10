package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/guestimage"
	"or3-sandbox/internal/model"
)

func TestRunStopForceSendsLifecycleRequest(t *testing.T) {
	var method string
	var path string
	var req model.LifecycleRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-1", Status: model.SandboxStatusStopped})
	}))
	defer server.Close()

	output := captureStdout(t, func() {
		if err := runStop(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"--force", "sbx-1"}); err != nil {
			t.Fatalf("runStop failed: %v", err)
		}
	})

	if method != http.MethodPost || path != "/v1/sandboxes/sbx-1/stop" {
		t.Fatalf("unexpected request: %s %s", method, path)
	}
	if !req.Force {
		t.Fatal("expected force=true")
	}
	if !strings.Contains(output, "\"id\": \"sbx-1\"") {
		t.Fatalf("unexpected output: %s", output)
	}
}

func TestRunCreateSendsRuntimeSelection(t *testing.T) {
	var req model.CreateSandboxRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(model.Sandbox{ID: "sbx-1", RuntimeSelection: req.RuntimeSelection})
	}))
	defer server.Close()

	if err := runCreate(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"--image", "alpine:3.20", "--runtime", "docker-dev", "--start=false"}); err != nil {
		t.Fatalf("runCreate failed: %v", err)
	}
	if req.RuntimeSelection != model.RuntimeSelectionDockerDev {
		t.Fatalf("expected runtime selection docker-dev, got %q", req.RuntimeSelection)
	}
}

func TestRunSnapshotCommandsUseExpectedEndpoints(t *testing.T) {
	tests := []struct {
		name       string
		run        func(clientConfig) error
		wantMethod string
		wantPath   string
		wantBody   string
		response   any
	}{
		{
			name: "create",
			run: func(client clientConfig) error {
				return runSnapshotCreate(client, []string{"--name", "snap-a", "sbx-1"})
			},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/sandboxes/sbx-1/snapshots",
			wantBody:   `{"name":"snap-a"}`,
			response:   model.Snapshot{ID: "snap-1", Name: "snap-a"},
		},
		{
			name: "list",
			run: func(client clientConfig) error {
				return runSnapshotList(client, []string{"sbx-1"})
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1/sandboxes/sbx-1/snapshots",
			response:   []model.Snapshot{{ID: "snap-1"}},
		},
		{
			name: "inspect",
			run: func(client clientConfig) error {
				return runSnapshotInspect(client, []string{"snap-1"})
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1/snapshots/snap-1",
			response:   model.Snapshot{ID: "snap-1"},
		},
		{
			name: "restore",
			run: func(client clientConfig) error {
				return runSnapshotRestore(client, []string{"snap-1", "sbx-1"})
			},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/snapshots/snap-1/restore",
			wantBody:   `{"target_sandbox_id":"sbx-1"}`,
			response:   model.Sandbox{ID: "sbx-1", Status: model.SandboxStatusStopped},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod string
			var gotPath string
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				gotBody = data
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			output := captureStdout(t, func() {
				if err := tt.run(clientConfig{baseURL: server.URL, token: "dev-token"}); err != nil {
					t.Fatalf("command failed: %v", err)
				}
			})

			if gotMethod != tt.wantMethod || gotPath != tt.wantPath {
				t.Fatalf("unexpected request: %s %s", gotMethod, gotPath)
			}
			if tt.wantBody != "" {
				if compactJSON(string(gotBody)) != compactJSON(tt.wantBody) {
					t.Fatalf("unexpected body: %s", string(gotBody))
				}
			}
			if strings.TrimSpace(output) == "" {
				t.Fatal("expected JSON output")
			}
		})
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = original }()

	fn()
	_ = w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy stdout: %v", err)
	}
	_ = r.Close()
	return buf.String()
}

func compactJSON(value string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(value)); err != nil {
		return value
	}
	return buf.String()
}

func TestRunDoctorRequiresProductionQEMUFlag(t *testing.T) {
	err := runDoctor(nil)
	if err == nil || !strings.Contains(err.Error(), "usage: sandboxctl doctor --production-qemu") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestProductionQEMUDoctorReportsConfigFailures(t *testing.T) {
	t.Setenv("SANDBOX_RUNTIME", "docker")
	t.Setenv("SANDBOX_TRUSTED_DOCKER_RUNTIME", "true")
	t.Setenv("SANDBOX_AUTH_MODE", "static")
	t.Setenv("SANDBOX_TOKENS", "dev-token=tenant-dev")
	t.Setenv("SANDBOX_DB_PATH", filepath.Join(t.TempDir(), "sandbox.db"))
	t.Setenv("SANDBOX_STORAGE_ROOT", t.TempDir())
	t.Setenv("SANDBOX_SNAPSHOT_ROOT", t.TempDir())
	summary := runProductionQEMUDoctor()
	if len(summary.Checks) == 0 {
		t.Fatal("expected doctor checks")
	}
	foundRuntimeFailure := false
	for _, check := range summary.Checks {
		if check.Name == "runtime" && check.Level == "fail" {
			foundRuntimeFailure = true
			break
		}
	}
	if !foundRuntimeFailure {
		t.Fatalf("expected runtime failure in doctor summary, got %#v", summary.Checks)
	}
}

func TestProductionQEMUDoctorFailsOnUnsupportedHostOS(t *testing.T) {
	originalHostOS := doctorHostOS
	doctorHostOS = "darwin"
	defer func() { doctorHostOS = originalHostOS }()

	t.Setenv("SANDBOX_RUNTIME", "qemu")
	t.Setenv("SANDBOX_AUTH_MODE", "jwt-hs256")
	t.Setenv("SANDBOX_QEMU_BINARY", "/usr/bin/false")
	t.Setenv("SANDBOX_DB_PATH", filepath.Join(t.TempDir(), "sandbox.db"))
	t.Setenv("SANDBOX_STORAGE_ROOT", t.TempDir())
	t.Setenv("SANDBOX_SNAPSHOT_ROOT", t.TempDir())

	summary := runProductionQEMUDoctor()
	for _, check := range summary.Checks {
		if check.Name == "host-os" {
			if check.Level != "fail" {
				t.Fatalf("expected host-os failure, got %#v", check)
			}
			return
		}
	}
	t.Fatalf("expected host-os check, got %#v", summary.Checks)
}

func TestProductionQEMUDoctorAccumulatesChecksAfterConfigLoadFailure(t *testing.T) {
	originalLoader := doctorConfigLoader
	originalHostOS := doctorHostOS
	doctorConfigLoader = func([]string) (config.Config, error) {
		return config.Config{}, errors.New("boom")
	}
	doctorHostOS = "darwin"
	defer func() {
		doctorConfigLoader = originalLoader
		doctorHostOS = originalHostOS
	}()

	t.Setenv("SANDBOX_RUNTIME", "docker")
	t.Setenv("SANDBOX_AUTH_MODE", "static")
	t.Setenv("SANDBOX_DB_PATH", filepath.Join(t.TempDir(), "sandbox.db"))
	t.Setenv("SANDBOX_STORAGE_ROOT", t.TempDir())
	t.Setenv("SANDBOX_SNAPSHOT_ROOT", t.TempDir())

	summary := runProductionQEMUDoctor()
	if len(summary.Checks) < 4 {
		t.Fatalf("expected multiple checks after config failure, got %#v", summary.Checks)
	}
	seen := map[string]bool{}
	for _, check := range summary.Checks {
		seen[check.Name] = true
	}
	for _, name := range []string{"config", "runtime", "auth", "host-os"} {
		if !seen[name] {
			t.Fatalf("expected %s check after config failure, got %#v", name, summary.Checks)
		}
	}
}

func TestProductionQEMUDoctorReportsFreeSpaceAndPostureWarnings(t *testing.T) {
	restore := captureDoctorGlobals()
	defer restore()

	root := t.TempDir()
	dbDir := filepath.Join(root, "db")
	storageRoot := filepath.Join(root, "storage")
	snapshotRoot := filepath.Join(root, "snapshots")
	keyDir := filepath.Join(root, "secrets")
	for _, dir := range []string{dbDir, storageRoot, snapshotRoot, keyDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.Chmod(storageRoot, 0o777); err != nil {
		t.Fatalf("chmod storage root: %v", err)
	}
	dbPath := filepath.Join(dbDir, "sandbox.db")
	keyPath := filepath.Join(keyDir, "tunnel.key")
	secretPath := filepath.Join(root, "jwt.secret")
	imagePath := filepath.Join(root, "guest.qcow2")
	if err := os.WriteFile(secretPath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write jwt secret: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("signing"), 0o644); err != nil {
		t.Fatalf("write tunnel key: %v", err)
	}
	if err := writeDoctorGuestContract(imagePath); err != nil {
		t.Fatalf("write guest contract: %v", err)
	}

	doctorHostOS = "linux"
	doctorLookPath = func(string) (string, error) { return "/usr/bin/true", nil }
	doctorStat = func(path string) (os.FileInfo, error) {
		if path == "/dev/kvm" {
			return os.Stat(secretPath)
		}
		if path == "/sys/fs/cgroup" {
			return os.Stat(storageRoot)
		}
		return os.Stat(path)
	}
	doctorReadFile = func(path string) ([]byte, error) {
		if path == "/sys/fs/cgroup/cgroup.controllers" {
			return []byte("cpu pids"), nil
		}
		return os.ReadFile(path)
	}
	doctorStatFS = func(path string) (doctorFSInfo, error) {
		switch path {
		case dbDir:
			return doctorFSInfo{AvailableBytes: 2 << 30}, nil
		case storageRoot:
			return doctorFSInfo{AvailableBytes: 512 << 20}, nil
		case snapshotRoot:
			return doctorFSInfo{AvailableBytes: 8 << 30}, nil
		default:
			return doctorFSInfo{AvailableBytes: 8 << 30}, nil
		}
	}
	doctorConfigLoader = func([]string) (config.Config, error) {
		return config.Config{
			RuntimeBackend:            "qemu",
			AuthMode:                  "jwt-hs256",
			AuthJWTSecretPaths:        []string{secretPath},
			DatabasePath:              dbPath,
			StorageRoot:               storageRoot,
			SnapshotRoot:              snapshotRoot,
			QEMUBinary:                "qemu-system-x86_64",
			TunnelSigningKeyPath:      keyPath,
			QEMUAllowedBaseImagePaths: []string{imagePath},
		}, nil
	}

	summary := runProductionQEMUDoctor()
	assertDoctorCheck(t, summary.Checks, "free-space", "fail", "storage filesystem")
	assertDoctorCheck(t, summary.Checks, "free-space", "warn", "database filesystem")
	assertDoctorCheck(t, summary.Checks, "tunnel-signing-key", "warn", "broader than 0600")
	assertDoctorCheck(t, summary.Checks, "path-posture", "warn", "storage-root")
	assertDoctorCheck(t, summary.Checks, "cgroup", "warn", "missing controllers: memory")
}

func TestProductionQEMUDoctorFailsUnreadableTunnelSigningKeyPath(t *testing.T) {
	restore := captureDoctorGlobals()
	defer restore()

	root := t.TempDir()
	dbDir := filepath.Join(root, "db")
	storageRoot := filepath.Join(root, "storage")
	snapshotRoot := filepath.Join(root, "snapshots")
	secretPath := filepath.Join(root, "jwt.secret")
	imagePath := filepath.Join(root, "guest.qcow2")
	for _, dir := range []string{dbDir, storageRoot, snapshotRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(secretPath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write jwt secret: %v", err)
	}
	if err := writeDoctorGuestContract(imagePath); err != nil {
		t.Fatalf("write guest contract: %v", err)
	}

	doctorHostOS = "linux"
	doctorLookPath = func(string) (string, error) { return "/usr/bin/true", nil }
	doctorStat = func(path string) (os.FileInfo, error) {
		if path == "/dev/kvm" {
			return os.Stat(secretPath)
		}
		if path == "/sys/fs/cgroup" {
			return os.Stat(storageRoot)
		}
		if strings.Contains(path, "missing-tunnel.key") {
			return nil, os.ErrNotExist
		}
		return os.Stat(path)
	}
	doctorReadFile = func(path string) ([]byte, error) {
		if path == "/sys/fs/cgroup/cgroup.controllers" {
			return []byte("cpu memory pids"), nil
		}
		return os.ReadFile(path)
	}
	doctorStatFS = func(string) (doctorFSInfo, error) {
		return doctorFSInfo{AvailableBytes: 8 << 30}, nil
	}
	doctorConfigLoader = func([]string) (config.Config, error) {
		return config.Config{
			RuntimeBackend:            "qemu",
			AuthMode:                  "jwt-hs256",
			AuthJWTSecretPaths:        []string{secretPath},
			DatabasePath:              filepath.Join(dbDir, "sandbox.db"),
			StorageRoot:               storageRoot,
			SnapshotRoot:              snapshotRoot,
			QEMUBinary:                "qemu-system-x86_64",
			TunnelSigningKeyPath:      filepath.Join(root, "missing-tunnel.key"),
			QEMUAllowedBaseImagePaths: []string{imagePath},
		}, nil
	}

	summary := runProductionQEMUDoctor()
	assertDoctorCheck(t, summary.Checks, "tunnel-signing-key", "fail", "not readable")
}

func TestProductionQEMUDoctorReportsEnabledRuntimeSelections(t *testing.T) {
	restore := captureDoctorGlobals()
	defer restore()

	root := t.TempDir()
	dbDir := filepath.Join(root, "db")
	storageRoot := filepath.Join(root, "storage")
	snapshotRoot := filepath.Join(root, "snapshots")
	keyPath := filepath.Join(root, "containerd.sock")
	imagePath := filepath.Join(root, "guest.qcow2")
	for _, dir := range []string{dbDir, storageRoot, snapshotRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(keyPath, []byte("sock"), 0o644); err != nil {
		t.Fatalf("write fake socket: %v", err)
	}
	if err := writeDoctorGuestContract(imagePath); err != nil {
		t.Fatalf("write guest contract: %v", err)
	}

	doctorHostOS = "linux"
	doctorLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	doctorStat = os.Stat
	doctorReadFile = func(path string) ([]byte, error) {
		if path == "/sys/fs/cgroup/cgroup.controllers" {
			return []byte("cpu memory pids"), nil
		}
		return os.ReadFile(path)
	}
	doctorStatFS = func(string) (doctorFSInfo, error) { return doctorFSInfo{AvailableBytes: 8 << 30}, nil }
	doctorConfigLoader = func([]string) (config.Config, error) {
		return config.Config{
			RuntimeBackend:            "qemu",
			DefaultRuntimeSelection:   model.RuntimeSelectionContainerdKataProfessional,
			EnabledRuntimeSelections:  []model.RuntimeSelection{model.RuntimeSelectionDockerDev, model.RuntimeSelectionContainerdKataProfessional, model.RuntimeSelectionQEMUProfessional},
			TrustedDockerRuntime:      true,
			AuthMode:                  "jwt-hs256",
			DatabasePath:              filepath.Join(dbDir, "sandbox.db"),
			StorageRoot:               storageRoot,
			SnapshotRoot:              snapshotRoot,
			QEMUBinary:                "qemu-system-x86_64",
			QEMUAllowedBaseImagePaths: []string{imagePath},
			KataBinary:                "ctr",
			KataRuntimeClass:          "io.containerd.kata.v2",
			KataContainerdSocket:      keyPath,
		}, nil
	}

	summary := runProductionQEMUDoctor()
	assertDoctorCheck(t, summary.Checks, "runtime-selection", "pass", "default runtime selection is \"containerd-kata-professional\"")
	assertDoctorCheck(t, summary.Checks, "runtime-selection", "pass", "docker-dev")
	assertDoctorCheck(t, summary.Checks, "kata", "pass", "io.containerd.kata.v2")
	assertDoctorCheck(t, summary.Checks, "docker", "pass", "docker-dev prerequisites are present")
}

func TestProductionQEMUDoctorFailsKataOnNonLinuxHost(t *testing.T) {
	restore := captureDoctorGlobals()
	defer restore()
	doctorHostOS = "darwin"
	doctorConfigLoader = func([]string) (config.Config, error) {
		return config.Config{
			RuntimeBackend:           "qemu",
			DefaultRuntimeSelection:  model.RuntimeSelectionQEMUProfessional,
			EnabledRuntimeSelections: []model.RuntimeSelection{model.RuntimeSelectionContainerdKataProfessional, model.RuntimeSelectionQEMUProfessional},
			AuthMode:                 "jwt-hs256",
			DatabasePath:             filepath.Join(t.TempDir(), "sandbox.db"),
			StorageRoot:              t.TempDir(),
			SnapshotRoot:             t.TempDir(),
			QEMUBinary:               "qemu-system-x86_64",
			KataBinary:               "ctr",
			KataRuntimeClass:         "io.containerd.kata.v2",
			KataContainerdSocket:     "/run/containerd/containerd.sock",
		}, nil
	}
	summary := runProductionQEMUDoctor()
	assertDoctorCheck(t, summary.Checks, "kata", "fail", "host OS darwin is not supported")
}

func captureDoctorGlobals() func() {
	loader := doctorConfigLoader
	hostOS := doctorHostOS
	lookPath := doctorLookPath
	stat := doctorStat
	readFile := doctorReadFile
	statFS := doctorStatFS
	return func() {
		doctorConfigLoader = loader
		doctorHostOS = hostOS
		doctorLookPath = lookPath
		doctorStat = stat
		doctorReadFile = readFile
		doctorStatFS = statFS
	}
}

func assertDoctorCheck(t *testing.T, checks []doctorCheck, name, level, detail string) {
	t.Helper()
	for _, check := range checks {
		if check.Name == name && check.Level == level && strings.Contains(check.Detail, detail) {
			return
		}
	}
	t.Fatalf("missing doctor check name=%s level=%s detail=%q in %#v", name, level, detail, checks)
}

func writeDoctorGuestContract(imagePath string) error {
	if err := os.WriteFile(imagePath, []byte("guest"), 0o644); err != nil {
		return err
	}
	sha, err := guestimage.ComputeSHA256(imagePath)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(guestimage.Contract{
		ContractVersion:          model.DefaultImageContractVersion,
		ImagePath:                imagePath,
		ImageSHA256:              sha,
		BuildVersion:             "test",
		Profile:                  model.GuestProfileCore,
		Capabilities:             []string{"exec", "files", "pty"},
		Control:                  guestimage.ControlContract{Mode: model.GuestControlModeAgent, ProtocolVersion: model.DefaultGuestControlProtocolVersion},
		WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(guestimage.SidecarPath(imagePath), payload, 0o644)
}
