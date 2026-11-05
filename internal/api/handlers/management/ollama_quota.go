package management

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/ollama"
)

// ListOllamaQuotaConnections discovers config-backed quota connections without
// sending the provider configuration (which contains API keys) to the browser.
func (h *Handler) ListOllamaQuotaConnections(c *gin.Context) {
	connections := h.ollamaQuota.Connections(h.configSnapshot())
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"connections": connections})
}

// GetOllamaQuota handles GET /v8/management/quotas/ollama.
//
// It returns Ollama Cloud usage and balance for one configured
// openai-compatibility connection. The client supplies only an opaque
// credential identifier from discovery plus allowlisted range and
// scope values; the API key is resolved server-side from configuration, so no
// credential material ever crosses the management boundary. The upstream
// origin is fixed to https://ollama.com, which keeps this endpoint free of
// SSRF and credential-exfiltration surface.
//
// Query parameters:
//   - provider_id (required): opaque connection_id returned by discovery.
//   - range (optional, default 7d): one of 24h, 7d, 30d.
//   - scope (optional, default self): only "self" is accepted; team scope
//     requires team-admin eligibility and is not exposed.
func (h *Handler) GetOllamaQuota(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h == nil || h.ollamaQuota == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ollama quota service unavailable"})
		return
	}

	providerID := firstNonEmptyString(queryPointer(c, "provider_id"), queryPointer(c, "providerId"))
	if providerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider_id is required"})
		return
	}
	known := false
	for _, connection := range h.ollamaQuota.Connections(h.configSnapshot()) {
		if connection.ID == providerID {
			known = true
			break
		}
	}
	if !known {
		c.JSON(http.StatusNotFound, gin.H{"error": "no Ollama Cloud connection is configured for this provider"})
		return
	}

	rangeValue := strings.TrimSpace(firstNonEmptyString(queryPointer(c, "range")))
	if rangeValue == "" {
		rangeValue = string(ollama.UsageRange7d)
	}
	rng := ollama.UsageRange(rangeValue)
	if !rng.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "range must be one of 24h, 7d, or 30d"})
		return
	}

	scopeValue := strings.TrimSpace(firstNonEmptyString(queryPointer(c, "scope")))
	if scopeValue == "" {
		scopeValue = string(ollama.UsageScopeSelf)
	}
	scope := ollama.UsageScope(scopeValue)
	if !scope.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scope must be self"})
		return
	}

	cfg := h.configSnapshot()
	resp, err := h.ollamaQuota.FetchQuota(c.Request.Context(), cfg, providerID, rng, scope)
	if err != nil {
		var upstreamErr *ollama.UpstreamError
		if errors.As(err, &upstreamErr) && upstreamErr.RetryAfter > 0 {
			c.Header("Retry-After", fmt.Sprintf("%d", int(math.Ceil(upstreamErr.RetryAfter.Seconds()))))
		}
		c.JSON(ollamaQuotaErrorStatus(err), gin.H{"error": sanitizeOllamaQuotaError(err)})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// queryPointer adapts a gin query lookup to the *string helpers used across
// this package.
func queryPointer(c *gin.Context, name string) *string {
	value := c.Query(name)
	return &value
}

// ollamaQuotaErrorStatus maps service errors to management HTTP statuses.
// Upstream 401/403 map to 502 rather than being passed through: a 401 would
// trigger client logout handling, and the underlying cause (a misconfigured
// or suspended Ollama account) is a gateway-side condition.
func ollamaQuotaErrorStatus(err error) int {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, ollama.ErrConnectionNotFound) {
		return http.StatusNotFound
	}
	var upstreamErr *ollama.UpstreamError
	if errors.As(err, &upstreamErr) {
		switch {
		case upstreamErr.StatusCode == http.StatusUnauthorized || upstreamErr.StatusCode == http.StatusForbidden:
			return http.StatusBadGateway
		case upstreamErr.StatusCode == http.StatusTooManyRequests || upstreamErr.StatusCode >= http.StatusInternalServerError:
			return http.StatusServiceUnavailable
		default:
			return http.StatusBadGateway
		}
	}
	return http.StatusBadGateway
}

// sanitizeOllamaQuotaError exposes fixed messages, never upstream response text.
func sanitizeOllamaQuotaError(err error) string {
	if err == nil {
		return "ollama cloud quota fetch failed"
	}
	var upstreamErr *ollama.UpstreamError
	if errors.As(err, &upstreamErr) {
		return upstreamErr.Message
	}
	if errors.Is(err, ollama.ErrConnectionNotFound) {
		return "no Ollama Cloud connection is configured for this provider"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "request cancelled"
	}
	return "Ollama Cloud quota fetch failed"
}
