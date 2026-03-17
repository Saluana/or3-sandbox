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
	runtimekata "or3-sandbox/internal/runtime/kata"
	runtimeqemu "or3-sandbox/internal/runtime/qemu"
	"or3-sandbox/internal/runtime/registry"
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
		log.Info("daemon listening", "event", "daemon.listen", "addr", cfg.ListenAddress, "default_runtime", cfg.DefaultRuntimeSelection, "enabled_runtimes", cfg.EnabledRuntimeSelections, "runtime_class", string(cfg.RuntimeClass()), "mode", cfg.DeploymentMode, "deployment_profile", cfg.DeploymentProfile, "auth_mode", cfg.AuthMode, "tls_enabled", cfg.TLSCertPath != "", "trusted_proxy", cfg.TrustedProxyHeaders, "production_transport_mode", cfg.ProductionTransportMode)
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
	runtimes := make(map[model.RuntimeSelection]model.RuntimeManager, len(cfg.EnabledRuntimeSelections))
	for _, selection := range cfg.EnabledRuntimeSelections {
		switch selection {
		case model.RuntimeSelectionDockerDev:
			runtimes[selection] = runtimedocker.New(runtimedocker.Options{
				User:                      cfg.DockerUser,
				TmpfsSizeMB:               cfg.DockerTmpfsSizeMB,
				SeccompProfile:            cfg.DockerSeccompProfile,
				AppArmorProfile:           cfg.DockerAppArmorProfile,
				SELinuxLabel:              cfg.DockerSELinuxLabel,
				AllowDangerousOverrides:   cfg.DockerAllowDangerousOverrides,
				SnapshotMaxBytes:          cfg.SnapshotMaxBytes,
				SnapshotMaxFiles:          cfg.SnapshotMaxFiles,
				SnapshotMaxExpansionRatio: cfg.SnapshotMaxExpansionRatio,
			})
		case model.RuntimeSelectionQEMUProfessional:
			rt, err := runtimeqemu.New(runtimeqemu.Options{
				Binary:                        cfg.QEMUBinary,
				Accel:                         cfg.QEMUAccel,
				BaseImagePath:                 cfg.QEMUBaseImagePath,
				ControlMode:                   cfg.QEMUControlMode,
				SSHUser:                       cfg.QEMUSSHUser,
				SSHKeyPath:                    cfg.QEMUSSHPrivateKeyPath,
				SSHHostKeyPath:                cfg.QEMUSSHHostKeyPath,
				BootTimeout:                   cfg.QEMUBootTimeout,
				WorkspaceFileTransferMaxBytes: cfg.WorkspaceFileTransferMaxBytes,
				TraceProtocol:                 cfg.QEMUAgentTrace,
			})
			if err != nil {
				return nil, err
			}
			runtimes[selection] = rt
		case model.RuntimeSelectionContainerdKataProfessional:
			runtimes[selection] = runtimekata.New(runtimekata.Options{
				Binary:                    cfg.KataBinary,
				RuntimeClass:              cfg.KataRuntimeClass,
				ContainerdSocket:          cfg.KataContainerdSocket,
				SnapshotMaxBytes:          cfg.SnapshotMaxBytes,
				SnapshotMaxFiles:          cfg.SnapshotMaxFiles,
				SnapshotMaxExpansionRatio: cfg.SnapshotMaxExpansionRatio,
			})
		default:
			return nil, errors.New("unsupported runtime selection")
		}
	}
	return registry.New(runtimes), nil
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
