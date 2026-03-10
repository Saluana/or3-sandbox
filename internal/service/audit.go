package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"or3-sandbox/internal/model"
)

func (s *Service) RecordAuditEvent(ctx context.Context, tenantID, sandboxID, action, resourceID, outcome, detail string, attrs ...any) {
	s.recordAudit(ctx, tenantID, sandboxID, action, resourceID, outcome, detail, attrs...)
}

func (s *Service) recordAudit(ctx context.Context, tenantID, sandboxID, action, resourceID, outcome, detail string, attrs ...any) {
	_ = s.store.AddAuditEvent(ctx, model.AuditEvent{
		ID:         newID("audit-"),
		TenantID:   tenantID,
		SandboxID:  sandboxID,
		Action:     action,
		ResourceID: resourceID,
		Outcome:    outcome,
		Message:    detail,
		CreatedAt:  time.Now().UTC(),
	})
	logAttrs := []any{
		"event", action,
		"tenant_id", tenantID,
		"sandbox_id", sandboxID,
		"resource_id", resourceID,
		"outcome", outcome,
	}
	if detail != "" {
		logAttrs = append(logAttrs, "detail", detail)
	}
	logAttrs = append(logAttrs, attrs...)
	s.log.Log(ctx, auditLevel(outcome), "service event", logAttrs...)
}

func auditLevel(outcome string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case "ok", "succeeded", "success":
		return slog.LevelInfo
	case "denied", "canceled":
		return slog.LevelWarn
	default:
		return slog.LevelError
	}
}

func auditKV(key string, value any) string {
	return key + "=" + auditValue(value)
}

func auditDetail(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		filtered = append(filtered, part)
	}
	return strings.Join(filtered, " ")
}

func auditValue(value any) string {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		trimmed = strings.ReplaceAll(trimmed, "\n", " ")
		trimmed = strings.ReplaceAll(trimmed, "\r", " ")
		if trimmed == "" {
			return `""`
		}
		if strings.ContainsAny(trimmed, " \t") {
			return strconv.Quote(trimmed)
		}
		return trimmed
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case time.Time:
		return typed.UTC().Format(time.RFC3339)
	case fmt.Stringer:
		return auditValue(typed.String())
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func execAuditDetail(req model.ExecRequest) string {
	entrypoint := ""
	if len(req.Command) > 0 {
		entrypoint = req.Command[0]
	}
	return auditDetail(
		auditKV("entrypoint", entrypoint),
		auditKV("argc", len(req.Command)),
		auditKV("cwd", req.Cwd),
		auditKV("detached", req.Detached),
		auditKV("timeout_seconds", int(req.Timeout.Seconds())),
	)
}

func executionAuditDetail(execution model.Execution) string {
	entrypoint := ""
	if fields := strings.Fields(execution.Command); len(fields) > 0 {
		entrypoint = fields[0]
	}
	return auditDetail(
		auditKV("entrypoint", entrypoint),
		auditKV("cwd", execution.Cwd),
		auditKV("timeout_seconds", execution.TimeoutSeconds),
	)
}

func tunnelAuditDetail(tunnel model.Tunnel) string {
	return auditDetail(
		auditKV("target_port", tunnel.TargetPort),
		auditKV("protocol", tunnel.Protocol),
		auditKV("auth_mode", tunnel.AuthMode),
		auditKV("visibility", tunnel.Visibility),
	)
}

func snapshotAuditDetail(snapshot model.Snapshot) string {
	return auditDetail(
		auditKV("name", snapshot.Name),
		auditKV("status", snapshot.Status),
		auditKV("runtime", snapshot.RuntimeBackend),
		auditKV("profile", snapshot.Profile),
		auditKV("exported", snapshot.ExportLocation != ""),
	)
}

func networkPolicyAuditDetail(policy model.NetworkPolicy) string {
	return auditDetail(
		auditKV("internet", policy.Internet),
		auditKV("loopback_only", policy.LoopbackOnly),
		auditKV("allow_tunnels", policy.AllowTunnels),
	)
}

func dockerOverrideAuditDetail(features, capabilities []string) string {
	return auditDetail(
		auditKV("docker_features", strings.Join(features, ",")),
		auditKV("docker_capabilities", strings.Join(capabilities, ",")),
	)
}
