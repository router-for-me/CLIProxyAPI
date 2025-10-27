// Package cliproxy provides the core service implementation for the CLI Proxy API.
// It includes service lifecycle management, authentication handling, file watching,
// and integration with various AI service providers through a unified interface.
package cliproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/wsrelay"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

var copilotExclusiveModelIDs = map[string]struct{}{
	"gpt-5-mini":       {},
	"grok-code-fast-1": {},
	"gpt-5":            {},
	"gpt-4.1":          {},
	"gpt-4":            {},
	"gpt-4o-mini":      {},
	"gpt-3.5-turbo":    {},
}

// Service wraps the proxy server lifecycle so external programs can embed the CLI proxy.
// It manages the complete lifecycle including authentication, file watching, HTTP server,
// and integration with various AI service providers.
type Service struct {
	// cfg holds the current application configuration.
	cfg *config.Config

	// cfgMu protects concurrent access to the configuration.
	cfgMu sync.RWMutex

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

	// serverOptions contains additional server configuration options.
	serverOptions []api.ServerOption

	// server is the HTTP API server instance.
	server *api.Server

	// serverErr channel for server startup/shutdown errors.
	serverErr chan error

	// watcher handles file system monitoring.
	watcher *WatcherWrapper

	// watcherCancel cancels the watcher context.
	watcherCancel context.CancelFunc

	// authUpdates channel for authentication updates.
	authUpdates chan watcher.AuthUpdate

	// authQueueStop cancels the auth update queue processing.
	authQueueStop context.CancelFunc

	// authManager handles legacy authentication operations.
	authManager *sdkAuth.Manager

	// accessManager handles request authentication providers.
	accessManager *sdkaccess.Manager

	// coreManager handles core authentication and execution.
	coreManager *coreauth.Manager

	// Python bridge removed

	// shutdownOnce ensures shutdown is called only once.
	shutdownOnce sync.Once

	// wsGateway manages websocket Gemini providers.
	wsGateway *wsrelay.Manager
}

// RegisterUsagePlugin registers a usage plugin on the global usage manager.
// This allows external code to monitor API usage and token consumption.
//
// Parameters:
//   - plugin: The usage plugin to register
func (s *Service) RegisterUsagePlugin(plugin usage.Plugin) {
	usage.RegisterPlugin(plugin)
}

// newDefaultAuthManager creates a default authentication manager with all supported providers.
func newDefaultAuthManager() *sdkAuth.Manager {
	return sdkAuth.NewManager(
		sdkAuth.GetTokenStore(),
		sdkAuth.NewGeminiAuthenticator(),
		sdkAuth.NewCodexAuthenticator(),
		sdkAuth.NewClaudeAuthenticator(),
		sdkAuth.NewQwenAuthenticator(),
	)
}

func (s *Service) ensureAuthUpdateQueue(ctx context.Context) {
	if s == nil {
		return
	}
	if s.authUpdates == nil {
		s.authUpdates = make(chan watcher.AuthUpdate, 256)
	}
	if s.authQueueStop != nil {
		return
	}
	queueCtx, cancel := context.WithCancel(ctx)
	s.authQueueStop = cancel
	go s.consumeAuthUpdates(queueCtx)
}

func (s *Service) consumeAuthUpdates(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-s.authUpdates:
			if !ok {
				return
			}
			s.handleAuthUpdate(ctx, update)
		labelDrain:
			for {
				select {
				case nextUpdate := <-s.authUpdates:
					s.handleAuthUpdate(ctx, nextUpdate)
				default:
					break labelDrain
				}
			}
		}
	}
}

func (s *Service) handleAuthUpdate(ctx context.Context, update watcher.AuthUpdate) {
	if s == nil {
		return
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil || s.coreManager == nil {
		return
	}
	switch update.Action {
	case watcher.AuthUpdateActionAdd, watcher.AuthUpdateActionModify:
		if update.Auth == nil || update.Auth.ID == "" {
			return
		}
		s.applyCoreAuthAddOrUpdate(ctx, update.Auth)
	case watcher.AuthUpdateActionDelete:
		id := update.ID
		if id == "" && update.Auth != nil {
			id = update.Auth.ID
		}
		if id == "" {
			return
		}
		s.applyCoreAuthRemoval(ctx, id)
	default:
		log.Debugf("received unknown auth update action: %v", update.Action)
	}
}

func (s *Service) ensureWebsocketGateway() {
	if s == nil {
		return
	}
	if s.wsGateway != nil {
		return
	}
	opts := wsrelay.Options{
		Path:           "/v1/ws",
		OnConnected:    s.wsOnConnected,
		OnDisconnected: s.wsOnDisconnected,
		LogDebugf:      log.Debugf,
		LogInfof:       log.Infof,
		LogWarnf:       log.Warnf,
	}
	s.wsGateway = wsrelay.NewManager(opts)
}

func (s *Service) wsOnConnected(provider string) {
	if s == nil || provider == "" {
		return
	}
	if !strings.HasPrefix(strings.ToLower(provider), "aistudio-") {
		return
	}
	if s.coreManager != nil {
		if existing, ok := s.coreManager.GetByID(provider); ok && existing != nil {
			if !existing.Disabled && existing.Status == coreauth.StatusActive {
				return
			}
		}
	}
	now := time.Now().UTC()
	auth := &coreauth.Auth{
		ID:         provider,
		Provider:   provider,
		Label:      provider,
		Status:     coreauth.StatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
		Attributes: map[string]string{"ws_provider": "gemini"},
	}
	log.Infof("websocket provider connected: %s", provider)
	s.applyCoreAuthAddOrUpdate(context.Background(), auth)
}

func (s *Service) wsOnDisconnected(provider string, reason error) {
	if s == nil || provider == "" {
		return
	}
	if reason != nil {
		if strings.Contains(reason.Error(), "replaced by new connection") {
			log.Infof("websocket provider replaced: %s", provider)
			return
		}
		log.Warnf("websocket provider disconnected: %s (%v)", provider, reason)
	} else {
		log.Infof("websocket provider disconnected: %s", provider)
	}
	ctx := context.Background()
	s.applyCoreAuthRemoval(ctx, provider)
	if s.coreManager != nil {
		s.coreManager.UnregisterExecutor(provider)
	}
}

func (s *Service) applyCoreAuthAddOrUpdate(ctx context.Context, auth *coreauth.Auth) {
	if s == nil || auth == nil || auth.ID == "" {
		return
	}
	if s.coreManager == nil {
		return
	}
	auth = auth.Clone()
	s.ensureExecutorsForAuth(auth)
	// Diagnostics: preview effective base_url for copilot (masked)
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "copilot") {
		if preview := copilotBaseURLPreview(auth); preview != "" {
			log.Infof("copilot auth registered: id=%s base_url=%s", auth.ID, preview)
		}
		// Defensive: when auth.Attributes.base_url points to a codex backend path, it cannot
		// serve Copilot chat/completions and often returns 401. Clear it so executor falls back
		// to the canonical https://api.githubcopilot.com host. Explicit non-codex base_url is kept.
		if auth.Attributes != nil {
			if raw := strings.TrimSpace(auth.Attributes["base_url"]); raw != "" {
				if strings.HasSuffix(strings.TrimRight(raw, "/"), "/backend-api/codex") {
					delete(auth.Attributes, "base_url")
				}
			}
		}
	}
	s.registerModelsForAuth(auth)
	if existing, ok := s.coreManager.GetByID(auth.ID); ok && existing != nil {
		auth.CreatedAt = existing.CreatedAt
		auth.LastRefreshedAt = existing.LastRefreshedAt
		auth.NextRefreshAfter = existing.NextRefreshAfter
		if _, err := s.coreManager.Update(ctx, auth); err != nil {
			log.Errorf("failed to update auth %s: %v", auth.ID, err)
		}
		return
	}
	if _, err := s.coreManager.Register(ctx, auth); err != nil {
		log.Errorf("failed to register auth %s: %v", auth.ID, err)
	}
}

func (s *Service) applyCoreAuthRemoval(ctx context.Context, id string) {
	if s == nil || id == "" {
		return
	}
	if s.coreManager == nil {
		return
	}
	GlobalModelRegistry().UnregisterClient(id)
	if existing, ok := s.coreManager.GetByID(id); ok && existing != nil {
		existing.Disabled = true
		existing.Status = coreauth.StatusDisabled
		if _, err := s.coreManager.Update(ctx, existing); err != nil {
			log.Errorf("failed to disable auth %s: %v", id, err)
		}
	}
}

func openAICompatInfoFromAuth(a *coreauth.Auth) (providerKey string, compatName string, ok bool) {
	if a == nil {
		return "", "", false
	}
	if len(a.Attributes) > 0 {
		providerKey = strings.TrimSpace(a.Attributes["provider_key"])
		compatName = strings.TrimSpace(a.Attributes["compat_name"])
		if providerKey != "" || compatName != "" {
			if providerKey == "" {
				providerKey = compatName
			}
			return strings.ToLower(providerKey), compatName, true
		}
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "openai-compatibility") {
		return "openai-compatibility", strings.TrimSpace(a.Label), true
	}
	return "", "", false
}

func (s *Service) ensureExecutorsForAuth(a *coreauth.Auth) {
	if s == nil || a == nil {
		return
	}
	if compatProviderKey, _, isCompat := openAICompatInfoFromAuth(a); isCompat {
		if compatProviderKey == "" {
			compatProviderKey = strings.ToLower(strings.TrimSpace(a.Provider))
		}
		if compatProviderKey == "" {
			compatProviderKey = "openai-compatibility"
		}
		s.coreManager.RegisterExecutor(executor.NewOpenAICompatExecutor(compatProviderKey, s.cfg))
		return
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.Provider)), "aistudio-") {
		if s.wsGateway != nil {
			s.coreManager.RegisterExecutor(executor.NewAistudioExecutor(s.cfg, a.Provider, s.wsGateway))
		}
		return
	}
	switch strings.ToLower(a.Provider) {
	case "gemini":
		s.coreManager.RegisterExecutor(executor.NewGeminiExecutor(s.cfg))
	case "gemini-cli":
		s.coreManager.RegisterExecutor(executor.NewGeminiCLIExecutor(s.cfg))
	case "claude":
		s.coreManager.RegisterExecutor(executor.NewClaudeExecutor(s.cfg))
	case "codex":
		s.coreManager.RegisterExecutor(executor.NewCodexExecutorWithID(s.cfg, "codex"))
	case "packycode":
		// 外部 provider=packycode → 内部复用 codex 执行器
		s.coreManager.RegisterExecutor(executor.NewCodexExecutorWithID(s.cfg, "packycode"))
	case "copilot":
		// Copilot 使用独立的执行器以便与 Codex 路径解耦
		s.coreManager.RegisterExecutor(executor.NewCopilotExecutor(s.cfg))
	case "qwen":
		s.coreManager.RegisterExecutor(executor.NewQwenExecutor(s.cfg))
	case "zhipu":
		s.coreManager.RegisterExecutor(executor.NewZhipuExecutor(s.cfg))
	case "iflow":
		s.coreManager.RegisterExecutor(executor.NewIFlowExecutor(s.cfg))
	default:
		providerKey := strings.ToLower(strings.TrimSpace(a.Provider))
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		s.coreManager.RegisterExecutor(executor.NewOpenAICompatExecutor(providerKey, s.cfg))
	}
}

// rebindExecutors refreshes provider executors so they observe the latest configuration.
func (s *Service) rebindExecutors() {
	if s == nil || s.coreManager == nil {
		return
	}
	auths := s.coreManager.List()
	for _, auth := range auths {
		s.ensureExecutorsForAuth(auth)
	}
}

// copilotBaseURLPreview computes a safe-to-log preview of the effective base URL for Copilot.
// Priority: attributes.base_url > metadata.base_url > derive from access_token.proxy-ep.
// The returned value is masked to scheme+host only (e.g., https://proxy.example.com).
func copilotBaseURLPreview(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	// 1) Attributes
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["base_url"]); v != "" {
			if host := maskURLHost(v); host != "" {
				return host
			}
		}
	}
	// 2) Metadata
	if a.Metadata != nil {
		if v, ok := a.Metadata["base_url"].(string); ok {
			if host := maskURLHost(v); host != "" {
				return host
			}
		}
		// 3) Derive from access_token proxy-ep
		if v, ok := a.Metadata["access_token"].(string); ok {
			if derived := deriveCopilotBaseFromTokenPreview(v); derived != "" {
				if host := maskURLHost(derived); host != "" {
					return host
				}
			}
		}
	}
	return ""
}

// deriveCopilotBaseFromTokenPreview extracts a base like https://<proxy-ep>/backend-api/codex from token when possible.
func deriveCopilotBaseFromTokenPreview(tok string) string {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return ""
	}
	const marker = "proxy-ep="
	idx := strings.Index(tok, marker)
	if idx < 0 {
		return ""
	}
	rest := tok[idx+len(marker):]
	if end := strings.IndexByte(rest, ';'); end >= 0 {
		rest = rest[:end]
	}
	ep := strings.TrimSpace(rest)
	if ep == "" {
		return ""
	}
	if !strings.Contains(ep, "://") {
		ep = "https://" + ep
	}
	ep = strings.TrimRight(ep, "/")
	if !strings.HasSuffix(ep, "/backend-api/codex") {
		ep += "/backend-api/codex"
	}
	return ep
}

// maskURLHost reduces a URL to scheme://host for safe logging; returns empty on parse failure.
func maskURLHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Run starts the service and blocks until the context is cancelled or the server stops.
// It initializes all components including authentication, file watching, HTTP server,
// and starts processing requests. The method blocks until the context is cancelled.
//
// Parameters:
//   - ctx: The context for controlling the service lifecycle
//
// Returns:
//   - error: An error if the service fails to start or run
func (s *Service) Run(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("cliproxy: service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	usage.StartDefault(ctx)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	defer func() {
		if err := s.Shutdown(shutdownCtx); err != nil {
			log.Errorf("service shutdown returned error: %v", err)
		}
	}()

	if err := s.ensureAuthDir(); err != nil {
		return err
	}

	if s.coreManager != nil {
		if errLoad := s.coreManager.Load(ctx); errLoad != nil {
			log.Warnf("failed to load auth store: %v", errLoad)
		}
		s.rebindExecutors()
	}

	tokenResult, err := s.tokenProvider.Load(ctx, s.cfg)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	if tokenResult == nil {
		tokenResult = &TokenClientResult{}
	}

	apiKeyResult, err := s.apiKeyProvider.Load(ctx, s.cfg)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	if apiKeyResult == nil {
		apiKeyResult = &APIKeyClientResult{}
	}

	// legacy clients removed; no caches to refresh

	// handlers no longer depend on legacy clients; pass nil slice initially
	s.server = api.NewServer(s.cfg, s.coreManager, s.accessManager, s.configPath, s.serverOptions...)

	if s.authManager == nil {
		s.authManager = newDefaultAuthManager()
	}

	// Python bridge removed

	s.ensureWebsocketGateway()
	if s.server != nil && s.wsGateway != nil {
		s.server.AttachWebsocketRoute(s.wsGateway.Path(), s.wsGateway.Handler())
		s.server.SetWebsocketAuthChangeHandler(func(oldEnabled, newEnabled bool) {
			if oldEnabled == newEnabled {
				return
			}
			if !oldEnabled && newEnabled {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if errStop := s.wsGateway.Stop(ctx); errStop != nil {
					log.Warnf("failed to reset websocket connections after ws-auth change %t -> %t: %v", oldEnabled, newEnabled, errStop)
					return
				}
				log.Debugf("ws-auth enabled; existing websocket sessions terminated to enforce authentication")
				return
			}
			log.Debugf("ws-auth disabled; existing websocket sessions remain connected")
		})
	}

	if s.hooks.OnBeforeStart != nil {
		s.hooks.OnBeforeStart(s.cfg)
	}

	s.serverErr = make(chan error, 1)
	go func() {
		if errStart := s.server.Start(); errStart != nil {
			s.serverErr <- errStart
		} else {
			s.serverErr <- nil
		}
	}()

	time.Sleep(100 * time.Millisecond)
	fmt.Println("API server started successfully")

	if s.hooks.OnAfterStart != nil {
		s.hooks.OnAfterStart(s)
	}

	// Ensure Packycode and Copilot models are registered for /v1/models visibility
	s.ensurePackycodeModelsRegistered(s.cfg)
	s.ensureCopilotModelsRegistered(s.cfg)

	var watcherWrapper *WatcherWrapper
	reloadCallback := func(newCfg *config.Config) {
		if newCfg == nil {
			s.cfgMu.RLock()
			newCfg = s.cfg
			s.cfgMu.RUnlock()
		}
		if newCfg == nil {
			return
		}
		if s.server != nil {
			s.server.UpdateClients(newCfg)
		}
		s.cfgMu.Lock()
		s.cfg = newCfg
		s.cfgMu.Unlock()
		// Keep model registry in sync for Packycode and Copilot
		s.ensurePackycodeModelsRegistered(newCfg)
		s.ensureCopilotModelsRegistered(newCfg)
		s.rebindExecutors()
	}

	watcherWrapper, err = s.watcherFactory(s.configPath, s.cfg.AuthDir, reloadCallback)
	if err != nil {
		return fmt.Errorf("cliproxy: failed to create watcher: %w", err)
	}
	s.watcher = watcherWrapper
	s.ensureAuthUpdateQueue(ctx)
	if s.authUpdates != nil {
		watcherWrapper.SetAuthUpdateQueue(s.authUpdates)
	}
	watcherWrapper.SetConfig(s.cfg)

	watcherCtx, watcherCancel := context.WithCancel(context.Background())
	s.watcherCancel = watcherCancel
	if err = watcherWrapper.Start(watcherCtx); err != nil {
		return fmt.Errorf("cliproxy: failed to start watcher: %w", err)
	}
	log.Info("file watcher started for config and auth directory changes")

	// Prefer core auth manager auto refresh if available.
	if s.coreManager != nil {
		interval := 15 * time.Minute
		s.coreManager.StartAutoRefresh(context.Background(), interval)
		log.Infof("core auth auto-refresh started (interval=%s)", interval)
	}

	select {
	case <-ctx.Done():
		log.Debug("service context cancelled, shutting down...")
		return ctx.Err()
	case err = <-s.serverErr:
		return err
	}
}

// Shutdown gracefully stops background workers and the HTTP server.
// It ensures all resources are properly cleaned up and connections are closed.
// The shutdown is idempotent and can be called multiple times safely.
//
// Parameters:
//   - ctx: The context for controlling the shutdown timeout
//
// Returns:
//   - error: An error if shutdown fails
func (s *Service) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var shutdownErr error
	s.shutdownOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}

		// legacy refresh loop removed; only stopping core auth manager below

		if s.watcherCancel != nil {
			s.watcherCancel()
		}
		if s.coreManager != nil {
			s.coreManager.StopAutoRefresh()
		}
		if s.watcher != nil {
			if err := s.watcher.Stop(); err != nil {
				log.Errorf("failed to stop file watcher: %v", err)
				shutdownErr = err
			}
		}
		if s.wsGateway != nil {
			if err := s.wsGateway.Stop(ctx); err != nil {
				log.Errorf("failed to stop websocket gateway: %v", err)
				if shutdownErr == nil {
					shutdownErr = err
				}
			}
		}
		if s.authQueueStop != nil {
			s.authQueueStop()
			s.authQueueStop = nil
		}

		// Python bridge removed

		// no legacy clients to persist

		if s.server != nil {
			shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := s.server.Stop(shutdownCtx); err != nil {
				log.Errorf("error stopping API server: %v", err)
				if shutdownErr == nil {
					shutdownErr = err
				}
			}
		}

		usage.StopDefault()
	})
	return shutdownErr
}

// packycodeModelsClientID computes a stable registry client ID for Packycode models
// based on base-url and openai api key. Returns (id, true) when packycode is enabled
// and configuration is valid; otherwise ("", false).
func (s *Service) packycodeModelsClientID(cfg *config.Config) (string, bool) {
	if cfg == nil || !cfg.Packycode.Enabled {
		return "", false
	}
	if err := config.ValidatePackycode(cfg); err != nil {
		return "", false
	}
	base := strings.TrimSpace(cfg.Packycode.BaseURL)
	key := strings.TrimSpace(cfg.Packycode.Credentials.OpenAIAPIKey)
	h := sha256.New()
	h.Write([]byte("packycode:models"))
	h.Write([]byte{0})
	h.Write([]byte(base))
	h.Write([]byte{0})
	h.Write([]byte(key))
	digest := hex.EncodeToString(h.Sum(nil))
	if len(digest) > 12 {
		digest = digest[:12]
	}
	return "packycode:models:" + digest, true
}

// ensurePackycodeModelsRegistered registers/unregisters Packycode OpenAI models
// in the global model registry depending on current configuration state.
func (s *Service) ensurePackycodeModelsRegistered(cfg *config.Config) {
	id, ok := s.packycodeModelsClientID(cfg)
	if !ok {
		// Best-effort removal using deterministic ID if any
		// Build ID ignoring enabled flag to attempt cleanup when toggled off
		base := strings.TrimSpace(cfg.Packycode.BaseURL)
		key := strings.TrimSpace(cfg.Packycode.Credentials.OpenAIAPIKey)
		h := sha256.New()
		h.Write([]byte("packycode:models"))
		h.Write([]byte{0})
		h.Write([]byte(base))
		h.Write([]byte{0})
		h.Write([]byte(key))
		digest := hex.EncodeToString(h.Sum(nil))
		if len(digest) > 12 {
			digest = digest[:12]
		}
		if digest != "" {
			GlobalModelRegistry().UnregisterClient("packycode:models:" + digest)
		}
		return
	}
	// Ensure executor exists early to avoid executor_not_found
	if s.coreManager != nil {
		// Register codex executor but expose provider name as 'packycode' via alias (handled in ensureExecutorsForAuth)
		s.coreManager.RegisterExecutor(executor.NewCodexExecutorWithID(s.cfg, "packycode"))
	}
	models := registry.GetOpenAIModels()
	models = filterModelsByID(models, copilotExclusiveModelIDs)
	// Register models under external provider key 'packycode' (internally still served by codex executor)
	GlobalModelRegistry().RegisterClient(id, "packycode", models)
	// Also ensure there is at least one runtime auth for provider 'packycode'
	if s.coreManager != nil {
		base := strings.TrimSpace(cfg.Packycode.BaseURL)
		key := strings.TrimSpace(cfg.Packycode.Credentials.OpenAIAPIKey)
		// Derive a stable auth ID similar to watcher synth rule
		ah := sha256.New()
		ah.Write([]byte("packycode:codex"))
		ah.Write([]byte{0})
		ah.Write([]byte(key))
		ah.Write([]byte{0})
		ah.Write([]byte(base))
		ad := hex.EncodeToString(ah.Sum(nil))
		if len(ad) > 12 {
			ad = ad[:12]
		}
		authID := "packycode:codex:" + ad
		now := time.Now()
		runtimeAuth := &coreauth.Auth{
			ID:         authID,
			Provider:   "packycode",
			Label:      "packycode",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"api_key": key, "base_url": base, "source": "packycode"},
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		// Register or update
		if _, ok := s.coreManager.GetByID(authID); ok {
			_, _ = s.coreManager.Update(context.Background(), runtimeAuth)
		} else {
			_, _ = s.coreManager.Register(context.Background(), runtimeAuth)
		}
	}
}

func (s *Service) ensureAuthDir() error {
	info, err := os.Stat(s.cfg.AuthDir)
	if err != nil {
		if os.IsNotExist(err) {
			if mkErr := os.MkdirAll(s.cfg.AuthDir, 0o755); mkErr != nil {
				return fmt.Errorf("cliproxy: failed to create auth directory %s: %w", s.cfg.AuthDir, mkErr)
			}
			log.Infof("created missing auth directory: %s", s.cfg.AuthDir)
			return nil
		}
		return fmt.Errorf("cliproxy: error checking auth directory %s: %w", s.cfg.AuthDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cliproxy: auth path exists but is not a directory: %s", s.cfg.AuthDir)
	}
	return nil
}

// registerModelsForAuth (re)binds provider models in the global registry using the core auth ID as client identifier.
// ensureCopilotModelsRegistered registers base Copilot inventory even before any auth is added,
// so that /v1/models can advertise provider=copilot and its models. When a real copilot auth
// appears, registerModelsForAuth will re-register with the auth ID, superseding this seed.
func (s *Service) ensureCopilotModelsRegistered(cfg *config.Config) {
	id := "copilot:models:seed"
	models := registry.GetCopilotModels()
	if len(models) == 0 {
		registry.GetGlobalRegistry().UnregisterClient(id)
		return
	}
	// Register under provider key 'copilot'
	registry.GetGlobalRegistry().RegisterClient(id, "copilot", models)
}

func filterModelsByID(models []*registry.ModelInfo, exclude map[string]struct{}) []*registry.ModelInfo {
	if len(models) == 0 || len(exclude) == 0 {
		return models
	}
	out := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		if _, blocked := exclude[strings.TrimSpace(model.ID)]; blocked {
			continue
		}
		out = append(out, model)
	}
	return out
}

func (s *Service) registerModelsForAuth(a *coreauth.Auth) {
	if a == nil || a.ID == "" {
		return
	}
	// Unregister legacy client ID (if present) to avoid double counting
	if a.Runtime != nil {
		if idGetter, ok := a.Runtime.(interface{ GetClientID() string }); ok {
			if rid := idGetter.GetClientID(); rid != "" && rid != a.ID {
				GlobalModelRegistry().UnregisterClient(rid)
			}
		}
	}
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	compatProviderKey, compatDisplayName, compatDetected := openAICompatInfoFromAuth(a)
	if a.Attributes != nil {
		if strings.EqualFold(a.Attributes["ws_provider"], "gemini") {
			models := mergeGeminiModels()
			GlobalModelRegistry().RegisterClient(a.ID, provider, models)
			return
		}
	}
	if compatDetected {
		provider = "openai-compatibility"
	}
	var models []*ModelInfo
	switch provider {
	case "gemini":
		models = registry.GetGeminiModels()
	case "gemini-cli":
		models = registry.GetGeminiCLIModels()
	case "claude":
		// 检测：当 claude 的 base_url 指向特定 Anthropic 兼容端点时，按后端仅注册对应模型，并将 provider 标记为实际后端
		if a.Attributes != nil {
			if v := strings.TrimSpace(a.Attributes["base_url"]); v != "" {
				// 智谱 Anthropic 兼容 → 默认使用 Claude 执行器对外提供（provider=claude），模型=glm-4.6
				if strings.EqualFold(v, "https://open.bigmodel.cn/api/anthropic") {
					z := registry.GetZhipuModels()
					only := make([]*ModelInfo, 0, 1)
					for i := range z {
						if z[i] != nil && strings.TrimSpace(z[i].ID) == "glm-4.6" {
							only = append(only, z[i])
							break
						}
					}
					GlobalModelRegistry().RegisterClient(a.ID, "claude", only)
					return
				}
				// MiniMax Anthropic 兼容 → 默认使用 Claude 执行器对外提供（provider=claude），模型=MiniMax-M2
				if strings.EqualFold(v, "https://api.minimaxi.com/anthropic") {
					mm := registry.GetMiniMaxModels()
					only := make([]*ModelInfo, 0, 1)
					for i := range mm {
						if mm[i] != nil && strings.TrimSpace(mm[i].ID) == "MiniMax-M2" {
							only = append(only, mm[i])
							break
						}
					}
					GlobalModelRegistry().RegisterClient(a.ID, "claude", only)
					return
				}
			}
		}
		models = registry.GetClaudeModels()
	case "codex":
		models = filterModelsByID(registry.GetOpenAIModels(), copilotExclusiveModelIDs)
	case "packycode":
		// 对外 provider=packycode 映射到 OpenAI(GPT) 模型集合
		models = filterModelsByID(registry.GetOpenAIModels(), copilotExclusiveModelIDs)
	case "copilot":
		// copilot 使用专属的清单（当前与 OpenAI 同步，后续可替换为真实 inventory）
		models = registry.GetCopilotModels()
	case "qwen":
		models = registry.GetQwenModels()
	case "iflow":
		models = registry.GetIFlowModels()
	case "zhipu":
		models = registry.GetZhipuModels()
	default:
		// Handle OpenAI-compatibility providers by name using config
		if s.cfg != nil {
			providerKey := provider
			compatName := strings.TrimSpace(a.Provider)
			isCompatAuth := false
			if compatDetected {
				if compatProviderKey != "" {
					providerKey = compatProviderKey
				}
				if compatDisplayName != "" {
					compatName = compatDisplayName
				}
				isCompatAuth = true
			}
			if strings.EqualFold(providerKey, "openai-compatibility") {
				isCompatAuth = true
				if a.Attributes != nil {
					if v := strings.TrimSpace(a.Attributes["compat_name"]); v != "" {
						compatName = v
					}
					if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
						providerKey = strings.ToLower(v)
						isCompatAuth = true
					}
				}
				if providerKey == "openai-compatibility" && compatName != "" {
					providerKey = strings.ToLower(compatName)
				}
			} else if a.Attributes != nil {
				if v := strings.TrimSpace(a.Attributes["compat_name"]); v != "" {
					compatName = v
					isCompatAuth = true
				}
				if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
					providerKey = strings.ToLower(v)
					isCompatAuth = true
				}
			}
			for i := range s.cfg.OpenAICompatibility {
				compat := &s.cfg.OpenAICompatibility[i]
				if strings.EqualFold(compat.Name, compatName) {
					isCompatAuth = true
					// Convert compatibility models to registry models
					ms := make([]*ModelInfo, 0, len(compat.Models))
					for j := range compat.Models {
						m := compat.Models[j]
						// Use alias as model ID, fallback to name if alias is empty
						modelID := m.Alias
						if modelID == "" {
							modelID = m.Name
						}
						ms = append(ms, &ModelInfo{
							ID:          modelID,
							Object:      "model",
							Created:     time.Now().Unix(),
							OwnedBy:     compat.Name,
							Type:        "openai-compatibility",
							DisplayName: m.Name,
						})
					}
					// Register and return
					if len(ms) > 0 {
						if providerKey == "" {
							providerKey = "openai-compatibility"
						}
						GlobalModelRegistry().RegisterClient(a.ID, providerKey, ms)
					} else {
						// Ensure stale registrations are cleared when model list becomes empty.
						GlobalModelRegistry().UnregisterClient(a.ID)
					}
					return
				}
			}
			if isCompatAuth {
				// No matching provider found or models removed entirely; drop any prior registration.
				GlobalModelRegistry().UnregisterClient(a.ID)
				return
			}
		}
	}
	if len(models) > 0 {
		key := provider
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(a.Provider))
		}
		GlobalModelRegistry().RegisterClient(a.ID, key, models)
	}
}

func mergeGeminiModels() []*ModelInfo {
	models := make([]*ModelInfo, 0, 16)
	seen := make(map[string]struct{})
	appendModels := func(items []*ModelInfo) {
		for i := range items {
			m := items[i]
			if m == nil || m.ID == "" {
				continue
			}
			if _, ok := seen[m.ID]; ok {
				continue
			}
			seen[m.ID] = struct{}{}
			models = append(models, m)
		}
	}
	appendModels(registry.GetGeminiModels())
	appendModels(registry.GetGeminiCLIModels())
	return models
}
