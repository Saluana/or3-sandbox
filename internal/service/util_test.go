package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspacePathAllowsSymlinkWithinWorkspace(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "scripts")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	targetFile := filepath.Join(targetDir, "tool.sh")
	if err := os.WriteFile(targetFile, []byte("echo ok\n"), 0o644); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	if err := os.Symlink("scripts", filepath.Join(root, "bin")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	resolved, err := resolveWorkspacePath(root, "bin/tool.sh")
	if err != nil {
		t.Fatalf("resolve symlinked path: %v", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(targetFile)
	if err != nil {
		t.Fatalf("eval target symlinks: %v", err)
	}
	if resolved != resolvedTarget {
		t.Fatalf("expected %q, got %q", resolvedTarget, resolved)
	}
}

func TestResolveWorkspacePathRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "leak")); err != nil {
		t.Fatalf("create escape symlink: %v", err)
	}

	if _, err := resolveWorkspacePath(root, "leak/secret.txt"); err == nil {
		t.Fatalf("expected symlink escape to be rejected")
	}
}

func TestCleanWorkspaceRelativePathAllowsNormalRelativePath(t *testing.T) {
	got, err := cleanWorkspaceRelativePath("src/project.txt")
	if err != nil {
		t.Fatalf("clean path: %v", err)
	}
	if got != filepath.Join("src", "project.txt") {
		t.Fatalf("unexpected cleaned path %q", got)
	}
}

func TestCleanWorkspaceRelativePathAllowsTopLevelWorkspaceDirectory(t *testing.T) {
	got, err := cleanWorkspaceRelativePath("workspace/project.txt")
	if err != nil {
		t.Fatalf("clean path: %v", err)
	}
	if got != filepath.Join("workspace", "project.txt") {
		t.Fatalf("unexpected cleaned path %q", got)
	}
}
