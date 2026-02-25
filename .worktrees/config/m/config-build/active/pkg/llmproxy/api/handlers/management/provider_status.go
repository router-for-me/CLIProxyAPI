package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// ProviderStatusRequest is the request for provider status
type ProviderStatusRequest struct {
	Provider string `uri:"provider" binding:"required"`
}

// ProviderStatusResponse is the JSON response for provider status
type ProviderStatusResponse struct {
	Provider      string           `json:"provider"`
	UpdatedAt     time.Time        `json:"updated_at"`
	Status        string           `json:"status"` // operational, degraded, outage
	Uptime24h     float64         `json:"uptime_24h"`
	Uptime7d      float64         `json:"uptime_7d"`
	AvgLatencyMs  float64         `json:"avg_latency_ms"`
	TotalRequests int64           `json:"total_requests"`
	ErrorRate     float64          `json:"error_rate"`
	Regions       []RegionStatus   `json:"regions"`
	Models        []ProviderModel  `json:"models"`
	Incidents     []Incident       `json:"incidents,omitempty"`
}

// RegionStatus contains status for a specific region
type RegionStatus struct {
	Region        string  `json:"region"`
	Status        string  `json:"status"` // operational, degraded, outage
	LatencyMs     float64 `json:"latency_ms"`
	ThroughputTPS float64 `json:"throughput_tps"`
	UptimePercent float64 `json:"uptime_percent"`
}

// ProviderModel contains model availability for a provider
type ProviderModel struct {
	ModelID         string  `json:"model_id"`
	Available       bool    `json:"available"`
	LatencyMs       int     `json:"latency_ms"`
	ThroughputTPS   float64 `json:"throughput_tps"`
	QueueDepth      int     `json:"queue_depth,omitempty"`
	MaxConcurrency int     `json:"max_concurrency,omitempty"`
}

// Incident represents an ongoing or past incident
type Incident struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"` // outage, degradation, maintenance
	Severity    string     `json:"severity"` // critical, major, minor
	Status      string     `json:"status"` // ongoing, resolved
	Title       string     `json:"title"`
	Description string     `json:"description"`
	StartedAt   time.Time `json:"started_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	Affected    []string  `json:"affected,omitempty"`
}

// ProviderStatusHandler handles provider status endpoints
type ProviderStatusHandler struct{}

// NewProviderStatusHandler returns a new ProviderStatusHandler
func NewProviderStatusHandler() *ProviderStatusHandler {
	return &ProviderStatusHandler{}
}

// GETProviderStatus handles GET /v1/providers/:provider/status
func (h *ProviderStatusHandler) GETProviderStatus(c *gin.Context) {
	var req ProviderStatusRequest
	if err := c.ShouldBindUri(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	status := h.getMockProviderStatus(req.Provider)
	if status == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}

	c.JSON(http.StatusOK, status)
}

// getMockProviderStatus returns mock provider status
func (h *ProviderStatusHandler) getMockProviderStatus(provider string) *ProviderStatusResponse {
	providerData := map[string]*ProviderStatusResponse{
		"google": {
			Provider:      "google",
			UpdatedAt:     time.Now(),
			Status:        "operational",
			Uptime24h:     99.2,
			Uptime7d:      98.9,
			AvgLatencyMs:  1250,
			TotalRequests:  50000000,
			ErrorRate:      0.8,
			Regions: []RegionStatus{
				{Region: "US", Status: "operational", LatencyMs: 800, ThroughputTPS: 150, UptimePercent: 99.5},
				{Region: "EU", Status: "operational", LatencyMs: 1500, ThroughputTPS: 100, UptimePercent: 99.1},
				{Region: "ASIA", Status: "degraded", LatencyMs: 2200, ThroughputTPS: 60, UptimePercent: 96.5},
			},
			Models: []ProviderModel{
				{ModelID: "gemini-3.1-pro", Available: true, LatencyMs: 3000, ThroughputTPS: 72, MaxConcurrency: 50},
				{ModelID: "gemini-3-flash-preview", Available: true, LatencyMs: 600, ThroughputTPS: 200, MaxConcurrency: 100},
				{ModelID: "gemini-2.5-flash", Available: true, LatencyMs: 500, ThroughputTPS: 250, MaxConcurrency: 100},
			},
		},
		"anthropic": {
			Provider:      "anthropic",
			UpdatedAt:     time.Now(),
			Status:        "operational",
			Uptime24h:     99.5,
			Uptime7d:      99.3,
			AvgLatencyMs:  1800,
			TotalRequests: 35000000,
			ErrorRate:      0.5,
			Regions: []RegionStatus{
				{Region: "US", Status: "operational", LatencyMs: 1500, ThroughputTPS: 80, UptimePercent: 99.5},
				{Region: "EU", Status: "operational", LatencyMs: 2200, ThroughputTPS: 50, UptimePercent: 99.2},
			},
			Models: []ProviderModel{
				{ModelID: "claude-opus-4.6", Available: true, LatencyMs: 4000, ThroughputTPS: 45, MaxConcurrency: 30},
				{ModelID: "claude-sonnet-4.6", Available: true, LatencyMs: 2000, ThroughputTPS: 80, MaxConcurrency: 50},
				{ModelID: "claude-haiku-4.5", Available: true, LatencyMs: 800, ThroughputTPS: 150, MaxConcurrency: 100},
			},
		},
		"openai": {
			Provider:      "openai",
			UpdatedAt:     time.Now(),
			Status:        "operational",
			Uptime24h:     98.8,
			Uptime7d:      98.5,
			AvgLatencyMs:  2000,
			TotalRequests:  42000000,
			ErrorRate:      1.2,
			Regions: []RegionStatus{
				{Region: "US", Status: "operational", LatencyMs: 1800, ThroughputTPS: 100, UptimePercent: 99.0},
				{Region: "EU", Status: "degraded", LatencyMs: 2800, ThroughputTPS: 60, UptimePercent: 97.0},
			},
			Models: []ProviderModel{
				{ModelID: "gpt-5.2", Available: true, LatencyMs: 3500, ThroughputTPS: 60, MaxConcurrency: 40},
				{ModelID: "gpt-4o", Available: true, LatencyMs: 2000, ThroughputTPS: 80, MaxConcurrency: 50},
			},
		},
	}

	if data, ok := providerData[provider]; ok {
		return data
	}

	return nil
}

// GETAllProviderStatuses handles GET /v1/providers/status
func (h *ProviderStatusHandler) GETAllProviderStatuses(c *gin.Context) {
	providers := []string{"google", "anthropic", "openai", "deepseek", "minimax", "moonshotai", "x-ai", "z-ai"}
	
	var statuses []ProviderStatusResponse
	for _, p := range providers {
		if status := h.getMockProviderStatus(p); status != nil {
			statuses = append(statuses, *status)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"updated_at": time.Now(),
		"count":      len(statuses),
		"providers":  statuses,
	})
}

// GETProviderIncidents handles GET /v1/providers/:provider/incidents
func (h *ProviderStatusHandler) GETProviderIncidents(c *gin.Context) {
	var req ProviderStatusRequest
	if err := c.ShouldBindUri(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Mock incidents
	incidents := []Incident{
		{
			ID:          "inc-001",
			Type:        "degradation",
			Severity:    "minor",
			Status:      "resolved",
			Title:       "Elevated latency in Asia Pacific region",
			Description: "Users in APAC experienced elevated latency due to network congestion",
			StartedAt:   time.Now().Add(-24 * time.Hour),
			ResolvedAt:  timePtr(time.Now().Add(-12 * time.Hour)),
			Affected:    []string{"gemini-2.5-flash", "gemini-3-flash-preview"},
		},
	}

	c.JSON(http.StatusOK, gin.H{
		"provider":  req.Provider,
		"incidents": incidents,
	})
}

func timePtr(t time.Time) *time.Time {
	return &t
}
