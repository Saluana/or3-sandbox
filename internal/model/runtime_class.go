package model

// RuntimeClass describes the isolation posture of a runtime backend.
//
// Policy decisions about production eligibility are based on RuntimeClass rather
// than on ad-hoc backend name checks spread across unrelated packages.
type RuntimeClass string

const (
	// RuntimeClassTrustedDocker is the class for Docker-backed sandboxes.
	// Docker uses the host kernel and is therefore suitable only for trusted
	// or local-development environments; it is not the hostile multi-tenant
	// production boundary.
	RuntimeClassTrustedDocker RuntimeClass = "trusted-docker"

	// RuntimeClassVM is the class for VM-backed sandboxes (e.g. QEMU with KVM).
	// VM-backed runtimes provide the strongest isolation boundary and are the
	// only class eligible for production use with untrusted workloads.
	RuntimeClassVM RuntimeClass = "vm"
)

// BackendToRuntimeClass derives the RuntimeClass from a backend name.
//
// Mapping for the first implementation wave:
//   - "docker" → RuntimeClassTrustedDocker
//   - "qemu"   → RuntimeClassVM
//
// Future VM-compatible backends (e.g. "kata") should also map to RuntimeClassVM.
// An empty RuntimeClass is returned for unknown backends.
func BackendToRuntimeClass(backend string) RuntimeClass {
	switch backend {
	case "docker":
		return RuntimeClassTrustedDocker
	case "qemu":
		return RuntimeClassVM
	default:
		return ""
	}
}

// IsVMBacked returns true when the class provides VM-level isolation and is
// therefore eligible for production use with untrusted workloads.
func (c RuntimeClass) IsVMBacked() bool {
	return c == RuntimeClassVM
}
