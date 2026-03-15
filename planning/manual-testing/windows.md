# Manual Testing Plan — Windows

This checklist is for a Windows host.

Native Windows is not a supported runtime host in this repo’s setup docs. The documented easy path is macOS or Linux, Kata is Linux-only, and native QEMU auto-configuration rejects Windows hosts.

Because of that, this plan is split into two parts:

- native Windows checks that should still be run on the Windows machine
- a required handoff into WSL2 or a Linux VM for any real runtime, Docker, Kata, or QEMU sandbox behavior

If you need full runtime/manual coverage, use this file to validate the Windows host posture and then run `planning/manual-testing/linux.md` inside WSL2 or a Linux VM.

---

## 0. What Windows should validate

### Lane A — native Windows host checks (required)

Covers:

- repo checkout and toolchain sanity
- host-agnostic control-plane smoke tests
- expected failure behavior for unsupported native Kata and QEMU runtime setups
- documentation and operator handoff readiness into Linux

### Lane B — Linux handoff from Windows (required for runtime coverage)

Covers:

- WSL2 or Linux VM readiness
- Docker/Kata/QEMU runtime testing on the supported Linux side
- production-style QEMU evidence if needed

### Lane C — secret-backed pass (optional)

Covers:

- `openclaw` preset with browser UI, but only from the supported Linux side
- any workflow requiring external API keys or tokens

---

## 1. Native Windows prep checklist

Mark these before you start:

- [ ] Go is installed on Windows
- [ ] Git is installed and the repo is checked out locally
- [ ] You have PowerShell available
- [ ] You have WSL2 or a Linux VM available for the supported runtime lanes
- [ ] You are in the repo root

Recommended scratch variables in PowerShell:

```powershell
Set-Location C:\path\to\or3-sandbox
$env:MANUAL_LOG = "$PWD\.tmp\manual-test-notes.txt"
New-Item -ItemType Directory -Force .tmp | Out-Null
Set-Content -Path $env:MANUAL_LOG -Value ""
```

---

## 2. Lane A — native Windows host checks

### 2.1 Repository and toolchain sanity

```powershell
go version
git status --short
```

- [ ] `go version` works
- [ ] repo is present and readable
- [ ] nothing obviously blocks local inspection or test execution

### 2.2 Host-agnostic control-plane smoke

The repo documents a fast smoke gate for non-host-specific packages. Run the equivalent `go test` command directly from PowerShell:

```powershell
go test ./internal/config ./internal/auth ./internal/service ./internal/api ./cmd/sandboxctl
```

- [ ] package smoke either passes or produces actionable failures
- [ ] failures are recorded with enough detail to distinguish repo regressions from Windows-environment issues
- [ ] results are treated as control-plane evidence only, not runtime-host evidence

### 2.3 Native Windows QEMU rejection should be explicit

Set a minimal fake QEMU configuration and confirm `config-lint` fails because native Windows is unsupported for the QEMU runtime auto path.

```powershell
$env:SANDBOX_RUNTIME = "qemu"
$env:SANDBOX_QEMU_BINARY = "qemu-system-x86_64"
$env:SANDBOX_QEMU_BASE_IMAGE_PATH = "C:\temp\base.qcow2"
go run ./cmd/sandboxctl config-lint
```

Expected result:

- [ ] config lint fails clearly on native Windows
- [ ] the error says the QEMU runtime or accelerator is unsupported on host OS `windows`
- [ ] failure happens early, before any misleading create-time behavior

Afterward, clear the vars:

```powershell
Remove-Item Env:SANDBOX_RUNTIME -ErrorAction SilentlyContinue
Remove-Item Env:SANDBOX_QEMU_BINARY -ErrorAction SilentlyContinue
Remove-Item Env:SANDBOX_QEMU_BASE_IMAGE_PATH -ErrorAction SilentlyContinue
```

### 2.4 Native Windows Kata rejection should be explicit

Set a minimal Kata configuration and confirm `config-lint` fails because Kata is Linux-only.

```powershell
$env:SANDBOX_ENABLED_RUNTIMES = "containerd-kata-professional"
$env:SANDBOX_DEFAULT_RUNTIME = "containerd-kata-professional"
$env:SANDBOX_KATA_BINARY = "ctr"
$env:SANDBOX_KATA_RUNTIME_CLASS = "io.containerd.kata.v2"
$env:SANDBOX_KATA_CONTAINERD_SOCKET = "\\.\pipe\containerd-containerd"
go run ./cmd/sandboxctl config-lint
```

Expected result:

- [ ] config lint fails clearly on native Windows
- [ ] the error says Kata requires Linux
- [ ] failure happens early, before any misleading runtime behavior

Afterward, clear the vars:

```powershell
Remove-Item Env:SANDBOX_ENABLED_RUNTIMES -ErrorAction SilentlyContinue
Remove-Item Env:SANDBOX_DEFAULT_RUNTIME -ErrorAction SilentlyContinue
Remove-Item Env:SANDBOX_KATA_BINARY -ErrorAction SilentlyContinue
Remove-Item Env:SANDBOX_KATA_RUNTIME_CLASS -ErrorAction SilentlyContinue
Remove-Item Env:SANDBOX_KATA_CONTAINERD_SOCKET -ErrorAction SilentlyContinue
```

### 2.5 Native Windows outcome review

- [ ] Windows-native results are written down as either supported control-plane smoke or expected unsupported-runtime failures
- [ ] nobody mistakes native Windows results for supported Docker/Kata/QEMU runtime evidence
- [ ] next-step handoff into Linux is clear

---

## 3. Lane B — hand off into WSL2 or a Linux VM

This lane is required if you want to test any real sandbox runtime behavior.

### 3.1 Confirm the Linux side exists and is usable

Examples:

```powershell
wsl.exe -l -v
wsl.exe uname -a
```

Or, if using a Linux VM, verify you can log in and access the repo there.

- [ ] WSL2 or the Linux VM is available
- [ ] the Linux environment can reach the repo contents
- [ ] the Linux environment can run Go commands

### 3.2 Decide which Linux plan applies

Use the Linux plan from the supported environment:

- use `planning/manual-testing/linux.md` for the full supported Linux workflow
- if you only need trusted Docker validation, you can stop after the Linux Docker lane
- if you need Kata or QEMU evidence, stay on the Linux host and complete those lanes there

- [ ] a concrete Linux target environment is chosen
- [ ] the Linux plan is the source of truth for runtime behavior from this point onward

### 3.3 If Docker runtime coverage is needed

Inside WSL2 or the Linux VM, run the Docker lane from `planning/manual-testing/linux.md`.

- [ ] Docker daemon reachability is validated from the Linux side
- [ ] sandbox lifecycle, files, tunnels, snapshots, metrics, and restart reconciliation are tested there

### 3.4 If Kata runtime coverage is needed

Inside WSL2 or the Linux VM, only proceed if containerd + Kata are truly installed.

- [ ] Kata testing is run only on Linux
- [ ] unsupported native Windows host posture is not used for Kata claims

### 3.5 If QEMU production coverage is needed

Inside WSL2 or the Linux VM, only proceed if the environment is a real prepared Linux/KVM host. Plain Windows-native execution is not sufficient.

- [ ] QEMU production testing is run only on prepared Linux/KVM
- [ ] host verification, smoke, abuse, recovery, and release-gate are run only there

---

## 4. Lane C — optional secret-backed checks

Run these only after the Linux handoff is complete.

Use the secret-backed lane from `planning/manual-testing/linux.md`.

- [ ] secrets are handled only from the supported Linux-side runtime workflow
- [ ] any browser/UI validation is recorded with the Linux-side sandbox/tunnel IDs

---

## 5. Success criteria

I would consider the Windows pass successful if all of these are true:

- [ ] native Windows control-plane smoke is run and results are recorded honestly
- [ ] native Windows unsupported Kata/QEMU behavior fails early and clearly
- [ ] no one treats native Windows as a supported runtime-host result
- [ ] any real runtime validation is handed off into WSL2 or a Linux VM
- [ ] any production-style QEMU claim is backed by a prepared Linux/KVM host, not Windows-native execution

---

## 6. Suggested results template

```text
Date:
Host:
Lane A (Windows native checks): PASS | FAIL
Lane B (Windows -> Linux handoff): PASS | FAIL
Lane C (OpenClaw/secrets): PASS | FAIL | N/A

Linux target used for runtime testing:
Native Windows failure messages captured:

Biggest failures seen:
Most convincing success signals:
Follow-up fixes to make:
```
