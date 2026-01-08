package websearch

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var webSearchToolTypes = []string{
	"web_search",
	"web_search_20250305",
	"google_search",
	"google_search_retrieval",
}

func HasWebSearchTool(rawJSON []byte) bool {
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.IsArray() {
		return false
	}

	for _, tool := range tools.Array() {
		toolType := strings.ToLower(tool.Get("type").String())
		for _, wsType := range webSearchToolTypes {
			if toolType == wsType {
				return true
			}
		}

		toolName := strings.ToLower(tool.Get("name").String())
		if toolName == "web_search" || toolName == "google_search" {
			return true
		}

		if tool.Get("google_search").Exists() {
			return true
		}
	}

	return false
}

func InjectGoogleSearchTool(rawJSON []byte) []byte {
	tools := gjson.GetBytes(rawJSON, "tools")

	if !tools.Exists() || !tools.IsArray() {
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "tools", []byte(`[{"google_search":{}}]`))
		return rawJSON
	}

	hasGoogleSearch := false
	for _, tool := range tools.Array() {
		if tool.Get("google_search").Exists() {
			hasGoogleSearch = true
			break
		}
	}

	if !hasGoogleSearch {
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "tools.-1", []byte(`{"google_search":{}}`))
	}

	return rawJSON
}

func RemoveWebSearchTools(rawJSON []byte) []byte {
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.IsArray() {
		return rawJSON
	}

	var filteredTools []string
	for _, tool := range tools.Array() {
		toolType := strings.ToLower(tool.Get("type").String())
		isWebSearch := false

		for _, wsType := range webSearchToolTypes {
			if toolType == wsType {
				isWebSearch = true
				break
			}
		}

		if !isWebSearch {
			toolName := strings.ToLower(tool.Get("name").String())
			if toolName == "web_search" || toolName == "google_search" {
				isWebSearch = true
			}
		}

		if !isWebSearch {
			filteredTools = append(filteredTools, tool.Raw)
		}
	}

	if len(filteredTools) == 0 {
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "tools")
	} else {
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "tools", []byte("["+strings.Join(filteredTools, ",")+"]"))
	}

	return rawJSON
}
