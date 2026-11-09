package payloadcompress

import (
	"encoding/json"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// progressive-aging (upstream progressiveAging.ts): older turns are compressed
// more aggressively than recent ones. Default thresholds: the last two
// messages stay verbatim, the fourth-from-last gets lite, distance 3 gets
// caveman, anything older is reduced to a summary. Chat shape only.
var agingThresholds = struct {
	verbatim, light, moderate int
}{verbatim: 2, light: 2, moderate: 3}

func applyProgressiveAging(combo *Combo, body []byte) []byte {
	step, ok := comboStep(combo, EngineProgressiveAging)
	if !ok {
		return body
	}
	doc := ojson.NewDocString(string(body))
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return body
	}
	items := messages.Array()
	if len(items) == 0 {
		return body
	}
	lastUser := -1
	for i, item := range items {
		if item.Get("role").String() == "user" {
			lastUser = i
		}
	}
	lite := liteEngine{opts: step.Options}
	caveman := cavemanEngine{level: step.Level}
	kept := make([]string, 0, len(items))
	changed := false
	for i, item := range items {
		role := item.Get("role").String()
		text := agingExtractText(item.Get("content"))
		if role == "system" || strings.HasPrefix(text, "[COMPRESSED:") || i == lastUser {
			kept = append(kept, item.Raw)
			continue
		}
		distance := len(items) - 1 - i
		switch {
		case distance <= agingThresholds.verbatim:
			kept = append(kept, item.Raw)
		case distance <= agingThresholds.light:
			kept = append(kept, agingRewrite(item, func() (string, bool) {
				return lite.compressKind(kindUser, text)
			}, "light"))
			changed = agingChanged(item.Raw, kept[len(kept)-1]) || changed
		case distance <= agingThresholds.moderate:
			kept = append(kept, agingRewrite(item, func() (string, bool) {
				return caveman.compressKind(kindUser, text)
			}, "moderate"))
			changed = agingChanged(item.Raw, kept[len(kept)-1]) || changed
		case role == "assistant":
			kept = append(kept, agingTag(item, text, agingTruncate(text, 200), "fullSummary"))
			changed = agingChanged(item.Raw, kept[len(kept)-1]) || changed
		case role == "user":
			kept = append(kept, agingTag(item, text, agingFirstLine(text, 120), "fullSummary"))
			changed = agingChanged(item.Raw, kept[len(kept)-1]) || changed
		default:
			kept = append(kept, item.Raw)
		}
	}
	if !changed {
		return body
	}
	if err := doc.SetRaw("messages", "["+strings.Join(kept, ",")+"]"); err != nil {
		return body
	}
	return []byte(doc.String())
}

func comboStep(combo *Combo, name string) (Step, bool) {
	for _, step := range combo.Pipeline {
		if step.Engine == name {
			return step, true
		}
	}
	return Step{}, false
}

// agingRewrite applies one tier's engine and tags the result. Untouched text
// stays byte-identical.
func agingRewrite(item ojson.Result, compress func() (string, bool), tier string) string {
	out, hit := compress()
	if !hit || out == "" {
		return item.Raw
	}
	return agingTag(item, agingExtractText(item.Get("content")), out, tier)
}

// agingTag builds the tagged content and writes it back into the message.
// Structured content is left untouched so it stays parseable.
func agingTag(item ojson.Result, original, compressed, tier string) string {
	switch agingStructuredKind(original) {
	case "json":
		return item.Raw
	case "fenced":
		return agingReplaceContent(item, "[COMPRESSED:aging:"+tier+"]\n"+original)
	}
	return agingReplaceContent(item, "[COMPRESSED:aging:"+tier+"] "+compressed)
}

func agingStructuredKind(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") && strings.HasSuffix(trimmed, "```") {
		return "fenced"
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if json.Valid([]byte(trimmed)) {
			return "json"
		}
	}
	return ""
}

func agingExtractText(content ojson.Result) string {
	if content.Type == ojson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	for _, part := range content.Array() {
		if agingIsTextBlock(part) {
			if text := part.Get("text").String(); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func agingIsTextBlock(part ojson.Result) bool {
	if part.Get("text").Type != ojson.String {
		return false
	}
	if !part.Get("type").Exists() {
		return true
	}
	switch part.Get("type").String() {
	case "text", "input_text":
		return true
	}
	return false
}

// agingReplaceContent mirrors upstream replaceTextContent: the first text block
// receives the new text, trailing text blocks subsumed by it are dropped.
func agingReplaceContent(item ojson.Result, newText string) string {
	sub := ojson.NewDocString(item.Raw)
	content := sub.Get("content")
	if content.Type == ojson.String || !content.IsArray() {
		if err := sub.Set("content", newText); err != nil {
			return item.Raw
		}
		return sub.String()
	}
	parts := content.Array()
	out := make([]string, 0, len(parts))
	replaced := false
	hasToolResult := false
	for _, part := range parts {
		if part.Get("type").String() == "tool_result" {
			hasToolResult = true
		}
		if !agingIsTextBlock(part) {
			out = append(out, part.Raw)
			continue
		}
		if !replaced {
			replaced = true
			block := ojson.NewDocString(part.Raw)
			if err := block.Set("text", newText); err != nil {
				return item.Raw
			}
			out = append(out, block.String())
			continue
		}
		if partText := part.Get("text").String(); partText != "" && !strings.Contains(newText, partText) {
			out = append(out, part.Raw)
		}
	}
	if !replaced {
		block := `{"type":"text","text":` + string(appendJSONString(nil, newText)) + `}`
		if hasToolResult {
			out = append(out, block)
		} else {
			out = append([]string{block}, out...)
		}
	}
	if err := sub.SetRaw("content", "["+strings.Join(out, ",")+"]"); err != nil {
		return item.Raw
	}
	return sub.String()
}

func agingChanged(before, after string) bool { return before != after }

func agingTruncate(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n])
}

func agingFirstLine(text string, n int) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return agingTruncate(text, n)
}
