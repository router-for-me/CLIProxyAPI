// Package gemini provides request translation functionality for Gemini to OpenAI API.
// It handles parsing and transforming Gemini API requests into OpenAI Chat Completions API format,
// extracting model information, generation config, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Gemini API format and OpenAI API's expected format.
package gemini

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertGeminiRequestToOpenAI parses and transforms a Gemini API request into OpenAI Chat Completions API format.
// It extracts the model name, generation config, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the OpenAI API.
func ConvertGeminiRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)
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
	}

	// Stream parameter
	out, _ = sjson.Set(out, "stream", stream)

	// Process contents (Gemini messages) -> OpenAI messages
	var openAIMessages []interface{}
	var toolCallIDs []string // Track tool call IDs for matching with tool results

	// System instruction -> OpenAI system message
	// Gemini may provide `systemInstruction` or `system_instruction`; support both keys.
	systemInstruction := root.Get("systemInstruction")
	if !systemInstruction.Exists() {
		systemInstruction = root.Get("system_instruction")
	}
	if systemInstruction.Exists() {
		parts := systemInstruction.Get("parts")
		msg := map[string]interface{}{
			"role":    "system",
			"content": []interface{}{},
		}

		var aggregatedParts []interface{}

		if parts.Exists() && parts.IsArray() {
			parts.ForEach(func(_, part gjson.Result) bool {
				// Handle text parts
				if text := part.Get("text"); text.Exists() {
					formattedText := text.String()
					aggregatedParts = append(aggregatedParts, map[string]interface{}{
						"type": "text",
						"text": formattedText,
					})
				}

				// Handle inline data (e.g., images)
				if inlineData := part.Get("inlineData"); inlineData.Exists() {
					mimeType := inlineData.Get("mimeType").String()
					if mimeType == "" {
						mimeType = "application/octet-stream"
					}
					data := inlineData.Get("data").String()
					imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, data)

					aggregatedParts = append(aggregatedParts, map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{
							"url": imageURL,
						},
					})
				}
				return true
			})
		}

		if len(aggregatedParts) > 0 {
			msg["content"] = aggregatedParts
			openAIMessages = append(openAIMessages, msg)
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

			// Create OpenAI message
			msg := map[string]interface{}{
				"role":    role,
				"content": "",
			}

			var textBuilder strings.Builder
			var aggregatedParts []interface{}
			onlyTextContent := true
			var toolCalls []interface{}

			if parts.Exists() && parts.IsArray() {
				parts.ForEach(func(_, part gjson.Result) bool {
					// Handle text parts
					if text := part.Get("text"); text.Exists() {
						formattedText := text.String()
						textBuilder.WriteString(formattedText)
						aggregatedParts = append(aggregatedParts, map[string]interface{}{
							"type": "text",
							"text": formattedText,
						})
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

						aggregatedParts = append(aggregatedParts, map[string]interface{}{
							"type": "image_url",
							"image_url": map[string]interface{}{
								"url": imageURL,
							},
						})
					}

					// Handle function calls (Gemini) -> tool calls (OpenAI)
					if functionCall := part.Get("functionCall"); functionCall.Exists() {
						toolCallID := genToolCallID()
						toolCallIDs = append(toolCallIDs, toolCallID)

						toolCall := map[string]interface{}{
							"id":   toolCallID,
							"type": "function",
							"function": map[string]interface{}{
								"name": functionCall.Get("name").String(),
							},
						}

						// Convert args to arguments JSON string
						if args := functionCall.Get("args"); args.Exists() {
							argsJSON, _ := json.Marshal(args.Value())
							toolCall["function"].(map[string]interface{})["arguments"] = string(argsJSON)
						} else {
							toolCall["function"].(map[string]interface{})["arguments"] = "{}"
						}

						toolCalls = append(toolCalls, toolCall)
					}

					// Handle function responses (Gemini) -> tool role messages (OpenAI)
					if functionResponse := part.Get("functionResponse"); functionResponse.Exists() {
						// Create tool message for function response
						toolMsg := map[string]interface{}{
							"role":         "tool",
							"tool_call_id": "", // Will be set based on context
							"content":      "",
						}

						// Convert response.content to JSON string
						if response := functionResponse.Get("response"); response.Exists() {
							if content = response.Get("content"); content.Exists() {
								// Use the content field from the response
								contentJSON, _ := json.Marshal(content.Value())
								toolMsg["content"] = string(contentJSON)
							} else {
								// Fallback to entire response
								responseJSON, _ := json.Marshal(response.Value())
								toolMsg["content"] = string(responseJSON)
							}
						}

						// Try to match with previous tool call ID
						_ = functionResponse.Get("name").String() // functionName not used for now
						if len(toolCallIDs) > 0 {
							// Use the last tool call ID (simple matching by function name)
							// In a real implementation, you might want more sophisticated matching
							toolMsg["tool_call_id"] = toolCallIDs[len(toolCallIDs)-1]
						} else {
							// Generate a tool call ID if none available
							toolMsg["tool_call_id"] = genToolCallID()
						}

						openAIMessages = append(openAIMessages, toolMsg)
					}

					return true
				})
			}

			// Set content
			if len(aggregatedParts) > 0 {
				if onlyTextContent {
					msg["content"] = textBuilder.String()
				} else {
					msg["content"] = aggregatedParts
				}
			}

			// Set tool calls if any
			if len(toolCalls) > 0 {
				msg["tool_calls"] = toolCalls
			}

			openAIMessages = append(openAIMessages, msg)

			// switch role {
			// case "user", "model":
			// 	// Convert role: model -> assistant
			// 	if role == "model" {
			// 		role = "assistant"
			// 	}
			//
			// 	// Create OpenAI message
			// 	msg := map[string]interface{}{
			// 		"role":    role,
			// 		"content": "",
			// 	}
			//
			// 	var contentParts []string
			// 	var toolCalls []interface{}
			//
			// 	if parts.Exists() && parts.IsArray() {
			// 		parts.ForEach(func(_, part gjson.Result) bool {
			// 			// Handle text parts
			// 			if text := part.Get("text"); text.Exists() {
			// 				contentParts = append(contentParts, text.String())
			// 			}
			//
			// 			// Handle function calls (Gemini) -> tool calls (OpenAI)
			// 			if functionCall := part.Get("functionCall"); functionCall.Exists() {
			// 				toolCallID := genToolCallID()
			// 				toolCallIDs = append(toolCallIDs, toolCallID)
			//
			// 				toolCall := map[string]interface{}{
			// 					"id":   toolCallID,
			// 					"type": "function",
			// 					"function": map[string]interface{}{
			// 						"name": functionCall.Get("name").String(),
			// 					},
			// 				}
			//
			// 				// Convert args to arguments JSON string
			// 				if args := functionCall.Get("args"); args.Exists() {
			// 					argsJSON, _ := json.Marshal(args.Value())
			// 					toolCall["function"].(map[string]interface{})["arguments"] = string(argsJSON)
			// 				} else {
			// 					toolCall["function"].(map[string]interface{})["arguments"] = "{}"
			// 				}
			//
			// 				toolCalls = append(toolCalls, toolCall)
			// 			}
			//
			// 			return true
			// 		})
			// 	}
			//
			// 	// Set content
			// 	if len(contentParts) > 0 {
			// 		msg["content"] = strings.Join(contentParts, "")
			// 	}
			//
			// 	// Set tool calls if any
			// 	if len(toolCalls) > 0 {
			// 		msg["tool_calls"] = toolCalls
			// 	}
			//
			// 	openAIMessages = append(openAIMessages, msg)
			//
			// case "function":
			// 	// Handle Gemini function role -> OpenAI tool role
			// 	if parts.Exists() && parts.IsArray() {
			// 		parts.ForEach(func(_, part gjson.Result) bool {
			// 			// Handle function responses (Gemini) -> tool role messages (OpenAI)
			// 			if functionResponse := part.Get("functionResponse"); functionResponse.Exists() {
			// 				// Create tool message for function response
			// 				toolMsg := map[string]interface{}{
			// 					"role":         "tool",
			// 					"tool_call_id": "", // Will be set based on context
			// 					"content":      "",
			// 				}
			//
			// 				// Convert response.content to JSON string
			// 				if response := functionResponse.Get("response"); response.Exists() {
			// 					if content = response.Get("content"); content.Exists() {
			// 						// Use the content field from the response
			// 						contentJSON, _ := json.Marshal(content.Value())
			// 						toolMsg["content"] = string(contentJSON)
			// 					} else {
			// 						// Fallback to entire response
			// 						responseJSON, _ := json.Marshal(response.Value())
			// 						toolMsg["content"] = string(responseJSON)
			// 					}
			// 				}
			//
			// 				// Try to match with previous tool call ID
			// 				_ = functionResponse.Get("name").String() // functionName not used for now
			// 				if len(toolCallIDs) > 0 {
			// 					// Use the last tool call ID (simple matching by function name)
			// 					// In a real implementation, you might want more sophisticated matching
			// 					toolMsg["tool_call_id"] = toolCallIDs[len(toolCallIDs)-1]
			// 				} else {
			// 					// Generate a tool call ID if none available
			// 					toolMsg["tool_call_id"] = genToolCallID()
			// 				}
			//
			// 				openAIMessages = append(openAIMessages, toolMsg)
			// 			}
			//
			// 			return true
			// 		})
			// 	}
			// }
			return true
		})
	}

	// Set messages
	if len(openAIMessages) > 0 {
		messagesJSON, _ := json.Marshal(openAIMessages)
		out, _ = sjson.SetRaw(out, "messages", string(messagesJSON))
	}

	// Tools mapping: Gemini tools -> OpenAI tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var openAITools []interface{}
		tools.ForEach(func(_, tool gjson.Result) bool {
			if functionDeclarations := tool.Get("functionDeclarations"); functionDeclarations.Exists() && functionDeclarations.IsArray() {
				functionDeclarations.ForEach(func(_, funcDecl gjson.Result) bool {
					openAITool := map[string]interface{}{
						"type": "function",
						"function": map[string]interface{}{
							"name":        funcDecl.Get("name").String(),
							"description": funcDecl.Get("description").String(),
						},
					}

					// Convert parameters schema
					if parameters := funcDecl.Get("parameters"); parameters.Exists() {
						openAITool["function"].(map[string]interface{})["parameters"] = parameters.Value()
					} else if parameters = funcDecl.Get("parametersJsonSchema"); parameters.Exists() {
						openAITool["function"].(map[string]interface{})["parameters"] = parameters.Value()
					}

					openAITools = append(openAITools, openAITool)
					return true
				})
			}
			return true
		})

		if len(openAITools) > 0 {
			toolsJSON, _ := json.Marshal(openAITools)
			out, _ = sjson.SetRaw(out, "tools", string(toolsJSON))
		}
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
