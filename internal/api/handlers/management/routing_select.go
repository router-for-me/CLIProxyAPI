package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RoutingSelectRequest is the JSON body for POST /v1/routing/select.
type RoutingSelectRequest struct {
	TaskComplexity  string  `json:"taskComplexity"`
	MaxCostPerCall float64 `json:"maxCostPerCall"`
	MaxLatencyMs   int     `json:"maxLatencyMs"`
	MinQualityScore float64 `json:"minQualityScore"`
}

// RoutingSelectResponse is the JSON response for POST /v1/routing/select.
type RoutingSelectResponse struct {
	ModelID            string  `json:"model_id"`
	Provider           string  `json:"provider"`
	EstimatedCost      float64 `json:"estimated_cost"`
	EstimatedLatencyMs int     `json:"estimated_latency_ms"`
	QualityScore       float64 `json:"quality_score"`
}

// POSTRoutingSelect handles POST /v1/routing/select.
func (h *Handler) POSTRoutingSelect(c *gin.Context) {
	var req RoutingSelectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Simple routing logic based on complexity
	model, provider, cost, latency, quality := selectModel(req.TaskComplexity, req.MaxCostPerCall, req.MaxLatencyMs, req.MinQualityScore)

	c.JSON(http.StatusOK, RoutingSelectResponse{
		ModelID:            model,
		Provider:           provider,
		EstimatedCost:      cost,
		EstimatedLatencyMs: latency,
		QualityScore:       quality,
	})
}

// selectModel returns a model based on complexity and constraints
func selectModel(complexity string, maxCost float64, maxLatency int, minQuality float64) (string, string, float64, int, float64) {
	// Default fallback
	defaultModel := "gemini-3-flash"
	defaultProvider := "gemini"
	defaultCost := 0.0001
	defaultLatency := 1000
	defaultQuality := 0.78

	complexity = toUpperSafe(complexity)

	switch complexity {
	case "FAST":
		// minimax-m2.5 - fastest, cheapest
		return "minimax-m2.5", "minimax", 0.00007, 300, 0.72
	case "NORMAL":
		// gemini-3-flash - balanced
		return "gemini-3-flash", "gemini", 0.0001, 800, 0.78
	case "COMPLEX":
		// claude-sonnet-4.6 - high quality
		return "claude-sonnet-4.6", "claude", 0.003, 2000, 0.88
	case "HIGH_COMPLEX":
		// gpt-5.3-codex-xhigh - highest quality for complex tasks
		return "gpt-5.3-codex-xhigh", "openai", 0.015, 4000, 0.95
	}

	return defaultModel, defaultProvider, defaultCost, defaultLatency, defaultQuality
}

func toUpperSafe(s string) string {
	if s == "" {
		return ""
	}
	// Simple uppercase without unicode issues
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		result[i] = c
	}
	return string(result)
}
