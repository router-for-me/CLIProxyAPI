// Package configsnapshot adds the normalized resource mapping planner for the
// Phase 1 PG-first control plane. The planner converts every supported
// configuration section into a pure, non-database representation that the
// store layer applies transactionally in a later phase. The planner never
// mutates the caller's config.Config, never sorts source backing slices
// in place, and never reaches into a database.
package configsnapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// Provider type discriminators match upstreamsync/render.go and are
// canonical across the control plane.
const (
	ptGeminiAPIKey        = "gemini-api-key"
	ptInteractionsAPIKey  = "interactions-api-key"
	ptCodexAPIKey         = "codex-api-key"
	ptXAIAPIKey           = "xai-api-key"
	ptMetaAPIKey          = "meta-api-key"
	ptNeuralwattAPIKey    = "neuralwatt-api-key"
	ptClaudeAPIKey        = "claude-api-key"
	ptOpenAICompatibility = "openai-compatibility"
	ptOpenCodeGo          = "opencode-go"
	ptVertexAPIKey        = "vertex-api-key"
)

// ExtraConfig keys for fields the normalized parent/model columns do not
// capture. Operators do not set these directly; the importer populates them
// and the runtime treats them as renderer-managed metadata.
const (
	ecDisableCooling        = "nixllm.yaml.disable_cooling"
	ecAlphaSearch           = "nixllm.yaml.alpha_search"
	ecSupportPromptCacheKey = "nixllm.yaml.support_prompt_cache_key"
	ecModelMaxContextLength = "nixllm.yaml.model.max_context_length"
	ecModelIsCompat         = "nixllm.yaml.model.is_compat"
	ecWireFormat            = "nixllm.yaml.model.wire_format"
	ecWeight                = "nixllm.yaml.credential_weight"
	ecDisabled              = "nixllm.yaml.credential_disabled"
	ecRelayBaseURL          = "nixllm.yaml.relay_base_url"
)

// NormalizedResourcePlan and ImportReport live in the store package (see
// store/pg_normalized_import_types.go) so the transactional apply path can
// consume the plan without an import cycle. The aliases below keep the
// planner's call sites readable.
type (
	// NormalizedResourcePlan is the pure, non-database output of
	// BuildResourcePlan.
	NormalizedResourcePlan = store.NormalizedResourcePlan
	// ImportReport describes planning and apply outcomes.
	ImportReport = store.ImportReport
)

// BuildResourcePlan converts every supported section in cfg into a
// NormalizedResourcePlan. It never mutates cfg or its nested slices/maps.
// Duplicate provider identities are recorded as errors but do not collapse
// the offending rows; the apply path treats the report's Errors map as the
// source of truth.
func BuildResourcePlan(cfg *config.Config) (*NormalizedResourcePlan, error) {
	plan := &NormalizedResourcePlan{Report: ImportReport{Planned: true}}
	if cfg == nil {
		return plan, fmt.Errorf("configsnapshot: BuildResourcePlan: cfg is nil")
	}
	appendProviders(&plan.Providers, &plan.Report,
		convertClaudeKeys(cfg.ClaudeKey)...,
	)
	appendProviders(&plan.Providers, &plan.Report,
		convertInteractionsKeys(cfg.InteractionsKey)...,
	)
	appendProviders(&plan.Providers, &plan.Report,
		convertGeminiKeys(cfg.GeminiKey)...,
	)
	appendProviders(&plan.Providers, &plan.Report,
		convertCodexKeys(cfg.CodexKey)...,
	)
	appendProviders(&plan.Providers, &plan.Report,
		convertXAIKeys(cfg.XAIKey)...,
	)
	appendProviders(&plan.Providers, &plan.Report,
		convertMetaKeys(cfg.MetaKey)...,
	)
	appendProviders(&plan.Providers, &plan.Report,
		convertNeuralwattKeys(cfg.NeuralwattKey)...,
	)
	appendProviders(&plan.Providers, &plan.Report,
		convertVertexCompatKeys(cfg.VertexCompatAPIKey)...,
	)
	appendProviders(&plan.Providers, &plan.Report,
		convertOpenAICompatibility(cfg.OpenAICompatibility)...,
	)
	appendProviders(&plan.Providers, &plan.Report,
		convertOpenCodeGo(cfg.OpenCodeGo)...,
	)
	convertClientAPIKeys(cfg.APIKeys, &plan.APIKeys, &plan.Report)
	plan.Report.Bump("internal_users", "not_applicable")
	if err := detectDuplicateProviderIdentities(&plan.Providers, &plan.Report); err != nil {
		// err is currently nil; reserved for future identity policy.
		_ = err
	}
	return plan, nil
}

// dupKey computes the dedupe identity for a provider row. Names that the
// planner itself synthesized (claude-1, gemini-2, ...) are excluded because
// they are positional labels, not operator-authored identity: two identical
// YAML credentials must collide. Operator-named rows (openai-compatibility,
// opencode-go) keep name-based identity.
func dupKey(p store.UpstreamProvider) string {
	switch {
	case isSyntheticName(p.ProviderType, p.Name):
		return strings.ToLower(p.ProviderType) + "\x00" + strings.ToLower(strings.TrimSpace(p.APIKey)) + "\x00" + strings.ToLower(strings.TrimSpace(p.BaseURL)) + "\x00" + strings.ToLower(strings.TrimSpace(p.Prefix))
	case strings.TrimSpace(p.Name) != "":
		return strings.ToLower(p.ProviderType) + "\x00" + strings.ToLower(strings.TrimSpace(p.Name))
	case strings.TrimSpace(p.APIKey) != "" && p.ProviderType != ptOpenAICompatibility && p.ProviderType != ptOpenCodeGo:
		return strings.ToLower(p.ProviderType) + "\x00" + strings.ToLower(strings.TrimSpace(p.APIKey)) + "\x00" + strings.ToLower(strings.TrimSpace(p.BaseURL)) + "\x00" + strings.ToLower(strings.TrimSpace(p.Prefix))
	default:
		return strings.ToLower(p.ProviderType) + "\x00" + keylessFingerprint(p)
	}
}

// isSyntheticName reports whether name matches the "<sectionType>-<index>"
// pattern the planner assigns to single-credential sections. These names
// are positional and must not participate in identity.
func isSyntheticName(providerType, name string) bool {
	suffix, ok := strings.CutPrefix(name, sectionPrefix(providerType))
	if !ok || suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sectionPrefix maps a provider_type to the synthetic-name prefix the
// planner's converters assign.
func sectionPrefix(providerType string) string {
	switch providerType {
	case ptClaudeAPIKey:
		return "claude-"
	case ptGeminiAPIKey:
		return "gemini-api-key-"
	case ptInteractionsAPIKey:
		return "interactions-api-key-"
	case ptCodexAPIKey:
		return "codex-"
	case ptXAIAPIKey:
		return "xai-"
	case ptMetaAPIKey:
		return "meta-"
	case ptNeuralwattAPIKey:
		return "neuralwatt-"
	case ptVertexAPIKey:
		return "vertex-"
	default:
		return "\x00" // never matches
	}
}

func keylessFingerprint(p store.UpstreamProvider) string {
	type entrySig struct {
		Key, Name, Proxy string
		Pool             int64
		Weight, Priority int
		Disabled         bool
	}
	sigs := make([]entrySig, 0, len(p.APIKeyEntries))
	for _, e := range p.APIKeyEntries {
		var pool int64
		if e.ProxyPoolID != nil {
			pool = *e.ProxyPoolID
		}
		sigs = append(sigs, entrySig{
			Key: strings.TrimSpace(e.APIKey), Name: strings.TrimSpace(e.Name),
			Proxy: strings.TrimSpace(e.ProxyURL), Pool: pool,
			Weight: trimIntPtr(e.Weight), Priority: trimIntPtr(e.Priority),
			Disabled: e.Disabled,
		})
	}
	sort.Slice(sigs, func(i, j int) bool {
		if sigs[i].Key != sigs[j].Key {
			return sigs[i].Key < sigs[j].Key
		}
		if sigs[i].Name != sigs[j].Name {
			return sigs[i].Name < sigs[j].Name
		}
		return sigs[i].Proxy < sigs[j].Proxy
	})
	h := sha256.Sum256([]byte(fmt.Sprintf("%v", sigs)))
	return hex.EncodeToString(h[:]) + "\x00" + strings.ToLower(strings.TrimSpace(p.BaseURL)) + "\x00" + strings.ToLower(strings.TrimSpace(p.Prefix))
}

func trimIntPtr(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func appendProviders(dst *[]store.UpstreamProvider, report *ImportReport, src ...store.UpstreamProvider) {
	for _, p := range src {
		*dst = append(*dst, p)
		report.Created("upstream_providers")
	}
}

// detectDuplicateProviderIdentities records an error for every identity
// that appears more than once. The offending rows remain in plan.Providers
// so the apply path can surface them individually.
func detectDuplicateProviderIdentities(providers *[]store.UpstreamProvider, report *ImportReport) error {
	if providers == nil {
		return nil
	}
	seen := map[string]int{}
	order := []string{}
	for i, p := range *providers {
		k := dupKey(p)
		if seen[k] == 0 {
			seen[k] = 1
			order = append(order, k)
			continue
		}
		seen[k]++
		report.AddError("upstream_providers", fmt.Sprintf("dup#%d identity=%s type=%s name=%s", i, k, p.ProviderType, p.Name),
			fmt.Errorf("duplicate provider identity at index %d", i))
	}
	return nil
}

// convertInteractionsKeys converts Config.InteractionsKey (GeminiKey shape)
// into normalized interactions-api-key providers.
func convertInteractionsKeys(in []config.GeminiKey) []store.UpstreamProvider {
	return convertGeminiLike(in, ptInteractionsAPIKey)
}

// convertGeminiKeys converts Config.GeminiKey into normalized providers.
func convertGeminiKeys(in []config.GeminiKey) []store.UpstreamProvider {
	return convertGeminiLike(in, ptGeminiAPIKey)
}

// convertGeminiLike is shared by gemini/interactions because both sections
// carry the same GeminiKey shape but different provider_type discriminators.
func convertGeminiLike(in []config.GeminiKey, pt string) []store.UpstreamProvider {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProvider, 0, len(in))
	for i, k := range in {
		p := store.UpstreamProvider{
			ProviderType:   pt,
			Priority:       k.Priority,
			Prefix:         k.Prefix,
			APIKey:         k.APIKey,
			BaseURL:        k.BaseURL,
			ProxyURL:       k.ProxyURL,
			Models:         convertGeminiModels(k.Models),
			Headers:        copyHeaders(k.Headers),
			ExcludedModels: append([]string(nil), k.ExcludedModels...),
		}
		extra := map[string]any{}
		if k.Weight != nil {
			extra[ecWeight] = *k.Weight
		}
		if k.DisableCooling {
			extra[ecDisableCooling] = true
		}
		if len(extra) > 0 {
			p.ExtraConfig = extra
		}
		p.Name = fmt.Sprintf("%s-%d", pt, i+1)
		out = append(out, p)
	}
	return out
}

// convertCodexKeys converts Config.CodexKey into normalized providers.
func convertCodexKeys(in []config.CodexKey) []store.UpstreamProvider {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProvider, 0, len(in))
	for i, k := range in {
		p := store.UpstreamProvider{
			ProviderType:   ptCodexAPIKey,
			Priority:       k.Priority,
			Prefix:         k.Prefix,
			APIKey:         k.APIKey,
			BaseURL:        k.BaseURL,
			ProxyURL:       k.ProxyURL,
			Websockets:     k.Websockets,
			Models:         convertCodexModels(k.Models),
			Headers:        copyHeaders(k.Headers),
			ExcludedModels: append([]string(nil), k.ExcludedModels...),
		}
		extra := map[string]any{}
		if k.Weight != nil {
			extra[ecWeight] = *k.Weight
		}
		if k.DisableCooling {
			extra[ecDisableCooling] = true
		}
		if k.AlphaSearch {
			extra[ecAlphaSearch] = true
		}
		if len(extra) > 0 {
			p.ExtraConfig = extra
		}
		p.Name = fmt.Sprintf("codex-%d", i+1)
		out = append(out, p)
	}
	return out
}

// convertXAIKeys converts Config.XAIKey (CodexKey alias) into xai providers.
func convertXAIKeys(in []config.CodexKey) []store.UpstreamProvider {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProvider, 0, len(in))
	for i, k := range in {
		p := store.UpstreamProvider{
			ProviderType:   ptXAIAPIKey,
			Priority:       k.Priority,
			Prefix:         k.Prefix,
			APIKey:         k.APIKey,
			BaseURL:        k.BaseURL,
			ProxyURL:       k.ProxyURL,
			Websockets:     k.Websockets,
			Models:         convertCodexModels(k.Models),
			Headers:        copyHeaders(k.Headers),
			ExcludedModels: append([]string(nil), k.ExcludedModels...),
		}
		extra := map[string]any{}
		if k.Weight != nil {
			extra[ecWeight] = *k.Weight
		}
		if k.DisableCooling {
			extra[ecDisableCooling] = true
		}
		if len(extra) > 0 {
			p.ExtraConfig = extra
		}
		p.Name = fmt.Sprintf("xai-%d", i+1)
		out = append(out, p)
	}
	return out
}

// convertMetaKeys converts Config.MetaKey (CodexKey alias) into meta providers.
func convertMetaKeys(in []config.CodexKey) []store.UpstreamProvider {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProvider, 0, len(in))
	for i, k := range in {
		p := store.UpstreamProvider{
			ProviderType:   ptMetaAPIKey,
			Priority:       k.Priority,
			Prefix:         k.Prefix,
			APIKey:         k.APIKey,
			BaseURL:        k.BaseURL,
			ProxyURL:       k.ProxyURL,
			Websockets:     k.Websockets,
			Models:         convertCodexModels(k.Models),
			Headers:        copyHeaders(k.Headers),
			ExcludedModels: append([]string(nil), k.ExcludedModels...),
		}
		extra := map[string]any{}
		if k.Weight != nil {
			extra[ecWeight] = *k.Weight
		}
		if k.DisableCooling {
			extra[ecDisableCooling] = true
		}
		if len(extra) > 0 {
			p.ExtraConfig = extra
		}
		p.Name = fmt.Sprintf("meta-%d", i+1)
		out = append(out, p)
	}
	return out
}

// convertNeuralwattKeys converts Config.NeuralwattKey (CodexKey alias) into
// neuralwatt providers.
func convertNeuralwattKeys(in []config.CodexKey) []store.UpstreamProvider {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProvider, 0, len(in))
	for i, k := range in {
		p := store.UpstreamProvider{
			ProviderType:   ptNeuralwattAPIKey,
			Priority:       k.Priority,
			Prefix:         k.Prefix,
			APIKey:         k.APIKey,
			BaseURL:        k.BaseURL,
			ProxyURL:       k.ProxyURL,
			Websockets:     k.Websockets,
			Models:         convertCodexModels(k.Models),
			Headers:        copyHeaders(k.Headers),
			ExcludedModels: append([]string(nil), k.ExcludedModels...),
		}
		extra := map[string]any{}
		if k.Weight != nil {
			extra[ecWeight] = *k.Weight
		}
		if k.DisableCooling {
			extra[ecDisableCooling] = true
		}
		if k.ServiceTier != "" {
			// The render-side helper codexKeyFromProvider reads the raw
			// "service_tier" key, so the planner writes the same key for
			// a planner→render round-trip. (Pre-existing planners use the
			// namespaced "nixllm.yaml.*" form; we deliberately match the
			// render contract here so PG re-imports do not silently drop
			// Neuralwatt's billing-tier selection.)
			extra["service_tier"] = k.ServiceTier
		}
		if len(extra) > 0 {
			p.ExtraConfig = extra
		}
		p.Name = fmt.Sprintf("neuralwatt-%d", i+1)
		out = append(out, p)
	}
	return out
}

// convertVertexCompatKeys converts Config.VertexCompatAPIKey into
// vertex-api-key providers.
func convertVertexCompatKeys(in []config.VertexCompatKey) []store.UpstreamProvider {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProvider, 0, len(in))
	for i, k := range in {
		p := store.UpstreamProvider{
			ProviderType:   ptVertexAPIKey,
			Priority:       k.Priority,
			Prefix:         k.Prefix,
			APIKey:         k.APIKey,
			BaseURL:        k.BaseURL,
			ProxyURL:       k.ProxyURL,
			Models:         convertVertexModels(k.Models),
			Headers:        copyHeaders(k.Headers),
			ExcludedModels: append([]string(nil), k.ExcludedModels...),
		}
		extra := map[string]any{}
		if k.Weight != nil {
			extra[ecWeight] = *k.Weight
		}
		if len(extra) > 0 {
			p.ExtraConfig = extra
		}
		p.Name = fmt.Sprintf("vertex-%d", i+1)
		out = append(out, p)
	}
	return out
}

// convertOpenAICompatibility converts Config.OpenAICompatibility (the
// entry-bearing pool section) into openai-compatibility providers.
func convertOpenAICompatibility(in []config.OpenAICompatibility) []store.UpstreamProvider {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProvider, 0, len(in))
	for _, k := range in {
		p := store.UpstreamProvider{
			ProviderType:    ptOpenAICompatibility,
			Name:            k.Name,
			Priority:        k.Priority,
			RoutingStrategy: k.Strategy,
			CircuitBreaker:  k.CircuitBreaker,
			Disabled:        k.Disabled,
			Prefix:          k.Prefix,
			BaseURL:         k.BaseURL,
			ProxyURL:        k.ProxyURL,
			ProxyPoolID:     k.ProxyPoolID,
			Models:          convertOpenAIModels(k.Models),
			Headers:         copyHeaders(k.Headers),
			APIKeyEntries:   convertOpenAIEntries(k.APIKeyEntries),
		}
		extra := map[string]any{}
		if k.SupportPromptCacheKey {
			extra[ecSupportPromptCacheKey] = true
		}
		if k.DisableCooling {
			extra[ecDisableCooling] = true
		}
		if k.RelayBaseURL != "" {
			extra[ecRelayBaseURL] = k.RelayBaseURL
		}
		if len(extra) > 0 {
			p.ExtraConfig = extra
		}
		out = append(out, p)
	}
	return out
}

// convertOpenCodeGo converts Config.OpenCodeGo into opencode-go providers.
func convertOpenCodeGo(in []config.OpenCodeGo) []store.UpstreamProvider {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProvider, 0, len(in))
	for _, k := range in {
		p := store.UpstreamProvider{
			ProviderType:    ptOpenCodeGo,
			Name:            k.Name,
			Priority:        k.Priority,
			RoutingStrategy: k.Strategy,
			CircuitBreaker:  k.CircuitBreaker,
			Disabled:        k.Disabled,
			Prefix:          k.Prefix,
			BaseURL:         k.BaseURL,
			ProxyURL:        k.ProxyURL,
			ProxyPoolID:     k.ProxyPoolID,
			Models:          convertOpenCodeModels(k.Models),
			Headers:         copyHeaders(k.Headers),
			APIKeyEntries:   convertOpenCodeEntries(k.APIKeyEntries),
		}
		extra := map[string]any{}
		if k.DisableCooling {
			extra[ecDisableCooling] = true
		}
		if k.RelayBaseURL != "" {
			extra[ecRelayBaseURL] = k.RelayBaseURL
		}
		if len(extra) > 0 {
			p.ExtraConfig = extra
		}
		out = append(out, p)
	}
	return out
}
func convertClaudeKeys(in []config.ClaudeKey) []store.UpstreamProvider {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProvider, 0, len(in))
	for i, k := range in {
		p := store.UpstreamProvider{
			ProviderType:            ptClaudeAPIKey,
			Priority:                k.Priority,
			Prefix:                  k.Prefix,
			APIKey:                  k.APIKey,
			BaseURL:                 k.BaseURL,
			ProxyURL:                k.ProxyURL,
			ProxyPoolID:             k.ProxyPoolID,
			Models:                  convertClaudeModels(k.Models),
			Headers:                 copyHeaders(k.Headers),
			ExcludedModels:          append([]string(nil), k.ExcludedModels...),
			RebuildMidSystemMessage: k.RebuildMidSystemMessage,
		}
		// Disabled and other per-credential values without a parent column
		// are preserved in ExtraConfig and reported as unsupported/preserved.
		extra := map[string]any{}
		if k.Disabled {
			extra[ecDisabled] = true
		}
		if k.Weight != nil {
			extra[ecWeight] = *k.Weight
		}
		if k.DisableCooling {
			extra[ecDisableCooling] = true
		}
		if k.RelayBaseURL != "" {
			extra[ecRelayBaseURL] = k.RelayBaseURL
		}
		if len(extra) > 0 {
			p.ExtraConfig = extra
		}
		// Deterministic per-section name; unnamed section rows get a
		// stable synthetic label so identity stays stable across imports.
		p.Name = fmt.Sprintf("claude-%d", i+1)
		out = append(out, p)
	}
	return out
}

// secrets. The apply path will look up by hash and INSERT only on miss.
func convertClientAPIKeys(in []string, dst *[]store.APIKey, report *ImportReport) {
	for i, raw := range in {
		secret := strings.TrimSpace(raw)
		if secret == "" {
			continue
		}
		*dst = append(*dst, store.APIKey{
			ID:        fmt.Sprintf("imported-%d", i+1),
			Name:      "imported",
			KeyHash:   HashSecretForImport(secret),
			KeyPrefix: prefixFromSecret(secret),
			Status:    "active",
		})
		report.Created("api_keys")
	}
}

// jsonMarshal is a tiny indirection so tests can snapshot structures
// without importing encoding/json in the test file.
var jsonMarshal = json.Marshal

// HashSecretForImport centralises the hashing contract so the planner and
// the apply path agree. The apply path uses the same helper when comparing
// lookups, so a hash algorithm change stays in one place.
var HashSecretForImport = func(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func prefixFromSecret(secret string) string {
	if len(secret) < 8 {
		return ""
	}
	return secret[:8]
}

// copyHeaders deep-copies a header map so callers can never observe later
// mutations of the source map through the plan.
func copyHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// convertClaudeModels maps config.ClaudeModel into normalized model rows.
func convertClaudeModels(in []config.ClaudeModel) []store.UpstreamProviderModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProviderModel, 0, len(in))
	for i, m := range in {
		row := store.UpstreamProviderModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
			Thinking:     thinkingToMap(m.Thinking),
			SortOrder:    i,
		}
		out = append(out, row)
	}
	return out
}

// convertGeminiModels maps config.GeminiModel into normalized model rows.
// MaxContextLength and IsCompat have no normalized columns, so they are
// recorded in Thinking-adjacent ExtraConfig keys carried per row via the
// Thinking map (the store's only structured per-model JSONB field).
func convertGeminiModels(in []config.GeminiModel) []store.UpstreamProviderModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProviderModel, 0, len(in))
	for i, m := range in {
		row := store.UpstreamProviderModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
			Thinking:     thinkingToMap(m.Thinking),
			SortOrder:    i,
		}
		applyModelExtras(&row, m.MaxContextLength, m.IsCompat, "")
		out = append(out, row)
	}
	return out
}

// convertCodexModels maps config.CodexModel into normalized model rows.
func convertCodexModels(in []config.CodexModel) []store.UpstreamProviderModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProviderModel, 0, len(in))
	for i, m := range in {
		row := store.UpstreamProviderModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
			Thinking:     thinkingToMap(m.Thinking),
			SortOrder:    i,
		}
		applyModelExtras(&row, m.MaxContextLength, m.IsCompat, "")
		out = append(out, row)
	}
	return out
}

// convertVertexModels maps config.VertexCompatModel into normalized rows.
func convertVertexModels(in []config.VertexCompatModel) []store.UpstreamProviderModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProviderModel, 0, len(in))
	for i, m := range in {
		out = append(out, store.UpstreamProviderModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
			Thinking:     thinkingToMap(m.Thinking),
			SortOrder:    i,
		})
	}
	return out
}

// convertOpenAIModels maps config.OpenAICompatibilityModel into normalized rows.
func convertOpenAIModels(in []config.OpenAICompatibilityModel) []store.UpstreamProviderModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProviderModel, 0, len(in))
	for i, m := range in {
		row := store.UpstreamProviderModel{
			Name:             m.Name,
			Alias:            m.Alias,
			DisplayName:      m.DisplayName,
			ForceMapping:     m.ForceMapping,
			Image:            m.Image,
			InputModalities:  append([]string(nil), m.InputModalities...),
			OutputModalities: append([]string(nil), m.OutputModalities...),
			Thinking:         thinkingToMap(m.Thinking),
			SortOrder:        i,
		}
		applyModelExtras(&row, m.MaxContextLength, m.IsCompat, "")
		out = append(out, row)
	}
	return out
}

// convertOpenCodeModels maps config.OpenCodeGoModel into normalized rows.
func convertOpenCodeModels(in []config.OpenCodeGoModel) []store.UpstreamProviderModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProviderModel, 0, len(in))
	for i, m := range in {
		row := store.UpstreamProviderModel{
			Name:         m.Name,
			Alias:        m.Alias,
			DisplayName:  m.DisplayName,
			ForceMapping: m.ForceMapping,
			Thinking:     thinkingToMap(m.Thinking),
			WireFormat:   m.WireFormat,
			SortOrder:    i,
		}
		applyModelExtras(&row, m.MaxContextLength, false, "")
		out = append(out, row)
	}
	return out
}

// applyModelExtras records model fields that lack normalized columns into
// the row's Thinking map under namespaced keys. The Thinking JSONB column
// is the store's structured per-model field, so this keeps values lossless
// without a schema migration. Keys are reserved for import ownership.
func applyModelExtras(row *store.UpstreamProviderModel, maxContextLength int, isCompat bool, _ string) {
	if row.Thinking == nil {
		row.Thinking = map[string]any{}
	}
	if maxContextLength != 0 {
		row.Thinking[ecModelMaxContextLength] = maxContextLength
	}
	if isCompat {
		row.Thinking[ecModelIsCompat] = true
	}
	if len(row.Thinking) == 0 {
		row.Thinking = nil
	}
}

// thinkingToMap converts *registry.ThinkingSupport into the JSONB map shape
// the store persists. Nil input stays nil.
func thinkingToMap(in any) map[string]any {
	if in == nil {
		return nil
	}
	// The registry.ThinkingSupport struct marshals to JSON losslessly; the
	// store column is JSONB, so encode/decode round-trips it faithfully.
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// convertOpenAIEntries maps config.OpenAICompatibilityAPIKey into normalized
// API-key entries. IDs stay zero: the apply path matches existing children
// by identity and assigns persisted IDs inside the import transaction.
//
// Weights are normalized here at the planner boundary so downstream code
// (signature hashing, apply path, renderer) sees a non-nil Weight that is
// always at least 1. The round-2 weighted selector (docs/plans/2026-09-17-
// omniroute-round-2-design.md) samples by Weight directly and would divide
// by zero on an unmodified nil/0/negative input.
func convertOpenAIEntries(in []config.OpenAICompatibilityAPIKey) []store.UpstreamProviderAPIKey {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProviderAPIKey, 0, len(in))
	for i, e := range in {
		out = append(out, store.UpstreamProviderAPIKey{
			APIKey:      e.APIKey,
			Name:        e.Name,
			ProxyURL:    e.ProxyURL,
			ProxyPoolID: e.ProxyPoolID,
			Weight:      normalizeEntryWeight(e.Weight),
			Priority:    e.Priority,
			Disabled:    e.Disabled,
			SortOrder:   i,
		})
	}
	return out
}

// convertOpenCodeEntries maps config.OpenCodeGoKey into normalized entries.
// Same Weight normalization contract as convertOpenAIEntries; see that
// function for the rationale.
func convertOpenCodeEntries(in []config.OpenCodeGoKey) []store.UpstreamProviderAPIKey {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.UpstreamProviderAPIKey, 0, len(in))
	for i, e := range in {
		out = append(out, store.UpstreamProviderAPIKey{
			APIKey:      e.APIKey,
			Name:        e.Name,
			ProxyURL:    e.ProxyURL,
			ProxyPoolID: e.ProxyPoolID,
			Weight:      normalizeEntryWeight(e.Weight),
			Priority:    e.Priority,
			Disabled:    e.Disabled,
			SortOrder:   i,
		})
	}
	return out
}

// normalizeEntryWeight returns a non-nil pointer to a value of at least 1.
// Nil, zero, and negative inputs are clamped to 1 so the round-2 weighted
// selector (docs/plans/2026-09-17-omniroute-round-2-design.md) and the
// canonical identity hash always see a positive weight.
func normalizeEntryWeight(w *int) *int {
	if w == nil || *w < 1 {
		one := 1
		return &one
	}
	return w
}
