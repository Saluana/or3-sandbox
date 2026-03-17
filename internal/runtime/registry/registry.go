package registry

import (
	"context"
	"fmt"
	"net"

	"or3-sandbox/internal/model"
)

// RuntimeUnavailableError reports that a requested runtime selection is not
// registered for an operation.
type RuntimeUnavailableError struct {
	Selection model.RuntimeSelection
	Operation string
}

// Error returns the formatted runtime availability error.
func (e RuntimeUnavailableError) Error() string {
	return fmt.Sprintf("runtime selection %q is unavailable for %s", e.Selection, e.Operation)
}

// Registry dispatches runtime operations to the implementation registered for a
// runtime selection.
type Registry struct {
	runtimes map[model.RuntimeSelection]model.RuntimeManager
}

// New returns a registry backed by a defensive copy of runtimes.
func New(runtimes map[model.RuntimeSelection]model.RuntimeManager) *Registry {
	copyMap := make(map[model.RuntimeSelection]model.RuntimeManager, len(runtimes))
	for selection, runtime := range runtimes {
		copyMap[selection] = runtime
	}
	return &Registry{runtimes: copyMap}
}

func (r *Registry) runtimeForSelection(selection model.RuntimeSelection, operation string) (model.RuntimeManager, error) {
	runtime, ok := r.runtimes[selection]
	if !ok {
		return nil, RuntimeUnavailableError{Selection: selection, Operation: operation}
	}
	return runtime, nil
}

func (r *Registry) runtimeForSandbox(sandbox model.Sandbox, operation string) (model.RuntimeManager, error) {
	selection := model.ResolveRuntimeSelection(sandbox.RuntimeSelection, sandbox.RuntimeBackend)
	return r.runtimeForSelection(selection, operation)
}

// Create delegates sandbox creation to the runtime selected by spec.
func (r *Registry) Create(ctx context.Context, spec model.SandboxSpec) (model.RuntimeState, error) {
	selection := model.ResolveRuntimeSelection(spec.RuntimeSelection, spec.RuntimeBackend)
	runtime, err := r.runtimeForSelection(selection, "create")
	if err != nil {
		return model.RuntimeState{}, err
	}
	return runtime.Create(ctx, spec)
}

// Start delegates sandbox start to the runtime selected by sandbox.
func (r *Registry) Start(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "start")
	if err != nil {
		return model.RuntimeState{}, err
	}
	return runtime.Start(ctx, sandbox)
}

// Stop delegates sandbox stop to the runtime selected by sandbox.
func (r *Registry) Stop(ctx context.Context, sandbox model.Sandbox, force bool) (model.RuntimeState, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "stop")
	if err != nil {
		return model.RuntimeState{}, err
	}
	return runtime.Stop(ctx, sandbox, force)
}

// Suspend delegates sandbox suspend to the runtime selected by sandbox.
func (r *Registry) Suspend(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "suspend")
	if err != nil {
		return model.RuntimeState{}, err
	}
	return runtime.Suspend(ctx, sandbox)
}

// Resume delegates sandbox resume to the runtime selected by sandbox.
func (r *Registry) Resume(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "resume")
	if err != nil {
		return model.RuntimeState{}, err
	}
	return runtime.Resume(ctx, sandbox)
}

// Destroy delegates sandbox teardown to the runtime selected by sandbox.
func (r *Registry) Destroy(ctx context.Context, sandbox model.Sandbox) error {
	runtime, err := r.runtimeForSandbox(sandbox, "destroy")
	if err != nil {
		return err
	}
	return runtime.Destroy(ctx, sandbox)
}

// Inspect delegates runtime inspection to the runtime selected by sandbox.
func (r *Registry) Inspect(ctx context.Context, sandbox model.Sandbox) (model.RuntimeState, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "inspect")
	if err != nil {
		return model.RuntimeState{}, err
	}
	return runtime.Inspect(ctx, sandbox)
}

// Exec delegates command execution to the runtime selected by sandbox.
func (r *Registry) Exec(ctx context.Context, sandbox model.Sandbox, req model.ExecRequest, streams model.ExecStreams) (model.ExecHandle, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "exec")
	if err != nil {
		return nil, err
	}
	return runtime.Exec(ctx, sandbox, req, streams)
}

// AttachTTY delegates TTY attachment to the runtime selected by sandbox.
func (r *Registry) AttachTTY(ctx context.Context, sandbox model.Sandbox, req model.TTYRequest) (model.TTYHandle, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "attach-tty")
	if err != nil {
		return nil, err
	}
	return runtime.AttachTTY(ctx, sandbox, req)
}

// CreateSnapshot delegates snapshot creation to the runtime selected by
// sandbox.
func (r *Registry) CreateSnapshot(ctx context.Context, sandbox model.Sandbox, snapshotID string) (model.SnapshotInfo, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "create-snapshot")
	if err != nil {
		return model.SnapshotInfo{}, err
	}
	return runtime.CreateSnapshot(ctx, sandbox, snapshotID)
}

// RestoreSnapshot delegates snapshot restore to the runtime selected by
// sandbox.
func (r *Registry) RestoreSnapshot(ctx context.Context, sandbox model.Sandbox, snapshot model.Snapshot) (model.RuntimeState, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "restore-snapshot")
	if err != nil {
		return model.RuntimeState{}, err
	}
	return runtime.RestoreSnapshot(ctx, sandbox, snapshot)
}

// ReadWorkspaceFile delegates workspace file reading to the runtime selected by sandbox.
func (r *Registry) ReadWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string) (model.FileReadResponse, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "read-workspace-file")
	if err != nil {
		return model.FileReadResponse{}, err
	}
	type reader interface {
		ReadWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string) (model.FileReadResponse, error)
	}
	if rt, ok := runtime.(reader); ok {
		return rt.ReadWorkspaceFile(ctx, sandbox, relativePath)
	}
	return model.FileReadResponse{}, fmt.Errorf("runtime %q does not support ReadWorkspaceFile", sandbox.RuntimeSelection)
}

// WriteWorkspaceFile delegates workspace file writing to the runtime selected by sandbox.
func (r *Registry) WriteWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string, content string) error {
	runtime, err := r.runtimeForSandbox(sandbox, "write-workspace-file")
	if err != nil {
		return err
	}
	type writer interface {
		WriteWorkspaceFile(ctx context.Context, sandbox model.Sandbox, relativePath string, content string) error
	}
	if rt, ok := runtime.(writer); ok {
		return rt.WriteWorkspaceFile(ctx, sandbox, relativePath, content)
	}
	return fmt.Errorf("runtime %q does not support WriteWorkspaceFile", sandbox.RuntimeSelection)
}

// DeleteWorkspacePath delegates workspace deletion to the runtime selected by sandbox.
func (r *Registry) DeleteWorkspacePath(ctx context.Context, sandbox model.Sandbox, relativePath string) error {
	runtime, err := r.runtimeForSandbox(sandbox, "delete-workspace-path")
	if err != nil {
		return err
	}
	type deleter interface {
		DeleteWorkspacePath(ctx context.Context, sandbox model.Sandbox, relativePath string) error
	}
	if rt, ok := runtime.(deleter); ok {
		return rt.DeleteWorkspacePath(ctx, sandbox, relativePath)
	}
	return fmt.Errorf("runtime %q does not support DeleteWorkspacePath", sandbox.RuntimeSelection)
}

// MkdirWorkspace delegates workspace directory creation to the runtime selected by sandbox.
func (r *Registry) MkdirWorkspace(ctx context.Context, sandbox model.Sandbox, relativePath string) error {
	runtime, err := r.runtimeForSandbox(sandbox, "mkdir-workspace")
	if err != nil {
		return err
	}
	type mkdirer interface {
		MkdirWorkspace(ctx context.Context, sandbox model.Sandbox, relativePath string) error
	}
	if rt, ok := runtime.(mkdirer); ok {
		return rt.MkdirWorkspace(ctx, sandbox, relativePath)
	}
	return fmt.Errorf("runtime %q does not support MkdirWorkspace", sandbox.RuntimeSelection)
}

// ReadWorkspaceFileBytes delegates binary workspace file reading to the runtime selected by sandbox.
func (r *Registry) ReadWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string) ([]byte, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "read-workspace-file-bytes")
	if err != nil {
		return nil, err
	}
	type binaryReader interface {
		ReadWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string) ([]byte, error)
	}
	if rt, ok := runtime.(binaryReader); ok {
		return rt.ReadWorkspaceFileBytes(ctx, sandbox, relativePath)
	}
	return nil, fmt.Errorf("runtime %q does not support ReadWorkspaceFileBytes", sandbox.RuntimeSelection)
}

// WriteWorkspaceFileBytes delegates binary workspace file writing to the runtime selected by sandbox.
func (r *Registry) WriteWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string, content []byte) error {
	runtime, err := r.runtimeForSandbox(sandbox, "write-workspace-file-bytes")
	if err != nil {
		return err
	}
	type binaryWriter interface {
		WriteWorkspaceFileBytes(ctx context.Context, sandbox model.Sandbox, relativePath string, content []byte) error
	}
	if rt, ok := runtime.(binaryWriter); ok {
		return rt.WriteWorkspaceFileBytes(ctx, sandbox, relativePath, content)
	}
	return fmt.Errorf("runtime %q does not support WriteWorkspaceFileBytes", sandbox.RuntimeSelection)
}

// MeasureStorage delegates storage measurement to the runtime selected by sandbox.
func (r *Registry) MeasureStorage(ctx context.Context, sandbox model.Sandbox) (model.StorageUsage, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "measure-storage")
	if err != nil {
		return model.StorageUsage{}, err
	}
	type measurer interface {
		MeasureStorage(ctx context.Context, sandbox model.Sandbox) (model.StorageUsage, error)
	}
	if rt, ok := runtime.(measurer); ok {
		return rt.MeasureStorage(ctx, sandbox)
	}
	return model.StorageUsage{}, fmt.Errorf("runtime %q does not support MeasureStorage", sandbox.RuntimeSelection)
}

// ExportWorkspaceArchive delegates workspace export to the runtime selected by sandbox.
func (r *Registry) ExportWorkspaceArchive(ctx context.Context, sandbox model.Sandbox, paths []string, maxBytes int64) (string, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "export-workspace-archive")
	if err != nil {
		return "", err
	}
	if rt, ok := runtime.(model.WorkspaceArchiveExporter); ok {
		return rt.ExportWorkspaceArchive(ctx, sandbox, paths, maxBytes)
	}
	return "", model.UnsupportedRuntimeOperationError{Selection: sandbox.RuntimeSelection, Operation: "ExportWorkspaceArchive"}
}

// OpenSandboxLocalConn delegates daemon-side TCP bridging to the runtime selected by sandbox.
func (r *Registry) OpenSandboxLocalConn(ctx context.Context, sandbox model.Sandbox, targetPort int) (net.Conn, error) {
	runtime, err := r.runtimeForSandbox(sandbox, "open-sandbox-local-conn")
	if err != nil {
		return nil, err
	}
	type bridgeOpener interface {
		OpenSandboxLocalConn(ctx context.Context, sandbox model.Sandbox, targetPort int) (net.Conn, error)
	}
	if rt, ok := runtime.(bridgeOpener); ok {
		return rt.OpenSandboxLocalConn(ctx, sandbox, targetPort)
	}
	return nil, fmt.Errorf("runtime %q does not support OpenSandboxLocalConn", sandbox.RuntimeSelection)
}

// AgentSessionMetrics exposes runtime agent-session telemetry when supported by the selected runtime(s).
func (r *Registry) AgentSessionMetrics() model.RuntimeAgentSessionsHealth {
	type metricsProvider interface {
		AgentSessionMetrics() model.RuntimeAgentSessionsHealth
	}
	combined := model.RuntimeAgentSessionsHealth{}
	for _, runtime := range r.runtimes {
		provider, ok := runtime.(metricsProvider)
		if !ok {
			continue
		}
		metrics := provider.AgentSessionMetrics()
		combined.SessionsOpened += metrics.SessionsOpened
		combined.SessionsReused += metrics.SessionsReused
		combined.SessionsInvalidated += metrics.SessionsInvalidated
		combined.SessionsClosed += metrics.SessionsClosed
		combined.BufferedExecEvents += metrics.BufferedExecEvents
		combined.BufferedFileEvents += metrics.BufferedFileEvents
		combined.DroppedExecEvents += metrics.DroppedExecEvents
		combined.DroppedFileEvents += metrics.DroppedFileEvents
	}
	return combined
}

func (r *Registry) AgentSessionMetricsForSandboxes(sandboxes []model.Sandbox) (model.RuntimeAgentSessionsHealth, bool) {
	type scopedMetricsProvider interface {
		AgentSessionMetricsForSandboxes(sandboxes []model.Sandbox) (model.RuntimeAgentSessionsHealth, bool)
	}
	grouped := make(map[model.RuntimeSelection][]model.Sandbox)
	for _, sandbox := range sandboxes {
		selection := model.ResolveRuntimeSelection(sandbox.RuntimeSelection, sandbox.RuntimeBackend)
		grouped[selection] = append(grouped[selection], sandbox)
	}
	combined := model.RuntimeAgentSessionsHealth{}
	supported := false
	for selection, scopedSandboxes := range grouped {
		runtime, ok := r.runtimes[selection]
		if !ok {
			continue
		}
		provider, ok := runtime.(scopedMetricsProvider)
		if !ok {
			continue
		}
		metrics, providerSupported := provider.AgentSessionMetricsForSandboxes(scopedSandboxes)
		if !providerSupported {
			continue
		}
		supported = true
		combined.SessionsOpened += metrics.SessionsOpened
		combined.SessionsReused += metrics.SessionsReused
		combined.SessionsInvalidated += metrics.SessionsInvalidated
		combined.SessionsClosed += metrics.SessionsClosed
		combined.BufferedExecEvents += metrics.BufferedExecEvents
		combined.BufferedFileEvents += metrics.BufferedFileEvents
		combined.DroppedExecEvents += metrics.DroppedExecEvents
		combined.DroppedFileEvents += metrics.DroppedFileEvents
	}
	return combined, supported
}
