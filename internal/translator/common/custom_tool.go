package common

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CustomToolInputKey is the synthetic JSON parameter name used when a freeform
// OpenAI `custom` tool (e.g. apply_patch) is downgraded into a regular
// `function` tool for backends (Claude/Gemini/DeepSeek) that do not natively
// support the Responses-API `custom` tool type.
//
// On the request side we declare the function with a single string parameter
// named CustomToolInputKey; on the response side we unwrap that argument back
// into the bare `input` text the Codex host expects for a custom_tool_call.
const CustomToolInputKey = "input"

// IsResponsesCustomTool reports whether a Responses-API tool entry is a
// freeform `custom` tool (the only current example is apply_patch).
func IsResponsesCustomTool(tool gjson.Result) bool {
	return strings.TrimSpace(tool.Get("type").String()) == "custom"
}

// CustomToolNamesFromRequest scans the original Responses-API request JSON and
// returns the set of tool names whose type is `custom`. The response side uses
// this to decide whether a tool call coming back from the backend must be
// re-emitted as a `custom_tool_call` (bare input text) instead of a
// `function_call` (JSON arguments).
func CustomToolNamesFromRequest(rawJSON []byte) map[string]struct{} {
	out := map[string]struct{}{}
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return out
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		if IsResponsesCustomTool(tool) {
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				out[name] = struct{}{}
			}
		}
		return true
	})
	return out
}

// RequestDeclaresTools reports whether the request snapshot declares a non-empty
// `tools` array. It distinguishes "snapshot available" (tools were declared, so
// the custom-tool set is authoritative) from "snapshot unavailable" (no tools
// found — either the request had none or the snapshot was lost). Callers use
// this to decide whether the defensive apply_patch fallback should fire: when
// the snapshot IS available and apply_patch is absent from the custom set, it is
// a deliberately-declared regular function and must NOT be force-customed.
func RequestDeclaresTools(rawJSON []byte) bool {
	tools := gjson.GetBytes(rawJSON, "tools")
	return tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
}

// CustomToolFunctionSchema returns the JSON Schema (as raw bytes) used when a
// freeform custom tool is downgraded to a regular function. The freeform tool
// carries no JSON-Schema parameters of its own (the model emits raw text), so
// we wrap it in a single required string parameter named CustomToolInputKey.
func CustomToolFunctionSchema() []byte {
	return []byte(`{"type":"object","properties":{"` + CustomToolInputKey +
		`":{"type":"string","description":"The full tool payload as a single raw text string."}},"required":["` +
		CustomToolInputKey + `"]}`)
}

// CustomToolDescription rewrites a freeform custom tool's description so it no
// longer instructs the model to avoid JSON (the original apply_patch
// description says "do not wrap the patch in JSON"). After downgrade the model
// MUST place the payload inside the JSON `input` argument, so the contradictory
// sentence is stripped and a clear instruction is appended.
func CustomToolDescription(original string) string {
	desc := strings.TrimSpace(original)
	// Remove the freeform-only instruction that conflicts with the function
	// (JSON-arguments) form we downgrade into.
	for _, bad := range []string{
		"This is a FREEFORM tool, so do not wrap the patch in JSON.",
		"This is a FREEFORM tool, so do not wrap the patch in JSON",
		"do not wrap the patch in JSON.",
		"do not wrap the patch in JSON",
		"This is a freeform tool, so do not wrap the patch in JSON.",
		"This is a freeform tool, so do not wrap the patch in JSON",
		"Do not wrap the patch in JSON.",
		"Do not wrap the patch in JSON",
	} {
		desc = strings.ReplaceAll(desc, bad, "")
	}
	desc = strings.TrimSpace(desc)
	suffix := "Provide the entire tool payload as a single string in the `" +
		CustomToolInputKey + "` argument."
	if desc == "" {
		return suffix
	}
	return desc + " " + suffix
}

// UnwrapCustomToolInput extracts the bare payload text from a downgraded
// function call's JSON arguments. The model was told to put the whole payload
// into the `input` string argument, so we read it back out. If the arguments
// are not the expected wrapper (e.g. the model emitted raw text anyway, or a
// different shape), the raw arguments string is returned unchanged as a
// best-effort fallback.
func UnwrapCustomToolInput(argumentsJSON string) string {
	trimmed := strings.TrimSpace(argumentsJSON)
	if trimmed == "" {
		return ""
	}
	if gjson.Valid(trimmed) {
		parsed := gjson.Parse(trimmed)
		if parsed.IsObject() {
			if v := parsed.Get(CustomToolInputKey); v.Exists() {
				return v.String()
			}
		}
	}
	// Some backends emit the wrapper with unescaped control characters (raw
	// newlines/tabs) inside the string value, which makes the payload invalid
	// JSON. Try escaping the common offenders and re-parsing before giving up.
	if escaped := escapeControlCharsInJSONString(trimmed); escaped != trimmed && gjson.Valid(escaped) {
		parsed := gjson.Parse(escaped)
		if parsed.IsObject() {
			if v := parsed.Get(CustomToolInputKey); v.Exists() {
				return v.String()
			}
		}
	}
	return argumentsJSON
}

// escapeControlCharsInJSONString escapes raw control characters (newline,
// carriage return, tab) inside a JSON-ish wrapper so a payload that arrived
// with literal control bytes can still be parsed. It only rewrites those
// bytes; it does not attempt full JSON repair.
func escapeControlCharsInJSONString(in string) string {
	var b strings.Builder
	b.Grow(len(in))
	inString := false // are we currently inside a double-quoted JSON string literal?
	escaped := false  // was the previous byte a backslash inside a string?
	for i := 0; i < len(in); i++ {
		ch := in[i]
		if inString {
			if escaped {
				// previous byte was '\\'; emit this byte verbatim (it's the escapee)
				b.WriteByte(ch)
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				b.WriteByte(ch)
				escaped = true
			case '"':
				b.WriteByte(ch)
				inString = false
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				b.WriteByte(ch)
			}
		} else {
			// Outside string literals: never escape. Structural whitespace
			// (newlines/indent in pretty-printed JSON) must be left untouched,
			// otherwise we'd corrupt valid JSON into invalid JSON.
			if ch == '"' {
				inString = true
			}
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// WrapCustomToolInput is the inverse of UnwrapCustomToolInput: given the bare
// input text from a historical custom_tool_call, it produces the JSON
// arguments string a downgraded function tool would carry, so multi-turn
// history replays consistently to the backend.
func WrapCustomToolInput(input string) string {
	out := []byte(`{}`)
	out, _ = sjson.SetBytes(out, CustomToolInputKey, input)
	return string(out)
}
