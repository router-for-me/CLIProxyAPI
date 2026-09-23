package executor

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// remapOAuthToolNames represents every declared third-party client tool as a
// semantic Claude Code MCP extension. Existing valid MCP names and explicit
// typed Anthropic tools remain unchanged.
//
// It operates on tools[].name, tool_choice.name, and all declared
// tool_use/tool_reference references in messages.
//
// The returned map is keyed on the upstream name and maps to the client-supplied
// original name. Callers MUST pass this map to the reverse
// functions so only aliases allocated for this request are restored on the
// response. A global reverse map would mix symbols from unrelated callers.
func remapOAuthToolNames(body []byte) ([]byte, map[string]string) {
	return remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "cpa-claude-mcp-default-caller"})
}

type claudeRawJSONEdit struct {
	start       int
	end         int
	replacement string
}

func remapOAuthToolNamesWithOptions(body []byte, mcpAliases claudeMCPAliasOptions) ([]byte, map[string]string) {
	remapped, reverseMap, ok := remapOAuthToolNamesWithBatchedEdits(body, mcpAliases)
	if ok {
		return remapped, reverseMap
	}
	return remapOAuthToolNamesWithOptionsLegacy(body, mcpAliases)
}

// remapOAuthToolNamesWithBatchedEdits records offsets from the original JSON
// and applies every rename in one copy. Repeated sjson.SetBytes calls copy most
// of the request for every historical tool reference, turning this path into
// O(body size * reference count) allocation growth.
func remapOAuthToolNamesWithBatchedEdits(body []byte, mcpAliases claudeMCPAliasOptions) ([]byte, map[string]string, bool) {
	if !gjson.ValidBytes(body) {
		return nil, nil, false
	}

	reverseMap := make(map[string]string)
	recordRename := func(original, renamed string) {
		// Preserve the first-seen original name if the same upstream name is
		// produced from multiple call sites; they all map back identically.
		if _, exists := reverseMap[renamed]; !exists {
			reverseMap[renamed] = original
		}
	}

	// Build one request-specific forward map from declarations. Every client
	// tool, including typed custom declarations and names resembling Claude
	// built-ins, gets an MCP alias. Historical references use this same map.
	tools := gjson.GetBytes(body, "tools")
	forwardMap := make(map[string]string)
	protectedNames := make(map[string]bool)
	reservedNames := helps.AugmentClaudeBuiltinToolRegistry(body, nil)
	if tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			name := tool.Get("name").String()
			if name != "" {
				reservedNames[name] = true
			}
			if helps.IsClaudeServerToolType(tool.Get("type").String()) {
				protectedNames[name] = true
			}
			return true
		})
		passthroughMCPTools := make([]string, 0, 4)
		tools.ForEach(func(_, tool gjson.Result) bool {
			if helps.IsClaudeServerToolType(tool.Get("type").String()) {
				return true
			}
			name := tool.Get("name").String()
			if name == "" {
				return true
			}
			if helps.IsClaudeMCPToolName(name) {
				passthroughMCPTools = append(passthroughMCPTools, name)
				return true
			}
			if _, exists := forwardMap[name]; exists {
				return true
			}
			alias, allocated := helps.AllocateClaudeMCPToolAlias(mcpAliases.secret, name, reservedNames)
			if !allocated {
				log.Warnf("claude oauth mcp alias: no free alias left for tool %q, forwarding the original name", name)
				return true
			}
			forwardMap[name] = alias
			reservedNames[alias] = true
			return true
		})
		recordPassthroughMCPTools(recordRename, forwardMap, passthroughMCPTools)
	}

	rewriteName := func(name string) (string, bool) {
		if name == "" || protectedNames[name] || helps.IsClaudeMCPToolName(name) {
			return name, false
		}
		if newName, ok := forwardMap[name]; ok && newName != name {
			return newName, true
		}
		return name, false
	}

	edits := make([]claudeRawJSONEdit, 0, len(forwardMap)+1)
	appendRawEdit := func(result gjson.Result, replacement string) bool {
		start := result.Index
		end := start + len(result.Raw)
		if result.Raw == "" || start < 0 || end < start || end > len(body) || !bytes.Equal(body[start:end], []byte(result.Raw)) {
			return false
		}
		edits = append(edits, claudeRawJSONEdit{start: start, end: end, replacement: replacement})
		return true
	}
	appendStringEdit := func(result gjson.Result, replacement string) bool {
		// Generated aliases only emit [A-Za-z0-9_-], so adding quotes is
		// byte-identical to sjson's encoding without another allocation.
		return appendRawEdit(result, `"`+replacement+`"`)
	}

	// 1. Rebuild typed custom tools exactly as before, but replace the original
	// tools array only after all offsets have been collected.
	toolsNeedRewrite := false
	if tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			toolType := tool.Get("type").String()
			if helps.IsClaudeServerToolType(toolType) {
				return true
			}
			if strings.TrimSpace(toolType) != "" {
				toolsNeedRewrite = true
				return false
			}
			name := tool.Get("name").String()
			_, toolsNeedRewrite = rewriteName(name)
			return !toolsNeedRewrite
		})
	}
	if toolsNeedRewrite {
		var toolsJSON strings.Builder
		toolsJSON.WriteByte('[')
		toolCount := 0
		tools.ForEach(func(_, tool gjson.Result) bool {
			if helps.IsClaudeServerToolType(tool.Get("type").String()) {
				if toolCount > 0 {
					toolsJSON.WriteByte(',')
				}
				toolsJSON.WriteString(tool.Raw)
				toolCount++
				return true
			}

			name := tool.Get("name").String()
			toolJSON := tool.Raw
			if strings.TrimSpace(tool.Get("type").String()) != "" {
				if updatedTool, errDelete := sjson.Delete(toolJSON, "type"); errDelete == nil {
					toolJSON = updatedTool
				}
			}
			if newName, renamed := rewriteName(name); renamed {
				updatedTool, err := sjson.Set(toolJSON, "name", newName)
				if err == nil {
					toolJSON = updatedTool
					recordRename(name, newName)
				}
			}

			if toolCount > 0 {
				toolsJSON.WriteByte(',')
			}
			toolsJSON.WriteString(toolJSON)
			toolCount++
			return true
		})
		toolsJSON.WriteByte(']')
		if !appendRawEdit(tools, toolsJSON.String()) {
			return nil, nil, false
		}
	}

	// 2. Rename tool_choice if it references a declared client tool.
	toolChoice := gjson.GetBytes(body, "tool_choice")
	if toolChoice.Get("type").String() == "tool" {
		nameResult := toolChoice.Get("name")
		tcName := nameResult.String()
		if newName, renamed := rewriteName(tcName); renamed {
			if !appendStringEdit(nameResult, newName) {
				return nil, nil, false
			}
			recordRename(tcName, newName)
		}
	}

	// 3. Rename tool references in messages while every Result.Index still
	// points into the original request bytes.
	messages := gjson.GetBytes(body, "messages")
	validOffsets := true
	if messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.Exists() || !content.IsArray() {
				return true
			}
			content.ForEach(func(_, part gjson.Result) bool {
				switch part.Get("type").String() {
				case "tool_use":
					nameResult := part.Get("name")
					name := nameResult.String()
					if newName, renamed := rewriteName(name); renamed {
						if !appendStringEdit(nameResult, newName) {
							validOffsets = false
							return false
						}
						recordRename(name, newName)
					}
				case "tool_reference":
					nameResult := part.Get("tool_name")
					toolName := nameResult.String()
					if newName, renamed := rewriteName(toolName); renamed {
						if !appendStringEdit(nameResult, newName) {
							validOffsets = false
							return false
						}
						recordRename(toolName, newName)
					}
				case "tool_result":
					nestedContent := part.Get("content")
					if nestedContent.Exists() && nestedContent.IsArray() {
						nestedContent.ForEach(func(_, nestedPart gjson.Result) bool {
							if nestedPart.Get("type").String() != "tool_reference" {
								return true
							}
							nameResult := nestedPart.Get("tool_name")
							nestedToolName := nameResult.String()
							if newName, renamed := rewriteName(nestedToolName); renamed {
								if !appendStringEdit(nameResult, newName) {
									validOffsets = false
									return false
								}
								recordRename(nestedToolName, newName)
							}
							return true
						})
					}
				case "tool_search_tool_result":
					toolRefs := part.Get("content.tool_references")
					if toolRefs.Exists() && toolRefs.IsArray() {
						toolRefs.ForEach(func(_, refPart gjson.Result) bool {
							if refPart.Get("type").String() != "tool_reference" {
								return true
							}
							nameResult := refPart.Get("tool_name")
							refToolName := nameResult.String()
							if newName, renamed := rewriteName(refToolName); renamed {
								if !appendStringEdit(nameResult, newName) {
									validOffsets = false
									return false
								}
								recordRename(refToolName, newName)
							}
							return true
						})
					}
				}
				return validOffsets
			})
			return validOffsets
		})
	}
	if !validOffsets {
		return nil, nil, false
	}

	remapped, ok := applyClaudeRawJSONEdits(body, edits)
	if !ok {
		return nil, nil, false
	}
	return remapped, reverseMap, true
}

func applyClaudeRawJSONEdits(body []byte, edits []claudeRawJSONEdit) ([]byte, bool) {
	if len(edits) == 0 {
		return body, true
	}
	sort.Slice(edits, func(i, j int) bool {
		return edits[i].start < edits[j].start
	})

	finalSize := len(body)
	cursor := 0
	for _, edit := range edits {
		if edit.start < cursor || edit.start < 0 || edit.end < edit.start || edit.end > len(body) {
			return nil, false
		}
		finalSize += len(edit.replacement) - (edit.end - edit.start)
		if finalSize < 0 {
			return nil, false
		}
		cursor = edit.end
	}

	out := make([]byte, 0, finalSize)
	cursor = 0
	for _, edit := range edits {
		out = append(out, body[cursor:edit.start]...)
		out = append(out, edit.replacement...)
		cursor = edit.end
	}
	out = append(out, body[cursor:]...)
	return out, true
}

// remapOAuthToolNamesWithOptionsLegacy is the byte-for-byte compatibility
// fallback for malformed JSON or an unexpected GJSON offset. Keep it available
// as a differential-test oracle for the batched implementation.
func remapOAuthToolNamesWithOptionsLegacy(body []byte, mcpAliases claudeMCPAliasOptions) ([]byte, map[string]string) {
	reverseMap := make(map[string]string)
	recordRename := func(original, renamed string) {
		// Preserve the first-seen original name if the same upstream name is
		// produced from multiple call sites; they all map back identically.
		if _, exists := reverseMap[renamed]; !exists {
			reverseMap[renamed] = original
		}
	}

	// Build one request-specific forward map from declarations. Every client
	// tool, including typed custom declarations and names resembling Claude
	// built-ins, gets an MCP alias. Historical references use this same map.
	tools := gjson.GetBytes(body, "tools")
	forwardMap := make(map[string]string)
	protectedNames := make(map[string]bool)
	reservedNames := helps.AugmentClaudeBuiltinToolRegistry(body, nil)
	if tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			name := tool.Get("name").String()
			if name != "" {
				reservedNames[name] = true
			}
			if helps.IsClaudeServerToolType(tool.Get("type").String()) {
				protectedNames[name] = true
			}
			return true
		})
		passthroughMCPTools := make([]string, 0, 4)
		tools.ForEach(func(_, tool gjson.Result) bool {
			if helps.IsClaudeServerToolType(tool.Get("type").String()) {
				return true
			}
			name := tool.Get("name").String()
			if name == "" {
				return true
			}
			if helps.IsClaudeMCPToolName(name) {
				passthroughMCPTools = append(passthroughMCPTools, name)
				return true
			}
			if _, exists := forwardMap[name]; exists {
				return true
			}
			alias, allocated := helps.AllocateClaudeMCPToolAlias(mcpAliases.secret, name, reservedNames)
			if !allocated {
				log.Warnf("claude oauth mcp alias: no free alias left for tool %q, forwarding the original name", name)
				return true
			}
			forwardMap[name] = alias
			reservedNames[alias] = true
			return true
		})
		recordPassthroughMCPTools(recordRename, forwardMap, passthroughMCPTools)
	}

	rewriteName := func(name string) (string, bool) {
		if name == "" || protectedNames[name] || helps.IsClaudeMCPToolName(name) {
			return name, false
		}
		if newName, ok := forwardMap[name]; ok && newName != name {
			return newName, true
		}
		return name, false
	}

	// 1. Rewrite the tools array without rebuilding from a stale gjson snapshot.
	toolsNeedRewrite := false
	if tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			toolType := tool.Get("type").String()
			if helps.IsClaudeServerToolType(toolType) {
				return true
			}
			if strings.TrimSpace(toolType) != "" {
				toolsNeedRewrite = true
				return false
			}
			name := tool.Get("name").String()
			_, toolsNeedRewrite = rewriteName(name)
			return !toolsNeedRewrite
		})
	}
	if toolsNeedRewrite {
		var toolsJSON strings.Builder
		toolsJSON.WriteByte('[')
		toolCount := 0
		tools.ForEach(func(_, tool gjson.Result) bool {
			if helps.IsClaudeServerToolType(tool.Get("type").String()) {
				if toolCount > 0 {
					toolsJSON.WriteByte(',')
				}
				toolsJSON.WriteString(tool.Raw)
				toolCount++
				return true
			}

			name := tool.Get("name").String()
			toolJSON := tool.Raw
			if strings.TrimSpace(tool.Get("type").String()) != "" {
				if updatedTool, errDelete := sjson.Delete(toolJSON, "type"); errDelete == nil {
					toolJSON = updatedTool
				}
			}
			if newName, renamed := rewriteName(name); renamed {
				updatedTool, err := sjson.Set(toolJSON, "name", newName)
				if err == nil {
					toolJSON = updatedTool
					recordRename(name, newName)
				}
			}

			if toolCount > 0 {
				toolsJSON.WriteByte(',')
			}
			toolsJSON.WriteString(toolJSON)
			toolCount++
			return true
		})
		toolsJSON.WriteByte(']')
		body, _ = sjson.SetRawBytes(body, "tools", []byte(toolsJSON.String()))
	}

	// 2. Rename tool_choice if it references a declared client tool.
	toolChoiceType := gjson.GetBytes(body, "tool_choice.type").String()
	if toolChoiceType == "tool" {
		tcName := gjson.GetBytes(body, "tool_choice.name").String()
		if newName, renamed := rewriteName(tcName); renamed {
			body, _ = sjson.SetBytes(body, "tool_choice.name", newName)
			recordRename(tcName, newName)
		}
	}

	// 3. Rename tool references in messages
	messages := gjson.GetBytes(body, "messages")
	if messages.Exists() && messages.IsArray() {
		messages.ForEach(func(msgIndex, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.Exists() || !content.IsArray() {
				return true
			}
			content.ForEach(func(contentIndex, part gjson.Result) bool {
				partType := part.Get("type").String()
				switch partType {
				case "tool_use":
					name := part.Get("name").String()
					if newName, renamed := rewriteName(name); renamed {
						path := fmt.Sprintf("messages.%d.content.%d.name", msgIndex.Int(), contentIndex.Int())
						body, _ = sjson.SetBytes(body, path, newName)
						recordRename(name, newName)
					}
				case "tool_reference":
					toolName := part.Get("tool_name").String()
					if newName, renamed := rewriteName(toolName); renamed {
						path := fmt.Sprintf("messages.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int())
						body, _ = sjson.SetBytes(body, path, newName)
						recordRename(toolName, newName)
					}
				case "tool_result":
					// Handle nested tool_reference blocks inside tool_result.content[]
					toolID := part.Get("tool_use_id").String()
					_ = toolID // tool_use_id stays as-is
					nestedContent := part.Get("content")
					if nestedContent.Exists() && nestedContent.IsArray() {
						nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
							if nestedPart.Get("type").String() == "tool_reference" {
								nestedToolName := nestedPart.Get("tool_name").String()
								if newName, renamed := rewriteName(nestedToolName); renamed {
									nestedPath := fmt.Sprintf("messages.%d.content.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int(), nestedIndex.Int())
									body, _ = sjson.SetBytes(body, nestedPath, newName)
									recordRename(nestedToolName, newName)
								}
							}
							return true
						})
					}
				case "tool_search_tool_result":
					toolRefs := part.Get("content.tool_references")
					if toolRefs.Exists() && toolRefs.IsArray() {
						toolRefs.ForEach(func(refIndex, refPart gjson.Result) bool {
							if refPart.Get("type").String() == "tool_reference" {
								refToolName := refPart.Get("tool_name").String()
								if newName, renamed := rewriteName(refToolName); renamed {
									refPath := fmt.Sprintf("messages.%d.content.%d.content.tool_references.%d.tool_name", msgIndex.Int(), contentIndex.Int(), refIndex.Int())
									body, _ = sjson.SetBytes(body, refPath, newName)
									recordRename(refToolName, newName)
								}
							}
							return true
						})
					}
				}
				return true
			})
			return true
		})
	}

	return body, reverseMap
}
