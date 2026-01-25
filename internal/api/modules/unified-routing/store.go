package unifiedrouting

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// ================== Store Interfaces ==================

// ConfigStore defines the interface for configuration persistence.
type ConfigStore interface {
	// Settings
	LoadSettings(ctx context.Context) (*Settings, error)
	SaveSettings(ctx context.Context, settings *Settings) error

	// Health check config
	LoadHealthCheckConfig(ctx context.Context) (*HealthCheckConfig, error)
	SaveHealthCheckConfig(ctx context.Context, config *HealthCheckConfig) error

	// Routes
	ListRoutes(ctx context.Context) ([]*Route, error)
	GetRoute(ctx context.Context, id string) (*Route, error)
	CreateRoute(ctx context.Context, route *Route) error
	UpdateRoute(ctx context.Context, route *Route) error
	DeleteRoute(ctx context.Context, id string) error

	// Pipelines
	GetPipeline(ctx context.Context, routeID string) (*Pipeline, error)
	SavePipeline(ctx context.Context, routeID string, pipeline *Pipeline) error
}

// StateStore defines the interface for runtime state storage (in-memory).
type StateStore interface {
	GetTargetState(ctx context.Context, targetID string) (*TargetState, error)
	SetTargetState(ctx context.Context, state *TargetState) error
	ListTargetStates(ctx context.Context) ([]*TargetState, error)
	DeleteTargetState(ctx context.Context, targetID string) error
}

// MetricsStore defines the interface for metrics storage.
type MetricsStore interface {
	// Traces
	RecordTrace(ctx context.Context, trace *RequestTrace) error
	GetTraces(ctx context.Context, filter TraceFilter) ([]*RequestTrace, error)
	GetTrace(ctx context.Context, traceID string) (*RequestTrace, error)

	// Events
	RecordEvent(ctx context.Context, event *RoutingEvent) error
	GetEvents(ctx context.Context, filter EventFilter) ([]*RoutingEvent, error)

	// Stats (computed from traces)
	GetStats(ctx context.Context, filter StatsFilter) (*AggregatedStats, error)
}

// ================== File-based Config Store ==================

// FileConfigStore implements ConfigStore using file-based persistence.
type FileConfigStore struct {
	baseDir string
	mu      sync.RWMutex
}

// NewFileConfigStore creates a new file-based config store.
func NewFileConfigStore(baseDir string) (*FileConfigStore, error) {
	// Create directories if they don't exist
	dirs := []string{
		baseDir,
		filepath.Join(baseDir, "routes"),
		filepath.Join(baseDir, "pipelines"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return &FileConfigStore{baseDir: baseDir}, nil
}

func (s *FileConfigStore) settingsPath() string {
	return filepath.Join(s.baseDir, "settings.yaml")
}

func (s *FileConfigStore) healthConfigPath() string {
	return filepath.Join(s.baseDir, "health-config.yaml")
}

func (s *FileConfigStore) routePath(id string) string {
	return filepath.Join(s.baseDir, "routes", id+".yaml")
}

func (s *FileConfigStore) pipelinePath(routeID string) string {
	return filepath.Join(s.baseDir, "pipelines", routeID+".yaml")
}

func (s *FileConfigStore) LoadSettings(ctx context.Context) (*Settings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.settingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Settings{Enabled: false, HideOriginalModels: false}, nil
		}
		return nil, err
	}

	var settings Settings
	if err := yaml.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

func (s *FileConfigStore) SaveSettings(ctx context.Context, settings *Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := yaml.Marshal(settings)
	if err != nil {
		return err
	}
	return os.WriteFile(s.settingsPath(), data, 0644)
}

func (s *FileConfigStore) LoadHealthCheckConfig(ctx context.Context) (*HealthCheckConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.healthConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultHealthCheckConfig()
			return &cfg, nil
		}
		return nil, err
	}

	var config HealthCheckConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func (s *FileConfigStore) SaveHealthCheckConfig(ctx context.Context, config *HealthCheckConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	return os.WriteFile(s.healthConfigPath(), data, 0644)
}

func (s *FileConfigStore) ListRoutes(ctx context.Context) ([]*Route, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	routesDir := filepath.Join(s.baseDir, "routes")
	entries, err := os.ReadDir(routesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Route{}, nil
		}
		return nil, err
	}

	var routes []*Route
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(routesDir, entry.Name()))
		if err != nil {
			continue
		}

		var route Route
		if err := yaml.Unmarshal(data, &route); err != nil {
			continue
		}
		routes = append(routes, &route)
	}

	return routes, nil
}

func (s *FileConfigStore) GetRoute(ctx context.Context, id string) (*Route, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.routePath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("route not found: %s", id)
		}
		return nil, err
	}

	var route Route
	if err := yaml.Unmarshal(data, &route); err != nil {
		return nil, err
	}
	return &route, nil
}

func (s *FileConfigStore) CreateRoute(ctx context.Context, route *Route) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if route already exists
	if _, err := os.Stat(s.routePath(route.ID)); err == nil {
		return fmt.Errorf("route already exists: %s", route.ID)
	}

	route.CreatedAt = time.Now()
	route.UpdatedAt = route.CreatedAt

	data, err := yaml.Marshal(route)
	if err != nil {
		return err
	}
	return os.WriteFile(s.routePath(route.ID), data, 0644)
}

func (s *FileConfigStore) UpdateRoute(ctx context.Context, route *Route) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if route exists
	if _, err := os.Stat(s.routePath(route.ID)); os.IsNotExist(err) {
		return fmt.Errorf("route not found: %s", route.ID)
	}

	route.UpdatedAt = time.Now()

	data, err := yaml.Marshal(route)
	if err != nil {
		return err
	}
	return os.WriteFile(s.routePath(route.ID), data, 0644)
}

func (s *FileConfigStore) DeleteRoute(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete route file
	if err := os.Remove(s.routePath(id)); err != nil && !os.IsNotExist(err) {
		return err
	}

	// Delete pipeline file
	if err := os.Remove(s.pipelinePath(id)); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (s *FileConfigStore) GetPipeline(ctx context.Context, routeID string) (*Pipeline, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.pipelinePath(routeID))
	if err != nil {
		if os.IsNotExist(err) {
			return &Pipeline{RouteID: routeID, Layers: []Layer{}}, nil
		}
		return nil, err
	}

	var pipeline Pipeline
	if err := yaml.Unmarshal(data, &pipeline); err != nil {
		return nil, err
	}
	pipeline.RouteID = routeID
	return &pipeline, nil
}

func (s *FileConfigStore) SavePipeline(ctx context.Context, routeID string, pipeline *Pipeline) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	pipeline.RouteID = routeID
	data, err := yaml.Marshal(pipeline)
	if err != nil {
		return err
	}
	return os.WriteFile(s.pipelinePath(routeID), data, 0644)
}

// ================== In-Memory State Store ==================

// MemoryStateStore implements StateStore using in-memory storage.
type MemoryStateStore struct {
	mu     sync.RWMutex
	states map[string]*TargetState
}

// NewMemoryStateStore creates a new in-memory state store.
func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{
		states: make(map[string]*TargetState),
	}
}

func (s *MemoryStateStore) GetTargetState(ctx context.Context, targetID string) (*TargetState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, ok := s.states[targetID]
	if !ok {
		// Return default healthy state
		return &TargetState{
			TargetID: targetID,
			Status:   StatusHealthy,
		}, nil
	}
	return state, nil
}

func (s *MemoryStateStore) SetTargetState(ctx context.Context, state *TargetState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.states[state.TargetID] = state
	return nil
}

func (s *MemoryStateStore) ListTargetStates(ctx context.Context) ([]*TargetState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	states := make([]*TargetState, 0, len(s.states))
	for _, state := range s.states {
		states = append(states, state)
	}
	return states, nil
}

func (s *MemoryStateStore) DeleteTargetState(ctx context.Context, targetID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.states, targetID)
	return nil
}

// ================== File-based Metrics Store ==================

// FileMetricsStore implements MetricsStore using file-based storage.
// Traces are stored as JSON files in the traces directory.
// Directory size is enforced by cleanup logic similar to LogDirCleaner.
type FileMetricsStore struct {
	mu         sync.RWMutex
	baseDir    string
	maxSizeMB  int // Maximum total size in MB for traces directory
}

// NewFileMetricsStore creates a new file-based metrics store.
func NewFileMetricsStore(baseDir string, maxSizeMB int) (*FileMetricsStore, error) {
	tracesDir := filepath.Join(baseDir, "traces")
	if err := os.MkdirAll(tracesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create traces directory: %w", err)
	}

	if maxSizeMB <= 0 {
		maxSizeMB = 100 // Default 100MB
	}

	store := &FileMetricsStore{
		baseDir:   baseDir,
		maxSizeMB: maxSizeMB,
	}

	// Start background cleanup
	go store.runCleanup()

	return store, nil
}

func (s *FileMetricsStore) tracesDir() string {
	return filepath.Join(s.baseDir, "traces")
}

func (s *FileMetricsStore) traceFilename(trace *RequestTrace) string {
	// Format: {timestamp}-{trace_id}.json
	ts := trace.Timestamp.Format("2006-01-02T150405")
	return fmt.Sprintf("%s-%s.json", ts, trace.TraceID[:8])
}

func (s *FileMetricsStore) RecordTrace(ctx context.Context, trace *RequestTrace) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(trace)
	if err != nil {
		return fmt.Errorf("failed to marshal trace: %w", err)
	}

	filename := s.traceFilename(trace)
	filePath := filepath.Join(s.tracesDir(), filename)

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write trace file: %w", err)
	}

	return nil
}

func (s *FileMetricsStore) GetTraces(ctx context.Context, filter TraceFilter) ([]*RequestTrace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.tracesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []*RequestTrace{}, nil
		}
		return nil, err
	}

	// Sort by name descending (newest first since filename starts with timestamp)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() > entries[j].Name()
	})

	var result []*RequestTrace
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.tracesDir(), entry.Name()))
		if err != nil {
			continue
		}

		var trace RequestTrace
		if err := json.Unmarshal(data, &trace); err != nil {
			continue
		}

		// Apply filters
		if filter.RouteID != "" && trace.RouteID != filter.RouteID {
			continue
		}
		if filter.Status != "" && string(trace.Status) != filter.Status {
			continue
		}

		result = append(result, &trace)

		if filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}

	return result, nil
}

func (s *FileMetricsStore) GetTrace(ctx context.Context, traceID string) (*RequestTrace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.tracesDir())
	if err != nil {
		return nil, fmt.Errorf("trace not found: %s", traceID)
	}

	shortID := traceID
	if len(traceID) > 8 {
		shortID = traceID[:8]
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// Check if filename contains the trace ID
		if !strings.Contains(entry.Name(), shortID) {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.tracesDir(), entry.Name()))
		if err != nil {
			continue
		}

		var trace RequestTrace
		if err := json.Unmarshal(data, &trace); err != nil {
			continue
		}

		if trace.TraceID == traceID || (len(trace.TraceID) >= 8 && trace.TraceID[:8] == shortID) {
			return &trace, nil
		}
	}

	return nil, fmt.Errorf("trace not found: %s", traceID)
}

// RecordEvent is a no-op for file store (events are no longer tracked)
func (s *FileMetricsStore) RecordEvent(ctx context.Context, event *RoutingEvent) error {
	// Events are no longer stored - this is intentionally a no-op
	return nil
}

// GetEvents returns empty list (events are no longer tracked)
func (s *FileMetricsStore) GetEvents(ctx context.Context, filter EventFilter) ([]*RoutingEvent, error) {
	// Events are no longer stored
	return []*RoutingEvent{}, nil
}

func (s *FileMetricsStore) GetStats(ctx context.Context, filter StatsFilter) (*AggregatedStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := &AggregatedStats{
		Period: filter.Period,
	}

	// Calculate time range
	var since time.Time
	switch filter.Period {
	case "1h":
		since = time.Now().Add(-1 * time.Hour)
	case "24h":
		since = time.Now().Add(-24 * time.Hour)
	case "7d":
		since = time.Now().Add(-7 * 24 * time.Hour)
	case "30d":
		since = time.Now().Add(-30 * 24 * time.Hour)
	default:
		since = time.Now().Add(-1 * time.Hour)
	}

	// Load all traces from files
	entries, err := os.ReadDir(s.tracesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return stats, nil
		}
		return nil, err
	}

	var totalLatency int64
	layerCounts := make(map[int]int64)
	targetCounts := make(map[string]*TargetDistribution)
	attemptsCounts := make(map[int]int64) // Track 1-attempt, 2-attempt, etc. successes

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.tracesDir(), entry.Name()))
		if err != nil {
			continue
		}

		var trace RequestTrace
		if err := json.Unmarshal(data, &trace); err != nil {
			continue
		}

		if trace.Timestamp.Before(since) {
			continue
		}

		stats.TotalRequests++
		totalLatency += trace.TotalLatencyMs

		switch trace.Status {
		case TraceStatusSuccess, TraceStatusRetry, TraceStatusFallback:
			stats.SuccessfulRequests++
		case TraceStatusFailed:
			stats.FailedRequests++
		}

		// Track attempts distribution (how many attempts needed for success)
		attemptCount := len(trace.Attempts)
		if trace.Status == TraceStatusSuccess || trace.Status == TraceStatusRetry || trace.Status == TraceStatusFallback {
			attemptsCounts[attemptCount]++
		}

		// Track layer distribution (use the successful attempt's layer)
		for _, attempt := range trace.Attempts {
			if attempt.Status == AttemptStatusSuccess {
				layerCounts[attempt.Layer]++

				// Track target distribution
				if _, ok := targetCounts[attempt.TargetID]; !ok {
					targetCounts[attempt.TargetID] = &TargetDistribution{
						TargetID:     attempt.TargetID,
						CredentialID: attempt.CredentialID,
					}
				}
				targetCounts[attempt.TargetID].Requests++
				break
			}
		}
	}

	if stats.TotalRequests > 0 {
		stats.SuccessRate = float64(stats.SuccessfulRequests) / float64(stats.TotalRequests)
		stats.AvgLatencyMs = totalLatency / stats.TotalRequests
	}

	// Build layer distribution
	for level, count := range layerCounts {
		stats.LayerDistribution = append(stats.LayerDistribution, LayerDistribution{
			Level:      level,
			Requests:   count,
			Percentage: float64(count) / float64(stats.TotalRequests) * 100,
		})
	}

	// Build target distribution
	for _, td := range targetCounts {
		stats.TargetDistribution = append(stats.TargetDistribution, *td)
	}

	// Build attempts distribution
	for attempts, count := range attemptsCounts {
		stats.AttemptsDistribution = append(stats.AttemptsDistribution, AttemptsDistribution{
			Attempts:   attempts,
			Count:      count,
			Percentage: float64(count) / float64(stats.SuccessfulRequests) * 100,
		})
	}
	// Sort by attempts
	sort.Slice(stats.AttemptsDistribution, func(i, j int) bool {
		return stats.AttemptsDistribution[i].Attempts < stats.AttemptsDistribution[j].Attempts
	})

	return stats, nil
}

// runCleanup runs the background cleanup task to enforce directory size limit.
func (s *FileMetricsStore) runCleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.enforceSize()
	}
}

// enforceSize removes old trace files if total size exceeds maxSizeMB.
func (s *FileMetricsStore) enforceSize() {
	s.mu.Lock()
	defer s.mu.Unlock()

	maxBytes := int64(s.maxSizeMB) * 1024 * 1024

	entries, err := os.ReadDir(s.tracesDir())
	if err != nil {
		return
	}

	type traceFile struct {
		path    string
		size    int64
		modTime time.Time
	}

	var files []traceFile
	var total int64

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(s.tracesDir(), entry.Name())
		files = append(files, traceFile{
			path:    path,
			size:    info.Size(),
			modTime: info.ModTime(),
		})
		total += info.Size()
	}

	if total <= maxBytes {
		return
	}

	// Sort by modTime ascending (oldest first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	// Remove oldest files until under limit
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
		}
	}
}

// MarshalJSON implements json.Marshaler for TargetState.
func (s *TargetState) MarshalJSON() ([]byte, error) {
	type Alias TargetState
	return json.Marshal(&struct {
		*Alias
		CooldownRemainingSeconds int `json:"cooldown_remaining_seconds,omitempty"`
	}{
		Alias:                    (*Alias)(s),
		CooldownRemainingSeconds: s.CooldownRemainingSeconds(),
	})
}

// CooldownRemainingSeconds returns the remaining cooldown time in seconds.
func (s *TargetState) CooldownRemainingSeconds() int {
	if s.CooldownEndsAt == nil || s.Status != StatusCooling {
		return 0
	}
	remaining := time.Until(*s.CooldownEndsAt).Seconds()
	if remaining < 0 {
		return 0
	}
	return int(remaining)
}
