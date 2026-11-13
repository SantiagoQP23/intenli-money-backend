// Command api runs the Intenli Finance HTTP API.
//
// Usage:
//
//	api            start the HTTP server (runs pending migrations first)
//	api migrate    apply pending migrations and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SantiagoQP23/intenli-money-backend/internal/auth"
	"github.com/SantiagoQP23/intenli-money-backend/internal/config"
	"github.com/SantiagoQP23/intenli-money-backend/internal/database"
	"github.com/SantiagoQP23/intenli-money-backend/internal/httpapi"
	"github.com/SantiagoQP23/intenli-money-backend/migrations"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(args) > 0 && args[0] == "migrate" {
		return migrations.Up(ctx, cfg.DatabaseURL)
	}
	if len(args) > 0 {
		return fmt.Errorf("unknown command %q", args[0])
	}

	if cfg.AutoMigrate {
		if err := migrations.Up(ctx, cfg.DatabaseURL); err != nil {
			return err
		}
	}

	if len(cfg.GoogleClientIDs) == 0 {
		logger.Warn("GOOGLE_CLIENT_IDS is empty: Google sign-in is disabled")
	}

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Logger:        logger,
			DB:            pool,
			Pool:          pool,
			CORSOrigins:   cfg.CORSOrigins,
			AccessTokens:  auth.NewAccessTokens(cfg.JWTSecret, cfg.AccessTokenTTL),
			Google:        auth.NewGoogleVerifier(cfg.GoogleClientIDs),
			RefreshTTL:    cfg.RefreshTokenTTL,
			InvitationTTL: cfg.InvitationTTL,
			AuthRateLimit: cfg.AuthRateLimit,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.Env == config.EnvDevelopment {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
