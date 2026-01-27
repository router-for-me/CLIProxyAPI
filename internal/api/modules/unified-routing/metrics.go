package unifiedrouting

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MetricsCollector collects and provides metrics for unified routing.
type MetricsCollector interface {
	// Recording
	RecordRequest(trace *RequestTrace)
	RecordEvent(event *RoutingEvent)

	// Queries
	GetStats(ctx context.Context, filter StatsFilter) (*AggregatedStats, error)
	GetRouteStats(ctx context.Context, routeID string, filter StatsFilter) (*AggregatedStats, error)
	GetTargetStats(ctx context.Context, targetID string, filter StatsFilter) (*AggregatedStats, error)

	// Events
	GetEvents(ctx context.Context, filter EventFilter) ([]*RoutingEvent, error)

	// Traces
	GetTraces(ctx context.Context, filter TraceFilter) ([]*RequestTrace, error)
	GetTrace(ctx context.Context, traceID string) (*RequestTrace, error)

	// Real-time subscriptions
	Subscribe(ctx context.Context) (<-chan MetricUpdate, error)
}

// MetricUpdate represents a real-time metric update.
type MetricUpdate struct {
	Type      string      `json:"type"` // "trace", "event", "stats"
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// DefaultMetricsCollector implements MetricsCollector.
type DefaultMetricsCollector struct {
	store       MetricsStore
	mu          sync.RWMutex
	subscribers []chan MetricUpdate
}

// NewMetricsCollector creates a new metrics collector.
func NewMetricsCollector(store MetricsStore) *DefaultMetricsCollector {
	return &DefaultMetricsCollector{
		store:       store,
		subscribers: make([]chan MetricUpdate, 0),
	}
}

func (c *DefaultMetricsCollector) RecordRequest(trace *RequestTrace) {
	if trace.TraceID == "" {
		trace.TraceID = "trace-" + uuid.New().String()[:8]
	}
	if trace.Timestamp.IsZero() {
		trace.Timestamp = time.Now()
	}

	ctx := context.Background()
	_ = c.store.RecordTrace(ctx, trace)

	// Notify subscribers
	c.broadcast(MetricUpdate{
		Type:      "trace",
		Timestamp: time.Now(),
		Data:      trace,
	})
}

func (c *DefaultMetricsCollector) RecordEvent(event *RoutingEvent) {
	if event.ID == "" {
		event.ID = "evt-" + uuid.New().String()[:8]
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	ctx := context.Background()
	_ = c.store.RecordEvent(ctx, event)

	// Notify subscribers
	c.broadcast(MetricUpdate{
		Type:      "event",
		Timestamp: time.Now(),
		Data:      event,
	})
}

func (c *DefaultMetricsCollector) GetStats(ctx context.Context, filter StatsFilter) (*AggregatedStats, error) {
	return c.store.GetStats(ctx, filter)
}

func (c *DefaultMetricsCollector) GetRouteStats(ctx context.Context, routeID string, filter StatsFilter) (*AggregatedStats, error) {
	// Get all traces for this route and calculate stats
	traces, err := c.store.GetTraces(ctx, TraceFilter{RouteID: routeID, Limit: 10000})
	if err != nil {
		return nil, err
	}

	return c.calculateStats(traces, filter), nil
}

func (c *DefaultMetricsCollector) GetTargetStats(ctx context.Context, targetID string, filter StatsFilter) (*AggregatedStats, error) {
	// Get all traces and filter by target
	traces, err := c.store.GetTraces(ctx, TraceFilter{Limit: 10000})
	if err != nil {
		return nil, err
	}

	// Filter traces that used this target
	var filteredTraces []*RequestTrace
	for _, trace := range traces {
		for _, attempt := range trace.Attempts {
			if attempt.TargetID == targetID {
				filteredTraces = append(filteredTraces, trace)
				break
			}
		}
	}

	return c.calculateStats(filteredTraces, filter), nil
}

func (c *DefaultMetricsCollector) calculateStats(traces []*RequestTrace, filter StatsFilter) *AggregatedStats {
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

	var totalLatency int64
	layerCounts := make(map[int]int64)
	targetStats := make(map[string]*TargetDistribution)

	for _, trace := range traces {
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

		// Track layer and target distribution
		for _, attempt := range trace.Attempts {
			if attempt.Status == AttemptStatusSuccess {
				layerCounts[attempt.Layer]++

				if _, ok := targetStats[attempt.TargetID]; !ok {
					targetStats[attempt.TargetID] = &TargetDistribution{
						TargetID:     attempt.TargetID,
						CredentialID: attempt.CredentialID,
					}
				}
				targetStats[attempt.TargetID].Requests++
				break
			}
		}
	}

	if stats.TotalRequests > 0 {
		stats.SuccessRate = float64(stats.SuccessfulRequests) / float64(stats.TotalRequests)
		stats.AvgLatencyMs = totalLatency / stats.TotalRequests
	}

	// Build distributions
	for level, count := range layerCounts {
		percentage := float64(0)
		if stats.TotalRequests > 0 {
			percentage = float64(count) / float64(stats.TotalRequests) * 100
		}
		stats.LayerDistribution = append(stats.LayerDistribution, LayerDistribution{
			Level:      level,
			Requests:   count,
			Percentage: percentage,
		})
	}

	for _, td := range targetStats {
		stats.TargetDistribution = append(stats.TargetDistribution, *td)
	}

	return stats
}

func (c *DefaultMetricsCollector) GetEvents(ctx context.Context, filter EventFilter) ([]*RoutingEvent, error) {
	return c.store.GetEvents(ctx, filter)
}

func (c *DefaultMetricsCollector) GetTraces(ctx context.Context, filter TraceFilter) ([]*RequestTrace, error) {
	return c.store.GetTraces(ctx, filter)
}

func (c *DefaultMetricsCollector) GetTrace(ctx context.Context, traceID string) (*RequestTrace, error) {
	return c.store.GetTrace(ctx, traceID)
}

func (c *DefaultMetricsCollector) Subscribe(ctx context.Context) (<-chan MetricUpdate, error) {
	ch := make(chan MetricUpdate, 100)

	c.mu.Lock()
	c.subscribers = append(c.subscribers, ch)
	c.mu.Unlock()

	// Clean up when context is done
	go func() {
		<-ctx.Done()
		c.mu.Lock()
		for i, sub := range c.subscribers {
			if sub == ch {
				c.subscribers = append(c.subscribers[:i], c.subscribers[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
		close(ch)
	}()

	return ch, nil
}

func (c *DefaultMetricsCollector) broadcast(update MetricUpdate) {
	c.mu.RLock()
	subscribers := c.subscribers
	c.mu.RUnlock()

	for _, ch := range subscribers {
		select {
		case ch <- update:
		default:
			// Channel full, skip
		}
	}
}

// TraceBuilder helps build request traces.
type TraceBuilder struct {
	trace *RequestTrace
}

// NewTraceBuilder creates a new trace builder.
func NewTraceBuilder(routeID, routeName string) *TraceBuilder {
	return &TraceBuilder{
		trace: &RequestTrace{
			TraceID:   "trace-" + uuid.New().String()[:8],
			RouteID:   routeID,
			RouteName: routeName,
			Timestamp: time.Now(),
			Attempts:  make([]AttemptTrace, 0),
		},
	}
}

// AddAttempt adds an attempt to the trace.
func (b *TraceBuilder) AddAttempt(layer int, targetID, credentialID, model string) *AttemptBuilder {
	attempt := AttemptTrace{
		Attempt:      len(b.trace.Attempts) + 1,
		Layer:        layer,
		TargetID:     targetID,
		CredentialID: credentialID,
		Model:        model,
	}
	b.trace.Attempts = append(b.trace.Attempts, attempt)
	return &AttemptBuilder{
		trace:   b.trace,
		attempt: &b.trace.Attempts[len(b.trace.Attempts)-1],
	}
}

// Build finalizes and returns the trace.
func (b *TraceBuilder) Build(totalLatencyMs int64) *RequestTrace {
	b.trace.TotalLatencyMs = totalLatencyMs

	// Determine trace status based on attempts
	hasSuccess := false
	hasRetry := false
	hasFallback := false

	for i, attempt := range b.trace.Attempts {
		if attempt.Status == AttemptStatusSuccess {
			hasSuccess = true
			if i > 0 {
				// Check if we retried or fell back
				prevLayer := b.trace.Attempts[i-1].Layer
				if attempt.Layer > prevLayer {
					hasFallback = true
				} else {
					hasRetry = true
				}
			}
			break
		}
	}

	if !hasSuccess {
		b.trace.Status = TraceStatusFailed
	} else if hasFallback {
		b.trace.Status = TraceStatusFallback
	} else if hasRetry {
		b.trace.Status = TraceStatusRetry
	} else {
		b.trace.Status = TraceStatusSuccess
	}

	return b.trace
}

// AttemptBuilder helps build attempt traces.
type AttemptBuilder struct {
	trace   *RequestTrace
	attempt *AttemptTrace
}

// Success marks the attempt as successful.
func (b *AttemptBuilder) Success(latencyMs int64) *TraceBuilder {
	b.attempt.Status = AttemptStatusSuccess
	b.attempt.LatencyMs = latencyMs
	return &TraceBuilder{trace: b.trace}
}

// Failed marks the attempt as failed.
func (b *AttemptBuilder) Failed(err string) *TraceBuilder {
	b.attempt.Status = AttemptStatusFailed
	b.attempt.Error = err
	return &TraceBuilder{trace: b.trace}
}

// Skipped marks the attempt as skipped.
func (b *AttemptBuilder) Skipped(reason string) *TraceBuilder {
	b.attempt.Status = AttemptStatusSkipped
	b.attempt.Error = reason
	return &TraceBuilder{trace: b.trace}
}
