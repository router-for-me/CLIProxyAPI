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

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertGeminiRequestToOpenAI parses and transforms a Gemini API request into OpenAI Chat Completions API format.
// It extracts the model name, generation config, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the OpenAI API.
func ConvertGeminiRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	// Base OpenAI Chat Completions API template
	out := `{"model":"","messages":[]}`

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
	out, _ = sjson.Set(out, "model", modelName)

	// Generation config mapping
	if genConfig := root.Get("generationConfig"); genConfig.Exists() {
		// Temperature
		if temp := genConfig.Get("temperature"); temp.Exists() {
			out, _ = sjson.Set(out, "temperature", temp.Float())
		}

		// Max tokens
		if maxTokens := genConfig.Get("maxOutputTokens"); maxTokens.Exists() {
			out, _ = sjson.Set(out, "max_tokens", maxTokens.Int())
		}

		// Top P
		if topP := genConfig.Get("topP"); topP.Exists() {
			out, _ = sjson.Set(out, "top_p", topP.Float())
		}

		// Top K (OpenAI doesn't have direct equivalent, but we can map it)
		if topK := genConfig.Get("topK"); topK.Exists() {
			// Store as custom parameter for potential use
			out, _ = sjson.Set(out, "top_k", topK.Int())
		}

		// Stop sequences
		if stopSequences := genConfig.Get("stopSequences"); stopSequences.Exists() && stopSequences.IsArray() {
			var stops []string
			stopSequences.ForEach(func(_, value gjson.Result) bool {
				stops = append(stops, value.String())
				return true
			})
			if len(stops) > 0 {
				out, _ = sjson.Set(out, "stop", stops)
			}
		}

		// Candidate count (OpenAI 'n' parameter)
		if candidateCount := genConfig.Get("candidateCount"); candidateCount.Exists() {
			out, _ = sjson.Set(out, "n", candidateCount.Int())
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
					out, _ = sjson.Set(out, "reasoning_effort", effort)
				}
			} else {
				thinkingBudget := thinkingConfig.Get("thinkingBudget")
				if !thinkingBudget.Exists() {
					thinkingBudget = thinkingConfig.Get("thinking_budget")
				}
				if thinkingBudget.Exists() {
					if effort, ok := thinking.ConvertBudgetToLevel(int(thinkingBudget.Int())); ok {
						out, _ = sjson.Set(out, "reasoning_effort", effort)
					}
				}
			}
		}
	}

	// Stream parameter
	out, _ = sjson.Set(out, "stream", stream)

	// Process contents (Gemini messages) -> OpenAI messages
	var toolCallIDs []string // Track tool call IDs for matching with tool results

	// System instruction -> OpenAI system message
	// Gemini may provide `systemInstruction` or `system_instruction`; support both keys.
	systemInstruction := root.Get("systemInstruction")
	if !systemInstruction.Exists() {
		systemInstruction = root.Get("system_instruction")
	}
	if systemInstruction.Exists() {
		parts := systemInstruction.Get("parts")
		msg := `{"role":"system","content":[]}`
		hasContent := false

		if parts.Exists() && parts.IsArray() {
			parts.ForEach(func(_, part gjson.Result) bool {
				// Handle text parts
				if text := part.Get("text"); text.Exists() {
					contentPart := `{"type":"text","text":""}`
					contentPart, _ = sjson.Set(contentPart, "text", text.String())
					msg, _ = sjson.SetRaw(msg, "content.-1", contentPart)
					hasContent = true
				}

				// Handle inline data (e.g., images)
				if inlineData := part.Get("inlineData"); inlineData.Exists() {
					mimeType := inlineData.Get("mimeType").String()
					if mimeType == "" {
						mimeType = "application/octet-stream"
					}
					data := inlineData.Get("data").String()
					imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, data)

					contentPart := `{"type":"image_url","image_url":{"url":""}}`
					contentPart, _ = sjson.Set(contentPart, "image_url.url", imageURL)
					msg, _ = sjson.SetRaw(msg, "content.-1", contentPart)
					hasContent = true
				}
				return true
			})
		}

		if hasContent {
			out, _ = sjson.SetRaw(out, "messages.-1", msg)
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

			msg := `{"role":"","content":""}`
			msg, _ = sjson.Set(msg, "role", role)

			var textBuilder strings.Builder
			contentWrapper := `{"arr":[]}`
			contentPartsCount := 0
			onlyTextContent := true
			toolCallsWrapper := `{"arr":[]}`
			toolCallsCount := 0

			if parts.Exists() && parts.IsArray() {
				parts.ForEach(func(_, part gjson.Result) bool {
					// Handle text parts
					if text := part.Get("text"); text.Exists() {
						formattedText := text.String()
						textBuilder.WriteString(formattedText)
						contentPart := `{"type":"text","text":""}`
						contentPart, _ = sjson.Set(contentPart, "text", formattedText)
						contentWrapper, _ = sjson.SetRaw(contentWrapper, "arr.-1", contentPart)
						contentPartsCount++
					}

					// Handle inline data (e.g., images)
					if inlineData := part.Get("inlineData"); inlineData.Exists() {
						onlyTextContent = false

						mimeType := inlineData.Get("mimeType").String()
						if mimeType == "" {
							mimeType = "application/octet-stream"
						}
						data := inlineData.Get("data").String()
						imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, data)

						contentPart := `{"type":"image_url","image_url":{"url":""}}`
						contentPart, _ = sjson.Set(contentPart, "image_url.url", imageURL)
						contentWrapper, _ = sjson.SetRaw(contentWrapper, "arr.-1", contentPart)
						contentPartsCount++
					}

					// Handle function calls (Gemini) -> tool calls (OpenAI)
					if functionCall := part.Get("functionCall"); functionCall.Exists() {
						toolCallID := genToolCallID()
						toolCallIDs = append(toolCallIDs, toolCallID)

						toolCall := `{"id":"","type":"function","function":{"name":"","arguments":""}}`
						toolCall, _ = sjson.Set(toolCall, "id", toolCallID)
						toolCall, _ = sjson.Set(toolCall, "function.name", functionCall.Get("name").String())

						// Convert args to arguments JSON string
						if args := functionCall.Get("args"); args.Exists() {
							toolCall, _ = sjson.Set(toolCall, "function.arguments", args.Raw)
						} else {
							toolCall, _ = sjson.Set(toolCall, "function.arguments", "{}")
						}

						toolCallsWrapper, _ = sjson.SetRaw(toolCallsWrapper, "arr.-1", toolCall)
						toolCallsCount++
					}

					// Handle function responses (Gemini) -> tool role messages (OpenAI)
					if functionResponse := part.Get("functionResponse"); functionResponse.Exists() {
						// Create tool message for function response
						toolMsg := `{"role":"tool","tool_call_id":"","content":""}`

						// Convert response.content to JSON string
						if response := functionResponse.Get("response"); response.Exists() {
							if contentField := response.Get("content"); contentField.Exists() {
								toolMsg, _ = sjson.Set(toolMsg, "content", contentField.Raw)
							} else {
								toolMsg, _ = sjson.Set(toolMsg, "content", response.Raw)
							}
						}

						// Try to match with previous tool call ID
						_ = functionResponse.Get("name").String() // functionName not used for now
						if len(toolCallIDs) > 0 {
							// Use the last tool call ID (simple matching by function name)
							// In a real implementation, you might want more sophisticated matching
							toolMsg, _ = sjson.Set(toolMsg, "tool_call_id", toolCallIDs[len(toolCallIDs)-1])
						} else {
							// Generate a tool call ID if none available
							toolMsg, _ = sjson.Set(toolMsg, "tool_call_id", genToolCallID())
						}

						out, _ = sjson.SetRaw(out, "messages.-1", toolMsg)
					}

					return true
				})
			}

			// Set content
			if contentPartsCount > 0 {
				if onlyTextContent {
					msg, _ = sjson.Set(msg, "content", textBuilder.String())
				} else {
					msg, _ = sjson.SetRaw(msg, "content", gjson.Get(contentWrapper, "arr").Raw)
				}
			}

			// Set tool calls if any
			if toolCallsCount > 0 {
				msg, _ = sjson.SetRaw(msg, "tool_calls", gjson.Get(toolCallsWrapper, "arr").Raw)
			}

			out, _ = sjson.SetRaw(out, "messages.-1", msg)
			return true
		})
	}

	// Tools mapping: Gemini tools -> OpenAI tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			if functionDeclarations := tool.Get("functionDeclarations"); functionDeclarations.Exists() && functionDeclarations.IsArray() {
				functionDeclarations.ForEach(func(_, funcDecl gjson.Result) bool {
					openAITool := `{"type":"function","function":{"name":"","description":""}}`
					openAITool, _ = sjson.Set(openAITool, "function.name", funcDecl.Get("name").String())
					openAITool, _ = sjson.Set(openAITool, "function.description", funcDecl.Get("description").String())

					// Convert parameters schema
					if parameters := funcDecl.Get("parameters"); parameters.Exists() {
						openAITool, _ = sjson.SetRaw(openAITool, "function.parameters", parameters.Raw)
					} else if parameters := funcDecl.Get("parametersJsonSchema"); parameters.Exists() {
						openAITool, _ = sjson.SetRaw(openAITool, "function.parameters", parameters.Raw)
					}

					out, _ = sjson.SetRaw(out, "tools.-1", openAITool)
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
			switch mode {
			case "NONE":
				out, _ = sjson.Set(out, "tool_choice", "none")
			case "AUTO":
				out, _ = sjson.Set(out, "tool_choice", "auto")
			case "ANY":
				out, _ = sjson.Set(out, "tool_choice", "required")
			}
		}
	}

	return []byte(out)
}
