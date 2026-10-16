package helps

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func legacyToolIDHistory(callCount int) []byte {
	var body strings.Builder
	body.WriteString(`{"messages":[{"role":"assistant","content":[`)
	for i := 0; i < callCount; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"type":"tool_use","id":"Bash:%d#abc","name":"Bash","input":{}}`, i)
	}
	body.WriteString(`]},{"role":"user","content":[`)
	text := strings.Repeat("x", 512)
	for i := 0; i < callCount; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"type":"tool_result","tool_use_id":"Bash:%d#abc","content":"%s"}`, i, text)
	}
	body.WriteString(`]}]}`)
	return []byte(body.String())
}

// Rewriting the entire transcript per field must fail this allocation bound.
func TestIssue6478_NormalizeLargeHistoryAllocation(t *testing.T) {
	const callCount = 5211
	body := legacyToolIDHistory(callCount)
	result := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			NormalizeClaudeToolCallIDs(body)
		}
	})
	t.Logf("%d ID fields, %d input bytes: %d allocated bytes/op, %d ns/op", 2*callCount, len(body), result.AllocedBytesPerOp(), result.NsPerOp())
	if allocated := result.AllocedBytesPerOp(); allocated > int64(len(body))*64 {
		t.Fatalf("history normalization allocated %d bytes for %d input bytes; repeated whole-payload copies exceed the linear allocation budget", allocated, len(body))
	}

	normalized := NormalizeClaudeToolCallIDs(body)
	uses := gjson.GetBytes(normalized, "messages.0.content").Array()
	results := gjson.GetBytes(normalized, "messages.1.content").Array()
	if len(uses) != callCount || len(results) != callCount {
		t.Fatalf("history lost calls/results: %d/%d, want %d", len(uses), len(results), callCount)
	}
	valid := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	seen := make(map[string]bool)
	for i, use := range uses {
		id := use.Get("id").String()
		if !valid.MatchString(id) || seen[id] || results[i].Get("tool_use_id").String() != id {
			t.Fatalf("invalid, colliding or mismatched call/result ID at %d: %q", i, id)
		}
		seen[id] = true
		if results[i].Get("content").String() != strings.Repeat("x", 512) {
			t.Fatalf("tool result content changed at %d", i)
		}
	}
}

func TestIssue6478_NormalizePreservesNonIDBytes(t *testing.T) {
	body := []byte(`{ "messages": [ { "role":"assistant", "content": [{"type":"text","text":"Bash:1#abc"}, {"type":"tool_use","id":"Bash\u003a1\u0023abc","name":"Bash","input":{"id":"Bash:1#abc"}}]}, {"role":"user","content":[{"type":"tool_result","tool_use_id":"Bash:1#abc","content":"Bash:1#abc"}]} ], "metadata": {"id":"Bash:1#abc"} }`)
	want := `{ "messages": [ { "role":"assistant", "content": [{"type":"text","text":"Bash:1#abc"}, {"type":"tool_use","id":"cpa_tid_v1_QmFzaDoxI2FiYw","name":"Bash","input":{"id":"Bash:1#abc"}}]}, {"role":"user","content":[{"type":"tool_result","tool_use_id":"cpa_tid_v1_QmFzaDoxI2FiYw","content":"Bash:1#abc"}]} ], "metadata": {"id":"Bash:1#abc"} }`
	if got := string(NormalizeClaudeToolCallIDs(body)); got != want {
		t.Fatalf("normalization must only replace ID value bytes:\ngot  %s\nwant %s", got, want)
	}
}

func BenchmarkNormalizeClaudeToolCallIDs(b *testing.B) {
	for _, fields := range []int{1000, 5000, 10422} {
		b.Run(fmt.Sprintf("IDs%d", fields), func(b *testing.B) {
			body := legacyToolIDHistory(fields / 2)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				NormalizeClaudeToolCallIDs(body)
			}
		})
	}
}
