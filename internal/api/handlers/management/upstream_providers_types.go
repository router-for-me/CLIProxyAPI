package management

import "time"

// upstreamProviderReq is the JSON body for POST/PUT
// /v0/management/upstream-providers. It mirrors store.UpstreamProvider; the
// id/timestamps are ignored on write (set by the DB). Child collections
// (models, headers, excluded_models) are replaced wholesale on update. The
// api_key_entries collection is synchronized by stable child-row id (see
// upstream_providers.go) so callers must round-trip the entry id to preserve
// row identity across edits.
type upstreamProviderReq struct {
	ProviderType string `json:"provider_type"`
	Name         string `json:"name,omitempty"`
	Priority     int    `json:"priority,omitempty"`
	// RoutingStrategy is the optional in-pool selection strategy for
	// entry-bearing providers. Accepts the canonical values plus the Model
	// Routes aliases (priority, failover) — canonicalized in toUpstreamProvider.
	RoutingStrategy string `json:"routing_strategy,omitempty"`
	// CircuitBreaker opts the row's pool into the pool-level circuit breaker
	// (design G3): true feeds 408/5xx failures into the pool-wide breaker and
	// subjects the pool's auths to its blocking. false (default) keeps
	// failures scoped to per-auth cooldowns.
	CircuitBreaker bool   `json:"circuit_breaker,omitempty"`
	Disabled       bool   `json:"disabled,omitempty"`
	Prefix         string `json:"prefix,omitempty"`
	APIKey         string `json:"api_key,omitempty"`
	BaseURL        string `json:"base_url,omitempty"`
	ProxyURL       string `json:"proxy_url,omitempty"`
	// ProxyPoolID, when non-nil, binds the row to a proxy_pools entry; the
	// renderer resolves it into the concrete ProxyURL (or RelayBaseURL).
	ProxyPoolID             *int64         `json:"proxy_pool_id,omitempty"`
	Label                   string         `json:"label,omitempty"`
	Email                   string         `json:"email,omitempty"`
	FileName                string         `json:"file_name,omitempty"`
	SourceBackend           string         `json:"source_backend,omitempty"`
	Websockets              bool           `json:"websockets,omitempty"`
	RebuildMidSystemMessage bool           `json:"rebuild_mid_system_message,omitempty"`
	ExperimentalCCHSigning  bool           `json:"experimental_cch_signing,omitempty"`
	CloakMode               string         `json:"cloak_mode,omitempty"`
	CloakStrictMode         bool           `json:"cloak_strict_mode,omitempty"`
	CloakSensitiveWords     []string       `json:"cloak_sensitive_words,omitempty"`
	CloakCacheUserID        *bool          `json:"cloak_cache_user_id,omitempty"`
	TokenAccessToken        string         `json:"token_access_token,omitempty"`
	TokenRefreshToken       string         `json:"token_refresh_token,omitempty"`
	TokenTokenType          string         `json:"token_token_type,omitempty"`
	TokenExpiry             string         `json:"token_expiry,omitempty"`
	TokenExpired            *bool          `json:"token_expired,omitempty"`
	TokenScope              string         `json:"token_scope,omitempty"`
	ExtraConfig             map[string]any `json:"extra_config,omitempty"`
	// AutoDisableErrorCodes lists the upstream error codes that auto-disable an
	// entry (rendered onto the provider row). A pointer with omitempty keeps
	// "absent from the PUT" (nil → store preserves existing codes) distinct
	// from "explicitly cleared" (non-nil, possibly empty slice → store clears).
	// Empty-but-present = feature on with no codes yet.
	AutoDisableErrorCodes *[]string `json:"auto_disable_error_codes,omitempty"`
	// AutoDisableCooldownSeconds is the auto-re-enable cooldown. nil = manual
	// re-enable only.
	AutoDisableCooldownSeconds *int `json:"auto_disable_cooldown_seconds,omitempty"`

	// Child collections.
	Models         []upstreamProviderModelReq `json:"models,omitempty"`
	Headers        map[string]string          `json:"headers,omitempty"`
	ExcludedModels []string                   `json:"excluded_models,omitempty"`
	APIKeyEntries  []upstreamProviderEntryReq `json:"api_key_entries,omitempty"`
}

type upstreamProviderModelReq struct {
	Name             string         `json:"name"`
	Alias            string         `json:"alias,omitempty"`
	DisplayName      string         `json:"display_name,omitempty"`
	ForceMapping     bool           `json:"force_mapping,omitempty"`
	Fork             bool           `json:"fork,omitempty"`
	Image            bool           `json:"image,omitempty"`
	InputModalities  []string       `json:"input_modalities,omitempty"`
	OutputModalities []string       `json:"output_modalities,omitempty"`
	Thinking         map[string]any `json:"thinking,omitempty"`
	// WireFormat selects the upstream protocol for this model ("openai"
	// default or "anthropic"); only meaningful for opencode-go rows.
	WireFormat string `json:"wire_format,omitempty"`
}

type upstreamProviderEntryReq struct {
	ID       int64  `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	APIKey   string `json:"api_key"`
	ProxyURL string `json:"proxy_url,omitempty"`
	// ProxyPoolID, when non-nil, overrides the row-level proxy pool binding.
	ProxyPoolID *int64 `json:"proxy_pool_id,omitempty"`
	// Weight is the optional proportional selection weight under
	// weighted-round-robin routing. nil/omitted falls back to the scheduler
	// default; the dashboard editor restricts user input to positive values
	// 1..MaxCredentialWeight. Encoded as `weight` only when set.
	Weight *int `json:"weight,omitempty"`

	// Priority is the optional selection tier for this entry. nil = inherit
	// the row-level priority.
	Priority *int `json:"priority,omitempty"`

	// Disabled excludes this entry from routing without deleting it. The
	// renderer skips disabled entries when rendering config.yaml.
	Disabled bool `json:"disabled,omitempty"`

	// MaxConcurrent is the per-entry in-flight request cap. nil/0 = unlimited.
	// Mirrors store.UpstreamProviderAPIKey.MaxConcurrent.
	MaxConcurrent *int `json:"max_concurrent,omitempty"`
	// MaxWaitMs is the per-entry wait budget in milliseconds before an
	// eligible-but-full entry fails over. nil/0 = default wait. Mirrors
	// store.UpstreamProviderAPIKey.MaxWaitMs.
	MaxWaitMs *int `json:"max_wait_ms,omitempty"`

	// AutoDisabled is the runtime auto-disable flag. Plan decision #7: the
	// dashboard re-enables an auto-disabled entry by PUTting false here; the
	// store always writes it (false clears the runtime flag).
	AutoDisabled bool `json:"auto_disabled,omitempty"`
	// AutoDisabledAt is when the entry was auto-disabled, as an RFC3339
	// string (matches the TokenExpiry convention; parsed via parseRFC3339).
	AutoDisabledAt string `json:"auto_disabled_at,omitempty"`
	// AutoDisabledReason is the short human-readable reason (e.g. the matched
	// upstream error code).
	AutoDisabledReason string `json:"auto_disabled_reason,omitempty"`
}

// parseRFC3339 parses an RFC3339 timestamp string, returning ok=false on
// failure or empty input.
func parseRFC3339(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}
