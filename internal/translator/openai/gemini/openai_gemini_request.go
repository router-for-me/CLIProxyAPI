// Package gemini provides request translation functionality for Gemini to OpenAI API.
// It handles parsing and transforming Gemini API requests into OpenAI Chat Completions API format,
// extracting model information, generation config, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Gemini API format and OpenAI API's expected format.
package gemini

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertGeminiRequestToOpenAI parses and transforms a Gemini API request into OpenAI Chat Completions API format.
// It extracts the model name, generation config, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the OpenAI API.
func ConvertGeminiRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	// Base OpenAI Chat Completions API template
	out := []byte(`{"model":"","messages":[]}`)

	root := gjson.ParseBytes(rawJSON)

	// Helper for generating tool call IDs in the form: call_<alphanum>
	genToolCallID := func() string {
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		var b strings.Builder
		// 24 chars random suffix
		for i := 0; i < 24; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b.WriteByte(letters[n.Int64()])
		}
		return "call_" + b.String()
	}

	// Model mapping
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Generation config mapping
	if genConfig := root.Get("generationConfig"); genConfig.Exists() {
		// Temperature
		if temp := genConfig.Get("temperature"); temp.Exists() {
			out, _ = sjson.SetBytes(out, "temperature", temp.Float())
		}

		// Max tokens
		if maxTokens := genConfig.Get("maxOutputTokens"); maxTokens.Exists() {
			out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
		}

		// Top P
		if topP := genConfig.Get("topP"); topP.Exists() {
			out, _ = sjson.SetBytes(out, "top_p", topP.Float())
		}

		// Top K (OpenAI doesn't have direct equivalent, but we can map it)
		if topK := genConfig.Get("topK"); topK.Exists() {
			// Store as custom parameter for potential use
			out, _ = sjson.SetBytes(out, "top_k", topK.Int())
		}

		// Stop sequences
		if stopSequences := genConfig.Get("stopSequences"); stopSequences.Exists() && stopSequences.IsArray() {
			var stops []string
			stopSequences.ForEach(func(_, value gjson.Result) bool {
				stops = append(stops, value.String())
				return true
			})
			if len(stops) > 0 {
				out, _ = sjson.SetBytes(out, "stop", stops)
			}
		}

		// Candidate count (OpenAI 'n' parameter)
		if candidateCount := genConfig.Get("candidateCount"); candidateCount.Exists() {
			out, _ = sjson.SetBytes(out, "n", candidateCount.Int())
		}

		if responseModalities := genConfig.Get("responseModalities"); responseModalities.Exists() && responseModalities.IsArray() {
			var modalities []string
			responseModalities.ForEach(func(_, value gjson.Result) bool {
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
				out, _ = sjson.SetBytes(out, "modalities", modalities)
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
					out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
				}
			} else {
				thinkingBudget := thinkingConfig.Get("thinkingBudget")
				if !thinkingBudget.Exists() {
					thinkingBudget = thinkingConfig.Get("thinking_budget")
				}
				if thinkingBudget.Exists() {
					if effort, ok := thinking.ConvertBudgetToLevel(int(thinkingBudget.Int())); ok {
						out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
					}
				}
			}
		}
	}

	// Stream parameter
	out, _ = sjson.SetBytes(out, "stream", stream)
	if serviceTier := root.Get("service_tier"); serviceTier.Exists() && serviceTier.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "service_tier", serviceTier.String())
	}

	// Process contents (Gemini messages) -> OpenAI messages
	var toolCallIDs []string // Track tool call IDs for matching with tool results
	toolCallConsumeIdx := 0

	// System instruction -> OpenAI system message
	// Gemini may provide `systemInstruction` or `system_instruction`; support both keys.
	systemInstruction := root.Get("systemInstruction")
	if !systemInstruction.Exists() {
		systemInstruction = root.Get("system_instruction")
	}
	if systemInstruction.Exists() {
		parts := systemInstruction.Get("parts")
		msg := []byte(`{"role":"system","content":[]}`)
		hasContent := false

		if parts.Exists() && parts.IsArray() {
			parts.ForEach(func(_, part gjson.Result) bool {
				// Handle text parts
				if text := part.Get("text"); text.Exists() {
					contentPart := []byte(`{"type":"text","text":""}`)
					contentPart, _ = sjson.SetBytes(contentPart, "text", text.String())
					msg, _ = sjson.SetRawBytes(msg, "content.-1", contentPart)
					hasContent = true
				}

				// Handle inline data (e.g., images)
				if contentPart, ok := openAIContentPartFromGeminiInlineData(part); ok {
					msg, _ = sjson.SetRawBytes(msg, "content.-1", contentPart)
					hasContent = true
				}
				if contentPart, ok := openAIContentPartFromGeminiFileData(part); ok {
					msg, _ = sjson.SetRawBytes(msg, "content.-1", contentPart)
					hasContent = true
				}
				return true
			})
		}

		if hasContent {
			out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
		}
	}

	if contents := root.Get("contents"); contents.Exists() && contents.IsArray() {
		contents.ForEach(func(_, content gjson.Result) bool {
			role := content.Get("role").String()
			parts := content.Get("parts")

			// Convert role: model -> assistant
			if role == "model" {
				role = "assistant"
			}

			msg := []byte(`{"role":"","content":""}`)
			msg, _ = sjson.SetBytes(msg, "role", role)

			var textBuilder strings.Builder
			contentWrapper := []byte(`{"arr":[]}`)
			contentPartsCount := 0
			onlyTextContent := true
			toolCallsWrapper := []byte(`{"arr":[]}`)
			toolCallsCount := 0

			if parts.Exists() && parts.IsArray() {
				parts.ForEach(func(_, part gjson.Result) bool {
					// Handle text parts
					if text := part.Get("text"); text.Exists() {
						formattedText := text.String()
						textBuilder.WriteString(formattedText)
						contentPart := []byte(`{"type":"text","text":""}`)
						contentPart, _ = sjson.SetBytes(contentPart, "text", formattedText)
						contentWrapper, _ = sjson.SetRawBytes(contentWrapper, "arr.-1", contentPart)
						contentPartsCount++
					}

					// Handle inline data (e.g., images)
					if contentPart, ok := openAIContentPartFromGeminiInlineData(part); ok {
						onlyTextContent = false
						contentWrapper, _ = sjson.SetRawBytes(contentWrapper, "arr.-1", contentPart)
						contentPartsCount++
					}
					if contentPart, ok := openAIContentPartFromGeminiFileData(part); ok {
						onlyTextContent = false
						contentWrapper, _ = sjson.SetRawBytes(contentWrapper, "arr.-1", contentPart)
						contentPartsCount++
					}

					// Handle function calls (Gemini) -> tool calls (OpenAI)
					if functionCall := part.Get("functionCall"); functionCall.Exists() {
						toolCallID := explicitGeminiToolID(functionCall)
						if toolCallID == "" {
							toolCallID = genToolCallID()
						}
						toolCallIDs = append(toolCallIDs, toolCallID)

						toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
						toolCall, _ = sjson.SetBytes(toolCall, "id", toolCallID)
						toolCall, _ = sjson.SetBytes(toolCall, "function.name", functionCall.Get("name").String())

						// Convert args to arguments JSON string
						if args := functionCall.Get("args"); args.Exists() {
							toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", args.Raw)
						} else {
							toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", "{}")
						}

						toolCallsWrapper, _ = sjson.SetRawBytes(toolCallsWrapper, "arr.-1", toolCall)
						toolCallsCount++
					}

					// Handle function responses (Gemini) -> tool role messages (OpenAI)
					if functionResponse := part.Get("functionResponse"); functionResponse.Exists() {
						// Create tool message for function response
						toolMsg := []byte(`{"role":"tool","tool_call_id":"","content":""}`)

						// Convert response.content to JSON string
						if response := functionResponse.Get("response"); response.Exists() {
							if contentField := response.Get("content"); contentField.Exists() {
								toolMsg, _ = sjson.SetBytes(toolMsg, "content", contentField.Raw)
							} else {
								toolMsg, _ = sjson.SetBytes(toolMsg, "content", response.Raw)
							}
						}

						if toolCallID := explicitGeminiToolID(functionResponse); toolCallID != "" {
							toolMsg, _ = sjson.SetBytes(toolMsg, "tool_call_id", toolCallID)
							if toolCallConsumeIdx < len(toolCallIDs) && toolCallIDs[toolCallConsumeIdx] == toolCallID {
								toolCallConsumeIdx++
							}
						} else if toolCallConsumeIdx < len(toolCallIDs) {
							toolMsg, _ = sjson.SetBytes(toolMsg, "tool_call_id", toolCallIDs[toolCallConsumeIdx])
							toolCallConsumeIdx++
						} else {
							// Generate a tool call ID if none available
							toolMsg, _ = sjson.SetBytes(toolMsg, "tool_call_id", genToolCallID())
						}

						out, _ = sjson.SetRawBytes(out, "messages.-1", toolMsg)
					}

					return true
				})
			}

			// Set content
			if contentPartsCount > 0 {
				if onlyTextContent {
					msg, _ = sjson.SetBytes(msg, "content", textBuilder.String())
				} else {
					msg, _ = sjson.SetRawBytes(msg, "content", []byte(gjson.GetBytes(contentWrapper, "arr").Raw))
				}
			}

			// Set tool calls if any
			if toolCallsCount > 0 {
				msg, _ = sjson.SetRawBytes(msg, "tool_calls", []byte(gjson.GetBytes(toolCallsWrapper, "arr").Raw))
			}

			out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
			return true
		})
	}

	// Tools mapping: Gemini tools -> OpenAI tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			if functionDeclarations := tool.Get("functionDeclarations"); functionDeclarations.Exists() && functionDeclarations.IsArray() {
				functionDeclarations.ForEach(func(_, funcDecl gjson.Result) bool {
					openAITool := []byte(`{"type":"function","function":{"name":"","description":""}}`)
					openAITool, _ = sjson.SetBytes(openAITool, "function.name", funcDecl.Get("name").String())
					openAITool, _ = sjson.SetBytes(openAITool, "function.description", funcDecl.Get("description").String())

					// Convert parameters schema
					if parameters := funcDecl.Get("parameters"); parameters.Exists() {
						openAITool, _ = sjson.SetRawBytes(openAITool, "function.parameters", []byte(parameters.Raw))
					} else if parameters := funcDecl.Get("parametersJsonSchema"); parameters.Exists() {
						openAITool, _ = sjson.SetRawBytes(openAITool, "function.parameters", []byte(parameters.Raw))
					}

					out, _ = sjson.SetRawBytes(out, "tools.-1", openAITool)
					return true
				})
			}
			return true
		})
	}

	// Tool choice mapping (Gemini doesn't have direct equivalent, but we can handle it)
	if toolConfig := root.Get("toolConfig"); toolConfig.Exists() {
		if functionCallingConfig := toolConfig.Get("functionCallingConfig"); functionCallingConfig.Exists() {
			mode := functionCallingConfig.Get("mode").String()
			allowedNames := functionCallingConfig.Get("allowedFunctionNames")
			switch mode {
			case "NONE":
				out, _ = sjson.SetBytes(out, "tool_choice", "none")
			case "AUTO":
				out, _ = sjson.SetBytes(out, "tool_choice", "auto")
			case "ANY":
				if allowedNames.IsArray() && len(allowedNames.Array()) == 1 {
					choice := []byte(`{"type":"function","function":{"name":""}}`)
					choice, _ = sjson.SetBytes(choice, "function.name", allowedNames.Array()[0].String())
					out, _ = sjson.SetRawBytes(out, "tool_choice", choice)
				} else {
					out, _ = sjson.SetBytes(out, "tool_choice", "required")
				}
			}
		}
	}

	return out
}

func explicitGeminiToolID(node gjson.Result) string {
	if id := strings.TrimSpace(node.Get("id").String()); id != "" {
		return id
	}
	return strings.TrimSpace(node.Get("call_id").String())
}

func openAIContentPartFromGeminiInlineData(part gjson.Result) ([]byte, bool) {
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
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, data)
	lowerMimeType := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(lowerMimeType, "image/"):
		contentPart := []byte(`{"type":"image_url","image_url":{"url":""}}`)
		contentPart, _ = sjson.SetBytes(contentPart, "image_url.url", dataURL)
		return contentPart, true
	case strings.HasPrefix(lowerMimeType, "audio/"):
		contentPart := []byte(`{"type":"input_audio","input_audio":{"data":"","format":""}}`)
		contentPart, _ = sjson.SetBytes(contentPart, "input_audio.data", data)
		contentPart, _ = sjson.SetBytes(contentPart, "input_audio.format", openAIInputAudioFormatFromMIME(mimeType))
		return contentPart, true
	case strings.HasPrefix(lowerMimeType, "video/"):
		contentPart := []byte(`{"type":"video_url","video_url":{"url":""}}`)
		contentPart, _ = sjson.SetBytes(contentPart, "video_url.url", dataURL)
		return contentPart, true
	default:
		contentPart := []byte(`{"type":"file","file":{"filename":"","file_data":""}}`)
		contentPart, _ = sjson.SetBytes(contentPart, "file.filename", openAIFileNameFromMIME(mimeType))
		contentPart, _ = sjson.SetBytes(contentPart, "file.file_data", data)
		return contentPart, true
	}
}

func openAIContentPartFromGeminiFileData(part gjson.Result) ([]byte, bool) {
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
		contentPart, _ = sjson.SetBytes(contentPart, "image_url.url", fileURI)
		return contentPart, true
	}
	if strings.HasPrefix(lowerMimeType, "video/") {
		contentPart := []byte(`{"type":"video_url","video_url":{"url":""}}`)
		contentPart, _ = sjson.SetBytes(contentPart, "video_url.url", fileURI)
		return contentPart, true
	}
	if strings.HasPrefix(lowerMimeType, "application/") || strings.HasPrefix(lowerMimeType, "text/") {
		contentPart := []byte(`{"type":"file","file":{"filename":"","file_url":""}}`)
		contentPart, _ = sjson.SetBytes(contentPart, "file.filename", openAIFileNameFromMIME(mimeType))
		contentPart, _ = sjson.SetBytes(contentPart, "file.file_url", fileURI)
		return contentPart, true
	}
	fileInfo := "File: " + fileURI
	if mimeType != "" {
		fileInfo += " (Type: " + mimeType + ")"
	}
	contentPart := []byte(`{"type":"text","text":""}`)
	contentPart, _ = sjson.SetBytes(contentPart, "text", fileInfo)
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
