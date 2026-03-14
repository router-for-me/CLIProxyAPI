package management

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	log "github.com/sirupsen/logrus"
)

// GetClusterState exposes the minimal peer handshake payload used by other cluster nodes.
func (h *Handler) GetClusterState(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "cluster_unavailable", "message": "cluster runtime config is unavailable"})
		return
	}
	if !h.cfg.Cluster.Enabled {
		c.JSON(http.StatusConflict, gin.H{"error": "cluster_disabled", "message": "cluster mode is disabled"})
		return
	}

	nodeID := strings.TrimSpace(h.cfg.Cluster.NodeID)
	advertiseURL := strings.TrimSpace(h.cfg.Cluster.AdvertiseURL)
	if nodeID == "" || advertiseURL == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "cluster_invalid", "message": "cluster.node-id and cluster.advertise-url must be configured"})
		return
	}

	c.JSON(http.StatusOK, cluster.State{
		NodeID:       nodeID,
		AdvertiseURL: advertiseURL,
		Version:      buildinfo.Version,
	})
}

// WithTargetProxy wraps approved management endpoints with explicit target=<node-id> proxy support.
func (h *Handler) WithTargetProxy(local gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		targetID := strings.TrimSpace(c.Query("target"))
		if targetID == "" {
			local(c)
			return
		}
		if h == nil || h.cluster == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "cluster_unavailable", "message": "cluster service is not available"})
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "read_failed", "message": "failed to read request body"})
			return
		}

		resp, err := h.cluster.ProxyManagementRequest(c.Request.Context(), targetID, c.Request.Method, c.Request.URL.Path, c.Request.URL.RawQuery, c.Request.Header, body)
		if err != nil {
			writeTargetProxyError(c, err)
			return
		}
		defer func() {
			_ = resp.Body.Close()
		}()

		copyTargetProxyHeaders(c.Writer.Header(), resp.Header)
		c.Status(resp.StatusCode)
		if _, err := io.Copy(c.Writer, resp.Body); err != nil {
			log.WithError(err).Debug("failed to stream proxied management response")
		}
		c.Abort()
	}
}

func writeTargetProxyError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, cluster.ErrClusterDisabled):
		c.JSON(http.StatusConflict, gin.H{"error": "cluster_disabled", "message": err.Error()})
	case errors.Is(err, cluster.ErrPeerNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown_target", "message": err.Error()})
	case errors.Is(err, cluster.ErrPeerDisabled):
		c.JSON(http.StatusConflict, gin.H{"error": "target_disabled", "message": err.Error()})
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "target_proxy_failed", "message": err.Error()})
	}
}

func copyTargetProxyHeaders(dst, src http.Header) {
	for key, values := range src {
		if isTargetProxyHopByHopHeader(key) {
			continue
		}
		copied := append([]string(nil), values...)
		dst[key] = copied
	}
}

func isTargetProxyHopByHopHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "connection", "proxy-connection", "keep-alive", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}
