package model

import "strings"

// RuntimeSelection names a user-visible runtime option.
type RuntimeSelection string

const (
	// RuntimeSelectionDockerDev selects the trusted Docker development backend.
	RuntimeSelectionDockerDev RuntimeSelection = "docker-dev"
	// RuntimeSelectionContainerdKataProfessional selects the Kata-backed VM runtime.
	RuntimeSelectionContainerdKataProfessional RuntimeSelection = "containerd-kata-professional"
	// RuntimeSelectionQEMUProfessional selects the QEMU-backed VM runtime.
	RuntimeSelectionQEMUProfessional RuntimeSelection = "qemu-professional"
)

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
	case "kata":
		return RuntimeClassVM
	case "qemu":
		return RuntimeClassVM
	default:
		return ""
	}
}

// RuntimeSelectionFromBackend returns the canonical runtime selection for a
// legacy backend name.
func RuntimeSelectionFromBackend(backend string) RuntimeSelection {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "docker":
		return RuntimeSelectionDockerDev
	case "kata":
		return RuntimeSelectionContainerdKataProfessional
	case "qemu":
		return RuntimeSelectionQEMUProfessional
	default:
		return ""
	}
}

// ParseRuntimeSelection normalizes value and returns an empty selection for
// unknown names.
func ParseRuntimeSelection(value string) RuntimeSelection {
	selection := RuntimeSelection(strings.ToLower(strings.TrimSpace(value)))
	if !selection.IsValid() {
		return ""
	}
	return selection
}

// ResolveRuntimeSelection prefers an explicit valid selection and otherwise
// falls back to the legacy backend name.
func ResolveRuntimeSelection(selection RuntimeSelection, backend string) RuntimeSelection {
	if selection.IsValid() {
		return selection
	}
	return RuntimeSelectionFromBackend(backend)
}

// IsValid reports whether s is a supported runtime selection.
func (s RuntimeSelection) IsValid() bool {
	switch s {
	case RuntimeSelectionDockerDev, RuntimeSelectionContainerdKataProfessional, RuntimeSelectionQEMUProfessional:
		return true
	default:
		return false
	}
}

// Backend returns the runtime backend name associated with s.
func (s RuntimeSelection) Backend() string {
	switch s {
	case RuntimeSelectionDockerDev:
		return "docker"
	case RuntimeSelectionContainerdKataProfessional:
		return "kata"
	case RuntimeSelectionQEMUProfessional:
		return "qemu"
	default:
		return ""
	}
}

// RuntimeClass returns the isolation class associated with s.
func (s RuntimeSelection) RuntimeClass() RuntimeClass {
	return BackendToRuntimeClass(s.Backend())
}

// IsVMBacked reports whether s resolves to a VM-backed runtime class.
func (s RuntimeSelection) IsVMBacked() bool {
	return s.RuntimeClass().IsVMBacked()
}

// IsVMBacked returns true when the class provides VM-level isolation and is
// therefore eligible for production use with untrusted workloads.
func (c RuntimeClass) IsVMBacked() bool {
	return c == RuntimeClassVM
}
