package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/registry"
)

// RoutingSelectRequest is the JSON body for POST /v1/routing/select.
type RoutingSelectRequest struct {
	TaskComplexity  string  `json:"taskComplexity"`
	MaxCostPerCall  float64 `json:"maxCostPerCall"`
	MaxLatencyMs    int     `json:"maxLatencyMs"`
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

// RoutingSelectHandler handles the /v1/routing/select endpoint.
type RoutingSelectHandler struct {
	router *registry.ParetoRouter
}

// NewRoutingSelectHandler returns a new RoutingSelectHandler.
func NewRoutingSelectHandler() *RoutingSelectHandler {
	return &RoutingSelectHandler{
		router: registry.NewParetoRouter(),
	}
}

// POSTRoutingSelect handles POST /v1/routing/select.
func (h *RoutingSelectHandler) POSTRoutingSelect(c *gin.Context) {
	var req RoutingSelectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	routingReq := &registry.RoutingRequest{
		TaskComplexity:  req.TaskComplexity,
		MaxCostPerCall:  req.MaxCostPerCall,
		MaxLatencyMs:    req.MaxLatencyMs,
		MinQualityScore: req.MinQualityScore,
	}

	selected, err := h.router.SelectModel(c.Request.Context(), routingReq)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, RoutingSelectResponse{
		ModelID:            selected.ModelID,
		Provider:           selected.Provider,
		EstimatedCost:      selected.EstimatedCost,
		EstimatedLatencyMs: selected.EstimatedLatencyMs,
		QualityScore:       selected.QualityScore,
	})
}
