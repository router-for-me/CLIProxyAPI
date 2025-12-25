/**
 * @file Claude API request parser
 * @description Converts Claude Messages API requests into unified format.
 */

package to_ir

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
	"github.com/tidwall/gjson"
)

// ParseClaudeRequest converts a raw Claude Messages API JSON body into unified format.
func ParseClaudeRequest(rawJSON []byte) (*ir.UnifiedChatRequest, error) {
	// URL format fix
	rawJSON = bytes.Replace(rawJSON, []byte(`"url":{"type":"string","format":"uri",`), []byte(`"url":{"type":"string",`), -1)

	if !gjson.ValidBytes(rawJSON) {
		return nil, &json.UnmarshalTypeError{Value: "invalid json"}
	}

	req := &ir.UnifiedChatRequest{}
	parsed := gjson.ParseBytes(rawJSON)

	req.Model = parsed.Get("model").String()

	if sessionID := deriveSessionID(rawJSON); sessionID != "" {
		if req.Metadata == nil {
			req.Metadata = make(map[string]any)
		}
		req.Metadata["session_id"] = sessionID
	}

	parseGenerationParams(parsed, req)
	parseSystemMessage(parsed, req)
	parseMessages(parsed, req)
	parseTools(parsed, req)
	parseThinkingConfig(parsed, req)
	parseMetadata(parsed, req)

	return req, nil
}

func deriveSessionID(rawJSON []byte) string {
	messages := gjson.GetBytes(rawJSON, "messages")
	if !messages.IsArray() {
		return ""
	}
	for _, msg := range messages.Array() {
		if msg.Get("role").String() == "user" {
			content := msg.Get("content").String()
			if content == "" {
				content = msg.Get("content.0.text").String()
			}
			if content != "" {
				h := sha256.Sum256([]byte(content))
				return hex.EncodeToString(h[:16])
			}
		}
	}
	return ""
}

func parseGenerationParams(parsed gjson.Result, req *ir.UnifiedChatRequest) {
	if v := parsed.Get("max_tokens"); v.Exists() {
		i := int(v.Int())
		req.MaxTokens = &i
	}
	if v := parsed.Get("temperature"); v.Exists() {
		f := v.Float()
		req.Temperature = &f
	}
	if v := parsed.Get("top_p"); v.Exists() {
		f := v.Float()
		req.TopP = &f
	}
	if v := parsed.Get("top_k"); v.Exists() {
		i := int(v.Int())
		req.TopK = &i
	}
	if v := parsed.Get("stop_sequences"); v.Exists() && v.IsArray() {
		for _, s := range v.Array() {
			req.StopSequences = append(req.StopSequences, s.String())
		}
	}
}

func parseSystemMessage(parsed gjson.Result, req *ir.UnifiedChatRequest) {
	if system := parsed.Get("system"); system.Exists() {
		var systemText string
		if system.Type == gjson.String {
			systemText = system.String()
		} else if system.IsArray() {
			var parts []string
			for _, part := range system.Array() {
				if part.Get("type").String() == "text" {
					parts = append(parts, part.Get("text").String())
				}
			}
			systemText = strings.Join(parts, "\n")
		}
		if systemText != "" {
			req.Messages = append(req.Messages, ir.Message{
				Role: ir.RoleSystem, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: systemText}},
			})
		}
	}
}

func parseMessages(parsed gjson.Result, req *ir.UnifiedChatRequest) {
	if messages := parsed.Get("messages"); messages.Exists() && messages.IsArray() {
		for _, m := range messages.Array() {
			req.Messages = append(req.Messages, parseClaudeMessage(m))
		}
	}
}

func parseTools(parsed gjson.Result, req *ir.UnifiedChatRequest) {
	if tools := parsed.Get("tools"); tools.Exists() && tools.IsArray() {
		for _, t := range tools.Array() {
			var params map[string]interface{}
			if schema := t.Get("input_schema"); schema.Exists() && schema.IsObject() {
				if err := json.Unmarshal([]byte(schema.Raw), &params); err == nil {
					params = ir.CleanJsonSchema(params)
				}
			}
			if params == nil {
				params = make(map[string]interface{})
			}
			req.Tools = append(req.Tools, ir.ToolDefinition{
				Name: t.Get("name").String(), Description: t.Get("description").String(), Parameters: params,
			})
		}
	}
}

func parseThinkingConfig(parsed gjson.Result, req *ir.UnifiedChatRequest) {
	if thinking := parsed.Get("thinking"); thinking.Exists() && thinking.IsObject() {
		if thinking.Get("type").String() == "enabled" {
			req.Thinking = &ir.ThinkingConfig{IncludeThoughts: true}
			if budget := thinking.Get("budget_tokens"); budget.Exists() {
				req.Thinking.Budget = int(budget.Int())
			} else {
				req.Thinking.Budget = -1
			}
		} else if thinking.Get("type").String() == "disabled" {
			req.Thinking = &ir.ThinkingConfig{IncludeThoughts: false, Budget: 0}
		}
	}
}

func parseMetadata(parsed gjson.Result, req *ir.UnifiedChatRequest) {
	if metadata := parsed.Get("metadata"); metadata.Exists() && metadata.IsObject() {
		var meta map[string]any
		if err := json.Unmarshal([]byte(metadata.Raw), &meta); err == nil {
			req.Metadata = meta
		}
	}
}

func parseClaudeMessage(m gjson.Result) ir.Message {
	roleStr := m.Get("role").String()
	role := ir.RoleUser
	if roleStr == "assistant" {
		role = ir.RoleAssistant
	}

	msg := ir.Message{Role: role}
	content := m.Get("content")

	if content.Type == gjson.String {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: content.String()})
		return msg
	}

	if content.IsArray() {
		for _, block := range content.Array() {
			switch block.Get("type").String() {
			case "text":
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: block.Get("text").String()})
			case "thinking":
				thinkingText := block.Get("thinking").String()
				if thinkingText == "" {
					if inner := block.Get("thinking.text"); inner.Exists() {
						thinkingText = inner.String()
					}
				}
				msg.Content = append(msg.Content, ir.ContentPart{
					Type:             ir.ContentTypeReasoning,
					Reasoning:        thinkingText,
					ThoughtSignature: block.Get("signature").String(),
				})
			case "image":
				if source := block.Get("source"); source.Exists() && source.Get("type").String() == "base64" {
					msg.Content = append(msg.Content, ir.ContentPart{
						Type: ir.ContentTypeImage,
						Image: &ir.ImagePart{MimeType: source.Get("media_type").String(), Data: source.Get("data").String()},
					})
				}
			case "tool_use":
				inputRaw := block.Get("input").Raw
				if inputRaw == "" {
					inputRaw = "{}"
				}
				msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{
					ID: block.Get("id").String(), Name: block.Get("name").String(), Args: inputRaw,
				})
			case "tool_result":
				resultContent := block.Get("content")
				var resultStr string
				if resultContent.Type == gjson.String {
					resultStr = resultContent.String()
				} else if resultContent.IsArray() {
					var parts []string
					for _, part := range resultContent.Array() {
						if part.Get("type").String() == "text" {
							parts = append(parts, part.Get("text").String())
						}
					}
					resultStr = strings.Join(parts, "\n")
				} else {
					resultStr = resultContent.Raw
				}
				msg.Content = append(msg.Content, ir.ContentPart{
					Type: ir.ContentTypeToolResult,
					ToolResult: &ir.ToolResultPart{ToolCallID: block.Get("tool_use_id").String(), Result: resultStr},
				})
			}
		}
	}
	return msg
}

// ParseClaudeResponse converts a non-streaming Claude API response into unified format.
func ParseClaudeResponse(rawJSON []byte) ([]ir.Message, *ir.Usage, error) {
	if !gjson.ValidBytes(rawJSON) {
		return nil, nil, &json.UnmarshalTypeError{Value: "invalid json"}
	}

	parsed := gjson.ParseBytes(rawJSON)
	var usage *ir.Usage
	if u := parsed.Get("usage"); u.Exists() {
		usage = ir.ParseClaudeUsage(u)
	}

	content := parsed.Get("content")
	if !content.Exists() || !content.IsArray() {
		return nil, usage, nil
	}

	msg := ir.Message{Role: ir.RoleAssistant}
	for _, block := range content.Array() {
		ir.ParseClaudeContentBlock(block, &msg)
	}

	if len(msg.Content) > 0 || len(msg.ToolCalls) > 0 {
		return []ir.Message{msg}, usage, nil
	}
	return nil, usage, nil
}

// ParseClaudeChunk converts a streaming Claude API chunk into events.
func ParseClaudeChunk(rawJSON []byte) ([]ir.UnifiedEvent, error) {
	data := ir.ExtractSSEData(rawJSON)
	if len(data) == 0 {
		return nil, nil
	}
	if !gjson.ValidBytes(data) {
		return nil, nil
	}

	parsed := gjson.ParseBytes(data)
	switch parsed.Get("type").String() {
	case "content_block_delta":
		return ir.ParseClaudeStreamDelta(parsed), nil
	case "message_delta":
		return ir.ParseClaudeMessageDelta(parsed), nil
	case "message_stop":
		return []ir.UnifiedEvent{{Type: ir.EventTypeFinish, FinishReason: ir.FinishReasonStop}}, nil
	case "error":
		return []ir.UnifiedEvent{{Type: ir.EventTypeError, Error: &ClaudeAPIError{Message: parsed.Get("error.message").String()}}}, nil
	}
	return nil, nil
}

// ClaudeAPIError represents an error from Claude API
type ClaudeAPIError struct {
	Message string
}

func (e *ClaudeAPIError) Error() string {
	return e.Message
}
