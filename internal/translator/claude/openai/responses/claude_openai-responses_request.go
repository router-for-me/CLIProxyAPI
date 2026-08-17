// Package responses provides request translation functionality for OpenAI Responses to Claude API compatibility.
// It converts OpenAI Responses API requests into Claude compatible JSON using gjson/sjson only.
package responses

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
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

// ConvertOpenAIResponsesRequestToClaude transforms an OpenAI Responses API request
// into a Claude Messages API request using only gjson/sjson for JSON handling.
// It supports:
//   - instructions, input[].role==system and input[].role==developer -> separate
//     top-level system blocks, in source order
//   - input[].type==message with input_text/output_text -> user/assistant messages
//   - function_call/custom_tool_call -> assistant tool_use
//   - function_call_output/custom_tool_call_output -> user tool_result
//   - top-level tools and input[].additional_tools -> Claude tools[].input_schema
//   - max_output_tokens -> max_tokens
//   - stream passthrough via parameter
func ConvertOpenAIResponsesRequestToClaude(modelName string, inputRawJSON []byte, stream bool) []byte {
	return convertOpenAIResponsesRequestToClaude(modelName, inputRawJSON, stream, false)
}

// ConvertOpenAIResponsesRequestToClaudeWithCompat preserves reasoning items
// whose encrypted content is empty for configured compatibility endpoints.
func ConvertOpenAIResponsesRequestToClaudeWithCompat(modelName string, inputRawJSON []byte, stream bool) []byte {
	return convertOpenAIResponsesRequestToClaude(modelName, inputRawJSON, stream, true)
}

func convertOpenAIResponsesRequestToClaude(modelName string, inputRawJSON []byte, stream, preserveEmptyThinkingBlocks bool) []byte {
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

	out := []byte(fmt.Sprintf(`{"model":"","max_tokens":32000,"messages":[],"metadata":{"user_id":"%s"}}`, userID))
	root := gjson.ParseBytes(rawJSON)

	out, _ = sjson.SetBytes(out, "model", modelName)

	// OpenAI Responses service_tier -> Claude speed
	// "priority" maps to "fast", other/absent values leave speed unset.
	if st := root.Get("service_tier"); st.Exists() && strings.EqualFold(strings.TrimSpace(st.String()), "priority") {
		out, _ = sjson.SetBytes(out, "speed", "fast")
	}

	// max_output_tokens -> max_tokens
	if maxTokens := root.Get("max_output_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}

	// top_p
	if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.SetBytes(out, "top_p", topP.Float())
	}

	// temperature
	if temperature := root.Get("temperature"); temperature.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", temperature.Float())
	}

	// stop_sequences
	if stop := root.Get("stop_sequences"); stop.Exists() && stop.IsArray() {
		var stopSequences []string
		stop.ForEach(func(_, value gjson.Result) bool {
			stopSequences = append(stopSequences, value.String())
			return true
		})
		if len(stopSequences) > 0 {
			out, _ = sjson.SetBytes(out, "stop_sequences", stopSequences)
		}
	}

	// stream
	out, _ = sjson.SetBytes(out, "stream", stream)

	// thinking configuration from reasoning_effort
	if effort := root.Get("reasoning_effort"); effort.Exists() {
		switch strings.ToLower(strings.TrimSpace(effort.String())) {
		case "none":
			out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
		case "auto":
			out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
		case "low", "medium", "high":
			out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
			out, _ = sjson.SetBytes(out, "output_config.effort", strings.ToLower(strings.TrimSpace(effort.String())))
		}
	}

	genToolCallID := func() string {
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		var b strings.Builder
		for i := 0; i < 24; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b.WriteByte(letters[n.Int64()])
		}
		return "toolu_" + b.String()
	}

	// System blocks accumulator (instructions + input[role==system|developer])
	systemBlocks := make([][]byte, 0)
	if instructions := root.Get("instructions"); instructions.Exists() && instructions.String() != "" {
		textPart := []byte(`{"type":"text","text":""}`)
		textPart, _ = sjson.SetBytes(textPart, "text", instructions.String())
		systemBlocks = append(systemBlocks, textPart)
	}

	messageAccumulator := common.NewClaudeMessageAccumulator(int(root.Get("input.#").Int()))

	// Process input array
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			itemType := item.Get("type").String()
			role := item.Get("role").String()

			// Check for system/developer messages in input array
			if role == "system" || role == "developer" {
				systemStart := len(systemBlocks)
				content := item.Get("content")
				if content.Exists() && content.Type == gjson.String && content.String() != "" {
					textPart := []byte(`{"type":"text","text":""}`)
					textPart, _ = sjson.SetBytes(textPart, "text", content.String())
					textPart = common.AttachCacheControl(textPart, item)
					systemBlocks = append(systemBlocks, textPart)
				} else if content.Exists() && content.IsArray() {
					content.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() == "text" || part.Get("type").String() == "input_text" {
							textPart := []byte(`{"type":"text","text":""}`)
							textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
							textPart = common.AttachCacheControl(textPart, part)
							systemBlocks = append(systemBlocks, textPart)
						}
						return true
					})
				}
				if len(systemBlocks) > systemStart && !gjson.GetBytes(systemBlocks[len(systemBlocks)-1], "cache_control").Exists() {
					systemBlocks[len(systemBlocks)-1] = common.AttachCacheControl(systemBlocks[len(systemBlocks)-1], item)
				}
				return true
			}

			switch itemType {
			case "message":
				switch role {
				case "user":
					userBlocks := make([][]byte, 0)
					userStart := len(userBlocks)
					content := item.Get("content")

					if content.Exists() && content.Type == gjson.String && content.String() != "" {
						textPart := []byte(`{"type":"text","text":""}`)
						textPart, _ = sjson.SetBytes(textPart, "text", content.String())
						textPart = common.AttachCacheControl(textPart, item)
						userBlocks = append(userBlocks, textPart)
					} else if content.Exists() && content.IsArray() {
						content.ForEach(func(_, part gjson.Result) bool {
							partType := part.Get("type").String()
							switch partType {
							case "text", "input_text":
								textPart := []byte(`{"type":"text","text":""}`)
								textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
								textPart = common.AttachCacheControl(textPart, part)
								userBlocks = append(userBlocks, textPart)
							case "input_image", "image":
								url := part.Get("image_url").String()
								if url == "" {
									url = part.Get("url").String()
								}
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
						userBlocks[len(userBlocks)-1] = common.AttachCacheControl(userBlocks[len(userBlocks)-1], item)
					}

					if len(userBlocks) > 0 {
						messageAccumulator.AppendUserBlocks(userBlocks)
					}

				case "assistant":
					assistantBlocks := make([][]byte, 0)
					assistantStart := len(assistantBlocks)
					content := item.Get("content")

					if content.Exists() && content.Type == gjson.String && content.String() != "" {
						textPart := []byte(`{"type":"text","text":""}`)
						textPart, _ = sjson.SetBytes(textPart, "text", content.String())
						textPart = common.AttachCacheControl(textPart, item)
						assistantBlocks = append(assistantBlocks, textPart)
					} else if content.Exists() && content.IsArray() {
						content.ForEach(func(_, part gjson.Result) bool {
							partType := part.Get("type").String()
							switch partType {
							case "text", "output_text":
								textPart := []byte(`{"type":"text","text":""}`)
								textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
								textPart = common.AttachCacheControl(textPart, part)
								assistantBlocks = append(assistantBlocks, textPart)
							}
							return true
						})
					}

					if len(assistantBlocks) > assistantStart && !gjson.GetBytes(assistantBlocks[len(assistantBlocks)-1], "cache_control").Exists() {
						assistantBlocks[len(assistantBlocks)-1] = common.AttachCacheControl(assistantBlocks[len(assistantBlocks)-1], item)
					}

					if len(assistantBlocks) > 0 {
						messageAccumulator.AppendAssistantBlocks(assistantBlocks)
					}
				}

			case "function_call", "custom_tool_call":
				callID := item.Get("call_id").String()
				if callID == "" {
					callID = item.Get("id").String()
				}
				if callID == "" {
					callID = genToolCallID()
				}
				callID = common.SanitizeToolCallIDForClaude(callID)

				name := item.Get("name").String()
				if name == "" {
					name = item.Get("function.name").String()
				}

				toolUsePart := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
				toolUsePart, _ = sjson.SetBytes(toolUsePart, "id", callID)
				toolUsePart, _ = sjson.SetBytes(toolUsePart, "name", name)

				arguments := item.Get("arguments").String()
				if arguments == "" {
					arguments = item.Get("function.arguments").String()
				}
				if arguments != "" {
					var inputObj map[string]any
					if err := util.UnmarshalJSONCaseFold([]byte(arguments), &inputObj); err == nil {
						toolUsePart, _ = sjson.SetBytes(toolUsePart, "input", inputObj)
					}
				}

				toolUsePart = common.AttachCacheControl(toolUsePart, item)
				messageAccumulator.AppendAssistantBlocks([][]byte{toolUsePart})

			case "function_call_output", "custom_tool_call_output":
				callID := item.Get("call_id").String()
				if callID != "" {
					callID = common.SanitizeToolCallIDForClaude(callID)
					toolResultPart := []byte(`{"type":"tool_result","tool_use_id":""}`)
					toolResultPart, _ = sjson.SetBytes(toolResultPart, "tool_use_id", callID)

					output := item.Get("output")
					if output.Exists() {
						toolResultPart = common.AppendClaudeToolResultContent(toolResultPart, output)
					}
					toolResultPart = common.AttachCacheControl(toolResultPart, item)
					messageAccumulator.AppendToolResult(toolResultPart)
				}

			case "reasoning":
				// Convert reasoning item to Claude thinking block if valid signature exists
				rawSignatureResult := item.Get("encrypted_content")
				rawSignature := rawSignatureResult.String()
				signature, ok := sigcompat.CompatibleAntigravityClaudeThinkingSignature(rawSignature)
				if !ok && rawSignatureResult.Exists() && !preserveEmptyThinkingBlocks {
					// Incompatible signature -> drop the thinking block entirely
					return true
				}

				// Extract reasoning text
				thoughtText := ""
				if summary := item.Get("summary"); summary.Exists() && summary.IsArray() {
					var summaryTexts []string
					summary.ForEach(func(_, s gjson.Result) bool {
						if t := s.Get("text").String(); t != "" {
							summaryTexts = append(summaryTexts, t)
						}
						return true
					})
					thoughtText = strings.Join(summaryTexts, "\n")
				}
				if thoughtText == "" {
					thoughtText = item.Get("reasoning_content.text").String()
				}

				if thoughtText != "" || signature != "" {
					thinkingPart := []byte(`{"type":"thinking","thinking":"","signature":""}`)
					thinkingPart, _ = sjson.SetBytes(thinkingPart, "thinking", thoughtText)
					if signature != "" {
						thinkingPart, _ = sjson.SetBytes(thinkingPart, "signature", signature)
					} else {
						thinkingPart, _ = sjson.DeleteBytes(thinkingPart, "signature")
					}
					thinkingPart = common.AttachCacheControl(thinkingPart, item)
					messageAccumulator.AppendAssistantBlocks([][]byte{thinkingPart})
				}
			}
			return true
		})

		messageBlocks := messageAccumulator.Messages()
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

	// Tools mapping: top-level tools + input[].additional_tools -> Claude tools
	anthropicTools := collectOpenAIResponsesToolsForClaude(root)
	if len(anthropicTools) > 0 {
		out, _ = sjson.SetRawBytes(out, "tools", common.JoinRawArray(anthropicTools))
	} else {
		out, _ = sjson.DeleteBytes(out, "tools")
	}

	// tool_choice
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		switch toolChoice.Type {
		case gjson.String:
			switch strings.ToLower(toolChoice.String()) {
			case "none":
				// omit tool_choice
			case "auto":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto"}`))
			case "required":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"any"}`))
			}
		case gjson.JSON:
			if toolChoice.Get("type").String() == "function" {
				fnName := toolChoice.Get("function.name").String()
				if fnName == "" {
					fnName = toolChoice.Get("name").String()
				}
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

func collectOpenAIResponsesToolsForClaude(root gjson.Result) [][]byte {
	var anthropicTools [][]byte
	seenNames := make(map[string]struct{})

	appendTool := func(toolBytes []byte, name string) {
		if len(toolBytes) == 0 || name == "" {
			return
		}
		if _, exists := seenNames[name]; exists {
			return
		}
		seenNames[name] = struct{}{}
		anthropicTools = append(anthropicTools, toolBytes)
	}

	// 1. Top-level tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			processResponsesToolEntryForClaude(tool, "", appendTool)
			return true
		})
	}

	// 2. input[].additional_tools
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				if tools := item.Get("tools"); tools.Exists() && tools.IsArray() {
					tools.ForEach(func(_, tool gjson.Result) bool {
						processResponsesToolEntryForClaude(tool, "", appendTool)
						return true
					})
				}
			}
			return true
		})
	}

	return anthropicTools
}

func processResponsesToolEntryForClaude(tool gjson.Result, namespacePrefix string, emit func([]byte, string)) {
	toolType := tool.Get("type").String()

	switch toolType {
	case "namespace":
		nsName := strings.TrimSpace(tool.Get("name").String())
		if nsName == "" {
			return
		}
		nestedPrefix := nsName
		if namespacePrefix != "" {
			nestedPrefix = namespacePrefix + "__" + nsName
		}
		if subTools := tool.Get("tools"); subTools.Exists() && subTools.IsArray() {
			subTools.ForEach(func(_, subTool gjson.Result) bool {
				processResponsesToolEntryForClaude(subTool, nestedPrefix, emit)
				return true
			})
		}

	case "function":
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(tool.Get("function.name").String())
		}
		if namespacePrefix != "" && name != "" {
			name = namespacePrefix + "__" + name
		}
		if toolBytes, ok := convertResponsesFunctionToolToClaude(tool, name); ok {
			emit(toolBytes, name)
		}

	case "custom":
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "apply_patch" {
			// drop apply_patch custom tool as per specifications
			return
		}
		if namespacePrefix != "" && name != "" {
			name = namespacePrefix + "__" + name
		}
		if toolBytes, ok := convertResponsesCustomToolToClaude(tool, name); ok {
			emit(toolBytes, name)
		}

	case "web_search", "web_search_20250305":
		if toolBytes, ok := convertResponsesWebSearchToolToClaude(tool); ok {
			name := gjson.GetBytes(toolBytes, "name").String()
			emit(toolBytes, name)
		}

	default:
		// Fallback: check if it has a function or custom shape
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(tool.Get("function.name").String())
		}
		if name != "" {
			if namespacePrefix != "" {
				name = namespacePrefix + "__" + name
			}
			if toolBytes, ok := convertResponsesFunctionToolToClaude(tool, name); ok {
				emit(toolBytes, name)
			}
		}
	}
}

func convertResponsesFunctionToolToClaude(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}

	sanitizedName := util.SanitizeClaudeFunctionName(name)
	tJSON := []byte(`{"name":"","description":"","input_schema":{"type":"object","properties":{}}}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", sanitizedName)
	if d := responsesToolDescription(tool); d != "" {
		tJSON, _ = sjson.SetBytes(tJSON, "description", d)
	}
	if params := responsesToolParameters(tool); params.Exists() && params.Raw != "" && params.Raw != "null" && params.Raw != "{}" {
		tJSON, _ = sjson.SetRawBytes(tJSON, "input_schema", util.NormalizeClaudeToolInputSchema([]byte(params.Raw)))
	}
	tJSON = common.AttachCacheControl(tJSON, tool)
	if !gjson.GetBytes(tJSON, "cache_control").Exists() {
		tJSON = common.AttachCacheControl(tJSON, tool.Get("function"))
	}
	return tJSON, true
}

func convertResponsesCustomToolToClaude(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}

	tJSON := []byte(`{"name":"","description":"","input_schema":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", name)
	if description := responsesToolDescription(tool); description != "" {
		tJSON, _ = sjson.SetBytes(tJSON, "description", description)
	}
	tJSON = common.AttachCacheControl(tJSON, tool)
	return tJSON, true
}

func convertResponsesWebSearchToolToClaude(tool gjson.Result) ([]byte, bool) {
	if externalWebAccess := tool.Get("external_web_access"); externalWebAccess.Exists() && !externalWebAccess.Bool() {
		return nil, false
	}

	name := strings.TrimSpace(tool.Get("name").String())
	if name == "" {
		name = "web_search"
	}
	tJSON := []byte(`{"type":"web_search_20250305","name":""}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", name)
	if maxUses := tool.Get("max_uses"); maxUses.Exists() {
		tJSON, _ = sjson.SetBytes(tJSON, "max_uses", maxUses.Int())
	}
	if allowedDomains := tool.Get("filters.allowed_domains"); allowedDomains.Exists() && allowedDomains.IsArray() {
		tJSON, _ = sjson.SetRawBytes(tJSON, "allowed_domains", []byte(allowedDomains.Raw))
	}
	if userLocation := tool.Get("user_location"); userLocation.Exists() && userLocation.IsObject() {
		tJSON, _ = sjson.SetRawBytes(tJSON, "user_location", []byte(userLocation.Raw))
	}
	return tJSON, true
}

func responsesToolName(tool gjson.Result) string {
	if name := tool.Get("name").String(); name != "" {
		return strings.TrimSpace(name)
	}
	return strings.TrimSpace(tool.Get("function.name").String())
}

func responsesToolDescription(tool gjson.Result) string {
	if description := tool.Get("description").String(); description != "" {
		return description
	}
	return tool.Get("function.description").String()
}

func responsesToolParameters(tool gjson.Result) gjson.Result {
	for _, path := range []string{
		"parameters",
		"parametersJsonSchema",
		"input_schema",
		"function.parameters",
		"function.parametersJsonSchema",
	} {
		if parameters := tool.Get(path); parameters.Exists() {
			return parameters
		}
	}
	return gjson.Result{}
}
