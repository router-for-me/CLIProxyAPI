package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// evaluateReadiness reports whether the process has a usable provider credential
// or is intentionally running without local credentials (Home / empty config).
// It uses cached auth state only and never issues live upstream calls.
func evaluateReadiness(cfg *config.Config, manager *auth.Manager) (ready bool, reason string, usable int, total int) {
	if manager != nil {
		for _, entry := range manager.List() {
			total++
			if authIsUsable(entry) {
				usable++
			}
		}
	}
	if usable > 0 {
		return true, "ok", usable, total
	}
	if cfg != nil && cfg.Home.Enabled {
		return true, "ok", usable, total
	}
	if total == 0 && !hasConfiguredProviderCredentials(cfg) {
		return true, "ok", usable, total
	}
	return false, "no_usable_auth", usable, total
}

func authIsUsable(entry *auth.Auth) bool {
	if entry == nil || entry.Disabled || entry.Unavailable {
		return false
	}
	switch entry.Status {
	case auth.StatusError, auth.StatusDisabled, auth.StatusPending:
		return false
	default:
		return true
	}
}

func hasConfiguredProviderCredentials(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	if len(cfg.OpenAICompatibility) > 0 || len(cfg.GeminiKey) > 0 || len(cfg.ClaudeKey) > 0 ||
		len(cfg.CodexKey) > 0 || len(cfg.XAIKey) > 0 || len(cfg.VertexCompatAPIKey) > 0 ||
		len(cfg.InteractionsKey) > 0 {
		return true
	}
	return false
}

func (s *Server) handleReadyz(c *gin.Context) {
	var manager *auth.Manager
	if s != nil && s.handlers != nil {
		manager = s.handlers.AuthManager
	}
	var cfg *config.Config
	if s != nil {
		cfg = s.cfg
	}
	ready, reason, usable, total := evaluateReadiness(cfg, manager)
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	if c.Request.Method == http.MethodHead {
		c.Status(status)
		return
	}
	c.JSON(status, gin.H{
		"status":      reason,
		"ready":       ready,
		"usable_auth": usable,
		"total_auth":  total,
	})
}

func (s *Server) handleMetrics(c *gin.Context) {
	var manager *auth.Manager
	if s != nil && s.handlers != nil {
		manager = s.handlers.AuthManager
	}
	c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	c.String(http.StatusOK, renderPrometheusMetrics(manager))
}

func renderPrometheusMetrics(manager *auth.Manager) string {
	statusCounts := map[string]int{
		string(auth.StatusUnknown):    0,
		string(auth.StatusActive):     0,
		string(auth.StatusPending):    0,
		string(auth.StatusRefreshing): 0,
		string(auth.StatusError):      0,
		string(auth.StatusDisabled):   0,
	}
	unavailable := 0
	if manager != nil {
		for _, entry := range manager.List() {
			if entry == nil {
				continue
			}
			key := string(entry.Status)
			if key == "" {
				key = string(auth.StatusUnknown)
			}
			statusCounts[key]++
			if entry.Unavailable {
				unavailable++
			}
		}
	}

	var b strings.Builder
	b.WriteString("# HELP cliproxy_auth_status Number of auth entries by lifecycle status.\n")
	b.WriteString("# TYPE cliproxy_auth_status gauge\n")
	statusKeys := make([]string, 0, len(statusCounts))
	for key := range statusCounts {
		statusKeys = append(statusKeys, key)
	}
	sort.Strings(statusKeys)
	for _, key := range statusKeys {
		fmt.Fprintf(&b, "cliproxy_auth_status{status=%q} %d\n", key, statusCounts[key])
	}
	b.WriteString("# HELP cliproxy_auth_unavailable Number of auth entries marked unavailable.\n")
	b.WriteString("# TYPE cliproxy_auth_unavailable gauge\n")
	fmt.Fprintf(&b, "cliproxy_auth_unavailable %d\n", unavailable)

	success, errorsByStatus := auth.SnapshotUpstreamMetrics()
	b.WriteString("# HELP cliproxy_upstream_requests_total Upstream execution results by outcome.\n")
	b.WriteString("# TYPE cliproxy_upstream_requests_total counter\n")
	fmt.Fprintf(&b, "cliproxy_upstream_requests_total{result=%q} %d\n", "success", success)
	b.WriteString("# HELP cliproxy_upstream_errors_total Upstream execution errors by HTTP status.\n")
	b.WriteString("# TYPE cliproxy_upstream_errors_total counter\n")
	errorStatuses := make([]string, 0, len(errorsByStatus))
	for status := range errorsByStatus {
		errorStatuses = append(errorStatuses, status)
	}
	sort.Strings(errorStatuses)
	for _, status := range errorStatuses {
		fmt.Fprintf(&b, "cliproxy_upstream_errors_total{status=%q} %d\n", status, errorsByStatus[status])
	}
	return b.String()
}
