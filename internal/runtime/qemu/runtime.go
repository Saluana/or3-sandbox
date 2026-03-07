package qemu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"time"

	"or3-sandbox/internal/model"
)

const (
	defaultSSHBinary = "ssh"
	defaultSCPBinary = "scp"
)

var _ model.RuntimeManager = (*Runtime)(nil)

type Options struct {
	Binary         string
	Accel          string
	BaseImagePath  string
	SSHUser        string
	SSHKeyPath     string
	BootTimeout    time.Duration
	SSHBinary      string
	SCPBinary      string
}

type Runtime struct {
	qemuBinary    string
	sshBinary     string
	scpBinary     string
	accelerator   string
	baseImagePath string
	sshUser       string
	sshKeyPath    string
	bootTimeout   time.Duration
}

type hostProbe struct {
	goos         string
	commandExists func(string) error
	fileReadable func(string) error
	kvmAvailable func() error
	hvfAvailable func() error
}

type sandboxLayout struct {
	baseDir          string
	rootfsDir        string
	workspaceDir     string
	cacheDir         string
	runtimeDir       string
	rootDiskPath     string
	workspaceDiskPath string
	pidPath          string
	monitorPath      string
	knownHostsPath   string
}

func New(opts Options) (*Runtime, error) {
	if strings.TrimSpace(opts.SSHBinary) == "" {
		opts.SSHBinary = defaultSSHBinary
	}
	if strings.TrimSpace(opts.SCPBinary) == "" {
		opts.SCPBinary = defaultSCPBinary
	}
	accel, err := resolveAccel(opts.Accel, goruntime.GOOS)
	if err != nil {
		return nil, err
	}
	if err := validateHost(opts, accel, defaultHostProbe()); err != nil {
		return nil, err
	}
	return &Runtime{
		qemuBinary:    opts.Binary,
		sshBinary:     opts.SSHBinary,
		scpBinary:     opts.SCPBinary,
		accelerator:   accel,
		baseImagePath: opts.BaseImagePath,
		sshUser:       opts.SSHUser,
		sshKeyPath:    opts.SSHKeyPath,
		bootTimeout:   opts.BootTimeout,
	}, nil
}

func (r *Runtime) Create(ctx context.Context, spec model.SandboxSpec) (model.RuntimeState, error) {
	_ = ctx
	layout := layoutForSpec(spec)
	if err := ensureLayout(layout); err != nil {
		return model.RuntimeState{}, err
	}
	rootBytes, workspaceBytes := splitDiskBytes(spec.DiskLimitMB)
	if err := createSparseFile(layout.rootDiskPath, rootBytes); err != nil {
		return model.RuntimeState{}, err
	}
	if err := createSparseFile(layout.workspaceDiskPath, workspaceBytes); err != nil {
		return model.RuntimeState{}, err
	}
	if err := touchFile(layout.knownHostsPath); err != nil {
		return model.RuntimeState{}, err
	}
	return model.RuntimeState{
		RuntimeID: "qemu-" + spec.SandboxID,
		Status:    model.SandboxStatusStopped,
		Running:   false,
	}, nil
}

func (r *Runtime) Start(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	_ = ctx
	_ = sandbox
	return model.RuntimeState{}, errors.New("qemu backend start is not implemented yet")
}

func (r *Runtime) Stop(ctx context.Context, sandbox model.Sandbox, force bool) (model.RuntimeState, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	pid, err := readPID(layout.pidPath)
	if errors.Is(err, os.ErrNotExist) {
		return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
	}
	if err != nil {
		return model.RuntimeState{}, err
	}
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	if err := syscall.Kill(pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return model.RuntimeState{}, err
	}
	_ = os.Remove(layout.pidPath)
	return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
}

func (r *Runtime) Suspend(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	_ = ctx
	_ = sandbox
	return model.RuntimeState{}, errors.New("qemu backend suspend is not implemented yet")
}

func (r *Runtime) Resume(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	_ = ctx
	_ = sandbox
	return model.RuntimeState{}, errors.New("qemu backend resume is not implemented yet")
}

func (r *Runtime) Destroy(ctx context.Context, sandbox model.Sandbox) error {
	_ = ctx
	_, _ = r.Stop(context.Background(), sandbox, true)
	return os.RemoveAll(layoutForSandbox(sandbox).baseDir)
}

func (r *Runtime) Inspect(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	pid, err := readPID(layout.pidPath)
	if errors.Is(err, os.ErrNotExist) {
		return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
	}
	if err != nil {
		return model.RuntimeState{}, err
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			_ = os.Remove(layout.pidPath)
			return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
		}
		return model.RuntimeState{}, err
	}
	return model.RuntimeState{
		RuntimeID: sandbox.RuntimeID,
		Status:    model.SandboxStatusRunning,
		Running:   true,
		Pid:       pid,
	}, nil
}

func (r *Runtime) Exec(ctx context.Context, sandbox model.Sandbox, req model.ExecRequest, streams model.ExecStreams) (model.ExecHandle, error) {
	_ = ctx
	_ = sandbox
	_ = req
	_ = streams
	return nil, errors.New("qemu backend exec is not implemented yet")
}

func (r *Runtime) AttachTTY(ctx context.Context, sandbox model.Sandbox, req model.TTYRequest) (model.TTYHandle, error) {
	_ = ctx
	_ = sandbox
	_ = req
	return nil, errors.New("qemu backend tty attach is not implemented yet")
}

func (r *Runtime) CreateSnapshot(ctx context.Context, sandbox model.Sandbox, snapshotID string) (model.SnapshotInfo, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	snapshotDir := filepath.Join(sandbox.StorageRoot, ".snapshots", snapshotID)
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return model.SnapshotInfo{}, err
	}
	rootSnapshot := filepath.Join(snapshotDir, "rootfs.img")
	workspaceSnapshot := filepath.Join(snapshotDir, "workspace.img")
	if err := copyFile(layout.rootDiskPath, rootSnapshot); err != nil {
		return model.SnapshotInfo{}, err
	}
	if err := copyFile(layout.workspaceDiskPath, workspaceSnapshot); err != nil {
		return model.SnapshotInfo{}, err
	}
	return model.SnapshotInfo{
		ImageRef:     rootSnapshot,
		WorkspaceTar: workspaceSnapshot,
	}, nil
}

func (r *Runtime) RestoreSnapshot(ctx context.Context, sandbox model.Sandbox, snapshot model.Snapshot) (model.RuntimeState, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	if err := ensureLayout(layout); err != nil {
		return model.RuntimeState{}, err
	}
	if snapshot.ImageRef != "" {
		if err := copyFile(snapshot.ImageRef, layout.rootDiskPath); err != nil {
			return model.RuntimeState{}, err
		}
	}
	if snapshot.WorkspaceTar != "" {
		if err := copyFile(snapshot.WorkspaceTar, layout.workspaceDiskPath); err != nil {
			return model.RuntimeState{}, err
		}
	}
	return model.RuntimeState{
		RuntimeID: sandbox.RuntimeID,
		Status:    model.SandboxStatusStopped,
	}, nil
}

func resolveAccel(value, goos string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		switch goos {
		case "linux":
			return "kvm", nil
		case "darwin":
			return "hvf", nil
		default:
			return "", fmt.Errorf("qemu runtime is unsupported on host OS %q", goos)
		}
	case "kvm":
		if goos != "linux" {
			return "", fmt.Errorf("qemu accel %q is unsupported on host OS %q", value, goos)
		}
		return "kvm", nil
	case "hvf":
		if goos != "darwin" {
			return "", fmt.Errorf("qemu accel %q is unsupported on host OS %q", value, goos)
		}
		return "hvf", nil
	default:
		return "", fmt.Errorf("unsupported qemu accelerator %q", value)
	}
}

func defaultHostProbe() hostProbe {
	return hostProbe{
		goos:         goruntime.GOOS,
		commandExists: requireCommand,
		fileReadable: requireReadableFile,
		kvmAvailable: requireKVM,
		hvfAvailable: requireHVF,
	}
}

func validateHost(opts Options, accel string, probe hostProbe) error {
	if strings.TrimSpace(opts.Binary) == "" {
		return errors.New("qemu binary is required")
	}
	if strings.TrimSpace(opts.BaseImagePath) == "" {
		return errors.New("qemu base image path is required")
	}
	if strings.TrimSpace(opts.SSHUser) == "" {
		return errors.New("qemu ssh user is required")
	}
	if strings.TrimSpace(opts.SSHKeyPath) == "" {
		return errors.New("qemu ssh key path is required")
	}
	if opts.BootTimeout <= 0 {
		return errors.New("qemu boot timeout must be positive")
	}
	for _, command := range []string{opts.Binary, opts.SSHBinary, opts.SCPBinary} {
		if err := probe.commandExists(command); err != nil {
			return fmt.Errorf("required host command %q is unavailable: %w", command, err)
		}
	}
	for _, path := range []string{opts.BaseImagePath, opts.SSHKeyPath} {
		if err := probe.fileReadable(path); err != nil {
			return fmt.Errorf("required file %q is unavailable: %w", path, err)
		}
	}
	switch accel {
	case "kvm":
		if err := probe.kvmAvailable(); err != nil {
			return err
		}
	case "hvf":
		if err := probe.hvfAvailable(); err != nil {
			return err
		}
	}
	return nil
}

func layoutForSpec(spec model.SandboxSpec) sandboxLayout {
	baseDir := filepath.Dir(spec.StorageRoot)
	return sandboxLayout{
		baseDir:           baseDir,
		rootfsDir:         spec.StorageRoot,
		workspaceDir:      spec.WorkspaceRoot,
		cacheDir:          spec.CacheRoot,
		runtimeDir:        filepath.Join(baseDir, ".runtime"),
		rootDiskPath:      filepath.Join(spec.StorageRoot, "overlay.qcow2"),
		workspaceDiskPath: filepath.Join(spec.WorkspaceRoot, "workspace.img"),
		pidPath:           filepath.Join(baseDir, ".runtime", "qemu.pid"),
		monitorPath:       filepath.Join(baseDir, ".runtime", "monitor.sock"),
		knownHostsPath:    filepath.Join(baseDir, ".runtime", "ssh-known-hosts"),
	}
}

func layoutForSandbox(sandbox model.Sandbox) sandboxLayout {
	return layoutForSpec(model.SandboxSpec{
		SandboxID:     sandbox.ID,
		StorageRoot:   sandbox.StorageRoot,
		WorkspaceRoot: sandbox.WorkspaceRoot,
		CacheRoot:     sandbox.CacheRoot,
	})
}

func ensureLayout(layout sandboxLayout) error {
	for _, dir := range []string{layout.rootfsDir, layout.workspaceDir, layout.cacheDir, layout.runtimeDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func splitDiskBytes(totalMB int) (int64, int64) {
	totalBytes := int64(totalMB) * 1024 * 1024
	if totalBytes <= 0 {
		return 0, 0
	}
	root := totalBytes / 2
	workspace := totalBytes - root
	return root, workspace
}

func createSparseFile(path string, sizeBytes int64) error {
	if sizeBytes <= 0 {
		return fmt.Errorf("invalid sparse file size %d", sizeBytes)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Truncate(sizeBytes)
}

func touchFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	return file.Close()
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil {
		return 0, fmt.Errorf("parse pid %s: %w", path, err)
	}
	return pid, nil
}

func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	target, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer target.Close()
	_, err = io.Copy(target, source)
	return err
}

func requireCommand(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("command path is empty")
	}
	if filepath.IsAbs(name) || strings.ContainsRune(name, os.PathSeparator) {
		return requireReadableFile(name)
	}
	_, err := exec.LookPath(name)
	return err
}

func requireReadableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func requireKVM() error {
	return requireReadableFile("/dev/kvm")
}

func requireHVF() error {
	output, err := exec.Command("sysctl", "-n", "kern.hv_support").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	if strings.TrimSpace(string(output)) != "1" {
		return fmt.Errorf("kern.hv_support=%s", strings.TrimSpace(string(output)))
	}
	return nil
}
