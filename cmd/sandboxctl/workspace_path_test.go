package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"or3-sandbox/internal/model"
)

func TestNormalizeWorkspaceAPIPath(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "relative file", input: "from-host.txt", want: "from-host.txt"},
		{name: "workspace absolute file", input: "/workspace/from-host.txt", want: "from-host.txt"},
		{name: "workspace absolute dir", input: "/workspace/demo/subdir", want: "demo/subdir"},
		{name: "workspace root", input: "/workspace", want: ""},
		{name: "dot relative", input: "./demo/file.txt", want: "demo/file.txt"},
		{name: "escape rejected", input: "../secret", wantErr: true},
		{name: "foreign absolute rejected", input: "/tmp/secret", wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := normalizeWorkspaceAPIPath(testCase.input)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("expected error, got path %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize path: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("normalize path = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestRunUploadNormalizesAbsoluteWorkspacePath(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(inputPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("unexpected method %s", r.Method)
		}
		if r.URL.Path != "/v1/sandboxes/sbx-1/files/from-host.txt" {
			t.Fatalf("unexpected upload path %s", r.URL.Path)
		}
		var req model.FileWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode upload request: %v", err)
		}
		decoded, err := base64.StdEncoding.DecodeString(req.ContentBase64)
		if err != nil {
			t.Fatalf("decode content: %v", err)
		}
		if string(decoded) != "hello" {
			t.Fatalf("unexpected content %q", string(decoded))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := runUpload(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"sbx-1", inputPath, "/workspace/from-host.txt"}); err != nil {
		t.Fatalf("runUpload: %v", err)
	}
}

func TestRunMkdirNormalizesAbsoluteWorkspacePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %s", r.Method)
		}
		if r.URL.Path != "/v1/sandboxes/sbx-1/mkdir" {
			t.Fatalf("unexpected mkdir path %s", r.URL.Path)
		}
		var req model.MkdirRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode mkdir request: %v", err)
		}
		if req.Path != "demo/subdir" {
			t.Fatalf("mkdir path = %q, want demo/subdir", req.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := runMkdir(clientConfig{baseURL: server.URL, token: "dev-token"}, []string{"sbx-1", "/workspace/demo/subdir"}); err != nil {
		t.Fatalf("runMkdir: %v", err)
	}
}
