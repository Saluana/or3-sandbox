package config

import (
	"strings"
	"testing"
	"time"
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
		goos:         "linux",
		commandExists: func(string) error { return nil },
		fileReadable: func(string) error { return nil },
		kvmAvailable: func() error { return nil },
		hvfAvailable: func() error { t.Fatal("hvf probe should not be used on linux"); return nil },
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
		goos:         "darwin",
		commandExists: func(string) error { return nil },
		fileReadable: func(string) error { return nil },
		kvmAvailable: func() error { t.Fatal("kvm probe should not be used on macOS"); return nil },
		hvfAvailable: func() error { return nil },
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
		goos:         "linux",
		commandExists: func(string) error { return nil },
		fileReadable: func(string) error { return nil },
		kvmAvailable: func() error { return errString("missing /dev/kvm") },
		hvfAvailable: func() error { return nil },
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

type errString string

func (e errString) Error() string {
	return string(e)
}
