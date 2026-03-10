package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"or3-sandbox/internal/model"
	"or3-sandbox/internal/runtime/qemu/agentproto"
)

const (
	defaultPortPath = "/dev/virtio-ports/org.or3.guest_agent"
	readyMarkerPath = "/var/lib/or3/bootstrap.ready"
	previewLimit    = 64 * 1024
	maxFileTransfer = 8 * 1024 * 1024
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	agent := &guestAgent{portPath: env("OR3_GUEST_AGENT_PORT_PATH", defaultPortPath)}
	if err := agent.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type guestAgent struct {
	portPath string
}

func (a *guestAgent) run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		file, err := os.OpenFile(a.portPath, os.O_RDWR, 0)
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}
		err = a.serveConn(ctx, file)
		_ = file.Close()
		if err != nil && !errors.Is(err, io.EOF) && ctx.Err() == nil {
			return err
		}
	}
}

func (a *guestAgent) serveConn(ctx context.Context, conn io.ReadWriter) error {
	for {
		message, err := agentproto.ReadMessage(conn)
		if err != nil {
			return err
		}
		if message.Op == agentproto.OpPTYOpen {
			var req agentproto.PTYOpenRequest
			if err := json.Unmarshal(message.Result, &req); err != nil {
				return agentproto.WriteMessage(conn, agentproto.Message{Op: message.Op, OK: false, Error: err.Error()})
			}
			return a.servePTY(ctx, conn, req)
		}
		response, err := a.handle(ctx, message)
		if err != nil {
			response = agentproto.Message{Op: message.Op, OK: false, Error: err.Error()}
		}
		if err := agentproto.WriteMessage(conn, response); err != nil {
			return err
		}
	}
}

func (a *guestAgent) handle(ctx context.Context, message agentproto.Message) (agentproto.Message, error) {
	switch message.Op {
	case agentproto.OpHello:
		result, err := json.Marshal(agentproto.HelloResult{
			ProtocolVersion:          agentproto.ProtocolVersion,
			WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
			Ready:                    isReady(),
			Capabilities:             []string{"exec", "pty", "files", "shutdown"},
		})
		return agentproto.Message{Op: message.Op, OK: true, Result: result}, err
	case agentproto.OpReady:
		ready := isReady()
		reason := ""
		if !ready {
			reason = "bootstrap marker not present"
		}
		result, err := json.Marshal(agentproto.ReadyResult{Ready: ready, Reason: reason})
		return agentproto.Message{Op: message.Op, OK: true, Result: result}, err
	case agentproto.OpExec:
		var req agentproto.ExecRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		result, err := runExec(ctx, req)
		if err != nil {
			return agentproto.Message{}, err
		}
		payload, err := json.Marshal(result)
		return agentproto.Message{Op: message.Op, OK: true, Result: payload}, err
	case agentproto.OpFileRead:
		var req agentproto.FileReadRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		target, err := workspacePath(req.Path)
		if err != nil {
			return agentproto.Message{}, err
		}
		data, err := readFileLimited(target, maxFileTransfer)
		if err != nil {
			return agentproto.Message{}, err
		}
		payload, err := json.Marshal(agentproto.FileReadResult{Path: target, Content: agentproto.EncodeBytes(data)})
		return agentproto.Message{Op: message.Op, OK: true, Result: payload}, err
	case agentproto.OpFileWrite:
		var req agentproto.FileWriteRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		target, err := workspacePath(req.Path)
		if err != nil {
			return agentproto.Message{}, err
		}
		data, err := agentproto.DecodeBytes(req.Content)
		if err != nil {
			return agentproto.Message{}, err
		}
		if err := os.MkdirAll(path.Dir(target), 0o755); err != nil {
			return agentproto.Message{}, err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return agentproto.Message{}, err
		}
		return agentproto.Message{Op: message.Op, OK: true}, nil
	case agentproto.OpFileDelete:
		var req agentproto.PathRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		target, err := workspacePath(req.Path)
		if err != nil {
			return agentproto.Message{}, err
		}
		return agentproto.Message{Op: message.Op, OK: true}, os.RemoveAll(target)
	case agentproto.OpMkdir:
		var req agentproto.PathRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		target, err := workspacePath(req.Path)
		if err != nil {
			return agentproto.Message{}, err
		}
		return agentproto.Message{Op: message.Op, OK: true}, os.MkdirAll(target, 0o755)
	case agentproto.OpShutdown:
		go func() {
			_ = exec.Command("/sbin/poweroff").Run()
		}()
		return agentproto.Message{Op: message.Op, OK: true}, nil
	default:
		return agentproto.Message{}, fmt.Errorf("unsupported guest agent operation %q", message.Op)
	}
}

func (a *guestAgent) servePTY(ctx context.Context, conn io.ReadWriter, req agentproto.PTYOpenRequest) error {
	command := req.Command
	if len(command) == 0 {
		command = []string{"bash"}
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = defaultString(req.Cwd, "/workspace")
	cmd.Env = append(os.Environ(), flattenEnv(req.Env)...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(defaultInt(req.Rows, 24)), Cols: uint16(defaultInt(req.Cols, 80))})
	if err != nil {
		return agentproto.WriteMessage(conn, agentproto.Message{Op: agentproto.OpPTYOpen, OK: false, Error: err.Error()})
	}
	defer ptmx.Close()
	sessionID := fmt.Sprintf("pty-%d", time.Now().UTC().UnixNano())
	ack, err := json.Marshal(agentproto.PTYOpenResult{SessionID: sessionID})
	if err != nil {
		return err
	}
	if err := agentproto.WriteMessage(conn, agentproto.Message{Op: agentproto.OpPTYOpen, OK: true, Result: ack}); err != nil {
		return err
	}
	var writeMu sync.Mutex
	sendPTY := func(data agentproto.PTYData) error {
		payload, err := json.Marshal(data)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		return agentproto.WriteMessage(conn, agentproto.Message{Op: agentproto.OpPTYData, OK: true, Result: payload})
	}
	errCh := make(chan error, 2)
	type connMessage struct {
		message agentproto.Message
		err     error
	}
	messageCh := make(chan connMessage, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if sendErr := sendPTY(agentproto.PTYData{SessionID: sessionID, Data: agentproto.EncodeBytes(buf[:n])}); sendErr != nil {
					errCh <- sendErr
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					errCh <- err
				}
				return
			}
		}
	}()
	go func() {
		for {
			message, err := agentproto.ReadMessage(conn)
			messageCh <- connMessage{message: message, err: err}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		exitCode := 0
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}
		_ = sendPTY(agentproto.PTYData{SessionID: sessionID, EOF: true, ExitCode: &exitCode})
		errCh <- io.EOF
	}()
	for {
		select {
		case inbound := <-messageCh:
			if inbound.err != nil {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				return inbound.err
			}
			switch inbound.message.Op {
			case agentproto.OpPTYData:
				var data agentproto.PTYData
				if err := json.Unmarshal(inbound.message.Result, &data); err != nil {
					return err
				}
				if data.Data != "" {
					decoded, err := agentproto.DecodeBytes(data.Data)
					if err != nil {
						return err
					}
					if _, err := ptmx.Write(decoded); err != nil {
						return err
					}
				}
			case agentproto.OpPTYResize:
				var resize agentproto.PTYResizeRequest
				if err := json.Unmarshal(inbound.message.Result, &resize); err != nil {
					return err
				}
				if err := pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(defaultInt(resize.Rows, 24)), Cols: uint16(defaultInt(resize.Cols, 80))}); err != nil {
					return err
				}
			case agentproto.OpPTYClose:
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				return nil
			}
		case err := <-errCh:
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func runExec(ctx context.Context, req agentproto.ExecRequest) (agentproto.ExecResult, error) {
	command := req.Command
	if len(command) == 0 {
		command = []string{"sh", "-lc", "pwd"}
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if req.Detached {
		runCtx = context.Background()
	}
	if req.Timeout > 0 {
		if req.Detached {
			runCtx, cancel = context.WithTimeout(context.Background(), req.Timeout)
		} else {
			runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
			defer cancel()
		}
	}
	cmd := exec.CommandContext(runCtx, command[0], command[1:]...)
	cmd.Dir = defaultString(req.Cwd, "/workspace")
	cmd.Env = append(os.Environ(), flattenEnv(req.Env)...)
	stdout := &limitedBuffer{limit: previewLimit}
	stderr := &limitedBuffer{limit: previewLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	startedAt := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		return agentproto.ExecResult{}, err
	}
	if req.Detached {
		go func() {
			_ = cmd.Wait()
			if cancel != nil {
				cancel()
			}
		}()
		return agentproto.ExecResult{ExitCode: 0, Status: string(model.ExecutionStatusDetached), StartedAt: startedAt, CompletedAt: startedAt}, nil
	}
	err := cmd.Wait()
	completedAt := time.Now().UTC()
	status := model.ExecutionStatusSucceeded
	exitCode := 0
	if err != nil {
		status = model.ExecutionStatusFailed
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			status = model.ExecutionStatusTimedOut
			exitCode = 124
		} else {
			exitCode = 1
		}
	}
	return agentproto.ExecResult{
		ExitCode:        exitCode,
		Status:          string(status),
		StartedAt:       startedAt,
		CompletedAt:     completedAt,
		StdoutPreview:   stdout.String(),
		StderrPreview:   stderr.String(),
		StdoutTruncated: stdout.truncated,
		StderrTruncated: stderr.truncated,
	}, nil
}

func isReady() bool {
	_, err := os.Stat(readyMarkerPath)
	return err == nil
}

func workspacePath(raw string) (string, error) {
	clean := path.Clean(defaultString(raw, "/workspace"))
	if clean != "/workspace" && !strings.HasPrefix(clean, "/workspace/") {
		return "", fmt.Errorf("path escapes workspace")
	}
	return clean, nil
}

func flattenEnv(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func defaultInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func readFileLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil && info.Size() > limit {
		return nil, fmt.Errorf("file exceeds transfer limit of %d bytes", limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds transfer limit of %d bytes", limit)
	}
	return data, nil
}

type limitedBuffer struct {
	limit     int
	buf       []byte
	truncated bool
	mu        sync.Mutex
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		if len(p) > remaining {
			b.buf = append(b.buf, p[:remaining]...)
			b.truncated = true
		} else {
			b.buf = append(b.buf, p...)
		}
	} else {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
