package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zhushilin/blog-project/internal/app"
	contentarchive "github.com/zhushilin/blog-project/internal/archive"
	"github.com/zhushilin/blog-project/internal/buildinfo"
	"github.com/zhushilin/blog-project/internal/identity"
	contentimport "github.com/zhushilin/blog-project/internal/importer"
	"github.com/zhushilin/blog-project/internal/media"
	"github.com/zhushilin/blog-project/internal/operations"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/logging"
	"github.com/zhushilin/blog-project/internal/platform/secrets"
	"github.com/zhushilin/blog-project/internal/presentation"
	"github.com/zhushilin/blog-project/internal/publishing"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return serve(arguments)
	}
	switch arguments[0] {
	case "serve":
		return serve(arguments[1:])
	case "migrate":
		return migrate(arguments[1:])
	case "healthcheck":
		return healthcheck(arguments[1:])
	case "auth":
		return auth(arguments[1:])
	case "backup":
		return backup(arguments[1:])
	case "restore":
		return restore(arguments[1:])
	case "upgrade":
		return upgrade(arguments[1:])
	case "theme":
		return theme(arguments[1:])
	case "archive":
		return archiveCommand(arguments[1:])
	case "storage":
		return storageCommand(arguments[1:])
	case "import":
		return importCommand(arguments[1:])
	case "audit":
		return audit(arguments[1:])
	case "status":
		return status(arguments[1:])
	case "version":
		fmt.Printf("blog %s (commit %s, built %s)\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime)
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func serve(arguments []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to TOML configuration")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	logger := logging.New(cfg.Logging, os.Stdout)
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	application, err := app.New(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if err := application.Close(); err != nil {
			logger.Error("close application", "error", err)
		}
	}()
	logger.Info("application starting",
		"version", buildinfo.Version,
		"commit", buildinfo.Commit,
		"data_dir", cfg.Storage.DataDir,
	)
	return application.Run(ctx)
}

func migrate(arguments []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to TOML configuration")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	version, err := db.MigrationVersion(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("database is at migration %d\n", version)
	return nil
}

func healthcheck(arguments []string) error {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	endpoint := flags.String("url", "http://127.0.0.1:8080/readyz", "readiness endpoint")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(*endpoint)
	if err != nil {
		return fmt.Errorf("request health endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("health endpoint returned %s", response.Status)
	}
	return nil
}

func auth(arguments []string) error {
	return authWithOutput(arguments, os.Stdout)
}

func authWithOutput(arguments []string, output io.Writer) error {
	if len(arguments) == 0 || arguments[0] != "recover" {
		return fmt.Errorf("usage: blog auth recover --username OWNER [--password-file FILE]")
	}
	flags := flag.NewFlagSet("auth recover", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to TOML configuration")
	username := flags.String("username", "", "owner username")
	passwordFile := flags.String("password-file", "-", "file containing the new password, or - for stdin")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if *username == "" {
		return fmt.Errorf("--username is required")
	}
	password, err := readPassword(*passwordFile)
	if err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	authSecret, err := secrets.LoadOrCreateAuthSecret(cfg.Security)
	if err != nil {
		return err
	}
	service, err := identity.NewService(identity.NewRepository(db), authSecret, cfg.Security.SessionLifetime.Duration)
	if err != nil {
		return err
	}
	recovered, err := service.Recover(context.Background(), *username, password)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "Authentication recovered. Existing sessions are invalid.")
	fmt.Fprintln(output, "Add this TOTP account to your authenticator:")
	fmt.Fprintln(output, recovered.TOTPURI)
	fmt.Fprintf(output, "Manual secret: %s\n", recovered.TOTPSecret)
	fmt.Fprintln(output, "Store these one-time recovery codes now:")
	for _, code := range recovered.RecoveryCodes {
		fmt.Fprintln(output, code)
	}
	return nil
}

func backup(arguments []string) error {
	return backupWithOutput(arguments, os.Stdout)
}

func backupWithOutput(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return fmt.Errorf("usage: blog backup <create|verify|drill|list> [options]")
	}
	switch arguments[0] {
	case "create":
		flags := flag.NewFlagSet("backup create", flag.ContinueOnError)
		configPath := flags.String("config", "", "path to TOML configuration")
		archivePath := flags.String("output", "", "output .tar.gz path; defaults to the data backup directory")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		service, db, err := openBackupService(*configPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := service.Create(context.Background(), "manual", *archivePath)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Backup created and verified: %s\n", result.Path)
		fmt.Fprintf(output, "Backup ID: %s\nMigration: %d\nFiles: %d\nSize: %d bytes\n", result.Manifest.PublicID, result.Manifest.MigrationVersion, len(result.Manifest.Entries), result.SizeBytes)
		return nil
	case "verify":
		flags := flag.NewFlagSet("backup verify", flag.ContinueOnError)
		archivePath := flags.String("archive", "", "backup .tar.gz path")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *archivePath == "" {
			return fmt.Errorf("--archive is required")
		}
		verified, err := operations.VerifyBackup(context.Background(), *archivePath)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Backup is valid: %s\n", *archivePath)
		fmt.Fprintf(output, "Backup ID: %s\nMigration: %d\nFiles: %d\nSize: %d bytes\n", verified.Manifest.PublicID, verified.Manifest.MigrationVersion, len(verified.Manifest.Entries), verified.SizeBytes)
		return nil
	case "drill":
		flags := flag.NewFlagSet("backup drill", flag.ContinueOnError)
		configPath := flags.String("config", "", "path to TOML configuration")
		archivePath := flags.String("archive", "", "registered backup .tar.gz path")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *archivePath == "" {
			return fmt.Errorf("--archive is required")
		}
		service, db, err := openBackupService(*configPath)
		if err != nil {
			return err
		}
		defer db.Close()
		work, err := os.MkdirTemp("", "blog-restore-drill-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(work)
		result, err := operations.RestoreBackup(context.Background(), *archivePath, filepath.Join(work, "data"), false)
		if err != nil {
			return err
		}
		if err := service.MarkRestoreTested(context.Background(), result.Manifest); err != nil {
			return err
		}
		fmt.Fprintf(output, "Restore drill passed for backup %s (%d files).\n", result.Manifest.PublicID, result.RestoredFiles)
		return nil
	case "list":
		flags := flag.NewFlagSet("backup list", flag.ContinueOnError)
		configPath := flags.String("config", "", "path to TOML configuration")
		limit := flags.Int("limit", 20, "maximum records to show (1-200)")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		db, err := openOperationalDatabase(*configPath)
		if err != nil {
			return err
		}
		defer db.Close()
		records, err := operations.ListBackups(context.Background(), db, *limit)
		if err != nil {
			return err
		}
		for _, record := range records {
			tested := "not-tested"
			if record.RestoreTestedAt != nil {
				tested = "tested=" + record.RestoreTestedAt.Format(time.RFC3339)
			}
			fmt.Fprintf(output, "%s  %s  %s  migration=%d  size=%d  %s  %s\n", record.CreatedAt.Format(time.RFC3339), record.PublicID, record.Reason, record.MigrationVersion, record.SizeBytes, tested, record.Path)
		}
		return nil
	default:
		return fmt.Errorf("usage: blog backup <create|verify|drill|list> [options]")
	}
}

func restore(arguments []string) error {
	return restoreWithOutput(arguments, os.Stdout)
}

func restoreWithOutput(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to TOML configuration")
	archivePath := flags.String("archive", "", "backup .tar.gz path")
	targetDataDir := flags.String("target-data-dir", "", "restore destination; defaults to configured data directory")
	replace := flags.Bool("replace", false, "atomically preserve and replace an existing destination")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *archivePath == "" {
		return fmt.Errorf("--archive is required")
	}
	if *targetDataDir == "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		*targetDataDir = cfg.Storage.DataDir
	}
	result, err := operations.RestoreBackup(context.Background(), *archivePath, *targetDataDir, *replace)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Backup %s restored to %s (%d files).\n", result.Manifest.PublicID, result.DataDir, result.RestoredFiles)
	if result.RollbackDir != "" {
		fmt.Fprintf(output, "Previous data preserved at: %s\n", result.RollbackDir)
	}
	return nil
}

func upgrade(arguments []string) error {
	return upgradeWithOutput(arguments, os.Stdout)
}

func upgradeWithOutput(arguments []string, output io.Writer) error {
	if len(arguments) == 0 || arguments[0] != "prepare" {
		return fmt.Errorf("usage: blog upgrade prepare [--config FILE] [--output ARCHIVE]")
	}
	flags := flag.NewFlagSet("upgrade prepare", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to TOML configuration")
	archivePath := flags.String("output", "", "pre-upgrade recovery point .tar.gz path")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	service, db, err := openBackupService(*configPath)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := service.Create(context.Background(), "pre_upgrade", *archivePath)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Upgrade recovery point created and verified: %s\n", result.Path)
	fmt.Fprintf(output, "Current migration: %d; binary supports: %d\n", result.Manifest.MigrationVersion, database.LatestMigrationVersion)
	return nil
}

func audit(arguments []string) error {
	return auditWithOutput(arguments, os.Stdout)
}

func auditWithOutput(arguments []string, output io.Writer) error {
	if len(arguments) == 0 || arguments[0] != "list" {
		return fmt.Errorf("usage: blog audit list [--config FILE] [--limit 50]")
	}
	flags := flag.NewFlagSet("audit list", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to TOML configuration")
	limit := flags.Int("limit", 50, "maximum entries to show (1-200)")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	db, err := openOperationalDatabase(*configPath)
	if err != nil {
		return err
	}
	defer db.Close()
	entries, err := operations.RecentAudit(context.Background(), db, *limit)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		object := entry.ObjectKind
		if entry.ObjectID != "" {
			object += ":" + entry.ObjectID
		}
		fmt.Fprintf(output, "%s  %s  %s  %s  %s\n", entry.CreatedAt.Format(time.RFC3339), entry.Result, entry.Action, object, entry.ContextJSON)
	}
	return nil
}

func status(arguments []string) error {
	return statusWithOutput(arguments, os.Stdout)
}

func statusWithOutput(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to TOML configuration")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	db, err := openOperationalDatabase(*configPath)
	if err != nil {
		return err
	}
	defer db.Close()
	current, err := operations.ReadStatus(context.Background(), db)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "migration_version: %d\nfailed_jobs: %d\nvalid_backups: %d\n", current.MigrationVersion, current.FailedJobs, current.ValidBackups)
	if current.LastBackupAt != nil {
		fmt.Fprintf(output, "last_backup_at: %s\n", current.LastBackupAt.Format(time.RFC3339))
	}
	if current.LastRestoreTest != nil {
		fmt.Fprintf(output, "last_restore_test_at: %s\n", current.LastRestoreTest.Format(time.RFC3339))
	}
	return nil
}

func theme(arguments []string) error {
	return themeWithOutput(arguments, os.Stdout)
}

func themeWithOutput(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return fmt.Errorf("usage: blog theme <install|list|activate|rollback> [options]")
	}
	flags := flag.NewFlagSet("theme", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to TOML configuration")
	archivePath := flags.String("archive", "", "theme ZIP package")
	themeID := flags.String("id", "", "theme ID")
	version := flags.String("version", "", "theme version")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	lock, err := operations.AcquireDataLock(cfg.Storage.DataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	fallback, err := presentation.NewDefaultTheme(presentation.NewMarkdown())
	if err != nil {
		return err
	}
	manager, err := presentation.NewThemeManager(fallback, filepath.Join(cfg.Storage.DataDir, "themes"))
	if err != nil {
		return err
	}
	catalog := presentation.NewThemeCatalog(db, manager)
	ctx := context.Background()
	switch arguments[0] {
	case "install":
		if *archivePath == "" {
			return fmt.Errorf("--archive is required")
		}
		archive, err := os.Open(*archivePath)
		if err != nil {
			return err
		}
		defer archive.Close()
		record, err := catalog.Install(ctx, archive, presentation.ThemeInstallOptions{Root: filepath.Join(cfg.Storage.DataDir, "themes"), MaxBytes: int64(cfg.Extensions.ThemePackageMaxBytes), MaxFiles: cfg.Extensions.ThemeMaxFiles, MaxUnpacked: cfg.Extensions.ThemeMaxUnpacked})
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Theme installed: %s@%s\n", record.Manifest.ID, record.Manifest.Version)
		return nil
	case "list":
		records, err := catalog.List(ctx)
		if err != nil {
			return err
		}
		for _, record := range records {
			active := "inactive"
			if record.Active {
				active = "active"
			}
			fmt.Fprintf(output, "%s@%s  %s  %s\n", record.Manifest.ID, record.Manifest.Version, active, record.ValidationStatus)
		}
		if manager.IsFallback() {
			fmt.Fprintln(output, "default@embedded  active  fallback")
		}
		return nil
	case "activate":
		if *themeID == "" || *version == "" {
			return fmt.Errorf("--id and --version are required")
		}
		if err := catalog.Activate(ctx, *themeID, *version); err != nil {
			return err
		}
		fmt.Fprintf(output, "Theme activated: %s@%s\n", *themeID, *version)
		return nil
	case "rollback":
		if err := catalog.Rollback(ctx); err != nil {
			return err
		}
		fmt.Fprintln(output, "Theme rolled back to embedded default.")
		return nil
	default:
		return fmt.Errorf("usage: blog theme <install|list|activate|rollback> [options]")
	}
}

func archiveCommand(arguments []string) error {
	if len(arguments) == 0 {
		return fmt.Errorf("usage: blog archive <export|verify|import> [options]")
	}
	flags := flag.NewFlagSet("archive", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to TOML configuration")
	archivePath := flags.String("archive", "", "content archive ZIP path")
	outputPath := flags.String("output", "", "output content archive ZIP path")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	lock, err := operations.AcquireDataLock(cfg.Storage.DataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	service := publishing.NewService(publishing.NewRepository(db))
	switch arguments[0] {
	case "export":
		if *outputPath == "" {
			return fmt.Errorf("--output is required")
		}
		manifest, err := contentarchive.Export(context.Background(), service, *outputPath)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Content archive exported: %s (%d entries)\n", *outputPath, len(manifest.Entries))
		return nil
	case "verify":
		if *archivePath == "" {
			return fmt.Errorf("--archive is required")
		}
		verified, err := contentarchive.Verify(context.Background(), *archivePath)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Content archive is valid: %s (%d entries, %d bytes)\n", *archivePath, len(verified.Manifest.Entries), verified.SizeBytes)
		return nil
	case "import":
		if *archivePath == "" {
			return fmt.Errorf("--archive is required")
		}
		created, err := contentarchive.Import(context.Background(), service, *archivePath)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Content archive imported: %d drafts\n", created)
		return nil
	default:
		return fmt.Errorf("usage: blog archive <export|verify|import> [options]")
	}
}

func storageCommand(arguments []string) error {
	if len(arguments) == 0 || arguments[0] != "migrate" {
		return fmt.Errorf("usage: blog storage migrate --to <local|s3> [options]")
	}
	flags := flag.NewFlagSet("storage migrate", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to TOML configuration")
	target := flags.String("to", "", "destination adapter: local or s3")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if *target != "local" && *target != "s3" {
		return fmt.Errorf("--to must be local or s3")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	lock, err := operations.AcquireDataLock(cfg.Storage.DataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	local, err := media.NewLocalStorage(filepath.Join(cfg.Storage.DataDir, "media"))
	if err != nil {
		return err
	}
	var s3 *media.S3Storage
	if cfg.Storage.S3.Endpoint != "" {
		s3, err = media.NewS3Storage(cfg.Storage.S3.Endpoint, cfg.Storage.S3.Bucket, cfg.Storage.S3.Region, cfg.Storage.S3.AccessKey, cfg.Storage.S3.SecretKey, cfg.Storage.S3.Prefix, cfg.Storage.S3.ForcePathStyle, cfg.Storage.S3.UseTLS)
		if err != nil {
			return err
		}
	}
	var source, destination media.Storage
	if cfg.Storage.Adapter == "s3" {
		source = s3
	} else {
		source = local
	}
	if *target == "s3" {
		if s3 == nil {
			return fmt.Errorf("S3 settings are required for --to s3")
		}
		destination = s3
	} else {
		destination = local
	}
	result, err := media.MigrateStorage(context.Background(), db, source, destination)
	if err != nil {
		return err
	}
	fmt.Printf("Storage migration %s -> %s completed: %d/%d objects\n", result.SourceAdapter, result.Destination, result.CopiedObjects, result.TotalObjects)
	return nil
}

func importCommand(arguments []string) error {
	if len(arguments) == 0 {
		return fmt.Errorf("usage: blog import <wordpress|ghost|markdown> --input PATH [--dry-run] [--report FILE]")
	}
	format := strings.ToLower(strings.TrimSpace(arguments[0]))
	flags := flag.NewFlagSet("import", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to TOML configuration")
	input := flags.String("input", "", "WordPress WXR, Ghost JSON, Markdown file, directory, or ZIP")
	dryRun := flags.Bool("dry-run", false, "parse and report without creating drafts")
	reportPath := flags.String("report", "", "write the JSON report to this file")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("--input is required")
	}
	if format != contentimport.FormatWordPress && format != contentimport.FormatGhost && format != contentimport.FormatMarkdown {
		return fmt.Errorf("unsupported import format %q", format)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	lock, err := operations.AcquireDataLock(cfg.Storage.DataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	service := contentimport.NewService(db, publishing.NewService(publishing.NewRepository(db)), organization.NewService(db))
	report, err := service.ImportPath(context.Background(), format, *input, *dryRun)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if *reportPath != "" {
		if err := os.WriteFile(*reportPath, encoded, 0o600); err != nil {
			return fmt.Errorf("write import report: %w", err)
		}
	}
	fmt.Fprintf(os.Stdout, "Import %s %s: total=%d planned=%d imported=%d skipped=%d conflicts=%d duplicate=%t\n", format, map[bool]string{true: "dry-run", false: "completed"}[*dryRun], report.Total, report.Planned, report.Imported, report.Skipped, report.Conflicts, report.Duplicate)
	if *reportPath != "" {
		fmt.Fprintf(os.Stdout, "Report: %s\n", *reportPath)
	}
	if *dryRun || report.Duplicate {
		_, _ = os.Stdout.Write(encoded)
	}
	return nil
}

func openBackupService(configPath string) (*operations.BackupService, *database.DB, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		return nil, nil, err
	}
	service, err := operations.NewBackupService(db, operations.BackupOptions{
		DataDir: cfg.Storage.DataDir, DatabasePath: cfg.Database.Path,
		ApplicationVersion: buildinfo.Version, ApplicationCommit: buildinfo.Commit,
		Interval: cfg.Operations.BackupInterval.Duration, DailyRetention: cfg.Operations.BackupDailyRetention,
		WeeklyRetention: cfg.Operations.BackupWeeklyRetention,
	})
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	if err := service.ReconcileStoredPaths(context.Background()); err != nil {
		db.Close()
		return nil, nil, err
	}
	return service, db, nil
}

func openOperationalDatabase(configPath string) (*database.DB, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return database.Open(context.Background(), cfg.Database)
}

func readPassword(path string) (string, error) {
	var value string
	if path == "-" {
		line, err := bufio.NewReaderSize(os.Stdin, 1024).ReadString('\n')
		if err != nil && len(line) == 0 {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		value = line
	} else {
		contents, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read password file: %w", err)
		}
		if len(contents) > 1024 {
			return "", fmt.Errorf("password file is too large")
		}
		value = string(contents)
	}
	value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	if err := identity.ValidatePassword(value); err != nil {
		return "", err
	}
	return value, nil
}

func printUsage() {
	fmt.Println("usage: blog <serve|migrate|healthcheck|status|auth|backup|restore|upgrade|theme|archive|storage|import|audit|version> [options]")
}
