package helps

import (
	"encoding/json"
	"strings"
	"testing"
)

func bareFixture(names ...string) []byte {
	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		tools = append(tools, map[string]any{"name": name, "input_schema": map[string]any{"type": "object"}})
	}
	body, _ := json.Marshal(map[string]any{"tools": tools, "messages": []any{map[string]any{"role": "user", "content": "hello"}}})
	return body
}

func TestClaudeMCPBareAliasProof(t *testing.T) {
	const alias = "mcp__alpha_beta__word_shell"
	const bare = "word_shell"
	pre, final := bareFixture("Shell"), bareFixture(alias)
	aliases := map[string]string{alias: "Shell"}
	change := func(body []byte, old, new string) []byte { return []byte(strings.Replace(string(body), old, new, 1)) }
	tests := []struct {
		name       string
		pre, final []byte
		aliases    map[string]string
		want       bool
	}{
		{"exact", pre, final, aliases, true},
		{"addition", pre, bareFixture(alias, "Other"), aliases, true},
		{"absent-allocation", pre, final, nil, false},
		{"stale-original", bareFixture("Other"), final, aliases, false},
		{"missing-final", pre, bareFixture(), aliases, false},
		{"renamed-final", pre, bareFixture(alias + "x"), aliases, false},
		{"whole-name", pre, bareFixture(alias, bare), aliases, false},
		{"original", bareFixture("Shell", bare), final, aliases, false},
		{"aliased-original", bareFixture("Shell", bare), bareFixture(alias, "mcp__alpha_beta__other_name"), map[string]string{alias: "Shell", "mcp__alpha_beta__other_name": bare}, false},
		{"caller-mcp", bareFixture("Shell", "mcp__caller__word_shell"), final, aliases, false},
		{"final-caller-mcp", pre, bareFixture(alias, "mcp__caller__word_shell"), aliases, false},
		{"two-components", bareFixture("Shell", "Other"), bareFixture(alias, "mcp__gamma_delta__word_shell"), map[string]string{alias: "Shell", "mcp__gamma_delta__word_shell": "Other"}, false},
		{"two-aliases-one-original", pre, bareFixture(alias, "mcp__gamma_delta__word_shell"), map[string]string{alias: "Shell", "mcp__gamma_delta__word_shell": "Shell"}, false},
		{"duplicate-pre", bareFixture("Shell", "Shell"), final, aliases, false},
		{"duplicate-final", pre, bareFixture(alias, alias), aliases, false},
		{"case-name", bareFixture("Shell", "shell"), final, aliases, false},
		{"case-original-component", bareFixture("Shell", "WORD_shell"), final, aliases, false},
		{"case-final-component", pre, bareFixture(alias, "WORD_shell"), aliases, false},
		{"case-caller-component", bareFixture("Shell", "mcp__caller__WORD_shell"), final, aliases, false},
		{"invalid-json", pre, []byte(`{"tools":`), aliases, false},
		{"wrong-tools", pre, change(final, `"tools":[`, `"tools":{`), aliases, false},
		{"null-name", pre, change(final, `"`+alias+`"`, `null`), aliases, false},
		{"number-name", pre, change(final, `"`+alias+`"`, `1`), aliases, false},
		{"empty-name", pre, change(final, alias, ""), aliases, false},
		{"duplicate-key", pre, change(final, `"name":`, `"name":"wrong","name":`), aliases, false},
		{"escaped-duplicate", pre, change(final, `"name":`, `"na\u006de":"wrong","name":`), aliases, false},
		{"case-key", pre, change(final, `"name":`, `"Name":"wrong","name":`), aliases, false},
		{"root-case-key", pre, change(final, `"tools":`, `"Tools":[],"tools":`), aliases, false},
		{"root-escaped-key", pre, change(final, `"tools":`, `"t\u006fols":[],"tools":`), aliases, false},
		{"pre-root-escaped-key", change(pre, `"tools":`, `"t\u006fols":[],"tools":`), final, aliases, false},
		{"message-duplicate-content", pre, change(final, `"content":`, `"content":"other","content":`), aliases, false},
		{"message-case-content", pre, change(final, `"content":`, `"Content":"other","content":`), aliases, false},
		{"message-escaped-content", pre, change(final, `"content":`, `"co\u006etent":"other","content":`), aliases, false},
		{"message-invalid-content", pre, change(final, `"content":"hello"`, `"content":{}`), aliases, false},
		{"tool-schema-not-object", pre, change(final, `"input_schema":{"type":"object"}`, `"input_schema":[]`), aliases, false},
		{"tool-search", pre, change(final, `"name":`, `"type":"tool_search_tool_regex_20251119","name":`), aliases, false},
		{"mcp-toolset", pre, change(final, `"name":`, `"type":"mcp_toolset","name":`), aliases, false},
		{"missing-tools", pre, []byte(`{"messages":[]}`), aliases, false},
		{"null-tools", pre, []byte(`{"tools":null,"messages":[]}`), aliases, false},
		{"wrong-tools-object", pre, []byte(`{"tools":{},"messages":[]}`), aliases, false},
		{"inline-collection", pre, change(final, `"name":`, `"tools":[],"name":`), aliases, false},
		{"unknown-typed", pre, change(final, `"name":`, `"type":"future","name":`), aliases, false},
		{"final-custom-typed", pre, change(final, `"name":`, `"type":"custom","name":`), aliases, false},
		{"pre-custom-typed", change(pre, `"name":`, `"type":"custom","name":`), final, aliases, true},
		{"server", pre, change(final, `"name":`, `"type":"web_search_20250305","name":`), aliases, false},
		{"deferred", pre, change(final, `"name":`, `"defer_loading":true,"name":`), aliases, false},
		{"mcp-servers", pre, change(final, `"tools":`, `"mcp_servers":[],"tools":`), aliases, false},
		{"additional-tools", pre, change(final, `"tools":`, `"additional_tools":[],"tools":`), aliases, false},
		{"thread-continue", change(pre, `"tools":`, `"thread":{"type":"continue","previous_message_id":"fake"},"tools":`), final, aliases, false},
		{"final-continuation", pre, change(final, `"tools":`, `"thread":{"type":"continue"},"tools":`), aliases, false},
		{"history-only", pre, []byte(`{"tools":[],"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"` + alias + `","input":{}}]}]}`), aliases, false},
	}
	for _, blockType := range []string{"tool_addition", "tool_removal", "tool_reference", "tool_definition", "mcp_toolset", "connector", "server_tool_use"} {
		body := change(final, `"content":"hello"`, `"content":[{"type":"`+blockType+`","tools":[]}]`)
		tests = append(tests, struct {
			name       string
			pre, final []byte
			aliases    map[string]string
			want       bool
		}{"inline-" + blockType, pre, body, aliases, false})
		body = change(pre, `"content":"hello"`, `"content":[{"type":"`+blockType+`","tools":[]}]`)
		tests = append(tests, struct {
			name       string
			pre, final []byte
			aliases    map[string]string
			want       bool
		}{"pre-inline-" + blockType, body, final, aliases, false})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforePre, beforeFinal := string(tt.pre), string(tt.final)
			got := BuildClaudeMCPBareAliasMap(tt.pre, tt.final, tt.aliases)
			if (got[bare] == "Shell") != tt.want {
				t.Fatalf("proof = %v, want eligible %v", got, tt.want)
			}
			if string(tt.pre) != beforePre || string(tt.final) != beforeFinal {
				t.Fatal("proof mutated payload")
			}
		})
	}
}

func TestClaudeMCPBareAliasGeneratedNames(t *testing.T) {
	for _, original := range []string{"CaseSensitiveTool", strings.Repeat("long_", 25)} {
		alias := ClaudeMCPToolAlias("fixture-only-secret", original, 0)
		component := claudeBareComponent(alias)
		proof := BuildClaudeMCPBareAliasMap(bareFixture(original), bareFixture(alias), map[string]string{alias: original})
		if proof[component] != original {
			t.Fatalf("truncated alias proof = %v", proof)
		}
	}
	alias := "mcp__alpha_beta__web_search"
	if proof := BuildClaudeMCPBareAliasMap(bareFixture("Search"), bareFixture(alias), map[string]string{alias: "Search"}); len(proof) != 0 {
		t.Fatalf("built-in collision: %v", proof)
	}
	pre := []byte(`{"tools":[{"name":"Shell","input_schema":{"properties":{"name":{"type":"string"},"tools":{"type":"array"}}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"Shell","input":{"name":"text","tools":["anything"]}}]}]}`)
	alias = "mcp__alpha_beta__word_shell"
	if proof := BuildClaudeMCPBareAliasMap(pre, bareFixture(alias), map[string]string{alias: "Shell"}); proof["word_shell"] != "Shell" {
		t.Fatalf("opaque inputs/schema falsely scanned: %v", proof)
	}
	other := ClaudeMCPToolAlias("fixture-only-secret", "Other", 0)
	aliases := map[string]string{alias: "Shell", other: "Other"}
	for _, originalNames := range [][]string{{"Shell", "Other"}, {"Other", "Shell"}} {
		for _, finalNames := range [][]string{{alias, other}, {other, alias}} {
			proof := BuildClaudeMCPBareAliasMap(bareFixture(originalNames...), bareFixture(finalNames...), aliases)
			if proof["word_shell"] != "Shell" || proof[claudeBareComponent(other)] != "Other" {
				t.Fatalf("order-dependent proof: %v", proof)
			}
		}
	}
}
