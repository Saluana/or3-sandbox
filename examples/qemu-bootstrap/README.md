# qemu-bootstrap

This preset shows the smallest honest QEMU flow:

- boot a guest image
- wait for guest-ready
- seed files into `/workspace`
- run bootstrap commands inside the guest
- download guest-produced artifacts back to the host

Guest profile expectations:

- `runtime.profile: base-guest`
- the guest must boot successfully and allow SSH with the host settings used by `sandboxd`
- the guest must provide `sh`, `cp`, `grep`, and `uname`

How to run it:

```bash
go run ./cmd/sandboxctl preset run qemu-bootstrap \
  --env QEMU_GUEST_IMAGE="$SANDBOX_QEMU_BASE_IMAGE_PATH"
```

Host requirements:

- run `sandboxd` with `--runtime qemu`
- export `SANDBOX_QEMU_BINARY`, `SANDBOX_QEMU_BASE_IMAGE_PATH`, `SANDBOX_QEMU_SSH_USER`, and `SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH`
- expect slower startup than Docker because the guest must finish booting before bootstrap begins