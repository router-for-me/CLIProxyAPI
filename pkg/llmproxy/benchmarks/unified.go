// Package benchmarks provides unified benchmark access with fallback to hardcoded values.
// This integrates with tokenledger for dynamic data while maintaining backward compatibility.
package benchmarks

import (
	"sync"
	"time"
)

// Hardcoded fallback values - maps model IDs to benchmark scores
var (
	qualityProxy = map[string]float64{
		"claude-opus-4.6":               0.95,
		"claude-opus-4.6-1m":            0.96,
		"claude-sonnet-4.6":             0.88,
		"claude-haiku-4.5":              0.75,
		"gpt-5.3-codex-high":            0.92,
		"gpt-5.3-codex":                 0.82,
		"claude-4.5-opus-high-thinking": 0.94,
		"claude-4.5-opus-high":          0.92,
		"claude-4.5-sonnet-thinking":   0.85,
		"claude-4-sonnet":               0.80,
		"gpt-4.5":                       0.85,
		"gpt-4o":                        0.82,
		"gpt-4o-mini":                   0.70,
		"gemini-2.5-pro":                0.90,
		"gemini-2.5-flash":              0.78,
		"gemini-2.0-flash":              0.72,
		"llama-4-maverick":              0.80,
		"llama-4-scout":                 0.75,
		"deepseek-v3":                   0.82,
		"deepseek-chat":                  0.75,
	}

	costPer1kProxy = map[string]float64{
		"claude-opus-4.6":               15.00,
		"claude-opus-4.6-1m":            15.00,
		"claude-sonnet-4.6":             3.00,
		"claude-haiku-4.5":              0.25,
		"gpt-5.3-codex-high":            10.00,
		"gpt-5.3-codex":                 5.00,
		"claude-4.5-opus-high-thinking": 15.00,
		"claude-4.5-opus-high":          15.00,
		"claude-4.5-sonnet-thinking":    3.00,
		"claude-4-sonnet":               3.00,
		"gpt-4.5":                       5.00,
		"gpt-4o":                        2.50,
		"gpt-4o-mini":                   0.15,
		"gemini-2.5-pro":                1.50,
		"gemini-2.5-flash":              0.10,
		"gemini-2.0-flash":              0.05,
		"llama-4-maverick":              0.40,
		"llama-4-scout":                  0.20,
		"deepseek-v3":                   0.60,
		"deepseek-chat":                  0.30,
	}

	latencyMsProxy = map[string]int{
		"claude-opus-4.6":               2500,
		"claude-sonnet-4.6":             1500,
		"claude-haiku-4.5":              800,
		"gpt-5.3-codex-high":            2000,
		"gpt-4o":                        1800,
		"gemini-2.5-pro":                1200,
		"gemini-2.5-flash":              500,
		"deepseek-v3":                   1500,
	}
)

// UnifiedBenchmarkStore combines dynamic tokenledger data with hardcoded fallbacks
type UnifiedBenchmarkStore struct {
	primary   *Client
	fallback  *FallbackProvider
	mu        sync.RWMutex
}

// FallbackProvider provides hardcoded benchmark values
type FallbackProvider struct {
	QualityProxy   map[string]float64
	CostPer1kProxy map[string]float64
	LatencyMsProxy map[string]int
}

// NewFallbackOnlyStore creates a store with fallback only (no tokenledger)
func NewFallbackOnlyStore() *UnifiedBenchmarkStore {
	return &UnifiedBenchmarkStore{
		fallback: &FallbackProvider{
			QualityProxy:   qualityProxy,
			CostPer1kProxy: costPer1kProxy,
			LatencyMsProxy: latencyMsProxy,
		},
	}
}

// NewUnifiedBenchmarkStore creates a store with tokenledger and fallback
func NewUnifiedBenchmarkStore(tokenledgerURL string, cacheTTL time.Duration) *UnifiedBenchmarkStore {
	return &UnifiedBenchmarkStore{
		primary: NewClient(tokenledgerURL, cacheTTL),
		fallback: &FallbackProvider{
			QualityProxy:   qualityProxy,
			CostPer1kProxy: costPer1kProxy,
			LatencyMsProxy: latencyMsProxy,
		},
	}
}

// GetQuality returns quality score for a model (0.0-1.0)
func (s *UnifiedBenchmarkStore) GetQuality(modelID string) float64 {
	// Try dynamic source first
	if s.primary != nil {
		if data, err := s.primary.GetBenchmark(modelID); err == nil && data != nil && data.IntelligenceIndex != nil {
			return *data.IntelligenceIndex / 100.0
		}
	}

	// Fallback to hardcoded
	s.mu.RLock()
	defer s.mu.RUnlock()
	if q, ok := s.fallback.QualityProxy[modelID]; ok {
		return q
	}
	return 0.5 // Default
}

// GetCost returns cost per 1k tokens for a model (USD)
func (s *UnifiedBenchmarkStore) GetCost(modelID string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c, ok := s.fallback.CostPer1kProxy[modelID]; ok {
		return c
	}
	return 1.0 // Default
}

// GetLatency returns latency in ms for a model
func (s *UnifiedBenchmarkStore) GetLatency(modelID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if l, ok := s.fallback.LatencyMsProxy[modelID]; ok {
		return l
	}
	return 2000 // Default
}

// GetAllModels returns all known model IDs
func (s *UnifiedBenchmarkStore) GetAllModels() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	models := make([]string, 0, len(s.fallback.QualityProxy))
	for model := range s.fallback.QualityProxy {
		models = append(models, model)
	}
	return models
}
