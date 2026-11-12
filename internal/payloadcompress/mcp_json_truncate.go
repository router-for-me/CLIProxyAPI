package payloadcompress

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// A JSON tool result must stay parseable. Upstream's smartFilterText checks the
// raw text for JSON before running the line-oriented noise/collapse pipeline and,
// when it is over budget, shrinks the `content` field and re-serializes (see
// engines/mcpAccessibility/index.ts). A head slice of the serialized string would
// hand the model invalid JSON.
//
// The port keeps the same shape with encoding/json. Key order differs (Go sorts
// map keys, JSON.stringify preserves insertion order), which a model reading a
// tool result does not care about and which keeps the transform deterministic.

const (
	// Re-fit attempts when JSON escaping makes the re-serialized result overshoot.
	mcpJSONFitAttempts = 8
	// Field name of the truncation mark, identical to upstream.
	mcpJSONTruncatedField = "truncated"
)

// mcpTruncationHint is upstream's MCP_TEXT_TRUNCATION_HINT. Deliberately not a
// web-page hint: the filter also truncates non-navigable tool output.
const mcpTruncationHint = "The result exceeded the size limit and was truncated. Re-run the same tool call with a bounded " +
	"slice to read the rest: a narrower query, limit/offset pagination, or mode: \"lines\" / mode: \"grep\" with an offset."

// mcpTruncateJSON shrinks an oversized JSON object's `content` field so the
// re-serialized result fits maxChars. It reports false when the text is not a
// JSON object, has no truncatable content, or cannot be brought under the limit
// (its other fields alone are too large) — the caller then falls back to the
// bounded preview envelope.
func mcpTruncateJSON(text string, maxChars int) (string, bool) {
	if maxChars <= 0 {
		return "", false
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil || value == nil {
		return "", false
	}
	if !mcpHasTruncatableContent(value["content"]) {
		return "", false
	}
	indented := mcpIsIndentedJSON(text)
	// The budget is what `content` may occupy once everything else is accounted
	// for, so it is measured against the envelope (content emptied, marks set).
	budget := maxChars - len(mcpMarshalJSON(mcpEnvelopeValue(value), indented))
	if budget < 0 {
		budget = 0
	}
	for attempt := 0; attempt < mcpJSONFitAttempts; attempt++ {
		shrunk, changed := mcpShrinkContent(value, budget)
		if !changed {
			return "", false
		}
		serialized := mcpMarshalJSON(shrunk, indented)
		if len(serialized) <= maxChars {
			return serialized, true
		}
		if budget == 0 {
			return "", false
		}
		budget -= len(serialized) - maxChars
		if budget < 0 {
			budget = 0
		}
	}
	return "", false
}

// mcpHasTruncatableContent reports whether the parsed `content` is a shape the
// filter can shrink: a plain string, or a non-empty array of text blocks.
// Anything else (absent or opaque) is left alone — the filter must not invent a
// new shape for a result it does not understand.
func mcpHasTruncatableContent(content any) bool {
	switch v := content.(type) {
	case string:
		return true
	case []any:
		if len(v) == 0 {
			return false
		}
		for _, raw := range v {
			if _, ok := mcpTextBlock(v2s(raw)); !ok {
				return false
			}
		}
		return true
	}
	return false
}

// mcpEnvelopeValue is a shallow copy with `content` emptied and the truncation
// marks applied: the smallest the result can serialize to while keeping its shape.
func mcpEnvelopeValue(value map[string]any) map[string]any {
	clone := make(map[string]any, len(value)+2)
	for k, v := range value {
		clone[k] = v
	}
	switch content := value["content"].(type) {
	case string:
		clone["content"] = ""
	case []any:
		blocks := make([]any, len(content))
		for i, raw := range content {
			block := v2s(raw)
			if _, ok := mcpTextBlock(block); !ok {
				blocks[i] = raw
				continue
			}
			emptied := make(map[string]any, len(block))
			for k, v := range block {
				emptied[k] = v
			}
			emptied["text"] = ""
			blocks[i] = emptied
		}
		clone["content"] = blocks
	}
	clone[mcpJSONTruncatedField] = true
	clone["hint"] = mcpTruncationHint
	return clone
}

// mcpTextBlock returns the text of a {type:"text", text:<string>} block.
func mcpTextBlock(block map[string]any) (string, bool) {
	if block == nil || block["type"] != "text" {
		return "", false
	}
	text, ok := block["text"].(string)
	return text, ok
}

// mcpShrinkContent returns a shallow copy with `content` cut to budget: a string
// is sliced, each text block gets its even share and carries the truncation mark.
// It reports false when the content already fits, so the caller can fall back.
func mcpShrinkContent(value map[string]any, budget int) (map[string]any, bool) {
	clone := make(map[string]any, len(value)+2)
	for k, v := range value {
		clone[k] = v
	}
	switch content := value["content"].(type) {
	case string:
		if len(content) <= budget {
			return clone, false
		}
		clone["content"] = mcpSlice(content, budget)
	case []any:
		blocks := make([]any, len(content))
		share := budget / len(content)
		changed := false
		for i, raw := range content {
			block := v2s(raw)
			text, ok := mcpTextBlock(block)
			if !ok || len(text) <= share {
				blocks[i] = raw
				continue
			}
			marked := make(map[string]any, len(block)+2)
			for k, v := range block {
				marked[k] = v
			}
			marked["text"] = mcpSlice(text, share)
			marked[mcpJSONTruncatedField] = true
			marked["hint"] = mcpTruncationHint
			blocks[i] = marked
			changed = true
		}
		if !changed {
			return clone, false
		}
		clone["content"] = blocks
	default:
		return clone, false
	}
	clone[mcpJSONTruncatedField] = true
	clone["hint"] = mcpTruncationHint
	return clone, true
}

// mcpSlice cuts s to at most n bytes without splitting a UTF-8 sequence, so the
// re-serialized text never carries a replacement character.
func mcpSlice(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// mcpBuildJSONEnvelope is the bounded fallback for a result that cannot shrink in
// place: `{truncated, hint, originalChars, preview}`.
func mcpBuildJSONEnvelope(text string, maxChars int) string {
	build := func(keep int) string {
		return mcpMarshalJSON(map[string]any{
			mcpJSONTruncatedField: true,
			"hint":                mcpTruncationHint,
			"originalChars":       len(text),
			"preview":             mcpSlice(text, keep),
		}, false)
	}
	keep := maxChars - len(build(0))
	if keep < 0 {
		keep = 0
	}
	out := build(keep)
	// Escaping (quotes, backslashes, newlines) expands the preview: drop the overshoot.
	for len(out) > maxChars && keep > 0 {
		keep -= len(out) - maxChars
		if keep < 0 {
			keep = 0
		}
		out = build(keep)
	}
	return out
}

// mcpIsIndentedJSON reports whether the document is pretty-printed. Compact JSON
// has no newlines; a newline followed by indentation in the head marks it.
func mcpIsIndentedJSON(text string) bool {
	head := text
	if len(head) > 200 {
		head = head[:200]
	}
	return strings.Contains(head, "\n  ")
}

func mcpMarshalJSON(value map[string]any, indented bool) string {
	var (
		out []byte
		err error
	)
	if indented {
		out, err = json.MarshalIndent(value, "", "  ")
	} else {
		out, err = json.Marshal(value)
	}
	if err != nil {
		return ""
	}
	return string(out)
}

// v2s narrows an any to a string-keyed object, returning nil for anything else.
func v2s(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}
