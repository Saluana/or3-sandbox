package dockerimage

import (
	"context"
	"strings"
	"testing"

	"or3-sandbox/internal/model"
)

func TestResolveUsesCuratedMapping(t *testing.T) {
	metadata, err := Resolve("mcr.microsoft.com/playwright:v1.51.1-noble")
	if err != nil {
		t.Fatalf("resolve playwright metadata: %v", err)
	}
	if metadata.Profile != model.GuestProfileBrowser {
		t.Fatalf("expected browser profile, got %+v", metadata)
	}
	if len(metadata.Capabilities) != 1 || metadata.Capabilities[0] != "browser" {
		t.Fatalf("expected browser capability, got %+v", metadata)
	}

	metadata, err = Resolve("or3-sandbox/base-container@sha256:deadbeef")
	if err != nil {
		t.Fatalf("resolve container metadata: %v", err)
	}
	if metadata.Profile != model.GuestProfileContainer || !metadata.Dangerous {
		t.Fatalf("expected dangerous container metadata, got %+v", metadata)
	}

	metadata, err = Resolve("docker.io/library/alpine:3.20")
	if err != nil {
		t.Fatalf("resolve canonical alpine metadata: %v", err)
	}
	if metadata.Profile != model.GuestProfileCore {
		t.Fatalf("expected core profile for canonical alpine ref, got %+v", metadata)
	}
}

func TestResolveRejectsUnknownImages(t *testing.T) {
	_, err := Resolve("registry.example.com/custom/app:1")
	if err == nil || !strings.Contains(err.Error(), "missing curated profile metadata") {
		t.Fatalf("expected missing metadata error, got %v", err)
	}
}

func TestParseLabels(t *testing.T) {
	metadata, err := ParseLabels("or3-sandbox/base:runtime", map[string]string{
		LabelProfile:      "runtime",
		LabelCapabilities: "pty,exec,files,pty",
		LabelDangerous:    "false",
	})
	if err != nil {
		t.Fatalf("parse labels: %v", err)
	}
	if metadata.Profile != model.GuestProfileRuntime {
		t.Fatalf("unexpected profile %+v", metadata)
	}
	if len(metadata.Capabilities) != 3 || metadata.Capabilities[0] != "exec" {
		t.Fatalf("unexpected capabilities %+v", metadata.Capabilities)
	}
	if metadata.Dangerous {
		t.Fatalf("expected non-dangerous metadata %+v", metadata)
	}
}

func TestResolveWithLabelProviderUsesImageLabels(t *testing.T) {
	metadata, err := ResolveWithLabelProvider(context.Background(), "registry.example.com/custom/app:1", func(context.Context, string) (map[string]string, error) {
		return map[string]string{
			LabelProfile:      "runtime",
			LabelCapabilities: "exec,files",
			LabelDangerous:    "false",
		}, nil
	})
	if err != nil {
		t.Fatalf("resolve via labels: %v", err)
	}
	if metadata.Profile != model.GuestProfileRuntime {
		t.Fatalf("expected runtime profile, got %+v", metadata)
	}
	if got := strings.Join(metadata.Capabilities, ","); got != "exec,files" {
		t.Fatalf("unexpected metadata capabilities %q", got)
	}
}
