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
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
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
	// Some OpenAI-compatible clients (notably Cursor in agent/tool mode) send
	// Anthropic-native content blocks (tool_use / tool_result) and bare tool
	// definitions to the OpenAI Chat Completions endpoint. Those payloads are not
	// valid OpenAI format, so normalize them to standard OpenAI shape before the
	// regular translation below runs.
	rawJSON := normalizeAnthropicRequestBlocks(inputRawJSON)

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

	// Temperature setting for controlling response randomness
	if temp := root.Get("temperature"); temp.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", temp.Float())
	} else if topP := root.Get("top_p"); topP.Exists() {
		// Top P setting for nucleus sampling (filtered out if temperature is set)
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
		messageIndex := 0
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")

			switch role {
			case "system":
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					textPart := []byte(`{"type":"text","text":""}`)
					textPart, _ = sjson.SetBytes(textPart, "text", contentResult.String())
					out, _ = sjson.SetRawBytes(out, "system.-1", textPart)
				} else if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() == "text" {
							textPart := []byte(`{"type":"text","text":""}`)
							textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
							out, _ = sjson.SetRawBytes(out, "system.-1", textPart)
						}
						return true
					})
				}
			case "user", "assistant":
				msg := []byte(`{"role":"","content":[]}`)
				msg, _ = sjson.SetBytes(msg, "role", role)

				// Handle content based on its type (string or array)
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					part := []byte(`{"type":"text","text":""}`)
					part, _ = sjson.SetBytes(part, "text", contentResult.String())
					msg, _ = sjson.SetRawBytes(msg, "content.-1", part)
				} else if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part gjson.Result) bool {
						claudePart := convertOpenAIContentPartToClaudePart(part)
						if claudePart != "" {
							msg, _ = sjson.SetRawBytes(msg, "content.-1", []byte(claudePart))
						}
						return true
					})
				}

				// Handle tool calls (for assistant messages)
				if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() && role == "assistant" {
					toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
						if toolCall.Get("type").String() == "function" {
							toolCallID := toolCall.Get("id").String()
							if toolCallID == "" {
								toolCallID = genToolCallID()
							}

							function := toolCall.Get("function")
							toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
							toolUse, _ = sjson.SetBytes(toolUse, "id", toolCallID)
							toolUse, _ = sjson.SetBytes(toolUse, "name", function.Get("name").String())

							// Parse arguments for the tool call
							if args := function.Get("arguments"); args.Exists() {
								argsStr := args.String()
								if argsStr != "" && gjson.Valid(argsStr) {
									argsJSON := gjson.Parse(argsStr)
									if argsJSON.IsObject() {
										toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(argsJSON.Raw))
									} else {
										toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte("{}"))
									}
								} else {
									toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte("{}"))
								}
							} else {
								toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte("{}"))
							}

							msg, _ = sjson.SetRawBytes(msg, "content.-1", toolUse)
						}
						return true
					})
				}

				out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
				messageIndex++

			case "tool":
				// Handle tool result messages conversion
				toolCallID := message.Get("tool_call_id").String()
				toolContentResult := message.Get("content")

				toolResultBlock := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
				toolResultBlock, _ = sjson.SetBytes(toolResultBlock, "tool_use_id", toolCallID)
				if message.Get("_anthropic_native_content").Bool() && toolContentResult.IsArray() {
					// Content is already a Claude-native block array; keep it raw so
					// blocks like image/source and tool_reference are not dropped or
					// stringified by the OpenAI content-part converter.
					toolResultBlock, _ = sjson.SetRawBytes(toolResultBlock, "content", []byte(toolContentResult.Raw))
				} else {
					toolResultContent, toolResultContentRaw := convertOpenAIToolResultContent(toolContentResult)
					if toolResultContentRaw {
						toolResultBlock, _ = sjson.SetRawBytes(toolResultBlock, "content", []byte(toolResultContent))
					} else {
						toolResultBlock, _ = sjson.SetBytes(toolResultBlock, "content", toolResultContent)
					}
				}
				// Reconstruct the Anthropic is_error flag carried from a Cursor
				// tool_result so a failed tool execution stays marked as failed.
				if isErr := message.Get("is_error"); isErr.Exists() && isErr.Bool() {
					toolResultBlock, _ = sjson.SetBytes(toolResultBlock, "is_error", true)
				}

				// Claude expects all tool_result blocks that answer the preceding
				// assistant tool_use turn to be grouped in a single user message.
				// Parallel tool calls arrive as consecutive role:"tool" messages, so
				// append to the previous user/tool_result turn when present instead of
				// emitting a separate user message per result.
				if messageIndex > 0 {
					lastIdx := messageIndex - 1
					lastMsg := gjson.GetBytes(out, "messages."+strconv.Itoa(lastIdx))
					lastContent := lastMsg.Get("content")
					if lastMsg.Get("role").String() == "user" &&
						lastContent.IsArray() && len(lastContent.Array()) > 0 &&
						lastContent.Array()[len(lastContent.Array())-1].Get("type").String() == "tool_result" {
						out, _ = sjson.SetRawBytes(out, "messages."+strconv.Itoa(lastIdx)+".content.-1", toolResultBlock)
						return true
					}
				}

				msg := []byte(`{"role":"user","content":[]}`)
				msg, _ = sjson.SetRawBytes(msg, "content.-1", toolResultBlock)
				out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
				messageIndex++
			}
			return true
		})

		// Preserve a minimal conversational turn for system-only inputs.
		// Claude payloads with top-level system instructions but no messages are risky for downstream validation.
		if messageIndex == 0 {
			system := gjson.GetBytes(out, "system")
			if system.Exists() && system.IsArray() && len(system.Array()) > 0 {
				fallbackMsg := []byte(`{"role":"user","content":[{"type":"text","text":""}]}`)
				out, _ = sjson.SetRawBytes(out, "messages.-1", fallbackMsg)
			}
		}
	}

	// Tools mapping: OpenAI tools -> Claude Code tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() && len(tools.Array()) > 0 {
		hasAnthropicTools := false
		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("type").String() == "function" {
				function := tool.Get("function")
				anthropicTool := []byte(`{"name":"","description":""}`)
				anthropicTool, _ = sjson.SetBytes(anthropicTool, "name", function.Get("name").String())
				anthropicTool, _ = sjson.SetBytes(anthropicTool, "description", function.Get("description").String())

				// Convert parameters schema for the tool
				if parameters := function.Get("parameters"); parameters.Exists() {
					anthropicTool, _ = sjson.SetRawBytes(anthropicTool, "input_schema", []byte(parameters.Raw))
				} else if parameters := function.Get("parametersJsonSchema"); parameters.Exists() {
					anthropicTool, _ = sjson.SetRawBytes(anthropicTool, "input_schema", []byte(parameters.Raw))
				}

				out, _ = sjson.SetRawBytes(out, "tools.-1", anthropicTool)
				hasAnthropicTools = true
			} else if t := tool.Get("type").String(); t != "" {
				// Typed Anthropic server tools (e.g. {"type":"web_search_20250305",...})
				// are already in Claude's native shape. Pass them through unchanged
				// instead of dropping them, so they survive the full conversion.
				out, _ = sjson.SetRawBytes(out, "tools.-1", []byte(tool.Raw))
				hasAnthropicTools = true
			}
			return true
		})

		if !hasAnthropicTools {
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
				functionName := toolChoice.Get("function.name").String()
				toolChoiceJSON := []byte(`{"type":"tool","name":""}`)
				toolChoiceJSON, _ = sjson.SetBytes(toolChoiceJSON, "name", functionName)
				out, _ = sjson.SetRawBytes(out, "tool_choice", toolChoiceJSON)
			}
		default:
		}
	}

	return out
}

// normalizeAnthropicRequestBlocks rewrites Anthropic-native message and tool
// shapes into standard OpenAI Chat Completions shapes. It is a no-op for
// already-valid OpenAI payloads.
//
// Conversions:
//  1. user message content with tool_result blocks -> separate role:"tool"
//     messages ({tool_call_id, content}). Any sibling text blocks are kept as a
//     trailing role:"user" message.
//  2. assistant message content with tool_use blocks -> assistant message with
//     tool_calls[].function (and text blocks merged into content).
//  3. bare tool definitions ({name, description, input_schema}) -> wrapped
//     {type:"function", function:{name, description, parameters}}.
func normalizeAnthropicRequestBlocks(rawJSON []byte) []byte {
	root := gjson.ParseBytes(rawJSON)

	if !anthropicBlocksPresent(root) {
		return rawJSON
	}

	out := rawJSON

	// Rebuild messages array if any message carries Anthropic content blocks.
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		newMessages := "[]"
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			content := message.Get("content")

			if !content.IsArray() || !messageHasAnthropicBlocks(content) {
				newMessages, _ = sjson.SetRaw(newMessages, "-1", message.Raw)
				return true
			}

			switch role {
			case "user":
				textParts := make([]string, 0)
				toolResults := make([]gjson.Result, 0)
				passthrough := make([]gjson.Result, 0)
				content.ForEach(func(_, block gjson.Result) bool {
					switch block.Get("type").String() {
					case "tool_result":
						toolResults = append(toolResults, block)
					case "text":
						if t := block.Get("text").String(); t != "" {
							textParts = append(textParts, t)
						}
					default:
						passthrough = append(passthrough, block)
					}
					return true
				})

				// tool_result blocks become standalone role:"tool" messages so
				// the downstream translator pairs them with the prior tool_calls.
				for _, tr := range toolResults {
					toolMsg := `{"role":"tool","tool_call_id":"","content":""}`
					toolMsg, _ = sjson.Set(toolMsg, "tool_call_id", tr.Get("tool_use_id").String())
					trContent := tr.Get("content")
					if trContent.IsArray() {
						toolMsg, _ = sjson.SetRaw(toolMsg, "content", trContent.Raw)
						// When the array holds Claude-native blocks (e.g. image with
						// source, tool_reference), flag it so the downstream mapper
						// keeps it verbatim instead of routing it through the OpenAI
						// content-part converter, which would drop unknown blocks.
						if toolResultContentIsAnthropicNative(trContent) {
							toolMsg, _ = sjson.Set(toolMsg, "_anthropic_native_content", true)
						}
					} else {
						toolMsg, _ = sjson.Set(toolMsg, "content", flattenAnthropicToolResultText(trContent))
					}
					// Carry the Anthropic is_error flag so the downstream Claude
					// mapper can reconstruct a failed tool_result instead of
					// silently turning a failure into an apparent success.
					if isErr := tr.Get("is_error"); isErr.Exists() && isErr.Bool() {
						toolMsg, _ = sjson.Set(toolMsg, "is_error", true)
					}
					newMessages, _ = sjson.SetRaw(newMessages, "-1", toolMsg)
				}

				// Remaining text / passthrough parts stay as a user message.
				if len(textParts) > 0 || len(passthrough) > 0 {
					userMsg := `{"role":"user","content":[]}`
					for _, t := range textParts {
						textPart := `{"type":"text","text":""}`
						textPart, _ = sjson.Set(textPart, "text", t)
						userMsg, _ = sjson.SetRaw(userMsg, "content.-1", textPart)
					}
					for _, p := range passthrough {
						userMsg, _ = sjson.SetRaw(userMsg, "content.-1", p.Raw)
					}
					newMessages, _ = sjson.SetRaw(newMessages, "-1", userMsg)
				}

			case "assistant":
				textParts := make([]string, 0)
				toolUses := make([]gjson.Result, 0)
				content.ForEach(func(_, block gjson.Result) bool {
					switch block.Get("type").String() {
					case "tool_use":
						toolUses = append(toolUses, block)
					case "text":
						if t := block.Get("text").String(); t != "" {
							textParts = append(textParts, t)
						}
					}
					return true
				})

				asstMsg := `{"role":"assistant","content":""}`
				asstMsg, _ = sjson.Set(asstMsg, "content", strings.Join(textParts, ""))
				if len(toolUses) > 0 {
					asstMsg, _ = sjson.SetRaw(asstMsg, "tool_calls", "[]")
					for _, tu := range toolUses {
						toolCall := `{"id":"","type":"function","function":{"name":"","arguments":"{}"}}`
						toolCall, _ = sjson.Set(toolCall, "id", tu.Get("id").String())
						toolCall, _ = sjson.Set(toolCall, "function.name", tu.Get("name").String())
						if input := tu.Get("input"); input.Exists() && input.IsObject() {
							toolCall, _ = sjson.Set(toolCall, "function.arguments", input.Raw)
						}
						asstMsg, _ = sjson.SetRaw(asstMsg, "tool_calls.-1", toolCall)
					}
				}
				newMessages, _ = sjson.SetRaw(newMessages, "-1", asstMsg)

			default:
				newMessages, _ = sjson.SetRaw(newMessages, "-1", message.Raw)
			}
			return true
		})
		out, _ = sjson.SetRawBytes(out, "messages", []byte(newMessages))
	}

	// Wrap bare Anthropic tool definitions into OpenAI function tools.
	// Only tools with NO "type" field are treated as bare custom tools. Typed
	// Anthropic server tools (e.g. {"type":"web_search_20250305","name":...})
	// are left untouched so the downstream mapper can handle them correctly.
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() && len(tools.Array()) > 0 {
		needsWrap := false
		tools.ForEach(func(_, tool gjson.Result) bool {
			if !tool.Get("type").Exists() && tool.Get("name").Exists() {
				needsWrap = true
				return false
			}
			return true
		})

		if needsWrap {
			newTools := "[]"
			tools.ForEach(func(_, tool gjson.Result) bool {
				// Pass through anything that already has a type (OpenAI function
				// tools and typed Anthropic server tools) unchanged.
				if tool.Get("type").Exists() {
					newTools, _ = sjson.SetRaw(newTools, "-1", tool.Raw)
					return true
				}
				wrapped := `{"type":"function","function":{"name":"","description":""}}`
				wrapped, _ = sjson.Set(wrapped, "function.name", tool.Get("name").String())
				wrapped, _ = sjson.Set(wrapped, "function.description", tool.Get("description").String())
				if schema := tool.Get("input_schema"); schema.Exists() {
					wrapped, _ = sjson.SetRaw(wrapped, "function.parameters", schema.Raw)
				} else if schema := tool.Get("parameters"); schema.Exists() {
					wrapped, _ = sjson.SetRaw(wrapped, "function.parameters", schema.Raw)
				}
				newTools, _ = sjson.SetRaw(newTools, "-1", wrapped)
				return true
			})
			out, _ = sjson.SetRawBytes(out, "tools", []byte(newTools))
		}
	}

	return out
}

// anthropicBlocksPresent reports whether the request carries any Anthropic-native
// message content blocks or bare tool definitions.
func anthropicBlocksPresent(root gjson.Result) bool {
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		found := false
		messages.ForEach(func(_, message gjson.Result) bool {
			if messageHasAnthropicBlocks(message.Get("content")) {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}

	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		found := false
		tools.ForEach(func(_, tool gjson.Result) bool {
			if !tool.Get("type").Exists() && tool.Get("name").Exists() {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}

	return false
}

// messageHasAnthropicBlocks reports whether a message content array contains
// tool_use or tool_result blocks.
func messageHasAnthropicBlocks(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	found := false
	content.ForEach(func(_, block gjson.Result) bool {
		switch block.Get("type").String() {
		case "tool_use", "tool_result":
			found = true
			return false
		}
		return true
	})
	return found
}

// toolResultContentIsAnthropicNative reports whether a tool_result content array
// holds Claude-native blocks that the OpenAI content-part converter does not
// understand (anything other than text / image_url / file). Such arrays must be
// passed through verbatim so blocks like image (with source) or tool_reference
// are not dropped or stringified.
func toolResultContentIsAnthropicNative(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	native := false
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Type == gjson.String {
			return true
		}
		switch block.Get("type").String() {
		case "text", "image_url", "file":
			return true
		default:
			native = true
			return false
		}
	})
	return native
}

// flattenAnthropicToolResultText reduces a tool_result content value to a plain
// string for the non-array case.
func flattenAnthropicToolResultText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	return content.Raw
}

func convertOpenAIContentPartToClaudePart(part gjson.Result) string {
	switch part.Get("type").String() {
	case "text":
		textPart := []byte(`{"type":"text","text":""}`)
		textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
		return string(textPart)

	case "image_url":
		return convertOpenAIImageURLToClaudePart(part.Get("image_url.url").String())

	case "file":
		fileData := part.Get("file.file_data").String()
		if strings.HasPrefix(fileData, "data:") {
			semicolonIdx := strings.Index(fileData, ";")
			commaIdx := strings.Index(fileData, ",")
			if semicolonIdx != -1 && commaIdx != -1 && commaIdx > semicolonIdx {
				mediaType := strings.TrimPrefix(fileData[:semicolonIdx], "data:")
				data := fileData[commaIdx+1:]
				docPart := []byte(`{"type":"document","source":{"type":"base64","media_type":"","data":""}}`)
				docPart, _ = sjson.SetBytes(docPart, "source.media_type", mediaType)
				docPart, _ = sjson.SetBytes(docPart, "source.data", data)
				return string(docPart)
			}
		}
	}

	return ""
}

func convertOpenAIImageURLToClaudePart(imageURL string) string {
	if imageURL == "" {
		return ""
	}

	if strings.HasPrefix(imageURL, "data:") {
		parts := strings.SplitN(imageURL, ",", 2)
		if len(parts) != 2 {
			return ""
		}

		mediaTypePart := strings.SplitN(parts[0], ";", 2)[0]
		mediaType := strings.TrimPrefix(mediaTypePart, "data:")
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}

		imagePart := []byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`)
		imagePart, _ = sjson.SetBytes(imagePart, "source.media_type", mediaType)
		imagePart, _ = sjson.SetBytes(imagePart, "source.data", parts[1])
		return string(imagePart)
	}

	imagePart := []byte(`{"type":"image","source":{"type":"url","url":""}}`)
	imagePart, _ = sjson.SetBytes(imagePart, "source.url", imageURL)
	return string(imagePart)
}

func convertOpenAIToolResultContent(content gjson.Result) (string, bool) {
	if !content.Exists() {
		return "", false
	}

	if content.Type == gjson.String {
		return content.String(), false
	}

	if content.IsArray() {
		claudeContent := []byte("[]")
		partCount := 0

		content.ForEach(func(_, part gjson.Result) bool {
			if part.Type == gjson.String {
				textPart := []byte(`{"type":"text","text":""}`)
				textPart, _ = sjson.SetBytes(textPart, "text", part.String())
				claudeContent, _ = sjson.SetRawBytes(claudeContent, "-1", textPart)
				partCount++
				return true
			}

			claudePart := convertOpenAIContentPartToClaudePart(part)
			if claudePart != "" {
				claudeContent, _ = sjson.SetRawBytes(claudeContent, "-1", []byte(claudePart))
				partCount++
			}
			return true
		})

		if partCount > 0 || len(content.Array()) == 0 {
			return string(claudeContent), true
		}

		return content.Raw, false
	}

	if content.IsObject() {
		claudePart := convertOpenAIContentPartToClaudePart(content)
		if claudePart != "" {
			claudeContent := []byte("[]")
			claudeContent, _ = sjson.SetRawBytes(claudeContent, "-1", []byte(claudePart))
			return string(claudeContent), true
		}
		return content.Raw, false
	}

	return content.Raw, false
}
