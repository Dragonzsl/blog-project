package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/zhushilin/blog-project/internal/buildinfo"
	"github.com/zhushilin/blog-project/internal/identity"
	"github.com/zhushilin/blog-project/internal/media"
	"github.com/zhushilin/blog-project/internal/operations"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/httpx"
	"github.com/zhushilin/blog-project/internal/platform/secrets"
	"github.com/zhushilin/blog-project/internal/presentation"
	"github.com/zhushilin/blog-project/internal/publishing"
)

type App struct {
	config               config.Config
	logger               *slog.Logger
	database             *database.DB
	server               *http.Server
	publishing           *publishing.Service
	lifecycleInterval    time.Duration
	trashCleanupInterval time.Duration
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
	publishingService := publishing.NewService(publishing.NewRepository(db), publishing.Options{
		SchedulerBatchSize: cfg.Publishing.SchedulerBatchSize,
		SnapshotInterval:   cfg.Publishing.EditingSnapshotInterval.Duration,
		RevisionLimit:      cfg.Publishing.RevisionLimit,
		TrashRetention:     time.Duration(cfg.Publishing.TrashRetentionDays) * 24 * time.Hour,
	})
	publishingHTTP, err := publishing.NewHTTPHandler(publishingService, identityHTTP, identityService, logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	organizationService := organization.NewService(db)
	organizationHTTP, err := organization.NewHTTPHandler(organizationService, publishingService, identityHTTP, identityService, logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	mediaService, err := media.NewService(db, filepath.Join(cfg.Storage.DataDir, "media"), logger, media.Options{
		MaxUploadBytes: int64(cfg.Media.MaxUploadBytes),
		MaxImagePixels: cfg.Media.MaxImagePixels,
		VariantWidths:  cfg.Media.VariantWidths,
		JPEGQuality:    cfg.Media.JPEGQuality,
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	mediaHTTP, err := media.NewHTTPHandler(mediaService, identityHTTP, identityService, logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	defaultTheme, err := presentation.NewDefaultTheme(presentation.NewMarkdown())
	if err != nil {
		db.Close()
		return nil, err
	}
	pageCache, err := presentation.NewPageCache(filepath.Join(cfg.Storage.DataDir, "cache", "pages"), 32, 4<<20)
	if err != nil {
		db.Close()
		return nil, err
	}
	presentationHTTP := presentation.NewHTTPHandler(
		publishingService,
		identityService,
		presentation.NewStateRepository(db),
		defaultTheme,
		pageCache,
		logger,
		organizationService,
	)

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.CleanPath)
	router.Use(httpx.Recoverer(logger))
	router.Use(httpx.AccessLog(logger))
	health := operations.NewHealthHandler(db, time.Now(), buildinfo.Version)
	router.Get("/livez", health.Live)
	router.Get("/readyz", health.Ready)
	mediaHTTP.RegisterPublic(router)
	presentationHTTP.RegisterPublic(router)
	router.Route("/admin", func(admin chi.Router) {
		admin.Use(identityHTTP.SecurityHeaders)
		identityHTTP.RegisterPublic(admin)
		admin.Group(func(protected chi.Router) {
			protected.Use(identityHTTP.RequireSession)
			identityHTTP.RegisterProtected(protected)
			publishingHTTP.RegisterAdmin(protected)
			organizationHTTP.RegisterAdmin(protected)
			mediaHTTP.RegisterAdmin(protected)
			presentationHTTP.RegisterAdmin(protected)
		})
	})

	server := &http.Server{
		Addr:              cfg.Server.ListenAddress,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return &App{config: cfg, logger: logger, database: db, server: server, publishing: publishingService, lifecycleInterval: cfg.Publishing.SchedulerInterval.Duration, trashCleanupInterval: cfg.Publishing.TrashCleanupInterval.Duration}, nil
}

func (app *App) Run(ctx context.Context) error {
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	serverErrors := make(chan error, 1)
	go func() {
		app.logger.Info("server listening", "address", app.server.Addr)
		serverErrors <- app.server.ListenAndServe()
	}()
	lifecycleDone := make(chan struct{})
	go func() {
		defer close(lifecycleDone)
		app.runLifecycle(runContext)
	}()

	select {
	case err := <-serverErrors:
		cancel()
		<-lifecycleDone
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		cancel()
		shutdownContext, cancel := context.WithTimeout(context.Background(), app.config.Server.ShutdownTimeout.Duration)
		defer cancel()
		if err := app.server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		<-lifecycleDone
		return nil
	}
}

func (app *App) runLifecycle(ctx context.Context) {
	processScheduled := func() {
		published, err := app.publishing.ProcessScheduled(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			app.logger.ErrorContext(ctx, "process scheduled publishing", "error", err)
			return
		}
		if published > 0 {
			app.logger.InfoContext(ctx, "scheduled publishing processed", "published", published)
		}
	}
	cleanupTrash := func() {
		purged, err := app.publishing.PurgeExpiredTrash(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			app.logger.ErrorContext(ctx, "purge expired trash", "error", err)
			return
		}
		if purged > 0 {
			app.logger.InfoContext(ctx, "expired trash purged", "purged", purged)
		}
	}
	processScheduled()
	cleanupTrash()
	schedulerTicker := time.NewTicker(app.lifecycleInterval)
	defer schedulerTicker.Stop()
	cleanupTicker := time.NewTicker(app.trashCleanupInterval)
	defer cleanupTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-schedulerTicker.C:
			processScheduled()
		case <-cleanupTicker.C:
			cleanupTrash()
		}
	}
}

func (app *App) Close() error {
	return app.database.Close()
}
