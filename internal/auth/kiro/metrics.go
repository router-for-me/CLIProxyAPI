package kiro

import (
	"math"
	"sync"
	"time"
)

// TokenMetrics holds performance metrics for a single token.
type TokenMetrics struct {
	SuccessRate    float64   // Success rate (0.0 - 1.0)
	AvgLatency     float64   // Average latency in milliseconds
	QuotaRemaining float64   // Remaining quota (0.0 - 1.0)
	LastUsed       time.Time // Last usage timestamp
	FailCount      int       // Consecutive failure count
	TotalRequests  int       // Total request count
	successCount   int       // Internal: successful request count
	totalLatency   float64   // Internal: cumulative latency
}

// TokenScorer manages token metrics and scoring.
type TokenScorer struct {
	mu      sync.RWMutex
	metrics map[string]*TokenMetrics

	// Scoring weights
	successRateWeight    float64
	quotaWeight          float64
	latencyWeight        float64
	lastUsedWeight       float64
	failPenaltyMultiplier float64
}

// NewTokenScorer creates a new TokenScorer with default weights.
func NewTokenScorer() *TokenScorer {
	return &TokenScorer{
		metrics:               make(map[string]*TokenMetrics),
		successRateWeight:     0.4,
		quotaWeight:           0.25,
		latencyWeight:         0.2,
		lastUsedWeight:        0.15,
		failPenaltyMultiplier: 0.1,
	}
}

// getOrCreateMetrics returns existing metrics or creates new ones.
func (s *TokenScorer) getOrCreateMetrics(tokenKey string) *TokenMetrics {
	if m, ok := s.metrics[tokenKey]; ok {
		return m
	}
	m := &TokenMetrics{
		SuccessRate:    1.0,
		QuotaRemaining: 1.0,
	}
	s.metrics[tokenKey] = m
	return m
}

// RecordRequest records the result of a request for a token.
func (s *TokenScorer) RecordRequest(tokenKey string, success bool, latency time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m := s.getOrCreateMetrics(tokenKey)
	m.TotalRequests++
	m.LastUsed = time.Now()
	m.totalLatency += float64(latency.Milliseconds())

	if success {
		m.successCount++
		m.FailCount = 0
	} else {
		m.FailCount++
	}

	// Update derived metrics
	if m.TotalRequests > 0 {
		m.SuccessRate = float64(m.successCount) / float64(m.TotalRequests)
		m.AvgLatency = m.totalLatency / float64(m.TotalRequests)
	}
}

// SetQuotaRemaining updates the remaining quota for a token.
func (s *TokenScorer) SetQuotaRemaining(tokenKey string, quota float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m := s.getOrCreateMetrics(tokenKey)
	m.QuotaRemaining = quota
}

// GetMetrics returns a copy of the metrics for a token.
func (s *TokenScorer) GetMetrics(tokenKey string) *TokenMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if m, ok := s.metrics[tokenKey]; ok {
		copy := *m
		return &copy
	}
	return nil
}

// CalculateScore computes the score for a token (higher is better).
func (s *TokenScorer) CalculateScore(tokenKey string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	m, ok := s.metrics[tokenKey]
	if !ok {
		return 1.0 // New tokens get a high initial score
	}

	// Success rate component (0-1)
	successScore := m.SuccessRate

	// Quota component (0-1)
	quotaScore := m.QuotaRemaining

	// Latency component (normalized, lower is better)
	// Using exponential decay: score = e^(-latency/1000)
	// 1000ms latency -> ~0.37 score, 100ms -> ~0.90 score
	latencyScore := math.Exp(-m.AvgLatency / 1000.0)
	if m.TotalRequests == 0 {
		latencyScore = 1.0
	}

	// Last used component (prefer tokens not recently used)
	// Score increases as time since last use increases
	timeSinceUse := time.Since(m.LastUsed).Seconds()
	// Normalize: 60 seconds -> ~0.63 score, 0 seconds -> 0 score
	lastUsedScore := 1.0 - math.Exp(-timeSinceUse/60.0)
	if m.LastUsed.IsZero() {
		lastUsedScore = 1.0
	}

	// Calculate weighted score
	score := s.successRateWeight*successScore +
		s.quotaWeight*quotaScore +
		s.latencyWeight*latencyScore +
		s.lastUsedWeight*lastUsedScore

	// Apply consecutive failure penalty
	if m.FailCount > 0 {
		penalty := s.failPenaltyMultiplier * float64(m.FailCount)
		score = score * math.Max(0, 1.0-penalty)
	}

	return score
}

// SelectBestToken selects the token with the highest score.
func (s *TokenScorer) SelectBestToken(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	if len(tokens) == 1 {
		return tokens[0]
	}

	bestToken := tokens[0]
	bestScore := s.CalculateScore(tokens[0])

	for _, token := range tokens[1:] {
		score := s.CalculateScore(token)
		if score > bestScore {
			bestScore = score
			bestToken = token
		}
	}

	return bestToken
}

// ResetMetrics clears all metrics for a token.
func (s *TokenScorer) ResetMetrics(tokenKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.metrics, tokenKey)
}

// ResetAllMetrics clears all stored metrics.
func (s *TokenScorer) ResetAllMetrics() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = make(map[string]*TokenMetrics)
}
