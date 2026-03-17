package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/db"
	"or3-sandbox/internal/guestimage"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
)

func runConfigLint(args []string) error {
	fs := flag.NewFlagSet("config-lint", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(nil)
	if err != nil {
		return err
	}
	result := map[string]any{
		"ok":                         true,
		"deployment_mode":            cfg.DeploymentMode,
		"deployment_profile":         cfg.DeploymentProfile,
		"production_transport_mode":  cfg.ProductionTransportMode,
		"default_runtime_selection":  cfg.DefaultRuntimeSelection,
		"enabled_runtime_selections": cfg.EnabledRuntimeSelections,
	}
	if *jsonOutput {
		return printJSON(result)
	}
	fmt.Fprintf(os.Stdout, "config lint ok: mode=%s profile=%s transport=%s default_runtime=%s\n", cfg.DeploymentMode, cfg.DeploymentProfile, cfg.ProductionTransportMode, cfg.DefaultRuntimeSelection)
	return nil
}

var (
	qemuLocateRepoRoot = locateRepoRoot
	qemuExecCommand    = exec.Command
)

func runQEMU(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sandboxctl qemu <init|smoke>")
	}
	switch args[0] {
	case "init":
		return runQEMUInit(args[1:])
	case "smoke":
		return runQEMUSmoke(args[1:])
	default:
		return errors.New("usage: sandboxctl qemu <init|smoke>")
	}
}

func runQEMUInit(args []string) error {
	return runQEMUScript("install-qemu-runtime.sh", args)
}

func runQEMUSmoke(args []string) error {
	return runQEMUScript("qemu-production-smoke.sh", args)
}

func runQEMUScript(name string, args []string) error {
	root, err := qemuLocateRepoRoot()
	if err != nil {
		return err
	}
	command := qemuExecCommand(filepath.Join(root, "scripts", name), args...)
	command.Env = append(os.Environ(), "SANDBOXCTL_BIN="+os.Args[0])
	command.Dir = root
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	return command.Run()
}

func runImage(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sandboxctl image <promote|list>")
	}
	switch args[0] {
	case "promote":
		return runImagePromote(args[1:])
	case "list":
		return runImageList(args[1:])
	default:
		return errors.New("usage: sandboxctl image <promote|list>")
	}
}

func runImagePromote(args []string) error {
	fs := flag.NewFlagSet("image promote", flag.ContinueOnError)
	imagePath := fs.String("image", "", "guest image path")
	promotedBy := fs.String("promoted-by", "sandboxctl", "promotion actor")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*imagePath) == "" {
		return errors.New("usage: sandboxctl image promote --image <path> [--promoted-by <actor>]")
	}
	cfg, store, cleanup, err := loadConfigStore()
	if err != nil {
		return err
	}
	defer cleanup()
	resolved := config.NormalizeQEMUBaseImagePath(*imagePath)
	contract, err := guestimage.Load(resolved)
	if err != nil {
		return err
	}
	if err := guestimage.Validate(resolved, contract); err != nil {
		return err
	}
	now := time.Now().UTC()
	provenanceJSON, _ := json.Marshal(contract.Provenance)
	record := model.PromotedGuestImage{
		ImageRef:               resolved,
		ImageSHA256:            contract.ImageSHA256,
		Profile:                contract.Profile,
		ControlMode:            contract.Control.Mode,
		ControlProtocolVersion: contract.Control.ProtocolVersion,
		ContractVersion:        contract.ContractVersion,
		ProvenanceJSON:         string(provenanceJSON),
		VerificationStatus:     "verified",
		PromotionStatus:        "promoted",
		PromotedAt:             &now,
		PromotedBy:             *promotedBy,
	}
	if err := store.UpsertPromotedGuestImage(context.Background(), record); err != nil {
		return err
	}
	return printJSON(map[string]any{
		"ok":        true,
		"image_ref": record.ImageRef,
		"profile":   record.Profile,
		"sha256":    record.ImageSHA256,
		"mode":      cfg.DeploymentMode,
	})
}

func runImageList(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: sandboxctl image list")
	}
	_, store, cleanup, err := loadConfigStore()
	if err != nil {
		return err
	}
	defer cleanup()
	images, err := store.ListPromotedGuestImages(context.Background())
	if err != nil {
		return err
	}
	return printJSON(images)
}

func runReleaseGate(args []string) error {
	fs := flag.NewFlagSet("release-gate", flag.ContinueOnError)
	artifactDir := fs.String("artifact-dir", "", "directory to store release gate logs")
	gateName := fs.String("name", "production-qemu", "gate name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, store, cleanup, err := loadConfigStore()
	if err != nil {
		return err
	}
	defer cleanup()
	root, err := locateRepoRoot()
	if err != nil {
		return err
	}
	if strings.TrimSpace(*artifactDir) == "" {
		*artifactDir = filepath.Join(root, "data", "release-evidence")
	}
	if err := os.MkdirAll(*artifactDir, 0o755); err != nil {
		return err
	}
	scripts := []string{
		filepath.Join(root, "scripts", "production-smoke.sh"),
		filepath.Join(root, "scripts", "qemu-host-verification.sh"),
		filepath.Join(root, "scripts", "qemu-production-smoke.sh"),
		filepath.Join(root, "scripts", "qemu-recovery-drill.sh"),
	}
	for _, script := range scripts {
		startedAt := time.Now().UTC()
		logPath := filepath.Join(*artifactDir, filepath.Base(script)+".log")
		cmd := exec.Command(script)
		output, err := cmd.CombinedOutput()
		if writeErr := os.WriteFile(logPath, output, 0o644); writeErr != nil {
			return writeErr
		}
		outcome := "ok"
		if err != nil {
			outcome = "failed"
		}
		completedAt := time.Now().UTC()
		if createErr := store.CreateReleaseEvidence(context.Background(), model.ReleaseEvidence{
			ID:               fmt.Sprintf("gate-%d", startedAt.UnixNano()),
			GateName:         *gateName,
			HostFingerprint:  hostFingerprint(),
			RuntimeSelection: cfg.DefaultRuntimeSelection,
			Outcome:          outcome,
			ArtifactPath:     logPath,
			StartedAt:        startedAt,
			CompletedAt:      &completedAt,
		}); createErr != nil {
			return createErr
		}
		if err != nil {
			return fmt.Errorf("%s failed: %w", filepath.Base(script), err)
		}
	}
	evidence, err := store.ListReleaseEvidence(context.Background(), *gateName)
	if err != nil {
		return err
	}
	return printJSON(evidence)
}

func loadConfigStore() (config.Config, *repository.Store, func(), error) {
	cfg, err := config.Load(nil)
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	sqlDB, err := db.Open(context.Background(), cfg.DatabasePath)
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	return cfg, repository.New(sqlDB), func() { _ = sqlDB.Close() }, nil
}

func locateRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	current := wd
	for {
		if _, err := os.Stat(filepath.Join(current, "scripts", "production-smoke.sh")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("could not locate repository root")
		}
		current = parent
	}
}

func hostFingerprint() string {
	hostname, _ := os.Hostname()
	return fmt.Sprintf("%s/%s:%s", runtime.GOOS, runtime.GOARCH, hostname)
}
