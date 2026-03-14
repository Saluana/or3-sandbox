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
	DeploymentMode                  string
	DeploymentProfile               string
	ProductionTransportMode         string
	ProductionAllowDockerBreakglass bool
	ListenAddress                   string
	DatabasePath                    string
	StorageRoot                     string
	SnapshotRoot                    string
	BaseImageRef                    string
	RuntimeBackend                  string
	EnabledRuntimeSelections        []model.RuntimeSelection
	DefaultRuntimeSelection         model.RuntimeSelection
	AuthMode                        string
	AuthJWTIssuer                   string
	AuthJWTAudience                 string
	AuthJWTSecretPaths              []string
	TLSCertPath                     string
	TLSKeyPath                      string
	TrustedProxyHeaders             bool
	TrustedDockerRuntime            bool
	PolicyAllowedImages             []string
	PolicyAllowPublicTunnels        bool
	PolicyMaxSandboxLifetime        time.Duration
	PolicyMaxIdleTimeout            time.Duration
	AdmissionMaxNodeSandboxes       int
	AdmissionMaxNodeRunning         int
	AdmissionMaxNodeCPU             model.CPUQuantity
	AdmissionMaxNodeMemoryMB        int
	AdmissionMinNodeFreeStorageMB   int
	AdmissionMaxTenantStarts        int
	AdmissionMaxTenantHeavyOps      int
	StorageWarningFileCount         int
	WorkspaceFileTransferMaxBytes   int64
	SnapshotMaxBytes                int64
	SnapshotMaxFiles                int
	SnapshotMaxExpansionRatio       int
	AllowedGuestProfiles            []model.GuestProfile
	DangerousGuestProfiles          []model.GuestProfile
	AllowDangerousProfiles          bool
	DockerUser                      string
	DockerTmpfsSizeMB               int
	DockerSeccompProfile            string
	DockerAppArmorProfile           string
	DockerSELinuxLabel              string
	DockerAllowDangerousOverrides   bool
	DefaultCPULimit                 model.CPUQuantity
	DefaultMemoryLimitMB            int
	DefaultPIDsLimit                int
	DefaultDiskLimitMB              int
	DefaultNetworkMode              model.NetworkMode
	DefaultAllowTunnels             bool
	RequestRatePerMinute            int
	RequestBurst                    int
	DefaultQuota                    model.TenantQuota
	GracefulShutdown                time.Duration
	ReconcileInterval               time.Duration
	CleanupInterval                 time.Duration
	OperatorHost                    string
	TunnelSigningKey                string
	TunnelSigningKeyPath            string
	Tenants                         []TenantConfig
	OptionalSnapshotExport          string
	QEMUBinary                      string
	QEMUAccel                       string
	QEMUBaseImagePath               string
	QEMUAllowedBaseImagePaths       []string
	QEMUControlMode                 model.GuestControlMode
	QEMUAllowedProfiles             []model.GuestProfile
	QEMUDangerousProfiles           []model.GuestProfile
	QEMUAllowDangerousProfiles      bool
	QEMUAllowSSHCompat              bool
	QEMUSSHUser                     string
	QEMUSSHPrivateKeyPath           string
	QEMUSSHHostKeyPath              string
	QEMUBootTimeout                 time.Duration
	KataBinary                      string
	KataRuntimeClass                string
	KataContainerdSocket            string
}

func Load(args []string) (Config, error) {
	fs := flag.NewFlagSet("sandboxd", flag.ContinueOnError)
	cfg := Config{}
	fs.StringVar(&cfg.DeploymentMode, "mode", env("SANDBOX_MODE", "development"), "deployment mode")
	fs.StringVar(&cfg.DeploymentProfile, "deployment-profile", env("SANDBOX_DEPLOYMENT_PROFILE", ""), "supported deployment profile")
	fs.StringVar(&cfg.ProductionTransportMode, "production-transport", env("SANDBOX_PRODUCTION_TRANSPORT", "auto"), "production transport mode: auto, direct-tls, terminated-proxy")
	fs.StringVar(&cfg.ListenAddress, "listen", env("SANDBOX_LISTEN", ":8080"), "HTTP listen address")
	fs.StringVar(&cfg.DatabasePath, "db", env("SANDBOX_DB_PATH", "./data/sandbox.db"), "SQLite path")
	fs.StringVar(&cfg.StorageRoot, "storage-root", env("SANDBOX_STORAGE_ROOT", "./data/storage"), "storage root")
	fs.StringVar(&cfg.SnapshotRoot, "snapshot-root", env("SANDBOX_SNAPSHOT_ROOT", "./data/snapshots"), "snapshot root")
	fs.StringVar(&cfg.BaseImageRef, "base-image", env("SANDBOX_BASE_IMAGE", "alpine:3.20"), "default base image")
	fs.StringVar(&cfg.RuntimeBackend, "runtime", defaultRuntimeBackend(args), "runtime backend")
	enabledRuntimeSelections := env("SANDBOX_ENABLED_RUNTIMES", "")
	defaultRuntimeSelection := env("SANDBOX_DEFAULT_RUNTIME", "")
	fs.StringVar(&enabledRuntimeSelections, "enabled-runtimes", enabledRuntimeSelections, "comma-separated enabled runtime selections")
	fs.StringVar(&defaultRuntimeSelection, "default-runtime", defaultRuntimeSelection, "default runtime selection")
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
	fs.IntVar(&cfg.AdmissionMaxNodeSandboxes, "admission-max-node-sandboxes", envInt("SANDBOX_ADMISSION_MAX_NODE_SANDBOXES", 0), "maximum non-deleted sandboxes this node should admit before denying create requests; 0 disables")
	fs.IntVar(&cfg.AdmissionMaxNodeRunning, "admission-max-node-running", envInt("SANDBOX_ADMISSION_MAX_NODE_RUNNING", 0), "maximum active sandboxes this node should admit before denying start-like requests; 0 disables")
	admissionMaxNodeCPU := env("SANDBOX_ADMISSION_MAX_NODE_CPU", "")
	fs.StringVar(&admissionMaxNodeCPU, "admission-max-node-cpu", admissionMaxNodeCPU, "maximum active CPU to admit on this node, for example 8 or 8000m; empty disables")
	fs.IntVar(&cfg.AdmissionMaxNodeMemoryMB, "admission-max-node-memory-mb", envInt("SANDBOX_ADMISSION_MAX_NODE_MEMORY_MB", 0), "maximum active guest memory this node should admit before denying start-like requests; 0 disables")
	fs.IntVar(&cfg.AdmissionMinNodeFreeStorageMB, "admission-min-node-free-storage-mb", envInt("SANDBOX_ADMISSION_MIN_NODE_FREE_STORAGE_MB", 0), "minimum free bytes required on storage and snapshot volumes before admitting heavy operations; 0 disables")
	fs.IntVar(&cfg.AdmissionMaxTenantStarts, "admission-max-tenant-starts", envInt("SANDBOX_ADMISSION_MAX_TENANT_STARTS", 0), "maximum concurrent create/start/boot operations per tenant; 0 disables")
	fs.IntVar(&cfg.AdmissionMaxTenantHeavyOps, "admission-max-tenant-heavy-ops", envInt("SANDBOX_ADMISSION_MAX_TENANT_HEAVY_OPS", 0), "maximum concurrent heavy lifecycle operations per tenant; 0 disables")
	fs.IntVar(&cfg.StorageWarningFileCount, "storage-warning-file-count", envInt("SANDBOX_STORAGE_WARNING_FILE_COUNT", 10000), "warn when a sandbox exceeds this many stored files across workspace, cache, scratch, and snapshots")
	workspaceFileTransferMaxMB := envInt("SANDBOX_WORKSPACE_FILE_TRANSFER_MAX_MB", int(model.DefaultWorkspaceFileTransferMaxBytes/(1024*1024)))
	fs.IntVar(&workspaceFileTransferMaxMB, "workspace-file-transfer-max-mb", workspaceFileTransferMaxMB, "maximum workspace file transfer size for file read/write APIs in megabytes")
	snapshotMaxMB := envInt("SANDBOX_SNAPSHOT_MAX_MB", 1024)
	fs.IntVar(&snapshotMaxMB, "snapshot-max-mb", snapshotMaxMB, "maximum extracted snapshot bundle size in megabytes")
	fs.IntVar(&cfg.SnapshotMaxFiles, "snapshot-max-files", envInt("SANDBOX_SNAPSHOT_MAX_FILES", 8192), "maximum files allowed in a restored snapshot archive")
	fs.IntVar(&cfg.SnapshotMaxExpansionRatio, "snapshot-max-expansion-ratio", envInt("SANDBOX_SNAPSHOT_MAX_EXPANSION_RATIO", 32), "maximum extracted-to-compressed ratio allowed for snapshot restore archives")
	fs.StringVar(&cfg.DockerUser, "docker-user", env("SANDBOX_DOCKER_USER", "10001:10001"), "docker user or uid:gid for trusted-docker sandboxes")
	fs.IntVar(&cfg.DockerTmpfsSizeMB, "docker-tmpfs-mb", envInt("SANDBOX_DOCKER_TMPFS_MB", 64), "docker tmpfs size for /tmp in megabytes")
	fs.StringVar(&cfg.DockerSeccompProfile, "docker-seccomp-profile", env("SANDBOX_DOCKER_SECCOMP_PROFILE", ""), "optional docker seccomp profile path")
	fs.StringVar(&cfg.DockerAppArmorProfile, "docker-apparmor-profile", env("SANDBOX_DOCKER_APPARMOR_PROFILE", ""), "optional docker AppArmor profile name")
	fs.StringVar(&cfg.DockerSELinuxLabel, "docker-selinux-label", env("SANDBOX_DOCKER_SELINUX_LABEL", ""), "optional docker SELinux label security option")
	dockerAllowDangerousOverrides := strings.EqualFold(env("SANDBOX_DOCKER_ALLOW_DANGEROUS_OVERRIDES", "false"), "true")
	fs.BoolVar(&dockerAllowDangerousOverrides, "docker-allow-dangerous-overrides", dockerAllowDangerousOverrides, "allow explicit dangerous docker capability overrides in trusted environments")
	fs.StringVar(&cfg.QEMUBinary, "qemu-binary", env("SANDBOX_QEMU_BINARY", defaultQEMUBinary()), "qemu system binary")
	fs.StringVar(&cfg.QEMUAccel, "qemu-accel", env("SANDBOX_QEMU_ACCEL", "auto"), "qemu accelerator selection")
	fs.StringVar(&cfg.QEMUBaseImagePath, "qemu-base-image-path", env("SANDBOX_QEMU_BASE_IMAGE_PATH", ""), "qemu base guest image path")
	qemuAllowedBaseImagePaths := env("SANDBOX_QEMU_ALLOWED_BASE_IMAGE_PATHS", "")
	fs.StringVar(&qemuAllowedBaseImagePaths, "qemu-allowed-base-image-paths", qemuAllowedBaseImagePaths, "comma-separated qemu guest image paths tenants may request")
	allowedGuestProfiles := env("SANDBOX_ALLOWED_PROFILES", "")
	qemuAllowedProfiles := env("SANDBOX_QEMU_ALLOWED_PROFILES", "")
	fs.StringVar(&qemuAllowedProfiles, "qemu-allowed-profiles", qemuAllowedProfiles, "comma-separated qemu guest profiles allowed for sandbox creation")
	dangerousGuestProfiles := env("SANDBOX_DANGEROUS_PROFILES", "")
	qemuDangerousProfiles := env("SANDBOX_QEMU_DANGEROUS_PROFILES", "container,debug")
	fs.StringVar(&qemuDangerousProfiles, "qemu-dangerous-profiles", qemuDangerousProfiles, "comma-separated qemu guest profiles treated as dangerous and blocked unless explicitly allowed")
	qemuControlMode := env("SANDBOX_QEMU_CONTROL_MODE", string(model.GuestControlModeAgent))
	fs.StringVar(&qemuControlMode, "qemu-control-mode", qemuControlMode, "qemu control mode: agent or ssh-compat")
	allowDangerousProfiles := strings.EqualFold(env("SANDBOX_ALLOW_DANGEROUS_PROFILES", ""), "true")
	qemuAllowDangerousProfiles := allowDangerousProfiles
	if !qemuAllowDangerousProfiles {
		qemuAllowDangerousProfiles = strings.EqualFold(env("SANDBOX_QEMU_ALLOW_DANGEROUS_PROFILES", "false"), "true")
	}
	fs.BoolVar(&qemuAllowDangerousProfiles, "qemu-allow-dangerous-profiles", qemuAllowDangerousProfiles, "allow dangerous qemu guest profiles such as container and debug")
	qemuAllowSSHCompat := strings.EqualFold(env("SANDBOX_QEMU_ALLOW_SSH_COMPAT", "false"), "true")
	fs.BoolVar(&qemuAllowSSHCompat, "qemu-allow-ssh-compat", qemuAllowSSHCompat, "allow ssh-compat qemu image contracts in production validation and policy")
	productionAllowDockerBreakglass := strings.EqualFold(env("SANDBOX_PRODUCTION_ALLOW_DOCKER_BREAKGLASS", "false"), "true")
	fs.BoolVar(&productionAllowDockerBreakglass, "production-allow-docker-breakglass", productionAllowDockerBreakglass, "allow docker-dev as an explicit production break-glass default")
	fs.StringVar(&cfg.QEMUSSHUser, "qemu-ssh-user", env("SANDBOX_QEMU_SSH_USER", ""), "qemu guest ssh user")
	fs.StringVar(&cfg.QEMUSSHPrivateKeyPath, "qemu-ssh-private-key", env("SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH", ""), "qemu guest ssh private key path")
	fs.StringVar(&cfg.QEMUSSHHostKeyPath, "qemu-ssh-host-key", env("SANDBOX_QEMU_SSH_HOST_KEY_PATH", ""), "qemu guest ssh host public key path")
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
	fs.StringVar(&cfg.KataBinary, "kata-binary", env("SANDBOX_KATA_BINARY", "ctr"), "kata/containerd client binary")
	fs.StringVar(&cfg.KataRuntimeClass, "kata-runtime-class", env("SANDBOX_KATA_RUNTIME_CLASS", "io.containerd.kata.v2"), "kata runtime class name")
	fs.StringVar(&cfg.KataContainerdSocket, "kata-containerd-socket", env("SANDBOX_KATA_CONTAINERD_SOCKET", "/run/containerd/containerd.sock"), "containerd socket path for kata runtime")
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
	if strings.TrimSpace(admissionMaxNodeCPU) != "" {
		cfg.AdmissionMaxNodeCPU, err = model.ParseCPUQuantity(admissionMaxNodeCPU)
		if err != nil {
			return Config{}, fmt.Errorf("parse admission max node cpu: %w", err)
		}
	}
	cfg.DefaultCPULimit = defaultCPULimit
	maxCPUCores, err := model.ParseCPUQuantity(env("SANDBOX_QUOTA_MAX_CPU", "16"))
	if err != nil {
		return Config{}, fmt.Errorf("parse max cpu quota: %w", err)
	}
	cfg.DefaultNetworkMode = model.NetworkMode(networkMode)
	cfg.WorkspaceFileTransferMaxBytes = int64(workspaceFileTransferMaxMB) * 1024 * 1024
	cfg.SnapshotMaxBytes = int64(snapshotMaxMB) * 1024 * 1024
	cfg.DefaultAllowTunnels = strings.EqualFold(allowTunnels, "true")
	cfg.TrustedDockerRuntime = strings.EqualFold(trustedDockerRuntime, "true")
	cfg.TrustedProxyHeaders = trustedProxyHeaders
	cfg.EnabledRuntimeSelections = parseRuntimeSelections(enabledRuntimeSelections)
	cfg.DefaultRuntimeSelection = model.ParseRuntimeSelection(defaultRuntimeSelection)
	cfg.AuthJWTSecretPaths = parseCommaSeparated(authJWTSecretPaths)
	cfg.PolicyAllowedImages = parseCommaSeparated(policyAllowedImages)
	cfg.PolicyAllowPublicTunnels = policyAllowPublicTunnels
	cfg.DockerAllowDangerousOverrides = dockerAllowDangerousOverrides
	cfg.OptionalSnapshotExport = env("SANDBOX_S3_EXPORT_URI", "")
	cfg.QEMUAllowedBaseImagePaths = parseCommaSeparated(qemuAllowedBaseImagePaths)
	cfg.QEMUControlMode = model.GuestControlMode(strings.ToLower(strings.TrimSpace(qemuControlMode)))
	cfg.AllowedGuestProfiles = parseGuestProfiles(allowedGuestProfiles)
	cfg.DangerousGuestProfiles = parseGuestProfiles(dangerousGuestProfiles)
	cfg.AllowDangerousProfiles = allowDangerousProfiles
	cfg.QEMUAllowedProfiles = parseGuestProfiles(qemuAllowedProfiles)
	cfg.QEMUDangerousProfiles = parseGuestProfiles(qemuDangerousProfiles)
	cfg.QEMUAllowDangerousProfiles = qemuAllowDangerousProfiles
	cfg.QEMUAllowSSHCompat = qemuAllowSSHCompat
	cfg.ProductionTransportMode = normalizeProductionTransportMode(cfg.ProductionTransportMode)
	cfg.ProductionAllowDockerBreakglass = productionAllowDockerBreakglass
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
	cfg.applyDeploymentProfile()
	cfg.applyRuntimeSelectionCompatibility()
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	c.applyDeploymentProfile()
	c.applyRuntimeSelectionCompatibility()
	var problems []string
	if c.StorageWarningFileCount == 0 {
		c.StorageWarningFileCount = 10000
	}
	if c.WorkspaceFileTransferMaxBytes == 0 {
		c.WorkspaceFileTransferMaxBytes = model.DefaultWorkspaceFileTransferMaxBytes
	}
	if c.SnapshotMaxBytes == 0 {
		c.SnapshotMaxBytes = 1024 * 1024 * 1024
	}
	if c.SnapshotMaxFiles == 0 {
		c.SnapshotMaxFiles = 8192
	}
	if c.SnapshotMaxExpansionRatio == 0 {
		c.SnapshotMaxExpansionRatio = 32
	}
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
	if c.AdmissionMaxNodeSandboxes < 0 {
		problems = append(problems, "admission max node sandboxes must be zero or positive")
	}
	if c.AdmissionMaxNodeRunning < 0 {
		problems = append(problems, "admission max node running must be zero or positive")
	}
	if c.AdmissionMaxNodeCPU < 0 {
		problems = append(problems, "admission max node cpu must be zero or positive")
	}
	if c.AdmissionMaxNodeMemoryMB < 0 {
		problems = append(problems, "admission max node memory must be zero or positive")
	}
	if c.AdmissionMinNodeFreeStorageMB < 0 {
		problems = append(problems, "admission min node free storage must be zero or positive")
	}
	if c.AdmissionMaxTenantStarts < 0 {
		problems = append(problems, "admission max tenant starts must be zero or positive")
	}
	if c.AdmissionMaxTenantHeavyOps < 0 {
		problems = append(problems, "admission max tenant heavy ops must be zero or positive")
	}
	if c.StorageWarningFileCount < 0 {
		problems = append(problems, "storage warning file count must be zero or positive")
	}
	if c.WorkspaceFileTransferMaxBytes <= 0 {
		problems = append(problems, "workspace file transfer max bytes must be positive")
	}
	if c.WorkspaceFileTransferMaxBytes > model.MaxWorkspaceFileTransferCeilingBytes {
		problems = append(problems, fmt.Sprintf("workspace file transfer max bytes must not exceed %d", model.MaxWorkspaceFileTransferCeilingBytes))
	}
	if c.SnapshotMaxBytes <= 0 {
		problems = append(problems, "snapshot max bytes must be positive")
	}
	if c.SnapshotMaxFiles <= 0 {
		problems = append(problems, "snapshot max files must be positive")
	}
	if c.SnapshotMaxExpansionRatio <= 0 {
		problems = append(problems, "snapshot max expansion ratio must be positive")
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
	if c.DefaultRuntimeSelection.Backend() == "qemu" && c.DefaultCPULimit.MilliValue()%1000 != 0 {
		problems = append(problems, "qemu runtime requires a whole-core default cpu limit")
	}
	if c.DefaultNetworkMode != model.NetworkModeInternetEnabled && c.DefaultNetworkMode != model.NetworkModeInternetDisabled {
		problems = append(problems, fmt.Sprintf("unsupported default network mode %q", c.DefaultNetworkMode))
	}
	if c.AuthMode == "static" && len(c.Tenants) == 0 {
		problems = append(problems, "at least one tenant token is required")
	}
	if c.DeploymentMode == "production" {
		if c.DefaultRuntimeSelection == model.RuntimeSelectionDockerDev && !c.ProductionAllowDockerBreakglass {
			problems = append(problems, "production mode rejects docker-dev as the default runtime selection unless SANDBOX_PRODUCTION_ALLOW_DOCKER_BREAKGLASS=true")
		}
		if !c.DefaultRuntimeSelection.IsVMBacked() {
			problems = append(problems, fmt.Sprintf("production mode requires a VM-backed runtime class; %q resolves to class %q which is not VM-backed", c.DefaultRuntimeSelection, c.DefaultRuntimeSelection.RuntimeClass()))
		}
		if c.AuthMode == "static" {
			problems = append(problems, "production mode requires SANDBOX_AUTH_MODE=jwt-hs256")
		}
		switch normalizeProductionTransportMode(c.ProductionTransportMode) {
		case "auto":
			if c.TLSCertPath == "" && !c.TrustedProxyHeaders {
				problems = append(problems, "production mode requires TLS certificate paths or SANDBOX_TRUST_PROXY_HEADERS=true")
			}
		case "direct-tls":
			if c.TLSCertPath == "" || c.TLSKeyPath == "" {
				problems = append(problems, "production direct-tls mode requires SANDBOX_TLS_CERT_PATH and SANDBOX_TLS_KEY_PATH")
			}
		case "terminated-proxy":
			if !c.TrustedProxyHeaders {
				problems = append(problems, "production terminated-proxy mode requires SANDBOX_TRUST_PROXY_HEADERS=true")
			}
		default:
			problems = append(problems, fmt.Sprintf("unsupported production transport mode %q", c.ProductionTransportMode))
		}
		for _, profile := range c.effectiveAllowedGuestProfiles("qemu") {
			if c.DeploymentProfile != "exception-container" && (profile == model.GuestProfileContainer || profile == model.GuestProfileDebug) {
				problems = append(problems, fmt.Sprintf("production mode rejects dangerous default qemu profile %q", profile))
			}
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
	c.applyRuntimeSelectionCompatibility()
	if len(c.EnabledRuntimeSelections) == 0 {
		return errors.New("at least one runtime selection must be enabled")
	}
	if !c.DefaultRuntimeSelection.IsValid() {
		return fmt.Errorf("unsupported default runtime selection %q", c.DefaultRuntimeSelection)
	}
	if !c.IsRuntimeSelectionEnabled(c.DefaultRuntimeSelection) {
		return fmt.Errorf("default runtime selection %q must also be enabled", c.DefaultRuntimeSelection)
	}
	for _, selection := range c.EnabledRuntimeSelections {
		switch selection {
		case model.RuntimeSelectionDockerDev:
			if err := validateDockerConfig(c, probe); err != nil {
				return err
			}
		case model.RuntimeSelectionQEMUProfessional:
			if err := validateQEMUConfig(c, probe); err != nil {
				return err
			}
		case model.RuntimeSelectionContainerdKataProfessional:
			if err := validateKataConfig(c, probe); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported runtime selection %q", selection)
		}
	}
	return nil
}

func validateDockerConfig(c Config, probe runtimeValidationProbe) error {
	if !c.TrustedDockerRuntime {
		return errors.New("docker runtime requires SANDBOX_TRUSTED_DOCKER_RUNTIME=true because it is shared-kernel and not a production multi-tenant boundary")
	}
	if strings.TrimSpace(c.DockerUser) == "" {
		c.DockerUser = defaultDockerUser()
	}
	if c.DockerTmpfsSizeMB == 0 {
		c.DockerTmpfsSizeMB = defaultDockerTmpfsSizeMB()
	}
	if c.DockerTmpfsSizeMB < 0 {
		return errors.New("docker runtime requires SANDBOX_DOCKER_TMPFS_MB to be positive")
	}
	if strings.TrimSpace(c.DockerSeccompProfile) != "" {
		if err := probe.fileReadable(c.DockerSeccompProfile); err != nil {
			return fmt.Errorf("docker seccomp profile is not readable: %w", err)
		}
	}
	return nil
}

func validateKataConfig(c Config, probe runtimeValidationProbe) error {
	if probe.goos != "linux" {
		return fmt.Errorf("kata runtime requires linux (host OS is %q)", probe.goos)
	}
	if err := probe.commandExists(c.KataBinary); err != nil {
		return fmt.Errorf("kata runtime requires a working client binary: %w", err)
	}
	if strings.TrimSpace(c.KataRuntimeClass) == "" {
		return errors.New("kata runtime requires SANDBOX_KATA_RUNTIME_CLASS")
	}
	if strings.TrimSpace(c.KataContainerdSocket) == "" {
		return errors.New("kata runtime requires SANDBOX_KATA_CONTAINERD_SOCKET")
	}
	return nil
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
	if !c.QEMUControlMode.IsValid() {
		return fmt.Errorf("qemu runtime requires SANDBOX_QEMU_CONTROL_MODE to be one of %q or %q", model.GuestControlModeAgent, model.GuestControlModeSSHCompat)
	}
	if len(c.effectiveAllowedGuestProfiles("qemu")) == 0 {
		return errors.New("qemu runtime requires at least one allowed guest profile")
	}
	if c.QEMUControlMode == model.GuestControlModeSSHCompat {
		if strings.TrimSpace(c.QEMUSSHUser) == "" {
			return errors.New("qemu ssh-compat mode requires SANDBOX_QEMU_SSH_USER")
		}
		if strings.TrimSpace(c.QEMUSSHPrivateKeyPath) == "" {
			return errors.New("qemu ssh-compat mode requires SANDBOX_QEMU_SSH_PRIVATE_KEY_PATH")
		}
		if strings.TrimSpace(c.QEMUSSHHostKeyPath) == "" {
			return errors.New("qemu ssh-compat mode requires SANDBOX_QEMU_SSH_HOST_KEY_PATH")
		}
	}
	if err := probe.commandExists(c.QEMUBinary); err != nil {
		return fmt.Errorf("qemu runtime requires a working QEMU binary: %w", err)
	}
	if err := probe.fileReadable(c.QEMUBaseImagePath); err != nil {
		return fmt.Errorf("qemu runtime base image path is not readable: %w", err)
	}
	if c.QEMUControlMode == model.GuestControlModeSSHCompat {
		if err := probe.fileReadable(c.QEMUSSHPrivateKeyPath); err != nil {
			return fmt.Errorf("qemu runtime ssh private key is not readable: %w", err)
		}
		if err := probe.fileReadable(c.QEMUSSHHostKeyPath); err != nil {
			return fmt.Errorf("qemu runtime ssh host public key is not readable: %w", err)
		}
	}
	for _, path := range c.EffectiveQEMUAllowedBaseImagePaths() {
		if err := probe.fileReadable(path); err != nil {
			return fmt.Errorf("qemu allowed base image path is not readable: %w", err)
		}
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
	if c.DeploymentMode == "production" && c.QEMUControlMode == model.GuestControlModeSSHCompat && !c.QEMUAllowSSHCompat {
		return errors.New("production qemu mode rejects ssh-compat images unless SANDBOX_QEMU_ALLOW_SSH_COMPAT=true")
	}
	return nil
}

// RuntimeClass returns the runtime class derived from the configured backend.
func (c Config) RuntimeClass() model.RuntimeClass {
	c.applyRuntimeSelectionCompatibility()
	return c.DefaultRuntimeSelection.RuntimeClass()
}

func (c Config) IsRuntimeSelectionEnabled(selection model.RuntimeSelection) bool {
	for _, enabled := range c.EnabledRuntimeSelections {
		if enabled == selection {
			return true
		}
	}
	return false
}

func (c *Config) applyRuntimeSelectionCompatibility() {
	if len(c.EnabledRuntimeSelections) == 0 {
		legacy := model.RuntimeSelectionFromBackend(c.RuntimeBackend)
		if legacy.IsValid() {
			c.EnabledRuntimeSelections = []model.RuntimeSelection{legacy}
		}
	}
	if !c.DefaultRuntimeSelection.IsValid() {
		c.DefaultRuntimeSelection = model.ResolveRuntimeSelection(c.DefaultRuntimeSelection, c.RuntimeBackend)
	}
	if c.DefaultRuntimeSelection.IsValid() {
		c.RuntimeBackend = c.DefaultRuntimeSelection.Backend()
	}
	if c.DeploymentMode == "production" && !c.DefaultRuntimeSelection.IsValid() && c.RuntimeBackend == "" {
		c.DefaultRuntimeSelection = model.RuntimeSelectionQEMUProfessional
		c.RuntimeBackend = c.DefaultRuntimeSelection.Backend()
	}
}

func parseRuntimeSelections(raw string) []model.RuntimeSelection {
	entries := parseCommaSeparated(raw)
	seen := make(map[model.RuntimeSelection]struct{}, len(entries))
	result := make([]model.RuntimeSelection, 0, len(entries))
	for _, entry := range entries {
		selection := model.ParseRuntimeSelection(entry)
		if !selection.IsValid() {
			continue
		}
		if _, ok := seen[selection]; ok {
			continue
		}
		seen[selection] = struct{}{}
		result = append(result, selection)
	}
	return result
}

func defaultRuntimeBackend(args []string) string {
	if value := strings.TrimSpace(os.Getenv("SANDBOX_RUNTIME")); value != "" {
		return value
	}
	if strings.EqualFold(strings.TrimSpace(flagValue(args, "mode")), "production") {
		return "qemu"
	}
	if strings.EqualFold(strings.TrimSpace(env("SANDBOX_MODE", "development")), "production") {
		return "qemu"
	}
	return "docker"
}

func flagValue(args []string, name string) string {
	short := "-" + name
	long := "--" + name
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case strings.HasPrefix(arg, short+"="):
			return strings.TrimPrefix(arg, short+"=")
		case strings.HasPrefix(arg, long+"="):
			return strings.TrimPrefix(arg, long+"=")
		case arg == short || arg == long:
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	return ""
}

func (c Config) EffectiveQEMUAllowedBaseImagePaths() []string {
	return appendDefaultQEMUBaseImagePath(c.QEMUAllowedBaseImagePaths, c.QEMUBaseImagePath)
}

func appendDefaultQEMUBaseImagePath(paths []string, defaultPath string) []string {
	seen := make(map[string]struct{}, len(paths)+1)
	var result []string
	for _, raw := range append(paths, defaultPath) {
		normalized := NormalizeQEMUBaseImagePath(raw)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func NormalizeQEMUBaseImagePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
}

func (c Config) IsAllowedQEMUProfile(profile model.GuestProfile) bool {
	return c.IsAllowedGuestProfile("qemu", profile)
}

func (c Config) IsAllowedGuestProfile(runtimeBackend string, profile model.GuestProfile) bool {
	for _, allowed := range c.effectiveAllowedGuestProfiles(runtimeBackend) {
		if allowed == profile {
			return true
		}
	}
	return false
}

func (c Config) IsDangerousQEMUProfile(profile model.GuestProfile) bool {
	return c.IsDangerousGuestProfile("qemu", profile)
}

func (c Config) IsDangerousGuestProfile(runtimeBackend string, profile model.GuestProfile) bool {
	for _, dangerous := range c.effectiveDangerousGuestProfiles(runtimeBackend) {
		if dangerous == profile {
			return true
		}
	}
	return false
}

func (c Config) AllowsDangerousGuestProfiles(runtimeBackend string) bool {
	return c.AllowDangerousProfiles || (runtimeBackend == "qemu" && c.QEMUAllowDangerousProfiles)
}

func (c Config) effectiveAllowedGuestProfiles(runtimeBackend string) []model.GuestProfile {
	if len(c.AllowedGuestProfiles) > 0 {
		return c.AllowedGuestProfiles
	}
	if runtimeBackend == "qemu" && len(c.QEMUAllowedProfiles) > 0 {
		return c.QEMUAllowedProfiles
	}
	return []model.GuestProfile{
		model.GuestProfileCore,
		model.GuestProfileRuntime,
		model.GuestProfileBrowser,
		model.GuestProfileContainer,
		model.GuestProfileDebug,
	}
}

func (c *Config) applyDeploymentProfile() {
	switch strings.TrimSpace(c.DeploymentProfile) {
	case "":
		if c.DeploymentMode == "production" {
			if c.RuntimeBackend == "" {
				c.RuntimeBackend = "qemu"
			}
			if len(c.EnabledRuntimeSelections) == 0 && c.RuntimeBackend == "qemu" {
				c.EnabledRuntimeSelections = []model.RuntimeSelection{model.RuntimeSelectionQEMUProfessional}
			}
			if !c.DefaultRuntimeSelection.IsValid() && c.RuntimeBackend == "qemu" {
				c.DefaultRuntimeSelection = model.RuntimeSelectionQEMUProfessional
			}
			if len(c.QEMUAllowedProfiles) == 0 {
				c.QEMUAllowedProfiles = []model.GuestProfile{model.GuestProfileCore, model.GuestProfileRuntime}
			}
		}
	case "dev-trusted-docker":
		c.DeploymentMode = "development"
		c.RuntimeBackend = "docker"
		c.TrustedDockerRuntime = true
		c.EnabledRuntimeSelections = []model.RuntimeSelection{model.RuntimeSelectionDockerDev}
		c.DefaultRuntimeSelection = model.RuntimeSelectionDockerDev
	case "production-qemu-core":
		c.DeploymentMode = "production"
		c.RuntimeBackend = "qemu"
		c.EnabledRuntimeSelections = []model.RuntimeSelection{model.RuntimeSelectionQEMUProfessional}
		c.DefaultRuntimeSelection = model.RuntimeSelectionQEMUProfessional
		c.QEMUAllowedProfiles = []model.GuestProfile{model.GuestProfileCore, model.GuestProfileRuntime}
	case "production-qemu-browser":
		c.DeploymentMode = "production"
		c.RuntimeBackend = "qemu"
		c.EnabledRuntimeSelections = []model.RuntimeSelection{model.RuntimeSelectionQEMUProfessional}
		c.DefaultRuntimeSelection = model.RuntimeSelectionQEMUProfessional
		c.QEMUAllowedProfiles = []model.GuestProfile{model.GuestProfileCore, model.GuestProfileRuntime, model.GuestProfileBrowser}
	case "exception-container":
		c.DeploymentMode = "production"
		c.RuntimeBackend = "qemu"
		c.EnabledRuntimeSelections = []model.RuntimeSelection{model.RuntimeSelectionQEMUProfessional}
		c.DefaultRuntimeSelection = model.RuntimeSelectionQEMUProfessional
		c.QEMUAllowedProfiles = []model.GuestProfile{model.GuestProfileCore, model.GuestProfileRuntime, model.GuestProfileContainer}
		c.QEMUAllowDangerousProfiles = true
	default:
	}
}

func normalizeProductionTransportMode(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return "auto"
	}
	return trimmed
}

func (c Config) effectiveDangerousGuestProfiles(runtimeBackend string) []model.GuestProfile {
	if len(c.DangerousGuestProfiles) > 0 {
		return c.DangerousGuestProfiles
	}
	if runtimeBackend == "qemu" && len(c.QEMUDangerousProfiles) > 0 {
		return c.QEMUDangerousProfiles
	}
	return []model.GuestProfile{model.GuestProfileContainer, model.GuestProfileDebug}
}

func parseGuestProfiles(raw string) []model.GuestProfile {
	entries := parseCommaSeparated(raw)
	seen := make(map[model.GuestProfile]struct{}, len(entries))
	result := make([]model.GuestProfile, 0, len(entries))
	for _, entry := range entries {
		profile := model.GuestProfile(strings.ToLower(strings.TrimSpace(entry)))
		if !profile.IsValid() {
			continue
		}
		if _, ok := seen[profile]; ok {
			continue
		}
		seen[profile] = struct{}{}
		result = append(result, profile)
	}
	return result
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
	case "tcg":
		return "tcg", nil
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

func defaultDockerUser() string {
	return "10001:10001"
}

func defaultDockerTmpfsSizeMB() int {
	return 64
}
