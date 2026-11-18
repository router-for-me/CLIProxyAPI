package claude

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func perfCodexClaudeRequest(tools []string) []byte {
	var toolJSON []string
	for _, name := range tools {
		toolJSON = append(toolJSON, fmt.Sprintf(`{"name":%q,"description":"d","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}`, name))
	}
	history := strings.Repeat("context ", 81920)
	return []byte(fmt.Sprintf(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":%q}],"tools":[%s]}`,
		history, strings.Join(toolJSON, ",")))
}

func perfCodexEvents(names []string) [][]byte {
	events := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp-1","model":"gpt-5"}}`),
	}
	for i, name := range names {
		events = append(events,
			[]byte(fmt.Sprintf(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_%d","name":%q,"status":"in_progress"},"output_index":%d}`, i, name, i+1)),
			[]byte(fmt.Sprintf(`data: {"type":"response.function_call_arguments.delta","item_id":"fc_%d","call_id":"call_%d","delta":"{\"path\":\"/tmp/x\"}","output_index":%d}`, i, i, i+1)),
			[]byte(fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_%d","name":%q,"arguments":"{\"path\":\"/tmp/x\"}"},"output_index":%d}`, i, name, i+1)),
		)
	}
	events = append(events, []byte(`data: {"type":"response.completed","response":{"id":"resp-1","model":"gpt-5","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`))
	return events
}

func runPerfCodexStream(request []byte, upstreamNames []string) []byte {
	var param any
	var out []byte
	for _, event := range perfCodexEvents(upstreamNames) {
		out = append(out, bytes.Join(ConvertCodexResponseToClaude(context.Background(), "", request, nil, event, &param), nil)...)
	}
	return out
}

// The per-stream cached resolver must return exactly what the uncached
// resolveCodexClaudeToolUseName returns for every request/name combination.
func TestCodexClaudeToolNameCacheEquivalence(t *testing.T) {
	longA := "mcp__very_long_server_name_for_testing__" + strings.Repeat("a", 40)
	longB := "mcp__very_long_server_name_for_testing__" + strings.Repeat("a", 39) + "b"
	shortOf := buildShortNameMap([]string{longA, longB, "Read"})
	cases := []struct {
		name     string
		request  []byte
		upstream []string
	}{
		{"nil-request", nil, []string{"Read", "unknown"}},
		{"malformed", []byte("{"), []string{"Read"}},
		{"no-tools", []byte(`{"messages":[]}`), []string{"Read"}},
		{"empty-tools", perfCodexClaudeRequest(nil), []string{"Read"}},
		{"short-names", perfCodexClaudeRequest([]string{"Read", "Write"}), []string{"Read", "Write", "read"}},
		{"long-names", perfCodexClaudeRequest([]string{longA, longB, "Read"}), []string{shortOf[longA], shortOf[longB], shortOf["Read"], longA}},
		{"duplicate-declared", perfCodexClaudeRequest([]string{"Read", "Read"}), []string{"Read"}},
		{"web-search", perfCodexClaudeRequest([]string{"web_search"}), []string{"web_search"}},
	}
	for _, tc := range cases {
		// The full stream through one param must be deterministic and exercise
		// the cache across multiple tool calls.
		first := runPerfCodexStream(tc.request, tc.upstream)
		second := runPerfCodexStream(tc.request, tc.upstream)
		if !bytes.Equal(first, second) {
			t.Fatalf("%s: nondeterministic output", tc.name)
		}
		params := &ConvertCodexResponseToClaudeParams{}
		for _, name := range tc.upstream {
			got := params.resolveToolUseName(tc.request, name)
			want := resolveCodexClaudeToolUseName(tc.request, name)
			if got != want {
				t.Errorf("%s: resolve(%q) = %q, want %q", tc.name, name, got, want)
			}
		}
		if !params.ToolNamesResolved {
			t.Errorf("%s: cache not marked resolved", tc.name)
		}
	}
}

func BenchmarkCodexClaudeLongRequestToolCalls(b *testing.B) {
	longA := "mcp__very_long_server_name_for_testing__" + strings.Repeat("a", 40)
	longB := "mcp__very_long_server_name_for_testing__" + strings.Repeat("a", 39) + "b"
	shortOf := buildShortNameMap([]string{longA, longB, "Read", "Write"})
	request := perfCodexClaudeRequest([]string{longA, longB, "Read", "Write"})
	names := []string{shortOf[longA], shortOf[longB], "Read", "Write", shortOf[longA], shortOf[longB], "Read", "Write"}
	events := perfCodexEvents(names)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var param any
		for _, event := range events {
			ConvertCodexResponseToClaude(context.Background(), "", request, nil, event, &param)
		}
	}
}
