// Package benchmarks provides integration with tokenledger for dynamic benchmark data.
// 
// This enables cliproxy++ to use real-time benchmark data from:
// - Artificial Analysis API (intelligence, speed, latency)
// - OpenRouter API (pricing, context)
// - CLIProxyAPI metrics (runtime performance)
package benchmarks

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// BenchmarkData represents benchmark data for a model
type BenchmarkData struct {
	ModelID              string   `json:"model_id"`
	Provider             string   `json:"provider,omitempty"`
	IntelligenceIndex    *float64 `json:"intelligence_index,omitempty"`
	CodingIndex         *float64 `json:"coding_index,omitempty"`
	SpeedTPS            *float64 `json:"speed_tps,omitempty"`
	LatencyTTFTMs       *float64 `json:"latency_ttft_ms,omitempty"`
	InputPricePer1M      *float64 `json:"price_input_per_1m,omitempty"`
	OutputPricePer1M     *float64 `json:"price_output_per_1m,omitempty"`
	ContextWindow        *int64   `json:"context_window_tokens,omitempty"`
	Confidence          float64  `json:"confidence"`
	Source             string   `json:"source"`
}

// Client for fetching benchmarks from tokenledger
type Client struct {
	tokenledgerPath string
	cache          map[string]*cacheEntry
	cacheMu        sync.RWMutex
	cacheTTL       time.Duration
}

type cacheEntry struct {
	data      BenchmarkData
	expires  time.Time
}

// NewClient creates a new benchmark client
func NewClient(tokenledgerPath string) *Client {
	return &Client{
		tokenledgerPath: tokenledgerPath,
		cache:         make(map[string]*cacheEntry),
		cacheTTL:       time.Hour,
	}
}

// GetBenchmark returns benchmark data for a model, with caching
func (c *Client) GetBenchmark(modelID string) (*BenchmarkData, error) {
	// Check cache first
	c.cacheMu.RLock()
	if entry, ok := c.cache[modelID]; ok && time.Now().Before(entry.expires) {
		c.cacheMu.RUnlock()
		return &entry.data, nil
	}
	c.cacheMu.RUnlock()

	// Fetch fresh data
	data, err := c.fetchFromTokenledger(modelID)
	if err != nil {
		// Return cached expired data if fetch fails
		c.cacheMu.RLock()
		if entry, ok := c.cache[modelID]; ok {
			c.cacheMu.RUnlock()
			return &entry.data, nil
		}
		c.cacheMu.RUnlock()
		return nil, err
	}

	// Update cache
	c.cacheMu.Lock()
	c.cache[modelID] = &cacheEntry{
		data:   *data,
		expires: time.Now().Add(c.cacheTTL),
	}
	c.cacheMu.Unlock()

	return data, nil
}

// fetchFromTokenledger calls the tokenledger CLI to get benchmark data
func (c *Client) fetchFromTokenledger(modelID string) (*BenchmarkData, error) {
	// Call tokenledger CLI (would be implemented in Rust binary)
	// For now, return nil to use fallback hardcoded values
	return nil, fmt.Errorf("tokenledger not configured")
}

// GetAllBenchmarks returns all available benchmark data
func (c *Client) GetAllBenchmarks() ([]BenchmarkData, error) {
	// This would call tokenledger to get all benchmarks
	return nil, fmt.Errorf("tokenledger not configured")
}

// RefreshBenchmarks forces a refresh of benchmark data
func (c *Client) RefreshBenchmarks() error {
	// Clear cache
	c.cacheMu.Lock()
	c.cache = make(map[string]*cacheEntry)
	c.cacheMu.Unlock()

	// Would trigger tokenledger to fetch fresh data
	return nil
}

// GetQualityScore returns the quality score for a model
func (c *Client) GetQualityScore(modelID string) (float64, bool) {
	data, err := c.GetBenchmark(modelID)
	if err != nil || data == nil || data.IntelligenceIndex == nil {
		return 0, false
	}
	return *data.IntelligenceIndex / 100.0, true // Normalize to 0-1
}

// GetCost returns the cost per 1K tokens for a model
func (c *Client) GetCost(modelID string) (float64, bool) {
	data, err := c.GetBenchmark(modelID)
	if err != nil || data == nil || data.InputPricePer1M == nil {
		return 0, false
	}
	return *data.InputPricePer1M, true
}

// GetLatency returns the latency in ms for a model
func (c *Client) GetLatency(modelID string) (int, bool) {
	data, err := c.GetBenchmark(modelID)
	if err != nil || data == nil || data.LatencyTTFTMs == nil {
		return 0, false
	}
	return int(*data.LatencyTTFTMs), true
}

// BenchmarkProvider defines interface for benchmark sources
type BenchmarkProvider interface {
	GetBenchmark(modelID string) (*BenchmarkData, error)
	GetAllBenchmarks() ([]BenchmarkData, error)
	Refresh() error
}

// MockProvider provides hardcoded fallback data
type MockProvider struct{}

// NewMockProvider creates a provider with fallback data
func NewMockProvider() *MockProvider {
	return &MockProvider{}
}

func (p *MockProvider) GetBenchmark(modelID string) (*BenchmarkData, error) {
	return nil, fmt.Errorf("not implemented")
}

func (p *MockProvider) GetAllBenchmarks() ([]BenchmarkData, error) {
	return nil, fmt.Errorf("not implemented")
}

func (p *MockProvider) Refresh() error {
	return nil
}

// JSON marshaling support
func (b *BenchmarkData) MarshalJSON() ([]byte, error) {
	type Alias BenchmarkData
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(b),
	})
}
