package unifiedrouting

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// HealthChecker performs health checks on routing targets.
type HealthChecker interface {
	// Trigger checks
	CheckAll(ctx context.Context) ([]*HealthResult, error)
	CheckRoute(ctx context.Context, routeID string) ([]*HealthResult, error)
	CheckTarget(ctx context.Context, targetID string) (*HealthResult, error)

	// Configuration
	GetSettings(ctx context.Context) (*HealthCheckConfig, error)
	UpdateSettings(ctx context.Context, settings *HealthCheckConfig) error

	// History
	GetHistory(ctx context.Context, filter HealthHistoryFilter) ([]*HealthResult, error)

	// Background task control
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// DefaultHealthChecker implements HealthChecker.
type DefaultHealthChecker struct {
	configSvc    ConfigService
	stateMgr     StateManager
	metrics      MetricsCollector
	authManager  *coreauth.Manager
	
	mu           sync.RWMutex
	history      []*HealthResult
	maxHistory   int
	
	stopChan     chan struct{}
	running      bool
}

// NewHealthChecker creates a new health checker.
func NewHealthChecker(
	configSvc ConfigService,
	stateMgr StateManager,
	metrics MetricsCollector,
	authManager *coreauth.Manager,
) *DefaultHealthChecker {
	return &DefaultHealthChecker{
		configSvc:   configSvc,
		stateMgr:    stateMgr,
		metrics:     metrics,
		authManager: authManager,
		history:     make([]*HealthResult, 0, 1000),
		maxHistory:  1000,
		stopChan:    make(chan struct{}),
	}
}

func (h *DefaultHealthChecker) CheckAll(ctx context.Context) ([]*HealthResult, error) {
	routes, err := h.configSvc.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}

	var results []*HealthResult
	for _, route := range routes {
		routeResults, err := h.CheckRoute(ctx, route.ID)
		if err != nil {
			continue
		}
		results = append(results, routeResults...)
	}

	return results, nil
}

func (h *DefaultHealthChecker) CheckRoute(ctx context.Context, routeID string) ([]*HealthResult, error) {
	pipeline, err := h.configSvc.GetPipeline(ctx, routeID)
	if err != nil {
		return nil, err
	}

	var results []*HealthResult
	for _, layer := range pipeline.Layers {
		for _, target := range layer.Targets {
			if !target.Enabled {
				continue
			}
			result, err := h.CheckTarget(ctx, target.ID)
			if err != nil {
				results = append(results, &HealthResult{
					TargetID:     target.ID,
					CredentialID: target.CredentialID,
					Model:        target.Model,
					Status:       "unhealthy",
					Message:      err.Error(),
					CheckedAt:    time.Now(),
				})
				continue
			}
			results = append(results, result)
		}
	}

	return results, nil
}

func (h *DefaultHealthChecker) CheckTarget(ctx context.Context, targetID string) (*HealthResult, error) {
	// Find the target configuration
	routes, err := h.configSvc.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}

	var target *Target
	for _, route := range routes {
		pipeline, err := h.configSvc.GetPipeline(ctx, route.ID)
		if err != nil {
			continue
		}
		for _, layer := range pipeline.Layers {
			for i := range layer.Targets {
				if layer.Targets[i].ID == targetID {
					target = &layer.Targets[i]
					break
				}
			}
			if target != nil {
				break
			}
		}
		if target != nil {
			break
		}
	}

	if target == nil {
		return nil, &TargetNotFoundError{TargetID: targetID}
	}

	// Perform health check
	result := h.performHealthCheck(ctx, target)

	// Record result
	h.recordResult(result)

	// Update state based on result
	if result.Status == "healthy" {
		h.stateMgr.RecordSuccess(ctx, targetID, time.Duration(result.LatencyMs)*time.Millisecond)
	} else {
		h.stateMgr.RecordFailure(ctx, targetID, result.Message)
	}

	// Record event
	eventType := EventTargetRecovered
	if result.Status == "unhealthy" {
		eventType = EventTargetFailed
	}
	h.metrics.RecordEvent(&RoutingEvent{
		Type:     eventType,
		RouteID:  "",
		TargetID: targetID,
		Details: map[string]any{
			"status":     result.Status,
			"latency_ms": result.LatencyMs,
			"message":    result.Message,
		},
	})

	return result, nil
}

func (h *DefaultHealthChecker) performHealthCheck(ctx context.Context, target *Target) *HealthResult {
	result := &HealthResult{
		TargetID:     target.ID,
		CredentialID: target.CredentialID,
		Model:        target.Model,
		CheckedAt:    time.Now(),
	}

	if h.authManager == nil {
		result.Status = "unhealthy"
		result.Message = "auth manager unavailable"
		return result
	}

	// Find the auth entry for this credential
	auths := h.authManager.List()
	var targetAuth *coreauth.Auth
	for _, auth := range auths {
		if auth.ID == target.CredentialID {
			targetAuth = auth
			break
		}
	}

	if targetAuth == nil {
		result.Status = "unhealthy"
		result.Message = "credential not found"
		return result
	}

	// Build minimal request for health check
	openAIRequest := map[string]interface{}{
		"model": target.Model,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "hi"},
		},
		"stream":     true,
		"max_tokens": 1,
	}

	requestJSON, err := json.Marshal(openAIRequest)
	if err != nil {
		result.Status = "unhealthy"
		result.Message = "failed to build request"
		return result
	}

	// Get health check config for timeout
	healthConfig, _ := h.configSvc.GetHealthCheckConfig(ctx)
	if healthConfig == nil {
		cfg := DefaultHealthCheckConfig()
		healthConfig = &cfg
	}

	checkCtx, cancel := context.WithTimeout(ctx, time.Duration(healthConfig.CheckTimeoutSeconds)*time.Second)
	defer cancel()

	startTime := time.Now()

	// Execute health check request
	req := cliproxyexecutor.Request{
		Model:   target.Model,
		Payload: requestJSON,
		Format:  sdktranslator.FormatOpenAI,
	}

	opts := cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: requestJSON,
	}

	stream, err := h.authManager.ExecuteStreamWithAuth(checkCtx, targetAuth, req, opts)
	if err != nil {
		result.Status = "unhealthy"
		result.Message = err.Error()
		return result
	}

	// Wait for first chunk
	select {
	case chunk, ok := <-stream:
		if ok {
			if chunk.Err != nil {
				result.Status = "unhealthy"
				result.Message = chunk.Err.Error()
			} else {
				result.Status = "healthy"
				result.LatencyMs = time.Since(startTime).Milliseconds()
			}
			// Drain remaining chunks
			cancel()
			go func() {
				for range stream {
				}
			}()
		} else {
			result.Status = "unhealthy"
			result.Message = "stream closed without data"
		}
	case <-checkCtx.Done():
		result.Status = "unhealthy"
		result.Message = "health check timeout"
	}

	return result
}

func (h *DefaultHealthChecker) recordResult(result *HealthResult) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Ring buffer behavior
	if len(h.history) >= h.maxHistory {
		h.history = h.history[1:]
	}
	h.history = append(h.history, result)
}

func (h *DefaultHealthChecker) GetSettings(ctx context.Context) (*HealthCheckConfig, error) {
	return h.configSvc.GetHealthCheckConfig(ctx)
}

func (h *DefaultHealthChecker) UpdateSettings(ctx context.Context, settings *HealthCheckConfig) error {
	return h.configSvc.UpdateHealthCheckConfig(ctx, settings)
}

func (h *DefaultHealthChecker) GetHistory(ctx context.Context, filter HealthHistoryFilter) ([]*HealthResult, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var results []*HealthResult
	for i := len(h.history) - 1; i >= 0; i-- {
		result := h.history[i]

		// Apply filters
		if filter.TargetID != "" && result.TargetID != filter.TargetID {
			continue
		}
		if filter.Status != "" && result.Status != filter.Status {
			continue
		}
		if !filter.Since.IsZero() && result.CheckedAt.Before(filter.Since) {
			continue
		}

		results = append(results, result)

		if filter.Limit > 0 && len(results) >= filter.Limit {
			break
		}
	}

	return results, nil
}

func (h *DefaultHealthChecker) Start(ctx context.Context) error {
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		return nil
	}
	h.running = true
	h.stopChan = make(chan struct{})
	h.mu.Unlock()

	go h.runBackgroundChecks(ctx)
	return nil
}

func (h *DefaultHealthChecker) Stop(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.running {
		return nil
	}

	close(h.stopChan)
	h.running = false
	return nil
}

func (h *DefaultHealthChecker) runBackgroundChecks(ctx context.Context) {
	// Get check interval from config
	healthConfig, _ := h.configSvc.GetHealthCheckConfig(ctx)
	if healthConfig == nil {
		cfg := DefaultHealthCheckConfig()
		healthConfig = &cfg
	}

	ticker := time.NewTicker(time.Duration(healthConfig.CheckIntervalSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Check targets that are in cooldown
			h.checkCoolingTargets(ctx)

		case <-h.stopChan:
			return

		case <-ctx.Done():
			return
		}
	}
}

func (h *DefaultHealthChecker) checkCoolingTargets(ctx context.Context) {
	states, err := h.stateMgr.ListTargetStates(ctx)
	if err != nil {
		return
	}

	for _, state := range states {
		if state.Status != StatusCooling {
			continue
		}

		// Check if cooldown has expired
		if state.CooldownEndsAt == nil || time.Now().After(*state.CooldownEndsAt) {
			// Perform health check
			result, err := h.CheckTarget(ctx, state.TargetID)
			if err != nil {
				log.Debugf("health check failed for target %s: %v", state.TargetID, err)
				continue
			}

			if result.Status == "healthy" {
				h.stateMgr.EndCooldown(ctx, state.TargetID)
				log.Infof("target %s recovered after health check", state.TargetID)
			}
		}
	}
}

// TargetNotFoundError is returned when a target is not found.
type TargetNotFoundError struct {
	TargetID string
}

func (e *TargetNotFoundError) Error() string {
	return "target not found: " + e.TargetID
}
