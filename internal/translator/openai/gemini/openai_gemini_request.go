// Package gemini provides request translation functionality for Gemini to OpenAI API.
// It handles parsing and transforming Gemini API requests into OpenAI Chat Completions API format,
// extracting model information, generation config, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Gemini API format and OpenAI API's expected format.
package gemini

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// ConvertGeminiRequestToOpenAI parses and transforms a Gemini API request into OpenAI Chat Completions API format.
// It extracts the model name, generation config, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the OpenAI API.
func ConvertGeminiRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	rawJSON := inputRawJSON
	// Base OpenAI Chat Completions API template
	outDoc := ojson.NewDocString(`{"model":"","messages":[]}`)

	root := util.ParseGJSONBytesNoCopy(rawJSON)

	// Model mapping
	_ = outDoc.Set("model", modelName)

	// Generation config mapping
	if genConfig := root.Get("generationConfig"); genConfig.Exists() {
		// Temperature
		if temp := genConfig.Get("temperature"); temp.Exists() {
			_ = outDoc.Set("temperature", temp.Float())
		}

		// Max tokens
		if maxTokens := genConfig.Get("maxOutputTokens"); maxTokens.Exists() {
			_ = outDoc.Set("max_tokens", maxTokens.Int())
		}

		// Top P
		if topP := genConfig.Get("topP"); topP.Exists() {
			_ = outDoc.Set("top_p", topP.Float())
		}

		// Top K (OpenAI doesn't have direct equivalent, but we can map it)
		if topK := genConfig.Get("topK"); topK.Exists() {
			// Store as custom parameter for potential use
			_ = outDoc.Set("top_k", topK.Int())
		}

		// Stop sequences
		if stopSequences := genConfig.Get("stopSequences"); stopSequences.Exists() && stopSequences.IsArray() {
			var stops []string
			stopSequences.ForEach(func(_, value ojson.Result) bool {
				stops = append(stops, value.String())
				return true
			})
			if len(stops) > 0 {
				_ = outDoc.Set("stop", stops)
			}
		}

		// Candidate count (OpenAI 'n' parameter)
		if candidateCount := genConfig.Get("candidateCount"); candidateCount.Exists() {
			_ = outDoc.Set("n", candidateCount.Int())
		}

		if responseModalities := genConfig.Get("responseModalities"); responseModalities.Exists() && responseModalities.IsArray() {
			var modalities []string
			responseModalities.ForEach(func(_, value ojson.Result) bool {
				switch strings.ToLower(strings.TrimSpace(value.String())) {
				case "text":
					modalities = append(modalities, "text")
				case "image":
					modalities = append(modalities, "image")
				case "audio":
					modalities = append(modalities, "audio")
				}
				return true
			})
			if len(modalities) > 0 {
				_ = outDoc.Set("modalities", modalities)
			}
		}

		// Map Gemini thinkingConfig to OpenAI reasoning_effort.
		// Always perform conversion to support allowCompat models that may not be in registry.
		// Note: Google official Python SDK sends snake_case fields (thinking_level/thinking_budget).
		if thinkingConfig := genConfig.Get("thinkingConfig"); thinkingConfig.Exists() && thinkingConfig.IsObject() {
			thinkingLevel := thinkingConfig.Get("thinkingLevel")
			if !thinkingLevel.Exists() {
				thinkingLevel = thinkingConfig.Get("thinking_level")
			}
			if thinkingLevel.Exists() {
				effort := strings.ToLower(strings.TrimSpace(thinkingLevel.String()))
				if effort != "" {
					_ = outDoc.Set("reasoning_effort", effort)
				}
			} else {
				thinkingBudget := thinkingConfig.Get("thinkingBudget")
				if !thinkingBudget.Exists() {
					thinkingBudget = thinkingConfig.Get("thinking_budget")
				}
				if thinkingBudget.Exists() {
					if effort, ok := thinking.ConvertBudgetToLevel(int(thinkingBudget.Int())); ok {
						_ = outDoc.Set("reasoning_effort", effort)
					}
				}
			}
		}
	}

	// Stream parameter
	_ = outDoc.Set("stream", stream)
	if serviceTier := root.Get("service_tier"); serviceTier.Exists() && serviceTier.Type == ojson.String {
		_ = outDoc.Set("service_tier", serviceTier.String())
	}

	// Process contents (Gemini messages) -> OpenAI messages
	messageCapacity := root.Get("contents.#").Int()
	if root.Get("systemInstruction").Exists() || root.Get("system_instruction").Exists() {
		messageCapacity++
	}
	messageItems := translatorcommon.NewRawArrayItems(messageCapacity)
	toolCallIDsByName := make(map[string][]string) // Track tool call IDs per function name for matching

	// System instruction -> OpenAI system message
	// Gemini may provide `systemInstruction` or `system_instruction`; support both keys.
	systemInstruction := root.Get("systemInstruction")
	if !systemInstruction.Exists() {
		systemInstruction = root.Get("system_instruction")
	}
	if systemInstruction.Exists() {
		parts := systemInstruction.Get("parts")
		contentItems := make([][]byte, 0, 2)

		if parts.Exists() && parts.IsArray() {
			parts.ForEach(func(_, part ojson.Result) bool {
				if translatorcommon.IsGeminiThoughtPart(part) {
					return true
				}

				// Handle text parts
				if text := part.Get("text"); text.Exists() {
					contentPart := []byte(`{"type":"text","text":""}`)
					contentPart, _ = ojson.SetBytes(contentPart, "text", text.String())
					contentItems = append(contentItems, contentPart)
				}

				// Handle inline data (e.g., images)
				if contentPart, ok := openAIContentPartFromGeminiInlineData(part); ok {
					contentItems = append(contentItems, contentPart)
				}
				if contentPart, ok := openAIContentPartFromGeminiFileData(part); ok {
					contentItems = append(contentItems, contentPart)
				}
				return true
			})
		}

		if len(contentItems) > 0 {
			msg := []byte(`{"role":"system","content":[]}`)
			msg, _ = ojson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray(contentItems))
			messageItems = append(messageItems, msg)
		}
	}

	if contents := root.Get("contents"); contents.Exists() && contents.IsArray() {
		msgIdx := 0
		previousRole := ""
		contents.ForEach(func(_, content ojson.Result) bool {
			parts := content.Get("parts")
			role := translatorcommon.NormalizeGeminiTurnRole(content.Get("role").String(), parts, previousRole)
			previousRole = role

			msgDoc := ojson.NewDocString(`{"role":"","content":""}`)
			_ = msgDoc.Set("role", role)

			var textBuilder strings.Builder
			contentItems := make([][]byte, 0, 4)
			onlyTextContent := true
			toolCallItems := make([][]byte, 0, 2)
			droppedThought := false
			hasFunctionResponse := false

			if parts.Exists() && parts.IsArray() {
				partIdx := 0
				parts.ForEach(func(_, part ojson.Result) bool {
					currentPartIdx := partIdx
					partIdx++

					if translatorcommon.IsGeminiThoughtPart(part) {
						droppedThought = true
						return true
					}

					// Handle text parts
					if text := part.Get("text"); text.Exists() {
						formattedText := text.String()
						textBuilder.WriteString(formattedText)
						contentPart := []byte(`{"type":"text","text":""}`)
						contentPart, _ = ojson.SetBytes(contentPart, "text", formattedText)
						contentItems = append(contentItems, contentPart)
					}

					// Handle inline data (e.g., images)
					if contentPart, ok := openAIContentPartFromGeminiInlineData(part); ok {
						onlyTextContent = false
						contentItems = append(contentItems, contentPart)
					}
					if contentPart, ok := openAIContentPartFromGeminiFileData(part); ok {
						onlyTextContent = false
						contentItems = append(contentItems, contentPart)
					}

					// Handle function calls (Gemini) -> tool calls (OpenAI)
					if functionCall := translatorcommon.GeminiPartFunctionCall(part); functionCall.Exists() {
						funcName := functionCall.Get("name").String()
						argsRaw := ""
						if args := functionCall.Get("args"); args.Exists() {
							argsRaw = args.Raw
						}
						toolCallID := explicitGeminiToolID(functionCall)
						if toolCallID == "" {
							toolCallID = deterministicToolCallID("call", msgIdx, currentPartIdx, funcName, argsRaw)
						}
						toolCallIDsByName[funcName] = append(toolCallIDsByName[funcName], toolCallID)

						toolCallDoc := ojson.NewDocString(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
						_ = toolCallDoc.Set("id", toolCallID)
						_ = toolCallDoc.Set("function.name", funcName)

						// Convert args to arguments JSON string
						if argsRaw != "" {
							_ = toolCallDoc.Set("function.arguments", argsRaw)
						} else {
							_ = toolCallDoc.Set("function.arguments", "{}")
						}
						if thoughtSig := geminiFunctionCallThoughtSignature(part); thoughtSig != "" {
							_ = toolCallDoc.Set("extra_content.google.thought_signature", thoughtSig)
						}

						toolCallItems = append(toolCallItems, toolCallDoc.Bytes())
					}

					// Handle function responses (Gemini) -> tool role messages (OpenAI)
					if functionResponse := translatorcommon.GeminiPartFunctionResponse(part); functionResponse.Exists() {
						funcName := functionResponse.Get("name").String()
						// Create tool message for function response
						toolMsgDoc := ojson.NewDocString(`{"role":"tool","tool_call_id":"","content":""}`)

						responseRaw := ""
						// Convert response.content to JSON string
						if response := functionResponse.Get("response"); response.Exists() {
							if contentField := response.Get("content"); contentField.Exists() {
								responseRaw = contentField.Raw
								_ = toolMsgDoc.Set("content", responseRaw)
							} else {
								responseRaw = response.Raw
								_ = toolMsgDoc.Set("content", responseRaw)
							}
						}

						if toolCallID := explicitGeminiToolID(functionResponse); toolCallID != "" {
							_ = toolMsgDoc.Set("tool_call_id", toolCallID)
							if queue := toolCallIDsByName[funcName]; len(queue) > 0 {
								for i, id := range queue {
									if id == toolCallID {
										toolCallIDsByName[funcName] = append(queue[:i], queue[i+1:]...)
										break
									}
								}
							}
						} else if queue := toolCallIDsByName[funcName]; len(queue) > 0 {
							toolCallID := queue[0]
							toolCallIDsByName[funcName] = queue[1:]
							_ = toolMsgDoc.Set("tool_call_id", toolCallID)
						} else {
							// Generate a deterministic tool call ID fallback if none available
							fallbackID := deterministicToolCallID("response", msgIdx, currentPartIdx, funcName, responseRaw)
							_ = toolMsgDoc.Set("tool_call_id", fallbackID)
						}

						messageItems = append(messageItems, toolMsgDoc.Bytes())
						hasFunctionResponse = true
					}

					return true
				})
			}

			// Set content
			if len(contentItems) > 0 {
				if onlyTextContent {
					_ = msgDoc.Set("content", textBuilder.String())
				} else {
					_ = msgDoc.SetRawBytes("content", translatorcommon.JoinRawArray(contentItems))
				}
			}

			// Set tool calls if any.
			if len(toolCallItems) > 0 {
				_ = msgDoc.SetRawBytes("tool_calls", translatorcommon.JoinRawArray(toolCallItems))
			}

			if (droppedThought || hasFunctionResponse) && len(contentItems) == 0 && len(toolCallItems) == 0 {
				msgIdx++
				return true
			}

			messageItems = append(messageItems, msgDoc.Bytes())
			msgIdx++
			return true
		})
	}
	translatorcommon.SetRawArrayItemsDoc(outDoc, "messages", messageItems)

	// Tools mapping: Gemini tools -> OpenAI tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var toolItems [][]byte
		tools.ForEach(func(_, tool ojson.Result) bool {
			if functionDeclarations := tool.Get("functionDeclarations"); functionDeclarations.Exists() && functionDeclarations.IsArray() {
				functionDeclarations.ForEach(func(_, funcDecl ojson.Result) bool {
					openAIToolDoc := ojson.NewDocString(`{"type":"function","function":{"name":"","description":""}}`)
					_ = openAIToolDoc.Set("function.name", funcDecl.Get("name").String())
					_ = openAIToolDoc.Set("function.description", funcDecl.Get("description").String())

					// Convert parameters schema
					if parameters := funcDecl.Get("parameters"); parameters.Exists() {
						normalizedParams := util.NormalizeJSONSchemaNumericConstraints(parameters.Raw)
						_ = openAIToolDoc.SetRaw("function.parameters", normalizedParams)
					} else if parameters := funcDecl.Get("parametersJsonSchema"); parameters.Exists() {
						normalizedParams := util.NormalizeJSONSchemaNumericConstraints(parameters.Raw)
						_ = openAIToolDoc.SetRaw("function.parameters", normalizedParams)
					}

					toolItems = append(toolItems, openAIToolDoc.Bytes())
					return true
				})
			}
			return true
		})
		if len(toolItems) > 0 {
			_ = outDoc.SetRawBytes("tools", translatorcommon.JoinRawArray(toolItems))
		}
	}

	// Tool choice mapping (Gemini doesn't have direct equivalent, but we can handle it)
	if toolConfig := root.Get("toolConfig"); toolConfig.Exists() {
		if functionCallingConfig := toolConfig.Get("functionCallingConfig"); functionCallingConfig.Exists() {
			mode := functionCallingConfig.Get("mode").String()
			allowedNames := functionCallingConfig.Get("allowedFunctionNames")
			switch mode {
			case "NONE":
				_ = outDoc.Set("tool_choice", "none")
			case "AUTO":
				_ = outDoc.Set("tool_choice", "auto")
			case "ANY":
				allowedNameItems := allowedNames.Array()
				if allowedNames.IsArray() && len(allowedNameItems) == 1 {
					choice := []byte(`{"type":"function","function":{"name":""}}`)
					choice, _ = ojson.SetBytes(choice, "function.name", allowedNameItems[0].String())
					_ = outDoc.SetRawBytes("tool_choice", choice)
				} else {
					_ = outDoc.Set("tool_choice", "required")
				}
			}
		}
	}

	return outDoc.Bytes(), nil
}

func deterministicToolCallID(kind string, msgIdx, partIdx int, name, payload string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%s|%s", kind, msgIdx, partIdx, name, payload)))
	return "call_" + hex.EncodeToString(sum[:12])
}

func geminiFunctionCallThoughtSignature(part ojson.Result) string {
	for _, path := range []string{
		"thoughtSignature",
		"thought_signature",
		"functionCall.thoughtSignature",
		"functionCall.thought_signature",
		"extra_content.google.thought_signature",
	} {
		if sig := part.Get(path); sig.Exists() && sig.String() != "" {
			return sig.String()
		}
	}
	return ""
}

func explicitGeminiToolID(node ojson.Result) string {
	if id := strings.TrimSpace(node.Get("id").String()); id != "" {
		return id
	}
	if callID := strings.TrimSpace(node.Get("call_id").String()); callID != "" {
		return callID
	}
	return strings.TrimSpace(node.Get("callId").String())
}

func openAIContentPartFromGeminiInlineData(part ojson.Result) ([]byte, bool) {
	inlineData := part.Get("inlineData")
	if !inlineData.Exists() {
		inlineData = part.Get("inline_data")
	}
	if !inlineData.Exists() {
		return nil, false
	}
	mimeType := inlineData.Get("mimeType").String()
	if mimeType == "" {
		mimeType = inlineData.Get("mime_type").String()
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	data := inlineData.Get("data").String()
	if data == "" {
		return nil, false
	}
	dataURL := "data:" + mimeType + ";base64," + data
	lowerMimeType := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(lowerMimeType, "image/"):
		contentPart := []byte(`{"type":"image_url","image_url":{"url":""}}`)
		contentPart, _ = ojson.SetBytes(contentPart, "image_url.url", dataURL)
		return contentPart, true
	case strings.HasPrefix(lowerMimeType, "audio/"):
		contentPartDoc := ojson.NewDocString(`{"type":"input_audio","input_audio":{"data":"","format":""}}`)
		_ = contentPartDoc.Set("input_audio.data", data)
		_ = contentPartDoc.Set("input_audio.format", openAIInputAudioFormatFromMIME(mimeType))
		return contentPartDoc.Bytes(), true
	case strings.HasPrefix(lowerMimeType, "video/"):
		contentPart := []byte(`{"type":"video_url","video_url":{"url":""}}`)
		contentPart, _ = ojson.SetBytes(contentPart, "video_url.url", dataURL)
		return contentPart, true
	default:
		contentPartDoc := ojson.NewDocString(`{"type":"file","file":{"filename":"","file_data":""}}`)
		_ = contentPartDoc.Set("file.filename", openAIFileNameFromMIME(mimeType))
		_ = contentPartDoc.Set("file.file_data", data)
		return contentPartDoc.Bytes(), true
	}
}

func openAIContentPartFromGeminiFileData(part ojson.Result) ([]byte, bool) {
	fileData := part.Get("fileData")
	if !fileData.Exists() {
		fileData = part.Get("file_data")
	}
	if !fileData.Exists() {
		return nil, false
	}
	fileURI := fileData.Get("fileUri").String()
	if fileURI == "" {
		fileURI = fileData.Get("file_uri").String()
	}
	if fileURI == "" {
		return nil, false
	}
	mimeType := fileData.Get("mimeType").String()
	if mimeType == "" {
		mimeType = fileData.Get("mime_type").String()
	}
	lowerMimeType := strings.ToLower(mimeType)
	if strings.HasPrefix(lowerMimeType, "image/") {
		contentPart := []byte(`{"type":"image_url","image_url":{"url":""}}`)
		contentPart, _ = ojson.SetBytes(contentPart, "image_url.url", fileURI)
		return contentPart, true
	}
	if strings.HasPrefix(lowerMimeType, "video/") {
		contentPart := []byte(`{"type":"video_url","video_url":{"url":""}}`)
		contentPart, _ = ojson.SetBytes(contentPart, "video_url.url", fileURI)
		return contentPart, true
	}
	if strings.HasPrefix(lowerMimeType, "application/") || strings.HasPrefix(lowerMimeType, "text/") {
		contentPartDoc := ojson.NewDocString(`{"type":"file","file":{"filename":"","file_url":""}}`)
		_ = contentPartDoc.Set("file.filename", openAIFileNameFromMIME(mimeType))
		_ = contentPartDoc.Set("file.file_url", fileURI)
		return contentPartDoc.Bytes(), true
	}
	fileInfo := "File: " + fileURI
	if mimeType != "" {
		fileInfo += " (Type: " + mimeType + ")"
	}
	contentPart := []byte(`{"type":"text","text":""}`)
	contentPart, _ = ojson.SetBytes(contentPart, "text", fileInfo)
	return contentPart, true
}

func openAIInputAudioFormatFromMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "wav"
	case "audio/flac":
		return "flac"
	case "audio/opus", "audio/ogg":
		return "opus"
	case "audio/pcm", "audio/l16":
		return "pcm16"
	default:
		return "mp3"
	}
}

func openAIFileNameFromMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "application/pdf":
		return "document.pdf"
	case "text/plain":
		return "document.txt"
	case "text/csv":
		return "document.csv"
	case "application/json":
		return "document.json"
	case "application/xml", "text/xml":
		return "document.xml"
	default:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "video/") {
			return "video"
		}
		return "document"
	}
}
