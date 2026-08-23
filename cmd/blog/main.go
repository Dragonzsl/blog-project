package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zhushilin/blog-project/internal/app"
	"github.com/zhushilin/blog-project/internal/buildinfo"
	"github.com/zhushilin/blog-project/internal/identity"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/logging"
	"github.com/zhushilin/blog-project/internal/platform/secrets"
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
	fmt.Println("usage: blog <serve|migrate|healthcheck|auth|version> [options]")
}
