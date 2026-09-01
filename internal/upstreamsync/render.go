// Package upstreamsync bridges the normalized upstream_providers PostgreSQL
// table and the existing file-based runtime artifacts (config.yaml provider
// sections + auth-dir JSON files).
//
// The routing/client pipeline (internal/watcher, BuildAPIKeyClients, the
// OAuth synthesizer, coreauth.Auth) reads from the *Config struct + auth-dir
// JSON. Rather than rewrite that pipeline, this package re-renders those
// artifacts from the normalized rows on demand so the existing hot-reload
// path picks up changes. It is the only place allowed to import both
// internal/config and internal/store, breaking what would otherwise be an
// import cycle (store does not import config).
package upstreamsync

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// ProviderType constants. API-key providers keep their config.yaml key name;
// OAuth/file-backed auths use the "oauth:<channel>" form.
const (
	TypeGeminiAPIKey        = "gemini-api-key"
	TypeInteractionsAPIKey  = "interactions-api-key"
	TypeCodexAPIKey         = "codex-api-key"
	TypeXAIAPIKey           = "xai-api-key"
	TypeClaudeAPIKey        = "claude-api-key"
	TypeOpenAICompatibility = "openai-compatibility"
	TypeVertexAPIKey        = "vertex-api-key"

	TypeOAuthClaude      = "oauth:claude"
	TypeOAuthCodex       = "oauth:codex"
	TypeOAuthKimi        = "oauth:kimi"
	TypeOAuthXAI         = "oauth:xai"
	TypeOAuthVertex      = "oauth:vertex"
	TypeOAuthAIStudio    = "oauth:aistudio"
	TypeOAuthAntigravity = "oauth:antigravity"
)

// IsOAuth reports whether providerType is an OAuth/file-backed auth.
func IsOAuth(providerType string) bool {
	return strings.HasPrefix(providerType, "oauth:")
}

// OAuthChannel returns the channel name for an oauth:* provider type
// (without the "oauth:" prefix). Returns "" for non-oauth types.
func OAuthChannel(providerType string) string {
	if !IsOAuth(providerType) {
		return ""
	}
	return strings.TrimPrefix(providerType, "oauth:")
}

// RenderConfig rebuilds the per-provider slices of a Config from the
// normalized upstream_providers rows. Only the provider list fields
// (GeminiKey, CodexKey, etc.) are populated; all other Config fields are
// left as their zero values. Callers should merge this into the live
// snapshot's provider fields rather than replacing the whole config.
func RenderConfig(providers []store.UpstreamProvider) config.Config {
	var cfg config.Config
	for _, p := range providers {
		switch p.ProviderType {
		case TypeGeminiAPIKey:
			cfg.GeminiKey = append(cfg.GeminiKey, geminiKeyFromProvider(p))
		case TypeInteractionsAPIKey:
			cfg.InteractionsKey = append(cfg.InteractionsKey, geminiKeyFromProvider(p))
		case TypeCodexAPIKey:
			cfg.CodexKey = append(cfg.CodexKey, codexKeyFromProvider(p))
		case TypeXAIAPIKey:
			cfg.XAIKey = append(cfg.XAIKey, codexKeyFromProvider(p))
		case TypeClaudeAPIKey:
			cfg.ClaudeKey = append(cfg.ClaudeKey, claudeKeyFromProvider(p)...)
		case TypeOpenAICompatibility:
			cfg.OpenAICompatibility = append(cfg.OpenAICompatibility, openAICompatFromProvider(p))
		case TypeVertexAPIKey:
			cfg.VertexCompatAPIKey = append(cfg.VertexCompatAPIKey, vertexKeyFromProvider(p))
		}
	}
	return cfg
}

func geminiKeyFromProvider(p store.UpstreamProvider) config.GeminiKey {
	k := config.GeminiKey{
		APIKey:             p.APIKey,
		Priority:           p.Priority,
		Prefix:             p.Prefix,
		BaseURL:            p.BaseURL,
		ProxyURL:           p.ProxyURL,
		Headers:            p.Headers,
		ExcludedModels:     p.ExcludedModels,
		UpstreamProviderID: p.ID,
	}
	for _, m := range p.Models {
		k.Models = append(k.Models, config.GeminiModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
		})
	}
	if v, ok := p.ExtraConfig["disable_cooling"].(bool); ok {
		k.DisableCooling = v
	}
	return k
}

func codexKeyFromProvider(p store.UpstreamProvider) config.CodexKey {
	k := config.CodexKey{
		APIKey:             p.APIKey,
		Priority:           p.Priority,
		Prefix:             p.Prefix,
		BaseURL:            p.BaseURL,
		Websockets:         p.Websockets,
		ProxyURL:           p.ProxyURL,
		Headers:            p.Headers,
		ExcludedModels:     p.ExcludedModels,
		UpstreamProviderID: p.ID,
	}
	for _, m := range p.Models {
		k.Models = append(k.Models, config.CodexModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
		})
	}
	if v, ok := p.ExtraConfig["disable_cooling"].(bool); ok {
		k.DisableCooling = v
	}
	return k
}

func claudeKeyFromProvider(p store.UpstreamProvider) []config.ClaudeKey {
	if len(p.APIKeyEntries) == 0 {
		// Legacy fallback: a Claude provider row that predates the modern
		// multi-entry editor still renders as exactly one config.ClaudeKey
		// sourced from the parent's api_key + provider proxy, with the
		// entry ID left zero.
		return []config.ClaudeKey{buildClaudeKey(p, store.UpstreamProviderAPIKey{
			APIKey:   p.APIKey,
			ProxyURL: p.ProxyURL,
		})}
	}
	keys := make([]config.ClaudeKey, 0, len(p.APIKeyEntries))
	for _, e := range p.APIKeyEntries {
		keys = append(keys, buildClaudeKey(p, e))
	}
	return keys
}

// buildClaudeKey projects one store.UpstreamProvider + child entry into a
// config.ClaudeKey, copying all provider-level fields onto the item and
// choosing the entry proxy when set or the provider proxy when blank.
// The entry proxy never overrides the provider proxy unless non-empty, so
// operators can leave the column null and inherit the row default.
func buildClaudeKey(p store.UpstreamProvider, e store.UpstreamProviderAPIKey) config.ClaudeKey {
	proxyURL := e.ProxyURL
	if proxyURL == "" {
		proxyURL = p.ProxyURL
	}
	k := config.ClaudeKey{
		APIKey:                  e.APIKey,
		Weight:                  e.Weight,
		Priority:                p.Priority,
		Prefix:                  p.Prefix,
		BaseURL:                 p.BaseURL,
		ProxyURL:                proxyURL,
		Headers:                 p.Headers,
		ExcludedModels:          p.ExcludedModels,
		RebuildMidSystemMessage: p.RebuildMidSystemMessage,
		ExperimentalCCHSigning:  p.ExperimentalCCHSigning,
		UpstreamProviderID:      p.ID,
		UpstreamProviderEntryID: e.ID,
	}
	for _, m := range p.Models {
		k.Models = append(k.Models, config.ClaudeModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
		})
	}
	if v, ok := p.ExtraConfig["disable_cooling"].(bool); ok {
		k.DisableCooling = v
	}
	if p.CloakMode != "" || p.CloakStrictMode || len(p.CloakSensitiveWords) > 0 || p.CloakCacheUserID != nil {
		cloak := &config.CloakConfig{
			Mode:           p.CloakMode,
			StrictMode:     p.CloakStrictMode,
			SensitiveWords: p.CloakSensitiveWords,
			CacheUserID:    p.CloakCacheUserID,
		}
		k.Cloak = cloak
	}
	return k
}

func openAICompatFromProvider(p store.UpstreamProvider) config.OpenAICompatibility {
	k := config.OpenAICompatibility{
		Name:     p.Name,
		Priority: p.Priority,
		Disabled: p.Disabled,
		Prefix:   p.Prefix,
		BaseURL:  p.BaseURL,
		Headers:  p.Headers,
	}
	for _, e := range p.APIKeyEntries {
		k.APIKeyEntries = append(k.APIKeyEntries, config.OpenAICompatibilityAPIKey{
			APIKey:                  e.APIKey,
			Name:                    e.Name,
			UpstreamProviderEntryID: e.ID,
			Weight:                  e.Weight,
			ProxyURL:                e.ProxyURL,
		})
	}
	for _, m := range p.Models {
		mm := config.OpenAICompatibilityModel{
			Name:             m.Name,
			Alias:            m.Alias,
			DisplayName:      m.DisplayName,
			ForceMapping:     m.ForceMapping,
			Image:            m.Image,
			InputModalities:  m.InputModalities,
			OutputModalities: m.OutputModalities,
		}
		if len(m.Thinking) > 0 {
			mm.Thinking = decodeThinkingSupport(m.Thinking)
		}
		k.Models = append(k.Models, mm)
	}
	if v, ok := p.ExtraConfig["disable_cooling"].(bool); ok {
		k.DisableCooling = v
	}
	return k
}

func vertexKeyFromProvider(p store.UpstreamProvider) config.VertexCompatKey {
	k := config.VertexCompatKey{
		APIKey:             p.APIKey,
		Priority:           p.Priority,
		Prefix:             p.Prefix,
		BaseURL:            p.BaseURL,
		ProxyURL:           p.ProxyURL,
		Headers:            p.Headers,
		ExcludedModels:     p.ExcludedModels,
		UpstreamProviderID: p.ID,
	}
	for _, m := range p.Models {
		k.Models = append(k.Models, config.VertexCompatModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
		})
	}
	return k
}

// decodeThinkingSupport converts the JSONB thinking column back into a
// *registry.ThinkingSupport. nil/empty yields nil.
func decodeThinkingSupport(m map[string]any) *registry.ThinkingSupport {
	if len(m) == 0 {
		return nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	var ts registry.ThinkingSupport
	if err := json.Unmarshal(raw, &ts); err != nil {
		return nil
	}
	return &ts
}

// RenderAuthFile rebuilds the auth-dir JSON blob for a single OAuth provider
// row. The keys mirror the format the synthesizer (internal/watcher/synthesizer)
// reads: "type" = channel, "email", "disabled", "disable_cooling",
// "request_retry", "tool_prefix_disabled", cloak_* for Claude OAuth, plus the
// token fields. The file_name column provides the on-disk filename.
func RenderAuthFile(p store.UpstreamProvider) ([]byte, error) {
	if !IsOAuth(p.ProviderType) {
		return nil, fmt.Errorf("upstreamsync: provider %q is not an oauth provider", p.ProviderType)
	}
	meta := map[string]any{
		"type": OAuthChannel(p.ProviderType),
	}
	if p.Email != "" {
		meta["email"] = p.Email
	}
	if p.Label != "" {
		meta["label"] = p.Label
	}
	if p.Disabled {
		meta["disabled"] = true
	}
	if p.Prefix != "" {
		meta["prefix"] = p.Prefix
	}
	// Extra config passthrough for auth-file keys.
	if v, ok := p.ExtraConfig["disable_cooling"].(bool); ok && v {
		meta["disable_cooling"] = true
	}
	if v, ok := p.ExtraConfig["request_retry"]; ok {
		meta["request_retry"] = v
	}
	if v, ok := p.ExtraConfig["tool_prefix_disabled"].(bool); ok && v {
		meta["tool_prefix_disabled"] = true
	}
	// Per-account OAuth model aliases are the normalized store.UpstreamProvider
	// .Models rows for this OAuth provider. Render them into the auth file's
	// "model_aliases" JSON key, which the synthesizer
	// (extractOAuthModelAliasesFromMetadata) reads back into config.OAuthModelAlias.
	// The JSON shape mirrors config.OAuthModelAlias struct tags
	// (name/alias/fork/display-name/force-mapping) so the round-trip is lossless.
	if aliases := oauthModelAliasesFromModels(p.Models); len(aliases) > 0 {
		meta["model_aliases"] = aliases
	} else if v, ok := p.ExtraConfig["model_aliases"]; ok {
		// Backward-compat: rows seeded before this field was modelled still
		// carry the raw blob in extra_config. Pass it through unchanged until
		// the row is re-saved through the editor (which populates Models).
		meta["model_aliases"] = v
	}
	// Cloak (Claude OAuth).
	if p.CloakMode != "" {
		meta["cloak_mode"] = p.CloakMode
	}
	if p.CloakStrictMode {
		meta["cloak_strict_mode"] = true
	}
	if len(p.CloakSensitiveWords) > 0 {
		meta["cloak_sensitive_words"] = p.CloakSensitiveWords
	}
	if p.CloakCacheUserID != nil {
		meta["cloak_cache_user_id"] = *p.CloakCacheUserID
	}
	// Token fields (only written when present).
	if p.TokenAccessToken != "" || p.TokenRefreshToken != "" {
		token := map[string]any{}
		if p.TokenAccessToken != "" {
			token["access_token"] = p.TokenAccessToken
		}
		if p.TokenRefreshToken != "" {
			token["refresh_token"] = p.TokenRefreshToken
		}
		if p.TokenTokenType != "" {
			token["token_type"] = p.TokenTokenType
		}
		if p.TokenScope != "" {
			token["scope"] = p.TokenScope
		}
		if p.TokenExpiry != nil && !p.TokenExpiry.IsZero() {
			token["expiry"] = p.TokenExpiry.UTC().Format(time.RFC3339)
		}
		if len(token) > 0 {
			meta["token"] = token
		}
	}
	if p.TokenExpiry != nil && !p.TokenExpiry.IsZero() {
		meta["expires_at"] = p.TokenExpiry.UTC().Format(time.RFC3339)
	}
	if p.TokenExpired != nil && *p.TokenExpired {
		meta["expired"] = true
	}
	return json.MarshalIndent(meta, "", "  ")
}

// oauthModelAliasesFromModels renders a provider's Models rows into the
// model_aliases array shape the synthesizer reads. Entries with no name or no
// alias are dropped (mirrors config.SanitizeOAuthModelAlias semantics), so a
// provider with only "name" rows (no aliasing) produces an empty array and the
// model_aliases key is omitted by the caller. The keys match config.OAuthModelAlias
// JSON tags: name, alias, fork, display-name, force-mapping.
func oauthModelAliasesFromModels(models []store.UpstreamProviderModel) []map[string]any {
	if len(models) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(models))
	for _, m := range models {
		name := strings.TrimSpace(m.Name)
		alias := strings.TrimSpace(m.Alias)
		if name == "" || alias == "" || strings.EqualFold(name, alias) {
			continue
		}
		entry := map[string]any{
			"name":  name,
			"alias": alias,
		}
		if m.Fork {
			entry["fork"] = true
		}
		if dn := strings.TrimSpace(m.DisplayName); dn != "" {
			entry["display-name"] = dn
		}
		if m.ForceMapping {
			entry["force-mapping"] = true
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
