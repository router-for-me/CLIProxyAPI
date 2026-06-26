package responses

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking"
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

	root := gjson.ParseBytes(rawJSON)

	// Convert OpenAI Responses reasoning.effort to Claude thinking config.
	if v := root.Get("reasoning.effort"); v.Exists() {
		effort := strings.ToLower(strings.TrimSpace(v.String()))
		if effort != "" {
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

	// input can be a raw string for compatibility with OpenAI Responses API.
	if instructionsText == "" {
		if input := root.Get("input"); input.Exists() && input.Type == gjson.String {
			msg := []byte(`{"role":"user","content":""}`)
			msg, _ = sjson.SetBytes(msg, "content", input.String())
			out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
		}
	}

	// input array processing
	pendingReasoning := ""
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
				hasRedactedThinking := false
				if parts := item.Get("content"); parts.Exists() && parts.IsArray() {
					parts.ForEach(func(_, part gjson.Result) bool {
						ptype := part.Get("type").String()
						switch ptype {
						case "input_text", "output_text":
							if t := part.Get("text"); t.Exists() {
								txt := t.String()
								textAggregate.WriteString(txt)
								contentPart := `{"type":"text","text":""}`
								contentPart, _ = sjson.Set(contentPart, "text", txt)
								partsJSON = append(partsJSON, contentPart)
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
								var contentPart string
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
										contentPart = `{"type":"image","source":{"type":"base64","media_type":"","data":""}}`
										contentPart, _ = sjson.Set(contentPart, "source.media_type", mediaType)
										contentPart, _ = sjson.Set(contentPart, "source.data", data)
									}
								} else {
									contentPart = `{"type":"image","source":{"type":"url","url":""}}`
									contentPart, _ = sjson.Set(contentPart, "source.url", url)
								}
								if contentPart != "" {
									partsJSON = append(partsJSON, contentPart)
									if role == "" {
										role = "user"
									}
									hasImage = true
								}
							}
						case "reasoning", "thinking", "reasoning_text", "summary_text":
							if redacted := redactedThinkingPartFromResult(part); redacted != "" {
								partsJSON = append(partsJSON, redacted)
								hasRedactedThinking = true
								if role == "" {
									role = "assistant"
								}
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

				if role == "assistant" && pendingReasoning != "" {
					partsJSON = append([]string{buildRedactedThinkingPart(pendingReasoning)}, partsJSON...)
					pendingReasoning = ""
					hasRedactedThinking = true
				}

				if len(partsJSON) > 0 {
					msg := []byte(`{"role":"","content":[]}`)
					msg, _ = sjson.SetBytes(msg, "role", role)
					// Preserve legacy single-text flattening, but keep structured arrays when
					// image/thinking content is present.
					if len(partsJSON) == 1 && !hasImage && !hasRedactedThinking {
						// Preserve legacy behavior for single text content
						msg, _ = sjson.DeleteBytes(msg, "content")
						textPart := gjson.Parse(partsJSON[0])
						msg, _ = sjson.SetBytes(msg, "content", textPart.Get("text").String())
					} else {
						for _, partJSON := range partsJSON {
							msg, _ = sjson.SetRawBytes(msg, "content.-1", []byte(partJSON))
						}
					}
					appendMessage(msg)
				} else if textAggregate.Len() > 0 || role == "system" {
					msg := []byte(`{"role":"","content":""}`)
					msg, _ = sjson.SetBytes(msg, "role", role)
					msg, _ = sjson.SetBytes(msg, "content", textAggregate.String())
					appendMessage(msg)
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
				callID = util.SanitizeClaudeToolID(callID)
				name := item.Get("name").String()
				argsStr := item.Get("arguments").String()

				toolUse := `{"type":"tool_use","id":"","name":"","input":{}}`
				toolUse, _ = sjson.Set(toolUse, "id", callID)
				toolUse, _ = sjson.Set(toolUse, "name", name)
				if argsStr != "" && gjson.Valid(argsStr) {
					argsJSON := gjson.Parse(argsStr)
					if argsJSON.IsObject() {
						toolUse, _ = sjson.SetRaw(toolUse, "input", argsJSON.Raw)
					}
				}

				asst := `{"role":"assistant","content":[]}`
				if pendingReasoning != "" {
					asst, _ = sjson.SetRaw(asst, "content.-1", buildRedactedThinkingPart(pendingReasoning))
					pendingReasoning = ""
				}
				asst, _ = sjson.SetRaw(asst, "content.-1", toolUse)
				out, _ = sjson.SetRawBytes(out, "messages.-1", []byte(asst))

			case "function_call_output":
				flushPendingReasoning()
				// Map to user tool_result
				callID := item.Get("call_id").String()
				callID = util.SanitizeClaudeToolID(callID)
				flushPendingToolUseFor(callID)
				outputStr := item.Get("output").String()
				toolResult := `{"type":"tool_result","tool_use_id":"","content":""}`
				toolResult, _ = sjson.Set(toolResult, "tool_use_id", callID)
				toolResult, _ = sjson.Set(toolResult, "content", outputStr)

				usr := `{"role":"user","content":[]}`
				usr, _ = sjson.SetRaw(usr, "content.-1", toolResult)
				out, _ = sjson.SetRawBytes(out, "messages.-1", []byte(usr))
			case "reasoning":
				// Preserve reasoning history so Claude thinking-enabled requests keep
				// thinking/redacted_thinking before tool_use blocks.
				if text := extractResponsesReasoningText(item); text != "" {
					if pendingReasoning == "" {
						pendingReasoning = text
					} else {
						pendingReasoning = pendingReasoning + "\n\n" + text
					}
				}
			}
			return true
		})
	}
	if pendingReasoning != "" {
		asst := `{"role":"assistant","content":[]}`
		asst, _ = sjson.SetRaw(asst, "content.-1", buildRedactedThinkingPart(pendingReasoning))
		out, _ = sjson.SetRawBytes(out, "messages.-1", []byte(asst))
	}

	// tools mapping: parameters -> input_schema
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		toolsJSON := "[]"
		tools.ForEach(func(_, tool gjson.Result) bool {
			tJSON := `{"name":"","description":"","input_schema":{}}`
			if n := tool.Get("name"); n.Exists() {
				tJSON, _ = sjson.Set(tJSON, "name", n.String())
			}
			if d := tool.Get("description"); d.Exists() {
				tJSON, _ = sjson.Set(tJSON, "description", d.String())
			}

			if params := tool.Get("parameters"); params.Exists() {
				tJSON, _ = sjson.SetRaw(tJSON, "input_schema", params.Raw)
			} else if params = tool.Get("parametersJsonSchema"); params.Exists() {
				tJSON, _ = sjson.SetRaw(tJSON, "input_schema", params.Raw)
			}

			toolsJSON, _ = sjson.SetRaw(toolsJSON, "-1", tJSON)
			return true
		})
		if gjson.Parse(toolsJSON).IsArray() && len(gjson.Parse(toolsJSON).Array()) > 0 {
			out, _ = sjson.SetRawBytes(out, "tools", []byte(toolsJSON))
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
				toolChoiceJSON := `{"name":"","type":"tool"}`
				toolChoiceJSON, _ = sjson.Set(toolChoiceJSON, "name", fn)
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(toolChoiceJSON))
			}
		default:

		}
	}

	return []byte(out)
}

func extractResponsesReasoningText(item gjson.Result) string {
	var parts []string

	appendText := func(v string) {
		if strings.TrimSpace(v) != "" {
			parts = append(parts, v)
		}
	}

	if summary := item.Get("summary"); summary.Exists() && summary.IsArray() {
		summary.ForEach(func(_, s gjson.Result) bool {
			if text := s.Get("text"); text.Exists() {
				appendText(text.String())
			}
			return true
		})
	}

	if content := item.Get("content"); content.Exists() && content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			if txt := extractThinkingLikeText(part); txt != "" {
				appendText(txt)
			}
			return true
		})
	}

	if text := item.Get("text"); text.Exists() {
		appendText(text.String())
	}
	if reasoning := item.Get("reasoning"); reasoning.Exists() {
		appendText(reasoning.String())
	}

	return strings.Join(parts, "\n\n")
}

func redactedThinkingPartFromResult(part gjson.Result) string {
	text := extractThinkingLikeText(part)
	if text == "" {
		return ""
	}
	return buildRedactedThinkingPart(text)
}

func extractThinkingLikeText(part gjson.Result) string {
	if txt := strings.TrimSpace(thinking.GetThinkingText(part)); txt != "" {
		return txt
	}
	if text := part.Get("text"); text.Exists() {
		if txt := strings.TrimSpace(text.String()); txt != "" {
			return txt
		}
	}
	if summary := part.Get("summary"); summary.Exists() {
		if txt := strings.TrimSpace(summary.String()); txt != "" {
			return txt
		}
	}
	return ""
}

func buildRedactedThinkingPart(text string) string {
	part := `{"type":"redacted_thinking","data":""}`
	part, _ = sjson.Set(part, "data", text)
	return part
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
		if isOpenAIResponsesApplyPatchCustomTool(toolType, tool) {
			return nil
		}
		if isUnsupportedOpenAIBuiltinToolType(toolType) {
			return nil
		}
		if tool.Get("name").String() != "" {
			return [][]byte{[]byte(tool.Raw)}
		}
	}
	return nil
}

func isOpenAIResponsesApplyPatchCustomTool(toolType string, tool gjson.Result) bool {
	return toolType == "custom" && strings.TrimSpace(tool.Get("name").String()) == "apply_patch"
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

	tJSON := []byte(`{"name":"","description":"","input_schema":{}}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", name)
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

func splitResponsesQualifiedFunctionCallFromRequest(requestRawJSON []byte, qualifiedName string) (name, namespace string) {
	qualifiedName = strings.TrimSpace(qualifiedName)
	if qualifiedName == "" {
		return "", ""
	}

	tools := gjson.GetBytes(requestRawJSON, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return qualifiedName, ""
	}

	var bestNamespace string
	var bestChild string
	tools.ForEach(func(_, tool gjson.Result) bool {
		if strings.TrimSpace(tool.Get("type").String()) != "namespace" {
			return true
		}
		namespaceName := strings.TrimSpace(tool.Get("name").String())
		if namespaceName == "" {
			return true
		}
		children := tool.Get("tools")
		if !children.Exists() || !children.IsArray() {
			return true
		}
		children.ForEach(func(_, child gjson.Result) bool {
			childName := responsesToolName(child)
			if childName == "" {
				return true
			}
			if qualifyResponsesNamespaceToolName(namespaceName, childName) == qualifiedName {
				bestNamespace = namespaceName
				bestChild = childName
			}
			return true
		})
		return true
	})

	if bestNamespace == "" || bestChild == "" {
		return qualifiedName, ""
	}
	return bestChild, bestNamespace
}

func isUnsupportedOpenAIBuiltinToolType(toolType string) bool {
	switch toolType {
	case "image_generation", "file_search", "code_interpreter", "computer_use_preview":
		return true
	default:
		return false
	}
}
