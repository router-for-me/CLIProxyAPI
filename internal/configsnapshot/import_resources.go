package configsnapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// ImportReport records per-resource actions and actionable errors. Writes are
// deliberately reported individually because the public stores each own a
// transaction; callers requiring cross-resource atomicity should use a single
// transaction-aware store operation.
type ImportReport struct {
	Counts map[string]map[string]int
	Errors map[string][]string
}

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
func (r *ImportReport) AddError(kind, message string) {
	if r.Errors == nil {
		r.Errors = map[string][]string{}
	}
	r.Errors[kind] = append(r.Errors[kind], message)
}

// MapResources imports supported YAML resources. It preserves partial counts
// and returns the report alongside the first error encountered.
func MapResources(ctx context.Context, pg *store.PostgresStore, upStore store.UpstreamProviderStore, keyStore *store.APIKeyStore, cfg *config.Config) (*ImportReport, error) {
	if pg == nil || upStore == nil || keyStore == nil || cfg == nil {
		return nil, fmt.Errorf("configsnapshot: nil dependency")
	}
	report := &ImportReport{}
	existing, err := upStore.List(ctx)
	if err != nil {
		return report, fmt.Errorf("configsnapshot: list upstream providers: %w", err)
	}
	byIdentity := make(map[string]store.UpstreamProvider, len(existing))
	for _, p := range existing {
		byIdentity[providerIdentityForProvider(p)] = p
	}
	for _, p := range configProviders(cfg) {
		id := providerIdentityForProvider(p)
		if old, ok := byIdentity[id]; ok {
			merged := mergeOwnedProvider(old, p)
			merged.ID = old.ID
			if !providerEqual(old, merged) {
				if _, e := upStore.Update(ctx, merged); e != nil {
					report.AddError("upstream_providers", fmt.Sprintf("update %s: %v", id, e))
					return report, e
				}
				report.Add("upstream_providers", "updated")
				byIdentity[id] = merged
			}
			continue
		}
		created, e := upStore.Create(ctx, p)
		if e != nil {
			report.AddError("upstream_providers", fmt.Sprintf("create %s: %v", id, e))
			return report, e
		}
		report.Add("upstream_providers", "created")
		if created != nil {
			byIdentity[id] = *created
		}
	}
	// Config has no internal-user YAML section; make that omission explicit.
	report.Add("internal_users", "unsupported")
	for _, secret := range cfg.APIKeys {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		if _, _, e := keyStore.LookupByHash(ctx, store.HashSecret(secret)); e == nil {
			continue
		} else if e != store.ErrAPIKeyNotFound {
			report.AddError("api_keys", e.Error())
			return report, e
		}
		if _, _, e := keyStore.Create(ctx, "imported", "", secret, nil, nil, nil); e != nil {
			// A concurrent importer may have won the unique race; verify before failing.
			if _, _, lookupErr := keyStore.LookupByHash(ctx, store.HashSecret(secret)); lookupErr == nil {
				continue
			}
			report.AddError("api_keys", fmt.Sprintf("create client api key: %v", e))
			return report, e
		}
		report.Add("api_keys", "created")
	}
	return report, nil
}

func providerIdentity(kind, stable string) string { return kind + "\x00" + stable }
func providerIdentityForProvider(p store.UpstreamProvider) string {
	stable := strings.TrimSpace(p.Name)
	if stable == "" {
		stable = strings.TrimSpace(p.APIKey)
	}
	if stable == "" {
		raw, _ := json.Marshal(struct{ T, N, B, P string }{p.ProviderType, p.Prefix, p.BaseURL, p.ProxyURL})
		sum := sha256.Sum256(raw)
		stable = "anonymous-" + hex.EncodeToString(sum[:8])
	}
	return providerIdentity(p.ProviderType, stable)
}

func providerEqual(a, b store.UpstreamProvider) bool {
	a.ID, b.ID = 0, 0
	a.CreatedAt, b.CreatedAt = time.Time{}, time.Time{}
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(normalizeProviderForCompare(a), normalizeProviderForCompare(b))
}

// mergeOwnedProvider changes only fields represented by YAML provider entries.
func mergeOwnedProvider(old, incoming store.UpstreamProvider) store.UpstreamProvider {
	merged := old
	merged.ProviderType, merged.Name, merged.Priority, merged.Disabled = incoming.ProviderType, incoming.Name, incoming.Priority, incoming.Disabled
	merged.RoutingStrategy, merged.Prefix, merged.APIKey, merged.BaseURL, merged.ProxyURL = incoming.RoutingStrategy, incoming.Prefix, incoming.APIKey, incoming.BaseURL, incoming.ProxyURL
	merged.Websockets, merged.RebuildMidSystemMessage, merged.ExperimentalCCHSigning = incoming.Websockets, incoming.RebuildMidSystemMessage, incoming.ExperimentalCCHSigning
	merged.CloakMode, merged.CloakStrictMode, merged.CloakSensitiveWords, merged.CloakCacheUserID = incoming.CloakMode, incoming.CloakStrictMode, incoming.CloakSensitiveWords, incoming.CloakCacheUserID
	merged.Models, merged.Headers, merged.ExcludedModels, merged.APIKeyEntries, merged.ExtraConfig = incoming.Models, incoming.Headers, incoming.ExcludedModels, incoming.APIKeyEntries, incoming.ExtraConfig
	return merged
}

func normalizeProviderForCompare(p store.UpstreamProvider) store.UpstreamProvider {
	p.APIKey, p.Name, p.Prefix, p.BaseURL, p.ProxyURL = strings.TrimSpace(p.APIKey), strings.TrimSpace(p.Name), strings.TrimSpace(p.Prefix), strings.TrimSpace(p.BaseURL), strings.TrimSpace(p.ProxyURL)
	p.ExcludedModels = cleanStrings(p.ExcludedModels)
	sort.Strings(p.ExcludedModels)
	if len(p.Headers) == 0 {
		p.Headers = nil
	} else {
		h := map[string]string{}
		for k, v := range p.Headers {
			k = strings.ToLower(strings.TrimSpace(k))
			h[k] = strings.TrimSpace(v)
		}
		p.Headers = h
	}
	for i := range p.Models {
		p.Models[i].Name = strings.TrimSpace(p.Models[i].Name)
		p.Models[i].Alias = strings.TrimSpace(p.Models[i].Alias)
		p.Models[i].DisplayName = strings.TrimSpace(p.Models[i].DisplayName)
		if len(p.Models[i].InputModalities) == 0 {
			p.Models[i].InputModalities = nil
		}
		if len(p.Models[i].OutputModalities) == 0 {
			p.Models[i].OutputModalities = nil
		}
	}
	if len(p.Models) == 0 {
		p.Models = nil
	}
	if len(p.APIKeyEntries) == 0 {
		p.APIKeyEntries = nil
	}
	return p
}
func cleanStrings(v []string) []string {
	out := v[:0]
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func configProviders(c *config.Config) []store.UpstreamProvider {
	out := make([]store.UpstreamProvider, 0)
	for _, k := range c.ClaudeKey {
		out = append(out, claudeProvider(k))
	}
	for _, k := range c.InteractionsKey {
		p := geminiProvider(k)
		p.ProviderType = "interactions-api-key"
		out = append(out, p)
	}
	for _, k := range c.GeminiKey {
		out = append(out, geminiProvider(k))
	}
	for _, k := range c.CodexKey {
		out = append(out, codexProvider(k, "codex-api-key"))
	}
	for _, k := range c.XAIKey {
		out = append(out, codexProvider(k, "xai-api-key"))
	}
	for _, k := range c.VertexCompatAPIKey {
		p := store.UpstreamProvider{ProviderType: "vertex-api-key", APIKey: k.APIKey, Priority: k.Priority, Prefix: k.Prefix, BaseURL: k.BaseURL, ProxyURL: k.ProxyURL, Headers: k.Headers, ExcludedModels: k.ExcludedModels}
		for _, m := range k.Models {
			p.Models = append(p.Models, store.UpstreamProviderModel{Name: m.Name, Alias: m.Alias, DisplayName: m.DisplayName, ForceMapping: m.ForceMapping, Thinking: thinkingMap(m.Thinking)})
		}
		out = append(out, p)
	}
	for _, k := range c.OpenAICompatibility {
		out = append(out, compatProvider(k))
	}
	for _, k := range c.OpenCodeGo {
		out = append(out, openCodeProvider(k))
	}
	seen := map[string]bool{}
	dedup := out[:0]
	for _, p := range out {
		p = normalizeProviderForCompare(p)
		id := providerIdentityForProvider(p)
		if !seen[id] {
			seen[id] = true
			dedup = append(dedup, p)
		}
	}
	return dedup
}
func geminiProvider(k config.GeminiKey) store.UpstreamProvider {
	p := store.UpstreamProvider{ProviderType: "gemini-api-key", APIKey: k.APIKey, Priority: k.Priority, Prefix: k.Prefix, BaseURL: k.BaseURL, ProxyURL: k.ProxyURL, Headers: k.Headers, ExcludedModels: k.ExcludedModels}
	for _, m := range k.Models {
		p.Models = append(p.Models, store.UpstreamProviderModel{Name: m.Name, Alias: m.Alias, DisplayName: m.DisplayName, ForceMapping: m.ForceMapping, Thinking: thinkingMap(m.Thinking)})
	}
	return p
}
func codexProvider(k config.CodexKey, typ string) store.UpstreamProvider {
	p := geminiProvider(config.GeminiKey{APIKey: k.APIKey, Priority: k.Priority, Prefix: k.Prefix, BaseURL: k.BaseURL, ProxyURL: k.ProxyURL, Headers: k.Headers, ExcludedModels: k.ExcludedModels})
	p.ProviderType = typ
	p.Websockets = k.Websockets
	for i, m := range k.Models {
		p.Models[i] = store.UpstreamProviderModel{Name: m.Name, Alias: m.Alias, DisplayName: m.DisplayName, ForceMapping: m.ForceMapping, Thinking: thinkingMap(m.Thinking)}
	}
	return p
}
func claudeProvider(k config.ClaudeKey) store.UpstreamProvider {
	p := store.UpstreamProvider{ProviderType: "claude-api-key", APIKey: k.APIKey, Priority: k.Priority, Prefix: k.Prefix, BaseURL: k.BaseURL, ProxyURL: k.ProxyURL, Headers: k.Headers, ExcludedModels: k.ExcludedModels, RebuildMidSystemMessage: k.RebuildMidSystemMessage, ExperimentalCCHSigning: k.ExperimentalCCHSigning}
	if k.Cloak != nil {
		p.CloakMode = k.Cloak.Mode
		p.CloakStrictMode = k.Cloak.StrictMode
		p.CloakSensitiveWords = k.Cloak.SensitiveWords
		p.CloakCacheUserID = k.Cloak.CacheUserID
	}
	for _, m := range k.Models {
		p.Models = append(p.Models, store.UpstreamProviderModel{Name: m.Name, Alias: m.Alias, DisplayName: m.DisplayName, ForceMapping: m.ForceMapping, Thinking: thinkingMap(m.Thinking)})
	}
	return p
}
func compatProvider(k config.OpenAICompatibility) store.UpstreamProvider {
	p := store.UpstreamProvider{ProviderType: "openai-compatibility", Name: k.Name, Priority: k.Priority, Disabled: k.Disabled, Prefix: k.Prefix, BaseURL: k.BaseURL, ProxyURL: k.ProxyURL, RoutingStrategy: k.Strategy, Headers: k.Headers}
	for _, e := range k.APIKeyEntries {
		p.APIKeyEntries = append(p.APIKeyEntries, store.UpstreamProviderAPIKey{APIKey: e.APIKey, Name: e.Name, ProxyURL: e.ProxyURL, ProxyPoolID: e.ProxyPoolID, Weight: e.Weight, Priority: e.Priority, Disabled: e.Disabled})
	}
	for _, m := range k.Models {
		p.Models = append(p.Models, store.UpstreamProviderModel{Name: m.Name, Alias: m.Alias, DisplayName: m.DisplayName, ForceMapping: m.ForceMapping, Image: m.Image, InputModalities: m.InputModalities, OutputModalities: m.OutputModalities, Thinking: thinkingMap(m.Thinking)})
	}
	return p
}
func openCodeProvider(k config.OpenCodeGo) store.UpstreamProvider {
	p := store.UpstreamProvider{ProviderType: "opencode-go", Name: k.Name, Priority: k.Priority, Disabled: k.Disabled, Prefix: k.Prefix, BaseURL: k.BaseURL, ProxyURL: k.ProxyURL, RoutingStrategy: k.Strategy, Headers: k.Headers}
	for _, e := range k.APIKeyEntries {
		p.APIKeyEntries = append(p.APIKeyEntries, store.UpstreamProviderAPIKey{APIKey: e.APIKey, Name: e.Name, ProxyURL: e.ProxyURL, ProxyPoolID: e.ProxyPoolID, Weight: e.Weight, Priority: e.Priority, Disabled: e.Disabled})
	}
	for _, m := range k.Models {
		p.Models = append(p.Models, store.UpstreamProviderModel{Name: m.Name, Alias: m.Alias, DisplayName: m.DisplayName, ForceMapping: m.ForceMapping, WireFormat: m.WireFormat, Thinking: thinkingMap(m.Thinking)})
	}
	return p
}
func thinkingMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
