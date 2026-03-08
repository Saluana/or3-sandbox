# Project Overview

## The big idea

`or3-sandbox` gives you a way to create **small isolated work environments** on one machine.

Think of it like this:

- the **server** is a manager
- each **sandbox** is a separate room
- users can only work inside their own room
- the manager keeps track of limits, files, and activity

This is useful when you want to give someone a workspace that can run commands, store files, and stay around between sessions.

## Core goals

The codebase is built around these goals:

- create durable sandboxes that do not disappear after one command
- keep a clear owner for each sandbox using bearer-token auth
- put limits on CPU, memory, process count, disk, and tunnels
- let users run commands, open an interactive terminal, move files, and expose selected ports
- store metadata in SQLite so the daemon can recover state after restart

## Main parts of the project

### `sandboxd`

`sandboxd` is the control plane server.

It:

- listens for HTTP requests
- checks auth tokens
- loads and saves sandbox data in SQLite
- talks to the selected runtime (`docker` or `qemu`)
- keeps runtime state in sync with saved state

### `sandboxctl`

`sandboxctl` is the CLI tool.

It:

- sends requests to `sandboxd`
- uses `SANDBOX_API` for the server URL
- uses `SANDBOX_TOKEN` for auth
- supports commands like `create`, `list`, `exec`, `tty`, `upload`, and `delete`

### SQLite database

SQLite stores the project's metadata, such as:

- sandboxes
- tenants
- quotas
- snapshots
- tunnels
- execution records
- audit events

SQLite is a good fit here because this project is designed for **single-node** use.

## What a sandbox includes

Each sandbox has:

- an ID
- an owner (tenant)
- a status like `creating`, `running`, or `stopped`
- resource limits such as CPU, memory, pids, and disk
- a network mode
- optional tunnel access
- runtime-specific state such as a container ID or VM process ID

## Sandboxes are meant to be durable

A sandbox is not just a one-time command runner.

The project is built so that a sandbox can:

- stay around after it is created
- keep workspace files
- be stopped and started again
- be inspected later

That is why the code stores both **control-plane state** and **runtime state**.

## Security model in plain language

The project supports two runtime styles:

### Docker runtime

- easiest path today
- best for local development, demos, and trusted environments
- **not** described as a strong hostile multi-tenant production barrier

### QEMU runtime

- more production-like because each sandbox is a guest machine
- uses SSH to control the guest
- still under active development and validation

A simple way to remember it:

- Docker = easier now
- QEMU = stronger long-term direction

## Networking in plain language

A sandbox can be created with:

- `internet-enabled`
- `internet-disabled`

Tunnels are also explicit. That means a sandbox does **not** automatically publish ports to the host. Instead, the daemon creates controlled tunnel endpoints when allowed.

## Features you can use today

The current codebase includes support for:

- creating sandboxes
- starting, stopping, suspending, resuming, and deleting sandboxes
- running commands
- opening an interactive terminal
- uploading and downloading files
- creating directories
- creating and revoking tunnels
- checking quotas
- checking runtime health
- creating snapshots through the HTTP API

## Important limitation to know

The CLI does **not** currently expose every server feature. For example, the daemon has snapshot API routes, but `sandboxctl` does not yet include snapshot commands.

## When to use this project

This project makes sense when you want:

- one machine managing sandbox workspaces
- a Go control plane instead of a large cluster system
- a clean place to experiment with runtime isolation ideas
- a foundation for durable coding or automation sandboxes

If you want a large distributed platform with many machines, this project is intentionally smaller than that.
