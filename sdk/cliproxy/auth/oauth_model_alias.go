package auth

import (
	"strings"

	"github.com/tidwall/gjson"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

type modelAliasEntry interface {
	GetName() string
	GetAlias() string
}

type oauthModelAliasEntry struct {
	upstreamModel         string
	toThinking            string
	toNonThinking         string
	stripThinkingResponse bool
}

type oauthModelAliasTable struct {
	reverse map[string]map[string]oauthModelAliasEntry
}

type OAuthModelAliasResult struct {
	UpstreamModel         string
	OriginalAlias         string
	StripThinkingResponse bool
}

func compileOAuthModelAliasTable(aliases map[string][]internalconfig.OAuthModelAlias) *oauthModelAliasTable {
	if len(aliases) == 0 {
		return &oauthModelAliasTable{}
	}
	out := &oauthModelAliasTable{
		reverse: make(map[string]map[string]oauthModelAliasEntry, len(aliases)),
	}
	for rawChannel, entries := range aliases {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(entries) == 0 {
			continue
		}
		rev := make(map[string]oauthModelAliasEntry, len(entries))
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if name == "" || alias == "" {
				continue
			}
			if strings.EqualFold(name, alias) {
				continue
			}
			aliasKey := strings.ToLower(alias)
			if _, exists := rev[aliasKey]; exists {
				continue
			}
			rev[aliasKey] = oauthModelAliasEntry{
				upstreamModel:         name,
				toThinking:            strings.TrimSpace(entry.ToThinking),
				toNonThinking:         strings.TrimSpace(entry.ToNonThinking),
				stripThinkingResponse: entry.StripThinkingResponse,
			}
		}
		if len(rev) > 0 {
			out.reverse[channel] = rev
		}
	}
	if len(out.reverse) == 0 {
		out.reverse = nil
	}
	return out
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

func resolveModelAliasFromConfigModels(requestedModel string, models []modelAliasEntry) string {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ""
	}
	if len(models) == 0 {
		return ""
	}

	requestResult := thinking.ParseSuffix(requestedModel)
	base := requestResult.ModelName
	candidates := []string{base}
	if base != requestedModel {
		candidates = append(candidates, requestedModel)
	}

	preserveSuffix := func(resolved string) string {
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

	for i := range models {
		name := strings.TrimSpace(models[i].GetName())
		alias := strings.TrimSpace(models[i].GetAlias())
		for _, candidate := range candidates {
			if candidate == "" {
				continue
			}
			if alias != "" && strings.EqualFold(alias, candidate) {
				if name != "" {
					return preserveSuffix(name)
				}
				return preserveSuffix(candidate)
			}
			if name != "" && strings.EqualFold(name, candidate) {
				return preserveSuffix(name)
			}
		}
	}
	return ""
}

// resolveOAuthUpstreamModel resolves the upstream model name from OAuth model alias.
// If an alias exists, returns the original (upstream) model name that corresponds
// to the requested alias.
//
// If the requested model contains a thinking suffix (e.g., "gemini-2.5-pro(8192)"),
// the suffix is preserved in the returned model name. However, if the alias's
// original name already contains a suffix, the config suffix takes priority.
func (m *Manager) resolveOAuthUpstreamModel(auth *Auth, requestedModel string) string {
	result := resolveUpstreamModelFromAliasTableWithThinking(m, auth, requestedModel, modelAliasChannel(auth), false)
	return result.UpstreamModel
}

func (m *Manager) applyOAuthModelAliasWithThinking(auth *Auth, requestedModel string, thinkingEnabled bool) OAuthModelAliasResult {
	result := resolveUpstreamModelFromAliasTableWithThinking(m, auth, requestedModel, modelAliasChannel(auth), thinkingEnabled)
	if result.UpstreamModel == "" {
		return OAuthModelAliasResult{UpstreamModel: requestedModel}
	}
	return result
}

func resolveUpstreamModelFromAliasTableWithThinking(m *Manager, auth *Auth, requestedModel, channel string, thinkingEnabled bool) OAuthModelAliasResult {
	if m == nil || auth == nil {
		return OAuthModelAliasResult{}
	}
	if channel == "" {
		return OAuthModelAliasResult{}
	}

	requestResult := thinking.ParseSuffix(requestedModel)
	baseModel := requestResult.ModelName

	candidates := []string{baseModel}
	if baseModel != requestedModel {
		candidates = append(candidates, requestedModel)
	}

	raw := m.oauthModelAlias.Load()
	table, _ := raw.(*oauthModelAliasTable)
	if table == nil || table.reverse == nil {
		return OAuthModelAliasResult{}
	}
	rev := table.reverse[channel]
	if rev == nil {
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

		targetModel, stripThinking := resolveThinkingTarget(entry, thinkingEnabled)
		if targetModel == "" {
			continue
		}

		if strings.EqualFold(targetModel, baseModel) {
			if !stripThinking {
				return OAuthModelAliasResult{}
			}
			return OAuthModelAliasResult{
				UpstreamModel:         baseModel,
				OriginalAlias:         requestedModel,
				StripThinkingResponse: true,
			}
		}

		var upstreamModel string
		if thinking.ParseSuffix(targetModel).HasSuffix {
			upstreamModel = targetModel
		} else if requestResult.HasSuffix && requestResult.RawSuffix != "" {
			upstreamModel = targetModel + "(" + requestResult.RawSuffix + ")"
		} else {
			upstreamModel = targetModel
		}

		return OAuthModelAliasResult{
			UpstreamModel:         upstreamModel,
			OriginalAlias:         requestedModel,
			StripThinkingResponse: stripThinking,
		}
	}

	return OAuthModelAliasResult{}
}

func resolveThinkingTarget(entry oauthModelAliasEntry, thinkingEnabled bool) (string, bool) {
	if thinkingEnabled {
		if entry.toThinking != "" {
			return entry.toThinking, false
		}
		return entry.upstreamModel, false
	}

	if entry.toNonThinking != "" {
		return entry.toNonThinking, entry.stripThinkingResponse
	}

	return entry.upstreamModel, entry.stripThinkingResponse
}

// modelAliasChannel extracts the OAuth model alias channel from an Auth object.
// It determines the provider and auth kind from the Auth's attributes and delegates
// to OAuthModelAliasChannel for the actual channel resolution.
func modelAliasChannel(auth *Auth) string {
	if auth == nil {
		return ""
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	authKind := ""
	if auth.Attributes != nil {
		authKind = strings.ToLower(strings.TrimSpace(auth.Attributes["auth_kind"]))
	}
	if authKind == "" {
		if kind, _ := auth.AccountInfo(); strings.EqualFold(kind, "api_key") {
			authKind = "apikey"
		}
	}
	return OAuthModelAliasChannel(provider, authKind)
}

// OAuthModelAliasChannel returns the OAuth model alias channel name for a given provider
// and auth kind. Returns empty string if the provider/authKind combination doesn't support
// OAuth model alias (e.g., API key authentication).
//
// Supported channels: gemini-cli, vertex, aistudio, antigravity, claude, codex, qwen, iflow.
func OAuthModelAliasChannel(provider, authKind string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	authKind = strings.ToLower(strings.TrimSpace(authKind))
	switch provider {
	case "gemini":
		// gemini provider uses gemini-api-key config, not oauth-model-alias.
		// OAuth-based gemini auth is converted to "gemini-cli" by the synthesizer.
		return ""
	case "vertex":
		if authKind == "apikey" {
			return ""
		}
		return "vertex"
	case "claude":
		if authKind == "apikey" {
			return ""
		}
		return "claude"
	case "codex":
		if authKind == "apikey" {
			return ""
		}
		return "codex"
	case "gemini-cli", "aistudio", "antigravity", "qwen", "iflow":
		return provider
	default:
		return ""
	}
}

func isThinkingEnabledInPayload(payload []byte, format sdktranslator.Format) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}

	formatName := strings.ToLower(strings.TrimSpace(format.String()))
	if enabled, ok := thinkingEnabledByFormat(payload, formatName); ok {
		return enabled
	}

	if enabled, ok := thinkingEnabledFromModelSuffix(payload); ok {
		return enabled
	}

	return false
}

func thinkingEnabledByFormat(payload []byte, formatName string) (bool, bool) {
	switch formatName {
	case "claude":
		return thinkingEnabledFromClaude(payload)
	case "openai":
		if enabled, ok := thinkingEnabledFromOpenAI(payload, "reasoning_effort"); ok {
			return enabled, true
		}
		return thinkingEnabledFromOpenAI(payload, "reasoning.effort")
	case "openai-response", "codex":
		if enabled, ok := thinkingEnabledFromOpenAI(payload, "reasoning.effort"); ok {
			return enabled, true
		}
		return thinkingEnabledFromOpenAI(payload, "reasoning_effort")
	case "gemini", "gemini-cli", "antigravity":
		if enabled, ok := thinkingEnabledFromGemini(payload, "generationConfig.thinkingConfig"); ok {
			return enabled, true
		}
		return thinkingEnabledFromGemini(payload, "request.generationConfig.thinkingConfig")
	default:
		return false, false
	}
}

func thinkingEnabledFromClaude(payload []byte) (bool, bool) {
	thinkingResult := gjson.GetBytes(payload, "thinking")
	if !thinkingResult.Exists() || !thinkingResult.IsObject() {
		return false, false
	}

	thinkingType := strings.ToLower(strings.TrimSpace(thinkingResult.Get("type").String()))
	if thinkingType == "disabled" {
		return false, true
	}

	if budget := thinkingResult.Get("budget_tokens"); budget.Exists() {
		value := budget.Int()
		switch value {
		case 0:
			return false, true
		case -1:
			return true, true
		default:
			return value > 0, true
		}
	}

	if thinkingType == "enabled" {
		return true, true
	}

	return false, false
}

func thinkingEnabledFromOpenAI(payload []byte, path string) (bool, bool) {
	effort := gjson.GetBytes(payload, path)
	if !effort.Exists() {
		return false, false
	}
	value := strings.ToLower(strings.TrimSpace(effort.String()))
	if value == "" || value == "none" {
		return false, true
	}
	return true, true
}

func thinkingEnabledFromGemini(payload []byte, basePath string) (bool, bool) {
	level := gjson.GetBytes(payload, basePath+".thinkingLevel")
	if level.Exists() {
		value := strings.ToLower(strings.TrimSpace(level.String()))
		if value == "none" || value == "" {
			return false, true
		}
		return true, true
	}

	budget := gjson.GetBytes(payload, basePath+".thinkingBudget")
	if budget.Exists() {
		value := budget.Int()
		switch value {
		case 0:
			return false, true
		case -1:
			return true, true
		default:
			return value > 0, true
		}
	}

	includeThoughts := gjson.GetBytes(payload, basePath+".includeThoughts")
	if includeThoughts.Exists() {
		return includeThoughts.Bool(), true
	}
	includeThoughts = gjson.GetBytes(payload, basePath+".include_thoughts")
	if includeThoughts.Exists() {
		return includeThoughts.Bool(), true
	}

	return false, false
}

func thinkingEnabledFromModelSuffix(payload []byte) (bool, bool) {
	model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	if model == "" {
		return false, false
	}
	result := thinking.ParseSuffix(model)
	if !result.HasSuffix || result.RawSuffix == "" {
		return false, false
	}

	if mode, ok := thinking.ParseSpecialSuffix(result.RawSuffix); ok {
		if mode == thinking.ModeNone {
			return false, true
		}
		return true, true
	}

	if budget, ok := thinking.ParseNumericSuffix(result.RawSuffix); ok {
		if budget == 0 {
			return false, true
		}
		return true, true
	}

	if _, ok := thinking.ParseLevelSuffix(result.RawSuffix); ok {
		return true, true
	}

	return false, false
}
