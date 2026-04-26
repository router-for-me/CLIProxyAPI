package helps

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestParseOpenAIUsageChatCompletions(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 1 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 1)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.TotalTokens != 3 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 3)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
}

func TestParseOpenAIUsageResponses(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestUsageReporterBuildRecordIncludesLatency(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: time.Now().Add(-1500 * time.Millisecond),
	}

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Latency < time.Second {
		t.Fatalf("latency = %v, want >= 1s", record.Latency)
	}
	if record.Latency > 3*time.Second {
		t.Fatalf("latency = %v, want <= 3s", record.Latency)
	}
}

func TestEstimateUsageCountsRequestAndResponse(t *testing.T) {
	request := []byte(`{"messages":[{"role":"user","content":"Hello there"}]}`)
	response := []byte(`{"choices":[{"message":{"role":"assistant","content":"General Kenobi"}}]}`)

	detail := EstimateUsage("gpt-4o", request, response)
	if detail.InputTokens <= 0 {
		t.Fatalf("input tokens = %d, want > 0", detail.InputTokens)
	}
	if detail.OutputTokens <= 0 {
		t.Fatalf("output tokens = %d, want > 0", detail.OutputTokens)
	}
	if detail.TotalTokens != detail.InputTokens+detail.OutputTokens {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, detail.InputTokens+detail.OutputTokens)
	}
	if detail.ReasoningTokens != 0 {
		t.Fatalf("reasoning tokens = %d, want 0", detail.ReasoningTokens)
	}
	if detail.CachedTokens != 0 {
		t.Fatalf("cached tokens = %d, want 0", detail.CachedTokens)
	}
}

func TestExtractOutputTextProviderFormats(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{
			name: "openai chat",
			data: []byte(`{"choices":[{"message":{"role":"assistant","content":"OpenAI output"}}]}`),
			want: "OpenAI output",
		},
		{
			name: "claude message",
			data: []byte(`{"content":[{"type":"text","text":"Claude output"}]}`),
			want: "Claude output",
		},
		{
			name: "gemini candidate",
			data: []byte(`{"candidates":[{"content":{"parts":[{"text":"Gemini output"}]}}]}`),
			want: "Gemini output",
		},
		{
			name: "openai stream",
			data: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"stream \"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"output\"}}]}\n\n"),
			want: "stream output",
		},
		{
			name: "codex response",
			data: []byte(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"Codex output"}]}]}}`),
			want: "Codex output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractOutputText(tt.data); got != tt.want {
				t.Fatalf("ExtractOutputText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEstimateUsageEmptyResponseOutput(t *testing.T) {
	request := []byte(`{"messages":[{"role":"user","content":"Hello there"}]}`)
	response := []byte(`{"choices":[{"message":{"role":"assistant","content":""}}]}`)

	detail := EstimateUsage("gpt-4o", request, response)
	if detail.InputTokens <= 0 {
		t.Fatalf("input tokens = %d, want > 0", detail.InputTokens)
	}
	if detail.OutputTokens != 0 {
		t.Fatalf("output tokens = %d, want 0", detail.OutputTokens)
	}
	if detail.TotalTokens != detail.InputTokens {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, detail.InputTokens)
	}
}

func TestUsageReporterDoesNotEstimateWhenUsageExists(t *testing.T) {
	reporter := &UsageReporter{model: "gpt-4o"}
	reporter.captureRequestBody([]byte(`{"messages":[{"role":"user","content":"Hello there"}]}`))
	reporter.captureResponseChunk([]byte(`{"choices":[{"message":{"role":"assistant","content":"General Kenobi"}}]}`))

	detail := reporter.completeDetail(usage.Detail{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}, false)
	if detail.InputTokens != 1 || detail.OutputTokens != 2 || detail.TotalTokens != 3 {
		t.Fatalf("detail = %+v, want original usage values", detail)
	}
}
