package management

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func (h *Handler) LeaseCaptures(c *gin.Context) {
	var body struct {
		Enabled *bool `json:"enabled"`
		Version int   `json:"version"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expected enabled boolean"})
		return
	}
	if *body.Enabled && body.Version != 2 {
		c.JSON(http.StatusConflict, gin.H{"error": "complete capture requires an updated Spillway client (protocol 2)"})
		return
	}
	if err := logging.DesktopCaptures.Lease(*body.Enabled); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"leaseSeconds": 0, "version": 2, "chunkBytes": logging.CaptureChunkBytes})
}

func (h *Handler) AcknowledgeCaptures(c *gin.Context) {
	var body struct {
		Epoch    string `json:"epoch"`
		Sequence uint64 `json:"sequence"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Epoch == "" || len(body.Epoch) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid capture acknowledgement"})
		return
	}
	if err := logging.DesktopCaptures.Acknowledge(body.Epoch, body.Sequence); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *Handler) ClearCaptures(c *gin.Context) {
	if err := logging.DesktopCaptures.Clear(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) GetCaptures(c *gin.Context) {
	after, err := strconv.ParseUint(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cursor"})
		return
	}
	c.JSON(http.StatusOK, logging.DesktopCaptures.Page(after))
}
