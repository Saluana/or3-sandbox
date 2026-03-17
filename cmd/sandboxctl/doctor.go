package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/guestimage"
	"or3-sandbox/internal/model"
)

var (
	doctorConfigLoader = config.Load
	doctorHostOS       = goruntime.GOOS
	doctorLookPath     = exec.LookPath
	doctorStat         = os.Stat
	doctorReadFile     = os.ReadFile
	doctorStatFS       = statDoctorFS
)

const (
	doctorWarnFreeBytes = 5 << 30
	doctorFailFreeBytes = 1 << 30
)

type doctorFSInfo struct {
	AvailableBytes uint64
}

type doctorCheck struct {
	Level  string `json:"level"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

type doctorSummary struct {
	Mode      string        `json:"mode"`
	CheckedAt time.Time     `json:"checked_at"`
	Checks    []doctorCheck `json:"checks,omitempty"`
	OutputDir string        `json:"output_dir,omitempty"`
	Artifacts []string      `json:"artifacts,omitempty"`
	Errors    []doctorCheck `json:"errors,omitempty"`
}

func runDoctor(args []string) error {
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "qemu") {
		return runQEMUDoctor(args[1:])
	}
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	productionQEMU := fs.Bool("production-qemu", false, "validate the production QEMU host and image profile posture")
	jsonOutput := fs.Bool("json", false, "print doctor results as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*productionQEMU {
		return errors.New("usage: sandboxctl doctor --production-qemu [--json]\n   or: sandboxctl doctor qemu --sandbox <id> [--output dir] [--daemon-log path]")
	}
	summary := runProductionQEMUDoctor()
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(summary)
	}
	blocking := 0
	warnings := 0
	for _, check := range summary.Checks {
		switch check.Level {
		case "fail":
			blocking++
		case "warn":
			warnings++
		}
		fmt.Fprintf(os.Stdout, "[%s] %s: %s\n", strings.ToUpper(check.Level), check.Name, check.Detail)
	}
	fmt.Fprintf(os.Stdout, "summary: %d blocking, %d warnings\n", blocking, warnings)
	if blocking > 0 {
		return fmt.Errorf("production qemu doctor found blocking failures")
	}
	return nil
}

func runQEMUDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor qemu", flag.ContinueOnError)
	sandboxID := fs.String("sandbox", "", "sandbox id to inspect")
	outputDir := fs.String("output", "", "bundle output directory")
	daemonLog := fs.String("daemon-log", "", "optional daemon log file to copy into the bundle")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*sandboxID) == "" {
		return errors.New("usage: sandboxctl doctor qemu --sandbox <id> [--output dir] [--daemon-log path]")
	}
	dir := strings.TrimSpace(*outputDir)
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("sandboxctl-doctor-qemu-%s-%d", *sandboxID, time.Now().UTC().Unix()))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	client := clientConfig{
		baseURL: env("SANDBOX_API", "http://127.0.0.1:8080"),
		token:   env("SANDBOX_TOKEN", "dev-token"),
	}
	summary := doctorSummary{Mode: "qemu", CheckedAt: time.Now().UTC(), OutputDir: dir}
	writeArtifact := func(name string, value any) {
		path := filepath.Join(dir, name)
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			summary.Errors = append(summary.Errors, doctorCheck{Level: "error", Name: name, Detail: err.Error()})
			return
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			summary.Errors = append(summary.Errors, doctorCheck{Level: "error", Name: name, Detail: err.Error()})
			return
		}
		summary.Artifacts = append(summary.Artifacts, path)
	}
	collectJSON := func(name, endpoint string, out any) {
		if err := doJSON(client, "GET", endpoint, nil, out); err != nil {
			summary.Errors = append(summary.Errors, doctorCheck{Level: "error", Name: endpoint, Detail: err.Error()})
			return
		}
		writeArtifact(name, out)
	}

	var info model.RuntimeInfo
	collectJSON("runtime-info.json", "/v1/runtime/info", &info)
	var health model.RuntimeHealth
	collectJSON("runtime-health.json", "/v1/runtime/health", &health)
	var sandbox model.Sandbox
	collectJSON("sandbox.json", "/v1/sandboxes/"+*sandboxID, &sandbox)
	var snapshots []model.Snapshot
	collectJSON("snapshots.json", "/v1/sandboxes/"+*sandboxID+"/snapshots", &snapshots)

	if info.GuestImage != nil && strings.TrimSpace(info.GuestImage.SidecarPath) != "" {
		data, err := os.ReadFile(info.GuestImage.SidecarPath)
		if err != nil {
			summary.Errors = append(summary.Errors, doctorCheck{Level: "error", Name: "guest-image-contract", Detail: err.Error()})
		} else if err := os.WriteFile(filepath.Join(dir, "guest-image-contract.json"), data, 0o644); err != nil {
			summary.Errors = append(summary.Errors, doctorCheck{Level: "error", Name: "guest-image-contract", Detail: err.Error()})
		} else {
			summary.Artifacts = append(summary.Artifacts, filepath.Join(dir, "guest-image-contract.json"))
		}
	}
	if strings.TrimSpace(*daemonLog) != "" {
		data, err := os.ReadFile(*daemonLog)
		if err != nil {
			summary.Errors = append(summary.Errors, doctorCheck{Level: "error", Name: "daemon-log", Detail: err.Error()})
		} else if err := os.WriteFile(filepath.Join(dir, "daemon.log"), data, 0o644); err != nil {
			summary.Errors = append(summary.Errors, doctorCheck{Level: "error", Name: "daemon-log", Detail: err.Error()})
		} else {
			summary.Artifacts = append(summary.Artifacts, filepath.Join(dir, "daemon.log"))
		}
	}
	writeArtifact("summary.json", summary)
	return printJSON(summary)
}

func runProductionQEMUDoctor() doctorSummary {
	summary := doctorSummary{Mode: "production-qemu", CheckedAt: time.Now().UTC()}
	add := func(level, name, detail string) {
		summary.Checks = append(summary.Checks, doctorCheck{Level: level, Name: name, Detail: detail})
	}
	cfg, err := doctorConfigLoader(nil)
	if err != nil {
		add("fail", "config", err.Error())
		cfg = doctorConfigFromEnv()
	}
	reportRuntimeSelections(add, cfg)
	if cfg.RuntimeBackend != "qemu" {
		add("fail", "runtime", "SANDBOX_RUNTIME must be qemu for production-qemu validation")
	} else {
		add("pass", "runtime", "runtime backend is qemu")
	}
	runtimeClass := model.BackendToRuntimeClass(cfg.RuntimeBackend)
	if !runtimeClass.IsVMBacked() {
		add("fail", "runtime-class", fmt.Sprintf("runtime backend %q resolves to class %q which is not VM-backed; production requires a VM-backed class", cfg.RuntimeBackend, runtimeClass))
	} else {
		add("pass", "runtime-class", fmt.Sprintf("runtime backend %q resolves to VM-backed class %q", cfg.RuntimeBackend, runtimeClass))
	}
	if cfg.AuthMode != "jwt-hs256" {
		add("fail", "auth", "production qemu requires SANDBOX_AUTH_MODE=jwt-hs256")
	} else {
		add("pass", "auth", "jwt auth is enabled")
	}
	switch cfg.ProductionTransportMode {
	case "", "auto":
		if cfg.TLSCertPath != "" && cfg.TLSKeyPath != "" {
			add("pass", "transport", "direct TLS material is configured")
		} else if cfg.TrustedProxyHeaders && strings.HasPrefix(strings.ToLower(cfg.OperatorHost), "https://") {
			add("pass", "transport", "trusted terminated-proxy transport is configured")
		} else {
			add("fail", "transport", "production transport requires direct TLS material or trusted terminated-proxy posture")
		}
	case "direct-tls":
		if cfg.TLSCertPath == "" || cfg.TLSKeyPath == "" {
			add("fail", "transport", "production direct-tls mode requires SANDBOX_TLS_CERT_PATH and SANDBOX_TLS_KEY_PATH")
		} else {
			add("pass", "transport", "production direct-tls mode is configured")
		}
	case "terminated-proxy":
		if !cfg.TrustedProxyHeaders || !strings.HasPrefix(strings.ToLower(cfg.OperatorHost), "https://") {
			add("fail", "transport", "production terminated-proxy mode requires SANDBOX_TRUST_PROXY_HEADERS=true and an https operator host")
		} else {
			add("pass", "transport", "production terminated-proxy mode is configured")
		}
	default:
		add("fail", "transport", fmt.Sprintf("unsupported production transport mode %q", cfg.ProductionTransportMode))
	}
	if doctorHostOS != "linux" {
		add("fail", "host-os", fmt.Sprintf("host OS %s is not the supported hostile-production target; production-qemu requires Linux with KVM", doctorHostOS))
	} else {
		add("pass", "host-os", "linux host detected")
	}
	reportDockerDoctor(add, cfg)
	reportKataDoctor(add, cfg)
	reportQEMUDoctor(add, cfg)
	if doctorHostOS == "linux" {
		if _, err := doctorStat("/dev/kvm"); err != nil {
			add("fail", "kvm", "/dev/kvm is not available")
		} else {
			add("pass", "kvm", "/dev/kvm is available")
		}
		checkDoctorCgroupPosture(add)
	}
	for _, root := range []string{cfg.StorageRoot, cfg.SnapshotRoot, filepath.Dir(cfg.DatabasePath)} {
		if root == "" {
			continue
		}
		info, err := doctorStat(root)
		if err != nil {
			add("fail", "path", fmt.Sprintf("required path %q is not accessible: %v", root, err))
			continue
		}
		if !info.IsDir() {
			add("fail", "path", fmt.Sprintf("required path %q is not a directory", root))
			continue
		}
		add("pass", "path", fmt.Sprintf("path %q is accessible", root))
	}
	for _, target := range []struct {
		name string
		path string
	}{
		{name: "database", path: filepath.Dir(cfg.DatabasePath)},
		{name: "storage", path: cfg.StorageRoot},
		{name: "snapshot", path: cfg.SnapshotRoot},
	} {
		checkDoctorFreeSpace(add, target.name, target.path)
	}
	for _, secret := range cfg.AuthJWTSecretPaths {
		if info, err := doctorStat(secret); err != nil {
			add("fail", "secret", fmt.Sprintf("jwt secret %q is not readable: %v", secret, err))
		} else if info.Mode().Perm()&0o077 != 0 {
			add("warn", "secret", fmt.Sprintf("jwt secret %q permissions are broader than 0600", secret))
		} else {
			add("pass", "secret", fmt.Sprintf("jwt secret %q is readable with restrictive permissions", secret))
		}
	}
	checkDoctorTunnelSigningKey(add, cfg)
	for _, root := range []struct {
		name string
		path string
	}{
		{name: "storage-root", path: cfg.StorageRoot},
		{name: "snapshot-root", path: cfg.SnapshotRoot},
		{name: "database-root", path: filepath.Dir(cfg.DatabasePath)},
	} {
		checkDoctorDirectoryPosture(add, root.name, root.path)
	}
	allowed := cfg.EffectiveQEMUAllowedBaseImagePaths()
	sort.Strings(allowed)
	if len(allowed) == 0 {
		add("fail", "images", "no approved qemu guest images are configured")
	}
	for _, imagePath := range allowed {
		if _, err := doctorStat(imagePath); err != nil {
			add("fail", "image", fmt.Sprintf("guest image %q is not readable: %v", imagePath, err))
			continue
		}
		contract, err := guestimage.Load(imagePath)
		if err != nil {
			add("fail", "image-contract", err.Error())
			continue
		}
		if err := guestimage.Validate(imagePath, contract); err != nil {
			add("fail", "image-contract", err.Error())
			continue
		}
		if contract.Control.Mode == model.GuestControlModeSSHCompat && !cfg.QEMUAllowSSHCompat {
			add("fail", "image-policy", fmt.Sprintf("image %q is ssh-compat and blocked without SANDBOX_QEMU_ALLOW_SSH_COMPAT=true", imagePath))
			continue
		}
		if contract.Profile == model.GuestProfileDebug && !cfg.QEMUAllowDangerousProfiles {
			add("fail", "image-policy", fmt.Sprintf("image %q uses debug profile and is production-ineligible by default policy", imagePath))
			continue
		}
		if cfg.IsDangerousQEMUProfile(contract.Profile) && !cfg.QEMUAllowDangerousProfiles {
			add("warn", "image-policy", fmt.Sprintf("image %q uses dangerous profile %q and is blocked until explicitly allowed", imagePath, contract.Profile))
		}
		add("pass", "image-contract", fmt.Sprintf("image %q profile=%s control=%s protocol=%s", imagePath, contract.Profile, contract.Control.Mode, contract.Control.ProtocolVersion))
	}
	return summary
}

func reportRuntimeSelections(add func(string, string, string), cfg config.Config) {
	if cfg.DefaultRuntimeSelection == "" {
		add("fail", "runtime-selection", "default runtime selection is not configured")
	} else {
		add("pass", "runtime-selection", fmt.Sprintf("default runtime selection is %q", cfg.DefaultRuntimeSelection))
	}
	if len(cfg.EnabledRuntimeSelections) == 0 {
		add("fail", "runtime-selection", "no enabled runtime selections are configured")
		return
	}
	enabled := make([]string, 0, len(cfg.EnabledRuntimeSelections))
	for _, selection := range cfg.EnabledRuntimeSelections {
		enabled = append(enabled, string(selection))
	}
	sort.Strings(enabled)
	add("pass", "runtime-selection", "enabled runtime selections: "+strings.Join(enabled, ", "))
}

func reportDockerDoctor(add func(string, string, string), cfg config.Config) {
	if !cfg.IsRuntimeSelectionEnabled(model.RuntimeSelectionDockerDev) {
		return
	}
	if !cfg.TrustedDockerRuntime {
		add("fail", "docker", "docker-dev is enabled but SANDBOX_TRUSTED_DOCKER_RUNTIME=true is required")
		return
	}
	if _, err := doctorLookPath("docker"); err != nil {
		add("fail", "docker", "docker CLI is not available")
		return
	}
	add("pass", "docker", "docker-dev prerequisites are present")
}

func reportKataDoctor(add func(string, string, string), cfg config.Config) {
	if !cfg.IsRuntimeSelectionEnabled(model.RuntimeSelectionContainerdKataProfessional) {
		return
	}
	if doctorHostOS != "linux" {
		add("fail", "kata", fmt.Sprintf("host OS %s is not supported for containerd+Kata", doctorHostOS))
		return
	}
	if strings.TrimSpace(cfg.KataBinary) == "" {
		add("fail", "kata", "SANDBOX_KATA_BINARY must be set")
		return
	}
	if _, err := doctorLookPath(cfg.KataBinary); err != nil {
		add("fail", "kata", fmt.Sprintf("kata client binary %q is not available", cfg.KataBinary))
		return
	}
	if strings.TrimSpace(cfg.KataRuntimeClass) == "" {
		add("fail", "kata", "SANDBOX_KATA_RUNTIME_CLASS must be set")
		return
	}
	if strings.TrimSpace(cfg.KataContainerdSocket) == "" {
		add("fail", "kata", "SANDBOX_KATA_CONTAINERD_SOCKET must be set")
		return
	}
	if _, err := doctorStat(cfg.KataContainerdSocket); err != nil {
		add("fail", "kata", fmt.Sprintf("containerd socket %q is not accessible: %v", cfg.KataContainerdSocket, err))
		return
	}
	add("pass", "kata", fmt.Sprintf("kata runtime class %q and containerd socket %q are configured", cfg.KataRuntimeClass, cfg.KataContainerdSocket))
}

func reportQEMUDoctor(add func(string, string, string), cfg config.Config) {
	if !cfg.IsRuntimeSelectionEnabled(model.RuntimeSelectionQEMUProfessional) {
		return
	}
	if cfg.QEMUControlMode == model.GuestControlModeSSHCompat {
		add("warn", "qemu-control", "ssh-compat is a debug and rescue path; agent mode is the normal production posture")
	} else {
		add("pass", "qemu-control", "agent mode is configured as the normal production control path")
	}
	for _, command := range []string{cfg.QEMUBinary, "qemu-img", "mkfs.ext4"} {
		if strings.TrimSpace(command) == "" {
			add("fail", "command", "SANDBOX_QEMU_BINARY must be set for production-qemu validation")
			continue
		}
		if _, err := doctorLookPath(command); err != nil {
			add("fail", "command", fmt.Sprintf("required command %q is not available", command))
		} else {
			add("pass", "command", fmt.Sprintf("found %q", command))
		}
	}
}

func doctorConfigFromEnv() config.Config {
	return config.Config{
		RuntimeBackend:             env("SANDBOX_RUNTIME", ""),
		DeploymentMode:             env("SANDBOX_MODE", ""),
		DeploymentProfile:          env("SANDBOX_DEPLOYMENT_PROFILE", ""),
		ProductionTransportMode:    env("SANDBOX_PRODUCTION_TRANSPORT", ""),
		TLSCertPath:                env("SANDBOX_TLS_CERT_PATH", ""),
		TLSKeyPath:                 env("SANDBOX_TLS_KEY_PATH", ""),
		TrustedProxyHeaders:        strings.EqualFold(env("SANDBOX_TRUST_PROXY_HEADERS", "false"), "true"),
		OperatorHost:               env("SANDBOX_OPERATOR_HOST", ""),
		AuthMode:                   env("SANDBOX_AUTH_MODE", ""),
		AuthJWTSecretPaths:         splitCommaSeparated(env("SANDBOX_AUTH_JWT_SECRET_PATHS", "")),
		DatabasePath:               env("SANDBOX_DB_PATH", ""),
		StorageRoot:                env("SANDBOX_STORAGE_ROOT", ""),
		SnapshotRoot:               env("SANDBOX_SNAPSHOT_ROOT", ""),
		TunnelSigningKey:           env("SANDBOX_TUNNEL_SIGNING_KEY", ""),
		TunnelSigningKeyPath:       env("SANDBOX_TUNNEL_SIGNING_KEY_PATH", ""),
		QEMUBinary:                 env("SANDBOX_QEMU_BINARY", ""),
		QEMUBaseImagePath:          env("SANDBOX_QEMU_BASE_IMAGE_PATH", ""),
		QEMUAllowedBaseImagePaths:  splitCommaSeparated(env("SANDBOX_QEMU_ALLOWED_BASE_IMAGE_PATHS", "")),
		QEMUDangerousProfiles:      parseDoctorGuestProfiles(env("SANDBOX_QEMU_DANGEROUS_PROFILES", "container,debug")),
		QEMUAllowDangerousProfiles: strings.EqualFold(env("SANDBOX_QEMU_ALLOW_DANGEROUS_PROFILES", "false"), "true"),
		QEMUAllowSSHCompat:         strings.EqualFold(env("SANDBOX_QEMU_ALLOW_SSH_COMPAT", "false"), "true"),
	}
}

func statDoctorFS(path string) (doctorFSInfo, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return doctorFSInfo{}, err
	}
	return doctorFSInfo{AvailableBytes: stat.Bavail * uint64(stat.Bsize)}, nil
}

func checkDoctorFreeSpace(add func(string, string, string), name, path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	info, err := doctorStatFS(path)
	if err != nil {
		add("warn", "free-space", fmt.Sprintf("%s filesystem %q could not be measured: %v", name, path, err))
		return
	}
	switch {
	case info.AvailableBytes < doctorFailFreeBytes:
		add("fail", "free-space", fmt.Sprintf("%s filesystem %q has only %s free; keep at least %s free for supported production operation", name, path, humanBytes(info.AvailableBytes), humanBytes(doctorFailFreeBytes)))
	case info.AvailableBytes < doctorWarnFreeBytes:
		add("warn", "free-space", fmt.Sprintf("%s filesystem %q has %s free; production operators should keep at least %s free", name, path, humanBytes(info.AvailableBytes), humanBytes(doctorWarnFreeBytes)))
	default:
		add("pass", "free-space", fmt.Sprintf("%s filesystem %q has %s free", name, path, humanBytes(info.AvailableBytes)))
	}
}

func checkDoctorTunnelSigningKey(add func(string, string, string), cfg config.Config) {
	switch {
	case strings.TrimSpace(cfg.TunnelSigningKeyPath) != "":
		info, err := doctorStat(cfg.TunnelSigningKeyPath)
		if err != nil {
			add("fail", "tunnel-signing-key", fmt.Sprintf("tunnel signing key path %q is not readable: %v", cfg.TunnelSigningKeyPath, err))
			return
		}
		if info.Mode().Perm()&0o077 != 0 {
			add("warn", "tunnel-signing-key", fmt.Sprintf("tunnel signing key %q permissions are broader than 0600", cfg.TunnelSigningKeyPath))
		} else {
			add("pass", "tunnel-signing-key", fmt.Sprintf("tunnel signing key %q is readable with restrictive permissions", cfg.TunnelSigningKeyPath))
		}
		checkDoctorDirectoryPosture(add, "tunnel-signing-key-parent", filepath.Dir(cfg.TunnelSigningKeyPath))
	case strings.TrimSpace(cfg.TunnelSigningKey) != "":
		add("warn", "tunnel-signing-key", "inline tunnel signing key is configured; prefer SANDBOX_TUNNEL_SIGNING_KEY_PATH for read-only production posture")
	default:
		add("warn", "tunnel-signing-key", "no tunnel signing key is configured; signed tunnel URLs and browser bootstrap cookies will not survive process restarts")
	}
}

func checkDoctorDirectoryPosture(add func(string, string, string), name, path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	info, err := doctorStat(path)
	if err != nil {
		add("warn", "path-posture", fmt.Sprintf("%s %q is not accessible for posture checks: %v", name, path, err))
		return
	}
	if !info.IsDir() {
		add("warn", "path-posture", fmt.Sprintf("%s %q is not a directory", name, path))
		return
	}
	if info.Mode().Perm()&0o022 != 0 {
		add("warn", "path-posture", fmt.Sprintf("%s %q is group/world writable; tighten parent-directory permissions", name, path))
		return
	}
	add("pass", "path-posture", fmt.Sprintf("%s %q is not group/world writable", name, path))
}

func checkDoctorCgroupPosture(add func(string, string, string)) {
	root := "/sys/fs/cgroup"
	if _, err := doctorStat(root); err != nil {
		add("warn", "cgroup", fmt.Sprintf("%s is not accessible: %v", root, err))
		return
	}
	data, err := doctorReadFile(filepath.Join(root, "cgroup.controllers"))
	if err != nil {
		add("warn", "cgroup", fmt.Sprintf("cgroup v2 controller list is not readable: %v", err))
		return
	}
	controllers := strings.Fields(string(data))
	if len(controllers) == 0 {
		add("warn", "cgroup", "cgroup v2 controller list is empty")
		return
	}
	required := []string{"cpu", "memory", "pids"}
	missing := make([]string, 0, len(required))
	for _, controller := range required {
		if !containsString(controllers, controller) {
			missing = append(missing, controller)
		}
	}
	if len(missing) > 0 {
		add("warn", "cgroup", fmt.Sprintf("cgroup v2 is present but missing controllers: %s", strings.Join(missing, ", ")))
		return
	}
	add("pass", "cgroup", fmt.Sprintf("cgroup v2 controllers available: %s", strings.Join(required, ", ")))
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func humanBytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := uint64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}

func splitCommaSeparated(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		result = append(result, part)
	}
	return result
}

func parseDoctorGuestProfiles(raw string) []model.GuestProfile {
	values := splitCommaSeparated(raw)
	profiles := make([]model.GuestProfile, 0, len(values))
	for _, value := range values {
		profile := model.GuestProfile(strings.ToLower(strings.TrimSpace(value)))
		if profile.IsValid() {
			profiles = append(profiles, profile)
		}
	}
	return profiles
}
