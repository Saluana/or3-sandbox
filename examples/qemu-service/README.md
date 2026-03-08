# qemu-service

This preset demonstrates that QEMU uses the same user-facing service flow as Docker:

- boot the guest
- wait for guest-ready
- start a detached service inside the guest
- use the shared tunnel API to publish it
- check HTTP readiness through the tunneled endpoint

Guest profile expectations:

- `runtime.profile: python-service-guest`
- the guest image supplied as `QEMU_GUEST_IMAGE` must include `python3`
- the guest must allow outbound loopback access to the local HTTP service on port `8080`

How to run it:

```bash
go run ./cmd/sandboxctl preset run qemu-service \
  --env QEMU_GUEST_IMAGE="$SANDBOX_QEMU_BASE_IMAGE_PATH" \
  --keep
```

Host requirements:

- same QEMU host variables as `qemu-bootstrap`
- a guest profile that is prepared for HTTP-service examples