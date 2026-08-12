package openai

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/tidwall/gjson"
)

const responsesWebsocketLargeRequestTranscriptSize = 8 << 20

var responsesWebsocketNormalizedRequestBenchmarkSink []byte

func TestDedupeFunctionCallsByCallIDReusesInputWithoutDuplicates(t *testing.T) {
	raw := `[{"type":"message","id":"msg-1","role":"user","content":"hello"},{"type":"function_call","id":"fc-1","call_id":"call-1","name":"lookup","arguments":"{}"}]`
	deduped, errDedupe := dedupeFunctionCallsByCallID(raw)
	if errDedupe != nil {
		t.Fatalf("dedupeFunctionCallsByCallID() error = %v", errDedupe)
	}
	if deduped != raw {
		t.Fatalf("deduped input changed without duplicates:\n got: %s\nwant: %s", deduped, raw)
	}
	if unsafe.StringData(deduped) != unsafe.StringData(raw) {
		t.Fatal("dedupeFunctionCallsByCallID copied an unchanged transcript")
	}
}

func TestDedupeInputItemsByIDReusesInputWithoutDuplicates(t *testing.T) {
	raw := `[{"type":"message","id":"msg-1","role":"user","content":"hello"},{"type":"function_call","id":"fc-1","call_id":"call-1","name":"lookup","arguments":"{}"},{"type":"function_call_output","id":"fco-1","call_id":"call-1","output":"done"}]`
	deduped, errDedupe := dedupeInputItemsByID(raw)
	if errDedupe != nil {
		t.Fatalf("dedupeInputItemsByID() error = %v", errDedupe)
	}
	if deduped != raw {
		t.Fatalf("deduped input changed without duplicates:\n got: %s\nwant: %s", deduped, raw)
	}
	if unsafe.StringData(deduped) != unsafe.StringData(raw) {
		t.Fatal("dedupeInputItemsByID copied an unchanged transcript")
	}
}

func TestDedupeFunctionCallsByCallIDKeepsFirstDuplicate(t *testing.T) {
	raw := `[{"type":"function_call","id":"fc-first","call_id":"call-1","name":"first","arguments":"{}"},{"type":"message","id":"msg-1","role":"assistant","content":"working"},{"type":"function_call","id":"fc-second","call_id":"call-1","name":"second","arguments":"{}"},{"type":"function_call_output","id":"fco-1","call_id":"call-1","output":"done"}]`
	deduped, errDedupe := dedupeFunctionCallsByCallID(raw)
	if errDedupe != nil {
		t.Fatalf("dedupeFunctionCallsByCallID() error = %v", errDedupe)
	}
	items := gjson.Parse(deduped).Array()
	if len(items) != 3 {
		t.Fatalf("deduped item count = %d, want 3: %s", len(items), deduped)
	}
	if got := items[0].Get("id").String(); got != "fc-first" {
		t.Fatalf("retained function call = %q, want fc-first", got)
	}
	if got := items[1].Get("id").String(); got != "msg-1" {
		t.Fatalf("middle item = %q, want msg-1", got)
	}
	if got := items[2].Get("id").String(); got != "fco-1" {
		t.Fatalf("output item = %q, want fco-1", got)
	}
}

func TestMergeJSONArraysRawPreservesOrderAndValues(t *testing.T) {
	merged, invalidPart, errMerge := mergeJSONArraysRaw(
		` [ {"type":"message", "content":"<tag>", "nested":[1,2]} ] `,
		`null`,
		`[true,"escaped\nvalue",null,{"id":"last"}]`,
		"",
	)
	if errMerge != nil {
		t.Fatalf("mergeJSONArraysRaw() error = %v", errMerge)
	}
	if invalidPart != -1 {
		t.Fatalf("invalid part = %d, want -1", invalidPart)
	}
	items := gjson.Parse(merged).Array()
	if len(items) != 5 {
		t.Fatalf("merged item count = %d, want 5: %s", len(items), merged)
	}
	if got := items[0].Get("content").String(); got != "<tag>" {
		t.Fatalf("first content = %q, want <tag>", got)
	}
	if got := items[0].Get("nested.1").Int(); got != 2 {
		t.Fatalf("nested value = %d, want 2", got)
	}
	if !items[1].Bool() || items[2].String() != "escaped\nvalue" || items[3].Type != gjson.Null || items[4].Get("id").String() != "last" {
		t.Fatalf("merged order or values changed: %s", merged)
	}
}

func TestMergeJSONArraysRawReportsInvalidPart(t *testing.T) {
	for _, tt := range []struct {
		name        string
		parts       []string
		invalidPart int
	}{
		{name: "first part is object", parts: []string{`{"id":"not-array"}`, `[]`, `[]`}, invalidPart: 0},
		{name: "last part malformed", parts: []string{`[]`, `[]`, `[{"id":`}, invalidPart: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, invalidPart, errMerge := mergeJSONArraysRaw(tt.parts...)
			if errMerge == nil {
				t.Fatal("mergeJSONArraysRaw() error = nil, want validation error")
			}
			if invalidPart != tt.invalidPart {
				t.Fatalf("invalid part = %d, want %d", invalidPart, tt.invalidPart)
			}
		})
	}
}

func BenchmarkNormalizeResponseSubsequentRequestLargeTranscript(b *testing.B) {
	lastRequest := []byte(`{"model":"gpt-5.4","instructions":"You are a coding agent.","stream":true,"input":[{"type":"message","id":"msg-large","role":"user","content":"` + strings.Repeat("x", responsesWebsocketLargeRequestTranscriptSize) + `"}]}`)
	lastResponseOutput := []byte(`[{"type":"message","id":"msg-assistant","role":"assistant","content":[{"type":"output_text","text":"ready"}]},{"type":"function_call","id":"fc-1","call_id":"call-1","name":"lookup","arguments":"{}"}]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call_output","id":"fco-1","call_id":"call-1","output":"done"}]}`)

	normalized, _, errMessage := normalizeResponseSubsequentRequest(raw, lastRequest, lastResponseOutput, "", nil, false, false)
	if errMessage != nil {
		b.Fatalf("normalizeResponseSubsequentRequest() error = %v", errMessage.Error)
	}
	if !strings.Contains(string(normalized), `"id":"msg-large"`) || !strings.Contains(string(normalized), `"id":"fco-1"`) {
		b.Fatalf("normalized request lost transcript items")
	}

	b.SetBytes(int64(len(lastRequest) + len(lastResponseOutput) + len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		normalized, _, errMessage = normalizeResponseSubsequentRequest(raw, lastRequest, lastResponseOutput, "", nil, false, false)
		if errMessage != nil {
			b.Fatalf("normalizeResponseSubsequentRequest() error = %v", errMessage.Error)
		}
		responsesWebsocketNormalizedRequestBenchmarkSink = normalized
	}
}
