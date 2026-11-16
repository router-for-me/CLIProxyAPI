package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestObservePluginExecutorStreamUsageKeepsTopLevelResponsesTokensWhenServiceTierPresent(t *testing.T) {
	t.Parallel()

	var buffer StreamUsageBuffer
	payload := []byte("data: {\"type\":\"response.completed\",\"service_tier\":\"default\",\"usage\":{\"input_tokens\":34,\"output_tokens\":499,\"total_tokens\":533}}\n\n")
	ObservePluginExecutorStreamUsage("openai-response", payload, &buffer)
	detail, ok := buffer.Detail()
	if !ok {
		t.Fatal("expected observed usage")
	}
	if detail.InputTokens != 34 || detail.OutputTokens != 499 || detail.TotalTokens != 533 {
		t.Fatalf("detail = %+v, want input=34 output=499 total=533", detail)
	}
	if detail.ResponseServiceTier != "default" {
		t.Fatalf("response service tier = %q, want default", detail.ResponseServiceTier)
	}
}

func TestParsePluginExecutorResponseUsageKeepsTopLevelResponsesTokensWhenServiceTierPresent(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"id":"resp_1","object":"response","service_tier":"default","usage":{"input_tokens":34,"output_tokens":499,"total_tokens":533}}`)
	detail := ParsePluginExecutorResponseUsage("openai-response", payload)
	if detail.InputTokens != 34 || detail.OutputTokens != 499 || detail.TotalTokens != 533 {
		t.Fatalf("detail = %+v, want input=34 output=499 total=533", detail)
	}
	if detail.ResponseServiceTier != "default" {
		t.Fatalf("response service tier = %q, want default", detail.ResponseServiceTier)
	}
}

func TestParsePluginExecutorResponseUsageResponsesShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		protocol     string
		payload      string
		inputTokens  int64
		outputTokens int64
		totalTokens  int64
		tier         string
	}{
		{
			name:         "top-level usage without service tier",
			protocol:     "openai-response",
			payload:      `{"usage":{"input_tokens":34,"output_tokens":499,"total_tokens":533}}`,
			inputTokens:  34,
			outputTokens: 499,
			totalTokens:  533,
		},
		{
			name:         "codex protocol keeps top-level usage with service tier",
			protocol:     "codex",
			payload:      `{"service_tier":"default","usage":{"input_tokens":34,"output_tokens":499,"total_tokens":533}}`,
			inputTokens:  34,
			outputTokens: 499,
			totalTokens:  533,
			tier:         "default",
		},
		{
			name:         "nested response usage wins over top-level usage",
			protocol:     "openai-response",
			payload:      `{"service_tier":"priority","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"response":{"usage":{"input_tokens":18,"output_tokens":22,"total_tokens":40}}}`,
			inputTokens:  18,
			outputTokens: 22,
			totalTokens:  40,
			tier:         "priority",
		},
		{
			name:     "service tier only stays empty of tokens",
			protocol: "openai-response",
			payload:  `{"service_tier":"default"}`,
			tier:     "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			detail := ParsePluginExecutorResponseUsage(tt.protocol, []byte(tt.payload))
			if detail.InputTokens != tt.inputTokens || detail.OutputTokens != tt.outputTokens || detail.TotalTokens != tt.totalTokens {
				t.Fatalf("detail = %+v, want input=%d output=%d total=%d", detail, tt.inputTokens, tt.outputTokens, tt.totalTokens)
			}
			if detail.ResponseServiceTier != tt.tier {
				t.Fatalf("response service tier = %q, want %q", detail.ResponseServiceTier, tt.tier)
			}
		})
	}
}

func TestMergeStreamUsageDetailPreservesInteractionsCacheAndReasoningSemantics(t *testing.T) {
	t.Parallel()

	initialPayload := []byte(`{"usage":{"input_tokens":100,"cache_read_tokens":20,"output_tokens":0,"reasoning_tokens":0,"total_tokens":100}}`)
	finalPayload := []byte(`{"usage":{"input_tokens":100,"cache_read_tokens":20,"output_tokens":30,"reasoning_tokens":10,"total_tokens":140}}`)

	initial := ParseInteractionsUsage(initialPayload)
	final := ParseInteractionsUsage(finalPayload)

	for _, tc := range []struct {
		name   string
		merged usage.Detail
	}{
		{
			name:   "cumulative initial and final snapshots",
			merged: MergeStreamUsageDetail(initial, final),
		},
		{
			name:   "identical final snapshots",
			merged: MergeStreamUsageDetail(final, final),
		},
		{
			name: "partial delta snapshot after initial input snapshot",
			merged: MergeStreamUsageDetail(
				initial,
				ParseInteractionsUsage([]byte(`{"usage":{"output_tokens":30,"reasoning_tokens":10}}`)),
			),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.merged
			if got.InputTokens != 100 || got.CacheReadTokens != 20 || got.OutputTokens != 30 || got.ReasoningTokens != 10 || got.TotalTokens != 140 {
				t.Fatalf("detail = %+v, want input=100 cache_read=20 output=30 reasoning=10 total=140", got)
			}
			tb := got.TokenBreakdown
			if !tb.Valid() || tb.Quality != usage.TokenAccountingQualityComplete {
				t.Fatalf("token breakdown = %+v, want valid complete breakdown", tb)
			}
			if tb.TotalTokens != 140 ||
				tb.Input.TotalTokens != 100 ||
				tb.Input.UncachedTokens != 80 ||
				tb.Input.CacheReadTokens != 20 ||
				tb.Input.CacheWriteTokens != 0 ||
				tb.Output.TotalTokens != 40 ||
				tb.Output.NonReasoningTokens != 30 ||
				tb.Output.ReasoningTokens != 10 {
				t.Fatalf("token breakdown = %+v, want total=140 input=(100/80/20/0) output=(40/30/10)", tb)
			}
		})
	}
}

func TestObservePluginExecutorStreamUsageInteractionsMergesSnapshotsWithoutMisclassifyingTokens(t *testing.T) {
	t.Parallel()

	for _, protocol := range []string{"interactions", "interactions-response"} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			var buffer StreamUsageBuffer
			payload := []byte(
				"data: {\"usage\":{\"input_tokens\":100,\"cache_read_tokens\":20,\"output_tokens\":0,\"reasoning_tokens\":0,\"total_tokens\":100}}\n\n" +
					"data: {\"usage\":{\"input_tokens\":100,\"cache_read_tokens\":20,\"output_tokens\":30,\"reasoning_tokens\":10,\"total_tokens\":140}}\n\n",
			)
			ObservePluginExecutorStreamUsage(protocol, payload, &buffer)
			detail, ok := buffer.Detail()
			if !ok {
				t.Fatal("expected observed usage")
			}
			if detail.InputTokens != 100 || detail.CacheReadTokens != 20 || detail.OutputTokens != 30 || detail.ReasoningTokens != 10 || detail.TotalTokens != 140 {
				t.Fatalf("detail = %+v, want input=100 cache_read=20 output=30 reasoning=10 total=140", detail)
			}
			tb := detail.TokenBreakdown
			if !tb.Valid() || tb.Quality != usage.TokenAccountingQualityComplete ||
				tb.TotalTokens != 140 ||
				tb.Input.TotalTokens != 100 || tb.Input.UncachedTokens != 80 || tb.Input.CacheReadTokens != 20 ||
				tb.Output.TotalTokens != 40 || tb.Output.NonReasoningTokens != 30 || tb.Output.ReasoningTokens != 10 {
				t.Fatalf("token breakdown = %+v, want total=140 input=(100/80/20/0) output=(40/30/10)", tb)
			}
		})
	}
}

func TestMergeStreamUsageDetailKeepsInconsistentClaudeDeltaInconsistent(t *testing.T) {
	t.Parallel()

	initial := ParseClaudeUsage([]byte(`{"usage":{"input_tokens":100,"cache_read_input_tokens":20,"output_tokens":0}}`))
	delta := ParseClaudeUsage([]byte(`{"usage":{"cache_read_input_tokens":20,"output_tokens":10,"output_tokens_details":{"thinking_tokens":20}}}`))

	merged := MergeStreamUsageDetail(initial, delta)
	if !merged.TokenBreakdown.Valid() || merged.TokenBreakdown.Quality != usage.TokenAccountingQualityInconsistent {
		t.Fatalf("token breakdown = %+v, want valid inconsistent breakdown", merged.TokenBreakdown)
	}
}
