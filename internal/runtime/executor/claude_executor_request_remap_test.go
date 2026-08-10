package executor

import (
	"bytes"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

func TestRemapOAuthToolNamesWithBatchedEditsMatchesLegacyBytes(t *testing.T) {
	secret := "differential-caller"
	collision := helps.ClaudeMCPToolAlias(secret, "fetch_url", 0)
	longName := "读取_" + strings.Repeat("very_long_tool_name_", 8)
	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "all reference shapes and undeclared history",
			body: []byte(`{"model":"claude-opus-5","tools":[{"name":"search_web","input_schema":{"type":"object"}},{"name":"Search_Web","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"search_web"},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"search_web","input":{}},{"type":"tool_reference","tool_name":"Search_Web"},{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"tool_reference","tool_name":"search_web"}]},{"type":"tool_use","id":"toolu_unknown","name":"not_declared","input":{}}]}]}`),
		},
		{
			name: "typed custom server existing MCP and duplicate declaration",
			body: []byte(`{"tools":[{"type":"custom","name":"client_custom","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"},{"name":"mcp__context7__query-docs"},{"name":"client_custom"}],"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"client_custom","id":"toolu_1","input":{}},{"type":"tool_reference","tool_name":"web_search"},{"type":"tool_reference","tool_name":"mcp__context7__query-docs"}]}]}`),
		},
		{
			name: "alias collision",
			body: []byte(fmt.Sprintf(`{"tools":[{"name":%q},{"name":"fetch_url"}],"tool_choice":{"type":"tool","name":"fetch_url"}}`, collision)),
		},
		{
			name: "unicode long and case distinct names",
			body: []byte(fmt.Sprintf(`{"messages":[{"content":[{"name":%q,"type":"tool_use"},{"tool_name":"read_file","type":"tool_reference"}]}],"tools":[{"name":%q},{"name":"read_file"}]}`, longName, longName)),
		},
		{
			name: "whitespace key order and escaped original",
			body: []byte("{\n  \"messages\" : [ { \"content\" : [ { \"name\" : \"fetch\\u005furl\", \"input\":{}, \"type\" : \"tool_use\" } ], \"role\" : \"assistant\" } ],\n  \"unknown\" : {\"number\":1.2300,\"escaped\":\"a\\/b\\n<>&\"},\n  \"tool_choice\" : { \"name\" : \"fetch\\u005furl\", \"type\" : \"tool\" },\n  \"tools\" : [ { \"description\" : \"keep \\\"bytes\\\"\", \"name\" : \"fetch\\u005furl\", \"input_schema\" : { \"type\" : \"object\" } } ]\n}"),
		},
		{
			name: "non-string names follow legacy coercion",
			body: []byte(`{"tools":[{"name":42}],"tool_choice":{"type":"tool","name":42},"messages":[{"content":[{"type":"tool_reference","tool_name":42}]}]}`),
		},
		{
			name: "no edits",
			body: []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"mcp__server__existing"}],"messages":[{"content":[{"type":"tool_reference","tool_name":"unknown"}]}]}`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := claudeMCPAliasOptions{secret: secret}
			wantBody, wantReverseMap := remapOAuthToolNamesWithOptionsLegacy(test.body, options)
			gotBody, gotReverseMap, ok := remapOAuthToolNamesWithBatchedEdits(test.body, options)
			if !ok {
				t.Fatal("batched remap unexpectedly rejected valid JSON offsets")
			}
			if !bytes.Equal(gotBody, wantBody) {
				t.Fatalf("batched body differs from legacy bytes\n got: %s\nwant: %s", gotBody, wantBody)
			}
			if !maps.Equal(gotReverseMap, wantReverseMap) {
				t.Fatalf("batched reverseMap = %v, want %v", gotReverseMap, wantReverseMap)
			}
		})
	}
}

func TestRemapOAuthToolNamesWithBatchedEditsReturnsOriginalSliceWithoutEdits(t *testing.T) {
	body := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
	out, reverseMap, ok := remapOAuthToolNamesWithBatchedEdits(body, claudeMCPAliasOptions{secret: "no-edits"})
	if !ok {
		t.Fatal("batched remap rejected valid JSON")
	}
	if len(reverseMap) != 0 {
		t.Fatalf("reverseMap = %v, want empty", reverseMap)
	}
	if len(out) == 0 || &out[0] != &body[0] {
		t.Fatal("no-edit remap did not return the original slice")
	}
}

func TestRemapOAuthToolNamesWithOptionsFallsBackForMalformedJSON(t *testing.T) {
	body := []byte(`{"tools":[{"name":"search_web"}],"messages":[`)
	options := claudeMCPAliasOptions{secret: "malformed"}
	if _, _, ok := remapOAuthToolNamesWithBatchedEdits(body, options); ok {
		t.Fatal("batched remap accepted malformed JSON")
	}
	wantBody, wantReverseMap := remapOAuthToolNamesWithOptionsLegacy(body, options)
	gotBody, gotReverseMap := remapOAuthToolNamesWithOptions(body, options)
	if !bytes.Equal(gotBody, wantBody) || !maps.Equal(gotReverseMap, wantReverseMap) {
		t.Fatalf("fallback differs from legacy: body=%q map=%v, want body=%q map=%v", gotBody, gotReverseMap, wantBody, wantReverseMap)
	}
}

func TestReverseRemapOAuthToolNamesRecoversMangledAliases(t *testing.T) {
	body := []byte(`{"tools":[{"name":"glob","input_schema":{"type":"object"}},{"name":"read","input_schema":{"type":"object"}}]}`)
	remapped, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "mangled-alias-caller"})
	globAlias := gjson.GetBytes(remapped, "tools.0.name").String()
	readAlias := gjson.GetBytes(remapped, "tools.1.name").String()
	globParts, ok := parseClaudeMCPAlias(globAlias)
	if !ok {
		t.Fatalf("glob alias is invalid: %q", globAlias)
	}
	readParts, ok := parseClaudeMCPAlias(readAlias)
	if !ok {
		t.Fatalf("read alias is invalid: %q", readAlias)
	}

	repeatedAlias := "mcp__" + globParts.server + "__" + globAlias
	mixedAlias := "mcp__" + globParts.server + "__" + globParts.toolID + "_" + readParts.semantic
	response := []byte(fmt.Sprintf(`{"content":[
		{"type":"tool_use","id":"toolu_glob","name":%q,"input":{}},
		{"type":"tool_reference","tool_name":%q},
		{"type":"tool_result","tool_use_id":"toolu_read","content":[{"type":"tool_reference","tool_name":%q}]}
	]}`, repeatedAlias, mixedAlias, mixedAlias))

	restored, errReverse := reverseRemapOAuthToolNames(response, reverseMap)
	if errReverse != nil {
		t.Fatalf("reverseRemapOAuthToolNames() error = %v", errReverse)
	}
	if got := gjson.GetBytes(restored, "content.0.name").String(); got != "glob" {
		t.Fatalf("repeated alias restored to %q, want glob", got)
	}
	if got := gjson.GetBytes(restored, "content.1.tool_name").String(); got != "read" {
		t.Fatalf("mixed alias restored to %q, want read", got)
	}
	if got := gjson.GetBytes(restored, "content.2.content.0.tool_name").String(); got != "read" {
		t.Fatalf("nested mixed alias restored to %q, want read", got)
	}

	streamTests := []struct {
		name      string
		block     string
		fieldPath string
		want      string
	}{
		{
			name:      "repeated tool use alias",
			block:     fmt.Sprintf(`{"type":"tool_use","id":"toolu_glob","name":%q,"input":{}}`, repeatedAlias),
			fieldPath: "content_block.name",
			want:      "glob",
		},
		{
			name:      "mixed tool reference alias",
			block:     fmt.Sprintf(`{"type":"tool_reference","tool_name":%q}`, mixedAlias),
			fieldPath: "content_block.tool_name",
			want:      "read",
		},
	}
	for _, test := range streamTests {
		t.Run(test.name, func(t *testing.T) {
			line := []byte(`data: {"type":"content_block_start","index":0,"content_block":` + test.block + `}`)
			restoredLine, errStream := reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap)
			if errStream != nil {
				t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %v", errStream)
			}
			if got := gjson.GetBytes(helps.JSONPayload(restoredLine), test.fieldPath).String(); got != test.want {
				t.Fatalf("restored stream name = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReverseRemapOAuthToolNamesRejectsUnsafeMangledAliases(t *testing.T) {
	body := []byte(`{"tools":[{"name":"tool.name"},{"name":"tool/name"}]}`)
	remapped, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "ambiguous-alias-caller"})
	firstAlias := gjson.GetBytes(remapped, "tools.0.name").String()
	secondAlias := gjson.GetBytes(remapped, "tools.1.name").String()
	firstParts, ok := parseClaudeMCPAlias(firstAlias)
	if !ok {
		t.Fatalf("first alias is invalid: %q", firstAlias)
	}
	secondParts, ok := parseClaudeMCPAlias(secondAlias)
	if !ok {
		t.Fatalf("second alias is invalid: %q", secondAlias)
	}
	if firstParts.semantic != secondParts.semantic {
		t.Fatalf("semantic suffixes differ: %q != %q", firstParts.semantic, secondParts.semantic)
	}

	unknownToolID := "aaaaaaaaaaaa"
	if unknownToolID == firstParts.toolID || unknownToolID == secondParts.toolID {
		unknownToolID = "bbbbbbbbbbbb"
	}
	tests := []struct {
		name      string
		alias     string
		wantError string
	}{
		{
			name:      "ambiguous semantic suffix",
			alias:     "mcp__" + firstParts.server + "__" + unknownToolID + "_" + firstParts.semantic,
			wantError: "semantic suffix matches multiple declared tools",
		},
		{
			name:      "unrecoverable semantic suffix",
			alias:     "mcp__" + firstParts.server + "__" + unknownToolID + "_missing_tool",
			wantError: "no unique request-local match",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, test.alias))
			if _, errReverse := reverseRemapOAuthToolNames(response, reverseMap); errReverse == nil || !strings.Contains(errReverse.Error(), test.wantError) {
				t.Fatalf("reverseRemapOAuthToolNames() error = %v, want %q", errReverse, test.wantError)
			}

			line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, test.alias))
			if _, errStream := reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap); errStream == nil || !strings.Contains(errStream.Error(), test.wantError) {
				t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %v, want %q", errStream, test.wantError)
			}
		})
	}
}

func TestReverseRemapOAuthToolNamesPreservesUnrelatedMCPName(t *testing.T) {
	body := []byte(`{"tools":[{"name":"glob"}]}`)
	_, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "unrelated-mcp-caller"})
	response := []byte(`{"content":[{"type":"tool_use","id":"toolu_1","name":"mcp__external__query","input":{}}]}`)

	restored, errReverse := reverseRemapOAuthToolNames(response, reverseMap)
	if errReverse != nil {
		t.Fatalf("reverseRemapOAuthToolNames() error = %v", errReverse)
	}
	if got := gjson.GetBytes(restored, "content.0.name").String(); got != "mcp__external__query" {
		t.Fatalf("unrelated MCP name = %q, want unchanged", got)
	}
}

func TestApplyClaudeRawJSONEditsRejectsInvalidRanges(t *testing.T) {
	body := []byte(`{"a":"one","b":"two"}`)
	tests := []struct {
		name  string
		edits []claudeRawJSONEdit
	}{
		{name: "overlap", edits: []claudeRawJSONEdit{{start: 5, end: 10}, {start: 8, end: 12}}},
		{name: "negative", edits: []claudeRawJSONEdit{{start: -1, end: 1}}},
		{name: "reversed", edits: []claudeRawJSONEdit{{start: 5, end: 4}}},
		{name: "past end", edits: []claudeRawJSONEdit{{start: 5, end: len(body) + 1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := applyClaudeRawJSONEdits(body, test.edits); ok {
				t.Fatal("invalid edits unexpectedly succeeded")
			}
		})
	}
}

func FuzzRemapOAuthToolNamesWithBatchedEditsMatchesLegacy(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{}`),
		[]byte(`{"tools":[{"name":"search_web"}]}`),
		[]byte(`{"tools":[{"type":"custom","name":"读取文件"}],"tool_choice":{"type":"tool","name":"读取文件"},"messages":[{"content":[{"type":"tool_use","name":"读取文件"}]}]}`),
		[]byte("{\n\"messages\":[{\"content\":[{\"type\":\"tool_reference\",\"tool_name\":\"a\\u005fb\"}]}],\"tools\":[{\"name\":\"a\\u005fb\"}]}"),
	}
	for _, seed := range seeds {
		f.Add(seed, "fuzz-caller")
	}

	f.Fuzz(func(t *testing.T, body []byte, secret string) {
		if len(body) > 1<<20 || !gjson.ValidBytes(body) {
			return
		}
		options := claudeMCPAliasOptions{secret: secret}
		wantBody, wantReverseMap := remapOAuthToolNamesWithOptionsLegacy(body, options)
		gotBody, gotReverseMap, ok := remapOAuthToolNamesWithBatchedEdits(body, options)
		if !ok {
			t.Fatal("batched remap rejected valid JSON offsets")
		}
		if !bytes.Equal(gotBody, wantBody) || !maps.Equal(gotReverseMap, wantReverseMap) {
			t.Fatalf("batched result differs from legacy\nbody: %q\n got: %q %v\nwant: %q %v", body, gotBody, gotReverseMap, wantBody, wantReverseMap)
		}
	})
}
