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
	"net/url"
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
	TypeOpenCodeGo          = "opencode-go"
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
// Pool bindings are not resolved (callers without a pool store); bound
// entries keep their manual proxy URLs.
func RenderConfig(providers []store.UpstreamProvider) config.Config {
	return RenderConfigWithPools(providers, nil)
}

// poolLookup resolves pool ids to rows for render-time binding. Missing
// entries (pool deleted between the two queries) treat the binding as unset.
type poolLookup map[int64]store.ProxyPool

// buildPoolLookup indexes pools by id; nil stays nil so callers without a
// pool store skip resolution entirely.
func buildPoolLookup(pools []store.ProxyPool) poolLookup {
	if pools == nil {
		return nil
	}
	look := make(poolLookup, len(pools))
	for _, p := range pools {
		look[p.ID] = p
	}
	return look
}

// resolvePool returns the concrete proxy value for one binding: the composite
// URL (proxy_url + ?no_proxy=…&strict=…) for standard pools, or the relay
// base for relay types. Inactive pools return empty (binding unset).
func (l poolLookup) resolve(id *int64) (proxyURL string, relayBase string) {
	if id == nil || l == nil {
		return "", ""
	}
	pool, ok := l[*id]
	if !ok || !pool.IsActive || pool.ProxyURL == "" {
		return "", ""
	}
	if pool.Type == "vercel" || pool.Type == "cloudflare" || pool.Type == "deno" {
		return "", pool.ProxyURL
	}
	attrs := make([]string, 0, 2)
	if pool.NoProxy != "" {
		parts := make([]string, 0, 4)
		for _, part := range strings.Split(pool.NoProxy, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
		if len(parts) > 0 {
			attrs = append(attrs, "no_proxy="+url.QueryEscape(strings.ToLower(strings.Join(parts, ","))))
		}
	}
	if pool.StrictProxy {
		attrs = append(attrs, "strict=true")
	} else {
		attrs = append(attrs, "strict=false")
	}
	sep := "?"
	if strings.Contains(pool.ProxyURL, "?") {
		sep = "&" // defensive: raw pool URLs should not carry queries
	}
	return pool.ProxyURL + sep + strings.Join(attrs, "&"), ""
}

// resolveBinding applies the design's precedence: entry pool → row pool →
// entry manual URL → row manual URL. Inactive/missing pools fall through.
func resolveBinding(look poolLookup, entryPool, rowPool *int64, entryURL, rowURL string) (proxyURL, relayBase string) {
	if proxyURL, relay := look.resolve(entryPool); proxyURL != "" || relay != "" {
		return proxyURL, relay
	}
	if proxyURL, relay := look.resolve(rowPool); proxyURL != "" || relay != "" {
		return proxyURL, relay
	}
	if entryURL != "" {
		return entryURL, ""
	}
	return rowURL, ""
}

// RenderConfigWithPools is RenderConfig with proxy-pool resolution: bindings
// on rows and entries resolve through the pool lookup into concrete proxy
// URLs (standard pools) or RelayBaseURL stamps (relay pools).
func RenderConfigWithPools(providers []store.UpstreamProvider, pools poolLookup) config.Config {
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
			cfg.ClaudeKey = append(cfg.ClaudeKey, claudeKeyFromProviderWithPools(p, pools)...)
		case TypeOpenAICompatibility:
			cfg.OpenAICompatibility = append(cfg.OpenAICompatibility, openAICompatFromProviderWithPools(p, pools))
		case TypeOpenCodeGo:
			cfg.OpenCodeGo = append(cfg.OpenCodeGo, openCodeGoFromProviderWithPools(p, pools))
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
	return claudeKeyFromProviderWithPools(p, nil)
}

func claudeKeyFromProviderWithPools(p store.UpstreamProvider, pools poolLookup) []config.ClaudeKey {
	if len(p.APIKeyEntries) == 0 {
		// Legacy fallback: a Claude provider row that predates the modern
		// multi-entry editor still renders as exactly one config.ClaudeKey
		// sourced from the parent's api_key + provider proxy, with the
		// entry ID left zero.
		return []config.ClaudeKey{buildClaudeKeyWithPools(p, store.UpstreamProviderAPIKey{
			APIKey:   p.APIKey,
			ProxyURL: p.ProxyURL,
		}, pools)}
	}
	keys := make([]config.ClaudeKey, 0, len(p.APIKeyEntries))
	for _, e := range p.APIKeyEntries {
		if e.Disabled {
			// Disabled entries stay persisted but never reach config.yaml;
			// toggling them back on re-renders them on the next reload.
			continue
		}
		keys = append(keys, buildClaudeKeyWithPools(p, e, pools))
	}
	return keys
}

// buildClaudeKey projects one store.UpstreamProvider + child entry into a
// config.ClaudeKey, copying all provider-level fields onto the item and
// choosing the entry proxy when set or the provider proxy when blank.
// The entry proxy never overrides the provider proxy unless non-empty, so
// operators can leave the column null and inherit the row default.
func buildClaudeKey(p store.UpstreamProvider, e store.UpstreamProviderAPIKey) config.ClaudeKey {
	return buildClaudeKeyWithPools(p, e, nil)
}

// buildClaudeKeyWithPools resolves the pool binding first (entry pool → row
// pool → manual URLs) and stamps the result onto ProxyURL/RelayBaseURL.
func buildClaudeKeyWithPools(p store.UpstreamProvider, e store.UpstreamProviderAPIKey, pools poolLookup) config.ClaudeKey {
	proxyURL, relayBase := resolveBinding(pools, e.ProxyPoolID, p.ProxyPoolID, e.ProxyURL, p.ProxyURL)
	k := config.ClaudeKey{
		APIKey:                  e.APIKey,
		Weight:                  e.Weight,
		Priority:                p.Priority,
		Prefix:                  p.Prefix,
		BaseURL:                 p.BaseURL,
		ProxyURL:                proxyURL,
		RelayBaseURL:            relayBase,
		ProxyPoolID:             e.ProxyPoolID,
		Headers:                 p.Headers,
		ExcludedModels:          p.ExcludedModels,
		RebuildMidSystemMessage: p.RebuildMidSystemMessage,
		ExperimentalCCHSigning:  p.ExperimentalCCHSigning,
		UpstreamProviderID:      p.ID,
		UpstreamProviderEntryID: e.ID,
	}
	// A nil entry priority inherits the row-level default set in the literal
	// above; a non-nil pointer overrides it with the entry's own tier.
	if e.Priority != nil {
		k.Priority = *e.Priority
	}
	// The pool strategy is row-level data stamped onto every fan-out item so
	// the synthesizer/conductor can read it per auth. Raw store values are
	// canonicalized here; blank stays blank (unset = global routing strategy).
	k.UpstreamProviderStrategy = config.NormalizePoolRoutingStrategy(p.RoutingStrategy)
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
	return openAICompatFromProviderWithPools(p, nil)
}

func openAICompatFromProviderWithPools(p store.UpstreamProvider, pools poolLookup) config.OpenAICompatibility {
	rowProxyURL, rowRelayBase := resolveBinding(pools, nil, p.ProxyPoolID, "", p.ProxyURL)
	k := config.OpenAICompatibility{
		Name:     p.Name,
		Priority: p.Priority,
		// Raw store strategies are canonicalized here; blank stays blank
		// (unset = follow the global routing strategy).
		Strategy:     config.NormalizePoolRoutingStrategy(p.RoutingStrategy),
		Disabled:     p.Disabled,
		Prefix:       p.Prefix,
		BaseURL:      p.BaseURL,
		ProxyURL:     rowProxyURL,
		RelayBaseURL: rowRelayBase,
		ProxyPoolID:  p.ProxyPoolID,
		Headers:      p.Headers,
	}
	for _, e := range p.APIKeyEntries {
		if e.Disabled {
			// Disabled entries stay persisted but never reach config.yaml;
			// toggling them back on re-renders them on the next reload.
			continue
		}
		// Priority is copied verbatim so nil stays nil and continues to mean
		// "inherit the pool-level Priority" on the runtime side. The entry
		// pool binding overrides the row binding (resolveBinding precedence).
		entryProxyURL, entryRelayBase := resolveBinding(pools, e.ProxyPoolID, p.ProxyPoolID, e.ProxyURL, "")
		k.APIKeyEntries = append(k.APIKeyEntries, config.OpenAICompatibilityAPIKey{
			APIKey:                  e.APIKey,
			Name:                    e.Name,
			UpstreamProviderEntryID: e.ID,
			Weight:                  e.Weight,
			Priority:                e.Priority,
			ProxyURL:                entryProxyURL,
			RelayBaseURL:            entryRelayBase,
			ProxyPoolID:             e.ProxyPoolID,
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

// openCodeGoFromProviderWithPools projects one opencode-go provider row into
// a config.OpenCodeGo. Field handling mirrors openAICompatFromProviderWithPools:
// disabled entries stay persisted but never reach config.yaml, entry pool
// bindings override the row binding, and raw routing strategies are
// canonicalized here. Per-model wire formats round-trip verbatim (empty =
// the executor's openai default).
func openCodeGoFromProviderWithPools(p store.UpstreamProvider, pools poolLookup) config.OpenCodeGo {
	rowProxyURL, rowRelayBase := resolveBinding(pools, nil, p.ProxyPoolID, "", p.ProxyURL)
	k := config.OpenCodeGo{
		Name:               p.Name,
		Priority:           p.Priority,
		Strategy:           config.NormalizePoolRoutingStrategy(p.RoutingStrategy),
		Disabled:           p.Disabled,
		Prefix:             p.Prefix,
		BaseURL:            p.BaseURL,
		ProxyURL:           rowProxyURL,
		RelayBaseURL:       rowRelayBase,
		ProxyPoolID:        p.ProxyPoolID,
		UpstreamProviderID: p.ID,
		Headers:            p.Headers,
	}
	for _, e := range p.APIKeyEntries {
		if e.Disabled {
			// Disabled entries stay persisted but never reach config.yaml;
			// toggling them back on re-renders them on the next reload.
			continue
		}
		entryProxyURL, entryRelayBase := resolveBinding(pools, e.ProxyPoolID, p.ProxyPoolID, e.ProxyURL, "")
		k.APIKeyEntries = append(k.APIKeyEntries, config.OpenCodeGoKey{
			APIKey:                  e.APIKey,
			Name:                    e.Name,
			UpstreamProviderEntryID: e.ID,
			Weight:                  e.Weight,
			Priority:                e.Priority,
			ProxyURL:                entryProxyURL,
			RelayBaseURL:            entryRelayBase,
			ProxyPoolID:             e.ProxyPoolID,
		})
	}
	for _, m := range p.Models {
		mm := config.OpenCodeGoModel{
			Name:             m.Name,
			Alias:            m.Alias,
			DisplayName:      m.DisplayName,
			ForceMapping:     m.ForceMapping,
			WireFormat:       m.WireFormat,
			MaxContextLength: maxContextLengthFromModel(m),
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

// maxContextLengthFromModel reads the optional max_context_length extra
// attribute off a store model row. Only opencode-go seeds set it today.
func maxContextLengthFromModel(m store.UpstreamProviderModel) int {
	if v, ok := m.Thinking["max_context_length"].(float64); ok {
		return int(v)
	}
	return 0
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
