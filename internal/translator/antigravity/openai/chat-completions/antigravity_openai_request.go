// Package openai provides request translation functionality for OpenAI to Antigravity API compatibility.
// It converts OpenAI Chat Completions requests into Antigravity compatible JSON using gjson/sjson only.
package chat_completions

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/gemini"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

const antigravityFunctionThoughtSignature = "skip_thought_signature_validator"

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
	return convertOpenAIRequestToAntigravity(modelName, inputRawJSON, stream)

}

// convertOpenAIRequestToAntigravity also reports a file part Antigravity cannot
// receive when it leaves a user turn with nothing to send.
func convertOpenAIRequestToAntigravity(modelName string, inputRawJSON []byte, _ bool) ([]byte, error) {
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
								partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, data, false))
							} else {
								drops.Drop("image_url")
							}
						case "video_url":
							if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData("", "", item.Get("video_url.url").String()); ok {
								partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, data, false))
							} else {
								drops.Drop("video_url")
							}
						case "file":
							filename := item.Get("file.filename").String()
							fileData := item.Get("file.file_data").String()
							if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData(filename, "", fileData); ok {
								partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, data, false))
							} else {
								log.Warn("Invalid file data or unknown file name extension in user message, skip")
								drops.Drop("file")
							}
						case "input_audio":
							audioData := item.Get("input_audio.data").String()
							if audioData != "" {
								mimeType := antigravityOpenAIAudioMIMEType(item.Get("input_audio.format").String())
								partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, audioData, true))
							} else {
								drops.Drop("input_audio")
							}
						}
					}
				}
				// Whitespace-only text is forwarded but never keeps an emptied turn alive.
				drops.EndTurn(translatorcommon.CountSendableGeminiParts(partItems))
				if len(partItems) > 0 {
					contentItems = append(contentItems, antigravityOpenAIContent("user", partItems))
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
							imageURL := item.Get("image_url.url").String()
							if len(imageURL) > 5 {
								pieces := strings.SplitN(imageURL[5:], ";", 2)
								if len(pieces) == 2 && len(pieces[1]) > 7 {
									part := antigravityOpenAIInlineDataPart(pieces[0], pieces[1][7:], false)
									partItems = append(partItems, part)
								}
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
						partDoc := ojson.NewDoc([]byte(`{"functionCall":{"id":"","name":""}}`))
						_ = partDoc.Set("functionCall.id", functionID)
						_ = partDoc.Set("functionCall.name", functionName)
						if ojson.Valid(functionArgs) {
							_ = partDoc.SetRaw("functionCall.args", functionArgs)
						} else {
							_ = partDoc.Set("functionCall.args.params", []byte(functionArgs))
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
						contentItems = append(contentItems, antigravityOpenAIContent("model", partItems))
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
						contentItems = append(contentItems, antigravityOpenAIContent("user", responseParts))
					}
				} else if len(partItems) > 0 {
					contentItems = append(contentItems, antigravityOpenAIContent("model", partItems))
				}
			}
		}
		if len(systemParts) > 0 {
			_ = outDoc.SetRawBytes("request.systemInstruction", antigravityOpenAIContent("user", systemParts))
		}
		translatorcommon.SetRawArrayItemsDoc(outDoc, "request.contents", contentItems)
	}

	// tools -> request.tools[].functionDeclarations + request.tools[].googleSearch/codeExecution/urlContext passthrough
	tools := ojson.GetBytesNoCopy(rawJSON, "tools")
	toolResults := tools.Array()
	if tools.IsArray() && len(toolResults) > 0 {
		functionDeclarations := make([][]byte, 0, len(toolResults))
		googleSearchNodes := make([][]byte, 0)
		codeExecutionNodes := make([][]byte, 0)
		urlContextNodes := make([][]byte, 0)
		for _, t := range toolResults {
			if t.Get("type").String() == "function" {
				fn := t.Get("function")
				if fn.Exists() && fn.IsObject() {
					fnRaw := fn.Raw
					needDefaultSchema := false
					if fn.Get("parameters").Exists() {
						renamed, errRename := util.RenameKey(fnRaw, "parameters", "parametersJsonSchema")
						if errRename != nil {
							log.Warnf("Failed to rename parameters for tool '%s': %v", fn.Get("name").String(), errRename)
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
							log.Warnf("Failed to set default schema type for tool '%s': %v", fn.Get("name").String(), errSet)
							continue
						}
						if errSet := fnDoc.SetRaw("parametersJsonSchema.properties", `{}`); errSet != nil {
							log.Warnf("Failed to set default schema properties for tool '%s': %v", fn.Get("name").String(), errSet)
							continue
						}
					}
					nameResult := fn.Get("name")
					originalName := nameResult.String()
					mappedName := util.MapSanitizedFunctionName(functionNameMap, originalName)
					if nameResult.Type != ojson.String || mappedName != originalName {
						_ = fnDoc.Set("name", mappedName)
					}
					if fn.Get("strict").Exists() {
						_ = fnDoc.Delete("strict")
					}
					functionDeclarations = append(functionDeclarations, fnDoc.Bytes())
				}
			}
			if gs := t.Get("google_search"); gs.Exists() {
				googleToolNode := []byte(`{}`)
				var errSet error
				googleToolNode, errSet = ojson.SetRawBytes(googleToolNode, "googleSearch", []byte(gs.Raw))
				if errSet != nil {
					log.Warnf("Failed to set googleSearch tool: %v", errSet)
					continue
				}
				googleSearchNodes = append(googleSearchNodes, googleToolNode)
			}
			if ce := t.Get("code_execution"); ce.Exists() {
				codeToolNode := []byte(`{}`)
				var errSet error
				codeToolNode, errSet = ojson.SetRawBytes(codeToolNode, "codeExecution", []byte(ce.Raw))
				if errSet != nil {
					log.Warnf("Failed to set codeExecution tool: %v", errSet)
					continue
				}
				codeExecutionNodes = append(codeExecutionNodes, codeToolNode)
			}
			if uc := t.Get("url_context"); uc.Exists() {
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
		deduplicated := util.DeduplicateFunctionDeclarations(translatorcommon.JoinRawArray(functionDeclarations))
		hasFunction := len(deduplicated) > 2
		if hasFunction || len(googleSearchNodes) > 0 || len(codeExecutionNodes) > 0 || len(urlContextNodes) > 0 {
			toolItems := make([][]byte, 0, 1+len(googleSearchNodes)+len(codeExecutionNodes)+len(urlContextNodes))
			if hasFunction {
				functionToolNode := []byte(`{"functionDeclarations":[]}`)
				functionToolNode, _ = ojson.SetRawBytes(functionToolNode, "functionDeclarations", deduplicated)
				toolItems = append(toolItems, functionToolNode)
			}
			toolItems = append(toolItems, googleSearchNodes...)
			toolItems = append(toolItems, codeExecutionNodes...)
			toolItems = append(toolItems, urlContextNodes...)
			_ = outDoc.SetRawBytes("request.tools", translatorcommon.JoinRawArray(toolItems))
		}
	}

	applyOpenAIToolChoiceToAntigravityDoc(outDoc, rawJSON, functionNameMap)
	common.AttachDefaultSafetySettingsDoc(outDoc, "request.safetySettings")
	summaryCfg := thinking.ExtractSummaryConfig(rawJSON, "openai")
	out := thinking.ApplySummaryConfig(outDoc.Bytes(), "antigravity", summaryCfg)
	if strings.Contains(strings.ToLower(modelName), "claude") {
		out = gemini.SanitizeAntigravityClaudeGeminiRequestSignatures(modelName, out)
	}
	return out, drops.Err()
}

func antigravityOpenAITextPart(text string) []byte {
	part := []byte(`{"text":""}`)
	part, _ = ojson.SetBytes(part, "text", text)
	return part
}

func antigravityOpenAIInlineDataPart(mimeType, data string, snakeCase bool) []byte {
	if snakeCase {
		partDoc := ojson.NewDoc([]byte(`{"inlineData":{"mime_type":"","data":""}}`))
		_ = partDoc.Set("inlineData.mime_type", mimeType)
		_ = partDoc.Set("inlineData.data", data)
		return partDoc.Bytes()
	}
	partDoc := ojson.NewDoc([]byte(`{"inlineData":{"mimeType":"","data":""}}`))
	_ = partDoc.Set("inlineData.mimeType", mimeType)
	_ = partDoc.Set("inlineData.data", data)
	return partDoc.Bytes()
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

func applyOpenAIToolChoiceToAntigravityDoc(outDoc *ojson.Doc, rawJSON []byte, functionNameMap map[string]string) {
	toolChoice := ojson.GetBytesNoCopy(rawJSON, "tool_choice")
	if !toolChoice.Exists() {
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
		case "function":
			mode = "ANY"
			allowedName = toolChoice.Get("function.name").String()
		}
	}
	if mode == "" {
		return
	}

	_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", mode)
	if strings.TrimSpace(allowedName) != "" {
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
