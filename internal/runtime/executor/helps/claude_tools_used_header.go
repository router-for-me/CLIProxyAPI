package helps

import (
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/MoeForge/moego/ojson"
)

// Claude Code 2.1.321 sends x-claude-code-sandy-plover on every first-party
// Messages call: the tools this conversation has used, as a JSON list of
// {"name":...} objects in first-use order, with Claude Code's own core tools
// moved behind the others (measured: `[]` before any tool call, then
// `[{"name":"Bash"}]`, then `[{"name":"Bash"},{"name":"Read"}]`). A cloaked
// request rebuilds that list from the tool_use blocks in its history. An MCP
// tool (name prefix mcp__) is marked {"name":...,"mcp":true}, which is the
// MCP marker 2.1.321 sends by default.
const (
	claudeToolsUsedMaxEntries = 64   // list stops after 64 entries
	claudeToolsUsedMaxBytes   = 7168 // or at 7168 bytes including brackets and commas
	claudeToolsUsedMaxName    = 256  // names longer than 256 bytes are skipped
)

// claudeCodeCoreToolNames are the built-in Claude Code tools that 2.1.321 lists last.
var claudeCodeCoreToolNames = map[string]bool{
	"Bash": true, "Read": true, "Edit": true, "Write": true, "Glob": true, "Grep": true,
	"NotebookEdit": true, "TodoWrite": true, "Agent": true, "Task": true, "ToolSearch": true,
	"EnterPlanMode": true, "ExitPlanMode": true,
}

// ClaudeToolsUsedHeader returns the x-claude-code-sandy-plover value for a Messages body.
func ClaudeToolsUsedHeader(body []byte) string {
	seen := map[string]bool{}
	var names []string
	for _, msg := range ojson.GetBytesNoCopy(body, "messages").Array() {
		if msg.Get("role").String() != "assistant" {
			continue
		}
		for _, blk := range msg.Get("content").Array() {
			if blk.Get("type").String() != "tool_use" {
				continue
			}
			name := blk.Get("name").String()
			if name == "" || len(name) > claudeToolsUsedMaxName || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	ordered := make([]string, 0, len(names))
	for _, name := range names {
		if !claudeCodeCoreToolNames[name] {
			ordered = append(ordered, name)
		}
	}
	for _, name := range names {
		if claudeCodeCoreToolNames[name] {
			ordered = append(ordered, name)
		}
	}
	parts := make([]string, 0, len(ordered))
	size := 2
	for _, name := range ordered {
		if len(parts) == claudeToolsUsedMaxEntries {
			break
		}
		entry := claudeToolsUsedEntry(name)
		add := len(entry)
		if len(parts) > 0 {
			add++
		}
		if size+add > claudeToolsUsedMaxBytes {
			break
		}
		parts = append(parts, entry)
		size += add
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// claudeToolsUsedEntry writes {"name":...} the way JavaScript's JSON.stringify
// does, with every UTF-16 code unit from U+007F up written as \uXXXX so the
// header stays ASCII.
func claudeToolsUsedEntry(name string) string {
	var out strings.Builder
	out.WriteString(`{"name":"`)
	for _, r := range name {
		switch {
		case r == '"':
			out.WriteString(`\"`)
		case r == '\\':
			out.WriteString(`\\`)
		case r == '\b':
			out.WriteString(`\b`)
		case r == '\f':
			out.WriteString(`\f`)
		case r == '\n':
			out.WriteString(`\n`)
		case r == '\r':
			out.WriteString(`\r`)
		case r == '\t':
			out.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(&out, "\\u%04x", r)
		case r < 0x7f:
			out.WriteRune(r)
		default:
			for _, unit := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&out, "\\u%04x", unit)
			}
		}
	}
	out.WriteByte('"')
	if strings.HasPrefix(name, "mcp__") {
		out.WriteString(`,"mcp":true`)
	}
	out.WriteByte('}')
	return out.String()
}
