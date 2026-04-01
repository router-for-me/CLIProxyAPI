package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

type circuitBreakerTarget struct {
	ClientID string `json:"clientId" binding:"required"`
	ModelID  string `json:"modelId" binding:"required"`
}

func (h *Handler) GetCircuitBreaker(c *gin.Context) {
	status := registry.GetGlobalRegistry().GetCircuitBreakerStatus()
	c.JSON(200, status)
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
