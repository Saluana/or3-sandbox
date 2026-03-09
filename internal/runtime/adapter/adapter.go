// Package adapter defines the internal request types that sit between the
// service layer and backend-specific runtime implementations.
//
// The adapter model lets service code describe sandbox intent in terms of
// sandbox lifecycle, storage attachments, and network attachments without
// encoding Docker-CLI semantics into the centre of the design.
//
// Adding a new VM-backed adapter (e.g. kata/containerd) does not require
// threading Docker-specific assumptions through service or API code; a new
// adapter implementation simply consumes these shared request types.
package adapter

import "or3-sandbox/internal/model"

// SandboxAttachment describes the storage volumes attached to a sandbox.
type SandboxAttachment struct {
	// WorkspaceRoot is the host path for the sandbox workspace volume.
	WorkspaceRoot string
	// CacheRoot is the host path for the sandbox cache volume.
	CacheRoot string
	// StorageRoot is the host path for the sandbox root filesystem storage.
	StorageRoot string
	// ReadOnlyRoot indicates that the root filesystem should be mounted read-only.
	ReadOnlyRoot bool
}

// NetworkAttachment describes the network posture of a sandbox.
type NetworkAttachment struct {
	// Mode is the requested network isolation mode.
	Mode model.NetworkMode
}

// AdapterCreateRequest is the internal request type passed to a runtime adapter
// when creating a new sandbox.  It carries the full sandbox specification, the
// resolved runtime class, and typed attachments for storage and network so that
// adapters do not need to interpret raw Docker/QEMU CLI semantics.
type AdapterCreateRequest struct {
	// Spec is the full sandbox specification as resolved by the service layer.
	Spec model.SandboxSpec
	// Class is the resolved runtime class for this request.
	Class model.RuntimeClass
	// Storage describes the storage volumes to attach.
	Storage SandboxAttachment
	// Network describes the network posture to apply.
	Network NetworkAttachment
}
