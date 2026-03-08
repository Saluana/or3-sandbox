# QEMU Review

## Localhost SSH trust is fake security

Code:
- `internal/runtime/qemu/runtime.go:463-476`
- `internal/runtime/qemu/runtime.go:479-495`
- `internal/runtime/qemu/runtime.go:549-562`
- `internal/runtime/qemu/runtime.go:843-865`

Why this is bad:
The runtime does a check-then-bind port allocation dance for guest SSH. It finds a free localhost port, closes it, then asks QEMU to use it later. That is a race. Any local process can grab the port first. The SSH client then compounds the mistake by setting `StrictHostKeyChecking=no`, so it will happily talk to whoever answered first. That means readiness checks, exec, file ops, and TTY can all hit the wrong process while the code pretends it is controlling the guest.

Real-world consequences:
Local code running under the same host can impersonate the guest control channel. In the best case, sandbox startup flakes randomly. In the worse case, a malicious local process can feed fake readiness, capture commands, or return fake file contents while the daemon reports a healthy VM.

Concrete fix:
Stop using a release-and-reacquire port allocation flow. Reserve the listener until QEMU is ready to inherit it, or switch to a Unix socket or vsock-based control path. Also stop disabling host verification. Pin the expected guest host key and fail if it changes unexpectedly.

## The pidfile can target the wrong host process

Code:
- `internal/runtime/qemu/runtime.go:190-211`
- `internal/runtime/qemu/runtime.go:214-238`
- `internal/runtime/qemu/runtime.go:241-267`
- `internal/runtime/qemu/runtime.go:275-337`
- `internal/runtime/qemu/runtime.go:754-763`

Why this is bad:
The runtime treats `.runtime/qemu.pid` as a trustworthy identity document. It parses an integer and starts sending signals. That is not identity verification. PIDs get reused. Stale pidfiles exist. Tampered pidfiles exist. There is no check that the PID still belongs to the intended QEMU process, or even to QEMU at all.

Real-world consequences:
`stop`, `suspend`, `resume`, and `inspect` can act on an unrelated same-user process. That is the kind of host-side footgun that turns a sandbox daemon into an accidental process killer.

Concrete fix:
Verify the process identity before signaling it. At minimum, inspect the command line or executable path and ensure it matches the expected QEMU invocation and storage layout. Better yet, keep an authenticated runtime handle instead of trusting a text pidfile.

## Tenants can pick arbitrary host-readable guest images

Code:
- `internal/service/service.go:61-177`
- `internal/service/policy.go:13-19`
- `internal/service/policy.go:66-86`
- `internal/runtime/qemu/runtime.go:565-570`

Why this is bad:
The create path accepts tenant-controlled `base_image_ref`, persists it, and passes it through to QEMU. The QEMU runtime then treats any readable file path as the image to boot. The policy layer only does naive string matching. That is not an operator-controlled image boundary. That is “if the service account can read it, a tenant might be able to boot it.”

Real-world consequences:
A tenant can boot arbitrary host-readable qcow2 or raw images instead of the intended operator-approved base image. That bypasses the supposed deployment model and destroys any confidence in image provenance.

Concrete fix:
For QEMU, do not accept arbitrary filesystem paths from tenants. Accept only named profiles or image IDs from an operator-managed allowlist. Resolve those IDs server-side to vetted paths outside tenant control.

## The API can say one image while QEMU boots another

Code:
- `internal/config/config.go:79-80`
- `internal/service/service.go:171-176`
- `internal/service/service.go:1128-1152`
- `internal/runtime/qemu/runtime.go:565-570`

Why this is bad:
The daemon default base image is a Docker image string. `applyCreateDefaults` applies it regardless of runtime. Then QEMU ignores non-file refs and silently falls back to `SANDBOX_QEMU_BASE_IMAGE_PATH`. So the stored sandbox record, policy checks, and audit trail can all describe one image while the runtime boots a different one.

Real-world consequences:
Operators debug the wrong image. Audits are misleading. Policy appears to pass on one value while the machine actually ran something else. That is exactly how incident response gets derailed by bad metadata.

Concrete fix:
Make QEMU image resolution explicit and validated at create time. If the request does not resolve to a valid QEMU-approved image, fail the request. Do not silently substitute a fallback image behind the user's back.

## Failed QEMU creates leak disks and storage junk

Code:
- `internal/service/service.go:80-88`
- `internal/service/service.go:128-153`
- `internal/runtime/qemu/runtime.go:143-156`

Why this is bad:
The service eagerly creates storage directories. QEMU `Create` eagerly materializes overlay and workspace disks. If create or boot fails, the sandbox is marked `error` and the garbage is left on disk. There is no rollback and no best-effort cleanup.

Real-world consequences:
Repeated bad boot attempts accumulate orphaned disk images until someone cleans them up manually. That is both a reliability bug and a quota-pressure attack surface.

Concrete fix:
On create or start failure, destroy the partially created runtime artifacts and remove the storage tree unless you have a specific reason to preserve it for forensics. If you do preserve it, mark it explicitly and exclude it from normal quotas only if the operator asked for that behavior.

## Snapshots are crashy file copies pretending to be a feature

Code:
- `internal/runtime/qemu/runtime.go:340-358`
- `internal/runtime/qemu/runtime.go:361-378`

Why this is bad:
Snapshot creation and restore are plain file copies of live disk artifacts. No guest quiesce. No fsfreeze. No QMP checkpointing. No requirement that the guest be stopped. This is a thin wrapper around `copyFile`, not a reliable snapshot implementation.

Real-world consequences:
Running guests can produce torn snapshots. Restores can clone corrupted or inconsistent state. Users think they have a durable recovery point when they may just have a copy of a filesystem mid-write.

Concrete fix:
Either require the sandbox to be stopped before QEMU snapshot operations or implement real VM-coordinated snapshotting. If you cannot guarantee consistency, say so explicitly in the API and docs instead of implying a proper snapshot capability.

## Workspace path handling can escape `/workspace` inside the guest

Code:
- `internal/runtime/qemu/workspace.go:16-18`
- `internal/runtime/qemu/workspace.go:31-34`
- `internal/runtime/qemu/workspace.go:45-48`
- `internal/runtime/qemu/workspace.go:58-67`
- `internal/runtime/qemu/workspace.go:99-103`

Why this is bad:
`workspaceGuestPath` uses `path.Join("/workspace", filepath.ToSlash(relativePath))`. `path.Join` normalizes `..`. So input like `../../etc/shadow` becomes `/etc/shadow`, not `/workspace/../../etc/shadow`. The rest of the file APIs then happily use that result for read, write, mkdir, and delete operations.

Real-world consequences:
If upstream validation ever regresses or a new code path bypasses it, the guest workspace boundary is gone instantly. This is fragile design even if a higher layer currently sanitizes inputs.

Concrete fix:
Make the runtime enforce the workspace boundary itself. Reject any path that resolves outside `/workspace` after normalization. Do not rely on higher layers to protect a low-level file API.

## The detached exec status is a lie

Code:
- `internal/runtime/qemu/exec.go:30-45`
- `internal/service/service.go:460-472`

Why this is bad:
QEMU detached exec starts a `nohup` command and returns a handle whose result status is `running`. The service layer then ignores that and immediately records the execution as `succeeded`, with exit code `0` and a completion timestamp. That is fake bookkeeping. The process may still be starting, may fail instantly, or may never execute correctly.

Real-world consequences:
Audit records and execution history claim success for commands that were merely launched optimistically. Anyone using exec records for reliability, billing, or incident triage gets garbage data.

Concrete fix:
Represent detached execs as `running` until there is an actual completion signal, or define detached exec as fire-and-forget and store it as a separate launch event rather than a completed execution result.

## Boot failure scanning gets slower as logs grow

Code:
- `internal/runtime/qemu/runtime.go:497-517`
- `internal/runtime/qemu/runtime.go:520-537`

Why this is bad:
The readiness loop polls every 500 ms and reads the entire serial log each time, only to trim it down to the last 64 KiB afterwards. That is lazy, wasteful IO. The bigger the log gets, the more pointless work this loop does.

Real-world consequences:
Large serial logs make boot monitoring progressively more expensive. You get extra disk IO and allocations during the exact time the system is already under startup stress.

Concrete fix:
Seek to the tail you actually need and only read that range. If you want robust boot diagnostics, maintain a bounded in-memory tail instead of slurping the full file on every poll.

## The docs sell more QEMU confidence than the tests justify

Code:
- `README.md:7-23`
- `docs/operations/production-deployment.md:1-25`
- `docs/tutorials/qemu-runtime.md:116-125`
- `internal/runtime/qemu/runtime_test.go:254-310`
- `internal/runtime/qemu/host_integration_test.go:271-286`
- `cmd/sandboxctl/preset_integration_test.go:336-347`

Why this is bad:
The repository says QEMU is the stronger production path and documents suspend/resume as working, but the real-host coverage is optional and skipped unless someone hand-prepares a machine. The suspend/resume proof is a unit test that sends signals to `sleep`. That is not the same thing as proving a real guest resumes cleanly.

Real-world consequences:
Readers infer a level of production confidence that the test matrix does not support. When the real host behavior differs, the documentation has already oversold the feature.

Concrete fix:
Tone the docs down to match actual evidence, or invest in real automated host-level QEMU verification that runs often enough to justify the claims.

## `sandboxctl preset run` guesses the runtime based on an admin-only endpoint

Code:
- `cmd/sandboxctl/preset.go:581-609`
- `internal/api/router.go:91-106`

Why this is bad:
The preset runner tries to detect the backend by calling `/v1/runtime/health`, which requires admin inspection permission. If that call fails, it silently falls back to Docker unless the manifest only allows one runtime. That means runtime-specific validation depends on caller privilege instead of actual backend state.

Real-world consequences:
Normal tenant tokens can skip QEMU-only checks, send a Docker-shaped request to a QEMU daemon, and fail later with dumber errors. That is a lousy UX and a stupid control-flow dependency.

Concrete fix:
Expose runtime backend info through a non-admin capability endpoint or make the preset manifest/runtime selection explicit. Do not guess critical runtime semantics from an endpoint many callers are not allowed to read.
