package presets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifestNormalizesDefaults(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "preset.yaml")
	content := `
name: sample
sandbox:
  image: alpine:3.20
bootstrap:
  - command: ["sh", "-lc", "echo hi"]
`
	if err := os.WriteFile(manifestPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if manifest.Sandbox.CPULimit != "1" || manifest.Sandbox.MemoryMB != 1024 || manifest.Cleanup != CleanupOnSuccess {
		t.Fatalf("unexpected defaults: %+v", manifest)
	}
	if manifest.Bootstrap[0].Timeout <= 0 || manifest.Bootstrap[0].Cwd != "/workspace" {
		t.Fatalf("expected bootstrap defaults, got %+v", manifest.Bootstrap[0])
	}
}

func TestLoadManifestRejectsDuplicateInputs(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "preset.yaml")
	content := `
name: sample
sandbox:
  image: alpine:3.20
inputs:
  - name: TOKEN
  - name: TOKEN
`
	if err := os.WriteFile(manifestPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadManifest(manifestPath)
	if err == nil || !strings.Contains(err.Error(), "duplicate input name") {
		t.Fatalf("expected duplicate input error, got %v", err)
	}
}

func TestLoadManifestRejectsInvalidReadiness(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "preset.yaml")
	content := `
name: sample
sandbox:
  image: alpine:3.20
readiness:
  type: http
`
	if err := os.WriteFile(manifestPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadManifest(manifestPath)
	if err == nil || !strings.Contains(err.Error(), "requires tunnel configuration") {
		t.Fatalf("expected readiness validation error, got %v", err)
	}
}

func TestListDiscoversPresetDirectories(t *testing.T) {
	root := t.TempDir()
	examplesDir := filepath.Join(root, "examples")
	if err := os.MkdirAll(filepath.Join(examplesDir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(examplesDir, "beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name := range map[string]string{"alpha": "Alpha", "beta": "Beta"} {
		content := "name: " + name + "\nsandbox:\n  image: alpine:3.20\n"
		if err := os.WriteFile(filepath.Join(examplesDir, name, ManifestFileName), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	summaries, err := List(examplesDir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(summaries) != 2 || summaries[0].Name != "alpha" || summaries[1].Name != "beta" {
		t.Fatalf("unexpected summaries: %+v", summaries)
	}
}

func TestLoadManifestNormalizesRuntimeHints(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "preset.yaml")
	content := `
name: sample
runtime:
  allowed: [QEMU, docker]
  profile: browser-guest
sandbox:
  image: ${QEMU_GUEST_IMAGE}
`
	if err := os.WriteFile(manifestPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if manifest.Runtime.Profile != "browser-guest" {
		t.Fatalf("unexpected runtime profile %q", manifest.Runtime.Profile)
	}
	if len(manifest.Runtime.Allowed) != 2 || manifest.Runtime.Allowed[0] != "docker" || manifest.Runtime.Allowed[1] != "qemu" {
		t.Fatalf("unexpected runtime.allowed normalization: %+v", manifest.Runtime.Allowed)
	}
}
