package multiagentv2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const (
	codexSpawnAgentDescriptionMarker      = "Spawns an agent"
	codexSpawnAgentModelsHeading          = "Available model overrides (optional; inherited parent model is preferred):"
	codexCollaborationNamespace           = "collaboration"
	codexOptimizedCollaborationNamespace  = "collaboration-optimize"
	codexOptimizedCollaborationNamePrefix = codexOptimizedCollaborationNamespace + "__"
	codexOptimizedCollaborationDotPrefix  = codexOptimizedCollaborationNamespace + "."
)

// CodexMultiAgentV2ToolsPreparedContextKey marks a request whose collaboration
// tool definitions were prepared at the Responses API boundary.
const CodexMultiAgentV2ToolsPreparedContextKey = "codex_multi_agent_v2_tools_prepared"

// codexCollaborationMessageTools are the collaboration tool names whose
// parameters.properties.message.encrypted field must be stripped so that
// message content remains readable by the proxy.
var codexCollaborationMessageTools = map[string]struct{}{
	"spawn_agent":   {},
	"send_message":  {},
	"followup_task": {},
}

type codexSpawnAgentModel struct {
	id                     string
	description            string
	reasoningEfforts       []string
	defaultReasoningEffort string
	serviceTiers           []string
	priority               int
	displayName            string
}

type codexClientModelsCatalog struct {
	Models []map[string]any `json:"models"`
}

// RewriteCodexSpawnAgentDescription optimizes spawn_agent definitions for
// official Codex clients when multi-agent v2 optimization is enabled.
func RewriteCodexSpawnAgentDescription(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) []byte {
	updated, _ := OptimizeCodexMultiAgentV2Request(ctx, headers, payload, cfg)
	return updated
}

// RewriteCodexMultiAgentV2Input converts official Codex multi-agent input into
// standard Responses API messages when multi-agent v2 optimization or model
// compatibility mode is enabled. When isCompat is true, it converts agent_message
// items to portable message/user input and proactively removes non-standard metadata
// fields (author, recipient, internal_chat_message_metadata_passthrough).
func RewriteCodexMultiAgentV2Input(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config, isCompat ...bool) []byte {
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !RewriteCodexMultiAgentV2InputDoc(doc, ctx, headers, cfg, isCompat...) {
		return payload
	}
	return doc.Bytes()
}

// RewriteCodexMultiAgentV2InputDoc converts official Codex multi-agent input in doc
// into standard Responses API messages in place.
func RewriteCodexMultiAgentV2InputDoc(doc *ojson.Doc, ctx context.Context, headers http.Header, cfg *config.Config, isCompat ...bool) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	compatMode := len(isCompat) > 0 && isCompat[0]
	optimizeEnabled := compatMode || (cfg != nil && cfg.Client.Codex.OptimizeMultiAgentV2 && isCodexMultiAgentClient(codexClientUserAgent(ctx, headers)))
	if !compatMode && !optimizeEnabled {
		return false
	}
	return rewriteCodexAgentMessageInputDoc(doc, optimizeEnabled, compatMode)
}

// RewriteCodexOrphanDelegationInputForConfig applies RewriteCodexOrphanDelegationInput
// based on cfg.Codex.OrphanDelegationCompatibility and the X-Openai-Subagent header.
func RewriteCodexOrphanDelegationInputForConfig(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) []byte {
	if cfg == nil || !cfg.Codex.OrphanDelegationCompatibility {
		return payload
	}
	return RewriteCodexOrphanDelegationInput(ctx, headers, payload, true)
}

// RewriteCodexOrphanDelegationInputForConfigDoc applies RewriteCodexOrphanDelegationInputDoc
// in place on doc based on cfg.Codex.OrphanDelegationCompatibility and the X-Openai-Subagent header.
func RewriteCodexOrphanDelegationInputForConfigDoc(doc *ojson.Doc, ctx context.Context, headers http.Header, cfg *config.Config) bool {
	if cfg == nil || !cfg.Codex.OrphanDelegationCompatibility {
		return false
	}
	return RewriteCodexOrphanDelegationInputDoc(doc, ctx, headers, true)
}

// TranslateRequestWithCodexMultiAgentV2 normalizes official Codex multi-agent
// input before translating it to a non-Codex target protocol.
func TranslateRequestWithCodexMultiAgentV2(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, payload []byte, stream bool) []byte {
	return TranslateRequestEnvelopeWithCodexMultiAgentV2(ctx, headers, cfg, from, to, sdktranslator.RequestEnvelope{
		Format: from,
		Model:  model,
		Stream: stream,
		Body:   payload,
	}).Body
}

// TranslateRequestEnvelopeWithCodexMultiAgentV2 normalizes official Codex
// multi-agent input while preserving request-scoped translation metadata.
func TranslateRequestEnvelopeWithCodexMultiAgentV2(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
	if from == sdktranslator.FormatOpenAIResponse && len(req.Body) > 0 {
		doc := ojson.NewDoc(req.Body)
		changed := RewriteCodexOrphanDelegationInputForConfigDoc(doc, ctx, headers, cfg)
		if to != sdktranslator.FormatCodex && to != sdktranslator.FormatOpenAIResponse {
			if RewriteCodexMultiAgentV2InputDoc(doc, ctx, headers, cfg) {
				changed = true
			}
		}
		if changed {
			req.Body = doc.Bytes()
		}
	}
	return sdktranslator.TranslateRequestEnvelope(ctx, from, to, req)
}

// PrepareCodexMultiAgentV2Tools prepares collaboration tool definitions at the
// Responses API boundary without changing the collaboration namespace.
func PrepareCodexMultiAgentV2Tools(ctx context.Context, headers http.Header, payload []byte, enabled, homeEnabled bool) ([]byte, bool) {
	if len(payload) == 0 {
		return payload, codexMultiAgentV2ClientEnabled(ctx, headers, enabled)
	}
	doc := ojson.NewDoc(payload)
	changed, prepared := prepareCodexMultiAgentV2ToolsDoc(doc, ctx, headers, enabled, homeEnabled)
	if !changed {
		return payload, prepared
	}
	return doc.Bytes(), prepared
}

// PrepareCodexMultiAgentV2ToolsDoc prepares collaboration tool definitions in doc at the
// Responses API boundary without changing the collaboration namespace.
func PrepareCodexMultiAgentV2ToolsDoc(doc *ojson.Doc, ctx context.Context, headers http.Header, enabled, homeEnabled bool) (bool, bool) {
	return prepareCodexMultiAgentV2ToolsDoc(doc, ctx, headers, enabled, homeEnabled)
}

func prepareCodexMultiAgentV2ToolsDoc(doc *ojson.Doc, ctx context.Context, headers http.Header, enabled, homeEnabled bool) (bool, bool) {
	if !codexMultiAgentV2ClientEnabled(ctx, headers, enabled) {
		return false, false
	}
	if doc == nil || doc.Len() == 0 {
		return false, true
	}

	toolPaths := codexSpawnAgentToolPathsDoc(doc)
	messageToolPaths := codexCollaborationMessageToolPathsDoc(doc)
	if len(toolPaths) == 0 && len(messageToolPaths) == 0 {
		return false, true
	}
	if hasCodexOptimizedCollaborationConflictDoc(doc) {
		return removeCodexCollaborationMessageEncryptionDoc(doc, messageToolPaths), true
	}

	var models []codexSpawnAgentModel
	var formattedMarkdown string
	if len(toolPaths) > 0 {
		models, formattedMarkdown = codexSpawnAgentModelsAndMarkdownForRequest(ctx, headers, homeEnabled)
	}

	return rewriteCodexCollaborationToolsDoc(doc, messageToolPaths, toolPaths, models, formattedMarkdown), true
}

// OptimizeCodexMultiAgentV2Request rewrites an eligible spawn_agent request and
// reports whether the collaboration namespace was renamed for upstream use.
func OptimizeCodexMultiAgentV2Request(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) ([]byte, bool) {
	if len(payload) == 0 {
		return payload, false
	}
	doc := ojson.NewDoc(payload)
	changed, optimized := optimizeCodexMultiAgentV2RequestDoc(doc, ctx, headers, cfg)
	if !changed {
		return payload, false
	}
	return doc.Bytes(), optimized
}

// OptimizeCodexMultiAgentV2RequestDoc rewrites an eligible spawn_agent request in doc
// and reports whether the collaboration namespace was renamed for upstream use.
func OptimizeCodexMultiAgentV2RequestDoc(doc *ojson.Doc, ctx context.Context, headers http.Header, cfg *config.Config) bool {
	_, optimized := optimizeCodexMultiAgentV2RequestDoc(doc, ctx, headers, cfg)
	return optimized
}

func optimizeCodexMultiAgentV2RequestDoc(doc *ojson.Doc, ctx context.Context, headers http.Header, cfg *config.Config) (bool, bool) {
	if doc == nil || doc.Len() == 0 || !codexMultiAgentV2Enabled(ctx, headers, cfg) {
		return false, false
	}
	changed := rewriteCodexAgentMessageContentDoc(doc)
	if codexMultiAgentV2ToolsPrepared(ctx) {
		if removeCodexCollaborationMessageEncryptionDoc(doc, codexCollaborationMessageToolPathsDoc(doc)) {
			changed = true
		}
	} else {
		if toolsChanged, _ := prepareCodexMultiAgentV2ToolsDoc(doc, ctx, headers, cfg.Client.Codex.OptimizeMultiAgentV2, cfg.Home.Enabled); toolsChanged {
			changed = true
		}
	}
	toolPaths := codexSpawnAgentToolPathsDoc(doc)
	if len(toolPaths) == 0 || hasCodexOptimizedCollaborationConflictDoc(doc) {
		return changed, false
	}
	optimized := optimizeCodexCollaborationNamespaceDoc(doc, toolPaths)
	if optimized {
		changed = true
	}
	return changed, optimized
}

func codexMultiAgentV2Enabled(ctx context.Context, headers http.Header, cfg *config.Config) bool {
	return cfg != nil && codexMultiAgentV2ClientEnabled(ctx, headers, cfg.Client.Codex.OptimizeMultiAgentV2)
}

func codexMultiAgentV2ClientEnabled(ctx context.Context, headers http.Header, enabled bool) bool {
	return enabled && isCodexMultiAgentClient(codexClientUserAgent(ctx, headers))
}

func codexMultiAgentV2ToolsPrepared(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return false
	}
	prepared, ok := ginCtx.Get(CodexMultiAgentV2ToolsPreparedContextKey)
	isPrepared, _ := prepared.(bool)
	return ok && isPrepared
}

func codexClientUserAgent(ctx context.Context, headers http.Header) string {
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			return headerValueCaseInsensitive(ginCtx.Request.Header, "User-Agent")
		}
	}
	return headerValueCaseInsensitive(headers, "User-Agent")
}

func headerValueCaseInsensitive(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := strings.TrimSpace(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

// IsCodexClientUserAgent reports whether a request uses an official Codex client identity.
func IsCodexClientUserAgent(userAgent string) bool {
	userAgent = strings.TrimSpace(userAgent)
	return strings.HasPrefix(userAgent, "Codex Desktop/") ||
		strings.HasPrefix(userAgent, "codex-tui/") ||
		userAgent == "codex_cli_rs" ||
		strings.HasPrefix(userAgent, "codex_cli_rs/") ||
		strings.HasPrefix(userAgent, "codex_exec/")
}

func isCodexMultiAgentClient(userAgent string) bool {
	return IsCodexClientUserAgent(userAgent)
}

var (
	codexCatalogTemplatesMu       sync.RWMutex
	codexCatalogTemplatesLoaded   bool
	codexCatalogTemplatesRevision uint64
	codexCatalogTemplates         map[string]map[string]any
	codexCatalogDefaultTemplate   map[string]any

	codexSpawnAgentCacheMu         sync.RWMutex
	codexSpawnAgentCacheRevision   uint64
	codexSpawnAgentCacheGeneration uint64
	codexSpawnAgentCachedModels    []codexSpawnAgentModel
	codexSpawnAgentCachedMarkdown  string
)

func loadCodexCatalogTemplates() (map[string]map[string]any, map[string]any, uint64, error) {
	currentRevision := registry.GetCodexClientModelsRevision()

	codexCatalogTemplatesMu.RLock()
	if codexCatalogTemplatesLoaded && codexCatalogTemplatesRevision == currentRevision {
		templates := codexCatalogTemplates
		defaultTemplate := codexCatalogDefaultTemplate
		codexCatalogTemplatesMu.RUnlock()
		return templates, defaultTemplate, currentRevision, nil
	}
	codexCatalogTemplatesMu.RUnlock()

	codexCatalogTemplatesMu.Lock()
	defer codexCatalogTemplatesMu.Unlock()
	if codexCatalogTemplatesLoaded && codexCatalogTemplatesRevision == currentRevision {
		return codexCatalogTemplates, codexCatalogDefaultTemplate, currentRevision, nil
	}

	raw, revision := registry.GetCodexClientModelsSnapshot()

	var catalog codexClientModelsCatalog
	errUnmarshal := json.Unmarshal(raw, &catalog)
	if errUnmarshal != nil || len(catalog.Models) == 0 {
		codexCatalogTemplatesLoaded = true
		codexCatalogTemplatesRevision = revision
		codexCatalogTemplates = nil
		codexCatalogDefaultTemplate = nil
		return nil, nil, revision, errUnmarshal
	}

	templates := make(map[string]map[string]any, len(catalog.Models))
	var defaultTemplate map[string]any
	for _, model := range catalog.Models {
		modelID := mapString(model, "slug")
		if modelID == "" {
			continue
		}
		templates[modelID] = model
		if defaultTemplate == nil || modelID == "gpt-5.5" {
			defaultTemplate = model
		}
	}

	codexCatalogTemplatesLoaded = true
	codexCatalogTemplatesRevision = revision
	codexCatalogTemplates = templates
	codexCatalogDefaultTemplate = defaultTemplate
	return templates, defaultTemplate, revision, nil
}

func codexSpawnAgentModelsAndMarkdownForRequest(ctx context.Context, headers http.Header, homeEnabled bool) ([]codexSpawnAgentModel, string) {
	if homeEnabled {
		availableModels := codexHomeAvailableModels(ctx, headers)
		templates, defaultTemplate, _, errLoad := loadCodexCatalogTemplates()
		if errLoad != nil || defaultTemplate == nil {
			return nil, ""
		}
		models := codexSpawnAgentModelsFromTemplates(availableModels, templates, defaultTemplate, func(modelID string) *registry.ModelInfo {
			return registry.LookupModelInfo(modelID)
		})
		formatted := formatCodexSpawnAgentModels(models)
		return models, formatted
	}

	currentRevision := registry.GetCodexClientModelsRevision()
	currentGeneration := registry.GetGlobalRegistry().GetGeneration()

	codexSpawnAgentCacheMu.RLock()
	if codexSpawnAgentCachedModels != nil && codexSpawnAgentCacheRevision == currentRevision && codexSpawnAgentCacheGeneration == currentGeneration {
		models := codexSpawnAgentCachedModels
		markdown := codexSpawnAgentCachedMarkdown
		codexSpawnAgentCacheMu.RUnlock()
		return models, markdown
	}
	codexSpawnAgentCacheMu.RUnlock()

	templates, defaultTemplate, _, errLoad := loadCodexCatalogTemplates()
	if errLoad != nil || defaultTemplate == nil {
		return nil, ""
	}

	availableModels := registry.GetGlobalRegistry().GetAvailableModels("openai")
	lookup := func(modelID string) *registry.ModelInfo {
		return registry.LookupModelInfo(modelID)
	}
	models := codexSpawnAgentModelsFromTemplates(availableModels, templates, defaultTemplate, lookup)
	formatted := formatCodexSpawnAgentModels(models)

	codexSpawnAgentCacheMu.Lock()
	if currentRevision == registry.GetCodexClientModelsRevision() && currentGeneration == registry.GetGlobalRegistry().GetGeneration() {
		codexSpawnAgentCacheRevision = currentRevision
		codexSpawnAgentCacheGeneration = currentGeneration
		codexSpawnAgentCachedModels = models
		codexSpawnAgentCachedMarkdown = formatted
	}
	codexSpawnAgentCacheMu.Unlock()

	return models, formatted
}

func codexSpawnAgentModelsForRequest(ctx context.Context, headers http.Header, homeEnabled bool) []codexSpawnAgentModel {
	models, _ := codexSpawnAgentModelsAndMarkdownForRequest(ctx, headers, homeEnabled)
	return models
}

func formatCodexSpawnAgentModelsForRequest(ctx context.Context, headers http.Header, homeEnabled bool) string {
	_, formatted := codexSpawnAgentModelsAndMarkdownForRequest(ctx, headers, homeEnabled)
	return formatted
}

func codexHomeAvailableModels(ctx context.Context, headers http.Header) []map[string]any {
	client := home.Current()
	if client == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestHeaders := headers
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		requestHeaders = ginCtx.Request.Header
	}
	query := make(url.Values)
	query.Set("client_version", "")
	raw, errGet := client.GetModels(ctx, requestHeaders, query)
	if errGet != nil {
		return nil
	}
	return decodeCodexHomeAvailableModels(raw)
}

func decodeCodexHomeAvailableModels(raw []byte) []map[string]any {
	var sections map[string][]map[string]any
	if err := json.Unmarshal(raw, &sections); err != nil || len(sections) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	models := make([]map[string]any, 0, 256)
	for _, sectionModels := range sections {
		for _, model := range sectionModels {
			modelID := mapString(model, "id")
			if modelID == "" {
				modelID = strings.TrimPrefix(mapString(model, "name"), "models/")
			}
			if modelID == "" {
				continue
			}
			if _, exists := seen[modelID]; exists {
				continue
			}
			seen[modelID] = struct{}{}

			displayName := mapString(model, "display_name")
			if displayName == "" {
				displayName = mapString(model, "displayName")
			}
			entry := map[string]any{"id": modelID}
			if displayName != "" {
				entry["display_name"] = displayName
				entry["description"] = displayName
			}
			models = append(models, entry)
		}
	}
	sort.Slice(models, func(i, j int) bool {
		return mapString(models[i], "id") < mapString(models[j], "id")
	})
	return models
}

func codexSpawnAgentModelsFromSources(availableModels []map[string]any, catalogJSON []byte, lookupModel func(string) *registry.ModelInfo) []codexSpawnAgentModel {
	var catalog codexClientModelsCatalog
	if err := json.Unmarshal(catalogJSON, &catalog); err != nil || len(catalog.Models) == 0 {
		return nil
	}

	templates := make(map[string]map[string]any, len(catalog.Models))
	var defaultTemplate map[string]any
	for _, model := range catalog.Models {
		modelID := mapString(model, "slug")
		if modelID == "" {
			continue
		}
		templates[modelID] = model
		if defaultTemplate == nil || modelID == "gpt-5.5" {
			defaultTemplate = model
		}
	}
	if defaultTemplate == nil {
		return nil
	}

	return codexSpawnAgentModelsFromTemplates(availableModels, templates, defaultTemplate, lookupModel)
}

func codexSpawnAgentModelsFromTemplates(availableModels []map[string]any, templates map[string]map[string]any, defaultTemplate map[string]any, lookupModel func(string) *registry.ModelInfo) []codexSpawnAgentModel {
	if defaultTemplate == nil {
		return nil
	}

	seen := make(map[string]struct{}, len(availableModels))
	templateModels := make([]codexSpawnAgentModel, 0, len(availableModels))
	synthesizedModels := make([]codexSpawnAgentModel, 0, len(availableModels))
	for _, availableModel := range availableModels {
		modelID := mapString(availableModel, "id")
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}

		if template, ok := templates[modelID]; ok {
			templateModels = append(templateModels, codexSpawnAgentModelFromMetadata(modelID, template))
			continue
		}

		profile := codexSpawnAgentModelFromMetadata(modelID, defaultTemplate)
		profile.id = modelID
		profile.description = mapString(availableModel, "description")
		profile.displayName = mapString(availableModel, "display_name")
		if profile.displayName == "" {
			profile.displayName = modelID
		}
		if lookupModel != nil {
			if info := lookupModel(modelID); info != nil {
				if strings.TrimSpace(info.Description) != "" {
					profile.description = strings.TrimSpace(info.Description)
				}
				applyCodexSpawnAgentThinking(&profile, info.Thinking)
			}
		}
		if profile.description == "" {
			profile.description = modelID
		}
		profile.serviceTiers = nil
		synthesizedModels = append(synthesizedModels, profile)
	}

	sort.SliceStable(templateModels, func(i, j int) bool {
		if templateModels[i].priority == templateModels[j].priority {
			return templateModels[i].id < templateModels[j].id
		}
		return templateModels[i].priority < templateModels[j].priority
	})
	sort.SliceStable(synthesizedModels, func(i, j int) bool {
		left := strings.ToLower(synthesizedModels[i].displayName)
		right := strings.ToLower(synthesizedModels[j].displayName)
		if left == right {
			return synthesizedModels[i].id < synthesizedModels[j].id
		}
		return left < right
	})
	return append(templateModels, synthesizedModels...)
}

func codexSpawnAgentModelFromMetadata(modelID string, metadata map[string]any) codexSpawnAgentModel {
	profile := codexSpawnAgentModel{
		id:          modelID,
		description: mapString(metadata, "description"),
		displayName: mapString(metadata, "display_name"),
		priority:    mapInt(metadata, "priority"),
	}
	profile.reasoningEfforts, profile.defaultReasoningEffort = codexReasoningMetadata(metadata)
	profile.serviceTiers = codexServiceTierIDs(metadata)
	return profile
}

func applyCodexSpawnAgentThinking(profile *codexSpawnAgentModel, thinking *registry.ThinkingSupport) {
	if profile == nil || thinking == nil || len(thinking.Levels) == 0 {
		return
	}

	efforts := make([]string, 0, len(thinking.Levels))
	defaultEffort := ""
	firstEffort := ""
	for _, rawEffort := range thinking.Levels {
		effort := normalizeCodexReasoningEffort(rawEffort)
		if effort == "" {
			continue
		}
		if firstEffort == "" {
			firstEffort = effort
		}
		if (defaultEffort == "" && effort != "none") || effort == "medium" {
			defaultEffort = effort
		}
		efforts = append(efforts, effort)
	}
	if len(efforts) == 0 {
		return
	}
	if defaultEffort == "" {
		defaultEffort = firstEffort
	}
	profile.reasoningEfforts = efforts
	profile.defaultReasoningEffort = defaultEffort
}

func codexReasoningMetadata(metadata map[string]any) ([]string, string) {
	rawLevels, _ := metadata["supported_reasoning_levels"].([]any)
	efforts := make([]string, 0, len(rawLevels))
	allowed := make(map[string]struct{}, len(rawLevels))
	for _, rawLevel := range rawLevels {
		level, _ := rawLevel.(map[string]any)
		effort := normalizeCodexReasoningEffort(mapString(level, "effort"))
		if effort == "" {
			continue
		}
		efforts = append(efforts, effort)
		allowed[effort] = struct{}{}
	}
	if len(efforts) == 0 {
		return nil, ""
	}

	defaultEffort := normalizeCodexReasoningEffort(mapString(metadata, "default_reasoning_level"))
	if _, ok := allowed[defaultEffort]; !ok {
		defaultEffort = efforts[0]
	}
	return efforts, defaultEffort
}

func normalizeCodexReasoningEffort(effort string) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	switch effort {
	case "none", "low", "medium", "high", "xhigh", "max", "ultra":
		return effort
	default:
		return ""
	}
}

func codexServiceTierIDs(metadata map[string]any) []string {
	rawTiers, _ := metadata["service_tiers"].([]any)
	tiers := make([]string, 0, len(rawTiers))
	seen := make(map[string]struct{}, len(rawTiers))
	for _, rawTier := range rawTiers {
		tier, _ := rawTier.(map[string]any)
		tierID := mapString(tier, "id")
		if tierID == "" {
			continue
		}
		if _, exists := seen[tierID]; exists {
			continue
		}
		seen[tierID] = struct{}{}
		tiers = append(tiers, tierID)
	}
	return tiers
}

func mapString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func mapInt(values map[string]any, key string) int {
	if values == nil {
		return 0
	}
	switch value := values[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func rewriteCodexSpawnAgentDescription(payload []byte, models []codexSpawnAgentModel) []byte {
	return rewriteCodexSpawnAgentTools(payload, codexSpawnAgentToolPaths(payload), models)
}

func rewriteCodexSpawnAgentTools(payload []byte, toolPaths []string, models []codexSpawnAgentModel) []byte {
	return rewriteCodexCollaborationTools(payload, toolPaths, toolPaths, models, "")
}

func rewriteCodexCollaborationTools(payload []byte, messageToolPaths, spawnAgentToolPaths []string, models []codexSpawnAgentModel, modelList string) []byte {
	if len(payload) == 0 || (len(messageToolPaths) == 0 && len(spawnAgentToolPaths) == 0) {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !rewriteCodexCollaborationToolsDoc(doc, messageToolPaths, spawnAgentToolPaths, models, modelList) {
		return payload
	}
	return doc.Bytes()
}

func rewriteCodexCollaborationToolsDoc(doc *ojson.Doc, messageToolPaths, spawnAgentToolPaths []string, models []codexSpawnAgentModel, modelList string) bool {
	if doc == nil || (len(messageToolPaths) == 0 && len(spawnAgentToolPaths) == 0) {
		return false
	}
	if modelList == "" && len(models) > 0 {
		modelList = formatCodexSpawnAgentModels(models)
	}
	changed := false
	for _, toolPath := range spawnAgentToolPaths {
		descriptionPath := toolPath + ".description"
		description := doc.Get(descriptionPath)
		if description.Type == ojson.String && modelList != "" {
			rewritten := replaceCodexSpawnAgentModels(description.String(), modelList)
			if rewritten != description.String() {
				if errSet := doc.Set(descriptionPath, rewritten); errSet != nil {
					return false
				}
				changed = true
			}
		}
	}

	for _, toolPath := range messageToolPaths {
		encryptedPath := toolPath + ".parameters.properties.message.encrypted"
		if doc.Get(encryptedPath).Exists() {
			if errDelete := doc.Delete(encryptedPath); errDelete != nil {
				return false
			}
			changed = true
		}
	}
	return changed
}

// HasCodexMultiAgentV2NamespaceConflict reports whether the request defines
// the reserved optimized namespace, which must remain untouched.
func HasCodexMultiAgentV2NamespaceConflict(payload []byte) bool {
	return hasCodexOptimizedCollaborationConflict(payload)
}

// HasCodexMultiAgentV2NamespaceConflictDoc reports whether doc defines
// the reserved optimized namespace, which must remain untouched.
func HasCodexMultiAgentV2NamespaceConflictDoc(doc *ojson.Doc) bool {
	return hasCodexOptimizedCollaborationConflictDoc(doc)
}

func hasCodexOptimizedCollaborationConflict(payload []byte) bool {
	if codexToolsHaveOptimizedCollaborationConflict(util.GetGJSONBytesNoCopy(payload, "tools")) {
		return true
	}
	input := util.GetGJSONBytesNoCopy(payload, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) == "additional_tools" && codexToolsHaveOptimizedCollaborationConflict(item.Get("tools")) {
			return true
		}
	}
	return false
}

func hasCodexOptimizedCollaborationConflictDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	if codexToolsHaveOptimizedCollaborationConflict(doc.Get("tools")) {
		return true
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) == "additional_tools" && codexToolsHaveOptimizedCollaborationConflict(item.Get("tools")) {
			return true
		}
	}
	return false
}

func codexToolsHaveOptimizedCollaborationConflict(tools ojson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == codexOptimizedCollaborationNamespace || strings.HasPrefix(name, codexOptimizedCollaborationNamePrefix) || strings.HasPrefix(name, codexOptimizedCollaborationDotPrefix) {
			return true
		}
		if strings.TrimSpace(tool.Get("type").String()) == "namespace" && codexToolsHaveOptimizedCollaborationConflict(tool.Get("tools")) {
			return true
		}
	}
	return false
}

func optimizeCodexCollaborationNamespace(payload []byte, toolPaths []string) ([]byte, bool) {
	if len(payload) == 0 || len(toolPaths) == 0 {
		return payload, false
	}
	doc := ojson.NewDoc(payload)
	if !optimizeCodexCollaborationNamespaceDoc(doc, toolPaths) {
		return payload, false
	}
	return doc.Bytes(), true
}

func optimizeCodexCollaborationNamespaceDoc(doc *ojson.Doc, toolPaths []string) bool {
	if doc == nil || len(toolPaths) == 0 {
		return false
	}
	optimized := false
	for _, toolPath := range toolPaths {
		separatorIndex := strings.LastIndex(toolPath, ".tools.")
		if separatorIndex < 0 {
			continue
		}
		namespacePath := toolPath[:separatorIndex]
		namespace := doc.Get(namespacePath)
		if strings.TrimSpace(namespace.Get("type").String()) != "namespace" || strings.TrimSpace(namespace.Get("name").String()) != codexCollaborationNamespace {
			continue
		}
		if errSet := doc.Set(namespacePath+".name", codexOptimizedCollaborationNamespace); errSet != nil {
			return false
		}
		optimized = true
	}
	return optimized
}

// RestoreCodexMultiAgentV2Response restores optimized collaboration namespace
// values before an upstream response is translated and returned to the client.
func RestoreCodexMultiAgentV2Response(payload []byte, optimized bool) []byte {
	if !optimized || len(payload) == 0 || !ojson.ValidBytes(payload) {
		return payload
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return payload
	}
	if !restoreCodexCollaborationValue(value) {
		return payload
	}
	restored, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return payload
	}
	return restored
}

func restoreCodexCollaborationValue(value any) bool {
	changed := false
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if restoreCodexCollaborationValue(item) {
				changed = true
			}
		}
	case map[string]any:
		itemType := strings.TrimSpace(mapString(typed, "type"))
		isToolCall := itemType == "function_call" || itemType == "custom_tool_call"
		if isToolCall {
			if namespace, ok := typed["namespace"].(string); ok && namespace == codexOptimizedCollaborationNamespace {
				typed["namespace"] = codexCollaborationNamespace
				changed = true
			}
		}
		if name, ok := typed["name"].(string); ok {
			switch {
			case name == codexOptimizedCollaborationNamespace && itemType == "namespace":
				typed["name"] = codexCollaborationNamespace
				changed = true
			case isToolCall && strings.HasPrefix(name, codexOptimizedCollaborationDotPrefix):
				toolName := strings.TrimPrefix(name, codexOptimizedCollaborationDotPrefix)
				if toolName != "" {
					typed["namespace"] = codexCollaborationNamespace
					typed["name"] = toolName
					changed = true
				}
			case isToolCall && strings.HasPrefix(name, codexOptimizedCollaborationNamePrefix):
				typed["name"] = codexCollaborationNamespace + "__" + strings.TrimPrefix(name, codexOptimizedCollaborationNamePrefix)
				changed = true
			}
		}
		for key, child := range typed {
			if key == "arguments" || key == "input" || key == "output" && (itemType == "function_call_output" || itemType == "custom_tool_call_output") {
				continue
			}
			if restoreCodexCollaborationValue(child) {
				changed = true
			}
		}
	}
	return changed
}

func rewriteCodexAgentMessageInput(payload []byte, optimizeEnabled bool, compatMode bool) []byte {
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !rewriteCodexAgentMessageInputDoc(doc, optimizeEnabled, compatMode) {
		return payload
	}
	return doc.Bytes()
}

func rewriteCodexAgentMessageInputDoc(doc *ojson.Doc, optimizeEnabled bool, compatMode bool) bool {
	if doc == nil {
		return false
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}

	changed := false
	if optimizeEnabled {
		if rewriteCodexAgentMessageContentInItemsDoc(doc, input) {
			changed = true
		}
	}
	for itemIndex, item := range input.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		itemPath := fmt.Sprintf("input.%d", itemIndex)
		if itemType == "agent_message" && optimizeEnabled {
			if errSet := doc.Set(itemPath+".role", "user"); errSet != nil {
				return false
			}
			if errSet := doc.Set(itemPath+".type", "message"); errSet != nil {
				return false
			}
			changed = true
		}
		if compatMode {
			if item.Get("author").Exists() {
				if errDelete := doc.Delete(itemPath + ".author"); errDelete != nil {
					return false
				}
				changed = true
			}
			if item.Get("recipient").Exists() {
				if errDelete := doc.Delete(itemPath + ".recipient"); errDelete != nil {
					return false
				}
				changed = true
			}
			if item.Get("internal_chat_message_metadata_passthrough").Exists() {
				if errDelete := doc.Delete(itemPath + ".internal_chat_message_metadata_passthrough"); errDelete != nil {
					return false
				}
				changed = true
			}
		}
	}
	return changed
}

func rewriteCodexAgentMessageContent(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !rewriteCodexAgentMessageContentDoc(doc) {
		return payload
	}
	return doc.Bytes()
}

func rewriteCodexAgentMessageContentDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}
	return rewriteCodexAgentMessageContentInItemsDoc(doc, input)
}

func rewriteCodexAgentMessageContentInItemsDoc(doc *ojson.Doc, input ojson.Result) bool {
	changed := false
	for itemIndex, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "agent_message" {
			continue
		}
		content := item.Get("content")
		if !content.IsArray() {
			continue
		}
		for partIndex, part := range content.Array() {
			if strings.TrimSpace(part.Get("type").String()) != "encrypted_content" {
				continue
			}
			encryptedContent := part.Get("encrypted_content")
			if encryptedContent.Type != ojson.String {
				continue
			}
			partPath := fmt.Sprintf("input.%d.content.%d", itemIndex, partIndex)
			if errSet := doc.Set(partPath+".type", "input_text"); errSet != nil {
				return false
			}
			if errSet := doc.Set(partPath+".text", encryptedContent.String()); errSet != nil {
				return false
			}
			if errDelete := doc.Delete(partPath + ".encrypted_content"); errDelete != nil {
				return false
			}
			changed = true
		}
	}
	return changed
}

func codexSpawnAgentToolPaths(payload []byte) []string {
	return codexToolPathsByNames(payload, map[string]struct{}{"spawn_agent": {}})
}

func codexSpawnAgentToolPathsDoc(doc *ojson.Doc) []string {
	return codexToolPathsByNamesDoc(doc, map[string]struct{}{"spawn_agent": {}})
}

// codexCollaborationMessageToolPaths discovers function tools named
// spawn_agent, send_message, or followup_task inside top-level tools arrays and
// input[].additional_tools arrays, including nested namespace tools.
func codexCollaborationMessageToolPaths(payload []byte) []string {
	return codexToolPathsByNames(payload, codexCollaborationMessageTools)
}

func codexCollaborationMessageToolPathsDoc(doc *ojson.Doc) []string {
	return codexToolPathsByNamesDoc(doc, codexCollaborationMessageTools)
}

func codexToolPathsByNames(payload []byte, names map[string]struct{}) []string {
	paths := make([]string, 0, len(names))
	collectCodexToolPathsByNames(util.GetGJSONBytesNoCopy(payload, "tools"), "tools", &paths, names)

	input := util.GetGJSONBytesNoCopy(payload, "input")
	if input.IsArray() {
		for index, item := range input.Array() {
			if strings.TrimSpace(item.Get("type").String()) != "additional_tools" {
				continue
			}
			collectCodexToolPathsByNames(item.Get("tools"), fmt.Sprintf("input.%d.tools", index), &paths, names)
		}
	}
	return paths
}

func codexToolPathsByNamesDoc(doc *ojson.Doc, names map[string]struct{}) []string {
	if doc == nil {
		return nil
	}
	paths := make([]string, 0, len(names))
	collectCodexToolPathsByNames(doc.Get("tools"), "tools", &paths, names)

	input := doc.Get("input")
	if input.IsArray() {
		for index, item := range input.Array() {
			if strings.TrimSpace(item.Get("type").String()) != "additional_tools" {
				continue
			}
			collectCodexToolPathsByNames(item.Get("tools"), fmt.Sprintf("input.%d.tools", index), &paths, names)
		}
	}
	return paths
}

func collectCodexToolPathsByNames(tools ojson.Result, path string, paths *[]string, names map[string]struct{}) {
	if !tools.IsArray() {
		return
	}
	for index, tool := range tools.Array() {
		toolPath := fmt.Sprintf("%s.%d", path, index)
		toolType := strings.TrimSpace(tool.Get("type").String())
		if toolType == "function" {
			if _, ok := names[strings.TrimSpace(tool.Get("name").String())]; ok {
				*paths = append(*paths, toolPath)
			}
		}
		if toolType == "namespace" {
			collectCodexToolPathsByNames(tool.Get("tools"), toolPath+".tools", paths, names)
		}
	}
}

// removeCodexCollaborationMessageEncryption deletes the
// parameters.properties.message.encrypted field from each discovered
// collaboration message tool so the proxy can read the plaintext message.
func removeCodexCollaborationMessageEncryption(payload []byte, toolPaths []string) []byte {
	if len(payload) == 0 || len(toolPaths) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !removeCodexCollaborationMessageEncryptionDoc(doc, toolPaths) {
		return payload
	}
	return doc.Bytes()
}

func removeCodexCollaborationMessageEncryptionDoc(doc *ojson.Doc, toolPaths []string) bool {
	if doc == nil || len(toolPaths) == 0 {
		return false
	}
	changed := false
	for _, toolPath := range toolPaths {
		encryptedPath := toolPath + ".parameters.properties.message.encrypted"
		if !doc.Get(encryptedPath).Exists() {
			continue
		}
		if errDelete := doc.Delete(encryptedPath); errDelete != nil {
			return false
		}
		changed = true
	}
	return changed
}

func formatCodexSpawnAgentModels(models []codexSpawnAgentModel) string {
	var modelList strings.Builder
	for _, model := range models {
		modelID := strings.Join(strings.Fields(model.id), " ")
		if modelID == "" {
			continue
		}
		modelList.WriteString("- ")
		modelList.WriteString(markdownCode(modelID))
		modelList.WriteString(": ")
		hasDetails := false
		if description := strings.Join(strings.Fields(model.description), " "); description != "" {
			writeSentence(&modelList, description)
			hasDetails = true
		}
		if len(model.reasoningEfforts) > 0 {
			if hasDetails {
				modelList.WriteByte(' ')
			}
			modelList.WriteString("Reasoning efforts: ")
			for index, effort := range model.reasoningEfforts {
				if index > 0 {
					modelList.WriteString(", ")
				}
				modelList.WriteString(effort)
				if effort == model.defaultReasoningEffort {
					modelList.WriteString(" (default)")
				}
			}
			modelList.WriteByte('.')
			hasDetails = true
		}
		if len(model.serviceTiers) > 0 {
			if hasDetails {
				modelList.WriteByte(' ')
			}
			modelList.WriteString("Service tiers: ")
			modelList.WriteString(strings.Join(model.serviceTiers, ", "))
			modelList.WriteByte('.')
		}
		modelList.WriteByte('\n')
	}
	return strings.TrimSuffix(modelList.String(), "\n")
}

func markdownCode(value string) string {
	if strings.Contains(value, "`") {
		return "`` " + value + " ``"
	}
	return "`" + value + "`"
}

func writeSentence(builder *strings.Builder, value string) {
	builder.WriteString(value)
	if !strings.ContainsAny(value[len(value)-1:], ".!?") {
		builder.WriteByte('.')
	}
}

func replaceCodexSpawnAgentModels(description, modelList string) string {
	if modelList == "" {
		return description
	}

	cleaned, headingIndent := removeCodexSpawnAgentModelSections(description)
	section := headingIndent + codexSpawnAgentModelsHeading + "\n" + modelList + "\n"
	markerIndex := strings.Index(cleaned, codexSpawnAgentDescriptionMarker)
	if markerIndex >= 0 {
		markerLineStart := strings.LastIndex(cleaned[:markerIndex], "\n") + 1
		return cleaned[:markerLineStart] + section + cleaned[markerLineStart:]
	}
	separator := ""
	if cleaned != "" && !strings.HasSuffix(cleaned, "\n") {
		separator = "\n\n"
	}
	return cleaned + separator + strings.TrimSuffix(section, "\n")
}

func removeCodexSpawnAgentModelSections(description string) (string, string) {
	if !strings.Contains(description, codexSpawnAgentModelsHeading) {
		return description, ""
	}
	lines := strings.SplitAfter(description, "\n")
	var cleaned strings.Builder
	headingIndent := ""
	for index := 0; index < len(lines); {
		line := lines[index]
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine != codexSpawnAgentModelsHeading {
			cleaned.WriteString(line)
			index++
			continue
		}

		if headingIndent == "" {
			headingIndex := strings.Index(line, codexSpawnAgentModelsHeading)
			if headingIndex > 0 {
				headingIndent = line[:headingIndex]
			}
		}
		index++
		for index < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[index]), "- ") {
			index++
		}
	}
	return cleaned.String(), headingIndent
}
