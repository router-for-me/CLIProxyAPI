package toolstream

import "github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/toolcall"

func findFirstToolMarkupTagByName(s string, start int, name string) (toolcall.ToolMarkupTag, bool) {
	return findFirstToolMarkupTagByNameFrom(s, start, name, false)
}

func findFirstToolMarkupTagByNameFrom(s string, start int, name string, closing bool) (toolcall.ToolMarkupTag, bool) {
	for pos := maxInt(start, 0); pos < len(s); {
		tag, ok := toolcall.FindToolMarkupTagOutsideIgnored(s, pos)
		if !ok {
			return toolcall.ToolMarkupTag{}, false
		}
		if tag.Name == name && tag.Closing == closing {
			return tag, true
		}
		pos = tag.End + 1
	}
	return toolcall.ToolMarkupTag{}, false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
