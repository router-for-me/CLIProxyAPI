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

type claudeBuiltinToolTypeFamily struct {
	exact     string
	exactOnly bool
}

var defaultClaudeBuiltinToolTypeFamilies = []claudeBuiltinToolTypeFamily{
	{exact: "web_search"},
	{exact: "code_execution"},
	{exact: "text_editor"},
	{exact: "computer"},
	{exact: "bash"},
	{exact: "memory"},
	{exact: "web_fetch"},
	{exact: "tool_search_tool_regex"},
	{exact: "advisor"},
	{exact: "mcp_toolset", exactOnly: true},
}

func newClaudeBuiltinToolRegistry() map[string]bool {
	registry := make(map[string]bool, len(defaultClaudeBuiltinToolNames))
	for _, name := range defaultClaudeBuiltinToolNames {
		registry[name] = true
	}
	return registry
}

func IsClaudeBuiltinToolType(toolType string) bool {
	toolType = strings.TrimSpace(toolType)
	if toolType == "" {
		return false
	}
	for _, family := range defaultClaudeBuiltinToolTypeFamilies {
		if toolType == family.exact {
			return true
		}
		if !family.exactOnly && strings.HasPrefix(toolType, family.exact+"_") {
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
		if !IsClaudeBuiltinToolType(tool.Get("type").String()) {
			return true
		}
		if name := tool.Get("name").String(); name != "" {
			registry[name] = true
		}
		return true
	})
	return registry
}
