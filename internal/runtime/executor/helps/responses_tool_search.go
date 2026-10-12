package helps

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	responsesToolSearchType       = "tool_search"
	responsesToolSearchCallType   = "tool_search_call"
	responsesToolSearchOutputType = "tool_search_output"
)

// ResponsesHasToolSearch reports whether a Responses payload carries a client
// tool_search declaration or tool_search history items.
func ResponsesHasToolSearch(payload []byte) bool {
	if hasClientToolSearchDeclaration(gjson.GetBytes(payload, "tools")) {
		return true
	}
	found := false
	gjson.GetBytes(payload, "input").ForEach(func(_, item gjson.Result) bool {
		switch strings.TrimSpace(item.Get("type").String()) {
		case responsesToolSearchCallType, responsesToolSearchOutputType:
			found = true
		case "additional_tools":
			found = hasClientToolSearchDeclaration(item.Get("tools"))
		}
		return !found
	})
	return found
}

// StripResponsesToolSearch removes client tool_search declarations and every
// tool_search_call/tool_search_output input item, so the Claude translator
// produces the same output it did before tool_search support. Hosted
// declarations are left for the translator. An unchanged payload is returned
// as is.
func StripResponsesToolSearch(payload []byte) []byte {
	out := payload
	if tools := gjson.GetBytes(out, "tools"); tools.IsArray() {
		if kept, changed := withoutClientToolSearch(tools); changed {
			out, _ = sjson.SetRawBytes(out, "tools", kept)
		}
	}
	input := gjson.GetBytes(out, "input")
	if !input.IsArray() {
		return out
	}
	changed := false
	items := make([]string, 0, len(input.Array()))
	input.ForEach(func(_, item gjson.Result) bool {
		switch strings.TrimSpace(item.Get("type").String()) {
		case responsesToolSearchCallType, responsesToolSearchOutputType:
			changed = true
			return true
		case "additional_tools":
			if kept, keptChanged := withoutClientToolSearch(item.Get("tools")); keptChanged {
				updated, errSet := sjson.SetRawBytes([]byte(item.Raw), "tools", kept)
				if errSet == nil {
					items = append(items, string(updated))
					changed = true
					return true
				}
			}
		}
		items = append(items, item.Raw)
		return true
	})
	if !changed {
		return out
	}
	out, _ = sjson.SetRawBytes(out, "input", []byte("["+strings.Join(items, ",")+"]"))
	return out
}

func isClientToolSearchDeclaration(tool gjson.Result) bool {
	return strings.TrimSpace(tool.Get("type").String()) == responsesToolSearchType &&
		strings.TrimSpace(tool.Get("execution").String()) == "client"
}

func hasClientToolSearchDeclaration(tools gjson.Result) bool {
	found := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		found = isClientToolSearchDeclaration(tool)
		return !found
	})
	return found
}

// withoutClientToolSearch rebuilds a tools array without client tool_search
// declarations and reports whether anything was removed.
func withoutClientToolSearch(tools gjson.Result) ([]byte, bool) {
	if !tools.IsArray() {
		return nil, false
	}
	changed := false
	kept := make([]string, 0, len(tools.Array()))
	tools.ForEach(func(_, tool gjson.Result) bool {
		if isClientToolSearchDeclaration(tool) {
			changed = true
			return true
		}
		kept = append(kept, tool.Raw)
		return true
	})
	return []byte("[" + strings.Join(kept, ",") + "]"), changed
}
