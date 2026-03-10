package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"or3-sandbox/internal/runtime/qemu/agentproto"
)

func TestRunExecDetachedTimeoutKeepsProcessAlive(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "detached.txt")
	req := agentproto.ExecRequest{
		Command:  []string{"sh", "-lc", fmt.Sprintf("sleep 0.2; printf ok > %q", marker)},
		Cwd:      "/tmp",
		Timeout:  2 * time.Second,
		Detached: true,
	}
	result, err := runExec(context.Background(), req)
	if err != nil {
		t.Fatalf("runExec: %v", err)
	}
	if result.Status != "detached" {
		t.Fatalf("unexpected detached status: %+v", result)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(marker)
		if readErr == nil {
			if string(data) != "ok" {
				t.Fatalf("unexpected detached output %q", string(data))
			}
			return
		}
		if !os.IsNotExist(readErr) {
			t.Fatalf("read marker: %v", readErr)
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("detached command never wrote %s", marker)
}

func TestGuestAgentHelloUsesManifestCapabilities(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(manifestPath, []byte(`{"capabilities":["files","exec","tcp_bridge"],"workspace_contract_version":"9","control":{"mode":"agent"}}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	agent := &guestAgent{profileManifestPath: manifestPath}
	if err := agent.loadCapabilities(); err != nil {
		t.Fatalf("load capabilities: %v", err)
	}
	response, err := agent.handle(context.Background(), agentproto.Message{ID: "msg-1", Op: agentproto.OpHello})
	if err != nil {
		t.Fatalf("hello handle: %v", err)
	}
	var result agentproto.HelloResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("unmarshal hello result: %v", err)
	}
	if got := strings.Join(result.Capabilities, ","); got != "exec,files,tcp_bridge" {
		t.Fatalf("unexpected hello capabilities %q", got)
	}
	if result.WorkspaceContractVersion != "9" {
		t.Fatalf("unexpected workspace contract version %q", result.WorkspaceContractVersion)
	}
}

func TestGuestAgentRejectsDisallowedOperation(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(manifestPath, []byte(`{"capabilities":["files"],"workspace_contract_version":"1","control":{"mode":"agent"}}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	agent := &guestAgent{profileManifestPath: manifestPath}
	if err := agent.loadCapabilities(); err != nil {
		t.Fatalf("load capabilities: %v", err)
	}
	_, err := agent.handle(context.Background(), agentproto.Message{ID: "msg-1", Op: agentproto.OpExec})
	if err == nil || !strings.Contains(err.Error(), "does not allow operation") {
		t.Fatalf("expected disallowed operation error, got %v", err)
	}
}
