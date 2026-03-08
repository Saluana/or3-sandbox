package api

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"or3-sandbox/internal/auth"
	"or3-sandbox/internal/config"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
	"or3-sandbox/internal/service"
)

type Router struct {
	log              *slog.Logger
	service          *service.Service
	operatorHost     string
	tunnelSigningKey []byte
	upgrader         websocket.Upgrader
}

const (
	tunnelSignedURLDefaultTTL = 5 * time.Minute
	tunnelSignedURLMaxTTL     = 15 * time.Minute
	tunnelSignedURLExpiryKey  = "or3_exp"
	tunnelSignedURLSigKey     = "or3_sig"
	tunnelAuthCookieName      = "or3_tunnel_auth"
)

func New(log *slog.Logger, svc *service.Service, cfg config.Config) http.Handler {
	router := &Router{
		log:              log,
		service:          svc,
		operatorHost:     strings.TrimRight(cfg.OperatorHost, "/"),
		tunnelSigningKey: newTunnelSigningKey(cfg),
		upgrader:         websocket.Upgrader{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", router.health)
	mux.HandleFunc("/metrics", router.handleMetrics)
	mux.HandleFunc("/v1/runtime/info", router.handleRuntimeInfo)
	mux.HandleFunc("/v1/runtime/health", router.handleRuntimeHealth)
	mux.HandleFunc("/v1/runtime/capacity", router.handleRuntimeCapacity)
	mux.HandleFunc("/v1/quotas/me", router.handleQuota)
	mux.HandleFunc("/v1/sandboxes", router.handleSandboxes)
	mux.HandleFunc("/v1/sandboxes/", router.handleSandboxRoutes)
	mux.HandleFunc("/v1/snapshots/", router.handleSnapshotRoutes)
	mux.HandleFunc("/v1/tunnels/", router.handleTunnelRoutes)
	return loggingMiddleware(log, mux)
}

func loggingMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		attrs := []any{
			"event", "http.request",
			"method", r.Method,
			"path", r.URL.Path,
			"status_code", recorder.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"response_bytes", recorder.bytes,
			"remote_addr", r.RemoteAddr,
			"outcome", httpOutcome(recorder.status),
		}
		if tenantCtx, ok := auth.FromContext(r.Context()); ok {
			attrs = append(attrs,
				"tenant_id", tenantCtx.Tenant.ID,
				"subject", tenantCtx.Identity.Subject,
				"auth_method", tenantCtx.Identity.AuthMethod,
			)
		}
		attrs = append(attrs, requestResourceAttrs(r.URL.Path)...)
		log.Log(r.Context(), httpLogLevel(recorder.status), "http request completed", attrs...)
	})
}

func (rt *Router) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (rt *Router) handleRuntimeHealth(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requirePermission(w, r, auth.PermissionAdminInspect) {
		return
	}
	health, err := rt.service.RuntimeHealth(r.Context(), tenantCtx.Tenant.ID)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, health)
}

func (rt *Router) handleRuntimeInfo(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = tenantCtx
	writeJSON(w, http.StatusOK, model.RuntimeInfo{Backend: rt.service.RuntimeBackend()})
}

func (rt *Router) handleRuntimeCapacity(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requirePermission(w, r, auth.PermissionAdminInspect) {
		return
	}
	report, err := rt.service.CapacityReport(r.Context(), tenantCtx.Tenant.ID)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (rt *Router) handleMetrics(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requirePermission(w, r, auth.PermissionAdminInspect) {
		return
	}
	metrics, err := rt.service.MetricsReport(r.Context(), tenantCtx.Tenant.ID)
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = io.WriteString(w, metrics)
}

func (rt *Router) handleQuota(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	view, err := rt.service.GetTenantQuotaView(r.Context(), tenantCtx.Tenant.ID)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (rt *Router) handleSandboxes(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodPost:
		if !requirePermission(w, r, auth.PermissionSandboxLifecycle) {
			return
		}
		var req model.CreateSandboxRequest
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sandbox, err := rt.service.CreateSandbox(r.Context(), tenantCtx.Tenant, tenantCtx.Quota, req)
		if err != nil {
			handleError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, sandbox)
	case http.MethodGet:
		if !requirePermission(w, r, auth.PermissionSandboxRead) {
			return
		}
		sandboxes, err := rt.service.ListSandboxes(r.Context(), tenantCtx.Tenant.ID)
		if err != nil {
			handleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sandboxes)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (rt *Router) handleSandboxRoutes(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/sandboxes/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	sandboxID := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			if !requirePermission(w, r, auth.PermissionSandboxRead) {
				return
			}
			sandbox, err := rt.service.GetSandbox(r.Context(), tenantCtx.Tenant.ID, sandboxID)
			if err != nil {
				handleError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, sandbox)
		case http.MethodDelete:
			if !requirePermission(w, r, auth.PermissionSandboxLifecycle) {
				return
			}
			if err := rt.service.DeleteSandbox(r.Context(), tenantCtx.Tenant.ID, sandboxID, false); err != nil {
				handleError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	switch parts[1] {
	case "start":
		if !requirePermission(w, r, auth.PermissionSandboxLifecycle) {
			return
		}
		rt.handleLifecycle(w, r, func(ctx context.Context) (model.Sandbox, error) {
			return rt.service.StartSandbox(ctx, tenantCtx.Tenant.ID, sandboxID, tenantCtx.Quota)
		})
	case "stop":
		if !requirePermission(w, r, auth.PermissionSandboxLifecycle) {
			return
		}
		var req model.LifecycleRequest
		_ = decodeJSON(r, &req)
		rt.handleLifecycle(w, r, func(ctx context.Context) (model.Sandbox, error) {
			return rt.service.StopSandbox(ctx, tenantCtx.Tenant.ID, sandboxID, req.Force)
		})
	case "suspend":
		if !requirePermission(w, r, auth.PermissionSandboxLifecycle) {
			return
		}
		rt.handleLifecycle(w, r, func(ctx context.Context) (model.Sandbox, error) {
			return rt.service.SuspendSandbox(ctx, tenantCtx.Tenant.ID, sandboxID)
		})
	case "resume":
		if !requirePermission(w, r, auth.PermissionSandboxLifecycle) {
			return
		}
		rt.handleLifecycle(w, r, func(ctx context.Context) (model.Sandbox, error) {
			return rt.service.ResumeSandbox(ctx, tenantCtx.Tenant.ID, sandboxID, tenantCtx.Quota)
		})
	case "exec":
		if !requirePermission(w, r, auth.PermissionExecRun) {
			return
		}
		rt.handleExec(w, r, tenantCtx, sandboxID)
	case "tty":
		if !requirePermission(w, r, auth.PermissionTTYAttach) {
			return
		}
		rt.handleTTY(w, r, tenantCtx, sandboxID)
	case "files":
		if !requireFilePermission(w, r) {
			return
		}
		rt.handleFiles(w, r, tenantCtx.Tenant.ID, sandboxID, strings.Join(parts[2:], "/"))
	case "mkdir":
		if !requirePermission(w, r, auth.PermissionFilesWrite) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req model.MkdirRequest
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := rt.service.Mkdir(r.Context(), tenantCtx.Tenant.ID, sandboxID, req.Path); err != nil {
			handleError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "tunnels":
		if len(parts) > 2 {
			if !requirePermission(w, r, auth.PermissionTunnelsWrite) {
				return
			}
		} else if r.Method == http.MethodGet {
			if !requirePermission(w, r, auth.PermissionTunnelsRead) {
				return
			}
		} else if r.Method == http.MethodPost {
			if !requirePermission(w, r, auth.PermissionTunnelsWrite) {
				return
			}
		}
		if len(parts) > 2 {
			if r.Method != http.MethodDelete {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if err := rt.service.RevokeTunnel(r.Context(), tenantCtx.Tenant.ID, parts[2]); err != nil {
				handleError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		rt.handleTunnels(w, r, tenantCtx.Tenant.ID, sandboxID)
	case "snapshots":
		switch r.Method {
		case http.MethodPost:
			if !requirePermission(w, r, auth.PermissionSnapshotsWrite) {
				return
			}
			var req model.CreateSnapshotRequest
			if err := decodeJSON(r, &req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			snapshot, err := rt.service.CreateSnapshot(r.Context(), tenantCtx.Tenant.ID, sandboxID, req)
			if err != nil {
				handleError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, snapshot)
		case http.MethodGet:
			if !requirePermission(w, r, auth.PermissionSnapshotsRead) {
				return
			}
			snapshots, err := rt.service.ListSnapshots(r.Context(), tenantCtx.Tenant.ID, sandboxID)
			if err != nil {
				handleError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, snapshots)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

func (rt *Router) handleLifecycle(w http.ResponseWriter, r *http.Request, fn func(context.Context) (model.Sandbox, error)) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sandbox, err := fn(r.Context())
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sandbox)
}

func (rt *Router) handleExec(w http.ResponseWriter, r *http.Request, tenantCtx auth.TenantContext, sandboxID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req model.ExecRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Timeout == 0 {
		req.Timeout = 5 * time.Minute
	}
	if r.URL.Query().Get("stream") == "1" {
		rt.streamExec(w, r, tenantCtx, sandboxID, req)
		return
	}
	execution, err := rt.service.ExecSandbox(r.Context(), tenantCtx.Tenant, tenantCtx.Quota, sandboxID, req, io.Discard, io.Discard)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, execution)
}

func (rt *Router) streamExec(w http.ResponseWriter, r *http.Request, tenantCtx auth.TenantContext, sandboxID string, req model.ExecRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	stdout := &sseWriter{w: w, event: "stdout"}
	stderr := &sseWriter{w: w, event: "stderr"}
	execution, err := rt.service.ExecSandbox(r.Context(), tenantCtx.Tenant, tenantCtx.Quota, sandboxID, req, stdout, stderr)
	if err != nil {
		handleError(w, err)
		return
	}
	_, _ = fmt.Fprintf(w, "event: result\ndata: %s\n\n", mustJSON(execution))
	flusher.Flush()
}

func (rt *Router) handleTTY(w http.ResponseWriter, r *http.Request, tenantCtx auth.TenantContext, sandboxID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	conn, err := rt.upgrader.Upgrade(w, r, nil)
	if err != nil {
		handleError(w, err)
		return
	}
	defer conn.Close()
	var req model.TTYRequest
	if err := conn.ReadJSON(&req); err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("invalid tty init payload"))
		return
	}
	_, session, handle, err := rt.service.CreateTTYSession(r.Context(), tenantCtx.Tenant.ID, sandboxID, req)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
		return
	}
	defer handle.Close()
	defer rt.service.CloseTTYSession(r.Context(), tenantCtx.Tenant.ID, session.ID)

	errCh := make(chan error, 2)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := handle.Reader().Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
					errCh <- writeErr
					return
				}
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()
	go func() {
		for {
			messageType, payload, err := conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if messageType == websocket.TextMessage && strings.HasPrefix(string(payload), "{") {
				var resize struct {
					Type string `json:"type"`
					Rows int    `json:"rows"`
					Cols int    `json:"cols"`
				}
				if json.Unmarshal(payload, &resize) == nil && resize.Type == "resize" {
					err = handle.Resize(model.ResizeRequest{Rows: resize.Rows, Cols: resize.Cols})
					if err == nil {
						_ = rt.service.UpdateTTYResize(r.Context(), tenantCtx.Tenant.ID, session.ID, resize.Rows, resize.Cols)
					}
					if err != nil {
						errCh <- err
						return
					}
					continue
				}
			}
			if _, err := handle.Writer().Write(payload); err != nil {
				errCh <- err
				return
			}
		}
	}()
	<-errCh
}

func (rt *Router) handleFiles(w http.ResponseWriter, r *http.Request, tenantID, sandboxID, path string) {
	switch r.Method {
	case http.MethodGet:
		content, err := rt.service.ReadFile(r.Context(), tenantID, sandboxID, path, r.URL.Query().Get("encoding"))
		if err != nil {
			handleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, content)
	case http.MethodPut:
		var req model.FileWriteRequest
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var err error
		encoding := strings.TrimSpace(req.Encoding)
		contentBase64 := strings.TrimSpace(req.ContentBase64)
		if strings.EqualFold(encoding, "base64") {
			if contentBase64 == "" {
				http.Error(w, "content_base64 is required when encoding=base64", http.StatusBadRequest)
				return
			}
			data, decodeErr := base64.StdEncoding.DecodeString(contentBase64)
			if decodeErr != nil {
				http.Error(w, "invalid content_base64 payload", http.StatusBadRequest)
				return
			}
			err = rt.service.WriteFileBytes(r.Context(), tenantID, sandboxID, path, data)
		} else if contentBase64 != "" {
			data, decodeErr := base64.StdEncoding.DecodeString(contentBase64)
			if decodeErr != nil {
				http.Error(w, "invalid content_base64 payload", http.StatusBadRequest)
				return
			}
			err = rt.service.WriteFileBytes(r.Context(), tenantID, sandboxID, path, data)
		} else {
			err = rt.service.WriteFile(r.Context(), tenantID, sandboxID, path, req.Content)
		}
		if err != nil {
			handleError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := rt.service.DeleteFile(r.Context(), tenantID, sandboxID, path); err != nil {
			handleError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (rt *Router) handleTunnels(w http.ResponseWriter, r *http.Request, tenantID, sandboxID string) {
	switch r.Method {
	case http.MethodPost:
		var req model.CreateTunnelRequest
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tunnel, err := rt.service.CreateTunnel(r.Context(), tenantID, sandboxID, req)
		if err != nil {
			handleError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, tunnel)
	case http.MethodGet:
		tunnels, err := rt.service.ListTunnels(r.Context(), tenantID, sandboxID)
		if err != nil {
			handleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, tunnels)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (rt *Router) handleSnapshotRoutes(w http.ResponseWriter, r *http.Request) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/snapshots/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requirePermission(w, r, auth.PermissionSnapshotsRead) {
			return
		}
		snapshot, err := rt.service.GetSnapshot(r.Context(), tenantCtx.Tenant.ID, parts[0])
		if err != nil {
			handleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	if len(parts) < 2 || parts[1] != "restore" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if !requirePermission(w, r, auth.PermissionSnapshotsWrite) {
		return
	}
	var req model.RestoreSnapshotRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sandbox, err := rt.service.RestoreSnapshot(r.Context(), tenantCtx.Tenant.ID, parts[0], req)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sandbox)
}

func (rt *Router) handleTunnelRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/tunnels/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	tunnelID := parts[0]
	if len(parts) == 1 {
		tenantCtx, ok := auth.FromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requirePermission(w, r, auth.PermissionTunnelsWrite) {
			return
		}
		if err := rt.service.RevokeTunnel(r.Context(), tenantCtx.Tenant.ID, tunnelID); err != nil {
			handleError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if parts[1] == "proxy" {
		rt.handleTunnelProxy(w, r, tunnelID)
		return
	}
	if parts[1] == "signed-url" {
		rt.handleTunnelSignedURL(w, r, tunnelID)
		return
	}
	http.NotFound(w, r)
}

func (rt *Router) handleTunnelSignedURL(w http.ResponseWriter, r *http.Request, tunnelID string) {
	tenantCtx, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requirePermission(w, r, auth.PermissionTunnelsRead) {
		return
	}
	tunnel, _, err := rt.service.GetTunnelForProxy(r.Context(), tunnelID)
	if err != nil {
		handleError(w, err)
		return
	}
	requesterTenantID := tenantCtx.Tenant.ID
	if tunnel.RevokedAt != nil {
		rt.recordTunnelDenial(r.Context(), requesterTenantID, tunnel, "tunnel.signed_url", "reason=revoked")
		http.Error(w, "tunnel revoked", http.StatusGone)
		return
	}
	if requesterTenantID != tunnel.TenantID {
		rt.recordTunnelDenial(r.Context(), requesterTenantID, tunnel, "tunnel.signed_url", "reason=tenant_mismatch")
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var req model.CreateTunnelSignedURLRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	path := req.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		http.Error(w, "signed tunnel path must start with '/'", http.StatusBadRequest)
		return
	}
	ttl := tunnelSignedURLDefaultTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	if ttl <= 0 {
		http.Error(w, "signed tunnel ttl must be positive", http.StatusBadRequest)
		return
	}
	if ttl > tunnelSignedURLMaxTTL {
		http.Error(w, fmt.Sprintf("signed tunnel ttl must be <= %s", tunnelSignedURLMaxTTL), http.StatusBadRequest)
		return
	}
	expiresAt := time.Now().UTC().Add(ttl)
	expiry := strconv.FormatInt(expiresAt.Unix(), 10)
	sig := rt.signTunnelCapability(tunnel.ID, expiry)
	signedURL, err := rt.buildTunnelProxyURL(tunnel.ID, path, url.Values{
		tunnelSignedURLExpiryKey: []string{expiry},
		tunnelSignedURLSigKey:    []string{sig},
	}, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	auditPath := sanitizeTunnelAuditPath(path)
	rt.service.RecordAuditEvent(r.Context(), tunnel.TenantID, tunnel.SandboxID, "tunnel.signed_url", tunnel.ID, "ok", fmt.Sprintf("path=%q expires_at=%s ttl_seconds=%d", auditPath, expiresAt.UTC().Format(time.RFC3339), int(ttl.Seconds())))
	rt.log.Info("tunnel signed url issued",
		"event", "tunnel.signed_url",
		"tenant_id", tunnel.TenantID,
		"sandbox_id", tunnel.SandboxID,
		"tunnel_id", tunnel.ID,
		"path", auditPath,
		"expires_at", expiresAt.UTC().Format(time.RFC3339),
		"ttl_seconds", int(ttl.Seconds()),
		"outcome", "ok",
	)
	writeJSON(w, http.StatusOK, model.TunnelSignedURL{URL: signedURL, ExpiresAt: expiresAt})
}

func (rt *Router) handleTunnelProxy(w http.ResponseWriter, r *http.Request, tunnelID string) {
	tunnel, sandbox, err := rt.service.GetTunnelForProxy(r.Context(), tunnelID)
	if err != nil {
		handleError(w, err)
		return
	}
	tenantCtx, hasTenant := auth.FromContext(r.Context())
	requesterTenantID := ""
	if hasTenant {
		requesterTenantID = tenantCtx.Tenant.ID
	}
	if tunnel.RevokedAt != nil {
		rt.recordTunnelDenial(r.Context(), requesterTenantID, tunnel, "tunnel.proxy", "reason=revoked")
		http.Error(w, "tunnel revoked", http.StatusGone)
		return
	}
	browserAuthorized, bootstrapped := rt.authorizeTunnelBrowserSession(w, r, tunnel)
	if bootstrapped {
		return
	}
	if tunnel.Visibility != "public" {
		if (!hasTenant || tenantCtx.Tenant.ID != tunnel.TenantID) && !browserAuthorized {
			rt.recordTunnelDenial(r.Context(), requesterTenantID, tunnel, "tunnel.proxy", fmt.Sprintf("reason=tenant_mismatch visibility=%s", tunnel.Visibility))
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	}
	queryAccessToken := ""
	authSource := "none"
	if tunnel.AuthMode == "token" {
		authorized := browserAuthorized
		presented := r.Header.Get("X-Tunnel-Token")
		if browserAuthorized {
			authSource = "signed_cookie"
		}
		if presented == "" {
			presented = r.URL.Query().Get("token")
			queryAccessToken = presented
			if presented != "" {
				authSource = "query"
			}
		} else {
			authSource = "header"
		}
		if !authorized && presented != "" && config.HashToken(presented) == tunnel.AuthSecretHash {
			authorized = true
		}
		if !authorized {
			rt.recordTunnelDenial(r.Context(), requesterTenantID, tunnel, "tunnel.proxy", fmt.Sprintf("reason=token_auth_failed visibility=%s", tunnel.Visibility))
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if browserAuthorized {
		authSource = "signed_cookie"
	} else if hasTenant && tenantCtx.Tenant.ID == tunnel.TenantID {
		authSource = "tenant_bearer"
	} else if tunnel.Visibility == "public" {
		authSource = "public"
	}
	rt.log.Info("tunnel proxy authorized",
		"event", "tunnel.proxy",
		"tenant_id", tunnel.TenantID,
		"sandbox_id", tunnel.SandboxID,
		"tunnel_id", tunnel.ID,
		"visibility", tunnel.Visibility,
		"auth_mode", tunnel.AuthMode,
		"auth_source", authSource,
		"method", r.Method,
		"path", r.URL.Path,
		"outcome", "ok",
	)
	if websocket.IsWebSocketUpgrade(r) {
		rt.handleTunnelWebSocket(w, r, tunnel, sandbox, queryAccessToken)
		return
	}
	rt.handleTunnelHTTPRequest(w, r, tunnel, sandbox, queryAccessToken)
}

func (rt *Router) handleTunnelHTTPRequest(w http.ResponseWriter, r *http.Request, tunnel model.Tunnel, sandbox model.Sandbox, queryAccessToken string) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	bridgeConn, err := rt.service.OpenSandboxLocalConn(ctx, sandbox, tunnel.TargetPort)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	conn := bridgeConn
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	proxyReq := r.Clone(ctx)
	proxyReq.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1/tunnels/"+tunnel.ID+"/proxy")
	proxyReq.URL.RawQuery = tunnelUpstreamQuery(r.URL.Query(), queryAccessToken).Encode()
	proxyReq.RequestURI = ""
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("sandbox-local:%d", tunnel.TargetPort)}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out = pr.Out.WithContext(ctx)
			pr.Out.URL.Path = proxyReq.URL.Path
			pr.Out.URL.RawPath = proxyReq.URL.RawPath
			pr.Out.URL.RawQuery = proxyReq.URL.RawQuery
			pr.Out.Host = ""
			pr.SetXForwarded()
			sanitizeTunnelProxyRequest(pr.Out.Header)
		},
		Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				if conn == nil {
					return nil, fmt.Errorf("sandbox tunnel bridge already used")
				}
				used := conn
				conn = nil
				return used, nil
			},
			DisableKeepAlives:     true,
			ForceAttemptHTTP2:     false,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, err.Error(), http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, proxyReq)
}

func (rt *Router) handleTunnelWebSocket(w http.ResponseWriter, r *http.Request, tunnel model.Tunnel, sandbox model.Sandbox, queryAccessToken string) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	bridgeConn, err := rt.service.OpenSandboxLocalConn(ctx, sandbox, tunnel.TargetPort)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	upstreamURL := url.URL{
		Scheme:   "ws",
		Host:     fmt.Sprintf("127.0.0.1:%d", tunnel.TargetPort),
		Path:     strings.TrimPrefix(r.URL.Path, "/v1/tunnels/"+tunnel.ID+"/proxy"),
		RawQuery: tunnelUpstreamQuery(r.URL.Query(), queryAccessToken).Encode(),
	}
	requestHeader := http.Header{}
	if origin := r.Header.Get("Origin"); origin != "" {
		requestHeader.Set("Origin", origin)
	}
	if userAgent := r.Header.Get("User-Agent"); userAgent != "" {
		requestHeader.Set("User-Agent", userAgent)
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		Subprotocols:     websocket.Subprotocols(r),
		NetDialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return bridgeConn, nil
		},
	}
	upstreamConn, response, err := dialer.DialContext(ctx, upstreamURL.String(), requestHeader)
	if err != nil {
		_ = bridgeConn.Close()
		status := http.StatusBadGateway
		if response != nil {
			status = response.StatusCode
		}
		http.Error(w, err.Error(), status)
		return
	}
	responseHeader := http.Header{}
	if subprotocol := upstreamConn.Subprotocol(); subprotocol != "" {
		responseHeader.Set("Sec-WebSocket-Protocol", subprotocol)
	}
	clientConn, err := rt.upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		_ = upstreamConn.Close()
		return
	}
	defer clientConn.Close()
	defer upstreamConn.Close()
	errCh := make(chan error, 2)
	go proxyWebSocketMessages(clientConn, upstreamConn, errCh)
	go proxyWebSocketMessages(upstreamConn, clientConn, errCh)
	<-errCh
}

func (rt *Router) authorizeTunnelBrowserSession(w http.ResponseWriter, r *http.Request, tunnel model.Tunnel) (authorized bool, bootstrapped bool) {
	if cookie, err := r.Cookie(tunnelAuthCookieName); err == nil {
		if expiry, sig, ok := parseTunnelAuthCookie(cookie.Value); ok && rt.validateTunnelCapability(tunnel.ID, expiry, sig) {
			return true, false
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false, false
	}
	expiry := r.URL.Query().Get(tunnelSignedURLExpiryKey)
	sig := r.URL.Query().Get(tunnelSignedURLSigKey)
	if !rt.validateTunnelCapability(tunnel.ID, expiry, sig) {
		return false, false
	}
	expiresAt, _ := strconv.ParseInt(expiry, 10, 64)
	http.SetCookie(w, &http.Cookie{
		Name:     tunnelAuthCookieName,
		Value:    tunnelAuthCookieValue(expiry, sig),
		Path:     "/v1/tunnels/" + tunnel.ID + "/proxy",
		Expires:  time.Unix(expiresAt, 0).UTC(),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(strings.ToLower(rt.operatorHost), "https://") || r.TLS != nil,
	})
	redirectURL := *r.URL
	query := redirectURL.Query()
	query.Del(tunnelSignedURLExpiryKey)
	query.Del(tunnelSignedURLSigKey)
	redirectURL.RawQuery = query.Encode()
	rt.serveTunnelBootstrapPage(w, redirectURL.String())
	return false, true
}

// serveTunnelBootstrapPage serves a small HTML page that clears stale gateway
// settings from localStorage and then redirects the browser to redirectURL.
// The JavaScript redirect preserves the URL fragment (e.g. #token=...) which
// a 302 redirect cannot guarantee.
//
// Background: browser-based apps behind the tunnel proxy (e.g. OpenClaw) may
// store the gateway WebSocket URL in localStorage.  When the tunnel is
// recreated the stored URL points at the old (revoked) tunnel and the
// WebSocket connection fails.  By clearing the stored gatewayUrl before the
// app boots, the app falls back to deriving it from the current page URL.
func (rt *Router) serveTunnelBootstrapPage(w http.ResponseWriter, redirectURL string) {
	urlJSON, _ := json.Marshal(redirectURL)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Loading…</title>
<script>
try{var k="openclaw.control.settings.v1",r=localStorage.getItem(k);if(r){var s=JSON.parse(r);delete s.gatewayUrl;localStorage.setItem(k,JSON.stringify(s))}}catch(e){}
window.location.replace(%s+window.location.hash);
</script></head><body><noscript><a href=%s>Continue</a></noscript></body></html>`, urlJSON, urlJSON)
}

func (rt *Router) buildTunnelProxyURL(tunnelID, path string, query url.Values, r *http.Request) (string, error) {
	base := rt.operatorHost
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid operator host: %w", err)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/tunnels/" + tunnelID + "/proxy" + path
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (rt *Router) signTunnelCapability(tunnelID, expiry string) string {
	mac := hmac.New(sha256.New, rt.tunnelSigningKey)
	_, _ = io.WriteString(mac, tunnelID)
	_, _ = io.WriteString(mac, ":")
	_, _ = io.WriteString(mac, expiry)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (rt *Router) validateTunnelCapability(tunnelID, expiry, signature string) bool {
	if strings.TrimSpace(expiry) == "" || strings.TrimSpace(signature) == "" {
		return false
	}
	expiresAt, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil {
		return false
	}
	if time.Now().UTC().After(time.Unix(expiresAt, 0).UTC()) {
		return false
	}
	expected := rt.signTunnelCapability(tunnelID, expiry)
	return hmac.Equal([]byte(expected), []byte(signature))
}

func newTunnelSigningKey(cfg config.Config) []byte {
	if key := strings.TrimSpace(cfg.TunnelSigningKey); key != "" {
		sum := sha256.Sum256([]byte(key))
		return sum[:]
	}
	if path := strings.TrimSpace(cfg.TunnelSigningKeyPath); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
				sum := sha256.Sum256([]byte(trimmed))
				return sum[:]
			}
		}
	}
	seed := stableTunnelSigningSeed(cfg)
	sum := sha256.Sum256(seed)
	return sum[:]
}

func stableTunnelSigningSeed(cfg config.Config) []byte {
	var builder strings.Builder
	builder.WriteString("or3-sandbox-tunnel-signing-key\n")
	builder.WriteString("auth_mode=")
	builder.WriteString(cfg.AuthMode)
	builder.WriteString("\n")
	switch cfg.AuthMode {
	case "jwt-hs256":
		builder.WriteString("issuer=")
		builder.WriteString(cfg.AuthJWTIssuer)
		builder.WriteString("\n")
		builder.WriteString("audience=")
		builder.WriteString(cfg.AuthJWTAudience)
		builder.WriteString("\n")
		paths := append([]string(nil), cfg.AuthJWTSecretPaths...)
		sort.Strings(paths)
		for _, path := range paths {
			builder.WriteString("jwt_secret=")
			if data, err := os.ReadFile(path); err == nil {
				builder.Write(data)
			}
			builder.WriteString("\n")
		}
	default:
		tenants := append([]config.TenantConfig(nil), cfg.Tenants...)
		sort.Slice(tenants, func(i, j int) bool {
			if tenants[i].ID == tenants[j].ID {
				return tenants[i].Token < tenants[j].Token
			}
			return tenants[i].ID < tenants[j].ID
		})
		for _, tenant := range tenants {
			builder.WriteString("tenant=")
			builder.WriteString(tenant.ID)
			builder.WriteString(":")
			builder.WriteString(tenant.Token)
			builder.WriteString("\n")
		}
	}
	return []byte(builder.String())
}

func tunnelUpstreamQuery(query url.Values, queryAccessToken string) url.Values {
	filtered := url.Values{}
	for key, values := range query {
		switch key {
		case tunnelSignedURLExpiryKey, tunnelSignedURLSigKey:
			continue
		case "token":
			preserved := make([]string, 0, len(values))
			for _, value := range values {
				if queryAccessToken != "" && value == queryAccessToken {
					continue
				}
				preserved = append(preserved, value)
			}
			if len(preserved) > 0 {
				filtered[key] = preserved
			}
		default:
			filtered[key] = append([]string(nil), values...)
		}
	}
	return filtered
}

func sanitizeTunnelProxyRequest(header http.Header) {
	header.Del("Authorization")
	header.Del("X-Tunnel-Token")
	if cookies := header.Values("Cookie"); len(cookies) > 0 {
		filteredCookies := make([]string, 0, len(cookies))
		for _, cookieHeader := range cookies {
			parts := strings.Split(cookieHeader, ";")
			kept := make([]string, 0, len(parts))
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed == "" || strings.HasPrefix(trimmed, tunnelAuthCookieName+"=") {
					continue
				}
				kept = append(kept, trimmed)
			}
			if len(kept) > 0 {
				filteredCookies = append(filteredCookies, strings.Join(kept, "; "))
			}
		}
		header.Del("Cookie")
		for _, cookieHeader := range filteredCookies {
			header.Add("Cookie", cookieHeader)
		}
	}
}

func tunnelAuthCookieValue(expiry, sig string) string {
	return expiry + "." + sig
}

func parseTunnelAuthCookie(value string) (expiry string, sig string, ok bool) {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func proxyWebSocketMessages(src, dst *websocket.Conn, errCh chan<- error) {
	for {
		messageType, payload, err := src.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		if err := dst.WriteMessage(messageType, payload); err != nil {
			errCh <- err
			return
		}
	}
}

func decodeJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, repository.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func requirePermission(w http.ResponseWriter, r *http.Request, permission string) bool {
	if err := auth.Require(r.Context(), permission); err != nil {
		handleError(w, err)
		return false
	}
	return true
}

func requireFilePermission(w http.ResponseWriter, r *http.Request) bool {
	permission := auth.PermissionFilesRead
	switch r.Method {
	case http.MethodPut, http.MethodDelete, http.MethodPost:
		permission = auth.PermissionFilesWrite
	}
	return requirePermission(w, r, permission)
}

func (rt *Router) recordTunnelDenial(ctx context.Context, requesterTenantID string, tunnel model.Tunnel, action, detail string) {
	logAttrs := []any{
		"event", action,
		"tenant_id", tunnel.TenantID,
		"sandbox_id", tunnel.SandboxID,
		"tunnel_id", tunnel.ID,
		"outcome", "denied",
		"detail", detail,
	}
	if requesterTenantID != "" {
		logAttrs = append(logAttrs, "requester_tenant_id", requesterTenantID)
	}
	rt.log.Warn("tunnel access denied", logAttrs...)
	if requesterTenantID == tunnel.TenantID {
		rt.service.RecordAuditEvent(ctx, tunnel.TenantID, tunnel.SandboxID, action, tunnel.ID, "denied", detail)
	}
}

func sanitizeTunnelAuditPath(path string) string {
	parsed, err := url.Parse(path)
	if err != nil {
		return path
	}
	sanitized := parsed.EscapedPath()
	if sanitized == "" {
		sanitized = parsed.Path
	}
	if sanitized == "" {
		return "/"
	}
	return sanitized
}

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (r *responseRecorder) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := r.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

func httpOutcome(status int) string {
	switch {
	case status >= 500:
		return "error"
	case status >= 400:
		return "denied"
	default:
		return "ok"
	}
}

func httpLogLevel(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

func requestResourceAttrs(path string) []any {
	switch {
	case strings.HasPrefix(path, "/v1/sandboxes/"):
		parts := strings.Split(strings.TrimPrefix(path, "/v1/sandboxes/"), "/")
		if len(parts) > 0 && parts[0] != "" {
			return []any{"sandbox_id", parts[0]}
		}
	case strings.HasPrefix(path, "/v1/snapshots/"):
		parts := strings.Split(strings.TrimPrefix(path, "/v1/snapshots/"), "/")
		if len(parts) > 0 && parts[0] != "" {
			return []any{"snapshot_id", parts[0]}
		}
	case strings.HasPrefix(path, "/v1/tunnels/"):
		parts := strings.Split(strings.TrimPrefix(path, "/v1/tunnels/"), "/")
		if len(parts) > 0 && parts[0] != "" {
			return []any{"tunnel_id", parts[0]}
		}
	}
	return nil
}

func mustJSON(payload any) string {
	data, _ := json.Marshal(payload)
	return string(data)
}

type sseWriter struct {
	w     io.Writer
	event string
}

func (s *sseWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	line := strings.ReplaceAll(string(p), "\n", "\\n")
	_, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", s.event, line)
	if flusher, ok := s.w.(http.Flusher); ok {
		flusher.Flush()
	}
	return len(p), err
}
