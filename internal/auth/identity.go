package auth

import (
	"context"
	"errors"
	"strings"

	"or3-sandbox/internal/model"
)

var ErrForbidden = errors.New("forbidden")

type tenantContextKey struct{}

type Identity struct {
	Subject          string
	TenantID         string
	Roles            []string
	Scopes           []string
	ServiceAccountID string
	IsService        bool
	AuthMethod       string
}

type TenantContext struct {
	Tenant   model.Tenant
	Quota    model.TenantQuota
	Identity Identity
}

const (
	PermissionSandboxRead      = "sandbox.read"
	PermissionSandboxLifecycle = "sandbox.lifecycle"
	PermissionExecRun          = "exec.run"
	PermissionTTYAttach        = "tty.attach"
	PermissionFilesRead        = "files.read"
	PermissionFilesWrite       = "files.write"
	PermissionSnapshotsRead    = "snapshots.read"
	PermissionSnapshotsWrite   = "snapshots.write"
	PermissionTunnelsRead      = "tunnels.read"
	PermissionTunnelsWrite     = "tunnels.write"
	PermissionAdminInspect     = "admin.inspect"
)

func FromContext(ctx context.Context) (TenantContext, bool) {
	value, ok := ctx.Value(tenantContextKey{}).(TenantContext)
	return value, ok
}

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
