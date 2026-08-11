package management

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const (
	minUsageSettingsRetentionDays  = 1
	maxUsageSettingsPricingRules   = 500
	maxUsageSettingsPatternLength  = 256
	maxUsageSettingsCurrencyLength = 16
	maxUsageSettingsVersionLength  = 96
	maxUsageSettingsRateLength     = 64
)

var usageSettingsPlainDecimalPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

type usageBillingSettingsPricing struct {
	Currency string                    `json:"currency"`
	Version  string                    `json:"version"`
	Rules    []config.UsagePricingRule `json:"rules"`
}

type usageBillingSettingsLimits struct {
	MinRetentionDays  int `json:"min_retention_days"`
	MaxRetentionDays  int `json:"max_retention_days"`
	MaxRules          int `json:"max_rules"`
	MaxPatternLength  int `json:"max_pattern_length"`
	MaxCurrencyLength int `json:"max_currency_length"`
	MaxVersionLength  int `json:"max_version_length"`
	MaxRateLength     int `json:"max_rate_length"`
}

type usageBillingSettingsResponse struct {
	Enabled       bool                        `json:"enabled"`
	RetentionDays int                         `json:"retention_days"`
	Pricing       usageBillingSettingsPricing `json:"pricing"`
	Revision      string                      `json:"revision"`
	Limits        usageBillingSettingsLimits  `json:"limits"`
}

type usageBillingSettingsInput struct {
	Enabled          *bool                             `json:"enabled"`
	RetentionDays    *int                              `json:"retention_days"`
	Pricing          *usageBillingSettingsPricingInput `json:"pricing"`
	ExpectedRevision *string                           `json:"expected_revision"`
}

type usageBillingSettingsPricingInput struct {
	Currency *string                    `json:"currency"`
	Version  *string                    `json:"version"`
	Rules    *[]config.UsagePricingRule `json:"rules"`
}

// GetUsageBillingSettings returns only the secret-safe settings required by
// the usage dashboard. It deliberately does not return the full Config.
func (h *Handler) GetUsageBillingSettings(c *gin.Context) {
	h.mu.Lock()
	settings := usageBillingSettingsSnapshotLocked(h.cfg)
	h.mu.Unlock()
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, settings)
}

// PutUsageBillingSettings updates selected usage and billing settings.
func (h *Handler) PutUsageBillingSettings(c *gin.Context) {
	h.updateUsageBillingSettings(c)
}

// PatchUsageBillingSettings updates selected usage and billing settings.
func (h *Handler) PatchUsageBillingSettings(c *gin.Context) {
	h.updateUsageBillingSettings(c)
}

func (h *Handler) updateUsageBillingSettings(c *gin.Context) {
	var input usageBillingSettingsInput
	if errBindJSON := c.ShouldBindJSON(&input); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	hasPricingUpdate := input.Pricing != nil && (input.Pricing.Currency != nil || input.Pricing.Version != nil || input.Pricing.Rules != nil)
	if input.Enabled == nil && input.RetentionDays == nil && !hasPricingUpdate {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing settings fields"})
		return
	}
	if input.ExpectedRevision == nil || strings.TrimSpace(*input.ExpectedRevision) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expected_revision is required"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	current := usageBillingSettingsSnapshotLocked(h.cfg)
	if strings.TrimSpace(*input.ExpectedRevision) != current.Revision {
		c.JSON(http.StatusConflict, gin.H{
			"error":    "usage settings changed; refresh and retry",
			"revision": current.Revision,
		})
		return
	}

	next := current
	if input.Enabled != nil {
		next.Enabled = *input.Enabled
	}
	if input.RetentionDays != nil {
		next.RetentionDays = *input.RetentionDays
	}
	if hasPricingUpdate {
		if input.Pricing.Currency != nil {
			next.Pricing.Currency = *input.Pricing.Currency
		}
		if input.Pricing.Version != nil {
			next.Pricing.Version = *input.Pricing.Version
		}
		if input.Pricing.Rules != nil {
			next.Pricing.Rules = cloneUsagePricingRules(*input.Pricing.Rules)
		}
	}

	if errValidate := normalizeAndValidateUsageBillingSettings(&next); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errValidate.Error()})
		return
	}

	previousEnabled := h.cfg.UsageStatisticsEnabled
	previousRetentionDays := h.cfg.UsageStatisticsRetentionDays
	previousPricing := config.UsagePricingConfig{
		Currency: h.cfg.UsagePricing.Currency,
		Version:  h.cfg.UsagePricing.Version,
		Rules:    cloneUsagePricingRules(h.cfg.UsagePricing.Rules),
	}
	h.cfg.UsageStatisticsEnabled = next.Enabled
	h.cfg.UsageStatisticsRetentionDays = next.RetentionDays
	h.cfg.UsagePricing = config.UsagePricingConfig{
		Currency: next.Pricing.Currency,
		Version:  next.Pricing.Version,
		Rules:    cloneUsagePricingRules(next.Pricing.Rules),
	}
	if !h.persistLocked(c) {
		h.cfg.UsageStatisticsEnabled = previousEnabled
		h.cfg.UsageStatisticsRetentionDays = previousRetentionDays
		h.cfg.UsagePricing = previousPricing
	}
}

func usageBillingSettingsSnapshotLocked(cfg *config.Config) usageBillingSettingsResponse {
	settings := usageBillingSettingsResponse{
		RetentionDays: config.DefaultUsageStatisticsRetentionDays,
		Pricing: usageBillingSettingsPricing{
			Currency: config.DefaultUsagePricingCurrency,
			Version:  config.DefaultUsagePricingVersion,
			Rules:    []config.UsagePricingRule{},
		},
		Limits: usageBillingSettingsLimits{
			MinRetentionDays:  minUsageSettingsRetentionDays,
			MaxRetentionDays:  config.MaxUsageStatisticsRetentionDays,
			MaxRules:          maxUsageSettingsPricingRules,
			MaxPatternLength:  maxUsageSettingsPatternLength,
			MaxCurrencyLength: maxUsageSettingsCurrencyLength,
			MaxVersionLength:  maxUsageSettingsVersionLength,
			MaxRateLength:     maxUsageSettingsRateLength,
		},
	}
	if cfg != nil {
		settings.Enabled = cfg.UsageStatisticsEnabled
		settings.RetentionDays = cfg.UsageStatisticsRetentionDays
		settings.Pricing = usageBillingSettingsPricing{
			Currency: cfg.UsagePricing.Currency,
			Version:  cfg.UsagePricing.Version,
			Rules:    cloneUsagePricingRules(cfg.UsagePricing.Rules),
		}
	}
	if settings.Pricing.Rules == nil {
		settings.Pricing.Rules = []config.UsagePricingRule{}
	}
	normalizeUsageBillingSettings(&settings)
	settings.Revision = usageBillingSettingsRevision(settings)
	return settings
}

func normalizeAndValidateUsageBillingSettings(settings *usageBillingSettingsResponse) error {
	if settings == nil {
		return fmt.Errorf("settings are required")
	}
	if settings.RetentionDays < minUsageSettingsRetentionDays || settings.RetentionDays > config.MaxUsageStatisticsRetentionDays {
		return fmt.Errorf("retention_days must be between %d and %d", minUsageSettingsRetentionDays, config.MaxUsageStatisticsRetentionDays)
	}
	if len(settings.Pricing.Rules) > maxUsageSettingsPricingRules {
		return fmt.Errorf("pricing.rules must contain at most %d rules", maxUsageSettingsPricingRules)
	}
	if errText := validateUsageSettingsDisplayText("pricing.currency", settings.Pricing.Currency, maxUsageSettingsCurrencyLength); errText != nil {
		return errText
	}
	if errText := validateUsageSettingsDisplayText("pricing.version", settings.Pricing.Version, maxUsageSettingsVersionLength); errText != nil {
		return errText
	}
	for index := range settings.Pricing.Rules {
		rule := &settings.Pricing.Rules[index]
		for _, pattern := range []struct {
			name  string
			value string
		}{
			{name: "provider", value: rule.Provider},
			{name: "model", value: rule.Model},
			{name: "service-tier", value: rule.ServiceTier},
		} {
			if errText := validateUsageSettingsDisplayText(fmt.Sprintf("pricing.rules[%d].%s", index, pattern.name), pattern.value, maxUsageSettingsPatternLength); errText != nil {
				return errText
			}
		}

		hasRate := false
		for _, rate := range []struct {
			name  string
			value string
		}{
			{name: "input-per-million", value: rule.InputPerMillion},
			{name: "output-per-million", value: rule.OutputPerMillion},
			{name: "cache-read-per-million", value: rule.CacheReadPerMillion},
			{name: "cache-write-per-million", value: rule.CacheWritePerMillion},
		} {
			value := strings.TrimSpace(rate.value)
			if value == "" {
				continue
			}
			hasRate = true
			if len(value) > maxUsageSettingsRateLength || !usageSettingsPlainDecimalPattern.MatchString(value) {
				return fmt.Errorf("pricing.rules[%d].%s must be a nonnegative plain decimal with at most %d characters", index, rate.name, maxUsageSettingsRateLength)
			}
		}
		if !hasRate {
			return fmt.Errorf("pricing.rules[%d] must define at least one rate", index)
		}
	}

	normalizeUsageBillingSettings(settings)
	return nil
}

func normalizeUsageBillingSettings(settings *usageBillingSettingsResponse) {
	if settings.RetentionDays <= 0 {
		settings.RetentionDays = config.DefaultUsageStatisticsRetentionDays
	} else if settings.RetentionDays > config.MaxUsageStatisticsRetentionDays {
		settings.RetentionDays = config.MaxUsageStatisticsRetentionDays
	}
	settings.Pricing.Currency = strings.ToUpper(strings.TrimSpace(settings.Pricing.Currency))
	if settings.Pricing.Currency == "" {
		settings.Pricing.Currency = config.DefaultUsagePricingCurrency
	}
	settings.Pricing.Version = strings.TrimSpace(settings.Pricing.Version)
	if settings.Pricing.Version == "" {
		settings.Pricing.Version = config.DefaultUsagePricingVersion
	}
	if settings.Pricing.Rules == nil {
		settings.Pricing.Rules = []config.UsagePricingRule{}
	}
	for index := range settings.Pricing.Rules {
		rule := &settings.Pricing.Rules[index]
		rule.Provider = normalizeUsageSettingsPattern(rule.Provider)
		rule.Model = normalizeUsageSettingsPattern(rule.Model)
		rule.ServiceTier = normalizeUsageSettingsPattern(rule.ServiceTier)
		rule.InputPerMillion = strings.TrimSpace(rule.InputPerMillion)
		rule.OutputPerMillion = strings.TrimSpace(rule.OutputPerMillion)
		cacheInputRate := strings.TrimSpace(rule.CacheReadPerMillion)
		if cacheInputRate == "" {
			cacheInputRate = strings.TrimSpace(rule.CacheWritePerMillion)
		}
		rule.CacheReadPerMillion = cacheInputRate
		rule.CacheWritePerMillion = cacheInputRate
	}
}

func normalizeUsageSettingsPattern(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "*"
	}
	return value
}

func validateUsageSettingsDisplayText(name, value string, maxLength int) error {
	if utf8.RuneCountInString(strings.TrimSpace(value)) > maxLength {
		return fmt.Errorf("%s must contain at most %d characters", name, maxLength)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("%s must not contain control characters", name)
		}
	}
	return nil
}

func usageBillingSettingsRevision(settings usageBillingSettingsResponse) string {
	payload := struct {
		Enabled       bool                        `json:"enabled"`
		RetentionDays int                         `json:"retention_days"`
		Pricing       usageBillingSettingsPricing `json:"pricing"`
	}{
		Enabled:       settings.Enabled,
		RetentionDays: settings.RetentionDays,
		Pricing: usageBillingSettingsPricing{
			Currency: settings.Pricing.Currency,
			Version:  settings.Pricing.Version,
			Rules:    cloneUsagePricingRules(settings.Pricing.Rules),
		},
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func cloneUsagePricingRules(rules []config.UsagePricingRule) []config.UsagePricingRule {
	if len(rules) == 0 {
		return []config.UsagePricingRule{}
	}
	cloned := make([]config.UsagePricingRule, len(rules))
	copy(cloned, rules)
	return cloned
}
