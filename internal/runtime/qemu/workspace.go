package qemu

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"or3-sandbox/internal/model"
)

// ReadWorkspaceFile reads a workspace file as UTF-8 text.
func (r *Runtime) ReadWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string) (model.FileReadResponse, error) {
	output, err := r.readWorkspaceFileBytesWithLimit(ctx, sandbox, relativePath, -1)
	if err != nil {
		return model.FileReadResponse{}, err
	}
	return model.FileReadResponse{
		Path:     relativePath,
		Content:  string(output),
		Size:     int64(len(output)),
		Encoding: "utf-8",
	}, nil
}

// ReadWorkspaceFileBytes reads a workspace file as raw bytes.
func (r *Runtime) ReadWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string) ([]byte, error) {
	limit := int64(-1)
	if r.controlModeForSandbox(sandbox) == model.GuestControlModeAgent {
		maxBytes, err := r.effectiveWorkspaceFileTransferMaxBytes(ctx, layoutForSandbox(sandbox))
		if err != nil {
			return nil, err
		}
		limit = maxBytes
	}
	return r.readWorkspaceFileBytesWithLimit(ctx, sandbox, relativePath, limit)
}

func (r *Runtime) readWorkspaceFileBytesWithLimit(ctx context.Context, sandbox model.Sandbox, relativePath string, maxBytes int64) ([]byte, error) {
	if r.controlModeForSandbox(sandbox) == model.GuestControlModeAgent {
		return r.agentReadWorkspaceFileBytesWithLimit(ctx, layoutForSandbox(sandbox), relativePath, maxBytes)
	}
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return nil, err
	}
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", "cat "+shellQuote(target))
	output, err := r.runCommand(ctx, r.sshBinary, args...)
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && int64(len(output)) > maxBytes {
		return nil, model.FileTransferTooLargeError(maxBytes)
	}
	return output, nil
}

// WriteWorkspaceFile writes UTF-8 content to a workspace path.
func (r *Runtime) WriteWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string, content string) error {
	return r.writeWorkspaceFileBytes(ctx, sandbox, relativePath, bytes.NewBufferString(content))
}

// WriteWorkspaceFileBytes writes raw bytes to a workspace path.
func (r *Runtime) WriteWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string, content []byte) error {
	if r.controlModeForSandbox(sandbox) == model.GuestControlModeAgent {
		return r.agentWriteWorkspaceFileBytes(ctx, layoutForSandbox(sandbox), relativePath, content)
	}
	return r.writeWorkspaceFileBytes(ctx, sandbox, relativePath, bytes.NewReader(content))
}

func (r *Runtime) writeWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string, content io.Reader) error {
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	command := fmt.Sprintf("mkdir -p %s && cat > %s", shellQuote(path.Dir(target)), shellQuote(target))
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", command)
	cmd := exec.CommandContext(ctx, r.sshBinary, args...)
	cmd.Stdin = content
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("write workspace file: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// DeleteWorkspacePath removes a workspace file or directory.
func (r *Runtime) DeleteWorkspacePath(ctx context.Context, sandbox model.Sandbox, relativePath string) error {
	if r.controlModeForSandbox(sandbox) == model.GuestControlModeAgent {
		return r.agentDeleteWorkspacePath(ctx, layoutForSandbox(sandbox), relativePath)
	}
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", "rm -rf "+shellQuote(target))
	_, err = r.runCommand(ctx, r.sshBinary, args...)
	return err
}

// MkdirWorkspace creates a workspace directory and any missing parents.
func (r *Runtime) MkdirWorkspace(ctx context.Context, sandbox model.Sandbox, relativePath string) error {
	if r.controlModeForSandbox(sandbox) == model.GuestControlModeAgent {
		return r.agentMkdirWorkspace(ctx, layoutForSandbox(sandbox), relativePath)
	}
	target, err := workspaceGuestPath(relativePath)
	if err != nil {
		return err
	}
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", "mkdir -p "+shellQuote(target))
	_, err = r.runCommand(ctx, r.sshBinary, args...)
	return err
}

// MeasureStorage reports host-side storage usage for sandbox.
func (r *Runtime) MeasureStorage(ctx context.Context, sandbox model.Sandbox) (model.StorageUsage, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	rootfsBytes, rootfsEntries, err := allocatedPathUsage(layout.rootDiskPath)
	if err != nil {
		return model.StorageUsage{}, err
	}
	workspaceBytes, workspaceEntries, err := allocatedPathUsage(layout.workspaceDiskPath)
	if err != nil {
		return model.StorageUsage{}, err
	}
	cacheBytes, cacheEntries, err := allocatedPathUsage(sandbox.CacheRoot)
	if err != nil {
		return model.StorageUsage{}, err
	}
	snapshotBytes, snapshotEntries, err := allocatedPathUsage(filepath.Join(sandbox.StorageRoot, ".snapshots"))
	if err != nil {
		return model.StorageUsage{}, err
	}
	return model.StorageUsage{
		RootfsBytes:      rootfsBytes,
		WorkspaceBytes:   workspaceBytes,
		CacheBytes:       cacheBytes,
		SnapshotBytes:    snapshotBytes,
		RootfsEntries:    rootfsEntries,
		WorkspaceEntries: workspaceEntries,
		CacheEntries:     cacheEntries,
		SnapshotEntries:  snapshotEntries,
	}, nil
}

func workspaceGuestPath(relativePath string) (string, error) {
	if strings.TrimSpace(relativePath) == "" {
		return "/workspace", nil
	}
	clean := path.Clean("/workspace/" + filepath.ToSlash(relativePath))
	if clean != "/workspace" && !strings.HasPrefix(clean, "/workspace/") {
		return "", fmt.Errorf("path escapes workspace")
	}
	return clean, nil
}
