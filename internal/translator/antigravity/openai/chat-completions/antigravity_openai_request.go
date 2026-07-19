// Package openai provides request translation functionality for OpenAI to Antigravity API compatibility.
// It converts OpenAI Chat Completions requests into Antigravity compatible JSON using gjson/sjson only.
package chat_completions

import (
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const antigravityFunctionThoughtSignature = "skip_thought_signature_validator"

// ConvertOpenAIRequestToAntigravity converts an OpenAI Chat Completions request (raw JSON)
// into a complete Antigravity request JSON. All JSON construction uses sjson and lookups use gjson.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Antigravity API format
func ConvertOpenAIRequestToAntigravity(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	functionNameMap := util.SanitizedFunctionNameMap(rawJSON)
	// Base envelope (no default thinkingConfig)
	out := []byte(`{"project":"","request":{"contents":[]},"model":"gemini-2.5-pro"}`)

	// Model
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Let user-provided generationConfig pass through
	if genConfig := gjson.GetBytes(rawJSON, "generationConfig"); genConfig.Exists() {
		out, _ = sjson.SetRawBytes(out, "request.generationConfig", []byte(genConfig.Raw))
	} else if genConfig := gjson.GetBytes(rawJSON, "generation_config"); genConfig.Exists() {
		out, _ = sjson.SetRawBytes(out, "request.generationConfig", []byte(genConfig.Raw))
	}

	// Apply thinking configuration: convert OpenAI reasoning_effort to Antigravity thinkingConfig.
	// Inline translation-only mapping; capability checks happen later in ApplyThinking.
	re := gjson.GetBytes(rawJSON, "reasoning_effort")
	if re.Exists() {
		effort := strings.ToLower(strings.TrimSpace(re.String()))
		if effort != "" {
			thinkingPath := "request.generationConfig.thinkingConfig"
			if effort == "auto" {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingBudget", -1)
				out, _ = sjson.SetBytes(out, thinkingPath+".includeThoughts", true)
			} else {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingLevel", effort)
				out, _ = sjson.SetBytes(out, thinkingPath+".includeThoughts", effort != "none")
			}
		}
	}
	out = applyOpenAIThinkingCompatibilityToAntigravity(out, rawJSON, modelName)

	// Temperature/top_p/top_k/max_tokens
	if tr := gjson.GetBytes(rawJSON, "temperature"); tr.Exists() && tr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.temperature", tr.Num)
	}
	if tpr := gjson.GetBytes(rawJSON, "top_p"); tpr.Exists() && tpr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.topP", tpr.Num)
	}
	if tkr := gjson.GetBytes(rawJSON, "top_k"); tkr.Exists() && tkr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.topK", tkr.Num)
	}
	if maxTok := gjson.GetBytes(rawJSON, "max_tokens"); maxTok.Exists() && maxTok.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.maxOutputTokens", maxTok.Num)
	}

	// Candidate count (OpenAI 'n' parameter)
	if n := gjson.GetBytes(rawJSON, "n"); n.Exists() && n.Type == gjson.Number {
		if val := n.Int(); val > 1 {
			out, _ = sjson.SetBytes(out, "request.generationConfig.candidateCount", val)
		}
	}

	// Map OpenAI modalities -> Antigravity request.generationConfig.responseModalities
	// e.g. "modalities": ["image", "text"] -> ["IMAGE", "TEXT"]
	if mods := gjson.GetBytes(rawJSON, "modalities"); mods.Exists() && mods.IsArray() {
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
			out, _ = sjson.SetBytes(out, "request.generationConfig.responseModalities", responseMods)
		}
	}

	// OpenRouter-style image_config support
	// If the input uses top-level image_config.aspect_ratio, map it into request.generationConfig.imageConfig.aspectRatio.
	if imgCfg := gjson.GetBytes(rawJSON, "image_config"); imgCfg.Exists() && imgCfg.IsObject() {
		if ar := imgCfg.Get("aspect_ratio"); ar.Exists() && ar.Type == gjson.String {
			out, _ = sjson.SetBytes(out, "request.generationConfig.imageConfig.aspectRatio", ar.Str)
		}
		if size := imgCfg.Get("image_size"); size.Exists() && size.Type == gjson.String {
			out, _ = sjson.SetBytes(out, "request.generationConfig.imageConfig.imageSize", size.Str)
		}
	}

	// messages -> systemInstruction + contents
	messages := gjson.GetBytes(rawJSON, "messages")
	if messages.IsArray() {
		arr := messages.Array()
		systemParts := make([][]byte, 0, 2)
		contentItems := make([][]byte, 0, len(arr))
		// First pass: assistant tool_calls id->name map
		tcID2Name := map[string]string{}
		for i := 0; i < len(arr); i++ {
			m := arr[i]
			if m.Get("role").String() == "assistant" {
				tcs := m.Get("tool_calls")
				if tcs.IsArray() {
					for _, tc := range tcs.Array() {
						if tc.Get("type").String() == "function" {
							id := tc.Get("id").String()
							name := tc.Get("function.name").String()
							if id != "" && name != "" {
								tcID2Name[id] = name
							}
						}
					}
				}
			}
		}

		// Second pass build systemInstruction/tool responses cache
		toolResponses := map[string]string{} // tool_call_id -> response text
		for i := 0; i < len(arr); i++ {
			m := arr[i]
			role := m.Get("role").String()
			if role == "tool" {
				toolCallID := m.Get("tool_call_id").String()
				if toolCallID != "" {
					c := m.Get("content")
					toolResponses[toolCallID] = c.Raw
				}
			}
		}

		for i := 0; i < len(arr); i++ {
			m := arr[i]
			role := m.Get("role").String()
			content := m.Get("content")

			if (role == "system" || role == "developer") && len(arr) > 1 {
				// system -> request.systemInstruction as a user message style
				if content.Type == gjson.String {
					systemParts = append(systemParts, antigravityOpenAITextPart(content.String()))
				} else if content.IsObject() && content.Get("type").String() == "text" {
					systemParts = append(systemParts, antigravityOpenAITextPart(content.Get("text").String()))
				} else if content.IsArray() {
					for _, contentPart := range content.Array() {
						systemParts = append(systemParts, antigravityOpenAITextPart(contentPart.Get("text").String()))
					}
				}
			} else if role == "user" || ((role == "system" || role == "developer") && len(arr) == 1) {
				partItems := make([][]byte, 0, 4)
				if content.Type == gjson.String {
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
									part, _ = sjson.SetBytes(part, "thoughtSignature", antigravityFunctionThoughtSignature)
									partItems = append(partItems, part)
								}
							}
						case "file":
							filename := item.Get("file.filename").String()
							fileData := item.Get("file.file_data").String()
							if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData(filename, "", fileData); ok {
								partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, data, false))
							} else {
								log.Warn("Invalid file data or unknown file name extension in user message, skip")
							}
						case "input_audio":
							audioData := item.Get("input_audio.data").String()
							if audioData != "" {
								mimeType := antigravityOpenAIAudioMIMEType(item.Get("input_audio.format").String())
								partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, audioData, true))
							}
						}
					}
				}
				contentItems = append(contentItems, antigravityOpenAIContent("user", partItems))
			} else if role == "assistant" {
				partItems := make([][]byte, 0, 4)
				if reasoningContent := m.Get("reasoning_content"); reasoningContent.Type == gjson.String && reasoningContent.String() != "" {
					part := antigravityOpenAITextPart(reasoningContent.String())
					part, _ = sjson.SetBytes(part, "thought", true)
					part, _ = sjson.SetBytes(part, "thoughtSignature", antigravityFunctionThoughtSignature)
					partItems = append(partItems, part)
				}
				if content.Type == gjson.String && content.String() != "" {
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
									part, _ = sjson.SetBytes(part, "thoughtSignature", antigravityFunctionThoughtSignature)
									partItems = append(partItems, part)
								}
							}
						}
					}
				}

				tcs := m.Get("tool_calls")
				if tcs.IsArray() {
					functionIDs := make([]string, 0)
					for _, tc := range tcs.Array() {
						if tc.Get("type").String() != "function" {
							continue
						}
						functionID := tc.Get("id").String()
						functionName := util.MapSanitizedFunctionName(functionNameMap, tc.Get("function.name").String())
						if functionName == "" {
							continue
						}
						functionArgs := tc.Get("function.arguments").String()
						part := []byte(`{"functionCall":{"id":"","name":""}}`)
						part, _ = sjson.SetBytes(part, "functionCall.id", functionID)
						part, _ = sjson.SetBytes(part, "functionCall.name", functionName)
						if gjson.Valid(functionArgs) {
							part, _ = sjson.SetRawBytes(part, "functionCall.args", []byte(functionArgs))
						} else {
							part, _ = sjson.SetBytes(part, "functionCall.args.params", []byte(functionArgs))
						}
						part, _ = sjson.SetBytes(part, "thoughtSignature", antigravityFunctionThoughtSignature)
						partItems = append(partItems, part)
						if functionID != "" {
							functionIDs = append(functionIDs, functionID)
						}
					}
					if len(partItems) > 0 {
						contentItems = append(contentItems, antigravityOpenAIContent("model", partItems))
					}

					responseParts := make([][]byte, 0, len(functionIDs))
					for _, functionID := range functionIDs {
						if name, ok := tcID2Name[functionID]; ok {
							part := []byte(`{"functionResponse":{"id":"","name":""}}`)
							part, _ = sjson.SetBytes(part, "functionResponse.id", functionID)
							part, _ = sjson.SetBytes(part, "functionResponse.name", util.MapSanitizedFunctionName(functionNameMap, name))
							response := toolResponses[functionID]
							if response == "" {
								response = "{}"
							}
							if response != "null" {
								parsed := gjson.Parse(response)
								if parsed.Type == gjson.JSON {
									part, _ = sjson.SetRawBytes(part, "functionResponse.response.result", []byte(parsed.Raw))
								} else {
									part, _ = sjson.SetBytes(part, "functionResponse.response.result", response)
								}
							}
							responseParts = append(responseParts, part)
						}
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
			out, _ = sjson.SetRawBytes(out, "request.systemInstruction", antigravityOpenAIContent("user", systemParts))
		}
		out = translatorcommon.SetRawArrayItems(out, "request.contents", contentItems)
	}

	// tools -> request.tools[].functionDeclarations + request.tools[].googleSearch/codeExecution/urlContext passthrough
	tools := gjson.GetBytes(rawJSON, "tools")
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
					if fn.Get("parameters").Exists() {
						renamed, errRename := util.RenameKey(fnRaw, "parameters", "parametersJsonSchema")
						if errRename != nil {
							log.Warnf("Failed to rename parameters for tool '%s': %v", fn.Get("name").String(), errRename)
							var errSet error
							fnRawBytes, errSet := sjson.SetBytes([]byte(fnRaw), "parametersJsonSchema.type", "object")
							if errSet != nil {
								log.Warnf("Failed to set default schema type for tool '%s': %v", fn.Get("name").String(), errSet)
								continue
							}
							fnRaw = string(fnRawBytes)
							fnRawBytes, errSet = sjson.SetRawBytes([]byte(fnRaw), "parametersJsonSchema.properties", []byte(`{}`))
							if errSet != nil {
								log.Warnf("Failed to set default schema properties for tool '%s': %v", fn.Get("name").String(), errSet)
								continue
							}
							fnRaw = string(fnRawBytes)
						} else {
							fnRaw = renamed
						}
					} else {
						var errSet error
						fnRawBytes, errSet := sjson.SetBytes([]byte(fnRaw), "parametersJsonSchema.type", "object")
						if errSet != nil {
							log.Warnf("Failed to set default schema type for tool '%s': %v", fn.Get("name").String(), errSet)
							continue
						}
						fnRaw = string(fnRawBytes)
						fnRawBytes, errSet = sjson.SetRawBytes([]byte(fnRaw), "parametersJsonSchema.properties", []byte(`{}`))
						if errSet != nil {
							log.Warnf("Failed to set default schema properties for tool '%s': %v", fn.Get("name").String(), errSet)
							continue
						}
						fnRaw = string(fnRawBytes)
					}
					fnRawBytes := []byte(fnRaw)
					fnRawBytes, _ = sjson.SetBytes(fnRawBytes, "name", util.MapSanitizedFunctionName(functionNameMap, fn.Get("name").String()))
					fnRaw, _ = sjson.Delete(string(fnRawBytes), "strict")
					functionDeclarations = append(functionDeclarations, []byte(fnRaw))
				}
			}
			if gs := t.Get("google_search"); gs.Exists() {
				googleToolNode := []byte(`{}`)
				var errSet error
				googleToolNode, errSet = sjson.SetRawBytes(googleToolNode, "googleSearch", []byte(gs.Raw))
				if errSet != nil {
					log.Warnf("Failed to set googleSearch tool: %v", errSet)
					continue
				}
				googleSearchNodes = append(googleSearchNodes, googleToolNode)
			}
			if ce := t.Get("code_execution"); ce.Exists() {
				codeToolNode := []byte(`{}`)
				var errSet error
				codeToolNode, errSet = sjson.SetRawBytes(codeToolNode, "codeExecution", []byte(ce.Raw))
				if errSet != nil {
					log.Warnf("Failed to set codeExecution tool: %v", errSet)
					continue
				}
				codeExecutionNodes = append(codeExecutionNodes, codeToolNode)
			}
			if uc := t.Get("url_context"); uc.Exists() {
				urlToolNode := []byte(`{}`)
				var errSet error
				urlToolNode, errSet = sjson.SetRawBytes(urlToolNode, "urlContext", []byte(uc.Raw))
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
				functionToolNode, _ = sjson.SetRawBytes(functionToolNode, "functionDeclarations", deduplicated)
				toolItems = append(toolItems, functionToolNode)
			}
			toolItems = append(toolItems, googleSearchNodes...)
			toolItems = append(toolItems, codeExecutionNodes...)
			toolItems = append(toolItems, urlContextNodes...)
			out, _ = sjson.SetRawBytes(out, "request.tools", translatorcommon.JoinRawArray(toolItems))
		}
	}

	out = applyOpenAIToolChoiceToAntigravity(out, rawJSON, functionNameMap)
	return common.AttachDefaultSafetySettings(out, "request.safetySettings")
}

func antigravityOpenAITextPart(text string) []byte {
	part := []byte(`{"text":""}`)
	part, _ = sjson.SetBytes(part, "text", text)
	return part
}

func antigravityOpenAIInlineDataPart(mimeType, data string, snakeCase bool) []byte {
	part := []byte(`{"inlineData":{"mimeType":"","data":""}}`)
	if snakeCase {
		part = []byte(`{"inlineData":{"mime_type":"","data":""}}`)
		part, _ = sjson.SetBytes(part, "inlineData.mime_type", mimeType)
	} else {
		part, _ = sjson.SetBytes(part, "inlineData.mimeType", mimeType)
	}
	part, _ = sjson.SetBytes(part, "inlineData.data", data)
	return part
}

func antigravityOpenAIContent(role string, parts [][]byte) []byte {
	content := []byte(`{"role":"","parts":[]}`)
	content, _ = sjson.SetBytes(content, "role", role)
	content, _ = sjson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(parts))
	return content
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

func applyOpenAIToolChoiceToAntigravity(out, rawJSON []byte, functionNameMap map[string]string) []byte {
	toolChoice := gjson.GetBytes(rawJSON, "tool_choice")
	if !toolChoice.Exists() {
		return out
	}

	mode := ""
	allowedName := ""
	if toolChoice.Type == gjson.String {
		switch strings.ToLower(strings.TrimSpace(toolChoice.String())) {
		case "none":
			mode = "NONE"
		case "auto":
			mode = "AUTO"
		case "required", "any":
			mode = "ANY"
		}
	} else if toolChoice.IsObject() && strings.EqualFold(toolChoice.Get("type").String(), "function") {
		mode = "ANY"
		allowedName = toolChoice.Get("function.name").String()
	}
	if mode == "" {
		return out
	}

	out, _ = sjson.SetBytes(out, "request.toolConfig.functionCallingConfig.mode", mode)
	if strings.TrimSpace(allowedName) != "" {
		mappedName := util.MapSanitizedFunctionName(functionNameMap, allowedName)
		out, _ = sjson.SetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames", []string{mappedName})
	}
	return out
}

func applyOpenAIThinkingCompatibilityToAntigravity(out []byte, rawJSON []byte, modelName string) []byte {
	out = normalizeAntigravityOpenAIThinkingConfig(out)

	for _, path := range []string{
		"thinking.includeThoughts",
		"thinking.include_thoughts",
		"reasoning.includeThoughts",
		"reasoning.include_thoughts",
	} {
		if value := gjson.GetBytes(rawJSON, path); value.Exists() {
			out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", value.Bool())
		}
	}

	if exclude := gjson.GetBytes(rawJSON, "reasoning.exclude"); exclude.Exists() {
		out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", !exclude.Bool())
	}

	if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Exists() && antigravityOpenAIDefaultIncludeThoughts(modelName) {
		out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", true)
	}

	return normalizeAntigravityOpenAIThinkingConfig(out)
}

func normalizeAntigravityOpenAIThinkingConfig(out []byte) []byte {
	for _, prefix := range []string{
		"request.generationConfig.thinking_config",
		"request.generationConfig.thinkingConfig",
	} {
		if includeThoughts := gjson.GetBytes(out, prefix+".includeThoughts"); includeThoughts.Exists() {
			out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", includeThoughts.Bool())
		}
		if includeThoughts := gjson.GetBytes(out, prefix+".include_thoughts"); includeThoughts.Exists() {
			out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", includeThoughts.Bool())
		}
		if thinkingLevel := gjson.GetBytes(out, prefix+".thinkingLevel"); thinkingLevel.Exists() {
			out, _ = sjson.SetRawBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel", []byte(thinkingLevel.Raw))
		}
		if thinkingLevel := gjson.GetBytes(out, prefix+".thinking_level"); thinkingLevel.Exists() {
			out, _ = sjson.SetRawBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel", []byte(thinkingLevel.Raw))
		}
		if thinkingBudget := gjson.GetBytes(out, prefix+".thinkingBudget"); thinkingBudget.Exists() {
			out, _ = sjson.SetRawBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget", []byte(thinkingBudget.Raw))
		}
		if thinkingBudget := gjson.GetBytes(out, prefix+".thinking_budget"); thinkingBudget.Exists() {
			out, _ = sjson.SetRawBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget", []byte(thinkingBudget.Raw))
		}
	}

	for _, path := range []string{
		"request.generationConfig.includeThoughts",
		"request.generationConfig.include_thoughts",
	} {
		if includeThoughts := gjson.GetBytes(out, path); includeThoughts.Exists() {
			out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", includeThoughts.Bool())
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
		out, _ = sjson.DeleteBytes(out, path)
	}

	return out
}

func antigravityOpenAIDefaultIncludeThoughts(modelName string) bool {
	modelName = strings.ToLower(modelName)
	return strings.Contains(modelName, "gemini-3")
}
