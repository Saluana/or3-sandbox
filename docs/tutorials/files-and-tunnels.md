# Tutorial 2: Files, Commands, and Tunnels

This tutorial shows how to move files, make folders, and expose a service through a tunnel.

Before you start, make sure:

- `sandboxd` is already running
- your client environment variables are set
- you have a running sandbox

If not, complete [Tutorial 1](first-sandbox.md) first.

## Step 1: Create a local file

On your host machine, run:

```bash
echo 'Hello from my laptop' > hello.txt
```

## Step 2: Upload the file

```bash
go run ./cmd/sandboxctl upload <sandbox-id> ./hello.txt /workspace/hello.txt
```

## Step 3: Read it inside the sandbox

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc 'cat /workspace/hello.txt'
```

## Step 4: Make a folder

```bash
go run ./cmd/sandboxctl mkdir <sandbox-id> /workspace/demo
```

Add another file inside that folder:

```bash
go run ./cmd/sandboxctl exec <sandbox-id> sh -lc 'echo second-file > /workspace/demo/second.txt'
```

## Step 5: Download a file back to your machine

```bash
go run ./cmd/sandboxctl download <sandbox-id> /workspace/demo/second.txt ./second-copy.txt
```

Check it:

```bash
cat ./second-copy.txt
```

## Step 6: Start a simple web server inside the sandbox

This example uses Python if it exists in the image.

```bash
go run ./cmd/sandboxctl exec --detached <sandbox-id> sh -lc 'cd /workspace && python3 -m http.server 3000'
```

If your image does not have Python, use an image that does, or start a service another way.

## Step 7: Create a tunnel

```bash
go run ./cmd/sandboxctl tunnel-create --port 3000 <sandbox-id>
```

The result includes tunnel information such as:

- tunnel ID
- endpoint
- protocol
- auth settings

## Step 8: List tunnels

```bash
go run ./cmd/sandboxctl tunnel-list <sandbox-id>
```

## Step 9: Revoke a tunnel when done

```bash
go run ./cmd/sandboxctl tunnel-revoke <tunnel-id>
```

## Why this matters

These features show that a sandbox is more than a container or VM that just runs one command.

You can:

- keep files in `/workspace`
- treat `/workspace` as the only durable, user-managed storage class
- use `/cache` and `/scratch` only for disposable build output, downloads, and temp data
- keep operator-provided material under `/secrets`, not inside `/workspace`
- run background tasks
- publish a chosen port in a controlled way through the tunnel control plane

## Storage classes and durability

- `/workspace` is the durable user workspace and is the only class included in portable Docker snapshot archives
- `/cache` is service-owned writable cache space and is excluded from portable snapshot/export flows
- `/scratch` is disposable writable space for temporary work and is excluded from snapshot/export flows
- `/secrets` is reserved for operator-managed material and should be treated as read-only guest input

For QEMU-backed sandboxes, the requested disk limit is split between the writable system layer and the workspace disk. That split is the hard VM storage boundary; host-side files such as cache, scratch, snapshots, and logs still need separate operator monitoring.

## Tunnel exposure rules

- sandbox services stay loopback-only by default
- egress behavior still follows the requested network mode
- public reachability only happens when the control plane publishes a tunnel endpoint
- revoking the tunnel removes the published entry point without changing the sandbox's internal bind address

## Good safety habits

- only create tunnels you actually need
- revoke tunnels when finished
- keep important files in `/workspace`
- do not store secrets in `/workspace` if they should stay out of snapshots or exports
- prefer small test commands first
