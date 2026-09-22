package management

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
)

type billingAPITokenListResponse struct {
	Enabled  bool                       `json:"enabled"`
	Mode     string                     `json:"mode"`
	Currency string                     `json:"currency"`
	Tokens   []billingAPITokenListEntry `json:"tokens"`
	Summary  billing.Summary            `json:"summary"`
}

type billingAPITokenListEntry struct {
	Index                  int                     `json:"index"`
	TokenID                string                  `json:"token_id"`
	UserID                 string                  `json:"user_id"`
	KeyHint                string                  `json:"key_hint"`
	Status                 string                  `json:"status"`
	ConfiguredBillingToken bool                    `json:"configured_billing_token"`
	Quota                  billing.QuotaConfig     `json:"quota"`
	QuotaSource            string                  `json:"quota_source"`
	Rate                   billing.RateLimitConfig `json:"rate"`
	Usage                  billing.TokenSummary    `json:"usage"`
	RemainingDailyNanos    *int64                  `json:"remaining_daily_nanos,omitempty"`
	RemainingMonthlyNanos  *int64                  `json:"remaining_monthly_nanos,omitempty"`
}

type billingAPITokenQuotaPatch struct {
	Quota *struct {
		DailyNanos       *int64 `json:"daily_nanos"`
		DailyNanosHyphen *int64 `json:"daily-nanos"`
		MonthlyNanos     *int64 `json:"monthly_nanos"`
		MonthlyNanosHyph *int64 `json:"monthly-nanos"`
	} `json:"quota"`
	DailyNanos       *int64 `json:"daily_nanos"`
	DailyNanosHyphen *int64 `json:"daily-nanos"`
	MonthlyNanos     *int64 `json:"monthly_nanos"`
	MonthlyNanosHyph *int64 `json:"monthly-nanos"`
}

// GetBillingAPITokens returns top-level inbound api-keys enriched with billing
// token IDs, quota settings, and accumulated billing usage. The legacy token ID
// is stable for the current api-keys order: api-keys[0] => legacy-1.
func (h *Handler) GetBillingAPITokens(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	h.mu.Lock()
	cfg := h.cfg
	service := h.billingService
	h.mu.Unlock()
	if cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config unavailable"})
		return
	}

	summary := billing.Summary{Enabled: cfg.Billing.Enabled, Mode: cfg.Billing.Mode, Currency: cfg.Billing.Currency, Users: map[string]billing.UserSummary{}}
	if service != nil {
		summary = service.Summary()
	}
	legacyUsage := summary.Users["legacy"]
	legacyUser, _ := findBillingUser(cfg.Billing.Users, "legacy")

	entries := make([]billingAPITokenListEntry, 0, len(cfg.APIKeys))
	for i, rawKey := range cfg.APIKeys {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			continue
		}
		tokenID := legacyTokenID(i)
		tokenCfg, configured := findBillingToken(legacyUser, tokenID, key)
		quota, source := effectiveLegacyQuota(cfg.Billing, legacyUser, tokenCfg, configured)
		rate := cfg.Billing.RateLimit
		status := "active"
		if configured {
			if strings.TrimSpace(tokenCfg.Status) != "" {
				status = strings.TrimSpace(tokenCfg.Status)
			}
			rate = mergeRateForManagement(rate, tokenCfg.Rate)
		} else if legacyUser != nil {
			rate = mergeRateForManagement(rate, legacyUser.Rate)
		}
		usage := legacyUsage.Tokens[tokenID]
		entry := billingAPITokenListEntry{
			Index:                  i,
			TokenID:                tokenID,
			UserID:                 "legacy",
			KeyHint:                hintManagementToken(key),
			Status:                 status,
			ConfiguredBillingToken: configured,
			Quota:                  quota,
			QuotaSource:            source,
			Rate:                   rate,
			Usage:                  usage,
		}
		if quota.DailyNanos > 0 {
			remaining := quota.DailyNanos - usage.CustomerCostNanos
			if remaining < 0 {
				remaining = 0
			}
			entry.RemainingDailyNanos = &remaining
		}
		if quota.MonthlyNanos > 0 {
			remaining := quota.MonthlyNanos - usage.CustomerCostNanos
			if remaining < 0 {
				remaining = 0
			}
			entry.RemainingMonthlyNanos = &remaining
		}
		entries = append(entries, entry)
	}

	c.JSON(http.StatusOK, billingAPITokenListResponse{
		Enabled:  cfg.Billing.Enabled,
		Mode:     cfg.Billing.Mode,
		Currency: cfg.Billing.Currency,
		Tokens:   entries,
		Summary:  summary,
	})
}

// PatchBillingAPITokenQuota persists a per-token quota override for a top-level
// inbound api-key by creating/updating billing.users[legacy].tokens[].
func (h *Handler) PatchBillingAPITokenQuota(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}
	tokenID := strings.TrimSpace(c.Param("token_id"))
	if tokenID == "" {
		tokenID = strings.TrimSpace(c.Query("token_id"))
	}
	index, ok := legacyIndexFromTokenID(tokenID)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token_id must look like legacy-N"})
		return
	}

	var body billingAPITokenQuotaPatch
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	dailyNanos, monthlyNanos, okQuota := quotaPatchValues(body)
	if !okQuota {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing quota.daily_nanos/daily-nanos or quota.monthly_nanos/monthly-nanos"})
		return
	}
	if (dailyNanos != nil && *dailyNanos < 0) || (monthlyNanos != nil && *monthlyNanos < 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "quota values must be >= 0"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config unavailable"})
		return
	}
	if index < 0 || index >= len(h.cfg.APIKeys) || strings.TrimSpace(h.cfg.APIKeys[index]) == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "api token not found"})
		return
	}
	key := strings.TrimSpace(h.cfg.APIKeys[index])
	user := ensureBillingUser(&h.cfg.Billing.Users, "legacy")
	token := ensureBillingToken(user, tokenID, key)
	if dailyNanos != nil {
		token.Quota.DailyNanos = *dailyNanos
	}
	if monthlyNanos != nil {
		token.Quota.MonthlyNanos = *monthlyNanos
	}
	token.Token = key
	token.Hint = hintManagementToken(key)
	if strings.TrimSpace(token.Name) == "" {
		token.Name = "legacy"
	}
	if strings.TrimSpace(token.Status) == "" {
		token.Status = "active"
	}
	h.persistLocked(c)
}

func legacyTokenID(index int) string { return fmt.Sprintf("legacy-%d", index+1) }

func legacyIndexFromTokenID(tokenID string) (int, bool) {
	if !strings.HasPrefix(tokenID, "legacy-") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(tokenID, "legacy-"))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n - 1, true
}

func hintManagementToken(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 12 {
		return token
	}
	return token[:8] + "..." + token[len(token)-4:]
}

func findBillingUser(users []billing.UserConfig, id string) (*billing.UserConfig, int) {
	for i := range users {
		if strings.EqualFold(strings.TrimSpace(users[i].ID), id) {
			return &users[i], i
		}
	}
	return nil, -1
}

func ensureBillingUser(users *[]billing.UserConfig, id string) *billing.UserConfig {
	if user, _ := findBillingUser(*users, id); user != nil {
		if strings.TrimSpace(user.Status) == "" {
			user.Status = "active"
		}
		return user
	}
	*users = append(*users, billing.UserConfig{ID: id, Name: "Legacy API keys", Status: "active"})
	return &(*users)[len(*users)-1]
}

func findBillingToken(user *billing.UserConfig, tokenID, plain string) (billing.TokenConfigEntry, bool) {
	if user == nil {
		return billing.TokenConfigEntry{}, false
	}
	for _, token := range user.Tokens {
		if strings.TrimSpace(token.ID) == tokenID || (plain != "" && strings.TrimSpace(token.Token) == plain) {
			return token, true
		}
	}
	return billing.TokenConfigEntry{}, false
}

func ensureBillingToken(user *billing.UserConfig, tokenID, plain string) *billing.TokenConfigEntry {
	for i := range user.Tokens {
		if strings.TrimSpace(user.Tokens[i].ID) == tokenID || (plain != "" && strings.TrimSpace(user.Tokens[i].Token) == plain) {
			if strings.TrimSpace(user.Tokens[i].ID) == "" {
				user.Tokens[i].ID = tokenID
			}
			return &user.Tokens[i]
		}
	}
	user.Tokens = append(user.Tokens, billing.TokenConfigEntry{ID: tokenID, Name: "legacy", Token: plain, Hint: hintManagementToken(plain), Status: "active"})
	return &user.Tokens[len(user.Tokens)-1]
}

func effectiveLegacyQuota(cfg billing.Config, user *billing.UserConfig, token billing.TokenConfigEntry, configured bool) (billing.QuotaConfig, string) {
	quota := cfg.Quota
	source := "global"
	if user != nil {
		quota = mergeQuotaForManagement(quota, user.Quota)
		if user.Quota.DailyNanos != 0 || user.Quota.MonthlyNanos != 0 {
			source = "user"
		}
	}
	if configured {
		quota = mergeQuotaForManagement(quota, token.Quota)
		if token.Quota.DailyNanos != 0 || token.Quota.MonthlyNanos != 0 {
			source = "token"
		}
	}
	return quota, source
}

func mergeQuotaForManagement(base, override billing.QuotaConfig) billing.QuotaConfig {
	if override.DailyNanos != 0 {
		base.DailyNanos = override.DailyNanos
	}
	if override.MonthlyNanos != 0 {
		base.MonthlyNanos = override.MonthlyNanos
	}
	return base
}

func mergeRateForManagement(base, override billing.RateLimitConfig) billing.RateLimitConfig {
	if override.RPM != 0 {
		base.RPM = override.RPM
	}
	if override.Concurrent != 0 {
		base.Concurrent = override.Concurrent
	}
	return base
}

func quotaPatchValues(body billingAPITokenQuotaPatch) (dailyNanos, monthlyNanos *int64, ok bool) {
	applyDaily := func(v *int64) {
		if v != nil {
			dailyNanos = v
			ok = true
		}
	}
	applyMonthly := func(v *int64) {
		if v != nil {
			monthlyNanos = v
			ok = true
		}
	}
	applyDaily(body.DailyNanos)
	applyDaily(body.DailyNanosHyphen)
	applyMonthly(body.MonthlyNanos)
	applyMonthly(body.MonthlyNanosHyph)
	if body.Quota != nil {
		applyDaily(body.Quota.DailyNanos)
		applyDaily(body.Quota.DailyNanosHyphen)
		applyMonthly(body.Quota.MonthlyNanos)
		applyMonthly(body.Quota.MonthlyNanosHyph)
	}
	return dailyNanos, monthlyNanos, ok
}
