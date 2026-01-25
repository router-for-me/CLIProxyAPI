package unifiedrouting

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Handlers contains all HTTP handlers for unified routing.
type Handlers struct {
	configSvc     ConfigService
	stateMgr      StateManager
	metrics       MetricsCollector
	healthChecker HealthChecker
	authManager   *coreauth.Manager
	engine        RoutingEngine
}

// NewHandlers creates a new handlers instance.
func NewHandlers(
	configSvc ConfigService,
	stateMgr StateManager,
	metrics MetricsCollector,
	healthChecker HealthChecker,
	authManager *coreauth.Manager,
	engine RoutingEngine,
) *Handlers {
	return &Handlers{
		configSvc:     configSvc,
		stateMgr:      stateMgr,
		metrics:       metrics,
		healthChecker: healthChecker,
		authManager:   authManager,
		engine:        engine,
	}
}

// ================== Config: Settings ==================

// GetSettings returns the unified routing settings.
func (h *Handlers) GetSettings(c *gin.Context) {
	log.Info("[UnifiedRouting] GetSettings called")
	settings, err := h.configSvc.GetSettings(c.Request.Context())
	if err != nil {
		log.Errorf("[UnifiedRouting] GetSettings error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Infof("[UnifiedRouting] GetSettings success: %+v", settings)
	c.JSON(http.StatusOK, settings)
}

// PutSettings updates the unified routing settings.
func (h *Handlers) PutSettings(c *gin.Context) {
	var settings Settings
	if err := c.ShouldBindJSON(&settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.configSvc.UpdateSettings(c.Request.Context(), &settings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, settings)
}

// GetHealthCheckConfig returns the health check configuration.
func (h *Handlers) GetHealthCheckConfig(c *gin.Context) {
	config, err := h.configSvc.GetHealthCheckConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, config)
}

// PutHealthCheckConfig updates the health check configuration.
func (h *Handlers) PutHealthCheckConfig(c *gin.Context) {
	var config HealthCheckConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.configSvc.UpdateHealthCheckConfig(c.Request.Context(), &config); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, config)
}

// ================== Config: Routes ==================

// ListRoutes returns all routes.
func (h *Handlers) ListRoutes(c *gin.Context) {
	log.Info("[UnifiedRouting] ListRoutes called")
	routes, err := h.configSvc.ListRoutes(c.Request.Context())
	if err != nil {
		log.Errorf("[UnifiedRouting] ListRoutes error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Infof("[UnifiedRouting] ListRoutes: found %d routes", len(routes))

	// Build response with pipeline summary
	type RouteResponse struct {
		*Route
		PipelineSummary struct {
			TotalLayers  int `json:"total_layers"`
			TotalTargets int `json:"total_targets"`
		} `json:"pipeline_summary"`
	}

	response := make([]RouteResponse, 0, len(routes))
	for _, route := range routes {
		rr := RouteResponse{Route: route}

		pipeline, err := h.configSvc.GetPipeline(c.Request.Context(), route.ID)
		if err == nil {
			rr.PipelineSummary.TotalLayers = len(pipeline.Layers)
			for _, layer := range pipeline.Layers {
				rr.PipelineSummary.TotalTargets += len(layer.Targets)
			}
		}

		response = append(response, rr)
	}

	c.JSON(http.StatusOK, gin.H{
		"total":  len(response),
		"routes": response,
	})
}

// GetRoute returns a single route.
func (h *Handlers) GetRoute(c *gin.Context) {
	routeID := c.Param("route_id")

	route, err := h.configSvc.GetRoute(c.Request.Context(), routeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	pipeline, _ := h.configSvc.GetPipeline(c.Request.Context(), routeID)

	c.JSON(http.StatusOK, gin.H{
		"route":    route,
		"pipeline": pipeline,
	})
}

// CreateRoute creates a new route.
func (h *Handlers) CreateRoute(c *gin.Context) {
	var req struct {
		Name        string   `json:"name" binding:"required"`
		Description string   `json:"description"`
		Enabled     bool     `json:"enabled"`
		Pipeline    Pipeline `json:"pipeline"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate
	route := &Route{
		Name:        req.Name,
		Description: req.Description,
		Enabled:     req.Enabled,
	}

	// Only validate pipeline if it has layers (allow creating routes without pipeline)
	var pipelineToValidate *Pipeline
	if len(req.Pipeline.Layers) > 0 {
		pipelineToValidate = &req.Pipeline
	}

	if errs := h.configSvc.Validate(c.Request.Context(), route, pipelineToValidate); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"errors": errs})
		return
	}

	// Create route
	if err := h.configSvc.CreateRoute(c.Request.Context(), route); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Save pipeline if provided
	if len(req.Pipeline.Layers) > 0 {
		if err := h.configSvc.UpdatePipeline(c.Request.Context(), route.ID, &req.Pipeline); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":      route.ID,
		"name":    route.Name,
		"message": "route created successfully",
	})
}

// UpdateRoute updates a route.
func (h *Handlers) UpdateRoute(c *gin.Context) {
	routeID := c.Param("route_id")

	var req struct {
		Name        string   `json:"name" binding:"required"`
		Description string   `json:"description"`
		Enabled     bool     `json:"enabled"`
		Pipeline    Pipeline `json:"pipeline"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	route := &Route{
		ID:          routeID,
		Name:        req.Name,
		Description: req.Description,
		Enabled:     req.Enabled,
	}

	if err := h.configSvc.UpdateRoute(c.Request.Context(), route); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Update pipeline if provided
	if len(req.Pipeline.Layers) > 0 {
		if err := h.configSvc.UpdatePipeline(c.Request.Context(), routeID, &req.Pipeline); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "route updated successfully"})
}

// PatchRoute partially updates a route.
func (h *Handlers) PatchRoute(c *gin.Context) {
	routeID := c.Param("route_id")

	existing, err := h.configSvc.GetRoute(c.Request.Context(), routeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	var patch map[string]interface{}
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Apply patch
	if name, ok := patch["name"].(string); ok {
		existing.Name = name
	}
	if desc, ok := patch["description"].(string); ok {
		existing.Description = desc
	}
	if enabled, ok := patch["enabled"].(bool); ok {
		existing.Enabled = enabled
	}

	if err := h.configSvc.UpdateRoute(c.Request.Context(), existing); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "route updated successfully"})
}

// DeleteRoute deletes a route.
func (h *Handlers) DeleteRoute(c *gin.Context) {
	routeID := c.Param("route_id")

	if err := h.configSvc.DeleteRoute(c.Request.Context(), routeID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "route deleted successfully"})
}

// ================== Config: Pipeline ==================

// GetPipeline returns the pipeline for a route.
func (h *Handlers) GetPipeline(c *gin.Context) {
	routeID := c.Param("route_id")

	pipeline, err := h.configSvc.GetPipeline(c.Request.Context(), routeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, pipeline)
}

// UpdatePipeline updates the pipeline for a route.
func (h *Handlers) UpdatePipeline(c *gin.Context) {
	routeID := c.Param("route_id")

	var pipeline Pipeline
	if err := c.ShouldBindJSON(&pipeline); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.configSvc.UpdatePipeline(c.Request.Context(), routeID, &pipeline); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "pipeline updated successfully"})
}

// ================== Config: Export/Import ==================

// ExportConfig exports the configuration.
func (h *Handlers) ExportConfig(c *gin.Context) {
	data, err := h.configSvc.Export(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, data)
}

// ImportConfig imports the configuration.
func (h *Handlers) ImportConfig(c *gin.Context) {
	merge := c.DefaultQuery("merge", "false") == "true"

	var data ExportData
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.configSvc.Import(c.Request.Context(), &data, merge); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "configuration imported successfully"})
}

// ValidateConfig validates a configuration.
func (h *Handlers) ValidateConfig(c *gin.Context) {
	var req struct {
		Route    *Route    `json:"route"`
		Pipeline *Pipeline `json:"pipeline"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	errors := h.configSvc.Validate(c.Request.Context(), req.Route, req.Pipeline)

	c.JSON(http.StatusOK, gin.H{
		"valid":  len(errors) == 0,
		"errors": errors,
	})
}

// ================== State ==================

// GetOverview returns the overall state overview.
func (h *Handlers) GetOverview(c *gin.Context) {
	log.Info("[UnifiedRouting] GetOverview called")
	overview, err := h.stateMgr.GetOverview(c.Request.Context())
	if err != nil {
		log.Errorf("[UnifiedRouting] GetOverview error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	log.Infof("[UnifiedRouting] GetOverview success: %d routes", overview.TotalRoutes)
	c.JSON(http.StatusOK, overview)
}

// GetRouteStatus returns the status of a route.
func (h *Handlers) GetRouteStatus(c *gin.Context) {
	routeID := c.Param("route_id")

	state, err := h.stateMgr.GetRouteState(c.Request.Context(), routeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, state)
}

// GetTargetStatus returns the status of a target.
func (h *Handlers) GetTargetStatus(c *gin.Context) {
	targetID := c.Param("target_id")

	state, err := h.stateMgr.GetTargetState(c.Request.Context(), targetID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, state)
}

// ResetTarget resets a target's state.
func (h *Handlers) ResetTarget(c *gin.Context) {
	targetID := c.Param("target_id")

	if err := h.stateMgr.ResetTarget(c.Request.Context(), targetID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    "target status reset successfully",
		"target_id":  targetID,
		"new_status": "healthy",
	})
}

// ForceCooldown forces a target into cooldown.
func (h *Handlers) ForceCooldown(c *gin.Context) {
	targetID := c.Param("target_id")

	var req struct {
		DurationSeconds int `json:"duration_seconds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		req.DurationSeconds = 60
	}

	duration := time.Duration(req.DurationSeconds) * time.Second
	if err := h.stateMgr.ForceCooldown(c.Request.Context(), targetID, duration); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":          "cooldown started",
		"target_id":        targetID,
		"duration_seconds": req.DurationSeconds,
	})
}

// ================== Health ==================

// TriggerHealthCheck triggers a health check.
func (h *Handlers) TriggerHealthCheck(c *gin.Context) {
	routeID := c.Param("route_id")
	targetID := c.Query("target_id")

	var results []*HealthResult
	var err error

	if targetID != "" {
		result, e := h.healthChecker.CheckTarget(c.Request.Context(), targetID)
		if e != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": e.Error()})
			return
		}
		results = []*HealthResult{result}
	} else if routeID != "" {
		results, err = h.healthChecker.CheckRoute(c.Request.Context(), routeID)
	} else {
		results, err = h.healthChecker.CheckAll(c.Request.Context())
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"checked_at": time.Now(),
		"results":    results,
	})
}

// GetHealthSettings returns health check settings.
func (h *Handlers) GetHealthSettings(c *gin.Context) {
	settings, err := h.healthChecker.GetSettings(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, settings)
}

// UpdateHealthSettings updates health check settings.
func (h *Handlers) UpdateHealthSettings(c *gin.Context) {
	var settings HealthCheckConfig
	if err := c.ShouldBindJSON(&settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.healthChecker.UpdateSettings(c.Request.Context(), &settings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, settings)
}

// GetHealthHistory returns health check history.
func (h *Handlers) GetHealthHistory(c *gin.Context) {
	filter := HealthHistoryFilter{
		TargetID: c.Query("target_id"),
		Status:   c.Query("status"),
	}

	if limitStr := c.Query("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil {
			filter.Limit = limit
		}
	}

	history, err := h.healthChecker.GetHistory(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"total":   len(history),
		"history": history,
	})
}

// ================== Metrics ==================

// GetStats returns aggregated statistics.
func (h *Handlers) GetStats(c *gin.Context) {
	filter := StatsFilter{
		Period:      c.DefaultQuery("period", "1h"),
		Granularity: c.DefaultQuery("granularity", "minute"),
	}

	stats, err := h.metrics.GetStats(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, stats)
}

// GetRouteStats returns statistics for a route.
func (h *Handlers) GetRouteStats(c *gin.Context) {
	routeID := c.Param("route_id")
	filter := StatsFilter{
		Period:      c.DefaultQuery("period", "1h"),
		Granularity: c.DefaultQuery("granularity", "minute"),
	}

	stats, err := h.metrics.GetRouteStats(c.Request.Context(), routeID, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	stats.Period = filter.Period
	c.JSON(http.StatusOK, gin.H{
		"route_id": routeID,
		"stats":    stats,
	})
}

// GetEvents returns routing events.
func (h *Handlers) GetEvents(c *gin.Context) {
	filter := EventFilter{
		Type:    c.DefaultQuery("type", "all"),
		RouteID: c.Query("route_id"),
	}

	if limitStr := c.Query("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil {
			filter.Limit = limit
		}
	} else {
		filter.Limit = 100
	}

	events, err := h.metrics.GetEvents(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"total":  len(events),
		"events": events,
	})
}

// GetTraces returns request traces.
func (h *Handlers) GetTraces(c *gin.Context) {
	filter := TraceFilter{
		RouteID: c.Query("route_id"),
		Status:  c.Query("status"),
	}

	if limitStr := c.Query("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil {
			filter.Limit = limit
		}
	} else {
		filter.Limit = 50
	}

	traces, err := h.metrics.GetTraces(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"total":  len(traces),
		"traces": traces,
	})
}

// GetTrace returns a single trace.
func (h *Handlers) GetTrace(c *gin.Context) {
	traceID := c.Param("trace_id")

	trace, err := h.metrics.GetTrace(c.Request.Context(), traceID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, trace)
}

// ================== Credentials ==================

// ListCredentials returns all available credentials.
func (h *Handlers) ListCredentials(c *gin.Context) {
	log.Info("[UnifiedRouting] ListCredentials called")
	if h.authManager == nil {
		log.Error("[UnifiedRouting] ListCredentials: auth manager is nil")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}

	typeFilter := c.Query("type")
	providerFilter := c.Query("provider")
	log.Infof("[UnifiedRouting] ListCredentials: type=%s, provider=%s", typeFilter, providerFilter)

	auths := h.authManager.List()
	log.Infof("[UnifiedRouting] ListCredentials: found %d auths", len(auths))
	reg := registry.GetGlobalRegistry()

	credentials := make([]CredentialInfo, 0)

	for _, auth := range auths {
		// Determine type
		credType := "oauth"
		if auth.Attributes != nil {
			if _, ok := auth.Attributes["api_key"]; ok {
				credType = "api-key"
			}
		}

		// Apply filters
		if typeFilter != "" && credType != typeFilter && typeFilter != "all" {
			continue
		}
		if providerFilter != "" && auth.Provider != providerFilter {
			continue
		}

		// Get models
		models := reg.GetModelsForClient(auth.ID)
		modelInfos := make([]ModelInfo, 0, len(models))
		for _, m := range models {
			modelInfos = append(modelInfos, ModelInfo{
				ID:        m.ID,
				Name:      m.ID,
				Available: true,
			})
		}

		cred := CredentialInfo{
			ID:       auth.ID,
			Provider: auth.Provider,
			Type:     credType,
			Label:    auth.Label,
			Prefix:   auth.Prefix,
			Status:   string(auth.Status),
			Models:   modelInfos,
		}

		// Add masked API key if present
		if auth.Attributes != nil {
			if apiKey, ok := auth.Attributes["api_key"]; ok {
				cred.APIKey = util.HideAPIKey(apiKey)
			}
			if baseURL, ok := auth.Attributes["base_url"]; ok {
				cred.BaseURL = baseURL
			}
		}

		credentials = append(credentials, cred)
	}

	c.JSON(http.StatusOK, gin.H{
		"total":       len(credentials),
		"credentials": credentials,
	})
}

// GetCredential returns a single credential.
func (h *Handlers) GetCredential(c *gin.Context) {
	credentialID := c.Param("credential_id")

	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}

	auths := h.authManager.List()
	for _, auth := range auths {
		if auth.ID == credentialID {
			reg := registry.GetGlobalRegistry()
			models := reg.GetModelsForClient(auth.ID)

			modelInfos := make([]ModelInfo, 0, len(models))
			for _, m := range models {
				modelInfos = append(modelInfos, ModelInfo{
					ID:        m.ID,
					Name:      m.ID,
					Available: true,
				})
			}

			credType := "oauth"
			if auth.Attributes != nil {
				if _, ok := auth.Attributes["api_key"]; ok {
					credType = "api-key"
				}
			}

			cred := CredentialInfo{
				ID:       auth.ID,
				Provider: auth.Provider,
				Type:     credType,
				Label:    auth.Label,
				Prefix:   auth.Prefix,
				Status:   string(auth.Status),
				Models:   modelInfos,
			}

			if auth.Attributes != nil {
				if apiKey, ok := auth.Attributes["api_key"]; ok {
					cred.APIKey = util.HideAPIKey(apiKey)
				}
				if baseURL, ok := auth.Attributes["base_url"]; ok {
					cred.BaseURL = baseURL
				}
			}

			c.JSON(http.StatusOK, cred)
			return
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
}

// ================== Simulate Route ==================

// SimulateRouteRequest represents a request to simulate routing.
type SimulateRouteRequest struct {
	DryRun bool `json:"dry_run"` // If true, don't actually make requests, just check availability
}

// SimulateRouteResponse represents the result of a route simulation.
type SimulateRouteResponse struct {
	RouteID     string                    `json:"route_id"`
	RouteName   string                    `json:"route_name"`
	Success     bool                      `json:"success"`
	FinalTarget *SimulateTargetResult     `json:"final_target,omitempty"`
	Attempts    []SimulateLayerResult     `json:"attempts"`
	TotalTimeMs int64                     `json:"total_time_ms"`
}

// SimulateLayerResult represents the result of trying a layer.
type SimulateLayerResult struct {
	Layer   int                       `json:"layer"`
	Targets []SimulateTargetResult    `json:"targets"`
}

// SimulateTargetResult represents the result of trying a target.
type SimulateTargetResult struct {
	TargetID     string `json:"target_id"`
	CredentialID string `json:"credential_id"`
	Model        string `json:"model"`
	Status       string `json:"status"` // "success", "failed", "skipped"
	Message      string `json:"message,omitempty"`
	LatencyMs    int64  `json:"latency_ms,omitempty"`
}

// SimulateRoute simulates the routing process for a specific route.
// It follows the exact same logic as ExecuteWithFailover:
// - Uses the same load balancing strategy (round-robin, weighted, etc.)
// - Records success/failure statistics
// - Starts cooldown on failure
// - Records the request trace
// The only difference is it uses health check instead of a real API request.
func (h *Handlers) SimulateRoute(c *gin.Context) {
	routeID := c.Param("route_id")
	
	var req SimulateRouteRequest
	_ = c.ShouldBindJSON(&req)
	
	ctx := c.Request.Context()
	
	// Get route
	route, err := h.configSvc.GetRoute(ctx, routeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "route not found"})
		return
	}
	
	// Get pipeline
	pipeline, err := h.configSvc.GetPipeline(ctx, routeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	
	// Get health check config for cooldown duration
	healthConfig, _ := h.configSvc.GetHealthCheckConfig(ctx)
	if healthConfig == nil {
		cfg := DefaultHealthCheckConfig()
		healthConfig = &cfg
	}
	
	response := SimulateRouteResponse{
		RouteID:   routeID,
		RouteName: route.Name,
		Attempts:  make([]SimulateLayerResult, 0),
	}
	
	startTime := time.Now()
	traceBuilder := NewTraceBuilder(routeID, route.Name)
	
	// Follow the exact same logic as ExecuteWithFailover
	for layerIdx, layer := range pipeline.Layers {
		layerResult := SimulateLayerResult{
			Layer:   layer.Level,
			Targets: make([]SimulateTargetResult, 0),
		}
		
		// Calculate cooldown duration for this layer
		cooldownDuration := time.Duration(layer.CooldownSeconds) * time.Second
		if cooldownDuration == 0 {
			cooldownDuration = time.Duration(healthConfig.DefaultCooldownSeconds) * time.Second
		}
		
		// Keep trying targets in this layer until no available targets remain
		// SelectTarget automatically excludes cooling-down targets
		for {
			// Use engine's SelectTarget for proper load balancing (round-robin, weighted, etc.)
			target, err := h.engine.SelectTarget(ctx, routeID, &layer)
			if err != nil {
				// No more available targets in this layer
				break
			}
			
			targetResult := SimulateTargetResult{
				TargetID:     target.ID,
				CredentialID: target.CredentialID,
				Model:        target.Model,
			}
			
			if req.DryRun {
				// Just check availability without making requests
				targetResult.Status = "success"
				targetResult.Message = "target is available (dry run)"
				layerResult.Targets = append(layerResult.Targets, targetResult)
				response.Attempts = append(response.Attempts, layerResult)
				response.Success = true
				response.FinalTarget = &targetResult
				response.TotalTimeMs = time.Since(startTime).Milliseconds()
				c.JSON(http.StatusOK, response)
				return
			}
			
			// Perform health check (simulating a real request)
			checkStart := time.Now()
			result, checkErr := h.healthChecker.CheckTarget(ctx, target.ID)
			latency := time.Since(checkStart)
			targetResult.LatencyMs = latency.Milliseconds()
			
			if checkErr != nil || result.Status != "healthy" {
				// Failed - record failure and start cooldown (same as real request)
				errMsg := "health check failed"
				if checkErr != nil {
					errMsg = checkErr.Error()
				} else if result.Message != "" {
					errMsg = result.Message
				}
				
				targetResult.Status = "failed"
				targetResult.Message = errMsg
				layerResult.Targets = append(layerResult.Targets, targetResult)
				
				// Note: RecordFailure is already called by CheckTarget, so we don't call it again here
				traceBuilder.AddAttempt(layer.Level, target.ID, target.CredentialID, target.Model).
					Failed(errMsg)
				
				// Start cooldown immediately on failure (CheckTarget doesn't do this)
				h.stateMgr.StartCooldown(ctx, target.ID, cooldownDuration)
				h.metrics.RecordEvent(&RoutingEvent{
					Type:     EventCooldownStarted,
					RouteID:  routeID,
					TargetID: target.ID,
					Details: map[string]any{
						"duration_seconds": int(cooldownDuration.Seconds()),
						"reason":           errMsg,
						"source":           "simulate",
					},
				})
				
				// Continue to next target in this layer
				continue
			}
			
			// Success!
			targetResult.Status = "success"
			targetResult.Message = "health check passed"
			if result.LatencyMs > 0 {
				targetResult.LatencyMs = result.LatencyMs
			}
			layerResult.Targets = append(layerResult.Targets, targetResult)
			
			// Note: RecordSuccess is already called by CheckTarget, so we don't call it again here
			traceBuilder.AddAttempt(layer.Level, target.ID, target.CredentialID, target.Model).
				Success(targetResult.LatencyMs)
			
			// Record the trace
			trace := traceBuilder.Build(time.Since(startTime).Milliseconds())
			h.metrics.RecordRequest(trace)
			
			response.Attempts = append(response.Attempts, layerResult)
			response.Success = true
			response.FinalTarget = &targetResult
			response.TotalTimeMs = time.Since(startTime).Milliseconds()
			c.JSON(http.StatusOK, response)
			return
		}
		
		// Add layer result if we tried any targets in it
		if len(layerResult.Targets) > 0 {
			response.Attempts = append(response.Attempts, layerResult)
		}
		
		// Record layer fallback event when moving to next layer
		if layerIdx < len(pipeline.Layers)-1 && len(layerResult.Targets) > 0 {
			h.metrics.RecordEvent(&RoutingEvent{
				Type:    EventLayerFallback,
				RouteID: routeID,
				Details: map[string]any{
					"from_layer": layer.Level,
					"to_layer":   layer.Level + 1,
					"source":     "simulate",
				},
			})
		}
	}
	
	// All layers exhausted - record failed trace
	trace := traceBuilder.Build(time.Since(startTime).Milliseconds())
	h.metrics.RecordRequest(trace)
	
	response.TotalTimeMs = time.Since(startTime).Milliseconds()
	c.JSON(http.StatusOK, response)
}
