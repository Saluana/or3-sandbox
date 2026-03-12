package config

import (
	"os"
	"os/exec"
	"path/filepath"
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
	cfg.DockerUser = "10001:10001"
	cfg.DockerTmpfsSizeMB = 64
	if err := validateRuntimeConfig(cfg, runtimeValidationProbe{}); err != nil {
		t.Fatalf("expected docker config to validate, got %v", err)
	}
}

func TestValidateRuntimeConfigDockerSecurityProfile(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "seccomp.json")
	if err := os.WriteFile(profile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		RuntimeBackend:       "docker",
		TrustedDockerRuntime: true,
		DockerUser:           "10001:10001",
		DockerTmpfsSizeMB:    64,
		DockerSeccompProfile: profile,
	}
	if err := validateRuntimeConfig(cfg, runtimeValidationProbe{fileReadable: requireReadableFile}); err != nil {
		t.Fatalf("expected docker seccomp config to validate, got %v", err)
	}
	cfg.DockerTmpfsSizeMB = -1
	err := validateRuntimeConfig(cfg, runtimeValidationProbe{fileReadable: requireReadableFile})
	if err == nil || !strings.Contains(err.Error(), "SANDBOX_DOCKER_TMPFS_MB") {
		t.Fatalf("expected tmpfs validation error, got %v", err)
	}
}

func TestValidateRuntimeConfigQEMUOnLinux(t *testing.T) {
	cfg := Config{
		RuntimeBackend:            "qemu",
		QEMUBinary:                "qemu-system-x86_64",
		QEMUAccel:                 "auto",
		QEMUBaseImagePath:         "/images/base.qcow2",
		QEMUControlMode:           model.GuestControlModeAgent,
		QEMUAllowedProfiles:       []model.GuestProfile{model.GuestProfileCore},
		QEMUSSHUser:               "sandbox",
		QEMUSSHPrivateKeyPath:     "/keys/id_ed25519",
		QEMUSSHHostKeyPath:        "/keys/guest_host_ed25519.pub",
		QEMUBootTimeout:           90 * time.Second,
		QEMUAllowedBaseImagePaths: []string{"/images/extra.qcow2"},
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
		QEMUControlMode:       model.GuestControlModeAgent,
		QEMUAllowedProfiles:   []model.GuestProfile{model.GuestProfileCore},
		QEMUSSHUser:           "sandbox",
		QEMUSSHPrivateKeyPath: "/keys/id_ed25519",
		QEMUSSHHostKeyPath:    "/keys/guest_host_ed25519.pub",
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
		QEMUControlMode:       model.GuestControlModeAgent,
		QEMUAllowedProfiles:   []model.GuestProfile{model.GuestProfileCore},
		QEMUSSHUser:           "sandbox",
		QEMUSSHPrivateKeyPath: "/keys/id_ed25519",
		QEMUSSHHostKeyPath:    "/keys/guest_host_ed25519.pub",
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

func TestValidateRuntimeConfigKataRejectsNonLinuxHost(t *testing.T) {
	cfg := Config{
		EnabledRuntimeSelections: []model.RuntimeSelection{model.RuntimeSelectionContainerdKataProfessional},
		DefaultRuntimeSelection:  model.RuntimeSelectionContainerdKataProfessional,
		KataBinary:               "ctr",
		KataRuntimeClass:         "io.containerd.kata.v2",
		KataContainerdSocket:     "/run/containerd/containerd.sock",
	}
	probe := runtimeValidationProbe{
		goos:          "darwin",
		commandExists: func(string) error { return nil },
		fileReadable:  func(string) error { return nil },
		kvmAvailable:  func() error { return nil },
		hvfAvailable:  func() error { return nil },
	}
	err := validateRuntimeConfig(cfg, probe)
	if err == nil || !strings.Contains(err.Error(), "kata runtime requires linux") || !strings.Contains(err.Error(), "darwin") {
		t.Fatalf("expected non-linux kata validation error, got %v", err)
	}
}

func TestValidateRuntimeConfigRejectsUnsupportedBackend(t *testing.T) {
	cfg := Config{
		RuntimeBackend:           "podman",
		EnabledRuntimeSelections: []model.RuntimeSelection{"podman"},
		DefaultRuntimeSelection:  model.RuntimeSelection("podman"),
	}
	err := validateRuntimeConfig(cfg, runtimeValidationProbe{})
	if err == nil || !strings.Contains(err.Error(), "unsupported default runtime selection") {
		t.Fatalf("expected unsupported backend error, got %v", err)
	}
}

func TestValidateAuthConfigJWTRequiresSecretPath(t *testing.T) {
	err := validateAuthConfig(Config{
		AuthMode:        "jwt-hs256",
		AuthJWTIssuer:   "issuer.example",
		AuthJWTAudience: "sandbox-api",
	}, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "SANDBOX_AUTH_JWT_SECRET_PATHS") {
		t.Fatalf("expected missing jwt secret path error, got %v", err)
	}
}

func TestValidateTransportConfigRequiresTLSPairAndHTTPSProxyHost(t *testing.T) {
	err := validateTransportConfig(Config{TLSCertPath: "/tmp/cert.pem"}, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "both SANDBOX_TLS_CERT_PATH and SANDBOX_TLS_KEY_PATH") {
		t.Fatalf("expected tls pair error, got %v", err)
	}
	err = validateTransportConfig(Config{TrustedProxyHeaders: true, OperatorHost: "http://sandbox.invalid"}, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "https://") {
		t.Fatalf("expected https operator host error, got %v", err)
	}
}

func TestValidateProductionModeRejectsUnsafeSettings(t *testing.T) {
	cfg := Config{
		DeploymentMode:       "production",
		ListenAddress:        ":8080",
		DatabasePath:         "/tmp/test.db",
		StorageRoot:          t.TempDir(),
		SnapshotRoot:         t.TempDir(),
		BaseImageRef:         "alpine:3.20",
		RuntimeBackend:       "docker",
		TrustedDockerRuntime: true,
		AuthMode:             "static",
		DefaultCPULimit:      model.CPUCores(1),
		DefaultQuota: model.TenantQuota{
			MaxCPUCores: model.CPUCores(4),
		},
		DefaultNetworkMode: model.NetworkModeInternetEnabled,
		OperatorHost:       "http://sandbox.invalid",
		Tenants:            []TenantConfig{{ID: "tenant-a", Name: "Tenant A", Token: "token-a"}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "VM-backed runtime class") || !strings.Contains(err.Error(), "SANDBOX_AUTH_MODE=jwt-hs256") || !strings.Contains(err.Error(), "SANDBOX_TRUST_PROXY_HEADERS=true") {
		t.Fatalf("expected production mode validation errors, got %v", err)
	}
}

func TestValidateAuthConfigJWTAcceptsReadableSecrets(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "jwt.secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := validateAuthConfig(Config{
		AuthMode:           "jwt-hs256",
		AuthJWTIssuer:      "issuer.example",
		AuthJWTAudience:    "sandbox-api",
		AuthJWTSecretPaths: []string{secret},
	}, requireReadableFile)
	if err != nil {
		t.Fatalf("expected jwt config to validate, got %v", err)
	}
}

func TestLoadParsesFractionalCPUDefaultsAndQuota(t *testing.T) {
	t.Setenv("SANDBOX_RUNTIME", "docker")
	t.Setenv("SANDBOX_TRUSTED_DOCKER_RUNTIME", "true")
	t.Setenv("SANDBOX_DEFAULT_CPU", "1.5")
	t.Setenv("SANDBOX_QUOTA_MAX_CPU", "2500m")
	t.Setenv("SANDBOX_TOKENS", "token-a=tenant-a")
	t.Setenv("SANDBOX_POLICY_ALLOWED_IMAGES", "ghcr.io/acme/*,alpine:3.20")
	t.Setenv("SANDBOX_POLICY_ALLOW_PUBLIC_TUNNELS", "true")
	t.Setenv("SANDBOX_POLICY_MAX_SANDBOX_LIFETIME", "24h")
	t.Setenv("SANDBOX_POLICY_MAX_IDLE_TIMEOUT", "90m")

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
	if len(cfg.PolicyAllowedImages) != 2 || cfg.PolicyAllowedImages[0] != "ghcr.io/acme/*" || cfg.PolicyAllowedImages[1] != "alpine:3.20" {
		t.Fatalf("unexpected policy allowed images %#v", cfg.PolicyAllowedImages)
	}
	if !cfg.PolicyAllowPublicTunnels {
		t.Fatal("expected public tunnels policy to be enabled")
	}
	if cfg.PolicyMaxSandboxLifetime != 24*time.Hour {
		t.Fatalf("unexpected sandbox lifetime policy %v", cfg.PolicyMaxSandboxLifetime)
	}
	if cfg.PolicyMaxIdleTimeout != 90*time.Minute {
		t.Fatalf("unexpected idle timeout policy %v", cfg.PolicyMaxIdleTimeout)
	}
	if cfg.BaseImageRef != "alpine:3.20" {
		t.Fatalf("expected lightweight default base image, got %q", cfg.BaseImageRef)
	}
}

func TestLoadParsesAdmissionControls(t *testing.T) {
	t.Setenv("SANDBOX_RUNTIME", "docker")
	t.Setenv("SANDBOX_TRUSTED_DOCKER_RUNTIME", "true")
	t.Setenv("SANDBOX_TOKENS", "token-a=tenant-a")
	t.Setenv("SANDBOX_ADMISSION_MAX_NODE_SANDBOXES", "12")
	t.Setenv("SANDBOX_ADMISSION_MAX_NODE_RUNNING", "6")
	t.Setenv("SANDBOX_ADMISSION_MAX_NODE_CPU", "3500m")
	t.Setenv("SANDBOX_ADMISSION_MAX_NODE_MEMORY_MB", "8192")
	t.Setenv("SANDBOX_ADMISSION_MIN_NODE_FREE_STORAGE_MB", "2048")
	t.Setenv("SANDBOX_ADMISSION_MAX_TENANT_STARTS", "2")
	t.Setenv("SANDBOX_ADMISSION_MAX_TENANT_HEAVY_OPS", "3")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.AdmissionMaxNodeSandboxes != 12 || cfg.AdmissionMaxNodeRunning != 6 {
		t.Fatalf("unexpected node sandbox admission controls: %+v", cfg)
	}
	if cfg.AdmissionMaxNodeCPU != model.MustParseCPUQuantity("3500m") {
		t.Fatalf("unexpected admission node cpu %v", cfg.AdmissionMaxNodeCPU)
	}
	if cfg.AdmissionMaxNodeMemoryMB != 8192 || cfg.AdmissionMinNodeFreeStorageMB != 2048 {
		t.Fatalf("unexpected memory/storage admission controls: %+v", cfg)
	}
	if cfg.AdmissionMaxTenantStarts != 2 || cfg.AdmissionMaxTenantHeavyOps != 3 {
		t.Fatalf("unexpected tenant admission controls: %+v", cfg)
	}
}

func TestValidateRejectsNegativeAdmissionControls(t *testing.T) {
	cfg := Config{
		ListenAddress:        ":8080",
		DatabasePath:         "/tmp/test.db",
		StorageRoot:          t.TempDir(),
		SnapshotRoot:         t.TempDir(),
		BaseImageRef:         "alpine:3.20",
		RuntimeBackend:       "docker",
		TrustedDockerRuntime: true,
		AuthMode:             "static",
		DefaultCPULimit:      model.CPUCores(1),
		DefaultQuota: model.TenantQuota{
			MaxCPUCores: model.CPUCores(4),
		},
		DefaultNetworkMode:            model.NetworkModeInternetEnabled,
		OperatorHost:                  "http://sandbox.invalid",
		Tenants:                       []TenantConfig{{ID: "tenant-a", Name: "Tenant A", Token: "token-a"}},
		AdmissionMaxNodeSandboxes:     -1,
		AdmissionMaxNodeRunning:       -1,
		AdmissionMaxNodeCPU:           -1,
		AdmissionMaxNodeMemoryMB:      -1,
		AdmissionMinNodeFreeStorageMB: -1,
		AdmissionMaxTenantStarts:      -1,
		AdmissionMaxTenantHeavyOps:    -1,
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "admission max node sandboxes") || !strings.Contains(err.Error(), "admission max tenant heavy ops") {
		t.Fatalf("expected admission validation errors, got %v", err)
	}
}

func TestValidateRuntimeConfigUsesGenericGuestProfileDefaults(t *testing.T) {
	cfg := Config{
		RuntimeBackend:        "qemu",
		QEMUBinary:            "qemu-system-x86_64",
		QEMUAccel:             "kvm",
		QEMUBaseImagePath:     "/images/base.qcow2",
		QEMUControlMode:       model.GuestControlModeAgent,
		AllowedGuestProfiles:  []model.GuestProfile{model.GuestProfileCore, model.GuestProfileDebug},
		QEMUBootTimeout:       time.Minute,
		QEMUSSHUser:           "sandbox",
		QEMUSSHPrivateKeyPath: "/keys/id_ed25519",
		QEMUSSHHostKeyPath:    "/keys/guest_host_ed25519.pub",
	}
	probe := runtimeValidationProbe{
		goos:          "linux",
		commandExists: func(string) error { return nil },
		fileReadable:  func(string) error { return nil },
		kvmAvailable:  func() error { return nil },
		hvfAvailable:  func() error { return nil },
	}
	if err := validateRuntimeConfig(cfg, probe); err != nil {
		t.Fatalf("expected qemu config to accept generic guest profiles, got %v", err)
	}
	if !cfg.IsAllowedGuestProfile("qemu", model.GuestProfileDebug) {
		t.Fatal("expected debug profile to be allowed via generic guest profile list")
	}
}

func TestLoadKeepsQEMUProfileFlagsScopedToQEMU(t *testing.T) {
	t.Setenv("SANDBOX_RUNTIME", "docker")
	t.Setenv("SANDBOX_TRUSTED_DOCKER_RUNTIME", "true")
	t.Setenv("SANDBOX_TOKENS", "token-a=tenant-a")

	cfg, err := Load([]string{
		"--qemu-allowed-profiles=core",
		"--qemu-dangerous-profiles=debug",
		"--qemu-allow-dangerous-profiles=true",
	})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.IsAllowedGuestProfile("qemu", model.GuestProfileCore) || cfg.IsAllowedGuestProfile("qemu", model.GuestProfileBrowser) {
		t.Fatalf("expected qemu flag policy to allow only core, got %#v", cfg.QEMUAllowedProfiles)
	}
	if !cfg.IsAllowedGuestProfile("docker", model.GuestProfileBrowser) {
		t.Fatal("expected docker policy to keep default profile allowances")
	}
	if cfg.IsDangerousGuestProfile("qemu", model.GuestProfileContainer) || !cfg.IsDangerousGuestProfile("qemu", model.GuestProfileDebug) {
		t.Fatalf("expected qemu dangerous-profile flag to apply, got %#v", cfg.QEMUDangerousProfiles)
	}
	if cfg.AllowsDangerousGuestProfiles("docker") || !cfg.AllowsDangerousGuestProfiles("qemu") {
		t.Fatal("expected dangerous-profile allow flag to stay scoped to qemu")
	}
}

func TestLoadKeepsLegacyQEMUProfileEnvOutOfDockerPolicy(t *testing.T) {
	t.Setenv("SANDBOX_RUNTIME", "docker")
	t.Setenv("SANDBOX_TRUSTED_DOCKER_RUNTIME", "true")
	t.Setenv("SANDBOX_TOKENS", "token-a=tenant-a")
	t.Setenv("SANDBOX_QEMU_ALLOWED_PROFILES", "core")
	t.Setenv("SANDBOX_QEMU_DANGEROUS_PROFILES", "debug")
	t.Setenv("SANDBOX_QEMU_ALLOW_DANGEROUS_PROFILES", "true")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.IsAllowedGuestProfile("qemu", model.GuestProfileBrowser) {
		t.Fatal("expected legacy qemu env to keep browser blocked for qemu")
	}
	if !cfg.IsAllowedGuestProfile("docker", model.GuestProfileBrowser) {
		t.Fatal("expected legacy qemu env to leave docker profile policy unchanged")
	}
	if cfg.AllowsDangerousGuestProfiles("docker") {
		t.Fatal("expected qemu dangerous-profile env to stay out of docker policy")
	}
	if !cfg.AllowsDangerousGuestProfiles("qemu") {
		t.Fatal("expected qemu dangerous-profile env to apply to qemu")
	}
}

func TestLoadParsesExplicitRuntimeSelections(t *testing.T) {
	root := t.TempDir()
	qemuBinary := filepath.Join(root, "qemu-system-x86_64")
	qemuImage := filepath.Join(root, "base.qcow2")
	if err := os.WriteFile(qemuBinary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(qemuImage, []byte("qcow2"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOX_ENABLED_RUNTIMES", "docker-dev,qemu-professional")
	t.Setenv("SANDBOX_DEFAULT_RUNTIME", "qemu-professional")
	t.Setenv("SANDBOX_TOKENS", "token-a=tenant-a")
	t.Setenv("SANDBOX_QEMU_BINARY", qemuBinary)
	t.Setenv("SANDBOX_QEMU_BASE_IMAGE_PATH", qemuImage)
	t.Setenv("SANDBOX_QEMU_ALLOWED_PROFILES", "core")
	t.Setenv("SANDBOX_QEMU_CONTROL_MODE", "agent")
	t.Setenv("SANDBOX_QEMU_ACCEL", "tcg")
	t.Setenv("SANDBOX_TRUSTED_DOCKER_RUNTIME", "true")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.DefaultRuntimeSelection != model.RuntimeSelectionQEMUProfessional {
		t.Fatalf("default runtime selection = %q", cfg.DefaultRuntimeSelection)
	}
	if len(cfg.EnabledRuntimeSelections) != 2 {
		t.Fatalf("enabled runtime selections = %#v", cfg.EnabledRuntimeSelections)
	}
}

func TestValidateRuntimeConfigRequiresDefaultEnabled(t *testing.T) {
	cfg := Config{
		EnabledRuntimeSelections: []model.RuntimeSelection{model.RuntimeSelectionDockerDev},
		DefaultRuntimeSelection:  model.RuntimeSelectionQEMUProfessional,
		TrustedDockerRuntime:     true,
		DockerUser:               "10001:10001",
		DockerTmpfsSizeMB:        64,
	}
	err := validateRuntimeConfig(cfg, runtimeValidationProbe{})
	if err == nil || !strings.Contains(err.Error(), "must also be enabled") {
		t.Fatalf("expected default-enabled validation error, got %v", err)
	}
}

func TestValidateProductionModeUsesDefaultRuntimeSelection(t *testing.T) {
	cfg := Config{
		DeploymentMode:           "production",
		ListenAddress:            ":8080",
		DatabasePath:             filepath.Join(t.TempDir(), "sandbox.db"),
		StorageRoot:              t.TempDir(),
		SnapshotRoot:             t.TempDir(),
		BaseImageRef:             "alpine:3.20",
		RuntimeBackend:           "docker",
		EnabledRuntimeSelections: []model.RuntimeSelection{model.RuntimeSelectionDockerDev},
		DefaultRuntimeSelection:  model.RuntimeSelectionDockerDev,
		TrustedDockerRuntime:     true,
		AuthMode:                 "jwt-hs256",
		AuthJWTIssuer:            "issuer.example",
		AuthJWTAudience:          "sandbox-api",
		AuthJWTSecretPaths:       []string{filepath.Join(t.TempDir(), "jwt.secret")},
		TrustedProxyHeaders:      true,
		OperatorHost:             "https://sandbox.example",
		DefaultCPULimit:          model.CPUCores(1),
		DefaultQuota:             model.TenantQuota{MaxCPUCores: model.CPUCores(4)},
		DefaultNetworkMode:       model.NetworkModeInternetEnabled,
		Tenants:                  []TenantConfig{{ID: "tenant-a", Name: "Tenant A", Token: "token-a"}},
	}
	if err := os.WriteFile(cfg.AuthJWTSecretPaths[0], []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "VM-backed runtime class") {
		t.Fatalf("expected production validation to reject non-VM default runtime selection, got %v", err)
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
		QEMUControlMode:       model.GuestControlModeAgent,
		QEMUAllowedProfiles:   []model.GuestProfile{model.GuestProfileCore},
		QEMUSSHUser:           "sandbox",
		QEMUSSHPrivateKeyPath: "/keys/id_ed25519",
		QEMUSSHHostKeyPath:    "/keys/guest_host_ed25519.pub",
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

func TestValidateRuntimeConfigQEMUAgentModeDoesNotRequireSSHMaterial(t *testing.T) {
	cfg := Config{
		RuntimeBackend:      "qemu",
		QEMUBinary:          "qemu-system-x86_64",
		QEMUAccel:           "auto",
		QEMUBaseImagePath:   "/images/base.qcow2",
		QEMUControlMode:     model.GuestControlModeAgent,
		QEMUAllowedProfiles: []model.GuestProfile{model.GuestProfileCore},
		QEMUBootTimeout:     time.Minute,
	}
	probe := runtimeValidationProbe{
		goos:          "linux",
		commandExists: func(string) error { return nil },
		fileReadable:  func(string) error { return nil },
		kvmAvailable:  func() error { return nil },
		hvfAvailable:  func() error { return nil },
	}
	if err := validateRuntimeConfig(cfg, probe); err != nil {
		t.Fatalf("expected agent-mode qemu config to validate without ssh material, got %v", err)
	}
}

func TestEffectiveQEMUAllowedBaseImagePathsIncludesDefaultAndDeduplicates(t *testing.T) {
	cfg := Config{
		QEMUBaseImagePath:         "/images/base.qcow2",
		QEMUAllowedBaseImagePaths: []string{"/images/extra.qcow2", "/images/base.qcow2", " /images/extra.qcow2 "},
	}
	got := cfg.EffectiveQEMUAllowedBaseImagePaths()
	if len(got) != 2 || got[0] != "/images/extra.qcow2" || got[1] != "/images/base.qcow2" {
		t.Fatalf("unexpected allowed qemu images %#v", got)
	}
}

func TestProductionModeRejectsNonVMClass(t *testing.T) {
	base := Config{
		ListenAddress:        ":8080",
		DatabasePath:         "/tmp/test.db",
		StorageRoot:          t.TempDir(),
		SnapshotRoot:         t.TempDir(),
		BaseImageRef:         "alpine:3.20",
		TrustedDockerRuntime: true,
		DeploymentMode:       "production",
		AuthMode:             "jwt-hs256",
		AuthJWTIssuer:        "issuer.example",
		AuthJWTAudience:      "sandbox-api",
		TrustedProxyHeaders:  true,
		OperatorHost:         "https://sandbox.example",
		DefaultCPULimit:      model.CPUCores(1),
		DefaultQuota:         model.TenantQuota{MaxCPUCores: model.CPUCores(4)},
		DefaultNetworkMode:   model.NetworkModeInternetEnabled,
		Tenants:              []TenantConfig{{ID: "tenant-a", Name: "Tenant A", Token: "token-a"}},
	}

	// docker backend (trusted-docker class) must fail in production mode
	dockerCfg := base
	dockerCfg.RuntimeBackend = "docker"
	err := dockerCfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "VM-backed runtime class") {
		t.Fatalf("expected production to reject docker (non-VM class), got %v", err)
	}
}

func TestLoadAppliesProductionDeploymentProfileDefaults(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "jwt.secret")
	image := filepath.Join(root, "base.qcow2")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("qcow2"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOX_DEPLOYMENT_PROFILE", "production-qemu-browser")
	t.Setenv("SANDBOX_AUTH_MODE", "jwt-hs256")
	t.Setenv("SANDBOX_AUTH_JWT_ISSUER", "issuer.example")
	t.Setenv("SANDBOX_AUTH_JWT_AUDIENCE", "sandbox-api")
	t.Setenv("SANDBOX_AUTH_JWT_SECRET_PATHS", secret)
	t.Setenv("SANDBOX_TRUST_PROXY_HEADERS", "true")
	t.Setenv("SANDBOX_OPERATOR_HOST", "https://sandbox.example")
	t.Setenv("SANDBOX_QEMU_BINARY", trueBinary(t))
	t.Setenv("SANDBOX_QEMU_BASE_IMAGE_PATH", image)
	t.Setenv("SANDBOX_QEMU_ACCEL", "tcg")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.DefaultRuntimeSelection != model.RuntimeSelectionQEMUProfessional {
		t.Fatalf("default runtime selection = %q", cfg.DefaultRuntimeSelection)
	}
	if !cfg.IsAllowedGuestProfile("qemu", model.GuestProfileBrowser) {
		t.Fatal("expected browser profile to be enabled by production-qemu-browser")
	}
	if cfg.IsAllowedGuestProfile("qemu", model.GuestProfileContainer) {
		t.Fatal("expected dangerous container profile to remain blocked")
	}
}

func TestLoadDefaultsProductionModeToQEMU(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "jwt.secret")
	image := filepath.Join(root, "base.qcow2")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("qcow2"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOX_MODE", "production")
	t.Setenv("SANDBOX_AUTH_MODE", "jwt-hs256")
	t.Setenv("SANDBOX_AUTH_JWT_ISSUER", "issuer.example")
	t.Setenv("SANDBOX_AUTH_JWT_AUDIENCE", "sandbox-api")
	t.Setenv("SANDBOX_AUTH_JWT_SECRET_PATHS", secret)
	t.Setenv("SANDBOX_TRUST_PROXY_HEADERS", "true")
	t.Setenv("SANDBOX_OPERATOR_HOST", "https://sandbox.example")
	t.Setenv("SANDBOX_QEMU_BINARY", trueBinary(t))
	t.Setenv("SANDBOX_QEMU_BASE_IMAGE_PATH", image)
	t.Setenv("SANDBOX_QEMU_ACCEL", "tcg")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RuntimeBackend != "qemu" {
		t.Fatalf("runtime backend = %q, want qemu", cfg.RuntimeBackend)
	}
	if cfg.DefaultRuntimeSelection != model.RuntimeSelectionQEMUProfessional {
		t.Fatalf("default runtime selection = %q", cfg.DefaultRuntimeSelection)
	}
}

func TestLoadAllowsExceptionContainerDeploymentProfile(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "jwt.secret")
	image := filepath.Join(root, "base.qcow2")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("qcow2"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOX_DEPLOYMENT_PROFILE", "exception-container")
	t.Setenv("SANDBOX_AUTH_MODE", "jwt-hs256")
	t.Setenv("SANDBOX_AUTH_JWT_ISSUER", "issuer.example")
	t.Setenv("SANDBOX_AUTH_JWT_AUDIENCE", "sandbox-api")
	t.Setenv("SANDBOX_AUTH_JWT_SECRET_PATHS", secret)
	t.Setenv("SANDBOX_TRUST_PROXY_HEADERS", "true")
	t.Setenv("SANDBOX_OPERATOR_HOST", "https://sandbox.example")
	t.Setenv("SANDBOX_QEMU_BINARY", trueBinary(t))
	t.Setenv("SANDBOX_QEMU_BASE_IMAGE_PATH", image)
	t.Setenv("SANDBOX_QEMU_ACCEL", "tcg")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.IsAllowedGuestProfile("qemu", model.GuestProfileContainer) {
		t.Fatal("expected exception-container profile to allow container")
	}
	if !cfg.AllowsDangerousGuestProfiles("qemu") {
		t.Fatal("expected exception-container profile to enable dangerous qemu profiles")
	}
}

func TestValidateProductionTransportModes(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "jwt.secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := Config{
		DeploymentMode:           "production",
		ListenAddress:            ":8080",
		DatabasePath:             filepath.Join(t.TempDir(), "sandbox.db"),
		StorageRoot:              t.TempDir(),
		SnapshotRoot:             t.TempDir(),
		RuntimeBackend:           "qemu",
		EnabledRuntimeSelections: []model.RuntimeSelection{model.RuntimeSelectionQEMUProfessional},
		DefaultRuntimeSelection:  model.RuntimeSelectionQEMUProfessional,
		BaseImageRef:             "guest-base.qcow2",
		AuthMode:                 "jwt-hs256",
		AuthJWTIssuer:            "issuer.example",
		AuthJWTAudience:          "sandbox-api",
		AuthJWTSecretPaths:       []string{secret},
		DefaultCPULimit:          model.CPUCores(1),
		DefaultQuota:             model.TenantQuota{MaxCPUCores: model.CPUCores(4)},
		DefaultNetworkMode:       model.NetworkModeInternetEnabled,
		QEMUBinary:               "qemu-system-x86_64",
		QEMUAccel:                "tcg",
		QEMUBaseImagePath:        "/images/base.qcow2",
		QEMUAllowedProfiles:      []model.GuestProfile{model.GuestProfileCore},
		QEMUControlMode:          model.GuestControlModeAgent,
		QEMUBootTimeout:          time.Minute,
	}
	probe := runtimeValidationProbe{
		goos:          "linux",
		commandExists: func(string) error { return nil },
		fileReadable:  func(string) error { return nil },
		kvmAvailable:  func() error { return nil },
		hvfAvailable:  func() error { return nil },
	}
	if err := validateRuntimeConfig(base, probe); err != nil {
		t.Fatalf("validate runtime config: %v", err)
	}

	direct := base
	direct.ProductionTransportMode = "direct-tls"
	if err := direct.Validate(); err == nil || !strings.Contains(err.Error(), "direct-tls") {
		t.Fatalf("expected direct-tls validation error, got %v", err)
	}

	proxy := base
	proxy.ProductionTransportMode = "terminated-proxy"
	proxy.TrustedProxyHeaders = true
	proxy.OperatorHost = "https://sandbox.example"
	if err := validateTransportConfig(proxy, func(string) error { return nil }); err != nil {
		t.Fatalf("expected terminated-proxy transport validation to pass, got %v", err)
	}
}

func TestRuntimeClassMethodDerivesFromBackend(t *testing.T) {
	tests := []struct {
		backend string
		want    string
	}{
		{"docker", "trusted-docker"},
		{"qemu", "vm"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		cfg := Config{RuntimeBackend: tt.backend}
		got := string(cfg.RuntimeClass())
		if got != tt.want {
			t.Errorf("backend %q: RuntimeClass() = %q, want %q", tt.backend, got, tt.want)
		}
	}
}

type errString string

func (e errString) Error() string {
	return string(e)
}

func trueBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("true")
	if err != nil {
		t.Fatalf("look up true binary: %v", err)
	}
	return path
}
