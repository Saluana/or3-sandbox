package qemu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"or3-sandbox/internal/guestimage"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/runtime/qemu/agentproto"
)

type guestHandshake struct {
	ProtocolVersion          string
	WorkspaceContractVersion string
	Capabilities             []string
	MaxFileTransferBytes     int64
}

var agentRequestCounter atomic.Uint64

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
		Capabilities:             model.NormalizeCapabilities(result.Capabilities),
		MaxFileTransferBytes:     defaultGuestFileTransferMaxBytes(result.MaxFileTransferBytes),
	}, nil
}

func (r *Runtime) agentHandshakeForSandbox(ctx context.Context, layout sandboxLayout, sandbox model.Sandbox) (guestHandshake, error) {
	handshake, err := r.agentHandshake(ctx, layout)
	if err != nil {
		return guestHandshake{}, err
	}
	if expectedProtocol := strings.TrimSpace(sandbox.ControlProtocolVersion); expectedProtocol != "" && handshake.ProtocolVersion != expectedProtocol {
		return guestHandshake{}, fmt.Errorf("guest agent protocol mismatch: host=%s guest=%s", expectedProtocol, handshake.ProtocolVersion)
	}
	expectedWorkspaceVersion, expectedCapabilities := expectedAgentHandshakeForSandbox(sandbox)
	if expectedWorkspaceVersion != "" && handshake.WorkspaceContractVersion != expectedWorkspaceVersion {
		return guestHandshake{}, fmt.Errorf("guest workspace contract mismatch: host=%s guest=%s", expectedWorkspaceVersion, handshake.WorkspaceContractVersion)
	}
	if len(expectedCapabilities) > 0 {
		got := strings.Join(handshake.Capabilities, ",")
		want := strings.Join(expectedCapabilities, ",")
		if got != want {
			return guestHandshake{}, fmt.Errorf("guest agent capabilities mismatch: host=%s guest=%s", want, got)
		}
	}
	return handshake, nil
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
	maxBytes, err := r.effectiveWorkspaceFileTransferMaxBytes(ctx, layout)
	if err != nil {
		return nil, err
	}
	var output []byte
	var offset int64
	for {
		var result agentproto.FileReadResult
		if err := r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpFileRead, agentproto.FileReadRequest{
			Path:     target,
			Offset:   offset,
			MaxBytes: agentproto.MaxFileChunkSize,
		}, &result); err != nil {
			return nil, err
		}
		if result.Offset != offset {
			return nil, fmt.Errorf("guest agent returned unexpected file offset: host=%d guest=%d", offset, result.Offset)
		}
		if result.Size > maxBytes {
			return nil, model.FileTransferTooLargeError(maxBytes)
		}
		chunk, err := agentproto.DecodeBytes(result.Content)
		if err != nil {
			return nil, err
		}
		if int64(len(output)+len(chunk)) > maxBytes {
			return nil, model.FileTransferTooLargeError(maxBytes)
		}
		output = append(output, chunk...)
		offset += int64(len(chunk))
		if result.EOF {
			return output, nil
		}
	}
}

func (r *Runtime) agentWriteWorkspaceFileBytes(ctx context.Context, layout sandboxLayout, relativePath string, content []byte) error {
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	maxBytes, err := r.effectiveWorkspaceFileTransferMaxBytes(ctx, layout)
	if err != nil {
		return err
	}
	if int64(len(content)) > maxBytes {
		return model.FileTransferTooLargeError(maxBytes)
	}
	if len(content) == 0 {
		return r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpFileWrite, agentproto.FileWriteRequest{
			Path:      target,
			Offset:    0,
			TotalSize: 0,
			Truncate:  true,
			EOF:       true,
		}, nil)
	}
	totalSize := int64(len(content))
	for offset := 0; offset < len(content); offset += agentproto.MaxFileChunkSize {
		end := offset + agentproto.MaxFileChunkSize
		if end > len(content) {
			end = len(content)
		}
		if err := r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpFileWrite, agentproto.FileWriteRequest{
			Path:      target,
			Content:   agentproto.EncodeBytes(content[offset:end]),
			Offset:    int64(offset),
			TotalSize: totalSize,
			Truncate:  offset == 0,
			EOF:       end == len(content),
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) effectiveWorkspaceFileTransferMaxBytes(ctx context.Context, layout sandboxLayout) (int64, error) {
	limit := workspaceFileTransferLimit(r.workspaceFileTransferMaxBytes)
	handshake, err := r.agentHandshake(ctx, layout)
	if err != nil {
		return 0, err
	}
	guestLimit := defaultGuestFileTransferMaxBytes(handshake.MaxFileTransferBytes)
	if guestLimit < limit {
		return guestLimit, nil
	}
	return limit, nil
}

func defaultGuestFileTransferMaxBytes(value int64) int64 {
	if value <= 0 {
		return model.DefaultWorkspaceFileTransferMaxBytes
	}
	return value
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
	requestID := nextAgentRequestID()
	if err := agentproto.WriteMessage(conn, agentproto.Message{ID: requestID, Op: agentproto.OpPTYOpen, Result: requestPayload}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	message, err := agentproto.ReadMessage(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := agentproto.ValidateResponse(message, agentproto.OpPTYOpen, requestID); err != nil {
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
	if strings.TrimSpace(opened.SessionID) == "" {
		_ = conn.Close()
		return nil, fmt.Errorf("guest agent returned empty PTY session id")
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
	requestID := nextAgentRequestID()
	if err := agentproto.WriteMessage(conn, agentproto.Message{ID: requestID, Op: op, Result: payload}); err != nil {
		return err
	}
	message, err := agentproto.ReadMessage(conn)
	if err != nil {
		return err
	}
	if err := agentproto.ValidateResponse(message, op, requestID); err != nil {
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

func nextAgentRequestID() string {
	return fmt.Sprintf("host-%d", agentRequestCounter.Add(1))
}

func expectedAgentHandshakeForSandbox(sandbox model.Sandbox) (string, []string) {
	workspaceVersion := strings.TrimSpace(sandbox.WorkspaceContractVersion)
	capabilities := model.NormalizeCapabilities(sandbox.Capabilities)
	if strings.TrimSpace(sandbox.BaseImageRef) == "" {
		return workspaceVersion, capabilities
	}
	contract, err := guestimage.Load(sandbox.BaseImageRef)
	if err != nil {
		return workspaceVersion, capabilities
	}
	if workspaceVersion == "" {
		workspaceVersion = contract.WorkspaceContractVersion
	}
	if len(capabilities) == 0 {
		capabilities = model.NormalizeCapabilities(contract.Capabilities)
	}
	return workspaceVersion, capabilities
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
		if err := agentproto.WriteMessage(h.conn, agentproto.Message{ID: nextAgentRequestID(), Op: agentproto.OpPTYData, Result: payload}); err != nil {
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
	return agentproto.WriteMessage(h.conn, agentproto.Message{ID: nextAgentRequestID(), Op: agentproto.OpPTYResize, Result: payload})
}

func (h *agentTTYHandle) Close() error {
	h.closeOnce.Do(func() {
		payload, _ := json.Marshal(agentproto.PTYData{SessionID: h.sessionID, EOF: true})
		_ = agentproto.WriteMessage(h.conn, agentproto.Message{ID: nextAgentRequestID(), Op: agentproto.OpPTYClose, Result: payload})
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
			_ = h.conn.Close()
			return
		}
		if message.Op != agentproto.OpPTYData {
			_ = h.conn.Close()
			return
		}
		var data agentproto.PTYData
		if err := json.Unmarshal(message.Result, &data); err != nil {
			return
		}
		if strings.TrimSpace(data.SessionID) == "" || data.SessionID != h.sessionID {
			_ = h.conn.Close()
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

type sandboxLocalConnHandle struct {
	conn          net.Conn
	sessionID     string
	reader        *io.PipeReader
	writer        *io.PipeWriter
	local         net.Addr
	remote        net.Addr
	closeOnce     sync.Once
	closeErr      error
	mu            sync.RWMutex
	readDeadline  time.Time
	writeDeadline time.Time
}

func (r *Runtime) OpenSandboxLocalConn(ctx context.Context, sandbox model.Sandbox, targetPort int) (net.Conn, error) {
	if targetPort < 1 || targetPort > 65535 {
		return nil, fmt.Errorf("target port must be between 1 and 65535")
	}
	if r.controlModeForSandbox(sandbox) != model.GuestControlModeAgent {
		return nil, fmt.Errorf("sandbox-local bridge requires agent control mode")
	}
	layout := layoutForSandbox(sandbox)
	conn, err := r.agentDial(ctx, layout.agentSocketPath)
	if err != nil {
		return nil, err
	}
	requestID := nextAgentRequestID()
	payload, err := json.Marshal(agentproto.TCPBridgeOpenRequest{TargetPort: targetPort})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := agentproto.WriteMessage(conn, agentproto.Message{ID: requestID, Op: agentproto.OpTCPBridgeOpen, Result: payload}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	message, err := agentproto.ReadMessage(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := agentproto.ValidateResponse(message, agentproto.OpTCPBridgeOpen, requestID); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !message.OK {
		_ = conn.Close()
		return nil, errors.New(message.Error)
	}
	var opened agentproto.TCPBridgeOpenResult
	if err := json.Unmarshal(message.Result, &opened); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if strings.TrimSpace(opened.SessionID) == "" {
		_ = conn.Close()
		return nil, fmt.Errorf("guest agent returned empty bridge session id")
	}
	reader, writer := io.Pipe()
	handle := &sandboxLocalConnHandle{
		conn:      conn,
		sessionID: opened.SessionID,
		reader:    reader,
		writer:    writer,
		local:     bridgeAddr("daemon"),
		remote:    bridgeAddr(fmt.Sprintf("sandbox:%s:127.0.0.1:%d", sandbox.ID, targetPort)),
	}
	go handle.readLoop()
	return handle, nil
}

func (h *sandboxLocalConnHandle) Read(p []byte) (int, error) {
	return h.runWithDeadline(h.deadline(true), func() (int, error) {
		return h.reader.Read(p)
	})
}

func (h *sandboxLocalConnHandle) Write(p []byte) (int, error) {
	return h.runWithDeadline(h.deadline(false), func() (int, error) {
		payload, err := json.Marshal(agentproto.TCPBridgeData{SessionID: h.sessionID, Data: agentproto.EncodeBytes(p)})
		if err != nil {
			return 0, err
		}
		if err := agentproto.WriteMessage(h.conn, agentproto.Message{ID: nextAgentRequestID(), Op: agentproto.OpTCPBridgeData, Result: payload}); err != nil {
			return 0, err
		}
		return len(p), nil
	})
}

func (h *sandboxLocalConnHandle) Close() error {
	h.closeOnce.Do(func() {
		payload, _ := json.Marshal(agentproto.TCPBridgeData{SessionID: h.sessionID, EOF: true})
		_ = agentproto.WriteMessage(h.conn, agentproto.Message{ID: nextAgentRequestID(), Op: agentproto.OpTCPBridgeData, Result: payload})
		h.closeErr = h.conn.Close()
		_ = h.writer.Close()
	})
	return h.closeErr
}

func (h *sandboxLocalConnHandle) LocalAddr() net.Addr  { return h.local }
func (h *sandboxLocalConnHandle) RemoteAddr() net.Addr { return h.remote }

func (h *sandboxLocalConnHandle) SetDeadline(deadline time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.readDeadline = deadline
	h.writeDeadline = deadline
	return nil
}

func (h *sandboxLocalConnHandle) SetReadDeadline(deadline time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.readDeadline = deadline
	return nil
}

func (h *sandboxLocalConnHandle) SetWriteDeadline(deadline time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writeDeadline = deadline
	return nil
}

func (h *sandboxLocalConnHandle) readLoop() {
	defer h.writer.Close()
	for {
		message, err := agentproto.ReadMessage(h.conn)
		if err != nil {
			return
		}
		if !message.OK || message.Op != agentproto.OpTCPBridgeData {
			return
		}
		var data agentproto.TCPBridgeData
		if err := json.Unmarshal(message.Result, &data); err != nil {
			return
		}
		if strings.TrimSpace(data.SessionID) == "" || data.SessionID != h.sessionID {
			_ = h.conn.Close()
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

func (h *sandboxLocalConnHandle) deadline(read bool) time.Time {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if read {
		return h.readDeadline
	}
	return h.writeDeadline
}

func (h *sandboxLocalConnHandle) runWithDeadline(deadline time.Time, fn func() (int, error)) (int, error) {
	if deadline.IsZero() {
		return fn()
	}
	wait := time.Until(deadline)
	if wait <= 0 {
		_ = h.Close()
		return 0, deadlineExceededError{}
	}
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := fn()
		done <- result{n: n, err: err}
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case res := <-done:
		return res.n, res.err
	case <-timer.C:
		_ = h.Close()
		return 0, deadlineExceededError{}
	}
}

type deadlineExceededError struct{}

func (deadlineExceededError) Error() string   { return "i/o timeout" }
func (deadlineExceededError) Timeout() bool   { return true }
func (deadlineExceededError) Temporary() bool { return true }

type bridgeAddr string

func (a bridgeAddr) Network() string { return "tcp" }
func (a bridgeAddr) String() string  { return string(a) }

var _ model.TTYHandle = (*agentTTYHandle)(nil)
var _ net.Conn = (*sandboxLocalConnHandle)(nil)
