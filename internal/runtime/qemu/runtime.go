package qemu

import (
	"context"
	"encoding/json"
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

	"or3-sandbox/internal/guestimage"
	"or3-sandbox/internal/model"
)

const (
	defaultSSHBinary      = "ssh"
	defaultSCPBinary      = "scp"
	defaultQEMUImgBinary  = "qemu-img"
	defaultAgentTransport = "virtio-serial"
	sshCompatTransport    = "ssh-port-forward"
	readyMarkerPath       = "/var/lib/or3/bootstrap.ready"
	readyProbeTimeout     = 15 * time.Second
	defaultPollInterval   = 500 * time.Millisecond
	qemuRuntimePrefix     = "qemu-"
	sshPortBase           = 22000
	sshPortSpan           = 20000
	serialTailLimit       = 64 * 1024
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

type processArgsReader func(pid int) (string, error)

// Options configures the QEMU runtime adapter.
type Options struct {
	Binary                        string
	Accel                         string
	BaseImagePath                 string
	ControlMode                   model.GuestControlMode
	SSHUser                       string
	SSHKeyPath                    string
	SSHHostKeyPath                string
	BootTimeout                   time.Duration
	SSHBinary                     string
	SCPBinary                     string
	WorkspaceFileTransferMaxBytes int64
}

// Runtime implements [model.RuntimeManager] using QEMU virtual machines.
type Runtime struct {
	qemuBinary                    string
	qemuImgBinary                 string
	sshBinary                     string
	scpBinary                     string
	accelerator                   string
	baseImagePath                 string
	controlMode                   model.GuestControlMode
	agentTransport                string
	sshUser                       string
	sshKeyPath                    string
	sshHostKeyPath                string
	bootTimeout                   time.Duration
	pollInterval                  time.Duration
	workspaceFileTransferMaxBytes int64

	runCommand  commandRunner
	sshReady    sshProbe
	processArgs processArgsReader
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
	scratchDir        string
	secretsDir        string
	runtimeDir        string
	rootDiskPath      string
	workspaceDiskPath string
	pidPath           string
	monitorPath       string
	agentSocketPath   string
	knownHostsPath    string
	serialLogPath     string
}

type sshTarget struct {
	port           int
	knownHostsPath string
	hostKeyAlias   string
}

// New validates host support and constructs a QEMU runtime adapter.
func New(opts Options) (*Runtime, error) {
	if strings.TrimSpace(opts.SSHBinary) == "" {
		opts.SSHBinary = defaultSSHBinary
	}
	if strings.TrimSpace(opts.SCPBinary) == "" {
		opts.SCPBinary = defaultSCPBinary
	}
	if !opts.ControlMode.IsValid() {
		opts.ControlMode = model.GuestControlModeAgent
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
		qemuBinary:                    opts.Binary,
		qemuImgBinary:                 qemuImgBinary,
		sshBinary:                     opts.SSHBinary,
		scpBinary:                     opts.SCPBinary,
		accelerator:                   accel,
		baseImagePath:                 opts.BaseImagePath,
		controlMode:                   opts.ControlMode,
		agentTransport:                defaultAgentTransport,
		sshUser:                       opts.SSHUser,
		sshKeyPath:                    opts.SSHKeyPath,
		sshHostKeyPath:                opts.SSHHostKeyPath,
		bootTimeout:                   opts.BootTimeout,
		pollInterval:                  defaultPollInterval,
		workspaceFileTransferMaxBytes: workspaceFileTransferLimit(opts.WorkspaceFileTransferMaxBytes),
	}
	runtime.runCommand = runtime.defaultRunCommand
	runtime.sshReady = runtime.defaultSSHProbe
	runtime.processArgs = defaultProcessArgsReader
	return runtime, nil
}

func workspaceFileTransferLimit(limit int64) int64 {
	if limit <= 0 {
		return model.DefaultWorkspaceFileTransferMaxBytes
	}
	if limit > model.MaxWorkspaceFileTransferCeilingBytes {
		return model.MaxWorkspaceFileTransferCeilingBytes
	}
	return limit
}

// Create prepares host-side state and boots the guest when required.
func (r *Runtime) Create(ctx context.Context, spec model.SandboxSpec) (model.RuntimeState, error) {
	layout := layoutForSpec(spec)
	if err := ensureLayout(layout); err != nil {
		return model.RuntimeState{}, err
	}
	if err := os.Remove(layout.pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return model.RuntimeState{}, err
	}
	baseImagePath, spec, err := r.guestBaseImage(spec)
	if err != nil {
		return model.RuntimeState{}, err
	}
	baseVirtualSizeBytes, err := r.baseImageVirtualSizeBytes(ctx, baseImagePath)
	if err != nil {
		return model.RuntimeState{}, err
	}
	rootBytes, workspaceBytes := splitDiskBytes(spec.DiskLimitMB, baseVirtualSizeBytes)
	if err := r.createRootDisk(ctx, baseImagePath, layout.rootDiskPath, rootBytes); err != nil {
		return model.RuntimeState{}, err
	}
	if err := r.createWorkspaceDisk(ctx, layout.workspaceDiskPath, workspaceBytes); err != nil {
		return model.RuntimeState{}, err
	}
	if r.controlModeForSpec(spec) == model.GuestControlModeSSHCompat {
		if err := r.seedKnownHosts(layout.knownHostsPath, sandboxHostKeyAlias(spec.SandboxID)); err != nil {
			return model.RuntimeState{}, err
		}
	}
	if err := touchFile(layout.serialLogPath); err != nil {
		return model.RuntimeState{}, err
	}
	return model.RuntimeState{
		RuntimeID:   qemuRuntimePrefix + spec.SandboxID,
		Status:      model.SandboxStatusStopped,
		Running:     false,
		ControlMode: r.controlModeForSpec(spec),
	}, nil
}

// Start boots an existing QEMU sandbox.
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
	if err := r.waitForReady(ctx, sandboxWithRuntimeID(sandbox, runtimeID), target, layout.serialLogPath); err != nil {
		_, _ = r.Stop(context.Background(), sandboxWithRuntimeID(sandbox, runtimeID), true)
		return model.RuntimeState{}, err
	}
	return r.Inspect(ctx, sandboxWithRuntimeID(sandbox, runtimeID))
}

// Stop shuts down a QEMU sandbox.
func (r *Runtime) Stop(ctx context.Context, sandbox model.Sandbox, force bool) (model.RuntimeState, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	pid, err := r.liveSandboxPID(layout)
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

// Suspend saves a QEMU sandbox into a suspended state.
func (r *Runtime) Suspend(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	pid, err := r.liveSandboxPID(layout)
	if errors.Is(err, os.ErrNotExist) {
		return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
	}
	if err != nil {
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

// Resume resumes a previously suspended QEMU sandbox.
func (r *Runtime) Resume(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	layout := layoutForSandbox(sandbox)
	pid, err := r.liveSandboxPID(layout)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(suspendedMarkerPath(layout))
		return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
	}
	if err != nil {
		return model.RuntimeState{}, err
	}
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		return model.RuntimeState{}, err
	}
	_ = os.Remove(suspendedMarkerPath(layout))
	target := r.sshTarget(sandbox, layout)
	if err := r.waitForReady(ctx, sandbox, target, layout.serialLogPath); err != nil {
		return model.RuntimeState{}, err
	}
	return r.Inspect(ctx, sandbox)
}

// Destroy tears down the guest process and related runtime state.
func (r *Runtime) Destroy(ctx context.Context, sandbox model.Sandbox) error {
	_, _ = r.Stop(ctx, sandbox, true)
	return os.RemoveAll(layoutForSandbox(sandbox).baseDir)
}

// Inspect probes the current guest state for sandbox.
func (r *Runtime) Inspect(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	layout := layoutForSandbox(sandbox)
	pid, err := r.liveSandboxPID(layout)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(suspendedMarkerPath(layout))
		return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
	}
	if err != nil {
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

	// For agent-mode sandboxes, use the serial log bootstrap marker as the
	// primary readiness indicator.  Probing over the QEMU virtio-serial
	// chardev socket is unreliable for periodic health checks because the
	// single-client socket cannot reconnect deterministically after the
	// host disconnects.  The agent socket is still used for actual
	// operations (exec, files, TTY, tunnels) where retry logic handles any
	// transient chardev disruptions.
	transport, transportErr := r.controlTransportForSandbox(sandbox)
	if transportErr == nil && transport.mode == model.GuestControlModeAgent {
		if reason, ok := bootFailureReason(layout.serialLogPath); ok {
			return model.RuntimeState{
				RuntimeID:   sandbox.RuntimeID,
				Status:      model.SandboxStatusError,
				Running:     false,
				Pid:         pid,
				ControlMode: r.controlModeForSandbox(sandbox),
				Error:       reason,
			}, nil
		}
		if serialLogShowsReady(layout.serialLogPath) {
			return model.RuntimeState{
				RuntimeID:   sandbox.RuntimeID,
				Status:      model.SandboxStatusRunning,
				Running:     true,
				Pid:         pid,
				IPAddress:   "127.0.0.1",
				ControlMode: r.controlModeForSandbox(sandbox),
			}, nil
		}
		if withinBootWindow(layout.pidPath, r.effectiveBootTimeout()) {
			return model.RuntimeState{
				RuntimeID:   sandbox.RuntimeID,
				Status:      model.SandboxStatusBooting,
				Running:     false,
				Pid:         pid,
				ControlMode: r.controlModeForSandbox(sandbox),
				Error:       "guest is still booting",
			}, nil
		}
		return model.RuntimeState{
			RuntimeID:   sandbox.RuntimeID,
			Status:      model.SandboxStatusDegraded,
			Running:     false,
			Pid:         pid,
			ControlMode: r.controlModeForSandbox(sandbox),
			Error:       "guest process is alive but bootstrap marker not found",
		}, nil
	}

	// SSH-compat and fallback: probe via SSH or agent socket.
	target := r.sshTarget(sandbox, layout)
	probeCtx, cancel := context.WithTimeout(ctx, readyProbeTimeout)
	defer cancel()
	if err := r.probeReady(probeCtx, sandbox, layout, target); err != nil {
		if reason, ok := bootFailureReason(layout.serialLogPath); ok {
			return model.RuntimeState{
				RuntimeID:   sandbox.RuntimeID,
				Status:      model.SandboxStatusError,
				Running:     false,
				Pid:         pid,
				ControlMode: r.controlModeForSandbox(sandbox),
				Error:       reason,
			}, nil
		}
		if withinBootWindow(layout.pidPath, r.effectiveBootTimeout()) {
			return model.RuntimeState{
				RuntimeID:   sandbox.RuntimeID,
				Status:      model.SandboxStatusBooting,
				Running:     false,
				Pid:         pid,
				ControlMode: r.controlModeForSandbox(sandbox),
				Error:       fmt.Sprintf("guest is still booting: %v", err),
			}, nil
		}
		return model.RuntimeState{
			RuntimeID:   sandbox.RuntimeID,
			Status:      model.SandboxStatusDegraded,
			Running:     false,
			Pid:         pid,
			ControlMode: r.controlModeForSandbox(sandbox),
			Error:       fmt.Sprintf("guest process is alive but not ready: %v", err),
		}, nil
	}
	return model.RuntimeState{
		RuntimeID:   sandbox.RuntimeID,
		Status:      model.SandboxStatusRunning,
		Running:     true,
		Pid:         pid,
		IPAddress:   "127.0.0.1",
		ControlMode: r.controlModeForSandbox(sandbox),
	}, nil
}

// CreateSnapshot exports a snapshot artifact for sandbox.
func (r *Runtime) CreateSnapshot(ctx context.Context, sandbox model.Sandbox, snapshotID string) (model.SnapshotInfo, error) {
	state, err := r.Inspect(ctx, sandbox)
	if err != nil {
		return model.SnapshotInfo{}, err
	}
	if state.Status != model.SandboxStatusStopped {
		return model.SnapshotInfo{}, fmt.Errorf("qemu snapshots require the sandbox to be stopped")
	}
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

// RestoreSnapshot restores sandbox from snapshot.
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

type qemuImageInfo struct {
	VirtualSize int64 `json:"virtual-size"`
}

func (r *Runtime) baseImageVirtualSizeBytes(ctx context.Context, imagePath string) (int64, error) {
	if strings.TrimSpace(imagePath) == "" || strings.TrimSpace(r.qemuImgBinary) == "" || r.runCommand == nil {
		return 0, nil
	}
	output, err := r.runCommand(ctx, r.qemuImgBinary, "info", "--output=json", imagePath)
	if err != nil {
		return 0, fmt.Errorf("inspect qemu base image %q: %w", imagePath, err)
	}
	var info qemuImageInfo
	if err := json.Unmarshal(output, &info); err != nil {
		return 0, fmt.Errorf("parse qemu image info for %q: %w", imagePath, err)
	}
	if info.VirtualSize <= 0 {
		return 0, fmt.Errorf("qemu image %q reported non-positive virtual size %d", imagePath, info.VirtualSize)
	}
	return info.VirtualSize, nil
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
		"-device", "virtio-serial",
		"-chardev", "socket,id=agent0,path=" + layout.agentSocketPath + ",server=on,wait=off",
		"-device", "virtserialport,chardev=agent0,name=org.or3.guest_agent",
		"-drive", "if=virtio,file=" + layout.rootDiskPath + ",format=qcow2",
		"-drive", "if=virtio,file=" + layout.workspaceDiskPath + ",format=raw",
	}
	args = append(args, r.networkArgs(sandbox, sshPort)...)
	return args
}

func (r *Runtime) networkArgs(sandbox model.Sandbox, sshPort int) []string {
	netdev := "user,id=net0"
	if sandbox.NetworkMode == model.NetworkModeInternetDisabled {
		netdev = "user,id=net0,restrict=on"
	}
	transport, err := r.controlTransportForSandbox(sandbox)
	if err == nil && transport.mode == model.GuestControlModeSSHCompat {
		hostfwd := fmt.Sprintf(",hostfwd=tcp:127.0.0.1:%d-:22", sshPort)
		netdev += hostfwd
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
		hostKeyAlias:   sandboxHostKeyAlias(sandbox.ID),
	}
}

func (r *Runtime) startTarget(sandbox model.Sandbox, layout sandboxLayout) (sshTarget, string, error) {
	transport, err := r.controlTransportForSandbox(sandbox)
	if err != nil {
		return sshTarget{}, "", err
	}
	if transport.mode != model.GuestControlModeSSHCompat {
		return sshTarget{}, qemuRuntimePrefix + sandbox.ID, nil
	}
	port, ok := sshPortFromRuntimeID(sandbox.RuntimeID)
	if !ok || !isTCPPortAvailable(port) {
		port, err = allocateSSHPort()
		if err != nil {
			return sshTarget{}, "", err
		}
	}
	runtimeID := runtimeIDWithSSHPort(sandbox.ID, port)
	return sshTarget{
		port:           port,
		knownHostsPath: layout.knownHostsPath,
		hostKeyAlias:   sandboxHostKeyAlias(sandbox.ID),
	}, runtimeID, nil
}

func (r *Runtime) baseSSHArgs(target sshTarget, tty bool) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + target.knownHostsPath,
		"-o", "HostKeyAlias=" + target.hostKeyAlias,
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

func (r *Runtime) waitForReady(ctx context.Context, sandbox model.Sandbox, target sshTarget, serialLogPath string) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, r.effectiveBootTimeout())
	defer cancel()
	ticker := time.NewTicker(r.effectivePollInterval())
	defer ticker.Stop()
	var lastErr error
	for {
		if err := r.probeReady(timeoutCtx, sandbox, layoutForSandbox(sandbox), target); err == nil {
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
	data, err := readFileTail(serialLogPath, serialTailLimit)
	if err != nil || len(data) == 0 {
		return "", false
	}
	logTail := strings.ToLower(string(data))
	for _, marker := range bootFailureMarkers {
		if strings.Contains(logTail, marker) {
			return fmt.Sprintf("guest boot failed: %s", marker), true
		}
	}
	return "", false
}

// serialLogShowsReady returns true when the guest serial log contains the
// bootstrap ready marker, indicating the guest has fully booted and the agent
// service has started.
func serialLogShowsReady(serialLogPath string) bool {
	if strings.TrimSpace(serialLogPath) == "" {
		return false
	}
	data, err := readFileTail(serialLogPath, serialTailLimit)
	if err != nil || len(data) == 0 {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), "or3-bootstrap: ready")
}

func readFileTail(path string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	offset := int64(0)
	if size > limit {
		offset = size - limit
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
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
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + target.knownHostsPath,
		"-o", "HostKeyAlias=" + target.hostKeyAlias,
		"-o", "ConnectTimeout=2",
		"-i", r.sshKeyPath,
		"-p", strconv.Itoa(target.port),
		r.sshUser + "@127.0.0.1",
		"sh", "-lc", "test -f " + shellQuote(readyMarkerPath),
	}
	_, err := r.runCommand(ctx, r.sshBinary, args...)
	return err
}

func (r *Runtime) guestBaseImage(spec model.SandboxSpec) (string, model.SandboxSpec, error) {
	path := strings.TrimSpace(spec.BaseImageRef)
	if path == "" {
		path = r.baseImagePath
	}
	if !isReadableFile(path) {
		return "", model.SandboxSpec{}, fmt.Errorf("qemu base image path %q is not readable", path)
	}
	path = filepath.Clean(path)
	contract, err := guestimage.Load(path)
	if err != nil {
		return "", model.SandboxSpec{}, err
	}
	if err := guestimage.Validate(path, contract); err != nil {
		return "", model.SandboxSpec{}, err
	}
	if spec.Profile == "" {
		spec.Profile = contract.Profile
	}
	if !spec.ControlMode.IsValid() {
		spec.ControlMode = contract.Control.Mode
	}
	if strings.TrimSpace(spec.ControlProtocolVersion) == "" {
		spec.ControlProtocolVersion = contract.Control.ProtocolVersion
	}
	if strings.TrimSpace(spec.WorkspaceContractVersion) == "" {
		spec.WorkspaceContractVersion = contract.WorkspaceContractVersion
	}
	if strings.TrimSpace(spec.ImageContractVersion) == "" {
		spec.ImageContractVersion = contract.ContractVersion
	}
	if spec.Profile != "" && contract.Profile != spec.Profile {
		return "", model.SandboxSpec{}, fmt.Errorf("guest image profile %q does not match sandbox profile %q", contract.Profile, spec.Profile)
	}
	if spec.ControlMode.IsValid() && contract.Control.Mode != spec.ControlMode {
		return "", model.SandboxSpec{}, fmt.Errorf("guest image control mode %q does not match sandbox control mode %q", contract.Control.Mode, spec.ControlMode)
	}
	transport, err := r.controlTransportForSpec(spec)
	if err != nil {
		return "", model.SandboxSpec{}, err
	}
	if contract.Control.Mode == model.GuestControlModeAgent && len(contract.Control.SupportedTransports) > 0 {
		supported := false
		for _, candidate := range contract.Control.SupportedTransports {
			if strings.EqualFold(strings.TrimSpace(candidate), transport.name) {
				supported = true
				break
			}
		}
		if !supported {
			return "", model.SandboxSpec{}, fmt.Errorf("guest image does not support runtime agent transport %q", transport.name)
		}
	}
	if strings.TrimSpace(spec.ControlProtocolVersion) != "" && contract.Control.ProtocolVersion != spec.ControlProtocolVersion {
		return "", model.SandboxSpec{}, fmt.Errorf("guest image control protocol %q does not match sandbox control protocol %q", contract.Control.ProtocolVersion, spec.ControlProtocolVersion)
	}
	if strings.TrimSpace(spec.WorkspaceContractVersion) != "" && contract.WorkspaceContractVersion != spec.WorkspaceContractVersion {
		return "", model.SandboxSpec{}, fmt.Errorf("guest image workspace contract version %q does not match sandbox workspace contract version %q", contract.WorkspaceContractVersion, spec.WorkspaceContractVersion)
	}
	if strings.TrimSpace(spec.ImageContractVersion) != "" && contract.ContractVersion != spec.ImageContractVersion {
		return "", model.SandboxSpec{}, fmt.Errorf("guest image contract version %q does not match sandbox contract version %q", contract.ContractVersion, spec.ImageContractVersion)
	}
	return path, spec, nil
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
	if !opts.ControlMode.IsValid() {
		opts.ControlMode = model.GuestControlModeAgent
	}
	if opts.ControlMode == model.GuestControlModeSSHCompat {
		if strings.TrimSpace(opts.SSHUser) == "" {
			return errors.New("qemu ssh user is required")
		}
		if strings.TrimSpace(opts.SSHKeyPath) == "" {
			return errors.New("qemu ssh key path is required")
		}
		if strings.TrimSpace(opts.SSHHostKeyPath) == "" {
			return errors.New("qemu ssh host key path is required")
		}
	}
	if opts.BootTimeout <= 0 {
		return errors.New("qemu boot timeout must be positive")
	}
	requiredCommands := []string{opts.Binary, qemuImgBinary, "ps"}
	if opts.ControlMode == model.GuestControlModeSSHCompat {
		requiredCommands = append(requiredCommands, opts.SSHBinary, opts.SCPBinary)
	}
	for _, command := range requiredCommands {
		if err := probe.commandExists(command); err != nil {
			return fmt.Errorf("required host command %q is unavailable: %w", command, err)
		}
	}
	requiredFiles := []string{opts.BaseImagePath}
	if opts.ControlMode == model.GuestControlModeSSHCompat {
		requiredFiles = append(requiredFiles, opts.SSHKeyPath, opts.SSHHostKeyPath)
	}
	for _, path := range requiredFiles {
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

func sandboxHostKeyAlias(sandboxID string) string {
	return "or3-qemu-" + sandboxID
}

func (r *Runtime) seedKnownHosts(path, alias string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("known hosts path is required")
	}
	keyData, err := os.ReadFile(r.sshHostKeyPath)
	if err != nil {
		return err
	}
	entry, err := knownHostEntry(alias, string(keyData))
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(entry+"\n"), 0o600)
}

func knownHostEntry(alias, key string) (string, error) {
	trimmedAlias := strings.TrimSpace(alias)
	if trimmedAlias == "" {
		return "", errors.New("known hosts alias is required")
	}
	fields := strings.Fields(strings.TrimSpace(key))
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "ssh-") {
		return "", fmt.Errorf("invalid ssh host public key format")
	}
	entry := trimmedAlias + " " + fields[0] + " " + fields[1]
	if len(fields) > 2 {
		entry += " " + strings.Join(fields[2:], " ")
	}
	return entry, nil
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
		scratchDir:        spec.ScratchRoot,
		secretsDir:        spec.SecretsRoot,
		runtimeDir:        filepath.Join(baseDir, ".runtime"),
		rootDiskPath:      filepath.Join(spec.StorageRoot, "overlay.qcow2"),
		workspaceDiskPath: filepath.Join(spec.WorkspaceRoot, "workspace.img"),
		pidPath:           filepath.Join(baseDir, ".runtime", "qemu.pid"),
		monitorPath:       filepath.Join(baseDir, ".runtime", "monitor.sock"),
		agentSocketPath:   filepath.Join(baseDir, ".runtime", "agent.sock"),
		knownHostsPath:    filepath.Join(baseDir, ".runtime", "ssh-known-hosts"),
		serialLogPath:     filepath.Join(baseDir, ".runtime", "serial.log"),
	}
}

func layoutForSandbox(sandbox model.Sandbox) sandboxLayout {
	return layoutForSpec(model.SandboxSpec{
		SandboxID:     sandbox.ID,
		ControlMode:   sandbox.ControlMode,
		StorageRoot:   sandbox.StorageRoot,
		WorkspaceRoot: sandbox.WorkspaceRoot,
		CacheRoot:     sandbox.CacheRoot,
		ScratchRoot:   filepath.Join(filepath.Dir(sandbox.StorageRoot), "scratch"),
		SecretsRoot:   filepath.Join(filepath.Dir(sandbox.StorageRoot), "secrets"),
	})
}

func (r *Runtime) controlModeForSandbox(sandbox model.Sandbox) model.GuestControlMode {
	if sandbox.ControlMode.IsValid() {
		return sandbox.ControlMode
	}
	if path := strings.TrimSpace(sandbox.BaseImageRef); path != "" {
		if contract, err := guestimage.Load(path); err == nil && contract.Control.Mode.IsValid() {
			return contract.Control.Mode
		}
	}
	if r.controlMode.IsValid() {
		return r.controlMode
	}
	return model.GuestControlModeAgent
}

func (r *Runtime) controlModeForSpec(spec model.SandboxSpec) model.GuestControlMode {
	if spec.ControlMode.IsValid() {
		return spec.ControlMode
	}
	if r.controlMode.IsValid() {
		return r.controlMode
	}
	return model.GuestControlModeAgent
}

type controlTransport struct {
	mode model.GuestControlMode
	name string
}

func (r *Runtime) controlTransportForSandbox(sandbox model.Sandbox) (controlTransport, error) {
	return r.controlTransport(r.controlModeForSandbox(sandbox))
}

func (r *Runtime) controlTransportForSpec(spec model.SandboxSpec) (controlTransport, error) {
	return r.controlTransport(r.controlModeForSpec(spec))
}

func (r *Runtime) controlTransport(mode model.GuestControlMode) (controlTransport, error) {
	switch mode {
	case model.GuestControlModeAgent:
		transport := strings.TrimSpace(r.agentTransport)
		if transport == "" {
			transport = defaultAgentTransport
		}
		return controlTransport{mode: mode, name: transport}, nil
	case model.GuestControlModeSSHCompat:
		return controlTransport{mode: mode, name: sshCompatTransport}, nil
	default:
		return controlTransport{}, fmt.Errorf("unsupported control mode %q", mode)
	}
}

func (r *Runtime) probeReady(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout, target sshTarget) error {
	transport, err := r.controlTransportForSandbox(sandbox)
	if err != nil {
		return err
	}
	if transport.mode == model.GuestControlModeAgent {
		return r.agentProbeReadySingleConn(ctx, layout, sandbox)
	}
	return r.sshReady(ctx, target)
}

func ensureLayout(layout sandboxLayout) error {
	for _, dir := range []string{layout.rootfsDir, layout.workspaceDir, layout.cacheDir, layout.scratchDir, layout.secretsDir, layout.runtimeDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func splitDiskBytes(totalMB int, minimumRootBytes int64) (int64, int64) {
	totalBytes := int64(totalMB) * 1024 * 1024
	if totalBytes <= 0 {
		return 0, 0
	}
	// Keep the operator model simple in the first pass: split requested disk
	// budget evenly between the writable system layer and persistent workspace.
	// Never size the writable system disk smaller than the guest image it layers
	// on top of, or the guest will fail to find its root filesystem at boot.
	root := totalBytes / 2
	workspace := totalBytes - root
	if minimumRootBytes > root {
		root = minimumRootBytes
	}
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

func (r *Runtime) liveSandboxPID(layout sandboxLayout) (int, error) {
	pid, err := readPID(layout.pidPath)
	if err != nil {
		return 0, err
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			_ = os.Remove(layout.pidPath)
			_ = os.Remove(suspendedMarkerPath(layout))
			return 0, os.ErrNotExist
		}
		return 0, err
	}
	match, err := r.processMatchesSandbox(pid, layout)
	if err != nil {
		return 0, err
	}
	if !match {
		_ = os.Remove(layout.pidPath)
		_ = os.Remove(suspendedMarkerPath(layout))
		return 0, os.ErrNotExist
	}
	return pid, nil
}

func (r *Runtime) processMatchesSandbox(pid int, layout sandboxLayout) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	if strings.TrimSpace(r.qemuBinary) == "" || r.processArgs == nil {
		return true, nil
	}
	args, err := r.processArgs(pid)
	if err != nil {
		return false, err
	}
	expected := []string{
		filepath.Base(r.qemuBinary),
		layout.rootDiskPath,
		layout.monitorPath,
	}
	for _, needle := range expected {
		if strings.TrimSpace(needle) == "" {
			continue
		}
		if !strings.Contains(args, needle) {
			return false, nil
		}
	}
	return true, nil
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

func defaultProcessArgsReader(pid int) (string, error) {
	output, err := exec.Command("ps", "-ww", "-o", "args=", "-p", strconv.Itoa(pid)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("inspect process %d: %w: %s", pid, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
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
	bytes, _, err := allocatedPathUsage(path)
	return bytes, err
}

func allocatedPathUsage(path string) (int64, int64, error) {
	if strings.TrimSpace(path) == "" {
		return 0, 0, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	if !info.IsDir() {
		return allocatedFileSize(info), 1, nil
	}
	var total int64
	var entries int64
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
		entries++
		total += allocatedFileSize(info)
		return nil
	})
	return total, entries, err
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
