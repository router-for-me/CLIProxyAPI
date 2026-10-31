package helps

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// claudeBareMembers checks decoded member names, including escaped duplicates.
// Only structural boundaries are inspected: schemas, text and inputs are opaque.
func claudeBareMembers(object gjson.Result, keys ...string) bool {
	if !object.IsObject() {
		return false
	}
	seen := make(map[string]bool)
	ok := true
	object.ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		for _, structural := range keys {
			if strings.EqualFold(name, structural) {
				if name != structural || seen[name] {
					ok = false
					return false
				}
				seen[name] = true
			}
		}
		return true
	})
	return ok
}

// claudeBareOnly permits a finite set of structural members, failing closed for
// future namespace sources without interpreting arbitrary schemas or arguments.
func claudeBareOnly(object gjson.Result, keys ...string) bool {
	if !claudeBareMembers(object, keys...) {
		return false
	}
	ok := true
	object.ForEach(func(key, _ gjson.Result) bool {
		for _, allowed := range keys {
			if key.String() == allowed {
				return true
			}
		}
		ok = false
		return false
	})
	return ok
}

func claudeBareRequestBlocks(content gjson.Result) bool {
	if content.Type == gjson.String {
		return true
	}
	if !content.IsArray() {
		return false
	}
	ok := true
	content.ForEach(func(_, block gjson.Result) bool {
		if !claudeBareMembers(block, "type", "content", "name", "tool_name") || block.Get("type").Type != gjson.String {
			ok = false
			return false
		}
		switch block.Get("type").String() {
		case "text":
			ok = claudeBareOnly(block, "type", "text", "cache_control", "citations")
		case "thinking":
			ok = claudeBareOnly(block, "type", "thinking", "signature", "cache_control")
		case "redacted_thinking":
			ok = claudeBareOnly(block, "type", "data", "cache_control")
		case "image", "document":
			ok = claudeBareOnly(block, "type", "source", "cache_control", "title", "context", "citations")
		case "tool_use":
			ok = claudeBareOnly(block, "type", "id", "name", "input", "cache_control") && block.Get("name").Type == gjson.String
		case "tool_result":
			ok = claudeBareOnly(block, "type", "tool_use_id", "content", "is_error", "cache_control") && claudeBareRequestBlocks(block.Get("content"))
		default:
			ok = false
		}
		return ok
	})
	return ok
}

func claudeBareInventory(body []byte, preAlias bool) (map[string]int, bool) {
	if !gjson.ValidBytes(body) {
		return nil, false
	}
	root := gjson.ParseBytes(body)
	if !claudeBareOnly(root, "model", "tools", "messages", "system", "max_tokens", "stream", "metadata", "tool_choice", "thinking", "temperature", "top_p", "top_k", "stop_sequences", "service_tier", "betas", "cache_control", "output_config", "context_management", "diagnostics", "thread") {
		return nil, false
	}
	if thread := root.Get("thread"); thread.Exists() {
		if !claudeBareOnly(thread, "type") || thread.Get("type").String() != "create" {
			return nil, false
		}
	}
	if system := root.Get("system"); system.Exists() && !claudeBareRequestBlocks(system) {
		return nil, false
	}
	messages := root.Get("messages")
	if !messages.IsArray() {
		return nil, false
	}
	ok := true
	messages.ForEach(func(_, message gjson.Result) bool {
		ok = claudeBareOnly(message, "role", "content") && message.Get("role").Type == gjson.String && claudeBareRequestBlocks(message.Get("content"))
		return ok
	})
	if !ok || !root.Get("tools").IsArray() {
		return nil, false
	}
	names := make(map[string]int)
	foldedNames := make(map[string]bool)
	root.Get("tools").ForEach(func(_, tool gjson.Result) bool {
		if !claudeBareOnly(tool, "name", "description", "input_schema", "type", "cache_control", "strict") {
			ok = false
			return false
		}
		kind := tool.Get("type")
		if kind.Exists() && !(preAlias && kind.Type == gjson.String && kind.String() == "custom") {
			ok = false
			return false
		}
		name := tool.Get("name")
		if name.Type != gjson.String || name.String() == "" || !tool.Get("input_schema").IsObject() {
			ok = false
			return false
		}
		n := name.String()
		for _, c := range n {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				ok = false
				return false
			}
		}
		if (!preAlias && len(n) > 64) || strings.HasPrefix(n, "mcp__") && !IsClaudeMCPToolName(n) {
			ok = false
			return false
		}
		folded := strings.ToLower(n)
		if foldedNames[folded] {
			ok = false
			return false
		}
		foldedNames[folded] = true
		names[n]++
		return true
	})
	return names, ok
}

func claudeBareComponent(name string) string {
	if !IsClaudeMCPToolName(name) {
		return ""
	}
	_, component, _ := strings.Cut(strings.TrimPrefix(name, "mcp__"), "__")
	return component
}

// BuildClaudeMCPBareAliasMap proves exact components against both declaration
// inventories. currentAliases must be nil when preparation inherited state.
// The returned names are cloned and must remain immutable for the request.
func BuildClaudeMCPBareAliasMap(preAlias, final []byte, currentAliases map[string]string) map[string]string {
	if len(currentAliases) == 0 {
		return nil
	}
	originals, ok := claudeBareInventory(preAlias, true)
	if !ok {
		return nil
	}
	finished, ok := claudeBareInventory(final, false)
	if !ok {
		return nil
	}
	components := make(map[string]int)
	// Case folding only blocks confusable collisions; recovery stays exact.
	blocked := AugmentClaudeBuiltinToolRegistry(nil, nil)
	for name := range originals {
		blocked[strings.ToLower(name)] = true
		if component := claudeBareComponent(name); component != "" {
			blocked[strings.ToLower(component)] = true
		}
	}
	for name := range finished {
		blocked[strings.ToLower(name)] = true
		if _, allocated := currentAliases[name]; !allocated {
			if component := claudeBareComponent(name); component != "" {
				blocked[strings.ToLower(component)] = true
			}
		}
	}
	for alias, original := range currentAliases {
		if alias != original {
			components[strings.ToLower(claudeBareComponent(alias))]++
		}
	}
	result := make(map[string]string)
	for alias, original := range currentAliases {
		component := claudeBareComponent(alias)
		folded := strings.ToLower(component)
		if alias != original && component != "" && strings.Contains(component, "_") && !strings.HasPrefix(component, "mcp__") && components[folded] == 1 && !blocked[folded] && originals[original] == 1 && finished[alias] == 1 {
			result[strings.Clone(component)] = strings.Clone(original)
		}
	}
	return result
}

// ClaudeMCPBareResponseEligible validates only the envelope and recognised name
// boundaries. Existing full-alias restoration does not depend on this proof.
func ClaudeMCPBareResponseEligible(payload []byte, stream bool) bool {
	if !gjson.ValidBytes(payload) {
		return false
	}
	root := gjson.ParseBytes(payload)
	if !claudeBareMembers(root, "type", "content", "content_block", "index") || root.Get("type").Type != gjson.String {
		return false
	}
	if stream {
		index := root.Get("index")
		if index.Type != gjson.Number {
			return false
		}
		if value, err := strconv.ParseInt(index.Raw, 10, 64); err != nil || value < 0 {
			return false
		}
		return root.Get("type").String() == "content_block_start" && claudeBareResponseBlock(root.Get("content_block"))
	}
	if root.Get("type").String() != "message" || !root.Get("content").IsArray() {
		return false
	}
	ok := true
	root.Get("content").ForEach(func(_, block gjson.Result) bool {
		ok = claudeBareResponseBlock(block)
		return ok
	})
	return ok
}

func claudeBareResponseBlock(block gjson.Result) bool {
	if !claudeBareMembers(block, "type", "name", "tool_name", "content") || block.Get("type").Type != gjson.String {
		return false
	}
	switch block.Get("type").String() {
	case "tool_use":
		return block.Get("name").Type == gjson.String
	case "tool_reference":
		return block.Get("tool_name").Type == gjson.String
	case "tool_result":
		content := block.Get("content")
		if !content.IsArray() {
			return false
		}
		ok := true
		content.ForEach(func(_, nested gjson.Result) bool {
			ok = claudeBareResponseBlock(nested)
			return ok
		})
		return ok
	case "tool_search_tool_result":
		content := block.Get("content")
		if !claudeBareMembers(content, "tool_references") || !content.Get("tool_references").IsArray() {
			return false
		}
		ok := true
		content.Get("tool_references").ForEach(func(_, ref gjson.Result) bool {
			ok = ref.Get("type").String() == "tool_reference" && claudeBareResponseBlock(ref)
			return ok
		})
		return ok
	}
	return true
}
