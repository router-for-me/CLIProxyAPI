// Package main provides the standalone request log uploader service.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/loguploader"
	log "github.com/sirupsen/logrus"
)

func main() {
	logging.SetupBaseLogger()
	if errRun := run(); errRun != nil {
		log.WithError(errRun).Error("log uploader stopped with an error")
		os.Exit(1)
	}
}

func run() error {
	return runCLI(os.Args[1:], os.Stdout, defaultCLIDependencies())
}

type cliService interface {
	Run(context.Context, bool) error
	RunOnce(context.Context, bool) error
	MigrateLegacyState(context.Context, string, string, bool) error
	SyncSupabaseHistory(context.Context, bool) (loguploader.SupabaseHistorySummary, error)
}

type cliDependencies struct {
	loadConfig     func(string) (loguploader.Config, error)
	loadEnv        func(string) error
	newTOSUploader func(loguploader.UploadConfig) (loguploader.ObjectUploader, error)
	newService     func(loguploader.Config, loguploader.ObjectUploader) (cliService, error)
}

func defaultCLIDependencies() cliDependencies {
	return cliDependencies{
		loadConfig: loguploader.LoadConfig,
		loadEnv: func(path string) error {
			return godotenv.Load(path)
		},
		newTOSUploader: func(cfg loguploader.UploadConfig) (loguploader.ObjectUploader, error) {
			return loguploader.NewTOSUploader(cfg)
		},
		newService: func(cfg loguploader.Config, uploader loguploader.ObjectUploader) (cliService, error) {
			return loguploader.NewService(cfg, uploader)
		},
	}
}

func runCLI(args []string, stdout io.Writer, deps cliDependencies) error {
	var configPath string
	var once bool
	var dryRun bool
	var syncSupabaseHistory bool
	var migrateManifest string
	var migrateArchives string
	var migrateTrustLocal bool
	flags := flag.NewFlagSet("log-uploader", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&configPath, "config", "log-uploader.yaml", "Path to the log uploader YAML configuration")
	flags.BoolVar(&once, "once", false, "Process ready logs once and exit")
	flags.BoolVar(&dryRun, "dry-run", false, "Build local archives without uploading, recording state, or deleting source logs")
	flags.BoolVar(&syncSupabaseHistory, "sync-supabase-history", false, "Upload successful local audit history to Supabase and exit")
	flags.StringVar(&migrateManifest, "migrate-legacy-manifest", "", "Verified JSONL manifest used to migrate an untrusted legacy state")
	flags.StringVar(&migrateArchives, "migrate-legacy-archives", "", "Root containing verified local archives for legacy state migration")
	flags.BoolVar(&migrateTrustLocal, "migrate-legacy-trust-local", false, "Migrate using verified local archives and upload audit when HeadObject permission is unavailable")
	if errParse := flags.Parse(args); errParse != nil {
		return errParse
	}

	migrationRequested := migrateManifest != "" || migrateArchives != "" || migrateTrustLocal
	if syncSupabaseHistory && (once || migrationRequested) {
		return fmt.Errorf("--sync-supabase-history cannot be combined with --once or legacy migration flags")
	}
	if migrationRequested {
		if migrateManifest == "" || migrateArchives == "" {
			return fmt.Errorf("both --migrate-legacy-manifest and --migrate-legacy-archives are required")
		}
		if dryRun || once {
			return fmt.Errorf("legacy migration flags cannot be combined with --once or --dry-run")
		}
	}

	absoluteConfigPath, errAbsoluteConfig := filepath.Abs(configPath)
	if errAbsoluteConfig != nil {
		return fmt.Errorf("resolve config path: %w", errAbsoluteConfig)
	}
	if errEnv := deps.loadEnv(filepath.Join(filepath.Dir(absoluteConfigPath), ".env")); errEnv != nil && !os.IsNotExist(errEnv) {
		log.WithError(errEnv).Warn("failed to load .env")
	}

	cfg, errConfig := deps.loadConfig(absoluteConfigPath)
	if errConfig != nil {
		return errConfig
	}
	if !syncSupabaseHistory && !cfg.Upload.Enabled && !dryRun {
		return fmt.Errorf("upload is disabled; use --dry-run for local conversion testing")
	}

	var uploader loguploader.ObjectUploader
	if !syncSupabaseHistory && cfg.Upload.Enabled && !dryRun {
		tosUploader, errUploader := deps.newTOSUploader(cfg.Upload)
		if errUploader != nil {
			return errUploader
		}
		uploader = tosUploader
	}
	service, errService := deps.newService(cfg, uploader)
	if errService != nil {
		return errService
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if syncSupabaseHistory {
		summary, errSync := service.SyncSupabaseHistory(ctx, dryRun)
		if errSync != nil {
			return errSync
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		if errEncode := encoder.Encode(summary); errEncode != nil {
			return fmt.Errorf("write Supabase history summary: %w", errEncode)
		}
		return nil
	}
	if migrationRequested {
		return service.MigrateLegacyState(ctx, migrateManifest, migrateArchives, migrateTrustLocal)
	}
	if once {
		return service.RunOnce(ctx, dryRun)
	}
	log.WithFields(log.Fields{
		"interval":  cfg.Schedule.Interval,
		"logs_root": cfg.LogsRoot,
		"upload":    cfg.Upload.Enabled && !dryRun,
	}).Info("log uploader service started")
	return service.Run(ctx, dryRun)
}
