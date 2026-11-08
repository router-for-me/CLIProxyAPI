package helps

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// NormalizeCodexCompatToolOutputsDoc preserves standalone text tool outputs as
// user context for opted-in API-key models whose upstream requires call_id.
// Only unreferenced function_call_output items with string output are rewritten.
func NormalizeCodexCompatToolOutputsDoc(doc *ojson.Doc, enabled bool) {
	if !enabled || doc == nil {
		return
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return
	}
	for i, item := range input.Array() {
		if item.Get("type").String() != "function_call_output" || item.Get("output").Type != ojson.String {
			continue
		}
		if codexToolOutputHasCallReference(item) {
			continue
		}
		name := item.Get("name").String()
		namespace := item.Get("namespace").String()
		// Delegation outputs have their own opt-in compatibility policy.
		if namespace == "codex_app" && (name == "create_thread" || name == "send_message_to_thread") {
			continue
		}
		if name == "" {
			name = "tool"
		}
		if namespace != "" {
			name = namespace + "." + name
		}
		text := "Tool output from " + name + ":\n" + item.Get("output").String()
		path := fmt.Sprintf("input.%d", i)
		_ = doc.SetRawBytes(path, []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}`))
		_ = doc.Set(path+".content.0.text", text)
	}
}

func codexToolOutputHasCallReference(item ojson.Result) bool {
	for _, field := range []string{"call_id", "tool_call_id", "callId"} {
		value := item.Get(field)
		if value.Exists() && value.Type != ojson.Null && (value.Type != ojson.String || strings.TrimSpace(value.String()) != "") {
			return true
		}
	}
	return false
}
