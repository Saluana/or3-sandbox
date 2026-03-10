package service

import (
	"crypto/rand"
	"encoding/hex"
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
	if relativePath == "" {
		return root, nil
	}
	return filepath.Join(root, relativePath), nil
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
