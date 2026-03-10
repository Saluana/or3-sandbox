package archiveutil

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractTarGzRejectsTraversalAndLinks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		header   *tar.Header
		wantText string
	}{
		{
			name:     "path traversal",
			header:   &tar.Header{Name: "../escape.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len("bad"))},
			wantText: "escapes destination",
		},
		{
			name:     "symlink",
			header:   &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
			wantText: "unsupported tar entry type",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "bad.tar.gz")
			if err := writeTarGz(archive, []tarEntry{{header: tc.header, body: "bad"}}); err != nil {
				t.Fatalf("write archive: %v", err)
			}
			_, err := ExtractTarGz(archive, t.TempDir(), Limits{MaxBytes: 1024, MaxFiles: 10, MaxExpansionRatio: 32})
			if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("expected %q error, got %v", tc.wantText, err)
			}
		})
	}
}

func TestExtractTarGzAppliesLimitsAndNormalizesModes(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "files.tar.gz")
	body := strings.Repeat("A", 4096)
	entries := []tarEntry{{header: &tar.Header{Name: "nested/run.sh", Typeflag: tar.TypeReg, Mode: 0o777, Size: int64(len(body))}, body: body}}
	if err := writeTarGz(archive, entries); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	dest := t.TempDir()
	stats, err := ExtractTarGz(archive, dest, Limits{MaxBytes: 8192, MaxFiles: 4, MaxExpansionRatio: 128})
	if err != nil {
		t.Fatalf("extract archive: %v", err)
	}
	if stats.Files != 1 {
		t.Fatalf("unexpected file count %d", stats.Files)
	}
	info, err := os.Stat(filepath.Join(dest, "nested", "run.sh"))
	if err != nil {
		t.Fatalf("stat extracted file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("expected normalized executable mode 0755, got %o", got)
	}
	_, err = ExtractTarGz(archive, t.TempDir(), Limits{MaxBytes: 1024, MaxFiles: 4, MaxExpansionRatio: 128})
	if err == nil || !strings.Contains(err.Error(), "maximum extracted bytes") {
		t.Fatalf("expected max-bytes error, got %v", err)
	}
	_, err = ExtractTarGz(archive, t.TempDir(), Limits{MaxBytes: 8192, MaxFiles: 0, MaxExpansionRatio: 1})
	if err == nil || !strings.Contains(err.Error(), "maximum expansion ratio") {
		t.Fatalf("expected expansion ratio error, got %v", err)
	}
}

type tarEntry struct {
	header *tar.Header
	body   string
}

func writeTarGz(path string, entries []tarEntry) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for _, entry := range entries {
		if err := tw.WriteHeader(entry.header); err != nil {
			return err
		}
		if entry.header.Typeflag == tar.TypeReg || entry.header.Typeflag == tar.TypeRegA {
			if _, err := tw.Write([]byte(entry.body)); err != nil {
				return err
			}
		}
	}
	return nil
}
