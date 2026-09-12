package synthesizer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ConfigSynthesizer generates Auth entries from configuration API keys.
// It handles Gemini, Interactions, Claude, Codex, xAI, OpenAI-compat, and Vertex-compat providers.
type ConfigSynthesizer struct{}

// NewConfigSynthesizer creates a new ConfigSynthesizer instance.
func NewConfigSynthesizer() *ConfigSynthesizer {
	return &ConfigSynthesizer{}
}

func addWeightToAttrs(weight *int, attrs map[string]string) {
	if weight == nil {
		return
	}
	normalized := *weight
	if normalized <= 0 {
		normalized = 0
	}
	attrs[coreauth.AttributeWeight] = strconv.Itoa(normalized)
}

// addUpstreamProviderKey stamps the per-row routing identifier onto the
// auth's attributes when the entry was rendered from a PG-backed
// upstream_providers row. The renderer populates
// `entry.UpstreamProviderID`; when non-zero we encode it into a routing
// key of the form `<channel>:<rowID>` so the per-model routing picker
// can address this row individually instead of collapsing it onto
// every other auth of the same channel. The runtime executor manager
// still resolves the underlying executor via auth.Provider (the bare
// channel name), so per-row routing keys are routing-only.
//
// Legacy YAML-only configs leave UpstreamProviderID at zero; the
// runtime then matches the auth under the bare channel key, preserving
// the previous "all-rows-collapsed" semantics for operators who never
// used the upstream_providers dashboard.
func addUpstreamProviderKey(attrs map[string]string, channel string, rowID int64) {
	if attrs == nil || channel == "" || rowID <= 0 {
		return
	}
	attrs["provider_key"] = channel + ":" + strconv.FormatInt(rowID, 10)
}

func addOpenAICompatEntryProviderKey(attrs map[string]string, providerKey string, entry config.OpenAICompatibilityAPIKey) {
	if attrs == nil || providerKey == "" {
		return
	}
	// The entry-level routing identity MUST be derived from a value the
	// operator cannot rename. Using entry.Name here caused the per-model
	// route picker to silently break when an operator renamed a row: the
	// synthesised entry_provider_key changed, the persisted route still
	// referenced the old key, LiveProviderKeysForModel returned only the new
	// key, intersectProviders produced an empty slice, and the request
	// panicked on providers[0]. Prefer the stable persisted child-row ID and
	// fall back to the mutable name only for legacy YAML entries that have
	// no UpstreamProviderEntryID assigned yet.
	identity := ""
	if entry.UpstreamProviderEntryID > 0 {
		identity = "key-" + strconv.FormatInt(entry.UpstreamProviderEntryID, 10)
	} else {
		identity = strings.ToLower(strings.TrimSpace(entry.Name))
	}
	if identity == "" {
		return
	}
	attrs[coreauth.AttributeEntryProviderKey] = providerKey + ":" + identity
}

// addClaudeEntryProviderKey stamps the per-entry routing identifier onto
// the auth's attributes when the Claude entry was rendered from a PG-backed
// upstream_providers row. It is the Claude analogue of
// addOpenAICompatEntryProviderKey, but the ClaudeKey struct has no
// operator-renameable Name field — the child-row ID is the only stable
// identity available. The helper therefore emits the entry_provider_key
// strictly when both the parent provider_key and the child
// UpstreamProviderEntryID are positive; anything else (legacy YAML, mid-
// migration rows, or absent IDs) leaves the attribute absent so the
// runtime falls back to the bare "claude" channel key and the existing
// round-robin behavior is preserved.
//
// No API key material participates in the identity, so this helper —
// and the call sites that surface it (logs, error messages, dashboard
// labels) — never need to redact credentials.
func addClaudeEntryProviderKey(attrs map[string]string, providerKey string, entry config.ClaudeKey) {
	if attrs == nil || providerKey == "" {
		return
	}
	if entry.UpstreamProviderEntryID <= 0 {
		return
	}
	attrs[coreauth.AttributeEntryProviderKey] = providerKey + ":key-" + strconv.FormatInt(entry.UpstreamProviderEntryID, 10)
}

// Synthesize generates Auth entries from config API keys.
func (s *ConfigSynthesizer) Synthesize(ctx *SynthesisContext) ([]*coreauth.Auth, error) {
	out := make([]*coreauth.Auth, 0, 32)
	if ctx == nil || ctx.Config == nil {
		return out, nil
	}
	if errValidate := ctx.Config.ValidateCredentialWeights(); errValidate != nil {
		return nil, fmt.Errorf("synthesize config API key auths: %w", errValidate)
	}

	// Gemini API Keys
	out = append(out, s.synthesizeGeminiKeys(ctx)...)
	// Native Interactions API Keys
	out = append(out, s.synthesizeInteractionsKeys(ctx)...)
	// Claude API Keys
	out = append(out, s.synthesizeClaudeKeys(ctx)...)
	// Codex API Keys
	out = append(out, s.synthesizeCodexKeys(ctx)...)
	// xAI API Keys
	out = append(out, s.synthesizeXAIKeys(ctx)...)
	// OpenAI-compat
	out = append(out, s.synthesizeOpenAICompat(ctx)...)
	// OpenCode Go
	out = append(out, s.synthesizeOpenCodeGo(ctx)...)
	// Vertex-compat
	out = append(out, s.synthesizeVertexCompat(ctx)...)

	return out, nil
}

// synthesizeGeminiKeys creates Auth entries for Gemini API keys.
func (s *ConfigSynthesizer) synthesizeGeminiKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeGeminiKeyEntries(ctx, ctx.Config.GeminiKey, "gemini:apikey", "gemini", "gemini-apikey", constant.Gemini)
}

// synthesizeInteractionsKeys creates Auth entries for native Interactions API keys.
func (s *ConfigSynthesizer) synthesizeInteractionsKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeGeminiKeyEntries(ctx, ctx.Config.InteractionsKey, "gemini-interactions:apikey", "interactions", "interactions-apikey", constant.GeminiInteractions)
}

func (s *ConfigSynthesizer) synthesizeGeminiKeyEntries(ctx *SynthesisContext, entries []config.GeminiKey, idKind, sourceName, label, provider string) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(entries))
	for i := range entries {
		entry := entries[i]
		key := strings.TrimSpace(entry.APIKey)
		if key == "" {
			continue
		}
		prefix := strings.TrimSpace(entry.Prefix)
		base := strings.TrimSpace(entry.BaseURL)
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		id, token := idGen.Next(idKind, key, base)
		attrs := map[string]string{
			"source":       fmt.Sprintf("config:%s[%s]", sourceName, token),
			"api_key":      key,
			"config_index": strconv.Itoa(i),
		}
		metadata := map[string]any{}
		if entry.DisableCooling {
			metadata["disable_cooling"] = true
		}
		if entry.Priority != 0 {
			attrs["priority"] = strconv.Itoa(entry.Priority)
		}
		addWeightToAttrs(entry.Weight, attrs)
		if base != "" {
			attrs["base_url"] = base
		}
		if hash := diff.ComputeGeminiModelsHash(entry.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(entry.Headers, attrs)
		addUpstreamProviderKey(attrs, provider, entry.UpstreamProviderID)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   provider,
			Label:      label,
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			Metadata:   metadata,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, entry.ExcludedModels, "apikey")
		if len(a.Metadata) == 0 {
			a.Metadata = nil
		}
		out = append(out, a)
	}
	return out
}

// synthesizeClaudeKeys creates Auth entries for Claude API keys.
func (s *ConfigSynthesizer) synthesizeClaudeKeys(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(cfg.ClaudeKey))
	for i := range cfg.ClaudeKey {
		ck := cfg.ClaudeKey[i]
		key := strings.TrimSpace(ck.APIKey)
		if key == "" {
			continue
		}
		prefix := strings.TrimSpace(ck.Prefix)
		base := strings.TrimSpace(ck.BaseURL)
		id, token := idGen.Next("claude:apikey", key, base)
		attrs := map[string]string{
			"source":       fmt.Sprintf("config:claude[%s]", token),
			"api_key":      key,
			"config_index": strconv.Itoa(i),
		}
		metadata := map[string]any{}
		if ck.DisableCooling {
			metadata["disable_cooling"] = true
		}
		if ck.Priority != 0 {
			attrs["priority"] = strconv.Itoa(ck.Priority)
		}
		addWeightToAttrs(ck.Weight, attrs)
		if base != "" {
			attrs["base_url"] = base
		}
		if ck.RebuildMidSystemMessage {
			attrs["rebuild_mid_system_message"] = "true"
		}
		if hash := diff.ComputeClaudeModelsHash(ck.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(ck.Headers, attrs)
		addUpstreamProviderKey(attrs, "claude", ck.UpstreamProviderID)
		// Build the compound entry-level routing key only after the parent
		// provider_key is in place, so legacy YAML entries (no parent id,
		// no child id) keep their bare "claude" channel behavior and the
		// PG-rendered entries gain the stable `claude:<rowID>:key-<id>`
		// identity consumed by the model-route picker. The helper bails on
		// an empty provider key, so legacy rows never get an
		// entry_provider_key attribute.
		providerKey := attrs["provider_key"]
		addClaudeEntryProviderKey(attrs, providerKey, ck)
		// Stamp the row-level pool routing strategy so the conductor can
		// activate aggressive in-pool failover per auth. The renderer already
		// canonicalizes the store value; re-normalizing here also guards
		// hand-written YAML that sets the carry-through field directly.
		// Empty (unset/unknown) leaves the attribute absent, preserving the
		// legacy global-strategy behavior.
		if s := config.NormalizePoolRoutingStrategy(ck.UpstreamProviderStrategy); s != "" {
			attrs[coreauth.AttributePoolStrategy] = s
		}
		// The circuit-breaker opt-in is stamped as the literal "true" — the
		// runtime compares the attribute value literally (poolBreakerContributionKey),
		// so anything else means "not opted in". Absent = default off.
		if ck.UpstreamProviderCircuitBreaker {
			attrs[coreauth.AttributePoolCircuitBreaker] = "true"
		}
		proxyURL := strings.TrimSpace(ck.ProxyURL)
		relayBaseURL := strings.TrimSpace(ck.RelayBaseURL)
		if relayBaseURL != "" {
			proxyURL = "" // relay replaces proxy semantics entirely
		}
		a := &coreauth.Auth{
			ID:           id,
			Provider:     "claude",
			Label:        "claude-apikey",
			Prefix:       prefix,
			Status:       coreauth.StatusActive,
			ProxyURL:     proxyURL,
			RelayBaseURL: relayBaseURL,
			Attributes:   attrs,
			Metadata:     metadata,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, ck.ExcludedModels, "apikey")
		if len(a.Metadata) == 0 {
			a.Metadata = nil
		}
		out = append(out, a)
	}
	return out
}

// synthesizeCodexKeys creates Auth entries for Codex API keys.
func (s *ConfigSynthesizer) synthesizeCodexKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeCodexStyleKeys(ctx, ctx.Config.CodexKey, "codex")
}

// synthesizeXAIKeys creates Auth entries for xAI API keys.
func (s *ConfigSynthesizer) synthesizeXAIKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeCodexStyleKeys(ctx, ctx.Config.XAIKey, "xai")
}

func (s *ConfigSynthesizer) synthesizeCodexStyleKeys(ctx *SynthesisContext, entries []config.CodexKey, provider string) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(entries))
	for i := range entries {
		entry := entries[i]
		key := strings.TrimSpace(entry.APIKey)
		if key == "" {
			continue
		}
		prefix := strings.TrimSpace(entry.Prefix)
		baseURL := strings.TrimSpace(entry.BaseURL)
		id, token := idGen.Next(provider+":apikey", key, baseURL)
		attrs := map[string]string{
			"source":       fmt.Sprintf("config:%s[%s]", provider, token),
			"api_key":      key,
			"config_index": strconv.Itoa(i),
		}
		metadata := map[string]any{}
		if entry.DisableCooling {
			metadata["disable_cooling"] = true
		}
		if entry.Priority != 0 {
			attrs["priority"] = strconv.Itoa(entry.Priority)
		}
		addWeightToAttrs(entry.Weight, attrs)
		if baseURL != "" {
			attrs["base_url"] = baseURL
		}
		if entry.Websockets {
			attrs["websockets"] = "true"
		}
		if provider == "codex" && entry.AlphaSearch {
			attrs[coreauth.AttributeCodexAlphaSearch] = "true"
		}
		if hash := diff.ComputeCodexModelsHash(entry.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(entry.Headers, attrs)
		addUpstreamProviderKey(attrs, provider, entry.UpstreamProviderID)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   provider,
			Label:      provider + "-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   strings.TrimSpace(entry.ProxyURL),
			Attributes: attrs,
			Metadata:   metadata,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, entry.ExcludedModels, "apikey")
		if len(a.Metadata) == 0 {
			a.Metadata = nil
		}
		out = append(out, a)
	}
	return out
}

// synthesizeOpenAICompat creates Auth entries for OpenAI-compatible providers.
func (s *ConfigSynthesizer) synthesizeOpenAICompat(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0)
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		prefix := strings.TrimSpace(compat.Prefix)
		providerName := strings.ToLower(strings.TrimSpace(compat.Name))
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		internalProviderKey := util.OpenAICompatibleProviderKey(providerName)
		base := strings.TrimSpace(compat.BaseURL)
		disableCooling := compat.DisableCooling

		// Handle new APIKeyEntries format (preferred)
		createdEntries := 0
		for j := range compat.APIKeyEntries {
			entry := &compat.APIKeyEntries[j]
			key := strings.TrimSpace(entry.APIKey)
			proxyURL := strings.TrimSpace(entry.ProxyURL)
			relayBaseURL := strings.TrimSpace(entry.RelayBaseURL)
			if relayBaseURL != "" {
				proxyURL = "" // relay replaces proxy semantics entirely
			}
			idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
			id, token := idGen.Next(idKind, key, base, proxyURL)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  compat.Name,
				"provider_key": internalProviderKey,
				"config_index": strconv.Itoa(i),
			}
			metadata := map[string]any{}
			if disableCooling {
				metadata["disable_cooling"] = true
			}
			// Entry priority takes precedence over the pool-level Priority;
			// a nil entry priority inherits the pool value. An explicit
			// entry *0 stamps "0" (explicit-tier-0 contract), while nil +
			// pool 0 leaves the attribute absent (legacy behavior).
			switch {
			case entry.Priority != nil:
				attrs["priority"] = strconv.Itoa(*entry.Priority)
			case compat.Priority != 0:
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			addWeightToAttrs(entry.Weight, attrs)
			// Stamp the pool-level routing strategy on every entry auth so
			// the conductor can activate aggressive in-pool failover. Empty
			// (unset/unknown) leaves the attribute absent. The circuit-breaker
			// opt-in rides alongside it as the literal "true" (design G3).
			if s := config.NormalizePoolRoutingStrategy(compat.Strategy); s != "" {
				attrs[coreauth.AttributePoolStrategy] = s
			}
			if compat.CircuitBreaker {
				attrs[coreauth.AttributePoolCircuitBreaker] = "true"
			}
			if key != "" {
				attrs["api_key"] = key
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			addOpenAICompatEntryProviderKey(attrs, internalProviderKey, *entry)
			a := &coreauth.Auth{
				ID:           id,
				Provider:     internalProviderKey,
				Label:        compat.Name,
				Prefix:       prefix,
				Status:       coreauth.StatusActive,
				ProxyURL:     proxyURL,
				RelayBaseURL: relayBaseURL,
				Attributes:   attrs,
				Metadata:     metadata,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
			createdEntries++
		}
		// Fallback: create entry without API key if no APIKeyEntries
		if createdEntries == 0 {
			idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
			id, token := idGen.Next(idKind, base)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  compat.Name,
				"provider_key": internalProviderKey,
				"config_index": strconv.Itoa(i),
			}
			metadata := map[string]any{}
			if disableCooling {
				metadata["disable_cooling"] = true
			}
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			// The fallback auth still belongs to the pool, so it carries the
			// pool strategy and the circuit-breaker opt-in the same way the
			// entry auths would.
			if s := config.NormalizePoolRoutingStrategy(compat.Strategy); s != "" {
				attrs[coreauth.AttributePoolStrategy] = s
			}
			if compat.CircuitBreaker {
				attrs[coreauth.AttributePoolCircuitBreaker] = "true"
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   internalProviderKey,
				Label:      compat.Name,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				Attributes: attrs,
				Metadata:   metadata,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
		}
	}
	return out
}

// synthesizeVertexCompat creates Auth entries for Vertex-compatible providers.
func (s *ConfigSynthesizer) synthesizeVertexCompat(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(cfg.VertexCompatAPIKey))
	for i := range cfg.VertexCompatAPIKey {
		compat := &cfg.VertexCompatAPIKey[i]
		providerName := "vertex"
		base := strings.TrimSpace(compat.BaseURL)

		key := strings.TrimSpace(compat.APIKey)
		prefix := strings.TrimSpace(compat.Prefix)
		proxyURL := strings.TrimSpace(compat.ProxyURL)
		idKind := "vertex:apikey"
		id, token := idGen.Next(idKind, key, base, proxyURL)
		attrs := map[string]string{
			"source":       fmt.Sprintf("config:vertex-apikey[%s]", token),
			"base_url":     base,
			"provider_key": providerName,
			"config_index": strconv.Itoa(i),
		}
		if compat.Priority != 0 {
			attrs["priority"] = strconv.Itoa(compat.Priority)
		}
		addWeightToAttrs(compat.Weight, attrs)
		if key != "" {
			attrs["api_key"] = key
		}
		if hash := diff.ComputeVertexCompatModelsHash(compat.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(compat.Headers, attrs)
		addUpstreamProviderKey(attrs, providerName, compat.UpstreamProviderID)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   providerName,
			Label:      "vertex-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, compat.ExcludedModels, "apikey")
		out = append(out, a)
	}
	return out
}

// addOpenCodeGoEntryProviderKey stamps the per-entry routing identifier onto
// the auth's attributes when the opencode-go entry was rendered from a
// PG-backed upstream_providers row: "<provider_key>:key-<childID>". Like the
// Claude analogue, the child-row ID is the only stable identity available,
// so the attribute is emitted strictly when both the parent provider_key
// and the child UpstreamProviderEntryID are positive.
func addOpenCodeGoEntryProviderKey(attrs map[string]string, providerKey string, entry config.OpenCodeGoKey) {
	if attrs == nil || providerKey == "" || entry.UpstreamProviderEntryID <= 0 {
		return
	}
	attrs[coreauth.AttributeEntryProviderKey] = providerKey + ":key-" + strconv.FormatInt(entry.UpstreamProviderEntryID, 10)
}

// synthesizeOpenCodeGo creates Auth entries for opencode-go provider rows,
// one auth per active api_key_entries child. Shape mirrors
// synthesizeOpenAICompat (row + entries), with the routing key derived from
// util.UpstreamProviderKey so the conductor, per-model routing picker, and
// the management test/quota probes all resolve the same identities.
func (s *ConfigSynthesizer) synthesizeOpenCodeGo(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0)
	for i := range cfg.OpenCodeGo {
		row := &cfg.OpenCodeGo[i]
		if row.Disabled {
			continue
		}
		prefix := strings.TrimSpace(row.Prefix)
		base := strings.TrimSpace(row.BaseURL)
		providerKey := util.UpstreamProviderKey("opencode-go", row.Name, row.UpstreamProviderID)
		createdEntries := 0
		for j := range row.APIKeyEntries {
			entry := &row.APIKeyEntries[j]
			if entry.Disabled {
				// Defense-in-depth: the renderer already drops disabled
				// entries; never route from a hand-written YAML row either.
				continue
			}
			key := strings.TrimSpace(entry.APIKey)
			proxyURL := strings.TrimSpace(entry.ProxyURL)
			relayBaseURL := strings.TrimSpace(entry.RelayBaseURL)
			if relayBaseURL != "" {
				proxyURL = "" // relay replaces proxy semantics entirely
			}
			idKind := "opencode-go:apikey"
			id, token := idGen.Next(idKind, key, base, proxyURL)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:opencode-go[%s]", token),
				"base_url":     base,
				"provider_key": providerKey,
				"config_index": strconv.Itoa(i),
			}
			metadata := map[string]any{}
			if row.DisableCooling {
				metadata["disable_cooling"] = true
			}
			// Entry priority takes precedence over the row-level Priority;
			// a nil entry priority inherits the row value. An explicit
			// entry *0 stamps "0" (explicit-tier-0 contract), while nil +
			// row 0 leaves the attribute absent (legacy behavior).
			switch {
			case entry.Priority != nil:
				attrs["priority"] = strconv.Itoa(*entry.Priority)
			case row.Priority != 0:
				attrs["priority"] = strconv.Itoa(row.Priority)
			}
			addWeightToAttrs(entry.Weight, attrs)
			// Stamp the row-level routing strategy on every entry auth so
			// the conductor can activate aggressive in-pool failover. Empty
			// (unset/unknown) leaves the attribute absent. The circuit-breaker
			// opt-in rides alongside it as the literal "true" (design G3).
			if st := config.NormalizePoolRoutingStrategy(row.Strategy); st != "" {
				attrs[coreauth.AttributePoolStrategy] = st
			}
			if row.CircuitBreaker {
				attrs[coreauth.AttributePoolCircuitBreaker] = "true"
			}
			if key != "" {
				attrs["api_key"] = key
			}
			if hash := diff.ComputeOpenCodeGoModelsHash(row.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(row.Headers, attrs)
			addOpenCodeGoEntryProviderKey(attrs, providerKey, *entry)
			a := &coreauth.Auth{
				ID:           id,
				Provider:     "opencode-go",
				Label:        "opencode-go-apikey",
				Prefix:       prefix,
				Status:       coreauth.StatusActive,
				ProxyURL:     proxyURL,
				RelayBaseURL: relayBaseURL,
				Attributes:   attrs,
				Metadata:     metadata,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
			createdEntries++
		}
		// No entries: synthesize a bare row auth (keyless) so the row still
		// shows up live. Divergence from the OpenAI-compat fallback: this
		// path stamps neither pool_strategy nor pool_circuit_breaker — a
		// keyless row has no dispatches to fail over or feed the breaker, so
		// the row-level Strategy/CircuitBreaker only reach the entry auths.
		if createdEntries == 0 {
			idKind := "opencode-go:apikey"
			id, token := idGen.Next(idKind, base)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:opencode-go[%s]", token),
				"base_url":     base,
				"provider_key": providerKey,
				"config_index": strconv.Itoa(i),
			}
			metadata := map[string]any{}
			if row.DisableCooling {
				metadata["disable_cooling"] = true
			}
			if row.Priority != 0 {
				attrs["priority"] = strconv.Itoa(row.Priority)
			}
			if hash := diff.ComputeOpenCodeGoModelsHash(row.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(row.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   "opencode-go",
				Label:      "opencode-go-apikey",
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				Attributes: attrs,
				Metadata:   metadata,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
		}
	}
	return out
}
