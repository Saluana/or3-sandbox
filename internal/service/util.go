package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"or3-sandbox/internal/model"
)

func newID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("rand: %v", err))
	}
	return prefix + hex.EncodeToString(buf)
}

func resolveWorkspacePath(root, requested string) (string, error) {
	relativePath, err := cleanWorkspaceRelativePath(requested)
	if err != nil {
		return "", err
	}
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	if cleanRoot == "." || cleanRoot == "" {
		return "", fmt.Errorf("workspace root is empty")
	}
	resolvedRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return "", err
	}
	rootInfo, err := os.Stat(resolvedRoot)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() {
		return "", fmt.Errorf("workspace root is not a directory")
	}
	if relativePath == "" {
		return resolvedRoot, nil
	}
	current := resolvedRoot
	parts := strings.Split(relativePath, string(filepath.Separator))
	for i, part := range parts {
		next := filepath.Join(current, part)
		info, err := os.Lstat(next)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				resolvedNext, err := filepath.EvalSymlinks(next)
				if err != nil {
					return "", err
				}
				if !pathWithinRoot(resolvedRoot, resolvedNext) {
					return "", fmt.Errorf("path escapes workspace through symlink")
				}
				current = resolvedNext
				continue
			}
			current = next
		case errors.Is(err, os.ErrNotExist):
			return filepath.Join(current, filepath.Join(parts[i:]...)), nil
		default:
			return "", err
		}
	}
	return current, nil
}

func pathWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func cleanWorkspaceRelativePath(requested string) (string, error) {
	trimmed := strings.TrimLeft(requested, string(filepath.Separator))
	if trimmed == "" {
		return "", nil
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return cleaned, nil
}

type boundedBuffer struct {
	limit     int
	buf       []byte
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = true
		return len(p), nil
	}
	remaining := b.limit - len(b.buf)
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf = append(b.buf, p[:remaining]...)
		b.truncated = true
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	return string(b.buf)
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func readWorkspaceFileBytes(path string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = model.DefaultWorkspaceFileTransferMaxBytes
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory")
	}
	if info.Size() > maxBytes {
		return nil, model.FileTransferTooLargeError(maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, model.FileTransferTooLargeError(maxBytes)
	}
	return data, nil
}

func ensureWorkspaceTransferSize(sizeBytes, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = model.DefaultWorkspaceFileTransferMaxBytes
	}
	if sizeBytes > maxBytes {
		return model.FileTransferTooLargeError(maxBytes)
	}
	return nil
}

func isReadableFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return true
}

func looksLikeFilesystemPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	return filepath.IsAbs(trimmed) || strings.Contains(trimmed, string(os.PathSeparator)) || strings.HasPrefix(trimmed, ".")
}

func sandboxBaseDir(storageRoot string) string {
	if strings.TrimSpace(storageRoot) == "" {
		return ""
	}
	return filepath.Clean(filepath.Dir(storageRoot))
}

func storageClassRoot(baseDir string, class model.StorageClass) string {
	if strings.TrimSpace(baseDir) == "" {
		return ""
	}
	return filepath.Join(baseDir, string(class))
}

func scratchRootFromStorageRoot(storageRoot string) string {
	return storageClassRoot(sandboxBaseDir(storageRoot), model.StorageClassScratch)
}

func secretsRootFromStorageRoot(storageRoot string) string {
	return storageClassRoot(sandboxBaseDir(storageRoot), model.StorageClassSecrets)
}

func buildNetworkPolicy(mode model.NetworkMode, allowTunnels bool) model.NetworkPolicy {
	return model.ResolveNetworkPolicy(mode, allowTunnels)
}

func dirUsage(root string) (int64, int64, error) {
	if strings.TrimSpace(root) == "" {
		return 0, 0, nil
	}
	var bytes int64
	var entries int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries++
		bytes += info.Size()
		return nil
	})
	return bytes, entries, err
}
