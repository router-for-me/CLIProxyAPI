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
	exact  string
	prefix string
}

var defaultClaudeBuiltinToolTypeFamilies = []claudeBuiltinToolTypeFamily{
	{exact: "web_search", prefix: "web_search_"},
	{exact: "code_execution", prefix: "code_execution_"},
	{exact: "text_editor", prefix: "text_editor_"},
	{exact: "computer", prefix: "computer_"},
	{exact: "bash", prefix: "bash_"},
	{exact: "memory", prefix: "memory_"},
	{exact: "web_fetch", prefix: "web_fetch_"},
	{exact: "tool_search_tool_regex", prefix: "tool_search_tool_regex_"},
	{exact: "advisor", prefix: "advisor_"},
	{exact: "mcp_toolset"},
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
		if family.prefix != "" && strings.HasPrefix(toolType, family.prefix) {
			return true
		}
	}
	return false
}

func IsClaudeCustomToolType(toolType string) bool {
	return strings.TrimSpace(toolType) == "custom"
}

func IsClaudePreservedTypedToolType(toolType string) bool {
	toolType = strings.TrimSpace(toolType)
	return toolType != "" && toolType != "custom"
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

func AugmentClaudePreservedToolRegistry(body []byte, registry map[string]bool) map[string]bool {
	if registry == nil {
		registry = make(map[string]bool)
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return registry
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		if !IsClaudePreservedTypedToolType(tool.Get("type").String()) {
			return true
		}
		if name := tool.Get("name").String(); name != "" {
			registry[name] = true
		}
		return true
	})
	return registry
}
