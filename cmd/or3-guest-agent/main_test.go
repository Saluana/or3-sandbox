package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
