package qemu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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

type agentSessionManager struct {
	mu       sync.Mutex
	sessions map[string]*agentSession
}

type agentSession struct {
	runtime    *Runtime
	sandboxID  string
	runtimeID  string
	pid        int
	socketPath string

	conn      net.Conn
	handshake guestHandshake

	writeMu  sync.Mutex
	mu       sync.Mutex
	pending  map[string]chan agentproto.Message
	files    map[string]chan agentproto.FileData
	execs    map[string]chan agentproto.ExecEvent
	pty      map[string]*agentTTYHandle
	bridge   map[string]*sandboxLocalConnHandle
	archive  map[string]chan agentproto.ArchiveStreamChunk
	closed   bool
	closeCh  chan struct{}
	closeErr error
}

var agentRequestCounter atomic.Uint64

const defaultAgentRoundTripTimeout = 30 * time.Second

func (r *Runtime) agentSessions() *agentSessionManager {
	if r.sessionManager == nil {
		r.sessionManager = &agentSessionManager{sessions: make(map[string]*agentSession)}
	}
	return r.sessionManager
}

func agentSessionKey(sandboxID string) string {
	return strings.TrimSpace(sandboxID)
}

func (m *agentSessionManager) invalidate(sandboxID string) {
	if m == nil {
		return
	}
	key := agentSessionKey(sandboxID)
	m.mu.Lock()
	session := m.sessions[key]
	delete(m.sessions, key)
	m.mu.Unlock()
	if session != nil {
		session.close(errors.New("agent session invalidated"))
	}
}

func (r *Runtime) invalidateAgentSession(sandboxID string) {
	r.agentSessions().invalidate(sandboxID)
}

func (r *Runtime) ensureAgentSession(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout) (*agentSession, error) {
	if r.controlModeForSandbox(sandbox) != model.GuestControlModeAgent {
		return nil, fmt.Errorf("sandbox %s is not using agent control mode", sandbox.ID)
	}
	pid, err := r.liveSandboxPID(layout)
	if err != nil {
		return nil, err
	}
	manager := r.agentSessions()
	key := agentSessionKey(sandbox.ID)

	manager.mu.Lock()
	existing := manager.sessions[key]
	if existing != nil && existing.matches(sandbox.RuntimeID, pid, layout.agentSocketPath) && !existing.isClosed() {
		manager.mu.Unlock()
		return existing, nil
	}
	if existing != nil {
		delete(manager.sessions, key)
	}
	manager.mu.Unlock()
	if existing != nil {
		existing.close(errors.New("agent session replaced"))
	}

	session, err := r.openAgentSession(ctx, sandbox, layout, pid)
	if err != nil {
		return nil, err
	}
	manager.mu.Lock()
	current := manager.sessions[key]
	if current == nil || current.isClosed() {
		manager.sessions[key] = session
		manager.mu.Unlock()
		return session, nil
	}
	manager.mu.Unlock()
	session.close(errors.New("agent session superseded"))
	if current.matches(sandbox.RuntimeID, pid, layout.agentSocketPath) && !current.isClosed() {
		return current, nil
	}
	return r.ensureAgentSession(ctx, sandbox, layout)
}

func (r *Runtime) openAgentSession(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout, pid int) (*agentSession, error) {
	conn, err := r.agentDial(ctx, layout.agentSocketPath)
	if err != nil {
		return nil, err
	}
	session := &agentSession{
		runtime:    r,
		sandboxID:  sandbox.ID,
		runtimeID:  sandbox.RuntimeID,
		pid:        pid,
		socketPath: layout.agentSocketPath,
		conn:       conn,
		pending:    make(map[string]chan agentproto.Message),
		files:      make(map[string]chan agentproto.FileData),
		execs:      make(map[string]chan agentproto.ExecEvent),
		pty:        make(map[string]*agentTTYHandle),
		bridge:     make(map[string]*sandboxLocalConnHandle),
		archive:    make(map[string]chan agentproto.ArchiveStreamChunk),
		closeCh:    make(chan struct{}),
	}
	session.handshake = fallbackGuestHandshake(sandbox)
	go session.readLoop()
	return session, nil
}

func (s *agentSession) matches(runtimeID string, pid int, socketPath string) bool {
	return s.runtimeID == runtimeID && s.pid == pid && s.socketPath == socketPath
}

func (s *agentSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *agentSession) close(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if err == nil {
		err = io.EOF
	}
	s.closeErr = err
	pending := s.pending
	fileStreams := s.files
	execStreams := s.execs
	archiveStreams := s.archive
	ptyHandles := s.pty
	bridgeHandles := s.bridge
	s.pending = nil
	s.files = nil
	s.execs = nil
	s.archive = nil
	s.pty = nil
	s.bridge = nil
	close(s.closeCh)
	s.mu.Unlock()
	_ = s.conn.Close()
	for _, ch := range pending {
		close(ch)
	}
	for _, ch := range fileStreams {
		close(ch)
	}
	for _, ch := range execStreams {
		close(ch)
	}
	for _, ch := range archiveStreams {
		close(ch)
	}
	for _, handle := range ptyHandles {
		handle.fail()
	}
	for _, handle := range bridgeHandles {
		handle.fail()
	}
}

func (s *agentSession) hello(ctx context.Context, sandbox model.Sandbox) (guestHandshake, error) {
	var result agentproto.HelloResult
	if err := s.roundTrip(ctx, agentproto.OpHello, nil, &result); err != nil {
		return guestHandshake{}, err
	}
	if result.ProtocolVersion != agentproto.ProtocolVersion {
		return guestHandshake{}, fmt.Errorf("guest agent protocol mismatch: host=%s guest=%s", agentproto.ProtocolVersion, result.ProtocolVersion)
	}
	handshake := guestHandshake{
		ProtocolVersion:          result.ProtocolVersion,
		WorkspaceContractVersion: result.WorkspaceContractVersion,
		Capabilities:             model.NormalizeCapabilities(result.Capabilities),
		MaxFileTransferBytes:     defaultGuestFileTransferMaxBytes(result.MaxFileTransferBytes),
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

func (s *agentSession) Ping(ctx context.Context) (agentproto.PingResult, error) {
	var result agentproto.PingResult
	if err := s.roundTrip(ctx, agentproto.OpPing, nil, &result); err != nil {
		return agentproto.PingResult{}, err
	}
	return result, nil
}

func (s *agentSession) readLoop() {
	for {
		message, err := agentproto.ReadMessage(s.conn)
		if err != nil {
			s.close(err)
			return
		}
		if s.deliverPending(message) {
			continue
		}
		switch message.Op {
		case agentproto.OpFileData:
			var payload agentproto.FileData
			if err := json.Unmarshal(message.Result, &payload); err != nil {
				s.close(err)
				return
			}
			s.deliverFileRetry(payload)
		case agentproto.OpExecEvent:
			var payload agentproto.ExecEvent
			if err := json.Unmarshal(message.Result, &payload); err != nil {
				s.close(err)
				return
			}
			s.deliverExecRetry(payload)
		case agentproto.OpPTYData:
			var payload agentproto.PTYData
			if err := json.Unmarshal(message.Result, &payload); err != nil {
				s.close(err)
				return
			}
			if !s.deliverPTY(payload) {
				s.close(fmt.Errorf("unexpected pty stream session %q", payload.SessionID))
				return
			}
		case agentproto.OpTCPBridgeData:
			var payload agentproto.TCPBridgeData
			if err := json.Unmarshal(message.Result, &payload); err != nil {
				s.close(err)
				return
			}
			if !s.deliverBridge(payload) {
				s.close(fmt.Errorf("unexpected bridge stream session %q", payload.SessionID))
				return
			}
		case agentproto.OpArchiveStream:
			var payload agentproto.ArchiveStreamChunk
			if err := json.Unmarshal(message.Result, &payload); err != nil {
				s.close(err)
				return
			}
			if !s.deliverArchive(payload) {
				s.close(fmt.Errorf("unexpected archive stream session %q", payload.SessionID))
				return
			}
		default:
			if strings.TrimSpace(message.ID) == "" {
				s.close(fmt.Errorf("%w: response id is required", agentproto.ErrProtocol))
			} else {
				s.close(fmt.Errorf("%w: response id mismatch: got %q", agentproto.ErrProtocol, message.ID))
			}
			return
		}
	}
}

func (s *agentSession) deliverPending(message agentproto.Message) bool {
	if strings.TrimSpace(message.ID) == "" {
		return false
	}
	s.mu.Lock()
	ch, ok := s.pending[message.ID]
	if ok {
		delete(s.pending, message.ID)
	}
	s.mu.Unlock()
	if ok {
		ch <- message
		close(ch)
	}
	return ok
}

func (s *agentSession) deliverFile(payload agentproto.FileData) bool {
	s.mu.Lock()
	ch, ok := s.files[payload.SessionID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	ch <- payload
	if payload.EOF || payload.Error != "" {
		s.unregisterFile(payload.SessionID)
	}
	return true
}

func (s *agentSession) deliverExec(payload agentproto.ExecEvent) bool {
	s.mu.Lock()
	ch, ok := s.execs[payload.ExecID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	ch <- payload
	if payload.Result != nil || payload.Error != "" {
		s.unregisterExec(payload.ExecID)
	}
	return true
}

func (s *agentSession) deliverPTY(payload agentproto.PTYData) bool {
	s.mu.Lock()
	handle, ok := s.pty[payload.SessionID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	handle.deliver(payload)
	if payload.EOF {
		s.unregisterPTY(payload.SessionID)
	}
	return true
}

func (s *agentSession) deliverBridge(payload agentproto.TCPBridgeData) bool {
	s.mu.Lock()
	handle, ok := s.bridge[payload.SessionID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	handle.deliver(payload)
	if payload.EOF || payload.Error != "" {
		s.unregisterBridge(payload.SessionID)
	}
	return true
}

func (s *agentSession) deliverArchive(payload agentproto.ArchiveStreamChunk) bool {
	s.mu.Lock()
	ch, ok := s.archive[payload.SessionID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	ch <- payload
	if payload.End || payload.Error != "" {
		s.unregisterArchive(payload.SessionID)
	}
	return true
}

// deliverExecRetry retries delivery briefly to handle the race between
// roundTrip returning the exec_start result and registerExec being called.
// The guest may send exec_event data immediately after exec_start result.
func (s *agentSession) deliverExecRetry(payload agentproto.ExecEvent) {
	for i := 0; i < 50; i++ {
		if s.deliverExec(payload) {
			return
		}
		if s.isClosed() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	// Drop the message rather than tearing down the session.
}

// deliverFileRetry retries delivery briefly to handle the race between
// roundTrip returning the file_open result and registerFile being called.
func (s *agentSession) deliverFileRetry(payload agentproto.FileData) {
	for i := 0; i < 50; i++ {
		if s.deliverFile(payload) {
			return
		}
		if s.isClosed() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	// Drop the message rather than tearing down the session.
}

func (s *agentSession) roundTrip(ctx context.Context, op string, request any, out any) error {
	if s.isClosed() {
		return s.closeErr
	}
	var payload json.RawMessage
	if request != nil {
		encoded, err := json.Marshal(request)
		if err != nil {
			return err
		}
		payload = encoded
	}
	message := agentproto.Message{ID: nextAgentRequestID(), Op: op, Result: payload}
	responseCh := make(chan agentproto.Message, 1)
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		return err
	}
	s.pending[message.ID] = responseCh
	s.mu.Unlock()
	if err := s.writeMessage(ctx, message); err != nil {
		s.mu.Lock()
		delete(s.pending, message.ID)
		s.mu.Unlock()
		return err
	}
	select {
	case response, ok := <-responseCh:
		if !ok {
			if s.closeErr != nil {
				return s.closeErr
			}
			return io.EOF
		}
		if err := agentproto.ValidateResponse(response, op, message.ID); err != nil {
			return err
		}
		if !response.OK {
			if response.Error == "" {
				response.Error = "guest agent request failed"
			}
			return errors.New(response.Error)
		}
		if out != nil && len(response.Result) > 0 {
			if err := json.Unmarshal(response.Result, out); err != nil {
				return err
			}
		}
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, message.ID)
		s.mu.Unlock()
		return ctx.Err()
	case <-s.closeCh:
		if s.closeErr != nil {
			return s.closeErr
		}
		return io.EOF
	}
}

func (s *agentSession) writeMessage(ctx context.Context, message agentproto.Message) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := applyAgentConnDeadline(s.conn, ctx, defaultAgentRoundTripTimeout); err != nil {
		return err
	}
	return agentproto.WriteMessage(s.conn, message)
}

func (s *agentSession) send(ctx context.Context, op string, payload any) error {
	var encoded json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		encoded = data
	}
	return s.writeMessage(ctx, agentproto.Message{ID: nextAgentRequestID(), Op: op, Result: encoded})
}

func (s *agentSession) registerFile(sessionID string) (chan agentproto.FileData, error) {
	ch := make(chan agentproto.FileData, 8)
	s.mu.Lock()
	if s.closed || s.files == nil {
		s.mu.Unlock()
		return nil, errors.New("agent session closed")
	}
	s.files[sessionID] = ch
	s.mu.Unlock()
	return ch, nil
}

func (s *agentSession) unregisterFile(sessionID string) {
	s.mu.Lock()
	ch, ok := s.files[sessionID]
	if ok {
		delete(s.files, sessionID)
	}
	s.mu.Unlock()
	if ok {
		close(ch)
	}
}

func (s *agentSession) registerExec(execID string) (chan agentproto.ExecEvent, error) {
	ch := make(chan agentproto.ExecEvent, 32)
	s.mu.Lock()
	if s.closed || s.execs == nil {
		s.mu.Unlock()
		return nil, errors.New("agent session closed")
	}
	s.execs[execID] = ch
	s.mu.Unlock()
	return ch, nil
}

func (s *agentSession) unregisterExec(execID string) {
	s.mu.Lock()
	ch, ok := s.execs[execID]
	if ok {
		delete(s.execs, execID)
	}
	s.mu.Unlock()
	if ok {
		close(ch)
	}
}

func (s *agentSession) registerArchive(sessionID string) (chan agentproto.ArchiveStreamChunk, error) {
	ch := make(chan agentproto.ArchiveStreamChunk, 32)
	s.mu.Lock()
	if s.closed || s.archive == nil {
		s.mu.Unlock()
		return nil, errors.New("agent session closed")
	}
	s.archive[sessionID] = ch
	s.mu.Unlock()
	return ch, nil
}

func (s *agentSession) unregisterArchive(sessionID string) {
	s.mu.Lock()
	ch, ok := s.archive[sessionID]
	if ok {
		delete(s.archive, sessionID)
	}
	s.mu.Unlock()
	if ok {
		close(ch)
	}
}

func (s *agentSession) registerPTY(handle *agentTTYHandle) error {
	s.mu.Lock()
	if s.closed || s.pty == nil {
		s.mu.Unlock()
		return errors.New("agent session closed")
	}
	s.pty[handle.sessionID] = handle
	s.mu.Unlock()
	return nil
}

func (s *agentSession) unregisterPTY(sessionID string) {
	s.mu.Lock()
	delete(s.pty, sessionID)
	s.mu.Unlock()
}

func (s *agentSession) registerBridge(handle *sandboxLocalConnHandle) error {
	s.mu.Lock()
	if s.closed || s.bridge == nil {
		s.mu.Unlock()
		return errors.New("agent session closed")
	}
	s.bridge[handle.sessionID] = handle
	s.mu.Unlock()
	return nil
}

func (s *agentSession) unregisterBridge(sessionID string) {
	s.mu.Lock()
	delete(s.bridge, sessionID)
	s.mu.Unlock()
}

func (r *Runtime) agentHandshake(ctx context.Context, layout sandboxLayout) (guestHandshake, error) {
	conn, err := r.agentDial(ctx, layout.agentSocketPath)
	if err != nil {
		return guestHandshake{}, err
	}
	defer conn.Close()
	session := &agentSession{conn: conn, pending: make(map[string]chan agentproto.Message), closeCh: make(chan struct{})}
	go session.readLoop()
	return session.hello(ctx, model.Sandbox{})
}

func (r *Runtime) agentHandshakeForSandbox(ctx context.Context, layout sandboxLayout, sandbox model.Sandbox) (guestHandshake, error) {
	conn, err := r.agentDial(ctx, layout.agentSocketPath)
	if err != nil {
		return guestHandshake{}, err
	}
	defer conn.Close()
	session := &agentSession{conn: conn, pending: make(map[string]chan agentproto.Message), closeCh: make(chan struct{})}
	go session.readLoop()
	return session.hello(ctx, sandbox)
}

func (r *Runtime) agentReady(ctx context.Context, layout sandboxLayout) error {
	conn, err := r.agentDial(ctx, layout.agentSocketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	session := &agentSession{conn: conn, pending: make(map[string]chan agentproto.Message), closeCh: make(chan struct{})}
	go session.readLoop()
	result, err := session.Ping(ctx)
	if err != nil {
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

func (r *Runtime) agentPing(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout) (agentproto.PingResult, error) {
	session, err := r.ensureAgentSession(ctx, sandbox, layout)
	if err != nil {
		return agentproto.PingResult{}, err
	}
	result, err := session.Ping(ctx)
	if err != nil {
		r.invalidateAgentSession(sandbox.ID)
		return agentproto.PingResult{}, err
	}
	return result, nil
}

func (r *Runtime) agentExec(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout, req model.ExecRequest, streams model.ExecStreams) (model.ExecHandle, error) {
	session, err := r.ensureAgentSession(ctx, sandbox, layout)
	if err != nil {
		return nil, err
	}
	payload := agentproto.ExecStartRequest{
		Command:  req.Command,
		Cwd:      req.Cwd,
		Env:      req.Env,
		Timeout:  req.Timeout,
		Detached: req.Detached,
	}
	var opened agentproto.ExecStartResult
	if err := session.roundTrip(ctx, agentproto.OpExecStart, payload, &opened); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opened.ExecID) == "" {
		return nil, fmt.Errorf("guest agent returned empty exec id")
	}
	events, err := session.registerExec(opened.ExecID)
	if err != nil {
		return nil, err
	}
	stdoutCapture := newPreviewWriter(streams.Stdout, execPreviewLimit)
	stderrCapture := newPreviewWriter(streams.Stderr, execPreviewLimit)
	handle := &agentExecHandle{
		session:   session,
		execID:    opened.ExecID,
		resultCh:  make(chan model.ExecResult, 1),
		startedAt: time.Now().UTC(),
	}
	go func() {
		defer close(handle.resultCh)
		for event := range events {
			if event.Error != "" {
				now := time.Now().UTC()
				handle.resultCh <- model.ExecResult{Status: model.ExecutionStatusFailed, StartedAt: handle.startedAt, CompletedAt: now, Duration: now.Sub(handle.startedAt), StderrPreview: event.Error}
				return
			}
			switch event.Stream {
			case "stdout":
				if event.Data != "" {
					data, err := agentproto.DecodeBytes(event.Data)
					if err != nil {
						now := time.Now().UTC()
						handle.resultCh <- model.ExecResult{Status: model.ExecutionStatusFailed, StartedAt: handle.startedAt, CompletedAt: now, Duration: now.Sub(handle.startedAt), StderrPreview: err.Error()}
						return
					}
					_, _ = stdoutCapture.Write(data)
				}
			case "stderr":
				if event.Data != "" {
					data, err := agentproto.DecodeBytes(event.Data)
					if err != nil {
						now := time.Now().UTC()
						handle.resultCh <- model.ExecResult{Status: model.ExecutionStatusFailed, StartedAt: handle.startedAt, CompletedAt: now, Duration: now.Sub(handle.startedAt), StderrPreview: err.Error()}
						return
					}
					_, _ = stderrCapture.Write(data)
				}
			}
			if event.Result != nil {
				stdoutPreview := event.Result.StdoutPreview
				if stdoutPreview == "" {
					stdoutPreview = stdoutCapture.String()
				}
				stderrPreview := event.Result.StderrPreview
				if stderrPreview == "" {
					stderrPreview = stderrCapture.String()
				}
				result := model.ExecResult{
					ExitCode:        event.Result.ExitCode,
					Status:          model.ExecutionStatus(event.Result.Status),
					StartedAt:       event.Result.StartedAt,
					CompletedAt:     event.Result.CompletedAt,
					Duration:        event.Result.CompletedAt.Sub(event.Result.StartedAt),
					StdoutPreview:   stdoutPreview,
					StderrPreview:   stderrPreview,
					StdoutTruncated: event.Result.StdoutTruncated || stdoutCapture.Truncated(),
					StderrTruncated: event.Result.StderrTruncated || stderrCapture.Truncated(),
				}
				handle.resultCh <- result
				return
			}
		}
		now := time.Now().UTC()
		handle.resultCh <- model.ExecResult{Status: model.ExecutionStatusFailed, StartedAt: handle.startedAt, CompletedAt: now, Duration: now.Sub(handle.startedAt), StderrPreview: "guest exec stream closed unexpectedly"}
	}()
	return handle, nil
}

func (r *Runtime) agentReadWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout, relativePath string) ([]byte, error) {
	maxBytes, err := r.effectiveWorkspaceFileTransferMaxBytes(ctx, sandbox, layout)
	if err != nil {
		return nil, err
	}
	return r.agentReadWorkspaceFileBytesWithLimit(ctx, sandbox, layout, relativePath, maxBytes)
}

func (r *Runtime) agentReadWorkspaceFileBytesWithLimit(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout, relativePath string, maxBytes int64) ([]byte, error) {
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return nil, err
	}
	session, err := r.ensureAgentSession(ctx, sandbox, layout)
	if err != nil {
		return nil, err
	}
	var opened agentproto.FileOpenResult
	if err := session.roundTrip(ctx, agentproto.OpFileOpen, agentproto.FileOpenRequest{Path: target, Mode: "read"}, &opened); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opened.SessionID) == "" {
		return nil, fmt.Errorf("guest agent returned empty file session id")
	}
	if maxBytes > 0 && opened.Size > maxBytes {
		return nil, model.FileTransferTooLargeError(maxBytes)
	}
	dataCh, err := session.registerFile(opened.SessionID)
	if err != nil {
		return nil, err
	}
	defer session.unregisterFile(opened.SessionID)
	var output []byte
	for packet := range dataCh {
		if packet.Error != "" {
			return nil, errors.New(packet.Error)
		}
		if packet.Data != "" {
			chunk, err := agentproto.DecodeBytes(packet.Data)
			if err != nil {
				return nil, err
			}
			if maxBytes > 0 && int64(len(output)+len(chunk)) > maxBytes {
				return nil, model.FileTransferTooLargeError(maxBytes)
			}
			output = append(output, chunk...)
		}
		if packet.EOF {
			break
		}
	}
	if err := session.roundTrip(ctx, agentproto.OpFileClose, agentproto.FileCloseRequest{SessionID: opened.SessionID}, nil); err != nil {
		// Tolerate file_close failures for read-mode sessions; the guest
		// auto-cleans read sessions after streaming all data.
		_ = err
	}
	return output, nil
}

func (r *Runtime) agentWriteWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout, relativePath string, content []byte) error {
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	maxBytes, err := r.effectiveWorkspaceFileTransferMaxBytes(ctx, sandbox, layout)
	if err != nil {
		return err
	}
	if int64(len(content)) > maxBytes {
		return model.FileTransferTooLargeError(maxBytes)
	}
	session, err := r.ensureAgentSession(ctx, sandbox, layout)
	if err != nil {
		return err
	}
	var opened agentproto.FileOpenResult
	if err := session.roundTrip(ctx, agentproto.OpFileOpen, agentproto.FileOpenRequest{Path: target, Mode: "write", Truncate: true}, &opened); err != nil {
		return err
	}
	if strings.TrimSpace(opened.SessionID) == "" {
		return fmt.Errorf("guest agent returned empty file session id")
	}
	for offset := 0; offset < len(content); offset += agentproto.MaxFileChunkSize {
		end := offset + agentproto.MaxFileChunkSize
		if end > len(content) {
			end = len(content)
		}
		if err := session.send(ctx, agentproto.OpFileData, agentproto.FileData{SessionID: opened.SessionID, Data: agentproto.EncodeBytes(content[offset:end]), EOF: end == len(content)}); err != nil {
			session.unregisterFile(opened.SessionID)
			return err
		}
	}
	if len(content) == 0 {
		if err := session.send(ctx, agentproto.OpFileData, agentproto.FileData{SessionID: opened.SessionID, EOF: true}); err != nil {
			return err
		}
	}
	return session.roundTrip(ctx, agentproto.OpFileClose, agentproto.FileCloseRequest{SessionID: opened.SessionID}, nil)
}

func (r *Runtime) effectiveWorkspaceFileTransferMaxBytes(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout) (int64, error) {
	limit := workspaceFileTransferLimit(r.workspaceFileTransferMaxBytes)
	session, err := r.ensureAgentSession(ctx, sandbox, layout)
	if err != nil {
		return 0, err
	}
	guestLimit := defaultGuestFileTransferMaxBytes(session.handshake.MaxFileTransferBytes)
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

func (r *Runtime) agentDeleteWorkspacePath(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout, relativePath string) error {
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	session, err := r.ensureAgentSession(ctx, sandbox, layout)
	if err != nil {
		return err
	}
	return session.roundTrip(ctx, agentproto.OpFileDelete, agentproto.PathRequest{Path: target}, nil)
}

func (r *Runtime) agentMkdirWorkspace(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout, relativePath string) error {
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	session, err := r.ensureAgentSession(ctx, sandbox, layout)
	if err != nil {
		return err
	}
	return session.roundTrip(ctx, agentproto.OpMkdir, agentproto.PathRequest{Path: target}, nil)
}

func (r *Runtime) agentShutdown(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout) error {
	session, err := r.ensureAgentSession(ctx, sandbox, layout)
	if err != nil {
		return err
	}
	return session.roundTrip(ctx, agentproto.OpShutdown, agentproto.ShutdownRequest{}, nil)
}

func (r *Runtime) agentAttachTTY(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout, req model.TTYRequest) (model.TTYHandle, error) {
	session, err := r.ensureAgentSession(ctx, sandbox, layout)
	if err != nil {
		return nil, err
	}
	requestPayload := agentproto.PTYOpenRequest{
		Command: req.Command,
		Cwd:     req.Cwd,
		Env:     req.Env,
		Rows:    defaultInt(req.Rows, 24),
		Cols:    defaultInt(req.Cols, 80),
	}
	var opened agentproto.PTYOpenResult
	if err := session.roundTrip(ctx, agentproto.OpPTYOpen, requestPayload, &opened); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opened.SessionID) == "" {
		return nil, fmt.Errorf("guest agent returned empty PTY session id")
	}
	reader, writer := io.Pipe()
	handle := &agentTTYHandle{session: session, sessionID: opened.SessionID, reader: reader, writer: writer}
	if err := session.registerPTY(handle); err != nil {
		return nil, err
	}
	return handle, nil
}

func (r *Runtime) streamWorkspaceArchive(ctx context.Context, sandbox model.Sandbox, paths []string) (chan agentproto.ArchiveStreamChunk, string, error) {
	session, err := r.ensureAgentSession(ctx, sandbox, layoutForSandbox(sandbox))
	if err != nil {
		return nil, "", err
	}
	var opened agentproto.ArchiveStreamStart
	if err := session.roundTrip(ctx, agentproto.OpArchiveStream, agentproto.ArchiveStreamRequest{Paths: paths}, &opened); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(opened.SessionID) == "" {
		return nil, "", fmt.Errorf("guest agent returned empty archive session id")
	}
	ch, err := session.registerArchive(opened.SessionID)
	if err != nil {
		return nil, "", err
	}
	return ch, opened.SessionID, nil
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
		if isRetryableAgentDialError(err) {
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

func isRetryableAgentDialError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) || isTimeoutError(err) {
		return true
	}
	return errors.Is(err, syscall.EAGAIN) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ENOENT)
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

type agentTTYHandle struct {
	session   *agentSession
	sessionID string
	reader    *io.PipeReader
	writer    *io.PipeWriter
	closeOnce sync.Once
	closeErr  error
}

func (h *agentTTYHandle) Reader() io.Reader { return h.reader }

func (h *agentTTYHandle) Writer() io.Writer {
	return ttyWriterFunc(func(p []byte) (int, error) {
		if h.session == nil || h.session.conn == nil {
			return len(p), nil
		}
		if err := h.session.send(context.Background(), agentproto.OpPTYData, agentproto.PTYData{SessionID: h.sessionID, Data: agentproto.EncodeBytes(p)}); err != nil {
			return 0, err
		}
		return len(p), nil
	})
}

func (h *agentTTYHandle) Resize(req model.ResizeRequest) error {
	if h.session == nil || h.session.conn == nil {
		return nil
	}
	return h.session.send(context.Background(), agentproto.OpPTYResize, agentproto.PTYResizeRequest{SessionID: h.sessionID, Rows: defaultInt(req.Rows, 24), Cols: defaultInt(req.Cols, 80)})
}

func (h *agentTTYHandle) Close() error {
	h.closeOnce.Do(func() {
		if h.session != nil {
			h.session.unregisterPTY(h.sessionID)
			if h.session.conn != nil {
				_ = h.session.send(context.Background(), agentproto.OpPTYClose, agentproto.PTYData{SessionID: h.sessionID, EOF: true})
			}
		}
		h.closeErr = h.writer.Close()
	})
	return h.closeErr
}

func (h *agentTTYHandle) fail() {
	h.closeOnce.Do(func() {
		_ = h.writer.Close()
	})
}

func (h *agentTTYHandle) deliver(data agentproto.PTYData) {
	if strings.TrimSpace(data.SessionID) == "" || data.SessionID != h.sessionID {
		h.fail()
		return
	}
	if data.Data != "" {
		decoded, err := agentproto.DecodeBytes(data.Data)
		if err != nil {
			h.fail()
			return
		}
		if _, err := h.writer.Write(decoded); err != nil {
			h.fail()
			return
		}
	}
	if data.EOF {
		h.fail()
	}
}

type ttyWriterFunc func([]byte) (int, error)

func (f ttyWriterFunc) Write(p []byte) (int, error) { return f(p) }

type sandboxLocalConnHandle struct {
	session       *agentSession
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
	session, err := r.ensureAgentSession(ctx, sandbox, layout)
	if err != nil {
		return nil, err
	}
	var opened agentproto.TCPBridgeOpenResult
	if err := session.roundTrip(ctx, agentproto.OpTCPBridgeOpen, agentproto.TCPBridgeOpenRequest{TargetPort: targetPort}, &opened); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opened.SessionID) == "" {
		return nil, fmt.Errorf("guest agent returned empty bridge session id")
	}
	reader, writer := io.Pipe()
	handle := &sandboxLocalConnHandle{
		session:   session,
		sessionID: opened.SessionID,
		reader:    reader,
		writer:    writer,
		local:     bridgeAddr("daemon"),
		remote:    bridgeAddr(fmt.Sprintf("sandbox:%s:127.0.0.1:%d", sandbox.ID, targetPort)),
	}
	if err := session.registerBridge(handle); err != nil {
		return nil, err
	}
	return handle, nil
}

func (h *sandboxLocalConnHandle) Read(p []byte) (int, error) {
	return h.runWithDeadline(h.deadline(true), func() (int, error) { return h.reader.Read(p) })
}

func (h *sandboxLocalConnHandle) Write(p []byte) (int, error) {
	return h.runWithDeadline(h.deadline(false), func() (int, error) {
		if h.session == nil || h.session.conn == nil {
			return len(p), nil
		}
		if err := h.session.send(context.Background(), agentproto.OpTCPBridgeData, agentproto.TCPBridgeData{SessionID: h.sessionID, Data: agentproto.EncodeBytes(p)}); err != nil {
			return 0, err
		}
		return len(p), nil
	})
}

func (h *sandboxLocalConnHandle) Close() error {
	h.closeOnce.Do(func() {
		if h.session != nil {
			h.session.unregisterBridge(h.sessionID)
			if h.session.conn != nil {
				_ = h.session.send(context.Background(), agentproto.OpTCPBridgeData, agentproto.TCPBridgeData{SessionID: h.sessionID, EOF: true})
			}
		}
		h.closeErr = h.writer.Close()
	})
	return h.closeErr
}

func (h *sandboxLocalConnHandle) fail() {
	h.closeOnce.Do(func() {
		_ = h.writer.Close()
	})
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

func (h *sandboxLocalConnHandle) deliver(data agentproto.TCPBridgeData) {
	if strings.TrimSpace(data.SessionID) == "" || data.SessionID != h.sessionID {
		h.fail()
		return
	}
	if data.Error != "" {
		h.fail()
		return
	}
	if data.Data != "" {
		decoded, err := agentproto.DecodeBytes(data.Data)
		if err != nil {
			h.fail()
			return
		}
		if _, err := h.writer.Write(decoded); err != nil {
			h.fail()
			return
		}
	}
	if data.EOF {
		h.fail()
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

type agentExecHandle struct {
	session    *agentSession
	execID     string
	startedAt  time.Time
	resultCh   chan model.ExecResult
	cancelOnce sync.Once
	cancelErr  error
}

func (h *agentExecHandle) Wait() model.ExecResult {
	result, ok := <-h.resultCh
	if !ok {
		now := time.Now().UTC()
		return model.ExecResult{Status: model.ExecutionStatusFailed, StartedAt: h.startedAt, CompletedAt: now, Duration: now.Sub(h.startedAt), StderrPreview: "guest exec stream closed unexpectedly"}
	}
	return result
}

func (h *agentExecHandle) Cancel() error {
	h.cancelOnce.Do(func() {
		h.cancelErr = h.session.roundTrip(context.Background(), agentproto.OpExecCancel, agentproto.ExecCancelRequest{ExecID: h.execID}, nil)
	})
	return h.cancelErr
}

func applyAgentConnDeadline(conn net.Conn, ctx context.Context, fallback time.Duration) error {
	deadline := time.Now().Add(fallback)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	return conn.SetDeadline(deadline)
}

func nextAgentRequestID() string {
	return fmt.Sprintf("host-%d", agentRequestCounter.Add(1))
}

func fallbackGuestHandshake(sandbox model.Sandbox) guestHandshake {
	protocolVersion := strings.TrimSpace(sandbox.ControlProtocolVersion)
	if protocolVersion == "" {
		protocolVersion = agentproto.ProtocolVersion
	}
	workspaceVersion, capabilities := expectedAgentHandshakeForSandbox(sandbox)
	return guestHandshake{
		ProtocolVersion:          protocolVersion,
		WorkspaceContractVersion: workspaceVersion,
		Capabilities:             capabilities,
		MaxFileTransferBytes:     model.DefaultWorkspaceFileTransferMaxBytes,
	}
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

func (r *Runtime) workspaceDiskLabelForSandbox(sandboxID string) string {
	base := strings.TrimPrefix(filepath.Base(strings.TrimSpace(sandboxID)), "sbx-")
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= '0' && r <= '9':
			return r
		default:
			return -1
		}
	}, base)
	if len(base) > 10 {
		base = base[:10]
	}
	if base == "" {
		base = "workspace"
	}
	return "or3-" + base
}

var _ model.TTYHandle = (*agentTTYHandle)(nil)
var _ net.Conn = (*sandboxLocalConnHandle)(nil)
var _ model.ExecHandle = (*agentExecHandle)(nil)
