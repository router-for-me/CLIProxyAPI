// Package openai provides request translation functionality for OpenAI to Claude Code API compatibility.
// It handles parsing and transforming OpenAI Chat Completions API requests into Claude Code API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between OpenAI API format and Claude Code API's expected format.
package chat_completions

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	user    = ""
	account = ""
	session = ""
)

// ConvertOpenAIRequestToClaude parses and transforms an OpenAI Chat Completions API request into Claude Code API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Claude Code API.
// The function performs comprehensive transformation including:
// 1. Model name mapping and parameter extraction (max_tokens, temperature, top_p, etc.)
// 2. Message content conversion from OpenAI to Claude Code format
// 3. Tool call and tool result handling with proper ID mapping
// 4. Image data conversion from OpenAI data URLs to Claude Code base64 format
// 5. Stop sequence and streaming configuration handling
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI API
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in Claude Code API format
func ConvertOpenAIRequestToClaude(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)

	if account == "" {
		u, _ := uuid.NewRandom()
		account = u.String()
	}
	if session == "" {
		u, _ := uuid.NewRandom()
		session = u.String()
	}
	if user == "" {
		sum := sha256.Sum256([]byte(account + session))
		user = hex.EncodeToString(sum[:])
	}
	userID := fmt.Sprintf("user_%s_account_%s_session_%s", user, account, session)

	// Base Claude Code API template with default max_tokens value
	out := fmt.Sprintf(`{"model":"","max_tokens":32000,"messages":[],"metadata":{"user_id":"%s"}}`, userID)

	root := gjson.ParseBytes(rawJSON)

	if v := root.Get("reasoning_effort"); v.Exists() && util.ModelSupportsThinking(modelName) && !util.ModelUsesThinkingLevels(modelName) {
		out, _ = sjson.Set(out, "thinking.type", "enabled")

		switch v.String() {
		case "none":
			out, _ = sjson.Set(out, "thinking.type", "disabled")
		case "low":
			out, _ = sjson.Set(out, "thinking.budget_tokens", 1024)
		case "medium":
			out, _ = sjson.Set(out, "thinking.budget_tokens", 8192)
		case "high":
			out, _ = sjson.Set(out, "thinking.budget_tokens", 24576)
		case "xhigh":
			out, _ = sjson.Set(out, "thinking.budget_tokens", 32768)
		}
	}

	// Helper for generating tool call IDs in the form: toolu_<alphanum>
	// This ensures unique identifiers for tool calls in the Claude Code format
	genToolCallID := func() string {
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		var b strings.Builder
		// 24 chars random suffix for uniqueness
		for i := 0; i < 24; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b.WriteByte(letters[n.Int64()])
		}
		return "toolu_" + b.String()
	}

	// Model mapping to specify which Claude Code model to use
	out, _ = sjson.Set(out, "model", modelName)

	// Max tokens configuration with fallback to default value
	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.Set(out, "max_tokens", maxTokens.Int())
	}

	// Temperature setting for controlling response randomness
	if temp := root.Get("temperature"); temp.Exists() {
		out, _ = sjson.Set(out, "temperature", temp.Float())
	}

	// Top P setting for nucleus sampling
	if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.Set(out, "top_p", topP.Float())
	}

	// Stop sequences configuration for custom termination conditions
	if stop := root.Get("stop"); stop.Exists() {
		if stop.IsArray() {
			var stopSequences []string
			stop.ForEach(func(_, value gjson.Result) bool {
				stopSequences = append(stopSequences, value.String())
				return true
			})
			if len(stopSequences) > 0 {
				out, _ = sjson.Set(out, "stop_sequences", stopSequences)
			}
		} else {
			out, _ = sjson.Set(out, "stop_sequences", []string{stop.String()})
		}
	}

	// Stream configuration to enable or disable streaming responses
	out, _ = sjson.Set(out, "stream", stream)

	// Process messages and transform them to Claude Code format
	var anthropicMessages []interface{}
	var toolCallIDs []string // Track tool call IDs for matching with tool results

	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")

			switch role {
			case "system", "user", "assistant":
				// Create Claude Code message with appropriate role mapping
				if role == "system" {
					role = "user"
				}

				msg := map[string]interface{}{
					"role":    role,
					"content": []interface{}{},
				}

				// Handle content based on its type (string or array)
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					// Simple text content conversion
					msg["content"] = []interface{}{
						map[string]interface{}{
							"type": "text",
							"text": contentResult.String(),
						},
					}
				} else if contentResult.Exists() && contentResult.IsArray() {
					// Array of content parts processing
					var contentParts []interface{}
					contentResult.ForEach(func(_, part gjson.Result) bool {
						partType := part.Get("type").String()

						switch partType {
						case "text":
							// Text part conversion
							contentParts = append(contentParts, map[string]interface{}{
								"type": "text",
								"text": part.Get("text").String(),
							})

						case "image_url":
							// Convert OpenAI image format to Claude Code format
							imageURL := part.Get("image_url.url").String()
							if strings.HasPrefix(imageURL, "data:") {
								// Extract base64 data and media type from data URL
								parts := strings.Split(imageURL, ",")
								if len(parts) == 2 {
									mediaTypePart := strings.Split(parts[0], ";")[0]
									mediaType := strings.TrimPrefix(mediaTypePart, "data:")
									data := parts[1]

									contentParts = append(contentParts, map[string]interface{}{
										"type": "image",
										"source": map[string]interface{}{
											"type":       "base64",
											"media_type": mediaType,
											"data":       data,
										},
									})
								}
							}
						}
						return true
					})
					if len(contentParts) > 0 {
						msg["content"] = contentParts
					}
				} else {
					// Initialize empty content array for tool calls
					msg["content"] = []interface{}{}
				}

				// Handle tool calls (for assistant messages)
				if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() && role == "assistant" {
					var contentParts []interface{}

					// Add existing text content if any
					if existingContent, ok := msg["content"].([]interface{}); ok {
						contentParts = existingContent
					}

					toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
						if toolCall.Get("type").String() == "function" {
							toolCallID := toolCall.Get("id").String()
							if toolCallID == "" {
								toolCallID = genToolCallID()
							}
							toolCallIDs = append(toolCallIDs, toolCallID)

							function := toolCall.Get("function")
							toolUse := map[string]interface{}{
								"type": "tool_use",
								"id":   toolCallID,
								"name": function.Get("name").String(),
							}

							// Parse arguments for the tool call
							if args := function.Get("arguments"); args.Exists() {
								argsStr := args.String()
								if argsStr != "" {
									var argsMap map[string]interface{}
									if err := json.Unmarshal([]byte(argsStr), &argsMap); err == nil {
										toolUse["input"] = argsMap
									} else {
										toolUse["input"] = map[string]interface{}{}
									}
								} else {
									toolUse["input"] = map[string]interface{}{}
								}
							} else {
								toolUse["input"] = map[string]interface{}{}
							}

							contentParts = append(contentParts, toolUse)
						}
						return true
					})
					msg["content"] = contentParts
				}

				anthropicMessages = append(anthropicMessages, msg)

			case "tool":
				// Handle tool result messages conversion
				toolCallID := message.Get("tool_call_id").String()
				content := message.Get("content").String()

				// Create tool result message in Claude Code format
				msg := map[string]interface{}{
					"role": "user",
					"content": []interface{}{
						map[string]interface{}{
							"type":        "tool_result",
							"tool_use_id": toolCallID,
							"content":     content,
						},
					},
				}

				anthropicMessages = append(anthropicMessages, msg)
			}
			return true
		})
	}

	// Set messages in the output template
	if len(anthropicMessages) > 0 {
		messagesJSON, _ := json.Marshal(anthropicMessages)
		out, _ = sjson.SetRaw(out, "messages", string(messagesJSON))
	}

	// Tools mapping: OpenAI tools -> Claude Code tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() && len(tools.Array()) > 0 {
		var anthropicTools []interface{}
		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("type").String() == "function" {
				function := tool.Get("function")
				anthropicTool := map[string]interface{}{
					"name":        function.Get("name").String(),
					"description": function.Get("description").String(),
				}

				// Convert parameters schema for the tool
				if parameters := function.Get("parameters"); parameters.Exists() {
					anthropicTool["input_schema"] = parameters.Value()
				} else if parameters = function.Get("parametersJsonSchema"); parameters.Exists() {
					anthropicTool["input_schema"] = parameters.Value()
				}

				anthropicTools = append(anthropicTools, anthropicTool)
			}
			return true
		})

		if len(anthropicTools) > 0 {
			toolsJSON, _ := json.Marshal(anthropicTools)
			out, _ = sjson.SetRaw(out, "tools", string(toolsJSON))
		}
	}

	// Tool choice mapping from OpenAI format to Claude Code format
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		switch toolChoice.Type {
		case gjson.String:
			choice := toolChoice.String()
			switch choice {
			case "none":
				// Don't set tool_choice, Claude Code will not use tools
			case "auto":
				out, _ = sjson.Set(out, "tool_choice", map[string]interface{}{"type": "auto"})
			case "required":
				out, _ = sjson.Set(out, "tool_choice", map[string]interface{}{"type": "any"})
			}
		case gjson.JSON:
			// Specific tool choice mapping
			if toolChoice.Get("type").String() == "function" {
				functionName := toolChoice.Get("function.name").String()
				out, _ = sjson.Set(out, "tool_choice", map[string]interface{}{
					"type": "tool",
					"name": functionName,
				})
			}
		default:
		}
	}

	return []byte(out)
}
