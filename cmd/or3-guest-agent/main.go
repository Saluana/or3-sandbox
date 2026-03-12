package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path"
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
	workloadHelperModeEnv  = "OR3_GUEST_AGENT_HELPER_MODE"
)

func main() {
	if mode := strings.TrimSpace(os.Getenv(workloadHelperModeEnv)); mode != "" {
		if err := runHelperMode(mode, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
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

type workloadFileOpRequest struct {
	Op     string                      `json:"op"`
	Target string                      `json:"target"`
	Read   agentproto.FileReadRequest  `json:"read,omitempty"`
	Write  agentproto.FileWriteRequest `json:"write,omitempty"`
	Data   string                      `json:"data,omitempty"`
}

type workloadFileOpResponse struct {
	Read agentproto.FileReadResult `json:"read,omitempty"`
}

func (a *guestAgent) run(ctx context.Context) error {
	if err := a.loadCapabilities(); err != nil {
		return err
	}
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
		agentproto.OpReady:    {},
		agentproto.OpShutdown: {},
	}
	if strings.EqualFold(strings.TrimSpace(manifest.Control.Mode), string(model.GuestControlModeAgent)) {
		allowed[agentproto.OpTCPBridgeOpen] = struct{}{}
		allowed[agentproto.OpTCPBridgeData] = struct{}{}
	}
	for _, capability := range model.NormalizeCapabilities(manifest.Capabilities) {
		switch capability {
		case "exec":
			allowed[agentproto.OpExec] = struct{}{}
		case "files":
			allowed[agentproto.OpFileRead] = struct{}{}
			allowed[agentproto.OpFileWrite] = struct{}{}
			allowed[agentproto.OpFileDelete] = struct{}{}
			allowed[agentproto.OpMkdir] = struct{}{}
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

func (a *guestAgent) serveConn(ctx context.Context, conn io.ReadWriter) error {
	for {
		message, err := agentproto.ReadMessage(conn)
		if err != nil {
			return err
		}
		if err := agentproto.ValidateRequest(message); err != nil {
			if writeErr := agentproto.WriteMessage(conn, agentproto.Message{ID: message.ID, Op: message.Op, OK: false, Error: err.Error()}); writeErr != nil {
				return writeErr
			}
			continue
		}
		switch message.Op {
		case agentproto.OpPTYOpen:
			var req agentproto.PTYOpenRequest
			if err := json.Unmarshal(message.Result, &req); err != nil {
				return agentproto.WriteMessage(conn, agentproto.Message{ID: message.ID, Op: message.Op, OK: false, Error: err.Error()})
			}
			return a.servePTY(ctx, conn, message.ID, req)
		case agentproto.OpTCPBridgeOpen:
			var req agentproto.TCPBridgeOpenRequest
			if err := json.Unmarshal(message.Result, &req); err != nil {
				return agentproto.WriteMessage(conn, agentproto.Message{ID: message.ID, Op: message.Op, OK: false, Error: err.Error()})
			}
			return a.serveTCPBridge(ctx, conn, message.ID, req)
		}
		response, err := a.handle(ctx, message)
		if err != nil {
			response = agentproto.Message{ID: message.ID, Op: message.Op, OK: false, Error: err.Error()}
		}
		if err := agentproto.WriteMessage(conn, response); err != nil {
			return err
		}
	}
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
			Ready:                    isReady(),
			Capabilities:             append([]string(nil), a.capabilities...),
		})
		return agentproto.Message{ID: message.ID, Op: message.Op, OK: true, Result: result}, err
	case agentproto.OpReady:
		ready := isReady()
		reason := ""
		if !ready {
			reason = "bootstrap marker not present"
		}
		result, err := json.Marshal(agentproto.ReadyResult{Ready: ready, Reason: reason})
		return agentproto.Message{ID: message.ID, Op: message.Op, OK: true, Result: result}, err
	case agentproto.OpExec:
		var req agentproto.ExecRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		identity, err := a.workloadIdentityInfo()
		if err != nil {
			return agentproto.Message{}, err
		}
		result, err := runExec(ctx, req, identity)
		if err != nil {
			return agentproto.Message{}, err
		}
		payload, err := json.Marshal(result)
		return agentproto.Message{ID: message.ID, Op: message.Op, OK: true, Result: payload}, err
	case agentproto.OpFileRead:
		var req agentproto.FileReadRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		target, err := workspacePath(req.Path)
		if err != nil {
			return agentproto.Message{}, err
		}
		identity, err := a.workloadIdentityInfo()
		if err != nil {
			return agentproto.Message{}, err
		}
		result, err := readFileChunkAsWorkload(ctx, identity, target, req)
		if err != nil {
			return agentproto.Message{}, err
		}
		payload, err := json.Marshal(result)
		return agentproto.Message{ID: message.ID, Op: message.Op, OK: true, Result: payload}, err
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
		identity, err := a.workloadIdentityInfo()
		if err != nil {
			return agentproto.Message{}, err
		}
		if err := writeFileChunkAsWorkload(ctx, identity, target, req, data); err != nil {
			return agentproto.Message{}, err
		}
		return agentproto.Message{ID: message.ID, Op: message.Op, OK: true}, nil
	case agentproto.OpFileDelete:
		var req agentproto.PathRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		target, err := workspacePath(req.Path)
		if err != nil {
			return agentproto.Message{}, err
		}
		identity, err := a.workloadIdentityInfo()
		if err != nil {
			return agentproto.Message{}, err
		}
		return agentproto.Message{ID: message.ID, Op: message.Op, OK: true}, deletePathAsWorkload(ctx, identity, target)
	case agentproto.OpMkdir:
		var req agentproto.PathRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		target, err := workspacePath(req.Path)
		if err != nil {
			return agentproto.Message{}, err
		}
		identity, err := a.workloadIdentityInfo()
		if err != nil {
			return agentproto.Message{}, err
		}
		return agentproto.Message{ID: message.ID, Op: message.Op, OK: true}, mkdirAsWorkload(ctx, identity, target)
	case agentproto.OpShutdown:
		go func() {
			_ = exec.Command("/sbin/poweroff").Run()
		}()
		return agentproto.Message{ID: message.ID, Op: message.Op, OK: true}, nil
	default:
		return agentproto.Message{}, fmt.Errorf("unsupported guest agent operation %q", message.Op)
	}
}

func (a *guestAgent) servePTY(ctx context.Context, conn io.ReadWriter, requestID string, req agentproto.PTYOpenRequest) error {
	if !a.allows(agentproto.OpPTYOpen) {
		return agentproto.WriteMessage(conn, agentproto.Message{ID: requestID, Op: agentproto.OpPTYOpen, OK: false, Error: fmt.Sprintf("guest profile does not allow operation %q", agentproto.OpPTYOpen)})
	}
	identity, err := a.workloadIdentityInfo()
	if err != nil {
		return agentproto.WriteMessage(conn, agentproto.Message{ID: requestID, Op: agentproto.OpPTYOpen, OK: false, Error: err.Error()})
	}
	command := req.Command
	if len(command) == 0 {
		command = []string{"bash"}
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = defaultString(req.Cwd, "/workspace")
	cmd.Env = append(os.Environ(), flattenEnv(req.Env)...)
	configureWorkloadCommand(cmd, identity)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(defaultInt(req.Rows, 24)), Cols: uint16(defaultInt(req.Cols, 80))})
	if err != nil {
		return agentproto.WriteMessage(conn, agentproto.Message{ID: requestID, Op: agentproto.OpPTYOpen, OK: false, Error: err.Error()})
	}
	defer ptmx.Close()
	sessionID := fmt.Sprintf("pty-%d", time.Now().UTC().UnixNano())
	ack, err := json.Marshal(agentproto.PTYOpenResult{SessionID: sessionID})
	if err != nil {
		return err
	}
	if err := agentproto.WriteMessage(conn, agentproto.Message{ID: requestID, Op: agentproto.OpPTYOpen, OK: true, Result: ack}); err != nil {
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
		return agentproto.WriteMessage(conn, agentproto.Message{ID: a.nextMessageID(), Op: agentproto.OpPTYData, OK: true, Result: payload})
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
				if err := requireActiveSession(data.SessionID, sessionID); err != nil {
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
				if err := requireActiveSession(resize.SessionID, sessionID); err != nil {
					return err
				}
				if err := pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(defaultInt(resize.Rows, 24)), Cols: uint16(defaultInt(resize.Cols, 80))}); err != nil {
					return err
				}
			case agentproto.OpPTYClose:
				var closeReq agentproto.PTYData
				if len(inbound.message.Result) > 0 {
					if err := json.Unmarshal(inbound.message.Result, &closeReq); err != nil {
						return err
					}
				}
				if err := requireActiveSession(closeReq.SessionID, sessionID); err != nil {
					return err
				}
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				return nil
			default:
				return fmt.Errorf("unsupported PTY stream operation %q", inbound.message.Op)
			}
		case err := <-errCh:
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (a *guestAgent) serveTCPBridge(ctx context.Context, conn io.ReadWriter, requestID string, req agentproto.TCPBridgeOpenRequest) error {
	if !a.allows(agentproto.OpTCPBridgeOpen) {
		return agentproto.WriteMessage(conn, agentproto.Message{ID: requestID, Op: agentproto.OpTCPBridgeOpen, OK: false, Error: fmt.Sprintf("guest profile does not allow operation %q", agentproto.OpTCPBridgeOpen)})
	}
	if req.TargetPort < 1 || req.TargetPort > 65535 {
		return agentproto.WriteMessage(conn, agentproto.Message{ID: requestID, Op: agentproto.OpTCPBridgeOpen, OK: false, Error: "target port must be between 1 and 65535"})
	}
	dialer := &net.Dialer{}
	targetConn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", req.TargetPort))
	if err != nil {
		return agentproto.WriteMessage(conn, agentproto.Message{ID: requestID, Op: agentproto.OpTCPBridgeOpen, OK: false, Error: fmt.Sprintf("sandbox-local tunnel bridge failed to connect: %v", err)})
	}
	defer targetConn.Close()

	sessionID := fmt.Sprintf("tcp-%d", time.Now().UTC().UnixNano())
	payload, err := json.Marshal(agentproto.TCPBridgeOpenResult{SessionID: sessionID})
	if err != nil {
		return err
	}
	if err := agentproto.WriteMessage(conn, agentproto.Message{ID: requestID, Op: agentproto.OpTCPBridgeOpen, OK: true, Result: payload}); err != nil {
		return err
	}

	var writeMu sync.Mutex
	sendBridge := func(data agentproto.TCPBridgeData) error {
		payload, err := json.Marshal(data)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		return agentproto.WriteMessage(conn, agentproto.Message{ID: a.nextMessageID(), Op: agentproto.OpTCPBridgeData, OK: true, Result: payload})
	}

	errCh := make(chan error, 2)
	type connMessage struct {
		message agentproto.Message
		err     error
	}
	messageCh := make(chan connMessage, 1)
	go func() {
		buf := make([]byte, agentproto.MaxBridgeChunkSize)
		for {
			n, err := targetConn.Read(buf)
			if n > 0 {
				if sendErr := sendBridge(agentproto.TCPBridgeData{SessionID: sessionID, Data: agentproto.EncodeBytes(buf[:n])}); sendErr != nil {
					errCh <- sendErr
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					_ = sendBridge(agentproto.TCPBridgeData{SessionID: sessionID, EOF: true})
					errCh <- io.EOF
					return
				}
				errCh <- err
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

	for {
		select {
		case inbound := <-messageCh:
			if inbound.err != nil {
				return inbound.err
			}
			switch inbound.message.Op {
			case agentproto.OpTCPBridgeData:
				var data agentproto.TCPBridgeData
				if err := json.Unmarshal(inbound.message.Result, &data); err != nil {
					return err
				}
				if err := requireActiveSession(data.SessionID, sessionID); err != nil {
					return err
				}
				if data.Data != "" {
					decoded, err := agentproto.DecodeBytes(data.Data)
					if err != nil {
						return err
					}
					if _, err := targetConn.Write(decoded); err != nil {
						return err
					}
				}
				if data.EOF {
					if tcpConn, ok := targetConn.(*net.TCPConn); ok {
						_ = tcpConn.CloseWrite()
					} else {
						_ = targetConn.Close()
					}
				}
			default:
				return fmt.Errorf("unsupported tcp bridge operation %q", inbound.message.Op)
			}
		case err := <-errCh:
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func runExec(ctx context.Context, req agentproto.ExecRequest, identity workloadIdentity) (agentproto.ExecResult, error) {
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
	return workloadIdentity{
		Username: trimmed,
		UID:      uint32(uid),
		GID:      uint32(gid),
		HomeDir:  defaultString(account.HomeDir, "/workspace"),
	}, nil
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

func readFileChunkAsWorkload(ctx context.Context, identity workloadIdentity, target string, req agentproto.FileReadRequest) (agentproto.FileReadResult, error) {
	if uint32(os.Geteuid()) == identity.UID && uint32(os.Getegid()) == identity.GID {
		return readFileChunk(target, req)
	}
	response, err := runWorkloadFileOp(ctx, identity, workloadFileOpRequest{
		Op:     agentproto.OpFileRead,
		Target: target,
		Read:   req,
	})
	if err != nil {
		return agentproto.FileReadResult{}, err
	}
	return response.Read, nil
}

func writeFileChunkAsWorkload(ctx context.Context, identity workloadIdentity, target string, req agentproto.FileWriteRequest, data []byte) error {
	if uint32(os.Geteuid()) == identity.UID && uint32(os.Getegid()) == identity.GID {
		return writeFileChunk(target, req, data)
	}
	_, err := runWorkloadFileOp(ctx, identity, workloadFileOpRequest{
		Op:     agentproto.OpFileWrite,
		Target: target,
		Write:  req,
		Data:   agentproto.EncodeBytes(data),
	})
	return err
}

func deletePathAsWorkload(ctx context.Context, identity workloadIdentity, target string) error {
	if uint32(os.Geteuid()) == identity.UID && uint32(os.Getegid()) == identity.GID {
		return os.RemoveAll(target)
	}
	_, err := runWorkloadFileOp(ctx, identity, workloadFileOpRequest{
		Op:     agentproto.OpFileDelete,
		Target: target,
	})
	return err
}

func mkdirAsWorkload(ctx context.Context, identity workloadIdentity, target string) error {
	if uint32(os.Geteuid()) == identity.UID && uint32(os.Getegid()) == identity.GID {
		return os.MkdirAll(target, 0o755)
	}
	_, err := runWorkloadFileOp(ctx, identity, workloadFileOpRequest{
		Op:     agentproto.OpMkdir,
		Target: target,
	})
	return err
}

func runWorkloadFileOp(ctx context.Context, identity workloadIdentity, req workloadFileOpRequest) (workloadFileOpResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return workloadFileOpResponse{}, err
	}
	exePath, err := os.Executable()
	if err != nil {
		return workloadFileOpResponse{}, err
	}
	cmd := exec.CommandContext(ctx, exePath)
	cmd.Env = append(os.Environ(), workloadHelperModeEnv+"=file-op")
	configureWorkloadCommand(cmd, identity)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return workloadFileOpResponse{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return workloadFileOpResponse{}, err
	}
	if stdout.Len() == 0 {
		return workloadFileOpResponse{}, nil
	}
	var response workloadFileOpResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return workloadFileOpResponse{}, err
	}
	return response, nil
}

func runHelperMode(mode string, in io.Reader, out io.Writer) error {
	switch mode {
	case "file-op":
		return runFileOpHelper(in, out)
	default:
		return fmt.Errorf("unknown helper mode %q", mode)
	}
}

func runFileOpHelper(in io.Reader, out io.Writer) error {
	var req workloadFileOpRequest
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return err
	}
	switch req.Op {
	case agentproto.OpFileRead:
		result, err := readFileChunk(req.Target, req.Read)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(workloadFileOpResponse{Read: result})
	case agentproto.OpFileWrite:
		data, err := agentproto.DecodeBytes(req.Data)
		if err != nil {
			return err
		}
		return writeFileChunk(req.Target, req.Write, data)
	case agentproto.OpFileDelete:
		return os.RemoveAll(req.Target)
	case agentproto.OpMkdir:
		return os.MkdirAll(req.Target, 0o755)
	default:
		return fmt.Errorf("unsupported helper operation %q", req.Op)
	}
}

func readFileChunk(target string, req agentproto.FileReadRequest) (agentproto.FileReadResult, error) {
	if req.Offset < 0 {
		return agentproto.FileReadResult{}, fmt.Errorf("file read offset must be non-negative")
	}
	chunkSize := defaultInt(req.MaxBytes, agentproto.MaxFileChunkSize)
	if chunkSize > agentproto.MaxFileChunkSize {
		return agentproto.FileReadResult{}, fmt.Errorf("file read chunk exceeds limit of %d bytes", agentproto.MaxFileChunkSize)
	}
	file, err := os.Open(target)
	if err != nil {
		return agentproto.FileReadResult{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return agentproto.FileReadResult{}, err
	}
	if req.Offset > info.Size() {
		req.Offset = info.Size()
	}
	if _, err := file.Seek(req.Offset, io.SeekStart); err != nil {
		return agentproto.FileReadResult{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(chunkSize)+1))
	if err != nil {
		return agentproto.FileReadResult{}, err
	}
	if len(data) > chunkSize {
		data = data[:chunkSize]
	}
	return agentproto.FileReadResult{
		Path:    target,
		Content: agentproto.EncodeBytes(data),
		Offset:  req.Offset,
		Size:    info.Size(),
		EOF:     req.Offset+int64(len(data)) >= info.Size(),
	}, nil
}

func writeFileChunk(target string, req agentproto.FileWriteRequest, data []byte) error {
	if req.Offset < 0 {
		return fmt.Errorf("file write offset must be non-negative")
	}
	if len(data) > agentproto.MaxFileChunkSize {
		return fmt.Errorf("file write chunk exceeds limit of %d bytes", agentproto.MaxFileChunkSize)
	}
	if req.TotalSize < 0 || req.TotalSize > agentproto.MaxFileTransferSize {
		return fmt.Errorf("file write total size exceeds limit of %d bytes", agentproto.MaxFileTransferSize)
	}
	if req.TotalSize == 0 && len(data) > 0 {
		req.TotalSize = int64(len(data))
	}
	if req.Offset+int64(len(data)) > req.TotalSize {
		return fmt.Errorf("file write chunk exceeds declared total size")
	}
	if err := os.MkdirAll(path.Dir(target), 0o755); err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if req.Truncate && req.Offset == 0 {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(target, flags, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.WriteAt(data, req.Offset); err != nil {
		return err
	}
	if req.EOF {
		return file.Truncate(req.TotalSize)
	}
	return nil
}

func requireActiveSession(got, expected string) error {
	if strings.TrimSpace(got) == "" {
		return fmt.Errorf("session id is required")
	}
	if got != expected {
		return fmt.Errorf("session mismatch: expected %q got %q", expected, got)
	}
	return nil
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
