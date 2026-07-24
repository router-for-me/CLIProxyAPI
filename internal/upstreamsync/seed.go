package upstreamsync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// SeedFromArtifacts migrates existing config.yaml provider lists + auth-dir
// JSON files into normalized upstream_providers rows. It is intended to run
// once on first PG boot when the table is empty. Idempotency is provided by
// the (provider_type, file_name) uniqueness index for OAuth providers and by
// the caller's empty-table guard for API-key providers.
//
// Existing API-key provider rows are dropped first (the table is empty when
// this runs, so this is a safety no-op) to avoid duplicates on re-runs.
func SeedFromArtifacts(ctx context.Context, st store.UpstreamProviderStore, cfg *config.Config, authDir string) error {
	if st == nil || cfg == nil {
		return fmt.Errorf("upstreamsync: store and config are required")
	}
	// API-key providers.
	for _, k := range cfg.GeminiKey {
		if _, err := st.Create(ctx, providerFromGeminiKey(k)); err != nil {
			log.WithError(err).Warn("upstreamsync: seed gemini-api-key row failed")
		}
	}
	for _, k := range cfg.InteractionsKey {
		p := providerFromGeminiKey(k)
		p.ProviderType = TypeInteractionsAPIKey
		if _, err := st.Create(ctx, p); err != nil {
			log.WithError(err).Warn("upstreamsync: seed interactions-api-key row failed")
		}
	}
	for _, k := range cfg.CodexKey {
		if _, err := st.Create(ctx, providerFromCodexKey(k, TypeCodexAPIKey)); err != nil {
			log.WithError(err).Warn("upstreamsync: seed codex-api-key row failed")
		}
	}
	for _, k := range cfg.XAIKey {
		if _, err := st.Create(ctx, providerFromCodexKey(k, TypeXAIAPIKey)); err != nil {
			log.WithError(err).Warn("upstreamsync: seed xai-api-key row failed")
		}
	}
	for _, k := range cfg.ClaudeKey {
		if _, err := st.Create(ctx, providerFromClaudeKey(k)); err != nil {
			log.WithError(err).Warn("upstreamsync: seed claude-api-key row failed")
		}
	}
	for _, k := range cfg.OpenAICompatibility {
		if _, err := st.Create(ctx, providerFromOpenAICompat(k)); err != nil {
			log.WithError(err).Warn("upstreamsync: seed openai-compatibility row failed")
		}
	}
	for _, k := range cfg.VertexCompatAPIKey {
		if _, err := st.Create(ctx, providerFromVertexKey(k)); err != nil {
			log.WithError(err).Warn("upstreamsync: seed vertex-api-key row failed")
		}
	}

	// OAuth/file-backed auths.
	resolved, err := resolveAuthDir(authDir)
	if err != nil {
		log.WithError(err).Warn("upstreamsync: auth dir unreadable, skipping OAuth provider seeding")
		return nil
	}
	if resolved != "" {
		entries, errRead := os.ReadDir(resolved)
		if errRead != nil {
			log.WithError(errRead).Warn("upstreamsync: auth dir unreadable")
			return nil
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}
			fullPath := filepath.Join(resolved, name)
			raw, errReadFile := os.ReadFile(fullPath)
			if errReadFile != nil {
				log.WithError(errReadFile).Warnf("upstreamsync: skip unreadable auth %s", name)
				continue
			}
			p, errParse := providerFromAuthJSON(name, raw)
			if errParse != nil {
				log.WithError(errParse).Warnf("upstreamsync: skip malformed auth %s", name)
				continue
			}
			if p.ProviderType == "" {
				continue
			}
			if _, err := st.Create(ctx, p); err != nil {
				log.WithError(err).Warnf("upstreamsync: seed oauth provider %s failed", name)
			}
		}
	}
	return nil
}

func resolveAuthDir(authDir string) (string, error) {
	if authDir == "" {
		return "", nil
	}
	if filepath.IsAbs(authDir) {
		return authDir, nil
	}
	// Relative paths are resolved against the home/config dir elsewhere; for
	// seeding we only need a best-effort read.
	abs, err := filepath.Abs(authDir)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func providerFromGeminiKey(k config.GeminiKey) store.UpstreamProvider {
	p := store.UpstreamProvider{
		ProviderType:   TypeGeminiAPIKey,
		APIKey:         k.APIKey,
		Priority:       k.Priority,
		Prefix:         k.Prefix,
		BaseURL:        k.BaseURL,
		ProxyURL:       k.ProxyURL,
		Headers:        k.Headers,
		ExcludedModels: k.ExcludedModels,
		SourceBackend:  "config",
	}
	if k.DisableCooling {
		setExtra(&p, "disable_cooling", true)
	}
	for _, m := range k.Models {
		p.Models = append(p.Models, store.UpstreamProviderModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
		})
	}
	return p
}

func providerFromCodexKey(k config.CodexKey, providerType string) store.UpstreamProvider {
	p := store.UpstreamProvider{
		ProviderType:   providerType,
		APIKey:         k.APIKey,
		Priority:       k.Priority,
		Prefix:         k.Prefix,
		BaseURL:        k.BaseURL,
		Websockets:     k.Websockets,
		ProxyURL:       k.ProxyURL,
		Headers:        k.Headers,
		ExcludedModels: k.ExcludedModels,
		SourceBackend:  "config",
	}
	if k.DisableCooling {
		setExtra(&p, "disable_cooling", true)
	}
	for _, m := range k.Models {
		p.Models = append(p.Models, store.UpstreamProviderModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
		})
	}
	return p
}

func providerFromClaudeKey(k config.ClaudeKey) store.UpstreamProvider {
	p := store.UpstreamProvider{
		ProviderType:            TypeClaudeAPIKey,
		APIKey:                  k.APIKey,
		Priority:                k.Priority,
		Prefix:                  k.Prefix,
		BaseURL:                 k.BaseURL,
		ProxyURL:                k.ProxyURL,
		Headers:                 k.Headers,
		ExcludedModels:          k.ExcludedModels,
		RebuildMidSystemMessage: k.RebuildMidSystemMessage,
		ExperimentalCCHSigning:  k.ExperimentalCCHSigning,
		SourceBackend:           "config",
	}
	if k.DisableCooling {
		setExtra(&p, "disable_cooling", true)
	}
	if k.Cloak != nil {
		p.CloakMode = k.Cloak.Mode
		p.CloakStrictMode = k.Cloak.StrictMode
		p.CloakSensitiveWords = k.Cloak.SensitiveWords
		if k.Cloak.CacheUserID != nil {
			v := *k.Cloak.CacheUserID
			p.CloakCacheUserID = &v
		}
	}
	for _, m := range k.Models {
		p.Models = append(p.Models, store.UpstreamProviderModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
		})
	}
	return p
}

func providerFromOpenAICompat(k config.OpenAICompatibility) store.UpstreamProvider {
	p := store.UpstreamProvider{
		ProviderType:  TypeOpenAICompatibility,
		Name:          k.Name,
		Priority:      k.Priority,
		Disabled:      k.Disabled,
		Prefix:        k.Prefix,
		BaseURL:       k.BaseURL,
		Headers:       k.Headers,
		SourceBackend: "config",
	}
	if k.DisableCooling {
		setExtra(&p, "disable_cooling", true)
	}
	for _, e := range k.APIKeyEntries {
		p.APIKeyEntries = append(p.APIKeyEntries, store.UpstreamProviderAPIKey{
			APIKey:   e.APIKey,
			ProxyURL: e.ProxyURL,
		})
	}
	for _, m := range k.Models {
		mm := store.UpstreamProviderModel{
			Name:             m.Name,
			Alias:            m.Alias,
			DisplayName:      m.DisplayName,
			ForceMapping:     m.ForceMapping,
			Image:            m.Image,
			InputModalities:  m.InputModalities,
			OutputModalities: m.OutputModalities,
		}
		if m.Thinking != nil {
			mm.Thinking = encodeThinkingSupport(m.Thinking)
		}
		p.Models = append(p.Models, mm)
	}
	return p
}

func providerFromVertexKey(k config.VertexCompatKey) store.UpstreamProvider {
	p := store.UpstreamProvider{
		ProviderType:   TypeVertexAPIKey,
		APIKey:         k.APIKey,
		Priority:       k.Priority,
		Prefix:         k.Prefix,
		BaseURL:        k.BaseURL,
		ProxyURL:       k.ProxyURL,
		Headers:        k.Headers,
		ExcludedModels: k.ExcludedModels,
		SourceBackend:  "config",
	}
	for _, m := range k.Models {
		p.Models = append(p.Models, store.UpstreamProviderModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
		})
	}
	return p
}

// providerFromAuthJSON parses an auth-dir JSON file into an OAuth provider
// row. The "type" field provides the channel; the rest are mapped to the
// normalized columns where possible, with the remainder landing in
// extra_config.
func providerFromAuthJSON(fileName string, raw []byte) (store.UpstreamProvider, error) {
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		return store.UpstreamProvider{}, err
	}
	channel := strings.TrimSpace(valueString(meta["type"]))
	if channel == "" {
		return store.UpstreamProvider{}, fmt.Errorf("auth file %s missing type", fileName)
	}
	p := store.UpstreamProvider{
		ProviderType:  "oauth:" + channel,
		FileName:      strings.TrimSuffix(fileName, ".json"),
		SourceBackend: "file",
	}
	if v := valueString(meta["email"]); v != "" {
		p.Email = v
	}
	if v := valueString(meta["label"]); v != "" {
		p.Label = v
	}
	if v, ok := meta["disabled"].(bool); ok {
		p.Disabled = v
	}
	if v := valueString(meta["prefix"]); v != "" {
		p.Prefix = v
	}
	if v, ok := meta["disable_cooling"].(bool); ok && v {
		setExtra(&p, "disable_cooling", true)
	}
	if v, ok := meta["request_retry"]; ok {
		setExtra(&p, "request_retry", v)
	}
	if v, ok := meta["tool_prefix_disabled"].(bool); ok && v {
		setExtra(&p, "tool_prefix_disabled", true)
	}
	if v, ok := meta["model_aliases"]; ok {
		setExtra(&p, "model_aliases", v)
	}
	if v := valueString(meta["cloak_mode"]); v != "" {
		p.CloakMode = v
	}
	if v, ok := meta["cloak_strict_mode"].(bool); ok {
		p.CloakStrictMode = v
	}
	if v, ok := meta["cloak_sensitive_words"].([]any); ok {
		for _, item := range v {
			if s, ok := item.(string); ok {
				p.CloakSensitiveWords = append(p.CloakSensitiveWords, s)
			}
		}
	}
	if v, ok := meta["cloak_cache_user_id"].(bool); ok {
		val := v
		p.CloakCacheUserID = &val
	}
	// Token extraction: top-level fields + nested "token" object.
	if v := valueString(meta["access_token"]); v != "" {
		p.TokenAccessToken = v
	}
	if v := valueString(meta["refresh_token"]); v != "" {
		p.TokenRefreshToken = v
	}
	if nested, ok := meta["token"].(map[string]any); ok {
		if v := valueString(nested["access_token"]); v != "" {
			p.TokenAccessToken = v
		}
		if v := valueString(nested["refresh_token"]); v != "" {
			p.TokenRefreshToken = v
		}
		if v := valueString(nested["token_type"]); v != "" {
			p.TokenTokenType = v
		}
		if v := valueString(nested["scope"]); v != "" {
			p.TokenScope = v
		}
		if t, ok := parseTokenTime(nested["expiry"]); ok {
			p.TokenExpiry = &t
		}
	}
	if v := valueString(meta["expires_at"]); v != "" {
		if t, ok := parseTokenTimeStr(v); ok {
			p.TokenExpiry = &t
		}
	}
	if v, ok := meta["expired"].(bool); ok {
		val := v
		p.TokenExpired = &val
	}
	// Carry over any remaining keys we do not model explicitly.
	modelled := map[string]bool{
		"type": true, "email": true, "label": true, "disabled": true,
		"prefix": true, "disable_cooling": true, "request_retry": true,
		"tool_prefix_disabled": true, "model_aliases": true,
		"cloak_mode": true, "cloak_strict_mode": true,
		"cloak_sensitive_words": true, "cloak_cache_user_id": true,
		"access_token": true, "refresh_token": true, "token": true,
		"expires_at": true, "expired": true,
	}
	for k, v := range meta {
		if modelled[k] {
			continue
		}
		setExtra(&p, k, v)
	}
	return p, nil
}

// setExtra writes a key/value into the provider's extra_config map,
// initializing it if necessary.
func setExtra(p *store.UpstreamProvider, key string, value any) {
	if p.ExtraConfig == nil {
		p.ExtraConfig = make(map[string]any)
	}
	p.ExtraConfig[key] = value
}

// encodeThinkingSupport serializes *registry.ThinkingSupport into a JSONB-
// friendly map. nil yields nil.
func encodeThinkingSupport(ts *registry.ThinkingSupport) map[string]any {
	if ts == nil {
		return nil
	}
	raw, err := json.Marshal(ts)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// valueString coerces an any from a decoded JSON map to a trimmed string.
func valueString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	if n, ok := v.(json.Number); ok {
		return n.String()
	}
	return ""
}

// parseTokenTime accepts a time-like value (string or float epoch) and
// returns the parsed time.
func parseTokenTime(v any) (time.Time, bool) {
	if v == nil {
		return time.Time{}, false
	}
	if s, ok := v.(string); ok {
		return parseTokenTimeStr(s)
	}
	if f, ok := v.(float64); ok {
		return time.Unix(int64(f), 0), true
	}
	return time.Time{}, false
}

func parseTokenTimeStr(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
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
