// Package docker implements the sandbox runtime on top of the Docker CLI.
//
// It provides a trusted-development backend and is not the untrusted isolation
// boundary used for hostile multi-tenant production workloads.
package docker