package management

import (
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ListAuthFileUsage returns the most recently observed rate-limit/usage snapshot
// for each auth credential, as reported by the upstream provider on its response
// headers (Claude: Anthropic-Ratelimit-Unified-*; Codex: X-Codex-Primary/Secondary-*).
// The data is in-memory only (sdk/cliproxy/auth.Auth.RateLimits, not persisted to
// disk) and reflects whatever was last observed since the process started; auths
// with no recorded usage yet are omitted from the response.
func (h *Handler) ListAuthFileUsage(c *gin.Context) {
	if h == nil {
		c.JSON(500, gin.H{"error": "handler not initialized"})
		return
	}
	if h.authManager == nil {
		c.JSON(200, gin.H{"usage": []gin.H{}})
		return
	}
	auths := h.authManager.List()
	usage := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if entry := buildAuthUsageEntry(auth); entry != nil {
			usage = append(usage, entry)
		}
	}
	sort.Slice(usage, func(i, j int) bool {
		nameI, _ := usage[i]["name"].(string)
		nameJ, _ := usage[j]["name"].(string)
		return strings.ToLower(nameI) < strings.ToLower(nameJ)
	})
	c.JSON(200, gin.H{"usage": usage})
}

// buildAuthUsageEntry maps an auth's in-memory RateLimits snapshot (see
// sdk/cliproxy/auth/rate_limit_headers.go) onto a provider-agnostic long/short
// window shape ("usage_7d"/"usage_5h"). It returns nil when the provider is not
// rate-limit tracked or the auth has no recorded usage yet.
func buildAuthUsageEntry(auth *coreauth.Auth) gin.H {
	if auth == nil || len(auth.RateLimits) == 0 {
		return nil
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	if name == "" {
		return nil
	}

	var window7d, window5h gin.H
	switch strings.TrimSpace(auth.Provider) {
	case "claude":
		window7d = rateLimitWindow(auth.RateLimits, "7d_utilization", "7d_reset")
		window5h = rateLimitWindow(auth.RateLimits, "5h_utilization", "5h_reset")
	case "codex":
		// Codex's "primary" window is the long/weekly window and "secondary" is
		// the short window; both map onto the 7d/5h response shape for display.
		window7d = rateLimitWindow(auth.RateLimits, "primary_used_percent", "primary_reset_at")
		window5h = rateLimitWindow(auth.RateLimits, "secondary_used_percent", "secondary_reset_at")
	default:
		return nil
	}
	if window7d == nil && window5h == nil {
		return nil
	}

	entry := gin.H{
		"id":   auth.ID,
		"name": name,
		"type": strings.TrimSpace(auth.Provider),
	}
	if window7d != nil {
		entry["usage_7d"] = window7d
	}
	if window5h != nil {
		entry["usage_5h"] = window5h
	}
	if observedAt, ok := auth.RateLimits["observed_at"].(string); ok && observedAt != "" {
		entry["observed_at"] = observedAt
	}
	return entry
}

// rateLimitWindow extracts a {percent, reset_at} pair from a rate-limit snapshot.
// It returns nil when neither value is present.
func rateLimitWindow(snapshot map[string]any, percentKey, resetKey string) gin.H {
	window := gin.H{}
	if percent, ok := rateLimitInt(snapshot, percentKey); ok {
		window["percent"] = percent
	}
	if resetAt, ok := snapshot[resetKey].(string); ok && resetAt != "" {
		window["reset_at"] = resetAt
	}
	if len(window) == 0 {
		return nil
	}
	return window
}

// rateLimitInt reads a numeric rate-limit snapshot value. Values are normally
// stored as int (see setRateLimitInt in rate_limit_headers.go), but this also
// accepts float64/string in case the value ever round-trips through JSON or an
// upstream header did not contain a plain integer.
func rateLimitInt(snapshot map[string]any, key string) (int, bool) {
	switch v := snapshot[key].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n, true
		}
	}
	return 0, false
}
