// Package cliproxy provides the core service implementation for the CLI Proxy API.
// It includes service lifecycle management, authentication handling, file watching,
// and integration with various AI service providers through a unified interface.
package cliproxy

import (
	"fmt"

	configaccess "github.com/router-for-me/CLIProxyAPI/v6/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// Builder constructs a Service instance with customizable providers.
// It provides a fluent interface for configuring all aspects of the service
// including authentication, file watching, HTTP server options, and lifecycle hooks.
type Builder struct {
	// cfg holds the application configuration.
	cfg *config.Config

	// configPath is the path to the configuration file.
	configPath string

	// tokenProvider handles loading token-based clients.
	tokenProvider TokenClientProvider

	// apiKeyProvider handles loading API key-based clients.
	apiKeyProvider APIKeyClientProvider

	// watcherFactory creates file watcher instances.
	watcherFactory WatcherFactory

	// hooks provides lifecycle callbacks.
	hooks Hooks

	// authManager handles legacy authentication operations.
	authManager *sdkAuth.Manager

	// accessManager handles request authentication providers.
	accessManager *sdkaccess.Manager

	// coreManager handles core authentication and execution.
	coreManager *coreauth.Manager

	// serverOptions contains additional server configuration options.
	serverOptions []api.ServerOption

	// configErr stores configuration validation errors from WithEmbedConfig.
	configErr error
}

// Hooks allows callers to plug into service lifecycle stages.
// These callbacks provide opportunities to perform custom initialization
// and cleanup operations during service startup and shutdown.
type Hooks struct {
	// OnBeforeStart is called before the service starts, allowing configuration
	// modifications or additional setup.
	OnBeforeStart func(*config.Config)

	// OnAfterStart is called after the service has started successfully,
	// providing access to the service instance for additional operations.
	OnAfterStart func(*Service)
}

// NewBuilder creates a Builder with default dependencies left unset.
// Use the fluent interface methods to configure the service before calling Build().
//
// Returns:
//   - *Builder: A new builder instance ready for configuration
func NewBuilder() *Builder {
	return &Builder{}
}

// WithConfig sets the configuration instance used by the service.
//
// Parameters:
//   - cfg: The application configuration
//
// Returns:
//   - *Builder: The builder instance for method chaining
func (b *Builder) WithConfig(cfg *config.Config) *Builder {
	b.cfg = cfg
	return b
}

// WithConfigPath sets the absolute configuration file path used for reload watching.
//
// Parameters:
//   - path: The absolute path to the configuration file
//
// Returns:
//   - *Builder: The builder instance for method chaining
func (b *Builder) WithConfigPath(path string) *Builder {
	b.configPath = path
	return b
}

// WithEmbedConfig sets the configuration using a public EmbedConfig type that can be
// safely used by external Go applications without requiring access to internal packages.
//
// This method validates the configuration and converts it to the internal config.Config type.
// Validation errors are stored and returned during the Build() phase.
//
// Use this method when embedding CLIProxyAPI in external applications:
//
//	svc, err := cliproxy.NewBuilder().
//	    WithEmbedConfig(&cliproxy.EmbedConfig{
//	        Host:    "127.0.0.1",
//	        Port:    8317,
//	        AuthDir: "./auth",
//	    }).
//	    WithConfigPath("./config.yaml").
//	    Build()
//
// For provider-specific configurations (API keys, OAuth accounts, model mappings),
// use WithConfigPath() to load provider settings from a YAML file. EmbedConfig
// handles only essential server configuration options.
//
// Parameters:
//   - embedCfg: The public embedding configuration
//
// Returns:
//   - *Builder: The builder instance for method chaining
func (b *Builder) WithEmbedConfig(embedCfg *EmbedConfig) *Builder {
	if embedCfg == nil {
		b.configErr = fmt.Errorf("embed config cannot be nil")
		return b
	}

	// Validate configuration early - fail fast
	if err := embedCfg.Validate(); err != nil {
		// Store the validation error for later reporting in Build()
		b.configErr = fmt.Errorf("embed config validation failed: %w", err)
		return b
	}

	// Convert to internal config
	b.cfg = convertToInternalConfig(embedCfg)
	return b
}

// WithTokenClientProvider overrides the provider responsible for token-backed clients.
func (b *Builder) WithTokenClientProvider(provider TokenClientProvider) *Builder {
	b.tokenProvider = provider
	return b
}

// WithAPIKeyClientProvider overrides the provider responsible for API key-backed clients.
func (b *Builder) WithAPIKeyClientProvider(provider APIKeyClientProvider) *Builder {
	b.apiKeyProvider = provider
	return b
}

// WithWatcherFactory allows customizing the watcher factory that handles reloads.
func (b *Builder) WithWatcherFactory(factory WatcherFactory) *Builder {
	b.watcherFactory = factory
	return b
}

// WithHooks registers lifecycle hooks executed around service startup.
func (b *Builder) WithHooks(h Hooks) *Builder {
	b.hooks = h
	return b
}

// WithAuthManager overrides the authentication manager used for token lifecycle operations.
func (b *Builder) WithAuthManager(mgr *sdkAuth.Manager) *Builder {
	b.authManager = mgr
	return b
}

// WithRequestAccessManager overrides the request authentication manager.
func (b *Builder) WithRequestAccessManager(mgr *sdkaccess.Manager) *Builder {
	b.accessManager = mgr
	return b
}

// WithCoreAuthManager overrides the runtime auth manager responsible for request execution.
func (b *Builder) WithCoreAuthManager(mgr *coreauth.Manager) *Builder {
	b.coreManager = mgr
	return b
}

// WithServerOptions appends server configuration options used during construction.
func (b *Builder) WithServerOptions(opts ...api.ServerOption) *Builder {
	b.serverOptions = append(b.serverOptions, opts...)
	return b
}

// WithLocalManagementPassword configures a password that is only accepted from localhost management requests.
func (b *Builder) WithLocalManagementPassword(password string) *Builder {
	if password == "" {
		return b
	}
	b.serverOptions = append(b.serverOptions, api.WithLocalManagementPassword(password))
	return b
}

// loadAndMergeProviderConfigs loads provider configurations from the YAML file
// and merges them into the existing config (which contains server settings from EmbedConfig).
// This allows WithEmbedConfig to provide server configuration while WithConfigPath provides
// provider-specific configurations (API keys, OAuth accounts, model mappings, etc.).
func (b *Builder) loadAndMergeProviderConfigs() error {
	// Load YAML config containing provider configurations
	yamlCfg, err := config.LoadConfig(b.configPath)
	if err != nil {
		return fmt.Errorf("failed to load config from %s: %w", b.configPath, err)
	}

	// Merge provider configs from YAML into the existing config
	// Keep server settings from b.cfg (which came from EmbedConfig)
	// and merge in provider configs from yamlCfg
	b.cfg.GeminiKey = yamlCfg.GeminiKey
	b.cfg.ClaudeKey = yamlCfg.ClaudeKey
	b.cfg.CodexKey = yamlCfg.CodexKey
	b.cfg.VertexCompatAPIKey = yamlCfg.VertexCompatAPIKey
	b.cfg.OpenAICompatibility = yamlCfg.OpenAICompatibility
	b.cfg.AmpCode = yamlCfg.AmpCode
	b.cfg.OAuthExcludedModels = yamlCfg.OAuthExcludedModels
	b.cfg.Payload = yamlCfg.Payload

	// Merge SDK config fields but be selective to avoid overriding EmbedConfig settings
	if len(yamlCfg.APIKeys) > 0 {
		b.cfg.APIKeys = yamlCfg.APIKeys
	}
	if yamlCfg.ProxyURL != "" {
		b.cfg.ProxyURL = yamlCfg.ProxyURL
	}
	if yamlCfg.RequestLog {
		b.cfg.RequestLog = yamlCfg.RequestLog
	}
	// Note: We don't merge Access.Providers as they may reference unregistered provider types

	return nil
}

// Build validates inputs, applies defaults, and returns a ready-to-run service.
func (b *Builder) Build() (*Service, error) {
	// Register access providers before building
	configaccess.Register()

	// Check for configuration validation errors from WithEmbedConfig
	if b.configErr != nil {
		return nil, b.configErr
	}

	if b.cfg == nil {
		return nil, fmt.Errorf("cliproxy: configuration is required")
	}
	if b.configPath == "" {
		return nil, fmt.Errorf("cliproxy: configuration is required")
	}

	// Load provider configurations from YAML file and merge with EmbedConfig
	// This allows EmbedConfig to provide server settings while YAML provides provider configs
	if err := b.loadAndMergeProviderConfigs(); err != nil {
		return nil, fmt.Errorf("failed to load provider configs: %w", err)
	}

	tokenProvider := b.tokenProvider
	if tokenProvider == nil {
		tokenProvider = NewFileTokenClientProvider()
	}

	apiKeyProvider := b.apiKeyProvider
	if apiKeyProvider == nil {
		apiKeyProvider = NewAPIKeyClientProvider()
	}

	watcherFactory := b.watcherFactory
	if watcherFactory == nil {
		watcherFactory = defaultWatcherFactory
	}

	authManager := b.authManager
	if authManager == nil {
		authManager = newDefaultAuthManager()
	}

	accessManager := b.accessManager
	if accessManager == nil {
		accessManager = sdkaccess.NewManager()
	}

	providers, err := sdkaccess.BuildProviders(&b.cfg.SDKConfig)
	if err != nil {
		return nil, err
	}
	accessManager.SetProviders(providers)

	coreManager := b.coreManager
	if coreManager == nil {
		tokenStore := sdkAuth.GetTokenStore()
		if dirSetter, ok := tokenStore.(interface{ SetBaseDir(string) }); ok && b.cfg != nil {
			dirSetter.SetBaseDir(b.cfg.AuthDir)
		}
		coreManager = coreauth.NewManager(tokenStore, nil, nil)
	}
	// Attach a default RoundTripper provider so providers can opt-in per-auth transports.
	coreManager.SetRoundTripperProvider(newDefaultRoundTripperProvider())

	service := &Service{
		cfg:            b.cfg,
		configPath:     b.configPath,
		tokenProvider:  tokenProvider,
		apiKeyProvider: apiKeyProvider,
		watcherFactory: watcherFactory,
		hooks:          b.hooks,
		authManager:    authManager,
		accessManager:  accessManager,
		coreManager:    coreManager,
		serverOptions:  append([]api.ServerOption(nil), b.serverOptions...),
	}
	return service, nil
}
