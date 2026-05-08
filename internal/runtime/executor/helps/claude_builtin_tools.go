package helps

import "github.com/tidwall/gjson"

var defaultClaudeBuiltinToolNames = []string{
	"web_search",
	"code_execution",
	"text_editor",
	"computer",
}

func newClaudeBuiltinToolRegistry() map[string]bool {
	registry := make(map[string]bool, len(defaultClaudeBuiltinToolNames))
	for _, name := range defaultClaudeBuiltinToolNames {
		registry[name] = true
	}
	return registry
}

func AugmentClaudeBuiltinToolRegistry(body []byte, registry map[string]bool) map[string]bool {
	if registry == nil {
		registry = newClaudeBuiltinToolRegistry()
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return registry
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("type").String() == "" {
			return true
		}
		if name := tool.Get("name").String(); name != "" {
			registry[name] = true
		}
		return true
	})
	return registry
}
