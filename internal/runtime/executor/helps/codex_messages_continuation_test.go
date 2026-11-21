package helps

import (
	"strings"
	"testing"
)

func BenchmarkCodexMessagesTurnComplete(b *testing.B) {
	payload := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"probe","arguments":"{}"}],"padding":"` + strings.Repeat("x", 2<<20) + `"}}`)
	turn := &CodexMessagesTurn{}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if got := turn.Complete(payload); got.calls != 1 || got.id != "resp_1" {
			b.Fatal("tool completion snapshot not captured")
		}
	}
}
