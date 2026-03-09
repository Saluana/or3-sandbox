# Playwright Preset

This preset uses the Playwright Docker image to capture a screenshot and download it locally.

It explicitly selects the `browser` profile so browser tooling stays opt-in instead of riding along with the default lightweight image.

## Run

```bash
go run ./cmd/sandboxctl preset run playwright
```

Override the target URL:

```bash
go run ./cmd/sandboxctl preset run playwright --env TARGET_URL=https://playwright.dev
```

Downloaded artifact:

- [examples/playwright/outputs/screenshot.png](examples/playwright/outputs/screenshot.png)

## Notes

- The screenshot download relies on the new binary-safe file transfer mode.
- Docker is the initial path for this preset because the Playwright image is already packaged for container execution.
- This preset should stay on an explicit browser image; it should never fall back to the default lightweight `core` image.