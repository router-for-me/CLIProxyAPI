package mcp

import (
	"fmt"
	"net/http/httputil"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/modules"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

// MCPModule implements RouteModuleV2 and forwards /mcp/* traffic to an
// upstream MCP server with optional upstream API-key injection.
type MCPModule struct {
	proxy   *httputil.ReverseProxy
	proxyMu sync.RWMutex

	authMiddleware gin.HandlerFunc
	registerOnce   sync.Once

	configMu       sync.RWMutex
	upstreamURL    string
	upstreamAPIKey string
}

// New creates a new MCP forwarding module.
func New(authMiddleware gin.HandlerFunc) *MCPModule {
	return &MCPModule{authMiddleware: authMiddleware}
}

// Name returns the module identifier.
func (m *MCPModule) Name() string {
	return "mcp-forwarding"
}

// Register sets up MCP forwarding routes.
func (m *MCPModule) Register(ctx modules.Context) error {
	if ctx.Config == nil {
		return fmt.Errorf("mcp module: nil config")
	}

	m.setUpstreamConfig(ctx.Config.MCP.UpstreamURL, ctx.Config.MCP.UpstreamAPIKey)
	auth := m.getAuthMiddleware(ctx)

	var regErr error
	m.registerOnce.Do(func() {
		m.registerRoutes(ctx.Engine, auth)
		if err := m.rebuildProxyIfNeeded(); err != nil {
			regErr = err
		}
	})

	return regErr
}

// OnConfigUpdated applies MCP forwarding config changes on hot-reload.
func (m *MCPModule) OnConfigUpdated(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}

	oldURL := m.getUpstreamURL()
	newURL := strings.TrimSpace(cfg.MCP.UpstreamURL)
	newKey := strings.TrimSpace(cfg.MCP.UpstreamAPIKey)

	if newURL == "" {
		m.setUpstreamConfig(newURL, newKey)
		m.setProxy(nil)
		return nil
	}

	if oldURL != newURL || m.getProxy() == nil {
		proxy, err := createReverseProxy(newURL, m.getUpstreamAPIKey)
		if err != nil {
			return fmt.Errorf("mcp module: failed to create proxy: %w", err)
		}
		m.setUpstreamConfig(newURL, newKey)
		m.setProxy(proxy)
		return nil
	}

	m.setUpstreamConfig(newURL, newKey)
	return nil
}

func (m *MCPModule) getAuthMiddleware(ctx modules.Context) gin.HandlerFunc {
	if m.authMiddleware != nil {
		return m.authMiddleware
	}
	if ctx.AuthMiddleware != nil {
		return ctx.AuthMiddleware
	}
	log.Warn("mcp module: no auth middleware provided, allowing all requests")
	return func(c *gin.Context) {
		c.Next()
	}
}

func (m *MCPModule) rebuildProxyIfNeeded() error {
	upstreamURL := m.getUpstreamURL()
	if upstreamURL == "" {
		m.setProxy(nil)
		log.Debug("mcp upstream proxy disabled (no upstream URL configured)")
		return nil
	}

	proxy, err := createReverseProxy(upstreamURL, m.getUpstreamAPIKey)
	if err != nil {
		return fmt.Errorf("mcp module: failed to create proxy: %w", err)
	}
	m.setProxy(proxy)
	return nil
}

func (m *MCPModule) getProxy() *httputil.ReverseProxy {
	m.proxyMu.RLock()
	defer m.proxyMu.RUnlock()
	return m.proxy
}

func (m *MCPModule) setProxy(proxy *httputil.ReverseProxy) {
	m.proxyMu.Lock()
	m.proxy = proxy
	m.proxyMu.Unlock()
}

func (m *MCPModule) setUpstreamConfig(url string, apiKey string) {
	m.configMu.Lock()
	m.upstreamURL = strings.TrimSpace(url)
	m.upstreamAPIKey = strings.TrimSpace(apiKey)
	m.configMu.Unlock()
}

func (m *MCPModule) getUpstreamURL() string {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	return m.upstreamURL
}

func (m *MCPModule) getUpstreamAPIKey() string {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	return m.upstreamAPIKey
}
