// Package main provides the entry point for the NixLLM server.
// NixLLM is a Go proxy server core-derived from CLIProxyAPI; it provides
// OpenAI/Gemini/Claude/Codex compatible API interfaces for CLI models,
// allowing them to be used with tools and libraries designed for standard AI APIs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	configaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/config_access"
	litellmaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/litellm_access"
	pgaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/pg_access"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	internalBackup "github.com/router-for-me/CLIProxyAPI/v7/internal/backup"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/cmd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/errormessages"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/events"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/homeplugins"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementasset"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtimebridge"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/safemode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	runtimeconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/store/runtimeconfig"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/tui"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/upstreamsync"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
	log "github.com/sirupsen/logrus"
)

var (
	Version           = "dev"
	CoreVersion       = "unknown"
	Commit            = "none"
	BuildDate         = "unknown"
	DefaultConfigPath = ""
)

// init initializes the shared logger setup.
func init() {
	logging.SetupBaseLogger()
	buildinfo.Version = Version
	buildinfo.CoreVersion = CoreVersion
	buildinfo.Commit = Commit
	buildinfo.BuildDate = BuildDate
	// In dev builds (no ldflags) fall back to the nearest git tags so the
	// running version is meaningful even when built with a bare `go build`.
	buildinfo.ResolveFromGit()
}

func shouldEnableExampleAPIKeySafeMode(cfg *config.Config, commandMode, tuiMode, standalone, cloudConfigMissing, homeMode bool) bool {
	if cfg == nil || commandMode || homeMode || cloudConfigMissing {
		return false
	}
	if tuiMode && !standalone {
		return false
	}
	return safemode.HasExampleAPIKeys(cfg.APIKeys)
}

// main is the entry point of the application.
// It parses command-line flags, loads configuration, and starts the appropriate
// service based on the provided flags (login, codex-login, or server mode).
func main() {
	fmt.Printf("NixLLM %s (core CLIProxyAPI %s), Commit: %s, BuiltAt: %s\n", buildinfo.Version, buildinfo.CoreVersion, buildinfo.Commit, buildinfo.BuildDate)

	// Phase 5 deprecation warning. The OBJECTSTORE_* and GITSTORE_*
	// runtime paths are removed in this release; we still detect the
	// env vars so the boot path emits a loud, actionable error pointing
	// operators at the PG-first migration rather than silently ignoring
	// the configuration. Remove the block on the next major release.
	if v, ok := os.LookupEnv("OBJECTSTORE_ENDPOINT"); ok && strings.TrimSpace(v) != "" {
		log.Errorf("OBJECTSTORE_ENDPOINT is no longer supported; migrate to PGSTORE_DSN + 'nixllm -import-config config.yaml'")
	}
	if v, ok := os.LookupEnv("GITSTORE_GIT_URL"); ok && strings.TrimSpace(v) != "" {
		log.Errorf("GITSTORE_GIT_URL is no longer supported; migrate to PGSTORE_DSN + 'nixllm -import-config config.yaml'")
	}

	// Command-line flags to control the application's behavior.
	var codexLogin bool
	var codexDeviceLogin bool
	var claudeLogin bool
	var noBrowser bool
	var oauthCallbackPort int
	var antigravityLogin bool
	var kimiLogin bool
	var xaiLogin bool
	var metaLogin bool
	var vertexImport string
	var vertexImportPrefix string
	var configPath string
	var password string
	var homeJWT string
	var homeDisableClusterDiscovery bool
	var tuiMode bool
	var standalone bool
	var localModel bool

	// Define command-line flags for different operation modes.
	flag.BoolVar(&codexLogin, "codex-login", false, "Login to Codex using OAuth")
	flag.BoolVar(&codexDeviceLogin, "codex-device-login", false, "Login to Codex using device code flow")
	flag.BoolVar(&claudeLogin, "claude-login", false, "Login to Claude using OAuth")
	flag.BoolVar(&noBrowser, "no-browser", false, "Don't open browser automatically for OAuth")
	flag.IntVar(&oauthCallbackPort, "oauth-callback-port", 0, "Override OAuth callback port (defaults to provider-specific port)")
	flag.BoolVar(&antigravityLogin, "antigravity-login", false, "Login to Antigravity using OAuth")
	flag.BoolVar(&kimiLogin, "kimi-login", false, "Login to Kimi using OAuth")
	flag.BoolVar(&xaiLogin, "xai-login", false, "Login to xAI using OAuth")
	flag.BoolVar(&metaLogin, "meta-login", false, "Login to Meta using OAuth")
	flag.StringVar(&configPath, "config", DefaultConfigPath, "Configure File Path")
	flag.StringVar(&vertexImport, "vertex-import", "", "Import Vertex service account key JSON file")
	flag.StringVar(&vertexImportPrefix, "vertex-import-prefix", "", "Prefix for Vertex model namespacing (use with -vertex-import)")
	var importConfig string
	var importConfigDryRun bool
	flag.StringVar(&importConfig, "import-config", "", "One-shot import of a YAML config into the PostgreSQL control plane (requires PGSTORE_DSN)")
	flag.BoolVar(&importConfigDryRun, "import-config-dry-run", false, "With -import-config, plan and validate only; no DB writes")
	var exportConfigPath string
	flag.StringVar(&exportConfigPath, "export-config", "", "One-shot export of the active PostgreSQL snapshot to a YAML file")
	var verifyConfig bool
	flag.BoolVar(&verifyConfig, "verify-config", false, "One-shot verify of the active PostgreSQL snapshot integrity")
	flag.StringVar(&password, "password", "", "")
	flag.StringVar(&homeJWT, "home-jwt", "", "Home control plane JWT for mTLS certificate bootstrap and connection")
	flag.BoolVar(&homeDisableClusterDiscovery, "home-disable-cluster-discovery", false, "Disable Home CLUSTER NODES discovery and keep using the configured -home-jwt address")
	flag.BoolVar(&tuiMode, "tui", false, "Start with terminal management UI")
	flag.BoolVar(&standalone, "standalone", false, "In TUI mode, start an embedded local server")
	flag.BoolVar(&localModel, "local-model", false, "Use embedded models.json and codex_client_models.json only, skip remote model catalog fetching")

	flag.CommandLine.Usage = func() {
		out := flag.CommandLine.Output()
		_, _ = fmt.Fprintf(out, "Usage of %s\n", os.Args[0])
		flag.CommandLine.VisitAll(func(f *flag.Flag) {
			if f.Name == "password" {
				return
			}
			s := fmt.Sprintf("  -%s", f.Name)
			name, unquoteUsage := flag.UnquoteUsage(f)
			if name != "" {
				s += " " + name
			}
			if len(s) <= 4 {
				s += "	"
			} else {
				s += "\n    "
			}
			if unquoteUsage != "" {
				s += unquoteUsage
			}
			if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
				s += fmt.Sprintf(" (default %s)", f.DefValue)
			}
			_, _ = fmt.Fprint(out, s+"\n")
		})
	}

	pluginHost := pluginhost.New()
	if bootstrapCfg := loadPluginBootstrapConfig(pluginBootstrapConfigPath(os.Args[1:], DefaultConfigPath)); bootstrapCfg != nil {
		pluginHost.ApplyConfig(context.Background(), bootstrapCfg)
		pluginHost.RegisterCommandLineFlags(context.Background(), flag.CommandLine)
	}

	// Parse the command-line flags.
	flag.Parse()

	// Core application variables.
	var err error
	var cfg *config.Config
	var isCloudDeploy bool
	var configLoadedFromHome bool
	var homeClient *home.Client
	var homePluginSyncReport homeplugins.SyncReport
	var homePluginStatusReady bool
	var (
		usePostgresStore          bool
		pgStoreDSN                string
		pgStoreSchema             string
		pgStoreLocalPath          string
		pgStoreEncryptionKey      []byte
		pgStoreMaxOpenConns       int
		pgStoreMaxIdleConns       int
		pgStoreRollupBackfillDays = 7
		pgStoreInst               *store.PostgresStore
		// Phase 5: the legacy GITSTORE_* / OBJECTSTORE_* runtime paths are
		// removed. The env vars still trigger a loud boot-time error in
		// the deprecation block at the top of main(); no other branch in
		// main.go reads them.
	)

	wd, err := os.Getwd()
	if err != nil {
		log.Errorf("failed to get working directory: %v", err)
		return
	}

	// Phase 2: track the ephemeral PG-first bridge across the storage-backend
	// if/else chain. Only the PG-first boot path (snapshot populated) sets it;
	// the file/object/git branches leave it nil so the reload coordinator is
	// skipped. Hoisted to function scope so the serverOptions assembly below
	// can reference it.
	var activeBridge *runtimebridge.Bridge

	// Load environment variables from .env if present.
	if errLoad := godotenv.Load(filepath.Join(wd, ".env")); errLoad != nil {
		if !errors.Is(errLoad, os.ErrNotExist) {
			log.WithError(errLoad).Warn("failed to load .env file")
		}
	}

	lookupEnv := func(keys ...string) (string, bool) {
		for _, key := range keys {
			if value, ok := os.LookupEnv(key); ok {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					return trimmed, true
				}
			}
		}
		return "", false
	}
	writableBase := util.WritablePath()

	if strings.TrimSpace(homeJWT) == "" {
		if v, ok := lookupEnv("HOME_JWT", "home_jwt"); ok {
			homeJWT = v
		}
	}

	if value, ok := lookupEnv("PGSTORE_DSN", "pgstore_dsn"); ok {
		usePostgresStore = true
		pgStoreDSN = value
	}
	if usePostgresStore {
		if value, ok := lookupEnv("PGSTORE_SCHEMA", "pgstore_schema"); ok {
			pgStoreSchema = value
		}
		if value, ok := lookupEnv("PGSTORE_LOCAL_PATH", "pgstore_local_path"); ok {
			pgStoreLocalPath = value
		}
		if pgStoreLocalPath == "" {
			if writableBase != "" {
				pgStoreLocalPath = writableBase
			} else {
				pgStoreLocalPath = wd
			}
		}
		// Optional passphrase used to AES-GCM-seal sensitive columns
		// (api_key_principal in usage_events) at rest. When unset, encryption
		// is disabled and the column is stored in plaintext for backward
		// compatibility. Held in memory only.
		if value, ok := lookupEnv("PGSTORE_ENCRYPTION_KEY", "pgstore_encryption_key"); ok {
			pgStoreEncryptionKey = []byte(value)
		}
		if value, ok := lookupEnv("PGSTORE_MAX_OPEN_CONNS", "pgstore_max_open_conns"); ok {
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				pgStoreMaxOpenConns = n
			}
		}
		if value, ok := lookupEnv("PGSTORE_MAX_IDLE_CONNS", "pgstore_max_idle_conns"); ok {
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				pgStoreMaxIdleConns = n
			}
		}
		// PGSTORE_ROLLUP_BACKFILL_DAYS controls how many prior days the daily
		// usage_stat_day rollup backfills at startup. Defaults to 7 when unset to
		// give the dashboard a week of pre-aggregated history on first boot.
		if value, ok := lookupEnv("PGSTORE_ROLLUP_BACKFILL_DAYS", "pgstore_rollup_backfill_days"); ok {
			// Accept 0 explicitly to disable the startup backfill.
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				pgStoreRollupBackfillDays = n
			}
		}
		// Phase 5: GITSTORE_* / OBJECTSTORE_* env-var parsing removed.
		// The deprecation block at the top of main() emits a loud error
		// when these are set so operators see the migration path; nothing
		// here reads them anymore.
	}

	// Full-backup-to-S3 configuration. Independent of OBJECTSTORE_* so a
	// deployment can use a separate backup bucket / credentials. Backup is
	// active only when BACKUP_S3_ENDPOINT is set.
	var backupS3Cfg internalBackup.S3Config
	if value, ok := lookupEnv("BACKUP_S3_ENDPOINT", "backup_s3_endpoint"); ok {
		backupS3Cfg.Endpoint = value
	}
	if value, ok := lookupEnv("BACKUP_S3_BUCKET", "backup_s3_bucket"); ok {
		backupS3Cfg.Bucket = value
	}
	if value, ok := lookupEnv("BACKUP_S3_ACCESS_KEY", "backup_s3_access_key"); ok {
		backupS3Cfg.AccessKey = value
	}
	if value, ok := lookupEnv("BACKUP_S3_SECRET_KEY", "backup_s3_secret_key"); ok {
		backupS3Cfg.SecretKey = value
	}
	if value, ok := lookupEnv("BACKUP_S3_REGION", "backup_s3_region"); ok {
		backupS3Cfg.Region = value
	}
	if value, ok := lookupEnv("BACKUP_S3_PREFIX", "backup_s3_prefix"); ok {
		backupS3Cfg.Prefix = value
	}
	// BACKUP_S3_USE_SSL can be set explicitly. When it is left unset and the
	// endpoint carries an http/https scheme, the scheme decides: https -> SSL
	// on, http -> SSL off. This mirrors the OBJECTSTORE_ENDPOINT handling below
	// and keeps minio.New happy (it rejects endpoints that start with a scheme).
	if value, ok := lookupEnv("BACKUP_S3_USE_SSL", "backup_s3_use_ssl"); ok {
		backupS3Cfg.UseSSL = value == "1" || value == "true"
	}
	backupS3Cfg.Endpoint, backupS3Cfg.UseSSL = normalizeBackupS3Endpoint(backupS3Cfg.Endpoint, backupS3Cfg.UseSSL)
	// PathStyle defaults to true to match the project's existing object store
	// convention (MinIO-style endpoints); operators on AWS S3 can flip it off.
	backupS3Cfg.PathStyle = true
	if value, ok := lookupEnv("BACKUP_S3_PATH_STYLE", "backup_s3_path_style"); ok {
		backupS3Cfg.PathStyle = value == "1" || value == "true"
	}

	var backupInterval time.Duration
	if value, ok := lookupEnv("BACKUP_SCHEDULE_INTERVAL", "backup_schedule_interval"); ok && value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			backupInterval = d
		} else {
			log.WithError(err).Warn("main: ignoring invalid BACKUP_SCHEDULE_INTERVAL")
		}
	}

	backupRetention := 7
	if value, ok := lookupEnv("BACKUP_RETENTION", "backup_retention"); ok && value != "" {
		if n, err := strconv.Atoi(value); err == nil {
			backupRetention = n
		} else {
			log.WithError(err).Warn("main: ignoring invalid BACKUP_RETENTION")
		}
	}

	// Check for cloud deploy mode only on first execution
	// Read env var name in uppercase: DEPLOY
	deployEnv := os.Getenv("DEPLOY")
	if deployEnv == "cloud" {
		isCloudDeploy = true
	}

	// Determine and load the configuration file.
	// Prefer the Postgres store when configured, otherwise fallback to git or local files.
	var configFilePath string
	if strings.TrimSpace(homeJWT) != "" {
		configLoadedFromHome = true
		ctxHome, cancelHome := context.WithTimeout(context.Background(), 30*time.Second)
		homeCfg, errHomeCfg := home.ConfigFromJWT(ctxHome, homeJWT)
		cancelHome()
		if errHomeCfg != nil {
			log.Errorf("invalid -home-jwt: %v", errHomeCfg)
			return
		}
		if homeDisableClusterDiscovery {
			homeCfg.DisableClusterDiscovery = true
		}
		homeClient = home.New(homeCfg)
		defer func() {
			if homeClient != nil {
				homeClient.Close()
			}
		}()

		ctxHomeConfig, cancelHomeConfig := context.WithTimeout(context.Background(), 30*time.Second)
		raw, errGetConfig := homeClient.GetConfig(ctxHomeConfig)
		cancelHomeConfig()
		if errGetConfig != nil {
			log.Errorf("failed to fetch config from home: %v", errGetConfig)
			return
		}

		parsed, errParseConfig := config.ParseConfigBytes(raw)
		if errParseConfig != nil {
			log.Errorf("failed to parse config payload from home: %v", errParseConfig)
			return
		}
		if parsed == nil {
			parsed = &config.Config{}
		}
		parsed.Home = homeCfg
		parsed.Port = 8317 // Default to 8317 for home mode, can be overridden by home config
		parsed.UsageStatisticsEnabled = true
		pluginSyncCfg := *parsed
		parsed.Plugins.StoreAuth = nil
		var errHomePlugins error
		platform := homeplugins.CurrentPlatform()
		if pluginSyncCfg.Plugins.Enabled {
			ctxHomePlugins, cancelHomePlugins := context.WithTimeout(context.Background(), 30*time.Second)
			installedVersions, errInstalledPlugins := homeplugins.InstalledVersions(&pluginSyncCfg)
			if errInstalledPlugins != nil {
				homePluginStatusReady = true
				errHomePlugins = errInstalledPlugins
				homePluginSyncReport = homeplugins.CompletedSyncReport(platform, errInstalledPlugins)
			} else {
				pluginSyncRequest := sdkpluginstore.PluginSyncRequest{
					SchemaVersion:     sdkpluginstore.PluginSyncSchemaVersion,
					GOOS:              platform.GOOS,
					GOARCH:            platform.GOARCH,
					InstalledVersions: installedVersions,
				}
				pluginSyncResponse, errFetchPlugins := homeClient.GetPluginSync(ctxHomePlugins, pluginSyncRequest)
				errHomePlugins = errFetchPlugins
				switch {
				case errHomePlugins == nil:
					homePluginStatusReady = true
					homePluginSyncReport, errHomePlugins = homeplugins.SyncResolvedWithReport(ctxHomePlugins, &pluginSyncCfg, pluginSyncResponse.Items, pluginSyncResponse.ExpiresAt, pluginSyncRequest.InstalledVersions, pluginHost)
				case errors.Is(errHomePlugins, home.ErrPluginSyncUnsupported):
					homePluginStatusReady = true
					homePluginSyncReport, errHomePlugins = homeplugins.SyncWithReport(ctxHomePlugins, &pluginSyncCfg, pluginHost)
				default:
					homePluginStatusReady = true
					homePluginSyncReport = homeplugins.CompletedSyncReport(platform, errHomePlugins)
				}
				pluginSyncRequest.Clear()
				pluginSyncResponse.Clear()
			}
			cancelHomePlugins()
		} else {
			homePluginStatusReady = true
			homePluginSyncReport = homeplugins.CompletedSyncReport(platform, nil)
		}
		if errHomePlugins != nil {
			log.Errorf("failed to sync plugins from home: %v", errHomePlugins)
		}
		if homePluginStatusReady {
			errReportPlugins := home.ReportPluginStatus(context.Background(), homeClient, homeCfg.NodeID, homePluginSyncReport)
			if errReportPlugins != nil {
				log.Warnf("failed to report home plugin sync status: %v", errReportPlugins)
			}
		}
		if errHomePlugins != nil {
			return
		}
		cfg = parsed

		// Keep a non-empty config path for downstream components (log paths, management assets, etc),
		// but do not require the file to exist when loading config from home.
		if strings.TrimSpace(configPath) != "" {
			configFilePath = configPath
		} else {
			configFilePath = filepath.Join(wd, "config.yaml")
		}

		// Local stores are intentionally disabled when config is loaded from home.
		usePostgresStore = false
	} else if usePostgresStore && importConfig == "" && !verifyConfig && exportConfigPath == "" {
		// The -import-config / -verify-config / -export-config command modes
		// skip the boot path entirely: they open their own store connection
		// and must not race with (or be pre-empted by) the boot auto-import
		// below. Skipping here keeps the verify path read-only against the
		// current snapshot rather than accidentally seeding an empty one.
		if pgStoreLocalPath == "" {
			pgStoreLocalPath = wd
		}
		pgStoreLocalPath = filepath.Join(pgStoreLocalPath, "pgstore")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		pgStoreInst, err = store.NewPostgresStore(ctx, store.PostgresStoreConfig{
			DSN:                pgStoreDSN,
			Schema:             pgStoreSchema,
			SpoolDir:           pgStoreLocalPath,
			UsageEncryptionKey: pgStoreEncryptionKey,
			MaxOpenConns:       pgStoreMaxOpenConns,
			MaxIdleConns:       pgStoreMaxIdleConns,
		})
		cancel()
		if err != nil {
			log.Errorf("failed to initialize postgres token store: %v", err)
			return
		}
		examplePath := filepath.Join(wd, "config.example.yaml")
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		if errBootstrap := pgStoreInst.Bootstrap(ctx, examplePath); errBootstrap != nil {
			cancel()
			log.Errorf("failed to bootstrap postgres-backed config: %v", errBootstrap)
			return
		}
		cancel()

		// Phase 2 PG-first boot path. When runtime_config has an active
		// revision, render an ephemeral bridge so the file-bound core can
		// consume it. Otherwise auto-import a legacy config.yaml when one
		// exists (boot decision matrix: DSN set + PG empty + legacy file),
		// or fall back to the legacy spool flow / fail closed when no
		// legacy file is present.
		runtimeCfg := runtimeconfig.New(pgStoreInst)
		snapshotCfg, snapErr := runtimebridge.LoadConfigFromSnapshot(context.Background(), runtimeCfg)
		if snapErr != nil {
			// runtime_config has no active revision. Auto-import the legacy
			// config when one exists at the resolved path so a first boot
			// with PGSTORE_DSN set seeds the control plane transparently.
			legacyPath := configPath
			if strings.TrimSpace(legacyPath) == "" {
				legacyPath = filepath.Join(wd, "config.yaml")
			}
			if _, statErr := os.Stat(legacyPath); statErr == nil {
				log.Infof("postgres-backed runtime_config empty; auto-importing legacy config from %s", legacyPath)
				if errImport := cmd.ApplyConfigFile(context.Background(), legacyPath, pgStoreInst, "boot-auto-import"); errImport != nil {
					log.Errorf("postgres-backed auto-import failed: %v", errImport)
				} else {
					// Re-attempt the snapshot load; on success fall through
					// to the bridge path below.
					snapshotCfg, snapErr = runtimebridge.LoadConfigFromSnapshot(context.Background(), runtimeCfg)
				}
			}
		}
		if snapErr == nil {
			// The bridge requires a SnapshotRenderer; we render straight
			// from the loaded *config.Config so the file is generated from
			// the same bytes the in-memory cfg was parsed from.
			bridge, buildErr := runtimebridge.BuildFromConfig(context.Background(), snapshotCfg, pgStoreInst.AuthDir())
			if buildErr != nil {
				log.Errorf("postgres-backed bridge build failed: %v", buildErr)
				return
			}
			activeBridge = bridge
			configFilePath = bridge.ConfigPath()
			cfg = snapshotCfg
			cfg.AuthDir = pgStoreInst.AuthDir()
			log.Infof("postgres-backed runtime_config bridge online, workspace path: %s", pgStoreInst.WorkDir())
		} else {
			configFilePath = pgStoreInst.ConfigPath()
			cfg, err = config.LoadConfigOptional(configFilePath, isCloudDeploy)
			if err == nil {
				cfg.AuthDir = pgStoreInst.AuthDir()
				log.Infof("postgres-backed token store enabled, workspace path: %s", pgStoreInst.WorkDir())
			} else {
				log.Warnf("postgres-backed snapshot not ready and no legacy config.yaml at %s; run 'nixllm -import-config config.yaml' or place a config.yaml at the -config path to bootstrap: %v", configPath, snapErr)
			}
		}
		// Phase 5: the legacy OBJECTSTORE_* and GITSTORE_* runtime paths
		// have been removed. The deprecation warning at the top of main()
		// already explained the migration. Falling through to the legacy
		// file boot is the only remaining path.
	} else if configPath != "" {
		configFilePath = configPath
		cfg, err = config.LoadConfigOptional(configPath, isCloudDeploy)
	} else {
		wd, err = os.Getwd()
		if err != nil {
			log.Errorf("failed to get working directory: %v", err)
			return
		}
		configFilePath = filepath.Join(wd, "config.yaml")
		cfg, err = config.LoadConfigOptional(configFilePath, isCloudDeploy)
	}
	if err != nil {
		log.Errorf("failed to load config: %v", err)
		return
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	// In cloud deploy mode, check if we have a valid configuration
	var configFileExists bool
	if isCloudDeploy {
		if configLoadedFromHome && cfg != nil {
			configFileExists = cfg.Port != 0
		} else {
			if info, errStat := os.Stat(configFilePath); errStat != nil {
				// Don't mislead: API server will not start until configuration is provided.
				log.Info("Cloud deploy mode: No configuration file detected; standing by for configuration")
				configFileExists = false
			} else if info.IsDir() {
				log.Info("Cloud deploy mode: Config path is a directory; standing by for configuration")
				configFileExists = false
			} else if cfg.Port == 0 {
				// LoadConfigOptional returns empty config when file is empty or invalid.
				// Config file exists but is empty or invalid; treat as missing config
				log.Info("Cloud deploy mode: Configuration file is empty or invalid; standing by for valid configuration")
				configFileExists = false
			} else {
				log.Info("Cloud deploy mode: Configuration file detected; starting service")
				configFileExists = true
			}
		}
	}
	redisqueue.SetUsageStatisticsEnabled(cfg.UsageStatisticsEnabled)
	redisqueue.SetRetentionSeconds(cfg.RedisUsageQueueRetentionSeconds)
	coreauth.SetQuotaCooldownDisabled(cfg.DisableCooling)
	coreauth.SetTransientErrorCooldownSeconds(cfg.TransientErrorCooldownSeconds)

	if err = logging.ConfigureLogOutput(cfg); err != nil {
		log.Errorf("failed to configure log output: %v", err)
		return
	}

	log.Infof("NixLLM %s (core CLIProxyAPI %s), Commit: %s, BuiltAt: %s", buildinfo.Version, buildinfo.CoreVersion, buildinfo.Commit, buildinfo.BuildDate)

	// Set the log level based on the configuration.
	util.SetLogLevel(cfg)

	if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir); errResolveAuthDir != nil {
		log.Errorf("failed to resolve auth directory: %v", errResolveAuthDir)
		return
	} else {
		cfg.AuthDir = resolvedAuthDir
	}
	managementasset.SetCurrentConfig(cfg)

	// Create login options to be used in authentication flows.
	options := &cmd.LoginOptions{
		NoBrowser:    noBrowser,
		CallbackPort: oauthCallbackPort,
	}

	commandMode := vertexImport != "" || antigravityLogin || codexLogin || codexDeviceLogin || claudeLogin || kimiLogin || xaiLogin || metaLogin || importConfig != ""
	cloudConfigMissing := isCloudDeploy && !configFileExists
	homeMode := configLoadedFromHome || (cfg != nil && cfg.Home.Enabled)
	exampleAPIKeySafeMode := shouldEnableExampleAPIKeySafeMode(cfg, commandMode, tuiMode, standalone, cloudConfigMissing, homeMode)
	serverOptions := []api.ServerOption(nil)
	if exampleAPIKeySafeMode {
		matches := safemode.ExampleAPIKeys(cfg.APIKeys)
		log.WithField("api_keys", strings.Join(matches, ",")).Error("unsafe example API key configured; proxy API endpoints disabled until api-keys is updated")
		serverOptions = append(serverOptions, api.WithExampleAPIKeySafeMode())
	}

	// Register the shared token store once so all components use the same persistence backend.
	// The -import-config command mode skips the PG boot path (pgStoreInst
	// stays nil), so it registers the plain file token store; the command
	// exits before any auth flow can run.
	if usePostgresStore && pgStoreInst != nil {
		sdkAuth.RegisterTokenStore(pgStoreInst)
	} else if !usePostgresStore && !configLoadedFromHome {
		// Phase 5: only the PG and the plain file token stores remain;
		// the OBJECTSTORE_* / GITSTORE_* branches are removed. Home mode
		// keeps the upstream auth manager and skips both.
		sdkAuth.RegisterTokenStore(sdkAuth.NewFileTokenStore())
	}

	// When the PG backend is active, wire the API key + usage + model stores,
	// the policy service, the async usage flusher, and the pg-access provider.
	// These remain nil when the PG backend is inactive; downstream features
	// detect the nil state and degrade to file-only behavior.
	var (
		pgAPIKeyStore        *store.APIKeyStore
		pgUsageStore         *store.UsageStore
		pgModelsStore        *store.ModelsStore
		pgUserStore          *store.UserStore
		pgMgmtTokens         *store.ManagementTokenStore
		pgSyncLog            *store.SyncLogStore
		pgSyncAdapter        *registry.PGSync
		pgModelGroups        *store.ModelGroupStore
		pgAutoRouters        *store.AutoRouterStore
		pgAutoRouterProfiles *store.AutoRouterProfileStore
		policySvc            policy.PolicyService
		usageFlusher         *store.UsageFlusher
	)
	if usePostgresStore && pgStoreInst != nil {
		pgAPIKeyStore = store.NewAPIKeyStore(pgStoreInst)
		pgUsageStore = store.NewUsageStore(pgStoreInst)
		pgModelsStore = store.NewModelsStore(pgStoreInst)
		pgUserStore = store.NewUserStore(pgStoreInst)
		pgMgmtTokens = store.NewManagementTokenStore(pgStoreInst)
		pgSyncLog = store.NewSyncLogStore(pgStoreInst)
		pgModelGroups = store.NewModelGroupStore(pgStoreInst)
		pgAutoRouters = store.NewAutoRouterStore(pgStoreInst)
		pgAutoRouterProfiles = store.NewAutoRouterProfileStore(pgStoreInst)
		pgModelHealth := store.NewModelHealthStore(pgStoreInst)
		pgAlerts := store.NewAlertStore(pgStoreInst)
		// Jev AI classifier settings (global master toggle + sealed API key).
		pgJev := store.NewJevStore(pgStoreInst)
		// Manage-LiteLLM stores (dedicated litellm_* tables, management-only).
		pgLiteLLMUsers := store.NewLiteLLMUserStore(pgStoreInst)
		pgLiteLLMKeys := store.NewLiteLLMKeyStore(pgStoreInst)
		// Manage-LiteLLM external sync settings (base URL + sealed master key).
		pgLiteLLMSync := store.NewLiteLLMSyncStore(pgStoreInst)
		// Durable on-the-fly validation log + retention sweep.
		pgLiteLLMOnTheFly := store.NewOnTheFlyLogStore(pgStoreInst)
		store.StartOnTheFlyLogSweep(pgLiteLLMOnTheFly)
		pgSyncAdapter = registry.NewPGSync(store.NewPGModelsAdapter(pgModelsStore))
		policySvc = policy.NewService(pgAPIKeyStore, pgUsageStore, policy.ServiceConfig{})
		// Attach the user store so per-user budget/RPM enforcement is
		// active. When left unset, only per-key enforcement runs.
		if svc, ok := policySvc.(interface{ SetUserStore(*store.UserStore) }); ok {
			svc.SetUserStore(pgUserStore)
		}
		// Attach the model-group store so attached groups resolve into the
		// per-key snapshot (override allowed/blocked/routes) and into the
		// per-user snapshot (override Models only — routes are ignored for
		// users). When left unset, the group-override path is skipped.
		if svc, ok := policySvc.(interface{ SetModelGroupStore(*store.ModelGroupStore) }); ok {
			svc.SetModelGroupStore(pgModelGroups)
		}
		// Start the policy service background goroutine (sliding-window
		// cleanup). The goroutine exits when the context is canceled or
		// Stop() is invoked at shutdown.
		if svc, ok := policySvc.(interface{ Start(context.Context) error }); ok {
			if errStart := svc.Start(context.Background()); errStart != nil {
				log.Errorf("failed to start policy service: %v", errStart)
			}
		}
		// Register the async PG usage flusher as a usage plugin so completed
		// requests persist to usage_events. The flusher is started first so
		// it is draining the queue by the time records start arriving; the
		// plugin registration then wires it into the usage manager's fan-out
		// so HandleUsage invocations actually happen (without this the
		// flusher goroutine would spin idle forever).
		usageFlusher = store.NewUsageFlusher(pgUsageStore, pgAPIKeyStore, pgModelGroups, store.DefaultFlusherConfig())
		if errFlusherStart := usageFlusher.Start(context.Background()); errFlusherStart != nil {
			log.Errorf("failed to start PG usage flusher: %v", errFlusherStart)
		}
		// Start the daily usage_stat_day rollup driver: backfill pgStoreRollupBackfillDays
		// prior days once at startup (configurable via PGSTORE_ROLLUP_BACKFILL_DAYS,
		// default 7), then re-fold "today" every 24 hours as new events flush. The
		// fold is idempotent (overwrite), so re-running is safe. Runs for the lifetime
		// of the process; its context is derived from context.Background and the loop
		// exits on return of this func only at shutdown, mirroring the other
		// background sweeps.
		go pgUsageStore.RunRollupLoop(context.Background(), pgStoreRollupBackfillDays)
		coreusage.RegisterPlugin(usageFlusher)
		// Register the policy usage plugin so budget windows are
		// incremented as requests complete.
		if plugin := policy.NewUsagePlugin(policySvc); plugin != nil {
			coreusage.RegisterPlugin(plugin)
		}
		// Register the PG-backed access provider alongside the inline config
		// provider; the manager falls through from pg-store to config-inline
		// so legacy file-based keys keep working.
		pgaccess.Register(pgAPIKeyStore)
		// Register the config-inline provider before the on-the-fly provider so
		// that a config/plugin key is authenticated locally. The manager stops
		// at the first internal_error, so an enabled-but-down LiteLLM must not
		// sit ahead of config-inline or it would turn every config key into a
		// 500. This call is idempotent: RegisterProvider only appends the order
		// entry on first registration, so the later configaccess.Register call
		// just refreshes the instance.
		configaccess.Register(&cfg.SDKConfig)
		// Register the on-the-fly LiteLLM provider as the last fallback: it
		// validates a client key against the external LiteLLM instance, lazily
		// imports the matched key into the runtime api_keys table, and records
		// the outcome in litellm_onthefly_log.
		litellmaccess.Register(pgAPIKeyStore, pgLiteLLMKeys, pgLiteLLMOnTheFly,
			func(ctx context.Context) (litellmaccess.Settings, error) {
				set, err := pgLiteLLMSync.Get(ctx)
				if err != nil {
					return litellmaccess.Settings{}, err
				}
				return litellmaccess.Settings{
					Enabled:  set.OnTheFlyEnabled,
					BaseURL:  set.BaseURL,
					CacheTTL: time.Duration(set.OnTheFlyCacheTTLSeconds) * time.Second,
					Timeout:  time.Duration(set.OnTheFlyTimeoutMs) * time.Millisecond,
				}, nil
			})
		// Wire the PG-backed error messages store + warm the cache so the
		// policy middleware + handlers can serve customized error text
		// without a per-request DB round-trip.
		pgErrorMessages := store.NewErrorMessageStore(pgStoreInst)
		errorMessagesCtx, errorMessagesCancel := context.WithTimeout(context.Background(), 5*time.Second)
		errormessages.SetStore(pgErrorMessages)
		errormessages.RefreshFromDB(errorMessagesCtx)
		errorMessagesCancel()
		// Wire the PG-backed external pricing sources store
		// (LiteLLM-format JSON URLs / uploaded catalogs). On startup the
		// SetPricingSourcesStore call bootstraps the pricingsource registry
		// from the persisted rows so MatchAll can surface external
		// suggestions without waiting for an operator refresh.
		pgPricingSources := store.NewPricingSourceStore(pgStoreInst)
		pgPricingSourcesDir := pgStoreInst.PricingSourcesDir()
		// Wire the PG-backed upstream providers store (normalized source of
		// truth for both API-key providers and OAuth/file-backed auths).
		pgUpstreamProviders := store.NewUpstreamProviderStore(pgStoreInst)
		pgProxyPools := store.NewProxyPoolStore(pgStoreInst)
		// Seed model catalog when empty so the registry can load from PG.
		seedCtx, seedCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if count, countErr := pgModelsStore.Count(seedCtx); countErr == nil && count == 0 {
			if loadErr := loadEmbeddedModelsToPG(seedCtx, pgSyncAdapter); loadErr != nil {
				log.Warnf("failed to seed model catalog to PostgreSQL: %v", loadErr)
			}
		}
		// Seed the upstream_providers table from the existing config.yaml +
		// auth-dir artifacts on first boot (table empty). Subsequent boots load
		// from the table; the artifacts are then rendered FROM the table.
		if cfg != nil {
			if count, countErr := pgUpstreamProviders.Count(seedCtx); countErr == nil && count == 0 {
				if seedErr := upstreamsync.SeedFromArtifacts(seedCtx, pgUpstreamProviders, cfg, cfg.AuthDir); seedErr != nil {
					log.Warnf("failed to seed upstream providers to PostgreSQL: %v", seedErr)
				}
			}
		}
		seedCancel()
		// Re-render the runtime artifacts from the normalized table on every
		// startup. This keeps persisted row identities (for example
		// claude:94) in the config before the service synthesizes API-key auths
		// and registers their model capabilities.
		if renderedCfg, applyErr := applyPersistedUpstreamProviders(context.Background(), pgUpstreamProviders, cfg, configFilePath, cfg.AuthDir, pgProxyPools); applyErr != nil {
			log.WithError(applyErr).Warn("failed to apply persisted upstream providers at startup")
		} else if renderedCfg != nil {
			cfg = renderedCfg
		}
		// Expose the PG stores + policy service to the API server so the
		// policy middleware and PG management routes are activated.
		serverOptions = append(serverOptions, api.WithPolicyService(policySvc, &api.PgStoreHandles{
			APIKeys:            pgAPIKeyStore,
			Usage:              pgUsageStore,
			Models:             pgModelsStore,
			Users:              pgUserStore,
			PGSync:             pgSyncAdapter,
			Policy:             policySvc,
			ErrorMessages:      pgErrorMessages,
			PricingSources:     pgPricingSources,
			PricingSourcesDir:  pgPricingSourcesDir,
			ManagementTokens:   pgMgmtTokens,
			UpstreamProviders:  pgUpstreamProviders,
			ProxyPools:         pgProxyPools,
			SyncLog:            pgSyncLog,
			ModelGroups:        pgModelGroups,
			AutoRouters:        pgAutoRouters,
			AutoRouterProfiles: pgAutoRouterProfiles,
			ModelHealth:        pgModelHealth,
			Alerts:             pgAlerts,
			Jev:                pgJev,
			Backup:             pgStoreInst,
			LiteLLMUsers:       pgLiteLLMUsers,
			LiteLLMKeys:        pgLiteLLMKeys,
			LiteLLMSync:        pgLiteLLMSync,
			LiteLLMOnTheFly:    pgLiteLLMOnTheFly,
			Flusher:            usageFlusher,
			PG:                 pgStoreInst,
		}))
		// When the ephemeral bridge is active (Phase 2 PG-first boot), wire
		// the reload coordinator so a successful runtime-config commit or
		// rollback re-renders the bridge file and the fsnotify watcher picks
		// up the change.
		if activeBridge != nil {
			coord := runtimebridge.NewCoordinator(func(ctx context.Context) (runtimebridge.SnapshotRenderer, error) {
				return runtimebridge.PlanFromConfig(ctx, pgStoreInst)
			})
			coord.Bind(activeBridge)
			serverOptions = append(serverOptions, api.WithReloadCoordinator(coord.ReloadLatest))
		}
	}

	// Construct the full-backup-to-S3 subsystem. Activates only when
	// BACKUP_S3_ENDPOINT is set; without it the components are skipped and the
	// management /backup routes surface 503 (handler has nil runner). The
	// runner + restorer are attached to the handler via the WithBackupS3
	// server option so the /v0/management/backup routes can list, create, and
	// restore snapshots; the restorer is set on the runner for RestoreFromS3.
	if backupS3Cfg.Endpoint != "" {
		backupClient, errBackupClient := internalBackup.NewS3Client(backupS3Cfg)
		if errBackupClient != nil {
			log.WithError(errBackupClient).Warn("main: backup S3 client init failed; full backup disabled")
		} else {
			backupSrc := internalBackup.NewPathSource(configFilePath, cfg.AuthDir)
			// pgStoreInst is a typed *store.PostgresStore; pass nil interfaces
			// explicitly when no PG store was constructed so the collector /
			// restorer fallback paths trigger (a typed-nil *PG store would not
			// compare equal to nil).
			var backupCollector *internalBackup.Collector
			var backupRestorer *internalBackup.Restorer
			if pgStoreInst != nil {
				backupCollector = internalBackup.NewCollector(pgStoreInst, backupSrc)
				backupRestorer = internalBackup.NewRestorer(pgStoreInst, pgStoreInst, configFilePath, cfg.AuthDir)
			} else {
				backupCollector = internalBackup.NewCollector(nil, backupSrc)
				backupRestorer = internalBackup.NewRestorer(nil, nil, configFilePath, cfg.AuthDir)
			}
			backupRunner := internalBackup.NewRunner(backupClient, backupCollector, backupRetention)
			backupRunner.SetRestorer(backupRestorer)
			// Attach the runner (which doubles as the restore-from-S3 facade)
			// to the management handler via a server option, so the
			// /v0/management/backup routes resolve. The option applies the
			// handler setter inside api.NewServer.
			serverOptions = append(serverOptions, api.WithBackupS3(backupRunner, backupInterval, backupRetention))
			if backupInterval > 0 {
				backupSched := internalBackup.NewScheduler(backupInterval, func() {
					if _, err := backupRunner.RunBackup(context.Background()); err != nil {
						log.WithError(err).Error("main: scheduled full backup failed")
					} else {
						log.Info("main: scheduled full backup completed")
					}
				})
				backupSched.Start()
				// Note: scheduler lifetime is bound to the process; Stop() is
				// not explicitly invoked at shutdown.
			}
		}
	}

	// Initialize the in-memory events ring recorder that backs the
	// /v0/management/events and /events/stats endpoints (and the future
	// /events/stream SSE feed). The same ring backs both the management
	// handler (via api.WithEventsRing) and the global recorder that
	// emission sites (round-2 Task 12 — routing decisions, breaker trips,
	// cooldown waits) read from via events.Global(). Capacity falls back
	// to events.DefaultRingCapacity (5000) when the config knob is unset,
	// matching the round-2 design doc. This must happen after serverOptions
	// is fully assembled so both the foreground and TUI-embedded server
	// paths pick it up.
	ringCapacity := events.DefaultRingCapacity
	if cfg != nil && cfg.Routing.Events.RingCapacity > 0 {
		ringCapacity = cfg.Routing.Events.RingCapacity
	}
	eventsRing := events.NewRing(ringCapacity)
	events.SetGlobal(eventsRing)
	serverOptions = append(serverOptions, api.WithEventsRing(eventsRing))

	// Register built-in access providers before constructing services.
	configaccess.Register(&cfg.SDKConfig)
	pluginHost.ApplyConfig(context.Background(), cfg)
	if configLoadedFromHome && homePluginStatusReady {
		errHomePluginLoad := homeplugins.MarkLoadResults(&homePluginSyncReport, pluginHost)
		errReportPlugins := home.ReportPluginStatus(context.Background(), homeClient, cfg.Home.NodeID, homePluginSyncReport)
		if errHomePluginLoad != nil {
			log.Errorf("failed to load home plugins: %v", errHomePluginLoad)
		}
		if errReportPlugins != nil {
			log.Warnf("failed to report home plugin load status: %v", errReportPlugins)
		}
		if errHomePluginLoad != nil {
			return
		}
	}
	if homeClient != nil {
		// The bootstrap client is not owned by the runtime service. Close it after
		// the final startup report so it cannot retain an idle RESP connection.
		homeClient.Close()
		homeClient = nil
	}
	if pluginHost.HasTriggeredCommandLineFlags() {
		if exitCode, handled := pluginHost.ExecuteCommandLine(context.Background(), os.Args[0], os.Args[1:], configFilePath, flag.CommandLine); handled {
			if exitCode != 0 {
				os.Exit(exitCode)
			}
			return
		}
	}

	// Handle different command modes based on the provided flags.

	if verifyConfig {
		// Handle one-shot verify-config command. Failures are loud and
		// non-zero so a CI gate can wire this in directly.
		if errVerify := cmd.DoVerifyConfig(context.Background(), cmd.VerifyConfigOptions{}); errVerify != nil {
			log.Errorf("verify-config failed: %v", errVerify)
			os.Exit(1)
		}
	} else if importConfig != "" {
		// Handle one-shot PG-first import-config command. Failures must be
		// loud and non-zero: a silent import failure leaves the PG control
		// plane unseeded and the next boot fails closed with no hint why.
		if _, errImport := cmd.DoImportConfig(context.Background(), cmd.ImportConfigOptions{
			SourcePath: importConfig,
			DryRun:     importConfigDryRun,
		}); errImport != nil {
			log.Errorf("import-config failed: %v", errImport)
			os.Exit(1)
		}
	} else if exportConfigPath != "" {
		// Handle one-shot export-config command. Failures are loud and
		// non-zero; the operator can re-run after fixing the snapshot.
		if errExport := cmd.DoExportConfig(context.Background(), cmd.ExportConfigOptions{
			OutputPath: exportConfigPath,
		}); errExport != nil {
			log.Errorf("export-config failed: %v", errExport)
			os.Exit(1)
		}
	} else if vertexImport != "" {
		// Handle Vertex service account import
		cmd.DoVertexImport(cfg, vertexImport, vertexImportPrefix)
	} else if antigravityLogin {
		// Handle Antigravity login
		cmd.DoAntigravityLogin(cfg, options)
	} else if codexLogin {
		// Handle Codex login
		cmd.DoCodexLogin(cfg, options)
	} else if codexDeviceLogin {
		// Handle Codex device-code login
		cmd.DoCodexDeviceLogin(cfg, options)
	} else if claudeLogin {
		// Handle Claude login
		cmd.DoClaudeLogin(cfg, options)
	} else if kimiLogin {
		cmd.DoKimiLogin(cfg, options)
	} else if xaiLogin {
		cmd.DoXAILogin(cfg, options)
	} else if metaLogin {
		cmd.DoMetaLogin(cfg, options)
	} else {
		// In cloud deploy mode without config file, just wait for shutdown signals
		if isCloudDeploy && !configFileExists {
			// No config file available, just wait for shutdown
			cmd.WaitForCloudDeploy()
			return
		}
		if localModel && (!tuiMode || standalone) {
			log.Info("Local model mode: using embedded model catalogs, remote model updates disabled")
		}
		if tuiMode {
			if standalone {
				// Standalone mode: start an embedded local server and connect TUI client to it.
				managementasset.StartAutoUpdater(context.Background(), configFilePath)
				misc.StartAntigravityVersionUpdater(context.Background())
				startModelCatalogUpdaters(localModel, cfg.Home.Enabled)
				hook := tui.NewLogHook(2000)
				hook.SetFormatter(&logging.LogFormatter{})
				log.AddHook(hook)

				origStdout := os.Stdout
				origStderr := os.Stderr
				origLogOutput := log.StandardLogger().Out
				log.SetOutput(io.Discard)

				devNull, errOpenDevNull := os.Open(os.DevNull)
				if errOpenDevNull == nil {
					os.Stdout = devNull
					os.Stderr = devNull
				}

				restoreIO := func() {
					os.Stdout = origStdout
					os.Stderr = origStderr
					log.SetOutput(origLogOutput)
					if devNull != nil {
						_ = devNull.Close()
					}
				}

				localMgmtPassword := fmt.Sprintf("tui-%d-%d", os.Getpid(), time.Now().UnixNano())
				if password == "" {
					password = localMgmtPassword
				}

				cancel, done := cmd.StartServiceBackgroundWithPluginHost(cfg, configFilePath, password, pluginHost, serverOptions...)

				client := tui.NewClient(cfg.Port, password)
				ready := false
				backoff := 100 * time.Millisecond
				for i := 0; i < 30; i++ {
					if _, errGetConfig := client.GetConfig(); errGetConfig == nil {
						ready = true
						break
					}
					time.Sleep(backoff)
					if backoff < time.Second {
						backoff = time.Duration(float64(backoff) * 1.5)
					}
				}

				if !ready {
					restoreIO()
					cancel()
					<-done
					fmt.Fprintf(os.Stderr, "TUI error: embedded server is not ready\n")
					return
				}

				if errRun := tui.Run(cfg.Port, password, hook, origStdout); errRun != nil {
					restoreIO()
					fmt.Fprintf(os.Stderr, "TUI error: %v\n", errRun)
				} else {
					restoreIO()
				}

				cancel()
				<-done
			} else {
				// Default TUI mode: pure management client.
				// The proxy server must already be running.
				if errRun := tui.Run(cfg.Port, password, nil, os.Stdout); errRun != nil {
					fmt.Fprintf(os.Stderr, "TUI error: %v\n", errRun)
				}
			}
		} else {
			// Start the main proxy service
			managementasset.StartAutoUpdater(context.Background(), configFilePath)
			misc.StartAntigravityVersionUpdater(context.Background())
			startModelCatalogUpdaters(localModel, cfg.Home.Enabled)
			cmd.StartServiceWithPluginHost(cfg, configFilePath, password, pluginHost, serverOptions...)
		}
	}
}

// modelCatalogUpdaterPlan decides which remote model catalogs should refresh.
// Codex client templates still refresh under Home mode because the model list
// comes from Home IDs while template metadata stays edge-local.
func modelCatalogUpdaterPlan(localModel, homeEnabled bool) (startModels, startCodexClient bool) {
	if localModel {
		return false, false
	}
	return !homeEnabled, true
}

func startModelCatalogUpdaters(localModel, homeEnabled bool) {
	startModels, startCodexClient := modelCatalogUpdaterPlan(localModel, homeEnabled)
	if startCodexClient {
		registry.StartCodexClientModelsUpdater(context.Background())
	}
	if startModels {
		registry.StartModelsUpdater(context.Background())
	} else if homeEnabled {
		log.Info("Home mode: remote models.json updates disabled; Codex client model list follows Home model IDs")
	}
}

// normalizeBackupS3Endpoint prepares a BACKUP_S3_ENDPOINT value for minio.New,
// which rejects endpoints that begin with a scheme (e.g. "https://host"). When
// the endpoint carries an explicit http/https scheme, the scheme also decides
// UseSSL unless it was set explicitly by the operator. The returned endpoint is
// the bare host[:port][/path] without a trailing slash.
func normalizeBackupS3Endpoint(endpoint string, useSSL bool) (string, bool) {
	endpoint = strings.TrimSpace(endpoint)
	if strings.Contains(endpoint, "://") {
		if parsed, errParse := url.Parse(endpoint); errParse == nil {
			switch strings.ToLower(parsed.Scheme) {
			case "http":
				useSSL = false
			case "https":
				useSSL = true
			default:
				log.WithError(errParse).Warnf("main: unsupported backup S3 endpoint scheme %q (only http/https allowed)", parsed.Scheme)
			}
			if parsed.Host != "" {
				endpoint = strings.TrimSuffix(parsed.Host+parsed.Path, "/")
			}
		} else {
			log.WithError(errParse).Warnf("main: invalid backup S3 endpoint %q", endpoint)
		}
	}
	return strings.TrimRight(strings.TrimSpace(endpoint), "/"), useSSL
}

// loadEmbeddedModelsToPG seeds the PostgreSQL models_catalog table from the
// embedded JSON catalog on first run (when the table is empty). It is
// idempotent: subsequent calls re-upsert the same rows without producing
// duplicates because the table's primary key is (id, provider).
func loadEmbeddedModelsToPG(ctx context.Context, pgSync *registry.PGSync) error {
	if pgSync == nil || !pgSync.Enabled() {
		return nil
	}
	models := registry.EmbeddedModelsForSeed()
	if len(models) == 0 {
		return nil
	}
	return pgSync.UpsertModels(ctx, models)
}

func pluginBootstrapConfigPath(args []string, defaultPath string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return defaultPluginBootstrapConfigPath(defaultPath)
		case arg == "-config" || arg == "--config":
			if i+1 < len(args) {
				return args[i+1]
			}
			return defaultPluginBootstrapConfigPath(defaultPath)
		case strings.HasPrefix(arg, "-config="):
			return strings.TrimPrefix(arg, "-config=")
		case strings.HasPrefix(arg, "--config="):
			return strings.TrimPrefix(arg, "--config=")
		}
	}
	return defaultPluginBootstrapConfigPath(defaultPath)
}

func defaultPluginBootstrapConfigPath(defaultPath string) string {
	if strings.TrimSpace(defaultPath) != "" {
		return defaultPath
	}
	wd, errGetwd := os.Getwd()
	if errGetwd != nil {
		return "config.yaml"
	}
	return filepath.Join(wd, "config.yaml")
}

func loadPluginBootstrapConfig(path string) *config.Config {
	raw, errReadFile := os.ReadFile(path)
	if errReadFile != nil {
		if !errors.Is(errReadFile, os.ErrNotExist) {
			log.Warnf("failed to read plugin bootstrap config: %v", errReadFile)
		}
		cfg := &config.Config{}
		cfg.NormalizePluginsConfig()
		return cfg
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		cfg := &config.Config{}
		cfg.NormalizePluginsConfig()
		return cfg
	}
	cfg, errParseConfig := config.ParseConfigBytes(raw)
	if errParseConfig != nil {
		log.Warnf("failed to parse plugin bootstrap config: %v", errParseConfig)
		cfg = &config.Config{}
		cfg.NormalizePluginsConfig()
		return cfg
	}
	return cfg
}
