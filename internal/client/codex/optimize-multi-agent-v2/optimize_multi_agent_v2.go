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

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexSpawnAgentDescriptionMarker      = "Spawns an agent"
	codexSpawnAgentModelsHeading          = "Available model overrides (optional; inherited parent model is preferred):"
	codexCollaborationNamespace           = "collaboration"
	codexOptimizedCollaborationNamespace  = "collaboration-optimize"
	codexOptimizedCollaborationNamePrefix = codexOptimizedCollaborationNamespace + "__"
)

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
// standard Responses API messages when multi-agent v2 optimization is enabled.
func RewriteCodexMultiAgentV2Input(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) []byte {
	if !codexMultiAgentV2Enabled(ctx, headers, cfg) {
		return payload
	}
	return rewriteCodexAgentMessageInput(payload)
}

// TranslateRequestWithCodexMultiAgentV2 normalizes official Codex multi-agent
// input before translating it to a non-Codex target protocol.
func TranslateRequestWithCodexMultiAgentV2(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, payload []byte, stream bool) []byte {
	if from == sdktranslator.FormatOpenAIResponse && to != sdktranslator.FormatCodex && to != sdktranslator.FormatOpenAIResponse {
		payload = RewriteCodexMultiAgentV2Input(ctx, headers, payload, cfg)
	}
	return sdktranslator.TranslateRequest(from, to, model, payload, stream)
}

// OptimizeCodexMultiAgentV2Request rewrites an eligible spawn_agent request and
// reports whether the collaboration namespace was renamed for upstream use.
func OptimizeCodexMultiAgentV2Request(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) ([]byte, bool) {
	if !codexMultiAgentV2Enabled(ctx, headers, cfg) {
		return payload, false
	}
	updated := rewriteCodexAgentMessageContent(payload)
	toolPaths := codexSpawnAgentToolPaths(updated)
	if len(toolPaths) == 0 || hasCodexOptimizedCollaborationConflict(updated) {
		return updated, false
	}
	models := codexSpawnAgentModelsForRequest(ctx, headers, cfg.Home.Enabled)
	updated = rewriteCodexSpawnAgentTools(updated, toolPaths, models)
	return optimizeCodexCollaborationNamespace(updated, toolPaths)
}

func codexMultiAgentV2Enabled(ctx context.Context, headers http.Header, cfg *config.Config) bool {
	return cfg != nil && cfg.Codex.OptimizeMultiAgentV2 && isCodexMultiAgentClient(codexClientUserAgent(ctx, headers))
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

func isCodexMultiAgentClient(userAgent string) bool {
	userAgent = strings.TrimSpace(userAgent)
	return strings.HasPrefix(userAgent, "Codex Desktop/") || strings.HasPrefix(userAgent, "codex-tui/")
}

func codexSpawnAgentModelsForRequest(ctx context.Context, headers http.Header, homeEnabled bool) []codexSpawnAgentModel {
	availableModels := registry.GetGlobalRegistry().GetAvailableModels("openai")
	if homeEnabled {
		availableModels = codexHomeAvailableModels(ctx, headers)
	}
	return codexSpawnAgentModelsFromSources(availableModels, registry.GetCodexClientModelsJSON(), func(modelID string) *registry.ModelInfo {
		return registry.LookupModelInfo(modelID)
	})
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
		if modelID == "gpt-5.5" {
			defaultTemplate = model
		}
	}
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
	if len(toolPaths) == 0 {
		return payload
	}
	modelList := formatCodexSpawnAgentModels(models)
	updated := payload
	for _, toolPath := range toolPaths {
		descriptionPath := toolPath + ".description"
		description := gjson.GetBytes(updated, descriptionPath)
		if description.Type == gjson.String && modelList != "" {
			rewritten := replaceCodexSpawnAgentModels(description.String(), modelList)
			if rewritten != description.String() {
				var errSet error
				updated, errSet = sjson.SetBytes(updated, descriptionPath, rewritten)
				if errSet != nil {
					return payload
				}
			}
		}

		var errDelete error
		updated, errDelete = sjson.DeleteBytes(updated, toolPath+".parameters.properties.message.encrypted")
		if errDelete != nil {
			return payload
		}
	}
	return updated
}

func hasCodexOptimizedCollaborationConflict(payload []byte) bool {
	if codexToolsHaveOptimizedCollaborationConflict(gjson.GetBytes(payload, "tools")) {
		return true
	}
	input := gjson.GetBytes(payload, "input")
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

func codexToolsHaveOptimizedCollaborationConflict(tools gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == codexOptimizedCollaborationNamespace || strings.HasPrefix(name, codexOptimizedCollaborationNamePrefix) {
			return true
		}
		if strings.TrimSpace(tool.Get("type").String()) == "namespace" && codexToolsHaveOptimizedCollaborationConflict(tool.Get("tools")) {
			return true
		}
	}
	return false
}

func optimizeCodexCollaborationNamespace(payload []byte, toolPaths []string) ([]byte, bool) {
	updated := payload
	optimized := false
	for _, toolPath := range toolPaths {
		separatorIndex := strings.LastIndex(toolPath, ".tools.")
		if separatorIndex < 0 {
			continue
		}
		namespacePath := toolPath[:separatorIndex]
		namespace := gjson.GetBytes(updated, namespacePath)
		if strings.TrimSpace(namespace.Get("type").String()) != "namespace" || strings.TrimSpace(namespace.Get("name").String()) != codexCollaborationNamespace {
			continue
		}
		var errSet error
		updated, errSet = sjson.SetBytes(updated, namespacePath+".name", codexOptimizedCollaborationNamespace)
		if errSet != nil {
			return payload, false
		}
		optimized = true
	}
	return updated, optimized
}

// RestoreCodexMultiAgentV2Response restores optimized collaboration namespace
// values before an upstream response is translated and returned to the client.
func RestoreCodexMultiAgentV2Response(payload []byte, optimized bool) []byte {
	if !optimized || len(payload) == 0 || !gjson.ValidBytes(payload) {
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

func rewriteCodexAgentMessageInput(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}

	updated := rewriteCodexAgentMessageContent(payload)
	for itemIndex, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "agent_message" {
			continue
		}
		itemPath := fmt.Sprintf("input.%d", itemIndex)
		var errSet error
		updated, errSet = sjson.SetBytes(updated, itemPath+".role", "user")
		if errSet != nil {
			return payload
		}
		updated, errSet = sjson.SetBytes(updated, itemPath+".type", "message")
		if errSet != nil {
			return payload
		}
	}
	return updated
}

func rewriteCodexAgentMessageContent(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}

	updated := payload
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
			if encryptedContent.Type != gjson.String {
				continue
			}
			partPath := fmt.Sprintf("input.%d.content.%d", itemIndex, partIndex)
			var errSet error
			updated, errSet = sjson.SetBytes(updated, partPath+".type", "input_text")
			if errSet != nil {
				return payload
			}
			updated, errSet = sjson.SetBytes(updated, partPath+".text", encryptedContent.String())
			if errSet != nil {
				return payload
			}
			updated, errSet = sjson.DeleteBytes(updated, partPath+".encrypted_content")
			if errSet != nil {
				return payload
			}
		}
	}
	return updated
}

func codexSpawnAgentToolPaths(payload []byte) []string {
	paths := make([]string, 0, 1)
	collectCodexSpawnAgentToolPaths(gjson.GetBytes(payload, "tools"), "tools", &paths)

	input := gjson.GetBytes(payload, "input")
	if input.IsArray() {
		for index, item := range input.Array() {
			if strings.TrimSpace(item.Get("type").String()) != "additional_tools" {
				continue
			}
			collectCodexSpawnAgentToolPaths(item.Get("tools"), fmt.Sprintf("input.%d.tools", index), &paths)
		}
	}
	return paths
}

func collectCodexSpawnAgentToolPaths(tools gjson.Result, path string, paths *[]string) {
	if !tools.IsArray() {
		return
	}
	for index, tool := range tools.Array() {
		toolPath := fmt.Sprintf("%s.%d", path, index)
		toolType := strings.TrimSpace(tool.Get("type").String())
		if toolType == "function" && strings.TrimSpace(tool.Get("name").String()) == "spawn_agent" {
			*paths = append(*paths, toolPath)
		}
		if toolType == "namespace" {
			collectCodexSpawnAgentToolPaths(tool.Get("tools"), toolPath+".tools", paths)
		}
	}
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
