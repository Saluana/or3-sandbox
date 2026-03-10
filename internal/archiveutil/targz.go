package archiveutil

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Limits struct {
	MaxBytes          int64
	MaxFiles          int
	MaxExpansionRatio int
}

type Stats struct {
	Files int
	Bytes int64
}

func ExtractTarGz(source, destination string, limits Limits) (Stats, error) {
	file, err := os.Open(source)
	if err != nil {
		return Stats{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Stats{}, err
	}
	compressedSize := info.Size()
	gr, err := gzip.NewReader(file)
	if err != nil {
		return Stats{}, err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	cleanDestination := filepath.Clean(destination)
	stats := Stats{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return stats, nil
		}
		if err != nil {
			return Stats{}, err
		}
		if header == nil {
			continue
		}
		cleanName := filepath.Clean(strings.TrimPrefix(header.Name, string(filepath.Separator)))
		if cleanName == "." || cleanName == "" {
			continue
		}
		if cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
			return Stats{}, fmt.Errorf("tar entry escapes destination: %s", header.Name)
		}
		target := filepath.Join(cleanDestination, cleanName)
		if target != cleanDestination && !strings.HasPrefix(target, cleanDestination+string(os.PathSeparator)) {
			return Stats{}, fmt.Errorf("tar entry escapes destination: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return Stats{}, err
			}
			continue
		case tar.TypeReg, tar.TypeRegA:
		case tar.TypeSymlink, tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo, tar.TypeXGlobalHeader, tar.TypeXHeader:
			return Stats{}, fmt.Errorf("unsupported tar entry type for %s", header.Name)
		default:
			return Stats{}, fmt.Errorf("unsupported tar entry type for %s", header.Name)
		}
		if header.Size < 0 {
			return Stats{}, fmt.Errorf("invalid tar entry size for %s", header.Name)
		}
		stats.Files++
		stats.Bytes += header.Size
		if limits.MaxFiles > 0 && stats.Files > limits.MaxFiles {
			return Stats{}, fmt.Errorf("snapshot archive exceeds maximum file count %d", limits.MaxFiles)
		}
		if limits.MaxBytes > 0 && stats.Bytes > limits.MaxBytes {
			return Stats{}, fmt.Errorf("snapshot archive exceeds maximum extracted bytes %d", limits.MaxBytes)
		}
		if limits.MaxExpansionRatio > 0 && compressedSize > 0 && stats.Bytes > compressedSize*int64(limits.MaxExpansionRatio) {
			return Stats{}, fmt.Errorf("snapshot archive exceeds maximum expansion ratio %d", limits.MaxExpansionRatio)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return Stats{}, err
		}
		mode := os.FileMode(0o644)
		if header.FileInfo().Mode()&0o111 != 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			return Stats{}, err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return Stats{}, err
		}
		if err := out.Close(); err != nil {
			return Stats{}, err
		}
		if err := os.Chmod(target, mode); err != nil {
			return Stats{}, err
		}
	}
}
