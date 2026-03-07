package qemu

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"or3-sandbox/internal/model"
)

func TestResolveAccel(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		goos    string
		want    string
		wantErr bool
	}{
		{name: "auto linux", value: "auto", goos: "linux", want: "kvm"},
		{name: "auto darwin", value: "auto", goos: "darwin", want: "hvf"},
		{name: "explicit kvm", value: "kvm", goos: "linux", want: "kvm"},
		{name: "explicit hvf", value: "hvf", goos: "darwin", want: "hvf"},
		{name: "invalid host", value: "auto", goos: "windows", wantErr: true},
		{name: "invalid accel", value: "tcg", goos: "linux", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveAccel(tc.value, tc.goos)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("unexpected accel: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestStartArgsIncludeNetworkingAndDisks(t *testing.T) {
	r := &Runtime{
		qemuBinary:  "qemu-system-x86_64",
		accelerator: "kvm",
	}
	layout := sandboxLayout{
		pidPath:           "/tmp/qemu.pid",
		monitorPath:       "/tmp/monitor.sock",
		serialLogPath:     "/tmp/serial.log",
		rootDiskPath:      "/tmp/root.qcow2",
		workspaceDiskPath: "/tmp/workspace.img",
	}
	sandbox := model.Sandbox{
		ID:            "sbx-1",
		CPULimit:      2,
		MemoryLimitMB: 768,
		NetworkMode:   model.NetworkModeInternetDisabled,
	}
	args := r.startArgs(sandbox, layout, 2222)
	joined := strings.Join(args, " ")
	for _, snippet := range []string{
		"-daemonize",
		"-pidfile /tmp/qemu.pid",
		"-accel kvm",
		"hostfwd=tcp:127.0.0.1:2222-:22",
		"restrict=on",
		"file=/tmp/root.qcow2",
		"file=/tmp/workspace.img",
	} {
		if !strings.Contains(joined, snippet) {
			t.Fatalf("expected %q in args: %s", snippet, joined)
		}
	}
}

func TestWaitForReadyTimesOut(t *testing.T) {
	r := &Runtime{
		bootTimeout:  200 * time.Millisecond,
		pollInterval: 20 * time.Millisecond,
		sshReady: func(context.Context, sshTarget) error {
			return errors.New("still booting")
		},
	}
	err := r.waitForReady(context.Background(), sshTarget{port: 2222})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestCreateAndSnapshotArtifacts(t *testing.T) {
	base := t.TempDir()
	rootfs := filepath.Join(base, "rootfs")
	workspace := filepath.Join(base, "workspace")
	spec := model.SandboxSpec{
		SandboxID:     "sbx-test",
		StorageRoot:   rootfs,
		WorkspaceRoot: workspace,
		CacheRoot:     filepath.Join(base, "cache"),
		DiskLimitMB:   16,
	}
	r := &Runtime{}
	state, err := r.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if state.Status != model.SandboxStatusStopped {
		t.Fatalf("unexpected create status: %s", state.Status)
	}
	layout := layoutForSpec(spec)
	for _, path := range []string{layout.rootDiskPath, layout.workspaceDiskPath, layout.knownHostsPath, layout.serialLogPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected artifact %s: %v", path, err)
		}
	}

	sandbox := model.Sandbox{
		ID:            spec.SandboxID,
		RuntimeID:     state.RuntimeID,
		StorageRoot:   spec.StorageRoot,
		WorkspaceRoot: spec.WorkspaceRoot,
		CacheRoot:     spec.CacheRoot,
	}
	snapshot, err := r.CreateSnapshot(context.Background(), sandbox, "snap-test")
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	for _, path := range []string{snapshot.ImageRef, snapshot.WorkspaceTar} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected snapshot artifact %s: %v", path, err)
		}
	}
}

func TestInspectReportsErrorWhenGuestIsAliveButNotReady(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer cmd.Process.Kill()

	base := t.TempDir()
	layout := sandboxLayout{
		baseDir:    base,
		runtimeDir: filepath.Join(base, ".runtime"),
		pidPath:    filepath.Join(base, ".runtime", "qemu.pid"),
	}
	if err := ensureLayout(layout); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	if err := os.WriteFile(layout.pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	r := &Runtime{
		bootTimeout:  time.Second,
		pollInterval: 10 * time.Millisecond,
		sshReady: func(context.Context, sshTarget) error {
			return errors.New("not ready")
		},
	}
	sandbox := model.Sandbox{
		ID:            "sbx-inspect",
		RuntimeID:     "qemu-sbx-inspect",
		StorageRoot:   filepath.Join(base, "rootfs"),
		WorkspaceRoot: filepath.Join(base, "workspace"),
		CacheRoot:     filepath.Join(base, "cache"),
	}
	state, err := r.Inspect(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("inspect failed: %v", err)
	}
	if state.Status != model.SandboxStatusError {
		t.Fatalf("unexpected status: %s", state.Status)
	}
}

func TestSuspendAndResumeReturnExplicitUnsupportedErrors(t *testing.T) {
	r := &Runtime{}
	sandbox := model.Sandbox{ID: "sbx-1"}
	if _, err := r.Suspend(context.Background(), sandbox); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected suspend unsupported error, got %v", err)
	}
	if _, err := r.Resume(context.Background(), sandbox); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected resume unsupported error, got %v", err)
	}
}

func TestStartUsesRunnerAndReadinessProbe(t *testing.T) {
	base := t.TempDir()
	sandbox := model.Sandbox{
		ID:            "sbx-start",
		RuntimeID:     "qemu-sbx-start",
		StorageRoot:   filepath.Join(base, "rootfs"),
		WorkspaceRoot: filepath.Join(base, "workspace"),
		CacheRoot:     filepath.Join(base, "cache"),
		MemoryLimitMB: 512,
		CPULimit:      1,
		NetworkMode:   model.NetworkModeInternetEnabled,
	}
	layout := layoutForSandbox(sandbox)
	if err := ensureLayout(layout); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	r := &Runtime{
		qemuBinary:   "qemu-system-x86_64",
		accelerator:  "kvm",
		bootTimeout:  time.Second,
		pollInterval: 10 * time.Millisecond,
		runCommand: func(ctx context.Context, binary string, args ...string) ([]byte, error) {
			if err := os.WriteFile(layout.pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
				return nil, err
			}
			return nil, nil
		},
		sshReady: func(context.Context, sshTarget) error { return nil },
	}
	state, err := r.Start(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if state.Status != model.SandboxStatusRunning {
		t.Fatalf("unexpected start status: %s", state.Status)
	}
	if _, ok := sshPortFromRuntimeID(state.RuntimeID); !ok {
		t.Fatalf("expected runtime id to carry ssh port, got %q", state.RuntimeID)
	}
}

func TestStartCleansUpFailedBoot(t *testing.T) {
	base := t.TempDir()
	sandbox := model.Sandbox{
		ID:            "sbx-failed-start",
		RuntimeID:     "qemu-sbx-failed-start",
		StorageRoot:   filepath.Join(base, "rootfs"),
		WorkspaceRoot: filepath.Join(base, "workspace"),
		CacheRoot:     filepath.Join(base, "cache"),
		MemoryLimitMB: 512,
		CPULimit:      1,
		NetworkMode:   model.NetworkModeInternetEnabled,
	}
	layout := layoutForSandbox(sandbox)
	if err := ensureLayout(layout); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	var child *exec.Cmd
	r := &Runtime{
		qemuBinary:   "qemu-system-x86_64",
		accelerator:  "kvm",
		bootTimeout:  time.Second,
		pollInterval: 10 * time.Millisecond,
		runCommand: func(ctx context.Context, binary string, args ...string) ([]byte, error) {
			child = exec.Command("sleep", "30")
			if err := child.Start(); err != nil {
				return nil, err
			}
			if err := os.WriteFile(layout.pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
				return nil, err
			}
			return nil, nil
		},
		sshReady: func(context.Context, sshTarget) error {
			return errors.New("not ready")
		},
	}
	if _, err := r.Start(context.Background(), sandbox); err == nil {
		t.Fatal("expected start to fail")
	}
	if child == nil {
		t.Fatal("expected fake qemu process to start")
	}
	done := make(chan error, 1)
	go func() {
		done <- child.Wait()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected failed start to reap qemu process")
	}
	if _, err := os.Stat(layout.pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected pid file to be removed, got %v", err)
	}
}

func TestBaseSSHArgsIncludeTTYAndIdentityOptions(t *testing.T) {
	r := &Runtime{
		sshUser:    "ubuntu",
		sshKeyPath: "/tmp/id_ed25519",
	}
	target := sshTarget{port: 2222, knownHostsPath: "/tmp/known_hosts"}

	nonTTY := strings.Join(r.baseSSHArgs(target, false), " ")
	for _, snippet := range []string{
		"-o BatchMode=yes",
		"-o IdentitiesOnly=yes",
		"-o UserKnownHostsFile=/tmp/known_hosts",
		"-i /tmp/id_ed25519",
		"-p 2222",
		"-T",
		"ubuntu@127.0.0.1",
	} {
		if !strings.Contains(nonTTY, snippet) {
			t.Fatalf("expected %q in ssh args: %s", snippet, nonTTY)
		}
	}
	if strings.Contains(nonTTY, "-tt") {
		t.Fatalf("did not expect tty args in non-tty command: %s", nonTTY)
	}

	tty := strings.Join(r.baseSSHArgs(target, true), " ")
	if !strings.Contains(tty, "-tt") {
		t.Fatalf("expected tty args in ssh command: %s", tty)
	}
}

func TestSplitDiskBytesUsesEvenFirstPassPolicy(t *testing.T) {
	rootfsBytes, workspaceBytes := splitDiskBytes(513)
	totalBytes := int64(513) * 1024 * 1024
	if rootfsBytes+workspaceBytes != totalBytes {
		t.Fatalf("unexpected total bytes: got %d want %d", rootfsBytes+workspaceBytes, totalBytes)
	}
	delta := rootfsBytes - workspaceBytes
	if delta < 0 {
		delta = -delta
	}
	if delta > 1024*1024 {
		t.Fatalf("expected near-even split, delta=%d", delta)
	}
}

func TestQemuSizePreservesExactBytes(t *testing.T) {
	if got := qemuSize(512 * 1024); got != "524288" {
		t.Fatalf("unexpected qemu size for half MiB: %q", got)
	}
	if got := qemuSize(256*1024*1024 + 512*1024); got != "268959744" {
		t.Fatalf("unexpected qemu size for fractional MiB split: %q", got)
	}
}

func TestMeasureStorageAggregatesSandboxArtifacts(t *testing.T) {
	base := t.TempDir()
	sandbox := model.Sandbox{
		ID:            "sbx-storage",
		StorageRoot:   filepath.Join(base, "rootfs"),
		WorkspaceRoot: filepath.Join(base, "workspace"),
		CacheRoot:     filepath.Join(base, "cache"),
	}
	layout := layoutForSandbox(sandbox)
	if err := ensureLayout(layout); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	snapshotDir := filepath.Join(sandbox.StorageRoot, ".snapshots", "snap-1")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		t.Fatalf("mkdir snapshot dir: %v", err)
	}
	for path, content := range map[string]string{
		layout.rootDiskPath:                      "rootfs-bytes",
		layout.workspaceDiskPath:                 "workspace-bytes",
		filepath.Join(sandbox.CacheRoot, "x"):    "cache-bytes",
		filepath.Join(snapshotDir, "rootfs.img"): "snapshot-bytes",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write test artifact %s: %v", path, err)
		}
	}

	usage, err := (&Runtime{}).MeasureStorage(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("measure storage: %v", err)
	}
	if usage.RootfsBytes < int64(len("rootfs-bytes")) {
		t.Fatalf("unexpected rootfs bytes: %d", usage.RootfsBytes)
	}
	if usage.WorkspaceBytes < int64(len("workspace-bytes")) {
		t.Fatalf("unexpected workspace bytes: %d", usage.WorkspaceBytes)
	}
	if usage.CacheBytes < int64(len("cache-bytes")) {
		t.Fatalf("unexpected cache bytes: %d", usage.CacheBytes)
	}
	if usage.SnapshotBytes < int64(len("snapshot-bytes")) {
		t.Fatalf("unexpected snapshot bytes: %d", usage.SnapshotBytes)
	}
}

func TestRemoteExecScriptsIncludeWorkingDirEnvAndPidTracking(t *testing.T) {
	script := buildTrackedRemoteScript(
		[]string{"python3", "-c", "print('ok')"},
		"/workspace/app",
		map[string]string{"HELLO": "world"},
		"/tmp/or3-exec.pid",
	)
	for _, snippet := range []string{
		"rm -f '/tmp/or3-exec.pid'",
		"cd '/workspace/app'",
		"export HELLO='world'",
		"setsid sh -lc",
		"echo \"$child\" > '/tmp/or3-exec.pid'",
	} {
		if !strings.Contains(script, snippet) {
			t.Fatalf("expected %q in tracked script: %s", snippet, script)
		}
	}

	interactive := buildInteractiveRemoteScript([]string{"bash"}, "/workspace", nil)
	if !strings.Contains(interactive, "exec sh -lc") {
		t.Fatalf("expected interactive script to exec shell: %s", interactive)
	}
	if !strings.Contains(interactive, "cd '/workspace'") {
		t.Fatalf("expected interactive script to change directory: %s", interactive)
	}
}
