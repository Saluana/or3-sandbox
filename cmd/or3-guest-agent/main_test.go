package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
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
	result, err := runExec(context.Background(), req, currentWorkloadIdentity(t))
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
	if result.MaxFileTransferBytes != agentproto.MaxFileTransferSize {
		t.Fatalf("unexpected hello max file transfer bytes %d", result.MaxFileTransferBytes)
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

func TestRunExecOverridesIdentityEnvironment(t *testing.T) {
	t.Parallel()

	identity := currentWorkloadIdentity(t)
	identity.HomeDir = filepath.Join(t.TempDir(), "workload-home")
	req := agentproto.ExecRequest{
		Command: []string{"sh", "-lc", "printf '%s|%s|%s' \"$HOME\" \"$USER\" \"$LOGNAME\""},
		Cwd:     "/tmp",
		Timeout: time.Second,
	}
	result, err := runExec(context.Background(), req, identity)
	if err != nil {
		t.Fatalf("runExec: %v", err)
	}
	if result.Status != "succeeded" {
		t.Fatalf("unexpected exec status: %+v", result)
	}
	if got := result.StdoutPreview; got != identity.HomeDir+"|"+identity.Username+"|"+identity.Username {
		t.Fatalf("unexpected workload env %q", got)
	}
}

func TestConfigureWorkloadCommandSetsCredentialForDifferentUser(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sh", "-lc", "true")
	identity := workloadIdentity{
		Username: "sandbox",
		UID:      uint32(os.Geteuid()) + 1,
		GID:      uint32(os.Getegid()) + 1,
		HomeDir:  "/home/sandbox",
	}
	configureWorkloadCommand(cmd, identity)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
		t.Fatal("expected workload credential to be configured")
	}
	if cmd.SysProcAttr.Credential.Uid != identity.UID || cmd.SysProcAttr.Credential.Gid != identity.GID {
		t.Fatalf("unexpected workload credential %+v", cmd.SysProcAttr.Credential)
	}
}

func TestRunFileOpHelperReadWriteMkdirDelete(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "nested", "value.txt")
	if err := runFileOpHelper(strings.NewReader(fmt.Sprintf(`{"op":"%s","target":%q}`, agentproto.OpMkdir, filepath.Dir(target))), io.Discard); err != nil {
		t.Fatalf("mkdir helper: %v", err)
	}
	writePayload := fmt.Sprintf(`{"op":"%s","target":%q,"write":{"path":%q,"total_size":5,"truncate":true,"eof":true},"data":%q}`,
		agentproto.OpFileWrite,
		target,
		target,
		agentproto.EncodeBytes([]byte("hello")),
	)
	if err := runFileOpHelper(strings.NewReader(writePayload), io.Discard); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	var output strings.Builder
	readPayload := fmt.Sprintf(`{"op":"%s","target":%q,"read":{"path":%q,"max_bytes":16}}`, agentproto.OpFileRead, target, target)
	if err := runFileOpHelper(strings.NewReader(readPayload), &output); err != nil {
		t.Fatalf("read helper: %v", err)
	}
	var response workloadFileOpResponse
	if err := json.Unmarshal([]byte(output.String()), &response); err != nil {
		t.Fatalf("unmarshal helper response: %v", err)
	}
	data, err := agentproto.DecodeBytes(response.Read.Content)
	if err != nil {
		t.Fatalf("decode helper payload: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected helper file content %q", string(data))
	}
	if err := runFileOpHelper(strings.NewReader(fmt.Sprintf(`{"op":"%s","target":%q}`, agentproto.OpFileDelete, target)), io.Discard); err != nil {
		t.Fatalf("delete helper: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected helper delete to remove %s, got %v", target, err)
	}
}

func currentWorkloadIdentity(t *testing.T) workloadIdentity {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		t.Fatalf("parse current uid: %v", err)
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		t.Fatalf("parse current gid: %v", err)
	}
	return workloadIdentity{
		Username: account.Username,
		UID:      uint32(uid),
		GID:      uint32(gid),
		HomeDir:  account.HomeDir,
	}
}
