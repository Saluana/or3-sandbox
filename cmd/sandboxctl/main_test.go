package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
