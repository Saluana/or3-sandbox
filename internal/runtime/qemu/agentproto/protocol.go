package agentproto

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"or3-sandbox/internal/model"
)

// ProtocolVersion is the current guest-agent protocol version.
const ProtocolVersion = "3"

const (
	// MaxMessageSize is the maximum encoded size of a single agent message.
	MaxMessageSize = 16 * 1024 * 1024
	// MaxRequestIDLength is the maximum length of a request correlation ID.
	MaxRequestIDLength = 128
	// MaxFileTransferSize is the maximum file transfer size supported by the protocol.
	MaxFileTransferSize = model.MaxWorkspaceFileTransferCeilingBytes
	// MaxFileChunkSize is the maximum file payload size carried in one message.
	MaxFileChunkSize = 256 * 1024
	// MaxBridgeChunkSize is the maximum bridged TCP payload size per message.
	MaxBridgeChunkSize = 32 * 1024
)

const (
	// OpHello performs the initial guest-agent handshake.
	OpHello = "hello"
	// OpPing queries the guest agent readiness state and session health.
	OpPing = "ping"
	// OpExecStart executes a command in the guest.
	OpExecStart = "exec_start"
	// OpExecEvent carries streaming exec output and terminal results.
	OpExecEvent = "exec_event"
	// OpExecCancel cancels an in-flight exec.
	OpExecCancel = "exec_cancel"
	// OpPTYOpen opens a PTY session in the guest.
	OpPTYOpen = "pty_open"
	// OpPTYData carries PTY byte stream data.
	OpPTYData = "pty_data"
	// OpPTYResize resizes an active PTY session.
	OpPTYResize = "pty_resize"
	// OpPTYClose closes an active PTY session.
	OpPTYClose = "pty_close"
	// OpFileOpen opens a workspace file stream.
	OpFileOpen = "file_open"
	// OpFileData carries workspace file bytes.
	OpFileData = "file_data"
	// OpFileClose closes a workspace file stream.
	OpFileClose = "file_close"
	// OpFileDelete deletes a path in the guest workspace.
	OpFileDelete = "file_delete"
	// OpMkdir creates a directory in the guest workspace.
	OpMkdir = "mkdir"
	// OpArchiveStream streams normalized workspace archive entries.
	OpArchiveStream = "archive_stream"
	// OpTCPBridgeOpen opens a guest-local TCP bridge session.
	OpTCPBridgeOpen = "tcp_bridge_open"
	// OpTCPBridgeData carries bridged TCP payload data.
	OpTCPBridgeData = "tcp_bridge_data"
	// OpShutdown asks the guest to shut down or reboot.
	OpShutdown = "shutdown"
)

// ErrProtocol reports malformed or invalid guest-agent messages.
var ErrProtocol = errors.New("guest agent protocol error")

// Message is the common envelope exchanged over the guest-agent transport.
type Message struct {
	ID     string          `json:"id,omitempty"`
	Op     string          `json:"op"`
	OK     bool            `json:"ok,omitempty"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// HelloResult is returned by the guest agent hello handshake.
type HelloResult struct {
	ProtocolVersion          string   `json:"protocol_version"`
	WorkspaceContractVersion string   `json:"workspace_contract_version"`
	Capabilities             []string `json:"capabilities,omitempty"`
	MaxFileTransferBytes     int64    `json:"max_file_transfer_bytes,omitempty"`
	Ready                    bool     `json:"ready"`
}

// PingResult reports whether the guest agent is ready to serve requests.
type PingResult struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}

// ReadyResult is a compatibility alias for the pre-v3 readiness payload name.
type ReadyResult = PingResult

// ExecStartRequest describes a guest-agent exec request.
type ExecStartRequest struct {
	Command  []string          `json:"command"`
	Cwd      string            `json:"cwd,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Timeout  time.Duration     `json:"timeout,omitempty"`
	Detached bool              `json:"detached,omitempty"`
}

// ExecStartResult reports the opened exec session ID.
type ExecStartResult struct {
	ExecID string `json:"exec_id"`
}

// ExecResult reports the result of a guest-agent exec request.
type ExecResult struct {
	ExitCode        int       `json:"exit_code"`
	Status          string    `json:"status"`
	StartedAt       time.Time `json:"started_at"`
	CompletedAt     time.Time `json:"completed_at"`
	StdoutPreview   string    `json:"stdout_preview,omitempty"`
	StderrPreview   string    `json:"stderr_preview,omitempty"`
	StdoutTruncated bool      `json:"stdout_truncated,omitempty"`
	StderrTruncated bool      `json:"stderr_truncated,omitempty"`
}

// ExecEvent carries incremental stdout/stderr data or a final terminal result.
type ExecEvent struct {
	ExecID string      `json:"exec_id"`
	Stream string      `json:"stream,omitempty"`
	Data   string      `json:"data,omitempty"`
	EOF    bool        `json:"eof,omitempty"`
	Error  string      `json:"error,omitempty"`
	Result *ExecResult `json:"result,omitempty"`
}

// ExecCancelRequest identifies an exec session to cancel.
type ExecCancelRequest struct {
	ExecID string `json:"exec_id"`
}

// PTYOpenRequest opens a new PTY session.
type PTYOpenRequest struct {
	Command []string          `json:"command"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Rows    int               `json:"rows,omitempty"`
	Cols    int               `json:"cols,omitempty"`
}

// PTYOpenResult reports the opened PTY session ID.
type PTYOpenResult struct {
	SessionID string `json:"session_id"`
}

// PTYData carries PTY byte stream messages.
type PTYData struct {
	SessionID string `json:"session_id"`
	Data      string `json:"data,omitempty"`
	EOF       bool   `json:"eof,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
}

// PTYResizeRequest resizes an existing PTY session.
type PTYResizeRequest struct {
	SessionID string `json:"session_id"`
	Rows      int    `json:"rows"`
	Cols      int    `json:"cols"`
}

// FileOpenRequest opens a workspace file session.
type FileOpenRequest struct {
	Path     string `json:"path"`
	Mode     string `json:"mode"`
	Truncate bool   `json:"truncate,omitempty"`
}

// FileOpenResult reports an opened file session.
type FileOpenResult struct {
	SessionID string `json:"session_id"`
	Size      int64  `json:"size,omitempty"`
}

// FileData carries streamed workspace file bytes.
type FileData struct {
	SessionID string `json:"session_id"`
	Data      string `json:"data,omitempty"`
	EOF       bool   `json:"eof,omitempty"`
	Error     string `json:"error,omitempty"`
}

// FileCloseRequest closes an active file transfer session.
type FileCloseRequest struct {
	SessionID string `json:"session_id"`
}

// PathRequest names a guest path for delete and mkdir operations.
type PathRequest struct {
	Path string `json:"path"`
}

// ArchiveStreamRequest requests a normalized workspace archive stream.
type ArchiveStreamRequest struct {
	Paths []string `json:"paths,omitempty"`
}

// ArchiveStreamStart acknowledges an opened archive stream session.
type ArchiveStreamStart struct {
	SessionID string `json:"session_id"`
}

// ArchiveStreamChunk carries normalized archive entry metadata and file bytes.
type ArchiveStreamChunk struct {
	SessionID string    `json:"session_id"`
	Path      string    `json:"path,omitempty"`
	Type      string    `json:"type,omitempty"`
	Mode      int64     `json:"mode,omitempty"`
	ModTime   time.Time `json:"mod_time,omitempty"`
	Size      int64     `json:"size,omitempty"`
	Data      string    `json:"data,omitempty"`
	EOF       bool      `json:"eof,omitempty"`
	End       bool      `json:"end,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// ShutdownRequest asks the guest agent to shut down or reboot the guest.
type ShutdownRequest struct {
	Reboot bool `json:"reboot,omitempty"`
}

// TCPBridgeOpenRequest opens a TCP bridge to a guest-local port.
type TCPBridgeOpenRequest struct {
	TargetPort int `json:"target_port"`
}

// TCPBridgeOpenResult returns the opened TCP bridge session ID.
type TCPBridgeOpenResult struct {
	SessionID string `json:"session_id"`
}

// TCPBridgeData carries bridged TCP payloads.
type TCPBridgeData struct {
	SessionID string `json:"session_id"`
	Data      string `json:"data,omitempty"`
	EOF       bool   `json:"eof,omitempty"`
	Error     string `json:"error,omitempty"`
}

// EncodeBytes encodes binary payloads for JSON transport.
func EncodeBytes(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// DecodeBytes decodes JSON-transported binary payloads.
func DecodeBytes(value string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(value)
}

// WriteMessage validates and writes a length-prefixed agent message.
func WriteMessage(w io.Writer, message Message) error {
	if err := ValidateEnvelope(message); err != nil {
		return err
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(payload) > MaxMessageSize {
		return fmt.Errorf("agent message exceeds max size of %d bytes", MaxMessageSize)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

// ReadMessage reads and validates a single length-prefixed agent message.
func ReadMessage(r io.Reader) (Message, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Message{}, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 {
		return Message{}, fmt.Errorf("empty agent message")
	}
	if length > uint32(MaxMessageSize) {
		return Message{}, fmt.Errorf("agent message exceeds max size of %d bytes", MaxMessageSize)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Message{}, err
	}
	var message Message
	if err := json.Unmarshal(payload, &message); err != nil {
		return Message{}, fmt.Errorf("%w: invalid json: %v", ErrProtocol, err)
	}
	if err := ValidateEnvelope(message); err != nil {
		return Message{}, err
	}
	return message, nil
}

// ValidateEnvelope checks the message envelope fields shared by requests and
// responses.
func ValidateEnvelope(message Message) error {
	if strings.TrimSpace(message.Op) == "" {
		return fmt.Errorf("%w: agent message op is required", ErrProtocol)
	}
	if len(strings.TrimSpace(message.ID)) > MaxRequestIDLength {
		return fmt.Errorf("%w: agent message id exceeds max length of %d bytes", ErrProtocol, MaxRequestIDLength)
	}
	return nil
}

// ValidateRequest validates a guest-agent request message.
func ValidateRequest(message Message) error {
	if err := ValidateEnvelope(message); err != nil {
		return err
	}
	if RequiresRequestID(message.Op) && strings.TrimSpace(message.ID) == "" {
		return fmt.Errorf("%w: request id is required for %s", ErrProtocol, message.Op)
	}
	if message.OK {
		return fmt.Errorf("%w: requests must not set ok=true", ErrProtocol)
	}
	return nil
}

// ValidateResponse validates a guest-agent response message.
func ValidateResponse(message Message, expectedOp, expectedID string) error {
	if err := ValidateEnvelope(message); err != nil {
		return err
	}
	if strings.TrimSpace(message.ID) == "" {
		return fmt.Errorf("%w: response id is required", ErrProtocol)
	}
	if expectedID != "" && message.ID != expectedID {
		return fmt.Errorf("%w: response id mismatch: expected %q got %q", ErrProtocol, expectedID, message.ID)
	}
	if expectedOp != "" && message.Op != expectedOp {
		return fmt.Errorf("%w: response op mismatch: expected %q got %q", ErrProtocol, expectedOp, message.Op)
	}
	return nil
}

// RequiresRequestID reports whether op participates in request-response
// correlation.
func RequiresRequestID(op string) bool {
	switch op {
	case OpExecEvent, OpFileData, OpPTYData, OpPTYResize, OpPTYClose, OpTCPBridgeData, OpArchiveStream:
		return false
	default:
		return true
	}
}

// NewBufferedReadWriter wraps conn with a buffered read writer tuned for agent
// message exchange.
func NewBufferedReadWriter(conn io.ReadWriter) *bufio.ReadWriter {
	return bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
}
