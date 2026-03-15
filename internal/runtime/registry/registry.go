package registry

import (
	"context"
	"fmt"

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
