package qemu

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteWorkspaceArchiveNormalizesNames(t *testing.T) {
	archive := mustArchiveBytes(t, []*tar.Header{
		{Name: "./src/", Mode: 0o755, Typeflag: tar.TypeDir},
		{Name: "./src/main.ts", Mode: 0o644, Size: int64(len("ok\n")), Typeflag: tar.TypeReg},
		{Name: "./README.md", Mode: 0o644, Size: int64(len("hello\n")), Typeflag: tar.TypeReg},
	}, [][]byte{
		nil,
		[]byte("ok\n"),
		[]byte("hello\n"),
	})

	output := filepath.Join(t.TempDir(), "workspace.tar.gz")
	if err := rewriteWorkspaceArchive(output, archive, 64*1024); err != nil {
		t.Fatalf("rewrite workspace archive: %v", err)
	}
	entries := readArchiveEntries(t, output)
	if entries["README.md"] != "hello\n" {
		t.Fatalf("unexpected README.md content %q", entries["README.md"])
	}
	if entries["src/main.ts"] != "ok\n" {
		t.Fatalf("unexpected src/main.ts content %q", entries["src/main.ts"])
	}
}

func TestRewriteWorkspaceArchiveRejectsSymlinkEntries(t *testing.T) {
	archive := mustArchiveBytes(t, []*tar.Header{
		{Name: "./escape", Typeflag: tar.TypeSymlink, Linkname: "/tmp/host"},
	}, [][]byte{nil})
	output := filepath.Join(t.TempDir(), "workspace.tar.gz")
	err := rewriteWorkspaceArchive(output, archive, 64*1024)
	if err == nil || !strings.Contains(err.Error(), "unsupported symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func mustArchiveBytes(t *testing.T, headers []*tar.Header, payloads [][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for index, header := range headers {
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write header %d: %v", index, err)
		}
		if len(payloads[index]) == 0 {
			continue
		}
		if _, err := tarWriter.Write(payloads[index]); err != nil {
			t.Fatalf("write payload %d: %v", index, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buffer.Bytes()
}

func readArchiveEntries(t *testing.T, archivePath string) map[string]string {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("open gzip reader: %v", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	entries := make(map[string]string)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatalf("read tar entry: %v", err)
		}
		if header.FileInfo().IsDir() {
			continue
		}
		data, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatalf("read tar payload %s: %v", header.Name, err)
		}
		entries[header.Name] = string(data)
	}
}
