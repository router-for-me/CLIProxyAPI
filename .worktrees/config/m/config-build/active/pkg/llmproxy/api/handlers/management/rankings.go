package management

import (
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// RankingCategory represents a category for model rankings
type RankingCategory string

const (
	// RankingByUsage ranks by token usage
	RankingByUsage RankingCategory = "usage"
	// RankingByQuality ranks by quality score
	RankingByQuality RankingCategory = "quality"
	// RankingBySpeed ranks by speed/latency
	RankingBySpeed RankingCategory = "speed"
	// RankingByCost ranks by cost efficiency
	RankingByCost RankingCategory = "cost"
	// RankingByPopularity ranks by popularity
	RankingByPopularity RankingCategory = "popularity"
)

// RankingsRequest is the JSON body for GET /v1/rankings
type RankingsRequest struct {
	// Category is the ranking category: usage, quality, speed, cost, popularity
	Category string `form:"category"`
	// Limit is the number of results to return
	Limit int `form:"limit"`
	// Provider filters to specific provider
	Provider string `form:"provider"`
	// TimeRange is the time range: week, month, all
	TimeRange string `form:"timeRange"`
}

// RankingsResponse is the JSON response for GET /v1/rankings
type RankingsResponse struct {
	Category   string        `json:"category"`
	TimeRange  string        `json:"timeRange"`
	UpdatedAt  time.Time     `json:"updated_at"`
	TotalCount int          `json:"total_count"`
	Rankings   []ModelRank  `json:"rankings"`
}

// ModelRank represents a model's ranking entry
type ModelRank struct {
	Rank              int     `json:"rank"`
	ModelID           string  `json:"model_id"`
	Provider          string  `json:"provider"`
	QualityScore      float64 `json:"quality_score"`
	EstimatedCost     float64 `json:"estimated_cost"`
	LatencyMs         int     `json:"latency_ms"`
	WeeklyTokens      int64   `json:"weekly_tokens"`
	MarketSharePercent float64 `json:"market_share_percent"`
	Category          string  `json:"category,omitempty"`
}

// RankingsHandler handles the /v1/rankings endpoint
type RankingsHandler struct {
	// This would connect to actual usage data in production
}

// NewRankingsHandler returns a new RankingsHandler
func NewRankingsHandler() *RankingsHandler {
	return &RankingsHandler{}
}

// GETRankings handles GET /v1/rankings
func (h *RankingsHandler) GETRankings(c *gin.Context) {
	var req RankingsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Set defaults
	if req.Category == "" {
		req.Category = string(RankingByUsage)
	}
	if req.Limit == 0 || req.Limit > 100 {
		req.Limit = 20
	}
	if req.TimeRange == "" {
		req.TimeRange = "week"
	}

	// Generate rankings based on category (in production, this would come from actual metrics)
	rankings := h.generateRankings(req)

	c.JSON(http.StatusOK, RankingsResponse{
		Category:   req.Category,
		TimeRange:  req.TimeRange,
		UpdatedAt:  time.Now(),
		TotalCount: len(rankings),
		Rankings:   rankings[:min(len(rankings), req.Limit)],
	})
}

// generateRankings generates mock rankings based on category
// In production, this would fetch from actual metrics storage
func (h *RankingsHandler) generateRankings(req RankingsRequest) []ModelRank {
	// This would be replaced with actual data from metrics storage
	mockModels := []struct {
		ModelID    string
		Provider   string
		Quality    float64
		Cost       float64
		Latency    int
		WeeklyTokens int64
	}{
		{"claude-opus-4.6", "anthropic", 0.95, 0.015, 4000, 651000000000},
		{"claude-sonnet-4.6", "anthropic", 0.88, 0.003, 2000, 520000000000},
		{"gemini-3.1-pro", "google", 0.90, 0.007, 3000, 100000000000},
		{"gemini-3-flash-preview", "google", 0.78, 0.00015, 600, 887000000000},
		{"gpt-5.2", "openai", 0.92, 0.020, 3500, 300000000000},
		{"deepseek-v3.2", "deepseek", 0.80, 0.0005, 1000, 762000000000},
		{"glm-5", "z-ai", 0.78, 0.001, 1500, 769000000000},
		{"minimax-m2.5", "minimax", 0.75, 0.001, 1200, 2290000000000},
		{"kimi-k2.5", "moonshotai", 0.82, 0.001, 1100, 967000000000},
		{"grok-4.1-fast", "x-ai", 0.76, 0.001, 800, 692000000000},
		{"gemini-2.5-flash", "google", 0.76, 0.0001, 500, 429000000000},
		{"claude-haiku-4.5", "anthropic", 0.75, 0.00025, 800, 100000000000},
	}

	// Filter by provider if specified
	var filtered []struct {
		ModelID    string
		Provider   string
		Quality    float64
		Cost       float64
		Latency    int
		WeeklyTokens int64
	}
	
	if req.Provider != "" {
		for _, m := range mockModels {
			if m.Provider == req.Provider {
				filtered = append(filtered, m)
			}
		}
	} else {
		filtered = mockModels
	}

	// Sort based on category
	switch RankingCategory(req.Category) {
	case RankingByUsage:
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].WeeklyTokens > filtered[j].WeeklyTokens
		})
	case RankingByQuality:
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Quality > filtered[j].Quality
		})
	case RankingBySpeed:
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Latency < filtered[j].Latency
		})
	case RankingByCost:
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Cost < filtered[j].Cost
		})
	default:
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].WeeklyTokens > filtered[j].WeeklyTokens
		})
	}

	// Calculate total tokens for market share
	var totalTokens int64
	for _, m := range filtered {
		totalTokens += m.WeeklyTokens
	}

	// Build rankings
	rankings := make([]ModelRank, len(filtered))
	for i, m := range filtered {
		marketShare := 0.0
		if totalTokens > 0 {
			marketShare = float64(m.WeeklyTokens) / float64(totalTokens) * 100
		}
		rankings[i] = ModelRank{
			Rank:               i + 1,
			ModelID:            m.ModelID,
			Provider:           m.Provider,
			QualityScore:       m.Quality,
			EstimatedCost:      m.Cost,
			LatencyMs:          m.Latency,
			WeeklyTokens:       m.WeeklyTokens,
			MarketSharePercent: marketShare,
		}
	}

	return rankings
}

// GETProviderRankings handles GET /v1/rankings/providers
func (h *RankingsHandler) GETProviderRankings(c *gin.Context) {
	// Mock provider rankings
	providerRankings := []gin.H{
		{"rank": 1, "provider": "google", "weekly_tokens": 730000000000, "market_share": 19.2, "model_count": 15},
		{"rank": 2, "provider": "anthropic", "weekly_tokens": 559000000000, "market_share": 14.7, "model_count": 8},
		{"rank": 3, "provider": "minimax", "weekly_tokens": 539000000000, "market_share": 14.2, "model_count": 5},
		{"rank": 4, "provider": "openai", "weekly_tokens": 351000000000, "market_share": 9.2, "model_count": 12},
		{"rank": 5, "provider": "z-ai", "weekly_tokens": 327000000000, "market_share": 8.6, "model_count": 6},
		{"rank": 6, "provider": "deepseek", "weekly_tokens": 304000000000, "market_share": 8.0, "model_count": 4},
		{"rank": 7, "provider": "x-ai", "weekly_tokens": 231000000000, "market_share": 6.1, "model_count": 3},
		{"rank": 8, "provider": "moonshotai", "weekly_tokens": 184000000000, "market_share": 4.8, "model_count": 3},
	}

	c.JSON(http.StatusOK, gin.H{
		"updated_at": time.Now(),
		"rankings":   providerRankings,
	})
}

// GETCategoryRankings handles GET /v1/rankings/categories
func (h *RankingsHandler) GETCategoryRankings(c *gin.Context) {
	// Mock category rankings
	categories := []gin.H{
		{
			"category": "coding",
			"top_model": "minimax-m2.5",
			"weekly_tokens": 216000000000,
			"percentage": 28.6,
		},
		{
			"category": "reasoning",
			"top_model": "claude-opus-4.6",
			"weekly_tokens": 150000000000,
			"percentage": 18.5,
		},
		{
			"category": "multimodal",
			"top_model": "gemini-3-flash-preview",
			"weekly_tokens": 120000000000,
			"percentage": 15.2,
		},
		{
			"category": "general",
			"top_model": "gpt-5.2",
			"weekly_tokens": 100000000000,
			"percentage": 12.8,
		},
	}

	c.JSON(http.StatusOK, gin.H{
		"updated_at": time.Now(),
		"categories": categories,
	})
}
