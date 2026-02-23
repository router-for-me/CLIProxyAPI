package responses

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON

	// Build tool name shortening map from original tools (if any).
	originalToolNameMap := map[string]string{}
	{
		tools := gjson.GetBytes(rawJSON, "tools")
		if tools.IsArray() && len(tools.Array()) > 0 {
			var names []string
			arr := tools.Array()
			for i := 0; i < len(arr); i++ {
				t := arr[i]
				namePath := t.Get("function.name")
				if namePath.Exists() {
					names = append(names, namePath.String())
				}
			}
			if len(names) > 0 {
				originalToolNameMap = buildShortNameMap(names)
			}
		}
	}

	inputResult := gjson.GetBytes(rawJSON, "input")
	if inputResult.Type == gjson.String {
		input, _ := sjson.Set(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`, "0.content.0.text", inputResult.String())
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "input", []byte(input))
	}

	rawJSON, _ = sjson.SetBytes(rawJSON, "stream", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "store", false)
	// Map variant -> reasoning.effort when reasoning.effort is not explicitly provided.
	if !gjson.GetBytes(rawJSON, "reasoning.effort").Exists() {
		if variant := gjson.GetBytes(rawJSON, "variant"); variant.Exists() {
			effort := strings.ToLower(strings.TrimSpace(variant.String()))
			if effort != "" {
				rawJSON, _ = sjson.SetBytes(rawJSON, "reasoning.effort", effort)
			}
		}
	}
	rawJSON, _ = sjson.SetBytes(rawJSON, "parallel_tool_calls", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "include", []string{"reasoning.encrypted_content"})
	// Codex Responses rejects token limit fields, so strip them out before forwarding.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_output_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_completion_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "temperature")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "top_p")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "service_tier")

	// Delete the user field as it is not supported by the Codex upstream.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "user")

	// Convert role "system" to "developer" in input array to comply with Codex API requirements.
	rawJSON = convertSystemRoleToDeveloper(rawJSON)
	// Normalize tools/tool_choice names for proxy_ prefixes and maximum-length handling.
	rawJSON = normalizeResponseTools(rawJSON, originalToolNameMap)
	rawJSON = normalizeResponseToolChoice(rawJSON, originalToolNameMap)
	rawJSON = removeItemReferences(rawJSON)

	return rawJSON
}

// convertSystemRoleToDeveloper traverses the input array and converts any message items
// with role "system" to role "developer". This is necessary because Codex API does not
// accept "system" role in the input array.
func convertSystemRoleToDeveloper(rawJSON []byte) []byte {
	inputResult := gjson.GetBytes(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}

	inputArray := inputResult.Array()
	result := rawJSON

	// Directly modify role values for items with "system" role
	for i := 0; i < len(inputArray); i++ {
		rolePath := fmt.Sprintf("input.%d.role", i)
		if gjson.GetBytes(result, rolePath).String() == "system" {
			result, _ = sjson.SetBytes(result, rolePath, "developer")
		}
	}

	return result
}

func removeItemReferences(rawJSON []byte) []byte {
	inputResult := gjson.GetBytes(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}

	filtered := make([]string, 0, len(inputResult.Array()))
	for _, item := range inputResult.Array() {
		if item.Get("type").String() == "item_reference" {
			continue
		}
		filtered = append(filtered, item.Raw)
	}

	if len(filtered) == len(inputResult.Array()) {
		return rawJSON
	}

	result := "[]"
	for _, itemRaw := range filtered {
		result, _ = sjson.SetRaw(result, "-1", itemRaw)
	}

	out, _ := sjson.SetRawBytes(rawJSON, "input", []byte(result))
	return out
}

// normalizeResponseTools remaps tool entries and long function names to match upstream expectations.
func normalizeResponseTools(rawJSON []byte, nameMap map[string]string) []byte {
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return rawJSON
	}

	arr := tools.Array()
	result := make([]string, 0, len(arr))
	changed := false

	for i := 0; i < len(arr); i++ {
		t := arr[i]
		if t.Get("type").String() != "function" {
			result = append(result, t.Raw)
			continue
		}

		fn := t.Get("function")
		if !fn.Exists() {
			result = append(result, t.Raw)
			continue
		}

		name := fn.Get("name").String()
		name = normalizeToolNameAgainstMap(name, nameMap)
		name = shortenNameIfNeeded(name)

		if name != fn.Get("name").String() {
			changed = true
			fnRaw := fn.Raw
			fnRaw, _ = sjson.Set(fnRaw, "name", name)
			item := `{}`
			item, _ = sjson.Set(item, "type", "function")
			item, _ = sjson.SetRaw(item, "function", fnRaw)
			result = append(result, item)
		} else {
			result = append(result, t.Raw)
		}
	}

	if !changed {
		return rawJSON
	}

	out := "[]"
	for _, item := range result {
		out, _ = sjson.SetRaw(out, "-1", item)
	}
	rawJSON, _ = sjson.SetRawBytes(rawJSON, "tools", []byte(out))
	return rawJSON
}

// normalizeResponseToolChoice remaps function tool_choice payload names when needed.
func normalizeResponseToolChoice(rawJSON []byte, nameMap map[string]string) []byte {
	tc := gjson.GetBytes(rawJSON, "tool_choice")
	if !tc.Exists() {
		return rawJSON
	}

	if tc.Type == gjson.String {
		return rawJSON
	}
	if !tc.IsObject() {
		return rawJSON
	}

	tcType := tc.Get("type").String()
	if tcType != "function" {
		return rawJSON
	}

	name := tc.Get("function.name").String()
	name = normalizeToolNameAgainstMap(name, nameMap)
	name = shortenNameIfNeeded(name)
	if name == tc.Get("function.name").String() {
		return rawJSON
	}

	updated, _ := sjson.Set(tc.Raw, "function.name", name)
	rawJSON, _ = sjson.SetRawBytes(rawJSON, "tool_choice", []byte(updated))
	return rawJSON
}

// shortenNameIfNeeded applies the simple shortening rule for a single name.
// If the name length exceeds 64, it will try to preserve the "mcp__" prefix and last segment.
// Otherwise it truncates to 64 characters.
func shortenNameIfNeeded(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			candidate := "mcp__" + name[idx+2:]
			if len(candidate) > limit {
				return candidate[:limit]
			}
			return candidate
		}
	}
	return name[:limit]
}

// buildShortNameMap generates unique short names (<=64) for the given list of names.
func buildShortNameMap(names []string) map[string]string {
	const limit = 64
	used := map[string]struct{}{}
	m := map[string]string{}

	baseCandidate := func(n string) string {
		if len(n) <= limit {
			return n
		}
		if strings.HasPrefix(n, "mcp__") {
			idx := strings.LastIndex(n, "__")
			if idx > 0 {
				cand := "mcp__" + n[idx+2:]
				if len(cand) > limit {
					cand = cand[:limit]
				}
				return cand
			}
		}
		return n[:limit]
	}

	makeUnique := func(cand string) string {
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; ; i++ {
			suffix := "_" + strconv.Itoa(i)
			allowed := limit - len(suffix)
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			tmp = tmp + suffix
			if _, ok := used[tmp]; !ok {
				return tmp
			}
		}
	}

	for _, n := range names {
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		m[n] = uniq
	}
	return m
}

func normalizeToolNameAgainstMap(name string, m map[string]string) string {
	if name == "" {
		return name
	}
	if _, ok := m[name]; ok {
		return name
	}

	const proxyPrefix = "proxy_"
	if strings.HasPrefix(name, proxyPrefix) {
		trimmed := strings.TrimPrefix(name, proxyPrefix)
		if _, ok := m[trimmed]; ok {
			return trimmed
		}
	}

	return name
}
