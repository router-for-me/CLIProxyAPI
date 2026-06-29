package responses

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
	out := []byte(fmt.Sprintf(`{"model":"","max_tokens":32000,"messages":[],"metadata":{"user_id":"%s"},"cache_control":{"type":"ephemeral"}}`, userID))

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
	type pendingToolUseMessage struct {
		callID string
		raw    []byte
	}
	var pendingToolUseMessages []pendingToolUseMessage
	appendMessage := func(msg []byte) {
		out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
	}
	flushPendingReasoning := func() {
		if len(pendingReasoningParts) == 0 {
			return
		}
		asst := []byte(`{"role":"assistant","content":[]}`)
		for _, partJSON := range pendingReasoningParts {
			asst, _ = sjson.SetRawBytes(asst, "content.-1", []byte(partJSON))
		}
		appendMessage(asst)
		pendingReasoningParts = nil
	}
	flushPendingToolUses := func() {
		for _, pending := range pendingToolUseMessages {
			appendMessage(pending.raw)
		}
		pendingToolUseMessages = nil
	}
	flushPendingToolUseFor := func(callID string) {
		if len(pendingToolUseMessages) == 0 {
			return
		}
		for i, pending := range pendingToolUseMessages {
			if pending.callID == callID {
				appendMessage(pending.raw)
				pendingToolUseMessages = append(pendingToolUseMessages[:i], pendingToolUseMessages[i+1:]...)
				return
			}
		}
		flushPendingToolUses()
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
				if role != "assistant" {
					flushPendingToolUses()
				}
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
					appendMessage(msg)
				} else if textAggregate.Len() > 0 || role == "system" {
					msg := []byte(`{"role":"","content":""}`)
					msg, _ = sjson.SetBytes(msg, "role", role)
					msg, _ = sjson.SetBytes(msg, "content", textAggregate.String())
					appendMessage(msg)
				}

			case "reasoning":
				// Do not reconstruct Claude thinking blocks from OpenAI Responses
				// reasoning items. Claude requires replayed thinking blocks in the
				// latest assistant message to remain byte-for-byte equivalent to the
				// original response; Responses summaries are not that original text.

			case "function_call":
				// Map to assistant tool_use
				callID := item.Get("call_id").String()
				if callID == "" {
					callID = genToolCallID()
				}
				callID = util.SanitizeClaudeToolID(callID)
				name := item.Get("name").String()
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

				asst := []byte(`{"role":"assistant","content":[]}`)
				for _, partJSON := range pendingReasoningParts {
					asst, _ = sjson.SetRawBytes(asst, "content.-1", []byte(partJSON))
				}
				pendingReasoningParts = nil
				asst, _ = sjson.SetRawBytes(asst, "content.-1", toolUse)
				pendingToolUseMessages = append(pendingToolUseMessages, pendingToolUseMessage{
					callID: callID,
					raw:    asst,
				})

			case "function_call_output":
				flushPendingReasoning()
				// Map to user tool_result
				callID := item.Get("call_id").String()
				callID = util.SanitizeClaudeToolID(callID)
				flushPendingToolUseFor(callID)
				toolResult := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
				toolResult, _ = sjson.SetBytes(toolResult, "tool_use_id", callID)
				toolResult = setResponsesToolResultContent(toolResult, item.Get("output"))

				usr := []byte(`{"role":"user","content":[]}`)
				usr, _ = sjson.SetRawBytes(usr, "content.-1", toolResult)
				appendMessage(usr)
			}
			return true
		})
	}
	flushPendingReasoning()
	flushPendingToolUses()

	includedToolNames := map[string]struct{}{}
	toolNameMap := map[string]string{}

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
					toolChoiceJSON, _ = sjson.SetBytes(toolChoiceJSON, "name", fn)
					out, _ = sjson.SetRawBytes(out, "tool_choice", toolChoiceJSON)
				}
			}
		default:

		}
	}

	out = normalizeMessageContent(out)
	out = stripModelSwitchPrompt(out)
	out = truncateLargeToolResults(out)
	out = stripOldImages(out)

	return out
}

func setResponsesToolResultContent(toolResult []byte, output gjson.Result) []byte {
	if output.IsArray() {
		content := []byte(`[]`)
		added := false
		output.ForEach(func(_, part gjson.Result) bool {
			switch part.Get("type").String() {
			case "input_text", "output_text", "text":
				text := part.Get("text").String()
				textPart := []byte(`{"type":"text","text":""}`)
				textPart, _ = sjson.SetBytes(textPart, "text", text)
				content, _ = sjson.SetRawBytes(content, "-1", textPart)
				added = true
			case "input_image", "image":
				if imagePart := convertResponsesImagePartToClaudeImage(part); len(imagePart) > 0 {
					content, _ = sjson.SetRawBytes(content, "-1", imagePart)
					added = true
				}
			}
			return true
		})
		if added {
			toolResult, _ = sjson.SetRawBytes(toolResult, "content", content)
			return toolResult
		}
	}

	toolResult, _ = sjson.SetBytes(toolResult, "content", output.String())
	return toolResult
}

func convertResponsesImagePartToClaudeImage(part gjson.Result) []byte {
	url := part.Get("image_url").String()
	if url == "" {
		url = part.Get("url").String()
	}
	if url == "" {
		source := part.Get("source")
		if source.Exists() {
			data := source.Get("data").String()
			if data == "" {
				data = source.Get("base64").String()
			}
			if data != "" {
				mediaType := source.Get("media_type").String()
				if mediaType == "" {
					mediaType = source.Get("mime_type").String()
				}
				if mediaType == "" {
					mediaType = "application/octet-stream"
				}
				url = fmt.Sprintf("data:%s;base64,%s", mediaType, data)
			}
		}
	}
	if url == "" {
		return nil
	}

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
		if data == "" {
			return nil
		}
		contentPart := []byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`)
		contentPart, _ = sjson.SetBytes(contentPart, "source.media_type", mediaType)
		contentPart, _ = sjson.SetBytes(contentPart, "source.data", data)
		return contentPart
	}

	contentPart := []byte(`{"type":"image","source":{"type":"url","url":""}}`)
	contentPart, _ = sjson.SetBytes(contentPart, "source.url", url)
	return contentPart
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

// maxRetainedUserImageTurns controls how many recent user turns keep their
// images. Older user turns have images replaced with a placeholder. This
// balances cache stability (fewer replacements = fewer cache rebuilds) against
// request size growth (more images = larger payload).
const maxRetainedUserImageTurns = 6

// stripOldImages keeps images only in the most recent N user turns (counted by
// role:"user" messages). All earlier user turn images are replaced with
// [image omitted]. A hard 28MB byte cap acts as a safety net for extreme cases.
func stripOldImages(out []byte) []byte {
	placeholder := []byte(`{"type":"text","text":"[image omitted]"}`)

	// Collect indices of all user messages.
	var userTurnIndices []int64
	gjson.GetBytes(out, "messages").ForEach(func(mi, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			userTurnIndices = append(userTurnIndices, mi.Int())
		}
		return true
	})

	// Determine cutoff: keep the last maxRetainedUserImageTurns user turns.
	cutoff := len(userTurnIndices) - maxRetainedUserImageTurns
	if cutoff <= 0 {
		// Fewer than N user turns total; nothing to strip.
		cutoff = 0
	}
	cutoffMsgIdx := int64(-1)
	if cutoff > 0 {
		cutoffMsgIdx = userTurnIndices[cutoff]
	}

	// Replace images in messages before the cutoff.
	if cutoffMsgIdx > 0 {
		for {
			path := firstImagePathBefore(out, cutoffMsgIdx)
			if path == "" {
				break
			}
			nb, err := sjson.SetRawBytes(out, path, placeholder)
			if err != nil {
				break
			}
			out = nb
		}
	}

	// Hard 28MB cap as safety net.
	const limit = 28 * 1024 * 1024
	for len(out) > limit {
		path := firstImagePathBefore(out, -1)
		if path == "" {
			break
		}
		nb, err := sjson.SetRawBytes(out, path, placeholder)
		if err != nil {
			break
		}
		out = nb
	}
	return out
}

// firstImagePathBefore returns the sjson path of the first image block in
// messages with index < beforeIdx. Pass beforeIdx == -1 to match all messages.
func firstImagePathBefore(out []byte, beforeIdx int64) string {
	found := ""
	gjson.GetBytes(out, "messages").ForEach(func(mi, msg gjson.Result) bool {
		if beforeIdx >= 0 && mi.Int() >= beforeIdx {
			return false // stop: reached the protected turn
		}
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(ci, part gjson.Result) bool {
			switch part.Get("type").String() {
			case "image":
				found = fmt.Sprintf("messages.%d.content.%d", mi.Int(), ci.Int())
				return false
			case "tool_result":
				inner := part.Get("content")
				if inner.IsArray() {
					inner.ForEach(func(ii, ipart gjson.Result) bool {
						if ipart.Get("type").String() == "image" {
							found = fmt.Sprintf("messages.%d.content.%d.content.%d", mi.Int(), ci.Int(), ii.Int())
							return false
						}
						return true
					})
				}
				if found != "" {
					return false
				}
			}
			return true
		})
		return found == ""
	})
	return found
}

// normalizeMessageContent ensures every message's content field is an array of
// typed blocks, never a bare string. This stabilizes the JSON byte sequence
// across turns so Anthropic prompt caching can match longer prefixes.
func normalizeMessageContent(out []byte) []byte {
	msgs := gjson.GetBytes(out, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		return out
	}
	msgs.ForEach(func(mi, msg gjson.Result) bool {
		c := msg.Get("content")
		if c.Type == gjson.String {
			block := []byte(`{"type":"text","text":""}`)
			block, _ = sjson.SetBytes(block, "text", c.String())
			arr := []byte(`[]`)
			arr, _ = sjson.SetRawBytes(arr, "-1", block)
			path := fmt.Sprintf("messages.%d.content", mi.Int())
			out, _ = sjson.SetRawBytes(out, path, arr)
		}
		return true
	})
	return out
}

// stripModelSwitchPrompt detects <model_switch> blocks that contain a full
// duplicate of the system prompt and replaces them with a short summary.
// This saves ~5,700 tokens per occurrence (typically 3x in a long session).
func stripModelSwitchPrompt(out []byte) []byte {
	const tag = "<model_switch>"
	const replacement = "<model_switch>\nThe user switched models. Continue the conversation following the system instructions from the first message.\n</model_switch>"

	gjson.GetBytes(out, "messages").ForEach(func(mi, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(ci, part gjson.Result) bool {
			if part.Get("type").String() != "text" {
				return true
			}
			text := part.Get("text").String()
			if len(text) > 1000 && strings.Contains(text, tag) {
				path := fmt.Sprintf("messages.%d.content.%d.text", mi.Int(), ci.Int())
				out, _ = sjson.SetBytes(out, path, replacement)
			}
			return true
		})
		return true
	})
	return out
}

// truncateLargeToolResults truncates tool_result content strings that exceed
// a threshold, keeping the head and tail to preserve useful context while
// cutting out the middle bulk. This targets cases like large log dumps that
// bloat the payload without adding value to later turns.
func truncateLargeToolResults(out []byte) []byte {
	const maxLen = 8192
	const keepEach = 2048

	gjson.GetBytes(out, "messages").ForEach(func(mi, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(ci, part gjson.Result) bool {
			if part.Get("type").String() != "tool_result" {
				return true
			}
			inner := part.Get("content")
			if inner.Type == gjson.String && len(inner.String()) > maxLen {
				text := inner.String()
				truncated := text[:keepEach] + "\n\n[...truncated " + fmt.Sprintf("%d", len(text)-2*keepEach) + " chars...]\n\n" + text[len(text)-keepEach:]
				path := fmt.Sprintf("messages.%d.content.%d.content", mi.Int(), ci.Int())
				out, _ = sjson.SetBytes(out, path, truncated)
			}
			return true
		})
		return true
	})
	return out
}
