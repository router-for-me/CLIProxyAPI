// Package claude provides request translation functionality for Claude Code API compatibility.
// It handles parsing and transforming Claude Code API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude Code API format and the internal client's expected format.
package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertClaudeRequestToCodex parses and transforms a Claude Code API request into the internal client format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the internal client.
// The function performs the following transformations:
// 1. Sets up a template with the model name and empty instructions field
// 2. Processes system messages and converts them to developer input content
// 3. Transforms message contents (text, image, tool_use, tool_result) to appropriate formats
// 4. Converts tools declarations to the expected format
// 5. Adds additional configuration parameters for the Codex API
// 6. Maps Claude thinking configuration to Codex reasoning settings
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the Claude Code API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in internal client format
func ConvertClaudeRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON

	template := []byte(`{"model":"","instructions":"","input":[]}`)

	rootResult := gjson.ParseBytes(rawJSON)
	toolNameMap := buildReverseMapFromClaudeOriginalToShort(rawJSON)
	toolsResult := rootResult.Get("tools")
	toolCatalog := buildClaudeToolCatalog(toolsResult, toolNameMap)
	toolDiscoveries, discoveryHistoryValid := buildClaudeToolDiscoveries(rootResult.Get("messages"), toolCatalog)
	nativeToolSearch := discoveryHistoryValid && supportsNativeClaudeToolSearch(toolsResult, rootResult.Get("tool_choice"))
	if !nativeToolSearch {
		toolDiscoveries = make(map[string]claudeToolDiscovery)
	}
	template, _ = sjson.SetBytes(template, "model", modelName)

	// Process system messages and convert them to input content format.
	systemsResult := rootResult.Get("system")
	if systemsResult.Exists() {
		message := []byte(`{"type":"message","role":"developer","content":[]}`)
		contentIndex := 0

		appendSystemText := func(text string) {
			if text == "" || util.IsClaudeCodeAttributionSystemText(text) {
				return
			}

			message, _ = sjson.SetBytes(message, fmt.Sprintf("content.%d.type", contentIndex), "input_text")
			message, _ = sjson.SetBytes(message, fmt.Sprintf("content.%d.text", contentIndex), text)
			contentIndex++
		}

		if systemsResult.Type == gjson.String {
			appendSystemText(systemsResult.String())
		} else if systemsResult.IsArray() {
			systemResults := systemsResult.Array()
			for i := 0; i < len(systemResults); i++ {
				systemResult := systemResults[i]
				if systemResult.Get("type").String() == "text" {
					appendSystemText(systemResult.Get("text").String())
				}
			}
		}

		if contentIndex > 0 {
			template, _ = sjson.SetRawBytes(template, "input.-1", message)
		}
	}

	// Process messages and transform their contents to appropriate formats.
	messagesResult := rootResult.Get("messages")
	if messagesResult.IsArray() {
		messageResults := messagesResult.Array()

		for i := 0; i < len(messageResults); i++ {
			messageResult := messageResults[i]
			messageRole := messageResult.Get("role").String()
			if messageRole == "system" {
				if reminderText, ok := translatorcommon.ClaudeMessageSystemReminderText(messageResult.Get("content")); ok {
					message := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}`)
					message, _ = sjson.SetBytes(message, "content.0.text", reminderText)
					template, _ = sjson.SetRawBytes(template, "input.-1", message)
				}
				continue
			}

			newMessage := func() []byte {
				msg := []byte(`{"type":"message","role":"","content":[]}`)
				msg, _ = sjson.SetBytes(msg, "role", messageRole)
				return msg
			}

			message := newMessage()
			contentIndex := 0
			hasContent := false

			flushMessage := func() {
				if hasContent {
					template, _ = sjson.SetRawBytes(template, "input.-1", message)
					message = newMessage()
					contentIndex = 0
					hasContent = false
				}
			}

			appendTextContent := func(text string) {
				partType := "input_text"
				if messageRole == "assistant" {
					partType = "output_text"
				}
				message, _ = sjson.SetBytes(message, fmt.Sprintf("content.%d.type", contentIndex), partType)
				message, _ = sjson.SetBytes(message, fmt.Sprintf("content.%d.text", contentIndex), text)
				contentIndex++
				hasContent = true
			}

			appendImageContent := func(dataURL string) {
				message, _ = sjson.SetBytes(message, fmt.Sprintf("content.%d.type", contentIndex), "input_image")
				message, _ = sjson.SetBytes(message, fmt.Sprintf("content.%d.image_url", contentIndex), dataURL)
				contentIndex++
				hasContent = true
			}

			appendReasoningContent := func(part gjson.Result) {
				if messageRole != "assistant" {
					return
				}

				rawSignature := part.Get("signature").String()
				signature, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderGPT, rawSignature)
				if !ok {
					if !codexClaudeTargetAcceptsGrokSignature(modelName) {
						return
					}
					if _, err := sigcompat.InspectGrokEncryptedContent(rawSignature); err != nil {
						return
					}
					signature = rawSignature
				}

				flushMessage()
				reasoningItem := []byte(`{"type":"reasoning","summary":[],"content":null}`)
				reasoningItem, _ = sjson.SetBytes(reasoningItem, "encrypted_content", signature)
				template, _ = sjson.SetRawBytes(template, "input.-1", reasoningItem)
			}

			messageContentsResult := messageResult.Get("content")
			if messageContentsResult.IsArray() {
				messageContentResults := messageContentsResult.Array()
				for j := 0; j < len(messageContentResults); j++ {
					messageContentResult := messageContentResults[j]
					contentType := messageContentResult.Get("type").String()

					switch contentType {
					case "text":
						appendTextContent(messageContentResult.Get("text").String())
					case "thinking":
						appendReasoningContent(messageContentResult)
					case "image":
						sourceResult := messageContentResult.Get("source")
						if sourceResult.Exists() {
							data := sourceResult.Get("data").String()
							if data == "" {
								data = sourceResult.Get("base64").String()
							}
							if data != "" {
								mediaType := sourceResult.Get("media_type").String()
								if mediaType == "" {
									mediaType = sourceResult.Get("mime_type").String()
								}
								if mediaType == "" {
									mediaType = "application/octet-stream"
								}
								dataURL := fmt.Sprintf("data:%s;base64,%s", mediaType, data)
								appendImageContent(dataURL)
							}
						}
					case "tool_use":
						flushMessage()
						callID := shortenCodexCallIDIfNeeded(messageContentResult.Get("id").String())
						if _, ok := toolDiscoveries[callID]; ok && messageContentResult.Get("name").String() == "ToolSearch" {
							toolSearchCall := []byte(`{"type":"tool_search_call","call_id":"","status":"completed","execution":"client","arguments":{}}`)
							toolSearchCall, _ = sjson.SetBytes(toolSearchCall, "call_id", callID)
							if input := messageContentResult.Get("input"); input.Raw != "" && gjson.Valid(input.Raw) {
								toolSearchCall, _ = sjson.SetRawBytes(toolSearchCall, "arguments", []byte(input.Raw))
							}
							template, _ = sjson.SetRawBytes(template, "input.-1", toolSearchCall)
							continue
						}
						functionCallMessage := []byte(`{"type":"function_call"}`)
						functionCallMessage, _ = sjson.SetBytes(functionCallMessage, "call_id", callID)
						{
							name := messageContentResult.Get("name").String()
							if short, ok := toolNameMap[name]; ok {
								name = short
							} else {
								name = shortenNameIfNeeded(name)
							}
							functionCallMessage, _ = sjson.SetBytes(functionCallMessage, "name", name)
						}
						functionCallMessage, _ = sjson.SetBytes(functionCallMessage, "arguments", messageContentResult.Get("input").Raw)
						template, _ = sjson.SetRawBytes(template, "input.-1", functionCallMessage)
					case "tool_result":
						flushMessage()
						callID := shortenCodexCallIDIfNeeded(messageContentResult.Get("tool_use_id").String())
						if discovery, ok := toolDiscoveries[callID]; ok {
							toolSearchOutput := []byte(`{"type":"tool_search_output","call_id":"","status":"completed","execution":"client","tools":[]}`)
							toolSearchOutput, _ = sjson.SetBytes(toolSearchOutput, "call_id", callID)
							for _, tool := range discovery.loadedTools {
								toolSearchOutput, _ = sjson.SetRawBytes(toolSearchOutput, "tools.-1", tool)
							}
							template, _ = sjson.SetRawBytes(template, "input.-1", toolSearchOutput)
							if len(discovery.residualContent) > 0 {
								template, _ = sjson.SetRawBytes(template, "input.-1", convertClaudeDiscoveryResidualToMessage(discovery.residualContent))
							}
							continue
						}
						functionCallOutputMessage := []byte(`{"type":"function_call_output"}`)
						functionCallOutputMessage, _ = sjson.SetBytes(functionCallOutputMessage, "call_id", callID)

						contentResult := messageContentResult.Get("content")
						if contentResult.IsArray() {
							toolResultContentIndex := 0
							toolResultContent := []byte(`[]`)
							contentResults := contentResult.Array()
							for k := 0; k < len(contentResults); k++ {
								toolResultContentType := contentResults[k].Get("type").String()
								if toolResultContentType == "image" {
									sourceResult := contentResults[k].Get("source")
									if sourceResult.Exists() {
										data := sourceResult.Get("data").String()
										if data == "" {
											data = sourceResult.Get("base64").String()
										}
										if data != "" {
											mediaType := sourceResult.Get("media_type").String()
											if mediaType == "" {
												mediaType = sourceResult.Get("mime_type").String()
											}
											if mediaType == "" {
												mediaType = "application/octet-stream"
											}
											dataURL := fmt.Sprintf("data:%s;base64,%s", mediaType, data)

											toolResultContent, _ = sjson.SetBytes(toolResultContent, fmt.Sprintf("%d.type", toolResultContentIndex), "input_image")
											toolResultContent, _ = sjson.SetBytes(toolResultContent, fmt.Sprintf("%d.image_url", toolResultContentIndex), dataURL)
											toolResultContentIndex++
										}
									}
								} else if toolResultContentType == "text" {
									toolResultContent, _ = sjson.SetBytes(toolResultContent, fmt.Sprintf("%d.type", toolResultContentIndex), "input_text")
									toolResultContent, _ = sjson.SetBytes(toolResultContent, fmt.Sprintf("%d.text", toolResultContentIndex), contentResults[k].Get("text").String())
									toolResultContentIndex++
								}
							}
							if toolResultContentIndex > 0 {
								functionCallOutputMessage, _ = sjson.SetRawBytes(functionCallOutputMessage, "output", toolResultContent)
							} else {
								functionCallOutputMessage, _ = sjson.SetBytes(functionCallOutputMessage, "output", messageContentResult.Get("content").String())
							}
						} else {
							functionCallOutputMessage, _ = sjson.SetBytes(functionCallOutputMessage, "output", messageContentResult.Get("content").String())
						}

						template, _ = sjson.SetRawBytes(template, "input.-1", functionCallOutputMessage)
					}
				}
				flushMessage()
			} else if messageContentsResult.Type == gjson.String {
				appendTextContent(messageContentsResult.String())
				flushMessage()
			}
		}

	}

	// Convert tools declarations to the expected format for the Codex API.
	if toolsResult.IsArray() {
		template, _ = sjson.SetRawBytes(template, "tools", []byte(`[]`))
		webSearchToolNames := buildClaudeWebSearchToolNameSet(toolsResult)
		template, _ = sjson.SetRawBytes(template, "tool_choice", convertClaudeToolChoiceToCodex(rootResult.Get("tool_choice"), toolNameMap, webSearchToolNames, nativeToolSearch))
		toolResults := toolsResult.Array()
		for i := 0; i < len(toolResults); i++ {
			toolResult := toolResults[i]
			// Special handling: map Claude web search tool to Codex web_search
			if isClaudeWebSearchToolType(toolResult.Get("type").String()) {
				template, _ = sjson.SetRawBytes(template, "tools.-1", convertClaudeWebSearchToolToCodex(toolResult))
				continue
			}
			if nativeToolSearch && toolResult.Get("name").String() == "ToolSearch" {
				template, _ = sjson.SetRawBytes(template, "tools.-1", convertClaudeToolSearchToCodex(toolResult))
				continue
			}
			if nativeToolSearch && toolResult.Get("defer_loading").Bool() {
				continue
			}
			tool := convertClaudeFunctionToolToCodex(toolResult, toolNameMap, false)
			template, _ = sjson.SetRawBytes(template, "tools.-1", tool)
		}
	}

	// Default to parallel tool calls unless tool_choice explicitly disables them.
	parallelToolCalls := true
	if disableParallelToolUse := rootResult.Get("tool_choice.disable_parallel_tool_use"); disableParallelToolUse.Exists() {
		parallelToolCalls = !disableParallelToolUse.Bool()
	}

	// Add additional configuration parameters for the Codex API.
	template, _ = sjson.SetBytes(template, "parallel_tool_calls", parallelToolCalls)

	// Convert thinking.budget_tokens to reasoning.effort.
	reasoningEffort := "medium"
	if thinkingConfig := rootResult.Get("thinking"); thinkingConfig.Exists() && thinkingConfig.IsObject() {
		switch thinkingConfig.Get("type").String() {
		case "enabled":
			if budgetTokens := thinkingConfig.Get("budget_tokens"); budgetTokens.Exists() {
				budget := int(budgetTokens.Int())
				if effort, ok := thinking.ConvertBudgetToLevel(budget); ok && effort != "" {
					reasoningEffort = effort
				}
			}
		case "adaptive", "auto":
			// Adaptive thinking can carry an explicit effort in output_config.effort (Claude 4.6).
			// Pass through directly; ApplyThinking handles clamping to target model's levels.
			effort := ""
			if v := rootResult.Get("output_config.effort"); v.Exists() && v.Type == gjson.String {
				effort = strings.ToLower(strings.TrimSpace(v.String()))
			}
			if effort != "" {
				reasoningEffort = effort
			} else {
				reasoningEffort = string(thinking.LevelXHigh)
			}
		case "disabled":
			if effort, ok := thinking.ConvertBudgetToLevel(0); ok && effort != "" {
				reasoningEffort = effort
			}
		}
	}
	template, _ = sjson.SetBytes(template, "reasoning.effort", reasoningEffort)
	template, _ = sjson.SetBytes(template, "reasoning.summary", "auto")
	if serviceTier := normalizeCodexServiceTier(rootResult.Get("service_tier")); serviceTier != "" {
		template, _ = sjson.SetBytes(template, "service_tier", serviceTier)
	}
	template, _ = sjson.SetBytes(template, "stream", true)
	template, _ = sjson.SetBytes(template, "store", false)
	template, _ = sjson.SetBytes(template, "include", []string{"reasoning.encrypted_content"})

	return template
}

type claudeToolCatalogEntry struct {
	loadedTool []byte
}

type claudeToolDiscovery struct {
	loadedTools     [][]byte
	residualContent []gjson.Result
}

func buildClaudeToolDiscoveries(messages gjson.Result, toolCatalog map[string]claudeToolCatalogEntry) (map[string]claudeToolDiscovery, bool) {
	discoveries := make(map[string]claudeToolDiscovery)
	if !messages.IsArray() {
		return discoveries, true
	}

	toolUseCounts := make(map[string]int)
	toolSearchUseCounts := make(map[string]int)
	toolResultCounts := make(map[string]int)
	toolResults := make(map[string]gjson.Result)
	discoveryResultIDs := make(map[string]struct{})
	toolUsePositions := make(map[string]int)
	toolResultPositions := make(map[string]int)
	position := 0
	for _, message := range messages.Array() {
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, part := range content.Array() {
			position++
			switch part.Get("type").String() {
			case "tool_use":
				callID := shortenCodexCallIDIfNeeded(part.Get("id").String())
				if part.Get("name").String() == "ToolSearch" && callID == "" {
					return make(map[string]claudeToolDiscovery), false
				}
				if callID != "" {
					toolUseCounts[callID]++
					toolUsePositions[callID] = position
					if part.Get("name").String() == "ToolSearch" {
						toolSearchUseCounts[callID]++
					}
				}
			case "tool_result":
				callID := shortenCodexCallIDIfNeeded(part.Get("tool_use_id").String())
				isDiscoveryResult := hasClaudeToolReference(part.Get("content"))
				if callID == "" {
					if isDiscoveryResult {
						return make(map[string]claudeToolDiscovery), false
					}
					continue
				}
				toolResultCounts[callID]++
				toolResults[callID] = part
				toolResultPositions[callID] = position
				if isDiscoveryResult {
					discoveryResultIDs[callID] = struct{}{}
				}
			}
		}
	}

	for callID := range discoveryResultIDs {
		if toolSearchUseCounts[callID] != 1 {
			return make(map[string]claudeToolDiscovery), false
		}
	}

	for callID, toolSearchUseCount := range toolSearchUseCounts {
		toolResult, hasResult := toolResults[callID]
		if toolSearchUseCount != 1 ||
			toolUseCounts[callID] != 1 ||
			toolResultCounts[callID] != 1 ||
			!hasResult ||
			toolUsePositions[callID] >= toolResultPositions[callID] ||
			toolResult.Get("is_error").Bool() {
			return make(map[string]claudeToolDiscovery), false
		}

		toolNames, residualContent, ok := parseClaudeToolDiscoveryContent(toolResult.Get("content"))
		if !ok {
			return make(map[string]claudeToolDiscovery), false
		}

		loadedTools := make([][]byte, 0, len(toolNames))
		for _, toolName := range toolNames {
			entry, exists := toolCatalog[toolName]
			if !exists {
				return make(map[string]claudeToolDiscovery), false
			}
			if len(entry.loadedTool) > 0 {
				loadedTools = append(loadedTools, entry.loadedTool)
			}
		}

		discoveries[callID] = claudeToolDiscovery{
			loadedTools:     loadedTools,
			residualContent: residualContent,
		}
	}

	return discoveries, true
}

func hasClaudeToolReference(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		if part.Get("type").String() == "tool_reference" {
			return true
		}
	}
	return false
}

func parseClaudeToolDiscoveryContent(content gjson.Result) ([]string, []gjson.Result, bool) {
	if !content.IsArray() {
		return nil, nil, false
	}

	parts := content.Array()
	if len(parts) == 0 {
		return nil, nil, false
	}
	toolNames := make([]string, 0, len(parts))
	residualContent := make([]gjson.Result, 0, len(parts))
	seenToolNames := make(map[string]struct{})
	for _, part := range parts {
		switch part.Get("type").String() {
		case "tool_reference":
			toolName := part.Get("tool_name").String()
			if toolName == "" {
				return nil, nil, false
			}
			if _, seen := seenToolNames[toolName]; !seen {
				seenToolNames[toolName] = struct{}{}
				toolNames = append(toolNames, toolName)
			}
		case "text":
			residualContent = append(residualContent, part)
		case "image":
			if claudeImageDataURL(part) == "" {
				return nil, nil, false
			}
			residualContent = append(residualContent, part)
		default:
			return nil, nil, false
		}
	}

	if len(toolNames) == 0 {
		return nil, nil, false
	}
	return toolNames, residualContent, true
}

func supportsNativeClaudeToolSearch(tools gjson.Result, toolChoice gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}

	seenNames := make(map[string]struct{})
	hasToolSearch := false
	hasDeferredTool := false
	deferredFunctionNames := make(map[string]struct{})
	for _, tool := range tools.Array() {
		name := tool.Get("name").String()
		if name == "" {
			continue
		}
		if _, exists := seenNames[name]; exists {
			return false
		}
		seenNames[name] = struct{}{}

		if name == "ToolSearch" {
			if tool.Get("type").String() != "" || !tool.Get("input_schema").IsObject() {
				return false
			}
			hasToolSearch = true
		}
		isHostedTool := isClaudeWebSearchToolType(tool.Get("type").String())
		if tool.Get("defer_loading").Bool() && name != "ToolSearch" {
			hasDeferredTool = true
			if !isHostedTool {
				deferredFunctionNames[name] = struct{}{}
			}
		}
	}

	if toolChoice.Get("type").String() == "tool" {
		if _, forcedDeferredFunction := deferredFunctionNames[toolChoice.Get("name").String()]; forcedDeferredFunction {
			return false
		}
	}

	return hasToolSearch && hasDeferredTool
}

func buildClaudeToolCatalog(tools gjson.Result, toolNameMap map[string]string) map[string]claudeToolCatalogEntry {
	toolCatalog := make(map[string]claudeToolCatalogEntry)
	if !tools.IsArray() {
		return toolCatalog
	}

	for _, tool := range tools.Array() {
		name := tool.Get("name").String()
		if name == "" {
			continue
		}

		entry := claudeToolCatalogEntry{}
		convertedName := name
		if !isClaudeWebSearchToolType(tool.Get("type").String()) {
			converted := convertClaudeFunctionToolToCodex(tool, toolNameMap, tool.Get("defer_loading").Bool())
			convertedName = gjson.GetBytes(converted, "name").String()
			if tool.Get("defer_loading").Bool() {
				entry.loadedTool = converted
			}
		}

		toolCatalog[name] = entry
		if convertedName != "" {
			toolCatalog[convertedName] = entry
		}
	}

	return toolCatalog
}

func convertClaudeFunctionToolToCodex(toolResult gjson.Result, toolNameMap map[string]string, keepDeferLoading bool) []byte {
	tool := []byte(`{"type":"function","name":"","parameters":{},"strict":false}`)
	if v := toolResult.Get("name"); v.Exists() {
		name := v.String()
		if short, ok := toolNameMap[name]; ok {
			name = short
		} else {
			name = shortenNameIfNeeded(name)
		}
		tool, _ = sjson.SetBytes(tool, "name", name)
	}
	if description := toolResult.Get("description"); description.Exists() && description.Type == gjson.String {
		tool, _ = sjson.SetBytes(tool, "description", description.String())
	}
	tool, _ = sjson.SetRawBytes(tool, "parameters", []byte(normalizeToolParameters(toolResult.Get("input_schema").Raw)))
	tool, _ = sjson.DeleteBytes(tool, "parameters.$schema")
	if keepDeferLoading {
		tool, _ = sjson.SetBytes(tool, "defer_loading", true)
	}
	return tool
}

func convertClaudeToolSearchToCodex(toolResult gjson.Result) []byte {
	tool := []byte(`{"type":"tool_search","execution":"client","parameters":{}}`)
	if description := toolResult.Get("description"); description.Exists() && description.Type == gjson.String {
		tool, _ = sjson.SetBytes(tool, "description", description.String())
	}
	tool, _ = sjson.SetRawBytes(tool, "parameters", []byte(normalizeToolParameters(toolResult.Get("input_schema").Raw)))
	tool, _ = sjson.DeleteBytes(tool, "parameters.$schema")
	return tool
}

func convertClaudeDiscoveryResidualToMessage(parts []gjson.Result) []byte {
	message := []byte(`{"type":"message","role":"user","content":[]}`)
	contentIndex := 0
	for _, part := range parts {
		switch part.Get("type").String() {
		case "text":
			message, _ = sjson.SetBytes(message, fmt.Sprintf("content.%d.type", contentIndex), "input_text")
			message, _ = sjson.SetBytes(message, fmt.Sprintf("content.%d.text", contentIndex), part.Get("text").String())
			contentIndex++
		case "image":
			message, _ = sjson.SetBytes(message, fmt.Sprintf("content.%d.type", contentIndex), "input_image")
			message, _ = sjson.SetBytes(message, fmt.Sprintf("content.%d.image_url", contentIndex), claudeImageDataURL(part))
			contentIndex++
		}
	}
	return message
}

func claudeImageDataURL(part gjson.Result) string {
	source := part.Get("source")
	if !source.Exists() {
		return ""
	}
	data := source.Get("data").String()
	if data == "" {
		data = source.Get("base64").String()
	}
	if data == "" {
		return ""
	}
	mediaType := source.Get("media_type").String()
	if mediaType == "" {
		mediaType = source.Get("mime_type").String()
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return fmt.Sprintf("data:%s;base64,%s", mediaType, data)
}

func codexClaudeTargetAcceptsGrokSignature(modelName string) bool {
	baseModel := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName))
	return strings.Contains(baseModel, "grok")
}

func normalizeCodexServiceTier(result gjson.Result) string {
	if !result.Exists() || result.Type != gjson.String {
		return ""
	}

	switch strings.ToLower(strings.TrimSpace(result.String())) {
	case "fast", "priority":
		return "priority"
	default:
		return ""
	}
}

// shortenCodexCallIDIfNeeded keeps Claude tool IDs within the OpenAI Responses
// API call_id limit while preserving a stable, low-collision mapping.
func shortenCodexCallIDIfNeeded(id string) string {
	const limit = 64
	if len(id) <= limit {
		return id
	}

	sum := sha256.Sum256([]byte(id))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLen := limit - len(suffix)
	if prefixLen <= 0 {
		return suffix[len(suffix)-limit:]
	}
	return id[:prefixLen] + suffix
}

func isClaudeWebSearchToolType(toolType string) bool {
	return toolType == "web_search_20250305" || toolType == "web_search_20260209"
}

func buildClaudeWebSearchToolNameSet(tools gjson.Result) map[string]struct{} {
	names := map[string]struct{}{}
	if !tools.IsArray() {
		return names
	}

	tools.ForEach(func(_, tool gjson.Result) bool {
		toolType := tool.Get("type").String()
		if !isClaudeWebSearchToolType(toolType) {
			return true
		}

		if name := tool.Get("name").String(); name != "" {
			names[name] = struct{}{}
		}
		return true
	})

	return names
}

func convertClaudeToolChoiceToCodex(toolChoice gjson.Result, toolNameMap map[string]string, webSearchToolNames map[string]struct{}, nativeToolSearch bool) []byte {
	if !toolChoice.Exists() || toolChoice.Type == gjson.Null {
		return []byte(`"auto"`)
	}

	choiceType := toolChoice.Get("type").String()
	if choiceType == "" && toolChoice.Type == gjson.String {
		choiceType = toolChoice.String()
	}

	switch choiceType {
	case "auto", "":
		return []byte(`"auto"`)
	case "any":
		return []byte(`"required"`)
	case "none":
		return []byte(`"none"`)
	case "tool":
		name := toolChoice.Get("name").String()
		if nativeToolSearch && name == "ToolSearch" {
			return []byte(`{"type":"tool_search"}`)
		}
		if _, ok := webSearchToolNames[name]; ok {
			return []byte(`{"type":"web_search"}`)
		}
		if short, ok := toolNameMap[name]; ok {
			name = short
		} else {
			name = shortenNameIfNeeded(name)
		}
		if name == "" {
			return []byte(`"auto"`)
		}

		choice := []byte(`{"type":"function","name":""}`)
		choice, _ = sjson.SetBytes(choice, "name", name)
		return choice
	default:
		return []byte(`"auto"`)
	}
}

func convertClaudeWebSearchToolToCodex(tool gjson.Result) []byte {
	out := []byte(`{"type":"web_search"}`)
	if allowedDomains := tool.Get("allowed_domains"); allowedDomains.Exists() && allowedDomains.IsArray() {
		out, _ = sjson.SetRawBytes(out, "filters.allowed_domains", []byte(allowedDomains.Raw))
	}
	if userLocation := tool.Get("user_location"); userLocation.Exists() && userLocation.IsObject() {
		out, _ = sjson.SetRawBytes(out, "user_location", []byte(userLocation.Raw))
	}
	return out
}

// shortenNameIfNeeded applies a simple shortening rule for a single name.
func shortenNameIfNeeded(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			cand := "mcp__" + name[idx+2:]
			if len(cand) > limit {
				return cand[:limit]
			}
			return cand
		}
	}
	return name[:limit]
}

// buildShortNameMap ensures uniqueness of shortened names within a request.
func buildShortNameMap(names []string) map[string]string {
	const limit = 64
	used := map[string]struct{}{}
	m := map[string]string{}

	baseCandidate := func(n string) string {
		if len(n) <= limit {
			return n
		}
		if strings.HasPrefix(n, "mcp__") {
			idx := strings.LastIndex(n, "__")
			if idx > 0 {
				cand := "mcp__" + n[idx+2:]
				if len(cand) > limit {
					cand = cand[:limit]
				}
				return cand
			}
		}
		return n[:limit]
	}

	makeUnique := func(cand string) string {
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; ; i++ {
			suffix := "_" + strconv.Itoa(i)
			allowed := limit - len(suffix)
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			tmp = tmp + suffix
			if _, ok := used[tmp]; !ok {
				return tmp
			}
		}
	}

	for _, n := range names {
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		m[n] = uniq
	}
	return m
}

// buildReverseMapFromClaudeOriginalToShort builds original->short map, used to map tool_use names to short.
func buildReverseMapFromClaudeOriginalToShort(original []byte) map[string]string {
	tools := gjson.GetBytes(original, "tools")
	m := map[string]string{}
	if !tools.IsArray() {
		return m
	}
	var names []string
	arr := tools.Array()
	for i := 0; i < len(arr); i++ {
		n := arr[i].Get("name").String()
		if n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		m = buildShortNameMap(names)
	}
	return m
}

// normalizeToolParameters ensures object schemas contain at least an empty properties map.
func normalizeToolParameters(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || !gjson.Valid(raw) {
		return `{"type":"object","properties":{}}`
	}
	result := gjson.Parse(raw)
	schema := []byte(raw)
	schemaType := result.Get("type").String()
	if schemaType == "" {
		schema, _ = sjson.SetBytes(schema, "type", "object")
		schemaType = "object"
	}
	if schemaType == "object" && !result.Get("properties").Exists() {
		schema, _ = sjson.SetRawBytes(schema, "properties", []byte(`{}`))
	}
	return string(schema)
}
