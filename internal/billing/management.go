package billing

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type ManagementHandler struct{ service *Service }

func NewManagementHandler(service *Service) *ManagementHandler {
	return &ManagementHandler{service: service}
}
func (h *ManagementHandler) Register(r gin.IRoutes) {
	if h == nil || r == nil {
		return
	}
	r.GET("/billing/usage", h.GetUsage)
	r.GET("/billing/usage/:request_id", h.GetUsageByRequestID)
	r.GET("/billing/usage/export", h.ExportUsage)
	r.GET("/billing/price-books/current", h.GetCurrentPriceBook)
}
func (h *ManagementHandler) GetUsage(c *gin.Context) {
	if h == nil || h.service == nil {
		c.JSON(http.StatusOK, Summary{})
		return
	}
	c.JSON(http.StatusOK, h.service.Summary())
}
func (h *ManagementHandler) GetUsageByRequestID(c *gin.Context) {
	id := strings.TrimSpace(c.Param("request_id"))
	if h == nil || h.service == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if e, ok := h.service.EventByRequestID(id); ok {
		c.JSON(http.StatusOK, e)
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
}
func (h *ManagementHandler) ExportUsage(c *gin.Context) {
	if h == nil || h.service == nil {
		c.JSON(http.StatusOK, []Event{})
		return
	}
	c.JSON(http.StatusOK, h.service.Events())
}
func (h *ManagementHandler) GetCurrentPriceBook(c *gin.Context) {
	if h == nil || h.service == nil {
		c.JSON(http.StatusOK, PriceBookConfig{})
		return
	}
	h.service.mu.Lock()
	pb := h.service.cfg.PriceBook
	rules := append([]PriceRule(nil), h.service.priceRules...)
	h.service.mu.Unlock()
	pb.Rules = rules
	c.JSON(http.StatusOK, pb)
}
