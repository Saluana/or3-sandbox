package qemu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"or3-sandbox/internal/guestimage"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/runtime/qemu/agentproto"
)

func TestResolveAccel(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		goos    string
		want    string
		wantErr bool
	}{
		{name: "auto linux", value: "auto", goos: "linux", want: "kvm"},
		{name: "auto darwin", value: "auto", goos: "darwin", want: "hvf"},
		{name: "explicit kvm", value: "kvm", goos: "linux", want: "kvm"},
		{name: "explicit hvf", value: "hvf", goos: "darwin", want: "hvf"},
		{name: "invalid host", value: "auto", goos: "windows", wantErr: true},
		{name: "invalid accel", value: "tcg", goos: "linux", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveAccel(tc.value, tc.goos)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("unexpected accel: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestStartArgsIncludeNetworkingAndDisks(t *testing.T) {
	r := &Runtime{
		qemuBinary:  "qemu-system-x86_64",
		accelerator: "kvm",
	}
	layout := sandboxLayout{
		pidPath:           "/tmp/qemu.pid",
		monitorPath:       "/tmp/monitor.sock",
		serialLogPath:     "/tmp/serial.log",
		rootDiskPath:      "/tmp/root.qcow2",
		workspaceDiskPath: "/tmp/workspace.img",
	}
	sandbox := model.Sandbox{
		ID:            "sbx-1",
		CPULimit:      model.CPUCores(2),
		MemoryLimitMB: 768,
		NetworkMode:   model.NetworkModeInternetDisabled,
		ControlMode:   model.GuestControlModeSSHCompat,
	}
	args := r.startArgs(sandbox, layout, 2222)
	joined := strings.Join(args, " ")
	for _, snippet := range []string{
		"-daemonize",
		"-pidfile /tmp/qemu.pid",
		"-accel kvm",
		"hostfwd=tcp:127.0.0.1:2222-:22",
		"restrict=on",
		"file=/tmp/root.qcow2",
		"file=/tmp/workspace.img",
	} {
		if !strings.Contains(joined, snippet) {
			t.Fatalf("expected %q in args: %s", snippet, joined)
		}
	}
}

func TestStartArgsKeepHostExposureLoopbackOnly(t *testing.T) {
	r := &Runtime{qemuBinary: "qemu-system-x86_64", accelerator: "kvm"}
	args := strings.Join(r.startArgs(model.Sandbox{
		ID:            "sbx-net",
		MemoryLimitMB: 512,
		CPULimit:      model.CPUCores(1),
		NetworkMode:   model.NetworkModeInternetEnabled,
		ControlMode:   model.GuestControlModeSSHCompat,
	}, sandboxLayout{
		pidPath:           "/tmp/qemu.pid",
		monitorPath:       "/tmp/monitor.sock",
		serialLogPath:     "/tmp/serial.log",
		rootDiskPath:      "/tmp/root.qcow2",
		workspaceDiskPath: "/tmp/workspace.img",
	}, 2233), " ")
	if !strings.Contains(args, "hostfwd=tcp:127.0.0.1:2233-:22") {
		t.Fatalf("expected loopback ssh forwarding, got %s", args)
	}
	if strings.Contains(args, "0.0.0.0") || strings.Contains(args, "::") {
		t.Fatalf("did not expect public host exposure in args: %s", args)
	}
}

func TestStartArgsAgentModeUsesVirtioSerialWithoutSSHForward(t *testing.T) {
	r := &Runtime{qemuBinary: "qemu-system-x86_64", accelerator: "kvm", controlMode: model.GuestControlModeAgent}
	args := strings.Join(r.startArgs(model.Sandbox{
		ID:            "sbx-agent",
		MemoryLimitMB: 512,
		CPULimit:      model.CPUCores(1),
		NetworkMode:   model.NetworkModeInternetEnabled,
		ControlMode:   model.GuestControlModeAgent,
	}, sandboxLayout{
		pidPath:           "/tmp/qemu.pid",
		monitorPath:       "/tmp/monitor.sock",
		agentSocketPath:   "/tmp/agent.sock",
		serialLogPath:     "/tmp/serial.log",
		rootDiskPath:      "/tmp/root.qcow2",
		workspaceDiskPath: "/tmp/workspace.img",
	}, 2233), " ")
	for _, snippet := range []string{"virtio-serial", "virtserialport", "agent.sock"} {
		if !strings.Contains(args, snippet) {
			t.Fatalf("expected %q in args: %s", snippet, args)
		}
	}
	if strings.Contains(args, "hostfwd=tcp:127.0.0.1:2233-:22") {
		t.Fatalf("did not expect ssh forwarding in agent mode args: %s", args)
	}
}

func TestWaitForReadyTimesOut(t *testing.T) {
	r := &Runtime{
		bootTimeout:  200 * time.Millisecond,
		pollInterval: 20 * time.Millisecond,
		sshReady: func(context.Context, sshTarget) error {
			return errors.New("still booting")
		},
	}
	err := r.waitForReady(context.Background(), model.Sandbox{ControlMode: model.GuestControlModeSSHCompat}, sshTarget{port: 2222}, "")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestCreateRejectsGuestContractProfileMismatch(t *testing.T) {
	spec := model.SandboxSpec{
		SandboxID:                "sbx-mismatch",
		BaseImageRef:             writeTestQEMUBaseImage(t),
		Profile:                  model.GuestProfileBrowser,
		ControlMode:              model.GuestControlModeAgent,
		ControlProtocolVersion:   model.DefaultGuestControlProtocolVersion,
		WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
		ImageContractVersion:     model.DefaultImageContractVersion,
		StorageRoot:              filepath.Join(t.TempDir(), "rootfs"),
		WorkspaceRoot:            filepath.Join(t.TempDir(), "workspace"),
		CacheRoot:                filepath.Join(t.TempDir(), "cache"),
		DiskLimitMB:              16,
	}
	r := &Runtime{}
	_, err := r.Create(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "does not match sandbox profile") {
		t.Fatalf("expected profile mismatch error, got %v", err)
	}
}

func TestCreateUsesGuestContractControlModeWhenSpecOmitsIt(t *testing.T) {
	imagePath := writeTestQEMUBaseImage(t)
	r := &Runtime{controlMode: model.GuestControlModeSSHCompat}
	state, err := r.Create(context.Background(), model.SandboxSpec{
		SandboxID:     "sbx-contract-mode",
		BaseImageRef:  imagePath,
		StorageRoot:   filepath.Join(t.TempDir(), "rootfs"),
		WorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
		CacheRoot:     filepath.Join(t.TempDir(), "cache"),
		DiskLimitMB:   16,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if state.ControlMode != model.GuestControlModeAgent {
		t.Fatalf("expected agent control mode from guest contract, got %q", state.ControlMode)
	}
}

func TestAgentHandshakeRejectsProtocolMismatch(t *testing.T) {
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("or3-agent-%d.sock", time.Now().UnixNano()))
	defer os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		request, _ := agentproto.ReadMessage(conn)
		result, _ := json.Marshal(agentproto.HelloResult{ProtocolVersion: "999", WorkspaceContractVersion: model.DefaultWorkspaceContractVersion})
		_ = agentproto.WriteMessage(conn, agentproto.Message{ID: request.ID, Op: agentproto.OpHello, OK: true, Result: result})
	}()
	r := &Runtime{}
	_, err = r.agentHandshake(context.Background(), sandboxLayout{agentSocketPath: socketPath})
	if err == nil || !strings.Contains(err.Error(), "protocol mismatch") {
		t.Fatalf("expected protocol mismatch error, got %v", err)
	}
}

func TestAgentRoundTripRejectsMissingResponseID(t *testing.T) {
	socketPath := startTestAgentSocket(t, func(conn net.Conn) {
		defer conn.Close()
		request, _ := agentproto.ReadMessage(conn)
		result, _ := json.Marshal(agentproto.ReadyResult{Ready: true})
		_ = agentproto.WriteMessage(conn, agentproto.Message{Op: request.Op, OK: true, Result: result})
	})
	r := &Runtime{}
	err := r.agentReady(context.Background(), sandboxLayout{agentSocketPath: socketPath})
	if err == nil || !strings.Contains(err.Error(), "response id is required") {
		t.Fatalf("expected missing id error, got %v", err)
	}
}

func TestAgentRoundTripRejectsMismatchedResponseID(t *testing.T) {
	socketPath := startTestAgentSocket(t, func(conn net.Conn) {
		defer conn.Close()
		request, _ := agentproto.ReadMessage(conn)
		result, _ := json.Marshal(agentproto.ReadyResult{Ready: true})
		_ = agentproto.WriteMessage(conn, agentproto.Message{ID: request.ID + "-wrong", Op: request.Op, OK: true, Result: result})
	})
	r := &Runtime{}
	err := r.agentReady(context.Background(), sandboxLayout{agentSocketPath: socketPath})
	if err == nil || !strings.Contains(err.Error(), "response id mismatch") {
		t.Fatalf("expected mismatched id error, got %v", err)
	}
}

func TestAgentHandshakeRejectsCapabilityMismatch(t *testing.T) {
	imagePath := writeTestQEMUBaseImage(t)
	socketPath := startTestAgentSocket(t, func(conn net.Conn) {
		defer conn.Close()
		request, _ := agentproto.ReadMessage(conn)
		result, _ := json.Marshal(agentproto.HelloResult{
			ProtocolVersion:          agentproto.ProtocolVersion,
			WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
			Capabilities:             []string{"exec", "files"},
		})
		_ = agentproto.WriteMessage(conn, agentproto.Message{ID: request.ID, Op: request.Op, OK: true, Result: result})
	})
	r := &Runtime{}
	_, err := r.agentHandshakeForSandbox(context.Background(), sandboxLayout{agentSocketPath: socketPath}, model.Sandbox{BaseImageRef: imagePath})
	if err == nil || !strings.Contains(err.Error(), "capabilities mismatch") {
		t.Fatalf("expected capability mismatch error, got %v", err)
	}
}

func TestAgentRoundTripRejectsOversizeResponse(t *testing.T) {
	socketPath := startTestAgentSocket(t, func(conn net.Conn) {
		defer conn.Close()
		_, _ = agentproto.ReadMessage(conn)
		_, _ = conn.Write([]byte{0xff, 0xff, 0xff, 0xff})
	})
	r := &Runtime{}
	err := r.agentReady(context.Background(), sandboxLayout{agentSocketPath: socketPath})
	if err == nil || !strings.Contains(err.Error(), "exceeds max size") {
		t.Fatalf("expected oversize response error, got %v", err)
	}
}

func TestAgentWriteWorkspaceFileBytesChunksLargeFiles(t *testing.T) {
	content := []byte(strings.Repeat("abcdef", agentproto.MaxFileChunkSize/6+5))
	expectedChunks := (len(content) + agentproto.MaxFileChunkSize - 1) / agentproto.MaxFileChunkSize
	type writeChunk struct {
		Offset    int64
		TotalSize int64
		Truncate  bool
		EOF       bool
		Data      []byte
	}
	chunksCh := make(chan writeChunk, expectedChunks)
	socketPath := startTestAgentSocketLoop(t, expectedChunks+1, func(conn net.Conn) {
		defer conn.Close()
		request, _ := agentproto.ReadMessage(conn)
		if request.Op == agentproto.OpHello {
			result, _ := json.Marshal(agentproto.HelloResult{
				ProtocolVersion:          agentproto.ProtocolVersion,
				WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
				MaxFileTransferBytes:     agentproto.MaxFileTransferSize,
			})
			_ = agentproto.WriteMessage(conn, agentproto.Message{ID: request.ID, Op: request.Op, OK: true, Result: result})
			return
		}
		var payload agentproto.FileWriteRequest
		if err := json.Unmarshal(request.Result, &payload); err != nil {
			t.Errorf("unmarshal file write request: %v", err)
			return
		}
		data, err := agentproto.DecodeBytes(payload.Content)
		if err != nil {
			t.Errorf("decode file write chunk: %v", err)
			return
		}
		chunksCh <- writeChunk{Offset: payload.Offset, TotalSize: payload.TotalSize, Truncate: payload.Truncate, EOF: payload.EOF, Data: data}
		_ = agentproto.WriteMessage(conn, agentproto.Message{ID: request.ID, Op: request.Op, OK: true})
	})
	r := &Runtime{workspaceFileTransferMaxBytes: model.DefaultWorkspaceFileTransferMaxBytes}
	if err := r.agentWriteWorkspaceFileBytes(context.Background(), sandboxLayout{agentSocketPath: socketPath}, "nested/file.txt", content); err != nil {
		t.Fatalf("write workspace file bytes: %v", err)
	}
	close(chunksCh)
	var rebuilt []byte
	for chunk := range chunksCh {
		if chunk.TotalSize != int64(len(content)) {
			t.Fatalf("unexpected total size %d", chunk.TotalSize)
		}
		if chunk.Truncate != (chunk.Offset == 0) {
			t.Fatalf("unexpected truncate flag for offset %d", chunk.Offset)
		}
		rebuilt = append(rebuilt, chunk.Data...)
	}
	if string(rebuilt) != string(content) {
		t.Fatal("chunked write content mismatch")
	}
}

func TestAgentWriteWorkspaceFileBytesRejectsOversizePayload(t *testing.T) {
	socketPath := startTestAgentSocket(t, func(conn net.Conn) {
		defer conn.Close()
		request, _ := agentproto.ReadMessage(conn)
		result, _ := json.Marshal(agentproto.HelloResult{
			ProtocolVersion:          agentproto.ProtocolVersion,
			WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
			MaxFileTransferBytes:     agentproto.MaxFileTransferSize,
		})
		_ = agentproto.WriteMessage(conn, agentproto.Message{ID: request.ID, Op: request.Op, OK: true, Result: result})
	})
	r := &Runtime{workspaceFileTransferMaxBytes: model.DefaultWorkspaceFileTransferMaxBytes}
	content := []byte(strings.Repeat("x", int(model.DefaultWorkspaceFileTransferMaxBytes)+1))
	err := r.agentWriteWorkspaceFileBytes(context.Background(), sandboxLayout{agentSocketPath: socketPath}, "large.bin", content)
	if err == nil || !strings.Contains(err.Error(), "transfer limit") {
		t.Fatalf("expected oversize write rejection, got %v", err)
	}
}

func TestAgentReadWorkspaceFileBytesAssemblesChunks(t *testing.T) {
	content := []byte(strings.Repeat("chunk-", agentproto.MaxFileChunkSize/6+3))
	expectedChunks := (len(content) + agentproto.MaxFileChunkSize - 1) / agentproto.MaxFileChunkSize
	socketPath := startTestAgentSocketLoop(t, expectedChunks+1, func(conn net.Conn) {
		defer conn.Close()
		request, _ := agentproto.ReadMessage(conn)
		if request.Op == agentproto.OpHello {
			result, _ := json.Marshal(agentproto.HelloResult{
				ProtocolVersion:          agentproto.ProtocolVersion,
				WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
				MaxFileTransferBytes:     agentproto.MaxFileTransferSize,
			})
			_ = agentproto.WriteMessage(conn, agentproto.Message{ID: request.ID, Op: request.Op, OK: true, Result: result})
			return
		}
		var payload agentproto.FileReadRequest
		if err := json.Unmarshal(request.Result, &payload); err != nil {
			t.Errorf("unmarshal file read request: %v", err)
			return
		}
		end := int(payload.Offset) + agentproto.MaxFileChunkSize
		if end > len(content) {
			end = len(content)
		}
		result, _ := json.Marshal(agentproto.FileReadResult{
			Path:    payload.Path,
			Content: agentproto.EncodeBytes(content[int(payload.Offset):end]),
			Offset:  payload.Offset,
			Size:    int64(len(content)),
			EOF:     end == len(content),
		})
		_ = agentproto.WriteMessage(conn, agentproto.Message{ID: request.ID, Op: request.Op, OK: true, Result: result})
	})
	r := &Runtime{workspaceFileTransferMaxBytes: model.DefaultWorkspaceFileTransferMaxBytes}
	data, err := r.agentReadWorkspaceFileBytes(context.Background(), sandboxLayout{agentSocketPath: socketPath}, "nested/file.txt")
	if err != nil {
		t.Fatalf("read workspace file bytes: %v", err)
	}
	if string(data) != string(content) {
		t.Fatal("chunked read content mismatch")
	}
}

func TestAgentReadWorkspaceFileBytesRejectsOversizePayload(t *testing.T) {
	socketPath := startTestAgentSocketLoop(t, 2, func(conn net.Conn) {
		defer conn.Close()
		request, _ := agentproto.ReadMessage(conn)
		if request.Op == agentproto.OpHello {
			result, _ := json.Marshal(agentproto.HelloResult{
				ProtocolVersion:          agentproto.ProtocolVersion,
				WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
				MaxFileTransferBytes:     agentproto.MaxFileTransferSize,
			})
			_ = agentproto.WriteMessage(conn, agentproto.Message{ID: request.ID, Op: request.Op, OK: true, Result: result})
			return
		}
		result, _ := json.Marshal(agentproto.FileReadResult{
			Path:    "/workspace/large.bin",
			Content: agentproto.EncodeBytes([]byte("ok")),
			Offset:  0,
			Size:    model.DefaultWorkspaceFileTransferMaxBytes + 1,
			EOF:     true,
		})
		_ = agentproto.WriteMessage(conn, agentproto.Message{ID: request.ID, Op: request.Op, OK: true, Result: result})
	})
	r := &Runtime{workspaceFileTransferMaxBytes: model.DefaultWorkspaceFileTransferMaxBytes}
	_, err := r.agentReadWorkspaceFileBytes(context.Background(), sandboxLayout{agentSocketPath: socketPath}, "large.bin")
	if !errors.Is(err, model.ErrFileTransferTooLarge) {
		t.Fatalf("expected oversize read rejection, got %v", err)
	}
}

func TestAgentTTYHandleRejectsWrongSessionData(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	reader, writer := io.Pipe()
	handle := &agentTTYHandle{
		conn:      clientConn,
		sessionID: "pty-123",
		reader:    reader,
		writer:    writer,
	}
	go handle.readLoop()

	payload, _ := json.Marshal(agentproto.PTYData{SessionID: "wrong-session", Data: agentproto.EncodeBytes([]byte("nope"))})
	if err := agentproto.WriteMessage(serverConn, agentproto.Message{ID: "server-1", Op: agentproto.OpPTYData, OK: true, Result: payload}); err != nil {
		t.Fatalf("write PTY payload: %v", err)
	}
	buf := make([]byte, 1)
	if n, err := reader.Read(buf); err != io.EOF || n != 0 {
		t.Fatalf("expected PTY reader EOF after wrong session, got n=%d err=%v", n, err)
	}
}

func TestAgentTTYHandleCloseIsIdempotent(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	go func() {
		for {
			if _, err := agentproto.ReadMessage(serverConn); err != nil {
				return
			}
		}
	}()

	reader, writer := io.Pipe()
	handle := &agentTTYHandle{
		conn:      clientConn,
		sessionID: "pty-123",
		reader:    reader,
		writer:    writer,
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("first close failed: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
}

func TestAgentTTYHandleStopsAfterEOFFrame(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	reader, writer := io.Pipe()
	handle := &agentTTYHandle{
		conn:      clientConn,
		sessionID: "pty-123",
		reader:    reader,
		writer:    writer,
	}
	go handle.readLoop()

	payload, _ := json.Marshal(agentproto.PTYData{SessionID: "pty-123", EOF: true})
	if err := agentproto.WriteMessage(serverConn, agentproto.Message{ID: "server-1", Op: agentproto.OpPTYData, OK: true, Result: payload}); err != nil {
		t.Fatalf("write PTY EOF payload: %v", err)
	}
	buf := make([]byte, 1)
	if n, err := reader.Read(buf); err != io.EOF || n != 0 {
		t.Fatalf("expected EOF after PTY exit frame, got n=%d err=%v", n, err)
	}
}

func TestControlTransportUsesVirtioSerialForAgentMode(t *testing.T) {
	r := &Runtime{controlMode: model.GuestControlModeAgent}
	transport, err := r.controlTransportForSandbox(model.Sandbox{ControlMode: model.GuestControlModeAgent})
	if err != nil {
		t.Fatalf("control transport: %v", err)
	}
	if transport.mode != model.GuestControlModeAgent {
		t.Fatalf("unexpected transport mode %q", transport.mode)
	}
	if transport.name != defaultAgentTransport {
		t.Fatalf("unexpected transport name %q", transport.name)
	}
}

func TestControlModeForSandboxUsesGuestContractWhenUnset(t *testing.T) {
	imagePath := writeTestQEMUBaseImage(t)
	r := &Runtime{controlMode: model.GuestControlModeSSHCompat}
	mode := r.controlModeForSandbox(model.Sandbox{BaseImageRef: imagePath})
	if mode != model.GuestControlModeAgent {
		t.Fatalf("expected guest contract control mode, got %q", mode)
	}
}

func TestCreateRejectsUnsupportedAgentTransport(t *testing.T) {
	imagePath := writeTestQEMUBaseImage(t)
	payload, err := os.ReadFile(guestimage.SidecarPath(imagePath))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var contract guestimage.Contract
	if err := json.Unmarshal(payload, &contract); err != nil {
		t.Fatalf("unmarshal sidecar: %v", err)
	}
	contract.Control.SupportedTransports = []string{"vsock"}
	payload, err = json.Marshal(contract)
	if err != nil {
		t.Fatalf("marshal sidecar: %v", err)
	}
	if err := os.WriteFile(guestimage.SidecarPath(imagePath), payload, 0o644); err != nil {
		t.Fatalf("rewrite sidecar: %v", err)
	}
	spec := model.SandboxSpec{
		SandboxID:                "sbx-unsupported-transport",
		BaseImageRef:             imagePath,
		Profile:                  model.GuestProfileCore,
		ControlMode:              model.GuestControlModeAgent,
		ControlProtocolVersion:   model.DefaultGuestControlProtocolVersion,
		WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
		ImageContractVersion:     model.DefaultImageContractVersion,
		StorageRoot:              filepath.Join(t.TempDir(), "rootfs"),
		WorkspaceRoot:            filepath.Join(t.TempDir(), "workspace"),
		CacheRoot:                filepath.Join(t.TempDir(), "cache"),
		DiskLimitMB:              16,
	}
	r := &Runtime{agentTransport: defaultAgentTransport}
	if _, err := r.Create(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "does not support runtime agent transport") {
		t.Fatalf("expected unsupported transport error, got %v", err)
	}
}

func TestValidateHostRequiresPSCommand(t *testing.T) {
	opts := Options{
		Binary:         "qemu-system-x86_64",
		BaseImagePath:  "/images/guest.qcow2",
		SSHUser:        "sandbox",
		SSHKeyPath:     "/keys/id_ed25519",
		SSHHostKeyPath: "/keys/guest_host_ed25519.pub",
		SSHBinary:      "ssh",
		SCPBinary:      "scp",
		BootTimeout:    time.Minute,
	}
	err := validateHost(opts, "qemu-img", "", hostProbe{
		commandExists: func(name string) error {
			if name == "ps" {
				return errors.New("missing")
			}
			return nil
		},
		fileReadable: func(string) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), `"ps"`) {
		t.Fatalf("expected ps validation failure, got %v", err)
	}
}

func TestCreateAndSnapshotArtifacts(t *testing.T) {
	base := t.TempDir()
	rootfs := filepath.Join(base, "rootfs")
	workspace := filepath.Join(base, "workspace")
	spec := model.SandboxSpec{
		SandboxID:     "sbx-test",
		BaseImageRef:  writeTestQEMUBaseImage(t),
		StorageRoot:   rootfs,
		WorkspaceRoot: workspace,
		CacheRoot:     filepath.Join(base, "cache"),
		DiskLimitMB:   16,
	}
	r := &Runtime{sshHostKeyPath: writeTestQEMUHostKey(t)}
	state, err := r.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if state.Status != model.SandboxStatusStopped {
		t.Fatalf("unexpected create status: %s", state.Status)
	}
	layout := layoutForSpec(spec)
	for _, path := range []string{layout.rootDiskPath, layout.workspaceDiskPath, layout.serialLogPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected artifact %s: %v", path, err)
		}
	}

	sandbox := model.Sandbox{
		ID:            spec.SandboxID,
		RuntimeID:     state.RuntimeID,
		StorageRoot:   spec.StorageRoot,
		WorkspaceRoot: spec.WorkspaceRoot,
		CacheRoot:     spec.CacheRoot,
	}
	snapshot, err := r.CreateSnapshot(context.Background(), sandbox, "snap-test")
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	for _, path := range []string{snapshot.ImageRef, snapshot.WorkspaceTar} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected snapshot artifact %s: %v", path, err)
		}
	}
}

func TestInspectReportsBootingWhenGuestIsAliveButNotReadyWithinBootWindow(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer cmd.Process.Kill()

	base := t.TempDir()
	layout := sandboxLayout{
		baseDir:    base,
		runtimeDir: filepath.Join(base, ".runtime"),
		pidPath:    filepath.Join(base, ".runtime", "qemu.pid"),
	}
	if err := ensureLayout(layout); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	if err := os.WriteFile(layout.pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	r := &Runtime{
		bootTimeout:  time.Second,
		pollInterval: 10 * time.Millisecond,
		sshReady: func(context.Context, sshTarget) error {
			return errors.New("not ready")
		},
	}
	sandbox := model.Sandbox{
		ID:            "sbx-inspect",
		RuntimeID:     "qemu-sbx-inspect",
		ControlMode:   model.GuestControlModeSSHCompat,
		StorageRoot:   filepath.Join(base, "rootfs"),
		WorkspaceRoot: filepath.Join(base, "workspace"),
		CacheRoot:     filepath.Join(base, "cache"),
	}
	state, err := r.Inspect(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("inspect failed: %v", err)
	}
	if state.Status != model.SandboxStatusBooting {
		t.Fatalf("unexpected status: %s", state.Status)
	}
}

func TestInspectReportsDegradedWhenGuestIsAliveButNotReadyAfterBootWindow(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer cmd.Process.Kill()

	base := t.TempDir()
	layout := sandboxLayout{
		baseDir:    base,
		runtimeDir: filepath.Join(base, ".runtime"),
		pidPath:    filepath.Join(base, ".runtime", "qemu.pid"),
	}
	if err := ensureLayout(layout); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	if err := os.WriteFile(layout.pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(layout.pidPath, old, old); err != nil {
		t.Fatalf("age pid file: %v", err)
	}
	r := &Runtime{
		bootTimeout:  time.Second,
		pollInterval: 10 * time.Millisecond,
		sshReady: func(context.Context, sshTarget) error {
			return errors.New("not ready")
		},
	}
	sandbox := model.Sandbox{
		ID:            "sbx-inspect-degraded",
		RuntimeID:     "qemu-sbx-inspect-degraded",
		ControlMode:   model.GuestControlModeSSHCompat,
		StorageRoot:   filepath.Join(base, "rootfs"),
		WorkspaceRoot: filepath.Join(base, "workspace"),
		CacheRoot:     filepath.Join(base, "cache"),
	}
	state, err := r.Inspect(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("inspect failed: %v", err)
	}
	if state.Status != model.SandboxStatusDegraded {
		t.Fatalf("unexpected status: %s", state.Status)
	}
}

func TestSuspendResumeAndInspectRoundTrip(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer cmd.Process.Kill()

	base := t.TempDir()
	sandbox := model.Sandbox{
		ID:            "sbx-suspend",
		RuntimeID:     "qemu-sbx-suspend@2222",
		ControlMode:   model.GuestControlModeSSHCompat,
		StorageRoot:   filepath.Join(base, "rootfs"),
		WorkspaceRoot: filepath.Join(base, "workspace"),
		CacheRoot:     filepath.Join(base, "cache"),
	}
	layout := layoutForSandbox(sandbox)
	if err := ensureLayout(layout); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	if err := os.WriteFile(layout.pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	r := &Runtime{
		bootTimeout:  time.Second,
		pollInterval: 10 * time.Millisecond,
		sshReady:     func(context.Context, sshTarget) error { return nil },
	}

	state, err := r.Suspend(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("suspend failed: %v", err)
	}
	if state.Status != model.SandboxStatusSuspended {
		t.Fatalf("unexpected suspend status: %s", state.Status)
	}
	inspected, err := r.Inspect(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("inspect after suspend failed: %v", err)
	}
	if inspected.Status != model.SandboxStatusSuspended {
		t.Fatalf("unexpected inspect status while suspended: %s", inspected.Status)
	}
	if !isSuspended(layout) {
		t.Fatal("expected suspended marker to exist")
	}

	state, err = r.Resume(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if state.Status != model.SandboxStatusRunning {
		t.Fatalf("unexpected resume status: %s", state.Status)
	}
	if isSuspended(layout) {
		t.Fatal("expected suspended marker to be removed")
	}
}

func TestStopClearsSuspendedMarker(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer cmd.Process.Kill()

	base := t.TempDir()
	sandbox := model.Sandbox{
		ID:            "sbx-stop-suspended",
		RuntimeID:     "qemu-sbx-stop-suspended@2222",
		ControlMode:   model.GuestControlModeSSHCompat,
		StorageRoot:   filepath.Join(base, "rootfs"),
		WorkspaceRoot: filepath.Join(base, "workspace"),
		CacheRoot:     filepath.Join(base, "cache"),
	}
	layout := layoutForSandbox(sandbox)
	if err := ensureLayout(layout); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	if err := os.WriteFile(layout.pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	if err := touchFile(suspendedMarkerPath(layout)); err != nil {
		t.Fatalf("touch suspended marker: %v", err)
	}
	r := &Runtime{}
	state, err := r.Stop(context.Background(), sandbox, true)
	if err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if state.Status != model.SandboxStatusStopped {
		t.Fatalf("unexpected stop status: %s", state.Status)
	}
	if _, err := os.Stat(suspendedMarkerPath(layout)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected suspended marker removed, got %v", err)
	}
}

func TestStartUsesRunnerAndReadinessProbe(t *testing.T) {
	base := t.TempDir()
	sandbox := model.Sandbox{
		ID:            "sbx-start",
		RuntimeID:     "qemu-sbx-start",
		ControlMode:   model.GuestControlModeSSHCompat,
		StorageRoot:   filepath.Join(base, "rootfs"),
		WorkspaceRoot: filepath.Join(base, "workspace"),
		CacheRoot:     filepath.Join(base, "cache"),
		MemoryLimitMB: 512,
		CPULimit:      model.CPUCores(1),
		NetworkMode:   model.NetworkModeInternetEnabled,
	}
	layout := layoutForSandbox(sandbox)
	if err := ensureLayout(layout); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	r := &Runtime{
		qemuBinary:   "qemu-system-x86_64",
		accelerator:  "kvm",
		bootTimeout:  time.Second,
		pollInterval: 10 * time.Millisecond,
		runCommand: func(ctx context.Context, binary string, args ...string) ([]byte, error) {
			if err := os.WriteFile(layout.pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
				return nil, err
			}
			return nil, nil
		},
		sshReady: func(context.Context, sshTarget) error { return nil },
	}
	state, err := r.Start(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if state.Status != model.SandboxStatusRunning {
		t.Fatalf("unexpected start status: %s", state.Status)
	}
	if _, ok := sshPortFromRuntimeID(state.RuntimeID); !ok {
		t.Fatalf("expected runtime id to carry ssh port, got %q", state.RuntimeID)
	}
}

func TestStartCleansUpFailedBoot(t *testing.T) {
	base := t.TempDir()
	sandbox := model.Sandbox{
		ID:            "sbx-failed-start",
		RuntimeID:     "qemu-sbx-failed-start",
		ControlMode:   model.GuestControlModeSSHCompat,
		StorageRoot:   filepath.Join(base, "rootfs"),
		WorkspaceRoot: filepath.Join(base, "workspace"),
		CacheRoot:     filepath.Join(base, "cache"),
		MemoryLimitMB: 512,
		CPULimit:      model.CPUCores(1),
		NetworkMode:   model.NetworkModeInternetEnabled,
	}
	layout := layoutForSandbox(sandbox)
	if err := ensureLayout(layout); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	var child *exec.Cmd
	r := &Runtime{
		qemuBinary:   "qemu-system-x86_64",
		accelerator:  "kvm",
		bootTimeout:  time.Second,
		pollInterval: 10 * time.Millisecond,
		runCommand: func(ctx context.Context, binary string, args ...string) ([]byte, error) {
			child = exec.Command("sleep", "30")
			if err := child.Start(); err != nil {
				return nil, err
			}
			if err := os.WriteFile(layout.pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
				return nil, err
			}
			return nil, nil
		},
		sshReady: func(context.Context, sshTarget) error {
			return errors.New("not ready")
		},
	}
	if _, err := r.Start(context.Background(), sandbox); err == nil {
		t.Fatal("expected start to fail")
	}
	if child == nil {
		t.Fatal("expected fake qemu process to start")
	}
	done := make(chan error, 1)
	go func() {
		done <- child.Wait()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected failed start to reap qemu process")
	}
	if _, err := os.Stat(layout.pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected pid file to be removed, got %v", err)
	}
}

func TestBaseSSHArgsIncludeTTYAndIdentityOptions(t *testing.T) {
	r := &Runtime{
		sshUser:    "ubuntu",
		sshKeyPath: "/tmp/id_ed25519",
	}
	target := sshTarget{port: 2222, knownHostsPath: "/tmp/known_hosts", hostKeyAlias: "or3-qemu-sbx-test"}

	nonTTY := strings.Join(r.baseSSHArgs(target, false), " ")
	for _, snippet := range []string{
		"-o BatchMode=yes",
		"-o IdentitiesOnly=yes",
		"-o StrictHostKeyChecking=yes",
		"-o UserKnownHostsFile=/tmp/known_hosts",
		"-o HostKeyAlias=or3-qemu-sbx-test",
		"-i /tmp/id_ed25519",
		"-p 2222",
		"-T",
		"ubuntu@127.0.0.1",
	} {
		if !strings.Contains(nonTTY, snippet) {
			t.Fatalf("expected %q in ssh args: %s", snippet, nonTTY)
		}
	}
	if strings.Contains(nonTTY, "-tt") {
		t.Fatalf("did not expect tty args in non-tty command: %s", nonTTY)
	}

	tty := strings.Join(r.baseSSHArgs(target, true), " ")
	if !strings.Contains(tty, "-tt") {
		t.Fatalf("expected tty args in ssh command: %s", tty)
	}
}

func TestSplitDiskBytesUsesEvenFirstPassPolicy(t *testing.T) {
	rootfsBytes, workspaceBytes := splitDiskBytes(513)
	totalBytes := int64(513) * 1024 * 1024
	if rootfsBytes+workspaceBytes != totalBytes {
		t.Fatalf("unexpected total bytes: got %d want %d", rootfsBytes+workspaceBytes, totalBytes)
	}
	delta := rootfsBytes - workspaceBytes
	if delta < 0 {
		delta = -delta
	}
	if delta > 1024*1024 {
		t.Fatalf("expected near-even split, delta=%d", delta)
	}
}

func TestWorkspaceGuestPathRejectsEscapes(t *testing.T) {
	if _, err := workspaceGuestPath("../../etc/passwd"); err == nil {
		t.Fatal("expected workspace escape rejection")
	}
	target, err := workspaceGuestPath("nested/file.txt")
	if err != nil {
		t.Fatalf("unexpected workspace guest path error: %v", err)
	}
	if target != "/workspace/nested/file.txt" {
		t.Fatalf("unexpected workspace guest path %q", target)
	}
}

func startTestAgentSocket(t *testing.T, handler func(net.Conn)) string {
	t.Helper()
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("or3-agent-%d.sock", time.Now().UnixNano()))
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		handler(conn)
	}()
	return socketPath
}

func startTestAgentSocketLoop(t *testing.T, accepts int, handler func(net.Conn)) string {
	t.Helper()
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("or3-agent-loop-%d.sock", time.Now().UnixNano()))
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < accepts; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			handler(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		<-done
	})
	return socketPath
}

func writeTestQEMUBaseImage(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "base.qcow2")
	if err := os.WriteFile(path, []byte("qcow2"), 0o644); err != nil {
		t.Fatalf("write test qemu base image: %v", err)
	}
	sha, err := guestimage.ComputeSHA256(path)
	if err != nil {
		t.Fatalf("compute test qemu base image sha: %v", err)
	}
	contract, err := json.Marshal(guestimage.Contract{
		ContractVersion:          model.DefaultImageContractVersion,
		ImagePath:                path,
		ImageSHA256:              sha,
		BuildVersion:             "test",
		Profile:                  model.GuestProfileCore,
		Capabilities:             []string{"exec", "files", "pty", "tcp_bridge"},
		Control:                  guestimage.ControlContract{Mode: model.GuestControlModeAgent, ProtocolVersion: model.DefaultGuestControlProtocolVersion, SupportedTransports: []string{"virtio-serial"}},
		WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
		SSHPresent:               false,
	})
	if err != nil {
		t.Fatalf("marshal test qemu contract: %v", err)
	}
	if err := os.WriteFile(guestimage.SidecarPath(path), contract, 0o644); err != nil {
		t.Fatalf("write test qemu contract: %v", err)
	}
	return path
}

func writeTestQEMUHostKey(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guest_host_ed25519.pub")
	if err := os.WriteFile(path, []byte("ssh-ed25519 AAAATESTHOSTKEY or3-test"), 0o644); err != nil {
		t.Fatalf("write test qemu host key: %v", err)
	}
	return path
}

func TestQemuSizePreservesExactBytes(t *testing.T) {
	if got := qemuSize(512 * 1024); got != "524288" {
		t.Fatalf("unexpected qemu size for half MiB: %q", got)
	}
	if got := qemuSize(256*1024*1024 + 512*1024); got != "268959744" {
		t.Fatalf("unexpected qemu size for fractional MiB split: %q", got)
	}
}

func TestBootFailureReasonReadsSerialMarkers(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "serial.log")
	if err := os.WriteFile(logPath, []byte("Kernel panic - not syncing"), 0o644); err != nil {
		t.Fatalf("write serial log: %v", err)
	}
	reason, ok := bootFailureReason(logPath)
	if !ok || !strings.Contains(reason, "kernel panic") {
		t.Fatalf("expected kernel panic marker, got %q %v", reason, ok)
	}
}

func TestMeasureStorageAggregatesSandboxArtifacts(t *testing.T) {
	base := t.TempDir()
	sandbox := model.Sandbox{
		ID:            "sbx-storage",
		StorageRoot:   filepath.Join(base, "rootfs"),
		WorkspaceRoot: filepath.Join(base, "workspace"),
		CacheRoot:     filepath.Join(base, "cache"),
	}
	layout := layoutForSandbox(sandbox)
	if err := ensureLayout(layout); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	snapshotDir := filepath.Join(sandbox.StorageRoot, ".snapshots", "snap-1")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		t.Fatalf("mkdir snapshot dir: %v", err)
	}
	for path, content := range map[string]string{
		layout.rootDiskPath:                      "rootfs-bytes",
		layout.workspaceDiskPath:                 "workspace-bytes",
		filepath.Join(sandbox.CacheRoot, "x"):    "cache-bytes",
		filepath.Join(snapshotDir, "rootfs.img"): "snapshot-bytes",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write test artifact %s: %v", path, err)
		}
	}

	usage, err := (&Runtime{}).MeasureStorage(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("measure storage: %v", err)
	}
	if usage.RootfsBytes < int64(len("rootfs-bytes")) {
		t.Fatalf("unexpected rootfs bytes: %d", usage.RootfsBytes)
	}
	if usage.WorkspaceBytes < int64(len("workspace-bytes")) {
		t.Fatalf("unexpected workspace bytes: %d", usage.WorkspaceBytes)
	}
	if usage.CacheBytes < int64(len("cache-bytes")) {
		t.Fatalf("unexpected cache bytes: %d", usage.CacheBytes)
	}
	if usage.SnapshotBytes < int64(len("snapshot-bytes")) {
		t.Fatalf("unexpected snapshot bytes: %d", usage.SnapshotBytes)
	}
	if usage.RootfsEntries != 1 {
		t.Fatalf("unexpected rootfs entries: %d", usage.RootfsEntries)
	}
	if usage.WorkspaceEntries != 1 {
		t.Fatalf("unexpected workspace entries: %d", usage.WorkspaceEntries)
	}
	if usage.CacheEntries != 1 {
		t.Fatalf("unexpected cache entries: %d", usage.CacheEntries)
	}
	if usage.SnapshotEntries != 1 {
		t.Fatalf("unexpected snapshot entries: %d", usage.SnapshotEntries)
	}
}

func TestRemoteExecScriptsIncludeWorkingDirEnvAndPidTracking(t *testing.T) {
	script, err := buildTrackedRemoteScript(
		[]string{"python3", "-c", "print('ok')"},
		"/workspace/app",
		map[string]string{"HELLO": "world"},
		"/tmp/or3-exec.pid",
	)
	if err != nil {
		t.Fatalf("build tracked remote script: %v", err)
	}
	for _, snippet := range []string{
		"rm -f '/tmp/or3-exec.pid'",
		"cd '/workspace/app'",
		"export HELLO='world'",
		"setsid sh -lc",
		"echo \"$child\" > '/tmp/or3-exec.pid'",
	} {
		if !strings.Contains(script, snippet) {
			t.Fatalf("expected %q in tracked script: %s", snippet, script)
		}
	}

	interactive, err := buildInteractiveRemoteScript([]string{"bash"}, "/workspace", nil)
	if err != nil {
		t.Fatalf("build interactive remote script: %v", err)
	}
	if !strings.Contains(interactive, "exec sh -lc") {
		t.Fatalf("expected interactive script to exec shell: %s", interactive)
	}
	if !strings.Contains(interactive, "cd '/workspace'") {
		t.Fatalf("expected interactive script to change directory: %s", interactive)
	}
}

func TestRemoteExecScriptsRejectInvalidEnvKey(t *testing.T) {
	_, err := buildDetachedRemoteScript([]string{"sh", "-lc", "echo ok"}, "/workspace", map[string]string{"BAD-KEY": "value"})
	if err == nil || !strings.Contains(err.Error(), "invalid env key") {
		t.Fatalf("expected invalid env key error, got %v", err)
	}
}
