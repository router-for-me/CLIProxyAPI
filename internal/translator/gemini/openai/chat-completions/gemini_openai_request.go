// Package openai provides request translation functionality for OpenAI to Gemini API compatibility.
// It converts OpenAI Chat Completions requests into Gemini compatible JSON using gjson/sjson only.
package chat_completions

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

const geminiFunctionThoughtSignature = "skip_thought_signature_validator"

// ConvertOpenAIRequestToGemini converts an OpenAI Chat Completions request (raw JSON)
// into a complete Gemini request JSON. All JSON construction uses sjson and lookups use ojson.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Gemini API format
func ConvertOpenAIRequestToGemini(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIRequestToGemini(modelName, inputRawJSON, stream)

}

// convertOpenAIRequestToGemini also reports a file part Gemini cannot receive
// when nothing else was left to send.
func convertOpenAIRequestToGemini(modelName string, inputRawJSON []byte, _ bool) ([]byte, error) {
	rawJSON := inputRawJSON
	var drops translatorcommon.UserTurnDrops
	// Base envelope (no default thinkingConfig)
	outDoc := ojson.NewDocString(`{"contents":[]}`)

	// Model
	_ = outDoc.Set("model", modelName)

	// Let user-provided generationConfig pass through
	if genConfig := ojson.GetBytesNoCopy(rawJSON, "generationConfig"); genConfig.Exists() {
		_ = outDoc.SetRaw("generationConfig", genConfig.Raw)
	}

	// Apply thinking configuration: convert OpenAI reasoning_effort to Gemini thinkingConfig.
	// Inline translation-only mapping; capability checks happen later in ApplyThinking.
	re := ojson.GetBytesNoCopy(rawJSON, "reasoning_effort")
	if re.Exists() {
		effort := strings.ToLower(strings.TrimSpace(re.String()))
		if effort != "" {
			thinkingPath := "generationConfig.thinkingConfig"
			if effort == "auto" {
				_ = outDoc.Set(thinkingPath+".thinkingBudget", -1)
			} else {
				_ = outDoc.Set(thinkingPath+".thinkingLevel", effort)
			}
		}
	}

	// Temperature/top_p/top_k
	if tr := ojson.GetBytesNoCopy(rawJSON, "temperature"); tr.Exists() && tr.Type == ojson.Number {
		_ = outDoc.Set("generationConfig.temperature", tr.Num)
	}
	if tpr := ojson.GetBytesNoCopy(rawJSON, "top_p"); tpr.Exists() && tpr.Type == ojson.Number {
		_ = outDoc.Set("generationConfig.topP", tpr.Num)
	}
	if tkr := ojson.GetBytesNoCopy(rawJSON, "top_k"); tkr.Exists() && tkr.Type == ojson.Number {
		_ = outDoc.Set("generationConfig.topK", tkr.Num)
	}

	// OpenAI max_tokens / max_completion_tokens -> Gemini generationConfig.maxOutputTokens
	if mt := ojson.GetBytesNoCopy(rawJSON, "max_tokens"); mt.Exists() && mt.Type == ojson.Number {
		_ = outDoc.Set("generationConfig.maxOutputTokens", mt.Num)
	} else if mct := ojson.GetBytesNoCopy(rawJSON, "max_completion_tokens"); mct.Exists() && mct.Type == ojson.Number {
		_ = outDoc.Set("generationConfig.maxOutputTokens", mct.Num)
	}

	// Candidate count (OpenAI 'n' parameter)
	if n := ojson.GetBytesNoCopy(rawJSON, "n"); n.Exists() && n.Type == ojson.Number {
		if val := n.Int(); val > 1 {
			_ = outDoc.Set("generationConfig.candidateCount", val)
		}
	}

	// Map OpenAI response_format to Gemini structured output settings.
	applyOpenAIResponseFormatToGeminiDoc(outDoc, rawJSON)

	// Map OpenAI modalities -> Gemini generationConfig.responseModalities
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
			_ = outDoc.Set("generationConfig.responseModalities", responseMods)
		}
	}

	// OpenRouter-style image_config support
	// If the input uses top-level image_config.aspect_ratio, map it into generationConfig.imageConfig.aspectRatio.
	if imgCfg := ojson.GetBytesNoCopy(rawJSON, "image_config"); imgCfg.Exists() && imgCfg.IsObject() {
		if ar := imgCfg.Get("aspect_ratio"); ar.Exists() && ar.Type == ojson.String {
			_ = outDoc.Set("generationConfig.imageConfig.aspectRatio", ar.Str)
		}
		if size := imgCfg.Get("image_size"); size.Exists() && size.Type == ojson.String {
			_ = outDoc.Set("generationConfig.imageConfig.imageSize", size.Str)
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
				// system -> systemInstruction as a user message style
				if content.Type == ojson.String {
					systemParts = append(systemParts, geminiTextPart(content.String()))
				} else if content.IsObject() && content.Get("type").String() == "text" {
					systemParts = append(systemParts, geminiTextPart(content.Get("text").String()))
				} else if content.IsArray() {
					contents := content.Array()
					for j := 0; j < len(contents); j++ {
						systemParts = append(systemParts, geminiTextPart(contents[j].Get("text").String()))
					}
				}
			} else if role == "user" || role == "system" || role == "developer" {
				hasEncounteredConversation = true
				isDemotedSystem := role == "system" || role == "developer"
				// Build single user content node to avoid splitting into multiple contents.
				partItems := make([][]byte, 0, 4)
				if content.Type == ojson.String {
					partItems = append(partItems, geminiTextPart(geminiDemotedSystemText(content.String(), isDemotedSystem)))
				} else if content.IsObject() && content.Get("type").String() == "text" {
					partItems = append(partItems, geminiTextPart(geminiDemotedSystemText(content.Get("text").String(), isDemotedSystem)))
				} else if content.IsArray() {
					for _, item := range content.Array() {
						switch item.Get("type").String() {
						case "text":
							if text := item.Get("text").String(); text != "" {
								partItems = append(partItems, geminiTextPart(geminiDemotedSystemText(text, isDemotedSystem)))
							}
						case "image_url":
							// Only a base64 data URL can be inlined; a remote URL has no equivalent here.
							if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData("", "", item.Get("image_url.url").String()); ok {
								partItems = append(partItems, geminiInlineDataPart(mimeType, data, ""))
							} else {
								drops.Drop("image_url")
							}
						case "video_url":
							if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData("", "", item.Get("video_url.url").String()); ok {
								partItems = append(partItems, geminiInlineDataPart(mimeType, data, ""))
							} else {
								drops.Drop("video_url")
							}
						case "file":
							filename := item.Get("file.filename").String()
							fileData := item.Get("file.file_data").String()
							if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData(filename, "", fileData); ok {
								partItems = append(partItems, geminiInlineDataPart(mimeType, data, ""))
							} else {
								log.Warn("Invalid file data or unknown file name extension in user message, skip")
								drops.Drop("file")
							}
						case "input_audio":
							audioData := item.Get("input_audio.data").String()
							if audioData != "" {
								mimeType := openAIInputAudioMimeType(item.Get("input_audio.format").String())
								partItems = append(partItems, geminiInlineDataPart(mimeType, audioData, ""))
							} else {
								drops.Drop("input_audio")
							}
						}
					}
				}
				// Whitespace-only text is forwarded but never keeps an emptied turn alive.
				drops.EndTurn(translatorcommon.CountSendableGeminiParts(partItems))
				if len(partItems) > 0 {
					contentItems = append(contentItems, geminiContentNode("user", partItems))
				}
			} else if role == "assistant" {
				hasEncounteredConversation = true
				partItems := make([][]byte, 0, 4)
				if reasoningContent := m.Get("reasoning_content"); reasoningContent.Type == ojson.String && reasoningContent.String() != "" {
					part := geminiTextPart(reasoningContent.String())
					part, _ = ojson.SetBytes(part, "thought", true)
					partItems = append(partItems, part)
				}
				if content.Type == ojson.String && content.String() != "" {
					partItems = append(partItems, geminiTextPart(content.String()))
				} else if content.IsArray() {
					// Assistant multimodal content (e.g. text + image) -> single model content with parts.
					for _, item := range content.Array() {
						switch item.Get("type").String() {
						case "text":
							if text := item.Get("text").String(); text != "" {
								partItems = append(partItems, geminiTextPart(text))
							}
						case "image_url":
							imageURL := item.Get("image_url.url").String()
							if len(imageURL) > 5 {
								pieces := strings.SplitN(imageURL[5:], ";", 2)
								if len(pieces) == 2 && len(pieces[1]) > 7 {
									partItems = append(partItems, geminiInlineDataPart(pieces[0], pieces[1][7:], ""))
								}
							}
						}
					}
				}

				// Tool calls -> single model content with functionCall parts.
				tcs := m.Get("tool_calls")
				if tcs.IsArray() {
					type assistantToolCall struct {
						id   string
						name string
					}
					toolCalls := make([]assistantToolCall, 0)
					for _, tc := range tcs.Array() {
						if tc.Get("type").String() != "function" {
							continue
						}
						functionID := tc.Get("id").String()
						functionName := util.SanitizeFunctionName(tc.Get("function.name").String())
						if functionName == "" {
							continue
						}
						partDoc := ojson.NewDocString(`{"functionCall":{"name":""}}`)
						_ = partDoc.Set("functionCall.name", functionName)
						_ = partDoc.SetRaw("functionCall.args", tc.Get("function.arguments").String())
						_ = partDoc.Set("thoughtSignature", openAIToolCallGeminiThoughtSignature(tc))
						partItems = append(partItems, partDoc.Bytes())
						toolCalls = append(toolCalls, assistantToolCall{
							id:   functionID,
							name: functionName,
						})
					}
					if len(partItems) > 0 {
						contentItems = append(contentItems, geminiContentNode("model", partItems))
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
								c := arr[j].Get("content")
								turnToolResponses[callID] = c.Raw
							}
						}
					}

					// Append a single tool content combining name + response per function.
					responseParts := make([][]byte, 0, len(toolCalls))
					for _, call := range toolCalls {
						partDoc := ojson.NewDocString(`{"functionResponse":{"name":"","response":{"result":""}}}`)
						_ = partDoc.Set("functionResponse.name", call.name)
						response := turnToolResponses[call.id]
						if response == "" {
							response = "{}"
						}
						_ = partDoc.Set("functionResponse.response.result", []byte(response))
						responseParts = append(responseParts, partDoc.Bytes())
					}
					if len(responseParts) > 0 {
						contentItems = append(contentItems, geminiContentNode("user", responseParts))
					}
				} else if len(partItems) > 0 {
					contentItems = append(contentItems, geminiContentNode("model", partItems))
				}
			}
		}

		if len(systemParts) > 0 {
			systemInstruction := geminiContentNode("user", systemParts)
			_ = outDoc.SetRawBytes("systemInstruction", systemInstruction)
		}
		if len(contentItems) > 0 && ojson.GetBytesNoCopy(contentItems[len(contentItems)-1], "role").String() == "model" {
			contentItems = contentItems[:len(contentItems)-1]
		}
		translatorcommon.SetRawArrayItemsDoc(outDoc, "contents", contentItems)
	}

	// tools -> tools[].functionDeclarations + tools[].googleSearch/codeExecution/urlContext passthrough
	allowedToolNames := make(map[string]struct{})
	isAllowedTools := false
	allowedMode := "auto"
	if toolChoice := ojson.GetBytesNoCopy(rawJSON, "tool_choice"); toolChoice.Exists() && toolChoice.IsObject() && toolChoice.Get("type").String() == "allowed_tools" {
		isAllowedTools = true
		toolList := toolChoice.Get("allowed_tools.tools").Array()
		if len(toolList) == 0 {
			toolList = toolChoice.Get("tools").Array()
		}
		for _, t := range toolList {
			fnName := strings.TrimSpace(t.Get("function.name").String())
			if fnName == "" {
				fnName = strings.TrimSpace(t.Get("name").String())
			}
			if fnName != "" {
				allowedToolNames[fnName] = struct{}{}
			}
		}
		modeVal := strings.ToLower(strings.TrimSpace(toolChoice.Get("allowed_tools.mode").String()))
		if modeVal == "" {
			modeVal = strings.ToLower(strings.TrimSpace(toolChoice.Get("mode").String()))
		}
		if modeVal != "" {
			allowedMode = modeVal
		}
	}

	declaredOriginalToSanitized := make(map[string]string)
	sanitizedToOriginalCounts := make(map[string]int)
	var functionDeclarations [][]byte
	var declaredSanitizedNames []string
	hasStrictTool := false
	tools := ojson.GetBytesNoCopy(rawJSON, "tools")
	toolResults := tools.Array()
	if tools.IsArray() && len(toolResults) > 0 {
		functionDeclarations = make([][]byte, 0, len(toolResults))
		googleSearchNodes := make([][]byte, 0)
		codeExecutionNodes := make([][]byte, 0)
		urlContextNodes := make([][]byte, 0)
		for _, t := range toolResults {
			if t.Get("type").String() == "function" {
				fn := t.Get("function")
				if fn.Exists() && fn.IsObject() {
					nameResult := fn.Get("name")
					originalName := nameResult.String()
					if isAllowedTools {
						if _, ok := allowedToolNames[originalName]; !ok {
							continue
						}
					}
					sanitizedName := util.SanitizeFunctionName(originalName)
					sanitizedToOriginalCounts[sanitizedName]++
					declaredOriginalToSanitized[originalName] = sanitizedName
					fnRaw := fn.Raw
					setDefaultSchema := false
					if fn.Get("parameters").Exists() {
						renamed, errRename := util.RenameKey(fnRaw, "parameters", "parametersJsonSchema")
						if errRename != nil {
							log.Warnf("Failed to rename parameters for tool '%s': %v", fn.Get("name").String(), errRename)
							setDefaultSchema = true
						} else {
							fnRaw = renamed
						}
					} else {
						setDefaultSchema = true
					}
					fnDoc := ojson.NewDocString(fnRaw)
					if setDefaultSchema {
						if errSet := fnDoc.Set("parametersJsonSchema.type", "object"); errSet != nil {
							log.Warnf("Failed to set default schema type for tool '%s': %v", fn.Get("name").String(), errSet)
							continue
						}
						if errSet := fnDoc.SetRaw("parametersJsonSchema.properties", `{}`); errSet != nil {
							log.Warnf("Failed to set default schema properties for tool '%s': %v", fn.Get("name").String(), errSet)
							continue
						}
					}
					if nameResult.Type != ojson.String || sanitizedName != originalName {
						_ = fnDoc.Set("name", sanitizedName)
					}
					if parameters := fn.Get("parameters"); parameters.Exists() {
						cleanedParameters := util.CleanJSONSchemaForGeminiJSONSchema(parameters.Raw)
						if cleanedParameters != parameters.Raw {
							_ = fnDoc.SetRaw("parametersJsonSchema", cleanedParameters)
						}
					}
					strictVal := fn.Get("strict")
					hasFnStrict := strictVal.Exists()
					if !strictVal.Exists() {
						strictVal = t.Get("strict")
					}
					if strictVal.Exists() {
						if strictVal.Type == ojson.True {
							hasStrictTool = true
						}
						if hasFnStrict {
							_ = fnDoc.Delete("strict")
						}
					}
					functionDeclarations = append(functionDeclarations, fnDoc.Bytes())
					declaredSanitizedNames = append(declaredSanitizedNames, sanitizedName)
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
		if len(functionDeclarations) > 0 || len(googleSearchNodes) > 0 || len(codeExecutionNodes) > 0 || len(urlContextNodes) > 0 {
			toolItems := make([][]byte, 0, 1+len(googleSearchNodes)+len(codeExecutionNodes)+len(urlContextNodes))
			if len(functionDeclarations) > 0 {
				functionToolNode := []byte(`{"functionDeclarations":[]}`)
				functionToolNode, _ = ojson.SetRawBytes(functionToolNode, "functionDeclarations", translatorcommon.JoinRawArray(functionDeclarations))
				toolItems = append(toolItems, functionToolNode)
			}
			toolItems = append(toolItems, googleSearchNodes...)
			toolItems = append(toolItems, codeExecutionNodes...)
			toolItems = append(toolItems, urlContextNodes...)
			_ = outDoc.SetRawBytes("tools", translatorcommon.JoinRawArray(toolItems))
			// Gemini rejects built-in tools combined with functionDeclarations
			// unless server-side tool invocations are declared.
			if len(functionDeclarations) > 0 && (len(googleSearchNodes) > 0 || len(codeExecutionNodes) > 0 || len(urlContextNodes) > 0) {
				_ = outDoc.Set("toolConfig.includeServerSideToolInvocations", true)
			}
		}
	}

	hasSanitizedCollision := false
	for _, count := range sanitizedToOriginalCounts {
		if count > 1 {
			hasSanitizedCollision = true
			break
		}
	}

	// tool_choice mapping
	if hasSanitizedCollision {
		// Ambiguous collision in function names: fail-closed to prevent invoking unintended tools.
		_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "NONE")
	} else if isAllowedTools {
		if len(functionDeclarations) == 0 {
			// Fail-closed when no allowed tools match or subset is empty.
			_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "NONE")
		} else if allowedMode == "required" || allowedMode == "any" {
			_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "ANY")
			_ = outDoc.Set("toolConfig.functionCallingConfig.allowedFunctionNames", declaredSanitizedNames)
		} else if hasStrictTool {
			_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "VALIDATED")
		} else {
			// Mode AUTO: functionDeclarations contains only allowed tools, mode is AUTO without allowedFunctionNames.
			_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "AUTO")
		}
	} else if toolChoice := ojson.GetBytesNoCopy(rawJSON, "tool_choice"); toolChoice.Exists() && toolChoice.Type != ojson.Null {
		toolChoiceType := ""
		if toolChoice.Type == ojson.String {
			toolChoiceType = strings.ToLower(strings.TrimSpace(toolChoice.String()))
		} else if toolChoice.IsObject() {
			toolChoiceType = strings.ToLower(strings.TrimSpace(toolChoice.Get("type").String()))
		}

		switch toolChoiceType {
		case "auto":
			if hasStrictTool {
				_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "VALIDATED")
			} else {
				_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "AUTO")
			}
		case "none":
			_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "NONE")
		case "required", "any":
			_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "ANY")
		case "function", "tool":
			fnName := strings.TrimSpace(toolChoice.Get("function.name").String())
			if fnName == "" {
				fnName = strings.TrimSpace(toolChoice.Get("name").String())
			}
			sanitized, declared := declaredOriginalToSanitized[fnName]
			if declared && sanitizedToOriginalCounts[sanitized] == 1 {
				_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "ANY")
				_ = outDoc.Set("toolConfig.functionCallingConfig.allowedFunctionNames", []string{sanitized})
			} else {
				// Missing, undeclared, or ambiguous: fail-closed.
				_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "NONE")
			}
		default:
			// Unrecognized tool_choice type: fail-closed.
			_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "NONE")
		}
	} else if hasStrictTool && len(functionDeclarations) > 0 {
		_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "VALIDATED")
	}

	// parallel_tool_calls handling:
	// Gemini function calling has no parameter to disable parallel tool calls while keeping tools enabled.
	// As a safe fail-closed measure when explicit restrictions cannot be faithfully expressed,
	// when parallel_tool_calls is explicitly false, mode is set to NONE.
	if parallelToolCalls := ojson.GetBytesNoCopy(rawJSON, "parallel_tool_calls"); parallelToolCalls.Type == ojson.False {
		_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "NONE")
		_ = outDoc.Delete("toolConfig.functionCallingConfig.allowedFunctionNames")
	}

	common.AttachDefaultSafetySettingsDoc(outDoc, "safetySettings")

	return outDoc.Bytes(), drops.Err()
}

func geminiTextPart(text string) []byte {
	part := []byte(`{"text":""}`)
	part, _ = ojson.SetBytes(part, "text", text)
	return part
}

func geminiInlineDataPart(mimeType, data, thoughtSignature string) []byte {
	partDoc := ojson.NewDocString(`{"inlineData":{"mime_type":"","data":""}}`)
	_ = partDoc.Set("inlineData.mime_type", mimeType)
	_ = partDoc.Set("inlineData.data", data)
	if thoughtSignature != "" {
		_ = partDoc.Set("thoughtSignature", thoughtSignature)
	}
	return partDoc.Bytes()
}

func geminiContentNode(role string, parts [][]byte) []byte {
	contentDoc := ojson.NewDocString(`{"role":"","parts":[]}`)
	_ = contentDoc.Set("role", role)
	_ = contentDoc.SetRawBytes("parts", translatorcommon.JoinRawArray(parts))
	return contentDoc.Bytes()
}

func openAIToolCallGeminiThoughtSignature(toolCall ojson.Result) string {
	for _, path := range []string{
		"extra_content.google.thought_signature",
		"function.extra_content.google.thought_signature",
		"thoughtSignature",
		"thought_signature",
	} {
		if signatureResult := toolCall.Get(path); signatureResult.Exists() {
			return sigcompat.GeminiReplaySignatureOrBypass(signatureResult.String(), sigcompat.SignatureBlockKindGeminiFunctionCall)
		}
	}
	return geminiFunctionThoughtSignature
}

func openAIInputAudioMimeType(audioFormat string) string {
	switch audioFormat {
	case "", "wav":
		return "audio/wav"
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
	default:
		return "audio/" + audioFormat
	}
}

// applyOpenAIResponseFormatToGeminiDoc maps OpenAI Chat Completions structured output settings to Gemini.
// Response schemas pass through unchanged because the tool schema cleaner removes supported response fields.
func applyOpenAIResponseFormatToGeminiDoc(outDoc *ojson.Doc, rawJSON []byte) {
	responseFormat := ojson.GetBytesNoCopy(rawJSON, "response_format")
	if !responseFormat.Exists() {
		return
	}

	switch strings.ToLower(strings.TrimSpace(responseFormat.Get("type").String())) {
	case "json_object":
		_ = outDoc.Set("generationConfig.responseMimeType", "application/json")
	case "json_schema":
		_ = outDoc.Set("generationConfig.responseMimeType", "application/json")
		_ = outDoc.Delete("generationConfig.responseSchema")
		if schema := responseFormat.Get("json_schema.schema"); schema.Exists() {
			_ = outDoc.SetRaw("generationConfig.responseJsonSchema", schema.Raw)
		}
	}
}

// geminiDemotedSystemText wraps a demoted mid-session system or developer
// message in the <system-reminder> envelope so non-Claude upstream models treat it
// as a directive rather than user speech.
func geminiDemotedSystemText(text string, isDemoted bool) string {
	if !isDemoted || strings.TrimSpace(text) == "" {
		return text
	}
	return translatorcommon.SystemReminderText(text)
}
