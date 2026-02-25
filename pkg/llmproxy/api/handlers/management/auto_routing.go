package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/registry"
)

// AutoRoutingRequest is the JSON body for POST /v1/routing/auto.
type AutoRoutingRequest struct {
	// Mode is the auto-routing mode: quality, speed, cheapest, balanced, free
	Mode string `json:"mode" binding:"required"`
	// TaskComplexity is one of: FAST, NORMAL, COMPLEX, HIGH_COMPLEX
	TaskComplexity string `json:"taskComplexity"`
	// MaxCostPerCall is the hard cost cap in USD
	MaxCostPerCall float64 `json:"maxCostPerCall"`
	// MaxLatencyMs is the hard latency cap in milliseconds
	MaxLatencyMs int `json:"maxLatencyMs"`
	// MinQualityScore is the minimum acceptable quality in [0,1]
	MinQualityScore float64 `json:"minQualityScore"`
	// RequireReasoning enables models with thinking/reasoning support
	RequireReasoning bool `json:"requireReasoning"`
	// RequireToolCall enables models that support tool calling
	RequireToolCall bool `json:"requireToolCall"`
	// RequireMultimodal enables models that support image/audio input
	RequireMultimodal bool `json:"requireMultimodal"`
	// RequireStructuredOutput enables models that support JSON schema output
	RequireStructuredOutput bool `json:"requireStructuredOutput"`
	// PreferredProvider is the preferred provider (if available)
	PreferredProvider string `json:"preferredProvider"`
	// ExcludedProviders is a list of providers to exclude
	ExcludedProviders []string `json:"excludedProviders"`
	// MinContextLength is the minimum context window required
	MinContextLength int `json:"minContextLength"`
	// Category filters by use case (coding, reasoning, multimodal, general)
	Category string `json:"category"`
}

// AutoRoutingResponse is the JSON response for POST /v1/routing/auto.
type AutoRoutingResponse struct {
	ModelID            string  `json:"model_id"`
	Provider           string  `json:"provider"`
	EstimatedCost      float64 `json:"estimated_cost"`
	EstimatedLatencyMs int     `json:"estimated_latency_ms"`
	QualityScore       float64 `json:"quality_score"`
	ModeUsed           string  `json:"mode_used"`
	Alternatives       []AlternativeModel `json:"alternatives,omitempty"`
	Reason            string  `json:"reason"`
}

// AlternativeModel provides alternative model options
type AlternativeModel struct {
	ModelID            string  `json:"model_id"`
	Provider           string  `json:"provider"`
	EstimatedCost      float64 `json:"estimated_cost"`
	EstimatedLatencyMs int     `json:"estimated_latency_ms"`
	QualityScore       float64 `json:"quality_score"`
}

// AutoRoutingHandler handles the /v1/routing/auto endpoint.
type AutoRoutingHandler struct {
	router *registry.AutoRouter
}

// NewAutoRoutingHandler returns a new AutoRoutingHandler.
func NewAutoRoutingHandler() *AutoRoutingHandler {
	return &AutoRoutingHandler{
		router: registry.NewAutoRouter(),
	}
}

// POSTAutoRouting handles POST /v1/routing/auto.
func (h *AutoRoutingHandler) POSTAutoRouting(c *gin.Context) {
	var req AutoRoutingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Convert string mode to AutoRouterMode
	mode := registry.AutoRouterMode(req.Mode)
	switch mode {
	case registry.AutoModeQuality, registry.AutoModeSpeed, registry.AutoModeCheapest, 
		 registry.AutoModeBalanced, registry.AutoModeFree:
		// Valid modes
	default:
		mode = registry.AutoModeBalanced
	}

	autoReq := &registry.AutoRoutingRequest{
		Mode:                  mode,
		TaskComplexity:        req.TaskComplexity,
		MaxCostPerCall:        req.MaxCostPerCall,
		MaxLatencyMs:          req.MaxLatencyMs,
		MinQualityScore:       req.MinQualityScore,
		RequireReasoning:      req.RequireReasoning,
		RequireToolCall:       req.RequireToolCall,
		RequireMultimodal:    req.RequireMultimodal,
		RequireStructuredOutput: req.RequireStructuredOutput,
		PreferredProvider:      req.PreferredProvider,
		ExcludedProviders:     req.ExcludedProviders,
		MinContextLength:      req.MinContextLength,
		Category:             req.Category,
	}

	result, err := h.router.SelectModel(c.Request.Context(), autoReq)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Convert alternatives
	var alternatives []AlternativeModel
	if len(result.Alternatives) > 0 {
		for _, alt := range result.Alternatives {
			alternatives = append(alternatives, AlternativeModel{
				ModelID:            alt.ModelID,
				Provider:           alt.Provider,
				EstimatedCost:      alt.EstimatedCost,
				EstimatedLatencyMs: alt.EstimatedLatencyMs,
				QualityScore:       alt.QualityScore,
			})
		}
	}

	c.JSON(http.StatusOK, AutoRoutingResponse{
		ModelID:            result.ModelID,
		Provider:           result.Provider,
		EstimatedCost:      result.EstimatedCost,
		EstimatedLatencyMs: result.EstimatedLatencyMs,
		QualityScore:       result.QualityScore,
		ModeUsed:           result.ModeUsed,
		Alternatives:       alternatives,
		Reason:            result.Reason,
	})
}

// GETAutoModes returns available auto-routing modes.
func (h *AutoRoutingHandler) GETAutoModes(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"modes": []gin.H{
			{"name": "quality", "description": "Select the highest quality model within constraints"},
			{"name": "speed", "description": "Select the fastest model within constraints"},
			{"name": "cheapest", "description": "Select the cheapest model within constraints"},
			{"name": "balanced", "description": "Select the best quality/cost ratio (default)"},
			{"name": "free", "description": "Select only free (zero-cost) models"},
		},
	})
}
