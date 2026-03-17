package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"

	"or3-sandbox/internal/model"
	"or3-sandbox/internal/runtime/qemu/agentproto"
)

const (
	defaultPortPath        = "/dev/virtio-ports/org.or3.guest_agent"
	defaultProfileManifest = "/etc/or3/profile-manifest.json"
	defaultWorkloadUser    = "sandbox"
	readyMarkerPath        = "/var/lib/or3/bootstrap.ready"
	previewLimit           = 64 * 1024
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	agent := &guestAgent{
		portPath:            env("OR3_GUEST_AGENT_PORT_PATH", defaultPortPath),
		profileManifestPath: env("OR3_GUEST_AGENT_PROFILE_MANIFEST", defaultProfileManifest),
		workloadUser:        env("OR3_GUEST_AGENT_WORKLOAD_USER", defaultWorkloadUser),
	}
	if err := agent.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type guestAgent struct {
	portPath             string
	profileManifestPath  string
	workloadUser         string
	workspaceContract    string
	capabilities         []string
	allowedOps           map[string]struct{}
	messageCounter       atomic.Uint64
	workloadIdentity     workloadIdentity
	workloadIdentityErr  error
	workloadIdentityOnce sync.Once
}

type guestProfileManifest struct {
	Capabilities             []string `json:"capabilities"`
	WorkspaceContractVersion string   `json:"workspace_contract_version"`
	Control                  struct {
		Mode string `json:"mode"`
	} `json:"control"`
}

type workloadIdentity struct {
	Username string
	UID      uint32
	GID      uint32
	HomeDir  string
}

type guestSession struct {
	agent  *guestAgent
	ctx    context.Context
	cancel context.CancelFunc
	conn   io.ReadWriter

	writeMu sync.Mutex
	mu      sync.Mutex
	files   map[string]*guestFileSession
	execs   map[string]*guestExecSession
	pty     map[string]*guestPTYSession
	bridge  map[string]*guestTCPBridgeSession
}

type guestFileSession struct {
	id    string
	mode  string
	path  string
	file  *os.File
	close func() error
	mu    sync.Mutex
	err   error
}

type guestExecSession struct {
	id        string
	command   *exec.Cmd
	startedAt time.Time
	cancel    context.CancelFunc
	detached  bool
}

type guestPTYSession struct {
	id   string
	cmd  *exec.Cmd
	ptmx *os.File
}

type guestTCPBridgeSession struct {
	id   string
	conn net.Conn
}

type testFileOpRequest struct {
	Op     string `json:"op"`
	Target string `json:"target"`
	Read   struct {
		MaxBytes int `json:"max_bytes,omitempty"`
	} `json:"read,omitempty"`
	Write struct {
		Truncate bool `json:"truncate,omitempty"`
		EOF      bool `json:"eof,omitempty"`
	} `json:"write,omitempty"`
	Data string `json:"data,omitempty"`
}

type testFileOpResponse struct {
	Read struct {
		Content string `json:"content,omitempty"`
		EOF     bool   `json:"eof,omitempty"`
	} `json:"read,omitempty"`
}

func (a *guestAgent) run(ctx context.Context) error {
	if err := a.loadCapabilities(); err != nil {
		return err
	}

	// Open the virtio-serial port once and keep the fd for the lifetime
	// of the process.  Closing and re-opening the device between sessions
	// creates a window during which incoming host data is silently
	// discarded by the kernel virtio-serial driver.
	var file *os.File
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if file == nil {
			var err error
			file, err = os.OpenFile(a.portPath, os.O_RDWR, 0)
			if err != nil {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(250 * time.Millisecond):
				}
				continue
			}
		}
		err := a.serveSession(ctx, file)
		if err != nil {
			// Real error — close the fd and re-open on next iteration.
			file.Close()
			file = nil
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		// EOF (host disconnected): keep the fd open and loop immediately.
	}
}

func (a *guestAgent) serveSession(ctx context.Context, conn io.ReadWriter) error {
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	session := &guestSession{
		agent:  a,
		ctx:    sessionCtx,
		cancel: cancel,
		conn:   conn,
		files:  make(map[string]*guestFileSession),
		execs:  make(map[string]*guestExecSession),
		pty:    make(map[string]*guestPTYSession),
		bridge: make(map[string]*guestTCPBridgeSession),
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message, err := agentproto.ReadMessage(conn)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := agentproto.ValidateRequest(message); err != nil {
			if writeErr := session.write(agentproto.Message{ID: message.ID, Op: message.Op, OK: false, Error: err.Error()}); writeErr != nil {
				return writeErr
			}
			continue
		}
		if err := session.handle(message); err != nil {
			if agentproto.RequiresRequestID(message.Op) {
				if writeErr := session.write(agentproto.Message{ID: message.ID, Op: message.Op, OK: false, Error: err.Error()}); writeErr != nil {
					return writeErr
				}
			}
		}
	}
}

func (a *guestAgent) loadCapabilities() error {
	data, err := os.ReadFile(a.profileManifestPath)
	if err != nil {
		return fmt.Errorf("load guest profile manifest %q: %w", a.profileManifestPath, err)
	}
	var manifest guestProfileManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse guest profile manifest %q: %w", a.profileManifestPath, err)
	}
	a.capabilities = model.NormalizeCapabilities(manifest.Capabilities)
	a.workspaceContract = defaultString(manifest.WorkspaceContractVersion, model.DefaultWorkspaceContractVersion)
	a.allowedOps = allowedOpsForManifest(manifest)
	return nil
}

func allowedOpsForManifest(manifest guestProfileManifest) map[string]struct{} {
	allowed := map[string]struct{}{
		agentproto.OpHello:    {},
		agentproto.OpPing:     {},
		agentproto.OpShutdown: {},
	}
	if strings.EqualFold(strings.TrimSpace(manifest.Control.Mode), string(model.GuestControlModeAgent)) {
		allowed[agentproto.OpTCPBridgeOpen] = struct{}{}
		allowed[agentproto.OpTCPBridgeData] = struct{}{}
	}
	for _, capability := range model.NormalizeCapabilities(manifest.Capabilities) {
		switch capability {
		case "exec":
			allowed[agentproto.OpExecStart] = struct{}{}
			allowed[agentproto.OpExecCancel] = struct{}{}
		case "files":
			allowed[agentproto.OpFileOpen] = struct{}{}
			allowed[agentproto.OpFileData] = struct{}{}
			allowed[agentproto.OpFileClose] = struct{}{}
			allowed[agentproto.OpFileDelete] = struct{}{}
			allowed[agentproto.OpMkdir] = struct{}{}
			allowed[agentproto.OpArchiveStream] = struct{}{}
		case "pty":
			allowed[agentproto.OpPTYOpen] = struct{}{}
			allowed[agentproto.OpPTYData] = struct{}{}
			allowed[agentproto.OpPTYResize] = struct{}{}
			allowed[agentproto.OpPTYClose] = struct{}{}
		}
	}
	return allowed
}

func (a *guestAgent) nextMessageID() string {
	return fmt.Sprintf("guest-%d", a.messageCounter.Add(1))
}

func (a *guestAgent) allows(op string) bool {
	_, ok := a.allowedOps[op]
	return ok
}

func (a *guestAgent) workloadIdentityInfo() (workloadIdentity, error) {
	a.workloadIdentityOnce.Do(func() {
		a.workloadIdentity, a.workloadIdentityErr = lookupWorkloadIdentity(a.workloadUser)
	})
	return a.workloadIdentity, a.workloadIdentityErr
}

func (a *guestAgent) handle(ctx context.Context, message agentproto.Message) (agentproto.Message, error) {
	if !a.allows(message.Op) {
		return agentproto.Message{}, fmt.Errorf("guest profile does not allow operation %q", message.Op)
	}
	switch message.Op {
	case agentproto.OpHello:
		result, err := json.Marshal(agentproto.HelloResult{
			ProtocolVersion:          agentproto.ProtocolVersion,
			WorkspaceContractVersion: a.workspaceContract,
			Capabilities:             append([]string(nil), a.capabilities...),
			MaxFileTransferBytes:     agentproto.MaxFileTransferSize,
			Ready:                    isReady(),
		})
		return agentproto.Message{ID: message.ID, Op: message.Op, OK: true, Result: result}, err
	case agentproto.OpPing:
		ready := isReady()
		reason := ""
		if !ready {
			reason = "bootstrap marker not present"
		}
		result, err := json.Marshal(agentproto.PingResult{Ready: ready, Reason: reason})
		return agentproto.Message{ID: message.ID, Op: message.Op, OK: true, Result: result}, err
	default:
		return agentproto.Message{}, fmt.Errorf("direct test handler does not support operation %q", message.Op)
	}
}

func (s *guestSession) write(message agentproto.Message) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return agentproto.WriteMessage(s.conn, message)
}

func (s *guestSession) writeJSON(id, op string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.write(agentproto.Message{ID: id, Op: op, OK: true, Result: data})
}

func (s *guestSession) writeStream(op string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.write(agentproto.Message{ID: s.agent.nextMessageID(), Op: op, OK: true, Result: data})
}

func (s *guestSession) handle(message agentproto.Message) error {
	if !s.agent.allows(message.Op) {
		return fmt.Errorf("guest profile does not allow operation %q", message.Op)
	}
	switch message.Op {
	case agentproto.OpHello:
		return s.writeJSON(message.ID, message.Op, agentproto.HelloResult{
			ProtocolVersion:          agentproto.ProtocolVersion,
			WorkspaceContractVersion: s.agent.workspaceContract,
			Capabilities:             append([]string(nil), s.agent.capabilities...),
			MaxFileTransferBytes:     agentproto.MaxFileTransferSize,
			Ready:                    isReady(),
		})
	case agentproto.OpPing:
		ready := isReady()
		reason := ""
		if !ready {
			reason = "bootstrap marker not present"
		}
		return s.writeJSON(message.ID, message.Op, agentproto.PingResult{Ready: ready, Reason: reason})
	case agentproto.OpFileOpen:
		var req agentproto.FileOpenRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		return s.handleFileOpen(message.ID, req)
	case agentproto.OpFileData:
		var req agentproto.FileData
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		return s.handleFileData(req)
	case agentproto.OpFileClose:
		var req agentproto.FileCloseRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		return s.handleFileClose(message.ID, req)
	case agentproto.OpFileDelete:
		var req agentproto.PathRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		target, err := workspacePath(req.Path)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		return s.write(agentproto.Message{ID: message.ID, Op: message.Op, OK: true})
	case agentproto.OpMkdir:
		var req agentproto.PathRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		target, err := workspacePath(req.Path)
		if err != nil {
			return err
		}
		identity, err := s.agent.workloadIdentityInfo()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		_ = os.Chown(target, int(identity.UID), int(identity.GID))
		return s.write(agentproto.Message{ID: message.ID, Op: message.Op, OK: true})
	case agentproto.OpExecStart:
		var req agentproto.ExecStartRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		return s.handleExecStart(message.ID, req)
	case agentproto.OpExecCancel:
		var req agentproto.ExecCancelRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		return s.handleExecCancel(message.ID, req)
	case agentproto.OpPTYOpen:
		var req agentproto.PTYOpenRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		return s.handlePTYOpen(message.ID, req)
	case agentproto.OpPTYData:
		var req agentproto.PTYData
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		return s.handlePTYData(req)
	case agentproto.OpPTYResize:
		var req agentproto.PTYResizeRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		return s.handlePTYResize(req)
	case agentproto.OpPTYClose:
		var req agentproto.PTYData
		if len(message.Result) > 0 {
			if err := json.Unmarshal(message.Result, &req); err != nil {
				return err
			}
		}
		return s.handlePTYClose(req.SessionID)
	case agentproto.OpTCPBridgeOpen:
		var req agentproto.TCPBridgeOpenRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		return s.handleTCPBridgeOpen(message.ID, req)
	case agentproto.OpTCPBridgeData:
		var req agentproto.TCPBridgeData
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		return s.handleTCPBridgeData(req)
	case agentproto.OpArchiveStream:
		var req agentproto.ArchiveStreamRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return err
		}
		return s.handleArchiveStream(message.ID, req)
	case agentproto.OpShutdown:
		go func() {
			_ = exec.Command("/sbin/poweroff").Run()
		}()
		return s.write(agentproto.Message{ID: message.ID, Op: message.Op, OK: true})
	default:
		return fmt.Errorf("unsupported guest agent operation %q", message.Op)
	}
}

func (s *guestSession) handleFileOpen(requestID string, req agentproto.FileOpenRequest) error {
	target, err := workspacePath(req.Path)
	if err != nil {
		return err
	}
	identity, err := s.agent.workloadIdentityInfo()
	if err != nil {
		return err
	}
	sessionID := fmt.Sprintf("file-%d", time.Now().UTC().UnixNano())
	fileSession := &guestFileSession{id: sessionID, mode: req.Mode, path: target}
	s.mu.Lock()
	s.files[sessionID] = fileSession
	s.mu.Unlock()
	switch req.Mode {
	case "read":
		file, err := os.Open(target)
		if err != nil {
			s.mu.Lock()
			delete(s.files, sessionID)
			s.mu.Unlock()
			return err
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			s.mu.Lock()
			delete(s.files, sessionID)
			s.mu.Unlock()
			return err
		}
		fileSession.file = file
		fileSession.close = file.Close
		if err := s.writeJSON(requestID, agentproto.OpFileOpen, agentproto.FileOpenResult{SessionID: sessionID, Size: info.Size()}); err != nil {
			return err
		}
		go s.streamFileRead(fileSession)
		return nil
	case "write":
		if err := os.MkdirAll(path.Dir(target), 0o755); err != nil {
			s.mu.Lock()
			delete(s.files, sessionID)
			s.mu.Unlock()
			return err
		}
		flags := os.O_CREATE | os.O_WRONLY
		if req.Truncate {
			flags |= os.O_TRUNC
		}
		file, err := os.OpenFile(target, flags, 0o644)
		if err != nil {
			s.mu.Lock()
			delete(s.files, sessionID)
			s.mu.Unlock()
			return err
		}
		_ = os.Chown(target, int(identity.UID), int(identity.GID))
		fileSession.file = file
		fileSession.close = file.Close
		return s.writeJSON(requestID, agentproto.OpFileOpen, agentproto.FileOpenResult{SessionID: sessionID})
	default:
		s.mu.Lock()
		delete(s.files, sessionID)
		s.mu.Unlock()
		return fmt.Errorf("unsupported file stream mode %q", req.Mode)
	}
}

func (s *guestSession) streamFileRead(fileSession *guestFileSession) {
	defer func() {
		fileSession.mu.Lock()
		if fileSession.close != nil {
			_ = fileSession.close()
		}
		fileSession.mu.Unlock()
		s.mu.Lock()
		delete(s.files, fileSession.id)
		s.mu.Unlock()
	}()
	buf := make([]byte, agentproto.MaxFileChunkSize)
	for {
		n, err := fileSession.file.Read(buf)
		payload := agentproto.FileData{SessionID: fileSession.id, EOF: errors.Is(err, io.EOF)}
		if n > 0 {
			payload.Data = agentproto.EncodeBytes(buf[:n])
		}
		if err != nil && !errors.Is(err, io.EOF) {
			payload.Error = err.Error()
			payload.EOF = true
		}
		if writeErr := s.writeStream(agentproto.OpFileData, payload); writeErr != nil {
			return
		}
		if payload.EOF {
			return
		}
	}
}

func (s *guestSession) handleFileData(req agentproto.FileData) error {
	s.mu.Lock()
	fileSession, ok := s.files[req.SessionID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown file session %q", req.SessionID)
	}
	if fileSession.mode != "write" {
		return fmt.Errorf("file session %q is not writable", req.SessionID)
	}
	fileSession.mu.Lock()
	defer fileSession.mu.Unlock()
	if fileSession.err != nil {
		return nil
	}
	if req.Data != "" {
		data, err := agentproto.DecodeBytes(req.Data)
		if err != nil {
			fileSession.err = err
			return nil
		}
		if _, err := fileSession.file.Write(data); err != nil {
			fileSession.err = err
			return nil
		}
	}
	if req.EOF {
		if err := fileSession.file.Sync(); err != nil {
			fileSession.err = err
		}
	}
	return nil
}

func (s *guestSession) handleFileClose(requestID string, req agentproto.FileCloseRequest) error {
	s.mu.Lock()
	fileSession, ok := s.files[req.SessionID]
	if ok {
		delete(s.files, req.SessionID)
	}
	s.mu.Unlock()
	if !ok {
		// Session may have been auto-cleaned by streamFileRead; treat as success.
		return s.write(agentproto.Message{ID: requestID, Op: agentproto.OpFileClose, OK: true})
	}
	fileSession.mu.Lock()
	defer fileSession.mu.Unlock()
	if fileSession.close != nil {
		_ = fileSession.close()
	}
	if fileSession.err != nil {
		return fileSession.err
	}
	return s.write(agentproto.Message{ID: requestID, Op: agentproto.OpFileClose, OK: true})
}

func (s *guestSession) handleExecStart(requestID string, req agentproto.ExecStartRequest) error {
	identity, err := s.agent.workloadIdentityInfo()
	if err != nil {
		return err
	}
	command := req.Command
	if len(command) == 0 {
		command = []string{"sh", "-lc", "pwd"}
	}
	execID := fmt.Sprintf("exec-%d", time.Now().UTC().UnixNano())
	runCtx := s.ctx
	cancel := func() {}
	if req.Detached {
		runCtx, cancel = context.WithCancel(context.Background())
	} else if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(s.ctx, req.Timeout)
	}
	cmd := exec.CommandContext(runCtx, command[0], command[1:]...)
	cmd.Dir = defaultString(req.Cwd, "/workspace")
	cmd.Env = append(os.Environ(), flattenEnv(req.Env)...)
	configureWorkloadCommand(cmd, identity)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return err
	}
	startedAt := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	s.mu.Lock()
	s.execs[execID] = &guestExecSession{id: execID, command: cmd, startedAt: startedAt, cancel: cancel, detached: req.Detached}
	s.mu.Unlock()
	if err := s.writeJSON(requestID, agentproto.OpExecStart, agentproto.ExecStartResult{ExecID: execID}); err != nil {
		return err
	}
	go s.streamExec(execID, cmd, runCtx, startedAt, stdoutPipe, stderrPipe, req.Detached, cancel)
	return nil
}

func (s *guestSession) streamExec(execID string, cmd *exec.Cmd, runCtx context.Context, startedAt time.Time, stdout io.ReadCloser, stderr io.ReadCloser, detached bool, cancel context.CancelFunc) {
	stdoutPreview := &limitedBuffer{limit: previewLimit}
	stderrPreview := &limitedBuffer{limit: previewLimit}
	var streamWG sync.WaitGroup
	copyStream := func(name string, reader io.Reader, preview *limitedBuffer) {
		defer streamWG.Done()
		buf := make([]byte, agentproto.MaxFileChunkSize)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				_, _ = preview.Write(chunk)
				_ = s.writeStream(agentproto.OpExecEvent, agentproto.ExecEvent{ExecID: execID, Stream: name, Data: agentproto.EncodeBytes(chunk)})
			}
			if err != nil {
				return
			}
		}
	}
	if detached {
		_ = s.writeStream(agentproto.OpExecEvent, agentproto.ExecEvent{ExecID: execID, Result: &agentproto.ExecResult{ExitCode: 0, Status: string(model.ExecutionStatusDetached), StartedAt: startedAt, CompletedAt: startedAt}})
		go func() {
			_ = cmd.Wait()
			cancel()
			s.mu.Lock()
			delete(s.execs, execID)
			s.mu.Unlock()
		}()
		return
	}
	streamWG.Add(2)
	go copyStream("stdout", stdout, stdoutPreview)
	go copyStream("stderr", stderr, stderrPreview)
	err := cmd.Wait()
	streamWG.Wait()
	completedAt := time.Now().UTC()
	status := model.ExecutionStatusSucceeded
	exitCode := 0
	if err != nil {
		status = model.ExecutionStatusFailed
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		status = model.ExecutionStatusTimedOut
		exitCode = 124
	}
	_ = s.writeStream(agentproto.OpExecEvent, agentproto.ExecEvent{ExecID: execID, Result: &agentproto.ExecResult{ExitCode: exitCode, Status: string(status), StartedAt: startedAt, CompletedAt: completedAt, StdoutPreview: stdoutPreview.String(), StderrPreview: stderrPreview.String(), StdoutTruncated: stdoutPreview.truncated, StderrTruncated: stderrPreview.truncated}})
	cancel()
	s.mu.Lock()
	delete(s.execs, execID)
	s.mu.Unlock()
}

func (s *guestSession) handleExecCancel(requestID string, req agentproto.ExecCancelRequest) error {
	s.mu.Lock()
	execSession, ok := s.execs[req.ExecID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown exec session %q", req.ExecID)
	}
	if execSession.command.Process != nil {
		_ = execSession.command.Process.Kill()
	}
	execSession.cancel()
	return s.write(agentproto.Message{ID: requestID, Op: agentproto.OpExecCancel, OK: true})
}

func (s *guestSession) handlePTYOpen(requestID string, req agentproto.PTYOpenRequest) error {
	identity, err := s.agent.workloadIdentityInfo()
	if err != nil {
		return err
	}
	command := req.Command
	if len(command) == 0 {
		command = []string{"bash"}
	}
	cmd := exec.CommandContext(s.ctx, command[0], command[1:]...)
	cmd.Dir = defaultString(req.Cwd, "/workspace")
	cmd.Env = append(os.Environ(), flattenEnv(req.Env)...)
	configureWorkloadCommand(cmd, identity)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(defaultInt(req.Rows, 24)), Cols: uint16(defaultInt(req.Cols, 80))})
	if err != nil {
		return err
	}
	sessionID := fmt.Sprintf("pty-%d", time.Now().UTC().UnixNano())
	s.mu.Lock()
	s.pty[sessionID] = &guestPTYSession{id: sessionID, cmd: cmd, ptmx: ptmx}
	s.mu.Unlock()
	if err := s.writeJSON(requestID, agentproto.OpPTYOpen, agentproto.PTYOpenResult{SessionID: sessionID}); err != nil {
		return err
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				_ = s.writeStream(agentproto.OpPTYData, agentproto.PTYData{SessionID: sessionID, Data: agentproto.EncodeBytes(buf[:n])})
			}
			if err != nil {
				exitCode := 0
				if cmd.ProcessState != nil {
					exitCode = cmd.ProcessState.ExitCode()
				}
				_ = s.writeStream(agentproto.OpPTYData, agentproto.PTYData{SessionID: sessionID, EOF: true, ExitCode: &exitCode})
				s.mu.Lock()
				delete(s.pty, sessionID)
				s.mu.Unlock()
				_ = ptmx.Close()
				return
			}
		}
	}()
	go func() { _ = cmd.Wait() }()
	return nil
}

func (s *guestSession) handlePTYData(req agentproto.PTYData) error {
	s.mu.Lock()
	ptySession, ok := s.pty[req.SessionID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown pty session %q", req.SessionID)
	}
	if req.Data != "" {
		decoded, err := agentproto.DecodeBytes(req.Data)
		if err != nil {
			return err
		}
		if _, err := ptySession.ptmx.Write(decoded); err != nil {
			return err
		}
	}
	return nil
}

func (s *guestSession) handlePTYResize(req agentproto.PTYResizeRequest) error {
	s.mu.Lock()
	ptySession, ok := s.pty[req.SessionID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown pty session %q", req.SessionID)
	}
	return pty.Setsize(ptySession.ptmx, &pty.Winsize{Rows: uint16(defaultInt(req.Rows, 24)), Cols: uint16(defaultInt(req.Cols, 80))})
}

func (s *guestSession) handlePTYClose(sessionID string) error {
	s.mu.Lock()
	ptySession, ok := s.pty[sessionID]
	if ok {
		delete(s.pty, sessionID)
	}
	s.mu.Unlock()
	if !ok {
		return nil
	}
	if ptySession.cmd.Process != nil {
		_ = ptySession.cmd.Process.Kill()
	}
	return ptySession.ptmx.Close()
}

func (s *guestSession) handleTCPBridgeOpen(requestID string, req agentproto.TCPBridgeOpenRequest) error {
	if req.TargetPort < 1 || req.TargetPort > 65535 {
		return fmt.Errorf("target port must be between 1 and 65535")
	}
	dialer := &net.Dialer{}
	targetConn, err := dialer.DialContext(s.ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", req.TargetPort))
	if err != nil {
		return fmt.Errorf("sandbox-local tunnel bridge failed to connect: %v", err)
	}
	sessionID := fmt.Sprintf("tcp-%d", time.Now().UTC().UnixNano())
	s.mu.Lock()
	s.bridge[sessionID] = &guestTCPBridgeSession{id: sessionID, conn: targetConn}
	s.mu.Unlock()
	if err := s.writeJSON(requestID, agentproto.OpTCPBridgeOpen, agentproto.TCPBridgeOpenResult{SessionID: sessionID}); err != nil {
		return err
	}
	go func() {
		buf := make([]byte, agentproto.MaxBridgeChunkSize)
		for {
			n, err := targetConn.Read(buf)
			if n > 0 {
				_ = s.writeStream(agentproto.OpTCPBridgeData, agentproto.TCPBridgeData{SessionID: sessionID, Data: agentproto.EncodeBytes(buf[:n])})
			}
			if err != nil {
				if err == io.EOF {
					_ = s.writeStream(agentproto.OpTCPBridgeData, agentproto.TCPBridgeData{SessionID: sessionID, EOF: true})
				}
				s.mu.Lock()
				delete(s.bridge, sessionID)
				s.mu.Unlock()
				_ = targetConn.Close()
				return
			}
		}
	}()
	return nil
}

func (s *guestSession) handleTCPBridgeData(req agentproto.TCPBridgeData) error {
	s.mu.Lock()
	bridgeSession, ok := s.bridge[req.SessionID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown tcp bridge session %q", req.SessionID)
	}
	if req.Data != "" {
		decoded, err := agentproto.DecodeBytes(req.Data)
		if err != nil {
			return err
		}
		if _, err := bridgeSession.conn.Write(decoded); err != nil {
			return err
		}
	}
	if req.EOF {
		if tcpConn, ok := bridgeSession.conn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		} else {
			_ = bridgeSession.conn.Close()
		}
	}
	return nil
}

func (s *guestSession) handleArchiveStream(requestID string, req agentproto.ArchiveStreamRequest) error {
	sessionID := fmt.Sprintf("archive-%d", time.Now().UTC().UnixNano())
	paths := req.Paths
	if len(paths) == 0 {
		paths = []string{""}
	}
	if err := s.writeJSON(requestID, agentproto.OpArchiveStream, agentproto.ArchiveStreamStart{SessionID: sessionID}); err != nil {
		return err
	}
	go s.streamArchive(sessionID, paths)
	return nil
}

func (s *guestSession) streamArchive(sessionID string, requestedPaths []string) {
	defer func() {
		_ = s.writeStream(agentproto.OpArchiveStream, agentproto.ArchiveStreamChunk{SessionID: sessionID, End: true})
	}()
	seen := map[string]struct{}{}
	for _, requested := range requestedPaths {
		target, err := workspacePath(defaultString(requested, "/workspace"))
		if err != nil {
			_ = s.writeStream(agentproto.OpArchiveStream, agentproto.ArchiveStreamChunk{SessionID: sessionID, Error: err.Error(), End: true})
			return
		}
		info, err := os.Stat(target)
		if err != nil {
			_ = s.writeStream(agentproto.OpArchiveStream, agentproto.ArchiveStreamChunk{SessionID: sessionID, Error: err.Error(), End: true})
			return
		}
		archiveRoot := strings.TrimPrefix(strings.TrimPrefix(target, "/workspace"), "/")
		if !info.IsDir() {
			archiveRoot = filepath.Base(target)
		}
		if err := filepath.WalkDir(target, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if current == target && info.IsDir() && archiveRoot == "" {
				return nil
			}
			name := archiveRoot
			if info.IsDir() {
				rel, err := filepath.Rel(target, current)
				if err != nil {
					return err
				}
				if rel != "." {
					name = filepath.ToSlash(filepath.Join(archiveRoot, rel))
				}
			}
			name = strings.TrimPrefix(filepath.ToSlash(name), "./")
			if name == "" && entry.IsDir() {
				return nil
			}
			if _, ok := seen[name]; ok {
				return nil
			}
			entryInfo, err := entry.Info()
			if err != nil {
				return err
			}
			if entryInfo.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("workspace path contains unsupported symlink: %s", name)
			}
			typeName := "file"
			if entry.IsDir() {
				typeName = "dir"
			}
			if err := s.writeStream(agentproto.OpArchiveStream, agentproto.ArchiveStreamChunk{SessionID: sessionID, Path: name, Type: typeName, Mode: int64(entryInfo.Mode().Perm()), ModTime: entryInfo.ModTime().UTC(), Size: entryInfo.Size()}); err != nil {
				return err
			}
			seen[name] = struct{}{}
			if entry.IsDir() {
				return s.writeStream(agentproto.OpArchiveStream, agentproto.ArchiveStreamChunk{SessionID: sessionID, Path: name, Type: typeName, EOF: true})
			}
			file, err := os.Open(current)
			if err != nil {
				return err
			}
			defer file.Close()
			buf := make([]byte, agentproto.MaxFileChunkSize)
			for {
				n, err := file.Read(buf)
				if n > 0 {
					if err := s.writeStream(agentproto.OpArchiveStream, agentproto.ArchiveStreamChunk{SessionID: sessionID, Path: name, Type: typeName, Data: agentproto.EncodeBytes(buf[:n])}); err != nil {
						return err
					}
				}
				if err != nil {
					if errors.Is(err, io.EOF) {
						return s.writeStream(agentproto.OpArchiveStream, agentproto.ArchiveStreamChunk{SessionID: sessionID, Path: name, Type: typeName, EOF: true})
					}
					return err
				}
			}
		}); err != nil {
			_ = s.writeStream(agentproto.OpArchiveStream, agentproto.ArchiveStreamChunk{SessionID: sessionID, Error: err.Error(), End: true})
			return
		}
	}
}

func isReady() bool {
	_, err := os.Stat(readyMarkerPath)
	return err == nil
}

func workspacePath(raw string) (string, error) {
	requested := strings.TrimSpace(raw)
	if requested == "" {
		requested = "/workspace"
	} else if !path.IsAbs(requested) {
		requested = "/workspace/" + requested
	}
	clean := path.Clean(requested)
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

func lookupWorkloadIdentity(username string) (workloadIdentity, error) {
	trimmed := strings.TrimSpace(username)
	if trimmed == "" {
		trimmed = defaultWorkloadUser
	}
	account, err := user.Lookup(trimmed)
	if err != nil {
		return workloadIdentity{}, fmt.Errorf("lookup workload user %q: %w", trimmed, err)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return workloadIdentity{}, fmt.Errorf("parse workload uid %q: %w", account.Uid, err)
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		return workloadIdentity{}, fmt.Errorf("parse workload gid %q: %w", account.Gid, err)
	}
	return workloadIdentity{Username: trimmed, UID: uint32(uid), GID: uint32(gid), HomeDir: defaultString(account.HomeDir, "/workspace")}, nil
}

func configureWorkloadCommand(cmd *exec.Cmd, identity workloadIdentity) {
	cmd.Env = setEnvValue(cmd.Env, "HOME", defaultString(identity.HomeDir, "/workspace"))
	cmd.Env = setEnvValue(cmd.Env, "USER", identity.Username)
	cmd.Env = setEnvValue(cmd.Env, "LOGNAME", identity.Username)
	if uint32(os.Geteuid()) == identity.UID && uint32(os.Getegid()) == identity.GID {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: identity.UID, Gid: identity.GID}
}

func runExec(ctx context.Context, req agentproto.ExecStartRequest, identity workloadIdentity) (agentproto.ExecResult, error) {
	command := req.Command
	if len(command) == 0 {
		command = []string{"sh", "-lc", "pwd"}
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if req.Detached {
		runCtx = context.Background()
	} else if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, req.Timeout)
		defer func() {
			if cancel != nil {
				cancel()
			}
		}()
	}
	cmd := exec.CommandContext(runCtx, command[0], command[1:]...)
	cmd.Dir = defaultString(req.Cwd, "/workspace")
	cmd.Env = append(os.Environ(), flattenEnv(req.Env)...)
	configureWorkloadCommand(cmd, identity)
	stdout := &limitedBuffer{limit: previewLimit}
	stderr := &limitedBuffer{limit: previewLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	startedAt := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		return agentproto.ExecResult{}, err
	}
	if req.Detached {
		go func() { _ = cmd.Wait() }()
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
		} else {
			exitCode = 1
		}
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		status = model.ExecutionStatusTimedOut
		exitCode = 124
	}
	return agentproto.ExecResult{ExitCode: exitCode, Status: string(status), StartedAt: startedAt, CompletedAt: completedAt, StdoutPreview: stdout.String(), StderrPreview: stderr.String(), StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated}, nil
}

func runFileOpHelper(in io.Reader, out io.Writer) error {
	var req testFileOpRequest
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return err
	}
	target := req.Target
	switch req.Op {
	case "mkdir":
		return os.MkdirAll(target, 0o755)
	case "file_delete":
		return os.RemoveAll(target)
	case "file_write":
		if err := os.MkdirAll(path.Dir(target), 0o755); err != nil {
			return err
		}
		flags := os.O_CREATE | os.O_WRONLY
		if req.Write.Truncate {
			flags |= os.O_TRUNC
		}
		file, err := os.OpenFile(target, flags, 0o644)
		if err != nil {
			return err
		}
		defer file.Close()
		data, err := agentproto.DecodeBytes(req.Data)
		if err != nil {
			return err
		}
		if _, err := file.Write(data); err != nil {
			return err
		}
		return nil
	case "file_open", "file_read":
		file, err := os.Open(target)
		if err != nil {
			return err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, int64(defaultInt(req.Read.MaxBytes, agentproto.MaxFileChunkSize))))
		if err != nil {
			return err
		}
		var response testFileOpResponse
		response.Read.Content = agentproto.EncodeBytes(data)
		response.Read.EOF = true
		return json.NewEncoder(out).Encode(response)
	default:
		return fmt.Errorf("unsupported helper operation %q", req.Op)
	}
}

func setEnvValue(env []string, key, value string) []string {
	prefix := key + "="
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
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
