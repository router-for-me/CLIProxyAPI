package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
)

// resolveLoginProxyURL extracts and validates the optional `?proxy_url` query
// parameter used by every OAuth-login management endpoint. When empty, the
// caller falls back to cfg.ProxyURL (existing behaviour). When non-empty and
// invalid, the function writes an HTTP 400 response and returns ok=false so the
// caller can return immediately.
//
// The returned proxyURL is the trimmed string ready to be passed to
// proxyutil.BuildHTTPTransport / persisted on the resulting Auth file.
func resolveLoginProxyURL(c *gin.Context) (proxyURL string, ok bool) {
	raw := strings.TrimSpace(c.Query("proxy_url"))
	if raw == "" {
		return "", true
	}
	if _, err := proxyutil.Parse(raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_proxy_url",
			"message": err.Error(),
		})
		return "", false
	}
	return raw, true
}

// validateLoginProxyURL is a body-friendly counterpart of resolveLoginProxyURL.
// It only validates the value (used by POST handlers like GitLab PAT and iFlow
// cookie login that read proxy_url from a JSON body field). Returns "" on
// empty input. On invalid input, writes a 400 response and returns ok=false.
func validateLoginProxyURL(c *gin.Context, raw string) (proxyURL string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", true
	}
	if _, err := proxyutil.Parse(trimmed); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_proxy_url",
			"message": err.Error(),
		})
		return "", false
	}
	return trimmed, true
}

// withLoginProxy returns a *config.Config whose SDKConfig.ProxyURL is replaced
// by proxyURL. When proxyURL is empty or cfg is nil, cfg itself is returned
// unchanged so callers can pass-through with no allocation.
func withLoginProxy(cfg *config.Config, proxyURL string) *config.Config {
	if cfg == nil || strings.TrimSpace(proxyURL) == "" {
		return cfg
	}
	cfgCopy := *cfg
	sdkCopy := cfg.SDKConfig
	sdkCopy.ProxyURL = strings.TrimSpace(proxyURL)
	cfgCopy.SDKConfig = sdkCopy
	return &cfgCopy
}

