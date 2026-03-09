package config

import (
	"os"
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
	if err := validateRuntimeConfig(cfg, runtimeValidationProbe{}); err != nil {
		t.Fatalf("expected docker config to validate, got %v", err)
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

func TestValidateRuntimeConfigRejectsUnsupportedBackend(t *testing.T) {
	cfg := Config{RuntimeBackend: "podman"}
	err := validateRuntimeConfig(cfg, runtimeValidationProbe{})
	if err == nil || !strings.Contains(err.Error(), "unsupported runtime backend") {
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
