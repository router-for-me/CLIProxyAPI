package unifiedrouting

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/modules"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Option configures the Module.
type Option func(*Module)

// Module implements the RouteModuleV2 interface for unified routing.
type Module struct {
	authManager    *coreauth.Manager
	authMiddleware gin.HandlerFunc

	configStore  ConfigStore
	stateStore   StateStore
	metricsStore MetricsStore

	configSvc     ConfigService
	stateMgr      StateManager
	metrics       MetricsCollector
	healthChecker HealthChecker
	engine        RoutingEngine
	handlers      *Handlers

	initOnce       sync.Once
	routesOnce     sync.Once
	dataDir        string
	skipAutoRoutes bool // If true, routes won't be registered in Register()
}

// New creates a new unified routing module.
func New(opts ...Option) *Module {
	m := &Module{}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// WithAuthManager sets the auth manager.
func WithAuthManager(am *coreauth.Manager) Option {
	return func(m *Module) {
		m.authManager = am
	}
}

// WithAuthMiddleware sets the authentication middleware.
func WithAuthMiddleware(middleware gin.HandlerFunc) Option {
	return func(m *Module) {
		m.authMiddleware = middleware
	}
}

// WithDataDir sets the data directory for configuration storage.
func WithDataDir(dir string) Option {
	return func(m *Module) {
		m.dataDir = dir
	}
}

// WithSkipAutoRoutes skips automatic route registration in Register().
// Use this when you want to register routes manually via RegisterRoutes().
func WithSkipAutoRoutes() Option {
	return func(m *Module) {
		m.skipAutoRoutes = true
	}
}

// Name returns the module identifier.
func (m *Module) Name() string {
	return "unified-routing"
}

// Register sets up unified routing routes.
func (m *Module) Register(ctx modules.Context) error {
	log.Info("[UnifiedRouting] Register() called")

	// Initialize module (only once)
	if err := m.init(ctx); err != nil {
		return err
	}

	// Register routes unless skipAutoRoutes is set
	if !m.skipAutoRoutes {
		auth := m.getAuthMiddleware(ctx)
		log.Info("[UnifiedRouting] Auth middleware configured (auto)")
		m.RegisterRoutes(ctx.Engine, auth)
	} else {
		log.Info("[UnifiedRouting] Skipping auto route registration (will be registered later)")
	}

	log.Info("[UnifiedRouting] Module registered successfully")
	return nil
}

// init initializes the module services (only once).
func (m *Module) init(ctx modules.Context) error {
	var initErr error

	m.initOnce.Do(func() {
		log.Info("[UnifiedRouting] Initializing module...")
		// Determine data directory
		dataDir := m.dataDir
		if dataDir == "" {
			// Default to auth-dir/unified-routing
			authDir := ctx.Config.AuthDir
			if authDir == "" {
				authDir = "~/.cli-proxy-api"
			}
			// Expand ~ if present
			if authDir[0] == '~' {
				home, _ := os.UserHomeDir()
				authDir = filepath.Join(home, authDir[1:])
			}
			dataDir = filepath.Join(authDir, "unified-routing")
		}
		log.Infof("[UnifiedRouting] Data directory: %s", dataDir)

		// Initialize stores
		configStore, err := NewFileConfigStore(dataDir)
		if err != nil {
			initErr = err
			return
		}
		m.configStore = configStore
		m.stateStore = NewMemoryStateStore()

		// Use separate logs directory for traces (outside auth-dir to avoid confusion)
		logsDir := filepath.Join(dataDir, "..", "logs", "unified-routing")
		metricsStore, err := NewFileMetricsStore(logsDir, 100) // 100MB max for traces
		if err != nil {
			initErr = err
			return
		}
		m.metricsStore = metricsStore
		log.Infof("[UnifiedRouting] Logs directory: %s", logsDir)

		// Initialize services
		m.configSvc = NewConfigService(m.configStore)
		m.stateMgr = NewStateManager(m.stateStore, m.configSvc)
		m.metrics = NewMetricsCollector(m.metricsStore)
		m.healthChecker = NewHealthChecker(m.configSvc, m.stateMgr, m.metrics, m.authManager)
		m.engine = NewRoutingEngine(m.configSvc, m.stateMgr, m.metrics, m.authManager)

		// Initialize handlers
		m.handlers = NewHandlers(m.configSvc, m.stateMgr, m.metrics, m.healthChecker, m.authManager, m.engine)

		log.Info("[UnifiedRouting] Module initialization complete")
	})

	return initErr
}

// getAuthMiddleware returns the authentication middleware.
func (m *Module) getAuthMiddleware(ctx modules.Context) gin.HandlerFunc {
	if m.authMiddleware != nil {
		return m.authMiddleware
	}
	if ctx.AuthMiddleware != nil {
		return ctx.AuthMiddleware
	}
	// Fallback: no authentication
	log.Warn("unified-routing module: no auth middleware provided, allowing all requests")
	return func(c *gin.Context) {
		c.Next()
	}
}

// RegisterRoutes registers all HTTP routes with the given auth middleware.
// This method can be called externally to register routes with custom auth.
// It will only register routes once (subsequent calls are no-ops).
func (m *Module) RegisterRoutes(engine *gin.Engine, auth gin.HandlerFunc) {
	m.routesOnce.Do(func() {
		log.Info("[UnifiedRouting] Registering routes...")
		m.doRegisterRoutes(engine, auth)
		log.Info("[UnifiedRouting] Routes registered")
	})
}

// doRegisterRoutes performs the actual route registration.
func (m *Module) doRegisterRoutes(engine *gin.Engine, auth gin.HandlerFunc) {
	// Base path: /v0/management/unified-routing
	ur := engine.Group("/v0/management/unified-routing", auth)

	// Config: Settings
	ur.GET("/config/settings", m.handlers.GetSettings)
	ur.PUT("/config/settings", m.handlers.PutSettings)

	// Config: Health check settings
	ur.GET("/config/health-check", m.handlers.GetHealthCheckConfig)
	ur.PUT("/config/health-check", m.handlers.PutHealthCheckConfig)

	// Config: Routes
	ur.GET("/config/routes", m.handlers.ListRoutes)
	ur.POST("/config/routes", m.handlers.CreateRoute)
	ur.GET("/config/routes/:route_id", m.handlers.GetRoute)
	ur.PUT("/config/routes/:route_id", m.handlers.UpdateRoute)
	ur.PATCH("/config/routes/:route_id", m.handlers.PatchRoute)
	ur.DELETE("/config/routes/:route_id", m.handlers.DeleteRoute)

	// Config: Pipeline
	ur.GET("/config/routes/:route_id/pipeline", m.handlers.GetPipeline)
	ur.PUT("/config/routes/:route_id/pipeline", m.handlers.UpdatePipeline)

	// Config: Export/Import
	ur.GET("/config/export", m.handlers.ExportConfig)
	ur.POST("/config/import", m.handlers.ImportConfig)
	ur.POST("/config/validate", m.handlers.ValidateConfig)

	// State
	ur.GET("/state/overview", m.handlers.GetOverview)
	ur.GET("/state/routes/:route_id", m.handlers.GetRouteStatus)
	ur.GET("/state/targets/:target_id", m.handlers.GetTargetStatus)
	ur.POST("/state/targets/:target_id/reset", m.handlers.ResetTarget)
	ur.POST("/state/targets/:target_id/force-cooldown", m.handlers.ForceCooldown)

	// Health
	ur.POST("/health/check", m.handlers.TriggerHealthCheck)
	ur.POST("/health/check/routes/:route_id", m.handlers.TriggerHealthCheck)
	ur.POST("/health/check/targets/:target_id", m.handlers.TriggerHealthCheck)
	ur.GET("/health/settings", m.handlers.GetHealthSettings)
	ur.PUT("/health/settings", m.handlers.UpdateHealthSettings)
	ur.GET("/health/history", m.handlers.GetHealthHistory)

	// Simulate
	ur.POST("/simulate/routes/:route_id", m.handlers.SimulateRoute)

	// Metrics
	ur.GET("/metrics/stats", m.handlers.GetStats)
	ur.GET("/metrics/stats/routes/:route_id", m.handlers.GetRouteStats)
	ur.GET("/metrics/events", m.handlers.GetEvents)
	ur.GET("/metrics/traces", m.handlers.GetTraces)
	ur.GET("/metrics/traces/:trace_id", m.handlers.GetTrace)

	// Credentials
	ur.GET("/credentials", m.handlers.ListCredentials)
	ur.GET("/credentials/:credential_id", m.handlers.GetCredential)
}

// OnConfigUpdated handles configuration updates.
func (m *Module) OnConfigUpdated(cfg *config.Config) error {
	// Reload engine configuration
	if m.engine != nil {
		return m.engine.Reload(nil)
	}
	return nil
}

// GetEngine returns the routing engine (for integration with main request handlers).
func (m *Module) GetEngine() RoutingEngine {
	return m.engine
}

// GetConfigService returns the config service.
func (m *Module) GetConfigService() ConfigService {
	return m.configSvc
}

// GetStateManager returns the state manager.
func (m *Module) GetStateManager() StateManager {
	return m.stateMgr
}

// GetMetricsCollector returns the metrics collector.
func (m *Module) GetMetricsCollector() MetricsCollector {
	return m.metrics
}

// GetHealthChecker returns the health checker.
func (m *Module) GetHealthChecker() HealthChecker {
	return m.healthChecker
}

// Start starts background tasks.
// Note: Background health checks are disabled by design.
// Cooldown expiration is handled automatically when a target is selected.
// Manual health checks can still be triggered via the API.
func (m *Module) Start() error {
	// Background health checker is intentionally NOT started.
	// Targets automatically become available again when their cooldown expires
	// (checked in GetTargetState and SelectTarget).
	// Manual health checks can still be triggered via POST /health/check endpoints.
	return nil
}

// Stop stops background tasks.
func (m *Module) Stop() error {
	if m.healthChecker != nil {
		return m.healthChecker.Stop(nil)
	}
	if sm, ok := m.stateMgr.(*DefaultStateManager); ok {
		sm.Stop()
	}
	return nil
}
