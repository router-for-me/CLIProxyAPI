package management

import (
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// RuntimeInfo describes the host contract exposed to a supervising process.
type RuntimeInfo struct {
	ContractVersion  string    `json:"contract_version"`
	ComponentVersion string    `json:"component_version"`
	Commit           string    `json:"commit"`
	BuildTime        string    `json:"build_time"`
	PID              int       `json:"pid"`
	StartedAt        time.Time `json:"-"`
}

type runtimeInfoResponse struct {
	ContractVersion  string `json:"contract_version"`
	ComponentVersion string `json:"component_version"`
	Commit           string `json:"commit"`
	BuildTime        string `json:"build_time"`
	PID              int    `json:"pid"`
	StartedAt        string `json:"started_at"`
}

// SetRuntimeControl configures host runtime metadata and graceful shutdown.
func (h *Handler) SetRuntimeControl(info RuntimeInfo, shutdown func()) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.runtimeInfo = info
	h.runtimeShutdown = shutdown
	h.mu.Unlock()
}

// GetRuntimeInfo returns the non-sensitive host runtime contract.
func (h *Handler) GetRuntimeInfo(c *gin.Context) {
	h.mu.Lock()
	info := h.runtimeInfo
	h.mu.Unlock()
	c.JSON(http.StatusOK, runtimeInfoResponse{
		ContractVersion:  info.ContractVersion,
		ComponentVersion: info.ComponentVersion,
		Commit:           info.Commit,
		BuildTime:        info.BuildTime,
		PID:              info.PID,
		StartedAt:        info.StartedAt.UTC().Format(time.RFC3339),
	})
}

// PostRuntimeShutdown schedules graceful shutdown for loopback callers.
func (h *Handler) PostRuntimeShutdown(c *gin.Context) {
	remoteAddr := c.Request.RemoteAddr
	remoteHost, _, errSplit := net.SplitHostPort(remoteAddr)
	if errSplit != nil {
		remoteHost = remoteAddr
	}
	clientIP := net.ParseIP(remoteHost)
	if clientIP == nil || !clientIP.IsLoopback() {
		c.JSON(http.StatusForbidden, gin.H{"error": "runtime shutdown is restricted to loopback clients"})
		return
	}

	h.mu.Lock()
	shutdown := h.runtimeShutdown
	h.mu.Unlock()
	if shutdown == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runtime shutdown is unavailable"})
		return
	}

	c.Status(http.StatusAccepted)
	c.Writer.WriteHeaderNow()
	go shutdown()
}
