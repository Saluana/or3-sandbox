package docker

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"or3-sandbox/internal/archiveutil"
	"or3-sandbox/internal/model"
)

const previewLimit = 64 * 1024

const (
	defaultUser                    = "10001:10001"
	defaultTmpfsSizeMB             = 64
	dockerCapabilityElevatedUser   = "docker.elevated-user"
	dockerCapabilityExtraCapPrefix = "docker.extra-cap:"
)

// Options configures the Docker runtime adapter.
type Options struct {
	Binary                    string
	HostOS                    string
	User                      string
	TmpfsSizeMB               int
	SeccompProfile            string
	AppArmorProfile           string
	SELinuxLabel              string
	AllowDangerousOverrides   bool
	SnapshotMaxBytes          int64
	SnapshotMaxFiles          int
	SnapshotMaxExpansionRatio int
}

// Runtime implements [model.RuntimeManager] using the Docker CLI.
type Runtime struct {
	binary                  string
	hostOS                  string
	user                    string
	tmpfsSizeMB             int
	seccompProfile          string
	appArmorProfile         string
	selinuxLabel            string
	allowDangerousOverrides bool
	restoreLimits           archiveutil.Limits
}

func defaultRestoreLimits() archiveutil.Limits {
	return archiveutil.Limits{
		MaxBytes:          256 * 1024 * 1024,
		MaxFiles:          4096,
		MaxExpansionRatio: 32,
	}
}

// New constructs a Docker runtime adapter.
func New(options ...Options) *Runtime {
	resolved := Options{
		Binary:      "docker",
		HostOS:      goruntime.GOOS,
		User:        defaultUser,
		TmpfsSizeMB: defaultTmpfsSizeMB,
	}
	if len(options) > 0 {
		if strings.TrimSpace(options[0].Binary) != "" {
			resolved.Binary = options[0].Binary
		}
		if strings.TrimSpace(options[0].HostOS) != "" {
			resolved.HostOS = options[0].HostOS
		}
		if strings.TrimSpace(options[0].User) != "" {
			resolved.User = options[0].User
		}
		if options[0].TmpfsSizeMB > 0 {
			resolved.TmpfsSizeMB = options[0].TmpfsSizeMB
		}
		resolved.SeccompProfile = strings.TrimSpace(options[0].SeccompProfile)
		resolved.AppArmorProfile = strings.TrimSpace(options[0].AppArmorProfile)
		resolved.SELinuxLabel = strings.TrimSpace(options[0].SELinuxLabel)
		resolved.AllowDangerousOverrides = options[0].AllowDangerousOverrides
	}
	limits := defaultRestoreLimits()
	if len(options) > 0 {
		if options[0].SnapshotMaxBytes > 0 {
			limits.MaxBytes = options[0].SnapshotMaxBytes
		}
		if options[0].SnapshotMaxFiles > 0 {
			limits.MaxFiles = options[0].SnapshotMaxFiles
		}
		if options[0].SnapshotMaxExpansionRatio > 0 {
			limits.MaxExpansionRatio = options[0].SnapshotMaxExpansionRatio
		}
	}
	return &Runtime{
		binary:                  resolved.Binary,
		hostOS:                  resolved.HostOS,
		user:                    resolved.User,
		tmpfsSizeMB:             resolved.TmpfsSizeMB,
		seccompProfile:          resolved.SeccompProfile,
		appArmorProfile:         resolved.AppArmorProfile,
		selinuxLabel:            resolved.SELinuxLabel,
		allowDangerousOverrides: resolved.AllowDangerousOverrides,
		restoreLimits:           limits,
	}
}

// NewWithBinary constructs a Docker runtime adapter that shells out through
// binary.
func NewWithBinary(binary string) *Runtime {
	return New(Options{Binary: binary})
}

// Create creates the container and its backing directories without starting it.
func (r *Runtime) Create(ctx context.Context, spec model.SandboxSpec) (model.RuntimeState, error) {
	if err := os.MkdirAll(spec.StorageRoot, 0o755); err != nil {
		return model.RuntimeState{}, err
	}
	if err := os.MkdirAll(spec.WorkspaceRoot, 0o755); err != nil {
		return model.RuntimeState{}, err
	}
	if spec.CacheRoot != "" {
		if err := os.MkdirAll(spec.CacheRoot, 0o755); err != nil {
			return model.RuntimeState{}, err
		}
	}
	if spec.ScratchRoot != "" {
		if err := os.MkdirAll(spec.ScratchRoot, 0o755); err != nil {
			return model.RuntimeState{}, err
		}
	}
	if spec.SecretsRoot != "" {
		if err := os.MkdirAll(spec.SecretsRoot, 0o755); err != nil {
			return model.RuntimeState{}, err
		}
	}
	if spec.NetworkMode == model.NetworkModeInternetEnabled {
		if err := r.ensureNetwork(ctx, spec.SandboxID); err != nil {
			return model.RuntimeState{}, err
		}
	}
	workspaceMount, err := absoluteHostPath(spec.WorkspaceRoot)
	if err != nil {
		return model.RuntimeState{}, err
	}
	security, warnings, err := r.resolveSecurityOptions(spec)
	if err != nil {
		return model.RuntimeState{}, err
	}
	for _, warning := range warnings {
		slog.Warn("docker runtime hardening warning", "runtime", "docker", "sandbox_id", spec.SandboxID, "detail", warning)
	}
	args := []string{
		"create",
		"--name", containerName(spec.SandboxID),
		"--hostname", hostname(spec.SandboxID),
		"--init",
		"--label", "or3.sandbox.id=" + spec.SandboxID,
		"--label", "or3.tenant.id=" + spec.TenantID,
		"--cpus", spec.CPULimit.String(),
		"--memory", fmt.Sprintf("%dm", spec.MemoryLimitMB),
		"--pids-limit", strconv.Itoa(spec.PIDsLimit),
		"--user", security.User,
		"--cap-drop", "ALL",
		"--read-only",
		"--tmpfs", security.TmpfsMount,
		"--security-opt", "no-new-privileges:true",
		"-v", fmt.Sprintf("%s:/workspace", workspaceMount),
	}
	for _, opt := range security.SecurityOpts {
		args = append(args, "--security-opt", opt)
	}
	for _, capAdd := range security.CapAdd {
		args = append(args, "--cap-add", capAdd)
	}
	if spec.CacheRoot != "" {
		cacheMount, err := absoluteHostPath(spec.CacheRoot)
		if err != nil {
			return model.RuntimeState{}, err
		}
		args = append(args, "-v", fmt.Sprintf("%s:/cache", cacheMount))
	}
	if spec.ScratchRoot != "" {
		scratchMount, err := absoluteHostPath(spec.ScratchRoot)
		if err != nil {
			return model.RuntimeState{}, err
		}
		args = append(args, "-v", fmt.Sprintf("%s:/scratch", scratchMount))
	}
	if spec.SecretsRoot != "" {
		secretsMount, err := absoluteHostPath(spec.SecretsRoot)
		if err != nil {
			return model.RuntimeState{}, err
		}
		args = append(args, "-v", fmt.Sprintf("%s:/secrets:ro", secretsMount))
	}
	switch spec.NetworkMode {
	case model.NetworkModeInternetEnabled:
		args = append(args, "--network", networkName(spec.SandboxID))
	case model.NetworkModeInternetDisabled:
		args = append(args, "--network", "none")
	default:
		return model.RuntimeState{}, fmt.Errorf("unsupported network mode %q", spec.NetworkMode)
	}
	withStorageOpt := r.hostOS == "linux" && spec.DiskLimitMB > 0
	storageOptArgs := append([]string(nil), args...)
	if withStorageOpt {
		storageOptArgs = append(storageOptArgs, "--storage-opt", fmt.Sprintf("size=%dm", spec.DiskLimitMB))
	}
	args = append(args, spec.BaseImageRef, "sleep", "infinity")
	storageOptArgs = append(storageOptArgs, spec.BaseImageRef, "sleep", "infinity")
	out, err := r.run(ctx, storageOptArgs...)
	if err != nil && withStorageOpt && dockerStorageOptUnsupported(err) {
		slog.Warn("docker storage-opt unsupported; retrying without disk quota", "runtime", "docker", "sandbox_id", spec.SandboxID, "disk_limit_mb", spec.DiskLimitMB, "error", err)
		_, _ = r.run(ctx, "rm", "-f", containerName(spec.SandboxID))
		out, err = r.run(ctx, args...)
	}
	if err != nil {
		return model.RuntimeState{}, err
	}
	return model.RuntimeState{
		RuntimeID: strings.TrimSpace(out),
		Status:    model.SandboxStatusStopped,
		Running:   false,
	}, nil
}

func absoluteHostPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("host path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

type dockerSecurityOptions struct {
	User         string
	TmpfsMount   string
	SecurityOpts []string
	CapAdd       []string
}

func (r *Runtime) resolveSecurityOptions(spec model.SandboxSpec) (dockerSecurityOptions, []string, error) {
	options := dockerSecurityOptions{
		User:       r.user,
		TmpfsMount: fmt.Sprintf("/tmp:rw,nosuid,nodev,noexec,size=%dm", r.tmpfsSizeMB),
	}
	var warnings []string
	if r.hostOS == "linux" {
		if r.seccompProfile != "" {
			options.SecurityOpts = append(options.SecurityOpts, "seccomp="+r.seccompProfile)
		}
		if r.appArmorProfile != "" {
			options.SecurityOpts = append(options.SecurityOpts, "apparmor="+r.appArmorProfile)
		}
		if r.selinuxLabel != "" {
			options.SecurityOpts = append(options.SecurityOpts, "label="+r.selinuxLabel)
		}
	} else {
		if r.seccompProfile != "" {
			warnings = append(warnings, fmt.Sprintf("seccomp profile %q requested but host OS %q cannot enforce Linux seccomp here", r.seccompProfile, r.hostOS))
		}
		if r.appArmorProfile != "" {
			warnings = append(warnings, fmt.Sprintf("AppArmor profile %q requested but host OS %q cannot enforce Linux AppArmor here", r.appArmorProfile, r.hostOS))
		}
		if r.selinuxLabel != "" {
			warnings = append(warnings, fmt.Sprintf("SELinux label %q requested but host OS %q cannot enforce Linux SELinux here", r.selinuxLabel, r.hostOS))
		}
	}
	for _, capability := range spec.Capabilities {
		switch {
		case capability == dockerCapabilityElevatedUser:
			if !r.allowDangerousOverrides {
				return dockerSecurityOptions{}, warnings, fmt.Errorf("docker capability %q requires dangerous override support", capability)
			}
			options.User = "0:0"
		case strings.HasPrefix(capability, dockerCapabilityExtraCapPrefix):
			if !r.allowDangerousOverrides {
				return dockerSecurityOptions{}, warnings, fmt.Errorf("docker capability %q requires dangerous override support", capability)
			}
			name := normalizeDockerLinuxCapability(strings.TrimPrefix(capability, dockerCapabilityExtraCapPrefix))
			if name == "" {
				return dockerSecurityOptions{}, warnings, fmt.Errorf("docker capability %q is invalid", capability)
			}
			options.CapAdd = append(options.CapAdd, name)
		default:
			return dockerSecurityOptions{}, warnings, fmt.Errorf("docker capability %q is unsupported", capability)
		}
	}
	return options, warnings, nil
}

func normalizeDockerLinuxCapability(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ToUpper(strings.ReplaceAll(value, "-", "_"))
	for _, r := range value {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return ""
		}
	}
	return value
}

// Start starts an existing Docker-backed sandbox.
func (r *Runtime) Start(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	if _, err := r.run(ctx, "start", containerName(sandbox.ID)); err != nil {
		return model.RuntimeState{}, err
	}
	return r.Inspect(ctx, sandbox)
}

// Stop stops a running Docker-backed sandbox.
func (r *Runtime) Stop(ctx context.Context, sandbox model.Sandbox, force bool) (model.RuntimeState, error) {
	args := []string{"stop"}
	if force {
		args = []string{"kill"}
	}
	args = append(args, containerName(sandbox.ID))
	if _, err := r.run(ctx, args...); err != nil && !isNoSuchContainer(err) {
		return model.RuntimeState{}, err
	}
	return r.Inspect(ctx, sandbox)
}

// Suspend pauses a running Docker-backed sandbox.
func (r *Runtime) Suspend(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	if _, err := r.run(ctx, "pause", containerName(sandbox.ID)); err != nil {
		return model.RuntimeState{}, err
	}
	state, err := r.Inspect(ctx, sandbox)
	if err != nil {
		return model.RuntimeState{}, err
	}
	state.Status = model.SandboxStatusSuspended
	state.Running = false
	return state, nil
}

// Resume unpauses a suspended Docker-backed sandbox.
func (r *Runtime) Resume(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	if _, err := r.run(ctx, "unpause", containerName(sandbox.ID)); err != nil {
		return model.RuntimeState{}, err
	}
	return r.Inspect(ctx, sandbox)
}

// Destroy removes the container and related runtime state for sandbox.
func (r *Runtime) Destroy(ctx context.Context, sandbox model.Sandbox) error {
	if _, err := r.run(ctx, "rm", "-f", containerName(sandbox.ID)); err != nil && !isNoSuchContainer(err) {
		return err
	}
	if sandbox.NetworkMode == model.NetworkModeInternetEnabled {
		if _, err := r.run(ctx, "network", "rm", networkName(sandbox.ID)); err != nil && !isNoSuchNetwork(err) {
			return err
		}
	}
	baseDir := filepath.Dir(sandbox.StorageRoot)
	for _, dir := range []string{sandbox.WorkspaceRoot, sandbox.CacheRoot, sandbox.StorageRoot, filepath.Join(baseDir, "scratch"), filepath.Join(baseDir, "secrets")} {
		if dir == "" {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

// Inspect returns the current runtime state for sandbox.
func (r *Runtime) Inspect(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	out, err := r.run(ctx, "inspect", containerName(sandbox.ID))
	if err != nil {
		if isNoSuchContainer(err) {
			return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusDeleted}, nil
		}
		return model.RuntimeState{}, err
	}
	var payload []inspectPayload
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return model.RuntimeState{}, err
	}
	if len(payload) == 0 {
		return model.RuntimeState{}, errors.New("docker inspect returned no payload")
	}
	state := payload[0]
	result := model.RuntimeState{
		RuntimeID: state.ID,
		Pid:       state.State.Pid,
		IPAddress: state.NetworkSettings.IPAddress,
		Error:     state.State.Error,
	}
	switch {
	case state.State.Running:
		result.Status = model.SandboxStatusRunning
		result.Running = true
	case state.State.Paused:
		result.Status = model.SandboxStatusSuspended
	case state.State.Status == "created" || state.State.Status == "exited":
		result.Status = model.SandboxStatusStopped
	case state.State.Status == "removing":
		result.Status = model.SandboxStatusDeleting
	default:
		result.Status = model.SandboxStatusError
	}
	if state.State.StartedAt != "" && !strings.HasPrefix(state.State.StartedAt, "0001-") {
		t, err := time.Parse(time.RFC3339Nano, state.State.StartedAt)
		if err == nil {
			result.StartedAt = &t
		}
	}
	if result.IPAddress == "" {
		for _, network := range state.NetworkSettings.Networks {
			if network.IPAddress != "" {
				result.IPAddress = network.IPAddress
				break
			}
		}
	}
	return result, nil
}

// Exec runs a command inside sandbox.
func (r *Runtime) Exec(ctx context.Context, sandbox model.Sandbox, req model.ExecRequest, streams model.ExecStreams) (model.ExecHandle, error) {
	command := req.Command
	if len(command) == 0 {
		command = []string{"sh", "-lc", "pwd"}
	}
	if req.Detached {
		args := append([]string{"exec", "-d"}, execOptions(req)...)
		args = append(args, containerName(sandbox.ID))
		args = append(args, command...)
		if _, err := r.run(ctx, args...); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		return &execHandle{
			resultCh: closedResult(model.ExecResult{
				ExitCode:    0,
				Status:      model.ExecutionStatusDetached,
				StartedAt:   now,
				CompletedAt: now,
			}),
		}, nil
	}
	execID := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	pidFile := fmt.Sprintf("/tmp/or3-exec-%s.pid", execID)
	script := fmt.Sprintf(`
set -eu
rm -f %[1]s
setsid "$@" &
child=$!
echo "$child" > %[1]s
wait "$child"
`, shellQuote(pidFile))
	args := append([]string{"exec"}, execOptions(req)...)
	args = append(args, containerName(sandbox.ID), "sh", "-lc", script, "sh")
	args = append(args, command...)

	cmd := exec.Command(r.binary, args...)
	stdoutCapture := newPreviewWriter(streams.Stdout, previewLimit)
	stderrCapture := newPreviewWriter(streams.Stderr, previewLimit)
	cmd.Stdout = stdoutCapture
	cmd.Stderr = stderrCapture
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	handle := &execHandle{
		runtime:     r,
		containerID: containerName(sandbox.ID),
		pidFile:     pidFile,
		cmd:         cmd,
		startedAt:   time.Now().UTC(),
		stdout:      stdoutCapture,
		stderr:      stderrCapture,
		resultCh:    make(chan model.ExecResult, 1),
		done:        make(chan struct{}),
	}

	go handle.wait(req.Timeout, ctx)
	return handle, nil
}

// AttachTTY opens an interactive terminal attached to sandbox.
func (r *Runtime) AttachTTY(ctx context.Context, sandbox model.Sandbox, req model.TTYRequest) (model.TTYHandle, error) {
	command := req.Command
	if len(command) == 0 {
		command = []string{"bash"}
	}
	args := append([]string{"exec", "-it"}, execOptions(model.ExecRequest{
		Env: req.Env,
		Cwd: req.Cwd,
	})...)
	args = append(args, containerName(sandbox.ID))
	args = append(args, command...)
	cmd := exec.CommandContext(ctx, r.binary, args...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(defaultInt(req.Rows, 24)),
		Cols: uint16(defaultInt(req.Cols, 80)),
	})
	if err != nil {
		return nil, err
	}
	if _, err := term.MakeRaw(int(ptmx.Fd())); err != nil {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, err
	}
	return &ttyHandle{cmd: cmd, pty: ptmx}, nil
}

// CreateSnapshot captures a snapshot artifact for sandbox.
func (r *Runtime) CreateSnapshot(ctx context.Context, sandbox model.Sandbox, snapshotID string) (model.SnapshotInfo, error) {
	imageRef := snapshotImage(snapshotID)
	if _, err := r.run(ctx, "commit", containerName(sandbox.ID), imageRef); err != nil {
		return model.SnapshotInfo{}, err
	}
	snapshotDir := filepath.Join(sandbox.StorageRoot, ".snapshots", snapshotID)
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return model.SnapshotInfo{}, err
	}
	tarPath := filepath.Join(snapshotDir, "workspace.tar.gz")
	if err := archiveDirectory(sandbox.WorkspaceRoot, tarPath); err != nil {
		return model.SnapshotInfo{}, err
	}
	return model.SnapshotInfo{
		ImageRef:     imageRef,
		WorkspaceTar: tarPath,
	}, nil
}

// RestoreSnapshot restores sandbox from a previously exported snapshot.
func (r *Runtime) RestoreSnapshot(ctx context.Context, sandbox model.Sandbox, snapshot model.Snapshot) (model.RuntimeState, error) {
	if _, err := r.run(ctx, "rm", "-f", containerName(sandbox.ID)); err != nil && !isNoSuchContainer(err) {
		return model.RuntimeState{}, err
	}
	if sandbox.NetworkMode == model.NetworkModeInternetEnabled {
		if err := r.ensureNetwork(ctx, sandbox.ID); err != nil {
			return model.RuntimeState{}, err
		}
	}
	if err := os.RemoveAll(sandbox.WorkspaceRoot); err != nil {
		return model.RuntimeState{}, err
	}
	if err := os.MkdirAll(sandbox.WorkspaceRoot, 0o755); err != nil {
		return model.RuntimeState{}, err
	}
	if snapshot.WorkspaceTar != "" {
		if err := r.extractArchive(snapshot.WorkspaceTar, sandbox.WorkspaceRoot); err != nil {
			return model.RuntimeState{}, err
		}
	}
	spec := model.SandboxSpec{
		SandboxID:                sandbox.ID,
		TenantID:                 sandbox.TenantID,
		BaseImageRef:             snapshot.ImageRef,
		Profile:                  snapshot.Profile,
		ControlProtocolVersion:   snapshot.ControlProtocolVersion,
		WorkspaceContractVersion: snapshot.WorkspaceContractVersion,
		ImageContractVersion:     snapshot.ImageContractVersion,
		CPULimit:                 sandbox.CPULimit,
		MemoryLimitMB:            sandbox.MemoryLimitMB,
		PIDsLimit:                sandbox.PIDsLimit,
		DiskLimitMB:              sandbox.DiskLimitMB,
		NetworkMode:              sandbox.NetworkMode,
		AllowTunnels:             sandbox.AllowTunnels,
		StorageRoot:              sandbox.StorageRoot,
		WorkspaceRoot:            sandbox.WorkspaceRoot,
		CacheRoot:                sandbox.CacheRoot,
		ScratchRoot:              filepath.Join(filepath.Dir(sandbox.StorageRoot), "scratch"),
		SecretsRoot:              filepath.Join(filepath.Dir(sandbox.StorageRoot), "secrets"),
		NetworkPolicy:            model.ResolveNetworkPolicy(sandbox.NetworkMode, sandbox.AllowTunnels),
	}
	return r.Create(ctx, spec)
}

func (r *Runtime) ensureNetwork(ctx context.Context, sandboxID string) error {
	if _, err := r.run(ctx, "network", "inspect", networkName(sandboxID)); err == nil {
		return nil
	}
	_, err := r.run(ctx, "network", "create", "--driver", "bridge", networkName(sandboxID))
	return err
}

func (r *Runtime) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

type execHandle struct {
	runtime     *Runtime
	containerID string
	pidFile     string
	cmd         *exec.Cmd
	startedAt   time.Time
	stdout      *previewWriter
	stderr      *previewWriter
	resultCh    chan model.ExecResult
	done        chan struct{}

	cancelOnce sync.Once
	cancelErr  error
	cancelKind model.ExecutionStatus
}

func (h *execHandle) Wait() model.ExecResult {
	return <-h.resultCh
}

func (h *execHandle) Cancel() error {
	h.cancel(model.ExecutionStatusCanceled)
	return h.cancelErr
}

func (h *execHandle) wait(timeout time.Duration, ctx context.Context) {
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		go func() {
			select {
			case <-timer.C:
				h.cancel(model.ExecutionStatusTimedOut)
			case <-ctx.Done():
				h.cancel(model.ExecutionStatusCanceled)
			case <-h.done:
			}
		}()
	} else {
		go func() {
			select {
			case <-ctx.Done():
				h.cancel(model.ExecutionStatusCanceled)
			case <-h.done:
			}
		}()
	}

	err := h.cmd.Wait()
	completedAt := time.Now().UTC()
	result := model.ExecResult{
		StartedAt:       h.startedAt,
		CompletedAt:     completedAt,
		Duration:        completedAt.Sub(h.startedAt),
		StdoutPreview:   h.stdout.String(),
		StderrPreview:   h.stderr.String(),
		StdoutTruncated: h.stdout.Truncated(),
		StderrTruncated: h.stderr.Truncated(),
		Status:          model.ExecutionStatusSucceeded,
	}
	if h.cancelKind != "" {
		result.Status = h.cancelKind
	} else if err != nil {
		result.Status = model.ExecutionStatusFailed
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				result.ExitCode = ws.ExitStatus()
			}
		} else {
			result.ExitCode = 1
			result.StderrPreview = strings.TrimSpace(result.StderrPreview + "\n" + err.Error())
		}
	}
	if result.Status == model.ExecutionStatusSucceeded {
		result.ExitCode = 0
	}
	h.resultCh <- result
	close(h.done)
	close(h.resultCh)
}

func (h *execHandle) cancel(kind model.ExecutionStatus) {
	h.cancelOnce.Do(func() {
		h.cancelKind = kind
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.cancelErr = h.runtime.killProcessGroup(ctx, h.containerID, h.pidFile)
		if h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
	})
}

func (r *Runtime) killProcessGroup(ctx context.Context, containerID, pidFile string) error {
	script := fmt.Sprintf(`
if [ -f %[1]s ]; then
	pgid=$(cat %[1]s)
	kill -TERM -- -"$pgid" 2>/dev/null || true
	sleep 1
	kill -KILL -- -"$pgid" 2>/dev/null || true
	rm -f %[1]s
fi
`, shellQuote(pidFile))
	_, err := r.run(ctx, "exec", containerID, "sh", "-lc", script)
	return err
}

type ttyHandle struct {
	cmd *exec.Cmd
	pty *os.File
}

func (h *ttyHandle) Reader() io.Reader {
	return h.pty
}

func (h *ttyHandle) Writer() io.Writer {
	return h.pty
}

func (h *ttyHandle) Resize(req model.ResizeRequest) error {
	return pty.Setsize(h.pty, &pty.Winsize{
		Rows: uint16(defaultInt(req.Rows, 24)),
		Cols: uint16(defaultInt(req.Cols, 80)),
	})
}

func (h *ttyHandle) Close() error {
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	if h.pty != nil {
		_ = h.pty.Close()
	}
	return nil
}

type previewWriter struct {
	target    io.Writer
	limit     int
	buf       strings.Builder
	truncated bool
	mu        sync.Mutex
}

func newPreviewWriter(target io.Writer, limit int) *previewWriter {
	return &previewWriter{target: target, limit: limit}
}

func (w *previewWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.target != nil {
		if _, err := w.target.Write(p); err != nil {
			return 0, err
		}
	}
	remaining := w.limit - w.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = w.buf.Write(p[:remaining])
			w.truncated = true
		} else {
			_, _ = w.buf.Write(p)
		}
	} else {
		w.truncated = true
	}
	return len(p), nil
}

func (w *previewWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *previewWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

type inspectPayload struct {
	ID    string `json:"Id"`
	State struct {
		Status    string `json:"Status"`
		Running   bool   `json:"Running"`
		Paused    bool   `json:"Paused"`
		Pid       int    `json:"Pid"`
		Error     string `json:"Error"`
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
	NetworkSettings struct {
		IPAddress string `json:"IPAddress"`
		Networks  map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func execOptions(req model.ExecRequest) []string {
	var args []string
	if req.Cwd != "" {
		args = append(args, "--workdir", req.Cwd)
	}
	for key, value := range req.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", key, value))
	}
	return args
}

func archiveDirectory(srcDir, destTarGz string) error {
	file, err := os.Create(destTarGz)
	if err != nil {
		return err
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, source)
		closeErr := source.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

func extractArchive(srcTarGz, destDir string) error {
	_, err := archiveutil.ExtractTarGz(srcTarGz, destDir, defaultRestoreLimits())
	return err
}

func (r *Runtime) extractArchive(srcTarGz, destDir string) error {
	limits := r.restoreLimits
	if limits.MaxBytes <= 0 || limits.MaxFiles <= 0 || limits.MaxExpansionRatio <= 0 {
		limits = defaultRestoreLimits()
	}
	_, err := archiveutil.ExtractTarGz(srcTarGz, destDir, limits)
	return err
}

func dockerStorageOptUnsupported(err error) bool {
	message := strings.ToLower(err.Error())
	for _, needle := range []string{
		"storage-opt is supported only",
		"unsupported storage opt",
		"invalid option: size",
		"unknown storage opt",
		"xfs project quota",
		"project quota",
	} {
		if strings.Contains(message, needle) {
			return true
		}
	}
	return false
}

func containerName(id string) string {
	return "or3-sandbox-" + id
}

func networkName(id string) string {
	return "or3-net-" + id
}

func hostname(id string) string {
	return "sandbox-" + id
}

func snapshotImage(id string) string {
	return "or3-snapshot-" + id + ":latest"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func defaultInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func isNoSuchContainer(err error) bool {
	return strings.Contains(err.Error(), "No such container")
}

func isNoSuchNetwork(err error) bool {
	return strings.Contains(err.Error(), "No such network")
}

func closedResult(result model.ExecResult) chan model.ExecResult {
	ch := make(chan model.ExecResult, 1)
	ch <- result
	close(ch)
	return ch
}
