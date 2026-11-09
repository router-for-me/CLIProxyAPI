package payloadcompress

import (
	"strings"
	"testing"
)

func agingBody() []byte {
	long := strings.Repeat("this is a long old message that should shrink once it is aged by the compressor ", 4)
	return []byte(`{"messages":[` +
		`{"role":"system","content":"SYSTEM PROMPT MUST STAY"},` +
		`{"role":"user","content":"` + long + `"},` +
		`{"role":"assistant","content":"` + long + `"},` +
		`{"role":"user","content":"` + long + `"},` +
		`{"role":"assistant","content":"` + long + `"},` +
		`{"role":"user","content":"Hi there! I was wondering if you could please basically fix the bug in the parser. It seems like the tokenizer is probably broken when it receives an empty input buffer, and this sentence keeps going so that the message is long enough to compress."},` +
		`{"role":"assistant","content":"Sure, I can basically help with that parser fix right now because it seems like a simple thing to do, and I will keep explaining it at length so the message is long enough to compress."},` +
		`{"role":"user","content":"recent question"},` +
		`{"role":"assistant","content":"recent answer"},` +
		`{"role":"user","content":"latest question"}]}`)
}

func TestProgressiveAgingTiers(t *testing.T) {
	cfg := testConfigWith(StepOptions{}, EngineProgressiveAging)
	out, stats := Apply(cfg, "gpt-4o", agingBody())
	text := string(out)
	if !stats.Applied {
		t.Fatalf("aging did not apply: %+v", stats)
	}
	if !strings.Contains(text, "SYSTEM PROMPT MUST STAY") {
		t.Fatalf("system prompt rewritten: %s", text)
	}
	if !strings.Contains(text, "latest question") || !strings.Contains(text, "recent question") {
		t.Fatalf("recent turns rewritten: %s", text)
	}
	if !strings.Contains(text, "[COMPRESSED:aging:fullSummary]") {
		t.Fatalf("old turn not summarized: %s", text)
	}
	if !strings.Contains(text, "[COMPRESSED:aging:moderate]") {
		t.Fatalf("moderate turn not compressed: %s", text)
	}
}

func TestProgressiveAgingIdempotent(t *testing.T) {
	cfg := testConfigWith(StepOptions{}, EngineProgressiveAging)
	once, _ := Apply(cfg, "gpt-4o", agingBody())
	twice, _ := Apply(cfg, "gpt-4o", once)
	if string(once) != string(twice) {
		t.Fatalf("not idempotent:\n%s\n%s", once, twice)
	}
}

func TestProgressiveAgingKeepsStructuredContent(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"user","content":"plain old question with several words in it for aging"},` +
		`{"role":"assistant","content":"{\"a\":1,\"b\":2}"},` +
		`{"role":"user","content":"recent"},` +
		`{"role":"assistant","content":"recent answer"},` +
		`{"role":"user","content":"latest"}]}`)
	cfg := testConfigWith(StepOptions{}, EngineProgressiveAging)
	out, _ := Apply(cfg, "gpt-4o", body)
	if !strings.Contains(string(out), `{\"a\":1,\"b\":2}`) && !strings.Contains(string(out), `{"a":1,"b":2}`) {
		t.Fatalf("json content rewritten: %s", out)
	}
}

func TestProgressiveAgingOnlyChatShape(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":"hi"}]}`)
	cfg := testConfigWith(StepOptions{}, EngineProgressiveAging)
	out, stats := Apply(cfg, "gpt-4o", body)
	if string(out) != string(body) || stats.Applied {
		t.Fatalf("non-chat body changed: %s", out)
	}
}
