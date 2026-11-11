package payloadcompress

import (
	"strconv"

	"github.com/MoeForge/moego/ojson"
)

// Context editing edit strategies, mirroring upstream contextEditing.ts.
const (
	ContextEditClearToolUses = "clear_tool_uses_20250919"
	ContextEditClearThinking = "clear_thinking_20251015"
)

// ContextEditingBetaHeader is the Anthropic beta feature string required by
// requests carrying context_management.
const ContextEditingBetaHeader = "context-management-2025-06-27"

// ApplyContextEditing merges an Anthropic clear_tool_uses_20250919 edit into
// the body's context_management object, mirroring upstream contextEditing.ts:
// idempotent (an existing edit of the same type is kept), other edits are
// preserved, and edits are stable-sorted so clear_thinking edits come first.
// It reports whether the body actually changed.
func ApplyContextEditing(doc *ojson.Doc, triggerTokens, keepToolUses int) bool {
	if doc == nil || triggerTokens <= 0 || keepToolUses <= 0 {
		return false
	}
	edit := `{"type":"` + ContextEditClearToolUses + `","trigger":{"type":"input_tokens","value":` +
		strconv.Itoa(triggerTokens) + `},"keep":{"type":"tool_uses","value":` +
		strconv.Itoa(keepToolUses) + `}}`

	cm := doc.Get("context_management")
	if !cm.Exists() {
		if err := doc.SetRawBytes("context_management", []byte(`{"edits":[`+edit+`]}`)); err != nil {
			return false
		}
		return true
	}
	if !cm.IsObject() {
		// Caller junk at this key cannot be merged; leave it caller-owned.
		return false
	}

	editsV := doc.Get("context_management.edits")
	var edits []string
	if editsV.IsArray() {
		for _, e := range editsV.Array() {
			if e.IsObject() {
				edits = append(edits, e.Raw)
			}
		}
	}
	changed := false
	if len(edits) == 0 {
		edits = []string{edit}
		changed = true
	} else {
		found := false
		for _, e := range edits {
			if ojson.NewDocString(e).Get("type").String() == ContextEditClearToolUses {
				found = true
				break
			}
		}
		if !found {
			edits = append(edits, edit)
			changed = true
		}
	}

	// Stable sort: clear_thinking edits first, everything else keeps its order.
	sorted := make([]string, len(edits))
	i := 0
	for _, e := range edits {
		if ojson.NewDocString(e).Get("type").String() == ContextEditClearThinking {
			sorted[i] = e
			i++
		}
	}
	for _, e := range edits {
		if ojson.NewDocString(e).Get("type").String() != ContextEditClearThinking {
			sorted[i] = e
			i++
		}
	}
	for j := range sorted {
		if j < len(edits) && sorted[j] != edits[j] {
			changed = true
		}
	}
	if !changed {
		return false
	}
	raw := "["
	for j, e := range sorted {
		if j > 0 {
			raw += ","
		}
		raw += e
	}
	raw += "]"
	if err := doc.SetRawBytes("context_management.edits", []byte(raw)); err != nil {
		return false
	}
	return true
}
