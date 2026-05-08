package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type apiKeyUsageEntry struct {
	Success        int64                          `json:"success"`
	Failed         int64                          `json:"failed"`
	RecentRequests []coreauth.RecentRequestBucket `json:"recent_requests"`
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

// GetAPIKeyUsage returns recent request buckets for all in-memory auths
// (api_key or OAuth), grouped by provider and keyed by "base_url|identity"
// where identity is the api key for api_key auths or the account email for
// OAuth auths.
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
		if auth == nil {
			continue
		}
		_, identity := auth.AccountInfo()
		identity = strings.TrimSpace(identity)
		if identity == "" {
			continue
		}
		baseURL := ""
		if auth.Attributes != nil {
			baseURL = strings.TrimSpace(auth.Attributes["base_url"])
			if baseURL == "" {
				baseURL = strings.TrimSpace(auth.Attributes["base-url"])
			}
		}
		compositeKey := baseURL + "|" + identity
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if provider == "" {
			provider = "unknown"
		}

		recent := auth.RecentRequestsSnapshot(now)
		providerBucket, ok := out[provider]
		if !ok {
			providerBucket = make(map[string]apiKeyUsageEntry)
			out[provider] = providerBucket
		}
		if existing, exists := providerBucket[compositeKey]; exists {
			existing.Success += auth.Success
			existing.Failed += auth.Failed
			existing.RecentRequests = mergeRecentRequestBuckets(existing.RecentRequests, recent)
			providerBucket[compositeKey] = existing
			continue
		}
		providerBucket[compositeKey] = apiKeyUsageEntry{
			Success:        auth.Success,
			Failed:         auth.Failed,
			RecentRequests: recent,
		}
	}

	c.JSON(http.StatusOK, out)
}
