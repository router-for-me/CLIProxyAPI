package openai

import (
	"runtime"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const responsesWebsocketLargeResponseFrameSize = 8 << 20

var responsesWebsocketResponseToolCacheBenchmarkSink *responsesWebsocketToolCacheTurn

func TestResponsesWebsocketToolCacheTurnOwnsRecordedResponseData(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		payload func(string) []byte
	}{
		{
			name: "response completed",
			payload: func(padding string) []byte {
				return []byte(`{"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":"` + padding + `"},{"type":"function_call","call_id":"call-owned","name":"lookup","arguments":"{}"}]}}`)
			},
		},
		{
			name: "output item added",
			payload: func(padding string) []byte {
				return []byte(`{"type":"response.output_item.added","padding":"` + padding + `","item":{"type":"function_call","call_id":"call-owned","name":"lookup","arguments":"{}"}}`)
			},
		},
		{
			name: "output item done",
			payload: func(padding string) []byte {
				return []byte(`{"type":"response.output_item.done","padding":"` + padding + `","item":{"type":"function_call","call_id":"call-owned","name":"lookup","arguments":"{}"}}`)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			payload := testCase.payload(strings.Repeat("x", responsesWebsocketLargeResponseFrameSize))
			turn := newResponsesWebsocketToolCacheTurn("owned-response-data-session")
			turn.recordResponse(payload)

			for index := range payload {
				payload[index] = 'x'
			}

			var order []string
			var stored []byte
			if len(turn.callOrder) > 0 {
				order = turn.callOrder
				stored = turn.calls["call-owned"]
			} else {
				order = turn.outputOrder
				stored = turn.outputs["call-owned"]
			}
			if len(order) != 1 || order[0] != "call-owned" {
				t.Fatalf("stored call IDs = %v, want [call-owned]", order)
			}
			if got := gjson.GetBytes(stored, "call_id").String(); got != "call-owned" {
				t.Fatalf("stored call_id = %q, want call-owned; item=%s", got, stored)
			}
			if output := gjson.GetBytes(stored, "output"); output.Exists() && output.String() != "done" {
				t.Fatalf("stored output = %q, want done; item=%s", output.String(), stored)
			}
		})
	}
}

func TestResponsesWebsocketToolCacheTurnDoesNotRetainResponseBackingStorage(t *testing.T) {
	const (
		paddingSize     = 32 << 20
		maxRetainedHeap = 8 << 20
	)

	for _, testCase := range []struct {
		name   string
		prefix string
		suffix string
	}{
		{
			name:   "response completed",
			prefix: `{"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":"`,
			suffix: `"},{"type":"function_call","call_id":"call-retained","name":"lookup","arguments":"{}"}]}}`,
		},
		{
			name:   "output item added",
			prefix: `{"type":"response.output_item.added","padding":"`,
			suffix: `","item":{"type":"function_call","call_id":"call-retained","name":"lookup","arguments":"{}"}}`,
		},
		{
			name:   "output item done",
			prefix: `{"type":"response.output_item.done","padding":"`,
			suffix: `","item":{"type":"function_call","call_id":"call-retained","name":"lookup","arguments":"{}"}}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)

			payload := make([]byte, len(testCase.prefix)+paddingSize+len(testCase.suffix))
			copy(payload, testCase.prefix)
			for index := len(testCase.prefix); index < len(testCase.prefix)+paddingSize; index++ {
				payload[index] = 'x'
			}
			copy(payload[len(testCase.prefix)+paddingSize:], testCase.suffix)

			turn := newResponsesWebsocketToolCacheTurn("response-backing-storage-session")
			turn.recordResponse(payload)
			payload = nil
			runtime.GC()

			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			stored := turn.calls["call-retained"]
			if got := gjson.GetBytes(stored, "call_id").String(); got != "call-retained" {
				t.Fatalf("stored call_id = %q, want call-retained; item=%s", got, stored)
			}
			runtime.KeepAlive(turn)

			retainedHeap := int64(after.HeapAlloc) - int64(before.HeapAlloc)
			if retainedHeap > maxRetainedHeap {
				t.Fatalf("tool cache turn retained %d bytes after response release, want at most %d", retainedHeap, maxRetainedHeap)
			}
		})
	}
}

func BenchmarkResponsesWebsocketToolCacheTurnRecordLargeResponse(b *testing.B) {
	payload := []byte(`{"type":"response.output_item.done","padding":"` + strings.Repeat("x", responsesWebsocketLargeResponseFrameSize) + `","item":{"type":"function_call","call_id":"call-large-response","name":"lookup","arguments":"{}"}}`)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		turn := newResponsesWebsocketToolCacheTurn("large-response-session")
		turn.recordResponse(payload)
		responsesWebsocketResponseToolCacheBenchmarkSink = turn
	}
}
