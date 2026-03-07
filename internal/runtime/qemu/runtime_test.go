package qemu

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"or3-sandbox/internal/model"
)

func TestResolveAccel(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		goos    string
		want    string
		wantErr bool
	}{
		{name: "auto linux", value: "auto", goos: "linux", want: "kvm"},
		{name: "auto darwin", value: "auto", goos: "darwin", want: "hvf"},
		{name: "explicit kvm", value: "kvm", goos: "linux", want: "kvm"},
		{name: "explicit hvf", value: "hvf", goos: "darwin", want: "hvf"},
		{name: "invalid host", value: "auto", goos: "windows", wantErr: true},
		{name: "invalid accel", value: "tcg", goos: "linux", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveAccel(tc.value, tc.goos)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("unexpected accel: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestCreateAndSnapshotArtifacts(t *testing.T) {
	base := t.TempDir()
	rootfs := filepath.Join(base, "rootfs")
	workspace := filepath.Join(base, "workspace")
	spec := model.SandboxSpec{
		SandboxID:     "sbx-test",
		StorageRoot:   rootfs,
		WorkspaceRoot: workspace,
		CacheRoot:     filepath.Join(base, "cache"),
		DiskLimitMB:   16,
	}
	r := &Runtime{}
	state, err := r.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if state.Status != model.SandboxStatusStopped {
		t.Fatalf("unexpected create status: %s", state.Status)
	}
	layout := layoutForSpec(spec)
	for _, path := range []string{layout.rootDiskPath, layout.workspaceDiskPath, layout.knownHostsPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected artifact %s: %v", path, err)
		}
	}

	sandbox := model.Sandbox{
		ID:            spec.SandboxID,
		RuntimeID:     state.RuntimeID,
		StorageRoot:   spec.StorageRoot,
		WorkspaceRoot: spec.WorkspaceRoot,
		CacheRoot:     spec.CacheRoot,
	}
	snapshot, err := r.CreateSnapshot(context.Background(), sandbox, "snap-test")
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	for _, path := range []string{snapshot.ImageRef, snapshot.WorkspaceTar} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected snapshot artifact %s: %v", path, err)
		}
	}
}
