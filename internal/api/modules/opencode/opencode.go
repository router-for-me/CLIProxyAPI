package opencode

import (
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/modules"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

// Option configures the OpenCodeModule.
type Option func(*OpenCodeModule)

// OpenCodeModule implements the modules.RouteModuleV2 interface for the OpenCode
// agent integration. It registers the dedicated /opencode/... route namespace
// (merged + provider-scoped) backed by the existing SDK handlers and applies an
// optional, OpenCode-scoped model-mapping layer.
type OpenCodeModule struct {
	authMiddleware gin.HandlerFunc
	modelMapper    *DefaultModelMapper
	registerOnce   sync.Once

	// configMu protects lastConfig for hot-reload comparison.
	configMu   sync.RWMutex
	lastConfig *config.OpenCode
}

// New creates a new OpenCode routing module with the given options.
func New(opts ...Option) *OpenCodeModule {
	m := &OpenCodeModule{}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// WithAuthMiddleware sets the authentication middleware applied to OpenCode routes.
func WithAuthMiddleware(middleware gin.HandlerFunc) Option {
	return func(m *OpenCodeModule) {
		m.authMiddleware = middleware
	}
}

// Name returns the module identifier.
func (m *OpenCodeModule) Name() string {
	return "opencode-routing"
}

// forceModelMappings reports whether mappings should take precedence over local providers.
func (m *OpenCodeModule) forceModelMappings() bool {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	if m.lastConfig == nil {
		return false
	}
	return m.lastConfig.ForceModelMappings
}

// Register wires the OpenCode routes into the engine. Registration is idempotent
// via sync.Once so repeated calls do not duplicate routes.
func (m *OpenCodeModule) Register(ctx modules.Context) error {
	settings := ctx.Config.OpenCode
	auth := m.getAuthMiddleware(ctx)

	m.registerOnce.Do(func() {
		m.modelMapper = NewModelMapper(settings.ModelMappings)

		settingsCopy := settings
		m.configMu.Lock()
		m.lastConfig = &settingsCopy
		m.configMu.Unlock()

		m.registerRoutes(ctx.Engine, ctx.BaseHandler, auth)
		log.Debug("opencode route namespace registered")
	})

	return nil
}

// OnConfigUpdated refreshes the model mapper when the OpenCode configuration changes.
func (m *OpenCodeModule) OnConfigUpdated(cfg *config.Config) error {
	newSettings := cfg.OpenCode
	if m.modelMapper != nil {
		m.modelMapper.UpdateMappings(newSettings.ModelMappings)
	}

	settingsCopy := newSettings
	m.configMu.Lock()
	m.lastConfig = &settingsCopy
	m.configMu.Unlock()
	return nil
}

// GetModelMapper returns the model mapper instance (for testing/debugging).
func (m *OpenCodeModule) GetModelMapper() *DefaultModelMapper {
	return m.modelMapper
}

// getAuthMiddleware resolves the auth middleware, preferring the module's configured
// middleware, then the context middleware, then a permissive fallback.
func (m *OpenCodeModule) getAuthMiddleware(ctx modules.Context) gin.HandlerFunc {
	if m.authMiddleware != nil {
		return m.authMiddleware
	}
	if ctx.AuthMiddleware != nil {
		return ctx.AuthMiddleware
	}
	log.Warn("opencode module: no auth middleware provided, allowing all requests")
	return func(c *gin.Context) { c.Next() }
}
