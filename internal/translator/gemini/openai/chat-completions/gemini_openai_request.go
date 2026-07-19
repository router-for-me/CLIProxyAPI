// Package openai provides request translation functionality for OpenAI to Gemini API compatibility.
// It converts OpenAI Chat Completions requests into Gemini compatible JSON using gjson/sjson only.
package chat_completions

import (
	"strings"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const geminiFunctionThoughtSignature = "skip_thought_signature_validator"

// ConvertOpenAIRequestToGemini converts an OpenAI Chat Completions request (raw JSON)
// into a complete Gemini request JSON. All JSON construction uses sjson and lookups use gjson.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Gemini API format
func ConvertOpenAIRequestToGemini(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	// Base envelope (no default thinkingConfig)
	out := []byte(`{"contents":[]}`)

	// Model
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Let user-provided generationConfig pass through
	if genConfig := gjson.GetBytes(rawJSON, "generationConfig"); genConfig.Exists() {
		out, _ = sjson.SetRawBytes(out, "generationConfig", []byte(genConfig.Raw))
	}

	// Apply thinking configuration: convert OpenAI reasoning_effort to Gemini thinkingConfig.
	// Inline translation-only mapping; capability checks happen later in ApplyThinking.
	re := gjson.GetBytes(rawJSON, "reasoning_effort")
	if re.Exists() {
		effort := strings.ToLower(strings.TrimSpace(re.String()))
		if effort != "" {
			thinkingPath := "generationConfig.thinkingConfig"
			if effort == "auto" {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingBudget", -1)
				out, _ = sjson.SetBytes(out, thinkingPath+".includeThoughts", true)
			} else {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingLevel", effort)
				out, _ = sjson.SetBytes(out, thinkingPath+".includeThoughts", effort != "none")
			}
		}
	}

	// Temperature/top_p/top_k
	if tr := gjson.GetBytes(rawJSON, "temperature"); tr.Exists() && tr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "generationConfig.temperature", tr.Num)
	}
	if tpr := gjson.GetBytes(rawJSON, "top_p"); tpr.Exists() && tpr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "generationConfig.topP", tpr.Num)
	}
	if tkr := gjson.GetBytes(rawJSON, "top_k"); tkr.Exists() && tkr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "generationConfig.topK", tkr.Num)
	}

	// OpenAI max_tokens / max_completion_tokens -> Gemini generationConfig.maxOutputTokens
	if mt := gjson.GetBytes(rawJSON, "max_tokens"); mt.Exists() && mt.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "generationConfig.maxOutputTokens", mt.Num)
	} else if mct := gjson.GetBytes(rawJSON, "max_completion_tokens"); mct.Exists() && mct.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "generationConfig.maxOutputTokens", mct.Num)
	}

	// Candidate count (OpenAI 'n' parameter)
	if n := gjson.GetBytes(rawJSON, "n"); n.Exists() && n.Type == gjson.Number {
		if val := n.Int(); val > 1 {
			out, _ = sjson.SetBytes(out, "generationConfig.candidateCount", val)
		}
	}

	// Map OpenAI modalities -> Gemini generationConfig.responseModalities
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
			out, _ = sjson.SetBytes(out, "generationConfig.responseModalities", responseMods)
		}
	}

	// OpenRouter-style image_config support
	// If the input uses top-level image_config.aspect_ratio, map it into generationConfig.imageConfig.aspectRatio.
	if imgCfg := gjson.GetBytes(rawJSON, "image_config"); imgCfg.Exists() && imgCfg.IsObject() {
		if ar := imgCfg.Get("aspect_ratio"); ar.Exists() && ar.Type == gjson.String {
			out, _ = sjson.SetBytes(out, "generationConfig.imageConfig.aspectRatio", ar.Str)
		}
		if size := imgCfg.Get("image_size"); size.Exists() && size.Type == gjson.String {
			out, _ = sjson.SetBytes(out, "generationConfig.imageConfig.imageSize", size.Str)
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
				// system -> systemInstruction as a user message style
				if content.Type == gjson.String {
					systemParts = append(systemParts, geminiTextPart(content.String()))
				} else if content.IsObject() && content.Get("type").String() == "text" {
					systemParts = append(systemParts, geminiTextPart(content.Get("text").String()))
				} else if content.IsArray() {
					contents := content.Array()
					for j := 0; j < len(contents); j++ {
						systemParts = append(systemParts, geminiTextPart(contents[j].Get("text").String()))
					}
				}
			} else if role == "user" || ((role == "system" || role == "developer") && len(arr) == 1) {
				// Build single user content node to avoid splitting into multiple contents.
				partItems := make([][]byte, 0, 4)
				if content.Type == gjson.String {
					partItems = append(partItems, geminiTextPart(content.String()))
				} else if content.IsArray() {
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
									partItems = append(partItems, geminiInlineDataPart(pieces[0], pieces[1][7:], geminiFunctionThoughtSignature))
								}
							}
						case "video_url":
							videoURL := item.Get("video_url.url").String()
							if len(videoURL) > 5 {
								pieces := strings.SplitN(videoURL[5:], ";", 2)
								if len(pieces) == 2 && len(pieces[1]) > 7 {
									partItems = append(partItems, geminiInlineDataPart(pieces[0], pieces[1][7:], ""))
								}
							}
						case "file":
							filename := item.Get("file.filename").String()
							fileData := item.Get("file.file_data").String()
							if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData(filename, "", fileData); ok {
								partItems = append(partItems, geminiInlineDataPart(mimeType, data, ""))
							} else {
								log.Warn("Invalid file data or unknown file name extension in user message, skip")
							}
						case "input_audio":
							audioData := item.Get("input_audio.data").String()
							if audioData != "" {
								mimeType := openAIInputAudioMimeType(item.Get("input_audio.format").String())
								partItems = append(partItems, geminiInlineDataPart(mimeType, audioData, ""))
							}
						}
					}
				}
				contentItems = append(contentItems, geminiContentNode("user", partItems))
			} else if role == "assistant" {
				partItems := make([][]byte, 0, 4)
				if reasoningContent := m.Get("reasoning_content"); reasoningContent.Type == gjson.String && reasoningContent.String() != "" {
					part := geminiTextPart(reasoningContent.String())
					part, _ = sjson.SetBytes(part, "thought", true)
					part, _ = sjson.SetBytes(part, "thoughtSignature", geminiFunctionThoughtSignature)
					partItems = append(partItems, part)
				}
				if content.Type == gjson.String && content.String() != "" {
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
									partItems = append(partItems, geminiInlineDataPart(pieces[0], pieces[1][7:], geminiFunctionThoughtSignature))
								}
							}
						}
					}
				}

				// Tool calls -> single model content with functionCall parts.
				tcs := m.Get("tool_calls")
				if tcs.IsArray() {
					functionIDs := make([]string, 0)
					for _, tc := range tcs.Array() {
						if tc.Get("type").String() != "function" {
							continue
						}
						functionID := tc.Get("id").String()
						functionName := util.SanitizeFunctionName(tc.Get("function.name").String())
						if functionName == "" {
							continue
						}
						part := []byte(`{"functionCall":{"name":""}}`)
						part, _ = sjson.SetBytes(part, "functionCall.name", functionName)
						part, _ = sjson.SetRawBytes(part, "functionCall.args", []byte(tc.Get("function.arguments").String()))
						part, _ = sjson.SetBytes(part, "thoughtSignature", openAIToolCallGeminiThoughtSignature(tc))
						partItems = append(partItems, part)
						if functionID != "" {
							functionIDs = append(functionIDs, functionID)
						}
					}
					if len(partItems) > 0 {
						contentItems = append(contentItems, geminiContentNode("model", partItems))
					}

					// Append a single tool content combining name + response per function.
					responseParts := make([][]byte, 0, len(functionIDs))
					for _, functionID := range functionIDs {
						if name, ok := tcID2Name[functionID]; ok {
							part := []byte(`{"functionResponse":{"name":"","response":{"result":""}}}`)
							part, _ = sjson.SetBytes(part, "functionResponse.name", util.SanitizeFunctionName(name))
							response := toolResponses[functionID]
							if response == "" {
								response = "{}"
							}
							part, _ = sjson.SetBytes(part, "functionResponse.response.result", []byte(response))
							responseParts = append(responseParts, part)
						}
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
			out, _ = sjson.SetRawBytes(out, "systemInstruction", systemInstruction)
		}
		if len(contentItems) > 0 && gjson.GetBytes(contentItems[len(contentItems)-1], "role").String() == "model" {
			contentItems = contentItems[:len(contentItems)-1]
		}
		out = translatorcommon.SetRawArrayItems(out, "contents", contentItems)
	}

	// tools -> tools[].functionDeclarations + tools[].googleSearch/codeExecution/urlContext passthrough
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
							fnRawBytes := []byte(fnRaw)
							fnRawBytes, errSet = sjson.SetBytes(fnRawBytes, "parametersJsonSchema.type", "object")
							if errSet != nil {
								log.Warnf("Failed to set default schema type for tool '%s': %v", fn.Get("name").String(), errSet)
								continue
							}
							fnRawBytes, errSet = sjson.SetRawBytes(fnRawBytes, "parametersJsonSchema.properties", []byte(`{}`))
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
						fnRawBytes := []byte(fnRaw)
						fnRawBytes, errSet = sjson.SetBytes(fnRawBytes, "parametersJsonSchema.type", "object")
						if errSet != nil {
							log.Warnf("Failed to set default schema type for tool '%s': %v", fn.Get("name").String(), errSet)
							continue
						}
						fnRawBytes, errSet = sjson.SetRawBytes(fnRawBytes, "parametersJsonSchema.properties", []byte(`{}`))
						if errSet != nil {
							log.Warnf("Failed to set default schema properties for tool '%s': %v", fn.Get("name").String(), errSet)
							continue
						}
						fnRaw = string(fnRawBytes)
					}
					fnRawBytes := []byte(fnRaw)
					nameResult := fn.Get("name")
					originalName := nameResult.String()
					sanitizedName := util.SanitizeFunctionName(originalName)
					if nameResult.Type != gjson.String || sanitizedName != originalName {
						fnRawBytes, _ = sjson.SetBytes(fnRawBytes, "name", sanitizedName)
					}
					if parameters := gjson.GetBytes(fnRawBytes, "parametersJsonSchema"); parameters.Exists() {
						cleanedParameters := util.CleanJSONSchemaForGemini(parameters.Raw)
						if cleanedParameters != parameters.Raw {
							fnRawBytes, _ = sjson.SetRawBytes(fnRawBytes, "parametersJsonSchema", []byte(cleanedParameters))
						}
					}
					if gjson.GetBytes(fnRawBytes, "strict").Exists() {
						fnRawBytes, _ = sjson.DeleteBytes(fnRawBytes, "strict")
					}
					functionDeclarations = append(functionDeclarations, fnRawBytes)
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
		if len(functionDeclarations) > 0 || len(googleSearchNodes) > 0 || len(codeExecutionNodes) > 0 || len(urlContextNodes) > 0 {
			toolItems := make([][]byte, 0, 1+len(googleSearchNodes)+len(codeExecutionNodes)+len(urlContextNodes))
			if len(functionDeclarations) > 0 {
				functionToolNode := []byte(`{"functionDeclarations":[]}`)
				functionToolNode, _ = sjson.SetRawBytes(functionToolNode, "functionDeclarations", translatorcommon.JoinRawArray(functionDeclarations))
				toolItems = append(toolItems, functionToolNode)
			}
			toolItems = append(toolItems, googleSearchNodes...)
			toolItems = append(toolItems, codeExecutionNodes...)
			toolItems = append(toolItems, urlContextNodes...)
			out, _ = sjson.SetRawBytes(out, "tools", translatorcommon.JoinRawArray(toolItems))
		}
	}

	out = common.AttachDefaultSafetySettings(out, "safetySettings")

	return out
}

func geminiTextPart(text string) []byte {
	part := []byte(`{"text":""}`)
	part, _ = sjson.SetBytes(part, "text", text)
	return part
}

func geminiInlineDataPart(mimeType, data, thoughtSignature string) []byte {
	part := []byte(`{"inlineData":{"mime_type":"","data":""}}`)
	part, _ = sjson.SetBytes(part, "inlineData.mime_type", mimeType)
	part, _ = sjson.SetBytes(part, "inlineData.data", data)
	if thoughtSignature != "" {
		part, _ = sjson.SetBytes(part, "thoughtSignature", thoughtSignature)
	}
	return part
}

func geminiContentNode(role string, parts [][]byte) []byte {
	content := []byte(`{"role":"","parts":[]}`)
	content, _ = sjson.SetBytes(content, "role", role)
	content, _ = sjson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(parts))
	return content
}

func openAIToolCallGeminiThoughtSignature(toolCall gjson.Result) string {
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
