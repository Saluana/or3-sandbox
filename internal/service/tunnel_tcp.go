package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"or3-sandbox/internal/model"
)

const sandboxLocalBridgeReady = "__OR3_TUNNEL_BRIDGE_READY__"

func (s *Service) OpenSandboxLocalConn(ctx context.Context, sandbox model.Sandbox, targetPort int) (net.Conn, error) {
	if sandbox.Status != model.SandboxStatusRunning {
		return nil, fmt.Errorf("sandbox %s is not running", sandbox.ID)
	}
	if targetPort < 1 || targetPort > 65535 {
		return nil, fmt.Errorf("target port must be between 1 and 65535")
	}
	handle, err := s.runtime.AttachTTY(ctx, sandbox, model.TTYRequest{
		Command: []string{"sh", "-lc", sandboxLocalTCPBridgeScript},
		Env: map[string]string{
			"OR3_TUNNEL_TARGET_PORT": strconv.Itoa(targetPort),
		},
		Cwd:  "/workspace",
		Cols: 1,
		Rows: 1,
	})
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReader(handle.Reader())
	if err := awaitSandboxLocalBridgeReady(reader); err != nil {
		_ = handle.Close()
		return nil, err
	}
	_ = s.touchSandboxActivity(ctx, sandbox)
	return &sandboxLocalConn{
		handle: handle,
		reader: reader,
		local:  tunnelBridgeAddr("daemon"),
		remote: tunnelBridgeAddr(fmt.Sprintf("sandbox:%s:127.0.0.1:%d", sandbox.ID, targetPort)),
	}, nil
}

func awaitSandboxLocalBridgeReady(reader *bufio.Reader) error {
	type result struct {
		line string
		err  error
	}
	readyCh := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		readyCh <- result{line: line, err: err}
	}()
	select {
	case res := <-readyCh:
		if res.err != nil {
			return fmt.Errorf("timed out opening sandbox-local tunnel bridge")
		}
		line := strings.TrimSpace(res.line)
		if line != sandboxLocalBridgeReady {
			if line == "" {
				return errors.New("sandbox-local tunnel bridge did not become ready")
			}
			return fmt.Errorf("sandbox-local tunnel bridge failed: %s", line)
		}
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timed out opening sandbox-local tunnel bridge")
	}
}

type sandboxLocalConn struct {
	handle model.TTYHandle
	reader *bufio.Reader
	local  net.Addr
	remote net.Addr
}

func (c *sandboxLocalConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *sandboxLocalConn) Write(p []byte) (int, error) {
	return c.handle.Writer().Write(p)
}

func (c *sandboxLocalConn) Close() error {
	return c.handle.Close()
}

func (c *sandboxLocalConn) LocalAddr() net.Addr {
	return c.local
}

func (c *sandboxLocalConn) RemoteAddr() net.Addr {
	return c.remote
}

func (c *sandboxLocalConn) SetDeadline(time.Time) error {
	return nil
}

func (c *sandboxLocalConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *sandboxLocalConn) SetWriteDeadline(time.Time) error {
	return nil
}

type tunnelBridgeAddr string

func (a tunnelBridgeAddr) Network() string {
	return "tcp"
}

func (a tunnelBridgeAddr) String() string {
	return string(a)
}

const sandboxLocalTCPBridgeScript = `
set -eu
port="${OR3_TUNNEL_TARGET_PORT:?}"
stty raw -echo -icanon min 1 time 0
if command -v python3 >/dev/null 2>&1; then
	exec python3 -u -c 'import os, select, socket, sys
port = int(sys.argv[1])
sock = socket.create_connection(("127.0.0.1", port))
os.write(sys.stdout.fileno(), b"__OR3_TUNNEL_BRIDGE_READY__\n")
while True:
	readable, _, _ = select.select([sys.stdin.fileno(), sock], [], [])
	if sys.stdin.fileno() in readable:
		data = os.read(sys.stdin.fileno(), 8192)
		if not data:
			break
		sock.sendall(data)
	if sock in readable:
		data = sock.recv(8192)
		if not data:
			break
		os.write(sys.stdout.fileno(), data)
' "$port"
fi
if command -v python >/dev/null 2>&1; then
	exec python -u -c 'import os, select, socket, sys
port = int(sys.argv[1])
sock = socket.create_connection(("127.0.0.1", port))
os.write(sys.stdout.fileno(), b"__OR3_TUNNEL_BRIDGE_READY__\n")
while True:
	readable, _, _ = select.select([sys.stdin.fileno(), sock], [], [])
	if sys.stdin.fileno() in readable:
		data = os.read(sys.stdin.fileno(), 8192)
		if not data:
			break
		sock.sendall(data)
	if sock in readable:
		data = sock.recv(8192)
		if not data:
			break
		os.write(sys.stdout.fileno(), data)
' "$port"
fi
if command -v node >/dev/null 2>&1; then
	exec node -e 'const net = require("net");
const port = Number(process.argv[1]);
const socket = net.createConnection({ host: "127.0.0.1", port }, () => {
	process.stdout.write("__OR3_TUNNEL_BRIDGE_READY__\n");
});
process.stdin.on("data", (chunk) => {
	if (!socket.destroyed) {
		socket.write(chunk);
	}
});
socket.on("data", (chunk) => {
	process.stdout.write(chunk);
});
const close = () => {
	if (!socket.destroyed) {
		socket.end();
	}
};
process.stdin.on("end", close);
process.stdin.on("close", close);
socket.on("end", () => process.exit(0));
socket.on("close", () => process.exit(0));
socket.on("error", (err) => {
	process.stderr.write(String(err && err.message ? err.message : err) + "\\n");
	process.exit(1);
});
' "$port"
fi
if command -v nc >/dev/null 2>&1; then
	printf '__OR3_TUNNEL_BRIDGE_READY__\n'
	exec nc 127.0.0.1 "$port"
fi
if command -v busybox >/dev/null 2>&1; then
	printf '__OR3_TUNNEL_BRIDGE_READY__\n'
	exec busybox nc 127.0.0.1 "$port"
fi
echo 'no supported tcp bridge helper in sandbox' >&2
exit 127
`
