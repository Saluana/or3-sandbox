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
	runtimedocker "or3-sandbox/internal/runtime/docker"
	"or3-sandbox/internal/service"
	"or3-sandbox/internal/repository"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		panic(err)
	}
	log := logging.New()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sqlDB, err := db.Open(ctx, cfg.DatabasePath)
	if err != nil {
		log.Error("open database", "error", err)
		os.Exit(1)
	}
	defer sqlDB.Close()

	store := repository.New(sqlDB)
	if err := store.SeedTenants(ctx, cfg.Tenants, cfg.DefaultQuota); err != nil {
		log.Error("seed tenants", "error", err)
		os.Exit(1)
	}

	runtime := runtimedocker.New()
	svc := service.New(cfg, store, runtime)
	if err := svc.Reconcile(ctx); err != nil {
		log.Error("initial reconcile failed", "error", err)
	}

	go reconcileLoop(ctx, log, svc, cfg.ReconcileInterval)

	handler := auth.New(store, cfg).Wrap(api.New(log, svc))
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Info("sandboxd listening", "addr", cfg.ListenAddress, "runtime", cfg.RuntimeBackend)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.GracefulShutdown)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown failed", "error", err)
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
				log.Error("reconcile failed", "error", err)
			}
		}
	}
}
