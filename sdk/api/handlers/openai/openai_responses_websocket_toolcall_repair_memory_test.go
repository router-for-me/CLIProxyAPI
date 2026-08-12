package openai

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/tidwall/gjson"
)

const responsesWebsocketLargeTranscriptSize = 8 << 20

var responsesWebsocketToolCacheBenchmarkSink *responsesWebsocketToolCacheTurn

func TestResponsesWebsocketToolCacheTurnDetachesCallIDFromInput(t *testing.T) {
	for _, tt := range []struct {
		name      string
		item      string
		storedIDs func(*responsesWebsocketToolCacheTurn) []string
	}{
		{
			name: "function call",
			item: `{"type":"function_call","call_id":"call-large-transcript","name":"lookup","arguments":"{}"}`,
			storedIDs: func(turn *responsesWebsocketToolCacheTurn) []string {
				return turn.callOrder
			},
		},
		{
			name: "function call output",
			item: `{"type":"function_call_output","call_id":"call-large-transcript","output":"done"}`,
			storedIDs: func(turn *responsesWebsocketToolCacheTurn) []string {
				return turn.outputOrder
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(`{"input":[{"type":"message","role":"user","content":"` + strings.Repeat("x", responsesWebsocketLargeTranscriptSize) + `"},` + tt.item + `]}`)
			input := gjson.GetBytes(payload, "input")
			items := input.Array()
			if len(items) != 2 {
				t.Fatalf("input item count = %d, want 2", len(items))
			}

			turn := newResponsesWebsocketToolCacheTurn("large-transcript-session")
			turn.recordItem(items[1])
			storedIDs := tt.storedIDs(turn)
			if len(storedIDs) != 1 {
				t.Fatalf("stored call IDs = %d, want 1", len(storedIDs))
			}
			if stringAliases(storedIDs[0], input.Raw) {
				t.Fatal("stored call_id retains the complete parsed input allocation")
			}
		})
	}
}

func TestResponsesWebsocketToolCacheTurnOwnsRecordedDataAfterRequestReturns(t *testing.T) {
	payload := []byte(`{"input":[{"type":"message","role":"user","content":"context"},{"type":"function_call_output","call_id":"call-owned","output":"done"}]}`)
	turn := newResponsesWebsocketToolCacheTurn("owned-data-session")
	turn.recordRequest(payload)

	for index := range payload {
		payload[index] = 'x'
	}

	if len(turn.outputOrder) != 1 || turn.outputOrder[0] != "call-owned" {
		t.Fatalf("stored call IDs = %v, want [call-owned]", turn.outputOrder)
	}
	stored := turn.outputs["call-owned"]
	if got := gjson.GetBytes(stored, "output").String(); got != "done" {
		t.Fatalf("stored output = %q, want done; item = %s", got, stored)
	}
}

func BenchmarkResponsesWebsocketToolCacheTurnRecordLargeRequest(b *testing.B) {
	payload := []byte(`{"input":[{"type":"message","role":"user","content":"` + strings.Repeat("x", responsesWebsocketLargeTranscriptSize) + `"},{"type":"function_call_output","call_id":"call-large-transcript","output":"done"}]}`)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		turn := newResponsesWebsocketToolCacheTurn("large-transcript-session")
		turn.recordRequest(payload)
		responsesWebsocketToolCacheBenchmarkSink = turn
	}
}

func stringAliases(value string, backing string) bool {
	if value == "" || backing == "" {
		return false
	}
	valueStart := uintptr(unsafe.Pointer(unsafe.StringData(value)))
	backingStart := uintptr(unsafe.Pointer(unsafe.StringData(backing)))
	return valueStart >= backingStart && valueStart < backingStart+uintptr(len(backing))
}
