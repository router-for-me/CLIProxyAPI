package management

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementauth"
)

// Account security decisions use the connected peer, not untrusted forwarding headers.
func accountPeerIP(c *gin.Context) string {
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err == nil {
		return host
	}
	return c.Request.RemoteAddr
}

func isUserAccount(c *gin.Context) bool {
	value, exists := c.Get("management_account")
	if !exists {
		return false
	} // Existing directly invoked handlers/legacy administrators.
	account, ok := value.(managementauth.User)
	return !ok || account.Role != "admin"
}

// accountVisibleJSON removes quota/billing metadata attached to credential lists.
func accountVisibleJSON(c *gin.Context, status int, value any) {
	if isUserAccount(c) {
		raw, err := json.Marshal(value)
		var decoded any
		if err != nil || json.Unmarshal(raw, &decoded) != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "cannot encode response"})
			return
		}
		value = accountSanitizeJSONConfig(decoded)
	}
	c.JSON(status, value)
}

// User accounts are trusted operators of the shared credential pool, not tenants.
// Keep billing/quota and identity administration out of that operator role.
func (h *Handler) authorizeAccountRequest(c *gin.Context) bool {
	if !isUserAccount(c) {
		return true
	}
	p := c.Request.URL.Path
	// Use the decoded, normalized path, including plugin-owned fallback routes.
	if decoded, err := url.PathUnescape(p); err == nil {
		p = decoded
	}
	p = strings.ToLower(path.Clean(p))
	p = strings.TrimPrefix(p, "/v0/management")
	forbidden := strings.Contains(p, "billing") || strings.Contains(p, "quota") ||
		p == "/accounts/users" || strings.HasPrefix(p, "/accounts/users/") ||
		p == "/usage" || strings.HasPrefix(p, "/usage/") ||
		p == "/usage-queue" || p == "/api-call"
	// Installing over an existing protected plugin also re-enables its config.
	if strings.HasPrefix(p, "/plugin-store/") && strings.HasSuffix(p, "/install") {
		h.mu.Lock()
		if h.cfg != nil {
			forbidden = forbidden || accountPluginConfigContainsProtectedFields(pluginConfigNode(h.cfg.Plugins.Configs[c.Param("id")]))
		}
		h.mu.Unlock()
	}
	// Do not let the generic routing editor disable an admin's quota-aware policy.
	if p == "/routing/strategy" && h.cfg != nil && strings.EqualFold(h.cfg.Routing.Strategy, "quota-aware") {
		forbidden = true
	}
	if p == "/routing/strategy" && c.Request.Method != http.MethodGet && !forbidden {
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 65536))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return false
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		var value struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(body, &value) == nil {
			normalized, _ := normalizeRoutingStrategy(value.Value)
			forbidden = normalized == "quota-aware"
		}
	}
	if forbidden {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin permission required for billing, quota or account administration"})
		return false
	}
	return true
}
