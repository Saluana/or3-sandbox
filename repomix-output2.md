This file is a merged representation of a subset of the codebase, containing files not matching ignore patterns, combined into a single document by Repomix.

# File Summary

## Purpose
This file contains a packed representation of a subset of the repository's contents that is considered the most important context.
It is designed to be easily consumable by AI systems for analysis, code review,
or other automated processes.

## File Format
The content is organized as follows:
1. This summary section
2. Repository information
3. Directory structure
4. Repository files (if enabled)
5. Multiple file entries, each consisting of:
  a. A header with the file path (## File: path/to/file)
  b. The full contents of the file in a code block

## Usage Guidelines
- This file should be treated as read-only. Any changes should be made to the
  original repository files, not this packed version.
- When processing this file, use the file path to distinguish
  between different files in the repository.
- Be aware that this file may contain sensitive information. Handle it with
  the same level of security as you would the original repository.

## Notes
- Some files may have been excluded based on .gitignore rules and Repomix's configuration
- Binary files are not included in this packed representation. Please refer to the Repository Structure section for a complete list of file paths, including binary files
- Files matching these patterns are excluded: .github, planning, .vscode, examples, docs, **/*_test.go
- Files matching patterns in .gitignore are excluded
- Files matching default ignore patterns are excluded
- Files are sorted by Git change count (files with more changes are at the bottom)

# Directory Structure
```
cmd/
  or3-guest-agent/
    main.go
  sandboxctl/
    doctor.go
    main.go
    preset.go
  sandboxd/
    main.go
images/
  base/
    Dockerfile
    smoke.sh
  guest/
    cloud-init/
      meta-data.tpl
      user-data.tpl
    profiles/
      browser.json
      container.json
      core.json
      debug.json
      runtime.json
    systemd/
      or3-bootstrap.service
      or3-bootstrap.sh
      or3-guest-agent.service
    build-base-image.sh
    README.md
    smoke-agent.sh
    smoke-ssh.sh
internal/
  api/
    router.go
  auth/
    authenticator.go
    identity.go
    middleware.go
  config/
    config.go
  db/
    db.go
  guestimage/
    contract.go
  logging/
    logging.go
  model/
    cpu.go
    guest.go
    model.go
    runtime.go
  presets/
    load.go
    manifest.go
  repository/
    store.go
  runtime/
    docker/
      runtime.go
    qemu/
      agentproto/
        protocol.go
      agent_client.go
      exec.go
      runtime.go
      workspace.go
  service/
    audit.go
    observability.go
    policy.go
    service.go
    tunnel_tcp.go
    util.go
scripts/
  production-smoke.sh
  qemu-production-smoke.sh
  qemu-recovery-drill.sh
  qemu-resource-abuse.sh
.gitignore
go.mod
README.md
```

# Files

## File: cmd/or3-guest-agent/main.go
````go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"or3-sandbox/internal/model"
	"or3-sandbox/internal/runtime/qemu/agentproto"
)

const (
	defaultPortPath = "/dev/virtio-ports/org.or3.guest_agent"
	readyMarkerPath = "/var/lib/or3/bootstrap.ready"
	previewLimit    = 64 * 1024
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	agent := &guestAgent{portPath: env("OR3_GUEST_AGENT_PORT_PATH", defaultPortPath)}
	if err := agent.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type guestAgent struct {
	portPath string
}

func (a *guestAgent) run(ctx context.Context) error {
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

func (a *guestAgent) serveConn(ctx context.Context, conn io.ReadWriter) error {
	for {
		message, err := agentproto.ReadMessage(conn)
		if err != nil {
			return err
		}
		if message.Op == agentproto.OpPTYOpen {
			var req agentproto.PTYOpenRequest
			if err := json.Unmarshal(message.Result, &req); err != nil {
				return agentproto.WriteMessage(conn, agentproto.Message{Op: message.Op, OK: false, Error: err.Error()})
			}
			return a.servePTY(ctx, conn, req)
		}
		response, err := a.handle(ctx, message)
		if err != nil {
			response = agentproto.Message{Op: message.Op, OK: false, Error: err.Error()}
		}
		if err := agentproto.WriteMessage(conn, response); err != nil {
			return err
		}
	}
}

func (a *guestAgent) handle(ctx context.Context, message agentproto.Message) (agentproto.Message, error) {
	switch message.Op {
	case agentproto.OpHello:
		result, err := json.Marshal(agentproto.HelloResult{
			ProtocolVersion:          agentproto.ProtocolVersion,
			WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
			Ready:                    isReady(),
			Capabilities:             []string{"exec", "pty", "files", "shutdown"},
		})
		return agentproto.Message{Op: message.Op, OK: true, Result: result}, err
	case agentproto.OpReady:
		ready := isReady()
		reason := ""
		if !ready {
			reason = "bootstrap marker not present"
		}
		result, err := json.Marshal(agentproto.ReadyResult{Ready: ready, Reason: reason})
		return agentproto.Message{Op: message.Op, OK: true, Result: result}, err
	case agentproto.OpExec:
		var req agentproto.ExecRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		result, err := runExec(ctx, req)
		if err != nil {
			return agentproto.Message{}, err
		}
		payload, err := json.Marshal(result)
		return agentproto.Message{Op: message.Op, OK: true, Result: payload}, err
	case agentproto.OpFileRead:
		var req agentproto.FileReadRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		target, err := workspacePath(req.Path)
		if err != nil {
			return agentproto.Message{}, err
		}
		data, err := os.ReadFile(target)
		if err != nil {
			return agentproto.Message{}, err
		}
		payload, err := json.Marshal(agentproto.FileReadResult{Path: target, Content: agentproto.EncodeBytes(data)})
		return agentproto.Message{Op: message.Op, OK: true, Result: payload}, err
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
		if err := os.MkdirAll(path.Dir(target), 0o755); err != nil {
			return agentproto.Message{}, err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return agentproto.Message{}, err
		}
		return agentproto.Message{Op: message.Op, OK: true}, nil
	case agentproto.OpFileDelete:
		var req agentproto.PathRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		target, err := workspacePath(req.Path)
		if err != nil {
			return agentproto.Message{}, err
		}
		return agentproto.Message{Op: message.Op, OK: true}, os.RemoveAll(target)
	case agentproto.OpMkdir:
		var req agentproto.PathRequest
		if err := json.Unmarshal(message.Result, &req); err != nil {
			return agentproto.Message{}, err
		}
		target, err := workspacePath(req.Path)
		if err != nil {
			return agentproto.Message{}, err
		}
		return agentproto.Message{Op: message.Op, OK: true}, os.MkdirAll(target, 0o755)
	case agentproto.OpShutdown:
		go func() {
			_ = exec.Command("/sbin/poweroff").Run()
		}()
		return agentproto.Message{Op: message.Op, OK: true}, nil
	default:
		return agentproto.Message{}, fmt.Errorf("unsupported guest agent operation %q", message.Op)
	}
}

func (a *guestAgent) servePTY(ctx context.Context, conn io.ReadWriter, req agentproto.PTYOpenRequest) error {
	command := req.Command
	if len(command) == 0 {
		command = []string{"bash"}
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = defaultString(req.Cwd, "/workspace")
	cmd.Env = append(os.Environ(), flattenEnv(req.Env)...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(defaultInt(req.Rows, 24)), Cols: uint16(defaultInt(req.Cols, 80))})
	if err != nil {
		return agentproto.WriteMessage(conn, agentproto.Message{Op: agentproto.OpPTYOpen, OK: false, Error: err.Error()})
	}
	defer ptmx.Close()
	sessionID := fmt.Sprintf("pty-%d", time.Now().UTC().UnixNano())
	ack, err := json.Marshal(agentproto.PTYOpenResult{SessionID: sessionID})
	if err != nil {
		return err
	}
	if err := agentproto.WriteMessage(conn, agentproto.Message{Op: agentproto.OpPTYOpen, OK: true, Result: ack}); err != nil {
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
		return agentproto.WriteMessage(conn, agentproto.Message{Op: agentproto.OpPTYData, OK: true, Result: payload})
	}
	errCh := make(chan error, 2)
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
		message, err := agentproto.ReadMessage(conn)
		if err != nil {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			return err
		}
		switch message.Op {
		case agentproto.OpPTYData:
			var data agentproto.PTYData
			if err := json.Unmarshal(message.Result, &data); err != nil {
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
			if err := json.Unmarshal(message.Result, &resize); err != nil {
				return err
			}
			if err := pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(defaultInt(resize.Rows, 24)), Cols: uint16(defaultInt(resize.Cols, 80))}); err != nil {
				return err
			}
		case agentproto.OpPTYClose:
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			return nil
		}
		select {
		case err := <-errCh:
			if err == io.EOF {
				return nil
			}
			return err
		default:
		}
	}
}

func runExec(ctx context.Context, req agentproto.ExecRequest) (agentproto.ExecResult, error) {
	command := req.Command
	if len(command) == 0 {
		command = []string{"sh", "-lc", "pwd"}
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, command[0], command[1:]...)
	cmd.Dir = defaultString(req.Cwd, "/workspace")
	cmd.Env = append(os.Environ(), flattenEnv(req.Env)...)
	stdout := &limitedBuffer{limit: previewLimit}
	stderr := &limitedBuffer{limit: previewLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	startedAt := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		return agentproto.ExecResult{}, err
	}
	if req.Detached {
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
````

## File: cmd/sandboxctl/doctor.go
````go
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/guestimage"
	"or3-sandbox/internal/model"
)

var (
	doctorConfigLoader = config.Load
	doctorHostOS       = goruntime.GOOS
	doctorLookPath     = exec.LookPath
	doctorStat         = os.Stat
)

type doctorCheck struct {
	Level  string `json:"level"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

type doctorSummary struct {
	Mode      string        `json:"mode"`
	CheckedAt time.Time     `json:"checked_at"`
	Checks    []doctorCheck `json:"checks"`
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	productionQEMU := fs.Bool("production-qemu", false, "validate the production QEMU host and image profile posture")
	jsonOutput := fs.Bool("json", false, "print doctor results as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*productionQEMU {
		return errors.New("usage: sandboxctl doctor --production-qemu [--json]")
	}
	summary := runProductionQEMUDoctor()
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(summary)
	}
	blocking := 0
	warnings := 0
	for _, check := range summary.Checks {
		switch check.Level {
		case "fail":
			blocking++
		case "warn":
			warnings++
		}
		fmt.Fprintf(os.Stdout, "[%s] %s: %s\n", strings.ToUpper(check.Level), check.Name, check.Detail)
	}
	fmt.Fprintf(os.Stdout, "summary: %d blocking, %d warnings\n", blocking, warnings)
	if blocking > 0 {
		return fmt.Errorf("production qemu doctor found blocking failures")
	}
	return nil
}

func runProductionQEMUDoctor() doctorSummary {
	summary := doctorSummary{Mode: "production-qemu", CheckedAt: time.Now().UTC()}
	add := func(level, name, detail string) {
		summary.Checks = append(summary.Checks, doctorCheck{Level: level, Name: name, Detail: detail})
	}
	cfg, err := doctorConfigLoader(nil)
	if err != nil {
		add("fail", "config", err.Error())
		cfg = doctorConfigFromEnv()
	}
	if cfg.RuntimeBackend != "qemu" {
		add("fail", "runtime", "SANDBOX_RUNTIME must be qemu for production-qemu validation")
	} else {
		add("pass", "runtime", "runtime backend is qemu")
	}
	if cfg.AuthMode != "jwt-hs256" {
		add("fail", "auth", "production qemu requires SANDBOX_AUTH_MODE=jwt-hs256")
	} else {
		add("pass", "auth", "jwt auth is enabled")
	}
	if doctorHostOS != "linux" {
		add("fail", "host-os", fmt.Sprintf("host OS %s is not the supported hostile-production target; production-qemu requires Linux with KVM", doctorHostOS))
	} else {
		add("pass", "host-os", "linux host detected")
	}
	for _, command := range []string{cfg.QEMUBinary, "qemu-img"} {
		if strings.TrimSpace(command) == "" {
			add("fail", "command", "SANDBOX_QEMU_BINARY must be set for production-qemu validation")
			continue
		}
		if _, err := doctorLookPath(command); err != nil {
			add("fail", "command", fmt.Sprintf("required command %q is not available", command))
		} else {
			add("pass", "command", fmt.Sprintf("found %q", command))
		}
	}
	if doctorHostOS == "linux" {
		if _, err := doctorStat("/dev/kvm"); err != nil {
			add("fail", "kvm", "/dev/kvm is not available")
		} else {
			add("pass", "kvm", "/dev/kvm is available")
		}
	}
	for _, root := range []string{cfg.StorageRoot, cfg.SnapshotRoot, filepath.Dir(cfg.DatabasePath)} {
		if root == "" {
			continue
		}
		info, err := doctorStat(root)
		if err != nil {
			add("fail", "path", fmt.Sprintf("required path %q is not accessible: %v", root, err))
			continue
		}
		if !info.IsDir() {
			add("fail", "path", fmt.Sprintf("required path %q is not a directory", root))
			continue
		}
		add("pass", "path", fmt.Sprintf("path %q is accessible", root))
	}
	for _, secret := range cfg.AuthJWTSecretPaths {
		if info, err := doctorStat(secret); err != nil {
			add("fail", "secret", fmt.Sprintf("jwt secret %q is not readable: %v", secret, err))
		} else if info.Mode().Perm()&0o077 != 0 {
			add("warn", "secret", fmt.Sprintf("jwt secret %q permissions are broader than 0600", secret))
		} else {
			add("pass", "secret", fmt.Sprintf("jwt secret %q is readable with restrictive permissions", secret))
		}
	}
	allowed := cfg.EffectiveQEMUAllowedBaseImagePaths()
	sort.Strings(allowed)
	if len(allowed) == 0 {
		add("fail", "images", "no approved qemu guest images are configured")
	}
	for _, imagePath := range allowed {
		if _, err := doctorStat(imagePath); err != nil {
			add("fail", "image", fmt.Sprintf("guest image %q is not readable: %v", imagePath, err))
			continue
		}
		contract, err := guestimage.Load(imagePath)
		if err != nil {
			add("fail", "image-contract", err.Error())
			continue
		}
		if err := guestimage.Validate(imagePath, contract); err != nil {
			add("fail", "image-contract", err.Error())
			continue
		}
		if contract.Control.Mode == model.GuestControlModeSSHCompat && !cfg.QEMUAllowSSHCompat {
			add("fail", "image-policy", fmt.Sprintf("image %q is ssh-compat and blocked without SANDBOX_QEMU_ALLOW_SSH_COMPAT=true", imagePath))
			continue
		}
		if contract.Profile == model.GuestProfileDebug && !cfg.QEMUAllowDangerousProfiles {
			add("fail", "image-policy", fmt.Sprintf("image %q uses debug profile and is production-ineligible by default policy", imagePath))
			continue
		}
		if cfg.IsDangerousQEMUProfile(contract.Profile) && !cfg.QEMUAllowDangerousProfiles {
			add("warn", "image-policy", fmt.Sprintf("image %q uses dangerous profile %q and is blocked until explicitly allowed", imagePath, contract.Profile))
		}
		add("pass", "image-contract", fmt.Sprintf("image %q profile=%s control=%s protocol=%s", imagePath, contract.Profile, contract.Control.Mode, contract.Control.ProtocolVersion))
	}
	return summary
}

func doctorConfigFromEnv() config.Config {
	return config.Config{
		RuntimeBackend:             env("SANDBOX_RUNTIME", ""),
		AuthMode:                   env("SANDBOX_AUTH_MODE", ""),
		AuthJWTSecretPaths:         splitCommaSeparated(env("SANDBOX_AUTH_JWT_SECRET_PATHS", "")),
		DatabasePath:               env("SANDBOX_DB_PATH", ""),
		StorageRoot:                env("SANDBOX_STORAGE_ROOT", ""),
		SnapshotRoot:               env("SANDBOX_SNAPSHOT_ROOT", ""),
		QEMUBinary:                 env("SANDBOX_QEMU_BINARY", ""),
		QEMUBaseImagePath:          env("SANDBOX_QEMU_BASE_IMAGE_PATH", ""),
		QEMUAllowedBaseImagePaths:  splitCommaSeparated(env("SANDBOX_QEMU_ALLOWED_BASE_IMAGE_PATHS", "")),
		QEMUDangerousProfiles:      parseDoctorGuestProfiles(env("SANDBOX_QEMU_DANGEROUS_PROFILES", "container,debug")),
		QEMUAllowDangerousProfiles: strings.EqualFold(env("SANDBOX_QEMU_ALLOW_DANGEROUS_PROFILES", "false"), "true"),
		QEMUAllowSSHCompat:         strings.EqualFold(env("SANDBOX_QEMU_ALLOW_SSH_COMPAT", "false"), "true"),
	}
}

func splitCommaSeparated(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		result = append(result, part)
	}
	return result
}

func parseDoctorGuestProfiles(raw string) []model.GuestProfile {
	values := splitCommaSeparated(raw)
	profiles := make([]model.GuestProfile, 0, len(values))
	for _, value := range values {
		profile := model.GuestProfile(strings.ToLower(strings.TrimSpace(value)))
		if profile.IsValid() {
			profiles = append(profiles, profile)
		}
	}
	return profiles
}
````

## File: images/base/Dockerfile
````
FROM mcr.microsoft.com/playwright:v1.51.1-noble

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      docker.io \
      git \
      jq \
      python3-pip \
      sudo \
      tini && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["sleep", "infinity"]
````

## File: images/base/smoke.sh
````bash
#!/usr/bin/env bash
set -euo pipefail

git --version
python3 --version
python3 -c 'import sys; print(sys.executable)'
node --version
npm --version
python3 -c 'from pathlib import Path; Path("/workspace/browser.txt").write_text("ok")'
node -e 'console.log("playwright deps available")'
````

## File: images/guest/cloud-init/meta-data.tpl
````
instance-id: __INSTANCE_ID__
local-hostname: __HOSTNAME__
````

## File: images/guest/profiles/browser.json
````json
{
  "extends": "runtime",
  "profile": "browser",
  "description": "Agent-based profile with browser-supporting system libraries for headless UI workloads.",
  "capabilities": [
    "browser"
  ],
  "packages": [
    "libasound2",
    "libatk-bridge2.0-0",
    "libcups2",
    "libdrm2",
    "libgbm1",
    "libgtk-3-0",
    "libnss3",
    "libxdamage1",
    "libxkbcommon0",
    "libxrandr2",
    "xvfb"
  ]
}
````

## File: images/guest/profiles/container.json
````json
{
  "extends": "core",
  "profile": "container",
  "description": "Agent-based profile that adds an inner Docker engine for explicitly approved workloads.",
  "capabilities": [
    "container"
  ],
  "allowed_features": [
    "docker"
  ],
  "packages": [
    "docker.io"
  ],
  "enable_services": [
    "docker"
  ],
  "sandbox_groups": [
    "docker"
  ]
}
````

## File: images/guest/profiles/core.json
````json
{
  "profile": "core",
  "description": "Minimal production profile with agent-based control and no SSH or inner Docker.",
  "control": {
    "mode": "agent",
    "protocol_version": "1",
    "supported_transports": [
      "virtio-serial"
    ]
  },
  "workspace_contract_version": "1",
  "capabilities": [
    "exec",
    "files",
    "pty"
  ],
  "allowed_features": [],
  "packages": [
    "ca-certificates",
    "curl",
    "e2fsprogs",
    "jq"
  ],
  "enable_services": [],
  "sandbox_groups": [],
  "sandbox_passwordless_sudo": false,
  "ssh_present": false,
  "dangerous": false,
  "debug": false
}
````

## File: images/guest/profiles/debug.json
````json
{
  "extends": "runtime",
  "profile": "debug",
  "description": "Compatibility and troubleshooting profile with SSH and elevated convenience tools.",
  "control": {
    "mode": "ssh-compat",
    "protocol_version": "1",
    "supported_transports": [
      "virtio-serial",
      "ssh"
    ]
  },
  "capabilities": [
    "debug"
  ],
  "packages": [
    "lsof",
    "openssh-server",
    "strace",
    "sudo",
    "tcpdump"
  ],
  "enable_services": [
    "ssh"
  ],
  "sandbox_groups": [
    "sudo"
  ],
  "sandbox_passwordless_sudo": true,
  "ssh_present": true,
  "dangerous": true,
  "debug": true
}
````

## File: images/guest/profiles/runtime.json
````json
{
  "extends": "core",
  "profile": "runtime",
  "description": "Agent-based profile with common language runtimes and build tools.",
  "capabilities": [
    "runtime"
  ],
  "packages": [
    "git",
    "nodejs",
    "npm",
    "python3",
    "python3-pip"
  ]
}
````

## File: images/guest/systemd/or3-guest-agent.service
````
[Unit]
Description=or3 guest agent
After=local-fs.target
Wants=local-fs.target

[Service]
Type=simple
ExecStart=/usr/local/bin/or3-guest-agent
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
````

## File: images/guest/smoke-agent.sh
````bash
#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_IMAGE="${BASE_IMAGE:-$ROOT_DIR/or3-guest-core.qcow2}"
QEMU_BINARY="${QEMU_BINARY:-qemu-system-x86_64}"
QEMU_IMG_BINARY="${QEMU_IMG_BINARY:-qemu-img}"
QEMU_ACCEL="${QEMU_ACCEL:-kvm}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"; if [ -f "$WORK_DIR/qemu.pid" ]; then kill "$(cat "$WORK_DIR/qemu.pid")" >/dev/null 2>&1 || true; fi' EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd "$QEMU_BINARY"
require_cmd "$QEMU_IMG_BINARY"
require_cmd jq
require_cmd python3

"$QEMU_IMG_BINARY" create -f qcow2 -F qcow2 -b "$BASE_IMAGE" "$WORK_DIR/root-overlay.qcow2" 20G >/dev/null
"$QEMU_IMG_BINARY" create -f raw "$WORK_DIR/workspace.img" 10G >/dev/null

QEMU_NET_DEVICE="${QEMU_NET_DEVICE:-virtio-net-pci}"
if [[ "$QEMU_BINARY" == *aarch64* ]] && [[ "${QEMU_NET_DEVICE:-}" == "virtio-net-pci" ]]; then
  QEMU_NET_DEVICE="virtio-net-device"
fi

"$QEMU_BINARY" \
  -daemonize \
  -pidfile "$WORK_DIR/qemu.pid" \
  -monitor "unix:$WORK_DIR/monitor.sock,server,nowait" \
  -serial "file:$WORK_DIR/serial.log" \
  -display none \
  -accel "$QEMU_ACCEL" \
  -m 2048 \
  -smp 2 \
  -device virtio-serial \
  -chardev "socket,id=agent0,path=$WORK_DIR/agent.sock,server=on,wait=off" \
  -device "virtserialport,chardev=agent0,name=org.or3.guest_agent" \
  -drive "if=virtio,file=$WORK_DIR/root-overlay.qcow2,format=qcow2" \
  -drive "if=virtio,file=$WORK_DIR/workspace.img,format=raw" \
  -netdev "user,id=net0" \
  -device "$QEMU_NET_DEVICE,netdev=net0"

agent_rpc() {
  local op="$1"
  local payload="${2:-null}"
  OR3_AGENT_OP="$op" OR3_AGENT_PAYLOAD="$payload" OR3_AGENT_SOCKET_PATH="$WORK_DIR/agent.sock" python3 - <<'PY'
import json
import os
import socket
import struct

sock_path = os.environ["OR3_AGENT_SOCKET_PATH"]
op = os.environ["OR3_AGENT_OP"]
payload = os.environ.get("OR3_AGENT_PAYLOAD", "null")
message = {"op": op}
if payload and payload != "null":
    message["result"] = json.loads(payload)
raw = json.dumps(message).encode("utf-8")
with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
    conn.connect(sock_path)
    conn.sendall(struct.pack(">I", len(raw)) + raw)
    header = conn.recv(4)
    if len(header) != 4:
        raise SystemExit("short guest-agent header")
    length = struct.unpack(">I", header)[0]
    data = b""
    while len(data) < length:
        chunk = conn.recv(length - len(data))
        if not chunk:
            raise SystemExit("short guest-agent body")
        data += chunk
reply = json.loads(data.decode("utf-8"))
if not reply.get("ok", False):
    raise SystemExit(reply.get("error") or "guest agent request failed")
result = reply.get("result")
if result is None:
    result = {}
print(json.dumps(result))
PY
}

for _ in $(seq 1 90); do
  if ready_json="$(agent_rpc ready '{}' 2>/dev/null)"; then
    if [ "$(printf '%s' "$ready_json" | jq -r '.ready // false')" = "true" ]; then
      echo "guest image is agent reachable and bootstrap-ready"
      agent_rpc shutdown '{"reboot":false}' >/dev/null 2>&1 || true
      exit 0
    fi
  fi
  sleep 2
done

echo "guest image did not become agent-ready" >&2
if [ -f "$WORK_DIR/serial.log" ]; then
  tail -n 50 "$WORK_DIR/serial.log" >&2 || true
fi
exit 1
````

## File: internal/auth/identity.go
````go
package auth

import (
	"context"
	"errors"
	"strings"

	"or3-sandbox/internal/model"
)

var ErrForbidden = errors.New("forbidden")

type tenantContextKey struct{}

type Identity struct {
	Subject    string
	TenantID   string
	Roles      []string
	IsService  bool
	AuthMethod string
}

type TenantContext struct {
	Tenant   model.Tenant
	Quota    model.TenantQuota
	Identity Identity
}

const (
	PermissionSandboxRead      = "sandbox.read"
	PermissionSandboxLifecycle = "sandbox.lifecycle"
	PermissionExecRun          = "exec.run"
	PermissionTTYAttach        = "tty.attach"
	PermissionFilesRead        = "files.read"
	PermissionFilesWrite       = "files.write"
	PermissionSnapshotsRead    = "snapshots.read"
	PermissionSnapshotsWrite   = "snapshots.write"
	PermissionTunnelsRead      = "tunnels.read"
	PermissionTunnelsWrite     = "tunnels.write"
	PermissionAdminInspect     = "admin.inspect"
)

func FromContext(ctx context.Context) (TenantContext, bool) {
	value, ok := ctx.Value(tenantContextKey{}).(TenantContext)
	return value, ok
}

func Require(ctx context.Context, permissions ...string) error {
	tenantCtx, ok := FromContext(ctx)
	if !ok {
		return errors.New("unauthorized")
	}
	for _, permission := range permissions {
		if tenantCtx.HasPermission(permission) {
			return nil
		}
	}
	return ErrForbidden
}

func (t TenantContext) HasPermission(permission string) bool {
	for _, role := range t.Identity.Roles {
		for _, granted := range rolePermissions(strings.ToLower(strings.TrimSpace(role))) {
			if granted == "*" || granted == permission {
				return true
			}
		}
	}
	return false
}

func rolePermissions(role string) []string {
	switch role {
	case "admin", "operator":
		return []string{"*"}
	case "developer":
		return []string{
			PermissionSandboxRead,
			PermissionSandboxLifecycle,
			PermissionExecRun,
			PermissionTTYAttach,
			PermissionFilesRead,
			PermissionFilesWrite,
			PermissionSnapshotsRead,
			PermissionSnapshotsWrite,
			PermissionTunnelsRead,
			PermissionTunnelsWrite,
		}
	case "viewer":
		return []string{
			PermissionSandboxRead,
			PermissionFilesRead,
			PermissionSnapshotsRead,
			PermissionTunnelsRead,
		}
	case "service":
		return []string{
			PermissionSandboxRead,
			PermissionSandboxLifecycle,
			PermissionExecRun,
			PermissionTTYAttach,
			PermissionFilesRead,
			PermissionFilesWrite,
			PermissionSnapshotsRead,
			PermissionSnapshotsWrite,
			PermissionTunnelsRead,
			PermissionTunnelsWrite,
			PermissionAdminInspect,
		}
	default:
		return nil
	}
}
````

## File: internal/guestimage/contract.go
````go
package guestimage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"or3-sandbox/internal/model"
)

const SidecarSuffix = ".or3.json"

type ControlContract struct {
	Mode                model.GuestControlMode `json:"mode"`
	ProtocolVersion     string                 `json:"protocol_version"`
	SupportedTransports []string               `json:"supported_transports,omitempty"`
}

type Contract struct {
	ContractVersion          string             `json:"contract_version"`
	ImagePath                string             `json:"image_path,omitempty"`
	ImageSHA256              string             `json:"image_sha256"`
	BuildVersion             string             `json:"build_version"`
	GitSHA                   string             `json:"git_sha,omitempty"`
	Profile                  model.GuestProfile `json:"profile"`
	Capabilities             []string           `json:"capabilities,omitempty"`
	AllowedFeatures          []string           `json:"allowed_features,omitempty"`
	Control                  ControlContract    `json:"control"`
	WorkspaceContractVersion string             `json:"workspace_contract_version"`
	SSHPresent               bool               `json:"ssh_present"`
	Dangerous                bool               `json:"dangerous,omitempty"`
	Debug                    bool               `json:"debug,omitempty"`
	PackageInventory         []string           `json:"package_inventory,omitempty"`
}

func SidecarPath(imagePath string) string {
	trimmed := strings.TrimSpace(imagePath)
	if trimmed == "" {
		return ""
	}
	return trimmed + SidecarSuffix
}

func Load(imagePath string) (Contract, error) {
	sidecarPath := SidecarPath(imagePath)
	if sidecarPath == "" {
		return Contract{}, fmt.Errorf("guest image path is required")
	}
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		return Contract{}, fmt.Errorf("read image contract %q: %w", sidecarPath, err)
	}
	var contract Contract
	if err := json.Unmarshal(data, &contract); err != nil {
		return Contract{}, fmt.Errorf("parse image contract %q: %w", sidecarPath, err)
	}
	if contract.ImagePath == "" {
		contract.ImagePath = filepath.Clean(imagePath)
	}
	contract.Capabilities = model.NormalizeFeatures(contract.Capabilities)
	contract.AllowedFeatures = model.NormalizeFeatures(contract.AllowedFeatures)
	return contract, nil
}

func Validate(imagePath string, contract Contract) error {
	if strings.TrimSpace(contract.ContractVersion) == "" {
		return fmt.Errorf("image contract is missing contract_version")
	}
	if !contract.Profile.IsValid() {
		return fmt.Errorf("image contract profile %q is invalid", contract.Profile)
	}
	if !contract.Control.Mode.IsValid() {
		return fmt.Errorf("image contract control mode %q is invalid", contract.Control.Mode)
	}
	if strings.TrimSpace(contract.Control.ProtocolVersion) == "" {
		return fmt.Errorf("image contract is missing control.protocol_version")
	}
	if strings.TrimSpace(contract.WorkspaceContractVersion) == "" {
		return fmt.Errorf("image contract is missing workspace_contract_version")
	}
	if strings.TrimSpace(contract.ImageSHA256) == "" {
		return fmt.Errorf("image contract is missing image_sha256")
	}
	actualSHA, err := ComputeSHA256(imagePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actualSHA, strings.TrimSpace(contract.ImageSHA256)) {
		return fmt.Errorf("image contract checksum mismatch for %q", imagePath)
	}
	if contract.Control.Mode == model.GuestControlModeAgent && contract.SSHPresent && !contract.Debug {
		return fmt.Errorf("agent-default image contract for %q must not advertise ssh unless debug=true", imagePath)
	}
	return nil
}

func ComputeSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read guest image %q: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func RequestedFeaturesAllowed(contract Contract, requested []string) error {
	requested = model.NormalizeFeatures(requested)
	if len(requested) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(contract.AllowedFeatures))
	for _, value := range contract.AllowedFeatures {
		allowed[value] = struct{}{}
	}
	for _, value := range requested {
		if _, ok := allowed[value]; !ok {
			return fmt.Errorf("feature %q is not allowed by image profile %q", value, contract.Profile)
		}
	}
	return nil
}
````

## File: internal/logging/logging.go
````go
package logging

import (
	"log/slog"
	"os"
)

func New() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}
````

## File: internal/model/cpu.go
````go
package model

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const milliCPUPerCore = 1000

type CPUQuantity int64

func CPUCores(value int) CPUQuantity {
	return CPUQuantity(int64(value) * milliCPUPerCore)
}

func MilliCPU(value int64) CPUQuantity {
	return CPUQuantity(value)
}

func ParseCPUQuantity(value string) (CPUQuantity, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, fmt.Errorf("cpu value is required")
	}
	if strings.HasSuffix(trimmed, "m") {
		millis, err := strconv.ParseInt(strings.TrimSuffix(trimmed, "m"), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse cpu milli value %q: %w", value, err)
		}
		return CPUQuantity(millis), nil
	}
	parts := strings.SplitN(trimmed, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse cpu cores %q: %w", value, err)
	}
	millis := whole * milliCPUPerCore
	if len(parts) == 1 {
		return CPUQuantity(millis), nil
	}
	fractional := parts[1]
	if fractional == "" {
		return 0, fmt.Errorf("parse cpu cores %q: missing fractional digits", value)
	}
	if len(fractional) > 3 {
		return 0, fmt.Errorf("parse cpu cores %q: supports at most 3 decimal places", value)
	}
	for len(fractional) < 3 {
		fractional += "0"
	}
	fractionalMillis, err := strconv.ParseInt(fractional, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse cpu cores %q: %w", value, err)
	}
	if strings.HasPrefix(trimmed, "-") {
		fractionalMillis = -fractionalMillis
	}
	return CPUQuantity(millis + fractionalMillis), nil
}

func MustParseCPUQuantity(value string) CPUQuantity {
	parsed, err := ParseCPUQuantity(value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func (q CPUQuantity) MarshalJSON() ([]byte, error) {
	if q%milliCPUPerCore == 0 {
		return []byte(strconv.FormatInt(int64(q/milliCPUPerCore), 10)), nil
	}
	value := strconv.FormatFloat(float64(q)/milliCPUPerCore, 'f', -1, 64)
	return []byte(value), nil
}

func (q *CPUQuantity) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*q = 0
		return nil
	}
	if trimmed[0] == '"' {
		var raw string
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		parsed, err := ParseCPUQuantity(raw)
		if err != nil {
			return err
		}
		*q = parsed
		return nil
	}
	parsed, err := ParseCPUQuantity(trimmed)
	if err != nil {
		return err
	}
	*q = parsed
	return nil
}

func (q CPUQuantity) String() string {
	if q%milliCPUPerCore == 0 {
		return strconv.FormatInt(int64(q/milliCPUPerCore), 10)
	}
	sign := ""
	value := int64(q)
	if value < 0 {
		sign = "-"
		value = -value
	}
	whole := value / milliCPUPerCore
	fractional := value % milliCPUPerCore
	decimal := fmt.Sprintf("%03d", fractional)
	decimal = strings.TrimRight(decimal, "0")
	return fmt.Sprintf("%s%d.%s", sign, whole, decimal)
}

func (q CPUQuantity) MilliValue() int64 {
	return int64(q)
}

func (q CPUQuantity) VCPUCount() int {
	if q <= 0 {
		return 1
	}
	return int(math.Ceil(float64(q) / milliCPUPerCore))
}
````

## File: internal/model/guest.go
````go
package model

import (
	"sort"
	"strings"
)

const DefaultGuestControlProtocolVersion = "1"
const DefaultWorkspaceContractVersion = "1"
const DefaultImageContractVersion = "1"

func NormalizeFeatures(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil
	}
	return result
}
````

## File: internal/presets/load.go
````go
package presets

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Summary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
}

func DiscoverExamplesDir(startDir string) (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("SANDBOX_EXAMPLES_DIR")); explicit != "" {
		info, err := os.Stat(explicit)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("SANDBOX_EXAMPLES_DIR %q is not a directory", explicit)
		}
		return explicit, nil
	}
	if strings.TrimSpace(startDir) == "" {
		var err error
		startDir, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	dir := startDir
	for {
		candidate := filepath.Join(dir, "examples")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not find examples directory from %s", startDir)
}

func List(exampleDir string) ([]Summary, error) {
	entries, err := os.ReadDir(exampleDir)
	if err != nil {
		return nil, err
	}
	var summaries []Summary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(exampleDir, entry.Name(), ManifestFileName)
		if _, err := os.Stat(manifestPath); err != nil {
			if errorsIs(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		manifest, err := LoadManifest(manifestPath)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, Summary{Name: manifest.Name, Description: manifest.Description, Path: manifestPath})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })
	return summaries, nil
}

func Load(exampleDir, name string) (Manifest, error) {
	manifestPath := filepath.Join(exampleDir, name, ManifestFileName)
	return LoadManifest(manifestPath)
}

func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	manifest.BaseDir = filepath.Dir(path)
	manifest.Normalize()
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate %s: %w", path, err)
	}
	return manifest, nil
}

func errorsIs(err, target error) bool {
	return err != nil && target != nil && os.IsNotExist(err)
}
````

## File: internal/runtime/qemu/agentproto/protocol.go
````go
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
````

## File: internal/runtime/qemu/agent_client.go
````go
package qemu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"or3-sandbox/internal/model"
	"or3-sandbox/internal/runtime/qemu/agentproto"
)

type guestHandshake struct {
	ProtocolVersion          string
	WorkspaceContractVersion string
	Capabilities             []string
}

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
		Capabilities:             append([]string(nil), result.Capabilities...),
	}, nil
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
	var result agentproto.FileReadResult
	if err := r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpFileRead, agentproto.FileReadRequest{Path: target}, &result); err != nil {
		return nil, err
	}
	return agentproto.DecodeBytes(result.Content)
}

func (r *Runtime) agentWriteWorkspaceFileBytes(ctx context.Context, layout sandboxLayout, relativePath string, content []byte) error {
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	return r.agentRoundTrip(ctx, layout.agentSocketPath, agentproto.OpFileWrite, agentproto.FileWriteRequest{Path: target, Content: agentproto.EncodeBytes(content)}, nil)
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
	if err := agentproto.WriteMessage(conn, agentproto.Message{Op: agentproto.OpPTYOpen, Result: requestPayload}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	message, err := agentproto.ReadMessage(conn)
	if err != nil {
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
	if err := agentproto.WriteMessage(conn, agentproto.Message{Op: op, Result: payload}); err != nil {
		return err
	}
	message, err := agentproto.ReadMessage(conn)
	if err != nil {
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
		if err := agentproto.WriteMessage(h.conn, agentproto.Message{Op: agentproto.OpPTYData, Result: payload}); err != nil {
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
	return agentproto.WriteMessage(h.conn, agentproto.Message{Op: agentproto.OpPTYResize, Result: payload})
}

func (h *agentTTYHandle) Close() error {
	h.closeOnce.Do(func() {
		payload, _ := json.Marshal(agentproto.PTYData{SessionID: h.sessionID, EOF: true})
		_ = agentproto.WriteMessage(h.conn, agentproto.Message{Op: agentproto.OpPTYClose, Result: payload})
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
			_, _ = h.writer.Write([]byte(message.Error))
			return
		}
		var data agentproto.PTYData
		if err := json.Unmarshal(message.Result, &data); err != nil {
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

var _ model.TTYHandle = (*agentTTYHandle)(nil)
````

## File: internal/service/audit.go
````go
package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"or3-sandbox/internal/model"
)

func (s *Service) RecordAuditEvent(ctx context.Context, tenantID, sandboxID, action, resourceID, outcome, detail string, attrs ...any) {
	s.recordAudit(ctx, tenantID, sandboxID, action, resourceID, outcome, detail, attrs...)
}

func (s *Service) recordAudit(ctx context.Context, tenantID, sandboxID, action, resourceID, outcome, detail string, attrs ...any) {
	_ = s.store.AddAuditEvent(ctx, model.AuditEvent{
		ID:         newID("audit-"),
		TenantID:   tenantID,
		SandboxID:  sandboxID,
		Action:     action,
		ResourceID: resourceID,
		Outcome:    outcome,
		Message:    detail,
		CreatedAt:  time.Now().UTC(),
	})
	logAttrs := []any{
		"event", action,
		"tenant_id", tenantID,
		"sandbox_id", sandboxID,
		"resource_id", resourceID,
		"outcome", outcome,
	}
	if detail != "" {
		logAttrs = append(logAttrs, "detail", detail)
	}
	logAttrs = append(logAttrs, attrs...)
	s.log.Log(ctx, auditLevel(outcome), "service event", logAttrs...)
}

func auditLevel(outcome string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case "ok", "succeeded", "success":
		return slog.LevelInfo
	case "denied", "canceled":
		return slog.LevelWarn
	default:
		return slog.LevelError
	}
}

func auditKV(key string, value any) string {
	return key + "=" + auditValue(value)
}

func auditDetail(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		filtered = append(filtered, part)
	}
	return strings.Join(filtered, " ")
}

func auditValue(value any) string {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		trimmed = strings.ReplaceAll(trimmed, "\n", " ")
		trimmed = strings.ReplaceAll(trimmed, "\r", " ")
		if trimmed == "" {
			return `""`
		}
		if strings.ContainsAny(trimmed, " \t") {
			return strconv.Quote(trimmed)
		}
		return trimmed
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case time.Time:
		return typed.UTC().Format(time.RFC3339)
	case fmt.Stringer:
		return auditValue(typed.String())
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func execAuditDetail(req model.ExecRequest) string {
	entrypoint := ""
	if len(req.Command) > 0 {
		entrypoint = req.Command[0]
	}
	return auditDetail(
		auditKV("entrypoint", entrypoint),
		auditKV("argc", len(req.Command)),
		auditKV("cwd", req.Cwd),
		auditKV("detached", req.Detached),
		auditKV("timeout_seconds", int(req.Timeout.Seconds())),
	)
}

func executionAuditDetail(execution model.Execution) string {
	entrypoint := ""
	if fields := strings.Fields(execution.Command); len(fields) > 0 {
		entrypoint = fields[0]
	}
	return auditDetail(
		auditKV("entrypoint", entrypoint),
		auditKV("cwd", execution.Cwd),
		auditKV("timeout_seconds", execution.TimeoutSeconds),
	)
}

func tunnelAuditDetail(tunnel model.Tunnel) string {
	return auditDetail(
		auditKV("target_port", tunnel.TargetPort),
		auditKV("protocol", tunnel.Protocol),
		auditKV("auth_mode", tunnel.AuthMode),
		auditKV("visibility", tunnel.Visibility),
	)
}

func snapshotAuditDetail(snapshot model.Snapshot) string {
	return auditDetail(
		auditKV("name", snapshot.Name),
		auditKV("status", snapshot.Status),
		auditKV("exported", snapshot.ExportLocation != ""),
	)
}
````

## File: internal/service/tunnel_tcp.go
````go
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
````

## File: scripts/production-smoke.sh
````bash
#!/bin/sh
set -eu

GO_BIN="${GO_BIN:-}"
if [ -z "$GO_BIN" ]; then
	if command -v go >/dev/null 2>&1; then
		GO_BIN="$(command -v go)"
	elif [ -x /usr/local/go/bin/go ]; then
		GO_BIN=/usr/local/go/bin/go
	else
		echo "go binary not found; set GO_BIN or install Go" >&2
		exit 127
	fi
fi

cd "$(dirname "$0")/.."

exec "$GO_BIN" test \
	./internal/config \
	./internal/auth \
	./internal/service \
	./internal/api \
	./cmd/sandboxctl
````

## File: scripts/qemu-production-smoke.sh
````bash
#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE_IMAGE="${CORE_IMAGE:-${SANDBOX_QEMU_BASE_IMAGE_PATH:-}}"
RUNTIME_IMAGE="${RUNTIME_IMAGE:-}"
BROWSER_IMAGE="${BROWSER_IMAGE:-}"
CONTAINER_IMAGE="${CONTAINER_IMAGE:-}"
WORK_DIR="$(mktemp -d)"
SANDBOX_IDS=()
trap 'for id in "${SANDBOX_IDS[@]:-}"; do sandboxctl delete "$id" >/dev/null 2>&1 || true; done; rm -rf "$WORK_DIR"' EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

sandboxctl() {
  if [ -n "${SANDBOXCTL_BIN:-}" ]; then
    "$SANDBOXCTL_BIN" "$@"
  else
    (cd "$ROOT_DIR" && go run ./cmd/sandboxctl "$@")
  fi
}

require_cmd jq
require_cmd mktemp
require_cmd go

if [ -z "$CORE_IMAGE" ]; then
  echo "set CORE_IMAGE or SANDBOX_QEMU_BASE_IMAGE_PATH before running this smoke script" >&2
  exit 1
fi

log() {
  printf '[qemu-smoke] %s\n' "$*"
}

create_qemu_sandbox() {
  local image="$1"
  local profile="$2"
  local features="${3:-}"
  local json
  if [ -n "$features" ]; then
    json="$(sandboxctl create --image "$image" --profile "$profile" --features "$features" --cpu 1 --memory-mb 1024 --disk-mb 2048 --network internet-disabled --allow-tunnels=false --start=true)"
  else
    json="$(sandboxctl create --image "$image" --profile "$profile" --cpu 1 --memory-mb 1024 --disk-mb 2048 --network internet-disabled --allow-tunnels=false --start=true)"
  fi
  local sandbox_id
  sandbox_id="$(printf '%s' "$json" | jq -r '.id')"
  if [ -z "$sandbox_id" ] || [ "$sandbox_id" = "null" ]; then
    echo "failed to parse sandbox id from create response" >&2
    printf '%s\n' "$json" >&2
    exit 1
  fi
  SANDBOX_IDS+=("$sandbox_id")
  printf '%s\n' "$sandbox_id"
}

inspect_status() {
  sandboxctl inspect "$1" | jq -r '.status'
}

wait_for_status() {
  local sandbox_id="$1"
  local want="$2"
  local attempts="${3:-60}"
  local status
  for _ in $(seq 1 "$attempts"); do
    status="$(inspect_status "$sandbox_id")"
    if [ "$status" = "$want" ]; then
      return 0
    fi
    sleep 2
  done
  echo "sandbox $sandbox_id did not reach status $want (last=$status)" >&2
  return 1
}

assert_core_substrate() {
  local sandbox_id="$1"
  sandboxctl exec "$sandbox_id" sh -lc 'for cmd in python3 node docker; do if command -v "$cmd" >/dev/null 2>&1; then echo "unexpected command present: $cmd"; exit 1; fi; done; test -d /workspace; test -f /var/lib/or3/bootstrap.ready'
}

assert_runtime_profile() {
  local sandbox_id="$1"
  sandboxctl exec "$sandbox_id" sh -lc 'command -v python3 >/dev/null 2>&1 && command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1'
}

assert_browser_profile() {
  local sandbox_id="$1"
  sandboxctl exec "$sandbox_id" sh -lc 'command -v Xvfb >/dev/null 2>&1'
}

assert_container_profile() {
  local sandbox_id="$1"
  sandboxctl exec "$sandbox_id" sh -lc 'command -v docker >/dev/null 2>&1 && getent group docker >/dev/null 2>&1'
}

log 'running production doctor'
sandboxctl doctor --production-qemu >/dev/null

log 'creating core sandbox'
core_id="$(create_qemu_sandbox "$CORE_IMAGE" core)"
wait_for_status "$core_id" running

printf 'uploaded-from-host\n' > "$WORK_DIR/input.txt"
log 'verifying guest exec, file upload, and download'
sandboxctl upload "$core_id" "$WORK_DIR/input.txt" input.txt
sandboxctl exec "$core_id" sh -lc 'cat /workspace/input.txt > /workspace/output.txt && printf restored > /workspace/restore-marker.txt && id -un > /workspace/user.txt'
sandboxctl download "$core_id" output.txt "$WORK_DIR/output.txt"
if [ "$(cat "$WORK_DIR/output.txt")" != 'uploaded-from-host' ]; then
  echo 'downloaded artifact content mismatch' >&2
  exit 1
fi
assert_core_substrate "$core_id"

log 'verifying suspend/resume'
sandboxctl suspend "$core_id" >/dev/null
wait_for_status "$core_id" suspended
sandboxctl resume "$core_id" >/dev/null
wait_for_status "$core_id" running

log 'verifying snapshot create/restore'
sandboxctl stop "$core_id" >/dev/null
wait_for_status "$core_id" stopped
snapshot_json="$(sandboxctl snapshot-create --name qemu-smoke "$core_id")"
snapshot_id="$(printf '%s' "$snapshot_json" | jq -r '.id')"
if [ -z "$snapshot_id" ] || [ "$snapshot_id" = 'null' ]; then
  echo 'failed to parse snapshot id' >&2
  printf '%s\n' "$snapshot_json" >&2
  exit 1
fi
sandboxctl start "$core_id" >/dev/null
wait_for_status "$core_id" running
sandboxctl exec "$core_id" sh -lc 'rm -f /workspace/output.txt /workspace/restore-marker.txt'
sandboxctl stop "$core_id" >/dev/null
wait_for_status "$core_id" stopped
sandboxctl snapshot-restore "$snapshot_id" "$core_id" >/dev/null
sandboxctl start "$core_id" >/dev/null
wait_for_status "$core_id" running
sandboxctl download "$core_id" restore-marker.txt "$WORK_DIR/restore-marker.txt"
if [ "$(cat "$WORK_DIR/restore-marker.txt")" != 'restored' ]; then
  echo 'snapshot restore marker mismatch' >&2
  exit 1
fi

if [ -n "${SANDBOXD_RESTART_COMMAND:-}" ]; then
  log 'running optional daemon restart reconciliation step'
  if [ "${OR3_ALLOW_DISRUPTIVE:-0}" != '1' ]; then
    echo 'set OR3_ALLOW_DISRUPTIVE=1 to run SANDBOXD_RESTART_COMMAND during smoke' >&2
    exit 1
  fi
  eval "$SANDBOXD_RESTART_COMMAND"
  wait_for_status "$core_id" running 90
else
  log 'skipping daemon restart reconciliation step (set SANDBOXD_RESTART_COMMAND and OR3_ALLOW_DISRUPTIVE=1 to enable)'
fi

if [ -n "$RUNTIME_IMAGE" ]; then
  log 'verifying runtime profile capabilities'
  runtime_id="$(create_qemu_sandbox "$RUNTIME_IMAGE" runtime)"
  wait_for_status "$runtime_id" running
  assert_runtime_profile "$runtime_id"
fi

if [ -n "$BROWSER_IMAGE" ]; then
  log 'verifying browser profile capabilities'
  browser_id="$(create_qemu_sandbox "$BROWSER_IMAGE" browser)"
  wait_for_status "$browser_id" running
  assert_browser_profile "$browser_id"
fi

if [ -n "$CONTAINER_IMAGE" ]; then
  log 'verifying container profile capabilities'
  container_id="$(create_qemu_sandbox "$CONTAINER_IMAGE" container docker)"
  wait_for_status "$container_id" running
  assert_container_profile "$container_id"
fi

log 'qemu production smoke completed successfully'
````

## File: scripts/qemu-recovery-drill.sh
````bash
#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE_IMAGE="${CORE_IMAGE:-${SANDBOX_QEMU_BASE_IMAGE_PATH:-}}"
WORK_DIR="$(mktemp -d)"
SANDBOX_ID=""
trap 'if [ -n "$SANDBOX_ID" ]; then sandboxctl delete "$SANDBOX_ID" >/dev/null 2>&1 || true; fi; rm -rf "$WORK_DIR"' EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

sandboxctl() {
  if [ -n "${SANDBOXCTL_BIN:-}" ]; then
    "$SANDBOXCTL_BIN" "$@"
  else
    (cd "$ROOT_DIR" && go run ./cmd/sandboxctl "$@")
  fi
}

require_cmd jq
require_cmd mktemp
require_cmd go

if [ "${OR3_ALLOW_DISRUPTIVE:-0}" != '1' ]; then
  echo 'qemu-recovery-drill.sh is disruptive; set OR3_ALLOW_DISRUPTIVE=1 to continue' >&2
  exit 1
fi
if [ -z "$CORE_IMAGE" ]; then
  echo 'set CORE_IMAGE or SANDBOX_QEMU_BASE_IMAGE_PATH before running this recovery drill' >&2
  exit 1
fi

log() {
  printf '[qemu-recovery] %s\n' "$*"
}

wait_for_status() {
  local sandbox_id="$1"
  local want="$2"
  local attempts="${3:-60}"
  local status
  for _ in $(seq 1 "$attempts"); do
    status="$(sandboxctl inspect "$sandbox_id" | jq -r '.status')"
    if [ "$status" = "$want" ]; then
      return 0
    fi
    sleep 2
  done
  echo "sandbox $sandbox_id did not reach status $want (last=$status)" >&2
  return 1
}

create_json="$(sandboxctl create --image "$CORE_IMAGE" --profile core --cpu 1 --memory-mb 1024 --disk-mb 2048 --network internet-disabled --allow-tunnels=false --start=true)"
SANDBOX_ID="$(printf '%s' "$create_json" | jq -r '.id')"
if [ -z "$SANDBOX_ID" ] || [ "$SANDBOX_ID" = 'null' ]; then
  echo 'failed to create drill sandbox' >&2
  printf '%s\n' "$create_json" >&2
  exit 1
fi
wait_for_status "$SANDBOX_ID" running
sandboxctl exec "$SANDBOX_ID" sh -lc 'printf recovery-ok > /workspace/recovery.txt'

if [ -n "${SANDBOXD_RESTART_COMMAND:-}" ]; then
  log 'running daemon restart drill'
  eval "$SANDBOXD_RESTART_COMMAND"
  wait_for_status "$SANDBOX_ID" running 90
  sandboxctl download "$SANDBOX_ID" recovery.txt "$WORK_DIR/recovery.txt"
  test "$(cat "$WORK_DIR/recovery.txt")" = 'recovery-ok'
else
  log 'skipping daemon restart drill (set SANDBOXD_RESTART_COMMAND to enable)'
fi

log 'running conservative stopped-state restore drill'
sandboxctl stop "$SANDBOX_ID" >/dev/null
wait_for_status "$SANDBOX_ID" stopped
snapshot_json="$(sandboxctl snapshot-create --name recovery-drill "$SANDBOX_ID")"
snapshot_id="$(printf '%s' "$snapshot_json" | jq -r '.id')"
if [ -z "$snapshot_id" ] || [ "$snapshot_id" = 'null' ]; then
  echo 'failed to create recovery snapshot' >&2
  exit 1
fi
sandboxctl snapshot-restore "$snapshot_id" "$SANDBOX_ID" >/dev/null
wait_for_status "$SANDBOX_ID" stopped 30

log 'recovery drill completed successfully'
log 'guest-agent handshake failure and interrupted snapshot subdrills still require host-level fault injection outside this script'
````

## File: scripts/qemu-resource-abuse.sh
````bash
#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE_IMAGE="${CORE_IMAGE:-${SANDBOX_QEMU_BASE_IMAGE_PATH:-}}"
DISK_FILL_MB="${DISK_FILL_MB:-64}"
FILE_COUNT="${FILE_COUNT:-2000}"
PID_FANOUT="${PID_FANOUT:-32}"
STDOUT_LINES="${STDOUT_LINES:-4000}"
WORK_DIR="$(mktemp -d)"
SANDBOX_ID=""
trap 'if [ -n "$SANDBOX_ID" ]; then sandboxctl delete "$SANDBOX_ID" >/dev/null 2>&1 || true; fi; rm -rf "$WORK_DIR"' EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

sandboxctl() {
  if [ -n "${SANDBOXCTL_BIN:-}" ]; then
    "$SANDBOXCTL_BIN" "$@"
  else
    (cd "$ROOT_DIR" && go run ./cmd/sandboxctl "$@")
  fi
}

require_cmd jq
require_cmd mktemp
require_cmd go

if [ -z "$CORE_IMAGE" ]; then
  echo 'set CORE_IMAGE or SANDBOX_QEMU_BASE_IMAGE_PATH before running this abuse drill' >&2
  exit 1
fi

log() {
  printf '[qemu-abuse] %s\n' "$*"
}

wait_for_status() {
  local sandbox_id="$1"
  local want="$2"
  local attempts="${3:-60}"
  local status
  for _ in $(seq 1 "$attempts"); do
    status="$(sandboxctl inspect "$sandbox_id" | jq -r '.status')"
    if [ "$status" = "$want" ]; then
      return 0
    fi
    sleep 2
  done
  echo "sandbox $sandbox_id did not reach status $want (last=$status)" >&2
  return 1
}

create_json="$(sandboxctl create --image "$CORE_IMAGE" --profile core --cpu 1 --memory-mb 1024 --disk-mb 2048 --network internet-disabled --allow-tunnels=false --start=true)"
SANDBOX_ID="$(printf '%s' "$create_json" | jq -r '.id')"
if [ -z "$SANDBOX_ID" ] || [ "$SANDBOX_ID" = 'null' ]; then
  echo 'failed to create abuse sandbox' >&2
  printf '%s\n' "$create_json" >&2
  exit 1
fi
wait_for_status "$SANDBOX_ID" running

log 'running bounded stdout flood'
sandboxctl exec "$SANDBOX_ID" sh -lc "i=0; while [ \"\$i\" -lt $STDOUT_LINES ]; do echo abuse-line-\$i; i=\$((i+1)); done" >/dev/null

log 'running bounded file-count abuse'
sandboxctl exec "$SANDBOX_ID" sh -lc "mkdir -p /workspace/flood && i=0; while [ \"\$i\" -lt $FILE_COUNT ]; do : > /workspace/flood/file-\$i.txt; i=\$((i+1)); done"

log 'running bounded disk pressure drill'
sandboxctl exec "$SANDBOX_ID" sh -lc "dd if=/dev/zero of=/workspace/fill.bin bs=1M count=$DISK_FILL_MB status=none"

log 'running bounded pid fan-out drill'
sandboxctl exec "$SANDBOX_ID" sh -lc "children=''; i=0; while [ \"\$i\" -lt $PID_FANOUT ]; do sleep 2 & children=\"\$children \$!\"; i=\$((i+1)); done; wait \$children"

log 'capturing runtime health and quota views'
sandboxctl runtime-health > "$WORK_DIR/runtime-health.json"
sandboxctl quota > "$WORK_DIR/quota.json"
status="$(sandboxctl inspect "$SANDBOX_ID" | jq -r '.status')"
case "$status" in
  running|degraded|stopped)
    ;;
  *)
    echo "unexpected sandbox status after abuse drill: $status" >&2
    exit 1
    ;;
esac

log 'resource abuse drill completed successfully'
log "artifacts written to $WORK_DIR during execution (removed on exit)"
````

## File: images/guest/cloud-init/user-data.tpl
````
#cloud-config
users:
  - default
  - name: __AGENT_USER__
    gecos: or3 guest agent user
    system: true
    shell: /usr/sbin/nologin
  - name: __SANDBOX_USER__
    gecos: or3 sandbox user
    groups: __SANDBOX_GROUPS__
    shell: /bin/bash
__SANDBOX_SUDO_LINE____SSH_AUTHORIZED_KEYS_BLOCK__

package_update: true
package_upgrade: false
packages:
__PROFILE_PACKAGES__

write_files:
  - path: /usr/local/bin/or3-guest-agent
    permissions: "0755"
    owner: root:root
    encoding: b64
    content: __GUEST_AGENT_BINARY_BASE64__
  - path: /etc/systemd/system/or3-guest-agent.service
    permissions: "0644"
    owner: root:root
    content: |
      __GUEST_AGENT_SERVICE__
  - path: /usr/local/bin/or3-bootstrap.sh
    permissions: "0755"
    owner: root:root
    content: |
      __BOOTSTRAP_SCRIPT__
  - path: /etc/systemd/system/or3-bootstrap.service
    permissions: "0644"
    owner: root:root
    content: |
      __BOOTSTRAP_SERVICE__
  - path: /etc/or3/profile-manifest.json
    permissions: "0644"
    owner: root:root
    content: |
      __PROFILE_MANIFEST_JSON__

runcmd:
  - mkdir -p /var/lib/or3
  - mkdir -p /etc/or3
  - systemctl daemon-reload
__SSH_ENABLE_COMMANDS____PROFILE_ENABLE_COMMANDS__  - systemctl enable or3-guest-agent.service
  - systemctl enable or3-bootstrap.service
  - systemctl start or3-guest-agent.service
  - systemctl start or3-bootstrap.service

final_message: "or3 guest bootstrap complete for profile __PROFILE_NAME__"
````

## File: images/guest/systemd/or3-bootstrap.service
````
[Unit]
Description=or3 guest bootstrap
After=local-fs.target network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/or3-bootstrap.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
````

## File: images/guest/systemd/or3-bootstrap.sh
````bash
#!/usr/bin/env bash
set -euo pipefail

WORKSPACE_DEVICE="${WORKSPACE_DEVICE:-/dev/vdb}"
WORKSPACE_MOUNT="${WORKSPACE_MOUNT:-/workspace}"
READY_MARKER="${READY_MARKER:-/var/lib/or3/bootstrap.ready}"
WORKSPACE_OWNER="${WORKSPACE_OWNER:-sandbox}"
WORKSPACE_GROUP="${WORKSPACE_GROUP:-$WORKSPACE_OWNER}"

log_bootstrap() {
  local message="$1"
  echo "$message" >&2
  if [ -w /dev/ttyS0 ]; then
    echo "$message" >/dev/ttyS0 || true
  fi
}

mkdir -p "$(dirname "$READY_MARKER")"
mkdir -p "$WORKSPACE_MOUNT"

log_bootstrap "or3-bootstrap: starting"

if [ -b "$WORKSPACE_DEVICE" ]; then
  if ! blkid "$WORKSPACE_DEVICE" >/dev/null 2>&1; then
    mkfs.ext4 -F "$WORKSPACE_DEVICE"
  fi

  uuid="$(blkid -s UUID -o value "$WORKSPACE_DEVICE")"
  if [ -n "$uuid" ] && ! grep -q "$uuid" /etc/fstab; then
    echo "UUID=$uuid $WORKSPACE_MOUNT ext4 defaults,nofail 0 2" >> /etc/fstab
  fi

  mountpoint -q "$WORKSPACE_MOUNT" || mount "$WORKSPACE_MOUNT"
fi

if id "$WORKSPACE_OWNER" >/dev/null 2>&1; then
  chown "$WORKSPACE_OWNER:$WORKSPACE_GROUP" "$WORKSPACE_MOUNT"
  chmod 0755 "$WORKSPACE_MOUNT"
fi

install -d -o root -g root -m 0755 /var/lib/or3
touch "$READY_MARKER"
sync
log_bootstrap "or3-bootstrap: ready"
````

## File: images/guest/smoke-ssh.sh
````bash
#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_IMAGE="${BASE_IMAGE:-$ROOT_DIR/or3-guest-debug.qcow2}"
SSH_USER="${SSH_USER:-sandbox}"
SSH_PRIVATE_KEY_PATH="${SSH_PRIVATE_KEY_PATH:?set SSH_PRIVATE_KEY_PATH to the operator private key path}"
QEMU_BINARY="${QEMU_BINARY:-qemu-system-x86_64}"
QEMU_IMG_BINARY="${QEMU_IMG_BINARY:-qemu-img}"
QEMU_ACCEL="${QEMU_ACCEL:-kvm}"
SSH_PORT="${SSH_PORT:-2222}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"; if [ -f "$WORK_DIR/qemu.pid" ]; then kill "$(cat "$WORK_DIR/qemu.pid")" >/dev/null 2>&1 || true; fi' EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd "$QEMU_BINARY"
require_cmd "$QEMU_IMG_BINARY"
require_cmd ssh

"$QEMU_IMG_BINARY" create -f qcow2 -F qcow2 -b "$BASE_IMAGE" "$WORK_DIR/root-overlay.qcow2" 20G >/dev/null
"$QEMU_IMG_BINARY" create -f raw "$WORK_DIR/workspace.img" 10G >/dev/null

QEMU_NET_DEVICE="${QEMU_NET_DEVICE:-virtio-net-pci}"
if [[ "$QEMU_BINARY" == *aarch64* ]] && [[ "${QEMU_NET_DEVICE:-}" == "virtio-net-pci" ]]; then
  QEMU_NET_DEVICE="virtio-net-device"
fi

"$QEMU_BINARY" \
  -daemonize \
  -pidfile "$WORK_DIR/qemu.pid" \
  -monitor "unix:$WORK_DIR/monitor.sock,server,nowait" \
  -serial "file:$WORK_DIR/serial.log" \
  -display none \
  -accel "$QEMU_ACCEL" \
  -m 2048 \
  -smp 2 \
  -drive "if=virtio,file=$WORK_DIR/root-overlay.qcow2,format=qcow2" \
  -drive "if=virtio,file=$WORK_DIR/workspace.img,format=raw" \
  -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22" \
  -device "$QEMU_NET_DEVICE,netdev=net0"

for _ in $(seq 1 90); do
  if ssh \
    -o BatchMode=yes \
    -o IdentitiesOnly=yes \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile="$WORK_DIR/known_hosts" \
    -o ConnectTimeout=2 \
    -i "$SSH_PRIVATE_KEY_PATH" \
    -p "$SSH_PORT" \
    "$SSH_USER@127.0.0.1" \
    sh -lc 'test -f /var/lib/or3/bootstrap.ready && test -d /workspace'
  then
    echo "guest image is SSH reachable and bootstrap-ready"
    exit 0
  fi
  sleep 2
done

echo "guest image did not become ready" >&2
if [ -f "$WORK_DIR/serial.log" ]; then
  tail -n 50 "$WORK_DIR/serial.log" >&2 || true
fi
exit 1
````

## File: internal/auth/authenticator.go
````go
package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	jwt "github.com/golang-jwt/jwt/v5"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
)

type Authenticator interface {
	Authenticate(ctx context.Context, token string) (Identity, model.Tenant, model.TenantQuota, error)
}

type staticAuthenticator struct {
	store *repository.Store
}

type jwtAuthenticator struct {
	store        *repository.Store
	issuer       string
	audience     string
	secrets      []string
	loadErr      error
	defaultQuota model.TenantQuota
}

type jwtClaims struct {
	TenantID string   `json:"tenant_id"`
	Roles    []string `json:"roles"`
	Service  bool     `json:"service"`
	jwt.RegisteredClaims
}

func newAuthenticator(store *repository.Store, cfg config.Config) Authenticator {
	switch cfg.AuthMode {
	case "jwt-hs256":
		secrets, err := loadSecretValues(cfg.AuthJWTSecretPaths)
		return &jwtAuthenticator{
			store:        store,
			issuer:       cfg.AuthJWTIssuer,
			audience:     cfg.AuthJWTAudience,
			secrets:      secrets,
			loadErr:      err,
			defaultQuota: cfg.DefaultQuota,
		}
	default:
		return &staticAuthenticator{store: store}
	}
}

func (a *staticAuthenticator) Authenticate(ctx context.Context, token string) (Identity, model.Tenant, model.TenantQuota, error) {
	tenant, quota, err := a.store.AuthenticateTenant(ctx, config.HashToken(token))
	if err != nil {
		return Identity{}, model.Tenant{}, model.TenantQuota{}, err
	}
	return Identity{
		Subject:    tenant.ID,
		TenantID:   tenant.ID,
		Roles:      []string{"admin"},
		AuthMethod: "static",
	}, tenant, quota, nil
}

func (a *jwtAuthenticator) Authenticate(ctx context.Context, token string) (Identity, model.Tenant, model.TenantQuota, error) {
	if a.loadErr != nil {
		return Identity{}, model.Tenant{}, model.TenantQuota{}, a.loadErr
	}
	var err error
	secrets := a.secrets
	if len(secrets) == 0 {
		return Identity{}, model.Tenant{}, model.TenantQuota{}, fmt.Errorf("no jwt secrets loaded")
	}
	var claims jwtClaims
	var parseErr error
	for _, secret := range secrets {
		claims = jwtClaims{}
		_, parseErr = jwt.ParseWithClaims(token, &claims, func(parsed *jwt.Token) (any, error) {
			if parsed.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, fmt.Errorf("unexpected signing method %s", parsed.Method.Alg())
			}
			return []byte(secret), nil
		}, jwt.WithIssuer(a.issuer), jwt.WithAudience(a.audience))
		if parseErr == nil {
			break
		}
	}
	if parseErr != nil {
		return Identity{}, model.Tenant{}, model.TenantQuota{}, parseErr
	}
	if strings.TrimSpace(claims.TenantID) == "" {
		return Identity{}, model.Tenant{}, model.TenantQuota{}, fmt.Errorf("jwt claim tenant_id is required")
	}
	if claims.RegisteredClaims.Subject == "" {
		claims.RegisteredClaims.Subject = claims.TenantID
	}
	roles := append([]string(nil), claims.Roles...)
	if claims.Service && len(roles) == 0 {
		roles = []string{"service"}
	}
	tenant := model.Tenant{ID: claims.TenantID, Name: claims.TenantID}
	quota, err := a.store.GetQuota(ctx, claims.TenantID)
	if errors.Is(err, repository.ErrNotFound) {
		if err := a.store.EnsureTenantQuota(ctx, tenant, a.defaultQuota, config.HashToken("jwt:"+claims.TenantID)); err != nil {
			return Identity{}, model.Tenant{}, model.TenantQuota{}, err
		}
		quota, err = a.store.GetQuota(ctx, claims.TenantID)
	}
	if err != nil {
		return Identity{}, model.Tenant{}, model.TenantQuota{}, err
	}
	return Identity{
		Subject:    claims.RegisteredClaims.Subject,
		TenantID:   claims.TenantID,
		Roles:      roles,
		IsService:  claims.Service,
		AuthMethod: "jwt-hs256",
	}, tenant, quota, nil
}

func loadSecretValues(paths []string) ([]string, error) {
	values := make([]string, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		secret := strings.TrimSpace(string(data))
		if secret == "" {
			return nil, fmt.Errorf("secret file %s is empty", path)
		}
		values = append(values, secret)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("no jwt secrets loaded")
	}
	return values, nil
}
````

## File: internal/presets/manifest.go
````go
package presets

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"or3-sandbox/internal/model"
)

// YAML is used for preset manifests because these files are intended to be
// human-maintained example definitions under examples/, not machine-generated payloads.
const ManifestFileName = "preset.yaml"

type Manifest struct {
	Name        string          `json:"name" yaml:"name"`
	Description string          `json:"description,omitempty" yaml:"description,omitempty"`
	Runtime     RuntimeSelector `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Sandbox     SandboxPreset   `json:"sandbox" yaml:"sandbox"`
	Inputs      []Input         `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Files       []FileAsset     `json:"files,omitempty" yaml:"files,omitempty"`
	Bootstrap   []Step          `json:"bootstrap,omitempty" yaml:"bootstrap,omitempty"`
	Startup     *Step           `json:"startup,omitempty" yaml:"startup,omitempty"`
	Readiness   *ReadinessCheck `json:"readiness,omitempty" yaml:"readiness,omitempty"`
	Tunnel      *Tunnel         `json:"tunnel,omitempty" yaml:"tunnel,omitempty"`
	Artifacts   []Artifact      `json:"artifacts,omitempty" yaml:"artifacts,omitempty"`
	Cleanup     CleanupPolicy   `json:"cleanup,omitempty" yaml:"cleanup,omitempty"`

	BaseDir string `json:"-" yaml:"-"`
}

type RuntimeSelector struct {
	Allowed []string `json:"allowed,omitempty" yaml:"allowed,omitempty"`
	Profile string   `json:"profile,omitempty" yaml:"profile,omitempty"`
}

type SandboxPreset struct {
	Image        string `json:"image" yaml:"image"`
	CPULimit     string `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	MemoryMB     int    `json:"memory_mb,omitempty" yaml:"memory_mb,omitempty"`
	PIDsLimit    int    `json:"pids,omitempty" yaml:"pids,omitempty"`
	DiskMB       int    `json:"disk_mb,omitempty" yaml:"disk_mb,omitempty"`
	NetworkMode  string `json:"network,omitempty" yaml:"network,omitempty"`
	AllowTunnels bool   `json:"allow_tunnels,omitempty" yaml:"allow_tunnels,omitempty"`
	Start        *bool  `json:"start,omitempty" yaml:"start,omitempty"`
}

type Input struct {
	Name        string `json:"name" yaml:"name"`
	Required    bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty" yaml:"secret,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Default     string `json:"default,omitempty" yaml:"default,omitempty"`
}

type FileAsset struct {
	Path    string `json:"path" yaml:"path"`
	Content string `json:"content,omitempty" yaml:"content,omitempty"`
	Source  string `json:"source,omitempty" yaml:"source,omitempty"`
	Binary  bool   `json:"binary,omitempty" yaml:"binary,omitempty"`
}

type Step struct {
	Name            string            `json:"name,omitempty" yaml:"name,omitempty"`
	Command         []string          `json:"command" yaml:"command"`
	Env             map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	Cwd             string            `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	Timeout         time.Duration     `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Detached        bool              `json:"detached,omitempty" yaml:"detached,omitempty"`
	ContinueOnError bool              `json:"continue_on_error,omitempty" yaml:"continue_on_error,omitempty"`
}

type ReadinessCheck struct {
	Type           string        `json:"type" yaml:"type"`
	Command        []string      `json:"command,omitempty" yaml:"command,omitempty"`
	Path           string        `json:"path,omitempty" yaml:"path,omitempty"`
	Port           int           `json:"port,omitempty" yaml:"port,omitempty"`
	ExpectedStatus int           `json:"expected_status,omitempty" yaml:"expected_status,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Interval       time.Duration `json:"interval,omitempty" yaml:"interval,omitempty"`
}

type Tunnel struct {
	Port       int    `json:"port" yaml:"port"`
	Protocol   string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	AuthMode   string `json:"auth_mode,omitempty" yaml:"auth_mode,omitempty"`
	Visibility string `json:"visibility,omitempty" yaml:"visibility,omitempty"`
}

type Artifact struct {
	RemotePath string `json:"remote_path" yaml:"remote_path"`
	LocalPath  string `json:"local_path" yaml:"local_path"`
	Binary     bool   `json:"binary,omitempty" yaml:"binary,omitempty"`
}

type CleanupPolicy string

const (
	CleanupOnSuccess CleanupPolicy = "on-success"
	CleanupAlways    CleanupPolicy = "always"
	CleanupNever     CleanupPolicy = "never"
)

func (m *Manifest) Normalize() {
	if strings.TrimSpace(m.Name) == "" && strings.TrimSpace(m.BaseDir) != "" {
		m.Name = filepath.Base(m.BaseDir)
	}
	if m.Sandbox.CPULimit == "" {
		m.Sandbox.CPULimit = "1"
	}
	if m.Sandbox.MemoryMB <= 0 {
		m.Sandbox.MemoryMB = 1024
	}
	if m.Sandbox.PIDsLimit <= 0 {
		m.Sandbox.PIDsLimit = 512
	}
	if m.Sandbox.DiskMB <= 0 {
		m.Sandbox.DiskMB = 4096
	}
	if strings.TrimSpace(m.Sandbox.NetworkMode) == "" {
		m.Sandbox.NetworkMode = "internet-enabled"
	}
	if m.Cleanup == "" {
		m.Cleanup = CleanupOnSuccess
	}
	for index := range m.Bootstrap {
		normalizeStep(&m.Bootstrap[index], fmt.Sprintf("bootstrap[%d]", index))
	}
	if m.Startup != nil {
		normalizeStep(m.Startup, "startup")
	}
	if m.Readiness != nil {
		if m.Readiness.Timeout <= 0 {
			m.Readiness.Timeout = 30 * time.Second
		}
		if m.Readiness.Interval <= 0 {
			m.Readiness.Interval = time.Second
		}
		if m.Readiness.ExpectedStatus == 0 {
			m.Readiness.ExpectedStatus = 200
		}
		if m.Readiness.Path == "" {
			m.Readiness.Path = "/"
		}
	}
	if m.Tunnel != nil {
		if m.Tunnel.Protocol == "" {
			m.Tunnel.Protocol = "http"
		}
		if m.Tunnel.AuthMode == "" {
			m.Tunnel.AuthMode = "token"
		}
		if m.Tunnel.Visibility == "" {
			m.Tunnel.Visibility = "private"
		}
	}
	for index := range m.Runtime.Allowed {
		m.Runtime.Allowed[index] = strings.ToLower(strings.TrimSpace(m.Runtime.Allowed[index]))
	}
	sort.Strings(m.Runtime.Allowed)
}

func normalizeStep(step *Step, fallbackName string) {
	if step.Name == "" {
		step.Name = fallbackName
	}
	if step.Timeout <= 0 {
		step.Timeout = 5 * time.Minute
	}
	if step.Cwd == "" {
		step.Cwd = "/workspace"
	}
}

func (m Manifest) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(m.Sandbox.Image) == "" {
		return fmt.Errorf("sandbox.image is required")
	}
	if len(m.Runtime.Allowed) > 0 {
		seen := map[string]struct{}{}
		for _, runtimeName := range m.Runtime.Allowed {
			if runtimeName == "" {
				return fmt.Errorf("runtime.allowed entries must not be empty")
			}
			if _, exists := seen[runtimeName]; exists {
				return fmt.Errorf("runtime.allowed contains duplicate value %q", runtimeName)
			}
			seen[runtimeName] = struct{}{}
		}
	}
	if profile := model.GuestProfile(strings.TrimSpace(m.Runtime.Profile)); m.Runtime.Profile != "" && !profile.IsValid() {
		return fmt.Errorf("runtime.profile %q is invalid", m.Runtime.Profile)
	}
	seenInputs := map[string]struct{}{}
	for _, input := range m.Inputs {
		name := strings.TrimSpace(input.Name)
		if name == "" {
			return fmt.Errorf("input name is required")
		}
		if _, exists := seenInputs[name]; exists {
			return fmt.Errorf("duplicate input name %q", name)
		}
		seenInputs[name] = struct{}{}
	}
	for _, file := range m.Files {
		if strings.TrimSpace(file.Path) == "" {
			return fmt.Errorf("file path is required")
		}
		hasContent := strings.TrimSpace(file.Content) != ""
		hasSource := strings.TrimSpace(file.Source) != ""
		if hasContent == hasSource {
			return fmt.Errorf("file %q must specify exactly one of content or source", file.Path)
		}
	}
	for index, step := range m.Bootstrap {
		if len(step.Command) == 0 {
			return fmt.Errorf("bootstrap[%d] command is required", index)
		}
	}
	if m.Startup != nil && len(m.Startup.Command) == 0 {
		return fmt.Errorf("startup command is required")
	}
	if m.Readiness != nil {
		switch strings.ToLower(strings.TrimSpace(m.Readiness.Type)) {
		case "command":
			if len(m.Readiness.Command) == 0 {
				return fmt.Errorf("readiness.command requires command")
			}
		case "http":
			if m.Tunnel == nil {
				return fmt.Errorf("readiness.http requires tunnel configuration")
			}
			if m.Tunnel.Port <= 0 {
				return fmt.Errorf("tunnel.port must be positive for readiness.http")
			}
		case "":
			return fmt.Errorf("readiness.type is required")
		default:
			return fmt.Errorf("unsupported readiness.type %q", m.Readiness.Type)
		}
	}
	if m.Tunnel != nil {
		if m.Tunnel.Port <= 0 {
			return fmt.Errorf("tunnel.port must be positive")
		}
	}
	for _, artifact := range m.Artifacts {
		if strings.TrimSpace(artifact.RemotePath) == "" || strings.TrimSpace(artifact.LocalPath) == "" {
			return fmt.Errorf("artifacts require remote_path and local_path")
		}
	}
	switch m.Cleanup {
	case CleanupOnSuccess, CleanupAlways, CleanupNever:
	default:
		return fmt.Errorf("unsupported cleanup policy %q", m.Cleanup)
	}
	return nil
}

func (m Manifest) AllowsRuntime(runtimeName string) bool {
	if len(m.Runtime.Allowed) == 0 {
		return true
	}
	runtimeName = strings.ToLower(strings.TrimSpace(runtimeName))
	for _, allowed := range m.Runtime.Allowed {
		if allowed == runtimeName {
			return true
		}
	}
	return false
}
````

## File: images/guest/build-base-image.sh
````bash
#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$ROOT_DIR/../.." && pwd)"
CLOUD_INIT_DIR="$ROOT_DIR/cloud-init"
SYSTEMD_DIR="$ROOT_DIR/systemd"
PROFILES_DIR="$ROOT_DIR/profiles"

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd cloud-localds
require_cmd go
require_cmd jq
require_cmd python3
QEMU_BINARY="${QEMU_BINARY:-qemu-system-x86_64}"
QEMU_IMG_BINARY="${QEMU_IMG_BINARY:-qemu-img}"
QEMU_ACCEL="${QEMU_ACCEL:-kvm}"
require_cmd "$QEMU_BINARY"
require_cmd "$QEMU_IMG_BINARY"

PROFILE="${PROFILE:-core}"
PROFILE_MANIFEST="${PROFILE_MANIFEST:-$PROFILES_DIR/$PROFILE.json}"
if [ ! -f "$PROFILE_MANIFEST" ]; then
	echo "missing guest profile manifest: $PROFILE_MANIFEST" >&2
	exit 1
fi

BASE_IMAGE_URL="${BASE_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"
DOWNLOAD_PATH="${DOWNLOAD_PATH:-$ROOT_DIR/.cache/base-cloudimg.qcow2}"
OUTPUT_IMAGE="${OUTPUT_IMAGE:-$ROOT_DIR/or3-guest-$PROFILE.qcow2}"
PROFILE_RESOLVED_OUTPUT="${PROFILE_RESOLVED_OUTPUT:-$OUTPUT_IMAGE.profile.json}"
PACKAGE_INVENTORY_OUTPUT="${PACKAGE_INVENTORY_OUTPUT:-$OUTPUT_IMAGE.packages.txt}"
CONTRACT_OUTPUT="${CONTRACT_OUTPUT:-$OUTPUT_IMAGE.or3.json}"
SSH_HOST_PUBLIC_KEY_OUTPUT="${SSH_HOST_PUBLIC_KEY_OUTPUT:-$OUTPUT_IMAGE.ssh-host-key.pub}"
SANDBOX_USER="${SANDBOX_USER:-sandbox}"
AGENT_USER="${AGENT_USER:-or3-agent}"
GUEST_AGENT_GOARCH="${GUEST_AGENT_GOARCH:-}"
BUILD_VERSION="${BUILD_VERSION:-$(date -u +%Y%m%dT%H%M%SZ)}"
GIT_SHA="${GIT_SHA:-$(git -C "$REPO_ROOT" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)}"
WORK_DIR="$(mktemp -d)"
trap 'if [ -f "$WORK_DIR/qemu.pid" ]; then kill "$(cat "$WORK_DIR/qemu.pid")" >/dev/null 2>&1 || true; fi; rm -rf "$WORK_DIR"' EXIT

mkdir -p "$(dirname "$DOWNLOAD_PATH")"

if [ -z "$GUEST_AGENT_GOARCH" ]; then
	case "$QEMU_BINARY" in
		*aarch64*) GUEST_AGENT_GOARCH="arm64" ;;
		*) GUEST_AGENT_GOARCH="amd64" ;;
	esac
fi

python3 - "$PROFILE_MANIFEST" > "$WORK_DIR/profile.json" <<'PY'
import json
import pathlib
import sys

ARRAY_FIELDS = {"allowed_features", "capabilities", "enable_services", "packages", "sandbox_groups"}

def unique(values):
    seen = []
    for value in values:
        if value not in seen:
            seen.append(value)
    return seen

def merge(base, child):
    merged = dict(base)
    for key, value in child.items():
        if key == "extends":
            continue
        if key == "control":
            next_value = dict(base.get("control", {}))
            next_value.update(value or {})
            merged[key] = next_value
            continue
        if key in ARRAY_FIELDS:
            merged[key] = unique(list(base.get(key, [])) + list(value or []))
            continue
        merged[key] = value
    return merged

def load(path):
    data = json.loads(path.read_text())
    parent = data.get("extends")
    if not parent:
        return merge({}, data)
    parent_path = path.parent / f"{parent}.json"
    return merge(load(parent_path), data)

manifest_path = pathlib.Path(sys.argv[1]).resolve()
resolved = load(manifest_path)
if resolved.get("profile") != manifest_path.stem:
    raise SystemExit(f"resolved profile name mismatch: expected {manifest_path.stem!r}, got {resolved.get('profile')!r}")
print(json.dumps(resolved, indent=2))
PY

profile_name="$(jq -r '.profile' "$WORK_DIR/profile.json")"
ssh_present="$(jq -r '.ssh_present // false' "$WORK_DIR/profile.json")"
profile_packages="$(jq -r '.packages[] | "  - " + .' "$WORK_DIR/profile.json")"
sandbox_groups="$(jq -r '(.sandbox_groups // []) | if length == 0 then "[]" else "[" + (join(", ")) + "]" end' "$WORK_DIR/profile.json")"
profile_enable_commands="$(jq -r '(.enable_services // []) | map("  - systemctl enable " + . + "\n  - systemctl start " + .) | join("\n")' "$WORK_DIR/profile.json")"
profile_manifest_json="$(cat "$WORK_DIR/profile.json")"

sandbox_sudo_line=""
if [ "$(jq -r '.sandbox_passwordless_sudo // false' "$WORK_DIR/profile.json")" = "true" ]; then
	sandbox_sudo_line=$'    sudo: ALL=(ALL) NOPASSWD:ALL\n'
fi

ssh_authorized_keys_block=""
ssh_enable_commands=""
if [ "$ssh_present" = "true" ]; then
	SSH_PUBLIC_KEY_PATH="${SSH_PUBLIC_KEY_PATH:?set SSH_PUBLIC_KEY_PATH for ssh-compat/debug guest profiles}"
	ssh_public_key="$(tr -d '\n' < "$SSH_PUBLIC_KEY_PATH")"
	ssh_authorized_keys_block=$'    ssh_authorized_keys:\n'
	ssh_authorized_keys_block+="      - $ssh_public_key"
	ssh_authorized_keys_block+=$'\n'
	ssh_enable_commands=$'  - systemctl enable ssh\n  - systemctl start ssh\n'
fi

cp "$SYSTEMD_DIR/or3-bootstrap.sh" "$WORK_DIR/or3-bootstrap.sh"
cp "$SYSTEMD_DIR/or3-bootstrap.service" "$WORK_DIR/or3-bootstrap.service"
cp "$SYSTEMD_DIR/or3-guest-agent.service" "$WORK_DIR/or3-guest-agent.service"

(
	cd "$REPO_ROOT"
	CGO_ENABLED=0 GOOS=linux GOARCH="$GUEST_AGENT_GOARCH" go build -o "$WORK_DIR/or3-guest-agent" ./cmd/or3-guest-agent
)
guest_agent_binary_base64="$(base64 < "$WORK_DIR/or3-guest-agent" | tr -d '\n')"

AGENT_USER="$AGENT_USER" \
BOOTSTRAP_SCRIPT_CONTENT="$(cat "$WORK_DIR/or3-bootstrap.sh")" \
BOOTSTRAP_SERVICE_CONTENT="$(cat "$WORK_DIR/or3-bootstrap.service")" \
GUEST_AGENT_BINARY_BASE64="$guest_agent_binary_base64" \
GUEST_AGENT_SERVICE_CONTENT="$(cat "$WORK_DIR/or3-guest-agent.service")" \
PROFILE_ENABLE_COMMANDS="$profile_enable_commands" \
PROFILE_MANIFEST_JSON="$profile_manifest_json" \
PROFILE_NAME="$profile_name" \
PROFILE_PACKAGES="$profile_packages" \
SANDBOX_GROUPS="$sandbox_groups" \
SANDBOX_SUDO_LINE="$sandbox_sudo_line" \
SANDBOX_USER="$SANDBOX_USER" \
SSH_AUTHORIZED_KEYS_BLOCK="$ssh_authorized_keys_block" \
SSH_ENABLE_COMMANDS="$ssh_enable_commands" \
python3 - "$CLOUD_INIT_DIR/user-data.tpl" > "$WORK_DIR/user-data" <<'PY'
import os
import sys

template = open(sys.argv[1], 'r', encoding='utf-8').read()
for key in [
    "AGENT_USER",
    "BOOTSTRAP_SCRIPT_CONTENT",
    "BOOTSTRAP_SERVICE_CONTENT",
    "GUEST_AGENT_BINARY_BASE64",
    "GUEST_AGENT_SERVICE_CONTENT",
    "PROFILE_ENABLE_COMMANDS",
    "PROFILE_MANIFEST_JSON",
    "PROFILE_NAME",
    "PROFILE_PACKAGES",
    "SANDBOX_GROUPS",
    "SANDBOX_SUDO_LINE",
    "SANDBOX_USER",
    "SSH_AUTHORIZED_KEYS_BLOCK",
    "SSH_ENABLE_COMMANDS",
]:
    template = template.replace(f"__{key}__", os.environ.get(key, ""))
sys.stdout.write(template)
PY

if [ ! -f "$DOWNLOAD_PATH" ]; then
  require_cmd curl
  curl -L "$BASE_IMAGE_URL" -o "$DOWNLOAD_PATH"
fi

cp "$DOWNLOAD_PATH" "$WORK_DIR/base.qcow2"

sed \
  -e "s/__INSTANCE_ID__/or3-build/g" \
  -e "s/__HOSTNAME__/or3-build/g" \
  "$CLOUD_INIT_DIR/meta-data.tpl" > "$WORK_DIR/meta-data"

cloud-localds "$WORK_DIR/seed.img" "$WORK_DIR/user-data" "$WORK_DIR/meta-data"
"$QEMU_IMG_BINARY" create -f qcow2 -F qcow2 -b "$WORK_DIR/base.qcow2" "$OUTPUT_IMAGE" 20G >/dev/null
"$QEMU_IMG_BINARY" create -f raw "$WORK_DIR/workspace.img" 10G >/dev/null

net_device="virtio-net-pci"
if [[ "$QEMU_BINARY" == *aarch64* ]]; then
  net_device="virtio-net-device"
fi

"$QEMU_BINARY" \
  -daemonize \
  -pidfile "$WORK_DIR/qemu.pid" \
  -monitor "unix:$WORK_DIR/monitor.sock,server,nowait" \
  -serial "file:$WORK_DIR/serial.log" \
  -display none \
  -accel "$QEMU_ACCEL" \
  -m 2048 \
  -smp 2 \
  -device virtio-serial \
  -chardev "socket,id=agent0,path=$WORK_DIR/agent.sock,server=on,wait=off" \
  -device "virtserialport,chardev=agent0,name=org.or3.guest_agent" \
  -drive "if=virtio,file=$OUTPUT_IMAGE,format=qcow2" \
  -drive "if=virtio,file=$WORK_DIR/workspace.img,format=raw" \
  -drive "if=virtio,file=$WORK_DIR/seed.img,format=raw,readonly=on" \
  -netdev "user,id=net0" \
  -device "$net_device,netdev=net0"

agent_rpc() {
	local op="$1"
	local payload="${2:-null}"
	OR3_AGENT_OP="$op" OR3_AGENT_PAYLOAD="$payload" OR3_AGENT_SOCKET_PATH="$WORK_DIR/agent.sock" python3 - <<'PY'
import json
import os
import socket
import struct
import sys

sock_path = os.environ["OR3_AGENT_SOCKET_PATH"]
op = os.environ["OR3_AGENT_OP"]
payload = os.environ.get("OR3_AGENT_PAYLOAD", "null")
message = {"op": op}
if payload and payload != "null":
    message["result"] = json.loads(payload)
raw = json.dumps(message).encode("utf-8")
with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
    conn.connect(sock_path)
    conn.sendall(struct.pack(">I", len(raw)) + raw)
    header = conn.recv(4)
    if len(header) != 4:
        raise SystemExit("short guest-agent header")
    length = struct.unpack(">I", header)[0]
    data = b""
    while len(data) < length:
        chunk = conn.recv(length - len(data))
        if not chunk:
            raise SystemExit("short guest-agent body")
        data += chunk
reply = json.loads(data.decode("utf-8"))
if not reply.get("ok", False):
    raise SystemExit(reply.get("error") or "guest agent request failed")
result = reply.get("result")
if result is None:
    result = {}
print(json.dumps(result))
PY
}

agent_exec_stdout() {
  local command_json="$1"
  local result_json
  result_json="$(agent_rpc exec "$command_json")"
  printf '%s' "$result_json" | jq -r '.stdout_preview // ""' | tr -d '\r'
}

verify_profile_artifacts() {
  local packages_json ssh_present_json capabilities_json allowed_features_json
  packages_json="$(jq -c '.packages // []' "$WORK_DIR/profile.json")"
  ssh_present_json="$(jq -c '.ssh_present // false' "$WORK_DIR/profile.json")"
  capabilities_json="$(jq -c '.capabilities // []' "$WORK_DIR/profile.json")"
  allowed_features_json="$(jq -c '.allowed_features // []' "$WORK_DIR/profile.json")"

  OR3_PACKAGES_JSON="$packages_json" \
  OR3_SSH_PRESENT_JSON="$ssh_present_json" \
  OR3_CAPABILITIES_JSON="$capabilities_json" \
  OR3_ALLOWED_FEATURES_JSON="$allowed_features_json" \
  python3 - <<'PY' > "$WORK_DIR/verify-profile.sh"
import json
import os
from shlex import quote

packages = json.loads(os.environ.get("OR3_PACKAGES_JSON", "[]"))
ssh_present = json.loads(os.environ.get("OR3_SSH_PRESENT_JSON", "false"))
capabilities = set(json.loads(os.environ.get("OR3_CAPABILITIES_JSON", "[]")))
allowed_features = set(json.loads(os.environ.get("OR3_ALLOWED_FEATURES_JSON", "[]")))

lines = ["set -euo pipefail"]
if packages:
    lines.append("dpkg-query -W -f='${Package}\t${Version}\\n' " + " ".join(quote(pkg) for pkg in packages) + " | sort")
else:
    lines.append(":")
if ssh_present:
    lines.append("command -v sshd >/dev/null")
    lines.append("systemctl is-enabled ssh >/dev/null")
else:
    lines.append("if command -v sshd >/dev/null 2>&1; then echo 'unexpected sshd present for non-ssh profile' >&2; exit 1; fi")
if "container" in capabilities or "docker" in allowed_features:
    lines.append("command -v docker >/dev/null")
    lines.append("systemctl is-enabled docker >/dev/null")
print("\n".join(lines))
PY
  chmod +x "$WORK_DIR/verify-profile.sh"

  local verify_result
  verify_result="$(agent_exec_stdout "$(jq -cn --arg script "$(cat "$WORK_DIR/verify-profile.sh")" '{command:["sh","-lc",$script],cwd:"/"}')")"
  printf '%s\n' "$verify_result" > "$PACKAGE_INVENTORY_OUTPUT"
  if [ -s "$PACKAGE_INVENTORY_OUTPUT" ]; then
    sed -i.bak '/^$/d' "$PACKAGE_INVENTORY_OUTPUT" && rm -f "$PACKAGE_INVENTORY_OUTPUT.bak"
  fi
}

ready=0
for _ in $(seq 1 120); do
	if ready_json="$(agent_rpc ready '{}' 2>/dev/null)"; then
		if [ "$(printf '%s' "$ready_json" | jq -r '.ready // false')" = "true" ]; then
			ready=1
			break
		fi
	fi
	sleep 2
done

if [ "$ready" != "1" ]; then
  echo "guest image bootstrap did not reach readiness" >&2
  if [ -f "$WORK_DIR/serial.log" ]; then
    tail -n 50 "$WORK_DIR/serial.log" >&2 || true
  fi
  exit 1
fi

verify_profile_artifacts

if [ "$ssh_present" = "true" ]; then
	host_key_json="$(agent_rpc exec "$(jq -cn '{command:["sh","-lc","cat /etc/ssh/ssh_host_ed25519_key.pub"],cwd:"/"}')")"
	printf '%s\n' "$(printf '%s' "$host_key_json" | jq -r '.stdout_preview' | tr -d '\r')" > "$SSH_HOST_PUBLIC_KEY_OUTPUT"
	if [ ! -s "$SSH_HOST_PUBLIC_KEY_OUTPUT" ]; then
		echo "failed to capture guest SSH host public key" >&2
		exit 1
	fi
fi

agent_rpc shutdown '{"reboot":false}' >/dev/null 2>&1 || true
if [ -f "$WORK_DIR/qemu.pid" ]; then
	pid="$(cat "$WORK_DIR/qemu.pid")"
	for _ in $(seq 1 30); do
		if ! kill -0 "$pid" >/dev/null 2>&1; then
			break
		fi
		sleep 1
	done
	if kill -0 "$pid" >/dev/null 2>&1; then
		kill "$pid" >/dev/null 2>&1 || true
		sleep 2
	fi
fi

python3 - "$OUTPUT_IMAGE" <<'PY' > "$WORK_DIR/image.sha256"
import hashlib
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
sha = hashlib.sha256(path.read_bytes()).hexdigest()
print(sha)
PY
image_sha="$(tr -d '\n' < "$WORK_DIR/image.sha256")"

cp "$WORK_DIR/profile.json" "$PROFILE_RESOLVED_OUTPUT"

jq -n \
	--slurpfile manifest "$WORK_DIR/profile.json" \
	--arg image_path "$OUTPUT_IMAGE" \
	--arg image_sha "$image_sha" \
	--arg build_version "$BUILD_VERSION" \
	--arg git_sha "$GIT_SHA" \
	'{
		contract_version: "1",
		image_path: $image_path,
		image_sha256: $image_sha,
		build_version: $build_version,
		git_sha: $git_sha,
		profile: $manifest[0].profile,
		capabilities: ($manifest[0].capabilities // []),
		allowed_features: ($manifest[0].allowed_features // []),
		control: ($manifest[0].control // {}),
		workspace_contract_version: ($manifest[0].workspace_contract_version // "1"),
		ssh_present: ($manifest[0].ssh_present // false),
		dangerous: ($manifest[0].dangerous // false),
		debug: ($manifest[0].debug // false),
		package_inventory: ($manifest[0].packages // [])
	}' > "$CONTRACT_OUTPUT"

cat <<EOF
Prepared guest image:
  $OUTPUT_IMAGE

Resolved profile manifest:
  $PROFILE_RESOLVED_OUTPUT

Package inventory (actual guest package versions):
  $PACKAGE_INVENTORY_OUTPUT

Host-side image contract:
  $CONTRACT_OUTPUT

Selected profile:
  $profile_name

Control mode:
  $(jq -r '.control.mode' "$WORK_DIR/profile.json")

Workspace contract version:
  $(jq -r '.workspace_contract_version' "$WORK_DIR/profile.json")

Declared capabilities:
  $(jq -r '(.capabilities // []) | join(", ")' "$WORK_DIR/profile.json")

The image has been bootstrapped once with cloud-init, the guest agent, and the guest bootstrap service.

EOF

if [ "$ssh_present" = "true" ]; then
	cat <<EOF

Guest SSH host public key:
  $SSH_HOST_PUBLIC_KEY_OUTPUT

EOF
fi

cat <<EOF

Next step:
  The build already ran a guest-agent smoke/verification pass against the selected profile.
  Run images/guest/smoke-ssh.sh only for debug/ssh-compat images.
  Use the generated sidecar contract with SANDBOX_QEMU_BASE_IMAGE_PATH and SANDBOX_QEMU_ALLOWED_BASE_IMAGE_PATHS.
EOF
````

## File: images/guest/README.md
````markdown
# Guest Image Path

This directory contains the guest-image preparation assets for the production-oriented `qemu` runtime.

The image contract is now intentionally split into two layers:

- a small immutable substrate that every supported profile shares
- additive profiles that opt into extra tooling such as language runtimes, browser libraries, inner Docker, or debug-only SSH conveniences

The production-default control path is the guest agent over virtio-serial. SSH is kept only for explicit compatibility and debug profiles.

## Fixed profiles

The supported profiles live under `images/guest/profiles/`:

- `core`
  - minimal production profile
  - agent-based control
  - no SSH
  - no inner Docker
- `runtime`
  - `core` plus Git, Python, and Node tooling
- `browser`
  - `runtime` plus browser-supporting system libraries
- `container`
  - `core` plus inner Docker service and `docker` group membership
- `debug`
  - compatibility and troubleshooting profile
  - keeps SSH and elevated conveniences
  - marked dangerous and production-ineligible by default policy unless explicitly allowed

`core` is the default profile and the intended production baseline.

## Substrate contract

Every supported image is expected to provide the same substrate behavior:

- the guest agent reachable via virtio-serial at `org.or3.guest_agent`
- the readiness marker at `/var/lib/or3/bootstrap.ready`
- a persistent `/workspace` filesystem on the secondary disk
- a `sandbox` workload user
- a separate `or3-agent` identity reserved for future in-guest control split

Profile manifests declare what gets layered on top of that substrate:

- control mode and protocol version
- workspace contract version
- declared capabilities
- allowed additive features
- package inventory
- whether SSH is present

The host-side sidecar contract format is written next to each built image as `*.or3.json` and is consumed by runtime and policy validation.

## SSH material

The daemon never stores guest SSH keys in SQLite, but most supported production profiles do not need SSH at all.

Only `debug` / `ssh-compat` images need the operator to provide:

- `SANDBOX_QEMU_SSH_USER`
- `SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH`
- `SANDBOX_QEMU_SSH_HOST_KEY_PATH`

`build-base-image.sh` captures the guest SSH host public key only for SSH-bearing profiles.

## What the prepared image contains

The shared image substrate installs or enables:

- the guest agent binary and systemd unit
- a `systemd` oneshot bootstrap service that prepares `/workspace`
- the selected profile's declared package set and services

Profile-specific additions come from the manifest overlays under `images/guest/profiles/`.

Notably:

- `core`, `runtime`, and `browser` do not grant passwordless sudo or Docker group membership to the workload user
- `container` is the only profile that enables inner Docker by default
- `debug` is the only profile that includes SSH and troubleshooting tools by default

Operator-visible storage behavior:

- `disk_limit_mb` is split 50/50 between the writable guest system disk and the persistent workspace disk
- guest-local Docker data stays on the writable system disk, so it counts against the sandbox disk budget instead of using separate host-side storage

## Files

- `build-base-image.sh`
  - builds a profile-resolved qcow2 guest image from a cloud image
  - injects `cmd/or3-guest-agent`
  - boots the image once and verifies the selected profile against the guest via the guest agent
  - emits a resolved manifest, versioned package inventory, and `*.or3.json` sidecar contract
- `smoke-ssh.sh`
  - boots an SSH-bearing compatibility/debug image and verifies SSH reachability plus the readiness marker
- `cloud-init/user-data.tpl`
  - cloud-init template used for profile-aware first boot preparation
- `cloud-init/meta-data.tpl`
  - cloud-init metadata template
- `profiles/*.json`
  - fixed supported guest profile manifests
- `systemd/or3-bootstrap.sh`
  - guest-side bootstrap script that formats or mounts the workspace disk and writes the readiness marker
- `systemd/or3-bootstrap.service`
  - systemd unit that runs the bootstrap script at boot
- `systemd/or3-guest-agent.service`
  - systemd unit that keeps the guest agent running

## Lightweight init and supervision choice

The guest uses its existing `systemd` environment rather than adding another process manager.

That gives enough behavior for this phase:

- boot-time bootstrap
- service restart semantics
- guest-agent supervision
- optional Docker daemon management in the `container` profile

The control plane remains a single Go process outside the guest.

## Expected runtime contract

The `qemu` runtime assumes the selected guest image already supports the sidecar-declared contract for its profile.

For agent-based profiles, that means:

- the guest agent protocol version declared by the sidecar
- the readiness marker path created by guest bootstrap
- successful guest-agent handshake after boot

For `debug` / `ssh-compat` profiles, the sidecar also declares SSH presence and the operator must provide the SSH user and pinned host key material.

## Building images

Build the default production profile:

```bash
images/guest/build-base-image.sh
```

Build a heavier profile:

```bash
PROFILE=runtime images/guest/build-base-image.sh
PROFILE=browser images/guest/build-base-image.sh
PROFILE=container images/guest/build-base-image.sh
```

Build the compatibility/debug profile with SSH:

```bash
PROFILE=debug SSH_PUBLIC_KEY_PATH=$HOME/.ssh/id_ed25519.pub images/guest/build-base-image.sh
```

Each build produces:

- a qcow2 image
- a resolved profile manifest copy
- a package inventory text file with the guest-observed Debian package versions for the selected profile packages
- a host-side `*.or3.json` contract file

## Reproducibility expectations

This pipeline does not yet fully vendor or mirror the upstream Ubuntu package repositories, so exact bit-for-bit rebuilds are still constrained by the moving cloud image and apt repository state.

The current reproducibility contract is:

- profile manifests may carry exact apt selections when the operator needs to use `package=version` syntax
- every build records the resolved package versions observed inside the booted guest in the emitted package inventory file
- every build records the image checksum and build metadata in the sidecar contract
- release promotion should retain the qcow2 image, its `*.or3.json` contract, the resolved manifest copy, and the versioned package inventory together

For production release candidates, treat the package inventory as the authoritative record of what was actually admitted into the guest image.

## Build-time smoke checks

`build-base-image.sh` now performs a bounded smoke pass against the freshly booted guest before the image is handed off:

- verifies guest-agent readiness
- verifies every manifest-declared package is actually installed in the guest
- emits the observed package/version inventory from the guest itself
- verifies SSH-bearing profiles really expose `sshd` and the `ssh` service
- verifies `container`/Docker-enabled profiles really expose Docker and the `docker` service

That smoke pass is intended to catch profile/contract drift at build time rather than deferring discovery to host-side operator drills.

Use those artifacts with `sandboxctl doctor --production-qemu` and the QEMU runtime policy checks before treating an image as production-ready.
````

## File: internal/service/observability.go
````go
package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
)

type TenantQuotaView struct {
	Quota             model.TenantQuota      `json:"quota"`
	Usage             repository.TenantUsage `json:"usage"`
	StorageQuotaBytes int64                  `json:"storage_quota_bytes"`
	StoragePressure   float64                `json:"storage_pressure"`
	RunningPressure   float64                `json:"running_pressure"`
	CPUPressure       float64                `json:"cpu_pressure"`
	MemoryPressure    float64                `json:"memory_pressure"`
	Alerts            []string               `json:"alerts,omitempty"`
}

type CapacityReport struct {
	Backend          string                        `json:"backend"`
	CheckedAt        time.Time                     `json:"checked_at"`
	QuotaView        TenantQuotaView               `json:"quota_view"`
	StatusCounts     map[string]int                `json:"status_counts"`
	ProfileCounts    map[string]int                `json:"profile_counts,omitempty"`
	CapabilityCounts map[string]int                `json:"capability_counts,omitempty"`
	SnapshotCounts   map[model.SnapshotStatus]int  `json:"snapshot_counts,omitempty"`
	ExecutionCounts  map[model.ExecutionStatus]int `json:"execution_counts,omitempty"`
	Alerts           []string                      `json:"alerts,omitempty"`
}

func buildTenantQuotaView(quota model.TenantQuota, usage repository.TenantUsage) TenantQuotaView {
	storageQuotaBytes := int64(quota.MaxStorageMB) * 1024 * 1024
	view := TenantQuotaView{
		Quota:             quota,
		Usage:             usage,
		StorageQuotaBytes: storageQuotaBytes,
		StoragePressure:   ratioInt64(usage.ActualStorageBytes, storageQuotaBytes),
		RunningPressure:   ratioInt(usage.RunningSandboxes, quota.MaxRunningSandboxes),
		CPUPressure:       ratioInt64(usage.RequestedCPU.MilliValue(), quota.MaxCPUCores.MilliValue()),
		MemoryPressure:    ratioInt(usage.RequestedMemory, quota.MaxMemoryMB),
	}
	if view.StoragePressure >= 1 {
		view.Alerts = append(view.Alerts, "storage quota pressure exceeded")
	} else if view.StoragePressure >= 0.8 {
		view.Alerts = append(view.Alerts, "storage quota pressure above 80%")
	}
	if view.RunningPressure >= 1 {
		view.Alerts = append(view.Alerts, "running sandbox quota exceeded")
	}
	if view.CPUPressure >= 1 {
		view.Alerts = append(view.Alerts, "cpu quota pressure exceeded")
	}
	if view.MemoryPressure >= 1 {
		view.Alerts = append(view.Alerts, "memory quota pressure exceeded")
	}
	return view
}

func (s *Service) CapacityReport(ctx context.Context, tenantID string) (CapacityReport, error) {
	if err := s.enforceAdminInspectionPolicy(ctx, tenantID, "capacity.inspect"); err != nil {
		return CapacityReport{}, err
	}
	quota, err := s.store.GetQuota(ctx, tenantID)
	if err != nil {
		return CapacityReport{}, err
	}
	usage, err := s.store.TenantUsage(ctx, tenantID)
	if err != nil {
		return CapacityReport{}, err
	}
	sandboxes, err := s.store.ListSandboxes(ctx, tenantID)
	if err != nil {
		return CapacityReport{}, err
	}
	statusCounts := make(map[string]int)
	profileCounts := make(map[string]int)
	capabilityCounts := make(map[string]int)
	for _, sandbox := range sandboxes {
		statusCounts[string(sandbox.Status)]++
		if profile := strings.TrimSpace(string(sandbox.Profile)); profile != "" {
			profileCounts[profile]++
		}
		for _, capability := range sandbox.Capabilities {
			if trimmed := strings.TrimSpace(capability); trimmed != "" {
				capabilityCounts[trimmed]++
			}
		}
	}
	snapshotCounts, err := s.store.SnapshotCounts(ctx, tenantID)
	if err != nil {
		return CapacityReport{}, err
	}
	executionCounts, err := s.store.ExecutionCounts(ctx, tenantID)
	if err != nil {
		return CapacityReport{}, err
	}
	quotaView := buildTenantQuotaView(quota, usage)
	report := CapacityReport{
		Backend:          s.cfg.RuntimeBackend,
		CheckedAt:        time.Now().UTC(),
		QuotaView:        quotaView,
		StatusCounts:     statusCounts,
		ProfileCounts:    profileCounts,
		CapabilityCounts: capabilityCounts,
		SnapshotCounts:   snapshotCounts,
		ExecutionCounts:  executionCounts,
		Alerts:           append([]string(nil), quotaView.Alerts...),
	}
	if statusCounts[string(model.SandboxStatusDegraded)] > 0 {
		report.Alerts = append(report.Alerts, "one or more sandboxes are degraded")
	}
	if snapshotCounts[model.SnapshotStatusCreating] > 0 {
		report.Alerts = append(report.Alerts, "one or more snapshots are incomplete")
	}
	return report, nil
}

func (s *Service) MetricsReport(ctx context.Context, tenantID string) (string, error) {
	if err := s.enforceAdminInspectionPolicy(ctx, tenantID, "metrics.inspect"); err != nil {
		return "", err
	}
	report, err := s.CapacityReport(ctx, tenantID)
	if err != nil {
		return "", err
	}
	health, err := s.persistedRuntimeHealth(ctx, tenantID)
	if err != nil {
		return "", err
	}
	var lines []string
	lines = append(lines,
		"# TYPE or3_sandbox_sandboxes_total gauge",
		fmt.Sprintf("or3_sandbox_sandboxes_total %d", report.QuotaView.Usage.Sandboxes),
		fmt.Sprintf("or3_sandbox_running_sandboxes %d", report.QuotaView.Usage.RunningSandboxes),
		fmt.Sprintf("or3_sandbox_exec_running %d", report.QuotaView.Usage.ConcurrentExecs),
		fmt.Sprintf("or3_sandbox_tunnels_active %d", report.QuotaView.Usage.ActiveTunnels),
		fmt.Sprintf("or3_sandbox_actual_storage_bytes %d", report.QuotaView.Usage.ActualStorageBytes),
		fmt.Sprintf("or3_sandbox_storage_pressure_ratio %.6f", report.QuotaView.StoragePressure),
		fmt.Sprintf("or3_sandbox_running_pressure_ratio %.6f", report.QuotaView.RunningPressure),
		fmt.Sprintf("or3_sandbox_runtime_healthy %d", boolMetric(health.Healthy)),
	)
	for _, status := range sortedStringKeys(health.StatusCounts) {
		lines = append(lines, fmt.Sprintf("or3_sandbox_runtime_status_count{status=%q} %d", status, health.StatusCounts[status]))
	}
	for _, profile := range sortedStringKeys(report.ProfileCounts) {
		lines = append(lines, fmt.Sprintf("or3_sandbox_profile_count{profile=%q} %d", profile, report.ProfileCounts[profile]))
	}
	for _, capability := range sortedStringKeys(report.CapabilityCounts) {
		lines = append(lines, fmt.Sprintf("or3_sandbox_capability_count{capability=%q} %d", capability, report.CapabilityCounts[capability]))
	}
	for _, status := range sortedSnapshotStatuses(report.SnapshotCounts) {
		lines = append(lines, fmt.Sprintf("or3_sandbox_snapshots_count{status=%q} %d", status, report.SnapshotCounts[status]))
	}
	for _, status := range sortedExecutionStatuses(report.ExecutionCounts) {
		lines = append(lines, fmt.Sprintf("or3_sandbox_executions_count{status=%q} %d", status, report.ExecutionCounts[status]))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func (s *Service) persistedRuntimeHealth(ctx context.Context, tenantID string) (model.RuntimeHealth, error) {
	health := model.RuntimeHealth{
		Backend:      s.cfg.RuntimeBackend,
		Healthy:      true,
		CheckedAt:    time.Now().UTC(),
		StatusCounts: make(map[string]int),
	}
	var sandboxes []model.Sandbox
	var err error
	if tenantID != "" {
		sandboxes, err = s.store.ListNonDeletedSandboxesByTenant(ctx, tenantID)
	} else {
		sandboxes, err = s.store.ListNonDeletedSandboxes(ctx)
	}
	if err != nil {
		return health, err
	}
	for _, sandbox := range sandboxes {
		observedStatus := sandbox.Status
		if sandbox.RuntimeStatus != "" {
			observedStatus = model.SandboxStatus(sandbox.RuntimeStatus)
		}
		entry := model.RuntimeSandboxHealth{
			SandboxID:       sandbox.ID,
			TenantID:        sandbox.TenantID,
			PersistedStatus: sandbox.Status,
			ObservedStatus:  observedStatus,
			RuntimeID:       sandbox.RuntimeID,
			RuntimeStatus:   sandbox.RuntimeStatus,
			Error:           sandbox.LastRuntimeError,
		}
		health.StatusCounts[string(entry.ObservedStatus)]++
		health.Sandboxes = append(health.Sandboxes, entry)
		if entry.ObservedStatus == model.SandboxStatusError || entry.ObservedStatus == model.SandboxStatusDegraded {
			health.Healthy = false
		}
	}
	return health, nil
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
}

func ratioInt(value, limit int) float64 {
	if limit <= 0 {
		return 0
	}
	return float64(value) / float64(limit)
}

func ratioInt64(value, limit int64) float64 {
	if limit <= 0 {
		return 0
	}
	return float64(value) / float64(limit)
}

func sortedStringKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSnapshotStatuses(values map[model.SnapshotStatus]int) []model.SnapshotStatus {
	keys := make([]model.SnapshotStatus, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func sortedExecutionStatuses(values map[model.ExecutionStatus]int) []model.ExecutionStatus {
	keys := make([]model.ExecutionStatus, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
````

## File: internal/service/policy.go
````go
package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"or3-sandbox/internal/auth"
	"or3-sandbox/internal/model"
)

func (s *Service) enforceCreatePolicy(ctx context.Context, tenantID string, req model.CreateSandboxRequest) error {
	if !s.runtimeImageAllowed(s.cfg.RuntimeBackend, req.BaseImageRef) {
		message := fmt.Sprintf("image %q is not allowed by policy", req.BaseImageRef)
		s.recordAudit(ctx, tenantID, "", "policy.create", req.BaseImageRef, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	return nil
}

func (s *Service) enforceLifecyclePolicy(ctx context.Context, sandbox model.Sandbox, action string) error {
	now := time.Now().UTC()
	if s.cfg.PolicyMaxSandboxLifetime > 0 {
		age := now.Sub(sandbox.CreatedAt)
		if age > s.cfg.PolicyMaxSandboxLifetime {
			message := fmt.Sprintf("sandbox lifetime %s exceeds policy limit %s", age.Round(time.Second), s.cfg.PolicyMaxSandboxLifetime)
			s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.ID, "denied", message)
			return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
		}
	}
	if s.cfg.PolicyMaxIdleTimeout > 0 && !sandbox.LastActiveAt.IsZero() {
		idle := now.Sub(sandbox.LastActiveAt)
		if idle > s.cfg.PolicyMaxIdleTimeout {
			message := fmt.Sprintf("sandbox idle time %s exceeds policy limit %s", idle.Round(time.Second), s.cfg.PolicyMaxIdleTimeout)
			s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.ID, "denied", message)
			return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
		}
	}
	if !s.runtimeImageAllowed(sandbox.RuntimeBackend, sandbox.BaseImageRef) {
		message := fmt.Sprintf("sandbox image %q is no longer allowed by policy", sandbox.BaseImageRef)
		s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.BaseImageRef, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	if sandbox.RuntimeBackend == "qemu" {
		if sandbox.Profile != "" && !s.cfg.IsAllowedQEMUProfile(sandbox.Profile) {
			message := fmt.Sprintf("sandbox profile %q is no longer allowed by policy", sandbox.Profile)
			s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.ID, "denied", message)
			return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
		}
		if sandbox.Profile != "" && s.cfg.IsDangerousQEMUProfile(sandbox.Profile) && !s.cfg.QEMUAllowDangerousProfiles {
			message := fmt.Sprintf("sandbox profile %q is blocked by dangerous-profile policy", sandbox.Profile)
			s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.ID, "denied", message)
			return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
		}
	}
	return nil
}

func (s *Service) enforceTunnelPolicy(ctx context.Context, sandbox model.Sandbox, req model.CreateTunnelRequest) error {
	if strings.EqualFold(req.Visibility, "public") && !s.cfg.PolicyAllowPublicTunnels {
		message := "public tunnels are disabled by policy"
		s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy.tunnel", sandbox.ID, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	return nil
}

func (s *Service) enforceAdminInspectionPolicy(ctx context.Context, tenantID, action string) error {
	if s.cfg.DeploymentMode == "production" && s.cfg.RuntimeBackend != "qemu" {
		message := "admin inspection requires the qemu production boundary in production mode"
		s.recordAudit(ctx, tenantID, "", "policy."+action, action, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	return nil
}

func (s *Service) imageAllowed(imageRef string) bool {
	if len(s.cfg.PolicyAllowedImages) == 0 {
		return true
	}
	for _, allowed := range s.cfg.PolicyAllowedImages {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if strings.HasSuffix(allowed, "*") {
			if strings.HasPrefix(imageRef, strings.TrimSuffix(allowed, "*")) {
				return true
			}
			continue
		}
		if imageRef == allowed {
			return true
		}
	}
	return false
}

func (s *Service) runtimeImageAllowed(runtimeBackend, imageRef string) bool {
	if runtimeBackend == "qemu" {
		normalized := s.normalizeQEMUBaseImageRef(imageRef)
		for _, allowed := range s.cfg.EffectiveQEMUAllowedBaseImagePaths() {
			if normalized == allowed {
				return true
			}
		}
		return false
	}
	return s.imageAllowed(imageRef)
}
````

## File: internal/service/util.go
````go
package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func newID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("rand: %v", err))
	}
	return prefix + hex.EncodeToString(buf)
}

func resolveWorkspacePath(root, requested string) (string, error) {
	relativePath, err := cleanWorkspaceRelativePath(requested)
	if err != nil {
		return "", err
	}
	if relativePath == "" {
		return root, nil
	}
	return filepath.Join(root, relativePath), nil
}

func cleanWorkspaceRelativePath(requested string) (string, error) {
	trimmed := strings.TrimLeft(requested, string(filepath.Separator))
	if trimmed == "" {
		return "", nil
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return cleaned, nil
}

type boundedBuffer struct {
	limit     int
	buf       []byte
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = true
		return len(p), nil
	}
	remaining := b.limit - len(b.buf)
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf = append(b.buf, p[:remaining]...)
		b.truncated = true
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	return string(b.buf)
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func isReadableFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return true
}

func looksLikeFilesystemPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	return filepath.IsAbs(trimmed) || strings.Contains(trimmed, string(os.PathSeparator)) || strings.HasPrefix(trimmed, ".")
}
````

## File: .gitignore
````
planning/completed
.tmp
.env
.playwright-mcp
data
sandbox.db
````

## File: go.mod
````
module or3-sandbox

go 1.26.0

require (
	github.com/creack/pty v1.1.24
	github.com/golang-jwt/jwt/v5 v5.2.2
	github.com/gorilla/websocket v1.5.3
	gopkg.in/yaml.v3 v3.0.1
	golang.org/x/term v0.40.0
	golang.org/x/time v0.11.0
	modernc.org/sqlite v1.38.2
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/exp v0.0.0-20250620022241-b7579e27df2b // indirect
	golang.org/x/sys v0.41.0 // indirect
	modernc.org/libc v1.66.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
````

## File: internal/model/runtime.go
````go
package model

import (
	"context"
	"io"
	"time"
)

type SandboxSpec struct {
	SandboxID                string
	TenantID                 string
	BaseImageRef             string
	Profile                  GuestProfile
	Features                 []string
	Capabilities             []string
	ControlMode              GuestControlMode
	ControlProtocolVersion   string
	WorkspaceContractVersion string
	ImageContractVersion     string
	CPULimit                 CPUQuantity
	MemoryLimitMB            int
	PIDsLimit                int
	DiskLimitMB              int
	NetworkMode              NetworkMode
	AllowTunnels             bool
	StorageRoot              string
	WorkspaceRoot            string
	CacheRoot                string
}

type RuntimeState struct {
	RuntimeID              string
	Status                 SandboxStatus
	Running                bool
	Pid                    int
	IPAddress              string
	ControlMode            GuestControlMode
	ControlProtocolVersion string
	StartedAt              *time.Time
	Error                  string
}

type ExecResult struct {
	ExitCode        int
	Status          ExecutionStatus
	StartedAt       time.Time
	CompletedAt     time.Time
	Duration        time.Duration
	StdoutPreview   string
	StderrPreview   string
	StdoutTruncated bool
	StderrTruncated bool
}

type ExecStreams struct {
	Stdout io.Writer
	Stderr io.Writer
}

type ExecHandle interface {
	Wait() ExecResult
	Cancel() error
}

type ResizeRequest struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

type TTYHandle interface {
	Reader() io.Reader
	Writer() io.Writer
	Resize(ResizeRequest) error
	Close() error
}

type SnapshotInfo struct {
	ImageRef     string
	WorkspaceTar string
}

type StorageUsage struct {
	RootfsBytes    int64
	WorkspaceBytes int64
	CacheBytes     int64
	SnapshotBytes  int64
}

type RuntimeManager interface {
	Create(ctx context.Context, spec SandboxSpec) (RuntimeState, error)
	Start(ctx context.Context, sandbox Sandbox) (RuntimeState, error)
	Stop(ctx context.Context, sandbox Sandbox, force bool) (RuntimeState, error)
	Suspend(ctx context.Context, sandbox Sandbox) (RuntimeState, error)
	Resume(ctx context.Context, sandbox Sandbox) (RuntimeState, error)
	Destroy(ctx context.Context, sandbox Sandbox) error
	Inspect(ctx context.Context, sandbox Sandbox) (RuntimeState, error)
	Exec(ctx context.Context, sandbox Sandbox, req ExecRequest, streams ExecStreams) (ExecHandle, error)
	AttachTTY(ctx context.Context, sandbox Sandbox, req TTYRequest) (TTYHandle, error)
	CreateSnapshot(ctx context.Context, sandbox Sandbox, snapshotID string) (SnapshotInfo, error)
	RestoreSnapshot(ctx context.Context, sandbox Sandbox, snapshot Snapshot) (RuntimeState, error)
}
````

## File: internal/runtime/docker/runtime.go
````go
package docker

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"or3-sandbox/internal/model"
)

const previewLimit = 64 * 1024

type Runtime struct {
	binary string
}

func New() *Runtime {
	return &Runtime{binary: "docker"}
}

func NewWithBinary(binary string) *Runtime {
	if binary == "" {
		binary = "docker"
	}
	return &Runtime{binary: binary}
}

func (r *Runtime) Create(ctx context.Context, spec model.SandboxSpec) (model.RuntimeState, error) {
	if err := os.MkdirAll(spec.StorageRoot, 0o755); err != nil {
		return model.RuntimeState{}, err
	}
	if err := os.MkdirAll(spec.WorkspaceRoot, 0o755); err != nil {
		return model.RuntimeState{}, err
	}
	if spec.CacheRoot != "" {
		if err := os.MkdirAll(spec.CacheRoot, 0o755); err != nil {
			return model.RuntimeState{}, err
		}
	}
	if spec.NetworkMode == model.NetworkModeInternetEnabled {
		if err := r.ensureNetwork(ctx, spec.SandboxID); err != nil {
			return model.RuntimeState{}, err
		}
	}
	workspaceMount, err := absoluteHostPath(spec.WorkspaceRoot)
	if err != nil {
		return model.RuntimeState{}, err
	}
	args := []string{
		"create",
		"--name", containerName(spec.SandboxID),
		"--hostname", hostname(spec.SandboxID),
		"--init",
		"--label", "or3.sandbox.id=" + spec.SandboxID,
		"--label", "or3.tenant.id=" + spec.TenantID,
		"--cpus", spec.CPULimit.String(),
		"--memory", fmt.Sprintf("%dm", spec.MemoryLimitMB),
		"--pids-limit", strconv.Itoa(spec.PIDsLimit),
		"-v", fmt.Sprintf("%s:/workspace", workspaceMount),
	}
	if spec.CacheRoot != "" {
		cacheMount, err := absoluteHostPath(spec.CacheRoot)
		if err != nil {
			return model.RuntimeState{}, err
		}
		args = append(args, "-v", fmt.Sprintf("%s:/cache", cacheMount))
	}
	switch spec.NetworkMode {
	case model.NetworkModeInternetEnabled:
		args = append(args, "--network", networkName(spec.SandboxID))
	case model.NetworkModeInternetDisabled:
		args = append(args, "--network", "none")
	default:
		return model.RuntimeState{}, fmt.Errorf("unsupported network mode %q", spec.NetworkMode)
	}
	args = append(args, spec.BaseImageRef, "sleep", "infinity")
	out, err := r.run(ctx, args...)
	if err != nil {
		return model.RuntimeState{}, err
	}
	return model.RuntimeState{
		RuntimeID: strings.TrimSpace(out),
		Status:    model.SandboxStatusStopped,
		Running:   false,
	}, nil
}

func absoluteHostPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func (r *Runtime) Start(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	if _, err := r.run(ctx, "start", containerName(sandbox.ID)); err != nil {
		return model.RuntimeState{}, err
	}
	return r.Inspect(ctx, sandbox)
}

func (r *Runtime) Stop(ctx context.Context, sandbox model.Sandbox, force bool) (model.RuntimeState, error) {
	args := []string{"stop"}
	if force {
		args = []string{"kill"}
	}
	args = append(args, containerName(sandbox.ID))
	if _, err := r.run(ctx, args...); err != nil && !isNoSuchContainer(err) {
		return model.RuntimeState{}, err
	}
	return r.Inspect(ctx, sandbox)
}

func (r *Runtime) Suspend(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	if _, err := r.run(ctx, "pause", containerName(sandbox.ID)); err != nil {
		return model.RuntimeState{}, err
	}
	state, err := r.Inspect(ctx, sandbox)
	if err != nil {
		return model.RuntimeState{}, err
	}
	state.Status = model.SandboxStatusSuspended
	state.Running = false
	return state, nil
}

func (r *Runtime) Resume(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	if _, err := r.run(ctx, "unpause", containerName(sandbox.ID)); err != nil {
		return model.RuntimeState{}, err
	}
	return r.Inspect(ctx, sandbox)
}

func (r *Runtime) Destroy(ctx context.Context, sandbox model.Sandbox) error {
	if _, err := r.run(ctx, "rm", "-f", containerName(sandbox.ID)); err != nil && !isNoSuchContainer(err) {
		return err
	}
	if sandbox.NetworkMode == model.NetworkModeInternetEnabled {
		if _, err := r.run(ctx, "network", "rm", networkName(sandbox.ID)); err != nil && !isNoSuchNetwork(err) {
			return err
		}
	}
	for _, dir := range []string{sandbox.WorkspaceRoot, sandbox.CacheRoot, sandbox.StorageRoot} {
		if dir == "" {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) Inspect(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	out, err := r.run(ctx, "inspect", containerName(sandbox.ID))
	if err != nil {
		if isNoSuchContainer(err) {
			return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusDeleted}, nil
		}
		return model.RuntimeState{}, err
	}
	var payload []inspectPayload
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return model.RuntimeState{}, err
	}
	if len(payload) == 0 {
		return model.RuntimeState{}, errors.New("docker inspect returned no payload")
	}
	state := payload[0]
	result := model.RuntimeState{
		RuntimeID: state.ID,
		Pid:       state.State.Pid,
		IPAddress: state.NetworkSettings.IPAddress,
		Error:     state.State.Error,
	}
	switch {
	case state.State.Running:
		result.Status = model.SandboxStatusRunning
		result.Running = true
	case state.State.Paused:
		result.Status = model.SandboxStatusSuspended
	case state.State.Status == "created" || state.State.Status == "exited":
		result.Status = model.SandboxStatusStopped
	case state.State.Status == "removing":
		result.Status = model.SandboxStatusDeleting
	default:
		result.Status = model.SandboxStatusError
	}
	if state.State.StartedAt != "" && !strings.HasPrefix(state.State.StartedAt, "0001-") {
		t, err := time.Parse(time.RFC3339Nano, state.State.StartedAt)
		if err == nil {
			result.StartedAt = &t
		}
	}
	if result.IPAddress == "" {
		for _, network := range state.NetworkSettings.Networks {
			if network.IPAddress != "" {
				result.IPAddress = network.IPAddress
				break
			}
		}
	}
	return result, nil
}

func (r *Runtime) Exec(ctx context.Context, sandbox model.Sandbox, req model.ExecRequest, streams model.ExecStreams) (model.ExecHandle, error) {
	command := req.Command
	if len(command) == 0 {
		command = []string{"sh", "-lc", "pwd"}
	}
	if req.Detached {
		args := append([]string{"exec", "-d"}, execOptions(req)...)
		args = append(args, containerName(sandbox.ID))
		args = append(args, command...)
		if _, err := r.run(ctx, args...); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		return &execHandle{
			resultCh: closedResult(model.ExecResult{
				ExitCode:    0,
				Status:      model.ExecutionStatusDetached,
				StartedAt:   now,
				CompletedAt: now,
			}),
		}, nil
	}
	execID := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	pidFile := fmt.Sprintf("/tmp/or3-exec-%s.pid", execID)
	script := fmt.Sprintf(`
set -eu
rm -f %[1]s
setsid "$@" &
child=$!
echo "$child" > %[1]s
wait "$child"
`, shellQuote(pidFile))
	args := append([]string{"exec"}, execOptions(req)...)
	args = append(args, containerName(sandbox.ID), "sh", "-lc", script, "sh")
	args = append(args, command...)

	cmd := exec.Command(r.binary, args...)
	stdoutCapture := newPreviewWriter(streams.Stdout, previewLimit)
	stderrCapture := newPreviewWriter(streams.Stderr, previewLimit)
	cmd.Stdout = stdoutCapture
	cmd.Stderr = stderrCapture
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	handle := &execHandle{
		runtime:     r,
		containerID: containerName(sandbox.ID),
		pidFile:     pidFile,
		cmd:         cmd,
		startedAt:   time.Now().UTC(),
		stdout:      stdoutCapture,
		stderr:      stderrCapture,
		resultCh:    make(chan model.ExecResult, 1),
		done:        make(chan struct{}),
	}

	go handle.wait(req.Timeout, ctx)
	return handle, nil
}

func (r *Runtime) AttachTTY(ctx context.Context, sandbox model.Sandbox, req model.TTYRequest) (model.TTYHandle, error) {
	command := req.Command
	if len(command) == 0 {
		command = []string{"bash"}
	}
	args := append([]string{"exec", "-it"}, execOptions(model.ExecRequest{
		Env: req.Env,
		Cwd: req.Cwd,
	})...)
	args = append(args, containerName(sandbox.ID))
	args = append(args, command...)
	cmd := exec.CommandContext(ctx, r.binary, args...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(defaultInt(req.Rows, 24)),
		Cols: uint16(defaultInt(req.Cols, 80)),
	})
	if err != nil {
		return nil, err
	}
	if _, err := term.MakeRaw(int(ptmx.Fd())); err != nil {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, err
	}
	return &ttyHandle{cmd: cmd, pty: ptmx}, nil
}

func (r *Runtime) CreateSnapshot(ctx context.Context, sandbox model.Sandbox, snapshotID string) (model.SnapshotInfo, error) {
	imageRef := snapshotImage(snapshotID)
	if _, err := r.run(ctx, "commit", containerName(sandbox.ID), imageRef); err != nil {
		return model.SnapshotInfo{}, err
	}
	snapshotDir := filepath.Join(sandbox.StorageRoot, ".snapshots", snapshotID)
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return model.SnapshotInfo{}, err
	}
	tarPath := filepath.Join(snapshotDir, "workspace.tar.gz")
	if err := archiveDirectory(sandbox.WorkspaceRoot, tarPath); err != nil {
		return model.SnapshotInfo{}, err
	}
	return model.SnapshotInfo{
		ImageRef:     imageRef,
		WorkspaceTar: tarPath,
	}, nil
}

func (r *Runtime) RestoreSnapshot(ctx context.Context, sandbox model.Sandbox, snapshot model.Snapshot) (model.RuntimeState, error) {
	if _, err := r.run(ctx, "rm", "-f", containerName(sandbox.ID)); err != nil && !isNoSuchContainer(err) {
		return model.RuntimeState{}, err
	}
	if sandbox.NetworkMode == model.NetworkModeInternetEnabled {
		if err := r.ensureNetwork(ctx, sandbox.ID); err != nil {
			return model.RuntimeState{}, err
		}
	}
	if err := os.RemoveAll(sandbox.WorkspaceRoot); err != nil {
		return model.RuntimeState{}, err
	}
	if err := os.MkdirAll(sandbox.WorkspaceRoot, 0o755); err != nil {
		return model.RuntimeState{}, err
	}
	if snapshot.WorkspaceTar != "" {
		if err := extractArchive(snapshot.WorkspaceTar, sandbox.WorkspaceRoot); err != nil {
			return model.RuntimeState{}, err
		}
	}
	spec := model.SandboxSpec{
		SandboxID:     sandbox.ID,
		TenantID:      sandbox.TenantID,
		BaseImageRef:  snapshot.ImageRef,
		CPULimit:      sandbox.CPULimit,
		MemoryLimitMB: sandbox.MemoryLimitMB,
		PIDsLimit:     sandbox.PIDsLimit,
		DiskLimitMB:   sandbox.DiskLimitMB,
		NetworkMode:   sandbox.NetworkMode,
		AllowTunnels:  sandbox.AllowTunnels,
		StorageRoot:   sandbox.StorageRoot,
		WorkspaceRoot: sandbox.WorkspaceRoot,
		CacheRoot:     sandbox.CacheRoot,
	}
	return r.Create(ctx, spec)
}

func (r *Runtime) ensureNetwork(ctx context.Context, sandboxID string) error {
	if _, err := r.run(ctx, "network", "inspect", networkName(sandboxID)); err == nil {
		return nil
	}
	_, err := r.run(ctx, "network", "create", "--driver", "bridge", networkName(sandboxID))
	return err
}

func (r *Runtime) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

type execHandle struct {
	runtime     *Runtime
	containerID string
	pidFile     string
	cmd         *exec.Cmd
	startedAt   time.Time
	stdout      *previewWriter
	stderr      *previewWriter
	resultCh    chan model.ExecResult
	done        chan struct{}

	cancelOnce sync.Once
	cancelErr  error
	cancelKind model.ExecutionStatus
}

func (h *execHandle) Wait() model.ExecResult {
	return <-h.resultCh
}

func (h *execHandle) Cancel() error {
	h.cancel(model.ExecutionStatusCanceled)
	return h.cancelErr
}

func (h *execHandle) wait(timeout time.Duration, ctx context.Context) {
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		go func() {
			select {
			case <-timer.C:
				h.cancel(model.ExecutionStatusTimedOut)
			case <-ctx.Done():
				h.cancel(model.ExecutionStatusCanceled)
			case <-h.done:
			}
		}()
	} else {
		go func() {
			select {
			case <-ctx.Done():
				h.cancel(model.ExecutionStatusCanceled)
			case <-h.done:
			}
		}()
	}

	err := h.cmd.Wait()
	completedAt := time.Now().UTC()
	result := model.ExecResult{
		StartedAt:       h.startedAt,
		CompletedAt:     completedAt,
		Duration:        completedAt.Sub(h.startedAt),
		StdoutPreview:   h.stdout.String(),
		StderrPreview:   h.stderr.String(),
		StdoutTruncated: h.stdout.Truncated(),
		StderrTruncated: h.stderr.Truncated(),
		Status:          model.ExecutionStatusSucceeded,
	}
	if h.cancelKind != "" {
		result.Status = h.cancelKind
	} else if err != nil {
		result.Status = model.ExecutionStatusFailed
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				result.ExitCode = ws.ExitStatus()
			}
		} else {
			result.ExitCode = 1
			result.StderrPreview = strings.TrimSpace(result.StderrPreview + "\n" + err.Error())
		}
	}
	if result.Status == model.ExecutionStatusSucceeded {
		result.ExitCode = 0
	}
	h.resultCh <- result
	close(h.done)
	close(h.resultCh)
}

func (h *execHandle) cancel(kind model.ExecutionStatus) {
	h.cancelOnce.Do(func() {
		h.cancelKind = kind
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.cancelErr = h.runtime.killProcessGroup(ctx, h.containerID, h.pidFile)
		if h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
	})
}

func (r *Runtime) killProcessGroup(ctx context.Context, containerID, pidFile string) error {
	script := fmt.Sprintf(`
if [ -f %[1]s ]; then
	pgid=$(cat %[1]s)
	kill -TERM -- -"$pgid" 2>/dev/null || true
	sleep 1
	kill -KILL -- -"$pgid" 2>/dev/null || true
	rm -f %[1]s
fi
`, shellQuote(pidFile))
	_, err := r.run(ctx, "exec", containerID, "sh", "-lc", script)
	return err
}

type ttyHandle struct {
	cmd *exec.Cmd
	pty *os.File
}

func (h *ttyHandle) Reader() io.Reader {
	return h.pty
}

func (h *ttyHandle) Writer() io.Writer {
	return h.pty
}

func (h *ttyHandle) Resize(req model.ResizeRequest) error {
	return pty.Setsize(h.pty, &pty.Winsize{
		Rows: uint16(defaultInt(req.Rows, 24)),
		Cols: uint16(defaultInt(req.Cols, 80)),
	})
}

func (h *ttyHandle) Close() error {
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	if h.pty != nil {
		_ = h.pty.Close()
	}
	return nil
}

type previewWriter struct {
	target    io.Writer
	limit     int
	buf       strings.Builder
	truncated bool
	mu        sync.Mutex
}

func newPreviewWriter(target io.Writer, limit int) *previewWriter {
	return &previewWriter{target: target, limit: limit}
}

func (w *previewWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.target != nil {
		if _, err := w.target.Write(p); err != nil {
			return 0, err
		}
	}
	remaining := w.limit - w.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = w.buf.Write(p[:remaining])
			w.truncated = true
		} else {
			_, _ = w.buf.Write(p)
		}
	} else {
		w.truncated = true
	}
	return len(p), nil
}

func (w *previewWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *previewWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

type inspectPayload struct {
	ID    string `json:"Id"`
	State struct {
		Status    string `json:"Status"`
		Running   bool   `json:"Running"`
		Paused    bool   `json:"Paused"`
		Pid       int    `json:"Pid"`
		Error     string `json:"Error"`
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
	NetworkSettings struct {
		IPAddress string `json:"IPAddress"`
		Networks  map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func execOptions(req model.ExecRequest) []string {
	var args []string
	if req.Cwd != "" {
		args = append(args, "--workdir", req.Cwd)
	}
	for key, value := range req.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", key, value))
	}
	return args
}

func archiveDirectory(srcDir, destTarGz string) error {
	file, err := os.Create(destTarGz)
	if err != nil {
		return err
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, source)
		closeErr := source.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

func extractArchive(srcTarGz, destDir string) error {
	file, err := os.Open(srcTarGz)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, filepath.Clean(header.Name))
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) && filepath.Clean(target) != filepath.Clean(destDir) {
			return fmt.Errorf("tar entry escapes destination: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, tr); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		}
	}
}

func containerName(id string) string {
	return "or3-sandbox-" + id
}

func networkName(id string) string {
	return "or3-net-" + id
}

func hostname(id string) string {
	return "sandbox-" + id
}

func snapshotImage(id string) string {
	return "or3-snapshot-" + id + ":latest"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func defaultInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func isNoSuchContainer(err error) bool {
	return strings.Contains(err.Error(), "No such container")
}

func isNoSuchNetwork(err error) bool {
	return strings.Contains(err.Error(), "No such network")
}

func closedResult(result model.ExecResult) chan model.ExecResult {
	ch := make(chan model.ExecResult, 1)
	ch <- result
	close(ch)
	return ch
}
````

## File: internal/runtime/qemu/exec.go
````go
package qemu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"or3-sandbox/internal/model"
)

const execPreviewLimit = 64 * 1024

func (r *Runtime) Exec(ctx context.Context, sandbox model.Sandbox, req model.ExecRequest, streams model.ExecStreams) (model.ExecHandle, error) {
	if r.controlModeForSandbox(sandbox) == model.GuestControlModeAgent {
		return r.agentExec(ctx, layoutForSandbox(sandbox), req, streams)
	}
	command := req.Command
	if len(command) == 0 {
		command = []string{"sh", "-lc", "pwd"}
	}
	target := r.sshTarget(sandbox, layoutForSandbox(sandbox))
	if req.Detached {
		remoteScript := buildDetachedRemoteScript(command, req.Cwd, req.Env)
		args := append(r.baseSSHArgs(target, false), "sh", "-lc", remoteScript)
		if _, err := r.runCommand(ctx, r.sshBinary, args...); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		return &qemuExecHandle{
			resultCh: closedResult(model.ExecResult{
				ExitCode:    0,
				Status:      model.ExecutionStatusDetached,
				StartedAt:   now,
				CompletedAt: now,
			}),
		}, nil
	}

	execID := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	pidFile := "/tmp/or3-exec-" + execID + ".pid"
	remoteScript := buildTrackedRemoteScript(command, req.Cwd, req.Env, pidFile)
	args := append(r.baseSSHArgs(target, false), "sh", "-lc", remoteScript)
	cmd := exec.Command(r.sshBinary, args...)
	stdoutCapture := newPreviewWriter(streams.Stdout, execPreviewLimit)
	stderrCapture := newPreviewWriter(streams.Stderr, execPreviewLimit)
	cmd.Stdout = stdoutCapture
	cmd.Stderr = stderrCapture
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	handle := &qemuExecHandle{
		runtime:   r,
		target:    target,
		pidFile:   pidFile,
		cmd:       cmd,
		startedAt: time.Now().UTC(),
		stdout:    stdoutCapture,
		stderr:    stderrCapture,
		resultCh:  make(chan model.ExecResult, 1),
		done:      make(chan struct{}),
	}
	go handle.wait(req.Timeout, ctx)
	return handle, nil
}

func (r *Runtime) AttachTTY(ctx context.Context, sandbox model.Sandbox, req model.TTYRequest) (model.TTYHandle, error) {
	if r.controlModeForSandbox(sandbox) == model.GuestControlModeAgent {
		return r.agentAttachTTY(ctx, layoutForSandbox(sandbox), req)
	}
	command := req.Command
	if len(command) == 0 {
		command = []string{"bash"}
	}
	target := r.sshTarget(sandbox, layoutForSandbox(sandbox))
	remoteScript := buildInteractiveRemoteScript(command, req.Cwd, req.Env)
	args := append(r.baseSSHArgs(target, true), "sh", "-lc", remoteScript)
	cmd := exec.CommandContext(ctx, r.sshBinary, args...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(defaultInt(req.Rows, 24)),
		Cols: uint16(defaultInt(req.Cols, 80)),
	})
	if err != nil {
		return nil, err
	}
	if _, err := term.MakeRaw(int(ptmx.Fd())); err != nil {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, err
	}
	return &ttyHandle{cmd: cmd, pty: ptmx}, nil
}

type qemuExecHandle struct {
	runtime   *Runtime
	target    sshTarget
	pidFile   string
	cmd       *exec.Cmd
	startedAt time.Time
	stdout    *previewWriter
	stderr    *previewWriter
	resultCh  chan model.ExecResult
	done      chan struct{}

	cancelOnce sync.Once
	cancelErr  error
	cancelKind model.ExecutionStatus
}

func (h *qemuExecHandle) Wait() model.ExecResult {
	return <-h.resultCh
}

func (h *qemuExecHandle) Cancel() error {
	h.cancel(model.ExecutionStatusCanceled)
	return h.cancelErr
}

func (h *qemuExecHandle) wait(timeout time.Duration, ctx context.Context) {
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		go func() {
			select {
			case <-timer.C:
				h.cancel(model.ExecutionStatusTimedOut)
			case <-ctx.Done():
				h.cancel(model.ExecutionStatusCanceled)
			case <-h.done:
			}
		}()
	} else {
		go func() {
			select {
			case <-ctx.Done():
				h.cancel(model.ExecutionStatusCanceled)
			case <-h.done:
			}
		}()
	}

	err := h.cmd.Wait()
	completedAt := time.Now().UTC()
	result := model.ExecResult{
		StartedAt:       h.startedAt,
		CompletedAt:     completedAt,
		Duration:        completedAt.Sub(h.startedAt),
		StdoutPreview:   h.stdout.String(),
		StderrPreview:   h.stderr.String(),
		StdoutTruncated: h.stdout.Truncated(),
		StderrTruncated: h.stderr.Truncated(),
		Status:          model.ExecutionStatusSucceeded,
	}
	if h.cancelKind != "" {
		result.Status = h.cancelKind
	} else if err != nil {
		result.Status = model.ExecutionStatusFailed
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				result.ExitCode = ws.ExitStatus()
			} else {
				result.ExitCode = 1
			}
		} else {
			result.ExitCode = 1
			result.StderrPreview = strings.TrimSpace(result.StderrPreview + "\n" + err.Error())
		}
	}
	if result.Status == model.ExecutionStatusSucceeded {
		result.ExitCode = 0
	}
	h.resultCh <- result
	close(h.done)
	close(h.resultCh)
}

func (h *qemuExecHandle) cancel(kind model.ExecutionStatus) {
	h.cancelOnce.Do(func() {
		h.cancelKind = kind
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.cancelErr = h.runtime.killProcessGroup(ctx, h.target, h.pidFile)
		if h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
	})
}

type ttyHandle struct {
	cmd *exec.Cmd
	pty *os.File
}

func (h *ttyHandle) Reader() io.Reader {
	return h.pty
}

func (h *ttyHandle) Writer() io.Writer {
	return h.pty
}

func (h *ttyHandle) Resize(req model.ResizeRequest) error {
	return pty.Setsize(h.pty, &pty.Winsize{
		Rows: uint16(defaultInt(req.Rows, 24)),
		Cols: uint16(defaultInt(req.Cols, 80)),
	})
}

func (h *ttyHandle) Close() error {
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	if h.pty != nil {
		_ = h.pty.Close()
	}
	return nil
}

type previewWriter struct {
	target    io.Writer
	limit     int
	buf       strings.Builder
	truncated bool
	mu        sync.Mutex
}

func newPreviewWriter(target io.Writer, limit int) *previewWriter {
	return &previewWriter{target: target, limit: limit}
}

func (w *previewWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.target != nil {
		if _, err := w.target.Write(p); err != nil {
			return 0, err
		}
	}
	remaining := w.limit - w.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = w.buf.Write(p[:remaining])
			w.truncated = true
		} else {
			_, _ = w.buf.Write(p)
		}
	} else {
		w.truncated = true
	}
	return len(p), nil
}

func (w *previewWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *previewWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

func buildTrackedRemoteScript(command []string, cwd string, env map[string]string, pidFile string) string {
	commandLine := shellJoin(command)
	return fmt.Sprintf(`
set -eu
rm -f %[1]s
%[2]s
%[3]s
setsid sh -lc %[4]s &
child=$!
echo "$child" > %[1]s
wait "$child"
`, shellQuote(pidFile), buildCwdSnippet(cwd), buildEnvSnippet(env), shellQuote(commandLine))
}

func buildDetachedRemoteScript(command []string, cwd string, env map[string]string) string {
	commandLine := shellJoin(command)
	return fmt.Sprintf(`
set -eu
%[1]s
%[2]s
nohup sh -lc %[3]s >/dev/null 2>&1 </dev/null &
`, buildCwdSnippet(cwd), buildEnvSnippet(env), shellQuote(commandLine))
}

func buildInteractiveRemoteScript(command []string, cwd string, env map[string]string) string {
	commandLine := shellJoin(command)
	return strings.TrimSpace(fmt.Sprintf(`
set -eu
%[1]s
%[2]s
exec sh -lc %[3]s
`, buildCwdSnippet(cwd), buildEnvSnippet(env), shellQuote(commandLine)))
}

func buildCwdSnippet(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	return "cd " + shellQuote(cwd)
}

func buildEnvSnippet(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var lines []string
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("export %s=%s", key, shellQuote(env[key])))
	}
	return strings.Join(lines, "\n")
}

func shellJoin(parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func (r *Runtime) killProcessGroup(ctx context.Context, target sshTarget, pidFile string) error {
	script := fmt.Sprintf(`
if [ -f %[1]s ]; then
	pgid=$(cat %[1]s)
	kill -TERM -- -"$pgid" 2>/dev/null || true
	sleep 1
	kill -KILL -- -"$pgid" 2>/dev/null || true
	rm -f %[1]s
fi
`, shellQuote(pidFile))
	args := append(r.baseSSHArgs(target, false), "sh", "-lc", script)
	_, err := r.runCommand(ctx, r.sshBinary, args...)
	return err
}

func closedResult(result model.ExecResult) chan model.ExecResult {
	ch := make(chan model.ExecResult, 1)
	ch <- result
	close(ch)
	return ch
}
````

## File: internal/runtime/qemu/workspace.go
````go
package qemu

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"or3-sandbox/internal/model"
)

func (r *Runtime) ReadWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string) (model.FileReadResponse, error) {
	if r.controlModeForSandbox(sandbox) == model.GuestControlModeAgent {
		output, err := r.agentReadWorkspaceFileBytes(ctx, layoutForSandbox(sandbox), relativePath)
		if err != nil {
			return model.FileReadResponse{}, err
		}
		return model.FileReadResponse{Path: relativePath, Content: string(output), Size: int64(len(output)), Encoding: "utf-8"}, nil
	}
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", "cat "+shellQuote(target))
	output, err := r.runCommand(ctx, r.sshBinary, args...)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	return model.FileReadResponse{
		Path:     relativePath,
		Content:  string(output),
		Size:     int64(len(output)),
		Encoding: "utf-8",
	}, nil
}

func (r *Runtime) ReadWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string) ([]byte, error) {
	if r.controlModeForSandbox(sandbox) == model.GuestControlModeAgent {
		return r.agentReadWorkspaceFileBytes(ctx, layoutForSandbox(sandbox), relativePath)
	}
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return nil, err
	}
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", "cat "+shellQuote(target))
	return r.runCommand(ctx, r.sshBinary, args...)
}

func (r *Runtime) WriteWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string, content string) error {
	return r.writeWorkspaceFileBytes(ctx, sandbox, relativePath, bytes.NewBufferString(content))
}

func (r *Runtime) WriteWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string, content []byte) error {
	if r.controlModeForSandbox(sandbox) == model.GuestControlModeAgent {
		return r.agentWriteWorkspaceFileBytes(ctx, layoutForSandbox(sandbox), relativePath, content)
	}
	return r.writeWorkspaceFileBytes(ctx, sandbox, relativePath, bytes.NewReader(content))
}

func (r *Runtime) writeWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string, content io.Reader) error {
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	command := fmt.Sprintf("mkdir -p %s && cat > %s", shellQuote(path.Dir(target)), shellQuote(target))
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", command)
	cmd := exec.CommandContext(ctx, r.sshBinary, args...)
	cmd.Stdin = content
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("write workspace file: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (r *Runtime) DeleteWorkspacePath(ctx context.Context, sandbox model.Sandbox, relativePath string) error {
	if r.controlModeForSandbox(sandbox) == model.GuestControlModeAgent {
		return r.agentDeleteWorkspacePath(ctx, layoutForSandbox(sandbox), relativePath)
	}
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", "rm -rf "+shellQuote(target))
	_, err = r.runCommand(ctx, r.sshBinary, args...)
	return err
}

func (r *Runtime) MkdirWorkspace(ctx context.Context, sandbox model.Sandbox, relativePath string) error {
	if r.controlModeForSandbox(sandbox) == model.GuestControlModeAgent {
		return r.agentMkdirWorkspace(ctx, layoutForSandbox(sandbox), relativePath)
	}
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", "mkdir -p "+shellQuote(target))
	_, err = r.runCommand(ctx, r.sshBinary, args...)
	return err
}

func (r *Runtime) MeasureStorage(ctx context.Context, sandbox model.Sandbox) (model.StorageUsage, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	rootfsBytes, err := allocatedPathSize(layout.rootDiskPath)
	if err != nil {
		return model.StorageUsage{}, err
	}
	workspaceBytes, err := allocatedPathSize(layout.workspaceDiskPath)
	if err != nil {
		return model.StorageUsage{}, err
	}
	cacheBytes, err := allocatedPathSize(sandbox.CacheRoot)
	if err != nil {
		return model.StorageUsage{}, err
	}
	snapshotBytes, err := allocatedPathSize(filepath.Join(sandbox.StorageRoot, ".snapshots"))
	if err != nil {
		return model.StorageUsage{}, err
	}
	return model.StorageUsage{
		RootfsBytes:    rootfsBytes,
		WorkspaceBytes: workspaceBytes,
		CacheBytes:     cacheBytes,
		SnapshotBytes:  snapshotBytes,
	}, nil
}

func workspaceGuestPath(relativePath string) (string, error) {
	if strings.TrimSpace(relativePath) == "" {
		return "/workspace", nil
	}
	clean := path.Clean("/workspace/" + filepath.ToSlash(relativePath))
	if clean != "/workspace" && !strings.HasPrefix(clean, "/workspace/") {
		return "", fmt.Errorf("path escapes workspace")
	}
	return clean, nil
}
````

## File: cmd/sandboxctl/preset.go
````go
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
	templateVars := r.templateVars(inputs)
	req, err := r.buildCreateRequest(templateVars)
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
	for key, value := range templateVars {
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

func (r presetRunner) templateVars(inputs map[string]string) map[string]string {
	vars := make(map[string]string, len(inputs)+len(r.dotEnvValues)+len(r.inputOverrides))
	for key, value := range r.dotEnvValues {
		if strings.TrimSpace(value) != "" {
			vars[key] = value
		}
	}
	for key, value := range inputs {
		if strings.TrimSpace(value) != "" {
			vars[key] = value
		}
	}
	for key, value := range r.inputOverrides {
		if strings.TrimSpace(value) != "" {
			vars[key] = value
		}
	}
	return vars
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
	return model.CreateSandboxRequest{BaseImageRef: image, Profile: model.GuestProfile(strings.ToLower(strings.TrimSpace(r.manifest.Runtime.Profile))), CPULimit: cpuLimit, MemoryLimitMB: memoryMB, PIDsLimit: pidsLimit, DiskLimitMB: diskMB, NetworkMode: model.NetworkMode(networkMode), AllowTunnels: &allowTunnels, Start: start}, nil
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
	var info model.RuntimeInfo
	if err := doJSON(client, http.MethodGet, "/v1/runtime/info", nil, &info); err == nil {
		backend := strings.ToLower(strings.TrimSpace(info.Backend))
		if backend != "" {
			adapter.name = backend
		}
	} else {
		var health model.RuntimeHealth
		if err := doJSON(client, http.MethodGet, "/v1/runtime/health", nil, &health); err == nil {
			backend := strings.ToLower(strings.TrimSpace(health.Backend))
			if backend != "" {
				adapter.name = backend
			}
		} else if len(manifest.Runtime.Allowed) == 1 {
			adapter.name = strings.ToLower(strings.TrimSpace(manifest.Runtime.Allowed[0]))
		}
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
````

## File: internal/auth/middleware.go
````go
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/repository"
)

type Middleware struct {
	store         *repository.Store
	authenticator Authenticator
	log           *slog.Logger
	limiters      sync.Map
	lastPruneUnix atomic.Int64
	rate          rate.Limit
	burst         int
}

type tenantLimiter struct {
	limiter  *rate.Limiter
	lastSeen atomic.Int64
}

func New(store *repository.Store, cfg config.Config, logs ...*slog.Logger) *Middleware {
	perSecond := float64(cfg.RequestRatePerMinute) / 60.0
	log := slog.Default()
	if len(logs) > 0 && logs[0] != nil {
		log = logs[0]
	}
	return &Middleware{
		store:         store,
		authenticator: newAuthenticator(store, cfg),
		log:           log,
		rate:          rate.Limit(perSecond),
		burst:         cfg.RequestBurst,
	}
}

func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		token, err := bearerToken(r.Header.Get("Authorization"))
		if err != nil {
			if isTunnelProxyPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			m.log.Warn("auth rejected", "event", "auth.reject", "auth_mode", "bearer", "reason", "missing_or_invalid_authorization_header", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		identity, tenant, quota, err := m.authenticator.Authenticate(r.Context(), token)
		if err != nil {
			m.log.Warn("auth rejected", "event", "auth.reject", "reason", "authentication_failed", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		limiter := m.limiterFor(tenant.ID)
		if !limiter.Allow() {
			m.log.Warn("rate limit exceeded", "event", "auth.rate_limit", "tenant_id", tenant.ID, "subject", identity.Subject, "auth_method", identity.AuthMethod, "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		ctx := context.WithValue(r.Context(), tenantContextKey{}, TenantContext{
			Tenant:   tenant,
			Quota:    quota,
			Identity: identity,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isTunnelProxyPath(path string) bool {
	return strings.HasPrefix(path, "/v1/tunnels/") && strings.Contains(path, "/proxy")
}

func bearerToken(header string) (string, error) {
	if header == "" {
		return "", errors.New("missing authorization header")
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", errors.New("invalid authorization header")
	}
	return strings.TrimSpace(parts[1]), nil
}

func (m *Middleware) limiterFor(tenantID string) *rate.Limiter {
	now := time.Now().UnixNano()
	if value, ok := m.limiters.Load(tenantID); ok {
		entry := value.(*tenantLimiter)
		entry.lastSeen.Store(now)
		m.maybePrune(now)
		return entry.limiter
	}
	entry := &tenantLimiter{limiter: rate.NewLimiter(m.rate, m.burst)}
	entry.lastSeen.Store(now)
	actual, _ := m.limiters.LoadOrStore(tenantID, entry)
	stored := actual.(*tenantLimiter)
	stored.lastSeen.Store(now)
	m.maybePrune(now)
	return stored.limiter
}

func Prune(limiters *sync.Map, olderThan time.Duration) {
	if limiters == nil || olderThan <= 0 {
		return
	}
	cutoff := time.Now().Add(-olderThan).UnixNano()
	limiters.Range(func(key, value any) bool {
		entry, ok := value.(*tenantLimiter)
		if !ok || entry.lastSeen.Load() < cutoff {
			limiters.Delete(key)
		}
		return true
	})
}

func (m *Middleware) maybePrune(nowUnixNano int64) {
	last := m.lastPruneUnix.Load()
	if last != 0 && nowUnixNano-last < int64(5*time.Minute) {
		return
	}
	if !m.lastPruneUnix.CompareAndSwap(last, nowUnixNano) {
		return
	}
	Prune(&m.limiters, 15*time.Minute)
}
````

## File: internal/db/db.go
````go
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 3

func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		return nil, err
	}
	return db, nil
}

func sqliteDSN(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	values := url.Values{}
	values.Add("_pragma", "foreign_keys(1)")
	values.Add("_pragma", "journal_mode(WAL)")
	values.Add("_pragma", "synchronous(NORMAL)")
	values.Add("_pragma", "busy_timeout(5000)")
	return (&url.URL{Scheme: "file", Path: absPath, RawQuery: values.Encode()}).String(), nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (0, CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS tenants (
			tenant_id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS quotas (
			tenant_id TEXT PRIMARY KEY REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			max_sandboxes INTEGER NOT NULL,
			max_running_sandboxes INTEGER NOT NULL,
			max_concurrent_execs INTEGER NOT NULL,
			max_tunnels INTEGER NOT NULL,
			max_cpu_cores INTEGER NOT NULL,
			max_cpu_millis INTEGER NOT NULL DEFAULT 0,
			max_memory_mb INTEGER NOT NULL,
			max_storage_mb INTEGER NOT NULL,
			allow_tunnels INTEGER NOT NULL,
			default_tunnel_auth_mode TEXT NOT NULL,
			default_tunnel_visibility TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS sandboxes (
			sandbox_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			status TEXT NOT NULL,
			runtime_backend TEXT NOT NULL,
			base_image_ref TEXT NOT NULL,
			profile TEXT NOT NULL DEFAULT '',
			feature_set TEXT NOT NULL DEFAULT '',
			capability_set TEXT NOT NULL DEFAULT '',
			control_mode TEXT NOT NULL DEFAULT '',
			control_protocol_version TEXT NOT NULL DEFAULT '',
			workspace_contract_version TEXT NOT NULL DEFAULT '',
			image_contract_version TEXT NOT NULL DEFAULT '',
			cpu_limit INTEGER NOT NULL,
			cpu_limit_millis INTEGER NOT NULL DEFAULT 0,
			memory_limit_mb INTEGER NOT NULL,
			pids_limit INTEGER NOT NULL,
			disk_limit_mb INTEGER NOT NULL,
			network_mode TEXT NOT NULL,
			allow_tunnels INTEGER NOT NULL,
			storage_root TEXT NOT NULL,
			workspace_root TEXT NOT NULL,
			cache_root TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_active_at TEXT NOT NULL,
			deleted_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS sandbox_runtime_state (
			sandbox_id TEXT PRIMARY KEY REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			runtime_id TEXT NOT NULL,
			runtime_status TEXT NOT NULL,
			last_runtime_error TEXT NOT NULL DEFAULT '',
			ip_address TEXT NOT NULL DEFAULT '',
			pid INTEGER NOT NULL DEFAULT 0,
			started_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS sandbox_storage (
			sandbox_id TEXT PRIMARY KEY REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			rootfs_bytes INTEGER NOT NULL DEFAULT 0,
			workspace_bytes INTEGER NOT NULL DEFAULT 0,
			cache_bytes INTEGER NOT NULL DEFAULT 0,
			snapshot_bytes INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS tunnels (
			tunnel_id TEXT PRIMARY KEY,
			sandbox_id TEXT NOT NULL REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			target_port INTEGER NOT NULL,
			protocol TEXT NOT NULL,
			auth_mode TEXT NOT NULL,
			auth_secret_hash TEXT NOT NULL DEFAULT '',
			visibility TEXT NOT NULL,
			endpoint TEXT NOT NULL,
			created_at TEXT NOT NULL,
			revoked_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS snapshots (
			snapshot_id TEXT PRIMARY KEY,
			sandbox_id TEXT NOT NULL REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			status TEXT NOT NULL,
			image_ref TEXT NOT NULL,
			profile TEXT NOT NULL DEFAULT '',
			image_contract_version TEXT NOT NULL DEFAULT '',
			control_protocol_version TEXT NOT NULL DEFAULT '',
			workspace_tar TEXT NOT NULL,
			export_location TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			completed_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS executions (
			execution_id TEXT PRIMARY KEY,
			sandbox_id TEXT NOT NULL REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			command TEXT NOT NULL,
			cwd TEXT NOT NULL,
			timeout_seconds INTEGER NOT NULL,
			status TEXT NOT NULL,
			exit_code INTEGER,
			stdout_preview TEXT NOT NULL DEFAULT '',
			stderr_preview TEXT NOT NULL DEFAULT '',
			stdout_truncated INTEGER NOT NULL DEFAULT 0,
			stderr_truncated INTEGER NOT NULL DEFAULT 0,
			started_at TEXT NOT NULL,
			completed_at TEXT,
			duration_ms INTEGER
		);`,
		`CREATE TABLE IF NOT EXISTS tty_sessions (
			tty_session_id TEXT PRIMARY KEY,
			sandbox_id TEXT NOT NULL REFERENCES sandboxes(sandbox_id) ON DELETE CASCADE,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			command TEXT NOT NULL,
			connected INTEGER NOT NULL,
			last_resize TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			closed_at TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			audit_event_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
			sandbox_id TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL,
			resource_id TEXT NOT NULL DEFAULT '',
			outcome TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_sandboxes_tenant_status_created ON sandboxes(tenant_id, status, created_at);`,
		`CREATE INDEX IF NOT EXISTS idx_sandboxes_status ON sandboxes(status);`,
		`CREATE INDEX IF NOT EXISTS idx_executions_tenant_status ON executions(tenant_id, status);`,
		`CREATE INDEX IF NOT EXISTS idx_tunnels_tenant_sandbox_revoked ON tunnels(tenant_id, sandbox_id, revoked_at);`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_tenant_status ON snapshots(tenant_id, status);`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_tenant_created ON audit_events(tenant_id, created_at);`,
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := ensureColumn(ctx, tx, "tunnels", "auth_secret_hash", `ALTER TABLE tunnels ADD COLUMN auth_secret_hash TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "cpu_limit_millis", `ALTER TABLE sandboxes ADD COLUMN cpu_limit_millis INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "profile", `ALTER TABLE sandboxes ADD COLUMN profile TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "feature_set", `ALTER TABLE sandboxes ADD COLUMN feature_set TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "capability_set", `ALTER TABLE sandboxes ADD COLUMN capability_set TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "control_mode", `ALTER TABLE sandboxes ADD COLUMN control_mode TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "control_protocol_version", `ALTER TABLE sandboxes ADD COLUMN control_protocol_version TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "workspace_contract_version", `ALTER TABLE sandboxes ADD COLUMN workspace_contract_version TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "sandboxes", "image_contract_version", `ALTER TABLE sandboxes ADD COLUMN image_contract_version TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "quotas", "max_cpu_millis", `ALTER TABLE quotas ADD COLUMN max_cpu_millis INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "snapshots", "profile", `ALTER TABLE snapshots ADD COLUMN profile TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "snapshots", "image_contract_version", `ALTER TABLE snapshots ADD COLUMN image_contract_version TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "snapshots", "control_protocol_version", `ALTER TABLE snapshots ADD COLUMN control_protocol_version TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sandboxes SET cpu_limit_millis = cpu_limit * 1000 WHERE cpu_limit_millis = 0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quotas SET max_cpu_millis = max_cpu_cores * 1000 WHERE max_cpu_millis = 0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO schema_migrations(version, applied_at) VALUES (?, ?)`, schemaVersion, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureColumn(ctx context.Context, tx *sql.Tx, table, column, alterSQL string) error {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, alterSQL)
	return err
}
````

## File: cmd/sandboxctl/main.go
````go
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
````

## File: cmd/sandboxd/main.go
````go
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"or3-sandbox/internal/api"
	"or3-sandbox/internal/auth"
	"or3-sandbox/internal/config"
	"or3-sandbox/internal/db"
	"or3-sandbox/internal/logging"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
	runtimedocker "or3-sandbox/internal/runtime/docker"
	runtimeqemu "or3-sandbox/internal/runtime/qemu"
	"or3-sandbox/internal/service"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		panic(err)
	}
	rootLog := logging.New()
	log := rootLog.With("component", "daemon")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sqlDB, err := db.Open(ctx, cfg.DatabasePath)
	if err != nil {
		log.Error("database open failed", "event", "database.open", "error", err)
		os.Exit(1)
	}
	defer sqlDB.Close()

	store := repository.New(sqlDB)
	if err := store.SeedTenants(ctx, cfg.Tenants, cfg.DefaultQuota); err != nil {
		log.Error("tenant seed failed", "event", "tenant.seed", "error", err)
		os.Exit(1)
	}

	runtime, err := buildRuntime(cfg)
	if err != nil {
		log.Error("runtime configure failed", "event", "runtime.configure", "runtime", cfg.RuntimeBackend, "error", err)
		os.Exit(1)
	}
	svc := service.New(cfg, store, runtime, rootLog.With("component", "service"))
	if err := svc.Reconcile(ctx); err != nil {
		log.Error("initial reconcile failed", "event", "reconcile.initial", "error", err)
	}

	go reconcileLoop(ctx, log, svc, cfg.ReconcileInterval)

	handler := auth.New(store, cfg, rootLog.With("component", "auth")).Wrap(api.New(rootLog.With("component", "api"), svc, cfg))
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Info("daemon listening", "event", "daemon.listen", "addr", cfg.ListenAddress, "runtime", cfg.RuntimeBackend, "mode", cfg.DeploymentMode, "auth_mode", cfg.AuthMode, "tls_enabled", cfg.TLSCertPath != "", "trusted_proxy", cfg.TrustedProxyHeaders)
		var err error
		if cfg.TLSCertPath != "" {
			err = server.ListenAndServeTLS(cfg.TLSCertPath, cfg.TLSKeyPath)
		} else {
			err = server.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "event", "daemon.serve", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.GracefulShutdown)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown failed", "event", "daemon.shutdown", "error", err)
	}
}

func buildRuntime(cfg config.Config) (model.RuntimeManager, error) {
	switch cfg.RuntimeBackend {
	case "docker":
		return runtimedocker.New(), nil
	case "qemu":
		return runtimeqemu.New(runtimeqemu.Options{
			Binary:         cfg.QEMUBinary,
			Accel:          cfg.QEMUAccel,
			BaseImagePath:  cfg.QEMUBaseImagePath,
			ControlMode:    cfg.QEMUControlMode,
			SSHUser:        cfg.QEMUSSHUser,
			SSHKeyPath:     cfg.QEMUSSHPrivateKeyPath,
			SSHHostKeyPath: cfg.QEMUSSHHostKeyPath,
			BootTimeout:    cfg.QEMUBootTimeout,
		})
	default:
		return nil, errors.New("unsupported runtime backend")
	}
}

func reconcileLoop(ctx context.Context, log *slog.Logger, svc *service.Service, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := svc.Reconcile(ctx); err != nil {
				log.Error("reconcile failed", "event", "reconcile.tick", "error", err)
			}
		}
	}
}
````

## File: README.md
````markdown
# or3-sandbox

Single-node sandbox control plane for durable tenant environments.

Current status:

- shipped today: trusted Docker-backed control plane for development or internal trusted use
- shipped today: guest-backed `qemu` runtime for the intended higher-isolation path, with real host validation still required before calling a deployment production-ready

Runtime rule of thumb:

- use `docker` when cost and density matter more than isolation and the workload is trusted
- use `qemu` when isolation strength matters more than density and you have validated the guest image, suspend/resume behavior, and recovery drills on your hosts

The current Docker backend is not the hostile multi-tenant production boundary described by the architecture docs.

The repository ships:

- `sandboxd`: Go HTTP daemon with SQLite metadata, static-token or JWT tenancy, quotas, lifecycle orchestration, file APIs, exec streaming, PTY attach, tunnels, snapshots, and restart reconciliation
- `sandboxctl`: CLI for lifecycle, exec, TTY, file transfer, and tunnel management
- Docker-backed runtime implementation for durable per-sandbox environments with isolated networks and persistent workspace mounts in trusted or development mode
- QEMU-backed runtime implementation with booting, suspended, degraded, and failed guest visibility, plus opt-in host-prepared guest verification
- integration tests that exercise the main control-plane flows, with opt-in host-prepared QEMU verification for the real guest path

See also:

- `planning/whats_left.md`
- `planning/tasks2.md`
- `planning/onwards/requirements.md`
- `planning/onwards/design.md`
- `planning/onwards/tasks.md`
- `planning/onwards/status_matrix.md`
- `docs/README.md`
- `docs/operations/README.md`

## Documentation

For a beginner-friendly walkthrough of the project, see:

- `docs/README.md`
- `docs/setup.md`
- `docs/usage.md`
- `docs/tutorials/first-sandbox.md`

## Quick Start

Requirements for the shipped trusted Docker path:

- Go 1.26+
- Docker

Run the daemon:

```bash
go run ./cmd/sandboxd \
  -listen :8080 \
  -db ./data/sandbox.db \
  -storage-root ./data/storage \
  -snapshot-root ./data/snapshots
```

Use the CLI:

```bash
export SANDBOX_API=http://127.0.0.1:8080
export SANDBOX_TOKEN=dev-token

go run ./cmd/sandboxctl create --image alpine:3.20 --start
go run ./cmd/sandboxctl list
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc 'echo hello > /workspace/hello.txt && cat /workspace/hello.txt'
go run ./cmd/sandboxctl tty <sandbox-id>
```

## Default Auth

Development mode seeds bearer tokens from `SANDBOX_TOKENS`.

Default:

- token: `dev-token`
- tenant: `tenant-dev`

Format:

```text
SANDBOX_TOKENS=token-a=tenant-a,token-b=tenant-b
```

For production mode, use JWT auth instead:

```bash
export SANDBOX_MODE=production
export SANDBOX_AUTH_MODE=jwt-hs256
export SANDBOX_AUTH_JWT_ISSUER=https://issuer.example
export SANDBOX_AUTH_JWT_AUDIENCE=sandbox-api
export SANDBOX_AUTH_JWT_SECRET_PATHS=/run/secrets/or3-jwt-hmac
export SANDBOX_TUNNEL_SIGNING_KEY_PATH=/run/secrets/or3-tunnel-signing-key
```

For browser-facing tunnel flows behind rolling restarts or multiple replicas, configure a shared tunnel signing secret so signed URLs and bootstrap cookies validate consistently across instances.

## Runtime Notes

- Each sandbox maps to a durable Docker container with a persistent `/workspace` mount.
- `internet-enabled` sandboxes receive a dedicated Docker bridge network.
- `internet-disabled` sandboxes run with Docker `--network none`.
- Tunnels are explicit daemon-managed proxy endpoints; containers do not publish host ports directly.
- Snapshots combine a committed container image with a workspace tarball.
- The daemon requires `SANDBOX_TRUSTED_DOCKER_RUNTIME=true` when `SANDBOX_RUNTIME=docker` because Docker is treated as a shared-kernel trusted mode, not a production hostile multi-tenant boundary.
- Policy guardrails can restrict allowed base images, public tunnels, maximum sandbox lifetime, and idle time.
- `GET /v1/runtime/capacity` and `GET /metrics` expose production-oriented capacity and pressure views for operators.
- Use `sandboxctl inspect <sandbox-id>` or `sandboxctl runtime-health` to confirm whether a sandbox is running on `docker` or `qemu`.

## Production Roadmap Notes

The active next-step design work is focused on:

- enterprise identity, authorization, TLS, and policy hardening around the shipped `qemu` production boundary
- stronger workload verification, failure drills, and operator runbooks
- resource enforcement, observability, and backup or recovery confidence for production deployments

## Tests

```bash
./scripts/production-smoke.sh
```

For host-prepared QEMU verification, backup or restore procedures, and incident drills, use the operator docs under `docs/operations/`.

Production-facing deployment language should be gated on the smoke path above plus the documented drills in `docs/operations/verification.md`.
````

## File: internal/config/config.go
````go
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"or3-sandbox/internal/model"
)

type TenantConfig struct {
	ID    string
	Name  string
	Token string
}

type Config struct {
	DeploymentMode             string
	ListenAddress              string
	DatabasePath               string
	StorageRoot                string
	SnapshotRoot               string
	BaseImageRef               string
	RuntimeBackend             string
	AuthMode                   string
	AuthJWTIssuer              string
	AuthJWTAudience            string
	AuthJWTSecretPaths         []string
	TLSCertPath                string
	TLSKeyPath                 string
	TrustedProxyHeaders        bool
	TrustedDockerRuntime       bool
	PolicyAllowedImages        []string
	PolicyAllowPublicTunnels   bool
	PolicyMaxSandboxLifetime   time.Duration
	PolicyMaxIdleTimeout       time.Duration
	DefaultCPULimit            model.CPUQuantity
	DefaultMemoryLimitMB       int
	DefaultPIDsLimit           int
	DefaultDiskLimitMB         int
	DefaultNetworkMode         model.NetworkMode
	DefaultAllowTunnels        bool
	RequestRatePerMinute       int
	RequestBurst               int
	DefaultQuota               model.TenantQuota
	GracefulShutdown           time.Duration
	ReconcileInterval          time.Duration
	CleanupInterval            time.Duration
	OperatorHost               string
	TunnelSigningKey           string
	TunnelSigningKeyPath       string
	Tenants                    []TenantConfig
	OptionalSnapshotExport     string
	QEMUBinary                 string
	QEMUAccel                  string
	QEMUBaseImagePath          string
	QEMUAllowedBaseImagePaths  []string
	QEMUControlMode            model.GuestControlMode
	QEMUAllowedProfiles        []model.GuestProfile
	QEMUDangerousProfiles      []model.GuestProfile
	QEMUAllowDangerousProfiles bool
	QEMUAllowSSHCompat         bool
	QEMUSSHUser                string
	QEMUSSHPrivateKeyPath      string
	QEMUSSHHostKeyPath         string
	QEMUBootTimeout            time.Duration
}

func Load(args []string) (Config, error) {
	fs := flag.NewFlagSet("sandboxd", flag.ContinueOnError)
	cfg := Config{}
	fs.StringVar(&cfg.DeploymentMode, "mode", env("SANDBOX_MODE", "development"), "deployment mode")
	fs.StringVar(&cfg.ListenAddress, "listen", env("SANDBOX_LISTEN", ":8080"), "HTTP listen address")
	fs.StringVar(&cfg.DatabasePath, "db", env("SANDBOX_DB_PATH", "./data/sandbox.db"), "SQLite path")
	fs.StringVar(&cfg.StorageRoot, "storage-root", env("SANDBOX_STORAGE_ROOT", "./data/storage"), "storage root")
	fs.StringVar(&cfg.SnapshotRoot, "snapshot-root", env("SANDBOX_SNAPSHOT_ROOT", "./data/snapshots"), "snapshot root")
	fs.StringVar(&cfg.BaseImageRef, "base-image", env("SANDBOX_BASE_IMAGE", "mcr.microsoft.com/playwright:v1.51.1-noble"), "default base image")
	fs.StringVar(&cfg.RuntimeBackend, "runtime", env("SANDBOX_RUNTIME", "docker"), "runtime backend")
	fs.StringVar(&cfg.AuthMode, "auth-mode", env("SANDBOX_AUTH_MODE", "static"), "auth mode")
	fs.StringVar(&cfg.AuthJWTIssuer, "auth-jwt-issuer", env("SANDBOX_AUTH_JWT_ISSUER", ""), "jwt issuer")
	fs.StringVar(&cfg.AuthJWTAudience, "auth-jwt-audience", env("SANDBOX_AUTH_JWT_AUDIENCE", ""), "jwt audience")
	authJWTSecretPaths := env("SANDBOX_AUTH_JWT_SECRET_PATHS", "")
	fs.StringVar(&authJWTSecretPaths, "auth-jwt-secret-paths", authJWTSecretPaths, "comma-separated jwt secret file paths")
	fs.StringVar(&cfg.TLSCertPath, "tls-cert", env("SANDBOX_TLS_CERT_PATH", ""), "tls certificate path")
	fs.StringVar(&cfg.TLSKeyPath, "tls-key", env("SANDBOX_TLS_KEY_PATH", ""), "tls private key path")
	policyAllowedImages := env("SANDBOX_POLICY_ALLOWED_IMAGES", "")
	fs.StringVar(&policyAllowedImages, "policy-allowed-images", policyAllowedImages, "comma-separated allowed image references or prefixes ending with *")
	policyAllowPublicTunnels := strings.EqualFold(env("SANDBOX_POLICY_ALLOW_PUBLIC_TUNNELS", "false"), "true")
	fs.BoolVar(&policyAllowPublicTunnels, "policy-allow-public-tunnels", policyAllowPublicTunnels, "allow public tunnels")
	fs.DurationVar(&cfg.PolicyMaxSandboxLifetime, "policy-max-sandbox-lifetime", envDuration("SANDBOX_POLICY_MAX_SANDBOX_LIFETIME", 0), "maximum sandbox lifetime before policy denial; 0 disables")
	fs.DurationVar(&cfg.PolicyMaxIdleTimeout, "policy-max-idle-timeout", envDuration("SANDBOX_POLICY_MAX_IDLE_TIMEOUT", 0), "maximum sandbox idle time before policy denial; 0 disables")
	fs.StringVar(&cfg.QEMUBinary, "qemu-binary", env("SANDBOX_QEMU_BINARY", defaultQEMUBinary()), "qemu system binary")
	fs.StringVar(&cfg.QEMUAccel, "qemu-accel", env("SANDBOX_QEMU_ACCEL", "auto"), "qemu accelerator selection")
	fs.StringVar(&cfg.QEMUBaseImagePath, "qemu-base-image-path", env("SANDBOX_QEMU_BASE_IMAGE_PATH", ""), "qemu base guest image path")
	qemuAllowedBaseImagePaths := env("SANDBOX_QEMU_ALLOWED_BASE_IMAGE_PATHS", "")
	fs.StringVar(&qemuAllowedBaseImagePaths, "qemu-allowed-base-image-paths", qemuAllowedBaseImagePaths, "comma-separated qemu guest image paths tenants may request")
	qemuAllowedProfiles := env("SANDBOX_QEMU_ALLOWED_PROFILES", "core,runtime,browser,container")
	fs.StringVar(&qemuAllowedProfiles, "qemu-allowed-profiles", qemuAllowedProfiles, "comma-separated qemu guest profiles allowed for sandbox creation")
	qemuDangerousProfiles := env("SANDBOX_QEMU_DANGEROUS_PROFILES", "container,debug")
	fs.StringVar(&qemuDangerousProfiles, "qemu-dangerous-profiles", qemuDangerousProfiles, "comma-separated qemu guest profiles treated as dangerous and blocked unless explicitly allowed")
	qemuControlMode := env("SANDBOX_QEMU_CONTROL_MODE", string(model.GuestControlModeAgent))
	fs.StringVar(&qemuControlMode, "qemu-control-mode", qemuControlMode, "qemu control mode: agent or ssh-compat")
	qemuAllowDangerousProfiles := strings.EqualFold(env("SANDBOX_QEMU_ALLOW_DANGEROUS_PROFILES", "false"), "true")
	fs.BoolVar(&qemuAllowDangerousProfiles, "qemu-allow-dangerous-profiles", qemuAllowDangerousProfiles, "allow dangerous qemu guest profiles such as container and debug")
	qemuAllowSSHCompat := strings.EqualFold(env("SANDBOX_QEMU_ALLOW_SSH_COMPAT", "false"), "true")
	fs.BoolVar(&qemuAllowSSHCompat, "qemu-allow-ssh-compat", qemuAllowSSHCompat, "allow ssh-compat qemu image contracts in production validation and policy")
	fs.StringVar(&cfg.QEMUSSHUser, "qemu-ssh-user", env("SANDBOX_QEMU_SSH_USER", ""), "qemu guest ssh user")
	fs.StringVar(&cfg.QEMUSSHPrivateKeyPath, "qemu-ssh-private-key", env("SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH", ""), "qemu guest ssh private key path")
	fs.StringVar(&cfg.QEMUSSHHostKeyPath, "qemu-ssh-host-key", env("SANDBOX_QEMU_SSH_HOST_KEY_PATH", ""), "qemu guest ssh host public key path")
	trustedDockerRuntime := env("SANDBOX_TRUSTED_DOCKER_RUNTIME", "false")
	trustedProxyHeaders := strings.EqualFold(env("SANDBOX_TRUST_PROXY_HEADERS", "false"), "true")
	fs.BoolVar(&trustedProxyHeaders, "trust-proxy-headers", trustedProxyHeaders, "trust reverse-proxy tls headers")
	defaultCPU := env("SANDBOX_DEFAULT_CPU", "2")
	fs.StringVar(&defaultCPU, "default-cpu", defaultCPU, "default cpu limit")
	fs.IntVar(&cfg.DefaultMemoryLimitMB, "default-memory-mb", envInt("SANDBOX_DEFAULT_MEMORY_MB", 2048), "default memory limit")
	fs.IntVar(&cfg.DefaultPIDsLimit, "default-pids", envInt("SANDBOX_DEFAULT_PIDS", 512), "default pids limit")
	fs.IntVar(&cfg.DefaultDiskLimitMB, "default-disk-mb", envInt("SANDBOX_DEFAULT_DISK_MB", 10240), "default disk limit")
	fs.IntVar(&cfg.RequestRatePerMinute, "rate-limit", envInt("SANDBOX_RATE_LIMIT_PER_MIN", 120), "per-tenant requests per minute")
	fs.IntVar(&cfg.RequestBurst, "rate-burst", envInt("SANDBOX_RATE_LIMIT_BURST", 30), "per-tenant burst")
	fs.DurationVar(&cfg.GracefulShutdown, "shutdown-timeout", envDuration("SANDBOX_SHUTDOWN_TIMEOUT", 15*time.Second), "graceful shutdown timeout")
	fs.DurationVar(&cfg.ReconcileInterval, "reconcile-interval", envDuration("SANDBOX_RECONCILE_INTERVAL", 30*time.Second), "reconcile interval")
	fs.DurationVar(&cfg.CleanupInterval, "cleanup-interval", envDuration("SANDBOX_CLEANUP_INTERVAL", 5*time.Minute), "cleanup interval")
	fs.DurationVar(&cfg.QEMUBootTimeout, "qemu-boot-timeout", envDuration("SANDBOX_QEMU_BOOT_TIMEOUT", 2*time.Minute), "qemu guest boot timeout")
	fs.StringVar(&cfg.OperatorHost, "operator-host", env("SANDBOX_OPERATOR_HOST", "http://127.0.0.1:8080"), "public control plane host")
	fs.StringVar(&cfg.TunnelSigningKey, "tunnel-signing-key", env("SANDBOX_TUNNEL_SIGNING_KEY", ""), "shared secret for tunnel signed URLs and browser bootstrap cookies")
	fs.StringVar(&cfg.TunnelSigningKeyPath, "tunnel-signing-key-path", env("SANDBOX_TUNNEL_SIGNING_KEY_PATH", ""), "path to shared secret for tunnel signed URLs and browser bootstrap cookies")
	networkMode := env("SANDBOX_DEFAULT_NETWORK_MODE", string(model.NetworkModeInternetEnabled))
	allowTunnels := env("SANDBOX_DEFAULT_ALLOW_TUNNELS", "true")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	defaultCPULimit, err := model.ParseCPUQuantity(defaultCPU)
	if err != nil {
		return Config{}, fmt.Errorf("parse default cpu: %w", err)
	}
	cfg.DefaultCPULimit = defaultCPULimit
	maxCPUCores, err := model.ParseCPUQuantity(env("SANDBOX_QUOTA_MAX_CPU", "16"))
	if err != nil {
		return Config{}, fmt.Errorf("parse max cpu quota: %w", err)
	}
	cfg.DefaultNetworkMode = model.NetworkMode(networkMode)
	cfg.DefaultAllowTunnels = strings.EqualFold(allowTunnels, "true")
	cfg.TrustedDockerRuntime = strings.EqualFold(trustedDockerRuntime, "true")
	cfg.TrustedProxyHeaders = trustedProxyHeaders
	cfg.AuthJWTSecretPaths = parseCommaSeparated(authJWTSecretPaths)
	cfg.PolicyAllowedImages = parseCommaSeparated(policyAllowedImages)
	cfg.PolicyAllowPublicTunnels = policyAllowPublicTunnels
	cfg.OptionalSnapshotExport = env("SANDBOX_S3_EXPORT_URI", "")
	cfg.QEMUAllowedBaseImagePaths = parseCommaSeparated(qemuAllowedBaseImagePaths)
	cfg.QEMUControlMode = model.GuestControlMode(strings.ToLower(strings.TrimSpace(qemuControlMode)))
	cfg.QEMUAllowedProfiles = parseGuestProfiles(qemuAllowedProfiles)
	cfg.QEMUDangerousProfiles = parseGuestProfiles(qemuDangerousProfiles)
	cfg.QEMUAllowDangerousProfiles = qemuAllowDangerousProfiles
	cfg.QEMUAllowSSHCompat = qemuAllowSSHCompat
	cfg.DefaultQuota = model.TenantQuota{
		MaxSandboxes:            envInt("SANDBOX_QUOTA_MAX_SANDBOXES", 10),
		MaxRunningSandboxes:     envInt("SANDBOX_QUOTA_MAX_RUNNING", 5),
		MaxConcurrentExecs:      envInt("SANDBOX_QUOTA_MAX_EXECS", 8),
		MaxTunnels:              envInt("SANDBOX_QUOTA_MAX_TUNNELS", 8),
		MaxCPUCores:             maxCPUCores,
		MaxMemoryMB:             envInt("SANDBOX_QUOTA_MAX_MEMORY_MB", 16384),
		MaxStorageMB:            envInt("SANDBOX_QUOTA_MAX_STORAGE_MB", 51200),
		AllowTunnels:            strings.EqualFold(env("SANDBOX_QUOTA_ALLOW_TUNNELS", "true"), "true"),
		DefaultTunnelAuthMode:   env("SANDBOX_DEFAULT_TUNNEL_AUTH", "token"),
		DefaultTunnelVisibility: env("SANDBOX_DEFAULT_TUNNEL_VISIBILITY", "private"),
	}
	cfg.Tenants = parseTenants(env("SANDBOX_TOKENS", "dev-token=tenant-dev"))
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	var problems []string
	if c.DeploymentMode != "development" && c.DeploymentMode != "production" {
		problems = append(problems, fmt.Sprintf("unsupported deployment mode %q", c.DeploymentMode))
	}
	if c.ListenAddress == "" {
		problems = append(problems, "listen address is required")
	}
	if c.DatabasePath == "" {
		problems = append(problems, "database path is required")
	}
	if c.StorageRoot == "" {
		problems = append(problems, "storage root is required")
	}
	if c.SnapshotRoot == "" {
		problems = append(problems, "snapshot root is required")
	}
	if c.BaseImageRef == "" {
		problems = append(problems, "base image reference is required")
	}
	if c.PolicyMaxSandboxLifetime < 0 {
		problems = append(problems, "policy max sandbox lifetime must be zero or positive")
	}
	if c.PolicyMaxIdleTimeout < 0 {
		problems = append(problems, "policy max idle timeout must be zero or positive")
	}
	if err := validateAuthConfig(c, requireReadableFile); err != nil {
		problems = append(problems, err.Error())
	}
	if err := validateTransportConfig(c, requireReadableFile); err != nil {
		problems = append(problems, err.Error())
	}
	if err := validateRuntimeConfig(c, defaultRuntimeValidationProbe()); err != nil {
		problems = append(problems, err.Error())
	}
	if c.DefaultCPULimit <= 0 {
		problems = append(problems, "default cpu limit must be positive")
	}
	if c.DefaultQuota.MaxCPUCores <= 0 {
		problems = append(problems, "default quota max cpu must be positive")
	}
	if c.RuntimeBackend == "qemu" && c.DefaultCPULimit.MilliValue()%1000 != 0 {
		problems = append(problems, "qemu runtime requires a whole-core default cpu limit")
	}
	if c.DefaultNetworkMode != model.NetworkModeInternetEnabled && c.DefaultNetworkMode != model.NetworkModeInternetDisabled {
		problems = append(problems, fmt.Sprintf("unsupported default network mode %q", c.DefaultNetworkMode))
	}
	if c.AuthMode == "static" && len(c.Tenants) == 0 {
		problems = append(problems, "at least one tenant token is required")
	}
	if c.DeploymentMode == "production" {
		if c.RuntimeBackend != "qemu" {
			problems = append(problems, "production mode requires SANDBOX_RUNTIME=qemu")
		}
		if c.AuthMode == "static" {
			problems = append(problems, "production mode requires SANDBOX_AUTH_MODE=jwt-hs256")
		}
		if c.TLSCertPath == "" && !c.TrustedProxyHeaders {
			problems = append(problems, "production mode requires TLS certificate paths or SANDBOX_TRUST_PROXY_HEADERS=true")
		}
	}
	for _, dir := range []string{filepath.Dir(c.DatabasePath), c.StorageRoot, c.SnapshotRoot} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			problems = append(problems, fmt.Sprintf("create %s: %v", dir, err))
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validateAuthConfig(c Config, fileReadable func(string) error) error {
	switch c.AuthMode {
	case "static":
		return nil
	case "jwt-hs256":
		var problems []string
		if strings.TrimSpace(c.AuthJWTIssuer) == "" {
			problems = append(problems, "jwt auth requires SANDBOX_AUTH_JWT_ISSUER")
		}
		if strings.TrimSpace(c.AuthJWTAudience) == "" {
			problems = append(problems, "jwt auth requires SANDBOX_AUTH_JWT_AUDIENCE")
		}
		if len(c.AuthJWTSecretPaths) == 0 {
			problems = append(problems, "jwt auth requires SANDBOX_AUTH_JWT_SECRET_PATHS")
		}
		for _, path := range c.AuthJWTSecretPaths {
			if err := fileReadable(path); err != nil {
				problems = append(problems, fmt.Sprintf("jwt auth secret path is not readable: %v", err))
			}
		}
		if len(problems) > 0 {
			return errors.New(strings.Join(problems, "; "))
		}
		return nil
	default:
		return fmt.Errorf("unsupported auth mode %q", c.AuthMode)
	}
}

func validateTransportConfig(c Config, fileReadable func(string) error) error {
	hasCert := strings.TrimSpace(c.TLSCertPath) != ""
	hasKey := strings.TrimSpace(c.TLSKeyPath) != ""
	if hasCert != hasKey {
		return errors.New("tls requires both SANDBOX_TLS_CERT_PATH and SANDBOX_TLS_KEY_PATH")
	}
	var problems []string
	if strings.TrimSpace(c.TunnelSigningKey) != "" && strings.TrimSpace(c.TunnelSigningKeyPath) != "" {
		problems = append(problems, "set only one of SANDBOX_TUNNEL_SIGNING_KEY or SANDBOX_TUNNEL_SIGNING_KEY_PATH")
	}
	if strings.TrimSpace(c.TunnelSigningKeyPath) != "" {
		if err := fileReadable(c.TunnelSigningKeyPath); err != nil {
			problems = append(problems, fmt.Sprintf("tunnel signing key path is not readable: %v", err))
		}
	}
	if hasCert {
		if err := fileReadable(c.TLSCertPath); err != nil {
			problems = append(problems, fmt.Sprintf("tls certificate path is not readable: %v", err))
		}
		if err := fileReadable(c.TLSKeyPath); err != nil {
			problems = append(problems, fmt.Sprintf("tls key path is not readable: %v", err))
		}
	}
	if c.TrustedProxyHeaders && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.OperatorHost)), "https://") {
		problems = append(problems, "trusted proxy mode requires SANDBOX_OPERATOR_HOST to use https://")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

type runtimeValidationProbe struct {
	goos          string
	commandExists func(string) error
	fileReadable  func(string) error
	kvmAvailable  func() error
	hvfAvailable  func() error
}

func defaultRuntimeValidationProbe() runtimeValidationProbe {
	return runtimeValidationProbe{
		goos:          goruntime.GOOS,
		commandExists: requireCommand,
		fileReadable:  requireReadableFile,
		kvmAvailable:  requireKVM,
		hvfAvailable:  requireHVF,
	}
}

func validateRuntimeConfig(c Config, probe runtimeValidationProbe) error {
	switch c.RuntimeBackend {
	case "docker":
		if !c.TrustedDockerRuntime {
			return errors.New("docker runtime requires SANDBOX_TRUSTED_DOCKER_RUNTIME=true because it is shared-kernel and not a production multi-tenant boundary")
		}
		return nil
	case "qemu":
		return validateQEMUConfig(c, probe)
	default:
		return fmt.Errorf("unsupported runtime backend %q", c.RuntimeBackend)
	}
}

func validateQEMUConfig(c Config, probe runtimeValidationProbe) error {
	accel, err := resolveQEMUAccel(c.QEMUAccel, probe.goos)
	if err != nil {
		return err
	}
	if c.QEMUBootTimeout <= 0 {
		return errors.New("qemu runtime requires SANDBOX_QEMU_BOOT_TIMEOUT to be a positive duration")
	}
	if strings.TrimSpace(c.QEMUBaseImagePath) == "" {
		return errors.New("qemu runtime requires SANDBOX_QEMU_BASE_IMAGE_PATH")
	}
	if !c.QEMUControlMode.IsValid() {
		return fmt.Errorf("qemu runtime requires SANDBOX_QEMU_CONTROL_MODE to be one of %q or %q", model.GuestControlModeAgent, model.GuestControlModeSSHCompat)
	}
	if len(c.QEMUAllowedProfiles) == 0 {
		return errors.New("qemu runtime requires at least one allowed guest profile")
	}
	if c.QEMUControlMode == model.GuestControlModeSSHCompat {
		if strings.TrimSpace(c.QEMUSSHUser) == "" {
			return errors.New("qemu ssh-compat mode requires SANDBOX_QEMU_SSH_USER")
		}
		if strings.TrimSpace(c.QEMUSSHPrivateKeyPath) == "" {
			return errors.New("qemu ssh-compat mode requires SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH")
		}
		if strings.TrimSpace(c.QEMUSSHHostKeyPath) == "" {
			return errors.New("qemu ssh-compat mode requires SANDBOX_QEMU_SSH_HOST_KEY_PATH")
		}
	}
	if err := probe.commandExists(c.QEMUBinary); err != nil {
		return fmt.Errorf("qemu runtime requires a working QEMU binary: %w", err)
	}
	if err := probe.fileReadable(c.QEMUBaseImagePath); err != nil {
		return fmt.Errorf("qemu runtime base image path is not readable: %w", err)
	}
	if c.QEMUControlMode == model.GuestControlModeSSHCompat {
		if err := probe.fileReadable(c.QEMUSSHPrivateKeyPath); err != nil {
			return fmt.Errorf("qemu runtime ssh private key is not readable: %w", err)
		}
		if err := probe.fileReadable(c.QEMUSSHHostKeyPath); err != nil {
			return fmt.Errorf("qemu runtime ssh host public key is not readable: %w", err)
		}
	}
	for _, path := range c.EffectiveQEMUAllowedBaseImagePaths() {
		if err := probe.fileReadable(path); err != nil {
			return fmt.Errorf("qemu allowed base image path is not readable: %w", err)
		}
	}
	switch accel {
	case "kvm":
		if err := probe.kvmAvailable(); err != nil {
			return fmt.Errorf("qemu runtime requires KVM support on Linux hosts: %w", err)
		}
	case "hvf":
		if err := probe.hvfAvailable(); err != nil {
			return fmt.Errorf("qemu runtime requires HVF support on macOS hosts: %w", err)
		}
	}
	if c.DeploymentMode == "production" && c.QEMUControlMode == model.GuestControlModeSSHCompat && !c.QEMUAllowSSHCompat {
		return errors.New("production qemu mode rejects ssh-compat images unless SANDBOX_QEMU_ALLOW_SSH_COMPAT=true")
	}
	return nil
}

func (c Config) EffectiveQEMUAllowedBaseImagePaths() []string {
	return appendDefaultQEMUBaseImagePath(c.QEMUAllowedBaseImagePaths, c.QEMUBaseImagePath)
}

func appendDefaultQEMUBaseImagePath(paths []string, defaultPath string) []string {
	seen := make(map[string]struct{}, len(paths)+1)
	var result []string
	for _, raw := range append(paths, defaultPath) {
		normalized := NormalizeQEMUBaseImagePath(raw)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func NormalizeQEMUBaseImagePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
}

func (c Config) IsAllowedQEMUProfile(profile model.GuestProfile) bool {
	for _, allowed := range c.QEMUAllowedProfiles {
		if allowed == profile {
			return true
		}
	}
	return false
}

func (c Config) IsDangerousQEMUProfile(profile model.GuestProfile) bool {
	for _, dangerous := range c.QEMUDangerousProfiles {
		if dangerous == profile {
			return true
		}
	}
	return false
}

func parseGuestProfiles(raw string) []model.GuestProfile {
	entries := parseCommaSeparated(raw)
	seen := make(map[model.GuestProfile]struct{}, len(entries))
	result := make([]model.GuestProfile, 0, len(entries))
	for _, entry := range entries {
		profile := model.GuestProfile(strings.ToLower(strings.TrimSpace(entry)))
		if !profile.IsValid() {
			continue
		}
		if _, ok := seen[profile]; ok {
			continue
		}
		seen[profile] = struct{}{}
		result = append(result, profile)
	}
	return result
}

func resolveQEMUAccel(value, goos string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		switch goos {
		case "linux":
			return "kvm", nil
		case "darwin":
			return "hvf", nil
		default:
			return "", fmt.Errorf("qemu runtime is unsupported on host OS %q", goos)
		}
	case "kvm":
		if goos != "linux" {
			return "", fmt.Errorf("qemu accel %q is unsupported on host OS %q", value, goos)
		}
		return "kvm", nil
	case "hvf":
		if goos != "darwin" {
			return "", fmt.Errorf("qemu accel %q is unsupported on host OS %q", value, goos)
		}
		return "hvf", nil
	default:
		return "", fmt.Errorf("unsupported qemu accelerator %q", value)
	}
}

func defaultQEMUBinary() string {
	switch goruntime.GOARCH {
	case "arm64":
		return "qemu-system-aarch64"
	default:
		return "qemu-system-x86_64"
	}
}

func requireCommand(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("command path is empty")
	}
	if filepath.IsAbs(name) || strings.ContainsRune(name, os.PathSeparator) {
		return requireReadableFile(name)
	}
	_, err := exec.LookPath(name)
	return err
}

func requireReadableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func requireKVM() error {
	return requireReadableFile("/dev/kvm")
}

func requireHVF() error {
	output, err := exec.Command("sysctl", "-n", "kern.hv_support").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	if strings.TrimSpace(string(output)) != "1" {
		return fmt.Errorf("kern.hv_support=%s", strings.TrimSpace(string(output)))
	}
	return nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.Atoi(value)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		parsed, err := time.ParseDuration(value)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func parseTenants(raw string) []TenantConfig {
	var tenants []TenantConfig
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, "=")
		if len(parts) != 2 {
			continue
		}
		token := strings.TrimSpace(parts[0])
		tenantID := strings.TrimSpace(parts[1])
		if token == "" || tenantID == "" {
			continue
		}
		tenants = append(tenants, TenantConfig{
			ID:    tenantID,
			Name:  tenantID,
			Token: token,
		})
	}
	return tenants
}

func parseCommaSeparated(raw string) []string {
	var values []string
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		values = append(values, entry)
	}
	return values
}
````

## File: internal/model/model.go
````go
package model

import "time"

type SandboxStatus string

const (
	SandboxStatusCreating   SandboxStatus = "creating"
	SandboxStatusBooting    SandboxStatus = "booting"
	SandboxStatusDegraded   SandboxStatus = "degraded"
	SandboxStatusStopped    SandboxStatus = "stopped"
	SandboxStatusStarting   SandboxStatus = "starting"
	SandboxStatusRunning    SandboxStatus = "running"
	SandboxStatusSuspending SandboxStatus = "suspending"
	SandboxStatusSuspended  SandboxStatus = "suspended"
	SandboxStatusStopping   SandboxStatus = "stopping"
	SandboxStatusDeleting   SandboxStatus = "deleting"
	SandboxStatusDeleted    SandboxStatus = "deleted"
	SandboxStatusError      SandboxStatus = "error"
)

type NetworkMode string

const (
	NetworkModeInternetEnabled  NetworkMode = "internet-enabled"
	NetworkModeInternetDisabled NetworkMode = "internet-disabled"
)

type TunnelProtocol string

const (
	TunnelProtocolHTTP TunnelProtocol = "http"
	TunnelProtocolTCP  TunnelProtocol = "tcp"
)

type SnapshotStatus string

const (
	SnapshotStatusCreating SnapshotStatus = "creating"
	SnapshotStatusReady    SnapshotStatus = "ready"
	SnapshotStatusError    SnapshotStatus = "error"
)

type ExecutionStatus string

const (
	ExecutionStatusRunning   ExecutionStatus = "running"
	ExecutionStatusDetached  ExecutionStatus = "detached"
	ExecutionStatusSucceeded ExecutionStatus = "succeeded"
	ExecutionStatusFailed    ExecutionStatus = "failed"
	ExecutionStatusTimedOut  ExecutionStatus = "timed_out"
	ExecutionStatusCanceled  ExecutionStatus = "canceled"
)

type GuestProfile string

const (
	GuestProfileCore      GuestProfile = "core"
	GuestProfileRuntime   GuestProfile = "runtime"
	GuestProfileBrowser   GuestProfile = "browser"
	GuestProfileContainer GuestProfile = "container"
	GuestProfileDebug     GuestProfile = "debug"
)

type GuestControlMode string

const (
	GuestControlModeAgent     GuestControlMode = "agent"
	GuestControlModeSSHCompat GuestControlMode = "ssh-compat"
)

func (p GuestProfile) IsValid() bool {
	switch p {
	case GuestProfileCore, GuestProfileRuntime, GuestProfileBrowser, GuestProfileContainer, GuestProfileDebug:
		return true
	default:
		return false
	}
}

func (m GuestControlMode) IsValid() bool {
	switch m {
	case GuestControlModeAgent, GuestControlModeSSHCompat:
		return true
	default:
		return false
	}
}

type Sandbox struct {
	ID                       string           `json:"id"`
	TenantID                 string           `json:"tenant_id"`
	Status                   SandboxStatus    `json:"status"`
	RuntimeBackend           string           `json:"runtime_backend"`
	BaseImageRef             string           `json:"base_image_ref"`
	Profile                  GuestProfile     `json:"profile,omitempty"`
	Features                 []string         `json:"features,omitempty"`
	Capabilities             []string         `json:"capabilities,omitempty"`
	ControlMode              GuestControlMode `json:"control_mode,omitempty"`
	ControlProtocolVersion   string           `json:"control_protocol_version,omitempty"`
	WorkspaceContractVersion string           `json:"workspace_contract_version,omitempty"`
	ImageContractVersion     string           `json:"image_contract_version,omitempty"`
	CPULimit                 CPUQuantity      `json:"cpu_limit"`
	MemoryLimitMB            int              `json:"memory_limit_mb"`
	PIDsLimit                int              `json:"pids_limit"`
	DiskLimitMB              int              `json:"disk_limit_mb"`
	NetworkMode              NetworkMode      `json:"network_mode"`
	AllowTunnels             bool             `json:"allow_tunnels"`
	StorageRoot              string           `json:"-"`
	WorkspaceRoot            string           `json:"-"`
	CacheRoot                string           `json:"-"`
	RuntimeID                string           `json:"runtime_id"`
	RuntimeStatus            string           `json:"runtime_status"`
	LastRuntimeError         string           `json:"last_runtime_error,omitempty"`
	CreatedAt                time.Time        `json:"created_at"`
	UpdatedAt                time.Time        `json:"updated_at"`
	LastActiveAt             time.Time        `json:"last_active_at"`
	DeletedAt                *time.Time       `json:"deleted_at,omitempty"`
}

type CreateSandboxRequest struct {
	BaseImageRef  string       `json:"base_image_ref"`
	Profile       GuestProfile `json:"profile,omitempty"`
	Features      []string     `json:"features,omitempty"`
	CPULimit      CPUQuantity  `json:"cpu_limit"`
	MemoryLimitMB int          `json:"memory_limit_mb"`
	PIDsLimit     int          `json:"pids_limit"`
	DiskLimitMB   int          `json:"disk_limit_mb"`
	NetworkMode   NetworkMode  `json:"network_mode"`
	AllowTunnels  *bool        `json:"allow_tunnels,omitempty"`
	Start         bool         `json:"start"`
}

type LifecycleRequest struct {
	Force bool `json:"force"`
}

type ExecRequest struct {
	Command  []string          `json:"command"`
	Env      map[string]string `json:"env"`
	Cwd      string            `json:"cwd"`
	Timeout  time.Duration     `json:"timeout"`
	Detached bool              `json:"detached"`
}

type Execution struct {
	ID              string          `json:"id"`
	SandboxID       string          `json:"sandbox_id"`
	TenantID        string          `json:"tenant_id"`
	Command         string          `json:"command"`
	Cwd             string          `json:"cwd"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
	Status          ExecutionStatus `json:"status"`
	ExitCode        *int            `json:"exit_code,omitempty"`
	StdoutPreview   string          `json:"stdout_preview,omitempty"`
	StderrPreview   string          `json:"stderr_preview,omitempty"`
	StdoutTruncated bool            `json:"stdout_truncated"`
	StderrTruncated bool            `json:"stderr_truncated"`
	StartedAt       time.Time       `json:"started_at"`
	CompletedAt     *time.Time      `json:"completed_at,omitempty"`
	DurationMS      *int64          `json:"duration_ms,omitempty"`
}

type TTYRequest struct {
	Command []string          `json:"command"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	Cols    int               `json:"cols"`
	Rows    int               `json:"rows"`
}

type TTYSession struct {
	ID         string     `json:"id"`
	SandboxID  string     `json:"sandbox_id"`
	TenantID   string     `json:"tenant_id"`
	Command    string     `json:"command"`
	Connected  bool       `json:"connected"`
	CreatedAt  time.Time  `json:"created_at"`
	ClosedAt   *time.Time `json:"closed_at,omitempty"`
	LastResize string     `json:"last_resize,omitempty"`
}

type FileWriteRequest struct {
	Content       string `json:"content,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	Encoding      string `json:"encoding,omitempty"`
}

type FileReadResponse struct {
	Path          string `json:"path"`
	Content       string `json:"content,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	Size          int64  `json:"size"`
	Encoding      string `json:"encoding"`
}

type MkdirRequest struct {
	Path string `json:"path"`
}

type CreateTunnelRequest struct {
	TargetPort int            `json:"target_port"`
	Protocol   TunnelProtocol `json:"protocol"`
	AuthMode   string         `json:"auth_mode"`
	Visibility string         `json:"visibility"`
}

type CreateTunnelSignedURLRequest struct {
	Path       string `json:"path,omitempty"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type Tunnel struct {
	ID             string         `json:"id"`
	SandboxID      string         `json:"sandbox_id"`
	TenantID       string         `json:"tenant_id"`
	TargetPort     int            `json:"target_port"`
	Protocol       TunnelProtocol `json:"protocol"`
	AuthMode       string         `json:"auth_mode"`
	Visibility     string         `json:"visibility"`
	Endpoint       string         `json:"endpoint"`
	AccessToken    string         `json:"access_token,omitempty"`
	AuthSecretHash string         `json:"-"`
	CreatedAt      time.Time      `json:"created_at"`
	RevokedAt      *time.Time     `json:"revoked_at,omitempty"`
}

type TunnelSignedURL struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type CreateSnapshotRequest struct {
	Name string `json:"name"`
}

type Snapshot struct {
	ID                     string         `json:"id"`
	SandboxID              string         `json:"sandbox_id"`
	TenantID               string         `json:"tenant_id"`
	Name                   string         `json:"name"`
	Status                 SnapshotStatus `json:"status"`
	ImageRef               string         `json:"image_ref"`
	Profile                GuestProfile   `json:"profile,omitempty"`
	ImageContractVersion   string         `json:"image_contract_version,omitempty"`
	ControlProtocolVersion string         `json:"control_protocol_version,omitempty"`
	WorkspaceTar           string         `json:"-"`
	ExportLocation         string         `json:"export_location,omitempty"`
	CreatedAt              time.Time      `json:"created_at"`
	CompletedAt            *time.Time     `json:"completed_at,omitempty"`
}

type RestoreSnapshotRequest struct {
	TargetSandboxID string `json:"target_sandbox_id"`
}

type RuntimeHealth struct {
	Backend      string                 `json:"backend"`
	Healthy      bool                   `json:"healthy"`
	CheckedAt    time.Time              `json:"checked_at"`
	StatusCounts map[string]int         `json:"status_counts,omitempty"`
	Sandboxes    []RuntimeSandboxHealth `json:"sandboxes"`
}

type RuntimeInfo struct {
	Backend string `json:"backend"`
}

type RuntimeSandboxHealth struct {
	SandboxID       string        `json:"sandbox_id"`
	TenantID        string        `json:"tenant_id"`
	PersistedStatus SandboxStatus `json:"persisted_status"`
	ObservedStatus  SandboxStatus `json:"observed_status"`
	RuntimeID       string        `json:"runtime_id"`
	RuntimeStatus   string        `json:"runtime_status"`
	Pid             int           `json:"pid"`
	IPAddress       string        `json:"ip_address,omitempty"`
	Error           string        `json:"error,omitempty"`
}

type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

type TenantQuota struct {
	TenantID                string      `json:"tenant_id"`
	MaxSandboxes            int         `json:"max_sandboxes"`
	MaxRunningSandboxes     int         `json:"max_running_sandboxes"`
	MaxConcurrentExecs      int         `json:"max_concurrent_execs"`
	MaxTunnels              int         `json:"max_tunnels"`
	MaxCPUCores             CPUQuantity `json:"max_cpu_cores"`
	MaxMemoryMB             int         `json:"max_memory_mb"`
	MaxStorageMB            int         `json:"max_storage_mb"`
	AllowTunnels            bool        `json:"allow_tunnels"`
	DefaultTunnelAuthMode   string      `json:"default_tunnel_auth_mode"`
	DefaultTunnelVisibility string      `json:"default_tunnel_visibility"`
}

type AuditEvent struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	SandboxID  string    `json:"sandbox_id,omitempty"`
	Action     string    `json:"action"`
	ResourceID string    `json:"resource_id,omitempty"`
	Outcome    string    `json:"outcome"`
	Message    string    `json:"message"`
	CreatedAt  time.Time `json:"created_at"`
}
````

## File: internal/repository/store.go
````go
package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/model"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SeedTenants(ctx context.Context, tenants []config.TenantConfig, quota model.TenantQuota) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, tenant := range tenants {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO tenants(tenant_id, name, token_hash, created_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT(tenant_id) DO UPDATE SET name=excluded.name, token_hash=excluded.token_hash
			`, tenant.ID, tenant.Name, config.HashToken(tenant.Token), now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO quotas(
					tenant_id, max_sandboxes, max_running_sandboxes, max_concurrent_execs, max_tunnels,
					max_cpu_cores, max_cpu_millis, max_memory_mb, max_storage_mb, allow_tunnels,
					default_tunnel_auth_mode, default_tunnel_visibility
				)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(tenant_id) DO UPDATE SET
					max_sandboxes=excluded.max_sandboxes,
					max_running_sandboxes=excluded.max_running_sandboxes,
					max_concurrent_execs=excluded.max_concurrent_execs,
					max_tunnels=excluded.max_tunnels,
					max_cpu_cores=excluded.max_cpu_cores,
					max_cpu_millis=excluded.max_cpu_millis,
					max_memory_mb=excluded.max_memory_mb,
					max_storage_mb=excluded.max_storage_mb,
					allow_tunnels=excluded.allow_tunnels,
					default_tunnel_auth_mode=excluded.default_tunnel_auth_mode,
					default_tunnel_visibility=excluded.default_tunnel_visibility
			`, tenant.ID, quota.MaxSandboxes, quota.MaxRunningSandboxes, quota.MaxConcurrentExecs, quota.MaxTunnels, quota.MaxCPUCores.VCPUCount(), quota.MaxCPUCores.MilliValue(), quota.MaxMemoryMB, quota.MaxStorageMB, boolToInt(quota.AllowTunnels), quota.DefaultTunnelAuthMode, quota.DefaultTunnelVisibility); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) EnsureTenantQuota(ctx context.Context, tenant model.Tenant, quota model.TenantQuota, tokenHash string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		name := tenant.Name
		if name == "" {
			name = tenant.ID
		}
		if tokenHash == "" {
			tokenHash = config.HashToken("jwt:" + tenant.ID)
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tenants(tenant_id, name, token_hash, created_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(tenant_id) DO UPDATE SET name=excluded.name
		`, tenant.ID, name, tokenHash, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO quotas(
				tenant_id, max_sandboxes, max_running_sandboxes, max_concurrent_execs, max_tunnels,
				max_cpu_cores, max_cpu_millis, max_memory_mb, max_storage_mb, allow_tunnels,
				default_tunnel_auth_mode, default_tunnel_visibility
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(tenant_id) DO NOTHING
		`, tenant.ID, quota.MaxSandboxes, quota.MaxRunningSandboxes, quota.MaxConcurrentExecs, quota.MaxTunnels, quota.MaxCPUCores.VCPUCount(), quota.MaxCPUCores.MilliValue(), quota.MaxMemoryMB, quota.MaxStorageMB, boolToInt(quota.AllowTunnels), quota.DefaultTunnelAuthMode, quota.DefaultTunnelVisibility); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) AuthenticateTenant(ctx context.Context, tokenHash string) (model.Tenant, model.TenantQuota, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT t.tenant_id, t.name, t.token_hash, t.created_at,
		       q.max_sandboxes, q.max_running_sandboxes, q.max_concurrent_execs, q.max_tunnels,
		       q.max_cpu_millis, q.max_memory_mb, q.max_storage_mb, q.allow_tunnels,
		       q.default_tunnel_auth_mode, q.default_tunnel_visibility
		FROM tenants t
		JOIN quotas q ON q.tenant_id = t.tenant_id
		WHERE t.token_hash = ?
	`, tokenHash)
	var tenant model.Tenant
	var quota model.TenantQuota
	var created string
	var allowTunnels int
	var maxCPUMillis int64
	if err := row.Scan(
		&tenant.ID, &tenant.Name, &tenant.TokenHash, &created,
		&quota.MaxSandboxes, &quota.MaxRunningSandboxes, &quota.MaxConcurrentExecs, &quota.MaxTunnels,
		&maxCPUMillis, &quota.MaxMemoryMB, &quota.MaxStorageMB, &allowTunnels,
		&quota.DefaultTunnelAuthMode, &quota.DefaultTunnelVisibility,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Tenant{}, model.TenantQuota{}, ErrNotFound
		}
		return model.Tenant{}, model.TenantQuota{}, err
	}
	parsedCreatedAt, err := parseTime(created)
	if err != nil {
		return model.Tenant{}, model.TenantQuota{}, err
	}
	tenant.CreatedAt = parsedCreatedAt
	quota.TenantID = tenant.ID
	quota.MaxCPUCores = model.CPUQuantity(maxCPUMillis)
	quota.AllowTunnels = allowTunnels == 1
	return tenant, quota, nil
}

func (s *Store) GetQuota(ctx context.Context, tenantID string) (model.TenantQuota, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT max_sandboxes, max_running_sandboxes, max_concurrent_execs, max_tunnels,
		       max_cpu_millis, max_memory_mb, max_storage_mb, allow_tunnels,
		       default_tunnel_auth_mode, default_tunnel_visibility
		FROM quotas
		WHERE tenant_id = ?
	`, tenantID)
	var quota model.TenantQuota
	var allowTunnels int
	var maxCPUMillis int64
	if err := row.Scan(
		&quota.MaxSandboxes, &quota.MaxRunningSandboxes, &quota.MaxConcurrentExecs, &quota.MaxTunnels,
		&maxCPUMillis, &quota.MaxMemoryMB, &quota.MaxStorageMB, &allowTunnels,
		&quota.DefaultTunnelAuthMode, &quota.DefaultTunnelVisibility,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.TenantQuota{}, ErrNotFound
		}
		return model.TenantQuota{}, err
	}
	quota.TenantID = tenantID
	quota.MaxCPUCores = model.CPUQuantity(maxCPUMillis)
	quota.AllowTunnels = allowTunnels == 1
	return quota, nil
}

func (s *Store) CreateSandbox(ctx context.Context, sandbox model.Sandbox) error {
	now := sandbox.CreatedAt.UTC().Format(time.RFC3339Nano)
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sandboxes(
				sandbox_id, tenant_id, status, runtime_backend, base_image_ref,
				profile, feature_set, capability_set, control_mode, control_protocol_version, workspace_contract_version, image_contract_version,
				cpu_limit, cpu_limit_millis, memory_limit_mb, pids_limit, disk_limit_mb,
				network_mode, allow_tunnels, storage_root, workspace_root, cache_root,
				created_at, updated_at, last_active_at, deleted_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
		`, sandbox.ID, sandbox.TenantID, string(sandbox.Status), sandbox.RuntimeBackend, sandbox.BaseImageRef,
			string(sandbox.Profile), joinStringList(sandbox.Features), joinStringList(sandbox.Capabilities), string(sandbox.ControlMode), sandbox.ControlProtocolVersion, sandbox.WorkspaceContractVersion, sandbox.ImageContractVersion,
			sandbox.CPULimit.VCPUCount(), sandbox.CPULimit.MilliValue(), sandbox.MemoryLimitMB, sandbox.PIDsLimit, sandbox.DiskLimitMB,
			string(sandbox.NetworkMode), boolToInt(sandbox.AllowTunnels), sandbox.StorageRoot, sandbox.WorkspaceRoot, sandbox.CacheRoot,
			now, sandbox.UpdatedAt.UTC().Format(time.RFC3339Nano), sandbox.LastActiveAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sandbox_runtime_state(sandbox_id, runtime_id, runtime_status, last_runtime_error, ip_address, pid, started_at)
			VALUES (?, ?, ?, '', '', 0, NULL)
		`, sandbox.ID, sandbox.RuntimeID, sandbox.RuntimeStatus); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sandbox_storage(sandbox_id, rootfs_bytes, workspace_bytes, cache_bytes, snapshot_bytes, updated_at)
			VALUES (?, 0, 0, 0, 0, ?)
		`, sandbox.ID, now); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) UpdateSandboxState(ctx context.Context, sandbox model.Sandbox) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		var deletedAt interface{}
		if sandbox.DeletedAt != nil {
			deletedAt = sandbox.DeletedAt.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sandboxes
			SET status=?, base_image_ref=?, profile=?, feature_set=?, capability_set=?, control_mode=?, control_protocol_version=?, workspace_contract_version=?, image_contract_version=?, cpu_limit=?, cpu_limit_millis=?, memory_limit_mb=?, pids_limit=?, disk_limit_mb=?, network_mode=?, allow_tunnels=?,
			    updated_at=?, last_active_at=?, deleted_at=?
			WHERE sandbox_id=? AND tenant_id=?
		`, string(sandbox.Status), sandbox.BaseImageRef, string(sandbox.Profile), joinStringList(sandbox.Features), joinStringList(sandbox.Capabilities), string(sandbox.ControlMode), sandbox.ControlProtocolVersion, sandbox.WorkspaceContractVersion, sandbox.ImageContractVersion, sandbox.CPULimit.VCPUCount(), sandbox.CPULimit.MilliValue(), sandbox.MemoryLimitMB, sandbox.PIDsLimit, sandbox.DiskLimitMB,
			string(sandbox.NetworkMode), boolToInt(sandbox.AllowTunnels), sandbox.UpdatedAt.UTC().Format(time.RFC3339Nano),
			sandbox.LastActiveAt.UTC().Format(time.RFC3339Nano), deletedAt, sandbox.ID, sandbox.TenantID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sandbox_runtime_state
			SET runtime_id=?, runtime_status=?, last_runtime_error=?
			WHERE sandbox_id=?
		`, sandbox.RuntimeID, sandbox.RuntimeStatus, sandbox.LastRuntimeError, sandbox.ID); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) UpdateRuntimeState(ctx context.Context, sandboxID string, state model.RuntimeState) error {
	var startedAt interface{}
	if state.StartedAt != nil {
		startedAt = state.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE sandbox_runtime_state
		SET runtime_id=?, runtime_status=?, last_runtime_error=?, ip_address=?, pid=?, started_at=?
		WHERE sandbox_id=?
	`, state.RuntimeID, string(state.Status), state.Error, state.IPAddress, state.Pid, startedAt, sandboxID)
	return err
}

func (s *Store) GetSandbox(ctx context.Context, tenantID, sandboxID string) (model.Sandbox, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT s.sandbox_id, s.tenant_id, s.status, s.runtime_backend, s.base_image_ref, s.profile, s.feature_set, s.capability_set, s.control_mode, s.control_protocol_version, s.workspace_contract_version, s.image_contract_version, s.cpu_limit_millis,
		       s.memory_limit_mb, s.pids_limit, s.disk_limit_mb, s.network_mode, s.allow_tunnels,
		       s.storage_root, s.workspace_root, s.cache_root,
		       s.created_at, s.updated_at, s.last_active_at, s.deleted_at,
		       r.runtime_id, r.runtime_status, r.last_runtime_error
		FROM sandboxes s
		JOIN sandbox_runtime_state r ON r.sandbox_id = s.sandbox_id
		WHERE s.sandbox_id = ? AND s.tenant_id = ?
	`, sandboxID, tenantID)
	sandbox, err := scanSandbox(row)
	if err != nil {
		return model.Sandbox{}, err
	}
	return sandbox, nil
}

func (s *Store) ListSandboxes(ctx context.Context, tenantID string) ([]model.Sandbox, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.sandbox_id, s.tenant_id, s.status, s.runtime_backend, s.base_image_ref, s.profile, s.feature_set, s.capability_set, s.control_mode, s.control_protocol_version, s.workspace_contract_version, s.image_contract_version, s.cpu_limit_millis,
		       s.memory_limit_mb, s.pids_limit, s.disk_limit_mb, s.network_mode, s.allow_tunnels,
		       s.storage_root, s.workspace_root, s.cache_root,
		       s.created_at, s.updated_at, s.last_active_at, s.deleted_at,
		       r.runtime_id, r.runtime_status, r.last_runtime_error
		FROM sandboxes s
		JOIN sandbox_runtime_state r ON r.sandbox_id = s.sandbox_id
		WHERE s.tenant_id = ?
		ORDER BY s.created_at
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sandboxes []model.Sandbox
	for rows.Next() {
		sandbox, err := scanSandbox(rows)
		if err != nil {
			return nil, err
		}
		sandboxes = append(sandboxes, sandbox)
	}
	return sandboxes, rows.Err()
}

func (s *Store) ListNonDeletedSandboxes(ctx context.Context) ([]model.Sandbox, error) {
	return s.listNonDeletedSandboxes(ctx, "")
}

func (s *Store) ListNonDeletedSandboxesByTenant(ctx context.Context, tenantID string) ([]model.Sandbox, error) {
	return s.listNonDeletedSandboxes(ctx, tenantID)
}

func (s *Store) listNonDeletedSandboxes(ctx context.Context, tenantID string) ([]model.Sandbox, error) {
	query := `
		SELECT s.sandbox_id, s.tenant_id, s.status, s.runtime_backend, s.base_image_ref, s.profile, s.feature_set, s.capability_set, s.control_mode, s.control_protocol_version, s.workspace_contract_version, s.image_contract_version, s.cpu_limit_millis,
		       s.memory_limit_mb, s.pids_limit, s.disk_limit_mb, s.network_mode, s.allow_tunnels,
		       s.storage_root, s.workspace_root, s.cache_root,
		       s.created_at, s.updated_at, s.last_active_at, s.deleted_at,
		       r.runtime_id, r.runtime_status, r.last_runtime_error
		FROM sandboxes s
		JOIN sandbox_runtime_state r ON r.sandbox_id = s.sandbox_id
		WHERE s.status != ?`
	args := []any{string(model.SandboxStatusDeleted)}
	if tenantID != "" {
		query += ` AND s.tenant_id = ?`
		args = append(args, tenantID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sandboxes []model.Sandbox
	for rows.Next() {
		sandbox, err := scanSandbox(rows)
		if err != nil {
			return nil, err
		}
		sandboxes = append(sandboxes, sandbox)
	}
	return sandboxes, rows.Err()
}

func (s *Store) StorageUsageUpdatedAt(ctx context.Context, sandboxID string) (time.Time, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT updated_at
		FROM sandbox_storage
		WHERE sandbox_id = ?
	`, sandboxID)
	var updated string
	if err := row.Scan(&updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, ErrNotFound
		}
		return time.Time{}, err
	}
	return parseTime(updated)
}

func (s *Store) UpdateStorageUsage(ctx context.Context, sandboxID string, rootfsBytes, workspaceBytes, cacheBytes, snapshotBytes int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sandbox_storage
		SET rootfs_bytes=?, workspace_bytes=?, cache_bytes=?, snapshot_bytes=?, updated_at=?
		WHERE sandbox_id=?
	`, rootfsBytes, workspaceBytes, cacheBytes, snapshotBytes, time.Now().UTC().Format(time.RFC3339Nano), sandboxID)
	return err
}

type TenantUsage struct {
	Sandboxes          int               `json:"sandboxes"`
	RunningSandboxes   int               `json:"running_sandboxes"`
	ConcurrentExecs    int               `json:"concurrent_execs"`
	ActiveTunnels      int               `json:"active_tunnels"`
	RequestedCPU       model.CPUQuantity `json:"requested_cpu"`
	RequestedMemory    int               `json:"requested_memory_mb"`
	RequestedStorage   int               `json:"requested_storage_mb"`
	ActualStorageBytes int64             `json:"actual_storage_bytes"`
}

func (s *Store) TenantUsage(ctx context.Context, tenantID string) (TenantUsage, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) AS sandboxes,
			SUM(CASE WHEN s.status = ? THEN 1 ELSE 0 END) AS running,
			SUM(s.cpu_limit_millis) AS cpu_total,
			SUM(s.memory_limit_mb) AS memory_total,
			SUM(s.disk_limit_mb) AS storage_total,
			SUM(COALESCE(ss.rootfs_bytes, 0) + COALESCE(ss.workspace_bytes, 0) + COALESCE(ss.cache_bytes, 0) + COALESCE(ss.snapshot_bytes, 0)) AS actual_storage_bytes,
			COALESCE((SELECT COUNT(*) FROM executions e WHERE e.tenant_id = ? AND e.status = ?), 0) AS concurrent_execs,
			COALESCE((SELECT COUNT(*) FROM tunnels t WHERE t.tenant_id = ? AND t.revoked_at IS NULL), 0) AS active_tunnels
		FROM sandboxes s
		LEFT JOIN sandbox_storage ss ON ss.sandbox_id = s.sandbox_id
		WHERE s.tenant_id = ? AND s.status != ?
	`, string(model.SandboxStatusRunning), tenantID, string(model.ExecutionStatusRunning), tenantID, tenantID, string(model.SandboxStatusDeleted))
	var usage TenantUsage
	var running, cpuTotal, memTotal, storageTotal, actualStorageBytes sql.NullInt64
	if err := row.Scan(&usage.Sandboxes, &running, &cpuTotal, &memTotal, &storageTotal, &actualStorageBytes, &usage.ConcurrentExecs, &usage.ActiveTunnels); err != nil {
		return usage, err
	}
	usage.RunningSandboxes = int(running.Int64)
	usage.RequestedCPU = model.CPUQuantity(cpuTotal.Int64)
	usage.RequestedMemory = int(memTotal.Int64)
	usage.RequestedStorage = int(storageTotal.Int64)
	usage.ActualStorageBytes = actualStorageBytes.Int64
	return usage, nil
}

func (s *Store) CreateExecution(ctx context.Context, execution model.Execution) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO executions(
			execution_id, sandbox_id, tenant_id, command, cwd, timeout_seconds, status, exit_code,
			stdout_preview, stderr_preview, stdout_truncated, stderr_truncated, started_at, completed_at, duration_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)
	`, execution.ID, execution.SandboxID, execution.TenantID, execution.Command, execution.Cwd,
		execution.TimeoutSeconds, string(execution.Status), nil, "", "", 0, 0, execution.StartedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) UpdateExecution(ctx context.Context, execution model.Execution) error {
	var completed interface{}
	var duration interface{}
	if execution.CompletedAt != nil {
		completed = execution.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if execution.DurationMS != nil {
		duration = *execution.DurationMS
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE executions
		SET status=?, exit_code=?, stdout_preview=?, stderr_preview=?, stdout_truncated=?, stderr_truncated=?, completed_at=?, duration_ms=?
		WHERE execution_id=? AND tenant_id=?
	`, string(execution.Status), execution.ExitCode, execution.StdoutPreview, execution.StderrPreview,
		boolToInt(execution.StdoutTruncated), boolToInt(execution.StderrTruncated), completed, duration, execution.ID, execution.TenantID)
	return err
}

func (s *Store) CreateTTYSession(ctx context.Context, session model.TTYSession) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tty_sessions(tty_session_id, sandbox_id, tenant_id, command, connected, last_resize, created_at, closed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL)
	`, session.ID, session.SandboxID, session.TenantID, session.Command, boolToInt(session.Connected), session.LastResize, session.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) CloseTTYSession(ctx context.Context, tenantID, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tty_sessions SET connected=0, closed_at=? WHERE tty_session_id=? AND tenant_id=?
	`, time.Now().UTC().Format(time.RFC3339Nano), sessionID, tenantID)
	return err
}

func (s *Store) UpdateTTYResize(ctx context.Context, tenantID, sessionID, resize string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tty_sessions SET last_resize=? WHERE tty_session_id=? AND tenant_id=?
	`, resize, sessionID, tenantID)
	return err
}

func (s *Store) CreateTunnel(ctx context.Context, tunnel model.Tunnel) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tunnels(tunnel_id, sandbox_id, tenant_id, target_port, protocol, auth_mode, auth_secret_hash, visibility, endpoint, created_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`, tunnel.ID, tunnel.SandboxID, tunnel.TenantID, tunnel.TargetPort, string(tunnel.Protocol), tunnel.AuthMode, tunnel.AuthSecretHash, tunnel.Visibility, tunnel.Endpoint, tunnel.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListTunnels(ctx context.Context, tenantID, sandboxID string) ([]model.Tunnel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tunnel_id, sandbox_id, tenant_id, target_port, protocol, auth_mode, auth_secret_hash, visibility, endpoint, created_at, revoked_at
		FROM tunnels
		WHERE tenant_id=? AND sandbox_id=?
		ORDER BY created_at
	`, tenantID, sandboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tunnels []model.Tunnel
	for rows.Next() {
		tunnel, err := scanTunnel(rows)
		if err != nil {
			return nil, err
		}
		tunnels = append(tunnels, tunnel)
	}
	return tunnels, rows.Err()
}

func (s *Store) GetTunnel(ctx context.Context, tenantID, tunnelID string) (model.Tunnel, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT tunnel_id, sandbox_id, tenant_id, target_port, protocol, auth_mode, auth_secret_hash, visibility, endpoint, created_at, revoked_at
		FROM tunnels WHERE tenant_id=? AND tunnel_id=?
	`, tenantID, tunnelID)
	return scanTunnel(row)
}

func (s *Store) GetTunnelByID(ctx context.Context, tunnelID string) (model.Tunnel, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT tunnel_id, sandbox_id, tenant_id, target_port, protocol, auth_mode, auth_secret_hash, visibility, endpoint, created_at, revoked_at
		FROM tunnels WHERE tunnel_id=?
	`, tunnelID)
	return scanTunnel(row)
}

func (s *Store) RevokeTunnel(ctx context.Context, tenantID, tunnelID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tunnels SET revoked_at=? WHERE tenant_id=? AND tunnel_id=? AND revoked_at IS NULL
	`, time.Now().UTC().Format(time.RFC3339Nano), tenantID, tunnelID)
	return err
}

func (s *Store) CreateSnapshot(ctx context.Context, snapshot model.Snapshot) error {
	var completed interface{}
	if snapshot.CompletedAt != nil {
		completed = snapshot.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO snapshots(snapshot_id, sandbox_id, tenant_id, name, status, image_ref, profile, image_contract_version, control_protocol_version, workspace_tar, export_location, created_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, snapshot.ID, snapshot.SandboxID, snapshot.TenantID, snapshot.Name, string(snapshot.Status), snapshot.ImageRef, string(snapshot.Profile), snapshot.ImageContractVersion, snapshot.ControlProtocolVersion, snapshot.WorkspaceTar, snapshot.ExportLocation, snapshot.CreatedAt.UTC().Format(time.RFC3339Nano), completed)
	return err
}

func (s *Store) UpdateSnapshot(ctx context.Context, snapshot model.Snapshot) error {
	var completed interface{}
	if snapshot.CompletedAt != nil {
		completed = snapshot.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE snapshots
		SET status=?, image_ref=?, profile=?, image_contract_version=?, control_protocol_version=?, workspace_tar=?, export_location=?, completed_at=?
		WHERE snapshot_id=? AND tenant_id=?
	`, string(snapshot.Status), snapshot.ImageRef, string(snapshot.Profile), snapshot.ImageContractVersion, snapshot.ControlProtocolVersion, snapshot.WorkspaceTar, snapshot.ExportLocation, completed, snapshot.ID, snapshot.TenantID)
	return err
}

func (s *Store) GetSnapshot(ctx context.Context, tenantID, snapshotID string) (model.Snapshot, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT snapshot_id, sandbox_id, tenant_id, name, status, image_ref, profile, image_contract_version, control_protocol_version, workspace_tar, export_location, created_at, completed_at
		FROM snapshots WHERE tenant_id=? AND snapshot_id=?
	`, tenantID, snapshotID)
	return scanSnapshot(row)
}

func (s *Store) ListSnapshots(ctx context.Context, tenantID, sandboxID string) ([]model.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT snapshot_id, sandbox_id, tenant_id, name, status, image_ref, profile, image_contract_version, control_protocol_version, workspace_tar, export_location, created_at, completed_at
		FROM snapshots
		WHERE tenant_id=? AND sandbox_id=?
		ORDER BY created_at DESC
	`, tenantID, sandboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []model.Snapshot
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

func (s *Store) AddAuditEvent(ctx context.Context, event model.AuditEvent) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_events(audit_event_id, tenant_id, sandbox_id, action, resource_id, outcome, message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, event.ID, event.TenantID, event.SandboxID, event.Action, event.ResourceID, event.Outcome, event.Message, event.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListRunningExecutions(ctx context.Context) ([]model.Execution, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT execution_id, sandbox_id, tenant_id, command, cwd, timeout_seconds, status, started_at
		FROM executions
		WHERE status = ?
		ORDER BY started_at
	`, string(model.ExecutionStatusRunning))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var executions []model.Execution
	for rows.Next() {
		var execution model.Execution
		var started string
		if err := rows.Scan(&execution.ID, &execution.SandboxID, &execution.TenantID, &execution.Command, &execution.Cwd, &execution.TimeoutSeconds, &execution.Status, &started); err != nil {
			return nil, err
		}
		execution.StartedAt, err = parseTime(started)
		if err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	return executions, rows.Err()
}

func (s *Store) ListSnapshotsByStatus(ctx context.Context, status model.SnapshotStatus) ([]model.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT snapshot_id, sandbox_id, tenant_id, name, status, image_ref, profile, image_contract_version, control_protocol_version, workspace_tar, export_location, created_at, completed_at
		FROM snapshots
		WHERE status = ?
		ORDER BY created_at
	`, string(status))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []model.Snapshot
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

func (s *Store) ExecutionCounts(ctx context.Context, tenantID string) (map[model.ExecutionStatus]int, error) {
	query := `SELECT status, COUNT(*) FROM executions`
	args := []any{}
	if tenantID != "" {
		query += ` WHERE tenant_id = ?`
		args = append(args, tenantID)
	}
	query += ` GROUP BY status`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[model.ExecutionStatus]int)
	for rows.Next() {
		var status model.ExecutionStatus
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (s *Store) SnapshotCounts(ctx context.Context, tenantID string) (map[model.SnapshotStatus]int, error) {
	query := `SELECT status, COUNT(*) FROM snapshots`
	args := []any{}
	if tenantID != "" {
		query += ` WHERE tenant_id = ?`
		args = append(args, tenantID)
	}
	query += ` GROUP BY status`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[model.SnapshotStatus]int)
	for rows.Next() {
		var status model.SnapshotStatus
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (s *Store) ListAuditEvents(ctx context.Context, tenantID string) ([]model.AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT audit_event_id, tenant_id, sandbox_id, action, resource_id, outcome, message, created_at
		FROM audit_events
		WHERE tenant_id = ?
		ORDER BY created_at
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []model.AuditEvent
	for rows.Next() {
		var event model.AuditEvent
		var created string
		if err := rows.Scan(&event.ID, &event.TenantID, &event.SandboxID, &event.Action, &event.ResourceID, &event.Outcome, &event.Message, &created); err != nil {
			return nil, err
		}
		event.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func scanSandbox(scanner interface{ Scan(...any) error }) (model.Sandbox, error) {
	var sandbox model.Sandbox
	var created, updated, lastActive string
	var deleted sql.NullString
	var allowTunnels int
	var profile, featureSet, capabilitySet, controlMode, controlProtocolVersion, workspaceContractVersion, imageContractVersion string
	var cpuLimitMillis int64
	if err := scanner.Scan(
		&sandbox.ID, &sandbox.TenantID, &sandbox.Status, &sandbox.RuntimeBackend, &sandbox.BaseImageRef, &profile, &featureSet, &capabilitySet, &controlMode, &controlProtocolVersion, &workspaceContractVersion, &imageContractVersion,
		&cpuLimitMillis, &sandbox.MemoryLimitMB, &sandbox.PIDsLimit, &sandbox.DiskLimitMB, &sandbox.NetworkMode,
		&allowTunnels, &sandbox.StorageRoot, &sandbox.WorkspaceRoot, &sandbox.CacheRoot,
		&created, &updated, &lastActive, &deleted,
		&sandbox.RuntimeID, &sandbox.RuntimeStatus, &sandbox.LastRuntimeError,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Sandbox{}, ErrNotFound
		}
		return model.Sandbox{}, err
	}
	sandbox.CPULimit = model.CPUQuantity(cpuLimitMillis)
	sandbox.Profile = model.GuestProfile(profile)
	sandbox.Features = splitStringList(featureSet)
	sandbox.Capabilities = splitStringList(capabilitySet)
	sandbox.ControlMode = model.GuestControlMode(controlMode)
	sandbox.ControlProtocolVersion = controlProtocolVersion
	sandbox.WorkspaceContractVersion = workspaceContractVersion
	sandbox.ImageContractVersion = imageContractVersion
	sandbox.AllowTunnels = allowTunnels == 1
	createdAt, err := parseTime(created)
	if err != nil {
		return model.Sandbox{}, err
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return model.Sandbox{}, err
	}
	lastActiveAt, err := parseTime(lastActive)
	if err != nil {
		return model.Sandbox{}, err
	}
	sandbox.CreatedAt = createdAt
	sandbox.UpdatedAt = updatedAt
	sandbox.LastActiveAt = lastActiveAt
	if deleted.Valid {
		t, err := parseTime(deleted.String)
		if err != nil {
			return model.Sandbox{}, err
		}
		sandbox.DeletedAt = &t
	}
	return sandbox, nil
}

func scanTunnel(scanner interface{ Scan(...any) error }) (model.Tunnel, error) {
	var tunnel model.Tunnel
	var created string
	var revoked sql.NullString
	if err := scanner.Scan(&tunnel.ID, &tunnel.SandboxID, &tunnel.TenantID, &tunnel.TargetPort, &tunnel.Protocol, &tunnel.AuthMode, &tunnel.AuthSecretHash, &tunnel.Visibility, &tunnel.Endpoint, &created, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Tunnel{}, ErrNotFound
		}
		return model.Tunnel{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return model.Tunnel{}, err
	}
	tunnel.CreatedAt = createdAt
	if revoked.Valid {
		t, err := parseTime(revoked.String)
		if err != nil {
			return model.Tunnel{}, err
		}
		tunnel.RevokedAt = &t
	}
	return tunnel, nil
}

func scanSnapshot(scanner interface{ Scan(...any) error }) (model.Snapshot, error) {
	var snapshot model.Snapshot
	var created string
	var completed sql.NullString
	var profile, imageContractVersion, controlProtocolVersion string
	if err := scanner.Scan(&snapshot.ID, &snapshot.SandboxID, &snapshot.TenantID, &snapshot.Name, &snapshot.Status, &snapshot.ImageRef, &profile, &imageContractVersion, &controlProtocolVersion, &snapshot.WorkspaceTar, &snapshot.ExportLocation, &created, &completed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Snapshot{}, ErrNotFound
		}
		return model.Snapshot{}, err
	}
	snapshot.Profile = model.GuestProfile(profile)
	snapshot.ImageContractVersion = imageContractVersion
	snapshot.ControlProtocolVersion = controlProtocolVersion
	createdAt, err := parseTime(created)
	if err != nil {
		return model.Snapshot{}, err
	}
	snapshot.CreatedAt = createdAt
	if completed.Valid {
		t, err := parseTime(completed.String)
		if err != nil {
			return model.Snapshot{}, err
		}
		snapshot.CompletedAt = &t
	}
	return snapshot, nil
}

func parseTime(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func joinStringList(values []string) string {
	return strings.Join(values, ",")
}

func splitStringList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		result = append(result, part)
	}
	return result
}
````

## File: internal/runtime/qemu/runtime.go
````go
package qemu

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"or3-sandbox/internal/guestimage"
	"or3-sandbox/internal/model"
)

const (
	defaultSSHBinary      = "ssh"
	defaultSCPBinary      = "scp"
	defaultQEMUImgBinary  = "qemu-img"
	defaultAgentTransport = "virtio-serial"
	sshCompatTransport    = "ssh-port-forward"
	readyMarkerPath       = "/var/lib/or3/bootstrap.ready"
	readyProbeTimeout     = 2 * time.Second
	defaultPollInterval   = 500 * time.Millisecond
	qemuRuntimePrefix     = "qemu-"
	sshPortBase           = 22000
	sshPortSpan           = 20000
	serialTailLimit       = 64 * 1024
)

var bootFailureMarkers = []string{
	"kernel panic",
	"no bootable device",
	"emergency mode",
	"failed to start",
	"gave up waiting",
}

var _ model.RuntimeManager = (*Runtime)(nil)

type commandRunner func(ctx context.Context, binary string, args ...string) ([]byte, error)

type sshProbe func(ctx context.Context, target sshTarget) error

type processArgsReader func(pid int) (string, error)

type Options struct {
	Binary         string
	Accel          string
	BaseImagePath  string
	ControlMode    model.GuestControlMode
	SSHUser        string
	SSHKeyPath     string
	SSHHostKeyPath string
	BootTimeout    time.Duration
	SSHBinary      string
	SCPBinary      string
}

type Runtime struct {
	qemuBinary     string
	qemuImgBinary  string
	sshBinary      string
	scpBinary      string
	accelerator    string
	baseImagePath  string
	controlMode    model.GuestControlMode
	agentTransport string
	sshUser        string
	sshKeyPath     string
	sshHostKeyPath string
	bootTimeout    time.Duration
	pollInterval   time.Duration

	runCommand  commandRunner
	sshReady    sshProbe
	processArgs processArgsReader
}

type hostProbe struct {
	goos          string
	commandExists func(string) error
	fileReadable  func(string) error
	kvmAvailable  func() error
	hvfAvailable  func() error
}

type sandboxLayout struct {
	baseDir           string
	rootfsDir         string
	workspaceDir      string
	cacheDir          string
	runtimeDir        string
	rootDiskPath      string
	workspaceDiskPath string
	pidPath           string
	monitorPath       string
	agentSocketPath   string
	knownHostsPath    string
	serialLogPath     string
}

type sshTarget struct {
	port           int
	knownHostsPath string
	hostKeyAlias   string
}

func New(opts Options) (*Runtime, error) {
	if strings.TrimSpace(opts.SSHBinary) == "" {
		opts.SSHBinary = defaultSSHBinary
	}
	if strings.TrimSpace(opts.SCPBinary) == "" {
		opts.SCPBinary = defaultSCPBinary
	}
	if !opts.ControlMode.IsValid() {
		opts.ControlMode = model.GuestControlModeAgent
	}
	accel, err := resolveAccel(opts.Accel, goruntime.GOOS)
	if err != nil {
		return nil, err
	}
	qemuImgBinary := deriveQEMUImgBinary(opts.Binary)
	if err := validateHost(opts, qemuImgBinary, accel, defaultHostProbe()); err != nil {
		return nil, err
	}
	runtime := &Runtime{
		qemuBinary:     opts.Binary,
		qemuImgBinary:  qemuImgBinary,
		sshBinary:      opts.SSHBinary,
		scpBinary:      opts.SCPBinary,
		accelerator:    accel,
		baseImagePath:  opts.BaseImagePath,
		controlMode:    opts.ControlMode,
		agentTransport: defaultAgentTransport,
		sshUser:        opts.SSHUser,
		sshKeyPath:     opts.SSHKeyPath,
		sshHostKeyPath: opts.SSHHostKeyPath,
		bootTimeout:    opts.BootTimeout,
		pollInterval:   defaultPollInterval,
	}
	runtime.runCommand = runtime.defaultRunCommand
	runtime.sshReady = runtime.defaultSSHProbe
	runtime.processArgs = defaultProcessArgsReader
	return runtime, nil
}

func (r *Runtime) Create(ctx context.Context, spec model.SandboxSpec) (model.RuntimeState, error) {
	layout := layoutForSpec(spec)
	if err := ensureLayout(layout); err != nil {
		return model.RuntimeState{}, err
	}
	if err := os.Remove(layout.pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return model.RuntimeState{}, err
	}
	baseImagePath, spec, err := r.guestBaseImage(spec)
	if err != nil {
		return model.RuntimeState{}, err
	}
	rootBytes, workspaceBytes := splitDiskBytes(spec.DiskLimitMB)
	if err := r.createRootDisk(ctx, baseImagePath, layout.rootDiskPath, rootBytes); err != nil {
		return model.RuntimeState{}, err
	}
	if err := r.createWorkspaceDisk(ctx, layout.workspaceDiskPath, workspaceBytes); err != nil {
		return model.RuntimeState{}, err
	}
	if r.controlModeForSpec(spec) == model.GuestControlModeSSHCompat {
		if err := r.seedKnownHosts(layout.knownHostsPath, sandboxHostKeyAlias(spec.SandboxID)); err != nil {
			return model.RuntimeState{}, err
		}
	}
	if err := touchFile(layout.serialLogPath); err != nil {
		return model.RuntimeState{}, err
	}
	return model.RuntimeState{
		RuntimeID:   qemuRuntimePrefix + spec.SandboxID,
		Status:      model.SandboxStatusStopped,
		Running:     false,
		ControlMode: r.controlModeForSpec(spec),
	}, nil
}

func (r *Runtime) Start(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	if state, err := r.Inspect(ctx, sandbox); err == nil && state.Status == model.SandboxStatusRunning {
		return state, nil
	}
	layout := layoutForSandbox(sandbox)
	if err := ensureLayout(layout); err != nil {
		return model.RuntimeState{}, err
	}
	target, runtimeID, err := r.startTarget(sandbox, layout)
	if err != nil {
		return model.RuntimeState{}, err
	}
	args := r.startArgs(sandbox, layout, target.port)
	if _, err := r.runCommand(ctx, r.qemuBinary, args...); err != nil {
		return model.RuntimeState{}, fmt.Errorf("start qemu guest: %w", err)
	}
	if _, err := waitForPID(layout.pidPath, r.bootTimeout); err != nil {
		return model.RuntimeState{}, err
	}
	if err := r.waitForReady(ctx, sandboxWithRuntimeID(sandbox, runtimeID), target, layout.serialLogPath); err != nil {
		_, _ = r.Stop(context.Background(), sandboxWithRuntimeID(sandbox, runtimeID), true)
		return model.RuntimeState{}, err
	}
	return r.Inspect(ctx, sandboxWithRuntimeID(sandbox, runtimeID))
}

func (r *Runtime) Stop(ctx context.Context, sandbox model.Sandbox, force bool) (model.RuntimeState, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	pid, err := r.liveSandboxPID(layout)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(suspendedMarkerPath(layout))
		return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
	}
	if err != nil {
		return model.RuntimeState{}, err
	}
	if isSuspended(layout) && !force {
		if err := syscall.Kill(pid, syscall.SIGCONT); err != nil && !errors.Is(err, syscall.ESRCH) {
			return model.RuntimeState{}, err
		}
	}
	if err := terminatePID(pid, force); err != nil {
		return model.RuntimeState{}, err
	}
	_ = os.Remove(layout.pidPath)
	_ = os.Remove(suspendedMarkerPath(layout))
	return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
}

func (r *Runtime) Suspend(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	pid, err := r.liveSandboxPID(layout)
	if errors.Is(err, os.ErrNotExist) {
		return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
	}
	if err != nil {
		return model.RuntimeState{}, err
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		return model.RuntimeState{}, err
	}
	if err := touchFile(suspendedMarkerPath(layout)); err != nil {
		return model.RuntimeState{}, err
	}
	return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusSuspended, Running: false, Pid: pid}, nil
}

func (r *Runtime) Resume(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	layout := layoutForSandbox(sandbox)
	pid, err := r.liveSandboxPID(layout)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(suspendedMarkerPath(layout))
		return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
	}
	if err != nil {
		return model.RuntimeState{}, err
	}
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		return model.RuntimeState{}, err
	}
	_ = os.Remove(suspendedMarkerPath(layout))
	target := r.sshTarget(sandbox, layout)
	if err := r.waitForReady(ctx, sandbox, target, layout.serialLogPath); err != nil {
		return model.RuntimeState{}, err
	}
	return r.Inspect(ctx, sandbox)
}

func (r *Runtime) Destroy(ctx context.Context, sandbox model.Sandbox) error {
	_, _ = r.Stop(ctx, sandbox, true)
	return os.RemoveAll(layoutForSandbox(sandbox).baseDir)
}

func (r *Runtime) Inspect(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	layout := layoutForSandbox(sandbox)
	pid, err := r.liveSandboxPID(layout)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(suspendedMarkerPath(layout))
		return model.RuntimeState{RuntimeID: sandbox.RuntimeID, Status: model.SandboxStatusStopped}, nil
	}
	if err != nil {
		return model.RuntimeState{}, err
	}
	if isSuspended(layout) {
		return model.RuntimeState{
			RuntimeID: sandbox.RuntimeID,
			Status:    model.SandboxStatusSuspended,
			Running:   false,
			Pid:       pid,
		}, nil
	}
	target := r.sshTarget(sandbox, layout)
	probeCtx, cancel := context.WithTimeout(ctx, readyProbeTimeout)
	defer cancel()
	if err := r.probeReady(probeCtx, sandbox, layout, target); err != nil {
		if reason, ok := bootFailureReason(layout.serialLogPath); ok {
			return model.RuntimeState{
				RuntimeID:   sandbox.RuntimeID,
				Status:      model.SandboxStatusError,
				Running:     false,
				Pid:         pid,
				ControlMode: r.controlModeForSandbox(sandbox),
				Error:       reason,
			}, nil
		}
		if withinBootWindow(layout.pidPath, r.effectiveBootTimeout()) {
			return model.RuntimeState{
				RuntimeID:   sandbox.RuntimeID,
				Status:      model.SandboxStatusBooting,
				Running:     false,
				Pid:         pid,
				ControlMode: r.controlModeForSandbox(sandbox),
				Error:       fmt.Sprintf("guest is still booting: %v", err),
			}, nil
		}
		return model.RuntimeState{
			RuntimeID:   sandbox.RuntimeID,
			Status:      model.SandboxStatusDegraded,
			Running:     false,
			Pid:         pid,
			ControlMode: r.controlModeForSandbox(sandbox),
			Error:       fmt.Sprintf("guest process is alive but not ready: %v", err),
		}, nil
	}
	return model.RuntimeState{
		RuntimeID:   sandbox.RuntimeID,
		Status:      model.SandboxStatusRunning,
		Running:     true,
		Pid:         pid,
		IPAddress:   "127.0.0.1",
		ControlMode: r.controlModeForSandbox(sandbox),
	}, nil
}

func (r *Runtime) CreateSnapshot(ctx context.Context, sandbox model.Sandbox, snapshotID string) (model.SnapshotInfo, error) {
	state, err := r.Inspect(ctx, sandbox)
	if err != nil {
		return model.SnapshotInfo{}, err
	}
	if state.Status != model.SandboxStatusStopped {
		return model.SnapshotInfo{}, fmt.Errorf("qemu snapshots require the sandbox to be stopped")
	}
	layout := layoutForSandbox(sandbox)
	snapshotDir := filepath.Join(sandbox.StorageRoot, ".snapshots", snapshotID)
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return model.SnapshotInfo{}, err
	}
	rootSnapshot := filepath.Join(snapshotDir, "rootfs.img")
	workspaceSnapshot := filepath.Join(snapshotDir, "workspace.img")
	if err := copyFile(layout.rootDiskPath, rootSnapshot); err != nil {
		return model.SnapshotInfo{}, err
	}
	if err := copyFile(layout.workspaceDiskPath, workspaceSnapshot); err != nil {
		return model.SnapshotInfo{}, err
	}
	return model.SnapshotInfo{
		ImageRef:     rootSnapshot,
		WorkspaceTar: workspaceSnapshot,
	}, nil
}

func (r *Runtime) RestoreSnapshot(ctx context.Context, sandbox model.Sandbox, snapshot model.Snapshot) (model.RuntimeState, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	if err := ensureLayout(layout); err != nil {
		return model.RuntimeState{}, err
	}
	if snapshot.ImageRef != "" {
		if err := copyFile(snapshot.ImageRef, layout.rootDiskPath); err != nil {
			return model.RuntimeState{}, err
		}
	}
	if snapshot.WorkspaceTar != "" {
		if err := copyFile(snapshot.WorkspaceTar, layout.workspaceDiskPath); err != nil {
			return model.RuntimeState{}, err
		}
	}
	return model.RuntimeState{
		RuntimeID: sandbox.RuntimeID,
		Status:    model.SandboxStatusStopped,
	}, nil
}

func (r *Runtime) createRootDisk(ctx context.Context, baseImagePath, outputPath string, sizeBytes int64) error {
	if r.qemuImgBinary == "" || r.runCommand == nil {
		return createSparseFile(outputPath, sizeBytes)
	}
	_ = os.Remove(outputPath)
	_, err := r.runCommand(
		ctx,
		r.qemuImgBinary,
		"create",
		"-f", "qcow2",
		"-F", "qcow2",
		"-b", baseImagePath,
		outputPath,
		qemuSize(sizeBytes),
	)
	return err
}

func (r *Runtime) createWorkspaceDisk(ctx context.Context, outputPath string, sizeBytes int64) error {
	if r.qemuImgBinary == "" || r.runCommand == nil {
		return createSparseFile(outputPath, sizeBytes)
	}
	_ = os.Remove(outputPath)
	_, err := r.runCommand(
		ctx,
		r.qemuImgBinary,
		"create",
		"-f", "raw",
		outputPath,
		qemuSize(sizeBytes),
	)
	return err
}

func (r *Runtime) startArgs(sandbox model.Sandbox, layout sandboxLayout, sshPort int) []string {
	args := []string{
		"-daemonize",
		"-pidfile", layout.pidPath,
		"-monitor", "unix:" + layout.monitorPath + ",server,nowait",
		"-serial", "file:" + layout.serialLogPath,
		"-display", "none",
		"-accel", r.accelerator,
		"-m", strconv.Itoa(defaultInt(sandbox.MemoryLimitMB, 512)),
		"-smp", strconv.Itoa(defaultVCPUCount(sandbox.CPULimit, 1)),
		"-device", "virtio-serial",
		"-chardev", "socket,id=agent0,path=" + layout.agentSocketPath + ",server=on,wait=off",
		"-device", "virtserialport,chardev=agent0,name=org.or3.guest_agent",
		"-drive", "if=virtio,file=" + layout.rootDiskPath + ",format=qcow2",
		"-drive", "if=virtio,file=" + layout.workspaceDiskPath + ",format=raw",
	}
	args = append(args, r.networkArgs(sandbox, sshPort)...)
	return args
}

func (r *Runtime) networkArgs(sandbox model.Sandbox, sshPort int) []string {
	netdev := "user,id=net0"
	if sandbox.NetworkMode == model.NetworkModeInternetDisabled {
		netdev = "user,id=net0,restrict=on"
	}
	transport, err := r.controlTransportForSandbox(sandbox)
	if err == nil && transport.mode == model.GuestControlModeSSHCompat {
		hostfwd := fmt.Sprintf(",hostfwd=tcp:127.0.0.1:%d-:22", sshPort)
		netdev += hostfwd
	}
	return []string{
		"-netdev", netdev,
		"-device", r.networkDeviceModel() + ",netdev=net0",
	}
}

func (r *Runtime) networkDeviceModel() string {
	if strings.Contains(r.qemuBinary, "aarch64") {
		return "virtio-net-device"
	}
	return "virtio-net-pci"
}

func (r *Runtime) sshTarget(sandbox model.Sandbox, layout sandboxLayout) sshTarget {
	port, ok := sshPortFromRuntimeID(sandbox.RuntimeID)
	if !ok {
		port = sshPortForSandbox(sandbox.ID)
	}
	return sshTarget{
		port:           port,
		knownHostsPath: layout.knownHostsPath,
		hostKeyAlias:   sandboxHostKeyAlias(sandbox.ID),
	}
}

func (r *Runtime) startTarget(sandbox model.Sandbox, layout sandboxLayout) (sshTarget, string, error) {
	transport, err := r.controlTransportForSandbox(sandbox)
	if err != nil {
		return sshTarget{}, "", err
	}
	if transport.mode != model.GuestControlModeSSHCompat {
		return sshTarget{}, qemuRuntimePrefix + sandbox.ID, nil
	}
	port, ok := sshPortFromRuntimeID(sandbox.RuntimeID)
	if !ok || !isTCPPortAvailable(port) {
		port, err = allocateSSHPort()
		if err != nil {
			return sshTarget{}, "", err
		}
	}
	runtimeID := runtimeIDWithSSHPort(sandbox.ID, port)
	return sshTarget{
		port:           port,
		knownHostsPath: layout.knownHostsPath,
		hostKeyAlias:   sandboxHostKeyAlias(sandbox.ID),
	}, runtimeID, nil
}

func (r *Runtime) baseSSHArgs(target sshTarget, tty bool) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + target.knownHostsPath,
		"-o", "HostKeyAlias=" + target.hostKeyAlias,
		"-o", "ConnectTimeout=5",
		"-i", r.sshKeyPath,
		"-p", strconv.Itoa(target.port),
	}
	if tty {
		args = append(args, "-tt")
	} else {
		args = append(args, "-T")
	}
	return append(args, r.sshUser+"@127.0.0.1")
}

func (r *Runtime) waitForReady(ctx context.Context, sandbox model.Sandbox, target sshTarget, serialLogPath string) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, r.effectiveBootTimeout())
	defer cancel()
	ticker := time.NewTicker(r.effectivePollInterval())
	defer ticker.Stop()
	var lastErr error
	for {
		if err := r.probeReady(timeoutCtx, sandbox, layoutForSandbox(sandbox), target); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if reason, ok := bootFailureReason(serialLogPath); ok {
			return errors.New(reason)
		}
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("guest readiness timed out: %w", lastErr)
		case <-ticker.C:
		}
	}
}

func bootFailureReason(serialLogPath string) (string, bool) {
	if strings.TrimSpace(serialLogPath) == "" {
		return "", false
	}
	data, err := readFileTail(serialLogPath, serialTailLimit)
	if err != nil || len(data) == 0 {
		return "", false
	}
	logTail := strings.ToLower(string(data))
	for _, marker := range bootFailureMarkers {
		if strings.Contains(logTail, marker) {
			return fmt.Sprintf("guest boot failed: %s", marker), true
		}
	}
	return "", false
}

func readFileTail(path string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	offset := int64(0)
	if size > limit {
		offset = size - limit
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func (r *Runtime) defaultRunCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (r *Runtime) defaultSSHProbe(ctx context.Context, target sshTarget) error {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + target.knownHostsPath,
		"-o", "HostKeyAlias=" + target.hostKeyAlias,
		"-o", "ConnectTimeout=2",
		"-i", r.sshKeyPath,
		"-p", strconv.Itoa(target.port),
		r.sshUser + "@127.0.0.1",
		"sh", "-lc", "test -f " + shellQuote(readyMarkerPath),
	}
	_, err := r.runCommand(ctx, r.sshBinary, args...)
	return err
}

func (r *Runtime) guestBaseImage(spec model.SandboxSpec) (string, model.SandboxSpec, error) {
	path := strings.TrimSpace(spec.BaseImageRef)
	if path == "" {
		path = r.baseImagePath
	}
	if !isReadableFile(path) {
		return "", model.SandboxSpec{}, fmt.Errorf("qemu base image path %q is not readable", path)
	}
	path = filepath.Clean(path)
	contract, err := guestimage.Load(path)
	if err != nil {
		return "", model.SandboxSpec{}, err
	}
	if err := guestimage.Validate(path, contract); err != nil {
		return "", model.SandboxSpec{}, err
	}
	if spec.Profile == "" {
		spec.Profile = contract.Profile
	}
	if !spec.ControlMode.IsValid() {
		spec.ControlMode = contract.Control.Mode
	}
	if strings.TrimSpace(spec.ControlProtocolVersion) == "" {
		spec.ControlProtocolVersion = contract.Control.ProtocolVersion
	}
	if strings.TrimSpace(spec.WorkspaceContractVersion) == "" {
		spec.WorkspaceContractVersion = contract.WorkspaceContractVersion
	}
	if strings.TrimSpace(spec.ImageContractVersion) == "" {
		spec.ImageContractVersion = contract.ContractVersion
	}
	if spec.Profile != "" && contract.Profile != spec.Profile {
		return "", model.SandboxSpec{}, fmt.Errorf("guest image profile %q does not match sandbox profile %q", contract.Profile, spec.Profile)
	}
	if spec.ControlMode.IsValid() && contract.Control.Mode != spec.ControlMode {
		return "", model.SandboxSpec{}, fmt.Errorf("guest image control mode %q does not match sandbox control mode %q", contract.Control.Mode, spec.ControlMode)
	}
	transport, err := r.controlTransportForSpec(spec)
	if err != nil {
		return "", model.SandboxSpec{}, err
	}
	if contract.Control.Mode == model.GuestControlModeAgent && len(contract.Control.SupportedTransports) > 0 {
		supported := false
		for _, candidate := range contract.Control.SupportedTransports {
			if strings.EqualFold(strings.TrimSpace(candidate), transport.name) {
				supported = true
				break
			}
		}
		if !supported {
			return "", model.SandboxSpec{}, fmt.Errorf("guest image does not support runtime agent transport %q", transport.name)
		}
	}
	if strings.TrimSpace(spec.ControlProtocolVersion) != "" && contract.Control.ProtocolVersion != spec.ControlProtocolVersion {
		return "", model.SandboxSpec{}, fmt.Errorf("guest image control protocol %q does not match sandbox control protocol %q", contract.Control.ProtocolVersion, spec.ControlProtocolVersion)
	}
	if strings.TrimSpace(spec.WorkspaceContractVersion) != "" && contract.WorkspaceContractVersion != spec.WorkspaceContractVersion {
		return "", model.SandboxSpec{}, fmt.Errorf("guest image workspace contract version %q does not match sandbox workspace contract version %q", contract.WorkspaceContractVersion, spec.WorkspaceContractVersion)
	}
	if strings.TrimSpace(spec.ImageContractVersion) != "" && contract.ContractVersion != spec.ImageContractVersion {
		return "", model.SandboxSpec{}, fmt.Errorf("guest image contract version %q does not match sandbox contract version %q", contract.ContractVersion, spec.ImageContractVersion)
	}
	return path, spec, nil
}

func (r *Runtime) effectiveBootTimeout() time.Duration {
	if r.bootTimeout > 0 {
		return r.bootTimeout
	}
	return 2 * time.Minute
}

func (r *Runtime) effectivePollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return defaultPollInterval
}

func resolveAccel(value, goos string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		switch goos {
		case "linux":
			return "kvm", nil
		case "darwin":
			return "hvf", nil
		default:
			return "", fmt.Errorf("qemu runtime is unsupported on host OS %q", goos)
		}
	case "kvm":
		if goos != "linux" {
			return "", fmt.Errorf("qemu accel %q is unsupported on host OS %q", value, goos)
		}
		return "kvm", nil
	case "hvf":
		if goos != "darwin" {
			return "", fmt.Errorf("qemu accel %q is unsupported on host OS %q", value, goos)
		}
		return "hvf", nil
	default:
		return "", fmt.Errorf("unsupported qemu accelerator %q", value)
	}
}

func defaultHostProbe() hostProbe {
	return hostProbe{
		goos:          goruntime.GOOS,
		commandExists: requireCommand,
		fileReadable:  requireReadableFile,
		kvmAvailable:  requireKVM,
		hvfAvailable:  requireHVF,
	}
}

func validateHost(opts Options, qemuImgBinary, accel string, probe hostProbe) error {
	if strings.TrimSpace(opts.Binary) == "" {
		return errors.New("qemu binary is required")
	}
	if strings.TrimSpace(opts.BaseImagePath) == "" {
		return errors.New("qemu base image path is required")
	}
	if !opts.ControlMode.IsValid() {
		opts.ControlMode = model.GuestControlModeAgent
	}
	if opts.ControlMode == model.GuestControlModeSSHCompat {
		if strings.TrimSpace(opts.SSHUser) == "" {
			return errors.New("qemu ssh user is required")
		}
		if strings.TrimSpace(opts.SSHKeyPath) == "" {
			return errors.New("qemu ssh key path is required")
		}
		if strings.TrimSpace(opts.SSHHostKeyPath) == "" {
			return errors.New("qemu ssh host key path is required")
		}
	}
	if opts.BootTimeout <= 0 {
		return errors.New("qemu boot timeout must be positive")
	}
	requiredCommands := []string{opts.Binary, qemuImgBinary, "ps"}
	if opts.ControlMode == model.GuestControlModeSSHCompat {
		requiredCommands = append(requiredCommands, opts.SSHBinary, opts.SCPBinary)
	}
	for _, command := range requiredCommands {
		if err := probe.commandExists(command); err != nil {
			return fmt.Errorf("required host command %q is unavailable: %w", command, err)
		}
	}
	requiredFiles := []string{opts.BaseImagePath}
	if opts.ControlMode == model.GuestControlModeSSHCompat {
		requiredFiles = append(requiredFiles, opts.SSHKeyPath, opts.SSHHostKeyPath)
	}
	for _, path := range requiredFiles {
		if err := probe.fileReadable(path); err != nil {
			return fmt.Errorf("required file %q is unavailable: %w", path, err)
		}
	}
	switch accel {
	case "kvm":
		if err := probe.kvmAvailable(); err != nil {
			return err
		}
	case "hvf":
		if err := probe.hvfAvailable(); err != nil {
			return err
		}
	}
	return nil
}

func sandboxHostKeyAlias(sandboxID string) string {
	return "or3-qemu-" + sandboxID
}

func (r *Runtime) seedKnownHosts(path, alias string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("known hosts path is required")
	}
	keyData, err := os.ReadFile(r.sshHostKeyPath)
	if err != nil {
		return err
	}
	entry, err := knownHostEntry(alias, string(keyData))
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(entry+"\n"), 0o600)
}

func knownHostEntry(alias, key string) (string, error) {
	trimmedAlias := strings.TrimSpace(alias)
	if trimmedAlias == "" {
		return "", errors.New("known hosts alias is required")
	}
	fields := strings.Fields(strings.TrimSpace(key))
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "ssh-") {
		return "", fmt.Errorf("invalid ssh host public key format")
	}
	entry := trimmedAlias + " " + fields[0] + " " + fields[1]
	if len(fields) > 2 {
		entry += " " + strings.Join(fields[2:], " ")
	}
	return entry, nil
}

func deriveQEMUImgBinary(qemuBinary string) string {
	if strings.TrimSpace(qemuBinary) == "" {
		return defaultQEMUImgBinary
	}
	base := filepath.Base(qemuBinary)
	if strings.HasPrefix(base, "qemu-system-") {
		candidate := strings.TrimSuffix(qemuBinary, base) + defaultQEMUImgBinary
		if candidate != "" {
			return candidate
		}
	}
	return defaultQEMUImgBinary
}

func layoutForSpec(spec model.SandboxSpec) sandboxLayout {
	baseDir := filepath.Dir(spec.StorageRoot)
	return sandboxLayout{
		baseDir:           baseDir,
		rootfsDir:         spec.StorageRoot,
		workspaceDir:      spec.WorkspaceRoot,
		cacheDir:          spec.CacheRoot,
		runtimeDir:        filepath.Join(baseDir, ".runtime"),
		rootDiskPath:      filepath.Join(spec.StorageRoot, "overlay.qcow2"),
		workspaceDiskPath: filepath.Join(spec.WorkspaceRoot, "workspace.img"),
		pidPath:           filepath.Join(baseDir, ".runtime", "qemu.pid"),
		monitorPath:       filepath.Join(baseDir, ".runtime", "monitor.sock"),
		agentSocketPath:   filepath.Join(baseDir, ".runtime", "agent.sock"),
		knownHostsPath:    filepath.Join(baseDir, ".runtime", "ssh-known-hosts"),
		serialLogPath:     filepath.Join(baseDir, ".runtime", "serial.log"),
	}
}

func layoutForSandbox(sandbox model.Sandbox) sandboxLayout {
	return layoutForSpec(model.SandboxSpec{
		SandboxID:     sandbox.ID,
		ControlMode:   sandbox.ControlMode,
		StorageRoot:   sandbox.StorageRoot,
		WorkspaceRoot: sandbox.WorkspaceRoot,
		CacheRoot:     sandbox.CacheRoot,
	})
}

func (r *Runtime) controlModeForSandbox(sandbox model.Sandbox) model.GuestControlMode {
	if sandbox.ControlMode.IsValid() {
		return sandbox.ControlMode
	}
	if path := strings.TrimSpace(sandbox.BaseImageRef); path != "" {
		if contract, err := guestimage.Load(path); err == nil && contract.Control.Mode.IsValid() {
			return contract.Control.Mode
		}
	}
	if r.controlMode.IsValid() {
		return r.controlMode
	}
	return model.GuestControlModeAgent
}

func (r *Runtime) controlModeForSpec(spec model.SandboxSpec) model.GuestControlMode {
	if spec.ControlMode.IsValid() {
		return spec.ControlMode
	}
	if r.controlMode.IsValid() {
		return r.controlMode
	}
	return model.GuestControlModeAgent
}

type controlTransport struct {
	mode model.GuestControlMode
	name string
}

func (r *Runtime) controlTransportForSandbox(sandbox model.Sandbox) (controlTransport, error) {
	return r.controlTransport(r.controlModeForSandbox(sandbox))
}

func (r *Runtime) controlTransportForSpec(spec model.SandboxSpec) (controlTransport, error) {
	return r.controlTransport(r.controlModeForSpec(spec))
}

func (r *Runtime) controlTransport(mode model.GuestControlMode) (controlTransport, error) {
	switch mode {
	case model.GuestControlModeAgent:
		transport := strings.TrimSpace(r.agentTransport)
		if transport == "" {
			transport = defaultAgentTransport
		}
		return controlTransport{mode: mode, name: transport}, nil
	case model.GuestControlModeSSHCompat:
		return controlTransport{mode: mode, name: sshCompatTransport}, nil
	default:
		return controlTransport{}, fmt.Errorf("unsupported control mode %q", mode)
	}
}

func (r *Runtime) probeReady(ctx context.Context, sandbox model.Sandbox, layout sandboxLayout, target sshTarget) error {
	transport, err := r.controlTransportForSandbox(sandbox)
	if err != nil {
		return err
	}
	if transport.mode == model.GuestControlModeAgent {
		return r.agentReady(ctx, layout)
	}
	return r.sshReady(ctx, target)
}

func ensureLayout(layout sandboxLayout) error {
	for _, dir := range []string{layout.rootfsDir, layout.workspaceDir, layout.cacheDir, layout.runtimeDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func splitDiskBytes(totalMB int) (int64, int64) {
	totalBytes := int64(totalMB) * 1024 * 1024
	if totalBytes <= 0 {
		return 0, 0
	}
	// Keep the operator model simple in the first pass: split requested disk
	// budget evenly between the writable system layer and persistent workspace.
	root := totalBytes / 2
	workspace := totalBytes - root
	return root, workspace
}

func createSparseFile(path string, sizeBytes int64) error {
	if sizeBytes <= 0 {
		return fmt.Errorf("invalid sparse file size %d", sizeBytes)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Truncate(sizeBytes)
}

func touchFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	return file.Close()
}

func suspendedMarkerPath(layout sandboxLayout) string {
	return filepath.Join(layout.runtimeDir, "suspended")
}

func isSuspended(layout sandboxLayout) bool {
	_, err := os.Stat(suspendedMarkerPath(layout))
	return err == nil
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil {
		return 0, fmt.Errorf("parse pid %s: %w", path, err)
	}
	return pid, nil
}

func (r *Runtime) liveSandboxPID(layout sandboxLayout) (int, error) {
	pid, err := readPID(layout.pidPath)
	if err != nil {
		return 0, err
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			_ = os.Remove(layout.pidPath)
			_ = os.Remove(suspendedMarkerPath(layout))
			return 0, os.ErrNotExist
		}
		return 0, err
	}
	match, err := r.processMatchesSandbox(pid, layout)
	if err != nil {
		return 0, err
	}
	if !match {
		_ = os.Remove(layout.pidPath)
		_ = os.Remove(suspendedMarkerPath(layout))
		return 0, os.ErrNotExist
	}
	return pid, nil
}

func (r *Runtime) processMatchesSandbox(pid int, layout sandboxLayout) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	if strings.TrimSpace(r.qemuBinary) == "" || r.processArgs == nil {
		return true, nil
	}
	args, err := r.processArgs(pid)
	if err != nil {
		return false, err
	}
	expected := []string{
		filepath.Base(r.qemuBinary),
		layout.rootDiskPath,
		layout.monitorPath,
	}
	for _, needle := range expected {
		if strings.TrimSpace(needle) == "" {
			continue
		}
		if !strings.Contains(args, needle) {
			return false, nil
		}
	}
	return true, nil
}

func withinBootWindow(pidPath string, bootTimeout time.Duration) bool {
	if bootTimeout <= 0 {
		return false
	}
	info, err := os.Stat(pidPath)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) <= bootTimeout
}

func terminatePID(pid int, force bool) error {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	if err := syscall.Kill(pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	if force {
		return nil
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func waitForPID(path string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pid, err := readPID(path)
		if err == nil {
			return pid, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, fmt.Errorf("qemu pidfile %s did not appear before timeout", path)
}

func defaultProcessArgsReader(pid int) (string, error) {
	output, err := exec.Command("ps", "-ww", "-o", "args=", "-p", strconv.Itoa(pid)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("inspect process %d: %w: %s", pid, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func sshPortForSandbox(id string) int {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(id))
	return sshPortBase + int(hasher.Sum32()%sshPortSpan)
}

func qemuSize(sizeBytes int64) string {
	return strconv.FormatInt(sizeBytes, 10)
}

func runtimeIDWithSSHPort(sandboxID string, port int) string {
	return fmt.Sprintf("%s%s@%d", qemuRuntimePrefix, sandboxID, port)
}

func sshPortFromRuntimeID(runtimeID string) (int, bool) {
	suffix := strings.TrimPrefix(runtimeID, qemuRuntimePrefix)
	index := strings.LastIndex(suffix, "@")
	if index < 0 || index == len(suffix)-1 {
		return 0, false
	}
	port, err := strconv.Atoi(suffix[index+1:])
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}

func allocateSSHPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 {
		return 0, fmt.Errorf("allocate ssh port: unexpected listener address %T", listener.Addr())
	}
	return addr.Port, nil
}

func isTCPPortAvailable(port int) bool {
	if port <= 0 {
		return false
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

func sandboxWithRuntimeID(sandbox model.Sandbox, runtimeID string) model.Sandbox {
	sandbox.RuntimeID = runtimeID
	return sandbox
}

func allocatedPathSize(path string) (int64, error) {
	if strings.TrimSpace(path) == "" {
		return 0, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		return allocatedFileSize(info), nil
	}
	var total int64
	err = filepath.Walk(path, func(current string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		total += allocatedFileSize(info)
		return nil
	})
	return total, err
}

func allocatedFileSize(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Blocks > 0 {
		return stat.Blocks * 512
	}
	return info.Size()
}

func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	target, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer target.Close()
	_, err = io.Copy(target, source)
	return err
}

func requireCommand(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("command path is empty")
	}
	if filepath.IsAbs(name) || strings.ContainsRune(name, os.PathSeparator) {
		return requireReadableFile(name)
	}
	_, err := exec.LookPath(name)
	return err
}

func requireReadableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func requireKVM() error {
	return requireReadableFile("/dev/kvm")
}

func requireHVF() error {
	output, err := exec.Command("sysctl", "-n", "kern.hv_support").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	if strings.TrimSpace(string(output)) != "1" {
		return fmt.Errorf("kern.hv_support=%s", strings.TrimSpace(string(output)))
	}
	return nil
}

func isReadableFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	return requireReadableFile(path) == nil
}

func defaultInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func defaultVCPUCount(value model.CPUQuantity, fallback int) int {
	if value > 0 {
		return value.VCPUCount()
	}
	return fallback
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
````

## File: internal/api/router.go
````go
package api

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"or3-sandbox/internal/auth"
	"or3-sandbox/internal/config"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
	"or3-sandbox/internal/service"
)

type Router struct {
	log              *slog.Logger
	service          *service.Service
	operatorHost     string
	tunnelSigningKey []byte
	upgrader         websocket.Upgrader
}

const (
	tunnelSignedURLDefaultTTL = 5 * time.Minute
	tunnelSignedURLMaxTTL     = 15 * time.Minute
	tunnelSignedURLExpiryKey  = "or3_exp"
	tunnelSignedURLSigKey     = "or3_sig"
	tunnelAuthCookieName      = "or3_tunnel_auth"
)

func New(log *slog.Logger, svc *service.Service, cfg config.Config) http.Handler {
	router := &Router{
		log:              log,
		service:          svc,
		operatorHost:     strings.TrimRight(cfg.OperatorHost, "/"),
		tunnelSigningKey: newTunnelSigningKey(cfg),
		upgrader:         websocket.Upgrader{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", router.health)
	mux.HandleFunc("/metrics", router.handleMetrics)
	mux.HandleFunc("/v1/runtime/info", router.handleRuntimeInfo)
	mux.HandleFunc("/v1/runtime/health", router.handleRuntimeHealth)
	mux.HandleFunc("/v1/runtime/capacity", router.handleRuntimeCapacity)
	mux.HandleFunc("/v1/quotas/me", router.handleQuota)
	mux.HandleFunc("/v1/sandboxes", router.handleSandboxes)
	mux.HandleFunc("/v1/sandboxes/", router.handleSandboxRoutes)
	mux.HandleFunc("/v1/snapshots/", router.handleSnapshotRoutes)
	mux.HandleFunc("/v1/tunnels/", router.handleTunnelRoutes)
	return loggingMiddleware(log, mux)
}

func loggingMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		attrs := []any{
			"event", "http.request",
			"method", r.Method,
			"path", r.URL.Path,
			"status_code", recorder.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"response_bytes", recorder.bytes,
			"remote_addr", r.RemoteAddr,
			"outcome", httpOutcome(recorder.status),
		}
		if tenantCtx, ok := auth.FromContext(r.Context()); ok {
			attrs = append(attrs,
				"tenant_id", tenantCtx.Tenant.ID,
				"subject", tenantCtx.Identity.Subject,
				"auth_method", tenantCtx.Identity.AuthMethod,
			)
		}
		attrs = append(attrs, requestResourceAttrs(r.URL.Path)...)
		log.Log(r.Context(), httpLogLevel(recorder.status), "http request completed", attrs...)
	})
}

func (rt *Router) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (rt *Router) handleRuntimeHealth(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requirePermission(w, r, auth.PermissionAdminInspect) {
		return
	}
	health, err := rt.service.RuntimeHealth(r.Context(), tenantCtx.Tenant.ID)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, health)
}

func (rt *Router) handleRuntimeInfo(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = tenantCtx
	writeJSON(w, http.StatusOK, model.RuntimeInfo{Backend: rt.service.RuntimeBackend()})
}

func (rt *Router) handleRuntimeCapacity(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requirePermission(w, r, auth.PermissionAdminInspect) {
		return
	}
	report, err := rt.service.CapacityReport(r.Context(), tenantCtx.Tenant.ID)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (rt *Router) handleMetrics(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requirePermission(w, r, auth.PermissionAdminInspect) {
		return
	}
	metrics, err := rt.service.MetricsReport(r.Context(), tenantCtx.Tenant.ID)
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = io.WriteString(w, metrics)
}

func (rt *Router) handleQuota(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	view, err := rt.service.GetTenantQuotaView(r.Context(), tenantCtx.Tenant.ID)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (rt *Router) handleSandboxes(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodPost:
		if !requirePermission(w, r, auth.PermissionSandboxLifecycle) {
			return
		}
		var req model.CreateSandboxRequest
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sandbox, err := rt.service.CreateSandbox(r.Context(), tenantCtx.Tenant, tenantCtx.Quota, req)
		if err != nil {
			handleError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, sandbox)
	case http.MethodGet:
		if !requirePermission(w, r, auth.PermissionSandboxRead) {
			return
		}
		sandboxes, err := rt.service.ListSandboxes(r.Context(), tenantCtx.Tenant.ID)
		if err != nil {
			handleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sandboxes)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (rt *Router) handleSandboxRoutes(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/sandboxes/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	sandboxID := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			if !requirePermission(w, r, auth.PermissionSandboxRead) {
				return
			}
			sandbox, err := rt.service.GetSandbox(r.Context(), tenantCtx.Tenant.ID, sandboxID)
			if err != nil {
				handleError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, sandbox)
		case http.MethodDelete:
			if !requirePermission(w, r, auth.PermissionSandboxLifecycle) {
				return
			}
			if err := rt.service.DeleteSandbox(r.Context(), tenantCtx.Tenant.ID, sandboxID, false); err != nil {
				handleError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	switch parts[1] {
	case "start":
		if !requirePermission(w, r, auth.PermissionSandboxLifecycle) {
			return
		}
		rt.handleLifecycle(w, r, func(ctx context.Context) (model.Sandbox, error) {
			return rt.service.StartSandbox(ctx, tenantCtx.Tenant.ID, sandboxID, tenantCtx.Quota)
		})
	case "stop":
		if !requirePermission(w, r, auth.PermissionSandboxLifecycle) {
			return
		}
		var req model.LifecycleRequest
		_ = decodeJSON(r, &req)
		rt.handleLifecycle(w, r, func(ctx context.Context) (model.Sandbox, error) {
			return rt.service.StopSandbox(ctx, tenantCtx.Tenant.ID, sandboxID, req.Force)
		})
	case "suspend":
		if !requirePermission(w, r, auth.PermissionSandboxLifecycle) {
			return
		}
		rt.handleLifecycle(w, r, func(ctx context.Context) (model.Sandbox, error) {
			return rt.service.SuspendSandbox(ctx, tenantCtx.Tenant.ID, sandboxID)
		})
	case "resume":
		if !requirePermission(w, r, auth.PermissionSandboxLifecycle) {
			return
		}
		rt.handleLifecycle(w, r, func(ctx context.Context) (model.Sandbox, error) {
			return rt.service.ResumeSandbox(ctx, tenantCtx.Tenant.ID, sandboxID, tenantCtx.Quota)
		})
	case "exec":
		if !requirePermission(w, r, auth.PermissionExecRun) {
			return
		}
		rt.handleExec(w, r, tenantCtx, sandboxID)
	case "tty":
		if !requirePermission(w, r, auth.PermissionTTYAttach) {
			return
		}
		rt.handleTTY(w, r, tenantCtx, sandboxID)
	case "files":
		if !requireFilePermission(w, r) {
			return
		}
		rt.handleFiles(w, r, tenantCtx.Tenant.ID, sandboxID, strings.Join(parts[2:], "/"))
	case "mkdir":
		if !requirePermission(w, r, auth.PermissionFilesWrite) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req model.MkdirRequest
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := rt.service.Mkdir(r.Context(), tenantCtx.Tenant.ID, sandboxID, req.Path); err != nil {
			handleError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "tunnels":
		if len(parts) > 2 {
			if !requirePermission(w, r, auth.PermissionTunnelsWrite) {
				return
			}
		} else if r.Method == http.MethodGet {
			if !requirePermission(w, r, auth.PermissionTunnelsRead) {
				return
			}
		} else if r.Method == http.MethodPost {
			if !requirePermission(w, r, auth.PermissionTunnelsWrite) {
				return
			}
		}
		if len(parts) > 2 {
			if r.Method != http.MethodDelete {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if err := rt.service.RevokeTunnel(r.Context(), tenantCtx.Tenant.ID, parts[2]); err != nil {
				handleError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		rt.handleTunnels(w, r, tenantCtx.Tenant.ID, sandboxID)
	case "snapshots":
		switch r.Method {
		case http.MethodPost:
			if !requirePermission(w, r, auth.PermissionSnapshotsWrite) {
				return
			}
			var req model.CreateSnapshotRequest
			if err := decodeJSON(r, &req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			snapshot, err := rt.service.CreateSnapshot(r.Context(), tenantCtx.Tenant.ID, sandboxID, req)
			if err != nil {
				handleError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, snapshot)
		case http.MethodGet:
			if !requirePermission(w, r, auth.PermissionSnapshotsRead) {
				return
			}
			snapshots, err := rt.service.ListSnapshots(r.Context(), tenantCtx.Tenant.ID, sandboxID)
			if err != nil {
				handleError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, snapshots)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

func (rt *Router) handleLifecycle(w http.ResponseWriter, r *http.Request, fn func(context.Context) (model.Sandbox, error)) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sandbox, err := fn(r.Context())
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sandbox)
}

func (rt *Router) handleExec(w http.ResponseWriter, r *http.Request, tenantCtx auth.TenantContext, sandboxID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req model.ExecRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Timeout == 0 {
		req.Timeout = 5 * time.Minute
	}
	if r.URL.Query().Get("stream") == "1" {
		rt.streamExec(w, r, tenantCtx, sandboxID, req)
		return
	}
	execution, err := rt.service.ExecSandbox(r.Context(), tenantCtx.Tenant, tenantCtx.Quota, sandboxID, req, io.Discard, io.Discard)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, execution)
}

func (rt *Router) streamExec(w http.ResponseWriter, r *http.Request, tenantCtx auth.TenantContext, sandboxID string, req model.ExecRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	stdout := &sseWriter{w: w, event: "stdout"}
	stderr := &sseWriter{w: w, event: "stderr"}
	execution, err := rt.service.ExecSandbox(r.Context(), tenantCtx.Tenant, tenantCtx.Quota, sandboxID, req, stdout, stderr)
	if err != nil {
		handleError(w, err)
		return
	}
	_, _ = fmt.Fprintf(w, "event: result\ndata: %s\n\n", mustJSON(execution))
	flusher.Flush()
}

func (rt *Router) handleTTY(w http.ResponseWriter, r *http.Request, tenantCtx auth.TenantContext, sandboxID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	conn, err := rt.upgrader.Upgrade(w, r, nil)
	if err != nil {
		handleError(w, err)
		return
	}
	defer conn.Close()
	var req model.TTYRequest
	if err := conn.ReadJSON(&req); err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("invalid tty init payload"))
		return
	}
	_, session, handle, err := rt.service.CreateTTYSession(r.Context(), tenantCtx.Tenant.ID, sandboxID, req)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
		return
	}
	defer handle.Close()
	defer rt.service.CloseTTYSession(r.Context(), tenantCtx.Tenant.ID, session.ID)

	errCh := make(chan error, 2)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := handle.Reader().Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
					errCh <- writeErr
					return
				}
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()
	go func() {
		for {
			messageType, payload, err := conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if messageType == websocket.TextMessage && strings.HasPrefix(string(payload), "{") {
				var resize struct {
					Type string `json:"type"`
					Rows int    `json:"rows"`
					Cols int    `json:"cols"`
				}
				if json.Unmarshal(payload, &resize) == nil && resize.Type == "resize" {
					err = handle.Resize(model.ResizeRequest{Rows: resize.Rows, Cols: resize.Cols})
					if err == nil {
						_ = rt.service.UpdateTTYResize(r.Context(), tenantCtx.Tenant.ID, session.ID, resize.Rows, resize.Cols)
					}
					if err != nil {
						errCh <- err
						return
					}
					continue
				}
			}
			if _, err := handle.Writer().Write(payload); err != nil {
				errCh <- err
				return
			}
		}
	}()
	<-errCh
}

func (rt *Router) handleFiles(w http.ResponseWriter, r *http.Request, tenantID, sandboxID, path string) {
	switch r.Method {
	case http.MethodGet:
		content, err := rt.service.ReadFile(r.Context(), tenantID, sandboxID, path, r.URL.Query().Get("encoding"))
		if err != nil {
			handleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, content)
	case http.MethodPut:
		var req model.FileWriteRequest
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var err error
		encoding := strings.TrimSpace(req.Encoding)
		contentBase64 := strings.TrimSpace(req.ContentBase64)
		if strings.EqualFold(encoding, "base64") {
			if contentBase64 == "" {
				http.Error(w, "content_base64 is required when encoding=base64", http.StatusBadRequest)
				return
			}
			data, decodeErr := base64.StdEncoding.DecodeString(contentBase64)
			if decodeErr != nil {
				http.Error(w, "invalid content_base64 payload", http.StatusBadRequest)
				return
			}
			err = rt.service.WriteFileBytes(r.Context(), tenantID, sandboxID, path, data)
		} else if contentBase64 != "" {
			data, decodeErr := base64.StdEncoding.DecodeString(contentBase64)
			if decodeErr != nil {
				http.Error(w, "invalid content_base64 payload", http.StatusBadRequest)
				return
			}
			err = rt.service.WriteFileBytes(r.Context(), tenantID, sandboxID, path, data)
		} else {
			err = rt.service.WriteFile(r.Context(), tenantID, sandboxID, path, req.Content)
		}
		if err != nil {
			handleError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := rt.service.DeleteFile(r.Context(), tenantID, sandboxID, path); err != nil {
			handleError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (rt *Router) handleTunnels(w http.ResponseWriter, r *http.Request, tenantID, sandboxID string) {
	switch r.Method {
	case http.MethodPost:
		var req model.CreateTunnelRequest
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tunnel, err := rt.service.CreateTunnel(r.Context(), tenantID, sandboxID, req)
		if err != nil {
			handleError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, tunnel)
	case http.MethodGet:
		tunnels, err := rt.service.ListTunnels(r.Context(), tenantID, sandboxID)
		if err != nil {
			handleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, tunnels)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (rt *Router) handleSnapshotRoutes(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/snapshots/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requirePermission(w, r, auth.PermissionSnapshotsRead) {
			return
		}
		snapshot, err := rt.service.GetSnapshot(r.Context(), tenantCtx.Tenant.ID, parts[0])
		if err != nil {
			handleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	if len(parts) < 2 || parts[1] != "restore" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if !requirePermission(w, r, auth.PermissionSnapshotsWrite) {
		return
	}
	var req model.RestoreSnapshotRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sandbox, err := rt.service.RestoreSnapshot(r.Context(), tenantCtx.Tenant.ID, parts[0], req)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sandbox)
}

func (rt *Router) handleTunnelRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/tunnels/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	tunnelID := parts[0]
	if len(parts) == 1 {
		tenantCtx, ok := auth.FromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requirePermission(w, r, auth.PermissionTunnelsWrite) {
			return
		}
		if err := rt.service.RevokeTunnel(r.Context(), tenantCtx.Tenant.ID, tunnelID); err != nil {
			handleError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if parts[1] == "proxy" {
		rt.handleTunnelProxy(w, r, tunnelID)
		return
	}
	if parts[1] == "signed-url" {
		rt.handleTunnelSignedURL(w, r, tunnelID)
		return
	}
	http.NotFound(w, r)
}

func (rt *Router) handleTunnelSignedURL(w http.ResponseWriter, r *http.Request, tunnelID string) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requirePermission(w, r, auth.PermissionTunnelsRead) {
		return
	}
	tunnel, _, err := rt.service.GetTunnelForProxy(r.Context(), tunnelID)
	if err != nil {
		handleError(w, err)
		return
	}
	requesterTenantID := tenantCtx.Tenant.ID
	if tunnel.RevokedAt != nil {
		rt.recordTunnelDenial(r.Context(), requesterTenantID, tunnel, "tunnel.signed_url", "reason=revoked")
		http.Error(w, "tunnel revoked", http.StatusGone)
		return
	}
	if requesterTenantID != tunnel.TenantID {
		rt.recordTunnelDenial(r.Context(), requesterTenantID, tunnel, "tunnel.signed_url", "reason=tenant_mismatch")
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var req model.CreateTunnelSignedURLRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	path := req.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		http.Error(w, "signed tunnel path must start with '/'", http.StatusBadRequest)
		return
	}
	ttl := tunnelSignedURLDefaultTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	if ttl <= 0 {
		http.Error(w, "signed tunnel ttl must be positive", http.StatusBadRequest)
		return
	}
	if ttl > tunnelSignedURLMaxTTL {
		http.Error(w, fmt.Sprintf("signed tunnel ttl must be <= %s", tunnelSignedURLMaxTTL), http.StatusBadRequest)
		return
	}
	expiresAt := time.Now().UTC().Add(ttl)
	expiry := strconv.FormatInt(expiresAt.Unix(), 10)
	sig := rt.signTunnelCapability(tunnel.ID, expiry)
	signedURL, err := rt.buildTunnelProxyURL(tunnel.ID, path, url.Values{
		tunnelSignedURLExpiryKey: []string{expiry},
		tunnelSignedURLSigKey:    []string{sig},
	}, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	auditPath := sanitizeTunnelAuditPath(path)
	rt.service.RecordAuditEvent(r.Context(), tunnel.TenantID, tunnel.SandboxID, "tunnel.signed_url", tunnel.ID, "ok", fmt.Sprintf("path=%q expires_at=%s ttl_seconds=%d", auditPath, expiresAt.UTC().Format(time.RFC3339), int(ttl.Seconds())))
	rt.log.Info("tunnel signed url issued",
		"event", "tunnel.signed_url",
		"tenant_id", tunnel.TenantID,
		"sandbox_id", tunnel.SandboxID,
		"tunnel_id", tunnel.ID,
		"path", auditPath,
		"expires_at", expiresAt.UTC().Format(time.RFC3339),
		"ttl_seconds", int(ttl.Seconds()),
		"outcome", "ok",
	)
	writeJSON(w, http.StatusOK, model.TunnelSignedURL{URL: signedURL, ExpiresAt: expiresAt})
}

func (rt *Router) handleTunnelProxy(w http.ResponseWriter, r *http.Request, tunnelID string) {
	tunnel, sandbox, err := rt.service.GetTunnelForProxy(r.Context(), tunnelID)
	if err != nil {
		handleError(w, err)
		return
	}
	tenantCtx, hasTenant := auth.FromContext(r.Context())
	requesterTenantID := ""
	if hasTenant {
		requesterTenantID = tenantCtx.Tenant.ID
	}
	if tunnel.RevokedAt != nil {
		rt.recordTunnelDenial(r.Context(), requesterTenantID, tunnel, "tunnel.proxy", "reason=revoked")
		http.Error(w, "tunnel revoked", http.StatusGone)
		return
	}
	browserAuthorized, bootstrapped := rt.authorizeTunnelBrowserSession(w, r, tunnel)
	if bootstrapped {
		return
	}
	if tunnel.Visibility != "public" {
		if (!hasTenant || tenantCtx.Tenant.ID != tunnel.TenantID) && !browserAuthorized {
			rt.recordTunnelDenial(r.Context(), requesterTenantID, tunnel, "tunnel.proxy", fmt.Sprintf("reason=tenant_mismatch visibility=%s", tunnel.Visibility))
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	}
	queryAccessToken := ""
	authSource := "none"
	if tunnel.AuthMode == "token" {
		authorized := browserAuthorized
		presented := r.Header.Get("X-Tunnel-Token")
		if browserAuthorized {
			authSource = "signed_cookie"
		}
		if presented == "" {
			presented = r.URL.Query().Get("token")
			queryAccessToken = presented
			if presented != "" {
				authSource = "query"
			}
		} else {
			authSource = "header"
		}
		if !authorized && presented != "" && config.HashToken(presented) == tunnel.AuthSecretHash {
			authorized = true
		}
		if !authorized {
			rt.recordTunnelDenial(r.Context(), requesterTenantID, tunnel, "tunnel.proxy", fmt.Sprintf("reason=token_auth_failed visibility=%s", tunnel.Visibility))
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if browserAuthorized {
		authSource = "signed_cookie"
	} else if hasTenant && tenantCtx.Tenant.ID == tunnel.TenantID {
		authSource = "tenant_bearer"
	} else if tunnel.Visibility == "public" {
		authSource = "public"
	}
	rt.log.Info("tunnel proxy authorized",
		"event", "tunnel.proxy",
		"tenant_id", tunnel.TenantID,
		"sandbox_id", tunnel.SandboxID,
		"tunnel_id", tunnel.ID,
		"visibility", tunnel.Visibility,
		"auth_mode", tunnel.AuthMode,
		"auth_source", authSource,
		"method", r.Method,
		"path", r.URL.Path,
		"outcome", "ok",
	)
	if websocket.IsWebSocketUpgrade(r) {
		rt.handleTunnelWebSocket(w, r, tunnel, sandbox, queryAccessToken)
		return
	}
	rt.handleTunnelHTTPRequest(w, r, tunnel, sandbox, queryAccessToken)
}

func (rt *Router) handleTunnelHTTPRequest(w http.ResponseWriter, r *http.Request, tunnel model.Tunnel, sandbox model.Sandbox, queryAccessToken string) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	bridgeConn, err := rt.service.OpenSandboxLocalConn(ctx, sandbox, tunnel.TargetPort)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	conn := bridgeConn
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	proxyReq := r.Clone(ctx)
	proxyReq.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1/tunnels/"+tunnel.ID+"/proxy")
	proxyReq.URL.RawQuery = tunnelUpstreamQuery(r.URL.Query(), queryAccessToken).Encode()
	proxyReq.RequestURI = ""
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("sandbox-local:%d", tunnel.TargetPort)}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out = pr.Out.WithContext(ctx)
			pr.Out.URL.Path = proxyReq.URL.Path
			pr.Out.URL.RawPath = proxyReq.URL.RawPath
			pr.Out.URL.RawQuery = proxyReq.URL.RawQuery
			pr.Out.Host = ""
			pr.SetXForwarded()
			sanitizeTunnelProxyRequest(pr.Out.Header)
		},
		Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				if conn == nil {
					return nil, fmt.Errorf("sandbox tunnel bridge already used")
				}
				used := conn
				conn = nil
				return used, nil
			},
			DisableKeepAlives:     true,
			ForceAttemptHTTP2:     false,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, err.Error(), http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, proxyReq)
}

func (rt *Router) handleTunnelWebSocket(w http.ResponseWriter, r *http.Request, tunnel model.Tunnel, sandbox model.Sandbox, queryAccessToken string) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	bridgeConn, err := rt.service.OpenSandboxLocalConn(ctx, sandbox, tunnel.TargetPort)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	upstreamURL := url.URL{
		Scheme:   "ws",
		Host:     fmt.Sprintf("127.0.0.1:%d", tunnel.TargetPort),
		Path:     strings.TrimPrefix(r.URL.Path, "/v1/tunnels/"+tunnel.ID+"/proxy"),
		RawQuery: tunnelUpstreamQuery(r.URL.Query(), queryAccessToken).Encode(),
	}
	requestHeader := http.Header{}
	if origin := r.Header.Get("Origin"); origin != "" {
		requestHeader.Set("Origin", origin)
	}
	if userAgent := r.Header.Get("User-Agent"); userAgent != "" {
		requestHeader.Set("User-Agent", userAgent)
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		Subprotocols:     websocket.Subprotocols(r),
		NetDialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return bridgeConn, nil
		},
	}
	upstreamConn, response, err := dialer.DialContext(ctx, upstreamURL.String(), requestHeader)
	if err != nil {
		_ = bridgeConn.Close()
		status := http.StatusBadGateway
		if response != nil {
			status = response.StatusCode
		}
		http.Error(w, err.Error(), status)
		return
	}
	responseHeader := http.Header{}
	if subprotocol := upstreamConn.Subprotocol(); subprotocol != "" {
		responseHeader.Set("Sec-WebSocket-Protocol", subprotocol)
	}
	clientConn, err := rt.upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		_ = upstreamConn.Close()
		return
	}
	defer clientConn.Close()
	defer upstreamConn.Close()
	errCh := make(chan error, 2)
	go proxyWebSocketMessages(clientConn, upstreamConn, errCh)
	go proxyWebSocketMessages(upstreamConn, clientConn, errCh)
	<-errCh
}

func (rt *Router) authorizeTunnelBrowserSession(w http.ResponseWriter, r *http.Request, tunnel model.Tunnel) (authorized bool, bootstrapped bool) {
	if cookie, err := r.Cookie(tunnelAuthCookieName); err == nil {
		if expiry, sig, ok := parseTunnelAuthCookie(cookie.Value); ok && rt.validateTunnelCapability(tunnel.ID, expiry, sig) {
			return true, false
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false, false
	}
	expiry := r.URL.Query().Get(tunnelSignedURLExpiryKey)
	sig := r.URL.Query().Get(tunnelSignedURLSigKey)
	if !rt.validateTunnelCapability(tunnel.ID, expiry, sig) {
		return false, false
	}
	expiresAt, _ := strconv.ParseInt(expiry, 10, 64)
	http.SetCookie(w, &http.Cookie{
		Name:     tunnelAuthCookieName,
		Value:    tunnelAuthCookieValue(expiry, sig),
		Path:     "/v1/tunnels/" + tunnel.ID + "/proxy",
		Expires:  time.Unix(expiresAt, 0).UTC(),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(strings.ToLower(rt.operatorHost), "https://") || r.TLS != nil,
	})
	redirectURL := *r.URL
	query := redirectURL.Query()
	query.Del(tunnelSignedURLExpiryKey)
	query.Del(tunnelSignedURLSigKey)
	redirectURL.RawQuery = query.Encode()
	rt.serveTunnelBootstrapPage(w, redirectURL.String())
	return false, true
}

// serveTunnelBootstrapPage serves a small HTML page that clears stale gateway
// settings from localStorage and then redirects the browser to redirectURL.
// The JavaScript redirect preserves the URL fragment (e.g. #token=...) which
// a 302 redirect cannot guarantee.
//
// Background: browser-based apps behind the tunnel proxy (e.g. OpenClaw) may
// store the gateway WebSocket URL in localStorage.  When the tunnel is
// recreated the stored URL points at the old (revoked) tunnel and the
// WebSocket connection fails.  By clearing the stored gatewayUrl before the
// app boots, the app falls back to deriving it from the current page URL.
func (rt *Router) serveTunnelBootstrapPage(w http.ResponseWriter, redirectURL string) {
	urlJSON, _ := json.Marshal(redirectURL)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Loading…</title>
<script>
try{var k="openclaw.control.settings.v1",r=localStorage.getItem(k);if(r){var s=JSON.parse(r);delete s.gatewayUrl;localStorage.setItem(k,JSON.stringify(s))}}catch(e){}
window.location.replace(%s+window.location.hash);
</script></head><body><noscript><a href=%s>Continue</a></noscript></body></html>`, urlJSON, urlJSON)
}

func (rt *Router) buildTunnelProxyURL(tunnelID, path string, query url.Values, r *http.Request) (string, error) {
	base := rt.operatorHost
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid operator host: %w", err)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/tunnels/" + tunnelID + "/proxy" + path
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (rt *Router) signTunnelCapability(tunnelID, expiry string) string {
	mac := hmac.New(sha256.New, rt.tunnelSigningKey)
	_, _ = io.WriteString(mac, tunnelID)
	_, _ = io.WriteString(mac, ":")
	_, _ = io.WriteString(mac, expiry)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (rt *Router) validateTunnelCapability(tunnelID, expiry, signature string) bool {
	if strings.TrimSpace(expiry) == "" || strings.TrimSpace(signature) == "" {
		return false
	}
	expiresAt, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil {
		return false
	}
	if time.Now().UTC().After(time.Unix(expiresAt, 0).UTC()) {
		return false
	}
	expected := rt.signTunnelCapability(tunnelID, expiry)
	return hmac.Equal([]byte(expected), []byte(signature))
}

func newTunnelSigningKey(cfg config.Config) []byte {
	if key := strings.TrimSpace(cfg.TunnelSigningKey); key != "" {
		sum := sha256.Sum256([]byte(key))
		return sum[:]
	}
	if path := strings.TrimSpace(cfg.TunnelSigningKeyPath); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
				sum := sha256.Sum256([]byte(trimmed))
				return sum[:]
			}
		}
	}
	seed := stableTunnelSigningSeed(cfg)
	sum := sha256.Sum256(seed)
	return sum[:]
}

func stableTunnelSigningSeed(cfg config.Config) []byte {
	var builder strings.Builder
	builder.WriteString("or3-sandbox-tunnel-signing-key\n")
	builder.WriteString("auth_mode=")
	builder.WriteString(cfg.AuthMode)
	builder.WriteString("\n")
	switch cfg.AuthMode {
	case "jwt-hs256":
		builder.WriteString("issuer=")
		builder.WriteString(cfg.AuthJWTIssuer)
		builder.WriteString("\n")
		builder.WriteString("audience=")
		builder.WriteString(cfg.AuthJWTAudience)
		builder.WriteString("\n")
		paths := append([]string(nil), cfg.AuthJWTSecretPaths...)
		sort.Strings(paths)
		for _, path := range paths {
			builder.WriteString("jwt_secret=")
			if data, err := os.ReadFile(path); err == nil {
				builder.Write(data)
			}
			builder.WriteString("\n")
		}
	default:
		tenants := append([]config.TenantConfig(nil), cfg.Tenants...)
		sort.Slice(tenants, func(i, j int) bool {
			if tenants[i].ID == tenants[j].ID {
				return tenants[i].Token < tenants[j].Token
			}
			return tenants[i].ID < tenants[j].ID
		})
		for _, tenant := range tenants {
			builder.WriteString("tenant=")
			builder.WriteString(tenant.ID)
			builder.WriteString(":")
			builder.WriteString(tenant.Token)
			builder.WriteString("\n")
		}
	}
	return []byte(builder.String())
}

func tunnelUpstreamQuery(query url.Values, queryAccessToken string) url.Values {
	filtered := url.Values{}
	for key, values := range query {
		switch key {
		case tunnelSignedURLExpiryKey, tunnelSignedURLSigKey:
			continue
		case "token":
			preserved := make([]string, 0, len(values))
			for _, value := range values {
				if queryAccessToken != "" && value == queryAccessToken {
					continue
				}
				preserved = append(preserved, value)
			}
			if len(preserved) > 0 {
				filtered[key] = preserved
			}
		default:
			filtered[key] = append([]string(nil), values...)
		}
	}
	return filtered
}

func sanitizeTunnelProxyRequest(header http.Header) {
	header.Del("Authorization")
	header.Del("X-Tunnel-Token")
	if cookies := header.Values("Cookie"); len(cookies) > 0 {
		filteredCookies := make([]string, 0, len(cookies))
		for _, cookieHeader := range cookies {
			parts := strings.Split(cookieHeader, ";")
			kept := make([]string, 0, len(parts))
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed == "" || strings.HasPrefix(trimmed, tunnelAuthCookieName+"=") {
					continue
				}
				kept = append(kept, trimmed)
			}
			if len(kept) > 0 {
				filteredCookies = append(filteredCookies, strings.Join(kept, "; "))
			}
		}
		header.Del("Cookie")
		for _, cookieHeader := range filteredCookies {
			header.Add("Cookie", cookieHeader)
		}
	}
}

func tunnelAuthCookieValue(expiry, sig string) string {
	return expiry + "." + sig
}

func parseTunnelAuthCookie(value string) (expiry string, sig string, ok bool) {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func proxyWebSocketMessages(src, dst *websocket.Conn, errCh chan<- error) {
	for {
		messageType, payload, err := src.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		if err := dst.WriteMessage(messageType, payload); err != nil {
			errCh <- err
			return
		}
	}
}

func decodeJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, repository.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func requirePermission(w http.ResponseWriter, r *http.Request, permission string) bool {
	if err := auth.Require(r.Context(), permission); err != nil {
		handleError(w, err)
		return false
	}
	return true
}

func requireFilePermission(w http.ResponseWriter, r *http.Request) bool {
	permission := auth.PermissionFilesRead
	switch r.Method {
	case http.MethodPut, http.MethodDelete, http.MethodPost:
		permission = auth.PermissionFilesWrite
	}
	return requirePermission(w, r, permission)
}

func (rt *Router) recordTunnelDenial(ctx context.Context, requesterTenantID string, tunnel model.Tunnel, action, detail string) {
	logAttrs := []any{
		"event", action,
		"tenant_id", tunnel.TenantID,
		"sandbox_id", tunnel.SandboxID,
		"tunnel_id", tunnel.ID,
		"outcome", "denied",
		"detail", detail,
	}
	if requesterTenantID != "" {
		logAttrs = append(logAttrs, "requester_tenant_id", requesterTenantID)
	}
	rt.log.Warn("tunnel access denied", logAttrs...)
	if requesterTenantID == tunnel.TenantID {
		rt.service.RecordAuditEvent(ctx, tunnel.TenantID, tunnel.SandboxID, action, tunnel.ID, "denied", detail)
	}
}

func sanitizeTunnelAuditPath(path string) string {
	parsed, err := url.Parse(path)
	if err != nil {
		return path
	}
	sanitized := parsed.EscapedPath()
	if sanitized == "" {
		sanitized = parsed.Path
	}
	if sanitized == "" {
		return "/"
	}
	return sanitized
}

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (r *responseRecorder) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := r.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

func httpOutcome(status int) string {
	switch {
	case status >= 500:
		return "error"
	case status >= 400:
		return "denied"
	default:
		return "ok"
	}
}

func httpLogLevel(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

func requestResourceAttrs(path string) []any {
	switch {
	case strings.HasPrefix(path, "/v1/sandboxes/"):
		parts := strings.Split(strings.TrimPrefix(path, "/v1/sandboxes/"), "/")
		if len(parts) > 0 && parts[0] != "" {
			return []any{"sandbox_id", parts[0]}
		}
	case strings.HasPrefix(path, "/v1/snapshots/"):
		parts := strings.Split(strings.TrimPrefix(path, "/v1/snapshots/"), "/")
		if len(parts) > 0 && parts[0] != "" {
			return []any{"snapshot_id", parts[0]}
		}
	case strings.HasPrefix(path, "/v1/tunnels/"):
		parts := strings.Split(strings.TrimPrefix(path, "/v1/tunnels/"), "/")
		if len(parts) > 0 && parts[0] != "" {
			return []any{"tunnel_id", parts[0]}
		}
	}
	return nil
}

func mustJSON(payload any) string {
	data, _ := json.Marshal(payload)
	return string(data)
}

type sseWriter struct {
	w     io.Writer
	event string
}

func (s *sseWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	line := strings.ReplaceAll(string(p), "\n", "\\n")
	_, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", s.event, line)
	if flusher, ok := s.w.(http.Flusher); ok {
		flusher.Flush()
	}
	return len(p), err
}
````

## File: internal/service/service.go
````go
package service

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/guestimage"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
)

const (
	previewLimit                           = 64 * 1024
	defaultReconcileStorageRefreshInterval = 5 * time.Minute
)

type Service struct {
	cfg     config.Config
	store   *repository.Store
	runtime model.RuntimeManager
	log     *slog.Logger
}

type workspaceFileRuntime interface {
	ReadWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string) (model.FileReadResponse, error)
	WriteWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string, content string) error
	DeleteWorkspacePath(ctx context.Context, sandbox model.Sandbox, relativePath string) error
	MkdirWorkspace(ctx context.Context, sandbox model.Sandbox, relativePath string) error
}

type workspaceBinaryFileRuntime interface {
	ReadWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string) ([]byte, error)
	WriteWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string, content []byte) error
}

type storageMeasurer interface {
	MeasureStorage(ctx context.Context, sandbox model.Sandbox) (model.StorageUsage, error)
}

func New(cfg config.Config, store *repository.Store, runtime model.RuntimeManager, logs ...*slog.Logger) *Service {
	log := slog.Default()
	if len(logs) > 0 && logs[0] != nil {
		log = logs[0]
	}
	return &Service{cfg: cfg, store: store, runtime: runtime, log: log}
}

func (s *Service) CreateSandbox(ctx context.Context, tenant model.Tenant, quota model.TenantQuota, req model.CreateSandboxRequest) (model.Sandbox, error) {
	req = s.applyCreateDefaults(req)
	if err := validateCreate(req); err != nil {
		return model.Sandbox{}, err
	}
	var err error
	req, contract, err := s.validateRuntimeCreate(req)
	if err != nil {
		return model.Sandbox{}, err
	}
	if err := s.enforceCreatePolicy(ctx, tenant.ID, req); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.checkQuota(ctx, tenant.ID, quota, req); err != nil {
		return model.Sandbox{}, err
	}
	if req.Start {
		if err := s.checkRunningQuota(ctx, tenant.ID, quota); err != nil {
			return model.Sandbox{}, err
		}
	}
	id := newID("sbx-")
	storageRoot := filepath.Join(s.cfg.StorageRoot, id, "rootfs")
	workspaceRoot := filepath.Join(s.cfg.StorageRoot, id, "workspace")
	cacheRoot := filepath.Join(s.cfg.StorageRoot, id, "cache")
	for _, dir := range []string{storageRoot, workspaceRoot, cacheRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return model.Sandbox{}, err
		}
	}
	now := time.Now().UTC()
	sandbox := model.Sandbox{
		ID:                       id,
		TenantID:                 tenant.ID,
		Status:                   model.SandboxStatusCreating,
		RuntimeBackend:           s.cfg.RuntimeBackend,
		BaseImageRef:             req.BaseImageRef,
		Profile:                  req.Profile,
		Features:                 model.NormalizeFeatures(req.Features),
		Capabilities:             append([]string(nil), contract.Capabilities...),
		ControlMode:              contract.Control.Mode,
		ControlProtocolVersion:   contract.Control.ProtocolVersion,
		WorkspaceContractVersion: contract.WorkspaceContractVersion,
		ImageContractVersion:     contract.ContractVersion,
		CPULimit:                 req.CPULimit,
		MemoryLimitMB:            req.MemoryLimitMB,
		PIDsLimit:                req.PIDsLimit,
		DiskLimitMB:              req.DiskLimitMB,
		NetworkMode:              req.NetworkMode,
		AllowTunnels:             *req.AllowTunnels,
		StorageRoot:              storageRoot,
		WorkspaceRoot:            workspaceRoot,
		CacheRoot:                cacheRoot,
		RuntimeID:                id,
		RuntimeStatus:            string(model.SandboxStatusCreating),
		CreatedAt:                now,
		UpdatedAt:                now,
		LastActiveAt:             now,
	}
	if err := s.store.CreateSandbox(ctx, sandbox); err != nil {
		_ = os.RemoveAll(filepath.Join(s.cfg.StorageRoot, id))
		return model.Sandbox{}, err
	}
	spec := model.SandboxSpec{
		SandboxID:                sandbox.ID,
		TenantID:                 sandbox.TenantID,
		BaseImageRef:             sandbox.BaseImageRef,
		Profile:                  sandbox.Profile,
		Features:                 append([]string(nil), sandbox.Features...),
		Capabilities:             append([]string(nil), sandbox.Capabilities...),
		ControlMode:              sandbox.ControlMode,
		ControlProtocolVersion:   sandbox.ControlProtocolVersion,
		WorkspaceContractVersion: sandbox.WorkspaceContractVersion,
		ImageContractVersion:     sandbox.ImageContractVersion,
		CPULimit:                 sandbox.CPULimit,
		MemoryLimitMB:            sandbox.MemoryLimitMB,
		PIDsLimit:                sandbox.PIDsLimit,
		DiskLimitMB:              sandbox.DiskLimitMB,
		NetworkMode:              sandbox.NetworkMode,
		AllowTunnels:             sandbox.AllowTunnels,
		StorageRoot:              sandbox.StorageRoot,
		WorkspaceRoot:            sandbox.WorkspaceRoot,
		CacheRoot:                sandbox.CacheRoot,
	}
	state, err := s.runtime.Create(ctx, spec)
	if err != nil {
		return model.Sandbox{}, s.rollbackFailedCreate(ctx, tenant.ID, sandbox, "runtime_create", req.Start, err)
	}
	if req.Start {
		state, err = s.runtime.Start(ctx, sandbox)
		if err != nil {
			return model.Sandbox{}, s.rollbackFailedCreate(ctx, tenant.ID, sandbox, "runtime_start", true, err)
		}
		sandbox.Status = model.SandboxStatusRunning
	} else {
		sandbox.Status = model.SandboxStatusStopped
	}
	sandbox.RuntimeStatus = string(state.Status)
	sandbox.UpdatedAt = time.Now().UTC()
	sandbox.LastActiveAt = sandbox.UpdatedAt
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.store.UpdateRuntimeState(ctx, sandbox.ID, state); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.refreshStorage(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	s.recordAudit(ctx, tenant.ID, sandbox.ID, "sandbox.create", sandbox.ID, "ok", auditDetail(
		auditKV("runtime", sandbox.RuntimeBackend),
		auditKV("base_image_ref", sandbox.BaseImageRef),
		auditKV("start_requested", req.Start),
		auditKV("allow_tunnels", sandbox.AllowTunnels),
	))
	return s.store.GetSandbox(ctx, tenant.ID, sandbox.ID)
}

func (s *Service) rollbackFailedCreate(ctx context.Context, tenantID string, sandbox model.Sandbox, stage string, startRequested bool, cause error) error {
	persistCtx := context.WithoutCancel(ctx)
	cleanupErr := s.runtime.Destroy(persistCtx, sandbox)
	if cleanupErr == nil {
		cleanupErr = os.RemoveAll(filepath.Join(s.cfg.StorageRoot, sandbox.ID))
	}
	now := time.Now().UTC()
	sandbox.LastRuntimeError = cause.Error()
	sandbox.UpdatedAt = now
	sandbox.LastActiveAt = now
	if cleanupErr == nil {
		sandbox.Status = model.SandboxStatusDeleted
		sandbox.RuntimeStatus = string(model.SandboxStatusDeleted)
		sandbox.DeletedAt = &now
	} else {
		sandbox.Status = model.SandboxStatusError
		sandbox.RuntimeStatus = string(model.SandboxStatusError)
	}
	_ = s.store.UpdateSandboxState(persistCtx, sandbox)
	s.recordAudit(persistCtx, tenantID, sandbox.ID, "sandbox.create", sandbox.ID, "error", auditDetail(
		auditKV("stage", stage),
		auditKV("start_requested", startRequested),
		auditKV("rollback", cleanupErr == nil),
	), "error", cause)
	if cleanupErr != nil {
		s.log.Error("failed create cleanup", "event", "sandbox.create.rollback", "sandbox_id", sandbox.ID, "stage", stage, "error", cleanupErr)
	}
	return cause
}

func (s *Service) GetSandbox(ctx context.Context, tenantID, sandboxID string) (model.Sandbox, error) {
	return s.store.GetSandbox(ctx, tenantID, sandboxID)
}

func (s *Service) RuntimeBackend() string {
	return s.cfg.RuntimeBackend
}

func (s *Service) ListSandboxes(ctx context.Context, tenantID string) ([]model.Sandbox, error) {
	return s.store.ListSandboxes(ctx, tenantID)
}

func (s *Service) GetTenantQuotaView(ctx context.Context, tenantID string) (TenantQuotaView, error) {
	quota, err := s.store.GetQuota(ctx, tenantID)
	if err != nil {
		return TenantQuotaView{}, err
	}
	usage, err := s.store.TenantUsage(ctx, tenantID)
	if err != nil {
		return TenantQuotaView{}, err
	}
	return buildTenantQuotaView(quota, usage), nil
}

func (s *Service) RuntimeHealth(ctx context.Context, tenantID string) (model.RuntimeHealth, error) {
	if err := s.enforceAdminInspectionPolicy(ctx, tenantID, "runtime.inspect"); err != nil {
		return model.RuntimeHealth{}, err
	}
	health := model.RuntimeHealth{
		Backend:      s.cfg.RuntimeBackend,
		Healthy:      true,
		CheckedAt:    time.Now().UTC(),
		StatusCounts: make(map[string]int),
	}
	var sandboxes []model.Sandbox
	var err error
	if tenantID != "" {
		sandboxes, err = s.store.ListNonDeletedSandboxesByTenant(ctx, tenantID)
	} else {
		sandboxes, err = s.store.ListNonDeletedSandboxes(ctx)
	}
	if err != nil {
		return health, err
	}
	for _, sandbox := range sandboxes {
		entry := model.RuntimeSandboxHealth{
			SandboxID:       sandbox.ID,
			TenantID:        sandbox.TenantID,
			PersistedStatus: sandbox.Status,
			ObservedStatus:  sandbox.Status,
			RuntimeID:       sandbox.RuntimeID,
			RuntimeStatus:   sandbox.RuntimeStatus,
			Error:           sandbox.LastRuntimeError,
		}
		state, err := s.runtime.Inspect(ctx, sandbox)
		if err != nil {
			entry.ObservedStatus = model.SandboxStatusDegraded
			entry.RuntimeStatus = string(model.SandboxStatusDegraded)
			entry.Error = err.Error()
			health.Healthy = false
		} else {
			entry.ObservedStatus = state.Status
			entry.RuntimeID = state.RuntimeID
			entry.RuntimeStatus = string(state.Status)
			entry.Pid = state.Pid
			entry.IPAddress = state.IPAddress
			entry.Error = state.Error
			if state.Status == model.SandboxStatusError || state.Status == model.SandboxStatusDegraded {
				health.Healthy = false
			}
		}
		health.StatusCounts[string(entry.ObservedStatus)]++
		health.Sandboxes = append(health.Sandboxes, entry)
	}
	return health, nil
}

func (s *Service) StartSandbox(ctx context.Context, tenantID, sandboxID string, quota model.TenantQuota) (model.Sandbox, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Sandbox{}, err
	}
	if err := s.enforceLifecyclePolicy(ctx, sandbox, "start"); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.checkRunningQuota(ctx, tenantID, quota); err != nil {
		return model.Sandbox{}, err
	}
	return s.transitionSandbox(ctx, tenantID, sandboxID, "sandbox.start", model.SandboxStatusStarting, func(sandbox model.Sandbox) (model.RuntimeState, model.SandboxStatus, error) {
		state, err := s.runtime.Start(ctx, sandbox)
		return state, model.SandboxStatusRunning, err
	}, model.SandboxStatusRunning)
}

func (s *Service) StopSandbox(ctx context.Context, tenantID, sandboxID string, force bool) (model.Sandbox, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Sandbox{}, err
	}
	sandbox.Status = model.SandboxStatusStopping
	sandbox.RuntimeStatus = string(model.SandboxStatusStopping)
	sandbox.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	state, err := s.runtime.Stop(ctx, sandbox, force)
	if err != nil {
		sandbox.Status = model.SandboxStatusError
		sandbox.RuntimeStatus = string(model.SandboxStatusError)
		sandbox.LastRuntimeError = err.Error()
		sandbox.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdateSandboxState(ctx, sandbox)
		s.recordAudit(ctx, tenantID, sandbox.ID, "sandbox.stop", sandbox.ID, "error", auditDetail(
			auditKV("force", force),
			auditKV("requested_status", model.SandboxStatusStopped),
		), "error", err)
		return model.Sandbox{}, err
	}
	sandbox.Status = model.SandboxStatusStopped
	sandbox.RuntimeStatus = string(state.Status)
	sandbox.UpdatedAt = time.Now().UTC()
	sandbox.LastActiveAt = sandbox.UpdatedAt
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.store.UpdateRuntimeState(ctx, sandbox.ID, state); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.refreshStorage(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	s.recordAudit(ctx, tenantID, sandbox.ID, "sandbox.stop", sandbox.ID, "ok", auditDetail(
		auditKV("force", force),
		auditKV("result_status", sandbox.Status),
	))
	return s.store.GetSandbox(ctx, tenantID, sandbox.ID)
}

func (s *Service) SuspendSandbox(ctx context.Context, tenantID, sandboxID string) (model.Sandbox, error) {
	return s.transitionSandbox(ctx, tenantID, sandboxID, "sandbox.suspend", model.SandboxStatusSuspending, func(sandbox model.Sandbox) (model.RuntimeState, model.SandboxStatus, error) {
		state, err := s.runtime.Suspend(ctx, sandbox)
		return state, model.SandboxStatusSuspended, err
	}, model.SandboxStatusSuspended)
}

func (s *Service) ResumeSandbox(ctx context.Context, tenantID, sandboxID string, quota model.TenantQuota) (model.Sandbox, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Sandbox{}, err
	}
	if err := s.enforceLifecyclePolicy(ctx, sandbox, "resume"); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.checkRunningQuota(ctx, tenantID, quota); err != nil {
		return model.Sandbox{}, err
	}
	return s.transitionSandbox(ctx, tenantID, sandboxID, "sandbox.resume", model.SandboxStatusStarting, func(sandbox model.Sandbox) (model.RuntimeState, model.SandboxStatus, error) {
		state, err := s.runtime.Resume(ctx, sandbox)
		return state, model.SandboxStatusRunning, err
	}, model.SandboxStatusRunning)
}

func (s *Service) DeleteSandbox(ctx context.Context, tenantID, sandboxID string, preserveSnapshots bool) error {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return err
	}
	tunnels, err := s.store.ListTunnels(ctx, tenantID, sandboxID)
	if err != nil {
		return err
	}
	for _, tunnel := range tunnels {
		if tunnel.RevokedAt == nil {
			if err := s.store.RevokeTunnel(ctx, tenantID, tunnel.ID); err != nil {
				return err
			}
			s.recordAudit(ctx, tenantID, sandbox.ID, "tunnel.revoke", tunnel.ID, "ok", auditDetail(
				auditKV("reason", "sandbox_delete"),
				tunnelAuditDetail(tunnel),
			))
		}
	}
	if sandbox.Status == model.SandboxStatusRunning || sandbox.Status == model.SandboxStatusSuspended {
		if _, err := s.StopSandbox(ctx, tenantID, sandboxID, true); err != nil {
			return err
		}
		sandbox, _ = s.store.GetSandbox(ctx, tenantID, sandboxID)
	}
	sandbox.Status = model.SandboxStatusDeleting
	sandbox.RuntimeStatus = string(model.SandboxStatusDeleting)
	sandbox.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return err
	}
	if err := s.runtime.Destroy(ctx, sandbox); err != nil {
		sandbox.Status = model.SandboxStatusError
		sandbox.RuntimeStatus = string(model.SandboxStatusError)
		sandbox.LastRuntimeError = err.Error()
		sandbox.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdateSandboxState(ctx, sandbox)
		s.recordAudit(ctx, tenantID, sandbox.ID, "sandbox.delete", sandbox.ID, "error", auditDetail(
			auditKV("preserve_snapshots", preserveSnapshots),
		), "error", err)
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.cfg.StorageRoot, sandbox.ID)); err != nil {
		return err
	}
	if !preserveSnapshots {
		_ = os.RemoveAll(filepath.Join(s.cfg.SnapshotRoot, sandbox.ID))
	}
	now := time.Now().UTC()
	sandbox.Status = model.SandboxStatusDeleted
	sandbox.RuntimeStatus = string(model.SandboxStatusDeleted)
	sandbox.UpdatedAt = now
	sandbox.LastActiveAt = now
	sandbox.DeletedAt = &now
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return err
	}
	s.recordAudit(ctx, tenantID, sandbox.ID, "sandbox.delete", sandbox.ID, "ok", auditDetail(
		auditKV("preserve_snapshots", preserveSnapshots),
	))
	return nil
}

func (s *Service) ExecSandbox(ctx context.Context, tenant model.Tenant, quota model.TenantQuota, sandboxID string, req model.ExecRequest, stdout, stderr io.Writer) (model.Execution, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenant.ID, sandboxID)
	if err != nil {
		return model.Execution{}, err
	}
	if sandbox.Status != model.SandboxStatusRunning {
		return model.Execution{}, fmt.Errorf("sandbox %s is not running", sandbox.ID)
	}
	if err := s.enforceLifecyclePolicy(ctx, sandbox, "exec"); err != nil {
		return model.Execution{}, err
	}
	usage, err := s.store.TenantUsage(ctx, tenant.ID)
	if err != nil {
		return model.Execution{}, err
	}
	if usage.ConcurrentExecs >= quota.MaxConcurrentExecs {
		return model.Execution{}, fmt.Errorf("tenant exec quota exceeded")
	}
	id := newID("exec-")
	started := time.Now().UTC()
	execution := model.Execution{
		ID:             id,
		SandboxID:      sandbox.ID,
		TenantID:       tenant.ID,
		Command:        strings.Join(req.Command, " "),
		Cwd:            req.Cwd,
		TimeoutSeconds: int(req.Timeout.Seconds()),
		Status:         model.ExecutionStatusRunning,
		StartedAt:      started,
	}
	if execution.TimeoutSeconds == 0 && req.Timeout > 0 {
		execution.TimeoutSeconds = 1
	}
	if err := s.store.CreateExecution(ctx, execution); err != nil {
		return model.Execution{}, err
	}
	stdoutCapture := &boundedBuffer{limit: previewLimit}
	stderrCapture := &boundedBuffer{limit: previewLimit}
	streams := model.ExecStreams{
		Stdout: io.MultiWriter(stdoutCapture, stdout),
		Stderr: io.MultiWriter(stderrCapture, stderr),
	}
	handle, err := s.runtime.Exec(ctx, sandbox, req, streams)
	if err != nil {
		persistCtx := context.WithoutCancel(ctx)
		now := time.Now().UTC()
		exitCode := 1
		durationMS := now.Sub(started).Milliseconds()
		execution.Status = model.ExecutionStatusFailed
		execution.ExitCode = &exitCode
		execution.StderrPreview = err.Error()
		execution.CompletedAt = &now
		execution.DurationMS = &durationMS
		_ = s.store.UpdateExecution(persistCtx, execution)
		s.recordAudit(persistCtx, tenant.ID, sandbox.ID, "sandbox.exec", execution.ID, "error", execAuditDetail(req), "error", err)
		return model.Execution{}, err
	}
	persistCtx := context.WithoutCancel(ctx)
	if req.Detached {
		now := time.Now().UTC()
		durationMS := now.Sub(started).Milliseconds()
		execution.Status = model.ExecutionStatusDetached
		execution.CompletedAt = &now
		execution.DurationMS = &durationMS
		if err := s.store.UpdateExecution(persistCtx, execution); err != nil {
			return model.Execution{}, err
		}
		s.recordAudit(persistCtx, tenant.ID, sandbox.ID, "sandbox.exec.detached", execution.ID, "ok", execAuditDetail(req))
		return execution, nil
	}
	result := handle.Wait()
	execution.Status = result.Status
	exitCode := result.ExitCode
	execution.ExitCode = &exitCode
	execution.StdoutPreview = stdoutCapture.String()
	execution.StderrPreview = stderrCapture.String()
	execution.StdoutTruncated = stdoutCapture.truncated || result.StdoutTruncated
	execution.StderrTruncated = stderrCapture.truncated || result.StderrTruncated
	completed := result.CompletedAt.UTC()
	durationMS := result.Duration.Milliseconds()
	execution.CompletedAt = &completed
	execution.DurationMS = &durationMS
	if err := s.store.UpdateExecution(persistCtx, execution); err != nil {
		return model.Execution{}, err
	}
	_ = s.touchSandboxActivity(persistCtx, sandbox)
	s.recordAudit(persistCtx, tenant.ID, sandbox.ID, "sandbox.exec", execution.ID, string(execution.Status), execAuditDetail(req))
	return execution, nil
}

func (s *Service) CreateTTYSession(ctx context.Context, tenantID, sandboxID string, req model.TTYRequest) (model.Sandbox, model.TTYSession, model.TTYHandle, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Sandbox{}, model.TTYSession{}, nil, err
	}
	if sandbox.Status != model.SandboxStatusRunning {
		return model.Sandbox{}, model.TTYSession{}, nil, fmt.Errorf("sandbox %s is not running", sandbox.ID)
	}
	if err := s.enforceLifecyclePolicy(ctx, sandbox, "tty"); err != nil {
		return model.Sandbox{}, model.TTYSession{}, nil, err
	}
	handle, err := s.runtime.AttachTTY(ctx, sandbox, req)
	if err != nil {
		return model.Sandbox{}, model.TTYSession{}, nil, err
	}
	session := model.TTYSession{
		ID:         newID("tty-"),
		SandboxID:  sandbox.ID,
		TenantID:   tenantID,
		Command:    strings.Join(req.Command, " "),
		Connected:  true,
		LastResize: fmt.Sprintf("%dx%d", req.Cols, req.Rows),
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.store.CreateTTYSession(ctx, session); err != nil {
		_ = handle.Close()
		return model.Sandbox{}, model.TTYSession{}, nil, err
	}
	_ = s.touchSandboxActivity(ctx, sandbox)
	return sandbox, session, handle, nil
}

func (s *Service) CloseTTYSession(ctx context.Context, tenantID, sessionID string) error {
	return s.store.CloseTTYSession(ctx, tenantID, sessionID)
}

func (s *Service) UpdateTTYResize(ctx context.Context, tenantID, sessionID string, rows, cols int) error {
	return s.store.UpdateTTYResize(ctx, tenantID, sessionID, fmt.Sprintf("%dx%d", cols, rows))
}

func (s *Service) ReadFile(ctx context.Context, tenantID, sandboxID, path, encoding string) (model.FileReadResponse, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	relativePath, err := cleanWorkspaceRelativePath(path)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	if strings.EqualFold(strings.TrimSpace(encoding), "base64") {
		data, err := s.readWorkspaceBytes(ctx, sandbox, relativePath)
		if err != nil {
			return model.FileReadResponse{}, err
		}
		_ = s.touchSandboxActivity(ctx, sandbox)
		return model.FileReadResponse{Path: relativePath, ContentBase64: base64.StdEncoding.EncodeToString(data), Size: int64(len(data)), Encoding: "base64"}, nil
	}
	if runtime, ok := s.runtime.(workspaceFileRuntime); ok {
		file, err := runtime.ReadWorkspaceFile(ctx, sandbox, relativePath)
		if err == nil {
			_ = s.touchSandboxActivity(ctx, sandbox)
		}
		return file, err
	}
	target, err := resolveWorkspacePath(sandbox.WorkspaceRoot, relativePath)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	_ = s.touchSandboxActivity(ctx, sandbox)
	return model.FileReadResponse{Path: relativePath, Content: string(data), Size: int64(len(data)), Encoding: "utf-8"}, nil
}

func (s *Service) WriteFile(ctx context.Context, tenantID, sandboxID, path string, content string) error {
	return s.WriteFileBytes(ctx, tenantID, sandboxID, path, []byte(content))
}

func (s *Service) WriteFileBytes(ctx context.Context, tenantID, sandboxID, path string, content []byte) error {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return err
	}
	relativePath, err := cleanWorkspaceRelativePath(path)
	if err != nil {
		return err
	}
	if runtime, ok := s.runtime.(workspaceBinaryFileRuntime); ok {
		if err := runtime.WriteWorkspaceFileBytes(ctx, sandbox, relativePath, content); err != nil {
			return err
		}
	} else if runtime, ok := s.runtime.(workspaceFileRuntime); ok {
		if !utf8.Valid(content) {
			return fmt.Errorf("binary file write is unsupported for runtime %q", sandbox.RuntimeBackend)
		}
		if err := runtime.WriteWorkspaceFile(ctx, sandbox, relativePath, string(content)); err != nil {
			return err
		}
	} else {
		target, err := resolveWorkspacePath(sandbox.WorkspaceRoot, relativePath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return err
		}
	}
	s.recordAudit(ctx, tenantID, sandboxID, "file.write", relativePath, "ok", "file written")
	_ = s.touchSandboxActivity(ctx, sandbox)
	return s.refreshStorage(ctx, sandbox)
}

func (s *Service) readWorkspaceBytes(ctx context.Context, sandbox model.Sandbox, relativePath string) ([]byte, error) {
	if runtime, ok := s.runtime.(workspaceBinaryFileRuntime); ok {
		return runtime.ReadWorkspaceFileBytes(ctx, sandbox, relativePath)
	}
	if runtime, ok := s.runtime.(workspaceFileRuntime); ok {
		file, err := runtime.ReadWorkspaceFile(ctx, sandbox, relativePath)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(file.Encoding, "base64") && strings.TrimSpace(file.ContentBase64) != "" {
			return base64.StdEncoding.DecodeString(file.ContentBase64)
		}
		return []byte(file.Content), nil
	}
	target, err := resolveWorkspacePath(sandbox.WorkspaceRoot, relativePath)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(target)
}

func (s *Service) DeleteFile(ctx context.Context, tenantID, sandboxID, path string) error {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return err
	}
	relativePath, err := cleanWorkspaceRelativePath(path)
	if err != nil {
		return err
	}
	if runtime, ok := s.runtime.(workspaceFileRuntime); ok {
		if err := runtime.DeleteWorkspacePath(ctx, sandbox, relativePath); err != nil {
			return err
		}
	} else {
		target, err := resolveWorkspacePath(sandbox.WorkspaceRoot, relativePath)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	s.recordAudit(ctx, tenantID, sandboxID, "file.delete", relativePath, "ok", "path deleted")
	_ = s.touchSandboxActivity(ctx, sandbox)
	return s.refreshStorage(ctx, sandbox)
}

func (s *Service) Mkdir(ctx context.Context, tenantID, sandboxID, path string) error {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return err
	}
	relativePath, err := cleanWorkspaceRelativePath(path)
	if err != nil {
		return err
	}
	if runtime, ok := s.runtime.(workspaceFileRuntime); ok {
		if err := runtime.MkdirWorkspace(ctx, sandbox, relativePath); err != nil {
			return err
		}
	} else {
		target, err := resolveWorkspacePath(sandbox.WorkspaceRoot, relativePath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
	}
	s.recordAudit(ctx, tenantID, sandboxID, "file.mkdir", relativePath, "ok", "directory created")
	_ = s.touchSandboxActivity(ctx, sandbox)
	return s.refreshStorage(ctx, sandbox)
}

func (s *Service) CreateTunnel(ctx context.Context, tenantID, sandboxID string, req model.CreateTunnelRequest) (model.Tunnel, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Tunnel{}, err
	}
	if !sandbox.AllowTunnels {
		s.recordAudit(ctx, tenantID, sandboxID, "tunnel.create", sandboxID, "denied", auditKV("reason", "sandbox_tunnel_policy_denied"))
		return model.Tunnel{}, fmt.Errorf("sandbox does not allow tunnels")
	}
	usage, err := s.store.TenantUsage(ctx, tenantID)
	if err != nil {
		return model.Tunnel{}, err
	}
	quota, err := s.store.GetQuota(ctx, tenantID)
	if err == nil && !quota.AllowTunnels {
		s.recordAudit(ctx, tenantID, sandboxID, "tunnel.create", sandboxID, "denied", auditKV("reason", "tenant_tunnel_policy_denied"))
		return model.Tunnel{}, fmt.Errorf("tenant tunnel policy denied")
	}
	if err == nil && usage.ActiveTunnels >= quota.MaxTunnels {
		s.recordAudit(ctx, tenantID, sandboxID, "tunnel.create", sandboxID, "denied", auditKV("reason", "tunnel_quota_exceeded"))
		return model.Tunnel{}, fmt.Errorf("tunnel quota exceeded")
	}
	id := newID("tun-")
	if req.TargetPort < 1 || req.TargetPort > 65535 {
		return model.Tunnel{}, fmt.Errorf("target_port must be between 1 and 65535")
	}
	if req.Protocol == "" {
		req.Protocol = model.TunnelProtocolHTTP
	}
	if req.Protocol != model.TunnelProtocolHTTP {
		return model.Tunnel{}, fmt.Errorf("unsupported tunnel protocol %q", req.Protocol)
	}
	if req.AuthMode == "" && err == nil {
		req.AuthMode = quota.DefaultTunnelAuthMode
	}
	if req.AuthMode == "" {
		req.AuthMode = "token"
	}
	if req.AuthMode != "token" && req.AuthMode != "none" {
		return model.Tunnel{}, fmt.Errorf("unsupported auth_mode %q", req.AuthMode)
	}
	if req.Visibility == "" && err == nil {
		req.Visibility = quota.DefaultTunnelVisibility
	}
	if req.Visibility == "" {
		req.Visibility = "private"
	}
	if req.Visibility != "private" && req.Visibility != "public" {
		return model.Tunnel{}, fmt.Errorf("unsupported visibility %q", req.Visibility)
	}
	if err := s.enforceTunnelPolicy(ctx, sandbox, req); err != nil {
		return model.Tunnel{}, err
	}
	accessToken := ""
	tunnel := model.Tunnel{
		ID:         id,
		SandboxID:  sandbox.ID,
		TenantID:   tenantID,
		TargetPort: req.TargetPort,
		Protocol:   req.Protocol,
		AuthMode:   req.AuthMode,
		Visibility: req.Visibility,
		Endpoint:   strings.TrimRight(s.cfg.OperatorHost, "/") + "/v1/tunnels/" + id + "/proxy",
		CreatedAt:  time.Now().UTC(),
	}
	if tunnel.AuthMode == "token" {
		accessToken = newID("ttok-")
		tunnel.AccessToken = accessToken
		tunnel.AuthSecretHash = config.HashToken(accessToken)
	}
	if err := s.store.CreateTunnel(ctx, tunnel); err != nil {
		return model.Tunnel{}, err
	}
	_ = s.touchSandboxActivity(ctx, sandbox)
	s.recordAudit(ctx, tenantID, sandboxID, "tunnel.create", tunnel.ID, "ok", tunnelAuditDetail(tunnel))
	return tunnel, nil
}

func (s *Service) ListTunnels(ctx context.Context, tenantID, sandboxID string) ([]model.Tunnel, error) {
	return s.store.ListTunnels(ctx, tenantID, sandboxID)
}

func (s *Service) RevokeTunnel(ctx context.Context, tenantID, tunnelID string) error {
	tunnel, err := s.store.GetTunnel(ctx, tenantID, tunnelID)
	if err != nil {
		return err
	}
	if err := s.store.RevokeTunnel(ctx, tenantID, tunnelID); err != nil {
		return err
	}
	s.recordAudit(ctx, tenantID, tunnel.SandboxID, "tunnel.revoke", tunnelID, "ok", tunnelAuditDetail(tunnel))
	return nil
}

func (s *Service) GetTunnel(ctx context.Context, tenantID, tunnelID string) (model.Tunnel, model.Sandbox, error) {
	tunnel, err := s.store.GetTunnel(ctx, tenantID, tunnelID)
	if err != nil {
		return model.Tunnel{}, model.Sandbox{}, err
	}
	sandbox, err := s.store.GetSandbox(ctx, tenantID, tunnel.SandboxID)
	if err != nil {
		return model.Tunnel{}, model.Sandbox{}, err
	}
	return tunnel, sandbox, nil
}

func (s *Service) GetTunnelForProxy(ctx context.Context, tunnelID string) (model.Tunnel, model.Sandbox, error) {
	tunnel, err := s.store.GetTunnelByID(ctx, tunnelID)
	if err != nil {
		return model.Tunnel{}, model.Sandbox{}, err
	}
	sandbox, err := s.store.GetSandbox(ctx, tunnel.TenantID, tunnel.SandboxID)
	if err != nil {
		return model.Tunnel{}, model.Sandbox{}, err
	}
	return tunnel, sandbox, nil
}

func (s *Service) CreateSnapshot(ctx context.Context, tenantID, sandboxID string, req model.CreateSnapshotRequest) (model.Snapshot, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Snapshot{}, err
	}
	if sandbox.RuntimeBackend == "qemu" && sandbox.Status != model.SandboxStatusStopped {
		return model.Snapshot{}, fmt.Errorf("qemu snapshots require the sandbox to be stopped")
	}
	snapshot := model.Snapshot{
		ID:                     newID("snap-"),
		SandboxID:              sandbox.ID,
		TenantID:               tenantID,
		Name:                   req.Name,
		Status:                 model.SnapshotStatusCreating,
		Profile:                sandbox.Profile,
		ImageContractVersion:   sandbox.ImageContractVersion,
		ControlProtocolVersion: sandbox.ControlProtocolVersion,
		CreatedAt:              time.Now().UTC(),
	}
	if snapshot.Name == "" {
		snapshot.Name = snapshot.ID
	}
	if err := s.store.CreateSnapshot(ctx, snapshot); err != nil {
		return model.Snapshot{}, err
	}
	snapshotDir := filepath.Join(s.cfg.SnapshotRoot, sandbox.ID, snapshot.ID)
	stage := "persist"
	failSnapshot := func(cause error) (model.Snapshot, error) {
		snapshot.Status = model.SnapshotStatusError
		_ = os.RemoveAll(snapshotDir)
		_ = s.store.UpdateSnapshot(ctx, snapshot)
		s.recordAudit(ctx, tenantID, sandboxID, "snapshot.create", snapshot.ID, "error", auditDetail(
			auditKV("stage", stage),
			auditKV("name", snapshot.Name),
		), "error", cause)
		return snapshot, cause
	}
	stage = "runtime_create"
	info, err := s.runtime.CreateSnapshot(ctx, sandbox, snapshot.ID)
	if err != nil {
		return failSnapshot(err)
	}
	stage = "mkdir_snapshot_dir"
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return failSnapshot(err)
	}
	if info.ImageRef != "" {
		if looksLikeFilesystemPath(info.ImageRef) {
			stage = "validate_rootfs_artifact"
			if !isReadableFile(info.ImageRef) {
				return failSnapshot(fmt.Errorf("snapshot image artifact is not readable: %s", info.ImageRef))
			}
			targetImage := filepath.Join(snapshotDir, "rootfs.img")
			if info.ImageRef != targetImage {
				stage = "copy_rootfs_artifact"
				if err := copyFile(targetImage, info.ImageRef); err != nil {
					return failSnapshot(err)
				}
			}
			snapshot.ImageRef = targetImage
			if info.ImageRef != targetImage {
				_ = os.Remove(info.ImageRef)
			}
		} else {
			snapshot.ImageRef = info.ImageRef
		}
	} else {
		snapshot.ImageRef = info.ImageRef
	}
	if info.WorkspaceTar != "" {
		targetTar := filepath.Join(snapshotDir, "workspace.img")
		if info.WorkspaceTar != targetTar {
			stage = "copy_workspace_artifact"
			if err := copyFile(targetTar, info.WorkspaceTar); err != nil {
				return failSnapshot(err)
			}
		}
		snapshot.WorkspaceTar = targetTar
		if info.WorkspaceTar != targetTar {
			_ = os.Remove(info.WorkspaceTar)
		}
	} else {
		snapshot.WorkspaceTar = info.WorkspaceTar
	}
	snapshot.Status = model.SnapshotStatusReady
	completed := time.Now().UTC()
	snapshot.CompletedAt = &completed
	if s.cfg.OptionalSnapshotExport != "" {
		stage = "export_bundle"
		exportLocation, err := s.exportSnapshotBundle(ctx, sandbox, snapshot)
		if err != nil {
			return failSnapshot(err)
		}
		snapshot.ExportLocation = exportLocation
	}
	stage = "persist_ready"
	if err := s.store.UpdateSnapshot(ctx, snapshot); err != nil {
		return failSnapshot(err)
	}
	stage = "refresh_storage"
	if err := s.refreshStorage(ctx, sandbox); err != nil {
		return failSnapshot(err)
	}
	s.recordAudit(ctx, tenantID, sandboxID, "snapshot.create", snapshot.ID, "ok", snapshotAuditDetail(snapshot))
	return snapshot, nil
}

func (s *Service) ListSnapshots(ctx context.Context, tenantID, sandboxID string) ([]model.Snapshot, error) {
	if _, err := s.store.GetSandbox(ctx, tenantID, sandboxID); err != nil {
		return nil, err
	}
	return s.store.ListSnapshots(ctx, tenantID, sandboxID)
}

func (s *Service) GetSnapshot(ctx context.Context, tenantID, snapshotID string) (model.Snapshot, error) {
	return s.store.GetSnapshot(ctx, tenantID, snapshotID)
}

func (s *Service) RestoreSnapshot(ctx context.Context, tenantID, snapshotID string, req model.RestoreSnapshotRequest) (model.Sandbox, error) {
	snapshot, err := s.store.GetSnapshot(ctx, tenantID, snapshotID)
	if err != nil {
		return model.Sandbox{}, err
	}
	sandbox, err := s.store.GetSandbox(ctx, tenantID, req.TargetSandboxID)
	if err != nil {
		return model.Sandbox{}, err
	}
	forcedStop := false
	if sandbox.Status == model.SandboxStatusRunning || sandbox.Status == model.SandboxStatusSuspended {
		forcedStop = true
		if _, err := s.StopSandbox(ctx, tenantID, sandbox.ID, true); err != nil {
			s.recordAudit(ctx, tenantID, sandbox.ID, "snapshot.restore", snapshot.ID, "error", auditDetail(
				auditKV("stage", "stop_target"),
				auditKV("target_sandbox_id", sandbox.ID),
				auditKV("forced_stop", true),
			), "error", err)
			return model.Sandbox{}, err
		}
		sandbox, _ = s.store.GetSandbox(ctx, tenantID, sandbox.ID)
	}
	if err := s.ensureSnapshotArtifacts(ctx, sandbox, snapshot); err != nil {
		s.recordAudit(ctx, tenantID, sandbox.ID, "snapshot.restore", snapshot.ID, "error", auditDetail(
			auditKV("stage", "ensure_artifacts"),
			auditKV("target_sandbox_id", sandbox.ID),
			auditKV("forced_stop", forcedStop),
		), "error", err)
		return model.Sandbox{}, err
	}
	state, err := s.runtime.RestoreSnapshot(ctx, sandbox, snapshot)
	if err != nil {
		s.recordAudit(ctx, tenantID, sandbox.ID, "snapshot.restore", snapshot.ID, "error", auditDetail(
			auditKV("stage", "runtime_restore"),
			auditKV("target_sandbox_id", sandbox.ID),
			auditKV("forced_stop", forcedStop),
		), "error", err)
		return model.Sandbox{}, err
	}
	sandbox.Status = model.SandboxStatusStopped
	sandbox.RuntimeStatus = string(state.Status)
	if sandbox.RuntimeBackend != "qemu" {
		sandbox.BaseImageRef = snapshot.ImageRef
	} else {
		sandbox.Profile = snapshot.Profile
		sandbox.ImageContractVersion = snapshot.ImageContractVersion
		sandbox.ControlProtocolVersion = snapshot.ControlProtocolVersion
	}
	sandbox.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.store.UpdateRuntimeState(ctx, sandbox.ID, state); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.refreshStorage(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	s.recordAudit(ctx, tenantID, sandbox.ID, "snapshot.restore", snapshot.ID, "ok", auditDetail(
		auditKV("target_sandbox_id", sandbox.ID),
		auditKV("forced_stop", forcedStop),
		snapshotAuditDetail(snapshot),
	))
	return s.store.GetSandbox(ctx, tenantID, sandbox.ID)
}

func (s *Service) Reconcile(ctx context.Context) error {
	var reconcileErr error
	if err := s.reconcileOrphanedExecutions(ctx); err != nil {
		return err
	}
	if err := s.reconcileIncompleteSnapshots(ctx); err != nil {
		return err
	}
	sandboxes, err := s.store.ListNonDeletedSandboxes(ctx)
	if err != nil {
		return err
	}
	for _, sandbox := range sandboxes {
		previousStatus := sandbox.Status
		previousRuntimeStatus := sandbox.RuntimeStatus
		previousRuntimeID := sandbox.RuntimeID
		previousRuntimeError := sandbox.LastRuntimeError
		state, err := s.runtime.Inspect(ctx, sandbox)
		if err != nil {
			sandbox.Status = model.SandboxStatusDegraded
			sandbox.RuntimeStatus = string(model.SandboxStatusDegraded)
			sandbox.LastRuntimeError = err.Error()
			sandbox.UpdatedAt = time.Now().UTC()
			if updateErr := s.store.UpdateSandboxState(ctx, sandbox); updateErr != nil {
				reconcileErr = errors.Join(reconcileErr, updateErr)
			}
			s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "sandbox.reconcile", sandbox.ID, "error", auditDetail(
				auditKV("previous_status", previousStatus),
				auditKV("result_status", sandbox.Status),
				auditKV("reason", "inspect_failed"),
			), "error", err)
			continue
		}
		switch {
		case state.Status == model.SandboxStatusBooting:
			sandbox.Status = model.SandboxStatusBooting
		case state.Status == model.SandboxStatusDegraded:
			sandbox.Status = model.SandboxStatusDegraded
		case state.Status == model.SandboxStatusRunning:
			sandbox.Status = model.SandboxStatusRunning
		case state.Status == model.SandboxStatusStopped:
			sandbox.Status = model.SandboxStatusStopped
		case state.Status == model.SandboxStatusSuspended:
			sandbox.Status = model.SandboxStatusSuspended
		default:
			sandbox.Status = state.Status
		}
		sandbox.RuntimeStatus = string(state.Status)
		sandbox.RuntimeID = state.RuntimeID
		sandbox.LastRuntimeError = state.Error
		sandbox.UpdatedAt = time.Now().UTC()
		if updateErr := s.store.UpdateSandboxState(ctx, sandbox); updateErr != nil {
			reconcileErr = errors.Join(reconcileErr, updateErr)
		}
		if updateErr := s.store.UpdateRuntimeState(ctx, sandbox.ID, state); updateErr != nil {
			reconcileErr = errors.Join(reconcileErr, updateErr)
		}
		stateChanged := sandbox.Status != previousStatus || sandbox.RuntimeStatus != previousRuntimeStatus || sandbox.RuntimeID != previousRuntimeID || sandbox.LastRuntimeError != previousRuntimeError
		if stateChanged {
			s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "sandbox.reconcile", sandbox.ID, "ok", auditDetail(
				auditKV("previous_status", previousStatus),
				auditKV("result_status", sandbox.Status),
				auditKV("runtime_status", sandbox.RuntimeStatus),
			))
		}
		if updateErr := s.refreshStorageIfStale(ctx, sandbox, stateChanged); updateErr != nil {
			reconcileErr = errors.Join(reconcileErr, updateErr)
		}
	}
	return reconcileErr
}

func (s *Service) reconcileOrphanedExecutions(ctx context.Context) error {
	executions, err := s.store.ListRunningExecutions(ctx)
	if err != nil {
		return err
	}
	for _, execution := range executions {
		now := time.Now().UTC()
		exitCode := 1
		durationMS := now.Sub(execution.StartedAt).Milliseconds()
		execution.Status = model.ExecutionStatusCanceled
		execution.ExitCode = &exitCode
		execution.StderrPreview = "control plane restarted during execution"
		execution.CompletedAt = &now
		execution.DurationMS = &durationMS
		if err := s.store.UpdateExecution(ctx, execution); err != nil {
			return err
		}
		s.recordAudit(ctx, execution.TenantID, execution.SandboxID, "sandbox.exec.reconcile", execution.ID, "canceled", executionAuditDetail(execution))
	}
	return nil
}

func (s *Service) reconcileIncompleteSnapshots(ctx context.Context) error {
	snapshots, err := s.store.ListSnapshotsByStatus(ctx, model.SnapshotStatusCreating)
	if err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		snapshot.Status = model.SnapshotStatusError
		if err := s.store.UpdateSnapshot(ctx, snapshot); err != nil {
			return err
		}
		s.recordAudit(ctx, snapshot.TenantID, snapshot.SandboxID, "snapshot.reconcile", snapshot.ID, "error", "control plane restarted during snapshot creation")
	}
	return nil
}

func (s *Service) touchSandboxActivity(ctx context.Context, sandbox model.Sandbox) error {
	now := time.Now().UTC()
	sandbox.LastActiveAt = now
	sandbox.UpdatedAt = now
	return s.store.UpdateSandboxState(ctx, sandbox)
}

func (s *Service) refreshStorage(ctx context.Context, sandbox model.Sandbox) error {
	if runtime, ok := s.runtime.(storageMeasurer); ok {
		usage, err := runtime.MeasureStorage(ctx, sandbox)
		if err != nil {
			return err
		}
		snapshotExportBytes, _ := dirSize(filepath.Join(s.cfg.SnapshotRoot, sandbox.ID))
		usage.SnapshotBytes += snapshotExportBytes
		return s.store.UpdateStorageUsage(ctx, sandbox.ID, usage.RootfsBytes, usage.WorkspaceBytes, usage.CacheBytes, usage.SnapshotBytes)
	}
	rootfsBytes, _ := dirSize(sandbox.StorageRoot)
	workspaceBytes, _ := dirSize(sandbox.WorkspaceRoot)
	cacheBytes, _ := dirSize(sandbox.CacheRoot)
	snapshotBytes, _ := dirSize(filepath.Join(s.cfg.SnapshotRoot, sandbox.ID))
	return s.store.UpdateStorageUsage(ctx, sandbox.ID, rootfsBytes, workspaceBytes, cacheBytes, snapshotBytes)
}

func (s *Service) refreshStorageIfStale(ctx context.Context, sandbox model.Sandbox, force bool) error {
	if !force {
		updatedAt, err := s.store.StorageUsageUpdatedAt(ctx, sandbox.ID)
		switch {
		case err == nil && time.Since(updatedAt) < s.reconcileStorageRefreshInterval():
			return nil
		case err != nil && !errors.Is(err, repository.ErrNotFound):
			return err
		}
	}
	return s.refreshStorage(ctx, sandbox)
}

func (s *Service) reconcileStorageRefreshInterval() time.Duration {
	if s.cfg.CleanupInterval > 0 {
		return s.cfg.CleanupInterval
	}
	return defaultReconcileStorageRefreshInterval
}

func (s *Service) applyCreateDefaults(req model.CreateSandboxRequest) model.CreateSandboxRequest {
	if req.BaseImageRef == "" {
		if s.cfg.RuntimeBackend == "qemu" {
			req.BaseImageRef = s.cfg.QEMUBaseImagePath
		} else {
			req.BaseImageRef = s.cfg.BaseImageRef
		}
	}
	if req.Profile == "" && s.cfg.RuntimeBackend == "qemu" {
		req.Profile = model.GuestProfileCore
	}
	req.Features = model.NormalizeFeatures(req.Features)
	if req.CPULimit == 0 {
		req.CPULimit = s.cfg.DefaultCPULimit
	}
	if req.MemoryLimitMB == 0 {
		req.MemoryLimitMB = s.cfg.DefaultMemoryLimitMB
	}
	if req.PIDsLimit == 0 {
		req.PIDsLimit = s.cfg.DefaultPIDsLimit
	}
	if req.DiskLimitMB == 0 {
		req.DiskLimitMB = s.cfg.DefaultDiskLimitMB
	}
	if req.NetworkMode == "" {
		req.NetworkMode = s.cfg.DefaultNetworkMode
	}
	if req.AllowTunnels == nil {
		value := s.cfg.DefaultAllowTunnels
		req.AllowTunnels = &value
	}
	return req
}

func validateCreate(req model.CreateSandboxRequest) error {
	if req.BaseImageRef == "" {
		return errors.New("base_image_ref is required")
	}
	if req.Profile != "" && !req.Profile.IsValid() {
		return fmt.Errorf("invalid guest profile %q", req.Profile)
	}
	if req.CPULimit <= 0 || req.MemoryLimitMB <= 0 || req.PIDsLimit <= 0 || req.DiskLimitMB <= 0 {
		return errors.New("cpu, memory, pids, and disk limits must be positive")
	}
	if req.NetworkMode != model.NetworkModeInternetEnabled && req.NetworkMode != model.NetworkModeInternetDisabled {
		return fmt.Errorf("invalid network mode %q", req.NetworkMode)
	}
	return nil
}

func (s *Service) validateRuntimeCreate(req model.CreateSandboxRequest) (model.CreateSandboxRequest, guestimage.Contract, error) {
	if s.cfg.RuntimeBackend == "qemu" && req.CPULimit.MilliValue()%1000 != 0 {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("qemu runtime requires whole CPU cores until fractional throttling is implemented")
	}
	if s.cfg.RuntimeBackend != "qemu" {
		return req, guestimage.Contract{}, nil
	}
	resolved, err := s.resolveQEMUBaseImageRef(req.BaseImageRef)
	if err != nil {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, err
	}
	contract, err := guestimage.Load(resolved)
	if err != nil {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, err
	}
	if err := guestimage.Validate(resolved, contract); err != nil {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, err
	}
	profile := req.Profile
	if profile == "" {
		profile = contract.Profile
	}
	if !profile.IsValid() {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("qemu runtime requires a valid guest profile")
	}
	if profile != contract.Profile {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("guest image profile %q does not match requested profile %q", contract.Profile, profile)
	}
	if !s.cfg.IsAllowedQEMUProfile(profile) {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("guest profile %q is not allowed by policy", profile)
	}
	if s.cfg.IsDangerousQEMUProfile(profile) && !s.cfg.QEMUAllowDangerousProfiles {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("guest profile %q is blocked by policy until SANDBOX_QEMU_ALLOW_DANGEROUS_PROFILES=true", profile)
	}
	if contract.Control.Mode == model.GuestControlModeSSHCompat && !s.cfg.QEMUAllowSSHCompat {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, fmt.Errorf("ssh-compat guest images are blocked by policy until SANDBOX_QEMU_ALLOW_SSH_COMPAT=true")
	}
	if err := guestimage.RequestedFeaturesAllowed(contract, req.Features); err != nil {
		return model.CreateSandboxRequest{}, guestimage.Contract{}, err
	}
	req.BaseImageRef = resolved
	req.Profile = profile
	req.Features = model.NormalizeFeatures(req.Features)
	return req, contract, nil
}

func (s *Service) resolveQEMUBaseImageRef(value string) (string, error) {
	normalized := s.normalizeQEMUBaseImageRef(value)
	if normalized == "" {
		return "", fmt.Errorf("qemu runtime requires a guest image path")
	}
	if !isReadableFile(normalized) {
		return "", fmt.Errorf("qemu guest image path %q is not readable", normalized)
	}
	for _, allowed := range s.cfg.EffectiveQEMUAllowedBaseImagePaths() {
		if normalized == allowed {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("qemu guest image path %q is not allowed", normalized)
}

func (s *Service) normalizeQEMUBaseImageRef(value string) string {
	normalized := config.NormalizeQEMUBaseImagePath(value)
	if normalized == "" {
		return ""
	}
	if normalized == config.NormalizeQEMUBaseImagePath(s.cfg.BaseImageRef) {
		return config.NormalizeQEMUBaseImagePath(s.cfg.QEMUBaseImagePath)
	}
	return normalized
}

func (s *Service) checkQuota(ctx context.Context, tenantID string, quota model.TenantQuota, req model.CreateSandboxRequest) error {
	usage, err := s.store.TenantUsage(ctx, tenantID)
	if err != nil {
		return err
	}
	switch {
	case usage.Sandboxes >= quota.MaxSandboxes:
		return fmt.Errorf("sandbox quota exceeded")
	case usage.RequestedCPU+req.CPULimit > quota.MaxCPUCores:
		return fmt.Errorf("cpu quota exceeded")
	case usage.RequestedMemory+req.MemoryLimitMB > quota.MaxMemoryMB:
		return fmt.Errorf("memory quota exceeded")
	case usage.RequestedStorage+req.DiskLimitMB > quota.MaxStorageMB:
		return fmt.Errorf("storage quota exceeded")
	case req.AllowTunnels != nil && *req.AllowTunnels && !quota.AllowTunnels:
		return fmt.Errorf("tenant tunnel policy denied")
	}
	return nil
}

func (s *Service) checkRunningQuota(ctx context.Context, tenantID string, quota model.TenantQuota) error {
	usage, err := s.store.TenantUsage(ctx, tenantID)
	if err != nil {
		return err
	}
	if usage.RunningSandboxes >= quota.MaxRunningSandboxes {
		return fmt.Errorf("running sandbox quota exceeded")
	}
	return nil
}

func (s *Service) transitionSandbox(ctx context.Context, tenantID, sandboxID, auditAction string, transitional model.SandboxStatus, action func(model.Sandbox) (model.RuntimeState, model.SandboxStatus, error), finalStatus model.SandboxStatus) (model.Sandbox, error) {
	sandbox, err := s.store.GetSandbox(ctx, tenantID, sandboxID)
	if err != nil {
		return model.Sandbox{}, err
	}
	sandbox.Status = transitional
	sandbox.RuntimeStatus = string(transitional)
	sandbox.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	state, nextStatus, err := action(sandbox)
	if err != nil {
		sandbox.Status = model.SandboxStatusError
		sandbox.RuntimeStatus = string(model.SandboxStatusError)
		sandbox.LastRuntimeError = err.Error()
		sandbox.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdateSandboxState(ctx, sandbox)
		s.recordAudit(ctx, tenantID, sandbox.ID, auditAction, sandbox.ID, "error", auditDetail(
			auditKV("transitional_status", transitional),
			auditKV("requested_status", finalStatus),
		), "error", err)
		return model.Sandbox{}, err
	}
	sandbox.Status = nextStatus
	sandbox.RuntimeStatus = string(state.Status)
	sandbox.UpdatedAt = time.Now().UTC()
	sandbox.LastActiveAt = sandbox.UpdatedAt
	if err := s.store.UpdateSandboxState(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.store.UpdateRuntimeState(ctx, sandbox.ID, state); err != nil {
		return model.Sandbox{}, err
	}
	if err := s.refreshStorage(ctx, sandbox); err != nil {
		return model.Sandbox{}, err
	}
	s.recordAudit(ctx, tenantID, sandbox.ID, auditAction, sandbox.ID, "ok", auditDetail(
		auditKV("transitional_status", transitional),
		auditKV("result_status", finalStatus),
	))
	return s.store.GetSandbox(ctx, tenantID, sandbox.ID)
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func (s *Service) exportSnapshotBundle(ctx context.Context, sandbox model.Sandbox, snapshot model.Snapshot) (string, error) {
	snapshotDir := filepath.Join(s.cfg.SnapshotRoot, sandbox.ID, snapshot.ID)
	bundle, err := os.CreateTemp(filepath.Dir(snapshotDir), snapshot.ID+"-*.tar.gz")
	if err != nil {
		return "", err
	}
	bundlePath := bundle.Name()
	_ = bundle.Close()
	defer os.Remove(bundlePath)
	if err := writeTarGz(bundlePath, snapshotDir); err != nil {
		return "", err
	}
	return putSnapshotBundle(ctx, s.cfg.OptionalSnapshotExport, sandbox.ID, snapshot.ID, bundlePath)
}

func (s *Service) ensureSnapshotArtifacts(ctx context.Context, sandbox model.Sandbox, snapshot model.Snapshot) error {
	haveLocal := true
	for _, path := range []string{snapshot.ImageRef, snapshot.WorkspaceTar} {
		if path == "" {
			continue
		}
		if !looksLikeFilesystemPath(path) {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			haveLocal = false
			break
		}
	}
	if haveLocal {
		return nil
	}
	if snapshot.ExportLocation == "" {
		return nil
	}
	targetDir := filepath.Join(s.cfg.SnapshotRoot, sandbox.ID, snapshot.ID)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	tempBundle := filepath.Join(targetDir, "snapshot.restore.tar.gz")
	if err := fetchSnapshotBundle(ctx, snapshot.ExportLocation, tempBundle); err != nil {
		return err
	}
	defer os.Remove(tempBundle)
	return extractTarGz(tempBundle, targetDir)
}

func putSnapshotBundle(ctx context.Context, exportRoot, sandboxID, snapshotID, localBundle string) (string, error) {
	switch {
	case strings.HasPrefix(exportRoot, "s3://"):
		target := strings.TrimRight(exportRoot, "/") + "/" + sandboxID + "/" + snapshotID + ".tar.gz"
		cmd := exec.CommandContext(ctx, "aws", "s3", "cp", localBundle, target)
		if output, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("export snapshot bundle: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return target, nil
	case strings.HasPrefix(exportRoot, "file://"):
		exportRoot = strings.TrimPrefix(exportRoot, "file://")
		fallthrough
	default:
		target := filepath.Join(exportRoot, sandboxID, snapshotID+".tar.gz")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := copyFile(target, localBundle); err != nil {
			return "", err
		}
		return target, nil
	}
}

func fetchSnapshotBundle(ctx context.Context, exportLocation, localPath string) error {
	switch {
	case strings.HasPrefix(exportLocation, "s3://"):
		cmd := exec.CommandContext(ctx, "aws", "s3", "cp", exportLocation, localPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("restore snapshot bundle: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	case strings.HasPrefix(exportLocation, "file://"):
		exportLocation = strings.TrimPrefix(exportLocation, "file://")
		fallthrough
	default:
		return copyFile(localPath, exportLocation)
	}
}

func writeTarGz(destination, root string) error {
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer file.Close()
	gw := gzip.NewWriter(file)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()
	return filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tw, file)
		return err
	})
}

func extractTarGz(source, destination string) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	gr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	cleanDestination := filepath.Clean(destination)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(cleanDestination, filepath.Clean(header.Name))
		if target != cleanDestination && !strings.HasPrefix(target, cleanDestination+string(os.PathSeparator)) {
			return fmt.Errorf("tar entry escapes destination: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("unsupported tar entry type for %s", header.Name)
		}
		if header.FileInfo().IsDir() {
			if err := os.MkdirAll(target, header.FileInfo().Mode()); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, header.FileInfo().Mode())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	}
}
````
