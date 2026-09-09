package helps

import (
	"strings"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeKimiResponsesTools removes unsupported tool_search declarations and
// history from native Kimi Responses requests, retaining discovered tools.
func NormalizeKimiResponsesTools(body []byte) []byte {
	body = normalizeKimiResponsesToolSearchHistory(body)
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

func normalizeKimiResponsesToolSearchHistory(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	var kept []string
	var discoveries [][]gjson.Result
	removed := false
	for _, item := range input.Array() {
		switch item.Get("type").String() {
		case "tool_search_call":
			removed = true
		case "tool_search_output":
			removed = true
			if tools := item.Get("tools"); tools.IsArray() {
				discoveries = append(discoveries, tools.Array())
			}
		default:
			kept = append(kept, item.Raw)
		}
	}
	if !removed {
		return body
	}
	out, errInput := sjson.SetRawBytes(body, "input", JoinRawJSONStrings(kept))
	if errInput != nil {
		return body
	}
	// Current declarations take precedence over historical definitions. Read
	// search results newest first so repeated discoveries use the latest schema.
	var discovered []gjson.Result
	for index := len(discoveries) - 1; index >= 0; index-- {
		discovered = append(discovered, discoveries[index]...)
	}
	if len(discovered) == 0 {
		return out
	}
	merged := mergeKimiDiscoveredTools(gjson.GetBytes(out, "tools").Array(), discovered)
	if updated, errTools := sjson.SetRawBytes(out, "tools", JoinRawJSONStrings(merged)); errTools == nil {
		return updated
	}
	return body
}

// mergeKimiDiscoveredTools keeps existing definitions and appends newly found
// tools. Namespaces merge by member name without flattening their call identity.
func mergeKimiDiscoveredTools(existing, discovered []gjson.Result) []string {
	type toolKey struct {
		kind string
		name string
	}
	indices := make(map[toolKey]int, len(existing)+len(discovered))
	out := make([]string, 0, len(existing)+len(discovered))
	keyFor := func(tool gjson.Result) toolKey {
		return toolKey{kind: tool.Get("type").String(), name: tool.Get("name").String()}
	}
	for _, tool := range existing {
		key := keyFor(tool)
		if _, found := indices[key]; !found {
			indices[key] = len(out)
		}
		out = append(out, tool.Raw)
	}
	for _, tool := range discovered {
		key := keyFor(tool)
		if index, found := indices[key]; found {
			if key.kind == "namespace" {
				current := gjson.Parse(out[index])
				members := mergeKimiDiscoveredTools(current.Get("tools").Array(), tool.Get("tools").Array())
				if updated, errMembers := sjson.SetRaw(out[index], "tools", string(JoinRawJSONStrings(members))); errMembers == nil {
					out[index] = updated
				}
			}
			continue
		}
		indices[key] = len(out)
		out = append(out, tool.Raw)
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
