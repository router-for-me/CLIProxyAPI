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
	TitlingToolName = "set_title"
)

// Hardcoded system-prompt prefixes used by the Amp client binary to invoke
// specific features. These literals appear as JS string constants in the
// bundled Amp executable, so they are stable per Amp release and only
// change when Amp itself is upgraded. Keep prefixes long enough to avoid
// collisions but short enough to tolerate trailing whitespace/newlines.
const (
	OraclePromptPrefix    = "you are the oracle - an expert ai advisor"
	SearchPromptPrefix    = "you are a fast, parallel code search agent."
	LookAtPromptPrefix    = "you are an ai assistant that analyzes files for a software engineer."
	ReviewPromptPrefix    = "you are an expert software engineer reviewing code changes."
	TitlingPromptPrefix   = "you are an assistant that generates short, descriptive titles"
	HandoffPromptPrefix   = "you are an assistant tasked with creating a handoff context"
	LibrarianPromptPrefix = "you are the librarian, a specialized codebase understanding agent"
)

// RequestFingerprint captures the per-request features a mapping condition
// can match against. All fields are optional; absent fields cannot match.
type RequestFingerprint struct {
	// ToolChoice is the forced tool name extracted from the request body.
	// Anthropic: tool_choice.name; OpenAI: tool_choice.function.name;
	// Gemini: toolConfig.functionCallingConfig.allowedFunctionNames[0]
	// when mode is ANY and exactly one tool is allowed.
	ToolChoice string

	// LastUserText is the trimmed text content of the final user-role message,
	// used by AmpMappingCondition.UserSuffix.
	LastUserText string

	// SystemText is the concatenated text of the request's system /
	// systemInstruction / instructions field. Used by SystemPrefix matching
	// and by Feature() to recognize features whose system prompts are
	// hardcoded in the Amp client binary.
	SystemText string

	// HasImageOutput indicates the request asks the upstream model to emit
	// an image (Gemini generationConfig.responseModalities contains
	// "IMAGE"). Used to identify the Amp painter feature.
	HasImageOutput bool
}

// Feature returns the canonical Amp feature alias inferred from the
// fingerprint, or an empty string if no high-level feature can be deduced.
//
// Detection order (first match wins):
//  1. Forced tool name (most reliable; emitted by Amp's tool_choice).
//  2. responseModalities=IMAGE (painter).
//  3. System-prompt prefix (hardcoded literals in the Amp binary).
//  4. User-suffix heuristic (handoff fallback).
func (f RequestFingerprint) Feature() string {
	switch {
	case strings.EqualFold(f.ToolChoice, HandoffToolName):
		return "handoff"
	case strings.EqualFold(f.ToolChoice, TitlingToolName):
		return "titling"
	}

	if f.HasImageOutput {
		return "painter"
	}

	sys := strings.ToLower(strings.TrimSpace(f.SystemText))
	if sys != "" {
		switch {
		case strings.HasPrefix(sys, OraclePromptPrefix):
			return "oracle"
		case strings.HasPrefix(sys, SearchPromptPrefix):
			return "search"
		case strings.HasPrefix(sys, LookAtPromptPrefix):
			return "look_at"
		case strings.HasPrefix(sys, ReviewPromptPrefix):
			return "review"
		case strings.HasPrefix(sys, LibrarianPromptPrefix):
			return "librarian"
		case strings.HasPrefix(sys, TitlingPromptPrefix):
			return "titling"
		case strings.HasPrefix(sys, HandoffPromptPrefix):
			return "handoff"
		}
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
		ToolChoice:     extractToolChoiceName(body),
		LastUserText:   extractLastUserText(body),
		SystemText:     extractSystemText(body),
		HasImageOutput: extractHasImageOutput(body),
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
			if len(arr) == 1 && arr[0].Type == gjson.String {
				return arr[0].String()
			}
		}
	}
	return ""
}

// extractLastUserText returns the last user-role textual content from
// Anthropic, OpenAI, or Gemini schemas. For Gemini contents[], a missing
// role is treated as user (single-turn requests sometimes omit it). Only
// the trailing text is needed for suffix matching; multi-part contents
// are concatenated in order.
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
	// OpenAI Responses: input[] with role+content[].text/input_text.
	if inp := gjson.GetBytes(body, "input"); inp.IsArray() {
		arr := inp.Array()
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
			role := m.Get("role").String()
			if role != "" && !strings.EqualFold(role, "user") {
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

// extractSystemText returns the textual system / systemInstruction /
// instructions content of the request, concatenating multiple parts in
// order. Anthropic and OpenAI accept either a string or an array of typed
// parts; Gemini wraps it in systemInstruction.parts[].text. OpenAI
// Responses additionally allows a system-role entry inside input[].
func extractSystemText(body []byte) string {
	// Anthropic / OpenAI Chat: top-level "system" (string or array).
	if v := gjson.GetBytes(body, "system"); v.Exists() {
		if s := strings.TrimSpace(messageContentText(v)); s != "" {
			return s
		}
	}
	// OpenAI Responses: top-level "instructions" (string).
	if v := gjson.GetBytes(body, "instructions"); v.Exists() && v.Type == gjson.String {
		if s := strings.TrimSpace(v.String()); s != "" {
			return s
		}
	}
	// OpenAI Responses: input[] entries with role=system/developer.
	if inp := gjson.GetBytes(body, "input"); inp.IsArray() {
		for _, m := range inp.Array() {
			role := strings.ToLower(m.Get("role").String())
			if role != "system" && role != "developer" {
				continue
			}
			if s := strings.TrimSpace(messageContentText(m.Get("content"))); s != "" {
				return s
			}
		}
	}
	// Gemini: systemInstruction.parts[].text
	if si := gjson.GetBytes(body, "systemInstruction"); si.Exists() {
		var b strings.Builder
		for _, p := range si.Get("parts").Array() {
			if t := p.Get("text"); t.Exists() {
				b.WriteString(t.String())
			}
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			return s
		}
	}
	return ""
}

// extractHasImageOutput reports whether the request asks Gemini to emit
// an image (used by Amp's painter feature). Only Gemini exposes this hint
// via generationConfig.responseModalities.
func extractHasImageOutput(body []byte) bool {
	mods := gjson.GetBytes(body, "generationConfig.responseModalities")
	if !mods.IsArray() {
		return false
	}
	for _, m := range mods.Array() {
		if strings.EqualFold(m.String(), "IMAGE") {
			return true
		}
	}
	return false
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
	if tc := strings.TrimSpace(cond.ToolChoice); tc != "" {
		if !strings.EqualFold(tc, strings.TrimSpace(fp.ToolChoice)) {
			return false
		}
	}
	if suffix := strings.TrimSpace(cond.UserSuffix); suffix != "" {
		if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(fp.LastUserText)), strings.ToLower(suffix)) {
			return false
		}
	}
	if prefix := strings.TrimSpace(cond.SystemPrefix); prefix != "" {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(fp.SystemText)), strings.ToLower(prefix)) {
			return false
		}
	}
	if feature := strings.TrimSpace(cond.Feature); feature != "" {
		want := strings.ToLower(feature)
		got := fp.Feature()
		if got == "" || got != want {
			return false
		}
	}
	return true
}
