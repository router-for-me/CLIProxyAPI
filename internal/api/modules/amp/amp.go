// Package amp implements the Amp CLI routing module, providing OAuth-based
// integration with Amp CLI for ChatGPT and Anthropic subscriptions.
package amp

import (
	"fmt"
	"net/http/httputil"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/modules"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	log "github.com/sirupsen/logrus"
)

// Option configures the AmpModule.
type Option func(*AmpModule)

// AmpModule implements the RouteModuleV2 interface for Amp CLI integration.
// It provides:
//   - Reverse proxy to Amp control plane for OAuth/management
//   - Provider-specific route aliases (/api/provider/{provider}/...)
//   - Automatic gzip decompression for misconfigured upstreams
//   - Model mapping for routing unavailable models to alternatives
type AmpModule struct {
	secretSource    SecretSource
	proxy           *httputil.ReverseProxy
	proxyMu         sync.RWMutex // protects proxy for hot-reload
	accessManager   *sdkaccess.Manager
	authMiddleware_ gin.HandlerFunc
	modelMapper     *DefaultModelMapper
	enabled         bool
	registerOnce    sync.Once

	// restrictToLocalhost controls localhost-only access for management routes (hot-reloadable)
	restrictToLocalhost bool
	restrictMu          sync.RWMutex

	// configMu protects lastConfig for partial reload comparison
	configMu   sync.RWMutex
	lastConfig *config.AmpCode
}

// New creates a new Amp routing module with the given options.
// This is the preferred constructor using the Option pattern.
//
// Example:
//
//	ampModule := amp.New(
//	    amp.WithAccessManager(accessManager),
//	    amp.WithAuthMiddleware(authMiddleware),
//	    amp.WithSecretSource(customSecret),
//	)
func New(opts ...Option) *AmpModule {
	m := &AmpModule{
		secretSource: nil, // Will be created on demand if not provided
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// NewLegacy creates a new Amp routing module using the legacy constructor signature.
// This is provided for backwards compatibility.
//
// DEPRECATED: Use New with options instead.
func NewLegacy(accessManager *sdkaccess.Manager, authMiddleware gin.HandlerFunc) *AmpModule {
	return New(
		WithAccessManager(accessManager),
		WithAuthMiddleware(authMiddleware),
	)
}

// WithSecretSource sets a custom secret source for the module.
func WithSecretSource(source SecretSource) Option {
	return func(m *AmpModule) {
		m.secretSource = source
	}
}

// WithAccessManager sets the access manager for the module.
func WithAccessManager(am *sdkaccess.Manager) Option {
	return func(m *AmpModule) {
		m.accessManager = am
	}
}

// WithAuthMiddleware sets the authentication middleware for provider routes.
func WithAuthMiddleware(middleware gin.HandlerFunc) Option {
	return func(m *AmpModule) {
		m.authMiddleware_ = middleware
	}
}

// Name returns the module identifier
func (m *AmpModule) Name() string {
	return "amp-routing"
}

// forceModelMappings returns whether model mappings should take precedence over local API keys
func (m *AmpModule) forceModelMappings() bool {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	if m.lastConfig == nil {
		return false
	}
	return m.lastConfig.ForceModelMappings
}

// Register sets up Amp routes if configured.
// This implements the RouteModuleV2 interface with Context.
// Routes are registered only once via sync.Once for idempotent behavior.
func (m *AmpModule) Register(ctx modules.Context) error {
	settings := ctx.Config.AmpCode
	upstreamURL := strings.TrimSpace(settings.UpstreamURL)

	// Determine auth middleware (from module or context)
	auth := m.getAuthMiddleware(ctx)

	// Use registerOnce to ensure routes are only registered once
	var regErr error
	m.registerOnce.Do(func() {
		// Initialize model mapper from config (for routing unavailable models to alternatives)
		m.modelMapper = NewModelMapper(settings.ModelMappings)

		// Store initial config for partial reload comparison
		m.lastConfig = new(settings)

		// Initialize localhost restriction setting (hot-reloadable)
		m.setRestrictToLocalhost(settings.RestrictManagementToLocalhost)

		// Always register provider aliases - these work without an upstream
		m.registerProviderAliases(ctx.Engine, ctx.BaseHandler, auth)

		// Register management proxy routes once; middleware will gate access when upstream is unavailable.
		// Pass auth middleware to require valid API key for all management routes.
		m.registerManagementRoutes(ctx.Engine, ctx.BaseHandler, auth)

		// If no upstream URL, skip proxy routes but provider aliases are still available
		if upstreamURL == "" {
			log.Debug("amp upstream proxy disabled (no upstream URL configured)")
			log.Debug("amp provider alias routes registered")
			m.enabled = false
			return
		}

		if err := m.enableUpstreamProxy(upstreamURL, &settings); err != nil {
			regErr = fmt.Errorf("failed to create amp proxy: %w", err)
			return
		}

		log.Debug("amp provider alias routes registered")
	})

	return regErr
}

// getAuthMiddleware returns the authentication middleware, preferring the
// module's configured middleware, then the context middleware, then a fallback.
func (m *AmpModule) getAuthMiddleware(ctx modules.Context) gin.HandlerFunc {
	if m.authMiddleware_ != nil {
		return m.authMiddleware_
	}
	if ctx.AuthMiddleware != nil {
		return ctx.AuthMiddleware
	}
	// Fallback: no authentication (should not happen in production)
	log.Warn("amp module: no auth middleware provided, allowing all requests")
	return func(c *gin.Context) {
		c.Next()
	}
}

// OnConfigUpdated handles configuration updates with partial reload support.
// Only updates components that have actually changed to avoid unnecessary work.
// Supports hot-reload for: model-mappings, upstream-api-key, upstream-url, restrict-management-to-localhost.
func (m *AmpModule) OnConfigUpdated(cfg *config.Config) error {
	newSettings := cfg.AmpCode

	// Get previous config for comparison
	m.configMu.RLock()
	oldSettings := m.lastConfig
	m.configMu.RUnlock()

	if oldSettings != nil && oldSettings.RestrictManagementToLocalhost != newSettings.RestrictManagementToLocalhost {
		m.setRestrictToLocalhost(newSettings.RestrictManagementToLocalhost)
	}

	newUpstreamURL := strings.TrimSpace(newSettings.UpstreamURL)
	oldUpstreamURL := ""
	if oldSettings != nil {
		oldUpstreamURL = strings.TrimSpace(oldSettings.UpstreamURL)
	}

	if !m.enabled && newUpstreamURL != "" {
		if err := m.enableUpstreamProxy(newUpstreamURL, &newSettings); err != nil {
			log.Errorf("amp config: failed to enable upstream proxy for %s: %v", newUpstreamURL, err)
		}
	}

	// Check model mappings change
	modelMappingsChanged := m.hasModelMappingsChanged(oldSettings, &newSettings)
	if modelMappingsChanged {
		if m.modelMapper != nil {
			m.modelMapper.UpdateMappings(newSettings.ModelMappings)
		} else if m.enabled {
			log.Warnf("amp model mapper not initialized, skipping model mapping update")
		}
	}

	if m.enabled {
		// Check upstream URL change - now supports hot-reload
		if newUpstreamURL == "" && oldUpstreamURL != "" {
			m.setProxy(nil)
			m.enabled = false
		} else if oldUpstreamURL != "" && newUpstreamURL != oldUpstreamURL && newUpstreamURL != "" {
			// Recreate proxy with new URL
			proxy, err := createReverseProxy(newUpstreamURL, m.secretSource)
			if err != nil {
				log.Errorf("amp config: failed to create proxy for new upstream URL %s: %v", newUpstreamURL, err)
			} else {
				m.setProxy(proxy)
			}
		}

		// Check API key change (both default and per-client mappings)
		apiKeyChanged := m.hasAPIKeyChanged(oldSettings, &newSettings)
		upstreamAPIKeysChanged := m.hasUpstreamAPIKeysChanged(oldSettings, &newSettings)
		if apiKeyChanged || upstreamAPIKeysChanged {
			if m.secretSource != nil {
				if ms, ok := m.secretSource.(*MappedSecretSource); ok {
					if apiKeyChanged {
						ms.UpdateDefaultExplicitKey(newSettings.UpstreamAPIKey)
						ms.InvalidateCache()
					}
					if upstreamAPIKeysChanged {
						ms.UpdateMappings(newSettings.UpstreamAPIKeys)
					}
				} else if ms, ok := m.secretSource.(*MultiSourceSecret); ok {
					ms.UpdateExplicitKey(newSettings.UpstreamAPIKey)
					ms.InvalidateCache()
				}
			}
		}

	}

	// Store current config for next comparison
	m.configMu.Lock()
	settingsCopy := newSettings // copy struct
	m.lastConfig = &settingsCopy
	m.configMu.Unlock()

	return nil
}

func (m *AmpModule) enableUpstreamProxy(upstreamURL string, settings *config.AmpCode) error {
	if m.secretSource == nil {
		// Create MultiSourceSecret as the default source, then wrap with MappedSecretSource
		defaultSource := NewMultiSourceSecret(settings.UpstreamAPIKey, 0 /* default 5min */)
		mappedSource := NewMappedSecretSource(defaultSource)
		mappedSource.UpdateMappings(settings.UpstreamAPIKeys)
		m.secretSource = mappedSource
	} else if ms, ok := m.secretSource.(*MappedSecretSource); ok {
		ms.UpdateDefaultExplicitKey(settings.UpstreamAPIKey)
		ms.InvalidateCache()
		ms.UpdateMappings(settings.UpstreamAPIKeys)
	} else if ms, ok := m.secretSource.(*MultiSourceSecret); ok {
		// Legacy path: wrap existing MultiSourceSecret with MappedSecretSource
		ms.UpdateExplicitKey(settings.UpstreamAPIKey)
		ms.InvalidateCache()
		mappedSource := NewMappedSecretSource(ms)
		mappedSource.UpdateMappings(settings.UpstreamAPIKeys)
		m.secretSource = mappedSource
	}

	proxy, err := createReverseProxy(upstreamURL, m.secretSource)
	if err != nil {
		return err
	}

	m.setProxy(proxy)
	m.enabled = true

	log.Infof("amp upstream proxy enabled for: %s", upstreamURL)
	return nil
}

// hasModelMappingsChanged compares old and new model mappings.
func (m *AmpModule) hasModelMappingsChanged(old *config.AmpCode, new *config.AmpCode) bool {
	if old == nil {
		return len(new.ModelMappings) > 0
	}

	if len(old.ModelMappings) != len(new.ModelMappings) {
		return true
	}

	// Build map for efficient and robust comparison
	type mappingInfo struct {
		to    string
		regex bool
	}
	oldMap := make(map[string]mappingInfo, len(old.ModelMappings))
	for _, mapping := range old.ModelMappings {
		oldMap[strings.TrimSpace(mapping.From)] = mappingInfo{
			to:    strings.TrimSpace(mapping.To),
			regex: mapping.Regex,
		}
	}

	for _, mapping := range new.ModelMappings {
		from := strings.TrimSpace(mapping.From)
		to := strings.TrimSpace(mapping.To)
		if oldVal, exists := oldMap[from]; !exists || oldVal.to != to || oldVal.regex != mapping.Regex {
			return true
		}
	}

	return false
}

// hasAPIKeyChanged compares old and new API keys.
func (m *AmpModule) hasAPIKeyChanged(old *config.AmpCode, new *config.AmpCode) bool {
	oldKey := ""
	if old != nil {
		oldKey = strings.TrimSpace(old.UpstreamAPIKey)
	}
	newKey := strings.TrimSpace(new.UpstreamAPIKey)
	return oldKey != newKey
}

// hasUpstreamAPIKeysChanged compares old and new per-client upstream API key mappings.
func (m *AmpModule) hasUpstreamAPIKeysChanged(old *config.AmpCode, new *config.AmpCode) bool {
	if old == nil {
		return len(new.UpstreamAPIKeys) > 0
	}

	if len(old.UpstreamAPIKeys) != len(new.UpstreamAPIKeys) {
		return true
	}

	// Build map for comparison: upstreamKey -> set of clientKeys
	type entryInfo struct {
		upstreamKey string
		clientKeys  map[string]struct{}
	}
	oldEntries := make([]entryInfo, len(old.UpstreamAPIKeys))
	for i, entry := range old.UpstreamAPIKeys {
		clientKeys := make(map[string]struct{}, len(entry.APIKeys))
		for _, k := range entry.APIKeys {
			trimmed := strings.TrimSpace(k)
			if trimmed == "" {
				continue
			}
			clientKeys[trimmed] = struct{}{}
		}
		oldEntries[i] = entryInfo{
			upstreamKey: strings.TrimSpace(entry.UpstreamAPIKey),
			clientKeys:  clientKeys,
		}
	}

	for i, newEntry := range new.UpstreamAPIKeys {
		if i >= len(oldEntries) {
			return true
		}
		oldE := oldEntries[i]
		if strings.TrimSpace(newEntry.UpstreamAPIKey) != oldE.upstreamKey {
			return true
		}
		newKeys := make(map[string]struct{}, len(newEntry.APIKeys))
		for _, k := range newEntry.APIKeys {
			trimmed := strings.TrimSpace(k)
			if trimmed == "" {
				continue
			}
			newKeys[trimmed] = struct{}{}
		}
		if len(newKeys) != len(oldE.clientKeys) {
			return true
		}
		for k := range newKeys {
			if _, ok := oldE.clientKeys[k]; !ok {
				return true
			}
		}
	}

	return false
}

// GetModelMapper returns the model mapper instance (for testing/debugging).
func (m *AmpModule) GetModelMapper() *DefaultModelMapper {
	return m.modelMapper
}

// getProxy returns the current proxy instance (thread-safe for hot-reload).
func (m *AmpModule) getProxy() *httputil.ReverseProxy {
	m.proxyMu.RLock()
	defer m.proxyMu.RUnlock()
	return m.proxy
}

// setProxy updates the proxy instance (thread-safe for hot-reload).
func (m *AmpModule) setProxy(proxy *httputil.ReverseProxy) {
	m.proxyMu.Lock()
	defer m.proxyMu.Unlock()
	m.proxy = proxy
}

// IsRestrictedToLocalhost returns whether management routes are restricted to localhost.
func (m *AmpModule) IsRestrictedToLocalhost() bool {
	m.restrictMu.RLock()
	defer m.restrictMu.RUnlock()
	return m.restrictToLocalhost
}

// setRestrictToLocalhost updates the localhost restriction setting.
func (m *AmpModule) setRestrictToLocalhost(restrict bool) {
	m.restrictMu.Lock()
	defer m.restrictMu.Unlock()
	m.restrictToLocalhost = restrict
}
