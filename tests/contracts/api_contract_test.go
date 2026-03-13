package contracts_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"or3-sandbox/internal/model"
)

type execStreamEventFixture struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

func TestSandboxCreateRequestFixtureParses(t *testing.T) {
	var payload model.CreateSandboxRequest
	readFixtureJSON(t, "sandbox-create-request.json", &payload)

	if payload.BaseImageRef != "alpine:3.20" {
		t.Fatalf("expected base_image_ref alpine:3.20, got %q", payload.BaseImageRef)
	}
	if payload.AllowTunnels == nil || !*payload.AllowTunnels {
		t.Fatal("expected allow_tunnels=true")
	}
	if payload.CPULimit != model.CPUCores(1) {
		t.Fatalf("expected cpu_limit 1 core, got %s", payload.CPULimit.String())
	}
}

func TestSandboxCreateResponseFixtureParses(t *testing.T) {
	var payload model.Sandbox
	readFixtureJSON(t, "sandbox-create-response.json", &payload)

	if payload.ID != "sbx-123" {
		t.Fatalf("expected sandbox id sbx-123, got %q", payload.ID)
	}
	if payload.TenantID != "tenant-a" {
		t.Fatalf("expected raw provider tenant_id tenant-a, got %q", payload.TenantID)
	}
	if payload.Status != model.SandboxStatusRunning {
		t.Fatalf("expected running sandbox, got %q", payload.Status)
	}
	if payload.RuntimeBackend != "docker" {
		t.Fatalf("expected runtime_backend docker, got %q", payload.RuntimeBackend)
	}
}

func TestSandboxExecResponseFixtureParses(t *testing.T) {
	var payload model.Execution
	readFixtureJSON(t, "sandbox-exec-response.json", &payload)

	if payload.ID != "exec-123" {
		t.Fatalf("expected execution id exec-123, got %q", payload.ID)
	}
	if payload.Status != model.ExecutionStatusSucceeded {
		t.Fatalf("expected succeeded execution, got %q", payload.Status)
	}
	if payload.ExitCode == nil || *payload.ExitCode != 0 {
		t.Fatalf("expected exit_code 0, got %+v", payload.ExitCode)
	}
	if payload.TenantID != "tenant-a" {
		t.Fatalf("expected tenant_id tenant-a, got %q", payload.TenantID)
	}
}

func TestSandboxExecStreamFixturesMatchDocumentedEventSet(t *testing.T) {
	events := readFixtureJSONLines(t, "sandbox-exec-stream-events.jsonl")
	if len(events) != 3 {
		t.Fatalf("expected 3 stream events, got %d", len(events))
	}

	expected := []string{"stdout", "stderr", "result"}
	for index, fixture := range events {
		if fixture.Event != expected[index] {
			t.Fatalf("expected event %q at index %d, got %q", expected[index], index, fixture.Event)
		}
		switch fixture.Event {
		case "stdout", "stderr":
			var chunk string
			if err := json.Unmarshal(fixture.Data, &chunk); err != nil {
				t.Fatalf("decode %s chunk: %v", fixture.Event, err)
			}
			if chunk == "" {
				t.Fatalf("expected non-empty %s chunk", fixture.Event)
			}
		case "result":
			var result model.Execution
			if err := json.Unmarshal(fixture.Data, &result); err != nil {
				t.Fatalf("decode result payload: %v", err)
			}
			if result.Status != model.ExecutionStatusSucceeded {
				t.Fatalf("expected succeeded result, got %q", result.Status)
			}
			if result.ExitCode == nil || *result.ExitCode != 0 {
				t.Fatalf("expected exit_code 0, got %+v", result.ExitCode)
			}
		}
	}
}

func TestSandboxErrorResponseFixtureParses(t *testing.T) {
	var payload model.ErrorResponse
	readFixtureJSON(t, "sandbox-error-response.json", &payload)

	if payload.Status != 404 {
		t.Fatalf("expected status 404, got %d", payload.Status)
	}
	if payload.Code != "not_found" {
		t.Fatalf("expected code not_found, got %q", payload.Code)
	}
	if payload.Error != "not found" {
		t.Fatalf("expected error message not found, got %q", payload.Error)
	}
}

func readFixtureJSON(t *testing.T, name string, target any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
}

func readFixtureJSONLines(t *testing.T, name string) []execStreamEventFixture {
	t.Helper()
	file, err := os.Open(filepath.Join("fixtures", name))
	if err != nil {
		t.Fatalf("open fixture %s: %v", name, err)
	}
	defer file.Close()

	var events []execStreamEventFixture
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var fixture execStreamEventFixture
		if err := json.Unmarshal(scanner.Bytes(), &fixture); err != nil {
			t.Fatalf("decode fixture line in %s: %v", name, err)
		}
		events = append(events, fixture)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fixture %s: %v", name, err)
	}
	return events
}
