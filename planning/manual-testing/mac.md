# Manual Testing Plan — macOS

This checklist is for a macOS host.

Use this plan when you want to validate everything this repo meaningfully supports on macOS:

- the full trusted-Docker local workflow
- operator surfaces and lifecycle behavior
- shipped Docker-friendly presets
- optional secret-backed flows
- optional QEMU development validation on Apple Silicon with HVF

Do not use this plan to make Linux/KVM production claims. The production QEMU host-verification, abuse, smoke, recovery, and release-gate evidence belongs on Linux and is covered in `planning/manual-testing/linux.md`.

---

## 0. Lanes

### Lane A — Docker local pass (required)

Covers:

- daemon startup
- health/auth checks
- create/list/inspect/start/stop/delete
- exec and TTY
- file upload/download/mkdir
- network mode behavior
- tunnel create/list/revoke
- signed browser URL flow
- snapshots
- quota/runtime health/capacity/metrics
- daemon restart reconciliation
- shipped Docker-friendly presets

### Lane B — QEMU/HVF development pass (optional)

Covers:

- basic QEMU daemon validation on macOS Apple Silicon
- QEMU create/exec/health/suspend/resume
- a shortened repeat of the core sandbox lifecycle on the macOS QEMU path

Use this lane only if your Mac is prepared for the repo’s QEMU tutorial path.

### Lane C — secret-backed pass (optional)

Covers:

- `openclaw` preset with browser UI
- any workflow requiring external API keys or tokens

---

## 1. Prep checklist

Mark these before you start:

- [ ] Go is installed
- [ ] Docker is installed and running
- [ ] `jq` is installed, or you are comfortable copying IDs manually from JSON
- [ ] You are in the repo root
- [ ] You are okay creating and deleting disposable sandboxes and snapshots
- [ ] For Lane B, QEMU is installed and the guest image exists
- [ ] For Lane C, needed secrets are exported

Recommended scratch variables for the session:

```bash
cd /Users/brendon/Documents/or3-sandbox
export SANDBOX_API=http://127.0.0.1:8080
export SANDBOX_TOKEN=dev-token
export TEST_FILE_LOCAL=$PWD/.tmp/manual-hello.txt
mkdir -p .tmp
printf 'hello from host\n' > "$TEST_FILE_LOCAL"
```

Optional notes log:

```bash
export MANUAL_LOG=$PWD/.tmp/manual-test-notes.txt
: > "$MANUAL_LOG"
```

---

## 2. Lane A — start the Docker daemon and prove the basics

### 2.1 Sanity-check config first

```bash
go run ./cmd/sandboxctl config-lint
```

- [ ] config lint passes for your local Docker setup
- [ ] failures, if any, point to a concrete config issue

### 2.2 Start `sandboxd`

Use the shipped trusted Docker development posture:

```bash
SANDBOX_DEPLOYMENT_PROFILE=dev-trusted-docker \
go run ./cmd/sandboxd \
  -listen :8080 \
  -db ./data/sandbox.db \
  -storage-root ./data/storage \
  -snapshot-root ./data/snapshots
```

- [ ] daemon starts without crashing
- [ ] no obvious runtime/config error appears on boot
- [ ] process stays up until stopped manually

### 2.3 Basic reachability

In a second terminal:

```bash
curl -fsS "$SANDBOX_API/healthz"
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/v1/runtime/info"
go run ./cmd/sandboxctl runtime-health
go run ./cmd/sandboxctl quota
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/v1/runtime/capacity"
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/metrics" | head -40
```

- [ ] `/healthz` returns `{"ok":true}`
- [ ] `/v1/runtime/info` shows the expected backend/default runtime data
- [ ] `runtime-health` returns valid JSON
- [ ] `quota` returns valid JSON
- [ ] `/v1/runtime/capacity` is reachable with auth
- [ ] `/metrics` returns Prometheus-style text

### 2.4 Auth sanity checks

```bash
curl -i "$SANDBOX_API/healthz"
curl -i -H "Authorization: Bearer bad-token" "$SANDBOX_API/v1/runtime/health"
curl -i "$SANDBOX_API/v1/runtime/health"
```

- [ ] `/healthz` works without auth
- [ ] protected endpoint rejects missing auth
- [ ] protected endpoint rejects wrong auth
- [ ] error responses are JSON, not HTML garbage

---

## 3. Lane A — create and inspect one main sandbox

### 3.1 Create it

```bash
sandbox_json="$(go run ./cmd/sandboxctl create \
  --image alpine:3.20 \
  --runtime docker-dev \
  --cpu 1 \
  --memory-mb 512 \
  --pids 128 \
  --disk-mb 2048 \
  --network internet-enabled \
  --allow-tunnels=true \
  --start)"
printf '%s\n' "$sandbox_json"
export SANDBOX_ID="$(printf '%s' "$sandbox_json" | jq -r '.id')"
echo "SANDBOX_ID=$SANDBOX_ID"
```

- [ ] create returns JSON
- [ ] a sandbox ID is present
- [ ] status is `running` or otherwise clearly healthy
- [ ] runtime selection/backend match the Docker path you intended

### 3.2 List and inspect it

```bash
go run ./cmd/sandboxctl list
go run ./cmd/sandboxctl inspect "$SANDBOX_ID"
```

- [ ] sandbox appears in `list`
- [ ] `inspect` shows the same ID
- [ ] limits and network mode look correct
- [ ] `runtime_backend` and `runtime_selection` look correct

---

## 4. Lane A — exec, stream output, and interactive TTY

### 4.1 Basic exec

```bash
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'echo hello from sandbox && pwd && id'
```

- [ ] command succeeds
- [ ] stdout streams back to the terminal
- [ ] default working directory is `/workspace`

### 4.2 Write persistent workspace data

```bash
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'echo sandbox-note > /workspace/note.txt && cat /workspace/note.txt'
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'ls -la /workspace'
```

- [ ] file write succeeds
- [ ] file contents are readable immediately
- [ ] `/workspace` persists changes

### 4.3 Timeout behavior

```bash
go run ./cmd/sandboxctl exec --timeout 2s "$SANDBOX_ID" sh -lc 'sleep 10'
```

- [ ] command is rejected or terminated near the timeout window
- [ ] failure output is understandable
- [ ] sandbox stays usable afterward

### 4.4 Detached exec behavior

```bash
go run ./cmd/sandboxctl exec --detached "$SANDBOX_ID" sh -lc 'sleep 5 && echo detached-ok > /workspace/detached.txt'
sleep 7
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'cat /workspace/detached.txt'
```

- [ ] detached exec returns immediately
- [ ] background work finishes
- [ ] later exec sees the result

### 4.5 TTY behavior

```bash
go run ./cmd/sandboxctl tty "$SANDBOX_ID"
```

Inside the shell, try:

```bash
pwd
ls -la /workspace
cat /workspace/note.txt
exit
```

- [ ] interactive shell opens
- [ ] keyboard input works normally
- [ ] shell output is readable
- [ ] exiting returns cleanly to the host terminal

---

## 5. Lane A — file APIs: upload, mkdir, download

### 5.1 Upload and inspect

```bash
go run ./cmd/sandboxctl upload "$SANDBOX_ID" "$TEST_FILE_LOCAL" /workspace/from-host.txt
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'cat /workspace/from-host.txt'
```

- [ ] upload succeeds
- [ ] sandbox sees uploaded content exactly

### 5.2 Create directories

```bash
go run ./cmd/sandboxctl mkdir "$SANDBOX_ID" /workspace/demo/subdir
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'find /workspace/demo -maxdepth 2 -type d | sort'
```

- [ ] nested directory creation succeeds
- [ ] resulting layout looks correct

### 5.3 Download back to host

```bash
go run ./cmd/sandboxctl download "$SANDBOX_ID" /workspace/from-host.txt ./.tmp/from-sandbox.txt
diff "$TEST_FILE_LOCAL" ./.tmp/from-sandbox.txt
```

- [ ] download succeeds
- [ ] downloaded file matches the uploaded file byte-for-byte

---

## 6. Lane A — network mode behavior

### 6.1 Confirm `internet-enabled`

```bash
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'wget -qO- https://example.com | head -5 || curl -fsS https://example.com | head -5'
```

- [ ] outbound network access works from an `internet-enabled` sandbox

### 6.2 Create an `internet-disabled` sandbox

```bash
blocked_json="$(go run ./cmd/sandboxctl create \
  --image alpine:3.20 \
  --runtime docker-dev \
  --network internet-disabled \
  --allow-tunnels=false \
  --start)"
printf '%s\n' "$blocked_json"
export BLOCKED_SANDBOX_ID="$(printf '%s' "$blocked_json" | jq -r '.id')"
```

```bash
go run ./cmd/sandboxctl exec "$BLOCKED_SANDBOX_ID" sh -lc 'wget -qO- https://example.com | head -5 || curl -fsS https://example.com | head -5'
```

- [ ] outbound network access fails or is clearly blocked
- [ ] failure matches the disabled-network expectation

---

## 7. Lane A — tunnels, proxying, and signed browser launch

### 7.1 Start a simple HTTP server inside the sandbox

```bash
go run ./cmd/sandboxctl exec --detached "$SANDBOX_ID" sh -lc \
  'mkdir -p /workspace/www && printf "hello tunnel\n" > /workspace/www/index.html && busybox httpd -f -p 3000 -h /workspace/www'
sleep 2
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'wget -qO- http://127.0.0.1:3000/'
```

- [ ] service starts inside the sandbox
- [ ] loopback access inside the sandbox works

### 7.2 Create and use a tunnel

```bash
tunnel_json="$(go run ./cmd/sandboxctl tunnel-create --port 3000 "$SANDBOX_ID")"
printf '%s\n' "$tunnel_json"
export TUNNEL_ID="$(printf '%s' "$tunnel_json" | jq -r '.id')"
export TUNNEL_ENDPOINT="$(printf '%s' "$tunnel_json" | jq -r '.endpoint')"
export TUNNEL_TOKEN="$(printf '%s' "$tunnel_json" | jq -r '.access_token')"

go run ./cmd/sandboxctl tunnel-list "$SANDBOX_ID"
curl -i -H "Authorization: Bearer $SANDBOX_TOKEN" -H "X-Tunnel-Token: $TUNNEL_TOKEN" "$TUNNEL_ENDPOINT/"
```

- [ ] tunnel create succeeds
- [ ] `tunnel-list` shows the created tunnel
- [ ] proxied HTTP request reaches the in-sandbox service

### 7.3 Verify a browser-friendly signed URL

```bash
signed_json="$(curl -fsS \
  -H "Authorization: Bearer $SANDBOX_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"path":"/","ttl_seconds":300}' \
  "$SANDBOX_API/v1/tunnels/$TUNNEL_ID/signed-url")"
printf '%s\n' "$signed_json"
export SIGNED_URL="$(printf '%s' "$signed_json" | jq -r '.url')"
echo "$SIGNED_URL"
```

Now test it in a browser.

- [ ] signed URL loads a bootstrap/redirect flow instead of asking for a raw tunnel token
- [ ] final app page renders correctly
- [ ] refreshing within the TTL still works as expected
- [ ] URL behaves like a short-lived capability

### 7.4 Revoke the tunnel

```bash
go run ./cmd/sandboxctl tunnel-revoke "$TUNNEL_ID"
curl -i -H "Authorization: Bearer $SANDBOX_TOKEN" -H "X-Tunnel-Token: $TUNNEL_TOKEN" "$TUNNEL_ENDPOINT/"
```

- [ ] revoke succeeds
- [ ] follow-up access is denied or returns `410 Gone`
- [ ] proxy does not keep serving after revocation

---

## 8. Lane A — snapshots and restore

### 8.1 Create a snapshot

```bash
snapshot_json="$(go run ./cmd/sandboxctl snapshot-create --name manual-checkpoint "$SANDBOX_ID")"
printf '%s\n' "$snapshot_json"
go run ./cmd/sandboxctl snapshot-list "$SANDBOX_ID"
```

Capture the snapshot ID from the list or response:

```bash
export SNAPSHOT_ID=<put-snapshot-id-here>
go run ./cmd/sandboxctl snapshot-inspect "$SNAPSHOT_ID"
```

- [ ] snapshot create succeeds
- [ ] snapshot appears in the list
- [ ] inspect shows expected snapshot metadata

### 8.2 Mutate the workspace after the snapshot

```bash
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'echo changed-after-snapshot > /workspace/note.txt && cat /workspace/note.txt'
```

- [ ] workspace now differs from the checkpoint state

### 8.3 Restore into a fresh sandbox

```bash
restore_target_json="$(go run ./cmd/sandboxctl create --image alpine:3.20 --runtime docker-dev --start=false)"
printf '%s\n' "$restore_target_json"
export RESTORE_TARGET_ID="$(printf '%s' "$restore_target_json" | jq -r '.id')"

go run ./cmd/sandboxctl snapshot-restore "$SNAPSHOT_ID" "$RESTORE_TARGET_ID"
go run ./cmd/sandboxctl start "$RESTORE_TARGET_ID"
go run ./cmd/sandboxctl exec "$RESTORE_TARGET_ID" sh -lc 'cat /workspace/note.txt'
```

- [ ] restore succeeds
- [ ] restored sandbox starts normally
- [ ] restored sandbox contains the pre-mutation snapshot content

---

## 9. Lane A — lifecycle transitions and persistence

### 9.1 Stop and start

```bash
go run ./cmd/sandboxctl stop "$SANDBOX_ID"
go run ./cmd/sandboxctl inspect "$SANDBOX_ID"
go run ./cmd/sandboxctl start "$SANDBOX_ID"
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'cat /workspace/note.txt && cat /workspace/from-host.txt'
```

- [ ] stop succeeds
- [ ] inspect shows a stopped state in between
- [ ] start succeeds
- [ ] workspace files survive stop/start

### 9.2 Force stop

```bash
go run ./cmd/sandboxctl exec --detached "$SANDBOX_ID" sh -lc 'sleep 300'
go run ./cmd/sandboxctl stop --force "$SANDBOX_ID"
go run ./cmd/sandboxctl inspect "$SANDBOX_ID"
```

- [ ] force stop succeeds even with active work
- [ ] sandbox returns to a safe non-running state

### 9.3 Suspend and resume

On macOS Docker, treat this as an expectation check rather than a hard requirement.

```bash
go run ./cmd/sandboxctl suspend "$SANDBOX_ID"
go run ./cmd/sandboxctl resume "$SANDBOX_ID"
```

- [ ] if supported, suspend succeeds and resume returns the sandbox to usability
- [ ] if unsupported, the error is explicit and understandable

### 9.4 macOS-specific Docker expectations

The Docker path on macOS uses a Linux VM underneath Docker Desktop, so Linux kernel controls and storage-limit enforcement are best-effort only.

- [ ] any seccomp/AppArmor/SELinux-related warnings are honest and understandable
- [ ] `disk-mb` behavior is believable, but not treated as a hard Linux quota guarantee on macOS

---

## 10. Lane A — runtime health, capacity, quota, and metrics after activity

```bash
go run ./cmd/sandboxctl runtime-health
go run ./cmd/sandboxctl quota
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/v1/runtime/capacity"
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/metrics" | grep 'or3_sandbox_' | head -50
```

- [ ] health output reflects current sandbox states
- [ ] capacity output reflects current counts or pressure signals
- [ ] metrics expose sensible counters after your activity
- [ ] nothing looks stuck or obviously stale

---

## 11. Lane A — daemon restart reconciliation

### 11.1 Prepare state to survive restart

```bash
go run ./cmd/sandboxctl start "$SANDBOX_ID"
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'echo before-restart > /workspace/restart-check.txt'
```

### 11.2 Restart `sandboxd`

Stop the daemon process from terminal 1, then start it again with the same command from section 2.2.

After it comes back:

```bash
curl -fsS "$SANDBOX_API/healthz"
go run ./cmd/sandboxctl runtime-health
go run ./cmd/sandboxctl list
go run ./cmd/sandboxctl inspect "$SANDBOX_ID"
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'cat /workspace/restart-check.txt'
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/metrics" | grep 'or3_sandbox_' | head -50
```

- [ ] daemon returns healthy after restart
- [ ] sandbox metadata is still present
- [ ] sandbox state is conservative and believable after reconcile
- [ ] workspace data survives the restart
- [ ] exec still works after reconcile

---

## 12. Lane A — cleanup pass

Delete all disposable resources you created:

```bash
go run ./cmd/sandboxctl delete "$SANDBOX_ID"
go run ./cmd/sandboxctl delete "$BLOCKED_SANDBOX_ID"
go run ./cmd/sandboxctl delete "$RESTORE_TARGET_ID"
go run ./cmd/sandboxctl list
```

- [ ] deletes succeed
- [ ] remaining list is empty or only shows resources you intentionally kept

Optional host cleanup:

```bash
rm -f ./.tmp/from-sandbox.txt "$TEST_FILE_LOCAL"
```

---

## 13. Lane A/C — optional preset checks

### 13.1 Preset discovery

```bash
go run ./cmd/sandboxctl preset list
go run ./cmd/sandboxctl preset inspect playwright
go run ./cmd/sandboxctl preset inspect openclaw
```

- [ ] presets list successfully
- [ ] inspect output is readable and useful

### 13.2 Playwright preset

```bash
go run ./cmd/sandboxctl preset run playwright --cleanup on-success
```

- [ ] preset completes without manual sandbox management
- [ ] screenshot artifact is downloaded locally
- [ ] output clearly tells you where the artifact went

### 13.3 OpenClaw preset (requires secrets)

Prep:

```bash
export OPENCLAW_GATEWAY_TOKEN=$(openssl rand -hex 32)
export OPENROUTER_API_KEY=<real-key>
export OPENCLAW_MODEL=minimax/minimax-m2.5
```

Run:

```bash
go run ./cmd/sandboxctl preset run openclaw --cleanup never
```

Then verify:

- [ ] preset prints `sandbox_id`, tunnel info, and `dashboard_url`
- [ ] opening `dashboard_url` in a browser reaches the UI
- [ ] health view comes online
- [ ] configured model is visible and uses the expected provider
- [ ] revoking or replacing the tunnel behaves safely

---

## 14. Lane B — optional macOS QEMU/HVF development pass

Run this only on a prepared Apple Silicon Mac with QEMU installed and a guest image that matches the intended control mode.

### 14.1 Prep QEMU variables

Agent-first example:

```bash
export SANDBOX_RUNTIME=qemu
export SANDBOX_QEMU_BINARY=qemu-system-aarch64
export SANDBOX_QEMU_ACCEL=hvf
export SANDBOX_QEMU_BASE_IMAGE_PATH=$PWD/images/guest/base.qcow2
```

If you intentionally use an `ssh-compat` image, also set:

```bash
export SANDBOX_QEMU_CONTROL_MODE=ssh-compat
export SANDBOX_QEMU_SSH_USER=or3
export SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH=$HOME/.ssh/or3-sandbox
export SANDBOX_QEMU_SSH_HOST_KEY_PATH=$PWD/images/guest/base.qcow2.ssh-host-key.pub
```

### 14.2 Preflight

```bash
go run ./cmd/sandboxctl config-lint
```

- [ ] config lint passes with the macOS QEMU/HVF setup
- [ ] failures, if any, point to a missing binary, image, key, or accelerator issue

### 14.3 Start a QEMU-capable daemon

```bash
go run ./cmd/sandboxd \
  -listen :8080 \
  -db ./data/sandbox.db \
  -storage-root ./data/storage \
  -snapshot-root ./data/snapshots
```

- [ ] daemon starts with the QEMU configuration active
- [ ] startup errors are concrete if the host/image setup is wrong

### 14.4 Create a QEMU sandbox and exercise the core path

```bash
qemu_json="$(go run ./cmd/sandboxctl create --image "$SANDBOX_QEMU_BASE_IMAGE_PATH" --runtime qemu-professional --start)"
printf '%s\n' "$qemu_json"
export QEMU_SANDBOX_ID="$(printf '%s' "$qemu_json" | jq -r '.id')"
```

Run a shortened core flow:

```bash
go run ./cmd/sandboxctl inspect "$QEMU_SANDBOX_ID"
go run ./cmd/sandboxctl exec "$QEMU_SANDBOX_ID" sh -lc 'echo hello from qemu && pwd'
go run ./cmd/sandboxctl runtime-health
go run ./cmd/sandboxctl suspend "$QEMU_SANDBOX_ID"
go run ./cmd/sandboxctl resume "$QEMU_SANDBOX_ID"
go run ./cmd/sandboxctl stop "$QEMU_SANDBOX_ID"
go run ./cmd/sandboxctl start "$QEMU_SANDBOX_ID"
go run ./cmd/sandboxctl delete "$QEMU_SANDBOX_ID"
```

- [ ] create succeeds and reaches a believable running state
- [ ] exec works inside the guest
- [ ] runtime health reflects the guest state honestly
- [ ] suspend/resume work if the guest/control path supports them
- [ ] stop/start/delete behave normally

### 14.5 QEMU notes specific to macOS

- [ ] QEMU is treated as development validation on macOS, not production evidence
- [ ] any boot/readiness slowness is recorded in notes
- [ ] if snapshots are tested, they are treated as offline/stop-first operations

---

## 15. Success criteria

I would consider the macOS pass successful if all of these are true:

- [ ] the full Docker lane passes end-to-end
- [ ] auth failures are clean and intentional
- [ ] workspace persistence survives lifecycle transitions and daemon restart
- [ ] tunnel creation, browser launch, and revocation behave safely
- [ ] snapshot restore really restores older workspace state
- [ ] operator surfaces remain believable after heavy activity
- [ ] optional secret-backed and QEMU/HVF lanes are either tested or marked `N/A` with a reason

---

## 16. Suggested results template

```text
Date:
Host:
Lane A (macOS Docker): PASS | FAIL
Lane B (macOS QEMU/HVF): PASS | FAIL | N/A
Lane C (OpenClaw/secrets): PASS | FAIL | N/A

Main sandbox IDs:
Tunnel IDs:
Snapshot IDs:

Biggest failures seen:
Most convincing success signals:
Follow-up fixes to make:
```
