package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type apiKeyUsageEntry struct {
	AuthIndex      string                         `json:"auth_index,omitempty"`
	AuthKey        string                         `json:"auth_key,omitempty"`
	AuthSource     string                         `json:"auth_source,omitempty"`
	Success        int64                          `json:"success"`
	Failed         int64                          `json:"failed"`
	RecentRequests []coreauth.RecentRequestBucket `json:"recent_requests"`
}

type apiKeyUsageIdentity struct {
	provider string
	key      string
	authKey  string
	source   string
}

func apiKeyUsageIdentityForAuth(auth *coreauth.Auth) (apiKeyUsageIdentity, bool) {
	var identity apiKeyUsageIdentity
	if auth == nil {
		return identity, false
	}

	baseURL := ""
	apiKey := ""
	commandKey := ""
	if auth.Attributes != nil {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		if baseURL == "" {
			baseURL = strings.TrimSpace(auth.Attributes["base-url"])
		}
		commandKey = strings.TrimSpace(auth.Attributes[coreauth.AttrAuthCommandKey])
	}

	identity.provider = apiKeyUsageProviderKey(auth)

	if apiKey != "" {
		identity.key = baseURL + "|" + apiKey
		identity.authKey = apiKey
		identity.source = "api_key"
		return identity, true
	}

	if coreauth.IsCommandAuth(auth) {
		if commandKey == "" {
			commandKey = auth.EnsureIndex()
		}
		if commandKey == "" {
			commandKey = strings.TrimSpace(auth.ID)
		}
		if commandKey == "" {
			return identity, false
		}
		identity.authKey = "auth-command:" + commandKey
		identity.source = coreauth.AttrAuthSourceCommand
		// Older management frontends key config credentials as "base_url|api_key".
		// Command-auth entries intentionally have no static api_key, so use the
		// empty-key shape while exposing the stable command identity in auth_key.
		identity.key = baseURL + "|"
		return identity, true
	}

	return identity, false
}

func mergeRecentRequestBuckets(dst, src []coreauth.RecentRequestBucket) []coreauth.RecentRequestBucket {
	if len(dst) == 0 {
		return src
	}
	if len(src) == 0 {
		return dst
	}
	if len(dst) != len(src) {
		n := len(dst)
		if len(src) < n {
			n = len(src)
		}
		for i := 0; i < n; i++ {
			dst[i].Success += src[i].Success
			dst[i].Failed += src[i].Failed
		}
		return dst
	}
	for i := range dst {
		dst[i].Success += src[i].Success
		dst[i].Failed += src[i].Failed
	}
	return dst
}

func apiKeyUsageProviderKey(auth *coreauth.Auth) string {
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if auth.Attributes != nil {
		if compatName := strings.TrimSpace(auth.Attributes["compat_name"]); compatName != "" {
			provider = strings.ToLower(compatName)
		}
	}
	if provider == "" {
		return "unknown"
	}
	return provider
}

// GetAPIKeyUsage returns recent request buckets for in-memory API-key-class auths,
// grouped by provider and keyed by "base_url|api_key". Command-auth credentials
// have no static api_key, so they use the legacy-compatible "base_url|" key and
// expose their stable command identity in the entry's auth_key field.
func (h *Handler) GetAPIKeyUsage(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	now := time.Now()
	out := make(map[string]map[string]apiKeyUsageEntry)
	for _, auth := range manager.List() {
		identity, okIdentity := apiKeyUsageIdentityForAuth(auth)
		if !okIdentity {
			continue
		}

		recent := auth.RecentRequestsSnapshot(now)
		providerBucket, ok := out[identity.provider]
		if !ok {
			providerBucket = make(map[string]apiKeyUsageEntry)
			out[identity.provider] = providerBucket
		}
		if existing, exists := providerBucket[identity.key]; exists {
			existing.Success += auth.Success
			existing.Failed += auth.Failed
			existing.RecentRequests = mergeRecentRequestBuckets(existing.RecentRequests, recent)
			providerBucket[identity.key] = existing
			continue
		}
		providerBucket[identity.key] = apiKeyUsageEntry{
			AuthIndex:      auth.EnsureIndex(),
			AuthKey:        identity.authKey,
			AuthSource:     identity.source,
			Success:        auth.Success,
			Failed:         auth.Failed,
			RecentRequests: recent,
		}
	}

	c.JSON(http.StatusOK, out)
}
