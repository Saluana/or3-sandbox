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
)

type doctorCheck struct {
	Level  string `json:"level"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

type doctorSummary struct {
	Mode      string        `json:"mode"`
	CheckedAt time.Time     `json:"checked_at"`
	Checks    []doctorCheck `json:"checks"`
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	productionQEMU := fs.Bool("production-qemu", false, "validate the production QEMU host and image profile posture")
	jsonOutput := fs.Bool("json", false, "print doctor results as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*productionQEMU {
		return errors.New("usage: sandboxctl doctor --production-qemu [--json]")
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
	if cfg.RuntimeBackend != "qemu" {
		add("fail", "runtime", "SANDBOX_RUNTIME must be qemu for production-qemu validation")
	} else {
		add("pass", "runtime", "runtime backend is qemu")
	}
	if cfg.AuthMode != "jwt-hs256" {
		add("fail", "auth", "production qemu requires SANDBOX_AUTH_MODE=jwt-hs256")
	} else {
		add("pass", "auth", "jwt auth is enabled")
	}
	if doctorHostOS != "linux" {
		add("fail", "host-os", fmt.Sprintf("host OS %s is not the supported hostile-production target; production-qemu requires Linux with KVM", doctorHostOS))
	} else {
		add("pass", "host-os", "linux host detected")
	}
	for _, command := range []string{cfg.QEMUBinary, "qemu-img"} {
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
	if doctorHostOS == "linux" {
		if _, err := doctorStat("/dev/kvm"); err != nil {
			add("fail", "kvm", "/dev/kvm is not available")
		} else {
			add("pass", "kvm", "/dev/kvm is available")
		}
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
	for _, secret := range cfg.AuthJWTSecretPaths {
		if info, err := doctorStat(secret); err != nil {
			add("fail", "secret", fmt.Sprintf("jwt secret %q is not readable: %v", secret, err))
		} else if info.Mode().Perm()&0o077 != 0 {
			add("warn", "secret", fmt.Sprintf("jwt secret %q permissions are broader than 0600", secret))
		} else {
			add("pass", "secret", fmt.Sprintf("jwt secret %q is readable with restrictive permissions", secret))
		}
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

func doctorConfigFromEnv() config.Config {
	return config.Config{
		RuntimeBackend:             env("SANDBOX_RUNTIME", ""),
		AuthMode:                   env("SANDBOX_AUTH_MODE", ""),
		AuthJWTSecretPaths:         splitCommaSeparated(env("SANDBOX_AUTH_JWT_SECRET_PATHS", "")),
		DatabasePath:               env("SANDBOX_DB_PATH", ""),
		StorageRoot:                env("SANDBOX_STORAGE_ROOT", ""),
		SnapshotRoot:               env("SANDBOX_SNAPSHOT_ROOT", ""),
		QEMUBinary:                 env("SANDBOX_QEMU_BINARY", ""),
		QEMUBaseImagePath:          env("SANDBOX_QEMU_BASE_IMAGE_PATH", ""),
		QEMUAllowedBaseImagePaths:  splitCommaSeparated(env("SANDBOX_QEMU_ALLOWED_BASE_IMAGE_PATHS", "")),
		QEMUDangerousProfiles:      parseDoctorGuestProfiles(env("SANDBOX_QEMU_DANGEROUS_PROFILES", "container,debug")),
		QEMUAllowDangerousProfiles: strings.EqualFold(env("SANDBOX_QEMU_ALLOW_DANGEROUS_PROFILES", "false"), "true"),
		QEMUAllowSSHCompat:         strings.EqualFold(env("SANDBOX_QEMU_ALLOW_SSH_COMPAT", "false"), "true"),
	}
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
