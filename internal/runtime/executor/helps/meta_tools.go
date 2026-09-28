package helps

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SanitizeMetaWebSearchTools strips unsupported fields from web_search tool definitions
// for Meta Muse upstreams. Specifically, Meta rejects search_content_types when tool type
// is "web_search" (it is only accepted on "web_search_preview" tools).
func SanitizeMetaWebSearchTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body
	}
	var pathsToDelete []string
	tools.ForEach(func(index, tool gjson.Result) bool {
		if tool.Get("type").String() == "web_search" && tool.Get("search_content_types").Exists() {
			pathsToDelete = append(pathsToDelete, fmt.Sprintf("tools.%d.search_content_types", index.Int()))
		}
		if tool.Get("type").String() == "namespace" {
			subtools := tool.Get("tools")
			if subtools.Exists() && subtools.IsArray() {
				subtools.ForEach(func(subIndex, subtool gjson.Result) bool {
					if subtool.Get("type").String() == "web_search" && subtool.Get("search_content_types").Exists() {
						pathsToDelete = append(pathsToDelete, fmt.Sprintf("tools.%d.tools.%d.search_content_types", index.Int(), subIndex.Int()))
					}
					return true
				})
			}
		}
		return true
	})
	if len(pathsToDelete) == 0 {
		return body
	}
	out := body
	for _, p := range pathsToDelete {
		if updated, errDel := sjson.DeleteBytes(out, p); errDel == nil {
			out = updated
		}
	}
	return out
}
