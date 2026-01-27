package unifiedrouting

import (
	"context"
	"sync"
	"time"
)

// StateManager manages runtime state for unified routing.
type StateManager interface {
	// State queries
	GetOverview(ctx context.Context) (*StateOverview, error)
	GetRouteState(ctx context.Context, routeID string) (*RouteState, error)
	GetTargetState(ctx context.Context, targetID string) (*TargetState, error)
	ListTargetStates(ctx context.Context) ([]*TargetState, error)

	// State changes (called by engine)
	RecordSuccess(ctx context.Context, targetID string, latency time.Duration)
	RecordFailure(ctx context.Context, targetID string, reason string)
	StartCooldown(ctx context.Context, targetID string, duration time.Duration)
	EndCooldown(ctx context.Context, targetID string)

	// Manual operations
	ResetTarget(ctx context.Context, targetID string) error
	ForceCooldown(ctx context.Context, targetID string, duration time.Duration) error

	// Initialize/cleanup
	InitializeTarget(ctx context.Context, targetID string) error
	RemoveTarget(ctx context.Context, targetID string) error
}

// DefaultStateManager implements StateManager.
type DefaultStateManager struct {
	store        StateStore
	configSvc    ConfigService
	mu           sync.RWMutex
	cooldownChan chan string // Channel for cooldown expiry notifications
	stopChan     chan struct{}
}

// NewStateManager creates a new state manager.
func NewStateManager(store StateStore, configSvc ConfigService) *DefaultStateManager {
	sm := &DefaultStateManager{
		store:        store,
		configSvc:    configSvc,
		cooldownChan: make(chan string, 100),
		stopChan:     make(chan struct{}),
	}

	// Start cooldown monitor
	go sm.monitorCooldowns()

	return sm
}

func (m *DefaultStateManager) GetOverview(ctx context.Context) (*StateOverview, error) {
	settings, err := m.configSvc.GetSettings(ctx)
	if err != nil {
		return nil, err
	}

	routes, err := m.configSvc.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}

	overview := &StateOverview{
		UnifiedRoutingEnabled: settings.Enabled,
		HideOriginalModels:    settings.HideOriginalModels,
		TotalRoutes:           len(routes),
		Routes:                make([]RouteState, 0, len(routes)),
	}

	for _, route := range routes {
		routeState, err := m.GetRouteState(ctx, route.ID)
		if err != nil {
			continue
		}

		switch routeState.Status {
		case "healthy":
			overview.HealthyRoutes++
		case "degraded":
			overview.DegradedRoutes++
		case "unhealthy":
			overview.UnhealthyRoutes++
		}

		overview.Routes = append(overview.Routes, *routeState)
	}

	return overview, nil
}

func (m *DefaultStateManager) GetRouteState(ctx context.Context, routeID string) (*RouteState, error) {
	route, err := m.configSvc.GetRoute(ctx, routeID)
	if err != nil {
		return nil, err
	}

	pipeline, err := m.configSvc.GetPipeline(ctx, routeID)
	if err != nil {
		return nil, err
	}

	routeState := &RouteState{
		RouteID:     route.ID,
		RouteName:   route.Name,
		ActiveLayer: 1,
		LayerStates: make([]LayerState, 0, len(pipeline.Layers)),
	}

	healthyTargets := 0
	totalTargets := 0
	activeLayerFound := false

	for _, layer := range pipeline.Layers {
		layerState := LayerState{
			Level:        layer.Level,
			Status:       "standby",
			TargetStates: make([]*TargetState, 0, len(layer.Targets)),
		}

		healthyInLayer := 0
		for _, target := range layer.Targets {
			totalTargets++
			state, _ := m.store.GetTargetState(ctx, target.ID)
			if state == nil {
				state = &TargetState{
					TargetID: target.ID,
					Status:   StatusHealthy,
				}
			}

			// Check if cooldown has expired
			if state.Status == StatusCooling && state.CooldownEndsAt != nil {
				if time.Now().After(*state.CooldownEndsAt) {
					state.Status = StatusHealthy
					state.CooldownEndsAt = nil
				}
			}

			if state.Status == StatusHealthy {
				healthyTargets++
				healthyInLayer++
			}

			layerState.TargetStates = append(layerState.TargetStates, state)
		}

		// Determine layer status
		if healthyInLayer > 0 && !activeLayerFound {
			layerState.Status = "active"
			routeState.ActiveLayer = layer.Level
			activeLayerFound = true
		} else if healthyInLayer == 0 {
			layerState.Status = "exhausted"
		}

		routeState.LayerStates = append(routeState.LayerStates, layerState)
	}

	// Determine overall route status
	if healthyTargets == totalTargets {
		routeState.Status = "healthy"
	} else if healthyTargets == 0 {
		routeState.Status = "unhealthy"
	} else {
		routeState.Status = "degraded"
	}

	return routeState, nil
}

func (m *DefaultStateManager) GetTargetState(ctx context.Context, targetID string) (*TargetState, error) {
	state, err := m.store.GetTargetState(ctx, targetID)
	if err != nil {
		return nil, err
	}

	// Check if cooldown has expired
	if state.Status == StatusCooling && state.CooldownEndsAt != nil {
		if time.Now().After(*state.CooldownEndsAt) {
			state.Status = StatusHealthy
			state.CooldownEndsAt = nil
			_ = m.store.SetTargetState(ctx, state)
		}
	}

	return state, nil
}

func (m *DefaultStateManager) ListTargetStates(ctx context.Context) ([]*TargetState, error) {
	return m.store.ListTargetStates(ctx)
}

func (m *DefaultStateManager) RecordSuccess(ctx context.Context, targetID string, latency time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, _ := m.store.GetTargetState(ctx, targetID)
	if state == nil {
		state = &TargetState{TargetID: targetID}
	}

	now := time.Now()
	state.Status = StatusHealthy
	state.ConsecutiveFailures = 0
	state.LastSuccessAt = &now
	state.CooldownEndsAt = nil
	state.TotalRequests++
	state.SuccessfulRequests++

	_ = m.store.SetTargetState(ctx, state)
}

func (m *DefaultStateManager) RecordFailure(ctx context.Context, targetID string, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, _ := m.store.GetTargetState(ctx, targetID)
	if state == nil {
		state = &TargetState{TargetID: targetID}
	}

	now := time.Now()
	state.ConsecutiveFailures++
	state.LastFailureAt = &now
	state.LastFailureReason = reason
	state.TotalRequests++

	_ = m.store.SetTargetState(ctx, state)
}

func (m *DefaultStateManager) StartCooldown(ctx context.Context, targetID string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, _ := m.store.GetTargetState(ctx, targetID)
	if state == nil {
		state = &TargetState{TargetID: targetID}
	}

	cooldownEnd := time.Now().Add(duration)
	state.Status = StatusCooling
	state.CooldownEndsAt = &cooldownEnd

	_ = m.store.SetTargetState(ctx, state)

	// Schedule cooldown expiry
	go func() {
		timer := time.NewTimer(duration)
		defer timer.Stop()

		select {
		case <-timer.C:
			m.cooldownChan <- targetID
		case <-m.stopChan:
			return
		}
	}()
}

func (m *DefaultStateManager) EndCooldown(ctx context.Context, targetID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, _ := m.store.GetTargetState(ctx, targetID)
	if state == nil {
		return
	}

	state.Status = StatusHealthy
	state.CooldownEndsAt = nil

	_ = m.store.SetTargetState(ctx, state)
}

func (m *DefaultStateManager) ResetTarget(ctx context.Context, targetID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := &TargetState{
		TargetID:            targetID,
		Status:              StatusHealthy,
		ConsecutiveFailures: 0,
		CooldownEndsAt:      nil,
	}

	return m.store.SetTargetState(ctx, state)
}

func (m *DefaultStateManager) ForceCooldown(ctx context.Context, targetID string, duration time.Duration) error {
	m.StartCooldown(ctx, targetID, duration)
	return nil
}

func (m *DefaultStateManager) InitializeTarget(ctx context.Context, targetID string) error {
	state := &TargetState{
		TargetID: targetID,
		Status:   StatusHealthy,
	}
	return m.store.SetTargetState(ctx, state)
}

func (m *DefaultStateManager) RemoveTarget(ctx context.Context, targetID string) error {
	return m.store.DeleteTargetState(ctx, targetID)
}

func (m *DefaultStateManager) monitorCooldowns() {
	for {
		select {
		case targetID := <-m.cooldownChan:
			ctx := context.Background()
			state, err := m.store.GetTargetState(ctx, targetID)
			if err != nil || state == nil {
				continue
			}

			// Check if still in cooldown and cooldown has expired
			if state.Status == StatusCooling && state.CooldownEndsAt != nil {
				if time.Now().After(*state.CooldownEndsAt) {
					m.EndCooldown(ctx, targetID)
				}
			}

		case <-m.stopChan:
			return
		}
	}
}

// Stop stops the state manager background tasks.
func (m *DefaultStateManager) Stop() {
	close(m.stopChan)
}

// IsTargetAvailable checks if a target is available for routing.
func (m *DefaultStateManager) IsTargetAvailable(ctx context.Context, targetID string) bool {
	state, err := m.GetTargetState(ctx, targetID)
	if err != nil {
		return true // Default to available if error
	}
	return state.Status == StatusHealthy
}

// GetAvailableTargetsInLayer returns available targets in a layer.
func (m *DefaultStateManager) GetAvailableTargetsInLayer(ctx context.Context, layer *Layer) []Target {
	available := make([]Target, 0, len(layer.Targets))
	for _, target := range layer.Targets {
		if !target.Enabled {
			continue
		}
		if m.IsTargetAvailable(ctx, target.ID) {
			available = append(available, target)
		}
	}
	return available
}
