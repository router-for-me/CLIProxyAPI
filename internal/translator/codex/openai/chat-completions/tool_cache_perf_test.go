package chat_completions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func tcHash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func tcRequest(historyCalls int) []byte {
	longName := "mcp__very_long_server_name_for_testing__" + strings.Repeat("a", 40)
	var msgs []string
	msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":%q}`, strings.Repeat("context ", 4096)))
	for i := 0; i < historyCalls; i++ {
		msgs = append(msgs, fmt.Sprintf(`{"role":"assistant","tool_calls":[{"id":"h%d","type":"function","function":{"name":%q,"arguments":"{}"}}]}`, i, longName))
		msgs = append(msgs, fmt.Sprintf(`{"role":"tool","tool_call_id":"h%d","content":"ok"}`, i))
	}
	return []byte(fmt.Sprintf(`{"model":"gpt-5","messages":[%s],"tools":[{"type":"function","function":{"name":%q}},{"type":"function","function":{"name":"plain"}},{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}},{"type":"function","function":{"name":"apply_patch"}}]}`,
		strings.Join(msgs, ","), longName))
}

func tcStreamEvents(shortLong string) [][]byte {
	return [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp-1","created_at":1700000000,"model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"hi"}`),
		[]byte(fmt.Sprintf(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","name":%q,"status":"in_progress"}}`, shortLong)),
		[]byte(`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_1","delta":"{\"path\":\"/a\"}"}`),
		[]byte(fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":%q,"arguments":"{\"path\":\"/a\"}","status":"completed"}}`, shortLong)),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_2","name":"plain","arguments":"{}","status":"completed"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","call_id":"call_3","name":"apply_patch","status":"in_progress"}}`),
		[]byte(`data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_3","call_id":"call_3","delta":"*** Begin Patch\n*** End Patch"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"call_3","name":"apply_patch","input":"*** Begin Patch\n*** End Patch","status":"completed"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_4","name":"unknown_tool","status":"in_progress"}}`),
		[]byte(`data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`),
	}
}

func tcCases() []struct {
	name string
	req  []byte
} {
	longName := "mcp__very_long_server_name_for_testing__" + strings.Repeat("a", 40)
	_ = longName
	return []struct {
		name string
		req  []byte
	}{
		{"nil", nil},
		{"malformed", []byte("{")},
		{"no-tools", []byte(`{"messages":[]}`)},
		{"full-history", tcRequest(6)},
		{"tools-object", []byte(`{"messages":[],"tools":{"type":"function","function":{"name":"apply_patch"}}}`)},
		{"custom-only", []byte(`{"messages":[],"tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}`)},
	}
}

// Golden hashes captured from the pre-optimization implementation; the
// per-stream tool metadata cache must keep stream and non-stream outputs
// byte-identical.
var codexChatGoldenHashes = map[string][2]string{
	"nil":          {"c5fffafcda772cf06a22d1fbf63663c423973cd09e26948a97120e03277cae3a", "d49a496d7bd81d73a3f5fc3e2b181518b4d012fe87d26adb630f168cc4e727ab"},
	"malformed":    {"c5fffafcda772cf06a22d1fbf63663c423973cd09e26948a97120e03277cae3a", "d49a496d7bd81d73a3f5fc3e2b181518b4d012fe87d26adb630f168cc4e727ab"},
	"no-tools":     {"c5fffafcda772cf06a22d1fbf63663c423973cd09e26948a97120e03277cae3a", "d49a496d7bd81d73a3f5fc3e2b181518b4d012fe87d26adb630f168cc4e727ab"},
	"full-history": {"500905685845bbe25e41bc9db2b432373562465b93ec187aee65abc3364f9241", "a958f138b0148820b97337ba7b21939c85cc48670d4032de05c24efee38d978f"},
	"tools-object": {"c5fffafcda772cf06a22d1fbf63663c423973cd09e26948a97120e03277cae3a", "d49a496d7bd81d73a3f5fc3e2b181518b4d012fe87d26adb630f168cc4e727ab"},
	"custom-only":  {"2db116761244e4f098550a2c0de5dfbe46ddee8c5f186c58b6acea6f3e23f320", "c1dbbc9730e2168bdb6bb8b4c20388d67ed9f2e4e67429e267df230f0720a0fa"},
}

func TestCodexChatGoldenOutputs(t *testing.T) {
	longName := "mcp__very_long_server_name_for_testing__" + strings.Repeat("a", 40)
	shortLong := buildShortNameMap(collectRequestToolNames(tcRequest(6)))[longName]
	events := tcStreamEvents(shortLong)
	nonStream := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-1","object":"response","created_at":1700000000,"model":"gpt-5","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]},{"type":"function_call","call_id":"call_1","name":%q,"arguments":"{\"path\":\"/a\"}","status":"completed"},{"type":"custom_tool_call","call_id":"call_3","name":"apply_patch","input":"*** Begin Patch\n*** End Patch","status":"completed"}],"usage":{"input_tokens":10,"output_tokens":5}}}`, shortLong))
	for _, tc := range tcCases() {
		run := func() []byte {
			var param any
			var out []byte
			for _, e := range events {
				out = append(out, bytes.Join(ConvertCodexResponseToOpenAI(context.Background(), "gpt-5", tc.req, tc.req, e, &param), nil)...)
			}
			return out
		}
		first, second := run(), run()
		if !bytes.Equal(first, second) {
			t.Fatalf("%s: nondeterministic", tc.name)
		}
		var p any
		non := ConvertCodexResponseToOpenAINonStream(context.Background(), "gpt-5", tc.req, tc.req, nonStream, &p)
		want := codexChatGoldenHashes[tc.name]
		if got := tcHash(first); got != want[0] {
			t.Errorf("%s stream: hash %s, want %s", tc.name, got, want[0])
		}
		if got := tcHash(non); got != want[1] {
			t.Errorf("%s nonstream: hash %s, want %s", tc.name, got, want[1])
		}
	}
}

func BenchmarkCodexChatToolStreamLongHistory(b *testing.B) {
	longName := "mcp__very_long_server_name_for_testing__" + strings.Repeat("a", 40)
	request := tcRequest(60)
	shortLong := buildShortNameMap(collectRequestToolNames(request))[longName]
	events := tcStreamEvents(shortLong)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var param any
		for _, e := range events {
			ConvertCodexResponseToOpenAI(context.Background(), "gpt-5", request, request, e, &param)
		}
	}
}

// The cached metadata must answer exactly like the legacy per-call rescans.
func TestCodexChatToolMetadataEquivalence(t *testing.T) {
	// Answered via the randomized differential below.
	requests := [][]byte{
		nil, []byte("{"), []byte(`{"messages":[]}`),
		tcRequest(3),
		[]byte(`{"messages":[],"tools":{"type":"function","function":{"name":"apply_patch"}}}`),
		[]byte(`{"messages":[],"tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}`),
		[]byte(`{"messages":[],"tools":[{"type":"function","function":{"name":"apply_patch"}},{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}`),
		[]byte(`{"messages":[],"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}]}`),
		[]byte(`{"messages":[],"tools":[{"type":"namespace","name":"editor","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}]}`),
	}
	names := []string{"", "plain", "apply_patch", "unknown", "editor__apply_patch"}
	items := []string{
		`{"type":"custom_tool_call","name":"apply_patch"}`,
		`{"type":"custom_tool_call","name":"apply_patch","namespace":"editor"}`,
		`{"type":"custom_tool_call","name":"other"}`,
		`{"type":"function_call","name":"apply_patch"}`,
	}
	for ri, req := range requests {
		var metadata codexChatToolMetadata
		rev := buildReverseMapFromOriginalOpenAI(req)
		for _, name := range names {
			want := name
			if orig, ok := rev[name]; ok {
				want = orig
			}
			if got := metadata.restoreToolName(req, name); got != want {
				t.Errorf("req %d restore(%q) = %q, want %q", ri, name, got, want)
			}
		}
		for _, itemJSON := range items {
			item := gjson.ParseBytes([]byte(itemJSON))
			if got, want := metadata.isCustomPatch(req, item), isOriginalCustomPatch(req, item); got != want {
				t.Errorf("req %d isCustomPatch(%s) = %v, want %v", ri, itemJSON, got, want)
			}
		}
	}
}
