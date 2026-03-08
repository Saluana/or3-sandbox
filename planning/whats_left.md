# What Is Left For Sandbox v1

## 1. Current State

The repository now ships two real runtime paths:

- a trusted Docker-backed path that is the easiest way to run the project today
- a first-pass guest-backed QEMU path that provides the production-oriented VM boundary described by the newer planning work

The control plane itself is no longer the main missing piece.

The remaining work is mostly about **hardening, parity polishing, and making the guest-backed path boringly reliable**.

## 2. What Is Already In Place

These areas are substantially implemented in the current codebase:

- Go daemon, HTTP API, SQLite schema, auth, tenancy, and rate limiting
- trusted Docker runtime and first-pass guest-backed QEMU runtime
- sandbox lifecycle APIs and CLI commands
- exec streaming and PTY attach
- file operations inside the sandbox workspace
- tunnel creation and revocation
- snapshot create, list, inspect, and restore through both the API and `sandboxctl`
- optional snapshot bundle export and restore fetch flow
- reconciliation and runtime health inspection
- host integration coverage for QEMU workload claims such as Git, Python persistence, npm persistence, headless browser use, guest container engine use, and restart durability

## 3. Real Remaining Gaps

### 3.1 QEMU runtime hardening is still incomplete

The guest-backed runtime exists, but it is still a first-pass implementation.

The guest-backed path is real and now supports the main lifecycle, including `suspend` and `resume`, but it is still not as mature operationally as the trusted Docker path.

### 3.2 Production confidence still needs more polishing

The project has crossed the line from "missing runtime" to "hardening and operational confidence."

That remaining work includes things like:

- making guest runtime behavior more predictable under failure and recovery conditions
- keeping docs and planning files aligned with the code as the implementation evolves
- continuing to tighten operator experience around prepared guest images, host prerequisites, and troubleshooting

### 3.3 Storage semantics are still not equally strong across backends

The code now measures storage usage and the QEMU host integration coverage includes disk-full behavior.

Even so, storage behavior is still not identical across backends:

- the guest-backed path is closer to real bounded storage behavior
- the trusted Docker path is still a more development-oriented environment

So storage enforcement should be treated as **stronger in the guest-backed path than in the trusted Docker path**.

### 3.4 Multi-node and extra runtime backends remain out of scope

The project is still intentionally:

- single-node
- SQLite-backed
- focused on `docker` and `qemu`

That is a product choice, not a bug.

## 4. Bottom Line

The old story was:

- "the control plane exists, but the guest-backed runtime is still missing"

The current story is:

- "the control plane and first-pass guest-backed runtime both exist, and the main remaining work is hardening and finishing the rough edges"

## 5. Honest Positioning Right Now

The most accurate product statement today is:

- trusted or development-ready Docker-backed sandbox control plane: yes
- first-pass guest-backed QEMU runtime with real lifecycle, readiness, file, exec, TTY, snapshot, and workload coverage: yes
- fully hardened production-ready guest runtime with every lifecycle feature and polished operator ergonomics: not yet

Next-step planning references:

- `planning/gap-closure/tasks.md`
- `planning/onwards/requirements.md`
- `planning/onwards/design.md`
- `planning/onwards/tasks.md`
