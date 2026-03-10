package agentproto

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const ProtocolVersion = "1"

const MaxMessageSize = 16 * 1024 * 1024

const (
	OpHello      = "hello"
	OpReady      = "ready"
	OpExec       = "exec"
	OpPTYOpen    = "pty_open"
	OpPTYData    = "pty_data"
	OpPTYResize  = "pty_resize"
	OpPTYClose   = "pty_close"
	OpFileRead   = "file_read"
	OpFileWrite  = "file_write"
	OpFileDelete = "file_delete"
	OpMkdir      = "mkdir"
	OpShutdown   = "shutdown"
)

type Message struct {
	ID     string          `json:"id,omitempty"`
	Op     string          `json:"op"`
	OK     bool            `json:"ok,omitempty"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type HelloResult struct {
	ProtocolVersion          string   `json:"protocol_version"`
	WorkspaceContractVersion string   `json:"workspace_contract_version"`
	Capabilities             []string `json:"capabilities,omitempty"`
	Ready                    bool     `json:"ready"`
}

type ReadyResult struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}

type ExecRequest struct {
	Command  []string          `json:"command"`
	Cwd      string            `json:"cwd,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Timeout  time.Duration     `json:"timeout,omitempty"`
	Detached bool              `json:"detached,omitempty"`
}

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

type PTYOpenRequest struct {
	Command []string          `json:"command"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Rows    int               `json:"rows,omitempty"`
	Cols    int               `json:"cols,omitempty"`
}

type PTYOpenResult struct {
	SessionID string `json:"session_id"`
}

type PTYData struct {
	SessionID string `json:"session_id"`
	Data      string `json:"data,omitempty"`
	EOF       bool   `json:"eof,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
}

type PTYResizeRequest struct {
	SessionID string `json:"session_id"`
	Rows      int    `json:"rows"`
	Cols      int    `json:"cols"`
}

type FileReadRequest struct {
	Path string `json:"path"`
}

type FileReadResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type FileWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type PathRequest struct {
	Path string `json:"path"`
}

type ShutdownRequest struct {
	Reboot bool `json:"reboot,omitempty"`
}

func EncodeBytes(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func DecodeBytes(value string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(value)
}

func WriteMessage(w io.Writer, message Message) error {
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
		return Message{}, err
	}
	return message, nil
}

func NewBufferedReadWriter(conn io.ReadWriter) *bufio.ReadWriter {
	return bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
}
