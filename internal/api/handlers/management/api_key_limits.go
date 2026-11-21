package management

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/apikeylimits"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprices"
)

type apiKeyModelUsage struct {
	Model        string `json:"model"`
	Requests     int64  `json:"requests"`
	Failed       int64  `json:"failed"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CachedTokens int64  `json:"cached_tokens"`
	// CacheWriteTokens is the part of InputTokens written to the prompt cache.
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	Tokens           int64   `json:"tokens"`
	Cost             float64 `json:"cost"`
	Streamed         int64   `json:"streamed"`
	// Latency and TTFT are sums in milliseconds over the requests that reported them.
	LatencyMs    int64 `json:"latency_ms"`
	LatencyCount int64 `json:"latency_count"`
	TTFTMs       int64 `json:"ttft_ms"`
	TTFTCount    int64 `json:"ttft_count"`
	// Efforts and Tiers list requests by reasoning effort and service tier
	// ("default" when the client did not set one), most requests first.
	Efforts []apiKeyDimUsage `json:"efforts"`
	Tiers   []apiKeyDimUsage `json:"tiers"`
}

// apiKeyDimUsage is a model's usage for one reasoning effort or service tier.
type apiKeyDimUsage struct {
	Name            string  `json:"name"`
	Requests        int64   `json:"requests"`
	Failed          int64   `json:"failed"`
	Tokens          int64   `json:"tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	Cost            float64 `json:"cost"`
	// LatencyMs is the summed latency of the LatencyCount requests that reported one.
	LatencyMs    int64 `json:"latency_ms"`
	LatencyCount int64 `json:"latency_count"`
}

func apiKeyDims(dims map[string]*apikeylimits.DimCounter) []apiKeyDimUsage {
	out := make([]apiKeyDimUsage, 0, len(dims))
	for name, d := range dims {
		if d == nil {
			continue
		}
		out = append(out, apiKeyDimUsage{
			Name:            name,
			Requests:        d.Requests,
			Failed:          d.Failed,
			Tokens:          d.Tokens,
			OutputTokens:    d.OutputTokens,
			ReasoningTokens: d.ReasoningTokens,
			Cost:            d.Cost,
			LatencyMs:       d.LatencyMs,
			LatencyCount:    d.LatencyCount,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Name < out[j].Name
	})
	return out
}

type apiKeyWindowUsage struct {
	Tokens   int64              `json:"tokens"`
	Cost     float64            `json:"cost"`
	Requests int64              `json:"requests"`
	Failed   int64              `json:"failed"`
	ResetAt  *time.Time         `json:"reset_at,omitempty"`
	Models   []apiKeyModelUsage `json:"models"`
	// Accounts lists usage by upstream credential, most requests first.
	Accounts []apiKeyAccountUsage `json:"accounts"`
	// Rejected counts requests refused by the key policy (not included in Requests).
	Rejected        int64            `json:"rejected"`
	RejectedReasons map[string]int64 `json:"rejected_reasons,omitempty"`
	LastRejectedAt  *time.Time       `json:"last_rejected_at,omitempty"`
}

type apiKeyHistoryBucket struct {
	Time   string             `json:"time"`
	Models []apiKeyModelUsage `json:"models"`
}

type apiKeyPolicyUsage struct {
	Key      string                       `json:"key"`
	Name     string                       `json:"name,omitempty"`
	Disabled bool                         `json:"disabled"`
	Expired  bool                         `json:"expired"`
	Usage    map[string]apiKeyWindowUsage `json:"usage"`
	// Daily (YYYY-MM-DD) and Hourly (YYYY-MM-DDTHH) buckets in server local time,
	// oldest first; empty buckets are omitted.
	Daily  []apiKeyHistoryBucket `json:"daily"`
	Hourly []apiKeyHistoryBucket `json:"hourly"`
}

func apiKeyHistory(buckets []apikeylimits.Bucket) []apiKeyHistoryBucket {
	out := make([]apiKeyHistoryBucket, 0, len(buckets))
	for _, b := range buckets {
		models := make(map[string]*apikeylimits.ModelCounter, len(b.Models))
		for name, m := range b.Models {
			mc := m.Clone()
			models[name] = &mc
		}
		out = append(out, apiKeyHistoryBucket{Time: b.Time, Models: apiKeyModelBreakdown(models)})
	}
	return out
}

// apiKeyModelBreakdown lists models, heaviest token users first.
func apiKeyModelBreakdown(counters map[string]*apikeylimits.ModelCounter) []apiKeyModelUsage {
	models := make([]apiKeyModelUsage, 0, len(counters))
	for name, m := range counters {
		if m == nil {
			continue
		}
		models = append(models, apiKeyModelUsage{
			Model:            name,
			Requests:         m.Requests,
			Failed:           m.Failed,
			InputTokens:      m.InputTokens,
			OutputTokens:     m.OutputTokens,
			CachedTokens:     m.CachedTokens,
			CacheWriteTokens: m.CacheWriteTokens,
			ReasoningTokens:  m.ReasoningTokens,
			Tokens:           m.Tokens,
			Cost:             m.Cost,
			Streamed:         m.Streamed,
			LatencyMs:        m.LatencyMs,
			LatencyCount:     m.LatencyCount,
			TTFTMs:           m.TTFTMs,
			TTFTCount:        m.TTFTCount,
			Efforts:          apiKeyDims(m.Efforts),
			Tiers:            apiKeyDims(m.Tiers),
		})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Tokens != models[j].Tokens {
			return models[i].Tokens > models[j].Tokens
		}
		if models[i].Requests != models[j].Requests {
			return models[i].Requests > models[j].Requests
		}
		return models[i].Model < models[j].Model
	})
	return models
}

// GetAPIKeyPolicyUsage reports per client key usage in the windows used by
// access.api-key-policies limits (server local time), plus an all-time "total"
// window, each broken down by model, plus daily and hourly per-model history.
func (h *Handler) GetAPIKeyPolicyUsage(c *gin.Context) {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	if cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	now := time.Now()
	store := apikeylimits.Default()
	accounts := h.accountDirectory()
	out := make([]apiKeyPolicyUsage, 0, len(cfg.APIKeys))
	for _, key := range cfg.APIKeys {
		if key == "" {
			continue
		}
		entry := apiKeyPolicyUsage{Key: key, Usage: make(map[string]apiKeyWindowUsage, 4)}
		if policy, ok := cfg.FindAPIKeyPolicy(key); ok {
			entry.Name = policy.Name
			entry.Disabled = policy.Disabled
			entry.Expired = policy.ExpiredAt(now)
		}
		for window, counter := range store.Usage(key) {
			usage := apiKeyWindowUsage{
				Tokens:   counter.Tokens,
				Cost:     counter.Cost,
				Requests: counter.Requests,
				Failed:   counter.Failed,
				Models:   apiKeyModelBreakdown(counter.Models),

				Accounts:        accounts.usage(counter.Accounts),
				Rejected:        counter.Rejected,
				RejectedReasons: counter.RejectedReasons,
				LastRejectedAt:  counter.LastRejectedAt,
			}
			if end := apikeylimits.WindowEnd(window, now); !end.IsZero() {
				usage.ResetAt = &end
			}
			entry.Usage[string(window)] = usage
		}
		daily, hourly := store.History(key)
		entry.Daily = apiKeyHistory(daily)
		entry.Hourly = apiKeyHistory(hourly)
		out = append(out, entry)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"keys":       out,
		"timezone":   now.Location().String(),
		"price_feed": modelprices.Default().Status(),
		// Models with a price in access.model-prices (those override the feed).
		"configured_prices": len(cfg.ModelPrices),
	})
}

// apiKeyAccountUsage is a key's usage through one upstream credential.
type apiKeyAccountUsage struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	Plan     string `json:"plan,omitempty"`
	apiKeyDimUsage
}

type accountInfo struct{ name, provider, plan string }

// accountDirectory maps credential IDs to display names for usage reports.
type accountDirectory map[string]accountInfo

func (h *Handler) accountDirectory() accountDirectory {
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	dir := make(accountDirectory)
	if manager == nil {
		return dir
	}
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		info := accountInfo{provider: auth.Provider, name: strings.TrimSpace(auth.Label)}
		// Never show API key material; OAuth credentials are named by their account.
		if kind, account := auth.AccountInfo(); !strings.EqualFold(kind, "api_key") && strings.TrimSpace(account) != "" {
			info.name = strings.TrimSpace(account)
		}
		if plan, ok := auth.Metadata["plan_type"].(string); ok {
			info.plan = strings.TrimSpace(plan)
		}
		if info.name == "" {
			info.name = auth.Provider
		}
		dir[auth.ID] = info
	}
	return dir
}

func (d accountDirectory) lookup(id string) accountInfo {
	if info, ok := d[id]; ok {
		return info
	}
	return accountInfo{name: id}
}

func (d accountDirectory) usage(dims map[string]*apikeylimits.DimCounter) []apiKeyAccountUsage {
	rows := apiKeyDims(dims)
	out := make([]apiKeyAccountUsage, 0, len(rows))
	for _, row := range rows {
		info := d.lookup(row.Name)
		out = append(out, apiKeyAccountUsage{ID: row.Name, Name: info.name, Provider: info.provider, Plan: info.plan, apiKeyDimUsage: row})
	}
	return out
}

type apiKeyRequestEntry struct {
	apikeylimits.RequestEntry
	// Key is the client key, as in the api-key-usage report, so the panel can match rows to keys.
	Key         string `json:"key,omitempty"`
	KeyName     string `json:"key_name,omitempty"`
	AccountName string `json:"account_name,omitempty"`
	Plan        string `json:"plan,omitempty"`
}

// GetAPIKeyRequests returns the latest served requests (metadata only), newest first.
func (h *Handler) GetAPIKeyRequests(c *gin.Context) {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	if cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	type keyInfo struct{ name, key string }
	keys := make(map[string]keyInfo, len(cfg.APIKeys))
	for _, key := range cfg.APIKeys {
		info := keyInfo{key: key}
		if policy, ok := cfg.FindAPIKeyPolicy(key); ok {
			info.name = policy.Name
		}
		keys[apikeylimits.Fingerprint(key)] = info
	}
	accounts := h.accountDirectory()
	recent := apikeylimits.Default().Recent(limit)
	out := make([]apiKeyRequestEntry, 0, len(recent))
	for _, entry := range recent {
		row := apiKeyRequestEntry{RequestEntry: entry}
		if info, ok := keys[entry.KeyFP]; ok {
			row.KeyName, row.Key = info.name, info.key
		}
		if entry.AuthID != "" {
			info := accounts.lookup(entry.AuthID)
			row.AccountName, row.Plan = info.name, info.plan
		}
		out = append(out, row)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"requests": out, "limit": apikeylimits.RecentLimit})
}
