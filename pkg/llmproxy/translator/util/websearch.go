package util

import "strings"

// IsWebSearchTool checks if a tool name or type indicates web search capability.
func IsWebSearchTool(name, toolType string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	toolType = strings.ToLower(strings.TrimSpace(toolType))

	return name == "web_search" ||
		strings.HasPrefix(toolType, "web_search") ||
		toolType == "web_search_20250305"
}
