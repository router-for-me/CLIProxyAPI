package config

import (
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// APIKeyPolicy restricts what a single client access key (access.api-keys) may use.
// Keys without a policy keep the default behavior and may use every credential.
type APIKeyPolicy struct {
	// Key is the client access key this policy applies to.
	Key string `yaml:"key" json:"key"`

	// Name is an optional human readable label shown in the management panel.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// Disabled rejects every request authenticated with this key.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`

	// AllowedAuths lists credential auth indexes (auth_index) this key may use.
	// Credential IDs (auth file names) are accepted as well. Empty means all credentials.
	AllowedAuths []string `yaml:"allowed-auths,omitempty" json:"allowed-auths,omitempty"`

	// AllowedModels lists model name patterns this key may request. "*" matches any
	// run of characters (for example "gpt-5*"). Empty means all models.
	AllowedModels []string `yaml:"allowed-models,omitempty" json:"allowed-models,omitempty"`

	// ExpiresAt is an RFC 3339 timestamp (or YYYY-MM-DD, meaning the end of that day in
	// server local time) after which the key is rejected. Empty means it never expires.
	ExpiresAt string `yaml:"expires-at,omitempty" json:"expires-at,omitempty"`

	// Limits caps token and cost usage per calendar window (server local time).
	Limits APIKeyLimits `yaml:"limits,omitempty" json:"limits,omitempty"`
}

// APIKeyLimits caps usage for one client key. Zero or negative values disable a limit.
// Costs are in USD and use upstream-reported cost or access.model-prices.
type APIKeyLimits struct {
	TokensPerDay   int64   `yaml:"tokens-per-day,omitempty" json:"tokens-per-day,omitempty"`
	TokensPerWeek  int64   `yaml:"tokens-per-week,omitempty" json:"tokens-per-week,omitempty"`
	TokensPerMonth int64   `yaml:"tokens-per-month,omitempty" json:"tokens-per-month,omitempty"`
	CostPerDay     float64 `yaml:"cost-per-day,omitempty" json:"cost-per-day,omitempty"`
	CostPerWeek    float64 `yaml:"cost-per-week,omitempty" json:"cost-per-week,omitempty"`
	CostPerMonth   float64 `yaml:"cost-per-month,omitempty" json:"cost-per-month,omitempty"`
}

// IsZero reports whether no limit is configured (used by yaml omitempty).
func (l APIKeyLimits) IsZero() bool {
	return l.TokensPerDay <= 0 && l.TokensPerWeek <= 0 && l.TokensPerMonth <= 0 &&
		l.CostPerDay <= 0 && l.CostPerWeek <= 0 && l.CostPerMonth <= 0
}

// ModelPrice is the USD price per one million tokens for models matching Model.
// "*" in Model matches any run of characters; the first matching entry wins.
type ModelPrice struct {
	Model       string  `yaml:"model" json:"model"`
	Input       float64 `yaml:"input,omitempty" json:"input,omitempty"`
	Output      float64 `yaml:"output,omitempty" json:"output,omitempty"`
	CachedInput float64 `yaml:"cached-input,omitempty" json:"cached-input,omitempty"`
	// CacheWrite prices prompt-cache writes; 0 bills them as regular input.
	CacheWrite float64 `yaml:"cache-write,omitempty" json:"cache-write,omitempty"`
}

// ModelPriceFeed configures the public price list used for models without a
// configured price. It is enabled by default with the LiteLLM price list.
type ModelPriceFeed struct {
	// Disabled turns the feed off; only ModelPrices and upstream-reported costs are used.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	// URL of a LiteLLM-format price JSON. Empty uses DefaultModelPriceFeedURL.
	URL string `yaml:"url,omitempty" json:"url,omitempty"`
	// RefreshHours between downloads. Values below 1 use 24.
	RefreshHours int `yaml:"refresh-hours,omitempty" json:"refresh-hours,omitempty"`
}

// DefaultModelPriceFeedURL is LiteLLM's public, community-maintained price list.
const DefaultModelPriceFeedURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// EffectiveURL returns the feed URL, falling back to the default.
func (f ModelPriceFeed) EffectiveURL() string {
	if url := strings.TrimSpace(f.URL); url != "" {
		return url
	}
	return DefaultModelPriceFeedURL
}

// RefreshInterval returns how often the feed is downloaded.
func (f ModelPriceFeed) RefreshInterval() time.Duration {
	if f.RefreshHours < 1 {
		return 24 * time.Hour
	}
	return time.Duration(f.RefreshHours) * time.Hour
}

// ExpiresAtTime parses ExpiresAt. ok is false when the key has no (valid) expiry.
func (p APIKeyPolicy) ExpiresAtTime() (time.Time, bool) {
	return parsePolicyExpiry(p.ExpiresAt)
}

func parsePolicyExpiry(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, true
	}
	if d, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		return d.AddDate(0, 0, 1), true
	}
	return time.Time{}, false
}

// MatchModelPattern reports whether model matches pattern, case-insensitively,
// where "*" matches any run of characters.
func MatchModelPattern(pattern, model string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	model = strings.ToLower(strings.TrimSpace(model))
	if pattern == "" || model == "" {
		return false
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == model
	}
	if !strings.HasPrefix(model, parts[0]) {
		return false
	}
	model = model[len(parts[0]):]
	last := len(parts) - 1
	for i := 1; i < last; i++ {
		idx := strings.Index(model, parts[i])
		if idx < 0 {
			return false
		}
		model = model[idx+len(parts[i]):]
	}
	return strings.HasSuffix(model, parts[last])
}

// SanitizeAPIKeyPolicies trims fields, removes entries without a key and merges
// duplicate entries for the same key (the first entry wins for scalar fields).
func (cfg *Config) SanitizeAPIKeyPolicies() {
	if cfg == nil {
		return
	}
	known := make(map[string]struct{}, len(cfg.APIKeys))
	for _, key := range cfg.APIKeys {
		known[strings.TrimSpace(key)] = struct{}{}
	}
	out := make([]APIKeyPolicy, 0, len(cfg.APIKeyPolicies))
	positions := make(map[string]int, len(cfg.APIKeyPolicies))
	for _, policy := range cfg.APIKeyPolicies {
		policy.Key = strings.TrimSpace(policy.Key)
		if policy.Key == "" {
			continue
		}
		policy.Name = strings.TrimSpace(policy.Name)
		policy.AllowedAuths = normalizePolicyList(policy.AllowedAuths)
		policy.AllowedModels = normalizePolicyList(policy.AllowedModels)
		policy.ExpiresAt = strings.TrimSpace(policy.ExpiresAt)
		if policy.ExpiresAt != "" {
			if _, ok := parsePolicyExpiry(policy.ExpiresAt); !ok {
				log.Warn("access.api-key-policies contains an invalid expires-at value; the key is treated as expired")
			}
		}
		if pos, ok := positions[policy.Key]; ok {
			merged := &out[pos]
			merged.Disabled = merged.Disabled || policy.Disabled
			merged.AllowedAuths = normalizePolicyList(append(merged.AllowedAuths, policy.AllowedAuths...))
			merged.AllowedModels = normalizePolicyList(append(merged.AllowedModels, policy.AllowedModels...))
			continue
		}
		if _, ok := known[policy.Key]; !ok {
			log.Warn("access.api-key-policies contains a policy for a key that is not listed in access.api-keys")
		}
		positions[policy.Key] = len(out)
		out = append(out, policy)
	}
	cfg.APIKeyPolicies = out

	prices := make([]ModelPrice, 0, len(cfg.ModelPrices))
	for _, price := range cfg.ModelPrices {
		price.Model = strings.TrimSpace(price.Model)
		if price.Model == "" || price.Input < 0 || price.Output < 0 || price.CachedInput < 0 || price.CacheWrite < 0 {
			continue
		}
		prices = append(prices, price)
	}
	cfg.ModelPrices = prices
}

// ExpiredAt reports whether the policy's expiry has passed at now. An expires-at value
// that cannot be parsed counts as expired so a typo never grants unlimited access.
func (p APIKeyPolicy) ExpiredAt(now time.Time) bool {
	if strings.TrimSpace(p.ExpiresAt) == "" {
		return false
	}
	t, ok := p.ExpiresAtTime()
	return !ok || !now.Before(t)
}

// AllowsModel reports whether any of the given model names matches AllowedModels.
func (p APIKeyPolicy) AllowsModel(models ...string) bool {
	if len(p.AllowedModels) == 0 {
		return true
	}
	for _, model := range models {
		for _, pattern := range p.AllowedModels {
			if MatchModelPattern(pattern, model) {
				return true
			}
		}
	}
	return false
}

// FindModelPrice returns the first configured price matching any of the model names.
func (cfg *SDKConfig) FindModelPrice(models ...string) (ModelPrice, bool) {
	if cfg == nil {
		return ModelPrice{}, false
	}
	for _, price := range cfg.ModelPrices {
		for _, model := range models {
			if MatchModelPattern(price.Model, model) {
				return price, true
			}
		}
	}
	return ModelPrice{}, false
}

// FindAPIKeyPolicy returns the policy configured for key, if any.
func (cfg *SDKConfig) FindAPIKeyPolicy(key string) (APIKeyPolicy, bool) {
	if cfg == nil || key == "" {
		return APIKeyPolicy{}, false
	}
	for _, policy := range cfg.APIKeyPolicies {
		if policy.Key == key {
			return policy, true
		}
	}
	return APIKeyPolicy{}, false
}

func normalizePolicyList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
