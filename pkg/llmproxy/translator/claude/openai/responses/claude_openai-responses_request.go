package responses

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/thinking"
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
	out := fmt.Sprintf(`{"model":"","max_tokens":32000,"messages":[],"metadata":{"user_id":"%s"}}`, userID)

	root := gjson.ParseBytes(rawJSON)

	// Convert OpenAI Responses reasoning.effort to Claude thinking config.
	if v := root.Get("reasoning.effort"); v.Exists() {
		effort := strings.ToLower(strings.TrimSpace(v.String()))
		if effort != "" {
			budget, ok := thinking.ConvertLevelToBudget(effort)
			if ok {
				switch budget {
				case 0:
					out, _ = sjson.Set(out, "thinking.type", "disabled")
				case -1:
					out, _ = sjson.Set(out, "thinking.type", "enabled")
				default:
					if budget > 0 {
						out, _ = sjson.Set(out, "thinking.type", "enabled")
						out, _ = sjson.Set(out, "thinking.budget_tokens", budget)
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
	out, _ = sjson.Set(out, "model", modelName)

	// Max tokens
	if mot := root.Get("max_output_tokens"); mot.Exists() {
		out, _ = sjson.Set(out, "max_tokens", mot.Int())
	}

	// Stream
	out, _ = sjson.Set(out, "stream", stream)

	// instructions -> as a leading message (use role user for Claude API compatibility)
	instructionsText := ""
	extractedFromSystem := false
	if instr := root.Get("instructions"); instr.Exists() && instr.Type == gjson.String {
		instructionsText = instr.String()
		if instructionsText != "" {
			sysMsg := `{"role":"user","content":""}`
			sysMsg, _ = sjson.Set(sysMsg, "content", instructionsText)
			out, _ = sjson.SetRaw(out, "messages.-1", sysMsg)
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
						sysMsg := `{"role":"user","content":""}`
						sysMsg, _ = sjson.Set(sysMsg, "content", instructionsText)
						out, _ = sjson.SetRaw(out, "messages.-1", sysMsg)
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
			msg := `{"role":"user","content":""}`
			msg, _ = sjson.Set(msg, "content", input.String())
			out, _ = sjson.SetRaw(out, "messages.-1", msg)
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
					msg := `{"role":"","content":[]}`
					msg, _ = sjson.Set(msg, "role", role)
					// Preserve legacy single-text flattening, but keep structured arrays when
					// image/thinking content is present.
					if len(partsJSON) == 1 && !hasImage && !hasRedactedThinking {
						// Preserve legacy behavior for single text content
						msg, _ = sjson.Delete(msg, "content")
						textPart := gjson.Parse(partsJSON[0])
						msg, _ = sjson.Set(msg, "content", textPart.Get("text").String())
					} else {
						for _, partJSON := range partsJSON {
							msg, _ = sjson.SetRaw(msg, "content.-1", partJSON)
						}
					}
					out, _ = sjson.SetRaw(out, "messages.-1", msg)
				} else if textAggregate.Len() > 0 || role == "system" {
					msg := `{"role":"","content":""}`
					msg, _ = sjson.Set(msg, "role", role)
					msg, _ = sjson.Set(msg, "content", textAggregate.String())
					out, _ = sjson.SetRaw(out, "messages.-1", msg)
				}

			case "function_call":
				// Map to assistant tool_use
				callID := item.Get("call_id").String()
				if callID == "" {
					callID = genToolCallID()
				}
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
				out, _ = sjson.SetRaw(out, "messages.-1", asst)

			case "function_call_output":
				// Map to user tool_result
				callID := item.Get("call_id").String()
				outputStr := item.Get("output").String()
				toolResult := `{"type":"tool_result","tool_use_id":"","content":""}`
				toolResult, _ = sjson.Set(toolResult, "tool_use_id", callID)
				toolResult, _ = sjson.Set(toolResult, "content", outputStr)

				usr := `{"role":"user","content":[]}`
				usr, _ = sjson.SetRaw(usr, "content.-1", toolResult)
				out, _ = sjson.SetRaw(out, "messages.-1", usr)
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
		out, _ = sjson.SetRaw(out, "messages.-1", asst)
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
			out, _ = sjson.SetRaw(out, "tools", toolsJSON)
		}
	}

	// Map tool_choice similar to Chat Completions translator (optional in docs, safe to handle)
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		switch toolChoice.Type {
		case gjson.String:
			switch toolChoice.String() {
			case "auto":
				out, _ = sjson.SetRaw(out, "tool_choice", `{"type":"auto"}`)
			case "none":
				// Leave unset; implies no tools
			case "required":
				out, _ = sjson.SetRaw(out, "tool_choice", `{"type":"any"}`)
			}
		case gjson.JSON:
			if toolChoice.Get("type").String() == "function" {
				fn := toolChoice.Get("function.name").String()
				toolChoiceJSON := `{"name":"","type":"tool"}`
				toolChoiceJSON, _ = sjson.Set(toolChoiceJSON, "name", fn)
				out, _ = sjson.SetRaw(out, "tool_choice", toolChoiceJSON)
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
