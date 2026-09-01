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
	ProviderType            string         `json:"provider_type"`
	Name                    string         `json:"name,omitempty"`
	Priority                int            `json:"priority,omitempty"`
	Disabled                bool           `json:"disabled,omitempty"`
	Prefix                  string         `json:"prefix,omitempty"`
	APIKey                  string         `json:"api_key,omitempty"`
	BaseURL                 string         `json:"base_url,omitempty"`
	ProxyURL                string         `json:"proxy_url,omitempty"`
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
}

type upstreamProviderEntryReq struct {
	ID       int64  `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	APIKey   string `json:"api_key"`
	ProxyURL string `json:"proxy_url,omitempty"`
	// Weight is the optional proportional selection weight under
	// weighted-round-robin routing. nil/omitted falls back to the scheduler
	// default; the dashboard editor restricts user input to positive values
	// 1..MaxCredentialWeight. Encoded as `weight` only when set.
	Weight *int `json:"weight,omitempty"`
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
