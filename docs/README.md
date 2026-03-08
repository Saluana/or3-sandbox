# or3-sandbox Docs

Welcome to the `or3-sandbox` documentation.

This project is a **single-node sandbox control plane**. In simple words, it is a tool that lets one server create and manage isolated workspaces called **sandboxes**. Each sandbox can be started, stopped, inspected, used like a tiny machine, and cleaned up when you are done.

## What this project does

`or3-sandbox` has two main programs:

- `sandboxd`: the server (daemon) that creates and manages sandboxes
- `sandboxctl`: the command-line tool (CLI) you use to talk to the server

Right now, the most complete path is the **Docker runtime**. It is meant for **trusted** or **development** use. The project also includes a **QEMU guest runtime** for a more production-like setup, but that path is still newer and more limited.

## Honest project status

This part matters:

- **Docker is the shipped and easiest path today**
- **Docker is not treated as a hostile multi-tenant security boundary**
- **QEMU is the production-oriented direction**
- Some advanced features are still being improved, especially around guest images, recovery drills, and broader production testing

If you are learning the project or trying it on your own computer, start with Docker.

## Read these docs in order

1. [Project Overview](project-overview.md) — what the project is and what problems it solves
2. [Architecture](architecture.md) — how the main parts fit together
3. [Setup](setup.md) — how to install and run the project
4. [Configuration](configuration.md) — environment variables, flags, and safe defaults
5. [Usage Guide](usage.md) — how to create, run, and manage sandboxes
6. [Runtimes](runtimes.md) — Docker vs. QEMU, and when to use each one
7. [API Reference](api-reference.md) — the main HTTP endpoints and quick examples

## Tutorials

- [Tutorial 1: Your First Sandbox](tutorials/first-sandbox.md)
- [Tutorial 2: Files, Commands, and Tunnels](tutorials/files-and-tunnels.md)
- [Tutorial 3: Trying the QEMU Runtime](tutorials/qemu-runtime.md)

## Quick summary

A normal flow looks like this:

1. Start `sandboxd`
2. Set `SANDBOX_API` and `SANDBOX_TOKEN`
3. Use `sandboxctl create` to make a sandbox
4. Use `sandboxctl exec` or `sandboxctl tty` to work inside it
5. Use `sandboxctl stop` or `sandboxctl delete` when finished

If you only want the fastest path, jump to [Setup](setup.md) and then [Tutorial 1: Your First Sandbox](tutorials/first-sandbox.md).
