package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/sjson"
)

// DropOrphanResponsesToolCalls removes function_call and custom_tool_call items from a
// Responses-style input whose call_id has no function_call_output or custom_tool_call_output
// anywhere in the same input. Strict upstreams reject such history with
// "No tool output found for tool call".
func DropOrphanResponsesToolCalls(body []byte) []byte {
	input := util.GetGJSONBytesNoCopy(body, "input")
	if !input.IsArray() {
		return body
	}

	items := input.Array()
	outputs := make(map[string]struct{})
	for _, item := range items {
		switch item.Get("type").String() {
		case "function_call_output", "custom_tool_call_output":
			if callID := item.Get("call_id").String(); callID != "" {
				outputs[callID] = struct{}{}
			}
		}
	}

	rebuilt := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			callID := item.Get("call_id").String()
			if _, ok := outputs[callID]; callID != "" && !ok {
				changed = true
				continue
			}
		}
		rebuilt = append(rebuilt, item.Raw)
	}
	if !changed {
		return body
	}

	updated, errSet := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if errSet != nil {
		return body
	}
	return updated
}
