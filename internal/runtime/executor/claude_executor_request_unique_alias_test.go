package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

// sharedStem reproduces the long-shared-stem collision: 44 caller MCP tools
// share one 71-byte stem, so the names exceed the 64-byte cap and the historical
// head-truncated semantic was identical for all of them.
const sharedStem = "mcp__example_apps__acme_inventory_service__acme_inventory_service_tool_"

func sharedStemToolNames(count int) []string {
	names := make([]string, count)
	for i := range names {
		names[i] = fmt.Sprintf("%sop%02d_%s", sharedStem, i, []string{"list", "get", "create", "update"}[i%4])
	}
	return names
}

func sharedStemRequest(names []string) []byte {
	tools := make([]string, len(names))
	for i, name := range names {
		tools[i] = fmt.Sprintf(`{"name":%q,"description":"d","input_schema":{"type":"object"}}`, name)
	}
	return []byte(`{"model":"claude-sonnet-5-5","tools":[` + strings.Join(tools, ",") + `],"messages":[{"role":"user","content":"hi"}]}`)
}

func TestRemapOAuthToolNamesSharedLongStemGetsUniqueSemantics(t *testing.T) {
	if len(sharedStem) < 64 {
		t.Fatalf("stem is %d bytes; the reproduction needs an over-length stem", len(sharedStem))
	}
	names := sharedStemToolNames(44)
	remapped, reverseMap := remapOAuthToolNamesWithOptions(sharedStemRequest(names), claudeMCPAliasOptions{secret: "shared-stem-caller"})

	seenAlias := make(map[string]string)
	seenSemantic := make(map[string]string)
	for i, original := range names {
		alias := gjson.GetBytes(remapped, fmt.Sprintf("tools.%d.name", i)).String()
		if !helps.IsClaudeMCPToolName(alias) {
			t.Fatalf("alias %q for %q is not a valid <=64-byte MCP name", alias, original)
		}
		if reverseMap[alias] != original {
			t.Fatalf("reverseMap[%q] = %q, want %q", alias, reverseMap[alias], original)
		}
		if other, dup := seenAlias[alias]; dup {
			t.Fatalf("alias %q shared by %q and %q", alias, other, original)
		}
		seenAlias[alias] = original
		parts, ok := parseClaudeMCPAlias(alias)
		if !ok {
			t.Fatalf("alias %q does not parse", alias)
		}
		if other, dup := seenSemantic[parts.semantic]; dup {
			t.Fatalf("semantic %q shared by %q and %q", parts.semantic, other, original)
		}
		seenSemantic[parts.semantic] = original
	}
}

func TestReverseRemapOAuthToolNamesSharedLongStemRoundTrip(t *testing.T) {
	names := sharedStemToolNames(44)
	remapped, reverseMap := remapOAuthToolNamesWithOptions(sharedStemRequest(names), claudeMCPAliasOptions{secret: "shared-stem-caller"})

	for i, original := range names {
		alias := gjson.GetBytes(remapped, fmt.Sprintf("tools.%d.name", i)).String()
		parts, _ := parseClaudeMCPAlias(alias)
		unknownToolID := "zzzzzz"
		variants := map[string]string{
			"exact":           alias,
			"unknown tool id": "mcp__" + parts.server + "__" + unknownToolID + "_" + parts.semantic,
			"repeated toolID": "mcp__" + parts.server + "__" + parts.toolID + "_" + parts.toolID + "_" + parts.semantic,
		}
		for label, emitted := range variants {
			response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, emitted))
			restored, err := reverseRemapOAuthToolNames(response, reverseMap)
			if err != nil {
				t.Fatalf("%s %q: non-stream restore error = %v", label, emitted, err)
			}
			if got := gjson.GetBytes(restored, "content.0.name").String(); got != original {
				t.Fatalf("%s %q: non-stream restored %q, want %q", label, emitted, got, original)
			}

			line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, emitted))
			restoredLine, err := reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap)
			if err != nil {
				t.Fatalf("%s %q: stream restore error = %v", label, emitted, err)
			}
			if got := gjson.GetBytes(helps.JSONPayload(restoredLine), "content_block.name").String(); got != original {
				t.Fatalf("%s %q: stream restored %q, want %q", label, emitted, got, original)
			}
		}
	}
}

func TestRemapOAuthToolNamesSanitizedCollisionGetsDistinctSemantics(t *testing.T) {
	body := []byte(`{"tools":[{"name":"tool.name"},{"name":"tool/name"}]}`)
	remapped, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "ambiguous-alias-caller"})
	first, _ := parseClaudeMCPAlias(gjson.GetBytes(remapped, "tools.0.name").String())
	second, _ := parseClaudeMCPAlias(gjson.GetBytes(remapped, "tools.1.name").String())
	if first.semantic == second.semantic {
		t.Fatalf("semantics collide: %q", first.semantic)
	}
	for _, tc := range []struct {
		parts claudeMCPAliasParts
		want  string
	}{{first, "tool.name"}, {second, "tool/name"}} {
		drifted := "mcp__" + tc.parts.server + "__zzzzzz_" + tc.parts.semantic
		response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, drifted))
		restored, err := reverseRemapOAuthToolNames(response, reverseMap)
		if err != nil {
			t.Fatalf("restore %q error = %v", drifted, err)
		}
		if got := gjson.GetBytes(restored, "content.0.name").String(); got != tc.want {
			t.Fatalf("restore %q = %q, want %q", drifted, got, tc.want)
		}
	}
}
