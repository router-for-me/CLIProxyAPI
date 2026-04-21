package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type configLoadResult struct {
	cfg            *config.Config
	configFilePath string
	tokenStore     coreauth.Store
}

func loadConfigResult(flags runtimeFlags, ctx startupContext, settings storeSettings) (configLoadResult, error) {
	switch {
	case settings.usePostgresStore:
		return loadPostgresConfig(ctx, settings)
	case settings.useObjectStore:
		return loadObjectConfig(ctx, settings)
	case settings.useGitStore:
		return loadGitConfig(ctx, settings)
	case flags.configPath != "":
		return loadFileConfig(flags.configPath, ctx.cloudDeploy)
	default:
		return loadFileConfig(filepath.Join(ctx.workdir, "config.yaml"), ctx.cloudDeploy)
	}
}

func loadPostgresConfig(ctx startupContext, settings storeSettings) (configLoadResult, error) {
	spoolDir := filepath.Join(settings.pgStoreLocalPath, "pgstore")
	pgStore, err := newPostgresStore(spoolDir, settings)
	if err != nil {
		return configLoadResult{}, err
	}
	if err := bootstrapBackedConfig(ctx.workdir, pgStore.Bootstrap); err != nil {
		return configLoadResult{}, err
	}

	cfg, err := config.LoadConfigOptional(pgStore.ConfigPath(), ctx.cloudDeploy)
	if err != nil {
		return configLoadResult{}, fmt.Errorf("failed to load config: %w", err)
	}
	if cfg != nil {
		cfg.AuthDir = pgStore.AuthDir()
		log.Infof("postgres-backed token store enabled, workspace path: %s", pgStore.WorkDir())
	}
	return configLoadResult{cfg: cfg, configFilePath: pgStore.ConfigPath(), tokenStore: pgStore}, nil
}

func newPostgresStore(spoolDir string, settings storeSettings) (*store.PostgresStore, error) {
	var result *store.PostgresStore
	err := withStoreInitContext(func(ctx context.Context) error {
		storeInst, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{
			DSN:      settings.pgStoreDSN,
			Schema:   settings.pgStoreSchema,
			SpoolDir: spoolDir,
		})
		if err != nil {
			return err
		}
		result = storeInst
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize postgres token store: %w", err)
	}
	return result, nil
}

func loadObjectConfig(ctx startupContext, settings storeSettings) (configLoadResult, error) {
	localRoot := defaultLocalPath(ctx, settings.objectStoreLocalPath)
	endpoint, useSSL, err := resolveObjectEndpoint(settings.objectStoreEndpoint)
	if err != nil {
		return configLoadResult{}, err
	}

	objectStore, err := store.NewObjectTokenStore(store.ObjectStoreConfig{
		Endpoint:  endpoint,
		Bucket:    settings.objectStoreBucket,
		AccessKey: settings.objectStoreAccess,
		SecretKey: settings.objectStoreSecret,
		LocalRoot: filepath.Join(localRoot, "objectstore"),
		UseSSL:    useSSL,
		PathStyle: true,
	})
	if err != nil {
		return configLoadResult{}, fmt.Errorf("failed to initialize object token store: %w", err)
	}
	if err := bootstrapBackedConfig(ctx.workdir, objectStore.Bootstrap); err != nil {
		return configLoadResult{}, fmt.Errorf("failed to bootstrap object-backed config: %w", err)
	}

	cfg, err := config.LoadConfigOptional(objectStore.ConfigPath(), ctx.cloudDeploy)
	if err != nil {
		return configLoadResult{}, fmt.Errorf("failed to load config: %w", err)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.AuthDir = objectStore.AuthDir()
	log.Infof("object-backed token store enabled, bucket: %s", settings.objectStoreBucket)
	return configLoadResult{cfg: cfg, configFilePath: objectStore.ConfigPath(), tokenStore: objectStore}, nil
}

func resolveObjectEndpoint(endpoint string) (string, bool, error) {
	resolved := strings.TrimSpace(endpoint)
	useSSL := true
	if !strings.Contains(resolved, "://") {
		return strings.TrimRight(resolved, "/"), useSSL, nil
	}

	parsed, err := url.Parse(resolved)
	if err != nil {
		return "", false, fmt.Errorf("failed to parse object store endpoint %q: %w", endpoint, err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		useSSL = false
	case "https":
		useSSL = true
	default:
		return "", false, fmt.Errorf("unsupported object store scheme %q (only http and https are allowed)", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", false, fmt.Errorf("object store endpoint %q is missing host information", endpoint)
	}

	resolved = parsed.Host
	if parsed.Path != "" && parsed.Path != "/" {
		resolved = strings.TrimSuffix(parsed.Host+parsed.Path, "/")
	}
	return strings.TrimRight(resolved, "/"), useSSL, nil
}

func loadGitConfig(ctx startupContext, settings storeSettings) (configLoadResult, error) {
	localRoot := defaultLocalPath(ctx, settings.gitStoreLocalPath)
	gitRoot := filepath.Join(localRoot, "gitstore")
	authDir := filepath.Join(gitRoot, "auths")

	gitStore := store.NewGitTokenStore(
		settings.gitStoreRemoteURL,
		settings.gitStoreUser,
		settings.gitStorePassword,
		settings.gitStoreBranch,
	)
	gitStore.SetBaseDir(authDir)
	if err := gitStore.EnsureRepository(); err != nil {
		return configLoadResult{}, fmt.Errorf("failed to prepare git token store: %w", err)
	}

	configFilePath, err := ensureGitBackedConfig(ctx.workdir, gitRoot, gitStore)
	if err != nil {
		return configLoadResult{}, err
	}
	cfg, err := config.LoadConfigOptional(configFilePath, ctx.cloudDeploy)
	if err != nil {
		return configLoadResult{}, fmt.Errorf("failed to load config: %w", err)
	}
	if cfg != nil {
		cfg.AuthDir = gitStore.AuthDir()
		log.Infof("git-backed token store enabled, repository path: %s", gitRoot)
	}
	return configLoadResult{cfg: cfg, configFilePath: configFilePath, tokenStore: gitStore}, nil
}

func ensureGitBackedConfig(workdir, gitRoot string, gitStore *store.GitTokenStore) (string, error) {
	configFilePath := gitStore.ConfigPath()
	if configFilePath == "" {
		configFilePath = filepath.Join(gitRoot, "config", "config.yaml")
	}

	if _, err := os.Stat(configFilePath); errors.Is(err, fs.ErrNotExist) {
		examplePath := filepath.Join(workdir, "config.example.yaml")
		if _, errExample := os.Stat(examplePath); errExample != nil {
			return "", fmt.Errorf("failed to find template config file: %w", errExample)
		}
		if err := misc.CopyConfigTemplate(examplePath, configFilePath); err != nil {
			return "", fmt.Errorf("failed to bootstrap git-backed config: %w", err)
		}
		if err := gitStore.PersistConfig(context.Background()); err != nil {
			return "", fmt.Errorf("failed to commit initial git-backed config: %w", err)
		}
		log.Infof("git-backed config initialized from template: %s", configFilePath)
		return configFilePath, nil
	} else if err != nil {
		return "", fmt.Errorf("failed to inspect git-backed config: %w", err)
	}
	return configFilePath, nil
}

func loadFileConfig(configFilePath string, cloudDeploy bool) (configLoadResult, error) {
	cfg, err := config.LoadConfigOptional(configFilePath, cloudDeploy)
	if err != nil {
		return configLoadResult{}, fmt.Errorf("failed to load config: %w", err)
	}
	return configLoadResult{
		cfg:            cfg,
		configFilePath: configFilePath,
		tokenStore:     nil,
	}, nil
}

func bootstrapBackedConfig(workdir string, bootstrap func(context.Context, string) error) error {
	examplePath := filepath.Join(workdir, "config.example.yaml")
	return withStoreInitContext(func(ctx context.Context) error {
		return bootstrap(ctx, examplePath)
	})
}

func withStoreInitContext(fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return fn(ctx)
}
