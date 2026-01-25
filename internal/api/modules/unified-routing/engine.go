package unifiedrouting

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RoutingEngine is the core routing engine for unified routing.
type RoutingEngine interface {
	// Route determines the routing decision for a given model name.
	Route(ctx context.Context, modelName string) (*RoutingDecision, error)

	// IsEnabled returns whether unified routing is enabled.
	IsEnabled(ctx context.Context) bool

	// ShouldHideOriginalModels returns whether original models should be hidden.
	ShouldHideOriginalModels(ctx context.Context) bool

	// GetRouteNames returns all configured route names.
	GetRouteNames(ctx context.Context) []string

	// Reload reloads the engine configuration.
	Reload(ctx context.Context) error

	// GetRoutingTarget returns the target model and credential for a route alias.
	// Returns the target model name, credential ID, and any error.
	// If modelName is not a route alias, returns RouteNotFoundError.
	GetRoutingTarget(ctx context.Context, modelName string) (targetModel string, credentialID string, err error)

	// SelectTarget selects the next target from a layer based on the load balancing strategy.
	SelectTarget(ctx context.Context, routeID string, layer *Layer) (*Target, error)
}

// RoutingDecision represents the decision made by the routing engine.
type RoutingDecision struct {
	RouteID   string
	RouteName string
	TraceID   string
	Pipeline  *Pipeline
}

// DefaultRoutingEngine implements RoutingEngine.
type DefaultRoutingEngine struct {
	configSvc   ConfigService
	stateMgr    StateManager
	metrics     MetricsCollector
	authManager *coreauth.Manager

	mu          sync.RWMutex
	routeIndex  map[string]*Route // name -> route
	pipelineIndex map[string]*Pipeline // routeID -> pipeline
	
	// Round-robin state per layer
	rrCounters map[string]*atomic.Uint64 // layerKey -> counter
}

// NewRoutingEngine creates a new routing engine.
func NewRoutingEngine(
	configSvc ConfigService,
	stateMgr StateManager,
	metrics MetricsCollector,
	authManager *coreauth.Manager,
) *DefaultRoutingEngine {
	engine := &DefaultRoutingEngine{
		configSvc:     configSvc,
		stateMgr:      stateMgr,
		metrics:       metrics,
		authManager:   authManager,
		routeIndex:    make(map[string]*Route),
		pipelineIndex: make(map[string]*Pipeline),
		rrCounters:    make(map[string]*atomic.Uint64),
	}

	// Subscribe to config changes
	configSvc.Subscribe(func(event ConfigChangeEvent) {
		_ = engine.Reload(context.Background())
	})

	// Initial load
	_ = engine.Reload(context.Background())

	return engine
}

func (e *DefaultRoutingEngine) Route(ctx context.Context, modelName string) (*RoutingDecision, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Look up route by name (case-insensitive)
	route, ok := e.routeIndex[strings.ToLower(modelName)]
	if !ok {
		return nil, &RouteNotFoundError{ModelName: modelName}
	}

	if !route.Enabled {
		return nil, &RouteDisabledError{RouteName: route.Name}
	}

	pipeline, ok := e.pipelineIndex[route.ID]
	if !ok || len(pipeline.Layers) == 0 {
		return nil, &PipelineEmptyError{RouteID: route.ID}
	}

	return &RoutingDecision{
		RouteID:   route.ID,
		RouteName: route.Name,
		TraceID:   "trace-" + generateShortID(),
		Pipeline:  pipeline,
	}, nil
}

func (e *DefaultRoutingEngine) IsEnabled(ctx context.Context) bool {
	settings, err := e.configSvc.GetSettings(ctx)
	if err != nil {
		return false
	}
	return settings.Enabled
}

func (e *DefaultRoutingEngine) ShouldHideOriginalModels(ctx context.Context) bool {
	settings, err := e.configSvc.GetSettings(ctx)
	if err != nil {
		return false
	}
	return settings.Enabled && settings.HideOriginalModels
}

func (e *DefaultRoutingEngine) GetRouteNames(ctx context.Context) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	names := make([]string, 0, len(e.routeIndex))
	for _, route := range e.routeIndex {
		if route.Enabled {
			names = append(names, route.Name)
		}
	}
	return names
}

// GetRoutingTarget returns the target model and credential for a route alias.
func (e *DefaultRoutingEngine) GetRoutingTarget(ctx context.Context, modelName string) (string, string, error) {
	decision, err := e.Route(ctx, modelName)
	if err != nil {
		return "", "", err
	}

	// Select target from the first available layer
	for _, layer := range decision.Pipeline.Layers {
		target, err := e.SelectTarget(ctx, decision.RouteID, &layer)
		if err != nil {
			continue // Try next layer
		}
		if target != nil {
			return target.Model, target.CredentialID, nil
		}
	}

	return "", "", &NoAvailableTargetsError{Layer: 0}
}

// GetRoutingDecision returns the full routing decision for a model name.
func (e *DefaultRoutingEngine) GetRoutingDecision(ctx context.Context, modelName string) (*RoutingDecision, error) {
	return e.Route(ctx, modelName)
}

func (e *DefaultRoutingEngine) Reload(ctx context.Context) error {
	routes, err := e.configSvc.ListRoutes(ctx)
	if err != nil {
		return err
	}

	newRouteIndex := make(map[string]*Route, len(routes))
	newPipelineIndex := make(map[string]*Pipeline, len(routes))

	for _, route := range routes {
		newRouteIndex[strings.ToLower(route.Name)] = route

		pipeline, err := e.configSvc.GetPipeline(ctx, route.ID)
		if err != nil {
			pipeline = &Pipeline{RouteID: route.ID, Layers: []Layer{}}
		}
		newPipelineIndex[route.ID] = pipeline
	}

	e.mu.Lock()
	e.routeIndex = newRouteIndex
	e.pipelineIndex = newPipelineIndex
	e.mu.Unlock()

	log.Debugf("unified routing engine reloaded: %d routes", len(routes))
	return nil
}

// SelectTarget selects the next target from a layer based on the strategy.
func (e *DefaultRoutingEngine) SelectTarget(ctx context.Context, routeID string, layer *Layer) (*Target, error) {
	// Get available targets
	availableTargets := make([]Target, 0)
	for _, target := range layer.Targets {
		if !target.Enabled {
			continue
		}
		state, _ := e.stateMgr.GetTargetState(ctx, target.ID)
		if state != nil && state.Status != StatusHealthy {
			continue
		}
		availableTargets = append(availableTargets, target)
	}

	if len(availableTargets) == 0 {
		return nil, &NoAvailableTargetsError{Layer: layer.Level}
	}

	// Select based on strategy
	var selected *Target
	switch layer.Strategy {
	case StrategyRoundRobin, "":
		selected = e.selectRoundRobin(routeID, layer.Level, availableTargets)
	case StrategyWeightedRound:
		selected = e.selectWeightedRoundRobin(routeID, layer.Level, availableTargets)
	case StrategyRandom:
		selected = e.selectRandom(availableTargets)
	case StrategyFirstAvailable:
		selected = &availableTargets[0]
	case StrategyLeastConn:
		selected = e.selectLeastConnections(ctx, availableTargets)
	default:
		selected = e.selectRoundRobin(routeID, layer.Level, availableTargets)
	}

	return selected, nil
}

func (e *DefaultRoutingEngine) selectRoundRobin(routeID string, level int, targets []Target) *Target {
	key := fmt.Sprintf("%s:%d", routeID, level)

	e.mu.Lock()
	counter, ok := e.rrCounters[key]
	if !ok {
		counter = &atomic.Uint64{}
		e.rrCounters[key] = counter
	}
	e.mu.Unlock()

	idx := counter.Add(1) - 1
	return &targets[int(idx)%len(targets)]
}

func (e *DefaultRoutingEngine) selectWeightedRoundRobin(routeID string, level int, targets []Target) *Target {
	// Calculate total weight
	totalWeight := 0
	for _, t := range targets {
		weight := t.Weight
		if weight <= 0 {
			weight = 1
		}
		totalWeight += weight
	}

	key := fmt.Sprintf("%s:%d:weighted", routeID, level)

	e.mu.Lock()
	counter, ok := e.rrCounters[key]
	if !ok {
		counter = &atomic.Uint64{}
		e.rrCounters[key] = counter
	}
	e.mu.Unlock()

	idx := int(counter.Add(1)-1) % totalWeight

	// Find the target
	cumulative := 0
	for i := range targets {
		weight := targets[i].Weight
		if weight <= 0 {
			weight = 1
		}
		cumulative += weight
		if idx < cumulative {
			return &targets[i]
		}
	}

	return &targets[0]
}

func (e *DefaultRoutingEngine) selectRandom(targets []Target) *Target {
	idx := rand.Intn(len(targets))
	return &targets[idx]
}

func (e *DefaultRoutingEngine) selectLeastConnections(ctx context.Context, targets []Target) *Target {
	var minConn int64 = -1
	var selected *Target

	for i := range targets {
		state, _ := e.stateMgr.GetTargetState(ctx, targets[i].ID)
		conn := int64(0)
		if state != nil {
			conn = state.ActiveConnections
		}

		if minConn < 0 || conn < minConn {
			minConn = conn
			selected = &targets[i]
		}
	}

	if selected == nil {
		return &targets[0]
	}
	return selected
}

// ExecuteWithFailover executes a request with automatic failover.
func (e *DefaultRoutingEngine) ExecuteWithFailover(
	ctx context.Context,
	decision *RoutingDecision,
	executeFunc func(ctx context.Context, auth *coreauth.Auth, model string) error,
) error {
	if decision == nil || decision.Pipeline == nil {
		return fmt.Errorf("invalid routing decision")
	}

	traceBuilder := NewTraceBuilder(decision.RouteID, decision.RouteName)
	startTime := time.Now()

	// Get health check config for cooldown
	healthConfig, _ := e.configSvc.GetHealthCheckConfig(ctx)
	if healthConfig == nil {
		cfg := DefaultHealthCheckConfig()
		healthConfig = &cfg
	}

	// Try each layer in order
	for layerIdx, layer := range decision.Pipeline.Layers {
		cooldownDuration := time.Duration(layer.CooldownSeconds) * time.Second
		if cooldownDuration == 0 {
			cooldownDuration = time.Duration(healthConfig.DefaultCooldownSeconds) * time.Second
		}

		// Keep trying targets in this layer until no available targets remain
		// SelectTarget automatically excludes cooling-down targets
		for {
			target, err := e.SelectTarget(ctx, decision.RouteID, &layer)
			if err != nil {
				// No available targets in this layer, move to next layer
				break
			}

			// Find auth for this target
			auth := e.findAuth(target.CredentialID)
			if auth == nil {
				traceBuilder.AddAttempt(layer.Level, target.ID, target.CredentialID, target.Model).
					Failed("credential not found")
				// Mark as cooldown so we don't keep trying this target
				e.stateMgr.StartCooldown(ctx, target.ID, cooldownDuration)
				continue
			}

			// Execute request
			attemptStart := time.Now()
			err = executeFunc(ctx, auth, target.Model)
			attemptLatency := time.Since(attemptStart).Milliseconds()

			if err == nil {
				// Success - record and return
				e.stateMgr.RecordSuccess(ctx, target.ID, time.Since(attemptStart))
				traceBuilder.AddAttempt(layer.Level, target.ID, target.CredentialID, target.Model).
					Success(attemptLatency)

				trace := traceBuilder.Build(time.Since(startTime).Milliseconds())
				e.metrics.RecordRequest(trace)
				return nil
			}

			// Failure - immediately start cooldown and try next target in this layer
			e.stateMgr.RecordFailure(ctx, target.ID, err.Error())
			traceBuilder.AddAttempt(layer.Level, target.ID, target.CredentialID, target.Model).
				Failed(err.Error())

			// Start cooldown immediately on failure
			e.stateMgr.StartCooldown(ctx, target.ID, cooldownDuration)
			e.metrics.RecordEvent(&RoutingEvent{
				Type:     EventCooldownStarted,
				RouteID:  decision.RouteID,
				TargetID: target.ID,
				Details: map[string]any{
					"duration_seconds": int(cooldownDuration.Seconds()),
					"reason":           err.Error(),
				},
			})

			// Continue loop - SelectTarget will automatically exclude cooling-down targets
		}

		// Record layer fallback event when moving to next layer
		if layerIdx < len(decision.Pipeline.Layers)-1 {
			e.metrics.RecordEvent(&RoutingEvent{
				Type:    EventLayerFallback,
				RouteID: decision.RouteID,
				Details: map[string]any{
					"from_layer": layer.Level,
					"to_layer":   layer.Level + 1,
				},
			})
		}
	}

	// All layers exhausted
	trace := traceBuilder.Build(time.Since(startTime).Milliseconds())
	e.metrics.RecordRequest(trace)

	return &AllTargetsExhaustedError{RouteID: decision.RouteID}
}

func (e *DefaultRoutingEngine) findAuth(credentialID string) *coreauth.Auth {
	if e.authManager == nil {
		return nil
	}

	auths := e.authManager.List()
	for _, auth := range auths {
		if auth.ID == credentialID {
			return auth
		}
	}
	return nil
}

// Error types

// RouteNotFoundError is returned when a route is not found.
type RouteNotFoundError struct {
	ModelName string
}

func (e *RouteNotFoundError) Error() string {
	return fmt.Sprintf("route not found for model: %s", e.ModelName)
}

// RouteDisabledError is returned when a route is disabled.
type RouteDisabledError struct {
	RouteName string
}

func (e *RouteDisabledError) Error() string {
	return fmt.Sprintf("route is disabled: %s", e.RouteName)
}

// PipelineEmptyError is returned when a pipeline has no layers.
type PipelineEmptyError struct {
	RouteID string
}

func (e *PipelineEmptyError) Error() string {
	return fmt.Sprintf("pipeline is empty for route: %s", e.RouteID)
}

// NoAvailableTargetsError is returned when no targets are available in a layer.
type NoAvailableTargetsError struct {
	Layer int
}

func (e *NoAvailableTargetsError) Error() string {
	return fmt.Sprintf("no available targets in layer %d", e.Layer)
}

// AllTargetsExhaustedError is returned when all targets in all layers are exhausted.
type AllTargetsExhaustedError struct {
	RouteID string
}

func (e *AllTargetsExhaustedError) Error() string {
	return fmt.Sprintf("all targets exhausted for route: %s", e.RouteID)
}
