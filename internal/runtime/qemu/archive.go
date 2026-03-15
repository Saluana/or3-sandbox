package qemu

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"or3-sandbox/internal/model"
)

const workspaceArchiveCommandTimeout = 2 * time.Minute

var _ model.WorkspaceArchiveExporter = (*Runtime)(nil)

// ExportWorkspaceArchive exports selected workspace paths into a host-side
// tar.gz archive.
func (r *Runtime) ExportWorkspaceArchive(ctx context.Context, sandbox model.Sandbox, paths []string, maxBytes int64) (string, error) {
	localArchive, err := os.CreateTemp(filepath.Dir(sandbox.WorkspaceRoot), "workspace-export-*.tar.gz")
	if err != nil {
		return "", err
	}
	localArchivePath := localArchive.Name()
	if err := localArchive.Close(); err != nil {
		_ = os.Remove(localArchivePath)
		return "", err
	}
	guestArchive := fmt.Sprintf(".or3-workspace-export-%d.tar.gz", time.Now().UTC().UnixNano())
	defer r.cleanupGuestWorkspaceArchive(sandbox, guestArchive)
	if err := r.runWorkspaceArchiveCommand(ctx, sandbox, buildWorkspaceArchiveExportCommand(guestArchive, paths)); err != nil {
		_ = os.Remove(localArchivePath)
		return "", err
	}
	archiveBytes, err := r.readWorkspaceFileBytesWithLimit(ctx, sandbox, guestArchive, -1)
	if err != nil {
		_ = os.Remove(localArchivePath)
		return "", err
	}
	if err := rewriteWorkspaceArchive(localArchivePath, archiveBytes, maxBytes); err != nil {
		_ = os.Remove(localArchivePath)
		return "", err
	}
	return localArchivePath, nil
}

func (r *Runtime) cleanupGuestWorkspaceArchive(sandbox model.Sandbox, relativePath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.DeleteWorkspacePath(ctx, sandbox, relativePath)
}

func (r *Runtime) runWorkspaceArchiveCommand(ctx context.Context, sandbox model.Sandbox, script string) error {
	handle, err := r.Exec(ctx, sandbox, model.ExecRequest{
		Command: []string{"sh", "-lc", script},
		Cwd:     "/workspace",
		Timeout: workspaceArchiveCommandTimeout,
	}, model.ExecStreams{})
	if err != nil {
		return err
	}
	result := handle.Wait()
	if result.Status == model.ExecutionStatusSucceeded {
		return nil
	}
	message := strings.TrimSpace(result.StderrPreview)
	if message == "" {
		message = strings.TrimSpace(result.StdoutPreview)
	}
	if message != "" {
		return fmt.Errorf("workspace archive command failed: %s", message)
	}
	return fmt.Errorf("workspace archive command failed with status %s", result.Status)
}

func buildWorkspaceArchiveExportCommand(archivePath string, paths []string) string {
	args := make([]string, 0, len(paths))
	for _, requested := range paths {
		if strings.TrimSpace(requested) == "" {
			args = append(args, ".")
			continue
		}
		args = append(args, filepath.ToSlash(requested))
	}
	if len(args) == 0 {
		args = append(args, ".")
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join([]string{
		"set -eu",
		"if ! command -v tar >/dev/null 2>&1; then echo 'workspace export requires tar in guest' >&2; exit 127; fi",
		"rm -f " + shellQuote(archivePath),
		"tar -czf " + shellQuote(archivePath) + " --exclude=" + shellQuote("./"+archivePath) + " -- " + strings.Join(quoted, " "),
	}, "\n")
}

func rewriteWorkspaceArchive(destination string, archive []byte, maxBytes int64) error {
	sourceGzip, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer sourceGzip.Close()
	sourceTar := tar.NewReader(sourceGzip)

	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	success := false
	defer func() {
		if !success {
			_ = file.Close()
			_ = os.Remove(destination)
		}
	}()

	seen := map[string]struct{}{}
	var totalBytes int64
	for {
		header, err := sourceTar.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeXHeader, tar.TypeXGlobalHeader:
			continue
		case tar.TypeDir, tar.TypeReg, tar.TypeRegA:
		case tar.TypeSymlink, tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			return fmt.Errorf("workspace path contains unsupported symlink: %s", header.Name)
		default:
			return fmt.Errorf("unsupported tar entry type for %s", header.Name)
		}
		name, err := normalizeWorkspaceArchiveEntryName(header.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		headerCopy := *header
		headerCopy.Name = name
		if headerCopy.Typeflag == tar.TypeDir && !strings.HasSuffix(headerCopy.Name, "/") {
			headerCopy.Name += "/"
		}
		if headerCopy.Typeflag != tar.TypeDir {
			totalBytes += headerCopy.Size
			if maxBytes > 0 && totalBytes > maxBytes {
				return model.FileTransferTooLargeError(maxBytes)
			}
		}
		if err := tarWriter.WriteHeader(&headerCopy); err != nil {
			return err
		}
		if headerCopy.Typeflag != tar.TypeDir {
			if _, err := io.Copy(tarWriter, sourceTar); err != nil {
				return err
			}
		}
		seen[name] = struct{}{}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	success = true
	return nil
}

func normalizeWorkspaceArchiveEntryName(name string) (string, error) {
	cleaned := strings.TrimLeft(filepath.ToSlash(name), "/")
	for strings.HasPrefix(cleaned, "./") {
		cleaned = strings.TrimPrefix(cleaned, "./")
	}
	if cleaned == "" {
		return "", nil
	}
	cleaned = path.Clean(cleaned)
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("workspace archive contains invalid path: %s", name)
	}
	return cleaned, nil
}
