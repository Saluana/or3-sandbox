# OpenSandbox-Style Example Presets Requirements

## Overview

This plan defines the functionality needed before adding polished example presets like the OpenSandbox `openclaw`, `claude-code`, and `playwright` examples.

The goal is **not** to copy OpenSandbox's Python SDK design directly. This repository is a Go, CLI-first, single-node sandbox control plane. The implementation should feel native to `or3-sandbox` while still supporting the same practical outcomes:

- launch a sandbox from a named preset
- apply preset defaults for image, resources, and startup behavior
- seed files into the workspace
- run bootstrap and startup commands
- wait for the workload to become ready
- expose a tunnel when needed
- download generated artifacts when needed

Assumptions:

- Initial preset support targets the shipped Docker runtime path, because the referenced OpenSandbox examples rely on container images such as Playwright, OpenClaw, and CLI-oriented Node/Python images.
- QEMU remains the stronger production boundary, but these example presets should not pretend that arbitrary Docker images automatically map onto the current guest-image-based QEMU flow.
- Presets should be repo-local files and should not require a new distributed service, frontend, or external scheduler.

## Requirements

1. **Preset catalog and manifest format**
   - The system must support a repo-local preset definition format that can describe example workflows such as `claude-code`, `playwright`, and `openclaw`.
   - Acceptance criteria:
     - A preset manifest can declare image, resource defaults, startup mode, optional env inputs, optional file assets, optional bootstrap commands, optional detached service commands, readiness checks, optional tunnel creation, and optional artifact downloads.
     - Invalid manifests fail fast with clear validation errors.
     - Manifest loading does not require SQLite schema changes.

2. **CLI-first preset execution**
   - The primary user flow must be a CLI command that runs a preset against an existing `sandboxd` server.
   - Acceptance criteria:
     - A user can list available presets from the repo.
     - A user can run a named preset with minimal flags.
     - The CLI creates the sandbox, runs preset steps in order, prints progress, and surfaces the final sandbox ID and any endpoints or downloaded artifacts.
     - The CLI can optionally preserve or delete the sandbox after completion.

3. **Preset-scoped sandbox creation defaults**
   - Presets must be able to supply sandbox creation defaults without forcing users to manually re-enter image and resource values.
   - Acceptance criteria:
     - A preset can define the image, CPU, memory, PIDs, disk, network mode, allow-tunnels flag, and auto-start behavior.
     - User-supplied CLI overrides can replace selected preset defaults.
     - Existing raw `sandboxctl create` behavior remains backward compatible.

4. **Bootstrap and runtime command orchestration**
   - Presets must be able to run one-time setup commands and optional long-running service commands.
   - Acceptance criteria:
     - A preset can run ordered bootstrap commands after sandbox creation.
     - A preset can start a detached process for gateway- or service-style examples.
     - Each command has a bounded timeout and clear failure output.
     - Failures stop the preset run unless the manifest explicitly allows a best-effort step.

5. **Environment injection for preset commands**
   - Presets must be able to inject environment variables into bootstrap and runtime commands for workflows like Claude Code and similar CLIs.
   - Acceptance criteria:
     - A preset can declare required env inputs and optional env inputs.
     - The runner passes env values to exec and detached startup commands without logging secret values.
     - Missing required env inputs fail fast before sandbox creation or before the relevant step runs.

6. **Workspace seeding and asset writing**
   - Presets must be able to place files into the sandbox before or during execution.
   - Acceptance criteria:
     - A preset can write small text assets directly from manifest content or from repo-local files.
     - File writes are applied in a deterministic order.
     - Existing path safety rules remain enforced.

7. **Readiness checks and endpoint exposure**
   - Presets that launch services must be able to wait for readiness and publish usable endpoints.
   - Acceptance criteria:
     - A preset can define a readiness probe using either command success or tunnel-backed HTTP checks.
     - A readiness probe has a bounded timeout and retry interval.
     - A preset can request tunnel creation after the service starts.
     - The CLI prints the resulting proxy endpoint and any required access token information.

8. **Binary-safe artifact download**
   - The system must support downloading generated binary artifacts such as screenshots.
   - Acceptance criteria:
     - The API and CLI can transfer binary file content without corrupting non-UTF-8 bytes.
     - The feature works for screenshot-style artifacts produced by a Playwright preset.
     - Existing text file read/write flows remain backward compatible.

9. **Preset lifecycle cleanup**
   - Preset runs must support predictable cleanup behavior.
   - Acceptance criteria:
     - The CLI can delete the sandbox automatically on success, automatically on failure, or preserve it for inspection.
     - Cleanup failures are reported clearly without hiding the original execution error.
     - The sandbox ID is always surfaced to the user before cleanup decisions are applied.

10. **Docs and examples parity at the user level**
    - The repo must include a usable example directory after the functionality exists.
    - Acceptance criteria:
      - Each shipped preset has a README with prerequisites, required env vars, launch command, and expected outcome.
      - At least three examples are supported in the first pass: one CLI example, one browser/artifact example, and one service/tunnel example.
      - The docs explicitly state where behavior intentionally differs from OpenSandbox.

## Non-functional constraints

- Behavior must remain deterministic and bounded:
  - bounded command timeouts
  - bounded readiness polling
  - bounded output previews
  - bounded retry loops
- Memory and implementation complexity must stay low:
  - prefer a file-backed preset catalog and CLI orchestration over adding a new daemon-side job system
- SQLite compatibility must be preserved:
  - no migration is required unless a later phase chooses to persist preset-run history
- Security defaults must remain intact:
  - no secret values in logs
  - no unbounded network behavior hidden in preset helpers
  - file access stays restricted to existing workspace-safe paths
  - public tunnels must still respect current operator policy controls
- Backward compatibility matters for:
  - existing create/exec/file/tunnel APIs
  - existing CLI subcommands
  - existing Docker and QEMU flows outside the new preset runner
