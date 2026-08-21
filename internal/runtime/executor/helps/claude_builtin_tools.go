package helps

import (
	"strings"

	"github.com/tidwall/gjson"
)

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

// IsClaudeServerToolType reports whether a typed declaration is a recognized
// Anthropic-operated tool. Client-defined type:"custom" declarations are not
// server tools and must remain eligible for MCP aliasing.
func IsClaudeServerToolType(toolType string) bool {
	toolType = strings.ToLower(strings.TrimSpace(toolType))
	for _, prefix := range []string{
		"bash_",
		"code_execution_",
		"computer_",
		"memory_",
		"text_editor_",
		"tool_search_tool_",
		"web_fetch_",
		"web_search_",
	} {
		if strings.HasPrefix(toolType, prefix) {
			return true
		}
	}
	return false
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
		if !IsClaudeServerToolType(tool.Get("type").String()) {
			return true
		}
		if name := tool.Get("name").String(); name != "" {
			registry[name] = true
		}
		return true
	})
	return registry
}
