package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
	ListenAddress          string
	DatabasePath           string
	StorageRoot            string
	SnapshotRoot           string
	BaseImageRef           string
	RuntimeBackend         string
	DefaultCPULimit        int
	DefaultMemoryLimitMB   int
	DefaultPIDsLimit       int
	DefaultDiskLimitMB     int
	DefaultNetworkMode     model.NetworkMode
	DefaultAllowTunnels    bool
	RequestRatePerMinute   int
	RequestBurst           int
	DefaultQuota           model.TenantQuota
	GracefulShutdown       time.Duration
	ReconcileInterval      time.Duration
	CleanupInterval        time.Duration
	OperatorHost           string
	Tenants                []TenantConfig
	OptionalSnapshotExport string
}

func Load(args []string) (Config, error) {
	fs := flag.NewFlagSet("sandboxd", flag.ContinueOnError)
	cfg := Config{}
	fs.StringVar(&cfg.ListenAddress, "listen", env("SANDBOX_LISTEN", ":8080"), "HTTP listen address")
	fs.StringVar(&cfg.DatabasePath, "db", env("SANDBOX_DB_PATH", "./data/sandbox.db"), "SQLite path")
	fs.StringVar(&cfg.StorageRoot, "storage-root", env("SANDBOX_STORAGE_ROOT", "./data/storage"), "storage root")
	fs.StringVar(&cfg.SnapshotRoot, "snapshot-root", env("SANDBOX_SNAPSHOT_ROOT", "./data/snapshots"), "snapshot root")
	fs.StringVar(&cfg.BaseImageRef, "base-image", env("SANDBOX_BASE_IMAGE", "mcr.microsoft.com/playwright:v1.51.1-noble"), "default base image")
	fs.StringVar(&cfg.RuntimeBackend, "runtime", env("SANDBOX_RUNTIME", "docker"), "runtime backend")
	fs.IntVar(&cfg.DefaultCPULimit, "default-cpu", envInt("SANDBOX_DEFAULT_CPU", 2), "default cpu limit")
	fs.IntVar(&cfg.DefaultMemoryLimitMB, "default-memory-mb", envInt("SANDBOX_DEFAULT_MEMORY_MB", 2048), "default memory limit")
	fs.IntVar(&cfg.DefaultPIDsLimit, "default-pids", envInt("SANDBOX_DEFAULT_PIDS", 512), "default pids limit")
	fs.IntVar(&cfg.DefaultDiskLimitMB, "default-disk-mb", envInt("SANDBOX_DEFAULT_DISK_MB", 10240), "default disk limit")
	fs.IntVar(&cfg.RequestRatePerMinute, "rate-limit", envInt("SANDBOX_RATE_LIMIT_PER_MIN", 120), "per-tenant requests per minute")
	fs.IntVar(&cfg.RequestBurst, "rate-burst", envInt("SANDBOX_RATE_LIMIT_BURST", 30), "per-tenant burst")
	fs.DurationVar(&cfg.GracefulShutdown, "shutdown-timeout", envDuration("SANDBOX_SHUTDOWN_TIMEOUT", 15*time.Second), "graceful shutdown timeout")
	fs.DurationVar(&cfg.ReconcileInterval, "reconcile-interval", envDuration("SANDBOX_RECONCILE_INTERVAL", 30*time.Second), "reconcile interval")
	fs.DurationVar(&cfg.CleanupInterval, "cleanup-interval", envDuration("SANDBOX_CLEANUP_INTERVAL", 5*time.Minute), "cleanup interval")
	fs.StringVar(&cfg.OperatorHost, "operator-host", env("SANDBOX_OPERATOR_HOST", "http://127.0.0.1:8080"), "public control plane host")
	networkMode := env("SANDBOX_DEFAULT_NETWORK_MODE", string(model.NetworkModeInternetEnabled))
	allowTunnels := env("SANDBOX_DEFAULT_ALLOW_TUNNELS", "true")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	cfg.DefaultNetworkMode = model.NetworkMode(networkMode)
	cfg.DefaultAllowTunnels = strings.EqualFold(allowTunnels, "true")
	cfg.OptionalSnapshotExport = env("SANDBOX_S3_EXPORT_URI", "")
	cfg.DefaultQuota = model.TenantQuota{
		MaxSandboxes:            envInt("SANDBOX_QUOTA_MAX_SANDBOXES", 10),
		MaxRunningSandboxes:     envInt("SANDBOX_QUOTA_MAX_RUNNING", 5),
		MaxConcurrentExecs:      envInt("SANDBOX_QUOTA_MAX_EXECS", 8),
		MaxTunnels:              envInt("SANDBOX_QUOTA_MAX_TUNNELS", 8),
		MaxCPUCores:             envInt("SANDBOX_QUOTA_MAX_CPU", 16),
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
	if c.RuntimeBackend != "docker" {
		problems = append(problems, fmt.Sprintf("unsupported runtime backend %q", c.RuntimeBackend))
	}
	if c.DefaultNetworkMode != model.NetworkModeInternetEnabled && c.DefaultNetworkMode != model.NetworkModeInternetDisabled {
		problems = append(problems, fmt.Sprintf("unsupported default network mode %q", c.DefaultNetworkMode))
	}
	if len(c.Tenants) == 0 {
		problems = append(problems, "at least one tenant token is required")
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
