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
	selection := s.resolveRuntimeSelection(req)
	if !s.cfg.IsRuntimeSelectionEnabled(selection) {
		message := fmt.Sprintf("runtime selection %q is not enabled", selection)
		s.recordAudit(ctx, tenantID, "", "policy.create", string(selection), "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	if !s.runtimeImageAllowed(selection.Backend(), req.BaseImageRef) {
		message := fmt.Sprintf("image %q is not allowed by policy", req.BaseImageRef)
		s.recordAudit(ctx, tenantID, "", "policy.create", req.BaseImageRef, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	if err := s.enforceGuestProfilePolicy(ctx, tenantID, "", selection.Backend(), req.Profile, req.DangerousProfileReason, "policy.create"); err != nil {
		return err
	}
	if err := s.enforcePromotedImagePolicy(ctx, tenantID, "", selection, req.BaseImageRef, "policy.create"); err != nil {
		return err
	}
	if selection.Backend() == "docker" {
		if err := s.enforceDockerCreatePolicy(ctx, tenantID, "", req.Profile, req.Features, req.Capabilities, "policy.create"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) enforceLifecyclePolicy(ctx context.Context, sandbox model.Sandbox, action string) error {
	selection := resolvedSandboxRuntimeSelection(sandbox)
	if !s.cfg.IsRuntimeSelectionEnabled(selection) {
		message := fmt.Sprintf("runtime selection %q is not enabled", selection)
		s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.ID, "denied", auditDetail(message, auditKV("runtime_selection", selection)))
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
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
	if !s.runtimeImageAllowed(selection.Backend(), sandbox.BaseImageRef) {
		message := fmt.Sprintf("sandbox image %q is no longer allowed by policy", sandbox.BaseImageRef)
		s.recordAudit(ctx, sandbox.TenantID, sandbox.ID, "policy."+action, sandbox.BaseImageRef, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	if selection.Backend() == "docker" {
		if err := s.enforceDockerCreatePolicy(ctx, sandbox.TenantID, sandbox.ID, sandbox.Profile, sandbox.Features, sandbox.Capabilities, "policy."+action); err != nil {
			return err
		}
	}
	if err := s.enforceGuestProfilePolicy(ctx, sandbox.TenantID, sandbox.ID, selection.Backend(), sandbox.Profile, "", "policy."+action); err != nil {
		return err
	}
	if err := s.enforcePromotedImagePolicy(ctx, sandbox.TenantID, sandbox.ID, selection, sandbox.BaseImageRef, "policy."+action); err != nil {
		return err
	}
	return nil
}

func (s *Service) enforceGuestProfilePolicy(ctx context.Context, tenantID, sandboxID, runtimeBackend string, profile model.GuestProfile, reason string, action string) error {
	if profile == "" {
		return nil
	}
	if !s.cfg.IsAllowedGuestProfile(runtimeBackend, profile) {
		message := fmt.Sprintf("sandbox profile %q is not allowed by policy", profile)
		s.recordAudit(ctx, tenantID, sandboxID, action, sandboxID, "denied", auditDetail(message, auditKV("runtime", runtimeBackend)))
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	if s.cfg.IsDangerousGuestProfile(runtimeBackend, profile) && !s.cfg.AllowsDangerousGuestProfiles(runtimeBackend) {
		message := fmt.Sprintf("sandbox profile %q is blocked by dangerous-profile policy until SANDBOX_ALLOW_DANGEROUS_PROFILES=true", profile)
		s.recordAudit(ctx, tenantID, sandboxID, action, sandboxID, "denied", auditDetail(message, auditKV("runtime", runtimeBackend)))
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	if s.cfg.DeploymentMode == "production" && s.cfg.IsDangerousGuestProfile(runtimeBackend, profile) && strings.TrimSpace(reason) == "" && action == "policy.create" {
		message := fmt.Sprintf("sandbox profile %q requires dangerous_profile_reason for audited exception use", profile)
		s.recordAudit(ctx, tenantID, sandboxID, action, sandboxID, "denied", auditDetail(message, auditKV("runtime", runtimeBackend)))
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	if s.cfg.IsDangerousGuestProfile(runtimeBackend, profile) && s.cfg.AllowsDangerousGuestProfiles(runtimeBackend) && action == "policy.create" {
		auditReason := strings.TrimSpace(reason)
		if auditReason == "" {
			auditReason = "dangerous_profile_explicitly_allowed"
		}
		s.recordAudit(ctx, tenantID, sandboxID, "policy.profile.override", sandboxID, "ok", auditDetail(
			auditKV("runtime", runtimeBackend),
			auditKV("profile", profile),
			auditKV("reason", auditReason),
		))
	}
	return nil
}

func (s *Service) enforcePromotedImagePolicy(ctx context.Context, tenantID, sandboxID string, selection model.RuntimeSelection, imageRef, action string) error {
	if s.cfg.DeploymentMode != "production" || selection.Backend() != "qemu" {
		return nil
	}
	record, err := s.store.GetPromotedGuestImage(ctx, imageRef)
	if err != nil {
		message := fmt.Sprintf("production qemu workloads require a promoted guest image; %q is not promoted", imageRef)
		s.recordAudit(ctx, tenantID, sandboxID, action, imageRef, "denied", message)
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
	if !strings.EqualFold(record.VerificationStatus, "verified") || !strings.EqualFold(record.PromotionStatus, "promoted") {
		message := fmt.Sprintf("guest image %q is not production-ready (verification=%s promotion=%s)", imageRef, record.VerificationStatus, record.PromotionStatus)
		s.recordAudit(ctx, tenantID, sandboxID, action, imageRef, "denied", message)
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
	if s.cfg.DeploymentMode == "production" && !s.cfg.DefaultRuntimeSelection.IsVMBacked() {
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

func (s *Service) enforceDockerCreatePolicy(ctx context.Context, tenantID, sandboxID string, profile model.GuestProfile, features, capabilities []string, action string) error {
	features = model.NormalizeFeatures(features)
	capabilities = model.NormalizeCapabilities(capabilities)
	if profile != "" && s.cfg.IsDangerousGuestProfile("docker", profile) && !s.cfg.AllowsDangerousGuestProfiles("docker") {
		message := fmt.Sprintf("docker profile %q is blocked by dangerous-profile policy", profile)
		s.recordAudit(ctx, tenantID, sandboxID, action, sandboxID, "denied", auditDetail(message, dockerOverrideAuditDetail(features, capabilities)))
		return fmt.Errorf("%w: %s", auth.ErrForbidden, message)
	}
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
