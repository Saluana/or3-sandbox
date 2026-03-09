package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/term"

	"or3-sandbox/internal/model"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	client := clientConfig{
		baseURL: env("SANDBOX_API", "http://127.0.0.1:8080"),
		token:   env("SANDBOX_TOKEN", "dev-token"),
	}
	var err error
	switch os.Args[1] {
	case "doctor":
		err = runDoctor(os.Args[2:])
	case "create":
		err = runCreate(client, os.Args[2:])
	case "list":
		err = runList(client)
	case "inspect":
		err = runInspect(client, os.Args[2:])
	case "start", "suspend", "resume":
		err = runLifecycle(client, os.Args[1], os.Args[2:])
	case "stop":
		err = runStop(client, os.Args[2:])
	case "delete":
		err = runDelete(client, os.Args[2:])
	case "exec":
		err = runExec(client, os.Args[2:])
	case "tty":
		err = runTTY(client, os.Args[2:])
	case "upload":
		err = runUpload(client, os.Args[2:])
	case "download":
		err = runDownload(client, os.Args[2:])
	case "mkdir":
		err = runMkdir(client, os.Args[2:])
	case "tunnel-create":
		err = runTunnelCreate(client, os.Args[2:])
	case "tunnel-list":
		err = runTunnelList(client, os.Args[2:])
	case "tunnel-revoke":
		err = runTunnelRevoke(client, os.Args[2:])
	case "quota":
		err = runQuota(client)
	case "runtime-health":
		err = runRuntimeHealth(client)
	case "snapshot-create":
		err = runSnapshotCreate(client, os.Args[2:])
	case "snapshot-list":
		err = runSnapshotList(client, os.Args[2:])
	case "snapshot-inspect":
		err = runSnapshotInspect(client, os.Args[2:])
	case "snapshot-restore":
		err = runSnapshotRestore(client, os.Args[2:])
	case "preset":
		err = runPreset(client, os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type clientConfig struct {
	baseURL string
	token   string
}

func runCreate(client clientConfig, args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	image := fs.String("image", "", "base image")
	profile := fs.String("profile", "", "guest profile for qemu images: core, runtime, browser, container, debug")
	features := fs.String("features", "", "comma-separated guest features to request when supported by the qemu image contract")
	cpu := fs.String("cpu", "2", "cpu limit (cores, decimal cores, or millicores like 1500m)")
	memory := fs.Int("memory-mb", 2048, "memory limit")
	pids := fs.Int("pids", 512, "pids limit")
	disk := fs.Int("disk-mb", 10240, "disk limit")
	network := fs.String("network", "internet-enabled", "network mode")
	allowTunnels := fs.Bool("allow-tunnels", true, "allow tunnels")
	start := fs.Bool("start", true, "start immediately")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var sandbox model.Sandbox
	allowTunnelsValue := *allowTunnels
	cpuLimit, err := model.ParseCPUQuantity(*cpu)
	if err != nil {
		return err
	}
	return doJSON(client, http.MethodPost, "/v1/sandboxes", model.CreateSandboxRequest{
		BaseImageRef:  *image,
		Profile:       model.GuestProfile(strings.ToLower(strings.TrimSpace(*profile))),
		Features:      model.NormalizeFeatures(strings.Split(*features, ",")),
		CPULimit:      cpuLimit,
		MemoryLimitMB: *memory,
		PIDsLimit:     *pids,
		DiskLimitMB:   *disk,
		NetworkMode:   model.NetworkMode(*network),
		AllowTunnels:  &allowTunnelsValue,
		Start:         *start,
	}, &sandbox)
}

func runList(client clientConfig) error {
	var sandboxes []model.Sandbox
	if err := doJSON(client, http.MethodGet, "/v1/sandboxes", nil, &sandboxes); err != nil {
		return err
	}
	return printJSON(sandboxes)
}

func runInspect(client clientConfig, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sandboxctl inspect <sandbox-id>")
	}
	var sandbox model.Sandbox
	if err := doJSON(client, http.MethodGet, "/v1/sandboxes/"+args[0], nil, &sandbox); err != nil {
		return err
	}
	return printJSON(sandbox)
}

func runRuntimeHealth(client clientConfig) error {
	var health model.RuntimeHealth
	if err := doJSON(client, http.MethodGet, "/v1/runtime/health", nil, &health); err != nil {
		return err
	}
	return printJSON(health)
}

func runLifecycle(client clientConfig, op string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: sandboxctl %s <sandbox-id>", op)
	}
	var sandbox model.Sandbox
	if err := doJSON(client, http.MethodPost, "/v1/sandboxes/"+args[0]+"/"+op, map[string]any{}, &sandbox); err != nil {
		return err
	}
	return printJSON(sandbox)
}

func runStop(client clientConfig, args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	force := fs.Bool("force", false, "force stop")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return errors.New("usage: sandboxctl stop [--force] <sandbox-id>")
	}
	var sandbox model.Sandbox
	if err := doJSON(client, http.MethodPost, "/v1/sandboxes/"+rest[0]+"/stop", model.LifecycleRequest{Force: *force}, &sandbox); err != nil {
		return err
	}
	return printJSON(sandbox)
}

func runDelete(client clientConfig, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sandboxctl delete <sandbox-id>")
	}
	return doJSON(client, http.MethodDelete, "/v1/sandboxes/"+args[0], nil, nil)
}

func runExec(client clientConfig, args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	stream := fs.Bool("stream", true, "stream output")
	timeout := fs.Duration("timeout", 5*time.Minute, "timeout")
	cwd := fs.String("cwd", "/workspace", "working directory")
	detached := fs.Bool("detached", false, "detached")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 2 {
		return errors.New("usage: sandboxctl exec [flags] <sandbox-id> <command...>")
	}
	sandboxID := rest[0]
	req := model.ExecRequest{
		Command:  rest[1:],
		Cwd:      *cwd,
		Timeout:  *timeout,
		Detached: *detached,
	}
	if *stream && !*detached {
		return streamExec(client, sandboxID, req)
	}
	var execution model.Execution
	if err := doJSON(client, http.MethodPost, "/v1/sandboxes/"+sandboxID+"/exec", req, &execution); err != nil {
		return err
	}
	return printJSON(execution)
}

func runTTY(client clientConfig, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: sandboxctl tty <sandbox-id> [command...]")
	}
	sandboxID := args[0]
	command := []string{"bash"}
	if len(args) > 1 {
		command = args[1:]
	}
	u, err := url.Parse(strings.TrimRight(client.baseURL, "/") + "/v1/sandboxes/" + sandboxID + "/tty")
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	cols, rows, _ := term.GetSize(int(os.Stdin.Fd()))
	header := http.Header{"Authorization": []string{"Bearer " + client.token}}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), header)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.WriteJSON(model.TTYRequest{
		Command: command,
		Cwd:     "/workspace",
		Rows:    rows,
		Cols:    cols,
	}); err != nil {
		return err
	}
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			cols, rows, _ := term.GetSize(int(os.Stdin.Fd()))
			_ = conn.WriteJSON(map[string]any{"type": "resize", "rows": rows, "cols": cols})
		}
	}()
	errCh := make(chan error, 2)
	go func() {
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if _, err := os.Stdout.Write(payload); err != nil {
				errCh <- err
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
					errCh <- err
					return
				}
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()
	return <-errCh
}

func runUpload(client clientConfig, args []string) error {
	if len(args) != 3 {
		return errors.New("usage: sandboxctl upload <sandbox-id> <local-path> <remote-path>")
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	return doJSON(client, http.MethodPut, "/v1/sandboxes/"+args[0]+"/files/"+strings.TrimLeft(args[2], "/"), model.FileWriteRequest{Encoding: "base64", ContentBase64: base64.StdEncoding.EncodeToString(data)}, nil)
}

func runDownload(client clientConfig, args []string) error {
	if len(args) != 3 {
		return errors.New("usage: sandboxctl download <sandbox-id> <remote-path> <local-path>")
	}
	var file model.FileReadResponse
	if err := doJSON(client, http.MethodGet, "/v1/sandboxes/"+args[0]+"/files/"+strings.TrimLeft(args[1], "/")+"?encoding=base64", nil, &file); err != nil {
		return err
	}
	data, err := base64.StdEncoding.DecodeString(file.ContentBase64)
	if err != nil {
		return err
	}
	return os.WriteFile(args[2], data, 0o644)
}

func runMkdir(client clientConfig, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: sandboxctl mkdir <sandbox-id> <path>")
	}
	return doJSON(client, http.MethodPost, "/v1/sandboxes/"+args[0]+"/mkdir", model.MkdirRequest{Path: args[1]}, nil)
}

func runTunnelCreate(client clientConfig, args []string) error {
	fs := flag.NewFlagSet("tunnel-create", flag.ContinueOnError)
	port := fs.Int("port", 0, "target port")
	protocol := fs.String("protocol", "http", "protocol")
	authMode := fs.String("auth-mode", "token", "auth mode")
	visibility := fs.String("visibility", "private", "visibility")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 || *port == 0 {
		return errors.New("usage: sandboxctl tunnel-create --port <port> <sandbox-id>")
	}
	var tunnel model.Tunnel
	if err := doJSON(client, http.MethodPost, "/v1/sandboxes/"+rest[0]+"/tunnels", model.CreateTunnelRequest{
		TargetPort: *port,
		Protocol:   model.TunnelProtocol(*protocol),
		AuthMode:   *authMode,
		Visibility: *visibility,
	}, &tunnel); err != nil {
		return err
	}
	return printJSON(tunnel)
}

func runTunnelList(client clientConfig, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sandboxctl tunnel-list <sandbox-id>")
	}
	var tunnels []model.Tunnel
	if err := doJSON(client, http.MethodGet, "/v1/sandboxes/"+args[0]+"/tunnels", nil, &tunnels); err != nil {
		return err
	}
	return printJSON(tunnels)
}

func runTunnelRevoke(client clientConfig, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sandboxctl tunnel-revoke <tunnel-id>")
	}
	return doJSON(client, http.MethodDelete, "/v1/tunnels/"+args[0], nil, nil)
}

func runQuota(client clientConfig) error {
	var view map[string]any
	if err := doJSON(client, http.MethodGet, "/v1/quotas/me", nil, &view); err != nil {
		return err
	}
	return printJSON(view)
}

func runSnapshotCreate(client clientConfig, args []string) error {
	fs := flag.NewFlagSet("snapshot-create", flag.ContinueOnError)
	name := fs.String("name", "", "snapshot name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return errors.New("usage: sandboxctl snapshot-create [--name <name>] <sandbox-id>")
	}
	var snapshot model.Snapshot
	if err := doJSON(client, http.MethodPost, "/v1/sandboxes/"+rest[0]+"/snapshots", model.CreateSnapshotRequest{Name: *name}, &snapshot); err != nil {
		return err
	}
	return printJSON(snapshot)
}

func runSnapshotList(client clientConfig, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sandboxctl snapshot-list <sandbox-id>")
	}
	var snapshots []model.Snapshot
	if err := doJSON(client, http.MethodGet, "/v1/sandboxes/"+args[0]+"/snapshots", nil, &snapshots); err != nil {
		return err
	}
	return printJSON(snapshots)
}

func runSnapshotInspect(client clientConfig, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sandboxctl snapshot-inspect <snapshot-id>")
	}
	var snapshot model.Snapshot
	if err := doJSON(client, http.MethodGet, "/v1/snapshots/"+args[0], nil, &snapshot); err != nil {
		return err
	}
	return printJSON(snapshot)
}

func runSnapshotRestore(client clientConfig, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: sandboxctl snapshot-restore <snapshot-id> <target-sandbox-id>")
	}
	var sandbox model.Sandbox
	if err := doJSON(client, http.MethodPost, "/v1/snapshots/"+args[0]+"/restore", model.RestoreSnapshotRequest{TargetSandboxID: args[1]}, &sandbox); err != nil {
		return err
	}
	return printJSON(sandbox)
}

func streamExec(client clientConfig, sandboxID string, req model.ExecRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Timeout: 0}
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(client.baseURL, "/")+"/v1/sandboxes/"+sandboxID+"/exec?stream=1", bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return errors.New(string(body))
	}
	_, err = io.Copy(os.Stdout, response.Body)
	return err
}

func doJSON(client clientConfig, method, endpoint string, requestBody any, out any) error {
	var body io.Reader
	if requestBody != nil {
		data, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	requestURL, err := buildRequestURL(client.baseURL, endpoint)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(method, requestURL, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 2 * time.Minute}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		data, _ := io.ReadAll(response.Body)
		return fmt.Errorf("%s", strings.TrimSpace(string(data)))
	}
	if out == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func buildRequestURL(baseURL, endpoint string) (string, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/") + "/")
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(strings.TrimLeft(endpoint, "/"))
	if err != nil {
		return "", err
	}
	ref.Path = path.Clean("/" + ref.Path)
	return base.ResolveReference(ref).String(), nil
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sandboxctl <doctor|create|list|inspect|start|stop|suspend|resume|delete|exec|tty|upload|download|mkdir|tunnel-create|tunnel-list|tunnel-revoke|quota|runtime-health|snapshot-create|snapshot-list|snapshot-inspect|snapshot-restore|preset>")
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
