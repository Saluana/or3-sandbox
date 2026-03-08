package main

import (
	"bufio"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"or3-sandbox/internal/model"
	"or3-sandbox/internal/presets"
)

func runPreset(client clientConfig, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sandboxctl preset <list|inspect|run>")
	}
	switch args[0] {
	case "list":
		return runPresetList(args[1:])
	case "inspect":
		return runPresetInspect(args[1:])
	case "run":
		return runPresetRun(client, args[1:])
	default:
		return errors.New("usage: sandboxctl preset <list|inspect|run>")
	}
}

func runPresetList(args []string) error {
	fs := flag.NewFlagSet("preset list", flag.ContinueOnError)
	examplesDir := fs.String("examples-dir", "", "examples directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := resolveExamplesDir(*examplesDir)
	if err != nil {
		return err
	}
	summaries, err := presets.List(root)
	if err != nil {
		return err
	}
	return printJSON(summaries)
}

func runPresetInspect(args []string) error {
	fs := flag.NewFlagSet("preset inspect", flag.ContinueOnError)
	examplesDir := fs.String("examples-dir", "", "examples directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: sandboxctl preset inspect [--examples-dir <dir>] <preset-name>")
	}
	root, err := resolveExamplesDir(*examplesDir)
	if err != nil {
		return err
	}
	manifest, err := presets.Load(root, fs.Args()[0])
	if err != nil {
		return err
	}
	return printJSON(manifest)
}

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }
func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func runPresetRun(client clientConfig, args []string) error {
	args = normalizePresetRunArgs(args)
	fs := flag.NewFlagSet("preset run", flag.ContinueOnError)
	examplesDir := fs.String("examples-dir", "", "examples directory")
	cleanup := fs.String("cleanup", "", "cleanup policy: always, never, on-success")
	keep := fs.Bool("keep", false, "preserve sandbox after execution")
	var setFlags stringListFlag
	var envFlags stringListFlag
	fs.Var(&setFlags, "set", "override sandbox defaults like image=...,cpu=...,memory-mb=...")
	fs.Var(&envFlags, "env", "set or override preset input values as KEY=VALUE")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: sandboxctl preset run [flags] <preset-name>")
	}
	root, err := resolveExamplesDir(*examplesDir)
	if err != nil {
		return err
	}
	manifest, err := presets.Load(root, fs.Args()[0])
	if err != nil {
		return err
	}
	inputOverrides, err := parseKeyValueFlags(envFlags)
	if err != nil {
		return err
	}
	sandboxOverrides, err := parseKeyValueFlags(setFlags)
	if err != nil {
		return err
	}
	dotEnvValues, err := loadPresetDotEnv()
	if err != nil {
		return err
	}
	runner := presetRunner{client: client, manifest: manifest, rootDir: root, cleanupOverride: *cleanup, keep: *keep, inputOverrides: inputOverrides, dotEnvValues: dotEnvValues, sandboxOverrides: sandboxOverrides}
	return runner.Run()
}

func normalizePresetRunArgs(args []string) []string {
	if len(args) <= 1 {
		return args
	}
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--") {
			flags = append(flags, arg)
			if presetRunFlagRequiresValue(arg) && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return append(flags, positionals...)
}

func presetRunFlagRequiresValue(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}
	switch arg {
	case "--examples-dir", "--cleanup", "--set", "--env":
		return true
	default:
		return false
	}
}

type presetRunner struct {
	client           clientConfig
	manifest         presets.Manifest
	rootDir          string
	cleanupOverride  string
	keep             bool
	inputOverrides   map[string]string
	dotEnvValues     map[string]string
	sandboxOverrides map[string]string
}

const (
	presetGuestReadyTimeout  = 2 * time.Minute
	presetGuestReadyInterval = time.Second
)

type presetRuntimeAdapter struct {
	name               string
	requiresGuestReady bool
	profile            string
}

func (r presetRunner) Run() error {
	inputs, err := r.resolveInputs()
	if err != nil {
		return err
	}
	req, err := r.buildCreateRequest(inputs)
	if err != nil {
		return err
	}
	adapter, err := resolvePresetRuntimeAdapter(r.client, r.manifest, req)
	if err != nil {
		return err
	}
	printlnProgress("creating sandbox", r.manifest.Name)
	var sandbox model.Sandbox
	if err := doJSON(r.client, http.MethodPost, "/v1/sandboxes", req, &sandbox); err != nil {
		return err
	}
	vars := map[string]string{"SANDBOX_ID": sandbox.ID}
	for key, value := range inputs {
		vars[key] = value
	}
	fmt.Fprintf(os.Stdout, "sandbox_id=%s\n", sandbox.ID)
	cleanupPolicy := r.manifest.Cleanup
	if r.keep {
		cleanupPolicy = presets.CleanupNever
	} else if strings.TrimSpace(r.cleanupOverride) != "" {
		cleanupPolicy = presets.CleanupPolicy(strings.TrimSpace(r.cleanupOverride))
	}
	succeeded := false
	defer func() {
		if !shouldCleanup(cleanupPolicy, succeeded) {
			return
		}
		_ = doJSON(r.client, http.MethodDelete, "/v1/sandboxes/"+sandbox.ID, nil, nil)
	}()
	if err := adapter.waitForGuestReady(r.client, sandbox.ID); err != nil {
		return err
	}
	if err := r.uploadFiles(sandbox.ID, vars); err != nil {
		return err
	}
	if err := r.runSteps(sandbox.ID, r.manifest.Bootstrap, vars); err != nil {
		return err
	}
	var tunnel *model.Tunnel
	if r.manifest.Startup != nil {
		if err := r.runStep(sandbox.ID, *r.manifest.Startup, vars); err != nil {
			return err
		}
	}
	if r.manifest.Tunnel != nil && r.manifest.Readiness != nil && strings.EqualFold(r.manifest.Readiness.Type, "http") {
		created, err := r.createTunnel(sandbox.ID)
		if err != nil {
			return err
		}
		tunnel = &created
		vars["TUNNEL_ENDPOINT"] = tunnel.Endpoint
		vars["TUNNEL_ACCESS_TOKEN"] = tunnel.AccessToken
	}
	if err := r.waitForReadiness(sandbox.ID, vars, tunnel); err != nil {
		return err
	}
	if tunnel == nil && r.manifest.Tunnel != nil {
		created, err := r.createTunnel(sandbox.ID)
		if err != nil {
			return err
		}
		tunnel = &created
	}
	if tunnel != nil {
		if err := r.printTunnelBrowserURLs(*tunnel, vars); err != nil {
			return err
		}
	}
	if err := r.downloadArtifacts(sandbox.ID); err != nil {
		return err
	}
	succeeded = true
	return nil
}

func (r presetRunner) resolveInputs() (map[string]string, error) {
	resolved := make(map[string]string, len(r.manifest.Inputs))
	for _, input := range r.manifest.Inputs {
		value, ok := r.inputOverrides[input.Name]
		if !ok {
			value, ok = os.LookupEnv(input.Name)
			if (!ok || strings.TrimSpace(value) == "") && r.dotEnvValues != nil {
				if dotEnvValue, exists := r.dotEnvValues[input.Name]; exists {
					value = dotEnvValue
					ok = true
				}
			}
		}
		if !ok || strings.TrimSpace(value) == "" {
			value = input.Default
		}
		if input.Required && strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("preset input %q is required", input.Name)
		}
		if strings.TrimSpace(value) != "" {
			resolved[input.Name] = value
		} else {
			delete(resolved, input.Name)
		}
	}
	return resolved, nil
}

func loadPresetDotEnv() (map[string]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(filepath.Join(cwd, ".env"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if len(value) >= 2 {
			if value[0] == '\'' && value[len(value)-1] == '\'' {
				value = value[1 : len(value)-1]
			} else if value[0] == '"' && value[len(value)-1] == '"' {
				unquoted, unquoteErr := strconv.Unquote(value)
				if unquoteErr == nil {
					value = unquoted
				}
			}
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func (r presetRunner) buildCreateRequest(inputs map[string]string) (model.CreateSandboxRequest, error) {
	allowTunnels := r.manifest.Sandbox.AllowTunnels
	start := true
	if r.manifest.Sandbox.Start != nil {
		start = *r.manifest.Sandbox.Start
	}
	image := expandTemplate(overrideValue(r.manifest.Sandbox.Image, r.sandboxOverrides, "image"), inputs)
	cpuText := overrideValue(r.manifest.Sandbox.CPULimit, r.sandboxOverrides, "cpu")
	cpuLimit, err := model.ParseCPUQuantity(cpuText)
	if err != nil {
		return model.CreateSandboxRequest{}, err
	}
	memoryMB, err := overrideInt(r.manifest.Sandbox.MemoryMB, r.sandboxOverrides, "memory-mb")
	if err != nil {
		return model.CreateSandboxRequest{}, err
	}
	pidsLimit, err := overrideInt(r.manifest.Sandbox.PIDsLimit, r.sandboxOverrides, "pids")
	if err != nil {
		return model.CreateSandboxRequest{}, err
	}
	diskMB, err := overrideInt(r.manifest.Sandbox.DiskMB, r.sandboxOverrides, "disk-mb")
	if err != nil {
		return model.CreateSandboxRequest{}, err
	}
	networkMode := expandTemplate(overrideValue(r.manifest.Sandbox.NetworkMode, r.sandboxOverrides, "network"), inputs)
	if raw, ok := r.sandboxOverrides["allow-tunnels"]; ok {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return model.CreateSandboxRequest{}, fmt.Errorf("invalid allow-tunnels override: %w", err)
		}
		allowTunnels = parsed
	}
	if raw, ok := r.sandboxOverrides["start"]; ok {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return model.CreateSandboxRequest{}, fmt.Errorf("invalid start override: %w", err)
		}
		start = parsed
	}
	return model.CreateSandboxRequest{BaseImageRef: image, CPULimit: cpuLimit, MemoryLimitMB: memoryMB, PIDsLimit: pidsLimit, DiskLimitMB: diskMB, NetworkMode: model.NetworkMode(networkMode), AllowTunnels: &allowTunnels, Start: start}, nil
}

func (r presetRunner) uploadFiles(sandboxID string, vars map[string]string) error {
	for _, asset := range r.manifest.Files {
		printlnProgress("uploading file", asset.Path)
		var payload model.FileWriteRequest
		if strings.TrimSpace(asset.Content) != "" {
			payload = model.FileWriteRequest{Content: expandTemplate(asset.Content, vars)}
		} else {
			sourcePath := filepath.Join(r.manifest.BaseDir, asset.Source)
			data, err := os.ReadFile(sourcePath)
			if err != nil {
				return err
			}
			if asset.Binary {
				payload = model.FileWriteRequest{Encoding: "base64", ContentBase64: base64.StdEncoding.EncodeToString(data)}
			} else {
				payload = model.FileWriteRequest{Content: expandTemplate(string(data), vars)}
			}
		}
		if err := doJSON(r.client, http.MethodPut, "/v1/sandboxes/"+sandboxID+"/files/"+strings.TrimLeft(asset.Path, "/"), payload, nil); err != nil {
			return err
		}
	}
	return nil
}

func (r presetRunner) runSteps(sandboxID string, steps []presets.Step, vars map[string]string) error {
	for _, step := range steps {
		if err := r.runStep(sandboxID, step, vars); err != nil {
			if step.ContinueOnError {
				fmt.Fprintf(os.Stderr, "step_continue_on_error=%s err=%v\n", step.Name, err)
				continue
			}
			return err
		}
	}
	return nil
}

func (r presetRunner) runStep(sandboxID string, step presets.Step, vars map[string]string) error {
	printlnProgress("running step", step.Name)
	req := model.ExecRequest{Command: expandSlice(step.Command, vars), Env: expandMap(step.Env, vars), Cwd: expandTemplate(step.Cwd, vars), Timeout: step.Timeout, Detached: step.Detached}
	var execution model.Execution
	if err := doJSON(r.client, http.MethodPost, "/v1/sandboxes/"+sandboxID+"/exec", req, &execution); err != nil {
		return fmt.Errorf("step %q: %w", step.Name, err)
	}
	if execution.StdoutPreview != "" {
		fmt.Fprint(os.Stdout, execution.StdoutPreview)
	}
	if execution.StderrPreview != "" {
		fmt.Fprint(os.Stderr, execution.StderrPreview)
	}
	if !step.Detached && execution.Status != model.ExecutionStatusSucceeded {
		return fmt.Errorf("step %q failed with status %s", step.Name, execution.Status)
	}
	return nil
}

func (r presetRunner) waitForReadiness(sandboxID string, vars map[string]string, tunnel *model.Tunnel) error {
	if r.manifest.Readiness == nil {
		return nil
	}
	printlnProgress("waiting for readiness", r.manifest.Readiness.Type)
	deadline := time.Now().Add(r.manifest.Readiness.Timeout)
	for time.Now().Before(deadline) {
		switch strings.ToLower(r.manifest.Readiness.Type) {
		case "command":
			var execution model.Execution
			req := model.ExecRequest{Command: expandSlice(r.manifest.Readiness.Command, vars), Timeout: r.manifest.Readiness.Interval, Cwd: "/workspace"}
			if err := doJSON(r.client, http.MethodPost, "/v1/sandboxes/"+sandboxID+"/exec", req, &execution); err == nil && execution.Status == model.ExecutionStatusSucceeded {
				return nil
			}
		case "http":
			if tunnel == nil {
				return fmt.Errorf("http readiness requires an active tunnel")
			}
			request, err := http.NewRequest(http.MethodGet, strings.TrimRight(tunnel.Endpoint, "/")+r.manifest.Readiness.Path, nil)
			if err != nil {
				return err
			}
			request.Header.Set("Authorization", "Bearer "+r.client.token)
			if tunnel.AuthMode == "token" && tunnel.AccessToken != "" {
				request.Header.Set("X-Tunnel-Token", tunnel.AccessToken)
			}
			response, err := (&http.Client{Timeout: r.manifest.Readiness.Interval}).Do(request)
			if err == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode == r.manifest.Readiness.ExpectedStatus {
					return nil
				}
			}
		}
		time.Sleep(r.manifest.Readiness.Interval)
	}
	return fmt.Errorf("timed out waiting for preset readiness")
}

func (r presetRunner) createTunnel(sandboxID string) (model.Tunnel, error) {
	printlnProgress("creating tunnel", strconv.Itoa(r.manifest.Tunnel.Port))
	var tunnel model.Tunnel
	req := model.CreateTunnelRequest{TargetPort: r.manifest.Tunnel.Port, Protocol: model.TunnelProtocol(r.manifest.Tunnel.Protocol), AuthMode: r.manifest.Tunnel.AuthMode, Visibility: r.manifest.Tunnel.Visibility}
	if err := doJSON(r.client, http.MethodPost, "/v1/sandboxes/"+sandboxID+"/tunnels", req, &tunnel); err != nil {
		return model.Tunnel{}, err
	}
	fmt.Fprintf(os.Stdout, "tunnel_endpoint=%s\n", tunnel.Endpoint)
	if strings.EqualFold(tunnel.AuthMode, "token") && strings.TrimSpace(tunnel.AccessToken) != "" {
		fmt.Fprintf(os.Stdout, "tunnel_access_token=%s\n", tunnel.AccessToken)
	}
	return tunnel, nil
}

func (r presetRunner) printTunnelBrowserURLs(tunnel model.Tunnel, vars map[string]string) error {
	signed, err := r.createTunnelSignedURL(tunnel.ID, "/")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "tunnel_browser_url=%s\n", signed.URL)
	fmt.Fprintf(os.Stdout, "tunnel_browser_url_expires_at=%s\n", signed.ExpiresAt.UTC().Format(time.RFC3339))
	if dashboardURL, ok := r.openClawDashboardURL(signed.URL, vars); ok {
		fmt.Fprintf(os.Stdout, "dashboard_url=%s\n", dashboardURL)
	}
	return nil
}

func (r presetRunner) createTunnelSignedURL(tunnelID, proxyPath string) (model.TunnelSignedURL, error) {
	request := model.CreateTunnelSignedURLRequest{Path: proxyPath}
	var signed model.TunnelSignedURL
	if err := doJSON(r.client, http.MethodPost, "/v1/tunnels/"+tunnelID+"/signed-url", request, &signed); err != nil {
		return model.TunnelSignedURL{}, err
	}
	return signed, nil
}

func (r presetRunner) openClawDashboardURL(browserURL string, vars map[string]string) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(r.manifest.Name), "openclaw") {
		return "", false
	}
	gatewayToken := strings.TrimSpace(vars["OPENCLAW_GATEWAY_TOKEN"])
	if gatewayToken == "" {
		return "", false
	}
	return browserURL + "#token=" + url.QueryEscape(gatewayToken), true
}

func (r presetRunner) downloadArtifacts(sandboxID string) error {
	for _, artifact := range r.manifest.Artifacts {
		printlnProgress("downloading artifact", artifact.LocalPath)
		var file model.FileReadResponse
		endpoint := "/v1/sandboxes/" + sandboxID + "/files/" + strings.TrimLeft(artifact.RemotePath, "/")
		if artifact.Binary {
			endpoint += "?encoding=base64"
		}
		if err := doJSON(r.client, http.MethodGet, endpoint, nil, &file); err != nil {
			return err
		}
		localPath := artifact.LocalPath
		if !filepath.IsAbs(localPath) {
			localPath = filepath.Join(r.manifest.BaseDir, localPath)
		}
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return err
		}
		var data []byte
		if artifact.Binary {
			decoded, err := base64.StdEncoding.DecodeString(file.ContentBase64)
			if err != nil {
				return err
			}
			data = decoded
		} else {
			data = []byte(file.Content)
		}
		if err := os.WriteFile(localPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func resolvePresetRuntimeAdapter(client clientConfig, manifest presets.Manifest, req model.CreateSandboxRequest) (presetRuntimeAdapter, error) {
	adapter := presetRuntimeAdapter{name: "docker", profile: strings.TrimSpace(manifest.Runtime.Profile)}
	var health model.RuntimeHealth
	if err := doJSON(client, http.MethodGet, "/v1/runtime/health", nil, &health); err == nil {
		backend := strings.ToLower(strings.TrimSpace(health.Backend))
		if backend != "" {
			adapter.name = backend
		}
	} else if len(manifest.Runtime.Allowed) == 1 {
		adapter.name = strings.ToLower(strings.TrimSpace(manifest.Runtime.Allowed[0]))
	}
	if !manifest.AllowsRuntime(adapter.name) {
		return presetRuntimeAdapter{}, fmt.Errorf("preset %q does not allow the %s runtime", manifest.Name, adapter.name)
	}
	switch adapter.name {
	case "docker":
		return adapter, nil
	case "qemu":
		adapter.requiresGuestReady = true
		if !req.Start {
			return presetRuntimeAdapter{}, fmt.Errorf("preset %q requires start=true when running on qemu", manifest.Name)
		}
		if adapter.profile == "" && !looksLikeQEMUGuestImage(req.BaseImageRef) {
			return presetRuntimeAdapter{}, fmt.Errorf("preset %q requires qemu guest packaging: set runtime.profile or use a guest image path in sandbox.image", manifest.Name)
		}
		return adapter, nil
	default:
		return presetRuntimeAdapter{}, fmt.Errorf("preset %q requires unsupported runtime %q", manifest.Name, adapter.name)
	}
}

func (a presetRuntimeAdapter) waitForGuestReady(client clientConfig, sandboxID string) error {
	if !a.requiresGuestReady {
		return nil
	}
	printlnProgress("waiting for guest-ready", a.name)
	deadline := time.Now().Add(presetGuestReadyTimeout)
	for time.Now().Before(deadline) {
		var sandbox model.Sandbox
		if err := doJSON(client, http.MethodGet, "/v1/sandboxes/"+sandboxID, nil, &sandbox); err == nil {
			switch sandbox.Status {
			case model.SandboxStatusRunning:
				return nil
			case model.SandboxStatusCreating, model.SandboxStatusStarting, model.SandboxStatusBooting:
			case model.SandboxStatusError, model.SandboxStatusDegraded:
				detail := strings.TrimSpace(sandbox.LastRuntimeError)
				if detail == "" {
					detail = strings.TrimSpace(sandbox.RuntimeStatus)
				}
				if detail == "" {
					return fmt.Errorf("guest did not become ready: status=%s", sandbox.Status)
				}
				return fmt.Errorf("guest did not become ready: status=%s detail=%s", sandbox.Status, detail)
			default:
				return fmt.Errorf("guest did not become ready: status=%s", sandbox.Status)
			}
		}
		time.Sleep(presetGuestReadyInterval)
	}
	return fmt.Errorf("timed out waiting for guest readiness")
}

func looksLikeQEMUGuestImage(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "./") || strings.HasPrefix(trimmed, "../") {
		return true
	}
	for _, suffix := range []string{".qcow2", ".img", ".raw", ".qcow"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func resolveExamplesDir(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return explicit, nil
	}
	return presets.DiscoverExamplesDir("")
}

func parseKeyValueFlags(values []string) (map[string]string, error) {
	parsed := make(map[string]string, len(values))
	for _, value := range values {
		parts := strings.SplitN(value, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("expected KEY=VALUE, got %q", value)
		}
		parsed[strings.TrimSpace(parts[0])] = parts[1]
	}
	return parsed, nil
}

func overrideValue(current string, overrides map[string]string, key string) string {
	if value, ok := overrides[key]; ok {
		return value
	}
	return current
}

func overrideInt(current int, overrides map[string]string, key string) (int, error) {
	if value, ok := overrides[key]; ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0, fmt.Errorf("invalid %s override: %w", key, err)
		}
		return parsed, nil
	}
	return current, nil
}

func expandTemplate(value string, vars map[string]string) string {
	return os.Expand(value, func(key string) string {
		return vars[key]
	})
}

func expandSlice(values []string, vars map[string]string) []string {
	expanded := make([]string, 0, len(values))
	for _, value := range values {
		expanded = append(expanded, expandTemplate(value, vars))
	}
	return expanded
}

func expandMap(values map[string]string, vars map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	expanded := make(map[string]string, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		expanded[key] = expandTemplate(values[key], vars)
	}
	return expanded
}

func shouldCleanup(policy presets.CleanupPolicy, succeeded bool) bool {
	switch policy {
	case presets.CleanupAlways:
		return true
	case presets.CleanupOnSuccess:
		return succeeded
	default:
		return false
	}
}

func printlnProgress(action, detail string) {
	fmt.Fprintf(os.Stdout, "[%s] %s\n", action, detail)
}
