package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"or3-sandbox/internal/model"
)

type TenantConfig struct {
	ID    string
	Name  string
	Token string
}

type Config struct {
	DeploymentMode           string
	ListenAddress            string
	DatabasePath             string
	StorageRoot              string
	SnapshotRoot             string
	BaseImageRef             string
	RuntimeBackend           string
	AuthMode                 string
	AuthJWTIssuer            string
	AuthJWTAudience          string
	AuthJWTSecretPaths       []string
	TLSCertPath              string
	TLSKeyPath               string
	TrustedProxyHeaders      bool
	TrustedDockerRuntime     bool
	PolicyAllowedImages      []string
	PolicyAllowPublicTunnels bool
	PolicyMaxSandboxLifetime time.Duration
	PolicyMaxIdleTimeout     time.Duration
	DefaultCPULimit          model.CPUQuantity
	DefaultMemoryLimitMB     int
	DefaultPIDsLimit         int
	DefaultDiskLimitMB       int
	DefaultNetworkMode       model.NetworkMode
	DefaultAllowTunnels      bool
	RequestRatePerMinute     int
	RequestBurst             int
	DefaultQuota             model.TenantQuota
	GracefulShutdown         time.Duration
	ReconcileInterval        time.Duration
	CleanupInterval          time.Duration
	OperatorHost             string
	TunnelSigningKey         string
	TunnelSigningKeyPath     string
	Tenants                  []TenantConfig
	OptionalSnapshotExport   string
	QEMUBinary               string
	QEMUAccel                string
	QEMUBaseImagePath        string
	QEMUSSHUser              string
	QEMUSSHPrivateKeyPath    string
	QEMUBootTimeout          time.Duration
}

func Load(args []string) (Config, error) {
	fs := flag.NewFlagSet("sandboxd", flag.ContinueOnError)
	cfg := Config{}
	fs.StringVar(&cfg.DeploymentMode, "mode", env("SANDBOX_MODE", "development"), "deployment mode")
	fs.StringVar(&cfg.ListenAddress, "listen", env("SANDBOX_LISTEN", ":8080"), "HTTP listen address")
	fs.StringVar(&cfg.DatabasePath, "db", env("SANDBOX_DB_PATH", "./data/sandbox.db"), "SQLite path")
	fs.StringVar(&cfg.StorageRoot, "storage-root", env("SANDBOX_STORAGE_ROOT", "./data/storage"), "storage root")
	fs.StringVar(&cfg.SnapshotRoot, "snapshot-root", env("SANDBOX_SNAPSHOT_ROOT", "./data/snapshots"), "snapshot root")
	fs.StringVar(&cfg.BaseImageRef, "base-image", env("SANDBOX_BASE_IMAGE", "mcr.microsoft.com/playwright:v1.51.1-noble"), "default base image")
	fs.StringVar(&cfg.RuntimeBackend, "runtime", env("SANDBOX_RUNTIME", "docker"), "runtime backend")
	fs.StringVar(&cfg.AuthMode, "auth-mode", env("SANDBOX_AUTH_MODE", "static"), "auth mode")
	fs.StringVar(&cfg.AuthJWTIssuer, "auth-jwt-issuer", env("SANDBOX_AUTH_JWT_ISSUER", ""), "jwt issuer")
	fs.StringVar(&cfg.AuthJWTAudience, "auth-jwt-audience", env("SANDBOX_AUTH_JWT_AUDIENCE", ""), "jwt audience")
	authJWTSecretPaths := env("SANDBOX_AUTH_JWT_SECRET_PATHS", "")
	fs.StringVar(&authJWTSecretPaths, "auth-jwt-secret-paths", authJWTSecretPaths, "comma-separated jwt secret file paths")
	fs.StringVar(&cfg.TLSCertPath, "tls-cert", env("SANDBOX_TLS_CERT_PATH", ""), "tls certificate path")
	fs.StringVar(&cfg.TLSKeyPath, "tls-key", env("SANDBOX_TLS_KEY_PATH", ""), "tls private key path")
	policyAllowedImages := env("SANDBOX_POLICY_ALLOWED_IMAGES", "")
	fs.StringVar(&policyAllowedImages, "policy-allowed-images", policyAllowedImages, "comma-separated allowed image references or prefixes ending with *")
	policyAllowPublicTunnels := strings.EqualFold(env("SANDBOX_POLICY_ALLOW_PUBLIC_TUNNELS", "false"), "true")
	fs.BoolVar(&policyAllowPublicTunnels, "policy-allow-public-tunnels", policyAllowPublicTunnels, "allow public tunnels")
	fs.DurationVar(&cfg.PolicyMaxSandboxLifetime, "policy-max-sandbox-lifetime", envDuration("SANDBOX_POLICY_MAX_SANDBOX_LIFETIME", 0), "maximum sandbox lifetime before policy denial; 0 disables")
	fs.DurationVar(&cfg.PolicyMaxIdleTimeout, "policy-max-idle-timeout", envDuration("SANDBOX_POLICY_MAX_IDLE_TIMEOUT", 0), "maximum sandbox idle time before policy denial; 0 disables")
	fs.StringVar(&cfg.QEMUBinary, "qemu-binary", env("SANDBOX_QEMU_BINARY", defaultQEMUBinary()), "qemu system binary")
	fs.StringVar(&cfg.QEMUAccel, "qemu-accel", env("SANDBOX_QEMU_ACCEL", "auto"), "qemu accelerator selection")
	fs.StringVar(&cfg.QEMUBaseImagePath, "qemu-base-image-path", env("SANDBOX_QEMU_BASE_IMAGE_PATH", ""), "qemu base guest image path")
	fs.StringVar(&cfg.QEMUSSHUser, "qemu-ssh-user", env("SANDBOX_QEMU_SSH_USER", ""), "qemu guest ssh user")
	fs.StringVar(&cfg.QEMUSSHPrivateKeyPath, "qemu-ssh-private-key", env("SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH", ""), "qemu guest ssh private key path")
	trustedDockerRuntime := env("SANDBOX_TRUSTED_DOCKER_RUNTIME", "false")
	trustedProxyHeaders := strings.EqualFold(env("SANDBOX_TRUST_PROXY_HEADERS", "false"), "true")
	fs.BoolVar(&trustedProxyHeaders, "trust-proxy-headers", trustedProxyHeaders, "trust reverse-proxy tls headers")
	defaultCPU := env("SANDBOX_DEFAULT_CPU", "2")
	fs.StringVar(&defaultCPU, "default-cpu", defaultCPU, "default cpu limit")
	fs.IntVar(&cfg.DefaultMemoryLimitMB, "default-memory-mb", envInt("SANDBOX_DEFAULT_MEMORY_MB", 2048), "default memory limit")
	fs.IntVar(&cfg.DefaultPIDsLimit, "default-pids", envInt("SANDBOX_DEFAULT_PIDS", 512), "default pids limit")
	fs.IntVar(&cfg.DefaultDiskLimitMB, "default-disk-mb", envInt("SANDBOX_DEFAULT_DISK_MB", 10240), "default disk limit")
	fs.IntVar(&cfg.RequestRatePerMinute, "rate-limit", envInt("SANDBOX_RATE_LIMIT_PER_MIN", 120), "per-tenant requests per minute")
	fs.IntVar(&cfg.RequestBurst, "rate-burst", envInt("SANDBOX_RATE_LIMIT_BURST", 30), "per-tenant burst")
	fs.DurationVar(&cfg.GracefulShutdown, "shutdown-timeout", envDuration("SANDBOX_SHUTDOWN_TIMEOUT", 15*time.Second), "graceful shutdown timeout")
	fs.DurationVar(&cfg.ReconcileInterval, "reconcile-interval", envDuration("SANDBOX_RECONCILE_INTERVAL", 30*time.Second), "reconcile interval")
	fs.DurationVar(&cfg.CleanupInterval, "cleanup-interval", envDuration("SANDBOX_CLEANUP_INTERVAL", 5*time.Minute), "cleanup interval")
	fs.DurationVar(&cfg.QEMUBootTimeout, "qemu-boot-timeout", envDuration("SANDBOX_QEMU_BOOT_TIMEOUT", 2*time.Minute), "qemu guest boot timeout")
	fs.StringVar(&cfg.OperatorHost, "operator-host", env("SANDBOX_OPERATOR_HOST", "http://127.0.0.1:8080"), "public control plane host")
	fs.StringVar(&cfg.TunnelSigningKey, "tunnel-signing-key", env("SANDBOX_TUNNEL_SIGNING_KEY", ""), "shared secret for tunnel signed URLs and browser bootstrap cookies")
	fs.StringVar(&cfg.TunnelSigningKeyPath, "tunnel-signing-key-path", env("SANDBOX_TUNNEL_SIGNING_KEY_PATH", ""), "path to shared secret for tunnel signed URLs and browser bootstrap cookies")
	networkMode := env("SANDBOX_DEFAULT_NETWORK_MODE", string(model.NetworkModeInternetEnabled))
	allowTunnels := env("SANDBOX_DEFAULT_ALLOW_TUNNELS", "true")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	defaultCPULimit, err := model.ParseCPUQuantity(defaultCPU)
	if err != nil {
		return Config{}, fmt.Errorf("parse default cpu: %w", err)
	}
	cfg.DefaultCPULimit = defaultCPULimit
	maxCPUCores, err := model.ParseCPUQuantity(env("SANDBOX_QUOTA_MAX_CPU", "16"))
	if err != nil {
		return Config{}, fmt.Errorf("parse max cpu quota: %w", err)
	}
	cfg.DefaultNetworkMode = model.NetworkMode(networkMode)
	cfg.DefaultAllowTunnels = strings.EqualFold(allowTunnels, "true")
	cfg.TrustedDockerRuntime = strings.EqualFold(trustedDockerRuntime, "true")
	cfg.TrustedProxyHeaders = trustedProxyHeaders
	cfg.AuthJWTSecretPaths = parseCommaSeparated(authJWTSecretPaths)
	cfg.PolicyAllowedImages = parseCommaSeparated(policyAllowedImages)
	cfg.PolicyAllowPublicTunnels = policyAllowPublicTunnels
	cfg.OptionalSnapshotExport = env("SANDBOX_S3_EXPORT_URI", "")
	cfg.DefaultQuota = model.TenantQuota{
		MaxSandboxes:            envInt("SANDBOX_QUOTA_MAX_SANDBOXES", 10),
		MaxRunningSandboxes:     envInt("SANDBOX_QUOTA_MAX_RUNNING", 5),
		MaxConcurrentExecs:      envInt("SANDBOX_QUOTA_MAX_EXECS", 8),
		MaxTunnels:              envInt("SANDBOX_QUOTA_MAX_TUNNELS", 8),
		MaxCPUCores:             maxCPUCores,
		MaxMemoryMB:             envInt("SANDBOX_QUOTA_MAX_MEMORY_MB", 16384),
		MaxStorageMB:            envInt("SANDBOX_QUOTA_MAX_STORAGE_MB", 51200),
		AllowTunnels:            strings.EqualFold(env("SANDBOX_QUOTA_ALLOW_TUNNELS", "true"), "true"),
		DefaultTunnelAuthMode:   env("SANDBOX_DEFAULT_TUNNEL_AUTH", "token"),
		DefaultTunnelVisibility: env("SANDBOX_DEFAULT_TUNNEL_VISIBILITY", "private"),
	}
	cfg.Tenants = parseTenants(env("SANDBOX_TOKENS", "dev-token=tenant-dev"))
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	var problems []string
	if c.DeploymentMode != "development" && c.DeploymentMode != "production" {
		problems = append(problems, fmt.Sprintf("unsupported deployment mode %q", c.DeploymentMode))
	}
	if c.ListenAddress == "" {
		problems = append(problems, "listen address is required")
	}
	if c.DatabasePath == "" {
		problems = append(problems, "database path is required")
	}
	if c.StorageRoot == "" {
		problems = append(problems, "storage root is required")
	}
	if c.SnapshotRoot == "" {
		problems = append(problems, "snapshot root is required")
	}
	if c.BaseImageRef == "" {
		problems = append(problems, "base image reference is required")
	}
	if c.PolicyMaxSandboxLifetime < 0 {
		problems = append(problems, "policy max sandbox lifetime must be zero or positive")
	}
	if c.PolicyMaxIdleTimeout < 0 {
		problems = append(problems, "policy max idle timeout must be zero or positive")
	}
	if err := validateAuthConfig(c, requireReadableFile); err != nil {
		problems = append(problems, err.Error())
	}
	if err := validateTransportConfig(c, requireReadableFile); err != nil {
		problems = append(problems, err.Error())
	}
	if err := validateRuntimeConfig(c, defaultRuntimeValidationProbe()); err != nil {
		problems = append(problems, err.Error())
	}
	if c.DefaultCPULimit <= 0 {
		problems = append(problems, "default cpu limit must be positive")
	}
	if c.DefaultQuota.MaxCPUCores <= 0 {
		problems = append(problems, "default quota max cpu must be positive")
	}
	if c.RuntimeBackend == "qemu" && c.DefaultCPULimit.MilliValue()%1000 != 0 {
		problems = append(problems, "qemu runtime requires a whole-core default cpu limit")
	}
	if c.DefaultNetworkMode != model.NetworkModeInternetEnabled && c.DefaultNetworkMode != model.NetworkModeInternetDisabled {
		problems = append(problems, fmt.Sprintf("unsupported default network mode %q", c.DefaultNetworkMode))
	}
	if c.AuthMode == "static" && len(c.Tenants) == 0 {
		problems = append(problems, "at least one tenant token is required")
	}
	if c.DeploymentMode == "production" {
		if c.RuntimeBackend != "qemu" {
			problems = append(problems, "production mode requires SANDBOX_RUNTIME=qemu")
		}
		if c.AuthMode == "static" {
			problems = append(problems, "production mode requires SANDBOX_AUTH_MODE=jwt-hs256")
		}
		if c.TLSCertPath == "" && !c.TrustedProxyHeaders {
			problems = append(problems, "production mode requires TLS certificate paths or SANDBOX_TRUST_PROXY_HEADERS=true")
		}
	}
	for _, dir := range []string{filepath.Dir(c.DatabasePath), c.StorageRoot, c.SnapshotRoot} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			problems = append(problems, fmt.Sprintf("create %s: %v", dir, err))
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validateAuthConfig(c Config, fileReadable func(string) error) error {
	switch c.AuthMode {
	case "static":
		return nil
	case "jwt-hs256":
		var problems []string
		if strings.TrimSpace(c.AuthJWTIssuer) == "" {
			problems = append(problems, "jwt auth requires SANDBOX_AUTH_JWT_ISSUER")
		}
		if strings.TrimSpace(c.AuthJWTAudience) == "" {
			problems = append(problems, "jwt auth requires SANDBOX_AUTH_JWT_AUDIENCE")
		}
		if len(c.AuthJWTSecretPaths) == 0 {
			problems = append(problems, "jwt auth requires SANDBOX_AUTH_JWT_SECRET_PATHS")
		}
		for _, path := range c.AuthJWTSecretPaths {
			if err := fileReadable(path); err != nil {
				problems = append(problems, fmt.Sprintf("jwt auth secret path is not readable: %v", err))
			}
		}
		if len(problems) > 0 {
			return errors.New(strings.Join(problems, "; "))
		}
		return nil
	default:
		return fmt.Errorf("unsupported auth mode %q", c.AuthMode)
	}
}

func validateTransportConfig(c Config, fileReadable func(string) error) error {
	hasCert := strings.TrimSpace(c.TLSCertPath) != ""
	hasKey := strings.TrimSpace(c.TLSKeyPath) != ""
	if hasCert != hasKey {
		return errors.New("tls requires both SANDBOX_TLS_CERT_PATH and SANDBOX_TLS_KEY_PATH")
	}
	var problems []string
	if strings.TrimSpace(c.TunnelSigningKey) != "" && strings.TrimSpace(c.TunnelSigningKeyPath) != "" {
		problems = append(problems, "set only one of SANDBOX_TUNNEL_SIGNING_KEY or SANDBOX_TUNNEL_SIGNING_KEY_PATH")
	}
	if strings.TrimSpace(c.TunnelSigningKeyPath) != "" {
		if err := fileReadable(c.TunnelSigningKeyPath); err != nil {
			problems = append(problems, fmt.Sprintf("tunnel signing key path is not readable: %v", err))
		}
	}
	if hasCert {
		if err := fileReadable(c.TLSCertPath); err != nil {
			problems = append(problems, fmt.Sprintf("tls certificate path is not readable: %v", err))
		}
		if err := fileReadable(c.TLSKeyPath); err != nil {
			problems = append(problems, fmt.Sprintf("tls key path is not readable: %v", err))
		}
	}
	if c.TrustedProxyHeaders && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.OperatorHost)), "https://") {
		problems = append(problems, "trusted proxy mode requires SANDBOX_OPERATOR_HOST to use https://")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

type runtimeValidationProbe struct {
	goos          string
	commandExists func(string) error
	fileReadable  func(string) error
	kvmAvailable  func() error
	hvfAvailable  func() error
}

func defaultRuntimeValidationProbe() runtimeValidationProbe {
	return runtimeValidationProbe{
		goos:          goruntime.GOOS,
		commandExists: requireCommand,
		fileReadable:  requireReadableFile,
		kvmAvailable:  requireKVM,
		hvfAvailable:  requireHVF,
	}
}

func validateRuntimeConfig(c Config, probe runtimeValidationProbe) error {
	switch c.RuntimeBackend {
	case "docker":
		if !c.TrustedDockerRuntime {
			return errors.New("docker runtime requires SANDBOX_TRUSTED_DOCKER_RUNTIME=true because it is shared-kernel and not a production multi-tenant boundary")
		}
		return nil
	case "qemu":
		return validateQEMUConfig(c, probe)
	default:
		return fmt.Errorf("unsupported runtime backend %q", c.RuntimeBackend)
	}
}

func validateQEMUConfig(c Config, probe runtimeValidationProbe) error {
	accel, err := resolveQEMUAccel(c.QEMUAccel, probe.goos)
	if err != nil {
		return err
	}
	if c.QEMUBootTimeout <= 0 {
		return errors.New("qemu runtime requires SANDBOX_QEMU_BOOT_TIMEOUT to be a positive duration")
	}
	if strings.TrimSpace(c.QEMUBaseImagePath) == "" {
		return errors.New("qemu runtime requires SANDBOX_QEMU_BASE_IMAGE_PATH")
	}
	if strings.TrimSpace(c.QEMUSSHUser) == "" {
		return errors.New("qemu runtime requires SANDBOX_QEMU_SSH_USER")
	}
	if strings.TrimSpace(c.QEMUSSHPrivateKeyPath) == "" {
		return errors.New("qemu runtime requires SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH")
	}
	if err := probe.commandExists(c.QEMUBinary); err != nil {
		return fmt.Errorf("qemu runtime requires a working QEMU binary: %w", err)
	}
	if err := probe.fileReadable(c.QEMUBaseImagePath); err != nil {
		return fmt.Errorf("qemu runtime base image path is not readable: %w", err)
	}
	if err := probe.fileReadable(c.QEMUSSHPrivateKeyPath); err != nil {
		return fmt.Errorf("qemu runtime ssh private key is not readable: %w", err)
	}
	switch accel {
	case "kvm":
		if err := probe.kvmAvailable(); err != nil {
			return fmt.Errorf("qemu runtime requires KVM support on Linux hosts: %w", err)
		}
	case "hvf":
		if err := probe.hvfAvailable(); err != nil {
			return fmt.Errorf("qemu runtime requires HVF support on macOS hosts: %w", err)
		}
	}
	return nil
}

func resolveQEMUAccel(value, goos string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		switch goos {
		case "linux":
			return "kvm", nil
		case "darwin":
			return "hvf", nil
		default:
			return "", fmt.Errorf("qemu runtime is unsupported on host OS %q", goos)
		}
	case "kvm":
		if goos != "linux" {
			return "", fmt.Errorf("qemu accel %q is unsupported on host OS %q", value, goos)
		}
		return "kvm", nil
	case "hvf":
		if goos != "darwin" {
			return "", fmt.Errorf("qemu accel %q is unsupported on host OS %q", value, goos)
		}
		return "hvf", nil
	default:
		return "", fmt.Errorf("unsupported qemu accelerator %q", value)
	}
}

func defaultQEMUBinary() string {
	switch goruntime.GOARCH {
	case "arm64":
		return "qemu-system-aarch64"
	default:
		return "qemu-system-x86_64"
	}
}

func requireCommand(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("command path is empty")
	}
	if filepath.IsAbs(name) || strings.ContainsRune(name, os.PathSeparator) {
		return requireReadableFile(name)
	}
	_, err := exec.LookPath(name)
	return err
}

func requireReadableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func requireKVM() error {
	return requireReadableFile("/dev/kvm")
}

func requireHVF() error {
	output, err := exec.Command("sysctl", "-n", "kern.hv_support").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	if strings.TrimSpace(string(output)) != "1" {
		return fmt.Errorf("kern.hv_support=%s", strings.TrimSpace(string(output)))
	}
	return nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.Atoi(value)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		parsed, err := time.ParseDuration(value)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func parseTenants(raw string) []TenantConfig {
	var tenants []TenantConfig
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, "=")
		if len(parts) != 2 {
			continue
		}
		token := strings.TrimSpace(parts[0])
		tenantID := strings.TrimSpace(parts[1])
		if token == "" || tenantID == "" {
			continue
		}
		tenants = append(tenants, TenantConfig{
			ID:    tenantID,
			Name:  tenantID,
			Token: token,
		})
	}
	return tenants
}

func parseCommaSeparated(raw string) []string {
	var values []string
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		values = append(values, entry)
	}
	return values
}
