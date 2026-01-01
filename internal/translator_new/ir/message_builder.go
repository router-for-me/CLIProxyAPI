package ir

import (
	"encoding/json"
	"strings"

	"github.com/tailscale/hujson"
	"github.com/tidwall/gjson"
)

// CombineParts combines content parts of a specific type from a message.
func CombineParts(msg Message, contentType ContentType) string {
	var parts []string
	for _, part := range msg.Content {
		if part.Type == contentType {
			if contentType == ContentTypeReasoning && part.Reasoning != "" {
				parts = append(parts, part.Reasoning)
			} else if contentType == ContentTypeText && part.Text != "" {
				parts = append(parts, part.Text)
			}
		}
	}
	return strings.Join(parts, "")
}

// CombineTextParts combines all text content parts from a message.
func CombineTextParts(msg Message) string {
	return CombineParts(msg, ContentTypeText)
}

// CombineReasoningParts combines all reasoning content parts from a message.
func CombineReasoningParts(msg Message) string {
	return CombineParts(msg, ContentTypeReasoning)
}

// BuildToolCallMap creates a map of tool call ID to function name.
func BuildToolCallMap(messages []Message) map[string]string {
	m := make(map[string]string)
	for _, msg := range messages {
		if msg.Role == RoleAssistant {
			for _, tc := range msg.ToolCalls {
				m[tc.ID] = tc.Name
			}
		}
	}
	return m
}

// BuildToolResultsMap creates a map of tool call ID to result part.
func BuildToolResultsMap(messages []Message) map[string]*ToolResultPart {
	m := make(map[string]*ToolResultPart)
	for _, msg := range messages {
		if msg.Role == RoleTool {
			for _, part := range msg.Content {
				if part.Type == ContentTypeToolResult && part.ToolResult != nil {
					m[part.ToolResult.ToolCallID] = part.ToolResult
				}
			}
		}
	}
	return m
}

// ValidateAndNormalizeJSON ensures a string is valid JSON, wrapping it if not.
func ValidateAndNormalizeJSON(s string) string {
	if s == "" {
		return "{}"
	}
	if !json.Valid([]byte(s)) {
		b, _ := json.Marshal(s)
		return string(b)
	}
	return s
}

// ParseToolCallArgs parses tool call arguments, using hujson for relaxed parsing.
func ParseToolCallArgs(argsJSON string) map[string]interface{} {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || trimmed == "{}" {
		return map[string]interface{}{}
	}

	// Try hujson standardizer first (handles comments, trailing commas, unquoted keys)
	// This covers most "tolerant" parsing needs with a battle-tested library.
	if standardized, err := hujson.Standardize([]byte(trimmed)); err == nil {
		var argsObj map[string]interface{}
		if json.Unmarshal(standardized, &argsObj) == nil {
			return argsObj
		}
	}

	// Fallback to strict JSON unmarshal if hujson fails or for simple cases
	var argsObj map[string]interface{}
	if json.Unmarshal([]byte(trimmed), &argsObj) == nil {
		return argsObj
	}

	return map[string]interface{}{}
}

// ParseOpenAIStyleToolCalls parses tool_calls array in OpenAI/Ollama format.
func ParseOpenAIStyleToolCalls(toolCalls []gjson.Result) []ToolCall {
	var result []ToolCall
	for _, tc := range toolCalls {
		if tc.Get("type").String() == "function" {
			result = append(result, ToolCall{
				ID:   tc.Get("id").String(),
				Name: tc.Get("function.name").String(),
				Args: tc.Get("function.arguments").String(),
			})
		}
	}
	return result
}

// ReasoningFields holds parsed reasoning content and signature from any format.
type ReasoningFields struct {
	Text      string // The reasoning/thinking text content
	Signature string // The signature/opaque/id field
}

// ParseReasoningFromJSON extracts reasoning content from any supported format.
func ParseReasoningFromJSON(data gjson.Result) ReasoningFields {
	var rf ReasoningFields

	// Parse reasoning text from multiple formats
	if rc := data.Get("reasoning_content"); rc.Exists() && rc.String() != "" {
		rf.Text = rc.String() // xAI Grok
	} else if r := data.Get("reasoning"); r.Exists() && r.String() != "" {
		rf.Text = r.String() // Cline/OpenRouter
	} else if rt := data.Get("reasoning_text"); rt.Exists() && rt.String() != "" {
		rf.Text = rt.String() // OpenAI o1/o3
	} else if th := data.Get("thinking"); th.Exists() && th.String() != "" {
		rf.Text = th.String() // Claude
	} else if cs := data.Get("cot_summary"); cs.Exists() && cs.String() != "" {
		rf.Text = cs.String() // GitHub Copilot
	}

	// Parse xAI reasoning_details array
	if rd := data.Get("reasoning_details"); rd.Exists() && rd.IsArray() {
		for _, detail := range rd.Array() {
			if detail.Get("type").String() == "reasoning.summary" {
				if summary := detail.Get("summary").String(); summary != "" {
					rf.Text = summary
					if format := detail.Get("format").String(); format != "" {
						rf.Signature = format
					}
				}
			}
		}
	}

	// Parse signature from multiple formats
	if rf.Signature == "" {
		if ro := data.Get("reasoning_opaque"); ro.Exists() && ro.String() != "" {
			rf.Signature = ro.String() // OpenAI o1/o3
		} else if sig := data.Get("signature"); sig.Exists() && sig.String() != "" {
			rf.Signature = sig.String() // Claude
		} else if cid := data.Get("cot_id"); cid.Exists() && cid.String() != "" {
			rf.Signature = cid.String() // GitHub Copilot
		}
	}

	return rf
}

// BuildReasoningDelta creates a delta map with all reasoning format fields populated.
func BuildReasoningDelta(reasoning, signature string) map[string]interface{} {
	if signature == "" {
		signature = "thinking"
	}
	return map[string]interface{}{
		"role":              "assistant",
		"reasoning_content": reasoning, // xAI Grok
		"reasoning_text":    reasoning, // OpenAI o1/o3
		"thinking":          reasoning, // Claude
		"cot_summary":       reasoning, // GitHub Copilot
		"signature":         signature, // Claude
		"reasoning_opaque":  signature, // OpenAI o1/o3
		"cot_id":            signature, // GitHub Copilot
	}
}

// AddReasoningToMessage adds all reasoning format fields to a message map.
func AddReasoningToMessage(msg map[string]interface{}, reasoning, signature string) {
	if reasoning == "" {
		return
	}
	msg["reasoning_content"] = reasoning // xAI Grok
	msg["reasoning_text"] = reasoning    // OpenAI o1/o3
	msg["thinking"] = reasoning          // Claude
	msg["cot_summary"] = reasoning       // GitHub Copilot

	msg["reasoning_details"] = []interface{}{
		map[string]interface{}{
			"type":    "reasoning.summary",
			"summary": reasoning,
			"format":  "xai-responses-v1",
			"index":   0,
		},
	}

	if signature != "" {
		msg["reasoning_opaque"] = signature // OpenAI o1/o3
		msg["signature"] = signature        // Claude
		msg["cot_id"] = signature           // GitHub Copilot
	}
}

// GetFirstReasoningSignature extracts the first ThoughtSignature from message content parts.
func GetFirstReasoningSignature(msg Message) string {
	for _, part := range msg.Content {
		if part.Type == ContentTypeReasoning && part.ThoughtSignature != "" {
			return part.ThoughtSignature
		}
	}
	return ""
}
