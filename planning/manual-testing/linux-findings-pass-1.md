# Linux Findings Pass 1

Date: 2026-03-15
Host: Ubuntu 22.04
Scope: Lane A executed as far as possible, Lane B preflight only, Lane C preflight plus guest-image build debugging, Lane D preset inspection only

## Summary

Lane A (Linux Docker): FAIL
Lane B (Linux Kata): N/A
Lane C (Linux QEMU): N/A
Lane D (OpenClaw/secrets): N/A

Main sandbox IDs:

- `sbx-64571384cac00df2`
- `sbx-19ad9808e7a64670`
- `sbx-68208c2e53306d89`
- `sbx-432cd06890d6bb7a`

Tunnel IDs:

- `tun-34178a84a65c77a7`
- `tun-5b1600047232431b`
- `tun-a36d612216dacfc5`

Snapshot IDs:

- `snap-b89c954ecb99698c`

## What Passed

- Docker trusted-profile config lint, daemon startup, health, runtime info, quota, capacity, and metrics all worked.
- Sandbox create/list/inspect/start/stop/delete basically worked, even though `sandboxctl create` output was not reliable for JSON capture in this pass.
- Basic exec, timeout handling, suspend/resume on a running Docker sandbox, stop/start persistence, force stop, snapshot create, snapshot restore, and daemon restart reconciliation all worked.
- Outbound network access worked for `internet-enabled` sandboxes over plain HTTP.
- `internet-disabled` sandboxes blocked outbound DNS/network access as expected.
- Signed tunnel URLs produced the expected bootstrap page and revocation correctly returned `410 Gone`.
- Preset inspection for `playwright` and `openclaw` worked.

## Broken Findings

- Protected auth failures are plain text `unauthorized`, not JSON.
- `/workspace` was not writable from `sandboxctl exec` as the sandbox workload user in the exact checklist flow.
- Absolute file API paths such as `/workspace/from-host.txt` and `/workspace/demo/subdir` were rewritten under `/workspace/workspace/...`.
- The exact detached exec checklist flow failed because the `/workspace` target file never appeared.
- The TTY transport returned useful output but detached with websocket close `1006` in scripted use.
- The checklist BusyBox `httpd` command did not yield a reachable service.
- Tunnel proxy requests consistently returned `502 Bad Gateway`, even when loopback access inside the sandbox was working via a minimal responder.
- `preset list` failed because `examples/qemu-service/preset.yaml` has an invalid `runtime.profile`.
- `preset run playwright` failed because the sandbox image did not provide the `playwright` Node module.
- After cleanup, `sandboxctl list` still showed deleted resources with `status: deleted` instead of an empty list.

## Kata Notes

- `ctr` exists on the host.
- The Kata config-lint path passed.
- The current blocker is operator access to `/run/containerd/containerd.sock`, which is root-owned on this host.

## QEMU Notes

- `qemu-system-x86_64`, `qemu-img`, `cloud-localds`, and `/dev/kvm` are present.
- `sandboxctl doctor --production-qemu` produced believable host-level output.
- The guest-image build path had two builder bugs fixed in pass 1:
  - oversized environment payload when passing the guest-agent binary through the environment
  - invalid YAML rendering from unindented multiline cloud-init substitutions
- Even after those fixes, the guest-image build still did not complete a valid prepared image with sidecar artifacts, so QEMU remains blocked on the in-guest readiness path.

## Files Changed During Pass 1

- [README.md](/home/brendon/Documents/or3/or3-sandbox/README.md): added Linux Docker group guidance
- [docs/setup.md](/home/brendon/Documents/or3/or3-sandbox/docs/setup.md): added Linux Docker group guidance
- [images/guest/build-base-image.sh](/home/brendon/Documents/or3/or3-sandbox/images/guest/build-base-image.sh): fixed guest-image builder environment and cloud-init rendering issues

## Recommended Next Fixes

1. Fix Docker file API absolute-path handling so `/workspace/...` does not become `/workspace/workspace/...`.
2. Fix workload ownership or mount permissions so `/workspace` is writable by the sandbox workload user during normal `exec` flows.
3. Fix tunnel proxy bridging so a healthy in-sandbox listener is reachable through the proxy endpoint.
4. Fix `preset list` by correcting the invalid `runtime.profile` in `examples/qemu-service/preset.yaml`.
5. Fix the Playwright preset image or bootstrap so `require('playwright')` resolves.
6. Continue debugging the QEMU guest-image readiness path now that the two builder issues are removed.