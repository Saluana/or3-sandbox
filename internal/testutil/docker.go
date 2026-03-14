package testutil

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// DockerAvailable verifies that the Docker CLI is installed and can talk to a
// live daemon. Tests can use this to skip Docker-backed suites on hosts where
// a socket path exists but the engine is not actually reachable.
func DockerAvailable(ctx context.Context) error {
	path, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker CLI not available: %w", err)
	}
	output, err := exec.CommandContext(ctx, path, "info", "--format", "{{.ServerVersion}}").CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return fmt.Errorf("docker daemon unavailable: %w", err)
		}
		return fmt.Errorf("docker daemon unavailable: %s", message)
	}
	return nil
}
