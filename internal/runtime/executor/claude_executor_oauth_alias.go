package executor

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type claudeMCPAliasParts struct {
	server   string
	toolID   string
	semantic string
}

type claudeMCPAliasEntry struct {
	alias    string
	original string
	parts    claudeMCPAliasParts
}

type claudeMCPAliasResolver struct {
	exact        map[string]string
	aliases      []claudeMCPAliasEntry
	servers      map[string]struct{}
	passthroughs []string
}

type claudeMCPAliasRestoreError struct {
	error
}

func (e claudeMCPAliasRestoreError) Unwrap() error {
	return e.error
}

func (claudeMCPAliasRestoreError) IsRequestScoped() bool {
	return true
}

func newClaudeMCPAliasResolver(reverseMap map[string]string) claudeMCPAliasResolver {
	resolver := claudeMCPAliasResolver{
		exact:        reverseMap,
		aliases:      make([]claudeMCPAliasEntry, 0, len(reverseMap)),
		servers:      make(map[string]struct{}),
		passthroughs: make([]string, 0),
	}
	for alias, original := range reverseMap {
		if alias == original {
			// Caller-owned MCP tool recorded for exact passthrough and fallback hybrid
			// recovery. It must not register a virtual server or take part in fuzzy
			// client-tool alias recovery.
			resolver.passthroughs = append(resolver.passthroughs, original)
			continue
		}
		parts, ok := parseClaudeMCPAlias(alias)
		if !ok {
			continue
		}
		resolver.aliases = append(resolver.aliases, claudeMCPAliasEntry{
			alias:    alias,
			original: original,
			parts:    parts,
		})
		resolver.servers[parts.server] = struct{}{}
	}
	return resolver
}

func parseClaudeMCPAlias(name string) (claudeMCPAliasParts, bool) {
	if !helps.IsClaudeMCPToolName(name) {
		return claudeMCPAliasParts{}, false
	}
	rest, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return claudeMCPAliasParts{}, false
	}
	server, tool, ok := strings.Cut(rest, "__")
	if !ok || server == "" {
		return claudeMCPAliasParts{}, false
	}
	toolID, semantic, ok := strings.Cut(tool, "_")
	if !ok || toolID == "" || semantic == "" {
		return claudeMCPAliasParts{}, false
	}
	return claudeMCPAliasParts{server: server, toolID: toolID, semantic: semantic}, true
}

func claudeMCPAliasServer(name string) string {
	rest, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return ""
	}
	server, _, ok := strings.Cut(rest, "__")
	if !ok {
		return ""
	}
	return server
}

// recordPassthroughMCPTools remembers caller-owned MCP tool names that were left
// untouched. Without this the response resolver would treat such a name as a
// drifted alias whenever the derived two-word virtual server happens to equal a
// real MCP server name, and would either restore the wrong tool or fail the
// request. Recording is skipped when nothing was aliased so an untouched request
// keeps an empty reverse map and the restore path stays a no-op.
func recordPassthroughMCPTools(recordRename func(original, renamed string), forwardMap map[string]string, passthrough []string) {
	if len(forwardMap) == 0 {
		return
	}
	for _, name := range passthrough {
		recordRename(name, name)
	}
}

func (resolver claudeMCPAliasResolver) resolve(name string) (string, bool, error) {
	if original, ok := resolver.exact[name]; ok {
		if original == name {
			// Caller-owned MCP tool: forward it exactly as the client declared it.
			return "", false, nil
		}
		return original, true, nil
	}

	server := claudeMCPAliasServer(name)
	if _, known := resolver.servers[server]; !known {
		return "", false, nil
	}

	canonicalServerPrefix := "mcp__" + server + "__"
	normalizedName := name
	suffix := strings.TrimPrefix(name, canonicalServerPrefix)
	for {
		strippedSuffix, repeatedServer := strings.CutPrefix(suffix, server+"__")
		if !repeatedServer {
			break
		}
		suffix = strippedSuffix
		normalizedName = canonicalServerPrefix + suffix
		if original, exact := resolver.exact[normalizedName]; exact {
			return original, true, nil
		}
	}

	matchedOriginal := ""
	matchCount := 0
	for _, entry := range resolver.aliases {
		if entry.parts.server == server && strings.HasSuffix(name, entry.alias) {
			matchedOriginal = entry.original
			matchCount++
		}
	}
	if matchCount == 1 {
		return matchedOriginal, true, nil
	}
	if matchCount > 1 {
		return "", false, claudeMCPAliasRestoreError{fmt.Errorf("cannot restore Claude OAuth MCP tool alias %q: matched multiple declared aliases", name)}
	}

	parts, validAlias := parseClaudeMCPAlias(normalizedName)
	if validAlias {
		for _, entry := range resolver.aliases {
			if entry.parts.server == parts.server && entry.parts.semantic == parts.semantic {
				matchedOriginal = entry.original
				matchCount++
			}
		}
	}
	// Extra words in the tool component still parse, but the semantic field
	// is then wrong. Fall through to an unambiguous suffix match so word-level
	// repeats do not become restore 500s.
	if matchCount == 0 {
		var suffixMatches []claudeMCPAliasEntry
		for _, entry := range resolver.aliases {
			if entry.parts.server == server && strings.HasSuffix(normalizedName, "_"+entry.parts.semantic) {
				suffixMatches = append(suffixMatches, entry)
			}
		}
		if len(suffixMatches) == 1 {
			matchedOriginal = suffixMatches[0].original
			matchCount = 1
		} else if len(suffixMatches) > 1 {
			// If multiple candidates match (e.g. "_file" and "_read_file"),
			// choose the strictly longest semantic match when unambiguous.
			longest := suffixMatches[0]
			tie := false
			for _, candidate := range suffixMatches[1:] {
				if len(candidate.parts.semantic) > len(longest.parts.semantic) {
					longest = candidate
					tie = false
				} else if len(candidate.parts.semantic) == len(longest.parts.semantic) {
					tie = true
				}
			}
			if !tie {
				matchedOriginal = longest.original
				matchCount = 1
			} else {
				matchCount = len(suffixMatches)
			}
		}
		if matchCount == 1 {
			// This path guesses instead of failing, so leave a trace: it is the only
			// way to tell a silent wrong-tool restore from a healthy request.
			log.Debugf("claude oauth mcp alias: recovered drifted tool name %q as %q via semantic suffix", name, matchedOriginal)
		}
	}
	if matchCount == 1 {
		return matchedOriginal, true, nil
	}
	if matchCount > 1 {
		return "", false, claudeMCPAliasRestoreError{fmt.Errorf("cannot restore Claude OAuth MCP tool alias %q: semantic suffix matches multiple declared tools", name)}
	}

	if len(resolver.passthroughs) > 0 {
		// Recovery 1: The model prepended the virtual server to the full caller MCP tool name
		// (e.g. "mcp__<virtual>__<real_server>__<tool>"). After stripping the virtual server prefix,
		// re-prefixing suffix with "mcp__" produces the original caller tool name.
		reprefixed := "mcp__" + suffix
		if original, exact := resolver.exact[reprefixed]; exact && original == reprefixed {
			log.Debugf("claude oauth mcp alias: recovered hybrid passthrough tool name %q as %q via exact prefix", name, original)
			return original, true, nil
		}

		// Recovery 2: The model replaced the caller's server with the virtual server
		// (e.g. "mcp__<virtual>__<tool>"). Match suffix against the tool component of
		// declared passthrough tools. This runs strictly after client-tool alias recovery
		// so that a client tool (e.g. "Bash") is never eclipsed by a passthrough tool
		// with the same suffix (e.g. "mcp__shell__Bash").
		matchedPassthrough := ""
		passthroughMatches := 0
		for _, pt := range resolver.passthroughs {
			toolPart := pt
			if rest, ok := strings.CutPrefix(pt, "mcp__"); ok {
				if _, tool, ok := strings.Cut(rest, "__"); ok {
					toolPart = tool
				}
			}
			if toolPart == suffix {
				matchedPassthrough = pt
				passthroughMatches++
			}
		}
		if passthroughMatches == 1 {
			log.Debugf("claude oauth mcp alias: recovered hybrid passthrough tool name %q as %q via unique tool suffix", name, matchedPassthrough)
			return matchedPassthrough, true, nil
		}
		if passthroughMatches > 1 {
			return "", false, claudeMCPAliasRestoreError{fmt.Errorf("cannot restore Claude OAuth MCP tool alias %q: passthrough tool suffix matches multiple declared tools", name)}
		}
	}

	return "", false, claudeMCPAliasRestoreError{fmt.Errorf("cannot restore Claude OAuth MCP tool alias %q: no unique request-local match", name)}
}

// reverseRemapOAuthToolNames reverses the tool name mapping for non-stream responses
// using the per-request map produced by remapOAuthToolNames. Names outside the
// request-local generated MCP server are passed through unchanged.
func reverseRemapOAuthToolNames(body []byte, reverseMap map[string]string) ([]byte, error) {
	if len(reverseMap) == 0 {
		return body, nil
	}
	content := gjson.GetBytes(body, "content")
	if !content.Exists() || !content.IsArray() {
		return body, nil
	}
	resolver := newClaudeMCPAliasResolver(reverseMap)
	var resolveErr error
	content.ForEach(func(index, part gjson.Result) bool {
		partType := part.Get("type").String()
		switch partType {
		case "tool_use":
			name := part.Get("name").String()
			origName, matched, errResolve := resolver.resolve(name)
			if errResolve != nil {
				resolveErr = errResolve
				return false
			}
			if matched {
				path := fmt.Sprintf("content.%d.name", index.Int())
				body, _ = sjson.SetBytes(body, path, origName)
			}
		case "tool_reference":
			toolName := part.Get("tool_name").String()
			origName, matched, errResolve := resolver.resolve(toolName)
			if errResolve != nil {
				resolveErr = errResolve
				return false
			}
			if matched {
				path := fmt.Sprintf("content.%d.tool_name", index.Int())
				body, _ = sjson.SetBytes(body, path, origName)
			}
		case "tool_result":
			nestedContent := part.Get("content")
			if nestedContent.Exists() && nestedContent.IsArray() {
				nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
					if nestedPart.Get("type").String() != "tool_reference" {
						return true
					}
					toolName := nestedPart.Get("tool_name").String()
					origName, matched, errResolve := resolver.resolve(toolName)
					if errResolve != nil {
						resolveErr = errResolve
						return false
					}
					if matched {
						path := fmt.Sprintf("content.%d.content.%d.tool_name", index.Int(), nestedIndex.Int())
						body, _ = sjson.SetBytes(body, path, origName)
					}
					return true
				})
			}
		case "tool_search_tool_result":
			toolRefs := part.Get("content.tool_references")
			if toolRefs.Exists() && toolRefs.IsArray() {
				toolRefs.ForEach(func(refIndex, refPart gjson.Result) bool {
					if refPart.Get("type").String() != "tool_reference" {
						return true
					}
					toolName := refPart.Get("tool_name").String()
					origName, matched, errResolve := resolver.resolve(toolName)
					if errResolve != nil {
						resolveErr = errResolve
						return false
					}
					if matched {
						path := fmt.Sprintf("content.%d.content.tool_references.%d.tool_name", index.Int(), refIndex.Int())
						body, _ = sjson.SetBytes(body, path, origName)
					}
					return true
				})
			}
		}
		return resolveErr == nil
	})
	return body, resolveErr
}

// reverseRemapOAuthToolNamesFromStreamLine reverses the tool name mapping for SSE
// stream lines, using the per-request reverseMap produced by remapOAuthToolNames.
func reverseRemapOAuthToolNamesFromStreamLine(line []byte, reverseMap map[string]string) ([]byte, error) {
	if len(reverseMap) == 0 {
		return line, nil
	}
	payload := helps.JSONPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return line, nil
	}

	contentBlock := gjson.GetBytes(payload, "content_block")
	if !contentBlock.Exists() {
		return line, nil
	}

	resolver := newClaudeMCPAliasResolver(reverseMap)
	blockType := contentBlock.Get("type").String()
	var updated []byte
	var err error

	switch blockType {
	case "tool_use":
		name := contentBlock.Get("name").String()
		origName, matched, errResolve := resolver.resolve(name)
		if errResolve != nil {
			return line, errResolve
		}
		if !matched {
			return line, nil
		}
		updated, err = sjson.SetBytes(payload, "content_block.name", origName)
	case "tool_reference":
		toolName := contentBlock.Get("tool_name").String()
		origName, matched, errResolve := resolver.resolve(toolName)
		if errResolve != nil {
			return line, errResolve
		}
		if !matched {
			return line, nil
		}
		updated, err = sjson.SetBytes(payload, "content_block.tool_name", origName)
	case "tool_search_tool_result":
		toolRefs := contentBlock.Get("content.tool_references")
		if !toolRefs.Exists() || !toolRefs.IsArray() {
			return line, nil
		}
		updatedPayload := payload
		var resolveErr error
		hasChange := false
		toolRefs.ForEach(func(refIndex, refPart gjson.Result) bool {
			if refPart.Get("type").String() != "tool_reference" {
				return true
			}
			toolName := refPart.Get("tool_name").String()
			origName, matched, errResolve := resolver.resolve(toolName)
			if errResolve != nil {
				resolveErr = errResolve
				return false
			}
			if matched {
				path := fmt.Sprintf("content_block.content.tool_references.%d.tool_name", refIndex.Int())
				updatedPayload, err = sjson.SetBytes(updatedPayload, path, origName)
				if err != nil {
					return false
				}
				hasChange = true
			}
			return true
		})
		if resolveErr != nil {
			return line, resolveErr
		}
		if err != nil {
			return line, fmt.Errorf("rewrite Claude OAuth MCP tool alias: %w", err)
		}
		if !hasChange {
			return line, nil
		}
		updated = updatedPayload
	default:
		return line, nil
	}
	if err != nil {
		return line, fmt.Errorf("rewrite Claude OAuth MCP tool alias: %w", err)
	}

	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		return append([]byte("data: "), updated...), nil
	}
	return updated, nil
}

func applyClaudeToolPrefix(body []byte, prefix string) []byte {
	if prefix == "" {
		return body
	}

	// Collect built-in tool names from the authoritative fallback seed list and
	// augment it with any typed built-ins present in the current request body.
	builtinTools := helps.AugmentClaudeBuiltinToolRegistry(body, nil)

	if tools := gjson.GetBytes(body, "tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(index, tool gjson.Result) bool {
			// Skip built-in tools (web_search, code_execution, etc.) which have
			// a "type" field and require their name to remain unchanged.
			if tool.Get("type").Exists() && tool.Get("type").String() != "" {
				if n := tool.Get("name").String(); n != "" {
					builtinTools[n] = true
				}
				return true
			}
			name := tool.Get("name").String()
			if name == "" || strings.HasPrefix(name, prefix) || helps.IsClaudeMCPToolName(name) {
				return true
			}
			path := fmt.Sprintf("tools.%d.name", index.Int())
			body, _ = sjson.SetBytes(body, path, prefix+name)
			return true
		})
	}

	if gjson.GetBytes(body, "tool_choice.type").String() == "tool" {
		name := gjson.GetBytes(body, "tool_choice.name").String()
		if name != "" && !strings.HasPrefix(name, prefix) && !builtinTools[name] && !helps.IsClaudeMCPToolName(name) {
			body, _ = sjson.SetBytes(body, "tool_choice.name", prefix+name)
		}
	}

	if messages := gjson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
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
					if name == "" || strings.HasPrefix(name, prefix) || builtinTools[name] || helps.IsClaudeMCPToolName(name) {
						return true
					}
					path := fmt.Sprintf("messages.%d.content.%d.name", msgIndex.Int(), contentIndex.Int())
					body, _ = sjson.SetBytes(body, path, prefix+name)
				case "tool_reference":
					toolName := part.Get("tool_name").String()
					if toolName == "" || strings.HasPrefix(toolName, prefix) || builtinTools[toolName] || helps.IsClaudeMCPToolName(toolName) {
						return true
					}
					path := fmt.Sprintf("messages.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int())
					body, _ = sjson.SetBytes(body, path, prefix+toolName)
				case "tool_result":
					// Handle nested tool_reference blocks inside tool_result.content[]
					nestedContent := part.Get("content")
					if nestedContent.Exists() && nestedContent.IsArray() {
						nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
							if nestedPart.Get("type").String() == "tool_reference" {
								nestedToolName := nestedPart.Get("tool_name").String()
								if nestedToolName != "" && !strings.HasPrefix(nestedToolName, prefix) && !builtinTools[nestedToolName] && !helps.IsClaudeMCPToolName(nestedToolName) {
									nestedPath := fmt.Sprintf("messages.%d.content.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int(), nestedIndex.Int())
									body, _ = sjson.SetBytes(body, nestedPath, prefix+nestedToolName)
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

	return body
}

func stripClaudeToolPrefixFromResponse(body []byte, prefix string) []byte {
	if prefix == "" {
		return body
	}
	content := gjson.GetBytes(body, "content")
	if !content.Exists() || !content.IsArray() {
		return body
	}
	content.ForEach(func(index, part gjson.Result) bool {
		partType := part.Get("type").String()
		switch partType {
		case "tool_use":
			name := part.Get("name").String()
			if !strings.HasPrefix(name, prefix) {
				return true
			}
			path := fmt.Sprintf("content.%d.name", index.Int())
			body, _ = sjson.SetBytes(body, path, strings.TrimPrefix(name, prefix))
		case "tool_reference":
			toolName := part.Get("tool_name").String()
			if !strings.HasPrefix(toolName, prefix) {
				return true
			}
			path := fmt.Sprintf("content.%d.tool_name", index.Int())
			body, _ = sjson.SetBytes(body, path, strings.TrimPrefix(toolName, prefix))
		case "tool_result":
			// Handle nested tool_reference blocks inside tool_result.content[]
			nestedContent := part.Get("content")
			if nestedContent.Exists() && nestedContent.IsArray() {
				nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
					if nestedPart.Get("type").String() == "tool_reference" {
						nestedToolName := nestedPart.Get("tool_name").String()
						if strings.HasPrefix(nestedToolName, prefix) {
							nestedPath := fmt.Sprintf("content.%d.content.%d.tool_name", index.Int(), nestedIndex.Int())
							body, _ = sjson.SetBytes(body, nestedPath, strings.TrimPrefix(nestedToolName, prefix))
						}
					}
					return true
				})
			}
		}
		return true
	})
	return body
}

func stripClaudeToolPrefixFromStreamLine(line []byte, prefix string) []byte {
	if prefix == "" {
		return line
	}
	payload := helps.JSONPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return line
	}
	contentBlock := gjson.GetBytes(payload, "content_block")
	if !contentBlock.Exists() {
		return line
	}

	blockType := contentBlock.Get("type").String()
	var updated []byte
	var err error

	switch blockType {
	case "tool_use":
		name := contentBlock.Get("name").String()
		if !strings.HasPrefix(name, prefix) {
			return line
		}
		updated, err = sjson.SetBytes(payload, "content_block.name", strings.TrimPrefix(name, prefix))
		if err != nil {
			return line
		}
	case "tool_reference":
		toolName := contentBlock.Get("tool_name").String()
		if !strings.HasPrefix(toolName, prefix) {
			return line
		}
		updated, err = sjson.SetBytes(payload, "content_block.tool_name", strings.TrimPrefix(toolName, prefix))
		if err != nil {
			return line
		}
	default:
		return line
	}

	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		return append([]byte("data: "), updated...)
	}
	return updated
}
