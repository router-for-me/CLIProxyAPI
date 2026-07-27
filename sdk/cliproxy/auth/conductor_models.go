package auth

import (
	"bytes"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func (m *Manager) lookupAPIKeyUpstreamModel(authID, requestedModel string) string {
	return lookupAPIKeyUpstreamModel(m.loadAPIKeyModelRouting(), authID, requestedModel)
}

func lookupAPIKeyUpstreamModel(snapshot *apiKeyModelRoutingSnapshot, authID, requestedModel string) string {
	if snapshot == nil {
		return ""
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return ""
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ""
	}
	byAlias := snapshot.aliases[authID]
	if len(byAlias) == 0 {
		return ""
	}
	key := strings.ToLower(thinking.ParseSuffix(requestedModel).ModelName)
	if key == "" {
		key = strings.ToLower(requestedModel)
	}
	resolved := strings.TrimSpace(byAlias[key])
	if resolved == "" {
		return ""
	}
	return preserveRequestedModelSuffix(requestedModel, resolved)
}

func isAPIKeyAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	return auth.AuthKind() == AuthKindAPIKey || IsConfigAPIKeyAuth(auth)
}

func isOpenAICompatAPIKeyAuth(auth *Auth) bool {
	if !isAPIKeyAuth(auth) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		return true
	}
	if auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["compat_name"]) != ""
}

func openAICompatProviderKey(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if providerKey := strings.TrimSpace(auth.Attributes["provider_key"]); providerKey != "" {
			return util.OpenAICompatibleProviderKey(providerKey)
		}
		if compatName := strings.TrimSpace(auth.Attributes["compat_name"]); compatName != "" {
			return util.OpenAICompatibleProviderKey(compatName)
		}
	}
	return util.OpenAICompatibleProviderKey(auth.Provider)
}

func openAICompatModelPoolKey(auth *Auth, requestedModel string) string {
	base := strings.TrimSpace(thinking.ParseSuffix(requestedModel).ModelName)
	if base == "" {
		base = strings.TrimSpace(requestedModel)
	}
	return strings.ToLower(strings.TrimSpace(auth.ID)) + "|" + openAICompatProviderKey(auth) + "|" + strings.ToLower(base)
}

func (m *Manager) nextModelPoolOffset(key string, size int) int {
	if m == nil || size <= 1 {
		return 0
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.modelPoolOffsets == nil {
		m.modelPoolOffsets = make(map[string]int)
	}
	offset := m.modelPoolOffsets[key]
	if offset >= 2_147_483_640 {
		offset = 0
	}
	m.modelPoolOffsets[key] = offset + 1
	if size <= 0 {
		return 0
	}
	return offset % size
}

func rotateStrings(values []string, offset int) []string {
	if len(values) <= 1 {
		return values
	}
	if offset <= 0 {
		out := make([]string, len(values))
		copy(out, values)
		return out
	}
	offset = offset % len(values)
	out := make([]string, 0, len(values))
	out = append(out, values[offset:]...)
	out = append(out, values[:offset]...)
	return out
}

func (m *Manager) resolveOpenAICompatUpstreamModelPool(auth *Auth, requestedModel string) []string {
	return resolveOpenAICompatUpstreamModelPool(m.loadAPIKeyModelRouting().config, auth, requestedModel)
}

func resolveOpenAICompatUpstreamModelPool(cfg *internalconfig.Config, auth *Auth, requestedModel string) []string {
	if !isOpenAICompatAPIKeyAuth(auth) {
		return nil
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil
	}
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	providerKey := ""
	compatName := ""
	if auth.Attributes != nil {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, auth.Provider)
	if entry == nil {
		return nil
	}
	return resolveModelAliasPoolFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func preserveRequestedModelSuffix(requestedModel, resolved string) string {
	return preserveResolvedModelSuffix(resolved, thinking.ParseSuffix(requestedModel))
}

func (m *Manager) executionModelCandidates(auth *Auth, routeModel string) []string {
	routing := m.loadAPIKeyModelRouting()
	if auth != nil && auth.Attributes != nil {
		if homeModel := strings.TrimSpace(auth.Attributes[homeUpstreamModelAttributeKey]); homeModel != "" {
			return []string{homeModel}
		}
	}
	requestedModel := rewriteModelForAuth(routeModel, auth)
	requestedModel = m.applyOAuthModelAlias(auth, requestedModel)
	if pool := resolveOpenAICompatUpstreamModelPool(routing.config, auth, requestedModel); len(pool) > 0 {
		if len(pool) == 1 {
			return pool
		}
		offset := m.nextModelPoolOffset(openAICompatModelPoolKey(auth, requestedModel), len(pool))
		return rotateStrings(pool, offset)
	}
	resolved := m.applyAPIKeyModelAliasWithRouting(routing, auth, requestedModel)
	if strings.TrimSpace(resolved) == "" {
		resolved = requestedModel
	}
	return []string{resolved}
}

func (m *Manager) selectionModelForAuth(auth *Auth, routeModel string) string {
	requestedModel := rewriteModelForAuth(routeModel, auth)
	if strings.TrimSpace(requestedModel) == "" {
		requestedModel = strings.TrimSpace(routeModel)
	}
	resolvedModel := m.applyOAuthModelAlias(auth, requestedModel)
	if strings.TrimSpace(resolvedModel) == "" {
		resolvedModel = requestedModel
	}
	return resolvedModel
}

func (m *Manager) selectionModelKeyForAuth(auth *Auth, routeModel string) string {
	return canonicalModelKey(m.selectionModelForAuth(auth, routeModel))
}

func (m *Manager) stateModelForExecution(auth *Auth, routeModel, upstreamModel string, pooled bool) string {
	if auth != nil && auth.Attributes != nil {
		if homeModel := strings.TrimSpace(auth.Attributes[homeUpstreamModelAttributeKey]); homeModel != "" {
			if resolved := strings.TrimSpace(upstreamModel); resolved != "" {
				return resolved
			}
			return homeModel
		}
	}
	stateModel := executionResultModel(routeModel, upstreamModel, pooled)
	selectionModel := m.selectionModelForAuth(auth, routeModel)
	if canonicalModelKey(selectionModel) == canonicalModelKey(upstreamModel) && strings.TrimSpace(selectionModel) != "" {
		return strings.TrimSpace(upstreamModel)
	}
	return stateModel
}

func executionResultModel(routeModel, upstreamModel string, pooled bool) string {
	if pooled {
		if resolved := strings.TrimSpace(upstreamModel); resolved != "" {
			return resolved
		}
	}
	if requested := strings.TrimSpace(routeModel); requested != "" {
		return requested
	}
	return strings.TrimSpace(upstreamModel)
}

func (m *Manager) filterExecutionModels(auth *Auth, routeModel string, candidates []string, pooled bool) []string {
	if len(candidates) == 0 {
		return nil
	}
	now := time.Now()
	out := make([]string, 0, len(candidates))
	for _, upstreamModel := range candidates {
		stateModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
		blocked, _, _ := isAuthBlockedForModel(auth, stateModel, now)
		if blocked {
			continue
		}
		out = append(out, upstreamModel)
	}
	return out
}

func (m *Manager) preparedExecutionModels(auth *Auth, routeModel string) ([]string, bool) {
	candidates := m.executionModelCandidates(auth, routeModel)
	pooled := len(candidates) > 1
	return m.filterExecutionModels(auth, routeModel, candidates, pooled), pooled
}

func (m *Manager) preparedExecutionModelsWithAlias(auth *Auth, routeModel string, executionKind apiKeyModelExecutionKind) ([]string, bool, OAuthModelAliasResult, *apiKeyModelRoutingSnapshot) {
	candidates, _, aliasResult, routing := m.executionModelCandidatesWithAlias(auth, routeModel)
	candidates = filterAPIKeyModelCandidates(routing, auth, routeModel, candidates, executionKind)
	pooled := len(candidates) > 1
	return m.filterExecutionModels(auth, routeModel, candidates, pooled), pooled, aliasResult, routing
}

func (m *Manager) executionModelCandidatesWithAlias(auth *Auth, routeModel string) ([]string, bool, OAuthModelAliasResult, *apiKeyModelRoutingSnapshot) {
	routing := m.loadAPIKeyModelRouting()
	requestedModel := rewriteModelForAuth(routeModel, auth)
	aliasResult := m.resolveExecutionAliasResultForRequestedWithRouting(routing, auth, requestedModel)
	if aliasResult.ForceMapping && auth != nil && auth.Attributes != nil && strings.EqualFold(strings.TrimSpace(auth.Attributes[homeForceMappingAttributeKey]), "true") {
		aliasResult.OriginalAlias = strings.TrimSpace(routeModel)
	}
	upstreamModel := executionAliasPoolModel(auth, requestedModel, aliasResult)

	var candidates []string
	if auth != nil && auth.Attributes != nil {
		if homeModel := strings.TrimSpace(auth.Attributes[homeUpstreamModelAttributeKey]); homeModel != "" {
			candidates = []string{homeModel}
		}
	}
	if len(candidates) == 0 {
		if pool := resolveOpenAICompatUpstreamModelPool(routing.config, auth, upstreamModel); len(pool) > 0 {
			if len(pool) == 1 {
				candidates = pool
			} else {
				offset := m.nextModelPoolOffset(openAICompatModelPoolKey(auth, upstreamModel), len(pool))
				candidates = rotateStrings(pool, offset)
			}
		} else {
			resolved := m.applyAPIKeyModelAliasWithRouting(routing, auth, upstreamModel)
			if strings.TrimSpace(resolved) == "" {
				resolved = upstreamModel
			}
			candidates = []string{resolved}
		}
	}
	pooled := len(candidates) > 1
	return candidates, pooled, aliasResult, routing
}

func (m *Manager) resolveExecutionAliasResult(auth *Auth, routeModel string) OAuthModelAliasResult {
	requestedModel := rewriteModelForAuth(routeModel, auth)
	return m.resolveExecutionAliasResultForRequested(auth, requestedModel)
}

func (m *Manager) resolveExecutionAliasResultForRequested(auth *Auth, requestedModel string) OAuthModelAliasResult {
	return m.resolveExecutionAliasResultForRequestedWithRouting(m.loadAPIKeyModelRouting(), auth, requestedModel)
}

func (m *Manager) resolveExecutionAliasResultForRequestedWithRouting(routing *apiKeyModelRoutingSnapshot, auth *Auth, requestedModel string) OAuthModelAliasResult {
	if result := homeForceMappingAliasResult(auth, requestedModel); result.ForceMapping {
		return result
	}
	if isAPIKeyAuth(auth) {
		return resolveAPIKeyModelAliasWithResult(routing.config, auth, requestedModel)
	}
	return m.applyOAuthModelAliasWithResult(auth, requestedModel)
}

func homeForceMappingAliasResult(auth *Auth, requestedModel string) OAuthModelAliasResult {
	if auth == nil || auth.Attributes == nil || !strings.EqualFold(strings.TrimSpace(auth.Attributes[homeForceMappingAttributeKey]), "true") {
		return OAuthModelAliasResult{}
	}
	originalAlias := strings.TrimSpace(auth.Attributes[homeOriginalAliasAttributeKey])
	canonicalOriginalAlias := canonicalHomeConcurrencyModelKey(auth.Attributes[homeOriginalAliasAttributeKey])
	canonicalRequestedModel := canonicalHomeConcurrencyModelKey(requestedModel)
	if canonicalOriginalAlias == "" || canonicalOriginalAlias != canonicalRequestedModel {
		return OAuthModelAliasResult{}
	}
	upstreamModel := strings.TrimSpace(auth.Attributes[homeUpstreamModelAttributeKey])
	if upstreamModel == "" {
		upstreamModel = strings.TrimSpace(requestedModel)
	}
	return OAuthModelAliasResult{
		UpstreamModel: upstreamModel,
		ForceMapping:  true,
		OriginalAlias: originalAlias,
	}
}

func executionAliasPoolModel(auth *Auth, requestedModel string, aliasResult OAuthModelAliasResult) string {
	if isAPIKeyAuth(auth) {
		if strings.TrimSpace(requestedModel) != "" {
			return requestedModel
		}
	}
	if strings.TrimSpace(aliasResult.UpstreamModel) != "" {
		return aliasResult.UpstreamModel
	}
	return requestedModel
}

func (m *Manager) resolveAPIKeyModelAliasWithResult(auth *Auth, requestedModel string) OAuthModelAliasResult {
	return resolveAPIKeyModelAliasWithResult(m.loadAPIKeyModelRouting().config, auth, requestedModel)
}

func resolveAPIKeyModelAliasWithResult(cfg *internalconfig.Config, auth *Auth, requestedModel string) OAuthModelAliasResult {
	if auth == nil {
		return OAuthModelAliasResult{}
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return OAuthModelAliasResult{}
	}
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	var models []modelAliasEntry
	switch provider {
	case "gemini":
		if entry := resolveGeminiAPIKeyConfig(cfg, auth); entry != nil {
			models = asModelAliasEntries(entry.Models)
		}
	case "gemini-interactions":
		if entry := resolveInteractionsAPIKeyConfig(cfg, auth); entry != nil {
			models = asModelAliasEntries(entry.Models)
		}
	case "claude":
		if entry := resolveClaudeAPIKeyConfig(cfg, auth); entry != nil {
			models = asModelAliasEntries(entry.Models)
		}
	case "codex":
		if entry := resolveCodexAPIKeyConfig(cfg, auth); entry != nil {
			models = asModelAliasEntries(entry.Models)
		}
	case "xai":
		if entry := resolveXAIAPIKeyConfig(cfg, auth); entry != nil {
			models = asModelAliasEntries(entry.Models)
		}
	case "vertex":
		if entry := resolveVertexAPIKeyConfig(cfg, auth); entry != nil {
			models = asModelAliasEntries(entry.Models)
		}
	default:
		providerKey := ""
		compatName := ""
		if auth.Attributes != nil {
			providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
			compatName = strings.TrimSpace(auth.Attributes["compat_name"])
		}
		if compatName != "" || strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
			if entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, auth.Provider); entry != nil {
				models = asModelAliasEntries(entry.Models)
			}
		}
	}
	if len(models) == 0 {
		return OAuthModelAliasResult{UpstreamModel: requestedModel}
	}
	result := resolveModelAliasResultFromConfigModels(requestedModel, models)
	if strings.TrimSpace(result.UpstreamModel) == "" {
		return OAuthModelAliasResult{UpstreamModel: requestedModel}
	}
	return result
}

func (m *Manager) prepareExecutionModels(auth *Auth, routeModel string) []string {
	models, _ := m.preparedExecutionModels(auth, routeModel)
	return models
}

func rewriteForceMappedResponse(resp *cliproxyexecutor.Response, aliasResult OAuthModelAliasResult) {
	if resp == nil || !aliasResult.ForceMapping || strings.TrimSpace(aliasResult.OriginalAlias) == "" {
		return
	}
	resp.Payload = rewriteModelInResponse(resp.Payload, aliasResult.OriginalAlias)
}

func rewriteForceMappedStreamChunk(rewriter *StreamRewriter, payload []byte) []byte {
	if rewriter == nil || len(payload) == 0 {
		return payload
	}
	rewritten := rewriter.RewriteChunk(payload)
	if len(rewritten) > 0 {
		return rewritten
	}
	if bytes.Contains(payload, []byte("data:")) {
		if lineWise := rewriteSSEPayloadLines(payload, rewriter.options.RewriteModel); len(lineWise) > 0 {
			return lineWise
		}
	}
	if len(rewriter.pendingBuf) > 0 {
		return nil
	}
	return nil
}

func finishForceMappedStreamChunks(rewriter *StreamRewriter) []byte {
	if rewriter == nil {
		return nil
	}
	return rewriter.Finish()
}

func (m *Manager) rebuildAPIKeyModelAliasFromRuntimeConfig() {
	if m == nil {
		return
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rebuildAPIKeyModelAliasLocked(cfg)
}

// RefreshAPIKeyModelAlias rebuilds API-key model aliases and configured capabilities.
func (m *Manager) RefreshAPIKeyModelAlias() {
	m.rebuildAPIKeyModelAliasFromRuntimeConfig()
}

func (m *Manager) rebuildAPIKeyModelAliasLocked(cfg *internalconfig.Config) {
	if m == nil {
		return
	}
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}

	out := make(apiKeyModelAliasTable)
	capabilities := make(apiKeyModelCapabilityTable)
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		if strings.TrimSpace(auth.ID) == "" {
			continue
		}
		if !isAPIKeyAuth(auth) {
			continue
		}

		byAlias := make(map[string]string)
		byCapability := make(map[string][]apiKeyModelCapabilityRoute)
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		switch provider {
		case "gemini":
			if entry := resolveGeminiAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
				compileAPIKeyModelCapabilities(byCapability, entry.Models, "gemini")
			}
		case "gemini-interactions":
			if entry := resolveInteractionsAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
				compileAPIKeyModelCapabilities(byCapability, entry.Models, "interactions")
			}
		case "claude":
			if entry := resolveClaudeAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
				compileAPIKeyModelCapabilities(byCapability, entry.Models, "claude")
			}
		case "codex":
			if entry := resolveCodexAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
				compileAPIKeyModelCapabilities(byCapability, entry.Models, "codex")
			}
		case "xai":
			if entry := resolveXAIAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
				compileAPIKeyModelCapabilities(byCapability, entry.Models, "xai")
			}
		case "vertex":
			if entry := resolveVertexAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
				compileAPIKeyModelCapabilities(byCapability, entry.Models, "gemini")
			}
		default:
			// OpenAI-compat uses config selection from auth.Attributes.
			providerKey := ""
			compatName := ""
			if auth.Attributes != nil {
				providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
				compatName = strings.TrimSpace(auth.Attributes["compat_name"])
			}
			if compatName != "" || strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
				if entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, auth.Provider); entry != nil {
					compileAPIKeyModelAliasForModels(byAlias, entry.Models)
					compileOpenAICompatModelCapabilities(byCapability, entry.Models)
				}
			}
		}

		if len(byAlias) > 0 {
			out[strings.TrimSpace(auth.ID)] = byAlias
		}
		if len(byCapability) > 0 {
			capabilities[strings.TrimSpace(auth.ID)] = byCapability
		}
	}

	m.apiKeyModelRouting.Store(&apiKeyModelRoutingSnapshot{
		config:       cfg,
		aliases:      out,
		capabilities: capabilities,
	})
}

func compileAPIKeyModelAliasForModels[T interface {
	GetName() string
	GetAlias() string
}](out map[string]string, models []T) {
	if out == nil {
		return
	}
	for i := range models {
		alias := strings.TrimSpace(models[i].GetAlias())
		name := strings.TrimSpace(models[i].GetName())
		if alias == "" || name == "" {
			continue
		}
		aliasKey := strings.ToLower(thinking.ParseSuffix(alias).ModelName)
		if aliasKey == "" {
			aliasKey = strings.ToLower(alias)
		}
		// Config priority: first alias wins.
		if _, exists := out[aliasKey]; exists {
			continue
		}
		out[aliasKey] = name
		// Also allow direct lookup by upstream name (case-insensitive), so lookups on already-upstream
		// models remain a cheap no-op.
		nameKey := strings.ToLower(thinking.ParseSuffix(name).ModelName)
		if nameKey == "" {
			nameKey = strings.ToLower(name)
		}
		if nameKey != "" {
			if _, exists := out[nameKey]; !exists {
				out[nameKey] = name
			}
		}
		// Preserve config suffix priority by seeding a base-name lookup when name already has suffix.
		nameResult := thinking.ParseSuffix(name)
		if nameResult.HasSuffix {
			baseKey := strings.ToLower(strings.TrimSpace(nameResult.ModelName))
			if baseKey != "" {
				if _, exists := out[baseKey]; !exists {
					out[baseKey] = name
				}
			}
		}
	}
}

func rewriteModelForAuth(model string, auth *Auth) string {
	if auth == nil || model == "" {
		return model
	}
	prefix := strings.TrimSpace(auth.Prefix)
	if prefix == "" {
		return model
	}
	needle := prefix + "/"
	if !strings.HasPrefix(model, needle) {
		return model
	}
	return strings.TrimPrefix(model, needle)
}

func (m *Manager) applyAPIKeyModelAlias(auth *Auth, requestedModel string) string {
	return m.applyAPIKeyModelAliasWithRouting(m.loadAPIKeyModelRouting(), auth, requestedModel)
}

func (m *Manager) applyAPIKeyModelAliasWithRouting(routing *apiKeyModelRoutingSnapshot, auth *Auth, requestedModel string) string {
	if auth == nil {
		return requestedModel
	}

	if !isAPIKeyAuth(auth) {
		return requestedModel
	}

	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return requestedModel
	}

	// Fast path: lookup per-auth mapping table (keyed by auth.ID).
	if resolved := lookupAPIKeyUpstreamModel(routing, auth.ID, requestedModel); resolved != "" {
		return resolved
	}

	// Slow path: scan config for the matching credential entry and resolve alias.
	// This acts as a safety net if mappings are stale or auth.ID is missing.
	cfg := routing.config
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}

	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	upstreamModel := ""
	switch provider {
	case "gemini":
		upstreamModel = resolveUpstreamModelForGeminiAPIKey(cfg, auth, requestedModel)
	case "gemini-interactions":
		upstreamModel = resolveUpstreamModelForInteractionsAPIKey(cfg, auth, requestedModel)
	case "claude":
		upstreamModel = resolveUpstreamModelForClaudeAPIKey(cfg, auth, requestedModel)
	case "codex":
		upstreamModel = resolveUpstreamModelForCodexAPIKey(cfg, auth, requestedModel)
	case "xai":
		upstreamModel = resolveUpstreamModelForXAIAPIKey(cfg, auth, requestedModel)
	case "vertex":
		upstreamModel = resolveUpstreamModelForVertexAPIKey(cfg, auth, requestedModel)
	default:
		upstreamModel = resolveUpstreamModelForOpenAICompatAPIKey(cfg, auth, requestedModel)
	}

	// Return upstream model if found, otherwise return requested model.
	if upstreamModel != "" {
		return upstreamModel
	}
	return requestedModel
}

// APIKeyConfigEntry is a generic interface for API key configurations.
type APIKeyConfigEntry interface {
	GetAPIKey() string
	GetBaseURL() string
}

func resolveAPIKeyConfig[T APIKeyConfigEntry](entries []T, auth *Auth) *T {
	if auth == nil || len(entries) == 0 {
		return nil
	}
	attrKey, attrBase := "", ""
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range entries {
		entry := &entries[i]
		cfgKey := strings.TrimSpace((*entry).GetAPIKey())
		cfgBase := strings.TrimSpace((*entry).GetBaseURL())
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range entries {
			entry := &entries[i]
			if strings.EqualFold(strings.TrimSpace((*entry).GetAPIKey()), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func resolveGeminiAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.GeminiKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.GeminiKey, auth)
}

func resolveInteractionsAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.GeminiKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.InteractionsKey, auth)
}

func resolveClaudeAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.ClaudeKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.ClaudeKey, auth)
}

func resolveCodexAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.CodexKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.CodexKey, auth)
}

func resolveXAIAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.XAIKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.XAIKey, auth)
}

func resolveVertexAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.VertexCompatKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.VertexCompatAPIKey, auth)
}

func resolveUpstreamModelForGeminiAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveGeminiAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForInteractionsAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveInteractionsAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForClaudeAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveClaudeAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForCodexAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveCodexAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForXAIAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveXAIAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForVertexAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveVertexAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForOpenAICompatAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	providerKey := ""
	compatName := ""
	if auth != nil && len(auth.Attributes) > 0 {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	if compatName == "" && !strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		return ""
	}
	entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, auth.Provider)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

type apiKeyModelAliasTable map[string]map[string]string

func resolveOpenAICompatConfig(cfg *internalconfig.Config, providerKey, compatName, authProvider string) *internalconfig.OpenAICompatibility {
	if cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 3)
	if v := strings.TrimSpace(compatName); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(providerKey); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(authProvider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func asModelAliasEntries[T interface {
	GetName() string
	GetAlias() string
	GetForceMapping() bool
}](models []T) []modelAliasEntry {
	if len(models) == 0 {
		return nil
	}
	out := make([]modelAliasEntry, 0, len(models))
	for i := range models {
		out = append(out, models[i])
	}
	return out
}
