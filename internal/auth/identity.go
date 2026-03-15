package auth

import (
	"context"
	"errors"
	"strings"

	"or3-sandbox/internal/model"
)

// ErrForbidden reports that the authenticated caller lacks the requested
// permission.
var ErrForbidden = errors.New("forbidden")

type tenantContextKey struct{}

// Identity describes the authenticated caller.
type Identity struct {
	Subject          string
	TenantID         string
	Roles            []string
	Scopes           []string
	ServiceAccountID string
	IsService        bool
	AuthMethod       string
}

// TenantContext is the request-scoped authentication payload stored in a
// [context.Context].
type TenantContext struct {
	Tenant   model.Tenant
	Quota    model.TenantQuota
	Identity Identity
}

const (
	// PermissionSandboxRead allows reading sandbox metadata.
	PermissionSandboxRead = "sandbox.read"
	// PermissionSandboxLifecycle allows lifecycle mutations such as start and stop.
	PermissionSandboxLifecycle = "sandbox.lifecycle"
	// PermissionExecRun allows running commands in sandboxes.
	PermissionExecRun = "exec.run"
	// PermissionTTYAttach allows opening interactive terminal sessions.
	PermissionTTYAttach = "tty.attach"
	// PermissionFilesRead allows reading sandbox workspace files.
	PermissionFilesRead = "files.read"
	// PermissionFilesWrite allows writing sandbox workspace files.
	PermissionFilesWrite = "files.write"
	// PermissionSnapshotsRead allows listing and reading snapshots.
	PermissionSnapshotsRead = "snapshots.read"
	// PermissionSnapshotsWrite allows creating and restoring snapshots.
	PermissionSnapshotsWrite = "snapshots.write"
	// PermissionTunnelsRead allows reading tunnel metadata.
	PermissionTunnelsRead = "tunnels.read"
	// PermissionTunnelsWrite allows creating and revoking tunnels.
	PermissionTunnelsWrite = "tunnels.write"
	// PermissionAdminInspect allows administrative inspection endpoints.
	PermissionAdminInspect = "admin.inspect"
)

// FromContext extracts the current [TenantContext] from ctx.
func FromContext(ctx context.Context) (TenantContext, bool) {
	value, ok := ctx.Value(tenantContextKey{}).(TenantContext)
	return value, ok
}

// Require reports nil when the current caller has at least one of the supplied
// permissions.
func Require(ctx context.Context, permissions ...string) error {
	tenantCtx, ok := FromContext(ctx)
	if !ok {
		return errors.New("unauthorized")
	}
	for _, permission := range permissions {
		if tenantCtx.HasPermission(permission) {
			return nil
		}
	}
	return ErrForbidden
}

// HasPermission reports whether t grants permission.
func (t TenantContext) HasPermission(permission string) bool {
	if t.Identity.IsService && len(t.Identity.Scopes) > 0 && !containsPermission(t.Identity.Scopes, permission) {
		return false
	}
	for _, role := range t.Identity.Roles {
		for _, granted := range rolePermissions(strings.ToLower(strings.TrimSpace(role))) {
			if granted == permission {
				return true
			}
		}
	}
	return false
}

// AllPermissions returns the complete set of permission names understood by the
// control plane.
func AllPermissions() []string {
	return []string{
		PermissionSandboxRead,
		PermissionSandboxLifecycle,
		PermissionExecRun,
		PermissionTTYAttach,
		PermissionFilesRead,
		PermissionFilesWrite,
		PermissionSnapshotsRead,
		PermissionSnapshotsWrite,
		PermissionTunnelsRead,
		PermissionTunnelsWrite,
		PermissionAdminInspect,
	}
}

func rolePermissions(role string) []string {
	switch role {
	case "operator":
		return AllPermissions()
	case "tenant-admin", "admin":
		return []string{
			PermissionSandboxRead,
			PermissionSandboxLifecycle,
			PermissionExecRun,
			PermissionTTYAttach,
			PermissionFilesRead,
			PermissionFilesWrite,
			PermissionSnapshotsRead,
			PermissionSnapshotsWrite,
			PermissionTunnelsRead,
			PermissionTunnelsWrite,
		}
	case "tenant-developer", "developer":
		return []string{
			PermissionSandboxRead,
			PermissionSandboxLifecycle,
			PermissionExecRun,
			PermissionTTYAttach,
			PermissionFilesRead,
			PermissionFilesWrite,
			PermissionSnapshotsRead,
			PermissionSnapshotsWrite,
			PermissionTunnelsRead,
			PermissionTunnelsWrite,
		}
	case "tenant-viewer", "viewer":
		return []string{
			PermissionSandboxRead,
			PermissionFilesRead,
			PermissionSnapshotsRead,
			PermissionTunnelsRead,
		}
	case "service-account", "service":
		return []string{
			PermissionSandboxRead,
			PermissionSandboxLifecycle,
			PermissionExecRun,
			PermissionTTYAttach,
			PermissionFilesRead,
			PermissionFilesWrite,
			PermissionSnapshotsRead,
			PermissionSnapshotsWrite,
			PermissionTunnelsRead,
			PermissionTunnelsWrite,
			PermissionAdminInspect,
		}
	default:
		return nil
	}
}

func containsPermission(granted []string, permission string) bool {
	for _, value := range granted {
		if strings.EqualFold(strings.TrimSpace(value), permission) {
			return true
		}
	}
	return false
}
