// Package amp - request fingerprinting for feature-aware model routing.
//
// Amp shares some upstream models across multiple features (e.g. Gemini 3
// Flash is used for handoff, search subagent, look-at). Name-based mapping
// alone cannot distinguish them. RequestFingerprint inspects the request
// body to recover semantic information about which Amp feature originated
// the request so that AmpModelMapping.When can route per feature.
package amp

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

// Known Amp tool names used to identify features.
const (
	HandoffToolName = "create_handoff_context"
)

// RequestFingerprint captures the per-request features a mapping condition
// can match against. All fields are optional; absent fields cannot match.
type RequestFingerprint struct {
	// ToolChoice is the forced tool name extracted from the request body.
	// Anthropic: tool_choice.name; OpenAI: tool_choice.function.name;
	// Gemini: toolConfig.functionCallingConfig.allowedFunctionNames[0]
	// when mode is ANY/AUTO and exactly one tool is allowed.
	ToolChoice string

	// LastUserText is the trimmed text content of the final user-role message,
	// used by AmpMappingCondition.UserSuffix.
	LastUserText string
}

// Feature returns the canonical Amp feature alias inferred from the
// fingerprint, or an empty string if no high-level feature can be deduced.
func (f RequestFingerprint) Feature() string {
	if strings.EqualFold(f.ToolChoice, HandoffToolName) {
		return "handoff"
	}
	// User-suffix based detection (cheap, anchored on Amp's actual prompt).
	lower := strings.ToLower(f.LastUserText)
	if strings.HasSuffix(lower, "use the create_handoff_context tool to extract relevant information and files.") {
		return "handoff"
	}
	return ""
}

// ExtractFingerprint inspects a JSON request body (Anthropic, OpenAI/Codex,
// or Gemini schema) and returns the best-effort fingerprint. The function
// is read-only and tolerant of malformed input; missing fields yield empty
// values rather than errors.
func ExtractFingerprint(body []byte) RequestFingerprint {
	if len(body) == 0 {
		return RequestFingerprint{}
	}

	fp := RequestFingerprint{
		ToolChoice:   extractToolChoiceName(body),
		LastUserText: extractLastUserText(body),
	}
	return fp
}

func extractToolChoiceName(body []byte) string {
	// Anthropic Messages: { "tool_choice": { "type": "tool", "name": "..." } }
	if v := gjson.GetBytes(body, "tool_choice.name"); v.Exists() && v.Type == gjson.String {
		return v.String()
	}
	// OpenAI Chat / Responses: { "tool_choice": { "type": "function", "function": { "name": "..." } } }
	if v := gjson.GetBytes(body, "tool_choice.function.name"); v.Exists() && v.Type == gjson.String {
		return v.String()
	}
	// Gemini: forced tool through allowedFunctionNames when mode is ANY.
	mode := gjson.GetBytes(body, "toolConfig.functionCallingConfig.mode").String()
	if strings.EqualFold(mode, "ANY") {
		names := gjson.GetBytes(body, "toolConfig.functionCallingConfig.allowedFunctionNames")
		if names.IsArray() {
			arr := names.Array()
			if len(arr) == 1 {
				return arr[0].String()
			}
		}
	}
	return ""
}

// extractLastUserText returns the last user-role textual content from
// Anthropic, OpenAI, or Gemini schemas. Only the trailing text is needed
// for suffix matching; multi-part contents are concatenated in order.
func extractLastUserText(body []byte) string {
	// Anthropic / OpenAI Chat: messages[] with role+content.
	if msgs := gjson.GetBytes(body, "messages"); msgs.IsArray() {
		arr := msgs.Array()
		for i := len(arr) - 1; i >= 0; i-- {
			m := arr[i]
			if !strings.EqualFold(m.Get("role").String(), "user") {
				continue
			}
			return strings.TrimSpace(messageContentText(m.Get("content")))
		}
	}
	// Gemini: contents[] with role+parts[].text
	if cts := gjson.GetBytes(body, "contents"); cts.IsArray() {
		arr := cts.Array()
		for i := len(arr) - 1; i >= 0; i-- {
			m := arr[i]
			if !strings.EqualFold(m.Get("role").String(), "user") {
				continue
			}
			var b strings.Builder
			for _, p := range m.Get("parts").Array() {
				if t := p.Get("text"); t.Exists() {
					b.WriteString(t.String())
				}
			}
			return strings.TrimSpace(b.String())
		}
	}
	return ""
}

// messageContentText extracts text from an Anthropic/OpenAI message.content
// value, which may be a plain string or an array of typed parts.
func messageContentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var b strings.Builder
	for _, part := range content.Array() {
		// Anthropic: { type: "text", text: "..." }
		// OpenAI:    { type: "text", text: "..." } or { type: "input_text", text: "..." }
		if t := part.Get("text"); t.Exists() && t.Type == gjson.String {
			b.WriteString(t.String())
		}
	}
	return b.String()
}

// ConditionMatches reports whether the given mapping condition is satisfied
// by the fingerprint. A nil condition always matches (unconditional mapping).
func ConditionMatches(cond *config.AmpMappingCondition, fp RequestFingerprint) bool {
	if cond == nil {
		return true
	}
	if cond.ToolChoice != "" && !strings.EqualFold(cond.ToolChoice, fp.ToolChoice) {
		return false
	}
	if cond.UserSuffix != "" {
		if !strings.HasSuffix(strings.ToLower(fp.LastUserText), strings.ToLower(cond.UserSuffix)) {
			return false
		}
	}
	if cond.Feature != "" {
		want := strings.ToLower(strings.TrimSpace(cond.Feature))
		if want == "look_at" {
			want = "search"
		}
		got := fp.Feature()
		if got == "" || got != want {
			return false
		}
	}
	return true
}
