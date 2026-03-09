package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"or3-sandbox/internal/auth"
	"or3-sandbox/internal/model"
)

func (s *Service) enforceCreatePolicy(ctx context.Context, tenantID string, req model.CreateSandboxRequest) error {
	if !s.runtimeImageAllowed(s.cfg.RuntimeBackend, req.BaseImageRef) {
		message := fmt.Sprintf("image %q is not allowed by policy", req.BaseImageRef)
		s.recordAudit(ctx, tenantID, "", "policy.create", req.BaseImageRef, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	if s.cfg.RuntimeBackend == "docker" {
		if err := s.enforceDockerCreatePolicy(ctx, tenantID, "", req.Features, req.Capabilities, "policy.create"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) enforceLifecyclePolicy(ctx context.Context, sandbox model.Sandbox, action string) error {
	now := time.Now().UTC()
	if s.cfg.PolicyMaxSandboxLifetime > 0 {
		age := now.Sub(sandbox.CreatedAt)
		if age > s.cfg.PolicyMaxSandboxLifetime {
			message := fmt.Sprintf("sandbox lifetime %s exceeds policy limit %s", age.Round(time.Second), s.cfg.PolicyMaxSandboxLifetime)
			s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.ID, "denied", message)
			return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
		}
	}
	if s.cfg.PolicyMaxIdleTimeout > 0 && !sandbox.LastActiveAt.IsZero() {
		idle := now.Sub(sandbox.LastActiveAt)
		if idle > s.cfg.PolicyMaxIdleTimeout {
			message := fmt.Sprintf("sandbox idle time %s exceeds policy limit %s", idle.Round(time.Second), s.cfg.PolicyMaxIdleTimeout)
			s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.ID, "denied", message)
			return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
		}
	}
	if !s.runtimeImageAllowed(sandbox.RuntimeBackend, sandbox.BaseImageRef) {
		message := fmt.Sprintf("sandbox image %q is no longer allowed by policy", sandbox.BaseImageRef)
		s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.BaseImageRef, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	if sandbox.RuntimeBackend == "docker" {
		if err := s.enforceDockerCreatePolicy(ctx, sandbox.TenantID, sandbox.ID, sandbox.Features, sandbox.Capabilities, "policy."+action); err != nil {
			return err
		}
	}
	if sandbox.RuntimeBackend == "qemu" {
		if sandbox.Profile != "" && !s.cfg.IsAllowedQEMUProfile(sandbox.Profile) {
			message := fmt.Sprintf("sandbox profile %q is no longer allowed by policy", sandbox.Profile)
			s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.ID, "denied", message)
			return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
		}
		if sandbox.Profile != "" && s.cfg.IsDangerousQEMUProfile(sandbox.Profile) && !s.cfg.QEMUAllowDangerousProfiles {
			message := fmt.Sprintf("sandbox profile %q is blocked by dangerous-profile policy", sandbox.Profile)
			s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.ID, "denied", message)
			return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
		}
	}
	return nil
}

func (s *Service) enforceTunnelPolicy(ctx context.Context, sandbox model.Sandbox, req model.CreateTunnelRequest) error {
	if strings.EqualFold(req.Visibility, "public") && !s.cfg.PolicyAllowPublicTunnels {
		message := "public tunnels are disabled by policy"
		s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy.tunnel", sandbox.ID, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	return nil
}

func (s *Service) enforceAdminInspectionPolicy(ctx context.Context, tenantID, action string) error {
	if s.cfg.DeploymentMode == "production" && !model.BackendToRuntimeClass(s.cfg.RuntimeBackend).IsVMBacked() {
		message := "admin inspection requires a VM-backed runtime class in production mode"
		s.recordAudit(ctx, tenantID, "", "policy."+action, action, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	return nil
}

func (s *Service) imageAllowed(imageRef string) bool {
	if len(s.cfg.PolicyAllowedImages) == 0 {
		return true
	}
	for _, allowed := range s.cfg.PolicyAllowedImages {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if strings.HasSuffix(allowed, "*") {
			if strings.HasPrefix(imageRef, strings.TrimSuffix(allowed, "*")) {
				return true
			}
			continue
		}
		if imageRef == allowed {
			return true
		}
	}
	return false
}

func (s *Service) runtimeImageAllowed(runtimeBackend, imageRef string) bool {
	if runtimeBackend == "qemu" {
		normalized := s.normalizeQEMUBaseImageRef(imageRef)
		for _, allowed := range s.cfg.EffectiveQEMUAllowedBaseImagePaths() {
			if normalized == allowed {
				return true
			}
		}
		return false
	}
	return s.imageAllowed(imageRef)
}

var deniedDockerFeatures = map[string]string{
	"docker.host-ipc":            "host IPC sharing is blocked by policy",
	"docker.host-network":        "host network sharing is blocked by policy",
	"docker.host-pid":            "host PID namespace sharing is blocked by policy",
	"docker.mount-docker-socket": "mounting the Docker socket is blocked by policy",
	"docker.privileged":          "privileged Docker mode is blocked by policy",
}

func (s *Service) enforceDockerCreatePolicy(ctx context.Context, tenantID, sandboxID string, features, capabilities []string, action string) error {
	features = model.NormalizeFeatures(features)
	capabilities = model.NormalizeCapabilities(capabilities)
	for _, feature := range features {
		if message, ok := deniedDockerFeatures[feature]; ok {
			s.recordAudit(ctx, tenantID, sandboxID, action, sandboxID, "denied", auditDetail(message, dockerOverrideAuditDetail(features, capabilities)))
			return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
		}
	}
	dangerous := false
	for _, capability := range capabilities {
		switch {
		case capability == "docker.elevated-user":
			dangerous = true
		case strings.HasPrefix(capability, "docker.extra-cap:"):
			dangerous = true
		default:
			message := fmt.Sprintf("docker capability %q is not supported", capability)
			s.recordAudit(ctx, tenantID, sandboxID, action, sandboxID, "denied", auditDetail(message, dockerOverrideAuditDetail(features, capabilities)))
			return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
		}
	}
	if dangerous && !s.cfg.DockerAllowDangerousOverrides {
		message := "dangerous Docker capability overrides are blocked until SANDBOX_DOCKER_ALLOW_DANGEROUS_OVERRIDES=true"
		s.recordAudit(ctx, tenantID, sandboxID, action, sandboxID, "denied", auditDetail(message, dockerOverrideAuditDetail(features, capabilities)))
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	if dangerous && action == "policy.create" {
		s.recordAudit(ctx, tenantID, sandboxID, "policy.create.override", sandboxID, "ok", auditDetail(
			"dangerous Docker override explicitly allowed",
			dockerOverrideAuditDetail(features, capabilities),
		))
	}
	return nil
}
