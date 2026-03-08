package presets

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Summary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
}

func DiscoverExamplesDir(startDir string) (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("SANDBOX_EXAMPLES_DIR")); explicit != "" {
		info, err := os.Stat(explicit)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("SANDBOX_EXAMPLES_DIR %q is not a directory", explicit)
		}
		return explicit, nil
	}
	if strings.TrimSpace(startDir) == "" {
		var err error
		startDir, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	dir := startDir
	for {
		candidate := filepath.Join(dir, "examples")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not find examples directory from %s", startDir)
}

func List(exampleDir string) ([]Summary, error) {
	entries, err := os.ReadDir(exampleDir)
	if err != nil {
		return nil, err
	}
	var summaries []Summary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(exampleDir, entry.Name(), ManifestFileName)
		if _, err := os.Stat(manifestPath); err != nil {
			if errorsIs(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		manifest, err := LoadManifest(manifestPath)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, Summary{Name: manifest.Name, Description: manifest.Description, Path: manifestPath})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })
	return summaries, nil
}

func Load(exampleDir, name string) (Manifest, error) {
	manifestPath := filepath.Join(exampleDir, name, ManifestFileName)
	return LoadManifest(manifestPath)
}

func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	manifest.BaseDir = filepath.Dir(path)
	manifest.Normalize()
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate %s: %w", path, err)
	}
	return manifest, nil
}

func errorsIs(err, target error) bool {
	return err != nil && target != nil && os.IsNotExist(err)
}