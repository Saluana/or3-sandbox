# Manual Test Walkthrough / Checklist

This is a human-run test plan for `or3-sandbox`.

It is intentionally half walkthrough, half checklist:

- use it top-to-bottom for one thorough manual pass
- mark each item `PASS`, `FAIL`, or `N/A`
- capture sandbox IDs, tunnel IDs, screenshots, and notable output as you go

The fastest complete pass on macOS is the **trusted Docker** path. Linux-only sections for **Kata** and **QEMU** are included separately so this one file still covers every area a human can reasonably exercise.

---

## 0. Test lanes

Use these lanes so you do not mix “works on my laptop” checks with Linux/KVM-specific validation.

### Lane A — core local pass (recommended first, works on macOS)

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

### Lane B — Linux-only optional pass

Covers:

- Kata runtime behavior
- QEMU runtime behavior
- `doctor --production-qemu`
- host verification / smoke / abuse / recovery drills

### Lane C — secret-backed optional pass

Covers:

- `openclaw` preset with browser UI
- any flow needing external API keys or tokens

---

## 1. Prep checklist

Mark these before you start:

- [ ] Go is installed
- [ ] Docker is installed and running
- [ ] `jq` is installed, or you are comfortable copying IDs manually from JSON
- [ ] You are in the repo root
- [ ] You are okay creating and deleting disposable sandboxes and snapshots
- [ ] For Linux-only sections: required runtime/tooling is installed on that host
- [ ] For secret-backed sections: needed env vars are exported

Recommended scratch variables for the session:

```bash
cd /Users/brendon/Documents/or3-sandbox
export SANDBOX_API=http://127.0.0.1:8080
export SANDBOX_TOKEN=dev-token
export TEST_FILE_LOCAL=$PWD/.tmp/manual-hello.txt
mkdir -p .tmp
printf 'hello from host\n' > "$TEST_FILE_LOCAL"
```

If you want an easy results log, also create one note file now:

```bash
export MANUAL_LOG=$PWD/.tmp/manual-test-notes.txt
: > "$MANUAL_LOG"
```

---

## 2. Start the daemon and prove the basics

### 2.0 Sanity-check config first

Before starting the daemon, run:

```bash
go run ./cmd/sandboxctl config-lint
```

Checklist:

- [ ] config lint passes for your intended local setup
- [ ] if it fails, the message points to a real config issue instead of something vague

### 2.1 Start `sandboxd`

Use the shipped trusted Docker development posture:

```bash
SANDBOX_DEPLOYMENT_PROFILE=dev-trusted-docker \
go run ./cmd/sandboxd \
	-listen :8080 \
	-db ./data/sandbox.db \
	-storage-root ./data/storage \
	-snapshot-root ./data/snapshots
```

Checklist:

- [ ] The daemon starts without crashing
- [ ] No obvious config/runtime error appears on boot
- [ ] The process keeps running until you stop it manually

### 2.2 Basic reachability

In a second terminal:

```bash
curl -fsS "$SANDBOX_API/healthz"
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/v1/runtime/info"
go run ./cmd/sandboxctl runtime-health
go run ./cmd/sandboxctl quota
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/v1/runtime/capacity"
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/metrics" | head -40
```

Checklist:

- [ ] `/healthz` returns `{\"ok\":true}`
- [ ] `/v1/runtime/info` returns the expected backend/default runtime data
- [ ] `runtime-health` returns valid JSON
- [ ] `quota` returns valid JSON
- [ ] `/v1/runtime/capacity` is reachable with auth
- [ ] `/metrics` returns Prometheus-style text

### 2.3 Auth sanity checks

Run one good request and two bad ones:

```bash
curl -i "$SANDBOX_API/healthz"
curl -i -H "Authorization: Bearer bad-token" "$SANDBOX_API/v1/runtime/health"
curl -i "$SANDBOX_API/v1/runtime/health"
```

Checklist:

- [ ] `/healthz` works without auth
- [ ] protected endpoint rejects missing auth
- [ ] protected endpoint rejects wrong auth
- [ ] error responses look like normal JSON errors, not HTML garbage

---

## 3. Create one main sandbox and keep reusing it

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

If you do not have `jq`, copy the `id` manually and export it yourself.

Checklist:

- [ ] create returns JSON
- [ ] a sandbox ID is present
- [ ] status is `running` or otherwise clearly healthy
- [ ] runtime selection/backend match what you intended

### 3.2 List and inspect it

```bash
go run ./cmd/sandboxctl list
go run ./cmd/sandboxctl inspect "$SANDBOX_ID"
```

Checklist:

- [ ] the sandbox appears in `list`
- [ ] `inspect` shows the same ID
- [ ] limits and network mode look correct
- [ ] `runtime_backend` / `runtime_selection` look correct

---

## 4. Exec, stream output, and interactive TTY

### 4.1 Basic exec

```bash
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'echo hello from sandbox && pwd && id'
```

- [ ] command succeeds
- [ ] stdout streams back to your terminal
- [ ] working directory is `/workspace` by default

### 4.2 Write persistent workspace data

```bash
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'echo sandbox-note > /workspace/note.txt && cat /workspace/note.txt'
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'ls -la /workspace'
```

- [ ] file write succeeds
- [ ] file contents are readable immediately
- [ ] `/workspace` persists your changes

### 4.3 Timeout behavior

```bash
go run ./cmd/sandboxctl exec --timeout 2s "$SANDBOX_ID" sh -lc 'sleep 10'
```

- [ ] the command is rejected or terminated around the timeout window
- [ ] failure output is understandable
- [ ] the sandbox stays usable afterward

### 4.4 Detached exec behavior

```bash
go run ./cmd/sandboxctl exec --detached "$SANDBOX_ID" sh -lc 'sleep 5 && echo detached-ok > /workspace/detached.txt'
sleep 7
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'cat /workspace/detached.txt'
```

- [ ] detached exec returns immediately
- [ ] background work actually finishes
- [ ] later exec can observe the result

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

Checklist:

- [ ] interactive shell opens
- [ ] keyboard input works normally
- [ ] shell output is readable
- [ ] exiting the shell returns you cleanly to the host terminal

---

## 5. File APIs: upload, mkdir, download

### 5.1 Upload and inspect

```bash
go run ./cmd/sandboxctl upload "$SANDBOX_ID" "$TEST_FILE_LOCAL" /workspace/from-host.txt
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'cat /workspace/from-host.txt'
```

- [ ] upload succeeds
- [ ] sandbox sees the uploaded content exactly

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

## 6. Network mode behavior

This is worth checking explicitly because it is easy to assume network behavior without testing it.

### 6.1 Confirm `internet-enabled`

Use your main sandbox:

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

Now try network from inside it:

```bash
go run ./cmd/sandboxctl exec "$BLOCKED_SANDBOX_ID" sh -lc 'wget -qO- https://example.com | head -5 || curl -fsS https://example.com | head -5'
```

- [ ] outbound network access fails or is clearly blocked
- [ ] failure is consistent with the disabled-network expectation

---

## 7. Tunnels, proxying, and signed browser launch

Use the main sandbox from Lane A.

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

Create a signed browser launch URL directly against the API:

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

Checklist:

- [ ] opening the signed URL loads a bootstrap/redirect flow instead of asking for a raw tunnel token
- [ ] the final app page renders correctly
- [ ] refreshing within the TTL still works as expected
- [ ] the URL behaves like a short-lived capability, not a permanent link

### 7.4 Revoke the tunnel

```bash
go run ./cmd/sandboxctl tunnel-revoke "$TUNNEL_ID"
curl -i -H "Authorization: Bearer $SANDBOX_TOKEN" -H "X-Tunnel-Token: $TUNNEL_TOKEN" "$TUNNEL_ENDPOINT/"
```

- [ ] revoke succeeds
- [ ] follow-up access is denied or returns `410 Gone`
- [ ] the proxy does not keep serving after revocation

---

## 8. Snapshots and restore

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
- [ ] inspect shows the snapshot metadata you expect

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
- [ ] restored sandbox contains the pre-mutation snapshot content, not the later change

---

## 9. Lifecycle transitions and persistence

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

Only do this on runtimes that support it.

For `docker-dev` on macOS, treat this as an expectation check rather than a hard requirement.

```bash
go run ./cmd/sandboxctl suspend "$SANDBOX_ID"
go run ./cmd/sandboxctl resume "$SANDBOX_ID"
```

- [ ] if supported, suspend succeeds and resume returns the sandbox to usability
- [ ] if unsupported, the error is explicit and understandable

---

## 10. Runtime health, capacity, quota, and metrics after activity

After you have created sandboxes, tunnels, execs, and snapshots, re-check the operator surfaces.

```bash
go run ./cmd/sandboxctl runtime-health
go run ./cmd/sandboxctl quota
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/v1/runtime/capacity"
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/metrics" | grep 'or3_sandbox_' | head -50
```

- [ ] health output reflects current sandbox states
- [ ] capacity output reflects counts for sandboxes/snapshots/tunnels or pressure signals
- [ ] metrics expose sensible counters after your activity
- [ ] nothing looks stuck or obviously stale

---

## 11. Daemon restart reconciliation

This is one of the highest-value manual checks because it exercises the single-node durability story.

### 11.1 Prepare state to survive restart

Make sure at least one sandbox is running and has known files:

```bash
go run ./cmd/sandboxctl start "$SANDBOX_ID"
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'echo before-restart > /workspace/restart-check.txt'
```

### 11.2 Restart `sandboxd`

Stop the daemon process from terminal 1, then start it again with the same command from section 2.1.

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

If anything looks wrong, also review [docs/operations/daemon-restart-recovery.md](../docs/operations/daemon-restart-recovery.md).

---

## 12. Cleanup pass

Delete all disposable resources you created:

```bash
go run ./cmd/sandboxctl delete "$SANDBOX_ID"
go run ./cmd/sandboxctl delete "$BLOCKED_SANDBOX_ID"
go run ./cmd/sandboxctl delete "$RESTORE_TARGET_ID"
go run ./cmd/sandboxctl list
```

- [ ] deletes succeed
- [ ] the remaining list is empty or only shows resources you intentionally kept

Optional host cleanup:

```bash
rm -f ./.tmp/from-sandbox.txt "$TEST_FILE_LOCAL"
```

---

## 13. Optional preset checks

These are not required for the core product pass, but they are very good human tests because they exercise real workflows.

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
- [ ] the output clearly tells you where the artifact went

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

- [ ] the preset prints `sandbox_id`, tunnel info, and `dashboard_url`
- [ ] opening `dashboard_url` in a browser reaches the UI
- [ ] the health view comes online
- [ ] the configured model is visible and uses the expected provider
- [ ] revoking or replacing the tunnel behaves safely

If needed, follow [examples/openclaw/README.md](../examples/openclaw/README.md).

---

## 14. Optional Linux-only manual pass: Kata

Run this only on a Linux host with containerd + Kata installed.

### 14.1 Start with Kata enabled

```bash
SANDBOX_ENABLED_RUNTIMES=containerd-kata-professional \
SANDBOX_DEFAULT_RUNTIME=containerd-kata-professional \
SANDBOX_KATA_BINARY=ctr \
SANDBOX_KATA_RUNTIME_CLASS=io.containerd.kata.v2 \
SANDBOX_KATA_CONTAINERD_SOCKET=/run/containerd/containerd.sock \
go run ./cmd/sandboxd \
	-listen :8080 \
	-db ./data/sandbox.db \
	-storage-root ./data/storage \
	-snapshot-root ./data/snapshots
```

### 14.2 Repeat the core flow

Repeat sections 2 through 12 with `--runtime containerd-kata-professional`.

Pay special attention to these Kata-specific expectations:

- [ ] create/list/inspect/exec/files/tunnels/snapshots work normally
- [ ] `suspend` and `resume` fail with a clear unsupported error
- [ ] `disk-mb` is treated as advisory rather than a strict filesystem promise

---

## 15. Optional Linux/KVM manual pass: QEMU

Run this only on a prepared Linux/KVM host.

Prep expectations:

- `SANDBOX_QEMU_BINARY` is set
- `SANDBOX_QEMU_BASE_IMAGE_PATH` is set
- guest image is prepared and matches the control mode you intend to test

### 15.1 Preflight checks

```bash
go run ./cmd/sandboxctl config-lint
go run ./cmd/sandboxctl doctor --production-qemu
go run ./cmd/sandboxctl image list
```

- [ ] config lint passes
- [ ] doctor output is believable and actionable
- [ ] image list is reachable and shows the current promotion state clearly

### 15.2 Start a QEMU-capable daemon

Use the repo’s current documented QEMU posture for your host.

### 15.3 Repeat the core flow with QEMU

Repeat sections 2 through 12 using a QEMU-capable image and `--runtime qemu-professional`.

Pay special attention to:

- [ ] boot and readiness speed
- [ ] exec / TTY usability
- [ ] file persistence
- [ ] tunnel behavior
- [ ] snapshot create and restore
- [ ] suspend and resume
- [ ] daemon restart reconciliation

### 15.4 Run the shipped host-gated drills

```bash
./scripts/qemu-host-verification.sh --profile core --control-mode agent
./scripts/qemu-production-smoke.sh
./scripts/qemu-resource-abuse.sh
OR3_ALLOW_DISRUPTIVE=1 ./scripts/qemu-recovery-drill.sh
go run ./cmd/sandboxctl release-gate
```

- [ ] host verification passes
- [ ] production smoke passes
- [ ] abuse drill shows conservative failure/degradation behavior
- [ ] recovery drill confirms believable restart/restore behavior
- [ ] release-gate gives you one bounded top-level pass/fail summary

### 15.5 Image promotion flow

If you are validating the production-style QEMU image path, also test image promotion explicitly:

```bash
go run ./cmd/sandboxctl image promote --image "$SANDBOX_QEMU_BASE_IMAGE_PATH"
go run ./cmd/sandboxctl image list
```

- [ ] promote succeeds for the intended image
- [ ] the promoted image shows up in the listing afterward

---

## 16. What counts as “good enough” for a human pass

I would consider the manual pass successful if all of these are true:

- [ ] core Docker lane passes end-to-end on your working machine
- [ ] auth failures are clean and intentional
- [ ] workspace persistence survives lifecycle transitions and daemon restart
- [ ] tunnel creation, browser launch, and revocation all behave safely
- [ ] snapshot restore actually restores older workspace state
- [ ] operator surfaces (`runtime-health`, `quota`, `capacity`, `metrics`) remain believable after heavy activity
- [ ] optional preset/runtime lanes are either tested or explicitly marked `N/A` with a reason

---

## 17. Suggested result template

Paste something like this into your notes when you are done:

```text
Date:
Host:
Lane A (Docker/macOS): PASS | FAIL
Lane B (Linux Kata): PASS | FAIL | N/A
Lane B (Linux QEMU): PASS | FAIL | N/A
Lane C (OpenClaw/secrets): PASS | FAIL | N/A

Main sandbox IDs:
Tunnel IDs:
Snapshot IDs:

Biggest failures seen:
Most convincing success signals:
Follow-up fixes to make:
```
