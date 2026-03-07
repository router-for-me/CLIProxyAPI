package openai

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// sanitizeOpenAICompatNames rewrites tool/function name fields to be compatible with strict OpenAI schema validation.
//
// It targets:
// - messages[*].tool_calls[*].function.name
// - tools[*].function.name
// - tool_choice.function.name (when tool_choice is an object)
//
// Note: This intentionally operates at the handler boundary (sdk/api/handlers/openai)
// to avoid touching restricted internal/translator paths.
func sanitizeOpenAICompatNames(rawJSON []byte) []byte {
	root := gjson.ParseBytes(rawJSON)
	out := rawJSON

	// messages[*].tool_calls[*].function.name
	msgs := root.Get("messages")
	if msgs.Exists() && msgs.IsArray() {
		msgs.ForEach(func(mi, msg gjson.Result) bool {
			tcs := msg.Get("tool_calls")
			if !tcs.Exists() || !tcs.IsArray() {
				return true
			}
			tcs.ForEach(func(ti, tc gjson.Result) bool {
				name := tc.Get("function.name")
				if !name.Exists() {
					return true
				}
				s := util.SanitizeOpenAICompatName(name.String())
				path := "messages." + mi.String() + ".tool_calls." + ti.String() + ".function.name"
				updated, err := sjson.SetBytes(out, path, s)
				if err == nil {
					out = updated
				}
				return true
			})
			return true
		})
	}

	// tools[*].function.name
	tools := root.Get("tools")
	if tools.Exists() && tools.IsArray() {
		tools.ForEach(func(ti, tool gjson.Result) bool {
			name := tool.Get("function.name")
			if !name.Exists() {
				return true
			}
			s := util.SanitizeOpenAICompatName(name.String())
			path := "tools." + ti.String() + ".function.name"
			updated, err := sjson.SetBytes(out, path, s)
			if err == nil {
				out = updated
			}
			return true
		})
	}

	// tool_choice.function.name (object form)
	toolChoice := root.Get("tool_choice")
	if toolChoice.Exists() && toolChoice.IsObject() {
		name := toolChoice.Get("function.name")
		if name.Exists() {
			s := util.SanitizeOpenAICompatName(name.String())
			updated, err := sjson.SetBytes(out, "tool_choice.function.name", s)
			if err == nil {
				out = updated
			}
		}
	}

	return out
}
