package helps

import (
	"fmt"
	"strings"
	"testing"
)

func TestJSONMayContainASCII(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		needles []string
		want    bool
	}{
		{"absent", `{"system":"hello"}`, []string{"naming a coding session"}, false},
		{"present", `{"system":"naming a coding session"}`, []string{"naming a coding session"}, true},
		{"any needle", `{"output_config":{}}`, []string{"naming", "output_config"}, true},
		{"escaped space falls back", `{"system":"naming a\u0020coding session"}`, []string{"naming a coding session"}, true},
		{"escaped letter falls back", `{"s":"n\u0061ming"}`, []string{"naming"}, true},
		{"upper hex falls back", `{"s":"\u004E"}`, []string{"N"}, true},
		{"escaped backslash is conservative", `{"s":"\\u0041"}`, []string{"zzz"}, true},
		{"control escape keeps shortcut", `{"s":"a\u001bbé"}`, []string{"naming"}, false},
		{"truncated escape", `{"s":"\u00`, []string{"naming"}, false},
		{"quoted value", `{"ttl":"1h"}`, []string{`"1h"`}, true},
		{"quoted value absent", `{"ttl":"5m","note":"11h"}`, []string{`"1h"`}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jsonMayContainASCII([]byte(tt.body), tt.needles...); got != tt.want {
				t.Fatalf("jsonMayContainASCII(%s, %q) = %v, want %v", tt.body, tt.needles, got, tt.want)
			}
		})
	}
}

// The pre-filters must never change an answer, including when the matched
// text is spelled with \u escapes.
func TestPrefilteredClassifiersKeepEscapedMatches(t *testing.T) {
	escapedTitle := `{"model":"m","system":[{"type":"text","text":"You are naming a\u0020coding session."}],` +
		`"messages":[{"role":"user","content":"hi"}]}`
	if !isClaudeTitleHelperRequest([]byte(escapedTitle)) {
		t.Fatal("escaped system title instruction must still be detected")
	}
	plainTitle := `{"model":"m","system":"Return a short title for this.","messages":[{"role":"user","content":"hi"}]}`
	if !isClaudeTitleHelperRequest([]byte(plainTitle)) {
		t.Fatal("plain system title instruction must be detected")
	}
	schemaTitle := `{"model":"m","output_config":{"format":{"schema":{"properties":{"title":{"type":"string"}}}}},` +
		`"messages":[{"role":"user","content":"<session>x</session>"}]}`
	if !isClaudeTitleHelperRequest([]byte(schemaTitle)) {
		t.Fatal("title schema request must be detected")
	}
	ordinary := `{"model":"m","system":"You are Claude Code.","messages":[{"role":"user","content":"Return a short answer"}]}`
	if isClaudeTitleHelperRequest([]byte(ordinary)) {
		t.Fatal("ordinary request must not be a title helper")
	}
	escaped1h := `{"messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral","ttl":"\u0031h"}}]}]}`
	if !ClaudePayloadHas1hTTL([]byte(escaped1h)) {
		t.Fatal("escaped 1h ttl must still be detected")
	}
	fiveMinutes := `{"messages":[{"role":"user","content":[{"type":"text","text":"took 11h","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`
	if ClaudePayloadHas1hTTL([]byte(fiveMinutes)) {
		t.Fatal("5m ttl must not count as 1h")
	}
}

// largeClaudeCodeBody builds a Claude Code style request with n tool round
// trips, about 2.2 KB each, ending with the top-level keys Claude Code sends
// after messages.
func largeClaudeCodeBody(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"claude-opus-5-5","messages":[`)
	output := strings.Repeat(`line of tool output with \"quotes\", tabs\t and a \u001b[0m reset `, 30)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"role":"assistant","content":[{"type":"tool_use","id":"toolu_%d","name":"Bash","input":{"command":"ls %d"}}]},`, i, i)
		fmt.Fprintf(&b, `{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%d","content":"%s"}]}`, i, output)
	}
	b.WriteString(`],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.286;"},`)
	b.WriteString(`{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral","ttl":"5m"}}],`)
	b.WriteString(`"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"metadata":{"user_id":"u"},`)
	b.WriteString(`"max_tokens":64000,"thinking":{"type":"adaptive"},"stream":true}`)
	return []byte(b.String())
}

func BenchmarkIsClaudeProbeOrHelperRequestLarge(b *testing.B) {
	body := largeClaudeCodeBody(1500)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if IsClaudeProbeOrHelperRequest(body) {
			b.Fatal("ordinary request classified as probe or helper")
		}
	}
}

func BenchmarkClaudePayloadHas1hTTLLarge(b *testing.B) {
	body := largeClaudeCodeBody(1500)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ClaudePayloadHas1hTTL(body) {
			b.Fatal("5m payload reported as 1h")
		}
	}
}

// The worst cases: a printable-ASCII escape forces the full walk, so the
// pre-filter's scan is pure overhead.
func BenchmarkClaudePayloadHas1hTTLLargeEscapeFallback(b *testing.B) {
	body := largeClaudeCodeBody(1500)
	body = append([]byte(`{"note":"\u0041",`), body[1:]...)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ClaudePayloadHas1hTTL(body) {
			b.Fatal("5m payload reported as 1h")
		}
	}
}

func BenchmarkClaudePayloadHas1hTTLLargeEscapeFallbackLate(b *testing.B) {
	body := largeClaudeCodeBody(1500)
	if len(body) > 1 && body[len(body)-1] == '}' {
		body = append(body[:len(body)-1], []byte(`,"late_escape":"\u0041"}`)...)
	}
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ClaudePayloadHas1hTTL(body) {
			b.Fatal("5m payload reported as 1h")
		}
	}
}
