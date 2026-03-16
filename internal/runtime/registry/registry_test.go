package registry

import (
	"context"
	"errors"
	"testing"

	"or3-sandbox/internal/model"
)

type stubRuntime struct {
	lastSpec    model.SandboxSpec
	lastSandbox model.Sandbox
	lastPath    string
	lastContent string
	lastBytes   []byte
}

func (s *stubRuntime) Create(_ context.Context, spec model.SandboxSpec) (model.RuntimeState, error) {
	s.lastSpec = spec
	return model.RuntimeState{RuntimeID: spec.SandboxID, Status: model.SandboxStatusStopped}, nil
}

func (s *stubRuntime) Start(_ context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	s.lastSandbox = sandbox
	return model.RuntimeState{RuntimeID: sandbox.ID, Status: model.SandboxStatusRunning}, nil
}

func (s *stubRuntime) Stop(_ context.Context, sandbox model.Sandbox, _ bool) (model.RuntimeState, error) {
	s.lastSandbox = sandbox
	return model.RuntimeState{RuntimeID: sandbox.ID, Status: model.SandboxStatusStopped}, nil
}

func (s *stubRuntime) Suspend(_ context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	s.lastSandbox = sandbox
	return model.RuntimeState{RuntimeID: sandbox.ID, Status: model.SandboxStatusSuspended}, nil
}

func (s *stubRuntime) Resume(_ context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	s.lastSandbox = sandbox
	return model.RuntimeState{RuntimeID: sandbox.ID, Status: model.SandboxStatusRunning}, nil
}

func (s *stubRuntime) Destroy(_ context.Context, sandbox model.Sandbox) error {
	s.lastSandbox = sandbox
	return nil
}

func (s *stubRuntime) Inspect(_ context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	s.lastSandbox = sandbox
	return model.RuntimeState{RuntimeID: sandbox.ID, Status: sandbox.Status}, nil
}

func (s *stubRuntime) Exec(_ context.Context, sandbox model.Sandbox, _ model.ExecRequest, _ model.ExecStreams) (model.ExecHandle, error) {
	s.lastSandbox = sandbox
	return nil, errors.New("not implemented")
}

func (s *stubRuntime) AttachTTY(_ context.Context, sandbox model.Sandbox, _ model.TTYRequest) (model.TTYHandle, error) {
	s.lastSandbox = sandbox
	return nil, errors.New("not implemented")
}

func (s *stubRuntime) CreateSnapshot(_ context.Context, sandbox model.Sandbox, _ string) (model.SnapshotInfo, error) {
	s.lastSandbox = sandbox
	return model.SnapshotInfo{}, nil
}

func (s *stubRuntime) RestoreSnapshot(_ context.Context, sandbox model.Sandbox, _ model.Snapshot) (model.RuntimeState, error) {
	s.lastSandbox = sandbox
	return model.RuntimeState{RuntimeID: sandbox.ID, Status: sandbox.Status}, nil
}

func (s *stubRuntime) ReadWorkspaceFile(_ context.Context, sandbox model.Sandbox, relativePath string) (model.FileReadResponse, error) {
	s.lastSandbox = sandbox
	s.lastPath = relativePath
	return model.FileReadResponse{Path: relativePath, Content: "workspace-content"}, nil
}

func (s *stubRuntime) WriteWorkspaceFile(_ context.Context, sandbox model.Sandbox, relativePath string, content string) error {
	s.lastSandbox = sandbox
	s.lastPath = relativePath
	s.lastContent = content
	return nil
}

func (s *stubRuntime) DeleteWorkspacePath(_ context.Context, sandbox model.Sandbox, relativePath string) error {
	s.lastSandbox = sandbox
	s.lastPath = relativePath
	return nil
}

func (s *stubRuntime) MkdirWorkspace(_ context.Context, sandbox model.Sandbox, relativePath string) error {
	s.lastSandbox = sandbox
	s.lastPath = relativePath
	return nil
}

func (s *stubRuntime) ReadWorkspaceFileBytes(_ context.Context, sandbox model.Sandbox, relativePath string) ([]byte, error) {
	s.lastSandbox = sandbox
	s.lastPath = relativePath
	return []byte("workspace-bytes"), nil
}

func (s *stubRuntime) WriteWorkspaceFileBytes(_ context.Context, sandbox model.Sandbox, relativePath string, content []byte) error {
	s.lastSandbox = sandbox
	s.lastPath = relativePath
	s.lastBytes = append([]byte(nil), content...)
	return nil
}

func (s *stubRuntime) MeasureStorage(_ context.Context, sandbox model.Sandbox) (model.StorageUsage, error) {
	s.lastSandbox = sandbox
	return model.StorageUsage{WorkspaceBytes: 123}, nil
}

func TestRegistryDispatchesCreateBySelection(t *testing.T) {
	dockerRuntime := &stubRuntime{}
	qemuRuntime := &stubRuntime{}
	reg := New(map[model.RuntimeSelection]model.RuntimeManager{
		model.RuntimeSelectionDockerDev:        dockerRuntime,
		model.RuntimeSelectionQEMUProfessional: qemuRuntime,
	})

	_, err := reg.Create(context.Background(), model.SandboxSpec{
		SandboxID:        "sbx-1",
		TenantID:         "tenant-a",
		RuntimeSelection: model.RuntimeSelectionQEMUProfessional,
		RuntimeBackend:   "qemu",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if qemuRuntime.lastSpec.SandboxID != "sbx-1" {
		t.Fatalf("expected qemu runtime to receive create spec, got %+v", qemuRuntime.lastSpec)
	}
	if dockerRuntime.lastSpec.SandboxID != "" {
		t.Fatalf("expected docker runtime to stay unused, got %+v", dockerRuntime.lastSpec)
	}
}

func TestRegistryDispatchesLifecycleByPersistedSandboxSelection(t *testing.T) {
	dockerRuntime := &stubRuntime{}
	qemuRuntime := &stubRuntime{}
	reg := New(map[model.RuntimeSelection]model.RuntimeManager{
		model.RuntimeSelectionDockerDev:        dockerRuntime,
		model.RuntimeSelectionQEMUProfessional: qemuRuntime,
	})

	_, err := reg.Start(context.Background(), model.Sandbox{
		ID:               "sbx-2",
		RuntimeSelection: model.RuntimeSelectionDockerDev,
		RuntimeBackend:   "docker",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if dockerRuntime.lastSandbox.ID != "sbx-2" {
		t.Fatalf("expected docker runtime to receive lifecycle call, got %+v", dockerRuntime.lastSandbox)
	}
	if qemuRuntime.lastSandbox.ID != "" {
		t.Fatalf("expected qemu runtime to stay unused, got %+v", qemuRuntime.lastSandbox)
	}
}

func TestRegistryFallsBackToLegacyBackendSelection(t *testing.T) {
	qemuRuntime := &stubRuntime{}
	reg := New(map[model.RuntimeSelection]model.RuntimeManager{
		model.RuntimeSelectionQEMUProfessional: qemuRuntime,
	})

	_, err := reg.Inspect(context.Background(), model.Sandbox{
		ID:             "sbx-legacy",
		RuntimeBackend: "qemu",
		Status:         model.SandboxStatusRunning,
	})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if qemuRuntime.lastSandbox.ID != "sbx-legacy" {
		t.Fatalf("expected legacy qemu sandbox to dispatch via backend fallback, got %+v", qemuRuntime.lastSandbox)
	}
}

func TestRegistryReturnsUnavailableError(t *testing.T) {
	reg := New(map[model.RuntimeSelection]model.RuntimeManager{})
	_, err := reg.Create(context.Background(), model.SandboxSpec{
		SandboxID:        "sbx-missing",
		RuntimeSelection: model.RuntimeSelectionDockerDev,
		RuntimeBackend:   "docker",
	})
	if err == nil {
		t.Fatal("expected missing runtime error")
	}
	var unavailable RuntimeUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected RuntimeUnavailableError, got %T (%v)", err, err)
	}
}

func TestRegistryDelegatesWorkspaceOperations(t *testing.T) {
	qemuRuntime := &stubRuntime{}
	reg := New(map[model.RuntimeSelection]model.RuntimeManager{
		model.RuntimeSelectionQEMUProfessional: qemuRuntime,
	})
	sandbox := model.Sandbox{ID: "sbx-workspace", RuntimeSelection: model.RuntimeSelectionQEMUProfessional, RuntimeBackend: "qemu"}
	if err := reg.WriteWorkspaceFile(context.Background(), sandbox, "notes.txt", "hello"); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	if qemuRuntime.lastPath != "notes.txt" || qemuRuntime.lastContent != "hello" {
		t.Fatalf("unexpected write delegation path=%q content=%q", qemuRuntime.lastPath, qemuRuntime.lastContent)
	}
	data, err := reg.ReadWorkspaceFileBytes(context.Background(), sandbox, "notes.txt")
	if err != nil {
		t.Fatalf("read workspace file bytes: %v", err)
	}
	if string(data) != "workspace-bytes" {
		t.Fatalf("unexpected delegated bytes %q", string(data))
	}
	if err := reg.WriteWorkspaceFileBytes(context.Background(), sandbox, "blob.bin", []byte("abc")); err != nil {
		t.Fatalf("write workspace file bytes: %v", err)
	}
	if string(qemuRuntime.lastBytes) != "abc" {
		t.Fatalf("unexpected delegated binary write %q", string(qemuRuntime.lastBytes))
	}
	usage, err := reg.MeasureStorage(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("measure storage: %v", err)
	}
	if usage.WorkspaceBytes != 123 {
		t.Fatalf("unexpected delegated storage usage %+v", usage)
	}
}
