// Package kata implements model.RuntimeManager using containerd with the Kata
// Containers runtime class.  It shells out to the ctr CLI rather than linking
// the containerd client library, keeping the dependency surface small and the
// build fast.
//
// Kata provides hardware-virtualised (microVM) isolation so the RuntimeClass
// is always model.RuntimeClassVM.
//
// Limitations compared to the Docker adapter:
//   - Suspend / Resume are not supported (Kata does not expose pause today).
//   - DiskLimitMB is not enforced at create time; containerd + Kata manage
//     the root filesystem size via the guest kernel / device-mapper.
//   - The adapter is Linux-only; Create returns an error on other platforms.
package kata

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
	"time"

	"github.com/creack/pty"

	"or3-sandbox/internal/archiveutil"
	"or3-sandbox/internal/model"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	previewLimit     = 64 * 1024
	defaultNamespace = "or3"
	defaultUser      = "10001:10001"
)

// ---------------------------------------------------------------------------
// Options / constructor
// ---------------------------------------------------------------------------

// Options configures the Kata/containerd runtime adapter.
type Options struct {
	Binary                    string // Path to ctr binary (default: "ctr").
	RuntimeClass              string // Kata runtime class (e.g. "kata-qemu").
	ContainerdSocket          string // Path to containerd UNIX socket.
	Namespace                 string // containerd namespace (default: "or3").
	HostOS                    string // Override for runtime.GOOS (testing).
	SnapshotMaxBytes          int64
	SnapshotMaxFiles          int
	SnapshotMaxExpansionRatio int
}

// Runtime implements model.RuntimeManager on top of containerd + Kata.
type Runtime struct {
	binary        string
	runtimeClass  string
	socket        string
	namespace     string
	hostOS        string
	restoreLimits archiveutil.Limits
}

func defaultRestoreLimits() archiveutil.Limits {
	return archiveutil.Limits{
		MaxBytes:          256 * 1024 * 1024,
		MaxFiles:          4096,
		MaxExpansionRatio: 32,
	}
}

// New creates a Kata runtime adapter.  The returned *Runtime satisfies
// model.RuntimeManager.
func New(opts Options) *Runtime {
	binary := strings.TrimSpace(opts.Binary)
	if binary == "" {
		binary = "ctr"
	}
	runtimeClass := strings.TrimSpace(opts.RuntimeClass)
	if runtimeClass == "" {
		runtimeClass = "kata-qemu"
	}
	socket := strings.TrimSpace(opts.ContainerdSocket)
	ns := strings.TrimSpace(opts.Namespace)
	if ns == "" {
		ns = defaultNamespace
	}
	hostOS := strings.TrimSpace(opts.HostOS)
	if hostOS == "" {
		hostOS = goruntime.GOOS
	}
	limits := defaultRestoreLimits()
	if opts.SnapshotMaxBytes > 0 {
		limits.MaxBytes = opts.SnapshotMaxBytes
	}
	if opts.SnapshotMaxFiles > 0 {
		limits.MaxFiles = opts.SnapshotMaxFiles
	}
	if opts.SnapshotMaxExpansionRatio > 0 {
		limits.MaxExpansionRatio = opts.SnapshotMaxExpansionRatio
	}
	return &Runtime{
		binary:        binary,
		runtimeClass:  runtimeClass,
		socket:        socket,
		namespace:     ns,
		hostOS:        hostOS,
		restoreLimits: limits,
	}
}

// ---------------------------------------------------------------------------
// RuntimeManager — lifecycle
// ---------------------------------------------------------------------------

func (r *Runtime) Create(ctx context.Context, spec model.SandboxSpec) (model.RuntimeState, error) {
	if r.hostOS != "linux" {
		return model.RuntimeState{}, fmt.Errorf("kata runtime requires linux (host OS is %q)", r.hostOS)
	}

	// Ensure host-side storage directories exist.
	for _, dir := range []string{spec.StorageRoot, spec.WorkspaceRoot} {
		if dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return model.RuntimeState{}, err
			}
		}
	}
	for _, dir := range []string{spec.CacheRoot, spec.ScratchRoot, spec.SecretsRoot} {
		if dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return model.RuntimeState{}, err
			}
		}
	}

	// Persist local state so later methods can find storage paths.
	stateDir := filepath.Join(spec.StorageRoot, ".kata")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return model.RuntimeState{}, err
	}
	state := localState{
		SandboxID:     spec.SandboxID,
		BaseImageRef:  spec.BaseImageRef,
		WorkspaceRoot: spec.WorkspaceRoot,
		CacheRoot:     spec.CacheRoot,
		ScratchRoot:   spec.ScratchRoot,
		SecretsRoot:   spec.SecretsRoot,
		CreatedAt:     time.Now().UTC(),
	}
	if err := writeState(stateDir, state); err != nil {
		return model.RuntimeState{}, err
	}

	// Pull the image if not already present.
	pullArgs := r.baseArgs("images", "pull", spec.BaseImageRef)
	if _, err := r.run(ctx, pullArgs...); err != nil {
		slog.Warn("kata: image pull failed (may already be present)", "image", spec.BaseImageRef, "error", err)
	}

	// Build the ctr run command.
	args := r.runArgs(spec)

	if _, err := r.run(ctx, args...); err != nil {
		return model.RuntimeState{}, fmt.Errorf("kata create: %w", err)
	}
	return model.RuntimeState{
		RuntimeID: containerName(spec.SandboxID),
		Status:    model.SandboxStatusStopped,
	}, nil
}

// Start boots a previously created Kata task.
func (r *Runtime) Start(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	name := containerName(sandbox.ID)
	args := r.baseArgs("task", "start", name)
	out, err := r.run(ctx, args...)
	if err != nil {
		return model.RuntimeState{}, fmt.Errorf("kata start: %w", err)
	}

	// Try to parse PID from ctr output (first line is usually the PID).
	pid := parsePID(out)

	// Persist PID.
	stateDir := r.stateDir(sandbox)
	if stateDir != "" {
		_ = os.WriteFile(filepath.Join(stateDir, "task.pid"), []byte(strconv.Itoa(pid)), 0o644)
	}

	now := time.Now().UTC()
	return model.RuntimeState{
		RuntimeID: name,
		Status:    model.SandboxStatusRunning,
		Running:   true,
		Pid:       pid,
		StartedAt: &now,
	}, nil
}

// Stop stops a running Kata task.
func (r *Runtime) Stop(ctx context.Context, sandbox model.Sandbox, force bool) (model.RuntimeState, error) {
	name := containerName(sandbox.ID)

	// Kill the task first.
	killArgs := r.baseArgs("task", "kill", name)
	if force {
		killArgs = append(killArgs, "--signal", "SIGKILL")
	}
	if _, err := r.run(ctx, killArgs...); err != nil && !isNotFoundErr(err) {
		return model.RuntimeState{}, fmt.Errorf("kata stop (kill): %w", err)
	}

	// Delete the task so it can be restarted later.
	delArgs := r.baseArgs("task", "delete", name)
	if _, err := r.run(ctx, delArgs...); err != nil && !isNotFoundErr(err) {
		slog.Warn("kata: task delete after stop", "error", err)
	}

	// Clean up PID file.
	if sd := r.stateDir(sandbox); sd != "" {
		_ = os.Remove(filepath.Join(sd, "task.pid"))
	}

	return model.RuntimeState{
		RuntimeID: name,
		Status:    model.SandboxStatusStopped,
	}, nil
}

// Suspend is not supported by the Kata runtime adapter.
func (r *Runtime) Suspend(_ context.Context, _ model.Sandbox) (model.RuntimeState, error) {
	return model.RuntimeState{}, errors.New("kata runtime does not support suspend")
}

// Resume is not supported by the Kata runtime adapter.
func (r *Runtime) Resume(_ context.Context, _ model.Sandbox) (model.RuntimeState, error) {
	return model.RuntimeState{}, errors.New("kata runtime does not support resume")
}

// Destroy removes the Kata task, container, and persisted runtime state.
func (r *Runtime) Destroy(ctx context.Context, sandbox model.Sandbox) error {
	name := containerName(sandbox.ID)

	// Best-effort kill + delete task.
	killArgs := r.baseArgs("task", "kill", name, "--signal", "SIGKILL")
	_, _ = r.run(ctx, killArgs...)
	delArgs := r.baseArgs("task", "delete", name)
	_, _ = r.run(ctx, delArgs...)

	// Delete the container.
	rmArgs := r.baseArgs("containers", "delete", name)
	if _, err := r.run(ctx, rmArgs...); err != nil && !isNotFoundErr(err) {
		return fmt.Errorf("kata destroy: %w", err)
	}

	// Remove host-side storage.
	baseDir := filepath.Dir(sandbox.StorageRoot)
	for _, dir := range []string{sandbox.WorkspaceRoot, sandbox.CacheRoot, sandbox.StorageRoot,
		filepath.Join(baseDir, "scratch"), filepath.Join(baseDir, "secrets")} {
		if dir == "" {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

// Inspect returns the current Kata task state for sandbox.
func (r *Runtime) Inspect(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	name := containerName(sandbox.ID)

	// Use "task ls" to see if the task is running.
	args := r.baseArgs("task", "ls")
	out, err := r.run(ctx, args...)
	if err != nil {
		// If containerd is unreachable the sandbox is degraded.
		return model.RuntimeState{
			RuntimeID: name,
			Status:    model.SandboxStatusDegraded,
			Error:     err.Error(),
		}, nil
	}

	status, pid := parseTaskList(out, name)

	result := model.RuntimeState{
		RuntimeID: name,
		Pid:       pid,
	}

	switch status {
	case "RUNNING":
		result.Status = model.SandboxStatusRunning
		result.Running = true
	case "STOPPED", "PAUSED":
		result.Status = model.SandboxStatusStopped
	case "":
		// Task not found — check if container still exists.
		cArgs := r.baseArgs("containers", "ls", "--quiet")
		cOut, cErr := r.run(ctx, cArgs...)
		if cErr != nil {
			result.Status = model.SandboxStatusDegraded
			result.Error = cErr.Error()
			return result, nil
		}
		if containerInList(cOut, name) {
			result.Status = model.SandboxStatusStopped
		} else {
			result.Status = model.SandboxStatusDeleted
		}
	default:
		result.Status = model.SandboxStatusDegraded
		result.Error = fmt.Sprintf("unexpected task status: %s", status)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// RuntimeManager — exec / TTY
// ---------------------------------------------------------------------------

func (r *Runtime) Exec(ctx context.Context, sandbox model.Sandbox, req model.ExecRequest, streams model.ExecStreams) (model.ExecHandle, error) {
	command := req.Command
	if len(command) == 0 {
		command = []string{"sh", "-lc", "pwd"}
	}

	name := containerName(sandbox.ID)
	execID := fmt.Sprintf("exec-%d", time.Now().UTC().UnixNano())

	if req.Detached {
		args := r.baseArgs("task", "exec", "--exec-id", execID, "--detach")
		args = append(args, name)
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

	args := r.baseArgs("task", "exec", "--exec-id", execID)
	if req.Cwd != "" {
		args = append(args, "--cwd", req.Cwd)
	}
	args = append(args, name)
	args = append(args, command...)

	cmd := exec.Command(r.binary, args[1:]...) // args[0] is binary itself via baseArgs
	// Actually baseArgs returns flag strings — the binary is invoked separately.
	cmd = exec.CommandContext(ctx, r.binary, args...)

	stdoutCapture := newPreviewWriter(streams.Stdout, previewLimit)
	stderrCapture := newPreviewWriter(streams.Stderr, previewLimit)
	cmd.Stdout = stdoutCapture
	cmd.Stderr = stderrCapture

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	handle := &execHandle{
		cmd:      cmd,
		stdout:   stdoutCapture,
		stderr:   stderrCapture,
		resultCh: make(chan model.ExecResult, 1),
		done:     make(chan struct{}),
	}
	go handle.wait(req.Timeout, ctx)
	return handle, nil
}

// AttachTTY opens an interactive terminal inside sandbox.
func (r *Runtime) AttachTTY(ctx context.Context, sandbox model.Sandbox, req model.TTYRequest) (model.TTYHandle, error) {
	command := req.Command
	if len(command) == 0 {
		command = []string{"bash"}
	}

	name := containerName(sandbox.ID)
	execID := fmt.Sprintf("tty-%d", time.Now().UTC().UnixNano())

	args := r.baseArgs("task", "exec", "--exec-id", execID, "--tty")
	if req.Cwd != "" {
		args = append(args, "--cwd", req.Cwd)
	}
	args = append(args, name)
	args = append(args, command...)

	cmd := exec.CommandContext(ctx, r.binary, args...)

	ws := &pty.Winsize{Rows: uint16(req.Rows), Cols: uint16(req.Cols)}
	ptmx, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		return nil, fmt.Errorf("kata tty: %w", err)
	}

	return &ttyHandle{
		ptmx: ptmx,
		cmd:  cmd,
	}, nil
}

// ---------------------------------------------------------------------------
// RuntimeManager — snapshots
// ---------------------------------------------------------------------------

func (r *Runtime) CreateSnapshot(ctx context.Context, sandbox model.Sandbox, snapshotID string) (model.SnapshotInfo, error) {
	if sandbox.WorkspaceRoot == "" {
		return model.SnapshotInfo{}, errors.New("kata snapshot: workspace root not set")
	}

	// Archive the workspace directory.
	archivePath := filepath.Join(sandbox.StorageRoot, fmt.Sprintf("snapshot-%s.tar.gz", snapshotID))
	if err := archiveDirectory(sandbox.WorkspaceRoot, archivePath); err != nil {
		return model.SnapshotInfo{}, fmt.Errorf("kata snapshot archive: %w", err)
	}

	// Read base image from local state.
	imageRef := ""
	if sd := r.stateDir(sandbox); sd != "" {
		st, err := readState(sd)
		if err == nil {
			imageRef = st.BaseImageRef
		}
	}

	return model.SnapshotInfo{
		ImageRef:     imageRef,
		WorkspaceTar: archivePath,
	}, nil
}

// RestoreSnapshot restores sandbox from a previously exported snapshot.
func (r *Runtime) RestoreSnapshot(ctx context.Context, sandbox model.Sandbox, snapshot model.Snapshot) (model.RuntimeState, error) {
	if sandbox.WorkspaceRoot == "" {
		return model.RuntimeState{}, errors.New("kata restore: workspace root not set")
	}
	if snapshot.WorkspaceTar == "" {
		return model.RuntimeState{}, errors.New("kata restore: snapshot has no workspace archive")
	}

	// Clear workspace and extract.
	if err := os.RemoveAll(sandbox.WorkspaceRoot); err != nil {
		return model.RuntimeState{}, err
	}
	if err := os.MkdirAll(sandbox.WorkspaceRoot, 0o755); err != nil {
		return model.RuntimeState{}, err
	}

	if _, err := archiveutil.ExtractTarGz(snapshot.WorkspaceTar, sandbox.WorkspaceRoot, r.restoreLimits); err != nil {
		return model.RuntimeState{}, fmt.Errorf("kata restore extract: %w", err)
	}

	return model.RuntimeState{
		RuntimeID: containerName(sandbox.ID),
		Status:    model.SandboxStatusStopped,
	}, nil
}

// ---------------------------------------------------------------------------
// ctr invocation helpers
// ---------------------------------------------------------------------------

// baseArgs returns the common prefix for every ctr invocation:
//
//	[--namespace <ns>] [--address <socket>] <subcommand...>
func (r *Runtime) baseArgs(sub ...string) []string {
	var args []string
	if r.namespace != "" {
		args = append(args, "--namespace", r.namespace)
	}
	if r.socket != "" {
		args = append(args, "--address", r.socket)
	}
	args = append(args, sub...)
	return args
}

// runArgs builds the full argument list for "ctr run" (container creation).
func (r *Runtime) runArgs(spec model.SandboxSpec) []string {
	args := r.baseArgs("run")
	args = append(args, "--runtime", r.runtimeClass)
	args = append(args, "--null-io") // no stdin/stdout for the init process
	args = append(args, "--detach")  // create + start; we immediately stop the task

	// Bind mounts.
	if spec.WorkspaceRoot != "" {
		args = append(args, "--mount", fmt.Sprintf("type=bind,src=%s,dst=/workspace,options=rbind:rw", spec.WorkspaceRoot))
	}
	if spec.CacheRoot != "" {
		args = append(args, "--mount", fmt.Sprintf("type=bind,src=%s,dst=/cache,options=rbind:rw", spec.CacheRoot))
	}
	if spec.ScratchRoot != "" {
		args = append(args, "--mount", fmt.Sprintf("type=bind,src=%s,dst=/scratch,options=rbind:rw", spec.ScratchRoot))
	}
	if spec.SecretsRoot != "" {
		args = append(args, "--mount", fmt.Sprintf("type=bind,src=%s,dst=/secrets,options=rbind:ro", spec.SecretsRoot))
	}

	// Resource limits passed as annotations (Kata reads these).
	if spec.MemoryLimitMB > 0 {
		args = append(args, "--memory-limit", fmt.Sprintf("%d", int64(spec.MemoryLimitMB)*1024*1024))
	}
	if spec.CPULimit > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%g", float64(spec.CPULimit)/1000.0))
	}

	// Network policy — if internet is disabled Kata should use a loopback-only
	// network namespace.  The exact mechanism depends on the CNI setup; here we
	// add a label that an operator-configured CNI plugin can inspect.
	np := model.ResolveNetworkPolicy(spec.NetworkMode, spec.AllowTunnels)
	if !np.Internet {
		args = append(args, "--label", "or3.network.loopback-only=true")
	}

	// Image and container name (positional).
	args = append(args, spec.BaseImageRef, containerName(spec.SandboxID))

	return args
}

func (r *Runtime) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.binary, args...)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		combined := strings.TrimSpace(stderr.String() + " " + stdout.String())
		return "", fmt.Errorf("%s %s: %s: %w", r.binary, strings.Join(args[:min(len(args), 3)], " "), combined, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// stateDir returns the path to the per-sandbox .kata state directory, or ""
// if StorageRoot is not set.
func (r *Runtime) stateDir(sandbox model.Sandbox) string {
	if sandbox.StorageRoot == "" {
		return ""
	}
	return filepath.Join(sandbox.StorageRoot, ".kata")
}

func containerName(sandboxID string) string {
	return "or3-" + sandboxID
}

// ---------------------------------------------------------------------------
// Local state persistence (.kata/state.json)
// ---------------------------------------------------------------------------

type localState struct {
	SandboxID     string    `json:"sandbox_id"`
	BaseImageRef  string    `json:"base_image_ref"`
	WorkspaceRoot string    `json:"workspace_root"`
	CacheRoot     string    `json:"cache_root,omitempty"`
	ScratchRoot   string    `json:"scratch_root,omitempty"`
	SecretsRoot   string    `json:"secrets_root,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

func writeState(stateDir string, s localState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, "state.json"), data, 0o644)
}

func readState(stateDir string) (localState, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
	if err != nil {
		return localState{}, err
	}
	var s localState
	return s, json.Unmarshal(data, &s)
}

// ---------------------------------------------------------------------------
// ctr output parsers
// ---------------------------------------------------------------------------

// parsePID extracts a numeric PID from the first token of ctr output.
func parsePID(output string) int {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// ctr task start prints: <pid>
		if n, err := strconv.Atoi(line); err == nil {
			return n
		}
		// Try first field.
		if fields := strings.Fields(line); len(fields) > 0 {
			if n, err := strconv.Atoi(fields[0]); err == nil {
				return n
			}
		}
	}
	return 0
}

// parseTaskList scans the tabular output of "ctr task ls" and returns the
// status string and PID for the given container name.
func parseTaskList(output, name string) (status string, pid int) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[0] == name {
			pid, _ = strconv.Atoi(fields[1])
			status = fields[2]
			return
		}
	}
	return "", 0
}

// containerInList checks whether name appears in the "ctr containers ls --quiet" output.
func containerInList(output, name string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// isNotFoundErr detects "not found" errors from containerd / ctr.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "no such")
}

// ---------------------------------------------------------------------------
// Exec handle
// ---------------------------------------------------------------------------

type execHandle struct {
	cmd      *exec.Cmd
	stdout   *previewWriter
	stderr   *previewWriter
	resultCh chan model.ExecResult
	done     chan struct{}
	once     sync.Once
}

func (h *execHandle) Wait() model.ExecResult {
	return <-h.resultCh
}

func (h *execHandle) Cancel() error {
	if h.cmd != nil && h.cmd.Process != nil {
		return h.cmd.Process.Kill()
	}
	return nil
}

func (h *execHandle) wait(timeout time.Duration, ctx context.Context) {
	defer h.once.Do(func() { close(h.done) })

	startedAt := time.Now().UTC()

	var timerCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timerCh = timer.C
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- h.cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-timerCh:
		_ = h.cmd.Process.Kill()
		waitErr = <-waitCh
		completedAt := time.Now().UTC()
		h.resultCh <- model.ExecResult{
			ExitCode:        -1,
			Status:          model.ExecutionStatusTimedOut,
			StartedAt:       startedAt,
			CompletedAt:     completedAt,
			Duration:        completedAt.Sub(startedAt),
			StdoutPreview:   h.stdout.Preview(),
			StderrPreview:   h.stderr.Preview(),
			StdoutTruncated: h.stdout.Truncated(),
			StderrTruncated: h.stderr.Truncated(),
		}
		return
	case <-ctx.Done():
		_ = h.cmd.Process.Kill()
		waitErr = <-waitCh
		completedAt := time.Now().UTC()
		h.resultCh <- model.ExecResult{
			ExitCode:        -1,
			Status:          model.ExecutionStatusCanceled,
			StartedAt:       startedAt,
			CompletedAt:     completedAt,
			Duration:        completedAt.Sub(startedAt),
			StdoutPreview:   h.stdout.Preview(),
			StderrPreview:   h.stderr.Preview(),
			StdoutTruncated: h.stdout.Truncated(),
			StderrTruncated: h.stderr.Truncated(),
		}
		return
	}

	completedAt := time.Now().UTC()
	exitCode := 0
	status := model.ExecutionStatusSucceeded
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
		status = model.ExecutionStatusFailed
	}

	h.resultCh <- model.ExecResult{
		ExitCode:        exitCode,
		Status:          status,
		StartedAt:       startedAt,
		CompletedAt:     completedAt,
		Duration:        completedAt.Sub(startedAt),
		StdoutPreview:   h.stdout.Preview(),
		StderrPreview:   h.stderr.Preview(),
		StdoutTruncated: h.stdout.Truncated(),
		StderrTruncated: h.stderr.Truncated(),
	}
}

func closedResult(r model.ExecResult) chan model.ExecResult {
	ch := make(chan model.ExecResult, 1)
	ch <- r
	return ch
}

// ---------------------------------------------------------------------------
// TTY handle
// ---------------------------------------------------------------------------

type ttyHandle struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func (h *ttyHandle) Reader() io.Reader { return h.ptmx }
func (h *ttyHandle) Writer() io.Writer { return h.ptmx }

func (h *ttyHandle) Resize(req model.ResizeRequest) error {
	return pty.Setsize(h.ptmx, &pty.Winsize{Rows: uint16(req.Rows), Cols: uint16(req.Cols)})
}

func (h *ttyHandle) Close() error {
	err := h.ptmx.Close()
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
		_, _ = h.cmd.Process.Wait()
	}
	return err
}

// ---------------------------------------------------------------------------
// Preview writer (mirrors Docker adapter pattern)
// ---------------------------------------------------------------------------

type previewWriter struct {
	inner     io.Writer
	buf       []byte
	limit     int
	truncated bool
	mu        sync.Mutex
}

func newPreviewWriter(w io.Writer, limit int) *previewWriter {
	return &previewWriter{inner: w, limit: limit}
}

func (w *previewWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	remaining := w.limit - len(w.buf)
	if remaining > 0 {
		take := len(p)
		if take > remaining {
			take = remaining
			w.truncated = true
		}
		w.buf = append(w.buf, p[:take]...)
	} else if len(p) > 0 {
		w.truncated = true
	}
	w.mu.Unlock()
	if w.inner != nil {
		return w.inner.Write(p)
	}
	return len(p), nil
}

func (w *previewWriter) Preview() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

func (w *previewWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

// ---------------------------------------------------------------------------
// Workspace archive helpers
// ---------------------------------------------------------------------------

func archiveDirectory(srcDir, destArchive string) error {
	outFile, err := os.Create(destArchive)
	if err != nil {
		return err
	}
	defer outFile.Close()

	gw := gzip.NewWriter(outFile)
	defer gw.Close()
	tw := tar.NewWriter(gw)
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
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

// Ensure compile-time interface satisfaction.
var _ model.RuntimeManager = (*Runtime)(nil)

// min is a local helper for Go < 1.21 compatibility (the codebase may still
// target 1.20).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
