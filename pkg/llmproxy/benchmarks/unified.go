// Package benchmarks provides unified benchmark access with fallback to hardcoded values.
// This integrates with tokenledger for dynamic data while maintaining backward compatibility.
package benchmarks

import (
	"fmt"
	"sync"
)

// UnifiedBenchmarkStore combines dynamic tokenledger data with hardcoded fallbacks
type UnifiedBenchmarkStore struct {
	primary   BenchmarkProvider
	fallbacks *FallbackProvider
	mu        sync.RWMutex
}

// FallbackProvider provides hardcoded benchmark values
type FallbackProvider struct {
	// qualityProxy maps known model IDs to their quality scores in [0,1]
	QualityProxy map[string]float64
	// CostPer1kProxy maps model IDs to estimated cost per 1k tokens (USD)
	CostPer1kProxy map[string]float64
	// LatencyMsProxy maps model IDs to estimated p50 latency in milliseconds
	LatencyMsProxy map[string]int
}

// DefaultFallbackProvider returns the hardcoded maps from pareto_router.go
func DefaultFallbackProvider() *FallbackProvider {
	return &FallbackProvider{
		QualityProxy: map[string]float64{
			"claude-opus-4.6":               0.95,
			"claude-opus-4.6-1m":            0.96,
			"claude-sonnet-4.6":             0.88,
			"claude-haiku-4.5":              0.75,
			"gpt-5.3-codex-high":            0.92,
			"gpt-5.3-codex":                 0.82,
			"claude-4.5-opus-high-thinking": 0.94,
			"claude-4.5-opus-high":          0.92,
			"claude-4.5-sonnet-thinking":    0.85,
			"claude-4-sonnet":               0.80,
			"gpt-4o":                        0.85,
			"gpt-5.1-codex":                 0.80,
			"gemini-3-flash":                0.78,
			"gemini-3.1-pro":                0.90,
			"gemini-2.5-flash":              0.76,
			"gemini-2.0-flash":              0.72,
			"glm-5":                         0.78,
			"minimax-m2.5":                  0.75,
			"deepseek-v3.2":                 0.80,
			"composer-1.5":                  0.82,
			"composer-1":                    0.78,
			"roo-default":                   0.70,
			"kilo-default":                  0.70,
		},
		CostPer1kProxy: map[string]float64{
			"claude-opus-4.6":               0.015,
			"claude-opus-4.6-1m":            0.015,
			"claude-sonnet-4.6":             0.003,
			"claude-haiku-4.5":              0.00025,
			"gpt-5.3-codex-high":            0.020,
			"gpt-5.3-codex":                 0.010,
			"claude-4.5-opus-high-thinking": 0.025,
			"claude-4.5-opus-high":          0.015,
			"claude-4.5-sonnet-thinking":    0.005,
			"claude-4-sonnet":               0.003,
			"gpt-4o":                        0.005,
			"gpt-5.1-codex":                 0.008,
			"gemini-3-flash":                0.00015,
			"gemini-3.1-pro":                0.007,
			"gemini-2.5-flash":              0.0001,
			"gemini-2.0-flash":              0.0001,
			"glm-5":                         0.001,
			"minimax-m2.5":                  0.001,
			"deepseek-v3.2":                 0.0005,
			"composer-1.5":                  0.002,
			"composer-1":                    0.001,
			"roo-default":                   0.0,
			"kilo-default":                  0.0,
		},
		LatencyMsProxy: map[string]int{
			"claude-opus-4.6":               4000,
			"claude-opus-4.6-1m":            5000,
			"claude-sonnet-4.6":             2000,
			"claude-haiku-4.5":              800,
			"gpt-5.3-codex-high":            6000,
			"gpt-5.3-codex":                 3000,
			"claude-4.5-opus-high-thinking": 8000,
			"claude-4.5-opus-high":          5000,
			"claude-4.5-sonnet-thinking":    4000,
			"claude-4-sonnet":               2500,
			"gpt-4o":                        2000,
			"gpt-5.1-codex":                 3000,
			"gemini-3-flash":                600,
			"gemini-3.1-pro":                3000,
			"gemini-2.5-flash":              500,
			"gemini-2.0-flash":              400,
			"glm-5":                         1500,
			"minimax-m2.5":                  1200,
			"deepseek-v3.2":                 1000,
			"composer-1.5":                  2000,
			"composer-1":                    1500,
			"roo-default":                   1000,
			"kilo-default":                  1000,
		},
	}
}

// NewUnifiedStore creates a store with primary and fallback providers
func NewUnifiedStore(primary BenchmarkProvider) *UnifiedBenchmarkStore {
	return &UnifiedBenchmarkStore{
		primary:   primary,
		fallbacks: DefaultFallbackProvider(),
	}
}

// NewFallbackOnlyStore creates a store with only hardcoded fallbacks
func NewFallbackOnlyStore() *UnifiedBenchmarkStore {
	return &UnifiedBenchmarkStore{
		primary:   nil,
		fallbacks: DefaultFallbackProvider(),
	}
}

// GetQuality returns quality score, trying primary first then fallback
func (s *UnifiedBenchmarkStore) GetQuality(modelID string) (float64, bool) {
	// Try primary (tokenledger) first
	if s.primary != nil {
		if data, err := s.primary.GetBenchmark(modelID); err == nil && data != nil && data.IntelligenceIndex != nil {
			return *data.IntelligenceIndex / 100.0, true
		}
	}
	
	// Fallback to hardcoded
	if q, ok := s.fallbacks.QualityProxy[modelID]; ok {
		return q, true
	}
	return 0, false
}

// GetCost returns cost per 1K tokens, trying primary then fallback
func (s *UnifiedBenchmarkStore) GetCost(modelID string) (float64, bool) {
	if s.primary != nil {
		if data, err := s.primary.GetBenchmark(modelID); err == nil && data != nil && data.InputPricePer1M != nil {
			return *data.InputPricePer1M, true
		}
	}
	
	if c, ok := s.fallbacks.CostPer1kProxy[modelID]; ok {
		return c, true
	}
	return 0, false
}

// GetLatency returns latency in ms, trying primary then fallback
func (s *UnifiedBenchmarkStore) GetLatency(modelID string) (int, bool) {
	if s.primary != nil {
		if data, err := s.primary.GetBenchmark(modelID); err == nil && data != nil && data.LatencyTTFTMs != nil {
			return int(*data.LatencyTTFTMs), true
		}
	}
	
	if l, ok := s.fallbacks.LatencyMsProxy[modelID]; ok {
		return l, true
	}
	return 0, false
}

// GetAll returns all benchmark data from primary
func (s *UnifiedBenchmarkStore) GetAll() ([]BenchmarkData, error) {
	if s.primary == nil {
		return nil, fmt.Errorf("no primary provider configured")
	}
	return s.primary.GetAllBenchmarks()
}

// Refresh triggers a refresh of benchmark data
func (s *UnifiedBenchmarkStore) Refresh() error {
	if s.primary != nil {
		return s.primary.Refresh()
	}
	return nil
}
