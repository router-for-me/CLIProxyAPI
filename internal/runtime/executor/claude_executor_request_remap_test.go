package executor

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestClaudeOAuthToolAliasRestoresContinuationWithoutDeclarations(t *testing.T) {
	secret := "continuation-alias-caller"
	createBody := []byte(`{"thread":{"type":"create"},"tools":[{"name":"Read","input_schema":{"type":"object"}}]}`)
	executor := &ClaudeExecutor{}
	_, reverseMap, errPrepare := executor.prepareClaudeOAuthToolNamesForRequest(createBody, claudeMCPAliasOptions{secret: secret})
	if errPrepare != nil {
		t.Fatalf("prepare create request: %v", errPrepare)
	}
	alias := ""
	for upstreamName := range reverseMap {
		alias = upstreamName
		break
	}
	if alias == "" {
		t.Fatal("create request did not allocate an OAuth MCP alias")
	}

	continuationBody := []byte(`{"thread":{"type":"continue","previous_message_id":"msg-one"},"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-one","content":"ok"}]}]}`)
	executor.rememberClaudeOAuthToolAliases(createBody, reverseMap, "msg-one")
	_, continuationReverseMap, errPrepareContinuation := executor.prepareClaudeOAuthToolNamesForRequest(continuationBody, claudeMCPAliasOptions{secret: secret})
	if errPrepareContinuation != nil {
		t.Fatalf("prepare continuation request: %v", errPrepareContinuation)
	}
	response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"tool-two","name":%q,"input":{}}]}`, alias))
	restored, errRestore := restoreClaudeOAuthToolNamesFromResponse(response, continuationReverseMap)
	if errRestore != nil {
		t.Fatalf("restore continuation tool name: %v", errRestore)
	}
	if got := gjson.GetBytes(restored, "content.0.name").String(); got != "Read" {
		t.Fatalf("continuation tool name = %q, want Read; response=%s", got, restored)
	}
}

func TestClaudeOAuthToolAliasMissingContinuationStateIsRequestScopedNotFound(t *testing.T) {
	continuationBody := []byte(`{"thread":{"type":"continue","previous_message_id":"missing-message"}}`)
	_, _, errPrepare := (&ClaudeExecutor{}).prepareClaudeOAuthToolNamesForRequest(continuationBody, claudeMCPAliasOptions{secret: "missing-state-caller"})
	if errPrepare == nil {
		t.Fatal("prepare continuation request returned nil error")
	}
	statusErr, okStatus := errPrepare.(interface{ StatusCode() int })
	if !okStatus || statusErr.StatusCode() != 404 {
		t.Fatalf("continuation error status = %v, want 404", errPrepare)
	}
	requestErr, okRequest := errPrepare.(cliproxyexecutor.RequestScopedError)
	if !okRequest || !requestErr.IsRequestScoped() {
		t.Fatalf("continuation error is not request-scoped: %T", errPrepare)
	}
}

func TestClaudeOAuthToolAliasEvictionReturnsThreadNotFound(t *testing.T) {
	executor := &ClaudeExecutor{}
	createBody := []byte(`{"thread":{"type":"create"}}`)
	executor.rememberClaudeOAuthToolAliases(createBody, map[string]string{"alias": "Read"}, "first-message")
	for i := 0; i < claudeOAuthToolAliasStateLimit; i++ {
		executor.rememberClaudeOAuthToolAliases(createBody, nil, fmt.Sprintf("message-%d", i))
	}

	continuationBody := []byte(`{"thread":{"type":"continue","previous_message_id":"first-message"}}`)
	_, _, errPrepare := executor.prepareClaudeOAuthToolNamesForRequest(continuationBody, claudeMCPAliasOptions{secret: "evicted-state-caller"})
	if errPrepare == nil {
		t.Fatal("evicted continuation returned nil error")
	}
	statusErr, okStatus := errPrepare.(interface{ StatusCode() int })
	if !okStatus || statusErr.StatusCode() != 404 {
		t.Fatalf("evicted continuation error status = %v, want 404", errPrepare)
	}
}

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
		{
			name: "mid-conversation tool changes",
			body: []byte(`{"tools":[{"name":"read_file","input_schema":{"type":"object"}},{"name":"lookup_notes","input_schema":{"type":"object"},"defer_loading":true},{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}]},{"role":"system","content":[{"type":"text","text":"tools changed"},{"type":"tool_addition","tool":{"type":"tool_reference","name":"lookup_notes"}},{"type":"tool_removal","tool":{"type":"tool_reference","name":"read_file"}},{"type":"tool_addition","tool":{"type":"tool_reference","name":"web_search"}},{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"lookup_notes","input_schema":{"type":"object"}}}},{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"db_query","input_schema":{"type":"object"}}}},{"type":"tool_removal","tool":{"type":"mcp_tool_reference","server_name":"docs","name":"read_file"}},{"type":"tool_removal","tool":"read_file"}]}]}`),
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

func TestReverseRemapOAuthToolNamesRecoversRepeatedServerAliases(t *testing.T) {
	const alias = "mcp__hmzqrngkulqv__xuo7jlxlpzee_Bash"
	reverseMap := map[string]string{
		alias:                                  "Bash",
		"mcp__hmzqrngkulqv__aaaaaaaaaaaa_Bash": "OtherBash",
	}
	tests := []struct {
		name          string
		responseAlias string
	}{
		{
			name:          "single repetition",
			responseAlias: "mcp__hmzqrngkulqv__hmzqrngkulqv__xuo7jlxlpzee_Bash",
		},
		{
			name:          "multiple repetitions",
			responseAlias: "mcp__hmzqrngkulqv__hmzqrngkulqv__hmzqrngkulqv__xuo7jlxlpzee_Bash",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, test.responseAlias))
			restored, errReverse := reverseRemapOAuthToolNames(response, reverseMap)
			if errReverse != nil {
				t.Fatalf("reverseRemapOAuthToolNames() error = %v", errReverse)
			}
			if got := gjson.GetBytes(restored, "content.0.name").String(); got != "Bash" {
				t.Fatalf("repeated server alias restored to %q, want Bash", got)
			}

			line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, test.responseAlias))
			restoredLine, errStream := reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap)
			if errStream != nil {
				t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %v", errStream)
			}
			if got := gjson.GetBytes(helps.JSONPayload(restoredLine), "content_block.name").String(); got != "Bash" {
				t.Fatalf("stream repeated server alias restored to %q, want Bash", got)
			}
		})
	}
}

func TestReverseRemapOAuthToolNamesRecoversMalformedToolIDBySemanticSuffix(t *testing.T) {
	const alias = "mcp__hmzqrngkulqv__xuo7jlxlpzee_Bash"
	reverseMap := map[string]string{alias: "Bash"}
	tests := []struct {
		name          string
		responseAlias string
	}{
		{name: "short tool ID", responseAlias: "mcp__hmzqrngkulqv__xuo7jlxlpze_Bash"},
		{name: "long tool ID", responseAlias: "mcp__hmzqrngkulqv__xuo7jlxlpzeea_Bash"},
		{name: "invalid base32 tool ID", responseAlias: "mcp__hmzqrngkulqv__xuo7jlxlpze0_Bash"},
		{name: "substituted base32 tool ID", responseAlias: "mcp__hmzqrngkulqv__auo7jlxlpzee_Bash"},
		{name: "repeated server and short tool ID", responseAlias: "mcp__hmzqrngkulqv__hmzqrngkulqv__xuo7jlxlpze_Bash"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, test.responseAlias))
			restored, errReverse := reverseRemapOAuthToolNames(response, reverseMap)
			if errReverse != nil {
				t.Fatalf("reverseRemapOAuthToolNames() error = %v", errReverse)
			}
			if got := gjson.GetBytes(restored, "content.0.name").String(); got != "Bash" {
				t.Fatalf("malformed tool ID alias restored to %q, want Bash", got)
			}

			line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, test.responseAlias))
			restoredLine, errStream := reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap)
			if errStream != nil {
				t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %v", errStream)
			}
			if got := gjson.GetBytes(helps.JSONPayload(restoredLine), "content_block.name").String(); got != "Bash" {
				t.Fatalf("stream malformed tool ID alias restored to %q, want Bash", got)
			}
		})
	}
}

func TestReverseRemapOAuthToolNamesWithBIP39Aliases(t *testing.T) {
	body := []byte(`{"tools":[{"name":"Bash","input_schema":{"type":"object"}},{"name":"fetch_url","input_schema":{"type":"object"}}]}`)
	remapped, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "bip39-caller"})
	bashAlias := gjson.GetBytes(remapped, "tools.0.name").String()
	fetchAlias := gjson.GetBytes(remapped, "tools.1.name").String()

	if !helps.IsClaudeMCPToolName(bashAlias) {
		t.Fatalf("generated bash alias is invalid: %q", bashAlias)
	}
	if !helps.IsClaudeMCPToolName(fetchAlias) {
		t.Fatalf("generated fetch alias is invalid: %q", fetchAlias)
	}

	bashParts, ok := parseClaudeMCPAlias(bashAlias)
	if !ok {
		t.Fatalf("parseClaudeMCPAlias(%q) failed", bashAlias)
	}
	if bashParts.semantic != "Bash" {
		t.Fatalf("bashParts.semantic = %q, want Bash", bashParts.semantic)
	}

	repeatedAlias := "mcp__" + bashParts.server + "__" + bashParts.server + "__" + bashParts.toolID + "_Bash"
	mangledToolIDAlias := "mcp__" + bashParts.server + "__corruptedword_Bash"
	repeatedToolIDAlias := "mcp__" + bashParts.server + "__" + bashParts.toolID + "_" + bashParts.toolID + "_Bash"
	extraWordAlias := "mcp__" + bashParts.server + "__" + bashParts.toolID + "_cabin_Bash"

	response := []byte(fmt.Sprintf(`{"content":[
		{"type":"tool_use","id":"toolu_1","name":%q,"input":{}},
		{"type":"tool_use","id":"toolu_2","name":%q,"input":{}},
		{"type":"tool_reference","tool_name":%q},
		{"type":"tool_use","id":"toolu_3","name":%q,"input":{}},
		{"type":"tool_use","id":"toolu_4","name":%q,"input":{}}
	]}`, bashAlias, repeatedAlias, mangledToolIDAlias, repeatedToolIDAlias, extraWordAlias))

	restored, errReverse := reverseRemapOAuthToolNames(response, reverseMap)
	if errReverse != nil {
		t.Fatalf("reverseRemapOAuthToolNames() error = %v", errReverse)
	}
	if got := gjson.GetBytes(restored, "content.0.name").String(); got != "Bash" {
		t.Fatalf("exact alias restored to %q, want Bash", got)
	}
	if got := gjson.GetBytes(restored, "content.1.name").String(); got != "Bash" {
		t.Fatalf("repeated alias restored to %q, want Bash", got)
	}
	if got := gjson.GetBytes(restored, "content.2.tool_name").String(); got != "Bash" {
		t.Fatalf("mangled toolID alias restored to %q, want Bash", got)
	}
	if got := gjson.GetBytes(restored, "content.3.name").String(); got != "Bash" {
		t.Fatalf("repeated toolID alias restored to %q, want Bash", got)
	}
	if got := gjson.GetBytes(restored, "content.4.name").String(); got != "Bash" {
		t.Fatalf("extra-word alias restored to %q, want Bash", got)
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
			name:      "ambiguous semantic suffix with malformed tool ID",
			alias:     "mcp__" + firstParts.server + "__" + unknownToolID[:len(unknownToolID)-1] + "_" + firstParts.semantic,
			wantError: "semantic suffix matches multiple declared tools",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, test.alias))
			_, errReverse := reverseRemapOAuthToolNames(response, reverseMap)
			if errReverse == nil || !strings.Contains(errReverse.Error(), test.wantError) {
				t.Fatalf("reverseRemapOAuthToolNames() error = %v, want %q", errReverse, test.wantError)
			}
			var requestErr cliproxyexecutor.RequestScopedError
			if !errors.As(errReverse, &requestErr) || !requestErr.IsRequestScoped() {
				t.Fatalf("reverseRemapOAuthToolNames() error = %T %v, want request-scoped", errReverse, errReverse)
			}

			line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, test.alias))
			_, errStream := reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap)
			if errStream == nil || !strings.Contains(errStream.Error(), test.wantError) {
				t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %v, want %q", errStream, test.wantError)
			}
			requestErr = nil
			if !errors.As(errStream, &requestErr) || !requestErr.IsRequestScoped() {
				t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %T %v, want request-scoped", errStream, errStream)
			}
		})
	}

	t.Run("unrecoverable semantic suffix fails open", func(t *testing.T) {
		unrecoverableAlias := "mcp__" + firstParts.server + "__" + unknownToolID + "_missing_tool"
		response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, unrecoverableAlias))
		restored, errReverse := reverseRemapOAuthToolNames(response, reverseMap)
		if errReverse != nil {
			t.Fatalf("reverseRemapOAuthToolNames() error = %v, want fail-open nil", errReverse)
		}
		if got := gjson.GetBytes(restored, "content.0.name").String(); got != unrecoverableAlias {
			t.Fatalf("unrecoverable alias = %q, want %q forwarded unchanged", got, unrecoverableAlias)
		}

		line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, unrecoverableAlias))
		restoredLine, errStream := reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap)
		if errStream != nil {
			t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %v, want fail-open nil", errStream)
		}
		if !bytes.Equal(restoredLine, line) {
			t.Fatalf("stream line = %s, want it forwarded unchanged", restoredLine)
		}
	})
}

func TestReverseRemapOAuthToolNames_OverlappingSemanticSuffix(t *testing.T) {
	body := []byte(`{"tools":[{"name":"file"},{"name":"read_file"}]}`)
	remapped, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "overlapping-caller"})
	fileAlias := gjson.GetBytes(remapped, "tools.0.name").String()
	readFileAlias := gjson.GetBytes(remapped, "tools.1.name").String()

	if !helps.IsClaudeMCPToolName(fileAlias) {
		t.Fatalf("fileAlias is invalid: %q", fileAlias)
	}
	readFileParts, ok := parseClaudeMCPAlias(readFileAlias)
	if !ok {
		t.Fatalf("parseClaudeMCPAlias(%q) failed", readFileAlias)
	}

	// Model generates repeated toolID for read_file: mcp__<server>__<toolID>_<toolID>_read_file
	// Even though "_file" is a suffix of "_read_file", longest match should resolve to "read_file"
	driftedReadFile := "mcp__" + readFileParts.server + "__" + readFileParts.toolID + "_" + readFileParts.toolID + "_read_file"
	response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, driftedReadFile))

	restored, errReverse := reverseRemapOAuthToolNames(response, reverseMap)
	if errReverse != nil {
		t.Fatalf("reverseRemapOAuthToolNames() error = %v", errReverse)
	}
	if got := gjson.GetBytes(restored, "content.0.name").String(); got != "read_file" {
		t.Fatalf("restored tool name = %q, want read_file", got)
	}

	// Exact file alias still resolves to file
	responseFile := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_2","name":%q,"input":{}}]}`, fileAlias))
	restoredFile, errFile := reverseRemapOAuthToolNames(responseFile, reverseMap)
	if errFile != nil {
		t.Fatalf("reverseRemapOAuthToolNames(file) error = %v", errFile)
	}
	if got := gjson.GetBytes(restoredFile, "content.0.name").String(); got != "file" {
		t.Fatalf("restored tool name = %q, want file", got)
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
		[]byte(`{"tools":[{"name":"lookup_notes"}],"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_reference","name":"lookup_notes"}},{"type":"tool_removal","tool":{"type":"tool_reference","name":"lookup_notes"}},{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"lookup_notes"}}}]}]}`),
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

// TestReverseRemapPassesThroughCallerMCPToolsOnVirtualServerCollision pins the
// behaviour when a caller's real MCP server is named exactly like the derived
// two-word virtual server. Word-based server components are only ~2048^2 wide,
// and plausible server names such as "file_system" or "web_search" are valid
// BIP-39 word pairs, so this collision is reachable. The caller's own tools must
// never be rewritten into a proxied tool, and must never fail the request.
func TestReverseRemapPassesThroughCallerMCPToolsOnVirtualServerCollision(t *testing.T) {
	const secret = "virtual-server-collision"
	server := strings.Split(helps.ClaudeMCPToolAlias(secret, "probe", 0), "__")[1]

	native := []string{
		// Same semantic suffix as a proxied tool.
		"mcp__" + server + "__read_file",
		// Semantic suffix of a proxied tool preceded by an extra word, which is
		// exactly the shape the drift fallback is designed to absorb.
		"mcp__" + server + "__grep_read_file",
		// No proxied counterpart at all.
		"mcp__" + server + "__write_file",
	}
	body := []byte(fmt.Sprintf(
		`{"tools":[{"name":"read_file","input_schema":{"type":"object"}},{"name":%q},{"name":%q},{"name":%q}]}`,
		native[0], native[1], native[2]))

	upstream, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: secret})

	alias := ""
	for renamed, original := range reverseMap {
		if original == "read_file" && renamed != original {
			alias = renamed
		}
	}
	if alias == "" {
		t.Fatal("read_file was not aliased, the collision scenario is not being exercised")
	}
	for index, name := range native {
		if got := gjson.GetBytes(upstream, fmt.Sprintf("tools.%d.name", index+1)).String(); got != name {
			t.Fatalf("caller MCP tool %d sent upstream as %q, want %q unchanged", index, got, name)
		}
	}

	for _, name := range native {
		response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, name))
		restored, err := restoreClaudeOAuthToolNamesFromResponse(response, reverseMap)
		if err != nil {
			t.Fatalf("caller MCP tool %q failed to restore: %v", name, err)
		}
		if got := gjson.GetBytes(restored, "content.0.name").String(); got != name {
			t.Fatalf("caller MCP tool %q restored as %q, want it passed through unchanged", name, got)
		}

		line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, name))
		restoredLine, errLine := restoreClaudeOAuthToolNamesFromStreamLine(line, reverseMap)
		if errLine != nil {
			t.Fatalf("caller MCP tool %q failed to restore from stream: %v", name, errLine)
		}
		if got := gjson.GetBytes(helps.JSONPayload(restoredLine), "content_block.name").String(); got != name {
			t.Fatalf("caller MCP tool %q restored from stream as %q, want unchanged", name, got)
		}
	}

	// The proxied tool must still round-trip, including the drifted shapes the
	// BIP-39 change was introduced to recover.
	toolPart := strings.SplitN(alias, "__", 3)[2]
	for _, drifted := range []string{alias, "mcp__" + server + "__" + server + "__" + toolPart, "mcp__" + server + "__abandon_read_file"} {
		response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, drifted))
		restored, err := restoreClaudeOAuthToolNamesFromResponse(response, reverseMap)
		if err != nil {
			t.Fatalf("proxied alias %q failed to restore: %v", drifted, err)
		}
		if got := gjson.GetBytes(restored, "content.0.name").String(); got != "read_file" {
			t.Fatalf("proxied alias %q restored as %q, want %q", drifted, got, "read_file")
		}
	}
}

// TestRemapKeepsReverseMapEmptyWhenOnlyCallerMCPToolsArePresent guards the
// passthrough bookkeeping from turning an untouched request into one that runs
// the restore path.
func TestRemapKeepsReverseMapEmptyWhenOnlyCallerMCPToolsArePresent(t *testing.T) {
	body := []byte(`{"tools":[{"name":"mcp__context7__query-docs"},{"type":"web_search_20250305","name":"web_search"}]}`)
	out, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "no-proxied-tools"})
	if len(reverseMap) != 0 {
		t.Fatalf("reverseMap = %v, want empty when nothing was aliased", reverseMap)
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("body = %s, want unchanged %s", out, body)
	}
}

// TestReverseRemapOAuthToolNamesFailsOpenOnTrailingMarkup verifies that an alias with
// trailing prompt markup glued to it is forwarded verbatim instead of failing the
// response or terminating the SSE stream.
func TestReverseRemapOAuthToolNamesFailsOpenOnTrailingMarkup(t *testing.T) {
	const alias = "mcp__hmzqrngkulqv__xuo7jlxlpzee_clear_thinking"
	malformedAlias := alias + "</parameter>\n<parameter name=\"merge\""
	reverseMap := map[string]string{alias: "clear_thinking"}

	response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, malformedAlias))
	restored, errReverse := reverseRemapOAuthToolNames(response, reverseMap)
	if errReverse != nil {
		t.Fatalf("reverseRemapOAuthToolNames() error = %v, want fail-open nil", errReverse)
	}
	if !bytes.Equal(restored, response) {
		t.Fatalf("reverseRemapOAuthToolNames() returned modified response: %s", restored)
	}

	line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, malformedAlias))
	restoredLine, errStream := reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap)
	if errStream != nil {
		t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %v, want fail-open nil", errStream)
	}
	if !bytes.Equal(restoredLine, line) {
		t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() returned modified line: %s", restoredLine)
	}
}

func TestClaudeMCPAliasRestoreErrorStaysRequestScoped(t *testing.T) {
	var requestErr cliproxyexecutor.RequestScopedError = claudeMCPAliasRestoreError{fmt.Errorf("probe")}
	if !requestErr.IsRequestScoped() {
		t.Fatal("claudeMCPAliasRestoreError.IsRequestScoped() = false, want true")
	}
}

func TestReverseRemapOAuthToolNames_ToolSearchResult_NonStream(t *testing.T) {
	body := []byte(`{"tools":[{"name":"task_list","input_schema":{"type":"object"}},{"name":"fetch_url","input_schema":{"type":"object"}}]}`)
	remapped, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "tool-search-caller"})
	taskAlias := gjson.GetBytes(remapped, "tools.0.name").String()
	fetchAlias := gjson.GetBytes(remapped, "tools.1.name").String()

	if taskAlias == "task_list" || fetchAlias == "fetch_url" {
		t.Fatalf("tools were not aliased: task=%q, fetch=%q", taskAlias, fetchAlias)
	}

	// 1. Successful search result with multiple tool_references
	resp := []byte(fmt.Sprintf(`{
		"id": "msg_01HczuyKgD1KCUuVzvZEH3WN",
		"type": "message",
		"role": "assistant",
		"content": [
			{
				"type": "tool_search_tool_result",
				"tool_use_id": "srvtoolu_01HczuyKgD1KCUuVzvZEH3WN",
				"content": {
					"type": "tool_search_tool_search_result",
					"tool_references": [
						{"type": "tool_reference", "tool_name": %q},
						{"type": "tool_reference", "tool_name": %q}
					]
				}
			},
			{
				"type": "tool_use",
				"id": "toolu_01",
				"name": %q,
				"input": {"action": "list"}
			}
		]
	}`, taskAlias, fetchAlias, taskAlias))

	restored, err := reverseRemapOAuthToolNames(resp, reverseMap)
	if err != nil {
		t.Fatalf("reverseRemapOAuthToolNames() error = %v", err)
	}

	ref0 := gjson.GetBytes(restored, "content.0.content.tool_references.0.tool_name").String()
	ref1 := gjson.GetBytes(restored, "content.0.content.tool_references.1.tool_name").String()
	toolUseName := gjson.GetBytes(restored, "content.1.name").String()

	if ref0 != "task_list" {
		t.Errorf("ref0 = %q, want task_list", ref0)
	}
	if ref1 != "fetch_url" {
		t.Errorf("ref1 = %q, want fetch_url", ref1)
	}
	if toolUseName != "task_list" {
		t.Errorf("toolUseName = %q, want task_list", toolUseName)
	}

	// 2. Error variant (tool_search_tool_result_error) without tool_references should pass cleanly
	errorResp := []byte(`{
		"id": "msg_02",
		"type": "message",
		"role": "assistant",
		"content": [
			{
				"type": "tool_search_tool_result",
				"tool_use_id": "srvtoolu_02",
				"content": {
					"type": "tool_search_tool_result_error",
					"error_code": "regex_compilation_failed"
				}
			}
		]
	}`)

	restoredErr, err := reverseRemapOAuthToolNames(errorResp, reverseMap)
	if err != nil {
		t.Fatalf("reverseRemapOAuthToolNames(errorResp) error = %v", err)
	}
	if !bytes.Equal(restoredErr, errorResp) {
		t.Fatalf("errorResp altered: %s", string(restoredErr))
	}
}

func TestReverseRemapOAuthToolNamesFromStreamLine_ToolSearchResult_Stream(t *testing.T) {
	body := []byte(`{"tools":[{"name":"task_list","input_schema":{"type":"object"}}]}`)
	remapped, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "tool-search-stream-caller"})
	taskAlias := gjson.GetBytes(remapped, "tools.0.name").String()

	line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_01","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":%q}]}}}`, taskAlias))

	restoredLine, err := reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap)
	if err != nil {
		t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %v", err)
	}

	if !bytes.HasPrefix(restoredLine, []byte("data: ")) {
		t.Fatalf("restoredLine lost data: prefix: %s", string(restoredLine))
	}
	gotToolName := gjson.GetBytes(helps.JSONPayload(restoredLine), "content_block.content.tool_references.0.tool_name").String()
	if gotToolName != "task_list" {
		t.Fatalf("stream restored tool_name = %q, want task_list", gotToolName)
	}
}

func TestRemapOAuthToolNames_ToolSearchResultInMessageHistory(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"name": "task_list", "input_schema": {"type": "object"}},
			{"type": "advisor_20260301", "name": "advisor", "model": "claude-haiku-4-5-20251001"},
			{"type": "agent_toolset_20260401"}
		],
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_search_tool_result",
						"tool_use_id": "srvtoolu_01",
						"content": {
							"type": "tool_search_tool_search_result",
							"tool_references": [
								{"type": "tool_reference", "tool_name": "task_list"}
							]
						}
					}
				]
			}
		]
	}`)

	remapped, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "history-caller"})
	taskAlias := reverseMapKeyFor(reverseMap, "task_list")
	if taskAlias == "" {
		t.Fatal("task_list was not aliased")
	}

	// Verify server tools were preserved
	tools := gjson.GetBytes(remapped, "tools")
	if tools.Get("1.type").String() != "advisor_20260301" || tools.Get("1.name").String() != "advisor" {
		t.Fatalf("advisor server tool was altered: %s", tools.Get("1").Raw)
	}
	if tools.Get("2.type").String() != "agent_toolset_20260401" {
		t.Fatalf("agent_toolset was altered: %s", tools.Get("2").Raw)
	}

	// Verify tool_search_tool_result in messages was remapped
	gotHistoryToolName := gjson.GetBytes(remapped, "messages.0.content.0.content.tool_references.0.tool_name").String()
	if gotHistoryToolName != taskAlias {
		t.Fatalf("history tool_name = %q, want aliased %q", gotHistoryToolName, taskAlias)
	}
}

// TestRemapOAuthToolNames_MidConversationToolChanges covers tool_addition and
// tool_removal blocks in role=system messages. Upstream resolves their tool
// references against tools[], so a reference to a declared client tool must
// carry the same MCP alias or the request fails with "references unknown
// tool". Both remap paths must agree.
func TestRemapOAuthToolNames_MidConversationToolChanges(t *testing.T) {
	const tools = `[` +
		`{"name":"read_file","description":"Read a file","input_schema":{"type":"object","properties":{}}},` +
		`{"name":"lookup_notes","description":"Look up notes","input_schema":{"type":"object","properties":{}},"defer_loading":true},` +
		`{"type":"web_search_20250305","name":"web_search"},` +
		`{"name":"mcp__context7__query-docs","input_schema":{"type":"object"}}` +
		`]`

	type nameCheck struct {
		path     string
		original string
		aliased  bool
	}
	tests := []struct {
		name     string
		messages string
		checks   []nameCheck
	}{
		{
			name:     "tool_addition reference to declared tool",
			messages: `[{"role":"user","content":"Reply with exactly: ok"},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_reference","name":"lookup_notes"}}]}]`,
			checks:   []nameCheck{{path: "messages.1.content.0.tool.name", original: "lookup_notes", aliased: true}},
		},
		{
			name:     "tool_removal reference to declared tool",
			messages: `[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_removal","tool":{"type":"tool_reference","name":"read_file"}}]}]`,
			checks:   []nameCheck{{path: "messages.1.content.0.tool.name", original: "read_file", aliased: true}},
		},
		{
			name:     "mixed with text and tool_use history",
			messages: `[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]},{"role":"system","content":[{"type":"text","text":"Tools changed."},{"type":"tool_removal","tool":{"type":"tool_reference","name":"read_file"}},{"type":"tool_addition","tool":{"type":"tool_reference","name":"lookup_notes"}}]}]`,
			checks: []nameCheck{
				{path: "messages.1.content.0.name", original: "read_file", aliased: true},
				{path: "messages.3.content.0.text", original: "Tools changed."},
				{path: "messages.3.content.1.tool.name", original: "read_file", aliased: true},
				{path: "messages.3.content.2.tool.name", original: "lookup_notes", aliased: true},
			},
		},
		{
			name:     "undeclared server and caller MCP names unchanged",
			messages: `[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_reference","name":"Bash"}},{"type":"tool_addition","tool":{"type":"tool_reference","name":"web_search"}},{"type":"tool_removal","tool":{"type":"tool_reference","name":"mcp__context7__query-docs"}}]}]`,
			checks: []nameCheck{
				{path: "messages.1.content.0.tool.name", original: "Bash"},
				{path: "messages.1.content.1.tool.name", original: "web_search"},
				{path: "messages.1.content.2.tool.name", original: "mcp__context7__query-docs"},
			},
		},
		{
			name:     "MCP connector references unchanged",
			messages: `[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"mcp_tool_reference","server_name":"docs","name":"read_file"}},{"type":"tool_removal","tool":{"type":"mcp_toolset_reference","server_name":"read_file"}}]}]`,
			checks: []nameCheck{
				{path: "messages.1.content.0.tool.name", original: "read_file"},
				{path: "messages.1.content.1.tool.server_name", original: "read_file"},
			},
		},
		{
			name:     "inline definition redefining declared tool",
			messages: `[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"lookup_notes","description":"v2","input_schema":{"type":"object"}}}}]}]`,
			checks:   []nameCheck{{path: "messages.1.content.0.tool.definition.name", original: "lookup_notes", aliased: true}},
		},
		{
			name:     "inline definitions of undeclared or server tools unchanged",
			messages: `[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"db_query","input_schema":{"type":"object"}}}},{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"type":"web_search_20260209","name":"read_file"}}},{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"type":"mcp_toolset","mcp_server_name":"calendar"}}}]}]`,
			checks: []nameCheck{
				{path: "messages.1.content.0.tool.definition.name", original: "db_query"},
				{path: "messages.1.content.1.tool.definition.name", original: "read_file"},
				{path: "messages.1.content.2.tool.definition.mcp_server_name", original: "calendar"},
			},
		},
		{
			name:     "malformed tool changes ignored",
			messages: `[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_addition"},{"type":"tool_addition","tool":"lookup_notes"},{"type":"tool_addition","tool":{"name":"lookup_notes"}},{"type":"tool_addition","tool":{"type":"tool_reference","tool_name":"lookup_notes"}},{"type":"tool_removal","tool":{"type":"tool_definition","definition":{"name":"lookup_notes"}}},{"type":"tool_addition","tool":{"type":"tool_reference","name":""}}]}]`,
			checks: []nameCheck{
				{path: "messages.1.content.1.tool", original: "lookup_notes"},
				{path: "messages.1.content.2.tool.name", original: "lookup_notes"},
				{path: "messages.1.content.3.tool.tool_name", original: "lookup_notes"},
				{path: "messages.1.content.4.tool.definition.name", original: "lookup_notes"},
				{path: "messages.1.content.5.tool.name", original: ""},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"claude-opus-5-5","max_tokens":64,"tools":` + tools + `,"messages":` + test.messages + `}`)
			options := claudeMCPAliasOptions{secret: "tool-change-caller"}
			legacyBody, legacyReverseMap := remapOAuthToolNamesWithOptionsLegacy(body, options)
			batchedBody, batchedReverseMap, ok := remapOAuthToolNamesWithBatchedEdits(body, options)
			if !ok {
				t.Fatal("batched remap unexpectedly rejected valid JSON offsets")
			}
			if !bytes.Equal(batchedBody, legacyBody) || !maps.Equal(batchedReverseMap, legacyReverseMap) {
				t.Fatalf("batched result differs from legacy\n got: %s %v\nwant: %s %v", batchedBody, batchedReverseMap, legacyBody, legacyReverseMap)
			}

			for _, variant := range []struct {
				name       string
				body       []byte
				reverseMap map[string]string
			}{
				{name: "batched", body: batchedBody, reverseMap: batchedReverseMap},
				{name: "legacy", body: legacyBody, reverseMap: legacyReverseMap},
			} {
				declared := make(map[string]string)
				gjson.GetBytes(variant.body, "tools").ForEach(func(index, tool gjson.Result) bool {
					original := gjson.Get(tools, fmt.Sprintf("%d.name", index.Int())).String()
					declared[original] = tool.Get("name").String()
					return true
				})
				for _, check := range test.checks {
					want := check.original
					if check.aliased {
						want = declared[check.original]
						if want == check.original || !helps.IsClaudeMCPToolName(want) {
							t.Fatalf("%s: tools[] name for %q = %q, want an MCP alias", variant.name, check.original, want)
						}
						if got := variant.reverseMap[want]; got != check.original {
							t.Fatalf("%s: reverseMap[%q] = %q, want %q", variant.name, want, got, check.original)
						}
					}
					if got := gjson.GetBytes(variant.body, check.path).String(); got != want {
						t.Fatalf("%s: %s = %q, want %q", variant.name, check.path, got, want)
					}
				}
				if got := gjson.GetBytes(variant.body, "tools.2.name").String(); got != "web_search" {
					t.Fatalf("%s: server tool name = %q, want web_search", variant.name, got)
				}
				if got := gjson.GetBytes(variant.body, "tools.3.name").String(); got != "mcp__context7__query-docs" {
					t.Fatalf("%s: caller MCP tool name = %q, want unchanged", variant.name, got)
				}
			}
		})
	}
}

func reverseMapKeyFor(m map[string]string, val string) string {
	for k, v := range m {
		if v == val && k != val {
			return k
		}
	}
	return ""
}

func TestReverseRemapOAuthToolNamesRestoresHybridPassthroughMCPTools(t *testing.T) {
	const virtual = "mcp__ripple_middle__"
	reverseMap := map[string]string{
		virtual + "blanket_Bash":       "Bash",
		virtual + "brand_Read":         "Read",
		"mcp__acme__link_pull_request": "mcp__acme__link_pull_request",
		"mcp__acme__list_threads":      "mcp__acme__list_threads",
	}

	testCases := []struct {
		name       string
		hybridName string
		wantName   string
	}{
		{
			name:       "model replaced server with virtual prefix",
			hybridName: "mcp__ripple_middle__link_pull_request",
			wantName:   "mcp__acme__link_pull_request",
		},
		{
			name:       "model prepended virtual prefix to full passthrough name",
			hybridName: "mcp__ripple_middle__acme__link_pull_request",
			wantName:   "mcp__acme__link_pull_request",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Non-stream response
			resp := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, tc.hybridName))
			restored, err := restoreClaudeOAuthToolNamesFromResponse(resp, reverseMap)
			if err != nil {
				t.Fatalf("restoreClaudeOAuthToolNamesFromResponse() error = %v, want %q", err, tc.wantName)
			}
			if got := gjson.GetBytes(restored, "content.0.name").String(); got != tc.wantName {
				t.Fatalf("content.0.name = %q, want %q", got, tc.wantName)
			}

			// Streaming response
			line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, tc.hybridName))
			restoredLine, errStream := restoreClaudeOAuthToolNamesFromStreamLine(line, reverseMap)
			if errStream != nil {
				t.Fatalf("restoreClaudeOAuthToolNamesFromStreamLine() error = %v, want %q", errStream, tc.wantName)
			}
			if got := gjson.GetBytes(helps.JSONPayload(restoredLine), "content_block.name").String(); got != tc.wantName {
				t.Fatalf("content_block.name = %q, want %q", got, tc.wantName)
			}
		})
	}
}

func TestReverseRemapOAuthToolNamesHybridPassthroughPrecedenceAndAmbiguity(t *testing.T) {
	const virtual = "mcp__ripple_middle__"

	t.Run("client tool takes precedence over passthrough with same suffix", func(t *testing.T) {
		reverseMap := map[string]string{
			virtual + "blanket_Bash": "Bash",
			"mcp__shell__Bash":       "mcp__shell__Bash",
		}
		// mcp__<virtual>__Bash is a semantic suffix match for Bash, should restore to Bash, NOT mcp__shell__Bash
		resp := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, virtual+"Bash"))
		restored, err := restoreClaudeOAuthToolNamesFromResponse(resp, reverseMap)
		if err != nil {
			t.Fatalf("restoreClaudeOAuthToolNamesFromResponse() error = %v", err)
		}
		if got := gjson.GetBytes(restored, "content.0.name").String(); got != "Bash" {
			t.Fatalf("content.0.name = %q, want %q", got, "Bash")
		}
	})

	t.Run("ambiguous passthrough tool rejected", func(t *testing.T) {
		reverseMap := map[string]string{
			virtual + "blanket_other": "other",
			"mcp__srv1__query":        "mcp__srv1__query",
			"mcp__srv2__query":        "mcp__srv2__query",
		}
		// mcp__<virtual>__query matches both srv1 and srv2, cannot disambiguate
		resp := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, virtual+"query"))
		_, err := restoreClaudeOAuthToolNamesFromResponse(resp, reverseMap)
		if err == nil {
			t.Fatal("restoreClaudeOAuthToolNamesFromResponse() expected error for ambiguous passthrough, got nil")
		}
	})
}

func TestReverseRemapOAuthToolNames_UndeclaredToolFailsOpen(t *testing.T) {
	// Under Claude OAuth cloaking, when the model generates an undeclared tool name
	// under the request's virtual server prefix (e.g. hallucinated or misspelled tool),
	// reverseRemapOAuthToolNames and reverseRemapOAuthToolNamesFromStreamLine must fail OPEN:
	// forwarding the tool name unchanged without error so the stream is not aborted.
	body := []byte(`{"tools":[{"name":"terminal","input_schema":{"type":"object"}}]}`)
	remapped, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "undeclared-tool-caller"})
	terminalAlias := gjson.GetBytes(remapped, "tools.0.name").String()
	parts, ok := parseClaudeMCPAlias(terminalAlias)
	if !ok {
		t.Fatalf("parseClaudeMCPAlias(%q) failed", terminalAlias)
	}

	// Model hallucinated an undeclared tool name under the same virtual server
	undeclaredToolName := "mcp__" + parts.server + "__tiny_terminap"

	// 1. Non-stream response
	resp := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, undeclaredToolName))
	restored, err := restoreClaudeOAuthToolNamesFromResponse(resp, reverseMap)
	if err != nil {
		t.Fatalf("restoreClaudeOAuthToolNamesFromResponse() error = %v, want fail-open nil", err)
	}
	if got := gjson.GetBytes(restored, "content.0.name").String(); got != undeclaredToolName {
		t.Fatalf("restored tool name = %q, want %q forwarded unchanged", got, undeclaredToolName)
	}

	// 2. Stream SSE line
	line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, undeclaredToolName))
	restoredLine, errStream := restoreClaudeOAuthToolNamesFromStreamLine(line, reverseMap)
	if errStream != nil {
		t.Fatalf("restoreClaudeOAuthToolNamesFromStreamLine() error = %v, want fail-open nil", errStream)
	}
	if !bytes.Equal(restoredLine, line) {
		t.Fatalf("restoredLine = %s, want %s forwarded unchanged", string(restoredLine), string(line))
	}
}
