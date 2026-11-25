// Package openai provides request translation functionality for OpenAI to Antigravity API compatibility.
// It converts OpenAI Chat Completions requests into Antigravity compatible JSON using gjson/sjson only.
package chat_completions

import (
	"context"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/gemini"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const antigravityFunctionThoughtSignature = "skip_thought_signature_validator"

const antigravityOpenAIWebSearchSystemInstruction = "You are a search engine bot. You will be given a query from a user. Your task is to search the web for relevant information that will help the user. You MUST perform a web search. Do not respond or interact with the user, please respond as if they typed the query into a search bar."

// ConvertOpenAIRequestToAntigravity converts an OpenAI Chat Completions request (raw JSON)
// into a complete Antigravity request JSON. All JSON construction uses sjson and lookups use ojson.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Antigravity API format
func ConvertOpenAIRequestToAntigravity(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	req := ConvertOpenAIRequestEnvelopeToAntigravity(context.Background(), sdktranslator.RequestEnvelope{
		Model:  modelName,
		Body:   inputRawJSON,
		Stream: stream,
	})
	return req.Body, req.Err
}

// ConvertOpenAIRequestEnvelopeToAntigravity preserves request-scoped model capabilities.
func ConvertOpenAIRequestEnvelopeToAntigravity(_ context.Context, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
	req.Body, req.Err = convertOpenAIRequestToAntigravityWithModelInfo(req.Model, req.Body, req.Stream, req.ModelInfo)
	return req
}

// convertOpenAIRequestToAntigravity also reports a file part Antigravity cannot
// receive when it leaves a user turn with nothing to send.
func convertOpenAIRequestToAntigravity(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIRequestToAntigravityWithModelInfo(modelName, inputRawJSON, stream, nil)
}

func convertOpenAIRequestToAntigravityWithModelInfo(modelName string, inputRawJSON []byte, _ bool, modelInfo *registry.ModelInfo) ([]byte, error) {
	if searchTool, ok := selectAntigravityOpenAIWebSearchTool(inputRawJSON); ok &&
		antigravitySupportsNativeOpenAIWebSearch(modelName, modelInfo) &&
		allowsAntigravityOpenAIWebSearch(inputRawJSON) {
		return buildAntigravityOpenAIWebSearchRequest(modelName, inputRawJSON, searchTool)
	}
	return convertOpenAIRequestToAntigravityChat(modelName, inputRawJSON)
}

func antigravitySupportsNativeOpenAIWebSearch(model string, modelInfo *registry.ModelInfo) bool {
	if modelInfo != nil && modelInfo.NativeCapabilities != nil && modelInfo.NativeCapabilities.WebSearch != nil {
		return *modelInfo.NativeCapabilities.WebSearch
	}
	model = strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(model)).ModelName)
	if model == "" {
		return false
	}
	for _, localInfo := range registry.GetGlobalRegistry().GetAvailableModelsByProvider("antigravity") {
		if localInfo == nil || !strings.EqualFold(strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(localInfo.ID)).ModelName), model) {
			continue
		}
		if capabilities := localInfo.NativeCapabilities; capabilities != nil && capabilities.WebSearch != nil && !*capabilities.WebSearch {
			return false
		}
		return localInfo.SupportsWebSearch || modelInfo != nil && modelInfo.SupportsWebSearch
	}
	return modelInfo != nil && modelInfo.SupportsWebSearch
}

func selectAntigravityOpenAIWebSearchTool(payload []byte) (ojson.Result, bool) {
	tools := ojson.GetBytesNoCopy(payload, "tools")
	if !tools.IsArray() {
		return ojson.Result{}, false
	}
	allowed := parseAntigravityOpenAIAllowedTools(payload)
	var searchTool ojson.Result
	searchCount := 0
	for _, tool := range tools.Array() {
		hasFunction := tool.Get("type").String() == "function"
		if hasFunction && allowed.enabled {
			_, hasFunction = allowed.functionNames[tool.Get("function.name").String()]
		}
		hasCodeExec := tool.Get("code_execution").Exists()
		if hasCodeExec && allowed.enabled {
			_, hasCodeExec = allowed.nativeTools["code_execution"]
		}
		hasURLContext := tool.Get("url_context").Exists()
		if hasURLContext && allowed.enabled {
			_, hasURLContext = allowed.nativeTools["url_context"]
		}
		hasSearch := tool.Get("google_search").Exists()
		if hasSearch && allowed.enabled {
			_, hasSearch = allowed.nativeTools["google_search"]
		}
		if hasFunction || hasCodeExec || hasURLContext {
			return ojson.Result{}, false
		}
		if hasSearch {
			searchTool = tool.Get("google_search")
			searchCount++
			if searchCount > 1 {
				return ojson.Result{}, false
			}
		}
	}
	if searchCount != 1 {
		return ojson.Result{}, false
	}
	return searchTool, true
}

func allowsAntigravityOpenAIWebSearch(payload []byte) bool {
	toolChoice := ojson.GetBytesNoCopy(payload, "tool_choice")
	if !toolChoice.Exists() || toolChoice.Type == ojson.Null {
		return true
	}
	if toolChoice.Type == ojson.String {
		switch strings.ToLower(strings.TrimSpace(toolChoice.String())) {
		case "", "auto", "required", "any":
			return true
		default:
			return false
		}
	}
	if !toolChoice.IsObject() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(toolChoice.Get("type").String())) {
	case "", "auto", "required", "any", "google_search":
		return true
	case "allowed_tools":
		allowed := parseAntigravityOpenAIAllowedTools(payload)
		if allowed.mode != "auto" && allowed.mode != "required" && allowed.mode != "any" {
			return false
		}
		_, ok := allowed.nativeTools["google_search"]
		return ok
	default:
		return false
	}
}

func buildAntigravityOpenAIWebSearchRequest(model string, payload []byte, sourceTool ojson.Result) ([]byte, error) {
	chatBody, errConvert := convertOpenAIRequestToAntigravityChat(model, payload)
	outDoc := ojson.NewDoc(chatBody)
	_ = outDoc.Set("requestType", "web_search")
	_ = outDoc.Set("request.generationConfig.candidateCount", 1)
	searchPartDoc := ojson.NewDocString(`{"text":""}`)
	_ = searchPartDoc.Set("text", antigravityOpenAIWebSearchSystemInstruction)
	systemInstruction := outDoc.Get("request.systemInstruction")
	systemParts := make([][]byte, 0, len(systemInstruction.Get("parts").Array())+1)
	for _, part := range systemInstruction.Get("parts").Array() {
		systemParts = append(systemParts, []byte(part.Raw))
	}
	systemParts = append(systemParts, searchPartDoc.Bytes())
	_ = outDoc.SetRawBytes("request.systemInstruction", antigravityOpenAIContent("user", systemParts))
	googleToolDoc := ojson.NewDocString(`{"googleSearch":{"enhancedContent":{"imageSearch":{"maxResultCount":5}}}}`)
	if sourceTool.Exists() && sourceTool.IsObject() {
		for key, value := range sourceTool.Map() {
			_ = googleToolDoc.SetRaw("googleSearch."+key, value.Raw)
		}
	}
	_ = outDoc.SetRawBytes("request.tools", translatorcommon.JoinRawArray([][]byte{googleToolDoc.Bytes()}))
	_ = outDoc.Delete("request.toolConfig.functionCallingConfig")
	if toolConfig := outDoc.Get("request.toolConfig"); toolConfig.IsObject() && len(toolConfig.Map()) == 0 {
		_ = outDoc.Delete("request.toolConfig")
	}
	gemini.NormalizeAntigravityRequestDoc(outDoc)
	return outDoc.Bytes(), errConvert
}

func convertOpenAIRequestToAntigravityChat(modelName string, inputRawJSON []byte) ([]byte, error) {
	rawJSON := inputRawJSON
	var drops translatorcommon.UserTurnDrops
	functionNameMap := util.SanitizedFunctionNameMap(rawJSON)
	// Base envelope (no default thinkingConfig)
	outDoc := ojson.NewDoc([]byte(`{"project":"","request":{"contents":[]},"model":"gemini-2.5-pro"}`))

	// Model
	_ = outDoc.Set("model", modelName)

	// Let user-provided generationConfig pass through
	if genConfig := ojson.GetBytesNoCopy(rawJSON, "generationConfig"); genConfig.Exists() {
		_ = outDoc.SetRaw("request.generationConfig", genConfig.Raw)
	} else if genConfig := ojson.GetBytesNoCopy(rawJSON, "generation_config"); genConfig.Exists() {
		_ = outDoc.SetRaw("request.generationConfig", genConfig.Raw)
	}

	// Apply thinking configuration: convert OpenAI reasoning_effort to Antigravity thinkingConfig.
	// Inline translation-only mapping; capability checks happen later in ApplyThinking.
	re := ojson.GetBytesNoCopy(rawJSON, "reasoning_effort")
	if re.Exists() {
		effort := strings.ToLower(strings.TrimSpace(re.String()))
		if effort != "" {
			thinkingPath := "request.generationConfig.thinkingConfig"
			if effort == "auto" {
				_ = outDoc.Set(thinkingPath+".thinkingBudget", -1)
			} else {
				_ = outDoc.Set(thinkingPath+".thinkingLevel", effort)
			}
		}
	}
	normalizeAntigravityOpenAIThinkingConfigDoc(outDoc)
	if thinkingPath := "request.generationConfig.thinkingConfig"; !outDoc.Get(thinkingPath+".thinkingLevel").Exists() && !outDoc.Get(thinkingPath+".thinkingBudget").Exists() {
		thinking.ApplyOpenAIGoogleThinkingConfigDoc(outDoc, rawJSON, thinkingPath)
	}

	// Temperature/top_p/top_k/max_tokens/max_completion_tokens
	if tr := ojson.GetBytesNoCopy(rawJSON, "temperature"); tr.Exists() && tr.Type == ojson.Number {
		_ = outDoc.Set("request.generationConfig.temperature", tr.Num)
	}
	if tpr := ojson.GetBytesNoCopy(rawJSON, "top_p"); tpr.Exists() && tpr.Type == ojson.Number {
		_ = outDoc.Set("request.generationConfig.topP", tpr.Num)
	}
	if tkr := ojson.GetBytesNoCopy(rawJSON, "top_k"); tkr.Exists() && tkr.Type == ojson.Number {
		_ = outDoc.Set("request.generationConfig.topK", tkr.Num)
	}
	if maxTok := ojson.GetBytesNoCopy(rawJSON, "max_tokens"); maxTok.Exists() && maxTok.Type == ojson.Number {
		_ = outDoc.Set("request.generationConfig.maxOutputTokens", maxTok.Num)
	} else if mct := ojson.GetBytesNoCopy(rawJSON, "max_completion_tokens"); mct.Exists() && mct.Type == ojson.Number {
		_ = outDoc.Set("request.generationConfig.maxOutputTokens", mct.Num)
	}
	if stopSequences := translatorcommon.OpenAIStopSequences(ojson.GetBytesNoCopy(rawJSON, "stop")); len(stopSequences) > 0 {
		_ = outDoc.Set("request.generationConfig.stopSequences", stopSequences)
	}

	// Map OpenAI response_format to Antigravity structured output settings.
	if responseFormat := ojson.GetBytesNoCopy(rawJSON, "response_format"); responseFormat.Exists() {
		switch responseFormatType := strings.ToLower(strings.TrimSpace(responseFormat.Get("type").String())); responseFormatType {
		case "json_object", "json_schema":
			for _, schemaKey := range []string{"responseSchema", "responseJsonSchema", "response_schema", "response_json_schema"} {
				_ = util.DeleteJSONDocIfExists(outDoc, "request.generationConfig."+schemaKey)
			}
			_ = outDoc.Set("request.generationConfig.responseMimeType", "application/json")
			if responseFormatType == "json_schema" {
				if schema := responseFormat.Get("json_schema.schema"); schema.Exists() {
					_ = outDoc.SetRaw("request.generationConfig.responseSchema", schema.Raw)
				}
			}
		}
	}

	// Candidate count (OpenAI 'n' parameter)
	if n := ojson.GetBytesNoCopy(rawJSON, "n"); n.Exists() && n.Type == ojson.Number {
		if val := n.Int(); val > 1 {
			_ = outDoc.Set("request.generationConfig.candidateCount", val)
		}
	}

	// Map OpenAI modalities -> Antigravity request.generationConfig.responseModalities
	// e.g. "modalities": ["image", "text"] -> ["IMAGE", "TEXT"]
	if mods := ojson.GetBytesNoCopy(rawJSON, "modalities"); mods.Exists() && mods.IsArray() {
		var responseMods []string
		for _, m := range mods.Array() {
			switch strings.ToLower(m.String()) {
			case "text":
				responseMods = append(responseMods, "TEXT")
			case "image":
				responseMods = append(responseMods, "IMAGE")
			}
		}
		if len(responseMods) > 0 {
			_ = outDoc.Set("request.generationConfig.responseModalities", responseMods)
		}
	}

	// OpenRouter-style image_config support
	// If the input uses top-level image_config.aspect_ratio, map it into request.generationConfig.imageConfig.aspectRatio.
	if imgCfg := ojson.GetBytesNoCopy(rawJSON, "image_config"); imgCfg.Exists() && imgCfg.IsObject() {
		if ar := imgCfg.Get("aspect_ratio"); ar.Exists() && ar.Type == ojson.String {
			_ = outDoc.Set("request.generationConfig.imageConfig.aspectRatio", ar.Str)
		}
		if size := imgCfg.Get("image_size"); size.Exists() && size.Type == ojson.String {
			_ = outDoc.Set("request.generationConfig.imageConfig.imageSize", size.Str)
		}
	}

	// messages -> systemInstruction + contents
	messages := ojson.GetBytesNoCopy(rawJSON, "messages")
	if messages.IsArray() {
		arr := messages.Array()
		systemParts := make([][]byte, 0, 2)
		contentItems := make([][]byte, 0, len(arr))
		contentCanMergeWithToolResponse := make([]bool, 0, len(arr))
		appendContent := func(content []byte, canMerge bool) {
			contentItems = append(contentItems, content)
			contentCanMergeWithToolResponse = append(contentCanMergeWithToolResponse, canMerge)
		}

		hasEncounteredConversation := false
		for i := 0; i < len(arr); i++ {
			m := arr[i]
			role := m.Get("role").String()
			content := m.Get("content")

			if (role == "system" || role == "developer") && len(arr) > 1 && !hasEncounteredConversation {
				// system -> request.systemInstruction as a user message style
				if content.Type == ojson.String {
					systemParts = append(systemParts, antigravityOpenAITextPart(content.String()))
				} else if content.IsObject() && content.Get("type").String() == "text" {
					systemParts = append(systemParts, antigravityOpenAITextPart(content.Get("text").String()))
				} else if content.IsArray() {
					for _, contentPart := range content.Array() {
						systemParts = append(systemParts, antigravityOpenAITextPart(contentPart.Get("text").String()))
					}
				}
			} else if role == "user" || role == "system" || role == "developer" {
				hasEncounteredConversation = true
				isDemotedSystem := role == "system" || role == "developer"
				partItems := make([][]byte, 0, 4)
				if content.Type == ojson.String {
					partItems = append(partItems, antigravityOpenAITextPart(antigravityDemotedSystemText(content.String(), isDemotedSystem)))
				} else if content.IsObject() && content.Get("type").String() == "text" {
					partItems = append(partItems, antigravityOpenAITextPart(antigravityDemotedSystemText(content.Get("text").String(), isDemotedSystem)))
				} else if content.IsArray() {
					for _, item := range content.Array() {
						switch item.Get("type").String() {
						case "text":
							if text := item.Get("text").String(); text != "" {
								partItems = append(partItems, antigravityOpenAITextPart(antigravityDemotedSystemText(text, isDemotedSystem)))
							}
						case "image_url":
							// Only a base64 data URL can be inlined; a remote URL has no equivalent here.
							if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData("", "", item.Get("image_url.url").String()); ok {
								partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, data))
							} else {
								drops.Drop("image_url")
							}
						case "video_url":
							if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData("", "", item.Get("video_url.url").String()); ok {
								partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, data))
							} else {
								drops.Drop("video_url")
							}
						case "file":
							filename := item.Get("file.filename").String()
							fileData := item.Get("file.file_data").String()
							if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData(filename, "", fileData); ok {
								partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, data))
							} else {
								log.Warn("Invalid file data or unknown file name extension in user message, skip")
								drops.Drop("file")
							}
						case "input_audio":
							audioData := item.Get("input_audio.data").String()
							if audioData != "" {
								mimeType := antigravityOpenAIAudioMIMEType(item.Get("input_audio.format").String())
								partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, audioData))
							} else {
								drops.Drop("input_audio")
							}
						}
					}
				}
				// Whitespace-only text is forwarded but never keeps an emptied turn alive.
				drops.EndTurn(translatorcommon.CountSendableGeminiParts(partItems))
				if len(partItems) > 0 {
					appendContent(antigravityOpenAIContent("user", partItems), role == "user")
				}
			} else if role == "assistant" {
				hasEncounteredConversation = true
				partItems := make([][]byte, 0, 4)
				if reasoningContent := m.Get("reasoning_content"); reasoningContent.Type == ojson.String && reasoningContent.String() != "" {
					part := antigravityOpenAITextPart(reasoningContent.String())
					part, _ = ojson.SetBytes(part, "thought", true)
					partItems = append(partItems, part)
				}
				if content.Type == ojson.String && content.String() != "" {
					partItems = append(partItems, antigravityOpenAITextPart(content.String()))
				} else if content.IsArray() {
					for _, item := range content.Array() {
						switch item.Get("type").String() {
						case "text":
							if text := item.Get("text").String(); text != "" {
								partItems = append(partItems, antigravityOpenAITextPart(text))
							}
						case "image_url":
							if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData("", "", item.Get("image_url.url").String()); ok {
								partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, data))
							}
						}
					}
				}

				tcs := m.Get("tool_calls")
				if tcs.IsArray() {
					type assistantToolCall struct {
						rawID string
						id    string
						name  string
					}
					toolCalls := make([]assistantToolCall, 0)
					usedToolCallIDs := make(map[string]struct{})
					for _, tc := range tcs.Array() {
						if tc.Get("type").String() != "function" {
							continue
						}
						rawID := tc.Get("id").String()
						baseID := util.SanitizeClaudeToolID(rawID)
						functionID := baseID
						suffix := 1
						for {
							if _, exists := usedToolCallIDs[functionID]; !exists {
								usedToolCallIDs[functionID] = struct{}{}
								break
							}
							functionID = fmt.Sprintf("%s_%d", baseID, suffix)
							suffix++
						}
						functionName := util.MapSanitizedFunctionName(functionNameMap, tc.Get("function.name").String())
						if functionName == "" {
							continue
						}
						functionArgs := tc.Get("function.arguments").String()
						trimmedArgs := strings.TrimSpace(functionArgs)
						partDoc := ojson.NewDocString(`{"functionCall":{"id":"","name":"","args":{}}}`)
						_ = partDoc.Set("functionCall.id", functionID)
						_ = partDoc.Set("functionCall.name", functionName)
						if trimmedArgs != "" && trimmedArgs != "null" {
							if ojson.Valid(trimmedArgs) && ojson.Parse(trimmedArgs).IsObject() {
								_ = partDoc.SetRaw("functionCall.args", trimmedArgs)
							} else {
								_ = partDoc.Set("functionCall.args.params", functionArgs)
							}
						}
						_ = partDoc.Set("thoughtSignature", antigravityFunctionThoughtSignature)
						partItems = append(partItems, partDoc.Bytes())
						toolCalls = append(toolCalls, assistantToolCall{
							rawID: rawID,
							id:    functionID,
							name:  functionName,
						})
					}
					if len(partItems) > 0 {
						appendContent(antigravityOpenAIContent("model", partItems), false)
					}

					// Collect tool responses scoped to this assistant turn.
					turnToolResponses := map[string]string{}
					for j := i + 1; j < len(arr); j++ {
						nextRole := arr[j].Get("role").String()
						if nextRole == "assistant" {
							break
						}
						if nextRole == "tool" {
							callID := arr[j].Get("tool_call_id").String()
							if callID != "" {
								turnToolResponses[callID] = arr[j].Get("content").String()
							}
						}
					}

					responseParts := make([][]byte, 0, len(toolCalls))
					for _, call := range toolCalls {
						partDoc := ojson.NewDoc([]byte(`{"functionResponse":{"id":"","name":""}}`))
						_ = partDoc.Set("functionResponse.id", call.id)
						_ = partDoc.Set("functionResponse.name", call.name)
						response := turnToolResponses[call.rawID]
						if response == "" {
							response = "{}"
						}
						// Keep it as a string instead of parsing it into JSON.
						// Parsing it as JSON, similar to reading a JSON file with readFile, may trigger an upstream 400 error.
						_ = partDoc.Set("functionResponse.response.result", response)
						responseParts = append(responseParts, partDoc.Bytes())
					}
					if len(responseParts) > 0 {
						appendContent(antigravityOpenAIContent("user", responseParts), false)
					}
				} else if len(partItems) > 0 {
					appendContent(antigravityOpenAIContent("model", partItems), false)
				}
			}
		}
		if len(systemParts) > 0 {
			_ = outDoc.SetRawBytes("request.systemInstruction", antigravityOpenAIContent("user", systemParts))
		}
		contentItems = mergeAntigravityOpenAIToolResponseUserContents(contentItems, contentCanMergeWithToolResponse)
		translatorcommon.SetRawArrayItemsDoc(outDoc, "request.contents", contentItems)
	}

	// tools -> request.tools[].functionDeclarations + request.tools[].googleSearch/codeExecution/urlContext passthrough
	allowed := parseAntigravityOpenAIAllowedTools(rawJSON)
	var declaredAllowedNames []string
	seenDeclaredAllowed := make(map[string]struct{})
	hasAllowedNativeTools := false
	tools := ojson.GetBytesNoCopy(rawJSON, "tools")
	toolResults := tools.Array()
	if tools.IsArray() && len(toolResults) > 0 {
		functionDeclarations := make([][]byte, 0, len(toolResults))
		codeExecutionNodes := make([][]byte, 0)
		urlContextNodes := make([][]byte, 0)
		for _, t := range toolResults {
			if t.Get("type").String() == "function" {
				fn := t.Get("function")
				if fn.Exists() && fn.IsObject() {
					nameResult := fn.Get("name")
					originalName := nameResult.String()
					if allowed.enabled {
						if _, ok := allowed.functionNames[originalName]; !ok {
							continue
						}
					}
					fnRaw := fn.Raw
					needDefaultSchema := false
					if fn.Get("parameters").Exists() {
						renamed, errRename := util.RenameKey(fnRaw, "parameters", "parametersJsonSchema")
						if errRename != nil {
							log.Warnf("Failed to rename parameters for tool '%s': %v", originalName, errRename)
							needDefaultSchema = true
						} else {
							fnRaw = renamed
						}
					} else {
						needDefaultSchema = true
					}
					fnDoc := ojson.NewDocString(fnRaw)
					if needDefaultSchema {
						if errSet := fnDoc.Set("parametersJsonSchema.type", "object"); errSet != nil {
							log.Warnf("Failed to set default schema type for tool '%s': %v", originalName, errSet)
							continue
						}
						if errSet := fnDoc.SetRaw("parametersJsonSchema.properties", `{}`); errSet != nil {
							log.Warnf("Failed to set default schema properties for tool '%s': %v", originalName, errSet)
							continue
						}
					}
					mappedName := util.MapSanitizedFunctionName(functionNameMap, originalName)
					if nameResult.Type != ojson.String || mappedName != originalName {
						_ = fnDoc.Set("name", mappedName)
					}
					if fn.Get("strict").Exists() {
						_ = fnDoc.Delete("strict")
					}
					functionDeclarations = append(functionDeclarations, fnDoc.Bytes())
					if allowed.enabled {
						if _, exists := seenDeclaredAllowed[mappedName]; !exists {
							seenDeclaredAllowed[mappedName] = struct{}{}
							declaredAllowedNames = append(declaredAllowedNames, mappedName)
						}
					}
				}
			}
			if ce := t.Get("code_execution"); ce.Exists() {
				_, allowedCodeExec := allowed.nativeTools["code_execution"]
				if !allowed.enabled || allowedCodeExec {
					codeToolNode := []byte(`{}`)
					var errSet error
					codeToolNode, errSet = ojson.SetRawBytes(codeToolNode, "codeExecution", []byte(ce.Raw))
					if errSet != nil {
						log.Warnf("Failed to set codeExecution tool: %v", errSet)
						continue
					}
					codeExecutionNodes = append(codeExecutionNodes, codeToolNode)
				}
			}
			if uc := t.Get("url_context"); uc.Exists() {
				_, allowedURLContext := allowed.nativeTools["url_context"]
				if !allowed.enabled || allowedURLContext {
					urlToolNode := []byte(`{}`)
					var errSet error
					urlToolNode, errSet = ojson.SetRawBytes(urlToolNode, "urlContext", []byte(uc.Raw))
					if errSet != nil {
						log.Warnf("Failed to set urlContext tool: %v", errSet)
						continue
					}
					urlContextNodes = append(urlContextNodes, urlToolNode)
				}
			}
		}
		deduplicated := util.DeduplicateFunctionDeclarations(translatorcommon.JoinRawArray(functionDeclarations))
		hasFunction := len(deduplicated) > 2
		hasAllowedNativeTools = len(codeExecutionNodes) > 0 || len(urlContextNodes) > 0
		toolItems := make([][]byte, 0, 1+len(codeExecutionNodes)+len(urlContextNodes))
		if hasFunction {
			functionToolNode := []byte(`{"functionDeclarations":[]}`)
			functionToolNode, _ = ojson.SetRawBytes(functionToolNode, "functionDeclarations", deduplicated)
			toolItems = append(toolItems, functionToolNode)
		}
		toolItems = append(toolItems, codeExecutionNodes...)
		toolItems = append(toolItems, urlContextNodes...)
		if len(toolItems) > 0 {
			_ = outDoc.SetRawBytes("request.tools", translatorcommon.JoinRawArray(toolItems))
		}
	}

	applyOpenAIToolChoiceToAntigravityDoc(outDoc, rawJSON, functionNameMap, allowed, declaredAllowedNames, hasAllowedNativeTools)
	common.AttachDefaultSafetySettingsDoc(outDoc, "request.safetySettings")
	summaryCfg := thinking.ExtractSummaryConfig(rawJSON, "openai")
	out := thinking.ApplySummaryConfig(outDoc.Bytes(), "antigravity", summaryCfg)
	if strings.Contains(strings.ToLower(modelName), "claude") {
		out = gemini.SanitizeAntigravityClaudeGeminiRequestSignatures(modelName, out)
	}
	finalDoc := ojson.NewDoc(out)
	gemini.NormalizeAntigravityRequestDoc(finalDoc)
	return finalDoc.Bytes(), drops.Err()
}

func antigravityOpenAITextPart(text string) []byte {
	part := []byte(`{"text":""}`)
	part, _ = ojson.SetBytes(part, "text", text)
	return part
}

func antigravityOpenAIInlineDataPart(mimeType, data string) []byte {
	partDoc := ojson.NewDoc([]byte(`{"inlineData":{"mimeType":"","data":""}}`))
	_ = partDoc.Set("inlineData.mimeType", mimeType)
	_ = partDoc.Set("inlineData.data", data)
	return partDoc.Bytes()
}

func mergeAntigravityOpenAIToolResponseUserContents(contents [][]byte, canMerge []bool) [][]byte {
	if len(contents) <= 1 {
		return contents
	}
	merged := make([][]byte, 0, len(contents))
	for i := 0; i < len(contents); i++ {
		content := contents[i]
		if ojson.GetBytesNoCopy(content, "role").String() != "user" ||
			!translatorcommon.ContentHasGeminiFunctionResponse(content) {
			merged = append(merged, content)
			continue
		}

		j := i + 1
		partCount := len(ojson.GetBytesNoCopy(content, "parts").Array())
		for j < len(contents) && j < len(canMerge) && canMerge[j] {
			partCount += len(ojson.GetBytesNoCopy(contents[j], "parts").Array())
			j++
		}
		if j == i+1 {
			merged = append(merged, content)
			continue
		}

		parts := make([][]byte, 0, partCount)
		for _, current := range contents[i:j] {
			for _, part := range ojson.GetBytesNoCopy(current, "parts").Array() {
				parts = append(parts, []byte(part.Raw))
			}
		}
		contentDoc := ojson.NewDoc(content)
		if errSet := contentDoc.SetRawBytes("parts", translatorcommon.JoinRawArray(parts)); errSet != nil {
			merged = append(merged, contents[i:j]...)
			i = j - 1
			continue
		}
		merged = append(merged, contentDoc.Bytes())
		i = j - 1
	}
	return merged
}

func antigravityOpenAIContent(role string, parts [][]byte) []byte {
	contentDoc := ojson.NewDoc([]byte(`{"role":"","parts":[]}`))
	_ = contentDoc.Set("role", role)
	_ = contentDoc.SetRawBytes("parts", translatorcommon.JoinRawArray(parts))
	return contentDoc.Bytes()
}

func antigravityOpenAIAudioMIMEType(format string) string {
	switch format {
	case "mp3":
		return "audio/mpeg"
	case "ogg":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "aac":
		return "audio/aac"
	case "webm":
		return "audio/webm"
	case "pcm16":
		return "audio/pcm"
	case "g711_ulaw", "g711_alaw":
		return "audio/basic"
	case "", "wav":
		return "audio/wav"
	default:
		return "audio/" + format
	}
}

type antigravityOpenAIAllowedTools struct {
	enabled       bool
	mode          string
	functionNames map[string]struct{}
	nativeTools   map[string]struct{}
}

func parseAntigravityOpenAIAllowedTools(rawJSON []byte) antigravityOpenAIAllowedTools {
	res := antigravityOpenAIAllowedTools{
		mode: "auto",
	}
	toolChoice := ojson.GetBytesNoCopy(rawJSON, "tool_choice")
	if toolChoice.Exists() && toolChoice.IsObject() && strings.EqualFold(strings.TrimSpace(toolChoice.Get("type").String()), "allowed_tools") {
		res.enabled = true
		res.functionNames = make(map[string]struct{})
		res.nativeTools = make(map[string]struct{})
		toolList := toolChoice.Get("allowed_tools.tools").Array()
		if len(toolList) == 0 {
			toolList = toolChoice.Get("tools").Array()
		}
		for _, t := range toolList {
			toolType := strings.ToLower(strings.TrimSpace(t.Get("type").String()))
			if toolType == "google_search" || t.Get("google_search").Exists() {
				res.nativeTools["google_search"] = struct{}{}
			}
			if toolType == "code_execution" || t.Get("code_execution").Exists() {
				res.nativeTools["code_execution"] = struct{}{}
			}
			if toolType == "url_context" || t.Get("url_context").Exists() {
				res.nativeTools["url_context"] = struct{}{}
			}
			if toolType != "google_search" && toolType != "code_execution" && toolType != "url_context" {
				fnName := strings.TrimSpace(t.Get("function.name").String())
				if fnName == "" {
					fnName = strings.TrimSpace(t.Get("name").String())
				}
				if fnName != "" {
					res.functionNames[fnName] = struct{}{}
				}
			}
		}
		modeVal := strings.ToLower(strings.TrimSpace(toolChoice.Get("allowed_tools.mode").String()))
		if modeVal == "" {
			modeVal = strings.ToLower(strings.TrimSpace(toolChoice.Get("mode").String()))
		}
		if modeVal != "" {
			res.mode = modeVal
		}
	}
	return res
}

func applyOpenAIToolChoiceToAntigravityDoc(outDoc *ojson.Doc, rawJSON []byte, functionNameMap map[string]string, allowed antigravityOpenAIAllowedTools, declaredAllowedNames []string, hasAllowedNativeTools bool) {
	if allowed.enabled {
		switch allowed.mode {
		case "none":
			_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", "NONE")
		case "required", "any":
			if len(declaredAllowedNames) > 0 {
				_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", "ANY")
				_ = outDoc.Set("request.toolConfig.functionCallingConfig.allowedFunctionNames", declaredAllowedNames)
			} else if !hasAllowedNativeTools {
				_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", "NONE")
			}
		case "auto":
			if len(declaredAllowedNames) > 0 {
				// Mode AUTO: functionDeclarations already contains only allowed tools.
				_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", "AUTO")
			} else if !hasAllowedNativeTools {
				_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", "NONE")
			}
		default:
			_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", "NONE")
		}
		return
	}

	toolChoice := ojson.GetBytesNoCopy(rawJSON, "tool_choice")
	if !toolChoice.Exists() || toolChoice.Type == ojson.Null {
		return
	}

	mode := ""
	allowedName := ""
	if toolChoice.Type == ojson.String {
		switch strings.ToLower(strings.TrimSpace(toolChoice.String())) {
		case "none":
			mode = "NONE"
		case "auto":
			mode = "AUTO"
		case "required", "any":
			mode = "ANY"
		}
	} else if toolChoice.IsObject() {
		switch strings.ToLower(strings.TrimSpace(toolChoice.Get("type").String())) {
		case "none":
			mode = "NONE"
		case "auto":
			mode = "AUTO"
		case "required", "any":
			mode = "ANY"
		case "function", "tool":
			allowedName = strings.TrimSpace(toolChoice.Get("function.name").String())
			if allowedName == "" {
				allowedName = strings.TrimSpace(toolChoice.Get("name").String())
			}
			if allowedName == "" {
				mode = "NONE"
			} else {
				mode = "ANY"
			}
		}
	}
	if mode == "" {
		return
	}

	_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", mode)
	if allowedName != "" {
		mappedName := util.MapSanitizedFunctionName(functionNameMap, allowedName)
		_ = outDoc.Set("request.toolConfig.functionCallingConfig.allowedFunctionNames", []string{mappedName})
	}
}

func normalizeAntigravityOpenAIThinkingConfig(out []byte) []byte {
	outDoc := ojson.NewDoc(out)
	if normalizeAntigravityOpenAIThinkingConfigDoc(outDoc) {
		return outDoc.Bytes()
	}
	return out
}

func normalizeAntigravityOpenAIThinkingConfigDoc(outDoc *ojson.Doc) bool {
	changed := false
	for _, prefix := range []string{
		"request.generationConfig.thinking_config",
		"request.generationConfig.thinkingConfig",
	} {
		if sourcePath := prefix + ".includeThoughts"; outDoc.Get(sourcePath).Exists() {
			includeThoughts := outDoc.Get(sourcePath)
			if setAntigravityOpenAIBoolResultIfValidDoc(outDoc, "request.generationConfig.thinkingConfig.includeThoughts", includeThoughts) {
				changed = true
			}
			if includeThoughts.Type != ojson.True && includeThoughts.Type != ojson.False {
				_ = outDoc.Delete(sourcePath)
				changed = true
			}
		}
		if sourcePath := prefix + ".include_thoughts"; outDoc.Get(sourcePath).Exists() {
			includeThoughts := outDoc.Get(sourcePath)
			if setAntigravityOpenAIBoolResultIfValidDoc(outDoc, "request.generationConfig.thinkingConfig.includeThoughts", includeThoughts) {
				changed = true
			}
			if includeThoughts.Type != ojson.True && includeThoughts.Type != ojson.False {
				_ = outDoc.Delete(sourcePath)
				changed = true
			}
		}
		if thinkingLevel := outDoc.Get(prefix + ".thinkingLevel"); thinkingLevel.Exists() {
			if setAntigravityOpenAIRawIfDifferentDoc(outDoc, "request.generationConfig.thinkingConfig.thinkingLevel", thinkingLevel) {
				changed = true
			}
		}
		if thinkingLevel := outDoc.Get(prefix + ".thinking_level"); thinkingLevel.Exists() {
			if setAntigravityOpenAIRawIfDifferentDoc(outDoc, "request.generationConfig.thinkingConfig.thinkingLevel", thinkingLevel) {
				changed = true
			}
		}
		if thinkingBudget := outDoc.Get(prefix + ".thinkingBudget"); thinkingBudget.Exists() {
			if setAntigravityOpenAIRawIfDifferentDoc(outDoc, "request.generationConfig.thinkingConfig.thinkingBudget", thinkingBudget) {
				changed = true
			}
		}
		if thinkingBudget := outDoc.Get(prefix + ".thinking_budget"); thinkingBudget.Exists() {
			if setAntigravityOpenAIRawIfDifferentDoc(outDoc, "request.generationConfig.thinkingConfig.thinkingBudget", thinkingBudget) {
				changed = true
			}
		}
	}

	for _, path := range []string{
		"request.generationConfig.includeThoughts",
		"request.generationConfig.include_thoughts",
	} {
		if includeThoughts := outDoc.Get(path); includeThoughts.Exists() {
			if setAntigravityOpenAIBoolResultIfValidDoc(outDoc, "request.generationConfig.thinkingConfig.includeThoughts", includeThoughts) {
				changed = true
			}
		}
	}

	for _, path := range []string{
		"request.generationConfig.thinking_config",
		"request.generationConfig.thinkingConfig.include_thoughts",
		"request.generationConfig.thinkingConfig.thinking_level",
		"request.generationConfig.thinkingConfig.thinking_budget",
		"request.generationConfig.includeThoughts",
		"request.generationConfig.include_thoughts",
	} {
		if outDoc.Get(path).Exists() {
			_ = outDoc.Delete(path)
			changed = true
		}
	}

	return changed
}

func setAntigravityOpenAIBoolResultIfValidDoc(outDoc *ojson.Doc, path string, value ojson.Result) bool {
	switch value.Type {
	case ojson.True:
		return setAntigravityOpenAIBoolIfDifferentDoc(outDoc, path, true)
	case ojson.False:
		return setAntigravityOpenAIBoolIfDifferentDoc(outDoc, path, false)
	default:
		return false
	}
}

func setAntigravityOpenAIBoolIfDifferentDoc(outDoc *ojson.Doc, path string, value bool) bool {
	current := outDoc.Get(path)
	if value && current.Type == ojson.True || !value && current.Type == ojson.False {
		return false
	}
	return outDoc.Set(path, value) == nil
}

func setAntigravityOpenAIRawIfDifferentDoc(outDoc *ojson.Doc, path string, value ojson.Result) bool {
	current := outDoc.Get(path)
	if current.Exists() && current.Raw == value.Raw {
		return false
	}
	return outDoc.SetRaw(path, value.Raw) == nil
}

// antigravityDemotedSystemText wraps a demoted mid-session system or developer
// message in the <system-reminder> envelope so non-Claude upstream models treat it
// as a directive rather than user speech.
func antigravityDemotedSystemText(text string, isDemoted bool) string {
	if !isDemoted || strings.TrimSpace(text) == "" {
		return text
	}
	return translatorcommon.SystemReminderText(text)
}
