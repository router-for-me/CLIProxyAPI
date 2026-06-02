package responses

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
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
// - instructions -> system message
// - input[].type==message with input_text/output_text -> user/assistant messages
// - function_call -> assistant tool_use
// - function_call_output -> user tool_result
// - tools[].parameters -> tools[].input_schema
// - max_output_tokens -> max_tokens
// - stream passthrough via parameter
func ConvertOpenAIResponsesRequestToClaude(modelName string, inputRawJSON []byte, stream bool) []byte {
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

	// Base Claude message payload
	out := []byte(fmt.Sprintf(`{"model":"","max_tokens":32000,"messages":[],"metadata":{"user_id":"%s"}}`, userID))

	// toolNameMap maps the *Anthropic-safe* name (i.e. what we send upstream)
	// back to the original Codex tool name. Populated both when we sanitize a
	// history function_call name and when a namespace tool's qualified name
	// gets sanitized. Used downstream by tool_choice + by the response-side
	// translator via setMcpToolNameOnItem to recover the original name.
	toolNameMap := map[string]string{}

	root := gjson.ParseBytes(rawJSON)

	// Convert OpenAI Responses reasoning.effort to Claude thinking config.
	if v := root.Get("reasoning.effort"); v.Exists() {
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

	// Helper for generating tool call IDs when missing
	genToolCallID := func() string {
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		var b strings.Builder
		for i := 0; i < 24; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b.WriteByte(letters[n.Int64()])
		}
		return "toolu_" + b.String()
	}

	// Model
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Max tokens
	if mot := root.Get("max_output_tokens"); mot.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", mot.Int())
	}

	// Stream
	out, _ = sjson.SetBytes(out, "stream", stream)

	// instructions -> as a leading message (use role user for Claude API compatibility)
	instructionsText := ""
	extractedFromSystem := false
	if instr := root.Get("instructions"); instr.Exists() && instr.Type == gjson.String {
		instructionsText = instr.String()
		if instructionsText != "" {
			sysMsg := []byte(`{"role":"user","content":""}`)
			sysMsg, _ = sjson.SetBytes(sysMsg, "content", instructionsText)
			out, _ = sjson.SetRawBytes(out, "messages.-1", sysMsg)
		}
	}

	if instructionsText == "" {
		if input := root.Get("input"); input.Exists() && input.IsArray() {
			input.ForEach(func(_, item gjson.Result) bool {
				if strings.EqualFold(item.Get("role").String(), "system") {
					var builder strings.Builder
					if parts := item.Get("content"); parts.Exists() && parts.IsArray() {
						parts.ForEach(func(_, part gjson.Result) bool {
							textResult := part.Get("text")
							text := textResult.String()
							if builder.Len() > 0 && text != "" {
								builder.WriteByte('\n')
							}
							builder.WriteString(text)
							return true
						})
					} else if parts.Type == gjson.String {
						builder.WriteString(parts.String())
					}
					instructionsText = builder.String()
					if instructionsText != "" {
						sysMsg := []byte(`{"role":"user","content":""}`)
						sysMsg, _ = sjson.SetBytes(sysMsg, "content", instructionsText)
						out, _ = sjson.SetRawBytes(out, "messages.-1", sysMsg)
						extractedFromSystem = true
					}
				}
				return instructionsText == ""
			})
		}
	}

	// input array processing
	var pendingReasoningParts []string
	flushPendingReasoning := func() {
		if len(pendingReasoningParts) == 0 {
			return
		}
		asst := []byte(`{"role":"assistant","content":[]}`)
		for _, partJSON := range pendingReasoningParts {
			asst, _ = sjson.SetRawBytes(asst, "content.-1", []byte(partJSON))
		}
		out, _ = sjson.SetRawBytes(out, "messages.-1", asst)
		pendingReasoningParts = nil
	}

	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if extractedFromSystem && strings.EqualFold(item.Get("role").String(), "system") {
				return true
			}
			typ := item.Get("type").String()
			if typ == "" && item.Get("role").String() != "" {
				typ = "message"
			}
			switch typ {
			case "message":
				// Determine role and construct Claude-compatible content parts.
				var role string
				var textAggregate strings.Builder
				var partsJSON []string
				hasImage := false
				hasFile := false
				if parts := item.Get("content"); parts.Exists() && parts.IsArray() {
					parts.ForEach(func(_, part gjson.Result) bool {
						ptype := part.Get("type").String()
						switch ptype {
						case "input_text", "output_text":
							if t := part.Get("text"); t.Exists() {
								txt := t.String()
								textAggregate.WriteString(txt)
								contentPart := []byte(`{"type":"text","text":""}`)
								contentPart, _ = sjson.SetBytes(contentPart, "text", txt)
								partsJSON = append(partsJSON, string(contentPart))
							}
							if ptype == "input_text" {
								role = "user"
							} else {
								role = "assistant"
							}
						case "input_image":
							url := part.Get("image_url").String()
							if url == "" {
								url = part.Get("url").String()
							}
							if url != "" {
								var contentPart []byte
								if strings.HasPrefix(url, "data:") {
									trimmed := strings.TrimPrefix(url, "data:")
									mediaAndData := strings.SplitN(trimmed, ";base64,", 2)
									mediaType := "application/octet-stream"
									data := ""
									if len(mediaAndData) == 2 {
										if mediaAndData[0] != "" {
											mediaType = mediaAndData[0]
										}
										data = mediaAndData[1]
									}
									if data != "" {
										contentPart = []byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`)
										contentPart, _ = sjson.SetBytes(contentPart, "source.media_type", mediaType)
										contentPart, _ = sjson.SetBytes(contentPart, "source.data", data)
									}
								} else {
									contentPart = []byte(`{"type":"image","source":{"type":"url","url":""}}`)
									contentPart, _ = sjson.SetBytes(contentPart, "source.url", url)
								}
								if len(contentPart) > 0 {
									partsJSON = append(partsJSON, string(contentPart))
									if role == "" {
										role = "user"
									}
									hasImage = true
								}
							}
						case "input_file":
							fileData := part.Get("file_data").String()
							if fileData != "" {
								mediaType := "application/octet-stream"
								data := fileData
								if strings.HasPrefix(fileData, "data:") {
									trimmed := strings.TrimPrefix(fileData, "data:")
									mediaAndData := strings.SplitN(trimmed, ";base64,", 2)
									if len(mediaAndData) == 2 {
										if mediaAndData[0] != "" {
											mediaType = mediaAndData[0]
										}
										data = mediaAndData[1]
									}
								}
								contentPart := []byte(`{"type":"document","source":{"type":"base64","media_type":"","data":""}}`)
								contentPart, _ = sjson.SetBytes(contentPart, "source.media_type", mediaType)
								contentPart, _ = sjson.SetBytes(contentPart, "source.data", data)
								partsJSON = append(partsJSON, string(contentPart))
								if role == "" {
									role = "user"
								}
								hasFile = true
							}
						}
						return true
					})
				} else if parts.Type == gjson.String {
					textAggregate.WriteString(parts.String())
				}

				// Fallback to given role if content types not decisive
				if role == "" {
					r := item.Get("role").String()
					switch r {
					case "user", "assistant", "system":
						role = r
					default:
						role = "user"
					}
				}

				hasReasoningParts := false
				if len(pendingReasoningParts) > 0 {
					if role == "assistant" {
						if len(partsJSON) == 0 && textAggregate.Len() > 0 {
							contentPart := []byte(`{"type":"text","text":""}`)
							contentPart, _ = sjson.SetBytes(contentPart, "text", textAggregate.String())
							partsJSON = append(partsJSON, string(contentPart))
						}
						partsJSON = append(append([]string{}, pendingReasoningParts...), partsJSON...)
						pendingReasoningParts = nil
						hasReasoningParts = true
					} else {
						flushPendingReasoning()
					}
				}

				if len(partsJSON) > 0 {
					msg := []byte(`{"role":"","content":[]}`)
					msg, _ = sjson.SetBytes(msg, "role", role)
					if len(partsJSON) == 1 && !hasImage && !hasFile && !hasReasoningParts {
						// Preserve legacy behavior for single text content
						msg, _ = sjson.DeleteBytes(msg, "content")
						textPart := gjson.Parse(partsJSON[0])
						msg, _ = sjson.SetBytes(msg, "content", textPart.Get("text").String())
					} else {
						for _, partJSON := range partsJSON {
							msg, _ = sjson.SetRawBytes(msg, "content.-1", []byte(partJSON))
						}
					}
					out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
				} else if textAggregate.Len() > 0 || role == "system" {
					msg := []byte(`{"role":"","content":""}`)
					msg, _ = sjson.SetBytes(msg, "role", role)
					msg, _ = sjson.SetBytes(msg, "content", textAggregate.String())
					out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
				}

			case "reasoning":
				if thinkingPart := convertResponsesReasoningToClaudeThinking(item); len(thinkingPart) > 0 {
					pendingReasoningParts = append(pendingReasoningParts, string(thinkingPart))
				}

			case "function_call":
				// Map to assistant tool_use
				callID := item.Get("call_id").String()
				if callID == "" {
					callID = genToolCallID()
				}
				rawName := item.Get("name").String()
				if ns := item.Get("namespace").String(); ns != "" {
					rawName = qualifyResponsesNamespaceToolName(ns, rawName)
				}
				name := sanitizeAnthropicToolName(rawName)
				if name != rawName {
					toolNameMap[name] = rawName
				}
				argsStr := item.Get("arguments").String()

				toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
				toolUse, _ = sjson.SetBytes(toolUse, "id", callID)
				toolUse, _ = sjson.SetBytes(toolUse, "name", name)
				if argsStr != "" && gjson.Valid(argsStr) {
					argsJSON := gjson.Parse(argsStr)
					if argsJSON.IsObject() {
						toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(argsJSON.Raw))
					}
				}

				// FIX (parallel-tool-merge): If the last message in the array is already
				// an assistant message whose content is an array (i.e., a tool_use container),
				// append this tool_use to that message instead of creating a new assistant
				// message. This avoids producing consecutive same-role messages, which violate
				// the Anthropic Messages API requirement that user/assistant strictly alternate
				// and which AWS Bedrock rejects as TOOL_USE_RESULT_MISMATCH (HTTP 400).
				appendedToolUse := false
				if msgsCount := gjson.GetBytes(out, "messages.#").Int(); msgsCount > 0 {
					lastIdx := msgsCount - 1
					lastRole := gjson.GetBytes(out, fmt.Sprintf("messages.%d.role", lastIdx)).String()
					lastContent := gjson.GetBytes(out, fmt.Sprintf("messages.%d.content", lastIdx))
					if lastRole == "assistant" {
						// If the previous assistant message stored its content as a
						// plain string (single-text case), promote it to an array so
						// the tool_use can be appended instead of forcing a new
						// consecutive assistant message (which Bedrock rejects with 400).
						if lastContent.Type == gjson.String {
							textBlock := []byte(`{"type":"text","text":""}`)
							textBlock, _ = sjson.SetBytes(textBlock, "text", lastContent.String())
							out, _ = sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content", lastIdx), []byte("[]"))
							out, _ = sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content.-1", lastIdx), textBlock)
							lastContent = gjson.GetBytes(out, fmt.Sprintf("messages.%d.content", lastIdx))
						}
						if lastContent.IsArray() {
							// Flush any pending reasoning parts into this same assistant
							// message *before* the tool_use, so the reasoning stays attached
							// to the merged tool call instead of being emitted as a separate
							// assistant message between the tool_use and its tool_result.
							for _, partJSON := range pendingReasoningParts {
								out, _ = sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content.-1", lastIdx), []byte(partJSON))
							}
							pendingReasoningParts = nil
							out, _ = sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content.-1", lastIdx), toolUse)
							appendedToolUse = true
						}
					}
				}
				if !appendedToolUse {
					asst := []byte(`{"role":"assistant","content":[]}`)
					// upstream reasoning-signature: flush any pending reasoning parts
					// into the new assistant message before the tool_use.
					for _, partJSON := range pendingReasoningParts {
						asst, _ = sjson.SetRawBytes(asst, "content.-1", []byte(partJSON))
					}
					pendingReasoningParts = nil
					asst, _ = sjson.SetRawBytes(asst, "content.-1", toolUse)
					out, _ = sjson.SetRawBytes(out, "messages.-1", asst)
				}

			case "function_call_output":
				flushPendingReasoning()
				// Map to user tool_result
				callID := item.Get("call_id").String()
				toolResult := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
				toolResult, _ = sjson.SetBytes(toolResult, "tool_use_id", callID)
				output := item.Get("output")
				var resultParts []string
				if output.IsArray() {
					output.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() == "input_image" {
							url := part.Get("image_url").String()
							if url == "" {
								url = part.Get("url").String()
							}
							if url == "" {
								return true
							}
							var contentPart []byte
							if strings.HasPrefix(url, "data:") {
								trimmed := strings.TrimPrefix(url, "data:")
								mediaAndData := strings.SplitN(trimmed, ";base64,", 2)
								mediaType := "application/octet-stream"
								data := ""
								if len(mediaAndData) == 2 {
									if mediaAndData[0] != "" {
										mediaType = mediaAndData[0]
									}
									data = mediaAndData[1]
								}
								if data != "" {
									contentPart = []byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`)
									contentPart, _ = sjson.SetBytes(contentPart, "source.media_type", mediaType)
									contentPart, _ = sjson.SetBytes(contentPart, "source.data", data)
								}
							} else {
								contentPart = []byte(`{"type":"image","source":{"type":"url","url":""}}`)
								contentPart, _ = sjson.SetBytes(contentPart, "source.url", url)
							}
							if len(contentPart) > 0 {
								resultParts = append(resultParts, string(contentPart))
							}
						} else if t := part.Get("text"); t.Exists() {
							contentPart := []byte(`{"type":"text","text":""}`)
							contentPart, _ = sjson.SetBytes(contentPart, "text", t.String())
							resultParts = append(resultParts, string(contentPart))
						}
						return true
					})
				}
				if len(resultParts) > 0 {
					toolResult, _ = sjson.SetRawBytes(toolResult, "content", []byte("[]"))
					for _, partJSON := range resultParts {
						toolResult, _ = sjson.SetRawBytes(toolResult, "content.-1", []byte(partJSON))
					}
				} else {
					toolResult, _ = sjson.SetBytes(toolResult, "content", output.String())
				}

				// FIX (parallel-tool-merge): Mirror of the function_call branch.
				// If the last message is already a user message whose content is an array
				// (i.e., a tool_result container), append this tool_result instead of creating
				// a new user message, to keep user/assistant strictly alternating.
				appendedToolResult := false
				if msgsCount := gjson.GetBytes(out, "messages.#").Int(); msgsCount > 0 {
					lastIdx := msgsCount - 1
					lastRole := gjson.GetBytes(out, fmt.Sprintf("messages.%d.role", lastIdx)).String()
					lastContent := gjson.GetBytes(out, fmt.Sprintf("messages.%d.content", lastIdx))
					if lastRole == "user" {
						// Promote a string-valued user content to an array first, so the
						// tool_result merges in instead of creating a consecutive user message.
						if lastContent.Type == gjson.String {
							textBlock := []byte(`{"type":"text","text":""}`)
							textBlock, _ = sjson.SetBytes(textBlock, "text", lastContent.String())
							out, _ = sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content", lastIdx), []byte("[]"))
							out, _ = sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content.-1", lastIdx), textBlock)
							lastContent = gjson.GetBytes(out, fmt.Sprintf("messages.%d.content", lastIdx))
						}
						if lastContent.IsArray() {
							out, _ = sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content.-1", lastIdx), toolResult)
							appendedToolResult = true
						}
					}
				}
				if !appendedToolResult {
					usr := []byte(`{"role":"user","content":[]}`)
					usr, _ = sjson.SetRawBytes(usr, "content.-1", toolResult)
					out, _ = sjson.SetRawBytes(out, "messages.-1", usr)
				}
			}
			return true
		})
	}
	flushPendingReasoning()

	includedToolNames := map[string]struct{}{}

	// tools mapping: parameters -> input_schema
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		toolsJSON := []byte("[]")
		tools.ForEach(func(_, tool gjson.Result) bool {
			convertedTools := convertResponsesToolToClaudeTools(tool, toolNameMap)
			for _, tJSON := range convertedTools {
				toolName := gjson.GetBytes(tJSON, "name").String()
				if toolName != "" {
					includedToolNames[toolName] = struct{}{}
				}
				toolsJSON, _ = sjson.SetRawBytes(toolsJSON, "-1", tJSON)
			}
			return true
		})
		if parsedTools := gjson.ParseBytes(toolsJSON); parsedTools.IsArray() && len(parsedTools.Array()) > 0 {
			out, _ = sjson.SetRawBytes(out, "tools", toolsJSON)
		}
	}

	// Map tool_choice similar to Chat Completions translator (optional in docs, safe to handle)
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		switch toolChoice.Type {
		case gjson.String:
			switch toolChoice.String() {
			case "auto":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto"}`))
			case "none":
				// Leave unset; implies no tools
			case "required":
				if len(includedToolNames) > 0 {
					out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"any"}`))
				}
			}
		case gjson.JSON:
			if toolChoice.Get("type").String() == "function" {
				fn := toolChoice.Get("function.name").String()
				if fn == "" {
					fn = toolChoice.Get("name").String()
				}
				if mappedName := toolNameMap[fn]; mappedName != "" {
					fn = mappedName
				}
				if _, ok := includedToolNames[fn]; ok {
					toolChoiceJSON := []byte(`{"name":"","type":"tool"}`)
					toolChoiceJSON, _ = sjson.SetBytes(toolChoiceJSON, "name", sanitizeAnthropicToolName(fn))
					out, _ = sjson.SetRawBytes(out, "tool_choice", toolChoiceJSON)
				}
			}
		default:

		}
	}

	return out
}

func convertResponsesReasoningToClaudeThinking(item gjson.Result) []byte {
	signature, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderClaude, item.Get("encrypted_content").String())
	if !ok {
		return nil
	}

	thinkingText := responsesReasoningSummaryText(item)
	thinkingPart := []byte(`{"type":"thinking","thinking":"","signature":""}`)
	thinkingPart, _ = sjson.SetBytes(thinkingPart, "thinking", thinkingText)
	thinkingPart, _ = sjson.SetBytes(thinkingPart, "signature", signature)
	return thinkingPart
}

func responsesReasoningSummaryText(item gjson.Result) string {
	var builder strings.Builder
	if summary := item.Get("summary"); summary.Exists() && summary.IsArray() {
		summary.ForEach(func(_, part gjson.Result) bool {
			if text := part.Get("text"); text.Exists() {
				builder.WriteString(text.String())
			} else if part.Type == gjson.String {
				builder.WriteString(part.String())
			}
			return true
		})
	}
	return builder.String()
}

func convertResponsesToolToClaudeTools(tool gjson.Result, toolNameMap map[string]string) [][]byte {
	toolType := strings.TrimSpace(tool.Get("type").String())
	switch toolType {
	case "", "function":
		if tJSON, ok := convertResponsesFunctionToolToClaude(tool, ""); ok {
			return [][]byte{tJSON}
		}
	case "namespace":
		return convertResponsesNamespaceToolToClaude(tool, toolNameMap)
	case "web_search":
		if tJSON, ok := convertResponsesWebSearchToolToClaude(tool); ok {
			if name := gjson.GetBytes(tJSON, "name").String(); name != "" {
				toolNameMap[name] = name
			}
			return [][]byte{tJSON}
		}
	default:
		if isUnsupportedOpenAIBuiltinToolType(toolType) {
			return nil
		}
		if tool.Get("name").String() != "" {
			return [][]byte{[]byte(tool.Raw)}
		}
	}
	return nil
}

func convertResponsesNamespaceToolToClaude(tool gjson.Result, toolNameMap map[string]string) [][]byte {
	namespaceName := strings.TrimSpace(tool.Get("name").String())
	children := tool.Get("tools")
	if !children.Exists() || !children.IsArray() {
		return nil
	}

	var out [][]byte
	children.ForEach(func(_, child gjson.Result) bool {
		childName := responsesToolName(child)
		qualifiedName := qualifyResponsesNamespaceToolName(namespaceName, childName)
		if tJSON, ok := convertResponsesFunctionToolToClaude(child, qualifiedName); ok {
			out = append(out, tJSON)
			toolNameMap[qualifiedName] = qualifiedName
			if childName != "" {
				toolNameMap[childName] = qualifiedName
			}
			// If the qualified name had to be sanitized for Anthropic, also
			// register the sanitized form so the response-side can recover the
			// original namespace-qualified name when the model echoes the
			// sanitized version back in tool_use.name.
			if cleaned := sanitizeAnthropicToolName(qualifiedName); cleaned != qualifiedName && cleaned != "" {
				toolNameMap[cleaned] = qualifiedName
			}
		}
		return true
	})
	return out
}

func convertResponsesFunctionToolToClaude(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}

	cleanName := sanitizeAnthropicToolName(name)
	tJSON := []byte(`{"name":"","description":"","input_schema":{}}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", cleanName)
	if d := responsesToolDescription(tool); d != "" {
		tJSON, _ = sjson.SetBytes(tJSON, "description", d)
	}
	tJSON, _ = sjson.SetRawBytes(tJSON, "input_schema", normalizeClaudeToolInputSchema(responsesToolParameters(tool)))
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
	tJSON, _ = sjson.SetBytes(tJSON, "name", sanitizeAnthropicToolName(name))
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
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
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

func normalizeClaudeToolInputSchema(parameters gjson.Result) []byte {
	raw := strings.TrimSpace(parameters.Raw)
	if raw == "" || raw == "null" || !gjson.Valid(raw) {
		return []byte(`{"type":"object","properties":{}}`)
	}
	result := gjson.Parse(raw)
	if !result.IsObject() {
		return []byte(`{"type":"object","properties":{}}`)
	}
	schema := []byte(raw)
	schemaType := result.Get("type").String()
	if schemaType == "" {
		schema, _ = sjson.SetBytes(schema, "type", "object")
		schemaType = "object"
	}
	if schemaType == "object" && !result.Get("properties").Exists() {
		schema, _ = sjson.SetRawBytes(schema, "properties", []byte(`{}`))
	}
	return schema
}

// anthropicToolNameSafeRe matches characters that are NOT allowed in
// Anthropic tool_use.name (which is constrained to ^[a-zA-Z0-9_-]{1,64}$).
// Common offenders we have seen in the wild:
//   - "computer-use:computer-use" (colon comes from a Codex plugin skill mention
//     [$plugin:skill] being mis-interpreted as a tool name).
//   - dotted names from non-Anthropic upstreams (e.g. "multi_agent_v1.spawn_agent").
//   - whitespace from sloppy model output.
var anthropicToolNameSafeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitizeAnthropicToolName replaces every character that is illegal under the
// Anthropic Messages API tool name pattern with "__". The mapping is reversible
// at the response side via splitMcpFlatName + bareCodexInternalLeafToNamespace
// for known Codex-internal namespaces, and otherwise via toolNameMap when the
// translator records the original "<sanitized>" -> "<original>" pair.
//
// Empty input returns empty (caller is expected to have the usual TrimSpace
// already done). Names that already match the Anthropic pattern are returned
// unchanged so that there is zero behavior change for the 99% case.
func sanitizeAnthropicToolName(name string) string {
	if name == "" {
		return name
	}
	if !anthropicToolNameSafeRe.MatchString(name) && len(name) <= 64 {
		return name
	}
	cleaned := anthropicToolNameSafeRe.ReplaceAllString(name, "__")
	// collapse runs of "__" that came from adjacent illegal chars
	for strings.Contains(cleaned, "____") {
		cleaned = strings.ReplaceAll(cleaned, "____", "__")
	}
	cleaned = strings.Trim(cleaned, "_")
	if cleaned == "" {
		// All chars were illegal; fall back to a deterministic placeholder so
		// the upstream still gets *some* valid name and we don't crash schema.
		cleaned = "tool"
	}
	if len(cleaned) > 64 {
		cleaned = cleaned[:64]
	}
	return cleaned
}

func qualifyResponsesNamespaceToolName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	if childName == "" || namespaceName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if strings.HasPrefix(childName, namespaceName) {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

func isUnsupportedOpenAIBuiltinToolType(toolType string) bool {
	switch toolType {
	case "image_generation", "file_search", "code_interpreter", "computer_use_preview":
		return true
	default:
		return false
	}
}
