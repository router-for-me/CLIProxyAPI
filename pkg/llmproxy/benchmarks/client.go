// Package benchmarks provides integration with tokenledger for dynamic benchmark data.
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
	CodingIndex          *float64 `json:"coding_index,omitempty"`
	SpeedTPS             *float64 `json:"speed_tps,omitempty"`
	LatencyMs            *float64 `json:"latency_ms,omitempty"`
	PricePer1MInput      *float64 `json:"price_per_1m_input,omitempty"`
	PricePer1MOutput     *float64 `json:"price_per_1m_output,omitempty"`
	ContextWindow        *int64   `json:"context_window,omitempty"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Client fetches benchmarks from tokenledger
type Client struct {
	tokenledgerURL string
	cacheTTL      time.Duration
	cache         map[string]BenchmarkData
	mu            sync.RWMutex
}

// NewClient creates a new tokenledger benchmark client
func NewClient(tokenledgerURL string, cacheTTL time.Duration) *Client {
	return &Client{
		tokenledgerURL: tokenledgerURL,
		cacheTTL:      cacheTTL,
		cache:         make(map[string]BenchmarkData),
	}
}

// GetBenchmark returns benchmark data for a model
func (c *Client) GetBenchmark(modelID string) (*BenchmarkData, error) {
	c.mu.RLock()
	if data, ok := c.cache[modelID]; ok {
		if time.Since(data.UpdatedAt) < c.cacheTTL {
			c.mu.RUnlock()
			return &data, nil
		}
	}
	c.mu.RUnlock()

	// TODO: Call tokenledger HTTP API
	// For now, return nil to use fallback
	return nil, nil
}

// String returns a string representation
func (b *BenchmarkData) String() string {
	return fmt.Sprintf("BenchmarkData{ModelID:%s, Provider:%s}", b.ModelID, b.Provider)
}

// MarshalJSON implements custom JSON marshaling
func (b BenchmarkData) MarshalJSON() ([]byte, error) {
	type Alias BenchmarkData
	return json.Marshal(&struct {
		Alias
	}{
		Alias: Alias(b),
	})
}
