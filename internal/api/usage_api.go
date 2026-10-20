package api

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// usageAPIProviderUsage is the public, redacted view of one provider's usage.
type usageAPIProviderUsage struct {
	Success        int64                          `json:"success"`
	Failed         int64                          `json:"failed"`
	RecentRequests []coreauth.RecentRequestBucket `json:"recent_requests"`
	Credentials    []usageAPICredentialUsage      `json:"credentials"`
}

// usageAPICredentialUsage is the public view of one explicitly exposed auth.
// The runtime auth index is intentionally absent.
type usageAPICredentialUsage struct {
	Name           string                         `json:"name"`
	Success        int64                          `json:"success"`
	Failed         int64                          `json:"failed"`
	RecentRequests []coreauth.RecentRequestBucket `json:"recent_requests"`
}

// usageAPISelection holds normalized allowlists from the configuration.
type usageAPISelection struct {
	providers   map[string]struct{}
	credentials map[string]string
}

// usageAPIFilters holds optional client-supplied narrowing filters.
// Filters never grant access to data outside the configured allowlist.
type usageAPIFilters struct {
	providers   map[string]struct{}
	credentials map[string]struct{}
}

func usageAPIFiltersFromRequest(c *gin.Context) usageAPIFilters {
	return usageAPIFilters{
		providers:   parseUsageAPIFilterValues(c.QueryArray("provider"), normalizeUsageAPIProvider),
		credentials: parseUsageAPIFilterValues(c.QueryArray("credential"), strings.TrimSpace),
	}
}

func parseUsageAPIFilterValues(values []string, normalize func(string) string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{})
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			value := normalize(part)
			if value == "" {
				continue
			}
			out[value] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func newUsageAPISelection(cfg config.UsageAPIConfig) usageAPISelection {
	selection := usageAPISelection{
		providers:   make(map[string]struct{}),
		credentials: make(map[string]string),
	}
	for _, provider := range cfg.Providers {
		provider = normalizeUsageAPIProvider(provider)
		if provider == "" {
			continue
		}
		selection.providers[provider] = struct{}{}
	}
	for _, credential := range cfg.Credentials {
		index := strings.TrimSpace(credential.AuthIndex)
		name := strings.TrimSpace(credential.Name)
		if index == "" || name == "" {
			continue
		}
		if _, exists := selection.credentials[index]; exists {
			continue
		}
		selection.credentials[index] = name
	}
	return selection
}

// handleUsageQuery serves GET /v1/usage for authenticated client keys.
// The endpoint is disabled unless usage-api.enabled is true. It returns only
// allowlisted provider aggregates and explicitly named credentials; upstream
// keys, base URLs, account emails, auth IDs, and file paths never leave the
// server.
func (s *Server) handleUsageQuery(c *gin.Context) {
	if s == nil || c == nil {
		return
	}

	// AuthMiddleware permits requests when no access providers are configured.
	// Require a real client principal so an open proxy cannot expose usage.
	principal, exists := c.Get("userApiKey")
	apiKey, _ := principal.(string)
	if !exists || strings.TrimSpace(apiKey) == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
		return
	}

	selection, enabled := s.usageAPISelection()
	if !enabled {
		// Behave as if the route does not exist while the feature is off.
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	var manager *coreauth.Manager
	if s.handlers != nil {
		manager = s.handlers.AuthManager
	}
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage service unavailable"})
		return
	}

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"providers": buildUsageAPIResponse(selection, usageAPIFiltersFromRequest(c), manager.List(), time.Now()),
	})
}

func (s *Server) usageAPISelection() (usageAPISelection, bool) {
	if s == nil {
		return usageAPISelection{}, false
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if s.cfg == nil || !s.cfg.UsageAPI.Enabled {
		return usageAPISelection{}, false
	}
	return newUsageAPISelection(s.cfg.UsageAPI), true
}

func normalizeUsageAPIProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func usageAPIProviderKey(auth *coreauth.Auth) string {
	provider := normalizeUsageAPIProvider(auth.Provider)
	if auth.Attributes != nil {
		if compatName := strings.TrimSpace(auth.Attributes["compat_name"]); compatName != "" {
			provider = normalizeUsageAPIProvider(compatName)
		}
	}
	if provider == "" {
		return "unknown"
	}
	return provider
}

type usageAPICredentialAggregate struct {
	success int64
	failed  int64
	recent  []coreauth.RecentRequestBucket
}

type usageAPIProviderAggregate struct {
	success     int64
	failed      int64
	recent      []coreauth.RecentRequestBucket
	credentials map[string]*usageAPICredentialAggregate
}

func buildUsageAPIResponse(selection usageAPISelection, filters usageAPIFilters, auths []*coreauth.Auth, now time.Time) map[string]usageAPIProviderUsage {
	aggregates := make(map[string]*usageAPIProviderAggregate)
	for _, auth := range auths {
		if auth == nil {
			continue
		}

		provider := usageAPIProviderKey(auth)
		_, providerExposed := selection.providers[provider]

		index := strings.TrimSpace(auth.EnsureIndex())
		publicName, credentialExposed := selection.credentials[index]
		if !providerExposed && !credentialExposed {
			continue
		}
		if len(filters.providers) > 0 {
			if _, ok := filters.providers[provider]; !ok {
				continue
			}
		}
		if len(filters.credentials) > 0 {
			if !credentialExposed {
				continue
			}
			if _, ok := filters.credentials[publicName]; !ok {
				continue
			}
		}

		aggregate, ok := aggregates[provider]
		if !ok {
			aggregate = &usageAPIProviderAggregate{
				credentials: make(map[string]*usageAPICredentialAggregate),
			}
			aggregates[provider] = aggregate
		}

		recent := auth.RecentRequestsSnapshot(now)
		aggregate.success += auth.Success
		aggregate.failed += auth.Failed
		aggregate.recent = mergeUsageAPIBuckets(aggregate.recent, recent)

		if !credentialExposed {
			continue
		}
		credential, ok := aggregate.credentials[publicName]
		if !ok {
			credential = &usageAPICredentialAggregate{}
			aggregate.credentials[publicName] = credential
		}
		credential.success += auth.Success
		credential.failed += auth.Failed
		credential.recent = mergeUsageAPIBuckets(credential.recent, recent)
	}

	out := make(map[string]usageAPIProviderUsage, len(aggregates))
	for provider, aggregate := range aggregates {
		entry := usageAPIProviderUsage{
			Success:        aggregate.success,
			Failed:         aggregate.failed,
			RecentRequests: ensureUsageAPIBuckets(aggregate.recent),
			Credentials:    make([]usageAPICredentialUsage, 0, len(aggregate.credentials)),
		}
		for name, credential := range aggregate.credentials {
			entry.Credentials = append(entry.Credentials, usageAPICredentialUsage{
				Name:           name,
				Success:        credential.success,
				Failed:         credential.failed,
				RecentRequests: ensureUsageAPIBuckets(credential.recent),
			})
		}
		sort.Slice(entry.Credentials, func(i, j int) bool {
			return entry.Credentials[i].Name < entry.Credentials[j].Name
		})
		out[provider] = entry
	}
	return out
}

func mergeUsageAPIBuckets(dst, src []coreauth.RecentRequestBucket) []coreauth.RecentRequestBucket {
	if len(dst) == 0 {
		// Copy so provider and credential aggregates never share a bucket slice.
		return append([]coreauth.RecentRequestBucket(nil), src...)
	}
	if len(src) == 0 {
		return dst
	}
	n := len(dst)
	if len(src) < n {
		n = len(src)
	}
	for i := range n {
		dst[i].Success += src[i].Success
		dst[i].Failed += src[i].Failed
	}
	return dst
}

func ensureUsageAPIBuckets(buckets []coreauth.RecentRequestBucket) []coreauth.RecentRequestBucket {
	if buckets == nil {
		return []coreauth.RecentRequestBucket{}
	}
	return buckets
}
