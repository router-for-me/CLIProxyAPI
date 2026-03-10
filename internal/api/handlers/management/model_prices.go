package management

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func (h *Handler) GetModelPrices(c *gin.Context) {
	prices := h.cfg.ModelPrices
	if prices == nil {
		prices = map[string]config.ModelPrice{}
	}
	c.JSON(http.StatusOK, gin.H{"model-prices": prices})
}

func (h *Handler) PutModelPrices(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var prices map[string]config.ModelPrice
	if err := json.Unmarshal(body, &prices); err != nil {
		var wrapped struct {
			Items map[string]config.ModelPrice `json:"items"`
		}
		if err2 := json.Unmarshal(body, &wrapped); err2 != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: expected map of model prices"})
			return
		}
		prices = wrapped.Items
	}

	if len(prices) == 0 {
		h.cfg.ModelPrices = nil
	} else {
		h.cfg.ModelPrices = prices
	}
	h.persist(c)
}

func (h *Handler) PatchModelPrices(c *gin.Context) {
	var req struct {
		Model string             `json:"model"`
		Price config.ModelPrice  `json:"price"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model and price are required"})
		return
	}

	if h.cfg.ModelPrices == nil {
		h.cfg.ModelPrices = make(map[string]config.ModelPrice)
	}
	h.cfg.ModelPrices[req.Model] = req.Price
	h.persist(c)
}

func (h *Handler) DeleteModelPrices(c *gin.Context) {
	model := c.Query("model")
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model query parameter is required"})
		return
	}

	if h.cfg.ModelPrices != nil {
		delete(h.cfg.ModelPrices, model)
		if len(h.cfg.ModelPrices) == 0 {
			h.cfg.ModelPrices = nil
		}
	}
	h.persist(c)
}
