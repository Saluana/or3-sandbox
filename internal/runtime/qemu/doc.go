// Package qemu implements the VM-backed sandbox runtime using QEMU.
//
// Agent control with protocol version 3 is the normal production path. The
// SSH-compatible path remains available only for explicit debug and rescue
// workflows and should not be treated as a standard production fallback.
package qemu
