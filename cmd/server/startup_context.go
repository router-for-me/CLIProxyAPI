package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

type envLookup func(keys ...string) (string, bool)

type startupContext struct {
	workdir      string
	writableBase string
	cloudDeploy  bool
}

type storeSettings struct {
	usePostgresStore     bool
	pgStoreDSN           string
	pgStoreSchema        string
	pgStoreLocalPath     string
	useGitStore          bool
	gitStoreRemoteURL    string
	gitStoreUser         string
	gitStorePassword     string
	gitStoreBranch       string
	gitStoreLocalPath    string
	useObjectStore       bool
	objectStoreEndpoint  string
	objectStoreAccess    string
	objectStoreSecret    string
	objectStoreBucket    string
	objectStoreLocalPath string
}

func resolveStartupContext() (startupContext, error) {
	workdir, err := os.Getwd()
	if err != nil {
		return startupContext{}, fmt.Errorf("failed to get working directory: %w", err)
	}
	return startupContext{
		workdir:      workdir,
		writableBase: util.WritablePath(),
		cloudDeploy:  os.Getenv("DEPLOY") == "cloud",
	}, nil
}

func loadDotEnvFile(workdir string) {
	err := godotenv.Load(workdir + string(os.PathSeparator) + ".env")
	if err == nil || os.IsNotExist(err) {
		return
	}
	log.WithError(err).Warn("failed to load .env file")
}

func newEnvLookup() envLookup {
	return func(keys ...string) (string, bool) {
		for _, key := range keys {
			value, ok := os.LookupEnv(key)
			if !ok {
				continue
			}
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				return trimmed, true
			}
		}
		return "", false
	}
}

func loadStoreSettings(ctx startupContext) storeSettings {
	lookup := newEnvLookup()
	settings := storeSettings{}

	if value, ok := lookup("PGSTORE_DSN", "pgstore_dsn"); ok {
		settings.usePostgresStore = true
		settings.pgStoreDSN = value
	}
	if settings.usePostgresStore {
		if value, ok := lookup("PGSTORE_SCHEMA", "pgstore_schema"); ok {
			settings.pgStoreSchema = value
		}
		if value, ok := lookup("PGSTORE_LOCAL_PATH", "pgstore_local_path"); ok {
			settings.pgStoreLocalPath = value
		}
		if settings.pgStoreLocalPath == "" {
			settings.pgStoreLocalPath = defaultLocalPath(ctx, ctx.workdir)
		}
		return settings
	}

	loadGitStoreSettings(&settings, lookup)
	loadObjectStoreSettings(&settings, lookup)
	return settings
}

func loadGitStoreSettings(settings *storeSettings, lookup envLookup) {
	if value, ok := lookup("GITSTORE_GIT_URL", "gitstore_git_url"); ok {
		settings.useGitStore = true
		settings.gitStoreRemoteURL = value
	}
	if value, ok := lookup("GITSTORE_GIT_USERNAME", "gitstore_git_username"); ok {
		settings.gitStoreUser = value
	}
	if value, ok := lookup("GITSTORE_GIT_TOKEN", "gitstore_git_token"); ok {
		settings.gitStorePassword = value
	}
	if value, ok := lookup("GITSTORE_LOCAL_PATH", "gitstore_local_path"); ok {
		settings.gitStoreLocalPath = value
	}
	if value, ok := lookup("GITSTORE_GIT_BRANCH", "gitstore_git_branch"); ok {
		settings.gitStoreBranch = value
	}
}

func loadObjectStoreSettings(settings *storeSettings, lookup envLookup) {
	if value, ok := lookup("OBJECTSTORE_ENDPOINT", "objectstore_endpoint"); ok {
		settings.useObjectStore = true
		settings.objectStoreEndpoint = value
	}
	if value, ok := lookup("OBJECTSTORE_ACCESS_KEY", "objectstore_access_key"); ok {
		settings.objectStoreAccess = value
	}
	if value, ok := lookup("OBJECTSTORE_SECRET_KEY", "objectstore_secret_key"); ok {
		settings.objectStoreSecret = value
	}
	if value, ok := lookup("OBJECTSTORE_BUCKET", "objectstore_bucket"); ok {
		settings.objectStoreBucket = value
	}
	if value, ok := lookup("OBJECTSTORE_LOCAL_PATH", "objectstore_local_path"); ok {
		settings.objectStoreLocalPath = value
	}
}

func defaultLocalPath(ctx startupContext, configured string) string {
	if configured != "" {
		return configured
	}
	if ctx.writableBase != "" {
		return ctx.writableBase
	}
	return ctx.workdir
}
