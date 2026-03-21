package management

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/localrouting"
)

func (h *Handler) localRoutingConfig() localrouting.Config {
	if h == nil || h.cfg == nil {
		return localrouting.BuildConfig(false, "", "localhost", localrouting.DefaultEdgePort, false, 0, "", false, false)
	}
	cfg := h.cfg.LocalRouting
	return localrouting.BuildConfig(
		cfg.Enabled,
		cfg.Name,
		cfg.TLD,
		cfg.EdgePort,
		cfg.HTTPS,
		cfg.AppPort,
		cfg.StateDir,
		cfg.Force,
		cfg.DisplayOAuthURL,
	)
}

// GetLocalRoutingStatus returns runtime status for local named routing.
func (h *Handler) GetLocalRoutingStatus(c *gin.Context) {
	cfg := h.localRoutingConfig()
	status := localrouting.LoadStatusFromConfig(cfg)
	if h != nil && h.localRouting != nil {
		status = h.localRouting.Status()
	}
	if status.StateDir != "" {
		if pid := localrouting.ReadEdgePID(status.StateDir); pid > 0 {
			status.EdgeOwnerPID = pid
		}
	}
	c.JSON(http.StatusOK, status)
}

// GetLocalRoutingRoutes lists persisted local routes.
func (h *Handler) GetLocalRoutingRoutes(c *gin.Context) {
	cfg := h.localRoutingConfig()
	stateDir := localrouting.ResolveStateDir(cfg.StateDir, cfg.EdgePort)
	store := localrouting.NewRouteStore(stateDir, cfg.EdgePort)
	routes, errList := store.List()
	if errList != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errList.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"routes": routes})
}

// PostLocalRoutingSyncHosts optionally adds current routes to /etc/hosts.
func (h *Handler) PostLocalRoutingSyncHosts(c *gin.Context) {
	cfg := h.localRoutingConfig()
	if strings.EqualFold(localrouting.NormalizeTLD(cfg.TLD), "localhost") {
		c.JSON(http.StatusOK, gin.H{"status": "skipped", "message": "tld localhost does not require hosts sync"})
		return
	}
	stateDir := localrouting.ResolveStateDir(cfg.StateDir, cfg.EdgePort)
	store := localrouting.NewRouteStore(stateDir, cfg.EdgePort)
	routes, errList := store.List()
	if errList != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errList.Error()})
		return
	}
	if errSync := syncHostsFile(routes); errSync != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errSync.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "routes": len(routes)})
}

func syncHostsFile(routes []localrouting.RouteInfo) error {
	const beginMarker = "# BEGIN cliproxyapi-local-routing"
	const endMarker = "# END cliproxyapi-local-routing"
	const hostsPath = "/etc/hosts"

	payload, errRead := os.ReadFile(hostsPath)
	if errRead != nil {
		return errRead
	}
	content := string(payload)
	start := strings.Index(content, beginMarker)
	end := strings.Index(content, endMarker)
	if start >= 0 && end > start {
		end += len(endMarker)
		content = strings.TrimSpace(content[:start]) + "\n"
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(content))
	b.WriteString("\n\n")
	b.WriteString(beginMarker)
	b.WriteString("\n")
	for _, route := range routes {
		if strings.TrimSpace(route.Host) == "" {
			continue
		}
		b.WriteString("127.0.0.1\t")
		b.WriteString(route.Host)
		b.WriteString("\n")
	}
	b.WriteString(endMarker)
	b.WriteString("\n")
	return os.WriteFile(hostsPath, []byte(b.String()), 0o644)
}
