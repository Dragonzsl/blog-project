package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/zhushilin/blog-project/internal/analytics"
	"github.com/zhushilin/blog-project/internal/buildinfo"
	"github.com/zhushilin/blog-project/internal/comments"
	"github.com/zhushilin/blog-project/internal/contentapi"
	"github.com/zhushilin/blog-project/internal/discovery"
	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/identity"
	"github.com/zhushilin/blog-project/internal/media"
	"github.com/zhushilin/blog-project/internal/notifications"
	"github.com/zhushilin/blog-project/internal/operations"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/clientip"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/httpx"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
	"github.com/zhushilin/blog-project/internal/platform/publicwrite"
	"github.com/zhushilin/blog-project/internal/platform/secrets"
	"github.com/zhushilin/blog-project/internal/presentation"
	"github.com/zhushilin/blog-project/internal/publishing"
	"github.com/zhushilin/blog-project/internal/webhooks"
)

type App struct {
	config               config.Config
	logger               *slog.Logger
	database             *database.DB
	server               *http.Server
	publishing           *publishing.Service
	organization         *organization.Service
	discovery            *discovery.Service
	backups              *operations.BackupService
	themeManager         *presentation.ThemeManager
	extensions           *extensions.Registry
	analytics            *analytics.Service
	outbox               *notifications.Outbox
	publicWrites         *publicwrite.Guard
	dataLock             *operations.DataLock
	lifecycleInterval    time.Duration
	trashCleanupInterval time.Duration
	backupInterval       time.Duration
}

type gatedAnalyticsRecorder struct {
	registry *extensions.Registry
	service  *analytics.Service
}

func (r gatedAnalyticsRecorder) Record(ctx context.Context, path, visitor string) error {
	if r.registry == nil || r.service == nil || !r.registry.Enabled("analytics.local") {
		return nil
	}
	return r.service.Record(ctx, path, visitor)
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	dataLock, err := operations.AcquireDataLock(cfg.Storage.DataDir)
	if err != nil {
		return nil, err
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = dataLock.Close()
		}
	}()
	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return nil, err
	}
	authSecret, err := secrets.LoadOrCreateAuthSecret(cfg.Security)
	if err != nil {
		db.Close()
		return nil, err
	}
	publicWriteGuard := publicwrite.NewGuard(db, authSecret)
	if err := notifications.RekeyNewsletterHashes(ctx, db, authSecret); err != nil {
		db.Close()
		return nil, fmt.Errorf("rekey newsletter hashes: %w", err)
	}
	identityService, err := identity.NewService(identity.NewRepository(db), authSecret, cfg.Security.SessionLifetime.Duration)
	if err != nil {
		db.Close()
		return nil, err
	}
	clientIPResolver, err := clientip.New(cfg.Server.TrustedProxyCIDRs)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("configure trusted proxy CIDRs: %w", err)
	}
	identityHTTP, err := identity.NewHTTPHandler(identityService, cfg.Security, logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	mailer, err := notifications.NewSMTPSender(notifications.SMTPConfig{Enabled: cfg.Mail.Enabled, Host: cfg.Mail.Host, Port: cfg.Mail.Port, Username: cfg.Mail.Username, Password: cfg.Mail.Password, From: cfg.Mail.From, StartTLS: cfg.Mail.StartTLS})
	if err != nil {
		db.Close()
		return nil, err
	}
	outbox := notifications.NewOutbox(db, mailer)
	publishingService := publishing.NewService(publishing.NewRepository(db), publishing.Options{
		SchedulerBatchSize: cfg.Publishing.SchedulerBatchSize,
		SnapshotInterval:   cfg.Publishing.EditingSnapshotInterval.Duration,
		RevisionLimit:      cfg.Publishing.RevisionLimit,
		TrashRetention:     time.Duration(cfg.Publishing.TrashRetentionDays) * 24 * time.Hour,
	})
	identityHTTP.SetDashboardQueries(publishingService)
	identityHTTP.SetClientIPResolver(clientIPResolver)
	publishingHTTP, err := publishing.NewHTTPHandler(publishingService, identityHTTP, identityService, logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	organizationService := organization.NewService(db)
	discoveryService, err := discovery.NewService(discovery.NewRepository(db), discovery.Options{
		BaseURL:       cfg.Discovery.BaseURL,
		SyncBatchSize: cfg.Discovery.SyncBatchSize,
		MaxResults:    cfg.Discovery.MaxResults,
		FeedLimit:     cfg.Discovery.FeedLimit,
		SitemapLimit:  cfg.Discovery.SitemapLimit,
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	var initialSiteSettings identity.SiteSettings
	if siteSettings, settingsErr := identityService.SiteSettings(ctx); settingsErr != nil {
		db.Close()
		return nil, fmt.Errorf("read site settings: %w", settingsErr)
	} else {
		initialSiteSettings = siteSettings
		if strings.TrimSpace(siteSettings.BaseURL) != "" {
			if err := discoveryService.SetBaseURL(siteSettings.BaseURL); err != nil {
				db.Close()
				return nil, fmt.Errorf("configure site base URL: %w", err)
			}
		}
	}
	var backupStore operations.BackupStore
	if cfg.Operations.Backup.Adapter == "s3" {
		backupStorage, storageErr := media.NewS3Storage(cfg.Operations.Backup.S3.Endpoint, cfg.Operations.Backup.S3.Bucket, cfg.Operations.Backup.S3.Region, cfg.Operations.Backup.S3.AccessKey, cfg.Operations.Backup.S3.SecretKey, cfg.Operations.Backup.S3.Prefix, cfg.Operations.Backup.S3.ForcePathStyle, cfg.Operations.Backup.S3.UseTLS)
		if storageErr != nil {
			db.Close()
			return nil, fmt.Errorf("configure backup storage: %w", storageErr)
		}
		backupStore, err = operations.NewMediaStorageBackupStore(backupStorage, cfg.Operations.Backup.Prefix)
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	backupService, err := operations.NewBackupService(db, operations.BackupOptions{
		DataDir: cfg.Storage.DataDir, DatabasePath: cfg.Database.Path,
		ApplicationVersion: buildinfo.Version, ApplicationCommit: buildinfo.Commit,
		Interval: cfg.Operations.BackupInterval.Duration, DailyRetention: cfg.Operations.BackupDailyRetention,
		WeeklyRetention:   cfg.Operations.BackupWeeklyRetention,
		EncryptionKeyFile: cfg.Operations.Backup.EncryptionKeyFile, Store: backupStore,
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := backupService.ReconcileStoredPaths(ctx); err != nil {
		db.Close()
		return nil, err
	}
	organizationHTTP, err := organization.NewHTTPHandler(organizationService, publishingService, identityHTTP, identityService, logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	redirectHTTP, err := organization.NewRedirectHTTPHandler(organizationService, identityHTTP, logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	mediaRoot := filepath.Join(cfg.Storage.DataDir, "media")
	var mediaStorage media.Storage
	if cfg.Storage.Adapter == "s3" {
		mediaStorage, err = media.NewS3Storage(cfg.Storage.S3.Endpoint, cfg.Storage.S3.Bucket, cfg.Storage.S3.Region, cfg.Storage.S3.AccessKey, cfg.Storage.S3.SecretKey, cfg.Storage.S3.Prefix, cfg.Storage.S3.ForcePathStyle, cfg.Storage.S3.UseTLS)
	} else {
		mediaStorage, err = media.NewLocalStorage(mediaRoot)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	mediaService, err := media.NewServiceWithStorage(db, mediaRoot, mediaStorage, logger, media.Options{
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
	publishingHTTP.SetMediaPicker(mediaService)
	defaultTheme, err := presentation.NewDefaultTheme(presentation.NewMarkdown())
	if err != nil {
		db.Close()
		return nil, err
	}
	themeManager, err := presentation.NewThemeManager(defaultTheme, filepath.Join(cfg.Storage.DataDir, "themes"))
	if err != nil {
		db.Close()
		return nil, err
	}
	themeCatalog := presentation.NewThemeCatalog(db, themeManager)
	themeCatalog.SetMediaResolver(func(ctx context.Context, publicID string) (presentation.MediaData, error) {
		decoded, err := platformid.DecodePublicID(publicID)
		if err != nil {
			return presentation.MediaData{}, err
		}
		item, err := mediaService.PublicItem(ctx, decoded)
		if err != nil {
			return presentation.MediaData{}, err
		}
		view, err := item.PublicView()
		if err != nil {
			return presentation.MediaData{}, err
		}
		return presentation.MediaData{URL: view.URL, Alt: view.Alt, Width: view.Width, Height: view.Height, SrcSet: view.SrcSet}, nil
	})
	if err := themeCatalog.Reconcile(ctx); err != nil {
		// A broken active package must never prevent the embedded fallback from
		// serving the site. The catalog remains authoritative for the next
		// repair/activation attempt.
		logger.WarnContext(ctx, "theme startup reconciliation failed", "error", err)
	}
	themeHTTP, err := presentation.NewThemeHTTPHandler(themeCatalog, themeManager, identityHTTP, identityService, logger, presentation.ThemeInstallOptions{
		Root: filepath.Join(cfg.Storage.DataDir, "themes"), MaxBytes: int64(cfg.Extensions.ThemePackageMaxBytes), MaxFiles: cfg.Extensions.ThemeMaxFiles, MaxUnpacked: cfg.Extensions.ThemeMaxUnpacked,
	})
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
	presentationHTTP.SetDiscovery(discoveryService)
	presentationHTTP.SetThemeManager(themeManager)
	presentationHTTP.SetMediaQueries(mediaService)
	applySiteMetadata := func(ctx context.Context, settings identity.SiteSettings) {
		baseURL := settings.BaseURL
		if strings.TrimSpace(baseURL) == "" {
			baseURL = cfg.Discovery.BaseURL
		}
		if err := discoveryService.SetBaseURL(baseURL); err != nil {
			logger.WarnContext(ctx, "update discovery base URL from site settings", "error", err)
		}
		metadata := presentation.SiteMetadata{
			Language:    settings.PrimaryLanguage,
			Description: settings.Description, DefaultSEOTitle: settings.DefaultSEOTitle,
			DefaultSEODescription: settings.DefaultSEODescription, SocialLinks: settings.SocialLinks,
		}
		if len(settings.DefaultSocialImageID) == 16 {
			if item, err := mediaService.PublicItem(ctx, settings.DefaultSocialImageID); err == nil {
				if view, err := item.PublicView(); err == nil {
					metadata.DefaultSocialImageURL = discoveryService.AbsoluteURL(view.URL)
				}
			}
		}
		presentationHTTP.SetSiteMetadata(metadata)
	}
	applySiteMetadata(ctx, initialSiteSettings)
	identityHTTP.SetSiteSettingsHook(func(ctx context.Context, settings identity.SiteSettings) {
		applySiteMetadata(ctx, settings)
	})
	commentService := comments.NewService(db, publishingService, presentation.NewMarkdown(), cfg.Comments.RequireModeration, authSecret)
	commentService.SetWriteGuard(publicWriteGuard)
	identityHTTP.SetPendingCommentQueries(commentService)
	if cfg.Mail.Enabled {
		commentService.SetNotifier(outbox, cfg.Mail.From)
	}
	// Built-in handlers are tiny and are constructed at startup so an admin
	// enable survives a restart; their routes remain absent (or guarded) while
	// the corresponding plugin is disabled.
	commentHTTP := comments.NewHTTPHandler(commentService, publishingService, identityHTTP, logger)
	commentHTTP.SetClientIPResolver(clientIPResolver)
	analyticsService := analytics.NewService(db, authSecret, cfg.Analytics.RetentionDays)
	analyticsHTTP, err := analytics.NewHTTPHandler(analyticsService, identityHTTP, logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	var newsletterPlugin *notifications.NewsletterPlugin
	var newsletterService *notifications.NewsletterService
	if cfg.Newsletter.Enabled {
		var adapter notifications.NewsletterAdapter
		if cfg.Newsletter.Provider == "local" {
			adapter = notifications.NewLocalNewsletter(db, authSecret)
		} else {
			adapter, err = notifications.NewHTTPNewsletter(cfg.Newsletter.Endpoint, cfg.Newsletter.Token, cfg.Newsletter.Provider)
			if err != nil {
				db.Close()
				return nil, err
			}
		}
		newsletterService = notifications.NewNewsletterService(db, adapter, authSecret)
		newsletterService.SetWriteGuard(publicWriteGuard)
		newsletterService.SetOutbox(outbox)
		newsletterService.SetBaseURL(cfg.Discovery.BaseURL)
		newsletterHandler := notifications.NewNewsletterHTTPHandlerFromService(newsletterService)
		newsletterHandler.SetSecurity(identityHTTP)
		newsletterHandler.SetClientIPResolver(clientIPResolver)
		pluginID, pluginName := "newsletter.external", "外部 Newsletter"
		if cfg.Newsletter.Provider == "local" {
			pluginID, pluginName = "newsletter.local", "本地 Newsletter"
		}
		newsletterPlugin = &notifications.NewsletterPlugin{ID: pluginID, Name: pluginName, Handler: newsletterHandler, Service: newsletterService}
	}

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
	extensionRegistry := extensions.NewRegistry(db, router, logger)
	outbox.SetTaskQueue(extensionRegistry.TaskQueue())
	if err := extensionRegistry.RegisterCoreTask("core:notification_send", outbox.ProcessTask); err != nil {
		db.Close()
		return nil, err
	}
	presentationHTTP.SetClientIPResolver(clientIPResolver)
	presentationHTTP.SetAnalyticsRecorder(gatedAnalyticsRecorder{registry: extensionRegistry, service: analyticsService})
	presentationHTTP.SetFeatureProvider(extensionRegistry)
	publishingService.SetEventSink(extensionRegistry)
	commentService.SetEventSink(extensionRegistry)
	extensionHTTP, err := extensions.NewHTTPHandler(extensionRegistry, identityHTTP, logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	taskHTTP, err := operations.NewTaskHTTPHandler(extensionRegistry, identityHTTP, logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	taskHTTP.SetOperationsSummaryProvider(func(ctx context.Context) (operations.OperationsSummary, error) {
		return operations.ReadOperationsSummary(ctx, db, extensionRegistry.TaskQueue(), filepath.Join(cfg.Storage.DataDir, "cache", "pages"))
	})
	if err := extensionRegistry.Register(comments.NewLocalPlugin(commentHTTP)); err != nil {
		db.Close()
		return nil, err
	}
	if err := extensionRegistry.Register(comments.NewExternalPlugin(cfg.Comments.ExternalEndpoint)); err != nil {
		db.Close()
		return nil, err
	}
	if err := extensionRegistry.Register(analytics.NewPlugin(analyticsHTTP)); err != nil {
		db.Close()
		return nil, err
	}
	contentAPIPlugin := contentapi.NewPlugin(publishingService, identityService, discoveryService, contentapi.Config{Token: cfg.ContentAPI.Token})
	contentAPIPlugin.SetMediaQueries(mediaService)
	if err := extensionRegistry.Register(contentAPIPlugin); err != nil {
		db.Close()
		return nil, err
	}
	if err := extensionRegistry.Register(webhooks.NewPlugin(webhooks.NewStore(db), webhooks.Config{Endpoint: cfg.Webhooks.Endpoint, Secret: cfg.Webhooks.Secret, MaxAttempts: cfg.Webhooks.MaxAttempts})); err != nil {
		db.Close()
		return nil, err
	}
	if newsletterPlugin != nil {
		if err := extensionRegistry.Register(newsletterPlugin); err != nil {
			db.Close()
			return nil, err
		}
	}
	var protectedRouter chi.Router
	router.Route("/admin", func(admin chi.Router) {
		admin.Use(identityHTTP.SecurityHeaders)
		identityHTTP.RegisterPublic(admin)
		admin.Group(func(protected chi.Router) {
			protectedRouter = protected
			protected.Use(identityHTTP.RequireSession)
			identityHTTP.RegisterProtected(protected)
			extensionHTTP.RegisterAdmin(protected)
			taskHTTP.RegisterAdmin(protected)
			publishingHTTP.RegisterAdmin(protected)
			organizationHTTP.RegisterAdmin(protected)
			redirectHTTP.RegisterAdmin(protected)
			mediaHTTP.RegisterAdmin(protected)
			presentationHTTP.RegisterAdmin(protected)
			themeHTTP.RegisterAdmin(protected)
		})
	})
	extensionRegistry.SetAdminRouter(protectedRouter)
	enabledPlugins := append([]string(nil), cfg.Extensions.EnabledPlugins...)
	if cfg.Comments.Enabled && cfg.Comments.Provider == "local" {
		enabledPlugins = appendUnique(enabledPlugins, "comments.local")
	}
	if cfg.Comments.Enabled && cfg.Comments.Provider == "external" {
		enabledPlugins = appendUnique(enabledPlugins, "comments.external")
	}
	if cfg.Analytics.Enabled {
		enabledPlugins = appendUnique(enabledPlugins, "analytics.local")
	}
	if cfg.ContentAPI.Enabled {
		enabledPlugins = appendUnique(enabledPlugins, contentapi.PluginID)
	}
	if cfg.Webhooks.Enabled {
		enabledPlugins = appendUnique(enabledPlugins, webhooks.PluginID)
	}
	if newsletterPlugin != nil {
		enabledPlugins = appendUnique(enabledPlugins, newsletterPlugin.ID)
	}
	if err := extensionRegistry.Initialize(ctx, enabledPlugins); err != nil {
		// Optional extensions must never prevent core publishing from starting.
		logger.ErrorContext(ctx, "optional plugin initialization failed", "error", err)
	}

	server := &http.Server{
		Addr:              cfg.Server.ListenAddress,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	keepLock = true
	return &App{config: cfg, logger: logger, database: db, server: server, publishing: publishingService, organization: organizationService, discovery: discoveryService, backups: backupService, themeManager: themeManager, extensions: extensionRegistry, analytics: analyticsService, outbox: outbox, publicWrites: publicWriteGuard, dataLock: dataLock, lifecycleInterval: cfg.Publishing.SchedulerInterval.Duration, trashCleanupInterval: cfg.Publishing.TrashCleanupInterval.Duration, backupInterval: cfg.Operations.BackupInterval.Duration}, nil
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func contains(values []string, value string) bool {
	for _, current := range values {
		if current == value {
			return true
		}
	}
	return false
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
	processSearchIndex := func() {
		const maxBatchesPerTick = 4
		for batch := 0; batch < maxBatchesPerTick; batch++ {
			indexed, err := app.discovery.SyncDirty(ctx)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					app.logger.ErrorContext(ctx, "synchronize bounded search batch", "error", err)
				}
				return
			}
			if indexed == 0 {
				return
			}
			app.logger.DebugContext(ctx, "synchronized bounded search batch", "documents", indexed)
		}
	}
	taxonomyWarmed := false
	warmPublicTaxonomy := func() {
		if app.organization == nil || taxonomyWarmed {
			return
		}
		if _, err := app.organization.PublicCategories(ctx); err != nil {
			if !errors.Is(err, context.Canceled) {
				app.logger.ErrorContext(ctx, "prewarm public categories", "error", err)
			}
			return
		}
		if _, err := app.organization.PublicTags(ctx); err != nil {
			if !errors.Is(err, context.Canceled) {
				app.logger.ErrorContext(ctx, "prewarm public tags", "error", err)
			}
			return
		}
		taxonomyWarmed = true
		app.logger.DebugContext(ctx, "prewarmed public taxonomy snapshots")
	}
	processTaxonomyProjection := func() {
		if app.organization == nil {
			return
		}
		state, err := app.organization.PublicTaxonomyRebuildState(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				app.logger.ErrorContext(ctx, "read public taxonomy projection state", "error", err)
			}
			return
		}
		if state.Status == "ready" {
			warmPublicTaxonomy()
			return
		}
		const maxBatchesPerTick = 2
		for batch := 0; batch < maxBatchesPerTick; batch++ {
			processed, complete, rebuildErr := app.organization.RebuildPublicTaxonomy(ctx, 256)
			if rebuildErr != nil {
				if !errors.Is(rebuildErr, context.Canceled) {
					app.logger.ErrorContext(ctx, "rebuild public taxonomy projection", "error", rebuildErr)
				}
				return
			}
			if processed > 0 {
				app.logger.DebugContext(ctx, "rebuilt public taxonomy batch", "records", processed)
			}
			if complete {
				app.logger.InfoContext(ctx, "public taxonomy projection ready")
				warmPublicTaxonomy()
				return
			}
		}
	}
	processScheduled := func() {
		published, err := app.publishing.ProcessScheduled(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			app.logger.ErrorContext(ctx, "process scheduled publishing", "error", err)
			return
		}
		if published > 0 {
			app.logger.InfoContext(ctx, "scheduled publishing processed", "published", published)
			processSearchIndex()
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
	backupIfDue := func() {
		result, err := app.backups.CreateScheduledIfDue(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			app.logger.ErrorContext(ctx, "create scheduled backup", "error", err)
			return
		}
		if result != nil {
			app.logger.InfoContext(ctx, "scheduled backup created", "path", result.Path, "size_bytes", result.SizeBytes)
		}
	}
	purgeAnalytics := func() {
		if app.analytics == nil || (!app.config.Analytics.Enabled && !contains(app.config.Extensions.EnabledPlugins, "analytics.local")) {
			return
		}
		if purged, err := app.analytics.Purge(ctx); err != nil && !errors.Is(err, context.Canceled) {
			app.logger.ErrorContext(ctx, "purge analytics", "error", err)
		} else if purged > 0 {
			app.logger.InfoContext(ctx, "analytics retention purge", "rows", purged)
		}
	}
	cleanupPublicWrites := func() {
		if app.publicWrites == nil {
			return
		}
		if removed, err := app.publicWrites.Cleanup(ctx); err != nil && !errors.Is(err, context.Canceled) {
			app.logger.ErrorContext(ctx, "purge public-write safety records", "error", err)
		} else if removed > 0 {
			app.logger.InfoContext(ctx, "public-write safety records purged", "rows", removed)
		}
	}
	processPluginTasks := func() {
		if app.extensions == nil {
			return
		}
		const maxTasksPerTick = 8
		for index := 0; index < maxTasksPerTick; index++ {
			processed, err := app.extensions.ProcessOne(ctx)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					app.logger.ErrorContext(ctx, "process plugin task", "error", err)
				}
				return
			}
			if !processed {
				return
			}
			app.logger.DebugContext(ctx, "plugin task processed")
		}
	}
	processTaxonomyProjection()
	processSearchIndex()
	processScheduled()
	cleanupTrash()
	backupIfDue()
	purgeAnalytics()
	cleanupPublicWrites()
	processPluginTasks()
	schedulerTicker := time.NewTicker(app.lifecycleInterval)
	defer schedulerTicker.Stop()
	cleanupTicker := time.NewTicker(app.trashCleanupInterval)
	defer cleanupTicker.Stop()
	backupTicker := time.NewTicker(app.backupInterval)
	defer backupTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-schedulerTicker.C:
			processScheduled()
			processTaxonomyProjection()
			processSearchIndex()
			processPluginTasks()
		case <-cleanupTicker.C:
			cleanupTrash()
			purgeAnalytics()
			cleanupPublicWrites()
		case <-backupTicker.C:
			backupIfDue()
		}
	}
}

func (app *App) Close() error {
	var databaseErr, lockErr error
	if app.database != nil {
		databaseErr = app.database.Close()
	}
	if app.dataLock != nil {
		lockErr = app.dataLock.Close()
	}
	if databaseErr != nil {
		return databaseErr
	}
	return lockErr
}
