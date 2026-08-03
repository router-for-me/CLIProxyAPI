package llmreqlog

import "testing"

func TestThinkStatsClaudeThinkingDelta(t *testing.T) {
	stats := &thinkStats{}
	stats.ingest([]byte("event: content_block_delta\n"))
	stats.ingest([]byte(`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"你好"}}` + "\n"))
	stats.ingest([]byte(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"答案"}}` + "\n"))
	stats.flush()
	has, length := stats.snapshot()
	if !has {
		t.Fatal("expected has think")
	}
	if length != 2 {
		t.Fatalf("think chars = %d, want 2", length)
	}
}

func TestThinkStatsIgnoresThoughtStyleUsageOnly(t *testing.T) {
	stats := &thinkStats{}
	stats.ingest([]byte(`data: {"usage":{"output_tokens":10,"completion_tokens_details":{"reasoning_tokens":9}}}` + "\n"))
	stats.ingest([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"x","thought":true}]}}]}` + "\n"))
	stats.flush()
	has, length := stats.snapshot()
	if has || length != 0 {
		t.Fatalf("thought/usage must not count as think: has=%v len=%d", has, length)
	}
}

func TestThinkStatsResponsesReasoningTextDelta(t *testing.T) {
	stats := &thinkStats{}
	stats.ingest([]byte(`data: {"type":"response.reasoning_text.delta","delta":"ab"}` + "\n"))
	stats.ingest([]byte(`data: {"type":"response.reasoning_summary_text.delta","delta":"cd"}` + "\n"))
	stats.ingest([]byte(`data: {"type":"response.reasoning_summary_text.done","text":"abcd"}` + "\n"))
	stats.flush()
	has, length := stats.snapshot()
	if !has {
		t.Fatal("expected has think")
	}
	if length != 4 {
		t.Fatalf("think chars = %d, want 4 (done text must not double-count)", length)
	}
}

func TestThinkStatsOpenAIReasoningContent(t *testing.T) {
	stats := &thinkStats{}
	stats.ingest([]byte(`data: {"choices":[{"delta":{"reasoning_content":"step1"}}]}` + "\n"))
	stats.ingest([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi","reasoning_content":"full"}}]}`))
	stats.flush()
	has, length := stats.snapshot()
	if !has {
		t.Fatal("expected has think")
	}
	want := int64(len([]rune("step1full")))
	if length != want {
		t.Fatalf("think chars = %d, want %d", length, want)
	}
}
