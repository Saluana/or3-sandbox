# qemu-browser-artifact

This preset demonstrates binary artifact retrieval from a browser-capable QEMU guest.

What it does:

- waits for the guest to become ready
- finds an installed Chromium or Chrome binary inside the guest
- captures a headless screenshot into `/workspace/screenshot.png`
- downloads the PNG artifact to the local example directory

Guest profile expectations:

- `runtime.profile: browser-guest`
- the guest image supplied as `QEMU_GUEST_IMAGE` must include one of `chromium`, `chromium-browser`, `google-chrome`, or `google-chrome-stable`
- the guest must have enough RAM and disk for browser startup

How to run it:

```bash
go run ./cmd/sandboxctl preset run qemu-browser-artifact \
  --env QEMU_GUEST_IMAGE="$SANDBOX_QEMU_BASE_IMAGE_PATH" \
  --env TARGET_URL=https://example.com
```

Host requirements:

- same QEMU host variables as `qemu-bootstrap`
- a browser-ready guest image or profile; this example is intentionally not honest for a minimal guest image