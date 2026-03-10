# Dumb Issues

Review target: `or3-sandbox` repomix dump.

## 1. The PTY loop that waits for user input after the process already died

**Where:** `cmd/or3-guest-agent/main.go` (183-291)

**Snippet:**

```go
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
```

**Why this is bad:**
`servePTY` blocks on `agentproto.ReadMessage(conn)` before it checks `errCh`. If the child process exits while the client is idle, the goroutine sends EOF into `errCh`, but the main loop will not observe it until another client message arrives. That means the PTY session can hang forever after the process is already gone.

**Real-world consequences:**
Stuck PTY sessions, leaked goroutines, dead terminal tabs, and a control plane that waits on a corpse because the event loop is backwards.

**Concrete fix:**
Wait on both the connection and the process exit concurrently. Put `ReadMessage` in its own goroutine or use a multiplexed design where the loop selects on incoming frames and `errCh` instead of blocking on the read first.

## 2. Detached exec that never calls Wait, because apparently zombies are a feature now

**Where:** `cmd/or3-guest-agent/main.go` (300-320)

**Snippet:**

```go
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
```

**Why this is bad:**
The detached path returns immediately after `cmd.Start()` and never reaps the child with `Wait()`. In Go, that leaves exited children unreaped until someone waits on them. Repeating this leaks process table entries and eventually becomes a real operational problem.

**Real-world consequences:**
Zombie processes accumulate inside the guest agent process over time. That means degraded long-running guests and a nice slow-motion failure nobody will enjoy debugging.

**Concrete fix:**
For detached commands, start a goroutine that calls `cmd.Wait()` and logs the exit. If you actually want durable detached jobs, stop pretending `exec.Cmd` is a job system and build one.

## 3. Frame parser with no max size, because trusting untrusted lengths is how you earn outages

**Where:** `internal/runtime/qemu/agentproto/protocol.go` (138-155)

**Snippet:**

```go
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
```

**Why this is bad:**
`ReadMessage` reads a 32-bit length from the wire and immediately allocates `make([]byte, length)` with no upper bound. One bad frame can force a huge allocation, blow memory, or at least trigger garbage collector misery.

**Real-world consequences:**
A malformed or compromised peer can force memory spikes or OOMs in the host or guest agent path. Great job turning a tiny control protocol into a memory bomb.

**Concrete fix:**
Define a protocol max frame size, reject anything larger before allocation, and stream large payloads through chunked file APIs instead of pretending every message should fit in RAM.

## 4. File read implemented as "load the whole damn thing into memory"

**Where:** `cmd/or3-guest-agent/main.go` (118-132)

**Snippet:**

```go
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
```

**Why this is bad:**
`OpFileRead` uses `os.ReadFile`, then base64-encodes the entire payload into JSON. That is bad twice: first you read the whole file, then you inflate it by about 33 percent before shipping it.

**Real-world consequences:**
Large workspace reads turn into memory spikes and oversized protocol messages. One giant file read can punch both the guest agent and the host client in the throat.

**Concrete fix:**
Add size limits and chunked reads. Return metadata plus ranged content or stream file chunks over the control channel with explicit bounds.

## 5. Docker disk quota flag appended after the image, which means Docker ignores your brilliant idea

**Where:** `internal/runtime/docker/runtime.go` (216-227)

**Snippet:**

```go
	args = append(args, spec.BaseImageRef, "sleep", "infinity")
	withStorageOpt := r.hostOS == "linux" && spec.DiskLimitMB > 0
	storageOptArgs := append([]string(nil), args...)
	if withStorageOpt {
		storageOptArgs = append(storageOptArgs, "--storage-opt", fmt.Sprintf("size=%dm", spec.DiskLimitMB))
	}
	out, err := r.run(ctx, storageOptArgs...)
	if err != nil && withStorageOpt && dockerStorageOptUnsupported(err) {
		slog.Warn("docker storage-opt unsupported; retrying without disk quota", "runtime", "docker", "sandbox_id", spec.SandboxID, "disk_limit_mb", spec.DiskLimitMB, "error", err)
		_, _ = r.run(ctx, "rm", "-f", containerName(spec.SandboxID))
		out, err = r.run(ctx, args...)
	}
```

**Why this is bad:**
Docker CLI options must appear before the image name. This code appends `--storage-opt size=...` after `spec.BaseImageRef, "sleep", "infinity"`, so the retry path is constructing an invalid command line. That means the quota path is broken or silently ignored depending on the daemon version and error handling.

**Real-world consequences:**
Your advertised disk limit for Docker sandboxes is unreliable. In the best case it errors. In the worse case operators think they have a quota and they do not.

**Concrete fix:**
Insert `--storage-opt` into the argument list before the image reference. Build the argument vector in ordered stages instead of blindly appending flags after the command payload.

## 6. CreateSandbox forgets cleanup if persistence fails after runtime create/start

**Where:** `internal/service/service.go` (161-181)

**Snippet:**

```go
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
```

**Why this is bad:**
Once `runtime.Create` and maybe `runtime.Start` succeed, failures in `UpdateSandboxState` or `UpdateRuntimeState` just return the DB error. There is no rollback, no destroy, no compensating transaction, nothing. You now have a real runtime object that the database may not describe correctly.

**Real-world consequences:**
Orphaned containers or VMs, leaked storage, quota drift, and operators wondering why reality and SQLite are in an abusive relationship.

**Concrete fix:**
Wrap post-create persistence in cleanup logic. If any DB update fails after runtime creation, destroy the runtime, remove storage, and mark the sandbox record consistently using a rollback path similar to `rollbackFailedCreate`.

## 7. Signed tunnel URLs that do not bind the path, because least privilege was too mainstream

**Where:** `internal/api/router.go` (765-791 and 1073-1093)

**Snippet:**

```go
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
...
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
```

**Why this is bad:**
The signed URL lets the caller request an arbitrary `path`, but the signature only covers `tunnelID` and `expiry`. Once a signed URL is issued, the same signature can be replayed for any path on that tunnel until expiry. That is capability inflation, not a signed path.

**Real-world consequences:**
A link intended for one route becomes a wildcard ticket for the whole proxied app during its lifetime. That is exactly the kind of half-baked access control people write incident reports about.

**Concrete fix:**
Include the normalized path, and ideally the allowed query string, in the HMAC input. Validate against the requested path on every proxy request, not just the tunnel ID and expiry.

## 8. A net.Conn implementation where deadlines are decorative fiction

**Where:** `internal/service/tunnel_tcp.go` (106-115)

**Snippet:**

```go
func (c *sandboxLocalConn) SetDeadline(time.Time) error {
	return nil
}

func (c *sandboxLocalConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *sandboxLocalConn) SetWriteDeadline(time.Time) error {
	return nil
```

**Why this is bad:**
This type claims to implement `net.Conn` but `SetDeadline`, `SetReadDeadline`, and `SetWriteDeadline` all return `nil` without doing anything. That violates caller expectations and breaks timeout handling in any code that assumes a real connection contract.

**Real-world consequences:**
Hung proxies and stuck reads or writes that cannot be interrupted using normal connection deadlines. Callers think they set timeouts. They did not.

**Concrete fix:**
Either implement deadlines properly or do not expose this as a `net.Conn`. At minimum, return an error saying deadlines are unsupported so callers are not lied to.

## 9. Readiness signal emitted before nc even connects. Incredible confidence for a script that has no clue

**Where:** `internal/service/tunnel_tcp.go` (199-205)

**Snippet:**

```go
if command -v nc >/dev/null 2>&1; then
	printf '__OR3_TUNNEL_BRIDGE_READY__\n'
	exec nc 127.0.0.1 "$port"
fi
if command -v busybox >/dev/null 2>&1; then
	printf '__OR3_TUNNEL_BRIDGE_READY__\n'
	exec busybox nc 127.0.0.1 "$port"
```

**Why this is bad:**
The `nc` and `busybox nc` fallback branches print `__OR3_TUNNEL_BRIDGE_READY__` before opening the TCP connection. So the host sees "ready" even when the target port is dead and the helper is about to fail immediately after.

**Real-world consequences:**
False-positive readiness, useless Bad Gateway errors after a so-called successful bridge open, and extra time wasted debugging a marker that lies.

**Concrete fix:**
Only emit readiness after the TCP connection succeeds. If the helper cannot provide a post-connect callback, stop pretending it is equivalent to the Python and Node implementations.

## 10. Raw shell export with unvalidated env keys. A tiny command-injection footgun, handcrafted just for you

**Where:** `internal/runtime/qemu/exec.go` (320-333)

**Snippet:**

```go
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
```

**Why this is bad:**
The env values are shell-quoted, but the env keys are interpolated directly into `export %s=%s`. If a caller passes an invalid or malicious key, the remote shell script becomes syntactically broken or worse. Shell code generation without validating identifiers is amateur hour.

**Real-world consequences:**
Broken exec sessions at best, shell injection at worst if untrusted callers can influence env keys. Either way, it is one of those bugs that looks obvious five minutes after production burns.

**Concrete fix:**
Reject keys that do not match a strict env-var regex like `^[A-Za-z_][A-Za-z0-9_]*$` before building the shell snippet. Better yet, avoid shell glue and pass environment through a safer transport.

## 11. SHA-256 by reading the whole qcow2 into RAM like it's a JPEG from 2004

**Where:** `internal/guestimage/contract.go` (101-107)

**Snippet:**

```go
func ComputeSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read guest image %q: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
```

**Why this is bad:**
`ComputeSHA256` uses `os.ReadFile` on the full image. These are disk images, not config files. Reading multi-gigabyte qcow2 images into memory to hash them is wasteful and dumb.

**Real-world consequences:**
Huge memory spikes during image validation, poor performance, and avoidable OOM risk on smaller hosts. You built a sandbox product and then forgot streams exist.

**Concrete fix:**
Hash via `io.Copy` into `sha256.New()` using a file handle. Streaming exists for a reason.

## 12. Supply-chain roulette: curl random cloud image, verify nothing, ship it

**Where:** `images/guest/build-base-image.sh` (34 and 176-177)

**Snippet:**

```go
BASE_IMAGE_URL="${BASE_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"
...
  require_cmd curl
  curl -L "$BASE_IMAGE_URL" -o "$DOWNLOAD_PATH"
```

**Why this is bad:**
The build pulls a mutable `current` cloud image over HTTPS and never verifies a checksum, signature, or pinned digest. For something positioned as a production-oriented guest image pipeline, this is reckless.

**Real-world consequences:**
Non-reproducible builds, silent upstream drift, and supply-chain exposure. The next time Ubuntu rotates something unexpected, your "same" build is not the same build.

**Concrete fix:**
Pin a specific image version and verify its published checksum or signature before using it. Store the expected digest in source control or release metadata.

## 13. Preset file sources can walk out of the preset directory and read whatever the host user can read

**Where:** `cmd/sandboxctl/preset.go` (402-420)

**Snippet:**

```go
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
```

**Why this is bad:**
`filepath.Join(r.manifest.BaseDir, asset.Source)` is not validation. A manifest can set `source: ../../../../some/secret` and this code will happily read it. `Manifest.Validate` never constrains `Source` to stay under `BaseDir` either.

**Real-world consequences:**
A malicious or sloppy preset can exfiltrate arbitrary local files into the sandbox upload step. That is a lovely way to leak host secrets through a convenience feature.

**Concrete fix:**
Clean and resolve the joined path, then ensure it stays under `BaseDir`. Reject absolute paths and any path that escapes the preset root.

## 14. Preset artifact downloads can overwrite arbitrary local files because path validation apparently hurt feelings

**Where:** `cmd/sandboxctl/preset.go` (557-575)

**Snippet:**

```go
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
```

**Why this is bad:**
For relative artifact destinations, the code blindly joins `artifact.LocalPath` with `BaseDir` and writes the file. `../` traversal is allowed, so a preset can write outside the project directory and clobber unrelated files.

**Real-world consequences:**
Local file overwrite from a preset manifest. Best case you trash someone's repo. Worst case you stomp something sensitive in CI or an operator workstation.

**Concrete fix:**
Resolve the final path and enforce that it remains inside an approved output directory. Reject parent traversal and absolute paths unless the user explicitly opted in.

## 15. Using eval in operational scripts, because command injection is more fun when you volunteer for it

**Where:** `scripts/qemu-production-smoke.sh and scripts/qemu-recovery-drill.sh` (qemu-production-smoke.sh 150-157; qemu-recovery-drill.sh 68-71)

**Snippet:**

```go
if [ -n "${SANDBOXD_RESTART_COMMAND:-}" ]; then
  log 'running optional daemon restart reconciliation step'
  if [ "${OR3_ALLOW_DISRUPTIVE:-0}" != '1' ]; then
    echo 'set OR3_ALLOW_DISRUPTIVE=1 to run SANDBOXD_RESTART_COMMAND during smoke' >&2
    exit 1
  fi
  eval "$SANDBOXD_RESTART_COMMAND"
  wait_for_status "$core_id" running 90
...
if [ -n "${SANDBOXD_RESTART_COMMAND:-}" ]; then
  log 'running daemon restart drill'
  eval "$SANDBOXD_RESTART_COMMAND"
  wait_for_status "$SANDBOX_ID" running 90
```

**Why this is bad:**
Both scripts execute `SANDBOXD_RESTART_COMMAND` through `eval`. That means shell metacharacters, accidental quoting bugs, or hostile environment input all get a free pass.

**Real-world consequences:**
Operator footgun, CI footgun, shell injection footgun. Pick your poison. `eval` in glue scripts is the kind of thing that makes experienced engineers roll their eyes so hard they see their own brain stem.

**Concrete fix:**
Use an argv-style command, or require an explicit executable plus arguments. If you absolutely must support shell syntax, at least make it a documented opt-in wrapper instead of the default execution path.

## 16. Printing tunnel access tokens straight to stdout like logs are a secure secret manager now

**Where:** `cmd/sandboxctl/preset.go` (499-510)

**Snippet:**

```go
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
```

**Why this is bad:**
The preset runner writes `tunnel_access_token` to stdout. That means terminal scrollback, CI logs, shell history captures, and any wrapper process can collect the token. Secrets do not become less secret because you printed them with confidence.

**Real-world consequences:**
Credential leakage through logs and build output. The exact place people least want secrets to show up is where this code sends them.

**Concrete fix:**
Do not print secrets by default. Gate secret output behind an explicit flag, or write them to stderr with a warning, or emit them to a protected file descriptor designed for machine consumption.
