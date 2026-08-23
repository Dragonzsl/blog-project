package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/zhushilin/blog-project/internal/buildinfo"
	"github.com/zhushilin/blog-project/internal/identity"
	"github.com/zhushilin/blog-project/internal/operations"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/httpx"
	"github.com/zhushilin/blog-project/internal/platform/secrets"
)

type App struct {
	config   config.Config
	logger   *slog.Logger
	database *database.DB
	server   *http.Server
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return nil, err
	}
	authSecret, err := secrets.LoadOrCreateAuthSecret(cfg.Security)
	if err != nil {
		db.Close()
		return nil, err
	}
	identityService, err := identity.NewService(identity.NewRepository(db), authSecret, cfg.Security.SessionLifetime.Duration)
	if err != nil {
		db.Close()
		return nil, err
	}
	identityHTTP, err := identity.NewHTTPHandler(identityService, cfg.Security, logger)
	if err != nil {
		db.Close()
		return nil, err
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.CleanPath)
	router.Use(httpx.Recoverer(logger))
	router.Use(httpx.AccessLog(logger))
	health := operations.NewHealthHandler(db, time.Now(), buildinfo.Version)
	router.Get("/livez", health.Live)
	router.Get("/readyz", health.Ready)
	router.Mount("/admin", identityHTTP.Routes())

	server := &http.Server{
		Addr:              cfg.Server.ListenAddress,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return &App{config: cfg, logger: logger, database: db, server: server}, nil
}

func (app *App) Run(ctx context.Context) error {
	serverErrors := make(chan error, 1)
	go func() {
		app.logger.Info("server listening", "address", app.server.Addr)
		serverErrors <- app.server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), app.config.Server.ShutdownTimeout.Duration)
		defer cancel()
		if err := app.server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		return nil
	}
}

func (app *App) Close() error {
	return app.database.Close()
}
