package kata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"or3-sandbox/internal/model"
)

// ---------------------------------------------------------------------------
// Fake ctr binary helpers
// ---------------------------------------------------------------------------

// newFakeCtr writes a shell script that logs all received arguments to a file
// and prints a canned response.  Returns the path to the fake binary and the
// path to the args log file.
func newFakeCtr(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	argsLog := filepath.Join(root, "ctr-args.txt")
	fakeCtr := filepath.Join(root, "ctr")
	script := `#!/bin/sh
for arg in "$@"; do printf '%s\n' "$arg"; done >> "$CTR_ARGS_LOG"
printf '%s\n' '---' >> "$CTR_ARGS_LOG"
`
	if err := os.WriteFile(fakeCtr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CTR_ARGS_LOG", argsLog)
	return fakeCtr, argsLog
}

// newFakeCtrScript writes a custom ctr script.
func newFakeCtrScript(t *testing.T, script string) (string, string) {
	t.Helper()
	root := t.TempDir()
	argsLog := filepath.Join(root, "ctr-args.txt")
	fakeCtr := filepath.Join(root, "ctr")
	if err := os.WriteFile(fakeCtr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CTR_ARGS_LOG", argsLog)
	return fakeCtr, argsLog
}

// readArgsLog returns the accumulated args from the log file.
func readArgsLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNewDefaults(t *testing.T) {
	rt := New(Options{})
	if rt.binary != "ctr" {
		t.Errorf("expected default binary 'ctr', got %q", rt.binary)
	}
	if rt.runtimeClass != "kata-qemu" {
		t.Errorf("expected default runtimeClass 'kata-qemu', got %q", rt.runtimeClass)
	}
	if rt.namespace != defaultNamespace {
		t.Errorf("expected default namespace %q, got %q", defaultNamespace, rt.namespace)
	}
}

func TestNewCustomOptions(t *testing.T) {
	rt := New(Options{
		Binary:                    "/usr/local/bin/ctr",
		RuntimeClass:              "kata-clh",
		ContainerdSocket:          "/run/containerd/containerd.sock",
		Namespace:                 "my-ns",
		SnapshotMaxBytes:          1024,
		SnapshotMaxFiles:          10,
		SnapshotMaxExpansionRatio: 5,
	})
	if rt.binary != "/usr/local/bin/ctr" {
		t.Errorf("binary = %q", rt.binary)
	}
	if rt.runtimeClass != "kata-clh" {
		t.Errorf("runtimeClass = %q", rt.runtimeClass)
	}
	if rt.socket != "/run/containerd/containerd.sock" {
		t.Errorf("socket = %q", rt.socket)
	}
	if rt.namespace != "my-ns" {
		t.Errorf("namespace = %q", rt.namespace)
	}
	if rt.restoreLimits.MaxBytes != 1024 {
		t.Errorf("MaxBytes = %d", rt.restoreLimits.MaxBytes)
	}
	if rt.restoreLimits.MaxFiles != 10 {
		t.Errorf("MaxFiles = %d", rt.restoreLimits.MaxFiles)
	}
	if rt.restoreLimits.MaxExpansionRatio != 5 {
		t.Errorf("MaxExpansionRatio = %d", rt.restoreLimits.MaxExpansionRatio)
	}
}

// ---------------------------------------------------------------------------
// Create tests
// ---------------------------------------------------------------------------

func TestCreateRejectsNonLinux(t *testing.T) {
	rt := New(Options{HostOS: "darwin"})
	_, err := rt.Create(context.Background(), model.SandboxSpec{SandboxID: "sbx-1"})
	if err == nil || !strings.Contains(err.Error(), "linux") {
		t.Fatalf("expected linux-only error, got: %v", err)
	}
}

func TestCreatePersistsState(t *testing.T) {
	fakeCtr, _ := newFakeCtr(t)
	storageRoot := t.TempDir()
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")

	rt := New(Options{Binary: fakeCtr, HostOS: "linux"})
	state, err := rt.Create(context.Background(), model.SandboxSpec{
		SandboxID:     "sbx-test-1",
		BaseImageRef:  "docker.io/library/ubuntu:22.04",
		StorageRoot:   storageRoot,
		WorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if state.RuntimeID != "or3-sbx-test-1" {
		t.Errorf("RuntimeID = %q", state.RuntimeID)
	}
	if state.Status != model.SandboxStatusStopped {
		t.Errorf("Status = %q", state.Status)
	}

	// Verify state file was written.
	stateFile := filepath.Join(storageRoot, ".kata", "state.json")
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		t.Fatal("state.json was not created")
	}

	// Verify workspace directory was created.
	if _, err := os.Stat(workspaceRoot); os.IsNotExist(err) {
		t.Fatal("workspace directory was not created")
	}
}

func TestRunArgsBuildCorrectly(t *testing.T) {
	rt := New(Options{
		Binary:           "/fake/ctr",
		RuntimeClass:     "kata-qemu",
		ContainerdSocket: "/run/containerd/test.sock",
		Namespace:        "test-ns",
		HostOS:           "linux",
	})

	spec := model.SandboxSpec{
		SandboxID:     "sbx-args-1",
		BaseImageRef:  "docker.io/library/alpine:latest",
		WorkspaceRoot: "/tmp/ws",
		CacheRoot:     "/tmp/cache",
		ScratchRoot:   "/tmp/scratch",
		SecretsRoot:   "/tmp/secrets",
		MemoryLimitMB: 512,
		CPULimit:      2000,
		NetworkMode:   model.NetworkModeInternetDisabled,
	}

	args := rt.runArgs(spec)

	// Should contain namespace and socket flags.
	if !slices.Contains(args, "--namespace") {
		t.Error("missing --namespace flag")
	}
	if !slices.Contains(args, "test-ns") {
		t.Error("missing namespace value")
	}
	if !slices.Contains(args, "--address") {
		t.Error("missing --address flag")
	}
	if !slices.Contains(args, "/run/containerd/test.sock") {
		t.Error("missing socket value")
	}

	// Should contain runtime class.
	if !slices.Contains(args, "--runtime") {
		t.Error("missing --runtime flag")
	}
	if !slices.Contains(args, "kata-qemu") {
		t.Error("missing runtime class value")
	}

	// Should contain bind mounts.
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "type=bind,src=/tmp/ws,dst=/workspace") {
		t.Error("missing workspace mount")
	}
	if !strings.Contains(joined, "type=bind,src=/tmp/cache,dst=/cache") {
		t.Error("missing cache mount")
	}
	if !strings.Contains(joined, "type=bind,src=/tmp/scratch,dst=/scratch") {
		t.Error("missing scratch mount")
	}
	if !strings.Contains(joined, "type=bind,src=/tmp/secrets,dst=/secrets,options=rbind:ro") {
		t.Error("missing secrets mount (should be ro)")
	}

	// Memory limit.
	memIdx := slices.Index(args, "--memory-limit")
	if memIdx < 0 || memIdx+1 >= len(args) {
		t.Fatal("missing --memory-limit")
	}
	if args[memIdx+1] != "536870912" { // 512 * 1024 * 1024
		t.Errorf("memory-limit = %q, want 536870912", args[memIdx+1])
	}

	// CPU.
	cpuIdx := slices.Index(args, "--cpus")
	if cpuIdx < 0 || cpuIdx+1 >= len(args) {
		t.Fatal("missing --cpus")
	}
	if args[cpuIdx+1] != "2" { // 2000/1000 = 2
		t.Errorf("cpus = %q, want '2'", args[cpuIdx+1])
	}

	// Loopback label when internet disabled.
	if !strings.Contains(joined, "or3.network.loopback-only=true") {
		t.Error("missing loopback-only label for internet-disabled")
	}

	// Image and container name should be the last two positional args.
	if args[len(args)-2] != "docker.io/library/alpine:latest" {
		t.Errorf("second-to-last arg = %q, want image ref", args[len(args)-2])
	}
	if args[len(args)-1] != "or3-sbx-args-1" {
		t.Errorf("last arg = %q, want container name", args[len(args)-1])
	}
}

func TestRunArgsNoLoopbackLabelWhenInternetEnabled(t *testing.T) {
	rt := New(Options{HostOS: "linux"})
	spec := model.SandboxSpec{
		SandboxID:    "sbx-net-1",
		BaseImageRef: "alpine:latest",
		NetworkMode:  model.NetworkModeInternetEnabled,
	}
	args := rt.runArgs(spec)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "loopback-only") {
		t.Error("loopback-only label should not be present when internet is enabled")
	}
}

// ---------------------------------------------------------------------------
// Start / Stop tests
// ---------------------------------------------------------------------------

func TestStartParsesPID(t *testing.T) {
	script := `#!/bin/sh
for arg in "$@"; do printf '%s\n' "$arg"; done >> "$CTR_ARGS_LOG"
printf '%s\n' '---' >> "$CTR_ARGS_LOG"
# Simulate ctr task start output: prints PID
echo "42"
`
	fakeCtr, argsLog := newFakeCtrScript(t, script)
	storageRoot := t.TempDir()
	stateDir := filepath.Join(storageRoot, ".kata")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	rt := New(Options{Binary: fakeCtr, HostOS: "linux"})
	state, err := rt.Start(context.Background(), model.Sandbox{
		ID:          "sbx-start-1",
		StorageRoot: storageRoot,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if state.Pid != 42 {
		t.Errorf("Pid = %d, want 42", state.Pid)
	}
	if state.Status != model.SandboxStatusRunning {
		t.Errorf("Status = %q", state.Status)
	}
	if !state.Running {
		t.Error("Running should be true")
	}

	// Verify PID file was written.
	pidData, err := os.ReadFile(filepath.Join(stateDir, "task.pid"))
	if err != nil {
		t.Fatalf("reading task.pid: %v", err)
	}
	if strings.TrimSpace(string(pidData)) != "42" {
		t.Errorf("task.pid = %q, want '42'", string(pidData))
	}

	// Verify args logged.
	logged := readArgsLog(t, argsLog)
	if !strings.Contains(logged, "task") || !strings.Contains(logged, "start") {
		t.Errorf("expected 'task start' in args log, got:\n%s", logged)
	}
}

func TestStopCleansUpPIDFile(t *testing.T) {
	fakeCtr, _ := newFakeCtr(t)
	storageRoot := t.TempDir()
	stateDir := filepath.Join(storageRoot, ".kata")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "task.pid"), []byte("42"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := New(Options{Binary: fakeCtr, HostOS: "linux"})
	state, err := rt.Stop(context.Background(), model.Sandbox{
		ID:          "sbx-stop-1",
		StorageRoot: storageRoot,
	}, false)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if state.Status != model.SandboxStatusStopped {
		t.Errorf("Status = %q", state.Status)
	}

	// PID file should be removed.
	if _, err := os.Stat(filepath.Join(stateDir, "task.pid")); !os.IsNotExist(err) {
		t.Error("task.pid should have been removed after stop")
	}
}

// ---------------------------------------------------------------------------
// Suspend / Resume tests
// ---------------------------------------------------------------------------

func TestSuspendReturnsUnsupported(t *testing.T) {
	rt := New(Options{HostOS: "linux"})
	_, err := rt.Suspend(context.Background(), model.Sandbox{})
	if err == nil || !strings.Contains(err.Error(), "not support") {
		t.Fatalf("expected unsupported error, got: %v", err)
	}
}

func TestResumeReturnsUnsupported(t *testing.T) {
	rt := New(Options{HostOS: "linux"})
	_, err := rt.Resume(context.Background(), model.Sandbox{})
	if err == nil || !strings.Contains(err.Error(), "not support") {
		t.Fatalf("expected unsupported error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Inspect tests
// ---------------------------------------------------------------------------

func TestInspectRunningTask(t *testing.T) {
	script := `#!/bin/sh
for arg in "$@"; do printf '%s\n' "$arg"; done >> "$CTR_ARGS_LOG"
printf '%s\n' '---' >> "$CTR_ARGS_LOG"
# Simulate "ctr task ls" output
if echo "$@" | grep -q "task ls"; then
    printf 'TASK                    PID      STATUS\n'
    printf 'or3-sbx-inspect-1       1234     RUNNING\n'
fi
`
	fakeCtr, _ := newFakeCtrScript(t, script)
	rt := New(Options{Binary: fakeCtr, HostOS: "linux"})
	state, err := rt.Inspect(context.Background(), model.Sandbox{ID: "sbx-inspect-1"})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.Status != model.SandboxStatusRunning {
		t.Errorf("Status = %q, want running", state.Status)
	}
	if !state.Running {
		t.Error("Running should be true")
	}
	if state.Pid != 1234 {
		t.Errorf("Pid = %d, want 1234", state.Pid)
	}
}

func TestInspectStoppedTask(t *testing.T) {
	script := `#!/bin/sh
for arg in "$@"; do printf '%s\n' "$arg"; done >> "$CTR_ARGS_LOG"
printf '%s\n' '---' >> "$CTR_ARGS_LOG"
if echo "$@" | grep -q "task ls"; then
    printf 'TASK                    PID      STATUS\n'
    printf 'or3-sbx-inspect-2       0        STOPPED\n'
fi
`
	fakeCtr, _ := newFakeCtrScript(t, script)
	rt := New(Options{Binary: fakeCtr, HostOS: "linux"})
	state, err := rt.Inspect(context.Background(), model.Sandbox{ID: "sbx-inspect-2"})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.Status != model.SandboxStatusStopped {
		t.Errorf("Status = %q, want stopped", state.Status)
	}
	if state.Running {
		t.Error("Running should be false")
	}
}

func TestInspectDegradedOnCtrFailure(t *testing.T) {
	script := `#!/bin/sh
echo "connection refused" >&2
exit 1
`
	fakeCtr, _ := newFakeCtrScript(t, script)
	rt := New(Options{Binary: fakeCtr, HostOS: "linux"})
	state, err := rt.Inspect(context.Background(), model.Sandbox{ID: "sbx-inspect-3"})
	if err != nil {
		t.Fatalf("Inspect should not error, got: %v", err)
	}
	if state.Status != model.SandboxStatusDegraded {
		t.Errorf("Status = %q, want degraded", state.Status)
	}
	if state.Error == "" {
		t.Error("Error message should be set for degraded state")
	}
}

func TestInspectDeletedWhenContainerMissing(t *testing.T) {
	script := `#!/bin/sh
for arg in "$@"; do printf '%s\n' "$arg"; done >> "$CTR_ARGS_LOG"
printf '%s\n' '---' >> "$CTR_ARGS_LOG"
if echo "$@" | grep -q "task ls"; then
    # No matching task
    printf 'TASK    PID    STATUS\n'
fi
if echo "$@" | grep -q "containers ls"; then
    # No matching container
    printf ''
fi
`
	fakeCtr, _ := newFakeCtrScript(t, script)
	rt := New(Options{Binary: fakeCtr, HostOS: "linux"})
	state, err := rt.Inspect(context.Background(), model.Sandbox{ID: "sbx-inspect-4"})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.Status != model.SandboxStatusDeleted {
		t.Errorf("Status = %q, want deleted", state.Status)
	}
}

// ---------------------------------------------------------------------------
// Snapshot / restore tests
// ---------------------------------------------------------------------------

func TestSnapshotAndRestore(t *testing.T) {
	fakeCtr, _ := newFakeCtr(t)
	storageRoot := t.TempDir()
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")

	// Set up workspace with a test file.
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write state so snapshot can read base image ref.
	stateDir := filepath.Join(storageRoot, ".kata")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeState(stateDir, localState{
		SandboxID:    "sbx-snap-1",
		BaseImageRef: "docker.io/library/ubuntu:22.04",
	}); err != nil {
		t.Fatal(err)
	}

	rt := New(Options{Binary: fakeCtr, HostOS: "linux"})
	sandbox := model.Sandbox{
		ID:            "sbx-snap-1",
		StorageRoot:   storageRoot,
		WorkspaceRoot: workspaceRoot,
	}

	// Create snapshot.
	info, err := rt.CreateSnapshot(context.Background(), sandbox, "snap-001")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if info.ImageRef != "docker.io/library/ubuntu:22.04" {
		t.Errorf("ImageRef = %q", info.ImageRef)
	}
	if info.WorkspaceTar == "" {
		t.Fatal("WorkspaceTar should not be empty")
	}
	if _, err := os.Stat(info.WorkspaceTar); os.IsNotExist(err) {
		t.Fatal("snapshot archive does not exist")
	}

	// Restore into a new workspace.
	newWorkspace := filepath.Join(t.TempDir(), "restored")
	restoreSandbox := model.Sandbox{
		ID:            "sbx-snap-1",
		StorageRoot:   storageRoot,
		WorkspaceRoot: newWorkspace,
	}
	snapshot := model.Snapshot{
		WorkspaceTar: info.WorkspaceTar,
	}
	restoreState, err := rt.RestoreSnapshot(context.Background(), restoreSandbox, snapshot)
	if err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	if restoreState.Status != model.SandboxStatusStopped {
		t.Errorf("Status = %q after restore", restoreState.Status)
	}

	// Verify restored file.
	data, err := os.ReadFile(filepath.Join(newWorkspace, "hello.txt"))
	if err != nil {
		t.Fatalf("reading restored file: %v", err)
	}
	if string(data) != "world" {
		t.Errorf("restored content = %q, want 'world'", string(data))
	}
}

func TestRestoreSnapshotNoWorkspaceRoot(t *testing.T) {
	rt := New(Options{HostOS: "linux"})
	_, err := rt.RestoreSnapshot(context.Background(), model.Sandbox{}, model.Snapshot{WorkspaceTar: "/some/archive.tar.gz"})
	if err == nil || !strings.Contains(err.Error(), "workspace root") {
		t.Fatalf("expected workspace root error, got: %v", err)
	}
}

func TestRestoreSnapshotNoArchive(t *testing.T) {
	rt := New(Options{HostOS: "linux"})
	_, err := rt.RestoreSnapshot(context.Background(), model.Sandbox{WorkspaceRoot: "/tmp/ws"}, model.Snapshot{})
	if err == nil || !strings.Contains(err.Error(), "workspace archive") {
		t.Fatalf("expected archive error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Destroy tests
// ---------------------------------------------------------------------------

func TestDestroyToleratesNotFound(t *testing.T) {
	script := `#!/bin/sh
for arg in "$@"; do printf '%s\n' "$arg"; done >> "$CTR_ARGS_LOG"
printf '%s\n' '---' >> "$CTR_ARGS_LOG"
if echo "$@" | grep -q "containers delete"; then
    echo "not found" >&2
    exit 1
fi
`
	fakeCtr, _ := newFakeCtrScript(t, script)

	storageRoot := filepath.Join(t.TempDir(), "storage")
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	for _, d := range []string{storageRoot, workspaceRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	rt := New(Options{Binary: fakeCtr, HostOS: "linux"})
	err := rt.Destroy(context.Background(), model.Sandbox{
		ID:            "sbx-destroy-1",
		StorageRoot:   storageRoot,
		WorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		t.Fatalf("Destroy should tolerate not-found, got: %v", err)
	}

	// Storage and workspace should be removed.
	if _, err := os.Stat(storageRoot); !os.IsNotExist(err) {
		t.Error("storageRoot should have been removed")
	}
	if _, err := os.Stat(workspaceRoot); !os.IsNotExist(err) {
		t.Error("workspaceRoot should have been removed")
	}
}

// ---------------------------------------------------------------------------
// Helper function tests
// ---------------------------------------------------------------------------

func TestIsNotFoundErr(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"ctr: container not found", true},
		{"no such container", true},
		{"something else entirely", false},
		{"", false},
	}
	for _, tc := range tests {
		var err error
		if tc.msg != "" {
			err = errors.New(tc.msg)
		}
		if got := isNotFoundErr(err); got != tc.want {
			t.Errorf("isNotFoundErr(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

func TestIsNotFoundErrNil(t *testing.T) {
	if isNotFoundErr(nil) {
		t.Error("isNotFoundErr(nil) should be false")
	}
}

func TestParsePID(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"42\n", 42},
		{"  1234  \n", 1234},
		{"PID is 5678", 0},
		{"no-number-here", 0},
		{"", 0},
	}
	for _, tc := range tests {
		if got := parsePID(tc.input); got != tc.want {
			t.Errorf("parsePID(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestParseTaskList(t *testing.T) {
	output := `TASK                    PID      STATUS
or3-sbx-1               1234     RUNNING
or3-sbx-2               5678     STOPPED
`
	status, pid := parseTaskList(output, "or3-sbx-1")
	if status != "RUNNING" || pid != 1234 {
		t.Errorf("parseTaskList(or3-sbx-1) = (%q, %d), want (RUNNING, 1234)", status, pid)
	}

	status2, pid2 := parseTaskList(output, "or3-sbx-2")
	if status2 != "STOPPED" || pid2 != 5678 {
		t.Errorf("parseTaskList(or3-sbx-2) = (%q, %d), want (STOPPED, 5678)", status2, pid2)
	}

	status3, pid3 := parseTaskList(output, "or3-sbx-missing")
	if status3 != "" || pid3 != 0 {
		t.Errorf("parseTaskList(missing) = (%q, %d), want ('', 0)", status3, pid3)
	}
}

func TestContainerInList(t *testing.T) {
	output := "or3-sbx-1\nor3-sbx-2\n"
	if !containerInList(output, "or3-sbx-1") {
		t.Error("should find or3-sbx-1")
	}
	if containerInList(output, "or3-sbx-3") {
		t.Error("should not find or3-sbx-3")
	}
}

func TestContainerName(t *testing.T) {
	if got := containerName("sbx-abc"); got != "or3-sbx-abc" {
		t.Errorf("containerName = %q", got)
	}
}

// ---------------------------------------------------------------------------
// State persistence tests
// ---------------------------------------------------------------------------

func TestWriteAndReadState(t *testing.T) {
	dir := t.TempDir()
	original := localState{
		SandboxID:     "sbx-state-1",
		BaseImageRef:  "docker.io/library/alpine:latest",
		WorkspaceRoot: "/tmp/ws",
		CacheRoot:     "/tmp/cache",
	}
	if err := writeState(dir, original); err != nil {
		t.Fatalf("writeState: %v", err)
	}

	loaded, err := readState(dir)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if loaded.SandboxID != original.SandboxID {
		t.Errorf("SandboxID = %q", loaded.SandboxID)
	}
	if loaded.BaseImageRef != original.BaseImageRef {
		t.Errorf("BaseImageRef = %q", loaded.BaseImageRef)
	}
	if loaded.WorkspaceRoot != original.WorkspaceRoot {
		t.Errorf("WorkspaceRoot = %q", loaded.WorkspaceRoot)
	}
	if loaded.CacheRoot != original.CacheRoot {
		t.Errorf("CacheRoot = %q", loaded.CacheRoot)
	}
}
