package configsnapshot

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// ImportReport records per-resource actions. It deliberately reports operations
// performed through public stores; those stores each own their transaction.
type ImportReport struct{ Counts map[string]map[string]int }

func (r *ImportReport) counts(kind, action string) int {
	if r == nil || r.Counts == nil {
		return 0
	}
	return r.Counts[kind][action]
}
func (r *ImportReport) Created(kind string) int     { return r.counts(kind, "created") }
func (r *ImportReport) Updated(kind string) int     { return r.counts(kind, "updated") }
func (r *ImportReport) Unsupported(kind string) int { return r.counts(kind, "unsupported") }
func (r *ImportReport) Add(kind, action string) {
	if r.Counts == nil {
		r.Counts = map[string]map[string]int{}
	}
	if r.Counts[kind] == nil {
		r.Counts[kind] = map[string]int{}
	}
	r.Counts[kind][action]++
}

// MapResources maps the API-key provider sections and client API keys in cfg.
// Provider identity is (provider_type, api_key), while client-key identity is
// the key hash. Existing stores expose independent transactions, so this
// function intentionally does not claim cross-resource atomicity.
func MapResources(ctx context.Context, pg *store.PostgresStore, upStore store.UpstreamProviderStore, keyStore *store.APIKeyStore, cfg *config.Config) (*ImportReport, error) {
	if pg == nil || upStore == nil || keyStore == nil || cfg == nil {
		return nil, fmt.Errorf("configsnapshot: nil dependency")
	}
	report := &ImportReport{}
	existing, err := upStore.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("configsnapshot: list upstream providers: %w", err)
	}
	byIdentity := make(map[string]store.UpstreamProvider, len(existing))
	for _, p := range existing {
		byIdentity[providerIdentity(p.ProviderType, p.APIKey)] = p
	}
	providers := configProviders(cfg)
	for _, p := range providers {
		if strings.TrimSpace(p.APIKey) == "" {
			continue
		}
		key := providerIdentity(p.ProviderType, p.APIKey)
		if old, ok := byIdentity[key]; ok {
			p.ID = old.ID
			if providerEqual(old, p) {
				continue
			}
			if _, err := upStore.Update(ctx, p); err != nil {
				return nil, fmt.Errorf("configsnapshot: update %s provider: %w", p.ProviderType, err)
			}
			report.Add("upstream_providers", "updated")
		} else {
			if _, err := upStore.Create(ctx, p); err != nil {
				return nil, fmt.Errorf("configsnapshot: create %s provider: %w", p.ProviderType, err)
			}
			report.Add("upstream_providers", "created")
		}
	}
	for _, secret := range cfg.APIKeys {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		if _, _, err := keyStore.LookupByHash(ctx, store.HashSecret(secret)); err == nil {
			continue
		} else if err != nil && err != store.ErrAPIKeyNotFound {
			return nil, fmt.Errorf("configsnapshot: lookup client api key: %w", err)
		}
		if _, _, err := keyStore.Create(ctx, "imported", "", secret, nil, nil, nil); err != nil {
			return nil, fmt.Errorf("configsnapshot: create client api key: %w", err)
		}
		report.Add("api_keys", "created")
	}
	return report, nil
}

func providerIdentity(kind, stable string) string { return kind + "\x00" + stable }

func providerEqual(a, b store.UpstreamProvider) bool {
	a.ID, b.ID = 0, 0
	a.CreatedAt, b.CreatedAt = time.Time{}, time.Time{}
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

func configProviders(c *config.Config) []store.UpstreamProvider {
	out := make([]store.UpstreamProvider, 0, len(c.ClaudeKey)+len(c.CodexKey)+len(c.GeminiKey)+len(c.XAIKey)+len(c.OpenAICompatibility)+len(c.VertexCompatAPIKey))
	for _, k := range c.ClaudeKey {
		out = append(out, store.UpstreamProvider{ProviderType: "claude-api-key", APIKey: k.APIKey, Prefix: k.Prefix, BaseURL: k.BaseURL, ProxyURL: k.ProxyURL, Priority: k.Priority, Disabled: k.Disabled, Models: claudeModels(k.Models), Headers: k.Headers, ExcludedModels: k.ExcludedModels})
	}
	for _, k := range c.CodexKey {
		out = append(out, store.UpstreamProvider{ProviderType: "codex-api-key", APIKey: k.APIKey, Prefix: k.Prefix, BaseURL: k.BaseURL, ProxyURL: k.ProxyURL, Priority: k.Priority, Websockets: k.Websockets, Models: codexModels(k.Models), Headers: k.Headers, ExcludedModels: k.ExcludedModels})
	}
	for _, k := range c.GeminiKey {
		out = append(out, store.UpstreamProvider{ProviderType: "gemini-api-key", APIKey: k.APIKey, Prefix: k.Prefix, BaseURL: k.BaseURL, ProxyURL: k.ProxyURL, Priority: k.Priority, Models: geminiModels(k.Models), Headers: k.Headers, ExcludedModels: k.ExcludedModels})
	}
	for _, k := range c.XAIKey {
		out = append(out, store.UpstreamProvider{ProviderType: "xai-api-key", APIKey: k.APIKey, Prefix: k.Prefix, BaseURL: k.BaseURL, ProxyURL: k.ProxyURL, Priority: k.Priority, Models: codexModels(k.Models), Headers: k.Headers, ExcludedModels: k.ExcludedModels})
	}
	for _, k := range c.OpenAICompatibility {
		p := store.UpstreamProvider{ProviderType: "openai-compatibility", Name: k.Name, APIKey: firstCompatKey(k.APIKeyEntries), Prefix: k.Prefix, BaseURL: k.BaseURL, ProxyURL: k.ProxyURL, Priority: k.Priority, Disabled: k.Disabled, RoutingStrategy: k.Strategy, Models: compatModels(k.Models), Headers: k.Headers}
		for _, e := range k.APIKeyEntries {
			p.APIKeyEntries = append(p.APIKeyEntries, store.UpstreamProviderAPIKey{APIKey: e.APIKey, Name: e.Name, ProxyURL: e.ProxyURL, Weight: e.Weight, Priority: e.Priority, Disabled: e.Disabled})
		}
		out = append(out, p)
	}
	return out
}
func firstCompatKey(v []config.OpenAICompatibilityAPIKey) string {
	if len(v) > 0 {
		return v[0].APIKey
	}
	return ""
}
func claudeModels(v []config.ClaudeModel) []store.UpstreamProviderModel {
	out := make([]store.UpstreamProviderModel, len(v))
	for i, m := range v {
		out[i] = store.UpstreamProviderModel{Name: m.Name, Alias: m.Alias, DisplayName: m.DisplayName, ForceMapping: m.ForceMapping}
	}
	return out
}
func codexModels(v []config.CodexModel) []store.UpstreamProviderModel {
	out := make([]store.UpstreamProviderModel, len(v))
	for i, m := range v {
		out[i] = store.UpstreamProviderModel{Name: m.Name, Alias: m.Alias, DisplayName: m.DisplayName, ForceMapping: m.ForceMapping}
	}
	return out
}
func geminiModels(v []config.GeminiModel) []store.UpstreamProviderModel {
	out := make([]store.UpstreamProviderModel, len(v))
	for i, m := range v {
		out[i] = store.UpstreamProviderModel{Name: m.Name, Alias: m.Alias, DisplayName: m.DisplayName, ForceMapping: m.ForceMapping}
	}
	return out
}
func compatModels(v []config.OpenAICompatibilityModel) []store.UpstreamProviderModel {
	out := make([]store.UpstreamProviderModel, len(v))
	for i, m := range v {
		out[i] = store.UpstreamProviderModel{Name: m.Name, Alias: m.Alias, DisplayName: m.DisplayName, ForceMapping: m.ForceMapping, Image: m.Image, InputModalities: m.InputModalities, OutputModalities: m.OutputModalities}
	}
	return out
}
