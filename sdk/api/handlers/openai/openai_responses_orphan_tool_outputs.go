package openai

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
)

// sanitizeOrphanResponsesToolOutputsDoc rewrites Responses tool output items
// that do not carry an explicit call ID into user messages. Codex Desktop
// injects companion-page context (for example codex_apps_open_page) as
// function_call_output items without call_id; strict Responses upstreams reject
// the request with "responses function_call_output call_id is required".
func sanitizeOrphanResponsesToolOutputsDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}
	inputItems := input.Array()
	if len(inputItems) == 0 {
		return false
	}

	changed := false
	rebuiltInput := make([][]byte, 0, len(inputItems))
	for _, item := range inputItems {
		itemRaw := []byte(item.Raw)
		switch item.Get("type").String() {
		case "function_call_output", "custom_tool_call_output":
			if !responsesToolOutputHasCallID(item) && !isCodexDelegationToolOutput(item) {
				itemRaw = responsesOrphanToolOutputMessage(item)
				changed = true
			}
		}
		rebuiltInput = append(rebuiltInput, itemRaw)
	}
	if !changed {
		return false
	}
	_ = doc.SetRawBytes("input", translatorcommon.JoinRawArray(rebuiltInput))
	return true
}

// isCodexDelegationToolOutput keeps Codex delegation outputs on the existing
// opt-in orphan-delegation-compatibility path.
func isCodexDelegationToolOutput(item ojson.Result) bool {
	if item.Get("namespace").String() != "codex_app" {
		return false
	}
	switch item.Get("name").String() {
	case "create_thread", "send_message_to_thread":
		return true
	default:
		return false
	}
}

// responsesToolOutputHasCallID reports whether a tool output item carries the
// explicit call ID field required by strict Responses upstreams. The generic
// item id is deliberately ignored because upstreams do not treat it as a call
// ID reference.
func responsesToolOutputHasCallID(item ojson.Result) bool {
	for _, path := range []string{"call_id", "tool_call_id", "callId"} {
		if value := item.Get(path); value.Exists() && strings.TrimSpace(value.String()) != "" {
			return true
		}
	}
	return false
}

// responsesOrphanToolOutputMessage folds an orphan tool output into a user
// message so the tool result survives without violating the Responses schema.
func responsesOrphanToolOutputMessage(item ojson.Result) []byte {
	name := strings.TrimSpace(item.Get("name").String())
	if name == "" {
		name = "tool"
	}
	outputText := responsesOrphanToolOutputText(item.Get("output"))
	if strings.TrimSpace(outputText) == "" {
		outputText = "(empty)"
	}

	messageDoc := ojson.NewDocString(`{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}`)
	_ = messageDoc.Set("content.0.text", "Tool output from "+name+":\n"+outputText)
	return messageDoc.Bytes()
}

func responsesOrphanToolOutputText(output ojson.Result) string {
	if !output.Exists() {
		return ""
	}
	if output.Type == ojson.String {
		return output.String()
	}
	if output.IsArray() {
		parts := make([]string, 0, len(output.Array()))
		for _, part := range output.Array() {
			switch part.Get("type").String() {
			case "text", "input_text", "output_text":
				parts = append(parts, part.Get("text").String())
			default:
				parts = append(parts, part.Raw)
			}
		}
		return strings.Join(parts, "\n")
	}
	return output.Raw
}
