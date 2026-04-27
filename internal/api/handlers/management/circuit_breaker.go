package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

type circuitBreakerTarget struct {
	ClientID string `json:"clientId" binding:"required"`
	ModelID  string `json:"modelId" binding:"required"`
}

type circuitBreakerErrorInsightFiltersResponse struct {
	Provider string `json:"provider"`
	AuthID   string `json:"authId"`
	Model    string `json:"model"`
}

type circuitBreakerStatusResponse struct {
	Provider            string                                    `json:"provider,omitempty"`
	State               registry.CircuitBreakerState              `json:"state"`
	FailureCount        int                                       `json:"failureCount"`
	ConsecutiveFailures int                                       `json:"consecutiveFailures"`
	OpenCycles          int                                       `json:"openCycles"`
	LastFailure         time.Time                                 `json:"lastFailure"`
	RecoveryAt          time.Time                                 `json:"recoveryAt,omitempty"`
	ErrorInsightFilters circuitBreakerErrorInsightFiltersResponse `json:"errorInsightFilters"`
}

func (h *Handler) GetCircuitBreaker(c *gin.Context) {
	status := registry.GetGlobalRegistry().GetCircuitBreakerStatus()
	response := make(map[string]map[string]circuitBreakerStatusResponse, len(status))
	for clientID, models := range status {
		clientResponse := make(map[string]circuitBreakerStatusResponse, len(models))
		for modelID, breakerStatus := range models {
			clientResponse[modelID] = circuitBreakerStatusResponse{
				Provider:            breakerStatus.Provider,
				State:               breakerStatus.State,
				FailureCount:        breakerStatus.FailureCount,
				ConsecutiveFailures: breakerStatus.ConsecutiveFailures,
				OpenCycles:          breakerStatus.OpenCycles,
				LastFailure:         breakerStatus.LastFailure,
				RecoveryAt:          breakerStatus.RecoveryAt,
				ErrorInsightFilters: circuitBreakerErrorInsightFiltersResponse{
					Provider: strings.ToLower(strings.TrimSpace(breakerStatus.Provider)),
					AuthID:   strings.TrimSpace(clientID),
					Model:    strings.TrimSpace(modelID),
				},
			}
		}
		response[clientID] = clientResponse
	}
	c.JSON(200, response)
}

func (h *Handler) DeleteCircuitBreaker(c *gin.Context) {
	var target circuitBreakerTarget
	if err := c.ShouldBindJSON(&target); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: clientId and modelId are required"})
		return
	}
	clientID := strings.TrimSpace(target.ClientID)
	modelID := strings.TrimSpace(target.ModelID)
	if clientID == "" || modelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clientId and modelId cannot be empty"})
		return
	}
	registry.GetGlobalRegistry().ResetCircuitBreaker(clientID, modelID)
	c.JSON(200, gin.H{"message": "circuit breaker reset", "clientId": clientID, "modelId": modelID})
}

func (h *Handler) PutCircuitBreaker(c *gin.Context) {
	var target circuitBreakerTarget
	if err := c.ShouldBindJSON(&target); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: clientId and modelId are required"})
		return
	}
	clientID := strings.TrimSpace(target.ClientID)
	modelID := strings.TrimSpace(target.ModelID)
	if clientID == "" || modelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clientId and modelId cannot be empty"})
		return
	}
	registry.GetGlobalRegistry().ForceOpenCircuitBreaker(clientID, modelID)
	c.JSON(200, gin.H{"message": "circuit breaker manually opened", "clientId": clientID, "modelId": modelID})
}
