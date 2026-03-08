package qemu

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"or3-sandbox/internal/model"
)

const (
	defaultSSHBinary     = "ssh"
	defaultSCPBinary     = "scp"
	defaultQEMUImgBinary = "qemu-img"
	readyMarkerPath      = "/var/lib/or3/bootstrap.ready"
	readyProbeTimeout    = 2 * time.Second
	defaultPollInterval  = 500 * time.Millisecond
	qemuRuntimePrefix    = "qemu-"
	sshPortBase          = 22000
	sshPortSpan          = 20000
	serialTailLimit      = 64 * 1024
)

var bootFailureMarkers = []string{
	"kernel panic",
	"no bootable device",
	"emergency mode",
	"failed to start",
	"gave up waiting",
}

var _ model.RuntimeManager = (*Runtime)(nil)

type commandRunner func(ctx context.Context, binary string, args ...string) ([]byte, error)

type sshProbe func(ctx context.Context, target sshTarget) error

type Options struct {
	Binary        string
	Accel         string
	BaseImagePath string
	SSHUser       string
	SSHKeyPath    string
	BootTimeout   time.Duration
	SSHBinary     string
	SCPBinary     string
}

type Runtime struct {
	qemuBinary    string
	qemuImgBinary string
	sshBinary     string
	scpBinary     string
	accelerator   string
	baseImagePath string
	sshUser       string
	sshKeyPath    string
	bootTimeout   time.Duration
	pollInterval  time.Duration

	runCommand commandRunner
	sshReady   sshProbe
}

type hostProbe struct {
	goos          string
	commandExists func(string) error
	fileReadable  func(string) error
	kvmAvailable  func() error
	hvfAvailable  func() error
}

type sandboxLayout struct {
	baseDir           string
	rootfsDir         string
	workspaceDir      string
	cacheDir          string
	runtimeDir        string
	rootDiskPath      string
	workspaceDiskPath string
	pidPath           string
	monitorPath       string
	knownHostsPath    string
	serialLogPath     string
}

type sshTarget struct {
	port           int
	knownHostsPath string
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
	qemuImgBinary := deriveQEMUImgBinary(opts.Binary)
	if err := validateHost(opts, qemuImgBinary, accel, defaultHostProbe()); err != nil {
		return nil, err
	}
	runtime := &Runtime{
		qemuBinary:    opts.Binary,
		qemuImgBinary: qemuImgBinary,
		sshBinary:     opts.SSHBinary,
		scpBinary:     opts.SCPBinary,
		accelerator:   accel,
		baseImagePath: opts.BaseImagePath,
		sshUser:       opts.SSHUser,
		sshKeyPath:    opts.SSHKeyPath,
		bootTimeout:   opts.BootTimeout,
		pollInterval:  defaultPollInterval,
	}
	runtime.runCommand = runtime.defaultRunCommand
	runtime.sshReady = runtime.defaultSSHProbe
	return runtime, nil
}

func (r *Runtime) Create(ctx context.Context, spec model.SandboxSpec) (model.RuntimeState, error) {
	layout := layoutForSpec(spec)
	if err := ensureLayout(layout); err != nil {
		return model.RuntimeState{}, err
	}
	if err := os.Remove(layout.pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return model.RuntimeState{}, err
	}
	baseImagePath := r.guestBaseImage(spec.BaseImageRef)
	rootBytes, workspaceBytes := splitDiskBytes(spec.DiskLimitMB)
	if err := r.createRootDisk(ctx, baseImagePath, layout.rootDiskPath, rootBytes); err != nil {
		return model.RuntimeState{}, err
	}
	if err := r.createWorkspaceDisk(ctx, layout.workspaceDiskPath, workspaceBytes); err != nil {
		return model.RuntimeState{}, err
	}
	if err := touchFile(layout.knownHostsPath); err != nil {
		return model.RuntimeState{}, err
	}
	if err := touchFile(layout.serialLogPath); err != nil {
		return model.RuntimeState{}, err
	}
	return model.RuntimeState{
		RuntimeID: qemuRuntimePrefix + spec.SandboxID,
		Status:    model.SandboxStatusStopped,
		Running:   false,
	}, nil
}

func (r *Runtime) Start(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	if state, err := r.Inspect(ctx, sandbox); err == nil && state.Status == model.SandboxStatusRunning {
		return state, nil
	}
	layout := layoutForSandbox(sandbox)
	if err := ensureLayout(layout); err != nil {
		return model.RuntimeState{}, err
	}
	target, runtimeID, err := r.startTarget(sandbox, layout)
	if err != nil {
		return model.RuntimeState{}, err
	}
	args := r.startArgs(sandbox, layout, target.port)
	if _, err := r.runCommand(ctx, r.qemuBinary, args...); err != nil {
		return model.RuntimeState{}, fmt.Errorf("start qemu guest: %w", err)
	}
	if _, err := waitForPID(layout.pidPath, r.bootTimeout); err != nil {
		return model.RuntimeState{}, err
	}
	if err := r.waitForReady(ctx, target, layout.serialLogPath); err != nil {
		_, _ = r.Stop(context.Background(), sandboxWithRuntimeID(sandbox, runtimeID), true)
		return model.RuntimeState{}, err
	}
	return r.Inspect(ctx, sandboxWithRuntimeID(sandbox, runtimeID))
}

func (r *Runtime) Stop(ctx context.Context, sandbox model.Sandbox, force bool) (model.RuntimeState, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	pid, err := readPID(layout.pidPath)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(suspendedMarkerPath(layout))
		return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
	}
	if err != nil {
		return model.RuntimeState{}, err
	}
	if isSuspended(layout) && !force {
		if err := syscall.Kill(pid, syscall.SIGCONT); err != nil && !errors.Is(err, syscall.ESRCH) {
			return model.RuntimeState{}, err
		}
	}
	if err := terminatePID(pid, force); err != nil {
		return model.RuntimeState{}, err
	}
	_ = os.Remove(layout.pidPath)
	_ = os.Remove(suspendedMarkerPath(layout))
	return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
}

func (r *Runtime) Suspend(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
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
			_ = os.Remove(suspendedMarkerPath(layout))
			return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
		}
		return model.RuntimeState{}, err
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		return model.RuntimeState{}, err
	}
	if err := touchFile(suspendedMarkerPath(layout)); err != nil {
		return model.RuntimeState{}, err
	}
	return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusSuspended, Running: false, Pid: pid}, nil
}

func (r *Runtime) Resume(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	layout := layoutForSandbox(sandbox)
	pid, err := readPID(layout.pidPath)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(suspendedMarkerPath(layout))
		return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
	}
	if err != nil {
		return model.RuntimeState{}, err
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			_ = os.Remove(layout.pidPath)
			_ = os.Remove(suspendedMarkerPath(layout))
			return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
		}
		return model.RuntimeState{}, err
	}
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		return model.RuntimeState{}, err
	}
	_ = os.Remove(suspendedMarkerPath(layout))
	target := r.sshTarget(sandbox, layout)
	if err := r.waitForReady(ctx, target, layout.serialLogPath); err != nil {
		return model.RuntimeState{}, err
	}
	return r.Inspect(ctx, sandbox)
}

func (r *Runtime) Destroy(ctx context.Context, sandbox model.Sandbox) error {
	_, _ = r.Stop(ctx, sandbox, true)
	return os.RemoveAll(layoutForSandbox(sandbox).baseDir)
}

func (r *Runtime) Inspect(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	layout := layoutForSandbox(sandbox)
	pid, err := readPID(layout.pidPath)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(suspendedMarkerPath(layout))
		return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
	}
	if err != nil {
		return model.RuntimeState{}, err
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			_ = os.Remove(layout.pidPath)
			_ = os.Remove(suspendedMarkerPath(layout))
			return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
		}
		return model.RuntimeState{}, err
	}
	if isSuspended(layout) {
		return model.RuntimeState{
			RuntimeID: sandbox.RuntimeID,
			Status:    model.SandboxStatusSuspended,
			Running:   false,
			Pid:       pid,
		}, nil
	}
	target := r.sshTarget(sandbox, layout)
	probeCtx, cancel := context.WithTimeout(ctx, readyProbeTimeout)
	defer cancel()
	if err := r.sshReady(probeCtx, target); err != nil {
		if reason, ok := bootFailureReason(layout.serialLogPath); ok {
			return model.RuntimeState{
				RuntimeID: sandbox.RuntimeID,
				Status:    model.SandboxStatusError,
				Running:   false,
				Pid:       pid,
				Error:     reason,
			}, nil
		}
		if withinBootWindow(layout.pidPath, r.effectiveBootTimeout()) {
			return model.RuntimeState{
				RuntimeID: sandbox.RuntimeID,
				Status:    model.SandboxStatusBooting,
				Running:   false,
				Pid:       pid,
				Error:     fmt.Sprintf("guest is still booting: %v", err),
			}, nil
		}
		return model.RuntimeState{
			RuntimeID: sandbox.RuntimeID,
			Status:    model.SandboxStatusDegraded,
			Running:   false,
			Pid:       pid,
			Error:     fmt.Sprintf("guest process is alive but not ready: %v", err),
		}, nil
	}
	return model.RuntimeState{
		RuntimeID: sandbox.RuntimeID,
		Status:    model.SandboxStatusRunning,
		Running:   true,
		Pid:       pid,
		IPAddress: "127.0.0.1",
	}, nil
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

func (r *Runtime) createRootDisk(ctx context.Context, baseImagePath, outputPath string, sizeBytes int64) error {
	if r.qemuImgBinary == "" || r.runCommand == nil {
		return createSparseFile(outputPath, sizeBytes)
	}
	_ = os.Remove(outputPath)
	_, err := r.runCommand(
		ctx,
		r.qemuImgBinary,
		"create",
		"-f", "qcow2",
		"-F", "qcow2",
		"-b", baseImagePath,
		outputPath,
		qemuSize(sizeBytes),
	)
	return err
}

func (r *Runtime) createWorkspaceDisk(ctx context.Context, outputPath string, sizeBytes int64) error {
	if r.qemuImgBinary == "" || r.runCommand == nil {
		return createSparseFile(outputPath, sizeBytes)
	}
	_ = os.Remove(outputPath)
	_, err := r.runCommand(
		ctx,
		r.qemuImgBinary,
		"create",
		"-f", "raw",
		outputPath,
		qemuSize(sizeBytes),
	)
	return err
}

func (r *Runtime) startArgs(sandbox model.Sandbox, layout sandboxLayout, sshPort int) []string {
	args := []string{
		"-daemonize",
		"-pidfile", layout.pidPath,
		"-monitor", "unix:" + layout.monitorPath + ",server,nowait",
		"-serial", "file:" + layout.serialLogPath,
		"-display", "none",
		"-accel", r.accelerator,
		"-m", strconv.Itoa(defaultInt(sandbox.MemoryLimitMB, 512)),
		"-smp", strconv.Itoa(defaultVCPUCount(sandbox.CPULimit, 1)),
		"-drive", "if=virtio,file=" + layout.rootDiskPath + ",format=qcow2",
		"-drive", "if=virtio,file=" + layout.workspaceDiskPath + ",format=raw",
	}
	args = append(args, r.networkArgs(sandbox.NetworkMode, sshPort)...)
	return args
}

func (r *Runtime) networkArgs(mode model.NetworkMode, sshPort int) []string {
	netdev := fmt.Sprintf("user,id=net0,hostfwd=tcp:127.0.0.1:%d-:22", sshPort)
	if mode == model.NetworkModeInternetDisabled {
		netdev = fmt.Sprintf("user,id=net0,restrict=on,hostfwd=tcp:127.0.0.1:%d-:22", sshPort)
	}
	return []string{
		"-netdev", netdev,
		"-device", r.networkDeviceModel() + ",netdev=net0",
	}
}

func (r *Runtime) networkDeviceModel() string {
	if strings.Contains(r.qemuBinary, "aarch64") {
		return "virtio-net-device"
	}
	return "virtio-net-pci"
}

func (r *Runtime) sshTarget(sandbox model.Sandbox, layout sandboxLayout) sshTarget {
	port, ok := sshPortFromRuntimeID(sandbox.RuntimeID)
	if !ok {
		port = sshPortForSandbox(sandbox.ID)
	}
	return sshTarget{
		port:           port,
		knownHostsPath: layout.knownHostsPath,
	}
}

func (r *Runtime) startTarget(sandbox model.Sandbox, layout sandboxLayout) (sshTarget, string, error) {
	port, ok := sshPortFromRuntimeID(sandbox.RuntimeID)
	if !ok || !isTCPPortAvailable(port) {
		var err error
		port, err = allocateSSHPort()
		if err != nil {
			return sshTarget{}, "", err
		}
	}
	runtimeID := runtimeIDWithSSHPort(sandbox.ID, port)
	return sshTarget{
		port:           port,
		knownHostsPath: layout.knownHostsPath,
	}, runtimeID, nil
}

func (r *Runtime) baseSSHArgs(target sshTarget, tty bool) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + target.knownHostsPath,
		"-o", "ConnectTimeout=5",
		"-i", r.sshKeyPath,
		"-p", strconv.Itoa(target.port),
	}
	if tty {
		args = append(args, "-tt")
	} else {
		args = append(args, "-T")
	}
	return append(args, r.sshUser+"@127.0.0.1")
}

func (r *Runtime) waitForReady(ctx context.Context, target sshTarget, serialLogPath string) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, r.effectiveBootTimeout())
	defer cancel()
	ticker := time.NewTicker(r.effectivePollInterval())
	defer ticker.Stop()
	var lastErr error
	for {
		if err := r.sshReady(timeoutCtx, target); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if reason, ok := bootFailureReason(serialLogPath); ok {
			return errors.New(reason)
		}
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("guest readiness timed out: %w", lastErr)
		case <-ticker.C:
		}
	}
}

func bootFailureReason(serialLogPath string) (string, bool) {
	if strings.TrimSpace(serialLogPath) == "" {
		return "", false
	}
	data, err := os.ReadFile(serialLogPath)
	if err != nil || len(data) == 0 {
		return "", false
	}
	if len(data) > serialTailLimit {
		data = data[len(data)-serialTailLimit:]
	}
	logTail := strings.ToLower(string(data))
	for _, marker := range bootFailureMarkers {
		if strings.Contains(logTail, marker) {
			return fmt.Sprintf("guest boot failed: %s", marker), true
		}
	}
	return "", false
}

func (r *Runtime) defaultRunCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (r *Runtime) defaultSSHProbe(ctx context.Context, target sshTarget) error {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + target.knownHostsPath,
		"-o", "ConnectTimeout=2",
		"-i", r.sshKeyPath,
		"-p", strconv.Itoa(target.port),
		r.sshUser + "@127.0.0.1",
		"sh", "-lc", "test -f " + shellQuote(readyMarkerPath),
	}
	_, err := r.runCommand(ctx, r.sshBinary, args...)
	return err
}

func (r *Runtime) guestBaseImage(specBaseImageRef string) string {
	if isReadableFile(specBaseImageRef) {
		return specBaseImageRef
	}
	return r.baseImagePath
}

func (r *Runtime) effectiveBootTimeout() time.Duration {
	if r.bootTimeout > 0 {
		return r.bootTimeout
	}
	return 2 * time.Minute
}

func (r *Runtime) effectivePollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return defaultPollInterval
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
		goos:          goruntime.GOOS,
		commandExists: requireCommand,
		fileReadable:  requireReadableFile,
		kvmAvailable:  requireKVM,
		hvfAvailable:  requireHVF,
	}
}

func validateHost(opts Options, qemuImgBinary, accel string, probe hostProbe) error {
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
	for _, command := range []string{opts.Binary, qemuImgBinary, opts.SSHBinary, opts.SCPBinary} {
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

func deriveQEMUImgBinary(qemuBinary string) string {
	if strings.TrimSpace(qemuBinary) == "" {
		return defaultQEMUImgBinary
	}
	base := filepath.Base(qemuBinary)
	if strings.HasPrefix(base, "qemu-system-") {
		candidate := strings.TrimSuffix(qemuBinary, base) + defaultQEMUImgBinary
		if candidate != "" {
			return candidate
		}
	}
	return defaultQEMUImgBinary
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
		serialLogPath:     filepath.Join(baseDir, ".runtime", "serial.log"),
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
	// Keep the operator model simple in the first pass: split requested disk
	// budget evenly between the writable system layer and persistent workspace.
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

func suspendedMarkerPath(layout sandboxLayout) string {
	return filepath.Join(layout.runtimeDir, "suspended")
}

func isSuspended(layout sandboxLayout) bool {
	_, err := os.Stat(suspendedMarkerPath(layout))
	return err == nil
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

func withinBootWindow(pidPath string, bootTimeout time.Duration) bool {
	if bootTimeout <= 0 {
		return false
	}
	info, err := os.Stat(pidPath)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) <= bootTimeout
}

func terminatePID(pid int, force bool) error {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	if err := syscall.Kill(pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	if force {
		return nil
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func waitForPID(path string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pid, err := readPID(path)
		if err == nil {
			return pid, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, fmt.Errorf("qemu pidfile %s did not appear before timeout", path)
}

func sshPortForSandbox(id string) int {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(id))
	return sshPortBase + int(hasher.Sum32()%sshPortSpan)
}

func qemuSize(sizeBytes int64) string {
	return strconv.FormatInt(sizeBytes, 10)
}

func runtimeIDWithSSHPort(sandboxID string, port int) string {
	return fmt.Sprintf("%s%s@%d", qemuRuntimePrefix, sandboxID, port)
}

func sshPortFromRuntimeID(runtimeID string) (int, bool) {
	suffix := strings.TrimPrefix(runtimeID, qemuRuntimePrefix)
	index := strings.LastIndex(suffix, "@")
	if index < 0 || index == len(suffix)-1 {
		return 0, false
	}
	port, err := strconv.Atoi(suffix[index+1:])
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}

func allocateSSHPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 {
		return 0, fmt.Errorf("allocate ssh port: unexpected listener address %T", listener.Addr())
	}
	return addr.Port, nil
}

func isTCPPortAvailable(port int) bool {
	if port <= 0 {
		return false
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

func sandboxWithRuntimeID(sandbox model.Sandbox, runtimeID string) model.Sandbox {
	sandbox.RuntimeID = runtimeID
	return sandbox
}

func allocatedPathSize(path string) (int64, error) {
	if strings.TrimSpace(path) == "" {
		return 0, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		return allocatedFileSize(info), nil
	}
	var total int64
	err = filepath.Walk(path, func(current string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		total += allocatedFileSize(info)
		return nil
	})
	return total, err
}

func allocatedFileSize(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Blocks > 0 {
		return stat.Blocks * 512
	}
	return info.Size()
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

func isReadableFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	return requireReadableFile(path) == nil
}

func defaultInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func defaultVCPUCount(value model.CPUQuantity, fallback int) int {
	if value > 0 {
		return value.VCPUCount()
	}
	return fallback
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
