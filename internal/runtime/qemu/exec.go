package qemu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"or3-sandbox/internal/model"
)

const execPreviewLimit = 64 * 1024

func (r *Runtime) Exec(ctx context.Context, sandbox model.Sandbox, req model.ExecRequest, streams model.ExecStreams) (model.ExecHandle, error) {
	command := req.Command
	if len(command) == 0 {
		command = []string{"sh", "-lc", "pwd"}
	}
	target := r.sshTarget(sandbox, layoutForSandbox(sandbox))
	if req.Detached {
		remoteScript := buildDetachedRemoteScript(command, req.Cwd, req.Env)
		args := append(r.baseSSHArgs(target, false), "sh", "-lc", remoteScript)
		if _, err := r.runCommand(ctx, r.sshBinary, args...); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		return &qemuExecHandle{
			resultCh: closedResult(model.ExecResult{
				ExitCode:    0,
				Status:      model.ExecutionStatusRunning,
				StartedAt:   now,
				CompletedAt: now,
			}),
		}, nil
	}

	execID := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	pidFile := "/tmp/or3-exec-" + execID + ".pid"
	remoteScript := buildTrackedRemoteScript(command, req.Cwd, req.Env, pidFile)
	args := append(r.baseSSHArgs(target, false), "sh", "-lc", remoteScript)
	cmd := exec.Command(r.sshBinary, args...)
	stdoutCapture := newPreviewWriter(streams.Stdout, execPreviewLimit)
	stderrCapture := newPreviewWriter(streams.Stderr, execPreviewLimit)
	cmd.Stdout = stdoutCapture
	cmd.Stderr = stderrCapture
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	handle := &qemuExecHandle{
		runtime:   r,
		target:    target,
		pidFile:   pidFile,
		cmd:       cmd,
		startedAt: time.Now().UTC(),
		stdout:    stdoutCapture,
		stderr:    stderrCapture,
		resultCh:  make(chan model.ExecResult, 1),
		done:      make(chan struct{}),
	}
	go handle.wait(req.Timeout, ctx)
	return handle, nil
}

func (r *Runtime) AttachTTY(ctx context.Context, sandbox model.Sandbox, req model.TTYRequest) (model.TTYHandle, error) {
	command := req.Command
	if len(command) == 0 {
		command = []string{"bash"}
	}
	target := r.sshTarget(sandbox, layoutForSandbox(sandbox))
	remoteScript := buildInteractiveRemoteScript(command, req.Cwd, req.Env)
	args := append(r.baseSSHArgs(target, true), "sh", "-lc", remoteScript)
	cmd := exec.CommandContext(ctx, r.sshBinary, args...)
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

type qemuExecHandle struct {
	runtime   *Runtime
	target    sshTarget
	pidFile   string
	cmd       *exec.Cmd
	startedAt time.Time
	stdout    *previewWriter
	stderr    *previewWriter
	resultCh  chan model.ExecResult
	done      chan struct{}

	cancelOnce sync.Once
	cancelErr  error
	cancelKind model.ExecutionStatus
}

func (h *qemuExecHandle) Wait() model.ExecResult {
	return <-h.resultCh
}

func (h *qemuExecHandle) Cancel() error {
	h.cancel(model.ExecutionStatusCanceled)
	return h.cancelErr
}

func (h *qemuExecHandle) wait(timeout time.Duration, ctx context.Context) {
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
			} else {
				result.ExitCode = 1
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

func (h *qemuExecHandle) cancel(kind model.ExecutionStatus) {
	h.cancelOnce.Do(func() {
		h.cancelKind = kind
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.cancelErr = h.runtime.killProcessGroup(ctx, h.target, h.pidFile)
		if h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
	})
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

func buildTrackedRemoteScript(command []string, cwd string, env map[string]string, pidFile string) string {
	commandLine := shellJoin(command)
	return fmt.Sprintf(`
set -eu
rm -f %[1]s
%[2]s
%[3]s
setsid sh -lc %[4]s &
child=$!
echo "$child" > %[1]s
wait "$child"
`, shellQuote(pidFile), buildCwdSnippet(cwd), buildEnvSnippet(env), shellQuote(commandLine))
}

func buildDetachedRemoteScript(command []string, cwd string, env map[string]string) string {
	commandLine := shellJoin(command)
	return fmt.Sprintf(`
set -eu
%[1]s
%[2]s
nohup sh -lc %[3]s >/dev/null 2>&1 </dev/null &
`, buildCwdSnippet(cwd), buildEnvSnippet(env), shellQuote(commandLine))
}

func buildInteractiveRemoteScript(command []string, cwd string, env map[string]string) string {
	commandLine := shellJoin(command)
	return strings.TrimSpace(fmt.Sprintf(`
set -eu
%[1]s
%[2]s
exec sh -lc %[3]s
`, buildCwdSnippet(cwd), buildEnvSnippet(env), shellQuote(commandLine)))
}

func buildCwdSnippet(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	return "cd " + shellQuote(cwd)
}

func buildEnvSnippet(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var lines []string
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("export %s=%s", key, shellQuote(env[key])))
	}
	return strings.Join(lines, "\n")
}

func shellJoin(parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func (r *Runtime) killProcessGroup(ctx context.Context, target sshTarget, pidFile string) error {
	script := fmt.Sprintf(`
if [ -f %[1]s ]; then
	pgid=$(cat %[1]s)
	kill -TERM -- -"$pgid" 2>/dev/null || true
	sleep 1
	kill -KILL -- -"$pgid" 2>/dev/null || true
	rm -f %[1]s
fi
`, shellQuote(pidFile))
	args := append(r.baseSSHArgs(target, false), "sh", "-lc", script)
	_, err := r.runCommand(ctx, r.sshBinary, args...)
	return err
}

func closedResult(result model.ExecResult) chan model.ExecResult {
	ch := make(chan model.ExecResult, 1)
	ch <- result
	close(ch)
	return ch
}
