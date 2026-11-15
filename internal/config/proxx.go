package config

// ProxxConfig groups the fork-only (proxx) features under the top-level `proxx:` YAML key.
// Keeping every fork feature behind one root keeps upstream merges cheap: upstream never
// touches this file or this key. See docs/proxx/README.md.
type ProxxConfig struct {
	// ModelGroups defines virtual models that fan out to an ordered list of real models
	// across any configured providers, skipping members that are cooling down or failing.
	ModelGroups []ProxxModelGroup `yaml:"model-groups,omitempty" json:"model-groups,omitempty"`

	// Pricing configures per-token cost estimation and the price catalog.
	Pricing ProxxPricingConfig `yaml:"pricing,omitempty" json:"pricing,omitempty"`

	// Usage configures the in-process usage/spend aggregator exposed to the panel.
	Usage ProxxUsageConfig `yaml:"usage,omitempty" json:"usage,omitempty"`
}

// ProxxModelGroup is a client-visible virtual model backed by several real models.
type ProxxModelGroup struct {
	// Name is the model name clients send (e.g. "auto", "free", "fast").
	Name string `yaml:"name" json:"name"`
	// Strategy selects the member order: "ordered" (default, strict priority order)
	// or "round-robin" (rotate the starting member per request, then fail over in order).
	Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	// Models lists client-visible model names (registry IDs or configured aliases), in priority order.
	Models []string `yaml:"models" json:"models"`
	// Hidden keeps the group out of model listings while still routing it.
	Hidden bool `yaml:"hidden,omitempty" json:"hidden,omitempty"`
}

// ProxxPricingConfig controls cost resolution. Prices are USD per 1M tokens.
type ProxxPricingConfig struct {
	// Disabled turns off cost estimation and pricing in model listings.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	// Catalog selects the remote price catalog: "" uses the default (models.dev; embedded
	// snapshot when --local-model), "embed", "disabled", an http(s) URL, or an absolute file path.
	Catalog string `yaml:"catalog,omitempty" json:"catalog,omitempty"`
	// OverrideUpstream makes configured/catalog prices win over cost reported by the upstream
	// response (body usage cost or cost headers) for every model.
	OverrideUpstream bool `yaml:"override-upstream,omitempty" json:"override-upstream,omitempty"`
	// CostHeaders lists upstream response headers that carry a USD request cost.
	CostHeaders []string `yaml:"cost-headers,omitempty" json:"cost-headers,omitempty"`
	// Models holds per-model price overrides; they take precedence over the catalog.
	Models []ProxxModelPrice `yaml:"models,omitempty" json:"models,omitempty"`
}

// ProxxModelPrice is a per-model price entry. Nil fields fall back to the catalog.
type ProxxModelPrice struct {
	// Model is the upstream model ID or alias; a trailing "*" matches by prefix.
	Model string `yaml:"model" json:"model"`
	// Provider optionally restricts the entry to one provider key (e.g. "gemini", "openrouter").
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	// Input, Output, CacheRead, CacheWrite and Reasoning are USD per 1M tokens.
	Input      *float64 `yaml:"input,omitempty" json:"input,omitempty"`
	Output     *float64 `yaml:"output,omitempty" json:"output,omitempty"`
	CacheRead  *float64 `yaml:"cache-read,omitempty" json:"cache-read,omitempty"`
	CacheWrite *float64 `yaml:"cache-write,omitempty" json:"cache-write,omitempty"`
	Reasoning  *float64 `yaml:"reasoning,omitempty" json:"reasoning,omitempty"`
	// Override makes this entry win over upstream-reported cost for this model.
	Override bool `yaml:"override,omitempty" json:"override,omitempty"`
}

// ProxxUsageConfig configures the usage aggregator.
type ProxxUsageConfig struct {
	// Disabled turns off aggregation (independent of usage-statistics-enabled).
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	// File is the JSON snapshot path; empty stores proxx-usage.json next to the config file.
	File string `yaml:"file,omitempty" json:"file,omitempty"`
	// RecentLimit caps the in-memory recent-request ring buffer (default 500).
	RecentLimit int `yaml:"recent-limit,omitempty" json:"recent-limit,omitempty"`
}
