package qemu

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"or3-sandbox/internal/model"
)

func (r *Runtime) ReadWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string) (model.FileReadResponse, error) {
	target := workspaceGuestPath(relativePath)
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", "cat "+shellQuote(target))
	output, err := r.runCommand(ctx, r.sshBinary, args...)
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

func (r *Runtime) WriteWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string, content string) error {
	target := workspaceGuestPath(relativePath)
	command := fmt.Sprintf("mkdir -p %s && cat > %s", shellQuote(path.Dir(target)), shellQuote(target))
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", command)
	cmd := exec.CommandContext(ctx, r.sshBinary, args...)
	cmd.Stdin = bytes.NewBufferString(content)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("write workspace file: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (r *Runtime) DeleteWorkspacePath(ctx context.Context, sandbox model.Sandbox, relativePath string) error {
	target := workspaceGuestPath(relativePath)
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", "rm -rf "+shellQuote(target))
	_, err := r.runCommand(ctx, r.sshBinary, args...)
	return err
}

func (r *Runtime) MkdirWorkspace(ctx context.Context, sandbox model.Sandbox, relativePath string) error {
	target := workspaceGuestPath(relativePath)
	args := append(r.baseSSHArgs(r.sshTarget(sandbox, layoutForSandbox(sandbox)), false), "sh", "-lc", "mkdir -p "+shellQuote(target))
	_, err := r.runCommand(ctx, r.sshBinary, args...)
	return err
}

func (r *Runtime) MeasureStorage(ctx context.Context, sandbox model.Sandbox) (model.StorageUsage, error) {
	_ = ctx
	layout := layoutForSandbox(sandbox)
	rootfsBytes, err := allocatedPathSize(layout.rootDiskPath)
	if err != nil {
		return model.StorageUsage{}, err
	}
	workspaceBytes, err := allocatedPathSize(layout.workspaceDiskPath)
	if err != nil {
		return model.StorageUsage{}, err
	}
	cacheBytes, err := allocatedPathSize(sandbox.CacheRoot)
	if err != nil {
		return model.StorageUsage{}, err
	}
	snapshotBytes, err := allocatedPathSize(filepath.Join(sandbox.StorageRoot, ".snapshots"))
	if err != nil {
		return model.StorageUsage{}, err
	}
	return model.StorageUsage{
		RootfsBytes:    rootfsBytes,
		WorkspaceBytes: workspaceBytes,
		CacheBytes:     cacheBytes,
		SnapshotBytes:  snapshotBytes,
	}, nil
}

func workspaceGuestPath(relativePath string) string {
	if strings.TrimSpace(relativePath) == "" {
		return "/workspace"
	}
	return path.Join("/workspace", filepath.ToSlash(relativePath))
}
