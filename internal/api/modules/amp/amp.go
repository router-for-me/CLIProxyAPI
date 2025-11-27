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
type AmpModule struct {
	secretSource    SecretSource
	proxy           *httputil.ReverseProxy
	accessManager   *sdkaccess.Manager
	authMiddleware_ gin.HandlerFunc
	enabled         bool
	registerOnce    sync.Once
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

// Register sets up Amp routes if configured.
// This implements the RouteModuleV2 interface with Context.
// Routes are registered only once via sync.Once for idempotent behavior.
func (m *AmpModule) Register(ctx modules.Context) error {
	upstreamURL := strings.TrimSpace(ctx.Config.AmpUpstreamURL)

	// Determine auth middleware (from module or context)
	auth := m.getAuthMiddleware(ctx)

	// Use registerOnce to ensure routes are only registered once
	var regErr error
	m.registerOnce.Do(func() {
		// Always register provider aliases - these work without an upstream
		m.registerProviderAliases(ctx.Engine, ctx.BaseHandler, auth)

		// If no upstream URL, skip proxy routes but provider aliases are still available
		if upstreamURL == "" {
			log.Debug("Amp upstream proxy disabled (no upstream URL configured)")
			log.Debug("Amp provider alias routes registered")
			m.enabled = false
			return
		}

		// Create secret source with precedence: config > env > file
		// Cache secrets for 5 minutes to reduce file I/O
		if m.secretSource == nil {
			m.secretSource = NewMultiSourceSecret(ctx.Config.AmpUpstreamAPIKey, 0 /* default 5min */)
		}

		// Create reverse proxy with gzip handling via ModifyResponse
		proxy, err := createReverseProxy(upstreamURL, m.secretSource)
		if err != nil {
			regErr = fmt.Errorf("failed to create amp proxy: %w", err)
			return
		}

		m.proxy = proxy
		m.enabled = true

		// Register management proxy routes (requires upstream)
		// Restrict to localhost by default for security (prevents drive-by browser attacks)
		handler := proxyHandler(proxy)
		m.registerManagementRoutes(ctx.Engine, ctx.BaseHandler, handler, ctx.Config.AmpRestrictManagementToLocalhost)

		log.Infof("Amp upstream proxy enabled for: %s", upstreamURL)
		log.Debug("Amp provider alias routes registered")
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
	log.Warn("Amp module: no auth middleware provided, allowing all requests")
	return func(c *gin.Context) {
		c.Next()
	}
}

// OnConfigUpdated handles configuration updates.
// Currently requires restart for URL changes (could be enhanced for dynamic updates).
func (m *AmpModule) OnConfigUpdated(cfg *config.Config) error {
	if !m.enabled {
		log.Debug("Amp routing not enabled, skipping config update")
		return nil
	}

	upstreamURL := strings.TrimSpace(cfg.AmpUpstreamURL)
	if upstreamURL == "" {
		log.Warn("Amp upstream URL removed from config, restart required to disable")
		return nil
	}

	// If API key changed, invalidate the cache
	if m.secretSource != nil {
		if ms, ok := m.secretSource.(*MultiSourceSecret); ok {
			ms.InvalidateCache()
			log.Debug("Amp secret cache invalidated due to config update")
		}
	}

	log.Debug("Amp config updated (restart required for URL changes)")
	return nil
}
