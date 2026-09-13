package auth

import (
	"encoding/json"
	"strings"
	"sync"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	log "github.com/sirupsen/logrus"
)

const oauthModelAliasesAttributeKey = "model_aliases"

type modelAliasEntry interface {
	GetName() string
	GetAlias() string
	GetForceMapping() bool
}

// oauthModelAliasEntry stores the upstream model name and mapping flags for an alias.
type oauthModelAliasEntry struct {
	upstreamModel string
	configAlias   string
	forceMapping  bool
}

type oauthModelAliasTable struct {
	// reverse maps channel -> alias (lower) -> entry with upstream model and flags.
	reverse map[string]map[string]oauthModelAliasEntry
	// wildcards maps channel -> ordered alias patterns containing '*'. Patterns are
	// consulted only after the exact lookup misses, so existing configurations that
	// use plain aliases keep their behaviour unchanged.
	wildcards map[string][]oauthModelAliasWildcard
}

// oauthModelAliasWildcard pairs a wildcard alias pattern with the entry it resolves to.
type oauthModelAliasWildcard struct {
	pattern string
	entry   oauthModelAliasEntry
}

// OAuthModelAliasResult contains the resolved upstream model and mapping metadata.
type OAuthModelAliasResult struct {
	UpstreamModel string // resolved upstream model name (empty if no mapping found)
	ForceMapping  bool   // whether to rewrite model name in responses
	OriginalAlias string // client-visible model for response rewrite; only applied when ForceMapping is true (see rewriteForceMappedResponse / wrapStreamResult)
}

func compileOAuthModelAliasTable(aliases map[string][]internalconfig.OAuthModelAlias) *oauthModelAliasTable {
	if len(aliases) == 0 {
		return &oauthModelAliasTable{}
	}
	out := &oauthModelAliasTable{
		reverse:   make(map[string]map[string]oauthModelAliasEntry, len(aliases)),
		wildcards: make(map[string][]oauthModelAliasWildcard, len(aliases)),
	}
	for rawChannel, entries := range aliases {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(entries) == 0 {
			continue
		}
		rev := make(map[string]oauthModelAliasEntry, len(entries))
		var wild []oauthModelAliasWildcard
		seenPattern := make(map[string]struct{})
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if name == "" || alias == "" {
				continue
			}
			if strings.EqualFold(name, alias) {
				continue
			}
			resolved := oauthModelAliasEntry{
				upstreamModel: name,
				configAlias:   alias,
				forceMapping:  entry.ForceMapping,
			}
			aliasKey := strings.ToLower(alias)
			if strings.Contains(alias, "*") {
				// Wildcard aliases keep configuration order: the first match wins.
				if _, exists := seenPattern[aliasKey]; exists {
					continue
				}
				seenPattern[aliasKey] = struct{}{}
				wild = append(wild, oauthModelAliasWildcard{pattern: alias, entry: resolved})
				continue
			}
			if _, exists := rev[aliasKey]; exists {
				continue
			}
			rev[aliasKey] = resolved
		}
		if len(rev) > 0 {
			out.reverse[channel] = rev
		}
		if len(wild) > 0 {
			out.wildcards[channel] = wild
		}
	}
	if len(out.reverse) == 0 {
		out.reverse = nil
	}
	if len(out.wildcards) == 0 {
		out.wildcards = nil
	}
	return out
}

// oauthModelAliasResultFor builds the alias result for a matched alias entry.
//
// The boolean reports whether the match is terminal. A matched alias always stops
// the search, including the case where it deliberately resolves to no rewrite.
func oauthModelAliasResultFor(upstreamModel, configAlias string, forceMapping bool, requestedModel, baseModel string, requestResult thinking.SuffixResult) (OAuthModelAliasResult, bool) {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return OAuthModelAliasResult{}, false
	}
	if strings.EqualFold(upstreamModel, baseModel) {
		if !forceMapping {
			return OAuthModelAliasResult{}, true
		}
		return OAuthModelAliasResult{
			UpstreamModel: preserveResolvedModelSuffix(upstreamModel, requestResult),
			ForceMapping:  true,
			OriginalAlias: oauthModelAliasForceMappingResponseModel(configAlias),
		}, true
	}
	originalAlias := requestedModel
	if forceMapping {
		originalAlias = oauthModelAliasForceMappingResponseModel(configAlias)
	}
	return OAuthModelAliasResult{
		UpstreamModel: preserveResolvedModelSuffix(upstreamModel, requestResult),
		ForceMapping:  forceMapping,
		OriginalAlias: originalAlias,
	}, true
}

// SetOAuthModelAlias updates the OAuth model name alias table used during execution.
// The alias is applied per-auth channel to resolve the upstream model name while keeping the
// client-visible model name unchanged for translation/response formatting.
func (m *Manager) SetOAuthModelAlias(aliases map[string][]internalconfig.OAuthModelAlias) {
	if m == nil {
		return
	}
	table := compileOAuthModelAliasTable(aliases)
	// atomic.Value requires non-nil store values.
	if table == nil {
		table = &oauthModelAliasTable{}
	}
	m.oauthModelAlias.Store(table)
}

// applyOAuthModelAlias resolves the upstream model from OAuth model alias.
// If an alias exists, the returned model is the upstream model.
func (m *Manager) applyOAuthModelAlias(auth *Auth, requestedModel string) string {
	upstreamModel := m.resolveOAuthUpstreamModel(auth, requestedModel)
	if upstreamModel == "" {
		return requestedModel
	}
	return upstreamModel
}

func modelAliasLookupCandidates(requestedModel string) (thinking.SuffixResult, []string) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return thinking.SuffixResult{}, nil
	}
	requestResult := thinking.ParseSuffix(requestedModel)
	base := requestResult.ModelName
	if base == "" {
		base = requestedModel
	}
	candidates := []string{requestedModel}
	if base != requestedModel {
		candidates = append(candidates, base)
	}
	return requestResult, candidates
}

func preserveResolvedModelSuffix(resolved string, requestResult thinking.SuffixResult) string {
	resolved = strings.TrimSpace(resolved)
	if resolved == "" {
		return ""
	}
	if thinking.ParseSuffix(resolved).HasSuffix {
		return resolved
	}
	if requestResult.HasSuffix && requestResult.RawSuffix != "" {
		return resolved + "(" + requestResult.RawSuffix + ")"
	}
	return resolved
}

func oauthModelAliasForceMappingResponseModel(configAlias string) string {
	return strings.TrimSpace(configAlias)
}

func resolveModelAliasPoolFromConfigModels(requestedModel string, models []modelAliasEntry) []string {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil
	}
	if len(models) == 0 {
		return nil
	}

	requestResult, candidates := modelAliasLookupCandidates(requestedModel)
	if len(candidates) == 0 {
		return nil
	}

	for _, candidate := range candidates {
		out := make([]string, 0)
		seen := make(map[string]struct{})
		for i := range models {
			name := strings.TrimSpace(models[i].GetName())
			alias := strings.TrimSpace(models[i].GetAlias())
			if candidate == "" || alias == "" || !strings.EqualFold(alias, candidate) {
				continue
			}
			resolved := candidate
			if name != "" {
				resolved = name
			}
			resolved = preserveResolvedModelSuffix(resolved, requestResult)
			key := strings.ToLower(strings.TrimSpace(resolved))
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, resolved)
		}
		if len(out) > 0 {
			return out
		}
	}

	for _, candidate := range candidates {
		for i := range models {
			name := strings.TrimSpace(models[i].GetName())
			if candidate == "" || name == "" || !strings.EqualFold(name, candidate) {
				continue
			}
			return []string{preserveResolvedModelSuffix(name, requestResult)}
		}
	}
	return nil
}

func resolveModelAliasFromConfigModels(requestedModel string, models []modelAliasEntry) string {
	resolved := resolveModelAliasPoolFromConfigModels(requestedModel, models)
	if len(resolved) > 0 {
		return resolved[0]
	}
	return ""
}

func resolveModelAliasResultFromConfigModels(requestedModel string, models []modelAliasEntry) OAuthModelAliasResult {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" || len(models) == 0 {
		return OAuthModelAliasResult{}
	}
	requestResult, candidates := modelAliasLookupCandidates(requestedModel)
	if len(candidates) == 0 {
		return OAuthModelAliasResult{}
	}
	baseModel := requestResult.ModelName
	if baseModel == "" {
		baseModel = requestedModel
	}
	for _, candidate := range candidates {
		key := strings.TrimSpace(candidate)
		if key == "" {
			continue
		}
		for i := range models {
			original := strings.TrimSpace(models[i].GetName())
			alias := strings.TrimSpace(models[i].GetAlias())
			if original == "" || alias == "" || !strings.EqualFold(alias, key) {
				continue
			}
			if strings.EqualFold(original, baseModel) {
				if !models[i].GetForceMapping() {
					return OAuthModelAliasResult{}
				}
				return OAuthModelAliasResult{
					UpstreamModel: preserveResolvedModelSuffix(original, requestResult),
					ForceMapping:  models[i].GetForceMapping(),
					OriginalAlias: oauthModelAliasForceMappingResponseModel(alias),
				}
			}
			originalAlias := requestedModel
			if models[i].GetForceMapping() {
				originalAlias = oauthModelAliasForceMappingResponseModel(alias)
			}
			return OAuthModelAliasResult{
				UpstreamModel: preserveResolvedModelSuffix(original, requestResult),
				ForceMapping:  models[i].GetForceMapping(),
				OriginalAlias: originalAlias,
			}
		}
	}
	return OAuthModelAliasResult{}
}

// resolveOAuthUpstreamModel resolves the upstream model name from OAuth model alias.
// If an alias exists, returns the original (upstream) model name that corresponds
// to the requested alias.
//
// If the requested model contains a thinking suffix (e.g., "gemini-2.5-pro(8192)"),
// the suffix is preserved in the returned model name. However, if the alias's
// original name already contains a suffix, the config suffix takes priority.
func (m *Manager) resolveOAuthUpstreamModel(auth *Auth, requestedModel string) string {
	result := m.resolveOAuthModelAliasWithResult(auth, requestedModel)
	return result.UpstreamModel
}

func (m *Manager) resolveOAuthModelAliasWithResult(auth *Auth, requestedModel string) OAuthModelAliasResult {
	channel := modelAliasChannel(auth)
	if channel == "" {
		return OAuthModelAliasResult{}
	}
	if result := resolveUpstreamModelFromAliases(OAuthModelAliasesFromAttributes(authAttributes(auth)), requestedModel); result.UpstreamModel != "" {
		return result
	}
	return resolveUpstreamModelFromAliasTable(m, auth, requestedModel, channel)
}

func authAttributes(auth *Auth) map[string]string {
	if auth == nil {
		return nil
	}
	return auth.Attributes
}

// SetOAuthModelAliasesAttribute stores sanitized per-auth OAuth model aliases on an auth entry.
func SetOAuthModelAliasesAttribute(auth *Auth, aliases []internalconfig.OAuthModelAlias) {
	if auth == nil {
		return
	}
	aliases = sanitizeOAuthModelAliases(aliases)
	if len(aliases) == 0 {
		return
	}
	warnUnroutablePerAuthWildcardAliases(auth, aliases)
	data, errMarshal := json.Marshal(aliases)
	if errMarshal != nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[oauthModelAliasesAttributeKey] = string(data)
}

// OAuthModelAliasesFromAttributes returns sanitized per-auth OAuth model aliases from auth attributes.
func OAuthModelAliasesFromAttributes(attributes map[string]string) []internalconfig.OAuthModelAlias {
	if len(attributes) == 0 {
		return nil
	}
	raw := strings.TrimSpace(attributes[oauthModelAliasesAttributeKey])
	if raw == "" {
		return nil
	}
	var aliases []internalconfig.OAuthModelAlias
	if errUnmarshal := json.Unmarshal([]byte(raw), &aliases); errUnmarshal != nil {
		return nil
	}
	return sanitizeOAuthModelAliases(aliases)
}

func sanitizeOAuthModelAliases(aliases []internalconfig.OAuthModelAlias) []internalconfig.OAuthModelAlias {
	if len(aliases) == 0 {
		return nil
	}
	cfg := internalconfig.Config{
		OAuthModelAlias: map[string][]internalconfig.OAuthModelAlias{
			"auth": aliases,
		},
	}
	cfg.SanitizeOAuthModelAlias()
	clean := cfg.OAuthModelAlias["auth"]
	if len(clean) == 0 {
		return nil
	}
	return append([]internalconfig.OAuthModelAlias(nil), clean...)
}

func resolveUpstreamModelFromAliases(aliases []internalconfig.OAuthModelAlias, requestedModel string) OAuthModelAliasResult {
	if len(aliases) == 0 {
		return OAuthModelAliasResult{}
	}
	requestResult, candidates := modelAliasLookupCandidates(requestedModel)
	if len(candidates) == 0 {
		return OAuthModelAliasResult{}
	}
	baseModel := requestResult.ModelName
	if baseModel == "" {
		baseModel = strings.TrimSpace(requestedModel)
	}
	// Exact aliases are resolved first; wildcard patterns are consulted only when no
	// exact alias matched any candidate, so plain configurations are unaffected.
	for _, exactPass := range []bool{true, false} {
		for _, candidate := range candidates {
			key := strings.TrimSpace(candidate)
			if key == "" {
				continue
			}
			for _, entry := range aliases {
				alias := strings.TrimSpace(entry.Alias)
				if alias == "" {
					continue
				}
				if strings.Contains(alias, "*") == exactPass {
					continue
				}
				if exactPass {
					if !strings.EqualFold(alias, key) {
						continue
					}
				} else if !registry.MatchModelPattern(alias, key) {
					continue
				}
				// A wildcard alias is a pattern, not a client-visible model id, so a
				// force-mapped response must echo the concrete model the client asked
				// for rather than the configured pattern. The thinking suffix is
				// dropped the same way an exact alias never carries one.
				responseAlias := alias
				if !exactPass {
					responseAlias = baseModel
				}
				if result, stop := oauthModelAliasResultFor(entry.Name, responseAlias, entry.ForceMapping, requestedModel, baseModel, requestResult); stop {
					return result
				}
			}
		}
	}
	return OAuthModelAliasResult{}
}

func (m *Manager) applyOAuthModelAliasWithResult(auth *Auth, requestedModel string) OAuthModelAliasResult {
	result := m.resolveOAuthModelAliasWithResult(auth, requestedModel)
	if result.UpstreamModel == "" {
		return OAuthModelAliasResult{UpstreamModel: requestedModel}
	}
	return result
}

func resolveUpstreamModelFromAliasTable(m *Manager, auth *Auth, requestedModel, channel string) OAuthModelAliasResult {
	if m == nil || auth == nil {
		return OAuthModelAliasResult{}
	}
	if channel == "" {
		return OAuthModelAliasResult{}
	}

	requestResult, candidates := modelAliasLookupCandidates(requestedModel)
	baseModel := requestResult.ModelName

	raw := m.oauthModelAlias.Load()
	table, _ := raw.(*oauthModelAliasTable)
	if table == nil {
		return OAuthModelAliasResult{}
	}
	var rev map[string]oauthModelAliasEntry
	if table.reverse != nil {
		rev = table.reverse[channel]
	}
	var wild []oauthModelAliasWildcard
	if table.wildcards != nil {
		wild = table.wildcards[channel]
	}
	if len(rev) == 0 && len(wild) == 0 {
		return OAuthModelAliasResult{}
	}

	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate))
		if key == "" {
			continue
		}
		entry, exists := rev[key]
		if !exists {
			continue
		}
		if result, stop := oauthModelAliasResultFor(entry.upstreamModel, entry.configAlias, entry.forceMapping, requestedModel, baseModel, requestResult); stop {
			return result
		}
	}

	// Wildcard patterns are consulted only after every exact candidate missed, which
	// keeps an exact alias authoritative whenever both could match.
	for _, candidate := range candidates {
		value := strings.TrimSpace(candidate)
		if value == "" {
			continue
		}
		for _, wildcard := range wild {
			if !registry.MatchModelPattern(wildcard.pattern, value) {
				continue
			}
			// The configured alias is a pattern here, so a force-mapped response echoes
			// the concrete requested model instead of the pattern itself. The thinking
			// suffix is dropped the same way an exact alias never carries one.
			if result, stop := oauthModelAliasResultFor(wildcard.entry.upstreamModel, baseModel, wildcard.entry.forceMapping, requestedModel, baseModel, requestResult); stop {
				return result
			}
		}
	}

	return OAuthModelAliasResult{}
}

// modelAliasChannel extracts the OAuth model alias channel from an Auth object.
// It determines the provider and auth kind from the Auth's attributes and delegates
// to OAuthModelAliasChannel for the actual channel resolution.
func modelAliasChannel(auth *Auth) string {
	if auth == nil {
		return ""
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	authKind := auth.AuthKind()
	return OAuthModelAliasChannel(provider, authKind)
}

// OAuthModelAliasChannel returns the OAuth model alias channel name for a given provider
// and auth kind. Returns empty string if the provider/authKind combination doesn't support
// OAuth model alias (e.g., API key authentication).
//
// Built-in channels: vertex, aistudio, antigravity, claude, codex, kimi.
// Plugin OAuth providers use their normalized provider key as the channel.
func OAuthModelAliasChannel(provider, authKind string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	authKind = normalizeOAuthModelAliasAuthKind(authKind)
	if authKind == "apikey" {
		return ""
	}
	switch provider {
	case "gemini":
		return ""
	case "vertex":
		return "vertex"
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	case "aistudio", "antigravity", "kimi":
		return provider
	default:
		return provider
	}
}

func normalizeOAuthModelAliasAuthKind(authKind string) string {
	authKind = strings.ToLower(strings.TrimSpace(authKind))
	switch authKind {
	case "api_key", "api-key":
		return "apikey"
	default:
		return authKind
	}
}

// warnedPerAuthWildcardAliases remembers the pattern list already reported for an
// auth ID. Auth files are re-synthesized on every watcher event, including the
// rewrite that follows a token refresh, so without this the warning would repeat on
// every reload for the lifetime of the process.
var warnedPerAuthWildcardAliases sync.Map // auth ID -> joined pattern list

// warnUnroutablePerAuthWildcardAliases reports per-auth wildcard aliases that the
// handler-level provider lookup cannot see.
//
// Only the global oauth-model-alias configuration feeds the registry pattern table,
// so a wildcard declared in an auth file resolves once a credential is selected but
// never makes the matching model routable on its own. Saying so keeps the gap
// visible instead of surfacing as an opaque model_not_found.
func warnUnroutablePerAuthWildcardAliases(auth *Auth, aliases []internalconfig.OAuthModelAlias) {
	if auth == nil {
		return
	}
	patterns := make([]string, 0, len(aliases))
	for _, entry := range aliases {
		alias := strings.TrimSpace(entry.Alias)
		if alias != "" && strings.Contains(alias, "*") {
			patterns = append(patterns, alias)
		}
	}
	if len(patterns) == 0 {
		return
	}
	joined := strings.Join(patterns, ",")
	if previous, reported := warnedPerAuthWildcardAliases.Load(auth.ID); reported && previous == joined {
		return
	}
	warnedPerAuthWildcardAliases.Store(auth.ID, joined)
	log.Warnf("auth %s: per-auth wildcard model aliases %v are not published for routing; declare them under oauth-model-alias so requests matching the pattern can reach a provider", auth.ID, patterns)
}
