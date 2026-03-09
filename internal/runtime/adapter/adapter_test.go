package adapter_test

import (
	"testing"

	"or3-sandbox/internal/model"
	"or3-sandbox/internal/runtime/adapter"
)

func TestAdapterCreateRequestCarriesRuntimeClass(t *testing.T) {
	req := adapter.AdapterCreateRequest{
		Spec: model.SandboxSpec{
			SandboxID: "sbx-test",
			TenantID:  "tenant-a",
		},
		Class: model.RuntimeClassVM,
		Storage: adapter.SandboxAttachment{
			WorkspaceRoot: "/data/workspace",
			CacheRoot:     "/data/cache",
			StorageRoot:   "/data/rootfs",
		},
		Network: adapter.NetworkAttachment{
			Mode: model.NetworkModeInternetDisabled,
		},
	}
	if req.Class != model.RuntimeClassVM {
		t.Errorf("expected class %q, got %q", model.RuntimeClassVM, req.Class)
	}
	if !req.Class.IsVMBacked() {
		t.Error("expected RuntimeClassVM to report IsVMBacked() = true")
	}
}

func TestDockerBackendMapsToTrustedDockerClass(t *testing.T) {
	class := model.BackendToRuntimeClass("docker")
	if class != model.RuntimeClassTrustedDocker {
		t.Errorf("docker backend: expected class %q, got %q", model.RuntimeClassTrustedDocker, class)
	}
	if class.IsVMBacked() {
		t.Error("trusted-docker class should not be VM-backed")
	}
}

func TestQEMUBackendMapsToVMClass(t *testing.T) {
	class := model.BackendToRuntimeClass("qemu")
	if class != model.RuntimeClassVM {
		t.Errorf("qemu backend: expected class %q, got %q", model.RuntimeClassVM, class)
	}
	if !class.IsVMBacked() {
		t.Error("vm class must be VM-backed")
	}
}
