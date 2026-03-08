package config

import (
	"strings"
	"testing"
	"time"

	"or3-sandbox/internal/model"
)

func TestValidateRuntimeConfigDockerRequiresTrustedFlag(t *testing.T) {
	cfg := Config{RuntimeBackend: "docker"}
	err := validateRuntimeConfig(cfg, runtimeValidationProbe{})
	if err == nil || !strings.Contains(err.Error(), "SANDBOX_TRUSTED_DOCKER_RUNTIME=true") {
		t.Fatalf("expected trusted docker error, got %v", err)
	}

	cfg.TrustedDockerRuntime = true
	if err := validateRuntimeConfig(cfg, runtimeValidationProbe{}); err != nil {
		t.Fatalf("expected docker config to validate, got %v", err)
	}
}

func TestValidateRuntimeConfigQEMUOnLinux(t *testing.T) {
	cfg := Config{
		RuntimeBackend:        "qemu",
		QEMUBinary:            "qemu-system-x86_64",
		QEMUAccel:             "auto",
		QEMUBaseImagePath:     "/images/base.qcow2",
		QEMUSSHUser:           "sandbox",
		QEMUSSHPrivateKeyPath: "/keys/id_ed25519",
		QEMUBootTimeout:       90 * time.Second,
	}
	probe := runtimeValidationProbe{
		goos:          "linux",
		commandExists: func(string) error { return nil },
		fileReadable:  func(string) error { return nil },
		kvmAvailable:  func() error { return nil },
		hvfAvailable:  func() error { t.Fatal("hvf probe should not be used on linux"); return nil },
	}
	if err := validateRuntimeConfig(cfg, probe); err != nil {
		t.Fatalf("expected qemu linux config to validate, got %v", err)
	}
}

func TestValidateRuntimeConfigQEMUOnDarwin(t *testing.T) {
	cfg := Config{
		RuntimeBackend:        "qemu",
		QEMUBinary:            "qemu-system-aarch64",
		QEMUAccel:             "auto",
		QEMUBaseImagePath:     "/images/base.qcow2",
		QEMUSSHUser:           "sandbox",
		QEMUSSHPrivateKeyPath: "/keys/id_ed25519",
		QEMUBootTimeout:       time.Minute,
	}
	probe := runtimeValidationProbe{
		goos:          "darwin",
		commandExists: func(string) error { return nil },
		fileReadable:  func(string) error { return nil },
		kvmAvailable:  func() error { t.Fatal("kvm probe should not be used on macOS"); return nil },
		hvfAvailable:  func() error { return nil },
	}
	if err := validateRuntimeConfig(cfg, probe); err != nil {
		t.Fatalf("expected qemu darwin config to validate, got %v", err)
	}
}

func TestValidateRuntimeConfigQEMUMissingPrerequisites(t *testing.T) {
	cfg := Config{
		RuntimeBackend:        "qemu",
		QEMUBinary:            "qemu-system-x86_64",
		QEMUAccel:             "kvm",
		QEMUBaseImagePath:     "/images/base.qcow2",
		QEMUSSHUser:           "sandbox",
		QEMUSSHPrivateKeyPath: "/keys/id_ed25519",
		QEMUBootTimeout:       time.Minute,
	}
	probe := runtimeValidationProbe{
		goos:          "linux",
		commandExists: func(string) error { return nil },
		fileReadable:  func(string) error { return nil },
		kvmAvailable:  func() error { return errString("missing /dev/kvm") },
		hvfAvailable:  func() error { return nil },
	}
	err := validateRuntimeConfig(cfg, probe)
	if err == nil || !strings.Contains(err.Error(), "KVM support") {
		t.Fatalf("expected kvm support error, got %v", err)
	}
}

func TestValidateRuntimeConfigRejectsUnsupportedBackend(t *testing.T) {
	cfg := Config{RuntimeBackend: "podman"}
	err := validateRuntimeConfig(cfg, runtimeValidationProbe{})
	if err == nil || !strings.Contains(err.Error(), "unsupported runtime backend") {
		t.Fatalf("expected unsupported backend error, got %v", err)
	}
}

func TestLoadParsesFractionalCPUDefaultsAndQuota(t *testing.T) {
	t.Setenv("SANDBOX_RUNTIME", "docker")
	t.Setenv("SANDBOX_TRUSTED_DOCKER_RUNTIME", "true")
	t.Setenv("SANDBOX_DEFAULT_CPU", "1.5")
	t.Setenv("SANDBOX_QUOTA_MAX_CPU", "2500m")
	t.Setenv("SANDBOX_TOKENS", "token-a=tenant-a")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.DefaultCPULimit != model.MustParseCPUQuantity("1.5") {
		t.Fatalf("unexpected default cpu %v", cfg.DefaultCPULimit)
	}
	if cfg.DefaultQuota.MaxCPUCores != model.MustParseCPUQuantity("2500m") {
		t.Fatalf("unexpected max cpu quota %v", cfg.DefaultQuota.MaxCPUCores)
	}
}

func TestValidateRejectsNonPositiveCPUValues(t *testing.T) {
	cfg := Config{
		ListenAddress:        ":8080",
		DatabasePath:         "/tmp/test.db",
		StorageRoot:          t.TempDir(),
		SnapshotRoot:         t.TempDir(),
		BaseImageRef:         "alpine:3.20",
		RuntimeBackend:       "docker",
		TrustedDockerRuntime: true,
		DefaultCPULimit:      0,
		DefaultQuota: model.TenantQuota{
			MaxCPUCores: 0,
		},
		DefaultNetworkMode: model.NetworkModeInternetEnabled,
		Tenants: []TenantConfig{
			{ID: "tenant-a", Name: "Tenant A", Token: "token-a"},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "default cpu limit must be positive") || !strings.Contains(err.Error(), "default quota max cpu must be positive") {
		t.Fatalf("expected positive CPU validation error, got %v", err)
	}
}

func TestValidateRejectsFractionalQEMUDefaultCPU(t *testing.T) {
	cfg := Config{
		ListenAddress:   ":8080",
		DatabasePath:    "/tmp/test.db",
		StorageRoot:     t.TempDir(),
		SnapshotRoot:    t.TempDir(),
		BaseImageRef:    "guest-base.qcow2",
		RuntimeBackend:  "qemu",
		DefaultCPULimit: model.MustParseCPUQuantity("1500m"),
		DefaultQuota: model.TenantQuota{
			MaxCPUCores: model.CPUCores(2),
		},
		DefaultNetworkMode: model.NetworkModeInternetEnabled,
		Tenants: []TenantConfig{
			{ID: "tenant-a", Name: "Tenant A", Token: "token-a"},
		},
		QEMUBinary:            "qemu-system-x86_64",
		QEMUAccel:             "kvm",
		QEMUBaseImagePath:     "/images/base.qcow2",
		QEMUSSHUser:           "sandbox",
		QEMUSSHPrivateKeyPath: "/keys/id_ed25519",
		QEMUBootTimeout:       time.Minute,
	}

	err := validateRuntimeConfig(cfg, runtimeValidationProbe{
		goos:          "linux",
		commandExists: func(string) error { return nil },
		fileReadable:  func(string) error { return nil },
		kvmAvailable:  func() error { return nil },
		hvfAvailable:  func() error { return nil },
	})
	if err != nil {
		t.Fatalf("expected qemu runtime config to validate, got %v", err)
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "whole-core default cpu") {
		t.Fatalf("expected fractional qemu default rejection, got %v", err)
	}
}

type errString string

func (e errString) Error() string {
	return string(e)
}
