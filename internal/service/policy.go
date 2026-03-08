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
	if !s.imageAllowed(req.BaseImageRef) {
		message := fmt.Sprintf("image %q is not allowed by policy", req.BaseImageRef)
		s.recordAudit(ctx, tenantID, "", "policy.create", req.BaseImageRef, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
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
	if !s.imageAllowed(sandbox.BaseImageRef) {
		message := fmt.Sprintf("sandbox image %q is no longer allowed by policy", sandbox.BaseImageRef)
		s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.BaseImageRef, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
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
	if s.cfg.DeploymentMode == "production" && s.cfg.RuntimeBackend != "qemu" {
		message := "admin inspection requires the qemu production boundary in production mode"
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
