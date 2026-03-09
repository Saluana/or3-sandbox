package qemu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"or3-sandbox/internal/model"
	"or3-sandbox/internal/runtime/qemu/agentproto"
)

type guestHandshake struct {
	ProtocolVersion          string
	WorkspaceContractVersion string
	Capabilities             []string
}

func (r *Runtime) agentHandshake(ctx context.Context, layout sandboxLayout) (guestHandshake, error) {
	var result agentproto.HelloResult
	if err := r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpHello, nil, &result); err != nil {
		return guestHandshake{}, err
	}
	if result.ProtocolVersion != agentproto.ProtocolVersion {
		return guestHandshake{}, fmt.Errorf("guest agent protocol mismatch: host=%s guest=%s", agentproto.ProtocolVersion, result.ProtocolVersion)
	}
	return guestHandshake{
		ProtocolVersion:          result.ProtocolVersion,
		WorkspaceContractVersion: result.WorkspaceContractVersion,
		Capabilities:             append([]string(nil), result.Capabilities...),
	}, nil
}

func (r *Runtime) agentReady(ctx context.Context, layout sandboxLayout) error {
	var result agentproto.ReadyResult
	if err := r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpReady, nil, &result); err != nil {
		return err
	}
	if !result.Ready {
		if result.Reason == "" {
			result.Reason = "guest agent reported not ready"
		}
		return errors.New(result.Reason)
	}
	return nil
}

func (r *Runtime) agentExec(ctx context.Context, layout sandboxLayout, req model.ExecRequest, streams model.ExecStreams) (model.ExecHandle, error) {
	payload := agentproto.ExecRequest{
		Command:  req.Command,
		Cwd:      req.Cwd,
		Env:      req.Env,
		Timeout:  req.Timeout,
		Detached: req.Detached,
	}
	var result agentproto.ExecResult
	if err := r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpExec, payload, &result); err != nil {
		return nil, err
	}
	if streams.Stdout != nil && result.StdoutPreview != "" {
		_, _ = io.WriteString(streams.Stdout, result.StdoutPreview)
	}
	if streams.Stderr != nil && result.StderrPreview != "" {
		_, _ = io.WriteString(streams.Stderr, result.StderrPreview)
	}
	execResult := model.ExecResult{
		ExitCode:        result.ExitCode,
		Status:          model.ExecutionStatus(result.Status),
		StartedAt:       result.StartedAt,
		CompletedAt:     result.CompletedAt,
		Duration:        result.CompletedAt.Sub(result.StartedAt),
		StdoutPreview:   result.StdoutPreview,
		StderrPreview:   result.StderrPreview,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
	}
	return &qemuExecHandle{resultCh: closedResult(execResult), done: make(chan struct{})}, nil
}

func (r *Runtime) agentReadWorkspaceFileBytes(ctx context.Context, layout sandboxLayout, relativePath string) ([]byte, error) {
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return nil, err
	}
	var result agentproto.FileReadResult
	if err := r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpFileRead, agentproto.FileReadRequest{Path: target}, &result); err != nil {
		return nil, err
	}
	return agentproto.DecodeBytes(result.Content)
}

func (r *Runtime) agentWriteWorkspaceFileBytes(ctx context.Context, layout sandboxLayout, relativePath string, content []byte) error {
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	return r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpFileWrite, agentproto.FileWriteRequest{Path: target, Content: agentproto.EncodeBytes(content)}, nil)
}

func (r *Runtime) agentDeleteWorkspacePath(ctx context.Context, layout sandboxLayout, relativePath string) error {
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	return r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpFileDelete, agentproto.PathRequest{Path: target}, nil)
}

func (r *Runtime) agentMkdirWorkspace(ctx context.Context, layout sandboxLayout, relativePath string) error {
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	return r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpMkdir, agentproto.PathRequest{Path: target}, nil)
}

func (r *Runtime) agentShutdown(ctx context.Context, layout sandboxLayout) error {
	return r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpShutdown, agentproto.ShutdownRequest{}, nil)
}

func (r *Runtime) agentAttachTTY(ctx context.Context, layout sandboxLayout, req model.TTYRequest) (model.TTYHandle, error) {
	conn, err := r.agentDial(ctx, layout.agentSocketPath)
	if err != nil {
		return nil, err
	}
	requestPayload, err := json.Marshal(agentproto.PTYOpenRequest{
		Command: req.Command,
		Cwd:     req.Cwd,
		Env:     req.Env,
		Rows:    defaultInt(req.Rows, 24),
		Cols:    defaultInt(req.Cols, 80),
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := agentproto.WriteMessage(conn, agentproto.Message{Op: agentproto.OpPTYOpen, Result: requestPayload}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	message, err := agentproto.ReadMessage(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !message.OK {
		_ = conn.Close()
		return nil, errors.New(message.Error)
	}
	var opened agentproto.PTYOpenResult
	if err := json.Unmarshal(message.Result, &opened); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader, writer := io.Pipe()
	handle := &agentTTYHandle{
		conn:      conn,
		sessionID: opened.SessionID,
		reader:    reader,
		writer:    writer,
	}
	go handle.readLoop()
	return handle, nil
}

func (r *Runtime) agentRoundTrip(ctx context.Context, socketPath string, op string, request any, out any) error {
	conn, err := r.agentDial(ctx, socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	var payload json.RawMessage
	if request != nil {
		encoded, err := json.Marshal(request)
		if err != nil {
			return err
		}
		payload = encoded
	}
	if err := agentproto.WriteMessage(conn, agentproto.Message{Op: op, Result: payload}); err != nil {
		return err
	}
	message, err := agentproto.ReadMessage(conn)
	if err != nil {
		return err
	}
	if !message.OK {
		if message.Error == "" {
			message.Error = "guest agent request failed"
		}
		return errors.New(message.Error)
	}
	if out != nil && len(message.Result) > 0 {
		if err := json.Unmarshal(message.Result, out); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) agentDial(ctx context.Context, socketPath string) (net.Conn, error) {
	dialer := net.Dialer{}
	for {
		conn, err := dialer.DialContext(ctx, "unix", socketPath)
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, os.ErrNotExist) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		return nil, err
	}
}

type agentTTYHandle struct {
	conn      net.Conn
	sessionID string
	reader    *io.PipeReader
	writer    *io.PipeWriter
	closeOnce sync.Once
	closeErr  error
}

func (h *agentTTYHandle) Reader() io.Reader { return h.reader }

func (h *agentTTYHandle) Writer() io.Writer {
	return ttyWriterFunc(func(p []byte) (int, error) {
		payload, err := json.Marshal(agentproto.PTYData{SessionID: h.sessionID, Data: agentproto.EncodeBytes(p)})
		if err != nil {
			return 0, err
		}
		if err := agentproto.WriteMessage(h.conn, agentproto.Message{Op: agentproto.OpPTYData, Result: payload}); err != nil {
			return 0, err
		}
		return len(p), nil
	})
}

func (h *agentTTYHandle) Resize(req model.ResizeRequest) error {
	payload, err := json.Marshal(agentproto.PTYResizeRequest{SessionID: h.sessionID, Rows: defaultInt(req.Rows, 24), Cols: defaultInt(req.Cols, 80)})
	if err != nil {
		return err
	}
	return agentproto.WriteMessage(h.conn, agentproto.Message{Op: agentproto.OpPTYResize, Result: payload})
}

func (h *agentTTYHandle) Close() error {
	h.closeOnce.Do(func() {
		payload, _ := json.Marshal(agentproto.PTYData{SessionID: h.sessionID, EOF: true})
		_ = agentproto.WriteMessage(h.conn, agentproto.Message{Op: agentproto.OpPTYClose, Result: payload})
		h.closeErr = h.conn.Close()
		_ = h.writer.Close()
	})
	return h.closeErr
}

func (h *agentTTYHandle) readLoop() {
	defer h.writer.Close()
	for {
		message, err := agentproto.ReadMessage(h.conn)
		if err != nil {
			return
		}
		if !message.OK {
			_, _ = h.writer.Write([]byte(message.Error))
			return
		}
		var data agentproto.PTYData
		if err := json.Unmarshal(message.Result, &data); err != nil {
			return
		}
		if data.Data != "" {
			decoded, err := agentproto.DecodeBytes(data.Data)
			if err != nil {
				return
			}
			if _, err := h.writer.Write(decoded); err != nil {
				return
			}
		}
		if data.EOF {
			return
		}
	}
}

type ttyWriterFunc func([]byte) (int, error)

func (f ttyWriterFunc) Write(p []byte) (int, error) { return f(p) }

var _ model.TTYHandle = (*agentTTYHandle)(nil)
