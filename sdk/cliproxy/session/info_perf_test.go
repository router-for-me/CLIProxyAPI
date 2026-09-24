package session

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func BenchmarkExtractSessionInfoLargePayload(b *testing.B) {
	for _, size := range []int{1024, 1 << 20, 8 << 20} {
		for _, kind := range []string{"no_session", "codex_header", "nested_metadata"} {
			b.Run(fmt.Sprintf("%s/%d", kind, size), func(b *testing.B) {
				payload := []byte(`{"input":[{"role":"user","content":"` + strings.Repeat("x", size) + `"}]}`)
				var headers http.Header
				if kind == "codex_header" {
					headers = http.Header{"Session_id": {"session-test"}}
				}
				if kind == "nested_metadata" {
					payload = []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"` + strings.Repeat("x", size) + `"}]}],"metadata":{"session_id":"child","parent_session_id":"parent"}}}`)
				}
				b.ReportAllocs()
				b.SetBytes(int64(len(payload)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					ExtractSessionInfo(headers, payload, nil)
				}
			})
		}
	}
}
