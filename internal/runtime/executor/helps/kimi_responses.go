package helps

import (
	"strings"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeKimiResponsesTools removes the unsupported tool_search declaration
// from native Kimi Responses requests while preserving other tool types.
func NormalizeKimiResponsesTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	removed := false
	var kept []string
	for _, tool := range tools.Array() {
		if tool.Get("type").String() == "tool_search" {
			removed = true
			continue
		}
		kept = append(kept, tool.Raw)
	}
	if !removed {
		return body
	}
	out, errSet := sjson.SetRawBytes(body, "tools", JoinRawJSONStrings(kept))
	if errSet != nil {
		return body
	}

	// Do not force a removed tool, or require a tool when none remain.
	choice := gjson.GetBytes(out, "tool_choice")
	if choice.Get("type").String() == "allowed_tools" && choice.Get("tools").IsArray() {
		var allowed []string
		for _, tool := range choice.Get("tools").Array() {
			if tool.Get("type").String() != "tool_search" {
				allowed = append(allowed, tool.Raw)
			}
		}
		if len(allowed) == 0 {
			if updated, errChoice := sjson.SetBytes(out, "tool_choice", "auto"); errChoice == nil {
				out = updated
			}
		} else if updated, errChoice := sjson.SetRawBytes(out, "tool_choice.tools", JoinRawJSONStrings(allowed)); errChoice == nil {
			out = updated
		}
	}
	if choice.Get("type").String() == "tool_search" || (len(kept) == 0 && choice.String() == "required") {
		if updated, errChoice := sjson.SetBytes(out, "tool_choice", "auto"); errChoice == nil {
			out = updated
		}
	}
	return out
}

// ResolveKimiResponsesURL resolves the upstream URL for Kimi Responses API requests.
func ResolveKimiResponsesURL(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if raw := strings.TrimRight(strings.TrimSpace(auth.Attributes["base_url"]), "/"); raw != "" {
			if strings.HasSuffix(raw, "/v1") {
				return raw + "/responses"
			}
			return raw + "/v1/responses"
		}
	}
	return kimiauth.KimiAPIBaseURL + "/v1/responses"
}
