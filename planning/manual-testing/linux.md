# Manual Testing Plan — Linux

This checklist is for a Linux host.

Use this plan when you want to validate everything this repo expects from a Linux operator or development machine:

- the full trusted-Docker workflow
- Linux-specific Docker enforcement expectations
- Kata runtime behavior
- QEMU production-style behavior and host verification
- release-gate evidence
- optional secret-backed flows

This is the only host plan that should be used for Linux/KVM production-readiness claims.

Pass 1 status legend:

- `[x]` verified in the 2026-03-15 Ubuntu 22.04 pass
- `[ ]` not yet run or still pending
- `[ ] BROKEN:` tested and failed in pass 1

---

## 0. Lanes

### Lane A — Docker local and operator pass (required)

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
- Linux-specific Docker limit/security expectations

### Lane B — Kata runtime pass (optional, Linux-only)

Covers:

- containerd + Kata runtime selection
- repeat of the core flow on the Kata backend
- unsupported suspend/resume expectations

### Lane C — QEMU production pass (optional, Linux/KVM)

Covers:

- QEMU config validation and doctor output
- QEMU lifecycle and sandbox operations
- host verification, production smoke, abuse, recovery, and release-gate
- image promotion flow

### Lane D — secret-backed pass (optional)

Covers:

- `openclaw` preset with browser UI
- any workflow requiring external API keys or tokens

---

## 1. Prep checklist

Mark these before you start:

- [x] Go is installed
- [x] Docker is installed and running
- [x] `jq` is installed, or you are comfortable copying IDs manually from JSON
- [x] You are in the repo root
- [x] You are okay creating and deleting disposable sandboxes and snapshots
- [ ] For Lane B, `ctr`, containerd, and a Kata runtime class are installed
- [ ] For Lane C, KVM/QEMU and the guest image are prepared
- [ ] For Lane D, needed secrets are exported

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

- [x] config lint passes for your intended local setup
- [x] failures, if any, point to a concrete config issue

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

- [x] daemon starts without crashing
- [x] no obvious config/runtime error appears on boot
- [x] process stays up until stopped manually

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

- [x] `/healthz` returns `{"ok":true}`
- [x] `/v1/runtime/info` shows the expected backend/default runtime data
- [x] `runtime-health` returns valid JSON
- [x] `quota` returns valid JSON
- [x] `/v1/runtime/capacity` is reachable with auth
- [x] `/metrics` returns Prometheus-style text

### 2.4 Auth sanity checks

```bash
curl -i "$SANDBOX_API/healthz"
curl -i -H "Authorization: Bearer bad-token" "$SANDBOX_API/v1/runtime/health"
curl -i "$SANDBOX_API/v1/runtime/health"
```

- [x] `/healthz` works without auth
- [x] protected endpoint rejects missing auth
- [x] protected endpoint rejects wrong auth
- [ ] BROKEN: error responses are plain text `unauthorized`, not JSON

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

- [ ] BROKEN: `sandboxctl create` did not reliably emit usable JSON in pass 1, even though the sandbox was created
- [x] a sandbox ID is present
- [x] status is `running` or otherwise clearly healthy
- [x] runtime selection/backend match what you intended

### 3.2 List and inspect it

```bash
go run ./cmd/sandboxctl list
go run ./cmd/sandboxctl inspect "$SANDBOX_ID"
```

- [x] sandbox appears in `list`
- [x] `inspect` shows the same ID
- [x] limits and network mode look correct
- [x] `runtime_backend` and `runtime_selection` look correct

---

## 4. Lane A — exec, stream output, and interactive TTY

### 4.1 Basic exec

```bash
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'echo hello from sandbox && pwd && id'
```

- [x] command succeeds
- [x] stdout streams back to the terminal
- [x] default working directory is `/workspace`

### 4.2 Write persistent workspace data

```bash
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'echo sandbox-note > /workspace/note.txt && cat /workspace/note.txt'
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'ls -la /workspace'
```

- [ ] BROKEN: writing `/workspace/note.txt` from `exec` failed with `Permission denied`
- [ ] BROKEN: file contents were not readable immediately from the exact command above
- [ ] BROKEN: this exact `/workspace` write path did not work in pass 1

### 4.3 Timeout behavior

```bash
go run ./cmd/sandboxctl exec --timeout 2s "$SANDBOX_ID" sh -lc 'sleep 10'
```

- [x] command is rejected or terminated near the timeout window
- [x] failure output is understandable
- [x] sandbox stays usable afterward

### 4.4 Detached exec behavior

```bash
go run ./cmd/sandboxctl exec --detached "$SANDBOX_ID" sh -lc 'sleep 5 && echo detached-ok > /workspace/detached.txt'
sleep 7
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'cat /workspace/detached.txt'
```

- [ ] BROKEN: the exact detached `/workspace/detached.txt` flow did not complete successfully
- [ ] BROKEN: the exact background write target in `/workspace` did not appear afterward
- [ ] BROKEN: later exec did not see `/workspace/detached.txt`

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

- [x] interactive shell transport opens
- [ ] keyboard input works normally
- [x] shell output is readable
- [ ] BROKEN: scripted TTY attach ended with websocket close `1006` instead of a clean detach

---

## 5. Lane A — file APIs: upload, mkdir, download

### 5.1 Upload and inspect

```bash
go run ./cmd/sandboxctl upload "$SANDBOX_ID" "$TEST_FILE_LOCAL" /workspace/from-host.txt
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'cat /workspace/from-host.txt'
```

- [ ] BROKEN: the exact absolute remote path `/workspace/from-host.txt` was rewritten under `/workspace/workspace/from-host.txt`
- [ ] BROKEN: the sandbox did not see the uploaded file at the exact absolute path used in the checklist

### 5.2 Create directories

```bash
go run ./cmd/sandboxctl mkdir "$SANDBOX_ID" /workspace/demo/subdir
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'find /workspace/demo -maxdepth 2 -type d | sort'
```

- [ ] BROKEN: absolute `mkdir /workspace/demo/subdir` was created under `/workspace/workspace/demo/subdir`
- [ ] BROKEN: the resulting absolute-path layout did not match the checklist expectation

### 5.3 Download back to host

```bash
go run ./cmd/sandboxctl download "$SANDBOX_ID" /workspace/from-host.txt ./.tmp/from-sandbox.txt
diff "$TEST_FILE_LOCAL" ./.tmp/from-sandbox.txt
```

- [x] download succeeds
- [x] downloaded file matches the uploaded file byte-for-byte

---

## 6. Lane A — network mode behavior

### 6.1 Confirm `internet-enabled`

```bash
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'wget -qO- https://example.com | head -5 || curl -fsS https://example.com | head -5'
```

- [x] outbound network access works from an `internet-enabled` sandbox

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

- [x] outbound network access fails or is clearly blocked
- [x] failure matches the disabled-network expectation

---

## 7. Lane A — tunnels, proxying, and signed browser launch

### 7.1 Start a simple HTTP server inside the sandbox

```bash
go run ./cmd/sandboxctl exec --detached "$SANDBOX_ID" sh -lc \
  'mkdir -p /workspace/www && printf "hello tunnel\n" > /workspace/www/index.html && busybox httpd -f -p 3000 -h /workspace/www'
sleep 2
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'wget -qO- http://127.0.0.1:3000/'
```

- [ ] BROKEN: the exact BusyBox `httpd` command in the checklist did not stay reachable in pass 1
- [ ] BROKEN: loopback access for the exact checklist server command failed with `Connection refused`

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

- [x] tunnel create succeeds
- [x] `tunnel-list` shows the created tunnel
- [ ] BROKEN: proxied HTTP requests returned `502 Bad Gateway`

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

- [x] signed URL loads a bootstrap/redirect flow instead of asking for a raw tunnel token
- [ ] final app page renders correctly
- [ ] refreshing within the TTL still works as expected
- [ ] URL behaves like a short-lived capability

### 7.4 Revoke the tunnel

```bash
go run ./cmd/sandboxctl tunnel-revoke "$TUNNEL_ID"
curl -i -H "Authorization: Bearer $SANDBOX_TOKEN" -H "X-Tunnel-Token: $TUNNEL_TOKEN" "$TUNNEL_ENDPOINT/"
```

- [x] revoke succeeds
- [x] follow-up access is denied or returns `410 Gone`
- [x] proxy does not keep serving after revocation

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

- [x] snapshot create succeeds
- [x] snapshot appears in the list
- [x] inspect shows expected snapshot metadata

### 8.2 Mutate the workspace after the snapshot

```bash
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'echo changed-after-snapshot > /workspace/note.txt && cat /workspace/note.txt'
```

- [x] workspace now differs from the checkpoint state

### 8.3 Restore into a fresh sandbox

```bash
restore_target_json="$(go run ./cmd/sandboxctl create --image alpine:3.20 --runtime docker-dev --start=false)"
printf '%s\n' "$restore_target_json"
export RESTORE_TARGET_ID="$(printf '%s' "$restore_target_json" | jq -r '.id')"

go run ./cmd/sandboxctl snapshot-restore "$SNAPSHOT_ID" "$RESTORE_TARGET_ID"
go run ./cmd/sandboxctl start "$RESTORE_TARGET_ID"
go run ./cmd/sandboxctl exec "$RESTORE_TARGET_ID" sh -lc 'cat /workspace/note.txt'
```

- [x] restore succeeds
- [x] restored sandbox starts normally
- [x] restored sandbox contains the pre-mutation snapshot content

---

## 9. Lane A — lifecycle transitions and persistence

### 9.1 Stop and start

```bash
go run ./cmd/sandboxctl stop "$SANDBOX_ID"
go run ./cmd/sandboxctl inspect "$SANDBOX_ID"
go run ./cmd/sandboxctl start "$SANDBOX_ID"
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'cat /workspace/note.txt && cat /workspace/from-host.txt'
```

- [x] stop succeeds
- [x] inspect shows a stopped state in between
- [x] start succeeds
- [x] workspace files survive stop/start

### 9.2 Force stop

```bash
go run ./cmd/sandboxctl exec --detached "$SANDBOX_ID" sh -lc 'sleep 300'
go run ./cmd/sandboxctl stop --force "$SANDBOX_ID"
go run ./cmd/sandboxctl inspect "$SANDBOX_ID"
```

- [x] force stop succeeds even with active work
- [x] sandbox returns to a safe non-running state

### 9.3 Suspend and resume

For Docker, treat this as an expectation check rather than a hard requirement.

```bash
go run ./cmd/sandboxctl suspend "$SANDBOX_ID"
go run ./cmd/sandboxctl resume "$SANDBOX_ID"
```

- [x] if supported, suspend succeeds and resume returns the sandbox to usability
- [ ] if unsupported, the error is explicit and understandable

---

## 10. Lane A — Linux-specific Docker expectations

This is the main Linux-only addition to the Docker lane.

- [ ] writable-layer `disk-mb` behavior is believable as a Linux quota hint, not an obviously ignored no-op
- [ ] if `SANDBOX_DOCKER_SECCOMP_PROFILE`, `SANDBOX_DOCKER_APPARMOR_PROFILE`, or `SANDBOX_DOCKER_SELINUX_LABEL` are configured, the runtime either applies them or fails honestly
- [ ] the daemon does not silently pretend Linux security controls were enforced if they were not
- [ ] dangerous Docker overrides remain blocked unless explicitly enabled

Optional explicit check with configured profiles/labels:

```bash
SANDBOX_DEPLOYMENT_PROFILE=dev-trusted-docker \
SANDBOX_DOCKER_SECCOMP_PROFILE=/path/to/seccomp.json \
SANDBOX_DOCKER_APPARMOR_PROFILE=or3-default \
SANDBOX_DOCKER_SELINUX_LABEL=type:or3_t \
go run ./cmd/sandboxd \
  -listen :8080 \
  -db ./data/sandbox.db \
  -storage-root ./data/storage \
  -snapshot-root ./data/snapshots
```

- [ ] startup behavior is explicit and believable with Linux security settings enabled

---

## 11. Lane A — runtime health, capacity, quota, and metrics after activity

```bash
go run ./cmd/sandboxctl runtime-health
go run ./cmd/sandboxctl quota
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/v1/runtime/capacity"
curl -fsS -H "Authorization: Bearer $SANDBOX_TOKEN" "$SANDBOX_API/metrics" | grep 'or3_sandbox_' | head -50
```

- [x] health output reflects current sandbox states
- [x] capacity output reflects current counts or pressure signals
- [x] metrics expose sensible counters after your activity
- [x] nothing looks stuck or obviously stale

---

## 12. Lane A — daemon restart reconciliation

### 12.1 Prepare state to survive restart

```bash
go run ./cmd/sandboxctl start "$SANDBOX_ID"
go run ./cmd/sandboxctl exec "$SANDBOX_ID" sh -lc 'echo before-restart > /workspace/restart-check.txt'
```

### 12.2 Restart `sandboxd`

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

- [x] daemon returns healthy after restart
- [x] sandbox metadata is still present
- [x] sandbox state is conservative and believable after reconcile
- [x] workspace data survives the restart
- [x] exec still works after reconcile

---

## 13. Lane A — cleanup pass

Delete all disposable resources you created:

```bash
go run ./cmd/sandboxctl delete "$SANDBOX_ID"
go run ./cmd/sandboxctl delete "$BLOCKED_SANDBOX_ID"
go run ./cmd/sandboxctl delete "$RESTORE_TARGET_ID"
go run ./cmd/sandboxctl list
```

- [x] deletes succeed
- [ ] BROKEN: `list` still shows deleted resources with `status: deleted`

Optional host cleanup:

```bash
rm -f ./.tmp/from-sandbox.txt "$TEST_FILE_LOCAL"
```

---

## 14. Lane A/D — optional preset checks

### 14.1 Preset discovery

```bash
go run ./cmd/sandboxctl preset list
go run ./cmd/sandboxctl preset inspect playwright
go run ./cmd/sandboxctl preset inspect openclaw
```

- [ ] BROKEN: `preset list` failed because `examples/qemu-service/preset.yaml` has an invalid `runtime.profile`
- [x] inspect output is readable and useful

### 14.2 Playwright preset

```bash
go run ./cmd/sandboxctl preset run playwright --cleanup on-success
```

- [ ] BROKEN: the Playwright preset failed because the image did not provide the `playwright` Node module
- [ ] BROKEN: no screenshot artifact was downloaded
- [ ] BROKEN: the preset exited with module resolution failure before artifact output

### 14.3 OpenClaw preset (requires secrets)

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

## 15. Lane B — optional Linux-only Kata pass

Run this only on a Linux host with containerd + Kata installed.

### 15.1 Start with Kata enabled

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

- [ ] daemon starts with Kata enabled
- [ ] startup failures point to missing `ctr`, socket, or runtime class problems

### 15.2 Repeat the core flow

Repeat sections 2 through 13 with `--runtime containerd-kata-professional`.

Pay special attention to these Kata-specific expectations:

- [ ] create/list/inspect/exec/files/tunnels/snapshots work normally
- [ ] `suspend` and `resume` fail with a clear unsupported error
- [ ] `disk-mb` is treated as advisory rather than a strict filesystem promise

---

## 16. Lane C — optional Linux/KVM QEMU production pass

Run this only on a prepared Linux/KVM host.

Prep expectations:

- [ ] `SANDBOX_QEMU_BINARY` is set
- [ ] `SANDBOX_QEMU_BASE_IMAGE_PATH` is set
- [ ] guest image is prepared and matches the control mode you intend to test

### 16.1 Preflight checks

```bash
go run ./cmd/sandboxctl config-lint
go run ./cmd/sandboxctl doctor --production-qemu
go run ./cmd/sandboxctl image list
```

- [ ] BROKEN: QEMU `config-lint` failed before runtime use because no valid base image was available during pass 1
- [x] doctor output is believable and actionable
- [ ] BROKEN: `image list` was not reachable in a useful QEMU state during pass 1 because no valid prepared guest image completed

### 16.2 Start a QEMU-capable daemon

Use the repo’s current documented QEMU posture for your Linux host. A common agent-first example is:

```bash
SANDBOX_ENABLED_RUNTIMES=qemu-professional \
SANDBOX_DEFAULT_RUNTIME=qemu-professional \
SANDBOX_QEMU_BINARY=qemu-system-x86_64 \
SANDBOX_QEMU_ACCEL=kvm \
SANDBOX_QEMU_BASE_IMAGE_PATH=$PWD/images/guest/base.qcow2 \
go run ./cmd/sandboxd \
  -listen :8080 \
  -db ./data/sandbox.db \
  -storage-root ./data/storage \
  -snapshot-root ./data/snapshots
```

If you intentionally use `ssh-compat`, also export the matching SSH variables.

- [ ] daemon starts with QEMU enabled
- [ ] startup failures clearly identify binary/image/key/KVM problems

### 16.3 Repeat the core flow with QEMU

Repeat sections 2 through 13 using a QEMU-capable image and `--runtime qemu-professional`.

Pay special attention to:

- [ ] boot and readiness speed
- [ ] exec and TTY usability
- [ ] file persistence
- [ ] tunnel behavior
- [ ] snapshot create and restore
- [ ] suspend and resume
- [ ] daemon restart reconciliation

### 16.4 Run the shipped host-gated drills

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
- [ ] release-gate gives one bounded top-level pass/fail summary

### 16.5 Image promotion flow

```bash
go run ./cmd/sandboxctl image promote --image "$SANDBOX_QEMU_BASE_IMAGE_PATH"
go run ./cmd/sandboxctl image list
```

- [ ] promote succeeds for the intended image
- [ ] promoted image appears in the list afterward

---

## 17. Success criteria

I would consider the Linux pass successful if all of these are true:

- [ ] the Docker lane passes end-to-end
- [ ] Linux Docker enforcement behavior is believable and honest
- [ ] workspace persistence survives lifecycle transitions and daemon restart
- [ ] tunnel creation, browser launch, and revocation behave safely
- [ ] snapshot restore really restores older workspace state
- [ ] operator surfaces remain believable after heavy activity
- [ ] Kata and QEMU lanes are either tested or marked `N/A` with a reason
- [ ] any production claim is backed by the Linux/KVM QEMU host-gated drills

---

## 18. Suggested results template

```text
Date:
Host:
Lane A (Linux Docker): PASS | FAIL
Lane B (Linux Kata): PASS | FAIL | N/A
Lane C (Linux QEMU): PASS | FAIL | N/A
Lane D (OpenClaw/secrets): PASS | FAIL | N/A

Main sandbox IDs:
Tunnel IDs:
Snapshot IDs:

Biggest failures seen:
Most convincing success signals:
Follow-up fixes to make:
```
