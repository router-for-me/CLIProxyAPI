package helps

import "testing"

func TestObservePluginExecutorInteractionsUsagePreservesFinalSnapshot(t *testing.T) {
	t.Parallel()

	for _, protocol := range []string{"interactions", "interactions-response"} {
		for _, tc := range []struct {
			name    string
			initial string
			final   string
		}{
			{
				name:    "cache and separate reasoning",
				initial: `{"usage":{"input_tokens":100,"cache_read_tokens":20,"total_tokens":100}}`,
				final:   `{"usage":{"input_tokens":100,"cache_read_tokens":20,"output_tokens":30,"reasoning_tokens":10,"total_tokens":140}}`,
			},
			{
				name:    "official metadata and cache writes",
				initial: `{"metadata":{"total_usage":{"total_input_tokens":100,"total_cached_tokens":20,"cache_write_tokens":10,"total_tokens":100}}}`,
				final:   `{"metadata":{"total_usage":{"total_input_tokens":100,"total_cached_tokens":20,"cache_write_tokens":10,"total_output_tokens":30,"total_thought_tokens":10,"total_tokens":140}}}`,
			},
			{
				name:    "Gemini usage metadata",
				initial: `{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":20,"totalTokenCount":100}}`,
				final:   `{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":20,"candidatesTokenCount":30,"thoughtsTokenCount":10,"totalTokenCount":140}}`,
			},
			{
				name:    "authoritative zero buckets",
				initial: `{"usage":{"input_tokens":100,"cache_read_tokens":20,"output_tokens":30,"reasoning_tokens":10,"total_tokens":140}}`,
				final:   `{"usage":{"input_tokens":100,"cache_read_tokens":0,"output_tokens":30,"reasoning_tokens":0,"total_tokens":130}}`,
			},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				var buffer StreamUsageBuffer
				ObservePluginExecutorStreamUsage(protocol, []byte("data: "+tc.initial+"\n\n"), &buffer)
				// Repeated cumulative snapshots must not change their accounting.
				for range 2 {
					ObservePluginExecutorStreamUsage(protocol, []byte("data: "+tc.final+"\n\n"), &buffer)
					got, ok := buffer.Detail()
					want := ParseInteractionsUsage([]byte(tc.final))
					if !ok || got != want {
						t.Fatalf("stream usage = %+v (ok=%v), want final snapshot %+v", got, ok, want)
					}
					if !got.TokenBreakdown.Valid() {
						t.Fatalf("invalid token breakdown: %+v", got.TokenBreakdown)
					}
				}
			})
		}
	}
}

func TestObservePluginExecutorInteractionsUsagePreservesPartialEvents(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{"interactions", "interactions-response"} {
		for _, tc := range []struct {
			name   string
			events []string
			want   string
		}{
			{
				name: "input then output",
				events: []string{
					`{"usage":{"input_tokens":100,"cache_read_tokens":20,"cache_write_tokens":10,"total_tokens":100}}`,
					`{"usage":{"output_tokens":30,"reasoning_tokens":10,"total_tokens":140}}`,
				},
				want: `{"usage":{"input_tokens":100,"cache_read_tokens":20,"cache_write_tokens":10,"output_tokens":30,"reasoning_tokens":10,"total_tokens":140}}`,
			},
			{
				name: "output then input without total",
				events: []string{
					`{"interaction":{"usage":{"total_output_tokens":30,"total_thought_tokens":10,"total_tokens":40}}}`,
					`{"metadata":{"total_usage":{"total_input_tokens":100,"total_cached_tokens":20}}}`,
				},
				want: `{"usage":{"input_tokens":100,"cached_tokens":20,"output_tokens":30,"reasoning_tokens":10,"total_tokens":140}}`,
			},
			{
				name: "Gemini aliases across events",
				events: []string{
					`{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":20}}`,
					`{"usageMetadata":{"candidatesTokenCount":30,"thoughtsTokenCount":10}}`,
					`{"usage_metadata":{"thoughtsTokenCount":0,"cachedContentTokenCount":0}}`,
				},
				want: `{"usage":{"input_tokens":100,"cached_tokens":0,"output_tokens":30,"reasoning_tokens":0,"total_tokens":130}}`,
			},
			{
				name: "tool input updated separately",
				events: []string{
					`{"usage":{"input_tokens":90,"tool_use_tokens":10,"output_tokens":30,"reasoning_tokens":10}}`,
					`{"usage":{"total_tool_use_tokens":15}}`,
					`{"usage":{"prompt_tokens":100}}`,
				},
				want: `{"usage":{"input_tokens":100,"tool_use_tokens":15,"output_tokens":30,"reasoning_tokens":10,"total_tokens":155}}`,
			},
			{
				name: "total-only zero snapshot",
				events: []string{
					`{"usage":{"input_tokens":100,"tool_use_tokens":5,"cached_tokens":20,"cache_read_tokens":20,"cache_write_tokens":10,"output_tokens":30,"reasoning_tokens":10}}`,
					`{"service_tier":"default","usage":{"total_tokens":0}}`,
				},
				want: `{"service_tier":"default","usage":{"total_tokens":0}}`,
			},
			{
				name: "total-only zero metadata alias",
				events: []string{
					`{"usage":{"input_tokens":100,"tool_use_tokens":5,"cached_tokens":20,"cache_read_tokens":20,"cache_write_tokens":10,"output_tokens":30,"reasoning_tokens":10}}`,
					`{"usageMetadata":{"totalTokenCount":0}}`,
				},
				want: `{"usage":{"total_tokens":0}}`,
			},
			{
				name: "partial update after zero reset",
				events: []string{
					`{"usage":{"input_tokens":100,"tool_use_tokens":5,"cached_tokens":20,"cache_read_tokens":20,"cache_write_tokens":10,"output_tokens":30,"reasoning_tokens":10}}`,
					`{"usage":{"total_tokens":0}}`,
					`{"usage":{"input_tokens":12}}`,
				},
				want: `{"usage":{"input_tokens":12,"total_tokens":12}}`,
			},
			{
				name: "all zero snapshot",
				events: []string{
					`{"usage":{"input_tokens":100,"cached_tokens":20,"cache_read_tokens":20,"cache_write_tokens":10,"output_tokens":30,"reasoning_tokens":10}}`,
					`{"service_tier":"default","usage":{"input_tokens":0,"cached_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"output_tokens":0,"reasoning_tokens":0,"total_tokens":0}}`,
				},
				want: `{"service_tier":"default","usage":{"input_tokens":0,"total_tokens":0}}`,
			},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				var buffer StreamUsageBuffer
				for _, event := range tc.events {
					// Repeating a partial cumulative event must not add its counts twice.
					for range 2 {
						ObservePluginExecutorStreamUsage(protocol, []byte("data: "+event+"\n\n"), &buffer)
					}
				}
				got, ok := buffer.Detail()
				want := ParseInteractionsUsage([]byte(tc.want))
				if !ok || got != want {
					t.Fatalf("stream usage = %+v (ok=%v), want %+v", got, ok, want)
				}
				if !got.TokenBreakdown.Valid() {
					t.Fatalf("invalid token breakdown: %+v", got.TokenBreakdown)
				}
			})
		}
	}
}

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
