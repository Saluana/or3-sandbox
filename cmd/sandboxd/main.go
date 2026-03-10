package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"or3-sandbox/internal/api"
	"or3-sandbox/internal/auth"
	"or3-sandbox/internal/config"
	"or3-sandbox/internal/db"
	"or3-sandbox/internal/logging"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
	runtimedocker "or3-sandbox/internal/runtime/docker"
	runtimeqemu "or3-sandbox/internal/runtime/qemu"
	"or3-sandbox/internal/service"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		panic(err)
	}
	rootLog := logging.New()
	log := rootLog.With("component", "daemon")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sqlDB, err := db.Open(ctx, cfg.DatabasePath)
	if err != nil {
		log.Error("database open failed", "event", "database.open", "error", err)
		os.Exit(1)
	}
	defer sqlDB.Close()

	store := repository.New(sqlDB)
	if err := store.SeedTenants(ctx, cfg.Tenants, cfg.DefaultQuota); err != nil {
		log.Error("tenant seed failed", "event", "tenant.seed", "error", err)
		os.Exit(1)
	}

	runtime, err := buildRuntime(cfg)
	if err != nil {
		log.Error("runtime configure failed", "event", "runtime.configure", "runtime", cfg.RuntimeBackend, "error", err)
		os.Exit(1)
	}
	svc := service.New(cfg, store, runtime, rootLog.With("component", "service"))
	if err := svc.Reconcile(ctx); err != nil {
		log.Error("initial reconcile failed", "event", "reconcile.initial", "error", err)
	}

	go reconcileLoop(ctx, log, svc, cfg.ReconcileInterval)

	handler := auth.New(store, cfg, rootLog.With("component", "auth")).Wrap(api.New(rootLog.With("component", "api"), svc, cfg))
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Info("daemon listening", "event", "daemon.listen", "addr", cfg.ListenAddress, "runtime", cfg.RuntimeBackend, "runtime_class", string(cfg.RuntimeClass()), "mode", cfg.DeploymentMode, "auth_mode", cfg.AuthMode, "tls_enabled", cfg.TLSCertPath != "", "trusted_proxy", cfg.TrustedProxyHeaders)
		var err error
		if cfg.TLSCertPath != "" {
			err = server.ListenAndServeTLS(cfg.TLSCertPath, cfg.TLSKeyPath)
		} else {
			err = server.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "event", "daemon.serve", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.GracefulShutdown)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown failed", "event", "daemon.shutdown", "error", err)
	}
}

func buildRuntime(cfg config.Config) (model.RuntimeManager, error) {
	switch cfg.RuntimeBackend {
	case "docker":
		return runtimedocker.New(runtimedocker.Options{
			User:                      cfg.DockerUser,
			TmpfsSizeMB:               cfg.DockerTmpfsSizeMB,
			SeccompProfile:            cfg.DockerSeccompProfile,
			AppArmorProfile:           cfg.DockerAppArmorProfile,
			SELinuxLabel:              cfg.DockerSELinuxLabel,
			AllowDangerousOverrides:   cfg.DockerAllowDangerousOverrides,
			SnapshotMaxBytes:          cfg.SnapshotMaxBytes,
			SnapshotMaxFiles:          cfg.SnapshotMaxFiles,
			SnapshotMaxExpansionRatio: cfg.SnapshotMaxExpansionRatio,
		}), nil
	case "qemu":
		return runtimeqemu.New(runtimeqemu.Options{
			Binary:         cfg.QEMUBinary,
			Accel:          cfg.QEMUAccel,
			BaseImagePath:  cfg.QEMUBaseImagePath,
			ControlMode:    cfg.QEMUControlMode,
			SSHUser:        cfg.QEMUSSHUser,
			SSHKeyPath:     cfg.QEMUSSHPrivateKeyPath,
			SSHHostKeyPath: cfg.QEMUSSHHostKeyPath,
			BootTimeout:    cfg.QEMUBootTimeout,
		})
	default:
		return nil, errors.New("unsupported runtime backend")
	}
}

func reconcileLoop(ctx context.Context, log *slog.Logger, svc *service.Service, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := svc.Reconcile(ctx); err != nil {
				log.Error("reconcile failed", "event", "reconcile.tick", "error", err)
			}
		}
	}
}
