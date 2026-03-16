package main

import (
	"fmt"
	"path"
	"strings"
)

func normalizeWorkspaceAPIPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("workspace path is required")
	}
	if strings.HasPrefix(trimmed, "/") {
		cleaned := path.Clean(trimmed)
		switch {
		case cleaned == "/" || cleaned == "/workspace":
			return "", nil
		case strings.HasPrefix(cleaned, "/workspace/"):
			return strings.TrimPrefix(cleaned, "/workspace/"), nil
		default:
			return "", fmt.Errorf("absolute workspace paths must stay under /workspace")
		}
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path escapes workspace")
	}
	return strings.TrimPrefix(cleaned, "./"), nil
}
