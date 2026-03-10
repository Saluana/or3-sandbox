package model

import "testing"

func TestBackendToRuntimeClass(t *testing.T) {
	tests := []struct {
		backend string
		want    RuntimeClass
	}{
		{"docker", RuntimeClassTrustedDocker},
		{"kata", RuntimeClassVM},
		{"qemu", RuntimeClassVM},
		{"", ""},
		{"unknown", ""},
	}
	for _, tt := range tests {
		got := BackendToRuntimeClass(tt.backend)
		if got != tt.want {
			t.Errorf("BackendToRuntimeClass(%q) = %q, want %q", tt.backend, got, tt.want)
		}
	}
}

func TestRuntimeClassIsVMBacked(t *testing.T) {
	if !RuntimeClassVM.IsVMBacked() {
		t.Error("expected RuntimeClassVM to be VM-backed")
	}
	if RuntimeClassTrustedDocker.IsVMBacked() {
		t.Error("expected RuntimeClassTrustedDocker to not be VM-backed")
	}
	if RuntimeClass("").IsVMBacked() {
		t.Error("expected empty RuntimeClass to not be VM-backed")
	}
}

func TestRuntimeSelectionHelpers(t *testing.T) {
	tests := []struct {
		selection RuntimeSelection
		backend   string
		class     RuntimeClass
		vmBacked  bool
	}{
		{RuntimeSelectionDockerDev, "docker", RuntimeClassTrustedDocker, false},
		{RuntimeSelectionContainerdKataProfessional, "kata", RuntimeClassVM, true},
		{RuntimeSelectionQEMUProfessional, "qemu", RuntimeClassVM, true},
	}
	for _, tt := range tests {
		if got := tt.selection.Backend(); got != tt.backend {
			t.Fatalf("Backend() = %q, want %q", got, tt.backend)
		}
		if got := tt.selection.RuntimeClass(); got != tt.class {
			t.Fatalf("RuntimeClass() = %q, want %q", got, tt.class)
		}
		if got := tt.selection.IsVMBacked(); got != tt.vmBacked {
			t.Fatalf("IsVMBacked() = %v, want %v", got, tt.vmBacked)
		}
	}
	if got := ParseRuntimeSelection(" docker-dev "); got != RuntimeSelectionDockerDev {
		t.Fatalf("ParseRuntimeSelection docker-dev = %q", got)
	}
	if got := ResolveRuntimeSelection("", "docker"); got != RuntimeSelectionDockerDev {
		t.Fatalf("ResolveRuntimeSelection docker = %q", got)
	}
	if got := ResolveRuntimeSelection("", "qemu"); got != RuntimeSelectionQEMUProfessional {
		t.Fatalf("ResolveRuntimeSelection qemu = %q", got)
	}
}
