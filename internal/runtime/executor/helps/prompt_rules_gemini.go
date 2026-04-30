package helps

import (
	"fmt"
	"regexp"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// geminiPromptFmt implements prompt-rule mutations for the Gemini source format.
// rootPrefix differentiates plain "gemini" (rootPrefix="") from "gemini-cli"
// (rootPrefix="request") — the gemini-cli source format wraps the entire Gemini
// request shape under a top-level "request" object, which is verified at the
// translator/gemini-cli/* and runtime/executor/gemini_cli_executor.go path
// usages (e.g., request.systemInstruction, request.contents).
//
// The system prompt lives in either systemInstruction (camelCase, used by
// OpenAI→Gemini and most clients) OR system_instruction (snake_case, used by
// Claude→Gemini per the original Codex review §5). User messages live in
// contents[?role=="user"] with parts that can include text, functionResponse,
// functionCall, or inlineData blocks.
type geminiPromptFmt struct {
	rootPrefix string
}

func newGeminiPromptFmt(rootPrefix string) *geminiPromptFmt {
	return &geminiPromptFmt{rootPrefix: rootPrefix}
}

const (
	geminiSystemInstructionCamel = "systemInstruction"
	geminiSystemInstructionSnake = "system_instruction"
)

// prefixedPath joins the format's optional root prefix with a relative path.
// e.g., for gemini-cli with rootPrefix="request" and rel="systemInstruction",
// returns "request.systemInstruction". For plain gemini, returns rel unchanged.
func (g *geminiPromptFmt) prefixedPath(rel string) string {
	if g.rootPrefix == "" {
		return rel
	}
	if rel == "" {
		return g.rootPrefix
	}
	return g.rootPrefix + "." + rel
}

func (g *geminiPromptFmt) InjectSystem(payload []byte, content, marker, position string) []byte {
	if len(content) == 0 {
		return payload
	}
	field := g.activeSystemField(payload)
	if field == "" {
		// Neither variant exists — create the camelCase shape under our root.
		instruction := map[string]any{
			"role":  "system",
			"parts": []map[string]any{{"text": content}},
		}
		raw, err := marshalJSONNoEscape(instruction)
		if err != nil {
			return payload
		}
		updated, err := sjson.SetRawBytes(payload, g.prefixedPath(geminiSystemInstructionCamel), raw)
		if err != nil {
			return payload
		}
		return updated
	}
	parts := gjson.GetBytes(payload, field+".parts")
	if !parts.IsArray() {
		// Replace the whole field with a fresh single-part shape.
		instruction := map[string]any{
			"role":  "system",
			"parts": []map[string]any{{"text": content}},
		}
		raw, err := marshalJSONNoEscape(instruction)
		if err != nil {
			return payload
		}
		updated, err := sjson.SetRawBytes(payload, field, raw)
		if err != nil {
			return payload
		}
		return updated
	}
	// Skip if any text part carries the marker.
	for _, p := range parts.Array() {
		if p.Get("text").Exists() && containsMarker(p.Get("text").String(), marker) {
			return payload
		}
	}
	newPart, err := marshalJSONNoEscape(map[string]any{"text": content})
	if err != nil {
		return payload
	}
	if position == "append" {
		updated, err := sjson.SetRawBytes(payload, field+".parts.-1", newPart)
		if err != nil {
			return payload
		}
		return updated
	}
	return prependArrayElement(payload, field+".parts", newPart)
}

func (g *geminiPromptFmt) StripSystem(payload []byte, re *regexp.Regexp) []byte {
	field := g.activeSystemField(payload)
	if field == "" {
		return payload
	}
	parts := gjson.GetBytes(payload, field+".parts")
	if !parts.IsArray() {
		return payload
	}
	out := payload
	for i, p := range parts.Array() {
		tx := p.Get("text")
		if !tx.Exists() {
			continue
		}
		s := tx.String()
		stripped := re.ReplaceAllString(s, "")
		if stripped == s {
			continue
		}
		if updated, err := sjson.SetBytes(out, fmt.Sprintf("%s.parts.%d.text", field, i), stripped); err == nil {
			out = updated
		}
	}
	return out
}

func (g *geminiPromptFmt) InjectLastUser(payload []byte, content, marker, position string) []byte {
	if len(content) == 0 {
		return payload
	}
	contents := gjson.GetBytes(payload, g.prefixedPath("contents"))
	if !contents.IsArray() {
		return payload
	}
	idx := geminiLastNaturalUserIndex(contents)
	if idx < 0 {
		// No natural-language user message; skip rather than synthesize one. A
		// phantom user message would change request semantics for empty / tool-only
		// histories. Documented design choice.
		return payload
	}
	return g.mutateContentParts(payload, idx, content, marker, position)
}

func (g *geminiPromptFmt) StripLastUser(payload []byte, re *regexp.Regexp) []byte {
	contents := gjson.GetBytes(payload, g.prefixedPath("contents"))
	if !contents.IsArray() {
		return payload
	}
	idx := geminiLastNaturalUserIndex(contents)
	if idx < 0 {
		return payload
	}
	return g.stripContentParts(payload, idx, re)
}

// activeSystemField returns the prefixed field path for whichever shape exists.
// camelCase wins when both are present (canonical form). Empty string means
// neither exists.
func (g *geminiPromptFmt) activeSystemField(payload []byte) string {
	camel := g.prefixedPath(geminiSystemInstructionCamel)
	if gjson.GetBytes(payload, camel).Exists() {
		return camel
	}
	snake := g.prefixedPath(geminiSystemInstructionSnake)
	if gjson.GetBytes(payload, snake).Exists() {
		return snake
	}
	return ""
}

// geminiLastNaturalUserIndex finds the last item with role=="user" whose parts
// contain at least one part with a non-empty text field. Skips items whose
// parts are purely functionResponse, functionCall, or inlineData (image/file).
func geminiLastNaturalUserIndex(contents gjson.Result) int {
	arr := contents.Array()
	for i := len(arr) - 1; i >= 0; i-- {
		item := arr[i]
		if item.Get("role").String() != "user" {
			continue
		}
		parts := item.Get("parts")
		if !parts.IsArray() {
			continue
		}
		for _, p := range parts.Array() {
			if hasNonEmptyText(p, "text") {
				return i
			}
		}
	}
	return -1
}

func (g *geminiPromptFmt) mutateContentParts(payload []byte, idx int, content, marker, position string) []byte {
	path := fmt.Sprintf("%s.%d.parts", g.prefixedPath("contents"), idx)
	parts := gjson.GetBytes(payload, path)
	if !parts.IsArray() {
		return payload
	}
	for _, p := range parts.Array() {
		if p.Get("text").Exists() && containsMarker(p.Get("text").String(), marker) {
			return payload
		}
	}
	newPart, err := marshalJSONNoEscape(map[string]any{"text": content})
	if err != nil {
		return payload
	}
	if position == "append" {
		updated, err := sjson.SetRawBytes(payload, path+".-1", newPart)
		if err != nil {
			return payload
		}
		return updated
	}
	return prependArrayElement(payload, path, newPart)
}

func (g *geminiPromptFmt) stripContentParts(payload []byte, idx int, re *regexp.Regexp) []byte {
	path := fmt.Sprintf("%s.%d.parts", g.prefixedPath("contents"), idx)
	parts := gjson.GetBytes(payload, path)
	if !parts.IsArray() {
		return payload
	}
	out := payload
	for i, p := range parts.Array() {
		tx := p.Get("text")
		if !tx.Exists() {
			continue
		}
		s := tx.String()
		stripped := re.ReplaceAllString(s, "")
		if stripped == s {
			continue
		}
		if updated, err := sjson.SetBytes(out, fmt.Sprintf("%s.%d.text", path, i), stripped); err == nil {
			out = updated
		}
	}
	return out
}
