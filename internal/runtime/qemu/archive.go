package qemu

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"or3-sandbox/internal/model"
	"or3-sandbox/internal/runtime/qemu/agentproto"
)

var _ model.WorkspaceArchiveExporter = (*Runtime)(nil)

// ExportWorkspaceArchive exports selected workspace paths into a host-side
// tar.gz archive.
func (r *Runtime) ExportWorkspaceArchive(ctx context.Context, sandbox model.Sandbox, paths []string, maxBytes int64) (string, error) {
	localArchive, err := os.CreateTemp(filepath.Dir(sandbox.WorkspaceRoot), "workspace-export-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = localArchive.Close()
			_ = os.Remove(localArchive.Name())
		}
	}()
	gzipWriter := gzip.NewWriter(localArchive)
	tarWriter := tar.NewWriter(gzipWriter)
	stream, sessionID, err := r.streamWorkspaceArchive(ctx, sandbox, paths)
	if err != nil {
		return "", err
	}
	defer func() {
		if r.sessionManager != nil {
			if session, sessionErr := r.ensureAgentSession(context.Background(), sandbox, layoutForSandbox(sandbox)); sessionErr == nil {
				session.unregisterArchive(sessionID)
			}
		}
	}()
	var currentFile string
	var totalBytes int64
	seen := map[string]struct{}{}
	for chunk := range stream {
		if chunk.Error != "" {
			return "", errors.New(chunk.Error)
		}
		if chunk.End {
			break
		}
		name, err := normalizeWorkspaceArchiveEntryName(chunk.Path)
		if err != nil {
			return "", err
		}
		switch chunk.Type {
		case "dir":
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			header := &tar.Header{Name: name + "/", Mode: chunk.Mode, ModTime: chunk.ModTime, Typeflag: tar.TypeDir}
			if err := tarWriter.WriteHeader(header); err != nil {
				return "", err
			}
			seen[name] = struct{}{}
		case "file":
			if _, ok := seen[name]; !ok {
				header := &tar.Header{Name: name, Mode: chunk.Mode, ModTime: chunk.ModTime, Size: chunk.Size, Typeflag: tar.TypeReg}
				if err := tarWriter.WriteHeader(header); err != nil {
					return "", err
				}
				seen[name] = struct{}{}
				currentFile = name
			}
			if chunk.Data != "" {
				data, err := agentproto.DecodeBytes(chunk.Data)
				if err != nil {
					return "", err
				}
				totalBytes += int64(len(data))
				if maxBytes > 0 && totalBytes > maxBytes {
					return "", model.FileTransferTooLargeError(maxBytes)
				}
				if _, err := tarWriter.Write(data); err != nil {
					return "", err
				}
			}
			if chunk.EOF && currentFile == name {
				currentFile = ""
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		return "", err
	}
	if err := gzipWriter.Close(); err != nil {
		return "", err
	}
	if err := localArchive.Close(); err != nil {
		return "", err
	}
	return localArchive.Name(), nil
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
