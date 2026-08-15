// Package openai provides request translation functionality for OpenAI to Claude Code API compatibility.
// It handles parsing and transforming OpenAI Chat Completions API requests into Claude Code API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between OpenAI API format and Claude Code API's expected format.
package chat_completions

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
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
// 1. Model name mapping and parameter extraction (max_tokens, top_p, etc.)
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
	return convertOpenAIRequestToClaude(modelName, inputRawJSON, stream, false)
}

// ConvertOpenAIRequestToClaudeWithCompat preserves assistant reasoning content
// as an unsigned thinking block for configured compatibility endpoints.
func ConvertOpenAIRequestToClaudeWithCompat(modelName string, inputRawJSON []byte, stream bool) []byte {
	return convertOpenAIRequestToClaude(modelName, inputRawJSON, stream, true)
}

func convertOpenAIRequestToClaude(modelName string, inputRawJSON []byte, stream, preserveEmptyThinkingBlocks bool) []byte {
	rawJSON := inputRawJSON

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
	out := []byte(fmt.Sprintf(`{"model":"","max_tokens":32000,"messages":[],"metadata":{"user_id":"%s"}}`, userID))

	root := gjson.ParseBytes(rawJSON)

	// Convert OpenAI reasoning_effort to Claude thinking config.
	if v := root.Get("reasoning_effort"); v.Exists() {
		effort := strings.ToLower(strings.TrimSpace(v.String()))
		if effort != "" {
			mi := registry.LookupModelInfo(modelName, "claude")
			supportsAdaptive := mi != nil && mi.Thinking != nil && len(mi.Thinking.Levels) > 0
			supportsMax := supportsAdaptive && thinking.HasLevel(mi.Thinking.Levels, string(thinking.LevelMax))

			// Claude 4.6 supports adaptive thinking with output_config.effort.
			// MapToClaudeEffort normalizes levels (e.g. minimal→low, xhigh→high) to avoid
			// validation errors since validate treats same-provider unsupported levels as errors.
			if supportsAdaptive {
				switch effort {
				case "none":
					out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
					out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					out, _ = sjson.DeleteBytes(out, "output_config.effort")
				case "auto":
					out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
					out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					out, _ = sjson.DeleteBytes(out, "output_config.effort")
				default:
					if mapped, ok := thinking.MapToClaudeEffort(effort, supportsMax); ok {
						effort = mapped
					}
					out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
					out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					out, _ = sjson.SetBytes(out, "output_config.effort", effort)
				}
			} else {
				// Legacy/manual thinking (budget_tokens).
				budget, ok := thinking.ConvertLevelToBudget(effort)
				if ok {
					switch budget {
					case 0:
						out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
					case -1:
						out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
					default:
						if budget > 0 {
							out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
							out, _ = sjson.SetBytes(out, "thinking.budget_tokens", budget)
						}
					}
				}
			}
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
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Max tokens configuration with fallback to default value
	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}

	// Top P setting for nucleus sampling.
	if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.SetBytes(out, "top_p", topP.Float())
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
				out, _ = sjson.SetBytes(out, "stop_sequences", stopSequences)
			}
		} else {
			out, _ = sjson.SetBytes(out, "stop_sequences", []string{stop.String()})
		}
	}

	// Stream configuration to enable or disable streaming responses
	out, _ = sjson.SetBytes(out, "stream", stream)

	// Process messages and transform them to Claude Code format
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		systemBlocks := make([][]byte, 0)
		messageAccumulator := common.NewClaudeMessageAccumulator(int(root.Get("messages.#").Int()))
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")

			switch role {
			// Developer messages rank with system messages in OpenAI's instruction
			// hierarchy, so both become top-level Claude system blocks. Dropping the
			// developer role, as this translator used to, silently removed operator
			// instructions from the upstream request.
			case "system", "developer":
				systemStart := len(systemBlocks)
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					textPart := []byte(`{"type":"text","text":""}`)
					textPart, _ = sjson.SetBytes(textPart, "text", contentResult.String())
					textPart = common.AttachCacheControl(textPart, message)
					systemBlocks = append(systemBlocks, textPart)
				} else if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() == "text" {
							textPart := []byte(`{"type":"text","text":""}`)
							textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
							textPart = common.AttachCacheControl(textPart, part)
							systemBlocks = append(systemBlocks, textPart)
						}
						return true
					})
				}
				if len(systemBlocks) > systemStart && !gjson.GetBytes(systemBlocks[len(systemBlocks)-1], "cache_control").Exists() {
					systemBlocks[len(systemBlocks)-1] = common.AttachCacheControl(systemBlocks[len(systemBlocks)-1], message)
				}

			case "user":
				// Build user turn blocks and append as an atomic turn.
				userBlocks := make([][]byte, 0)
				userStart := len(userBlocks)

				// Direct string content
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					textPart := []byte(`{"type":"text","text":""}`)
					textPart, _ = sjson.SetBytes(textPart, "text", contentResult.String())
					textPart = common.AttachCacheControl(textPart, message)
					userBlocks = append(userBlocks, textPart)
				}

				// Array content with mixed types (text, images)
				if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part gjson.Result) bool {
						partType := part.Get("type").String()
						switch partType {
						case "text":
							textPart := []byte(`{"type":"text","text":""}`)
							textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
							textPart = common.AttachCacheControl(textPart, part)
							userBlocks = append(userBlocks, textPart)

						case "image_url":
							url := part.Get("image_url.url").String()
							if url != "" {
								if mediaType, data, ok := common.ParseDataURL(url); ok {
									imagePart := []byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`)
									imagePart, _ = sjson.SetBytes(imagePart, "source.media_type", mediaType)
									imagePart, _ = sjson.SetBytes(imagePart, "source.data", data)
									imagePart = common.AttachCacheControl(imagePart, part)
									userBlocks = append(userBlocks, imagePart)
								}
							}
						}
						return true
					})
				}

				if len(userBlocks) > userStart && !gjson.GetBytes(userBlocks[len(userBlocks)-1], "cache_control").Exists() {
					userBlocks[len(userBlocks)-1] = common.AttachCacheControl(userBlocks[len(userBlocks)-1], message)
				}

				if len(userBlocks) > 0 {
					messageAccumulator.AppendUserBlocks(userBlocks)
				}

			case "assistant":
				// Build assistant turn blocks.
				assistantBlocks := make([][]byte, 0)
				assistantStart := len(assistantBlocks)

				// Assistant thinking blocks
				// ConvertOpenAIRequestToClaudeWithCompat preserves reasoning_content as an unsigned thinking block
				// for third-party compatibility endpoints. Upstream Anthropic ignores/strips this block
				// because it does not accept unsigned client thinking blocks on standard chat completions.
				if preserveEmptyThinkingBlocks {
					assistantBlocks = common.AppendOpenAIReasoningContentToClaude(assistantBlocks, message)
				}

				// Text content
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					textPart := []byte(`{"type":"text","text":""}`)
					textPart, _ = sjson.SetBytes(textPart, "text", contentResult.String())
					textPart = common.AttachCacheControl(textPart, message)
					assistantBlocks = append(assistantBlocks, textPart)
				} else if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() == "text" {
							textPart := []byte(`{"type":"text","text":""}`)
							textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
							textPart = common.AttachCacheControl(textPart, part)
							assistantBlocks = append(assistantBlocks, textPart)
						}
						return true
					})
				}

				// Tool calls handling
				if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
					toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
						if toolCall.Get("type").String() == "function" {
							callID := toolCall.Get("id").String()
							if callID == "" {
								callID = genToolCallID()
							}
							callID = common.SanitizeToolCallIDForClaude(callID)

							toolUsePart := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
							toolUsePart, _ = sjson.SetBytes(toolUsePart, "id", callID)
							toolUsePart, _ = sjson.SetBytes(toolUsePart, "name", toolCall.Get("function.name").String())

							// Parse function arguments JSON into input object
							if args := toolCall.Get("function.arguments"); args.Exists() {
								argsStr := args.String()
								if argsStr != "" {
									var inputObj map[string]any
									if err := util.UnmarshalJSONCaseFold([]byte(argsStr), &inputObj); err == nil {
										toolUsePart, _ = sjson.SetBytes(toolUsePart, "input", inputObj)
									}
								}
							}
							toolUsePart = common.AttachCacheControl(toolUsePart, toolCall)
							assistantBlocks = append(assistantBlocks, toolUsePart)
						}
						return true
					})
				}

				if len(assistantBlocks) > assistantStart && !gjson.GetBytes(assistantBlocks[len(assistantBlocks)-1], "cache_control").Exists() {
					assistantBlocks[len(assistantBlocks)-1] = common.AttachCacheControl(assistantBlocks[len(assistantBlocks)-1], message)
				}

				if len(assistantBlocks) > 0 {
					messageAccumulator.AppendAssistantBlocks(assistantBlocks)
				}

			case "tool":
				// Convert tool result message to Claude Code format
				callID := message.Get("tool_call_id").String()
				if callID != "" {
					callID = common.SanitizeToolCallIDForClaude(callID)
					toolResultPart := []byte(`{"type":"tool_result","tool_use_id":""}`)
					toolResultPart, _ = sjson.SetBytes(toolResultPart, "tool_use_id", callID)

					// Tool execution content
					if contentResult.Exists() {
						toolResultPart = common.AppendClaudeToolResultContent(toolResultPart, contentResult)
					}
					toolResultPart = common.AttachCacheControl(toolResultPart, message)
					messageAccumulator.AppendToolResult(toolResultPart)
				}
			}
			return true
		})

		messageBlocks := messageAccumulator.Messages()

		// Preserve a minimal conversational turn for system-only inputs.
		// Claude payloads with top-level system instructions but no messages are risky for downstream validation.
		if len(messageBlocks) == 0 && len(systemBlocks) > 0 {
			messageBlocks = append(messageBlocks, []byte(`{"role":"user","content":[{"type":"text","text":""}]}`))
		}

		if len(systemBlocks) > 0 {
			out, _ = sjson.SetRawBytes(out, "system", common.JoinRawArray(systemBlocks))
		}
		if len(messageBlocks) > 0 {
			out = common.SetRawArrayItems(out, "messages", messageBlocks)
		}
	}

	// Tools mapping: OpenAI tools -> Claude Code tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() && len(tools.Array()) > 0 {
		var anthropicTools [][]byte
		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("type").String() == "function" || tool.Get("type").String() == "custom" {
				function := tool.Get("function")
				if !function.Exists() || !function.IsObject() {
					if c := tool.Get("custom"); c.Exists() && c.IsObject() {
						function = c
					} else {
						function = tool
					}
				}
				toolName := function.Get("name").String()
				if toolName == "" {
					toolName = tool.Get("name").String()
				}
				sanitizedName := util.SanitizeClaudeFunctionName(toolName)
				anthropicTool := []byte(`{"name":"","description":"","input_schema":{"type":"object","properties":{}}}`)
				anthropicTool, _ = sjson.SetBytes(anthropicTool, "name", sanitizedName)
				anthropicTool, _ = sjson.SetBytes(anthropicTool, "description", function.Get("description").String())

				// Convert parameters schema for the tool
				if parameters := function.Get("parameters"); parameters.Exists() {
					anthropicTool, _ = sjson.SetRawBytes(anthropicTool, "input_schema", util.NormalizeClaudeToolInputSchema([]byte(parameters.Raw)))
				} else if parameters := function.Get("parametersJsonSchema"); parameters.Exists() {
					anthropicTool, _ = sjson.SetRawBytes(anthropicTool, "input_schema", util.NormalizeClaudeToolInputSchema([]byte(parameters.Raw)))
				}
				anthropicTool = common.AttachCacheControl(anthropicTool, tool)
				if !gjson.GetBytes(anthropicTool, "cache_control").Exists() {
					anthropicTool = common.AttachCacheControl(anthropicTool, function)
				}

				anthropicTools = append(anthropicTools, anthropicTool)
			}
			return true
		})

		if len(anthropicTools) > 0 {
			out, _ = sjson.SetRawBytes(out, "tools", common.JoinRawArray(anthropicTools))
		} else {
			out, _ = sjson.DeleteBytes(out, "tools")
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
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto"}`))
			case "required":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"any"}`))
			}
		case gjson.JSON:
			// Specific tool choice mapping
			if toolChoice.Get("type").String() == "function" {
				fnName := toolChoice.Get("function.name").String()
				if fnName != "" {
					tc := []byte(`{"type":"tool","name":""}`)
					tc, _ = sjson.SetBytes(tc, "name", fnName)
					out, _ = sjson.SetRawBytes(out, "tool_choice", tc)
				}
			}
		}
	}

	return out
}
