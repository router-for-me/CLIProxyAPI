package helps

import (
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// StripClaudeRequestMarkers removes billing CCH and thinking block binding before
// user payload rules run. Other billing fields and ordinary prompt text survive.
func StripClaudeRequestMarkers(body []byte) []byte {
	if gjson.GetBytes(body, "thinking.block_binding").Exists() {
		if updated, errDelete := sjson.DeleteBytes(body, "thinking.block_binding"); errDelete == nil {
			body = updated
		}
	}
	stripText := func(path string, text string) {
		cleaned := stripClaudeBillingCCH(text)
		if cleaned != text {
			if updated, errSet := sjson.SetBytes(body, path, cleaned); errSet == nil {
				body = updated
			}
		}
	}
	system := gjson.GetBytes(body, "system")
	if system.Type == gjson.String {
		stripText("system", system.String())
	} else if system.IsArray() {
		for i, block := range system.Array() {
			if block.Get("type").String() == "text" {
				stripText("system."+strconv.Itoa(i)+".text", block.Get("text").String())
			}
		}
	}
	return body
}

func stripClaudeBillingCCH(text string) string {
	if !util.IsClaudeCodeAttributionSystemText(text) {
		return text
	}
	const prefix = "x-anthropic-billing-header:"
	start := strings.Index(text, prefix) + len(prefix)
	end := len(text)
	if newline := strings.IndexAny(text[start:], "\r\n"); newline >= 0 {
		end = start + newline
	}
	var kept strings.Builder
	kept.WriteString(text[:start])
	removed := false
	for _, field := range strings.SplitAfter(text[start:end], ";") {
		if strings.HasPrefix(strings.TrimSpace(field), "cch=") {
			removed = true
			continue
		}
		kept.WriteString(field)
	}
	if !removed {
		return text
	}
	kept.WriteString(text[end:])
	return kept.String()
}
