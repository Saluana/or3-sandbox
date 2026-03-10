package model

import "testing"

func TestBackendToRuntimeClass(t *testing.T) {
	tests := []struct {
		backend string
		want    RuntimeClass
	}{
		{"docker", RuntimeClassTrustedDocker},
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
