package prompt

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var promptXMLTextEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

var promptXMLNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]*$`)

const (
	promptEPSEToolCallsOpen  = "<|EPSE|tool_calls>"
	promptEPSEToolCallsClose = "</|EPSE|tool_calls>"
	promptEPSEInvokeOpen     = "<|EPSE|invoke"
	promptEPSEInvokeClose    = "</|EPSE|invoke>"
	promptEPSEParameterOpen  = "<|EPSE|parameter"
	promptEPSEParameterClose = "</|EPSE|parameter>"
)

// FormatToolCallsForPrompt renders a tool_calls slice into the prompt-visible
// invoke/parameter history block used across adapters.
func FormatToolCallsForPrompt(raw any) string {
	calls, ok := raw.([]any)
	if !ok || len(calls) == 0 {
		return ""
	}

	blocks := make([]string, 0, len(calls))
	for _, item := range calls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		block := formatToolCallForPrompt(call)
		if block != "" {
			blocks = append(blocks, block)
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return promptEPSEToolCallsOpen + "\n" + strings.Join(blocks, "\n") + "\n" + promptEPSEToolCallsClose
}

// StringifyToolCallArguments normalizes tool arguments into a compact string
// while preserving raw concatenated payloads when they already look like model
// output rather than a single JSON object.
func StringifyToolCallArguments(v any) string {
	switch x := v.(type) {
	case nil:
		return "{}"
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return "{}"
		}
		s = normalizeToolArgumentString(s)
		if s == "" {
			return "{}"
		}
		return s
	default:
		b, err := json.Marshal(x)
		if err != nil || len(b) == 0 {
			return "{}"
		}
		return string(b)
	}
}

func formatToolCallForPrompt(call map[string]any) string {
	if call == nil {
		return ""
	}

	name := strings.TrimSpace(asString(call["name"]))
	fn, _ := call["function"].(map[string]any)
	if name == "" && fn != nil {
		name = strings.TrimSpace(asString(fn["name"]))
	}
	if name == "" {
		return ""
	}

	argsRaw := call["arguments"]
	if argsRaw == nil {
		argsRaw = call["input"]
	}
	if argsRaw == nil && fn != nil {
		argsRaw = fn["arguments"]
		if argsRaw == nil {
			argsRaw = fn["input"]
		}
	}

	parameters := formatToolCallParametersForPrompt(argsRaw)
	if parameters == "" {
		return `  ` + promptEPSEInvokeOpen + ` name="` + escapeXMLAttribute(name) + `">` + promptEPSEInvokeClose
	}

	return "  " + promptEPSEInvokeOpen + " name=\"" + escapeXMLAttribute(name) + "\">\n" +
		parameters + "\n" +
		"  " + promptEPSEInvokeClose
}

func formatToolCallParametersForPrompt(raw any) string {
	value := normalizePromptToolCallValue(raw)
	body, ok := renderPromptToolParameters(value, "    ")
	if ok && strings.TrimSpace(body) != "" {
		return body
	}

	fallback := StringifyToolCallArguments(raw)
	if strings.TrimSpace(fallback) == "" {
		return ""
	}
	return "    " + promptEPSEParameterOpen + " name=\"content\">" + renderPromptXMLText(fallback) + promptEPSEParameterClose
}

func renderPromptToolParameters(value any, indent string) (string, bool) {
	switch v := value.(type) {
	case nil:
		return "", true
	case map[string]any:
		if len(v) == 0 {
			return "", true
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines := make([]string, 0, len(keys))
		for _, key := range keys {
			rendered, ok := renderPromptParameterNode(key, v[key], indent)
			if !ok {
				return "", false
			}
			lines = append(lines, rendered)
		}
		return strings.Join(lines, "\n"), true
	case []any:
		lines := make([]string, 0, len(v))
		for _, item := range v {
			rendered, ok := renderPromptParameterNode("item", item, indent)
			if !ok {
				return "", false
			}
			lines = append(lines, rendered)
		}
		return strings.Join(lines, "\n"), true
	case string:
		return indent + promptEPSEParameterOpen + ` name="content">` + renderPromptXMLText(v) + promptEPSEParameterClose, true
	default:
		return indent + promptEPSEParameterOpen + ` name="value">` + renderPromptXMLText(fmt.Sprint(v)) + promptEPSEParameterClose, true
	}
}

func renderPromptParameterNode(name string, value any, indent string) (string, bool) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return "", false
	}
	switch v := value.(type) {
	case nil:
		return indent + promptEPSEParameterOpen + ` name="` + escapeXMLAttribute(trimmedName) + `">` + promptEPSEParameterClose, true
	case map[string]any:
		body, ok := renderPromptToolXMLBody(v, indent+"  ")
		if !ok {
			return "", false
		}
		if strings.TrimSpace(body) == "" {
			return indent + promptEPSEParameterOpen + ` name="` + escapeXMLAttribute(trimmedName) + `">` + promptEPSEParameterClose, true
		}
		return indent + promptEPSEParameterOpen + ` name="` + escapeXMLAttribute(trimmedName) + "\">\n" + body + "\n" + indent + promptEPSEParameterClose, true
	case []any:
		body, ok := renderPromptToolXMLArray(v, indent+"  ")
		if !ok {
			return "", false
		}
		if strings.TrimSpace(body) == "" {
			return indent + promptEPSEParameterOpen + ` name="` + escapeXMLAttribute(trimmedName) + `">` + promptEPSEParameterClose, true
		}
		return indent + promptEPSEParameterOpen + ` name="` + escapeXMLAttribute(trimmedName) + "\">\n" + body + "\n" + indent + promptEPSEParameterClose, true
	case string:
		return indent + promptEPSEParameterOpen + ` name="` + escapeXMLAttribute(trimmedName) + `">` + renderPromptXMLText(v) + promptEPSEParameterClose, true
	default:
		return indent + promptEPSEParameterOpen + ` name="` + escapeXMLAttribute(trimmedName) + `">` + renderPromptXMLText(fmt.Sprint(v)) + promptEPSEParameterClose, true
	}
}

func normalizePromptToolCallValue(raw any) any {
	switch x := raw.(type) {
	case nil:
		return nil
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return ""
		}
		var parsed any
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			return parsed
		}
		return x
	default:
		return x
	}
}

func renderPromptToolXMLBody(value any, indent string) (string, bool) {
	switch v := value.(type) {
	case nil:
		return "", true
	case map[string]any:
		return renderPromptToolXMLMap(v, indent)
	case []any:
		return renderPromptToolXMLArray(v, indent)
	case string:
		return indent + "<content>" + renderPromptXMLText(v) + "</content>", true
	case bool, float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return indent + "<value>" + escapeXMLText(fmt.Sprint(v)) + "</value>", true
	default:
		return indent + "<value>" + renderPromptXMLText(fmt.Sprint(v)) + "</value>", true
	}
}

func renderPromptToolXMLMap(m map[string]any, indent string) (string, bool) {
	if len(m) == 0 {
		return "", true
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if !isValidPromptXMLName(k) {
			return "", false
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		rendered, ok := renderPromptToolXMLNode(key, m[key], indent)
		if !ok {
			return "", false
		}
		lines = append(lines, rendered)
	}
	return strings.Join(lines, "\n"), true
}

func renderPromptToolXMLArray(items []any, indent string) (string, bool) {
	if len(items) == 0 {
		return "", true
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		rendered, ok := renderPromptToolXMLNode("item", item, indent)
		if !ok {
			return "", false
		}
		lines = append(lines, rendered)
	}
	return strings.Join(lines, "\n"), true
}

func renderPromptToolXMLNode(name string, value any, indent string) (string, bool) {
	if !isValidPromptXMLName(name) {
		return "", false
	}
	switch v := value.(type) {
	case nil:
		return indent + "<" + name + "></" + name + ">", true
	case map[string]any:
		inner, ok := renderPromptToolXMLMap(v, indent+"  ")
		if !ok {
			return "", false
		}
		if strings.TrimSpace(inner) == "" {
			return indent + "<" + name + "></" + name + ">", true
		}
		return indent + "<" + name + ">\n" + inner + "\n" + indent + "</" + name + ">", true
	case []any:
		if len(v) == 0 {
			return indent + "<" + name + "></" + name + ">", true
		}
		lines := make([]string, 0, len(v))
		for _, item := range v {
			rendered, ok := renderPromptToolXMLNode(name, item, indent)
			if !ok {
				return "", false
			}
			lines = append(lines, rendered)
		}
		return strings.Join(lines, "\n"), true
	case string:
		return indent + "<" + name + ">" + renderPromptXMLText(v) + "</" + name + ">", true
	case bool, float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return indent + "<" + name + ">" + escapeXMLText(fmt.Sprint(v)) + "</" + name + ">", true
	default:
		return indent + "<" + name + ">" + renderPromptXMLText(fmt.Sprint(v)) + "</" + name + ">", true
	}
}

// renderPromptXMLText emits CDATA for every string so prompt-visible tool
// history stays uniform and does not drift back toward ad-hoc escaping.
func renderPromptXMLText(text string) string {
	if text == "" {
		return ""
	}
	if strings.Contains(text, "]]>") {
		return "<![CDATA[" + strings.ReplaceAll(text, "]]>", "]]]]><![CDATA[>") + "]]>"
	}
	return "<![CDATA[" + text + "]]>"
}

func isValidPromptXMLName(name string) bool {
	return promptXMLNamePattern.MatchString(strings.TrimSpace(name))
}

func escapeXMLAttribute(text string) string {
	if text == "" {
		return ""
	}
	return strings.NewReplacer(
		"&", "&amp;",
		`"`, "&quot;",
		"<", "&lt;",
		">", "&gt;",
	).Replace(text)
}

func normalizeToolArgumentString(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if looksLikeConcatenatedJSON(trimmed) {
		// Keep the original payload to avoid silently rewriting model output.
		return raw
	}
	return trimmed
}

func looksLikeConcatenatedJSON(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, "}{") || strings.Contains(trimmed, "][") {
		return true
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	var first any
	if err := dec.Decode(&first); err != nil {
		return false
	}
	var second any
	return dec.Decode(&second) == nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func escapeXMLText(v string) string {
	if v == "" {
		return ""
	}
	return promptXMLTextEscaper.Replace(v)
}
