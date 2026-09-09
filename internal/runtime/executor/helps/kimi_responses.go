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
	removedNamespaces := make(map[string]bool)
	retainedNamespaces := make(map[string]bool)
	hasTools := false
	filterTools := func(tools []gjson.Result) ([]string, bool) {
		collectKimiNamespaces(tools, removedNamespaces)
		kept, removed := filterKimiToolSearchDeclarations(tools)
		for _, raw := range kept {
			collectKimiNamespaces([]gjson.Result{gjson.Parse(raw)}, retainedNamespaces)
		}
		hasTools = hasTools || len(kept) > 0
		return kept, removed
	}
	out := body
	removed := false
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() {
		if kept, removedTools := filterTools(tools.Array()); removedTools {
			var errSet error
			out, errSet = sjson.SetRawBytes(out, "tools", JoinRawJSONStrings(kept))
			if errSet != nil {
				return body
			}
			removed = true
		}
	}
	// Responses Lite can declare tools in input items as well. Keep surviving
	// declarations in their original channel and preserve unrelated history.
	if input := gjson.GetBytes(body, "input"); input.IsArray() {
		var keptInput []string
		removedInput := false
		for _, item := range input.Array() {
			if item.Get("type").String() != "additional_tools" || !item.Get("tools").IsArray() {
				keptInput = append(keptInput, item.Raw)
				continue
			}
			kept, removedTools := filterTools(item.Get("tools").Array())
			if !removedTools {
				keptInput = append(keptInput, item.Raw)
				continue
			}
			removedInput = true
			if len(kept) == 0 {
				continue
			}
			updated, errSet := sjson.SetRaw(item.Raw, "tools", string(JoinRawJSONStrings(kept)))
			if errSet != nil {
				return body
			}
			keptInput = append(keptInput, updated)
		}
		if removedInput {
			var errSet error
			out, errSet = sjson.SetRawBytes(out, "input", JoinRawJSONStrings(keptInput))
			if errSet != nil {
				return body
			}
			removed = true
		}
	}
	if !removed {
		return body
	}
	for name := range retainedNamespaces {
		delete(removedNamespaces, name)
	}

	// Do not force a removed tool, or require a tool when none remain.
	choice := gjson.GetBytes(out, "tool_choice")
	if choice.Get("type").String() == "allowed_tools" && choice.Get("tools").IsArray() {
		var allowed []string
		for _, tool := range choice.Get("tools").Array() {
			if tool.Get("type").String() != "tool_search" && !(tool.Get("type").String() == "namespace" && removedNamespaces[tool.Get("name").String()]) {
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
	if choice.Get("type").String() == "tool_search" || (!hasTools && choice.String() == "required") {
		if updated, errChoice := sjson.SetBytes(out, "tool_choice", "auto"); errChoice == nil {
			out = updated
		}
	}
	return out
}

func collectKimiNamespaces(tools []gjson.Result, names map[string]bool) {
	for _, tool := range tools {
		if tool.Get("type").String() == "namespace" {
			names[tool.Get("name").String()] = true
			collectKimiNamespaces(tool.Get("tools").Array(), names)
		}
	}
}

func filterKimiToolSearchDeclarations(tools []gjson.Result) ([]string, bool) {
	var kept []string
	removed := false
	for _, tool := range tools {
		if tool.Get("type").String() == "tool_search" {
			removed = true
			continue
		}
		raw := tool.Raw
		if tool.Get("type").String() == "namespace" && tool.Get("tools").IsArray() {
			members, removedMembers := filterKimiToolSearchDeclarations(tool.Get("tools").Array())
			if removedMembers {
				if len(members) == 0 {
					removed = true
					continue
				}
				if updated, errMembers := sjson.SetRaw(raw, "tools", string(JoinRawJSONStrings(members))); errMembers == nil {
					raw = updated
					removed = true
				}
			}
		}
		kept = append(kept, raw)
	}
	return kept, removed
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
