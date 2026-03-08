package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	log      *slog.Logger
	service  *service.Service
	upgrader websocket.Upgrader
}

func New(log *slog.Logger, svc *service.Service) http.Handler {
	router := &Router{
		log:     log,
		service: svc,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", router.health)
	mux.HandleFunc("/metrics", router.handleMetrics)
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
		next.ServeHTTP(w, r)
		log.Info("http request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
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
		content, err := rt.service.ReadFile(r.Context(), tenantID, sandboxID, path)
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
		if err := rt.service.WriteFile(r.Context(), tenantID, sandboxID, path, req.Content); err != nil {
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
	http.NotFound(w, r)
}

func (rt *Router) handleTunnelProxy(w http.ResponseWriter, r *http.Request, tunnelID string) {
	tunnel, sandbox, err := rt.service.GetTunnelForProxy(r.Context(), tunnelID)
	if err != nil {
		handleError(w, err)
		return
	}
	if tunnel.RevokedAt != nil {
		http.Error(w, "tunnel revoked", http.StatusGone)
		return
	}
	tenantCtx, hasTenant := auth.FromContext(r.Context())
	if tunnel.Visibility != "public" {
		if !hasTenant || tenantCtx.Tenant.ID != tunnel.TenantID {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	}
	if tunnel.AuthMode == "token" {
		presented := r.Header.Get("X-Tunnel-Token")
		if presented == "" {
			presented = r.URL.Query().Get("token")
		}
		if presented == "" || config.HashToken(presented) != tunnel.AuthSecretHash {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	target := fmt.Sprintf("http://127.0.0.1:%d%s", tunnel.TargetPort, strings.TrimPrefix(r.URL.Path, "/v1/tunnels/"+tunnel.ID+"/proxy"))
	if raw := r.URL.RawQuery; raw != "" {
		target += "?" + raw
	}
	script := `
if command -v curl >/dev/null 2>&1; then
	exec curl -sS -i -X "$METHOD" "$URL"
fi
if [ "$METHOD" = "GET" ] && command -v wget >/dev/null 2>&1; then
	exec wget -qO- "$URL"
fi
if [ "$METHOD" = "GET" ] && command -v busybox >/dev/null 2>&1; then
	exec busybox wget -qO- "$URL"
fi
if [ "$METHOD" = "GET" ] && command -v python3 >/dev/null 2>&1; then
	exec python3 -c 'import sys, urllib.request; sys.stdout.write(urllib.request.urlopen(sys.argv[1]).read().decode())' "$URL"
fi
echo "no supported http client in sandbox" >&2
exit 127
`
	req := model.ExecRequest{
		Command: []string{"sh", "-lc", script},
		Env: map[string]string{
			"METHOD": r.Method,
			"URL":    target,
		},
		Cwd:     "/workspace",
		Timeout: 30 * time.Second,
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		body, _ := io.ReadAll(r.Body)
		req.Env["BODY"] = string(body)
		req.Command = []string{"sh", "-lc", `
if ! command -v curl >/dev/null 2>&1; then
	echo "curl required for non-GET tunnel proxy requests" >&2
	exit 127
fi
exec curl -sS -i -X "$METHOD" --data-binary "$BODY" "$URL"
`}
	}
	var stdout strings.Builder
	var stderr strings.Builder
	quotaView, err := rt.service.GetTenantQuotaView(r.Context(), tunnel.TenantID)
	if err != nil {
		handleError(w, err)
		return
	}
	_, err = rt.service.ExecSandbox(r.Context(), model.Tenant{ID: tunnel.TenantID}, quotaView.Quota, sandbox.ID, req, &stdout, &stderr)
	if err != nil {
		handleError(w, err)
		return
	}
	payload := stdout.String()
	parts := strings.SplitN(payload, "\r\n\r\n", 2)
	if len(parts) == 2 {
		for _, line := range strings.Split(parts[0], "\r\n") {
			if strings.HasPrefix(strings.ToLower(line), "http/") {
				continue
			}
			header := strings.SplitN(line, ": ", 2)
			if len(header) == 2 {
				w.Header().Add(header[0], header[1])
			}
		}
		_, _ = io.WriteString(w, parts[1])
		return
	}
	_, _ = io.WriteString(w, payload)
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
