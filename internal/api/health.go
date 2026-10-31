package api

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// evaluateReadiness reports whether the process has a usable provider credential
// or is intentionally running without local credentials (Home / empty config).
// It uses cached auth state only and never issues live upstream calls.
func evaluateReadiness(cfg *config.Config, manager *auth.Manager) (ready bool, reason string, usable int, total int) {
	requireWeight := readinessRequiresPositiveWeight(cfg)
	if manager != nil {
		for _, entry := range manager.List() {
			total++
			if authIsUsable(manager, entry, requireWeight) {
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
	if total == 0 && !hasConfiguredProviderCredentials(cfg) && !hasAuthDirectoryCredentialSources(cfg) {
		return true, "ok", usable, total
	}
	return false, "no_usable_auth", usable, total
}

func readinessRequiresPositiveWeight(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	// Match sdk/cliproxy normalizedRoutingRuntimeState aliases so readiness
	// agrees with the weighted selector the runtime actually installs.
	switch strings.ToLower(strings.TrimSpace(cfg.Routing.Strategy)) {
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		return true
	default:
		return false
	}
}

// authIsUsable mirrors scheduler admission: availability/selection predicates,
// a registered executor, plus positive weight when routing.strategy is WRR.
func authIsUsable(manager *auth.Manager, entry *auth.Auth, requirePositiveWeight bool) bool {
	if !auth.IsSelectableCredential(entry) {
		return false
	}
	if !auth.HasRegisteredExecutor(manager, entry) {
		return false
	}
	if requirePositiveWeight && !auth.HasPositiveCredentialWeight(entry) {
		return false
	}
	return true
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

// hasAuthDirectoryCredentialSources reports whether the configured auth
// directory contains non-empty JSON files. FileTokenStore.List skips files
// that fail to load, so an empty manager must not be treated as ready when
// those sources exist.
func hasAuthDirectoryCredentialSources(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	dir := strings.TrimSpace(cfg.AuthDir)
	if dir == "" {
		return false
	}
	found := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		info, errStat := d.Info()
		if errStat != nil {
			if os.IsNotExist(errStat) {
				return nil
			}
			found = true
			return fs.SkipAll
		}
		if info.Size() <= 0 {
			return nil
		}
		found = true
		return fs.SkipAll
	})
	return found
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

type authStatusLabels struct {
	status   string
	provider string
	reason   string
}

func renderPrometheusMetrics(manager *auth.Manager) string {
	statusCounts := map[authStatusLabels]int{}
	// Keep a stable zero series for each lifecycle status when nothing is registered.
	for _, status := range []string{
		string(auth.StatusUnknown),
		string(auth.StatusActive),
		string(auth.StatusPending),
		string(auth.StatusRefreshing),
		string(auth.StatusError),
		string(auth.StatusDisabled),
	} {
		statusCounts[authStatusLabels{status: status, provider: auth.MetricAuthProvider(nil), reason: auth.MetricReasonOther}] = 0
	}
	unavailable := 0
	if manager != nil {
		for _, entry := range manager.List() {
			if entry == nil {
				continue
			}
			status := string(entry.Status)
			if status == "" {
				status = string(auth.StatusUnknown)
			}
			key := authStatusLabels{
				status:   status,
				provider: auth.MetricAuthProvider(entry),
				reason:   auth.MetricAuthReason(entry),
			}
			statusCounts[key]++
			if entry.Unavailable {
				unavailable++
			}
		}
	}

	var b strings.Builder
	b.WriteString("# HELP cliproxy_auth_status Number of auth entries by lifecycle status, provider, and coarse reason.\n")
	b.WriteString("# TYPE cliproxy_auth_status gauge\n")
	statusKeys := make([]authStatusLabels, 0, len(statusCounts))
	for key := range statusCounts {
		statusKeys = append(statusKeys, key)
	}
	sort.Slice(statusKeys, func(i, j int) bool {
		if statusKeys[i].status != statusKeys[j].status {
			return statusKeys[i].status < statusKeys[j].status
		}
		if statusKeys[i].provider != statusKeys[j].provider {
			return statusKeys[i].provider < statusKeys[j].provider
		}
		return statusKeys[i].reason < statusKeys[j].reason
	})
	for _, key := range statusKeys {
		fmt.Fprintf(&b, "cliproxy_auth_status{status=%q,provider=%q,reason=%q} %d\n", key.status, key.provider, key.reason, statusCounts[key])
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
