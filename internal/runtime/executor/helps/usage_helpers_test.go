package helps

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func TestParseOpenAIUsageChatCompletions(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 6 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 6)
	}
	if detail.TotalTokens != 16 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 16)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.CacheReadTokens != 4 {
		t.Fatalf("cache read tokens = %d, want %d", detail.CacheReadTokens, 4)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
	if detail.TokenBreakdown.Input.UncachedTokens != 6 || detail.TokenBreakdown.Output.NonReasoningTokens != 1 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseOpenAIUsageFlatReasoningDialect(t *testing.T) {
	// SGLang-style usage: reasoning_tokens flat on the usage object.
	data := []byte(`{"usage":{"prompt_tokens":241114,"total_tokens":242270,"completion_tokens":1156,"prompt_tokens_details":{"cached_tokens":240896},"reasoning_tokens":530}}`)
	detail := ParseOpenAIUsage(data)

	if detail.ReasoningTokens != 530 {
		t.Fatalf("reasoning tokens = %d, want 530", detail.ReasoningTokens)
	}
	if detail.InputTokens != 241114 || detail.OutputTokens != 1156 || detail.TotalTokens != 242270 {
		t.Fatalf("token counts = %+v", detail)
	}
	if detail.CachedTokens != 240896 || detail.CacheReadTokens != 240896 {
		t.Fatalf("cached tokens = %d/%d", detail.CachedTokens, detail.CacheReadTokens)
	}
	if !detail.TokenBreakdown.Valid() {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}

	// Streaming variant must behave the same.
	streamDetail, ok := ParseOpenAIStreamUsage([]byte(`data: {"id":"x","object":"chat.completion.chunk","usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16,"reasoning_tokens":4}}`))
	if !ok {
		t.Fatalf("ParseOpenAIStreamUsage reported no usage")
	}
	if streamDetail.ReasoningTokens != 4 {
		t.Fatalf("stream reasoning tokens = %d, want 4", streamDetail.ReasoningTokens)
	}
}

func TestParseOpenAIUsageResponses(t *testing.T) {
	data := []byte(`{"service_tier":"default","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
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
	if detail.CacheReadTokens != 7 {
		t.Fatalf("cache read tokens = %d, want %d", detail.CacheReadTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
	if detail.ResponseServiceTier != "default" {
		t.Fatalf("response service tier = %q, want default", detail.ResponseServiceTier)
	}
	if detail.TokenBreakdown.Input.UncachedTokens != 3 || detail.TokenBreakdown.Output.NonReasoningTokens != 11 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseOpenAIUsageTotalOnlyIsUnclassified(t *testing.T) {
	detail := ParseOpenAIUsage([]byte(`{"usage":{"total_tokens":42}}`))
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityUnclassified ||
		detail.TotalTokens != 42 || detail.TokenBreakdown.UnclassifiedTokens != 42 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestParseOpenAIUsagePartialBucketsPreserveKnownTokens(t *testing.T) {
	detail := ParseOpenAIUsage([]byte(`{"usage":{"input_tokens":10,"total_tokens":15}}`))
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityUnclassified ||
		detail.TokenBreakdown.Input.TotalTokens != 10 || detail.TokenBreakdown.UnclassifiedTokens != 5 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestParseOpenAIUsageExplicitZeroBucketsRemainInconsistent(t *testing.T) {
	detail := ParseOpenAIUsage([]byte(`{"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":42}}`))
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityInconsistent {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestParseCodexUsageIncludesCacheWriteTokens(t *testing.T) {
	data := []byte(`{"response":{"service_tier":"priority","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40}}}}`)
	detail, ok := ParseCodexUsage(data)
	if !ok {
		t.Fatal("ParseCodexUsage() ok = false, want true")
	}
	if detail.InputTokens != 100 {
		t.Fatalf("input tokens = %d, want 100", detail.InputTokens)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want 20", detail.OutputTokens)
	}
	if detail.CachedTokens != 30 {
		t.Fatalf("cached tokens = %d, want 30", detail.CachedTokens)
	}
	if detail.CacheReadTokens != 30 {
		t.Fatalf("cache read tokens = %d, want 30", detail.CacheReadTokens)
	}
	if detail.CacheCreationTokens != 40 {
		t.Fatalf("cache creation tokens = %d, want 40", detail.CacheCreationTokens)
	}
	if detail.TotalTokens != 120 {
		t.Fatalf("total tokens = %d, want 120", detail.TotalTokens)
	}
	if detail.ResponseServiceTier != "priority" {
		t.Fatalf("response service tier = %q, want priority", detail.ResponseServiceTier)
	}
	if detail.TokenBreakdown.Input.UncachedTokens != 30 || detail.TokenBreakdown.Input.CacheWriteTokens != 40 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseOpenAIUsageNormalizesCacheCreationAlias(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"cache_creation_tokens":4}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.CacheCreationTokens != 4 {
		t.Fatalf("cache creation tokens = %d, want 4", detail.CacheCreationTokens)
	}
}

func TestParseOpenAIUsageIgnoresNullUsage(t *testing.T) {
	data := []byte(`{"usage":null}`)
	detail := ParseOpenAIUsage(data)
	if detail != (usage.Detail{}) {
		t.Fatalf("detail = %+v, want zero detail", detail)
	}
}

func TestParseOpenAIUsagePreservesResponseTierWithoutUsage(t *testing.T) {
	t.Parallel()

	detail := ParseOpenAIUsage([]byte(`{"service_tier":"default"}`))
	if detail.ResponseServiceTier != "default" {
		t.Fatalf("response service tier = %q, want default", detail.ResponseServiceTier)
	}
}

func TestParseCodexUsagePreservesResponseTierWithoutUsage(t *testing.T) {
	t.Parallel()

	detail, ok := ParseCodexUsage([]byte(`{"response":{"service_tier":"default"}}`))
	if !ok || detail.ResponseServiceTier != "default" {
		t.Fatalf("ParseCodexUsage() = (%+v, %v), want response tier default", detail, ok)
	}
}

func TestParseOpenAIStreamUsageIgnoresNullUsage(t *testing.T) {
	line := []byte(`data: {"id":"chunk_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}],"usage":null}`)
	if detail, ok := ParseOpenAIStreamUsage(line); ok {
		t.Fatalf("ParseOpenAIStreamUsage() = (%+v, true), want false for null usage", detail)
	}
}

func TestParseOpenAIStreamUsageResponsesFields(t *testing.T) {
	line := []byte(`data: {"id":"chunk_1","object":"chat.completion.chunk","service_tier":"flex","choices":[],"usage":{"input_tokens":8,"output_tokens":5,"total_tokens":13,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}`)
	detail, ok := ParseOpenAIStreamUsage(line)
	if !ok {
		t.Fatal("ParseOpenAIStreamUsage() ok = false, want true")
	}
	if detail.InputTokens != 8 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 8)
	}
	if detail.OutputTokens != 5 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 5)
	}
	if detail.TotalTokens != 13 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 13)
	}
	if detail.CachedTokens != 3 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 3)
	}
	if detail.CacheReadTokens != 3 {
		t.Fatalf("cache read tokens = %d, want %d", detail.CacheReadTokens, 3)
	}
	if detail.ReasoningTokens != 2 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 2)
	}
	if detail.ResponseServiceTier != "flex" {
		t.Fatalf("response service tier = %q, want flex", detail.ResponseServiceTier)
	}
}

func TestStreamUsageBufferKeepsLastUsage(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.Observe(usage.Detail{}, true)
	buffer.Observe(usage.Detail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, false)
	buffer.Observe(usage.Detail{InputTokens: 39320, OutputTokens: 26, TotalTokens: 39346, CachedTokens: 33280}, true)

	detail, ok := buffer.Detail()
	if !ok {
		t.Fatal("buffer detail ok = false, want true")
	}
	if detail.InputTokens != 39320 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 39320)
	}
	if detail.OutputTokens != 26 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 26)
	}
	if detail.TotalTokens != 39346 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 39346)
	}
	if detail.CachedTokens != 33280 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 33280)
	}
}

func TestStreamUsageBufferPreservesTierAcrossChunks(t *testing.T) {
	t.Parallel()

	var buffer StreamUsageBuffer
	buffer.ObserveOpenAIStream([]byte(`data: {"service_tier":"default"}`))
	buffer.ObserveOpenAIStream([]byte(`data: {"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	detail, ok := buffer.Detail()
	if !ok {
		t.Fatal("Detail() ok = false, want true")
	}
	if detail.InputTokens != 1 || detail.OutputTokens != 1 || detail.ResponseServiceTier != "default" {
		t.Fatalf("detail = %+v, want usage with response tier default", detail)
	}
}

func TestStreamUsageBufferObserveOpenAIStreamStateTransitions(t *testing.T) {
	t.Parallel()

	t.Run("same chunk", func(t *testing.T) {
		var buffer StreamUsageBuffer
		buffer.ObserveOpenAIStream([]byte(`data: {"service_tier":"flex","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
		detail, ok := buffer.Detail()
		if !ok || detail.InputTokens != 2 || detail.ResponseServiceTier != "flex" {
			t.Fatalf("detail = %+v ok=%v", detail, ok)
		}
	})

	t.Run("usage before tier", func(t *testing.T) {
		var buffer StreamUsageBuffer
		buffer.ObserveOpenAIStream([]byte(`data: {"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
		buffer.ObserveOpenAIStream([]byte(`data: {"service_tier":"default"}`))
		detail, ok := buffer.Detail()
		if !ok || detail.InputTokens != 2 || detail.ResponseServiceTier != "default" {
			t.Fatalf("detail = %+v ok=%v", detail, ok)
		}
	})

	t.Run("final usage tier overrides early tier", func(t *testing.T) {
		var buffer StreamUsageBuffer
		buffer.ObserveOpenAIStream([]byte(`data: {"service_tier":"default"}`))
		buffer.ObserveOpenAIStream([]byte(`data: {"service_tier":"priority","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
		detail, ok := buffer.Detail()
		if !ok || detail.ResponseServiceTier != "priority" {
			t.Fatalf("detail = %+v ok=%v", detail, ok)
		}
	})

	t.Run("irrelevant and invalid chunks do not change state", func(t *testing.T) {
		var buffer StreamUsageBuffer
		buffer.ObserveOpenAIStream([]byte(`data: {"content":"the word \"usage\" appears here"}`))
		buffer.ObserveOpenAIStream([]byte(`data: {"usage":`))
		buffer.ObserveOpenAIStream([]byte(`data: {"usage":null}`))
		if detail, ok := buffer.Detail(); ok {
			t.Fatalf("detail = %+v ok=true, want empty buffer", detail)
		}
	})

	t.Run("zero token usage is retained", func(t *testing.T) {
		var buffer StreamUsageBuffer
		buffer.ObserveOpenAIStream([]byte(`data: {"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`))
		if _, ok := buffer.Detail(); !ok {
			t.Fatal("Detail() ok = false, want true")
		}
	})
}

func TestStreamUsageBufferPreservesOnlyZeroUsage(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.Observe(usage.Detail{}, true)

	detail, ok := buffer.Detail()
	if !ok {
		t.Fatal("buffer detail ok = false, want true")
	}
	if detail != (usage.Detail{}) {
		t.Fatalf("detail = %+v, want zero detail", detail)
	}
}

func TestParseClaudeUsageIncludesCacheTokensInTotal(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":3085,"output_tokens":253,"cache_read_input_tokens":7,"cache_creation_input_tokens":19514}}`)
	detail := ParseClaudeUsage(data)
	if detail.InputTokens != 3085 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 3085)
	}
	if detail.OutputTokens != 253 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 253)
	}
	if detail.CacheReadTokens != 7 {
		t.Fatalf("cache read tokens = %d, want %d", detail.CacheReadTokens, 7)
	}
	if detail.CacheCreationTokens != 19514 {
		t.Fatalf("cache creation tokens = %d, want %d", detail.CacheCreationTokens, 19514)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.TotalTokens != 22859 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 22859)
	}
	if detail.TokenBreakdown.Input.TotalTokens != 22606 || detail.TokenBreakdown.Input.UncachedTokens != 3085 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseClaudeUsageFallsBackCachedTokensToCacheCreation(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":3085,"output_tokens":253,"cache_creation_input_tokens":19514}}`)
	detail := ParseClaudeUsage(data)
	if detail.CachedTokens != 19514 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 19514)
	}
	if detail.TotalTokens != 22852 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 22852)
	}
}

func TestParseClaudeUsagePreservesThinkingTokensAsReasoningSubset(t *testing.T) {
	// Sanitized shape from local Anthropic request logs under ~/.config/cpa/logs.
	data := []byte(`{"usage":{"input_tokens":2,"cache_creation_input_tokens":831,"cache_read_input_tokens":44225,"output_tokens":244,"output_tokens_details":{"thinking_tokens":40}}}`)
	detail := ParseClaudeUsage(data)
	if detail.OutputTokens != 244 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 244)
	}
	if detail.ReasoningTokens != 40 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 40)
	}
	if detail.TotalTokens != 45302 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 45302)
	}
	if !detail.TokenBreakdown.Valid() ||
		detail.TokenBreakdown.Output.TotalTokens != 244 ||
		detail.TokenBreakdown.Output.NonReasoningTokens != 204 ||
		detail.TokenBreakdown.Output.ReasoningTokens != 40 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseClaudeStreamUsagePreservesThinkingTokensAsReasoningSubset(t *testing.T) {
	line := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":2,"cache_creation_input_tokens":831,"cache_read_input_tokens":44225,"output_tokens":244,"output_tokens_details":{"thinking_tokens":40}}}`)
	detail, ok := ParseClaudeStreamUsage(line)
	if !ok {
		t.Fatal("expected stream usage to parse")
	}
	if detail.OutputTokens != 244 || detail.ReasoningTokens != 40 || detail.TotalTokens != 45302 {
		t.Fatalf("stream usage detail = %+v", detail)
	}
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Output.NonReasoningTokens != 204 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseClaudeStreamUsage_MessageStart(t *testing.T) {
	line := []byte(`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-opus-5","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":2095,"cache_creation_input_tokens":7185,"cache_read_input_tokens":355598,"output_tokens":1}}}`)
	detail, ok := ParseClaudeStreamUsage(line)
	if !ok {
		t.Fatal("expected stream usage to parse from message_start")
	}
	if detail.InputTokens != 2095 {
		t.Errorf("input tokens = %d, want 2095", detail.InputTokens)
	}
	if detail.CacheReadTokens != 355598 {
		t.Errorf("cache read tokens = %d, want 355598", detail.CacheReadTokens)
	}
	if detail.CacheCreationTokens != 7185 {
		t.Errorf("cache creation tokens = %d, want 7185", detail.CacheCreationTokens)
	}
	if detail.CachedTokens != 355598 {
		t.Errorf("cached tokens = %d, want 355598", detail.CachedTokens)
	}
}

func TestParseClaudeUsageReportsSpeedAsResponseServiceTier(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"usage":{"input_tokens":10,"output_tokens":5,"speed":"fast"}}`, "fast"},
		{`{"usage":{"input_tokens":10,"output_tokens":5,"speed":"standard"}}`, "standard"},
		{`{"usage":{"input_tokens":10,"output_tokens":5}}`, ""},
	} {
		if got := ParseClaudeUsage([]byte(tc.body)).ResponseServiceTier; got != tc.want {
			t.Errorf("%s: ResponseServiceTier = %q, want %q", tc.body, got, tc.want)
		}
	}
}

// The speed can arrive on message_start while message_delta carries only the
// final output count; the merged stream usage must keep it.
func TestStreamUsageBufferObserveClaudeStream_KeepsSpeedAcrossDelta(t *testing.T) {
	for _, tc := range []struct{ start, delta, want string }{
		{`"speed":"fast"`, ``, "fast"},
		{``, `,"speed":"fast"`, "fast"},
		{``, ``, ""},
	} {
		var buffer StreamUsageBuffer
		start := `data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-5","usage":{"input_tokens":20,"output_tokens":1`
		if tc.start != "" {
			start += "," + tc.start
		}
		buffer.ObserveClaudeStream([]byte(start + `}}}`))
		buffer.ObserveClaudeStream([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15` + tc.delta + `}}`))
		detail, ok := buffer.Detail()
		if !ok || detail.ResponseServiceTier != tc.want || detail.OutputTokens != 15 || detail.InputTokens != 20 {
			t.Errorf("start %q delta %q: got tier %q in=%d out=%d ok=%v, want tier %q in=20 out=15",
				tc.start, tc.delta, detail.ResponseServiceTier, detail.InputTokens, detail.OutputTokens, ok, tc.want)
		}
	}
}

func TestParseClaudeUsageFallsBackToTopLevelThinkingTokens(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":3,"output_tokens":10,"thinking_tokens":4}}`)
	detail := ParseClaudeUsage(data)
	if detail.OutputTokens != 10 || detail.ReasoningTokens != 4 || detail.TotalTokens != 13 {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.TokenBreakdown.Output.NonReasoningTokens != 6 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseGeminiUsageNormalizesCachedContent(t *testing.T) {
	detail := ParseGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"cachedContentTokenCount":4,"totalTokenCount":12}}`))
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want 4", detail.CachedTokens)
	}
	if detail.CacheReadTokens != 4 {
		t.Fatalf("cache read tokens = %d, want 4", detail.CacheReadTokens)
	}
	if detail.TokenBreakdown.Input.UncachedTokens != 6 || detail.TokenBreakdown.TotalTokens != 12 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseGeminiUsageIncludesToolUsePromptTokens(t *testing.T) {
	detail := ParseGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"toolUsePromptTokenCount":5,"totalTokenCount":20}}`))
	if detail.InputTokens != 15 || detail.TotalTokens != 20 {
		t.Fatalf("detail = %+v", detail)
	}
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete ||
		detail.TokenBreakdown.Input.UncachedTokens != 15 || detail.TokenBreakdown.Output.ReasoningTokens != 3 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseGeminiStreamUsageSkipsZeroPlaceholder(t *testing.T) {
	lines := [][]byte{
		[]byte(`data: {"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"thoughtsTokenCount":0,"totalTokenCount":0}}`),
		[]byte(`data: {"usageMetadata":{"promptTokenCount":17984,"candidatesTokenCount":2668,"thoughtsTokenCount":1028,"totalTokenCount":21680}}`),
	}

	accepted := make([]usage.Detail, 0, len(lines))
	for _, line := range lines {
		detail, ok := ParseGeminiStreamUsage(line)
		if ok {
			accepted = append(accepted, detail)
		}
	}

	if len(accepted) != 1 {
		t.Fatalf("accepted usage count = %d, want 1", len(accepted))
	}
	detail := accepted[0]
	if detail.InputTokens != 17984 || detail.OutputTokens != 2668 || detail.ReasoningTokens != 1028 || detail.TotalTokens != 21680 {
		t.Fatalf("accepted usage detail = %+v", detail)
	}
}

func TestParseGeminiUsageRejectsInvalidToolUseSums(t *testing.T) {
	tests := map[string]string{
		"negative": `{"usageMetadata":{"promptTokenCount":10,"toolUsePromptTokenCount":-1,"totalTokenCount":10}}`,
		"overflow": `{"usageMetadata":{"promptTokenCount":9223372036854775807,"toolUsePromptTokenCount":1,"totalTokenCount":9223372036854775807}}`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			detail := ParseGeminiUsage([]byte(payload))
			if detail.InputTokens < 0 || !detail.TokenBreakdown.Valid() ||
				detail.TokenBreakdown.Quality != usage.TokenAccountingQualityInconsistent {
				t.Fatalf("detail = %+v", detail)
			}
		})
	}
}

func TestParseInteractionsUsage(t *testing.T) {
	detail := ParseInteractionsUsage([]byte(`{"usage":{"input_tokens":3,"output_tokens":4,"reasoning_tokens":5,"cached_tokens":2}}`))
	if detail.InputTokens != 3 {
		t.Fatalf("input tokens = %d, want 3", detail.InputTokens)
	}
	if detail.OutputTokens != 4 {
		t.Fatalf("output tokens = %d, want 4", detail.OutputTokens)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want 5", detail.ReasoningTokens)
	}
	if detail.TotalTokens != 12 {
		t.Fatalf("total tokens = %d, want 12", detail.TotalTokens)
	}
	if detail.CachedTokens != 2 {
		t.Fatalf("cached tokens = %d, want 2", detail.CachedTokens)
	}
	if detail.CacheReadTokens != 2 {
		t.Fatalf("cache read tokens = %d, want 2", detail.CacheReadTokens)
	}
	if detail.TokenBreakdown.Input.UncachedTokens != 1 || detail.TokenBreakdown.Output.TotalTokens != 9 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestNormalizeUsageDetailTotalDoesNotDoubleCountReasoning(t *testing.T) {
	detail := normalizeUsageDetailTotal(usage.Detail{
		InputTokens:     100,
		OutputTokens:    30,
		ReasoningTokens: 12,
	}, "openai", "")
	if detail.TotalTokens != 130 {
		t.Fatalf("total tokens = %d, want 130", detail.TotalTokens)
	}
	if detail.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete || detail.TokenBreakdown.Output.ReasoningTokens != 12 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseInteractionsUsageNormalizesCacheWriteAlias(t *testing.T) {
	detail := ParseInteractionsUsage([]byte(`{"usage":{"input_tokens":3,"cache_write_tokens":2}}`))
	if detail.CacheCreationTokens != 2 {
		t.Fatalf("cache creation tokens = %d, want 2", detail.CacheCreationTokens)
	}
}

func TestParseInteractionsUsageIncludesToolUseTokens(t *testing.T) {
	detail := ParseInteractionsUsage([]byte(`{"usage":{"total_input_tokens":2,"total_output_tokens":6,"total_thought_tokens":3,"total_tool_use_tokens":4,"total_tokens":15}}`))
	if detail.InputTokens != 6 || detail.OutputTokens != 6 || detail.ReasoningTokens != 3 || detail.TotalTokens != 15 {
		t.Fatalf("detail = %+v", detail)
	}
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete ||
		detail.TokenBreakdown.Input.UncachedTokens != 6 || detail.TokenBreakdown.Output.TotalTokens != 9 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestParseInteractionsStreamUsage(t *testing.T) {
	detail, ok := ParseInteractionsStreamUsage([]byte(`{"type":"interaction.completed","interaction":{"usage":{"input_tokens":2,"output_tokens":6,"total_tokens":8}}}`))
	if !ok {
		t.Fatal("ParseInteractionsStreamUsage() ok = false, want true")
	}
	if detail.TotalTokens != 8 {
		t.Fatalf("total tokens = %d, want 8", detail.TotalTokens)
	}
}

func TestParseInteractionsStreamUsageOfficialMetadata(t *testing.T) {
	detail, ok := ParseInteractionsStreamUsage([]byte(`data: {"event_type":"finish","metadata":{"total_usage":{"total_input_tokens":2,"total_output_tokens":6,"total_thought_tokens":3,"total_cached_tokens":1,"total_tokens":11}}}`))
	if !ok {
		t.Fatal("ParseInteractionsStreamUsage() ok = false, want true")
	}
	if detail.InputTokens != 2 {
		t.Fatalf("input tokens = %d, want 2", detail.InputTokens)
	}
	if detail.OutputTokens != 6 {
		t.Fatalf("output tokens = %d, want 6", detail.OutputTokens)
	}
	if detail.ReasoningTokens != 3 {
		t.Fatalf("reasoning tokens = %d, want 3", detail.ReasoningTokens)
	}
	if detail.CachedTokens != 1 {
		t.Fatalf("cached tokens = %d, want 1", detail.CachedTokens)
	}
	if detail.CacheReadTokens != 1 {
		t.Fatalf("cache read tokens = %d, want 1", detail.CacheReadTokens)
	}
	if detail.TotalTokens != 11 {
		t.Fatalf("total tokens = %d, want 11", detail.TotalTokens)
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

func TestUsageReporterTrackHTTPClientStartsTTFTBeforeRoundTrip(t *testing.T) {
	delay := 40 * time.Millisecond
	ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)
	client := reporter.TrackHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			time.Sleep(delay)
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("ok")),
				Request:    req,
			}, nil
		}),
	})

	req, errNewRequest := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid/v1/chat/completions", strings.NewReader("{}"))
	if errNewRequest != nil {
		t.Fatalf("NewRequestWithContext() error = %v", errNewRequest)
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatalf("Do() error = %v", errDo)
	}
	if _, errRead := io.ReadAll(resp.Body); errRead != nil {
		t.Fatalf("ReadAll() error = %v", errRead)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close error = %v", errClose)
	}
	if got := reporter.ttftDuration(); got < delay {
		t.Fatalf("ttft = %v, want >= %v", got, delay)
	}
	if !cliproxyexecutor.UpstreamAttempted(ctx) {
		t.Fatal("HTTP RoundTrip did not mark an upstream attempt")
	}
}

func TestUsageReporterTrackHTTPClientRoundTripOnly_DoesNotTriggerOnBodyRead(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6-luna", nil)
	client := reporter.TrackHTTPClientRoundTripOnly(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			time.Sleep(10 * time.Millisecond)
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.created\"}\n\n")),
				Request:    req,
			}, nil
		}),
	})

	req, errNewRequest := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.invalid/v1/responses", strings.NewReader("{}"))
	if errNewRequest != nil {
		t.Fatalf("NewRequestWithContext() error = %v", errNewRequest)
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatalf("Do() error = %v", errDo)
	}
	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatalf("ReadAll() error = %v", errRead)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close error = %v", errClose)
	}

	// 1. Plain body reading must NOT set TTFT
	if reporter.IsTTFTSet() {
		t.Fatalf("TrackHTTPClientRoundTripOnly must not set TTFT on plain body read")
	}

	// 2. Observing metadata event records fallback, but does NOT set effective TTFT
	ObserveResponsesTokenEvent(reporter, bodyBytes)
	if reporter.IsTTFTSet() {
		t.Fatalf("Observing metadata event must not set effective TTFT")
	}
	if reporter.ttftDuration() <= 0 {
		t.Fatalf("Fallback TTFT should be recorded and > 0, got %v", reporter.ttftDuration())
	}

	// 3. Observing substantive token event sets effective TTFT
	ObserveResponsesTokenEvent(reporter, []byte(`{"type":"response.output_text.delta","delta":"hello"}`))
	if !reporter.IsTTFTSet() {
		t.Fatalf("Observing token event must set effective TTFT")
	}
}

func TestUsageReporterTrackHTTPClientRoundTripOnly_ErrorResponseRecordsFirstPacketFallback(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6-luna", nil)
	client := reporter.TrackHTTPClientRoundTripOnly(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Status:     "429 Too Many Requests",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limit"}}`)),
				Request:    req,
			}, nil
		}),
	})

	req, errNewRequest := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.invalid/v1/responses", strings.NewReader("{}"))
	if errNewRequest != nil {
		t.Fatalf("NewRequestWithContext() error = %v", errNewRequest)
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatalf("Do() error = %v", errDo)
	}
	_, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatalf("ReadAll() error = %v", errRead)
	}
	_ = resp.Body.Close()

	if reporter.IsTTFTSet() {
		t.Fatalf("error response read must not set substantive token TTFT")
	}
	if !reporter.IsFirstPacketSet() {
		t.Fatalf("error response read must record first packet set fallback")
	}
}

func TestUsageReporterObserveTokenEvent_FastPathNonTokenAndToken(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6-luna", nil)
	reporter.StartResponseTTFT()

	// 1. Initial state
	if reporter.IsTTFTSet() {
		t.Fatalf("expected IsTTFTSet() == false initially")
	}

	// 2. First non-token event records firstPacketDuration, but does not mark TTFT set
	reporter.ObserveTokenEvent(false)
	if reporter.IsTTFTSet() {
		t.Fatalf("ObserveTokenEvent(false) must not set TTFT")
	}
	if !reporter.IsFirstPacketSet() {
		t.Fatalf("expected IsFirstPacketSet() == true")
	}
	firstPacketDuration := reporter.firstPacketDuration

	// 3. Subsequent non-token event is a fast-path return and does not alter firstPacketDuration
	reporter.ObserveTokenEvent(false)
	if reporter.firstPacketDuration != firstPacketDuration {
		t.Fatalf("subsequent ObserveTokenEvent(false) must preserve original firstPacketDuration")
	}

	// 4. Substantive token event sets effective TTFT
	reporter.ObserveTokenEvent(true)
	if !reporter.IsTTFTSet() {
		t.Fatalf("ObserveTokenEvent(true) must set IsTTFTSet() == true")
	}
	tokenTTFT := reporter.ttft

	// 5. Subsequent token event is a fast-path return and does not alter TTFT
	reporter.ObserveTokenEvent(true)
	if reporter.ttft != tokenTTFT {
		t.Fatalf("subsequent ObserveTokenEvent(true) must not alter already recorded TTFT")
	}
}

func TestUsageReporterBuildRecordIncludesRequestedModelAlias(t *testing.T) {
	ctx := usage.WithRequestedModelAlias(context.Background(), "client-gpt")
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want %q", record.Model, "gpt-5.4")
	}
	if record.Alias != "client-gpt" {
		t.Fatalf("alias = %q, want %q", record.Alias, "client-gpt")
	}
}

func TestNewExecutorUsageReporterIncludesExecutorType(t *testing.T) {
	reporter := NewExecutorUsageReporter(context.Background(), &TestUsageExecutor{}, "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Provider != "test-provider" {
		t.Fatalf("provider = %q, want %q", record.Provider, "test-provider")
	}
	if record.ExecutorType != "TestUsageExecutor" {
		t.Fatalf("executor type = %q, want %q", record.ExecutorType, "TestUsageExecutor")
	}
}

func TestUsageReporterBuildRecordIncludesReasoningEffort(t *testing.T) {
	ctx := usage.WithReasoningEffort(context.Background(), "medium")
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.ReasoningEffort != "medium" {
		t.Fatalf("reasoning effort = %q, want %q", record.ReasoningEffort, "medium")
	}
}

func TestUsageReporterBuildRecordIncludesServiceTier(t *testing.T) {
	ctx := usage.WithServiceTier(context.Background(), "auto")
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3, ResponseServiceTier: "default"}, false)
	if record.ServiceTier != "auto" {
		t.Fatalf("service tier = %q, want %q", record.ServiceTier, "auto")
	}
	if record.ResponseServiceTier != "default" {
		t.Fatalf("response service tier = %q, want default", record.ResponseServiceTier)
	}
}

func TestUsageReporterBuildRecordDefaultsGenerateTrue(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if !usage.GenerateEnabled(record.Generate) {
		t.Fatalf("generate = %v, want true", usage.GenerateEnabled(record.Generate))
	}
}

func TestUsageReporterBuildRecordIncludesGenerateFalse(t *testing.T) {
	ctx := usage.WithGenerate(context.Background(), false)
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if usage.GenerateEnabled(record.Generate) {
		t.Fatalf("generate = %v, want false", usage.GenerateEnabled(record.Generate))
	}
}

func TestUsageReporterBuildRecordDefaultsStreamFalse(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Stream {
		t.Fatalf("stream = %v, want false", record.Stream)
	}
}

func TestUsageReporterBuildRecordIncludesStreamTrue(t *testing.T) {
	ctx := usage.WithStream(context.Background(), true)
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if !record.Stream {
		t.Fatalf("stream = %v, want true", record.Stream)
	}
}

func TestUsageReporterSetStream(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "openai", "gpt-5.4", nil)
	reporter.SetStream(true)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if !record.Stream {
		t.Fatalf("stream = %v, want true", record.Stream)
	}
}

func TestUsageReporterSetTranslatedReasoningEffortPreservesClientServiceTier(t *testing.T) {
	ctx := usage.WithServiceTier(context.Background(), "auto")
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	reporter.SetTranslatedReasoningEffort([]byte(`{"service_tier":"priority"}`), "openai")

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3, ResponseServiceTier: "default"}, false)
	if record.ServiceTier != "auto" {
		t.Fatalf("service tier = %q, want %q", record.ServiceTier, "auto")
	}
	if record.EffectiveServiceTier != "priority" {
		t.Fatalf("effective service tier = %q, want %q", record.EffectiveServiceTier, "priority")
	}
	if record.ResponseServiceTier != "default" {
		t.Fatalf("response service tier = %q, want %q", record.ResponseServiceTier, "default")
	}
}

func TestUsageReporterExtractsEffectiveServiceTierFromOutboundShapes(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		payload string
		want    string
	}{
		{name: "top level", payload: `{"service_tier":"priority"}`, want: "priority"},
		{name: "antigravity request envelope", payload: `{"request":{"service_tier":"priority"}}`, want: "priority"},
		{name: "interaction envelope", payload: `{"interaction":{"service_tier":"standard"}}`, want: "standard"},
		{name: "missing", payload: `{"model":"gpt-5.4"}`, want: ""},
		{name: "invalid json", payload: `{"service_tier":`, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reporter := NewUsageReporter(context.Background(), "openai", "gpt-5.4", nil)
			reporter.SetTranslatedRequestMetadata([]byte(tt.payload), "openai")
			record := reporter.buildRecord(usage.Detail{TotalTokens: 1}, false)
			if record.EffectiveServiceTier != tt.want {
				t.Fatalf("effective service tier = %q, want %q", record.EffectiveServiceTier, tt.want)
			}
		})
	}
}

func TestUsageReporterCapturesEffectiveServiceTierAfterPayloadOverride(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "gpt-*", Protocol: "codex"}},
				Params: map[string]any{"service_tier": "priority"},
			}},
		},
	}
	finalPayload := ApplyPayloadConfigWithRequest(
		cfg,
		"gpt-6-astra",
		"codex",
		"openai",
		"",
		[]byte(`{"model":"gpt-6-astra"}`),
		nil,
		"gpt-6-astra",
		"/v1/chat/completions",
		nil,
	)

	ctx := usage.WithServiceTier(context.Background(), "auto")
	reporter := NewUsageReporter(ctx, "codex", "gpt-6-astra", nil)
	reporter.SetTranslatedRequestMetadata(finalPayload, "codex")
	record := reporter.buildRecord(usage.Detail{TotalTokens: 1, ResponseServiceTier: "default"}, false)
	if record.ServiceTier != "auto" || record.EffectiveServiceTier != "priority" || record.ResponseServiceTier != "default" {
		t.Fatalf("tiers = requested:%q effective:%q response:%q", record.ServiceTier, record.EffectiveServiceTier, record.ResponseServiceTier)
	}
}

func TestUsageReporterSetTranslatedReasoningEffortCodexConfigurationUpdate(t *testing.T) {
	ctx := context.Background()
	reporter := NewUsageReporter(ctx, "codex", "gpt-6-astra", nil)

	payload := []byte(`{"model":"gpt-6-astra","reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`)
	reporter.SetTranslatedReasoningEffort(payload, "codex")

	record := reporter.buildRecord(usage.Detail{TotalTokens: 10}, false)
	if record.ReasoningEffort != "low" {
		t.Fatalf("reasoning effort = %q, want %q", record.ReasoningEffort, "low")
	}
}

func TestUsageReporterSetTranslatedReasoningEffortConfigurationUpdateAfterCleanup(t *testing.T) {
	const source = `{"reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"medium"}},{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`
	for _, tc := range []struct {
		name      string
		supported bool
		want      string
	}{
		{name: "supported target reports effective update", supported: true, want: "low"},
		{name: "unsupported target reports cleaned suffix", want: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := "opaque-route(high)"
			requestEffort := thinking.ExtractReasoningEffort([]byte(source), "openai-response", model)
			if requestEffort != "low" {
				t.Fatalf("request effort = %q, want low", requestEffort)
			}
			info := &registry.ModelInfo{
				ID: "opaque-route", Type: "codex", SupportConfigurationUpdate: tc.supported,
				Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
			}
			body, err := thinking.ApplyThinkingWithModelInfoAndSummary([]byte(source), []byte(source), model, "openai-response", "codex", "codex", info, thinking.SummaryConfig{})
			if err != nil {
				t.Fatalf("ApplyThinkingWithModelInfoAndSummary() error = %v", err)
			}
			reporter := NewUsageReporter(usage.WithReasoningEffort(context.Background(), requestEffort), "codex", model, nil)
			reporter.SetTranslatedReasoningEffort(body, "codex")
			if got := reporter.buildRecord(usage.Detail{TotalTokens: 10}, false).ReasoningEffort; got != tc.want {
				t.Fatalf("final usage effort = %q, want %q; body=%s", got, tc.want, body)
			}
		})
	}
}

func TestUsageReporterBuildAdditionalModelRecordSkipsZeroTokens(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "codex",
		model:       "gpt-5.4",
		requestedAt: time.Now(),
	}

	if _, ok := reporter.buildAdditionalModelRecord("gpt-image-2", usage.Detail{}); ok {
		t.Fatalf("expected all-zero token usage to be skipped")
	}
	if _, ok := reporter.buildAdditionalModelRecord("gpt-image-2", usage.Detail{InputTokens: 2}); !ok {
		t.Fatalf("expected non-zero input token usage to be recorded")
	}
	if _, ok := reporter.buildAdditionalModelRecord("gpt-image-2", usage.Detail{CachedTokens: 2}); !ok {
		t.Fatalf("expected non-zero cached token usage to be recorded")
	}
}

type usageResponseBodyError struct {
	status  int
	message string
	body    []byte
}

func (e usageResponseBodyError) Error() string {
	return e.message
}

func (e usageResponseBodyError) StatusCode() int {
	return e.status
}

func (e usageResponseBodyError) ResponseBody() []byte {
	return e.body
}

func TestFailFromErrorsPrefersResponseBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "original response body", body: []byte(" \n{\"error\":\"upstream rejected request\"}\r\n")},
		{name: "empty response body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errExecute := fmt.Errorf("execute failed: %w", usageResponseBodyError{
				status:  http.StatusUnauthorized,
				message: "generic upstream error",
				body:    tc.body,
			})
			failure := failFromErrors(errExecute)
			wantBody := errExecute.Error()
			if len(tc.body) > 0 {
				wantBody = string(tc.body)
			}
			if failure.StatusCode != http.StatusUnauthorized || failure.Body != wantBody {
				t.Fatalf("failure = %#v, want status %d body %q", failure, http.StatusUnauthorized, wantBody)
			}
		})
	}
}

func TestFailFromErrorsMapsContextStatuses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "canceled", err: context.Canceled, want: clienterror.StatusClientClosedRequest},
		{name: "deadline", err: context.DeadlineExceeded, want: http.StatusGatewayTimeout},
		{
			name: "url error wraps canceled",
			err:  &url.Error{Op: "Post", URL: "https://example.com", Err: context.Canceled},
			want: clienterror.StatusClientClosedRequest,
		},
		{name: "plain error", err: errors.New("boom"), want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fail := failFromErrors(tc.err)
			if fail.StatusCode != tc.want {
				t.Fatalf("StatusCode = %d, want %d; body=%q", fail.StatusCode, tc.want, fail.Body)
			}
			if strings.TrimSpace(fail.Body) == "" {
				t.Fatalf("expected non-empty failure body")
			}
		})
	}

	if fail := failFromErrors(nil, nil); fail.StatusCode != 0 || fail.Body != "" {
		t.Fatalf("failFromErrors(nil) = %+v, want empty failure", fail)
	}
}

func TestStreamUsageBufferPublishFailure(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.Observe(usage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}, true)

	reporter := &UsageReporter{
		provider: "openai",
		model:    "gpt-5.4",
	}

	record := reporter.buildRecord(buffer.detail, true, failFromErrors(context.Canceled))
	if !record.Failed {
		t.Fatal("expected record to be marked failed")
	}
	if record.Fail.StatusCode != clienterror.StatusClientClosedRequest {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, clienterror.StatusClientClosedRequest)
	}
	if record.Detail.TotalTokens != 15 {
		t.Fatalf("Detail.TotalTokens = %d, want 15", record.Detail.TotalTokens)
	}
}

func TestStreamUsageBufferObserveClaudeStream_MergesStartAndDelta(t *testing.T) {
	var buffer StreamUsageBuffer

	lineStart := []byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-5","usage":{"input_tokens":2095,"cache_creation_input_tokens":7185,"cache_read_input_tokens":355598,"output_tokens":1}}}`)
	buffer.ObserveClaudeStream(lineStart)

	lineDelta := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}`)
	buffer.ObserveClaudeStream(lineDelta)

	detail, ok := buffer.Detail()
	if !ok {
		t.Fatal("expected buffer to contain usage detail")
	}
	if detail.InputTokens != 2095 {
		t.Errorf("InputTokens = %d, want 2095", detail.InputTokens)
	}
	if detail.OutputTokens != 15 {
		t.Errorf("OutputTokens = %d, want 15", detail.OutputTokens)
	}
	if detail.CacheReadTokens != 355598 {
		t.Errorf("CacheReadTokens = %d, want 355598", detail.CacheReadTokens)
	}
	if detail.CacheCreationTokens != 7185 {
		t.Errorf("CacheCreationTokens = %d, want 7185", detail.CacheCreationTokens)
	}
	if detail.CachedTokens != 355598 {
		t.Errorf("CachedTokens = %d, want 355598", detail.CachedTokens)
	}
	wantTotal := int64(2095 + 15 + 355598 + 7185)
	if detail.TotalTokens != wantTotal {
		t.Errorf("TotalTokens = %d, want %d", detail.TotalTokens, wantTotal)
	}
}

func TestStreamUsageBufferObserveClaudeStream_FailurePreservesUsage(t *testing.T) {
	var buffer StreamUsageBuffer

	lineStart := []byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-5","usage":{"input_tokens":2095,"cache_creation_input_tokens":7185,"cache_read_input_tokens":355598,"output_tokens":1}}}`)
	buffer.ObserveClaudeStream(lineStart)

	reporter := &UsageReporter{
		provider: "claude",
		model:    "claude-opus-5",
	}

	record := reporter.buildRecord(buffer.detail, true, failFromErrors(context.Canceled))
	if !record.Failed {
		t.Fatal("expected record to be marked failed")
	}
	if record.Detail.InputTokens != 2095 {
		t.Errorf("InputTokens = %d, want 2095", record.Detail.InputTokens)
	}
	if record.Detail.CacheReadTokens != 355598 {
		t.Errorf("CacheReadTokens = %d, want 355598", record.Detail.CacheReadTokens)
	}
	if record.Detail.CacheCreationTokens != 7185 {
		t.Errorf("CacheCreationTokens = %d, want 7185", record.Detail.CacheCreationTokens)
	}

	// Verify buffer.PublishFailure succeeds with the accumulated usage detail
	if !buffer.PublishFailure(context.Background(), reporter, context.Canceled) {
		t.Fatal("expected PublishFailure to return true")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type TestUsageExecutor struct{}

func (TestUsageExecutor) Identifier() string {
	return "test-provider"
}

func TestUsageReporterPropagatesSessionHierarchy(t *testing.T) {
	ctx := logging.WithClientRequestMetadata(context.Background(), logging.ClientRequestMetadata{
		SessionID:       "claude:sess-1:agent:sub-1",
		ParentSessionID: "claude:sess-1",
	})

	reporter := NewUsageReporter(ctx, "claude", "claude-3-7-sonnet", nil)
	record := reporter.buildRecord(usage.Detail{TotalTokens: 100}, false, usage.Failure{})
	if record.SessionID != "claude:sess-1:agent:sub-1" || record.ParentSessionID != "claude:sess-1" {
		t.Fatalf("record session hierarchy = (%q, %q), want (claude:sess-1:agent:sub-1, claude:sess-1)", record.SessionID, record.ParentSessionID)
	}

	// Test explicit override via SetSessionHierarchy
	reporter.SetSessionHierarchy("override:child", "override:parent")
	record2 := reporter.buildRecord(usage.Detail{TotalTokens: 100}, false, usage.Failure{})
	if record2.SessionID != "override:child" || record2.ParentSessionID != "override:parent" {
		t.Fatalf("overridden record session hierarchy = (%q, %q), want (override:child, override:parent)", record2.SessionID, record2.ParentSessionID)
	}

	// Test self-loop elimination in SetSessionHierarchy
	reporter.SetSessionHierarchy("loop:node", "loop:node")
	record3 := reporter.buildRecord(usage.Detail{TotalTokens: 100}, false, usage.Failure{})
	if record3.SessionID != "loop:node" || record3.ParentSessionID != "" {
		t.Fatalf("self loop record session hierarchy = (%q, %q), want (loop:node, empty)", record3.SessionID, record3.ParentSessionID)
	}

	// Test orphan parent elimination when sessionID is empty
	reporter.SetSessionHierarchy("", "orphan:parent")
	record4 := reporter.buildRecord(usage.Detail{TotalTokens: 100}, false, usage.Failure{})
	if record4.SessionID != "" || record4.ParentSessionID != "" {
		t.Fatalf("orphan parent record session hierarchy = (%q, %q), want empty both", record4.SessionID, record4.ParentSessionID)
	}

	// Test cross-prefix alias rejection (e.g. pck:* and conv:* are aliases, not parent-child)
	ctxAlias := logging.WithClientRequestMetadata(context.Background(), logging.ClientRequestMetadata{
		SessionID:       "pck:prompt-key-123",
		ParentSessionID: "conv:conv-456",
	})
	reporterAlias := NewUsageReporter(ctxAlias, "openai", "gpt-5.4", nil)
	recordAlias := reporterAlias.buildRecord(usage.Detail{TotalTokens: 100}, false, usage.Failure{})
	if recordAlias.SessionID != "pck:prompt-key-123" || recordAlias.ParentSessionID != "" {
		t.Fatalf("cross prefix alias emitted as parent: (%q, %q), want (pck:prompt-key-123, empty)", recordAlias.SessionID, recordAlias.ParentSessionID)
	}

	reporterAlias.SetSessionHierarchy("pck:key-999", "conv:alias-888")
	recordAlias2 := reporterAlias.buildRecord(usage.Detail{TotalTokens: 100}, false, usage.Failure{})
	if recordAlias2.SessionID != "pck:key-999" || recordAlias2.ParentSessionID != "" {
		t.Fatalf("SetSessionHierarchy cross prefix alias emitted as parent: (%q, %q), want (pck:key-999, empty)", recordAlias2.SessionID, recordAlias2.ParentSessionID)
	}
}

func TestUsageReporterPropagatesBaseURL(t *testing.T) {
	ctx := context.Background()
	auth := &cliproxyauth.Auth{
		ID:       "auth-base-url-test",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "test-api-key",
			"base_url": "https://custom.endpoint.example.com/v1",
		},
	}
	reporter := NewUsageReporter(ctx, "codex", "gpt-5.6-luna", auth)
	record := reporter.buildRecord(usage.Detail{TotalTokens: 10}, false, usage.Failure{})
	if record.BaseURL != "https://custom.endpoint.example.com/v1" {
		t.Fatalf("record.BaseURL = %q, want %q", record.BaseURL, "https://custom.endpoint.example.com/v1")
	}

	authMeta := &cliproxyauth.Auth{
		ID:       "auth-meta-base-url-test",
		Provider: "openai",
		Metadata: map[string]any{
			"base_url": "https://meta.endpoint.example.com/v1",
		},
	}
	reporterMeta := NewUsageReporter(ctx, "openai", "gpt-5.4", authMeta)
	recordMeta := reporterMeta.buildRecord(usage.Detail{TotalTokens: 10}, false, usage.Failure{})
	if recordMeta.BaseURL != "https://meta.endpoint.example.com/v1" {
		t.Fatalf("recordMeta.BaseURL = %q, want %q", recordMeta.BaseURL, "https://meta.endpoint.example.com/v1")
	}

	// Attribute precedence over metadata
	authBoth := &cliproxyauth.Auth{
		ID:       "auth-both-test",
		Provider: "openai",
		Attributes: map[string]string{
			"base_url": "https://attr.example.com",
		},
		Metadata: map[string]any{
			"base_url": "https://meta.example.com",
		},
	}
	reporterBoth := NewUsageReporter(ctx, "openai", "gpt-5.4", authBoth)
	recordBoth := reporterBoth.buildRecord(usage.Detail{TotalTokens: 10}, false, usage.Failure{})
	if recordBoth.BaseURL != "https://attr.example.com" {
		t.Fatalf("recordBoth.BaseURL = %q, want %q", recordBoth.BaseURL, "https://attr.example.com")
	}

	// Blank attribute falls back to metadata
	authBlankAttr := &cliproxyauth.Auth{
		ID:       "auth-blank-test",
		Provider: "openai",
		Attributes: map[string]string{
			"base_url": "   ",
		},
		Metadata: map[string]any{
			"base_url": "https://meta.example.com",
		},
	}
	reporterBlankAttr := NewUsageReporter(ctx, "openai", "gpt-5.4", authBlankAttr)
	recordBlankAttr := reporterBlankAttr.buildRecord(usage.Detail{TotalTokens: 10}, false, usage.Failure{})
	if recordBlankAttr.BaseURL != "https://meta.example.com" {
		t.Fatalf("recordBlankAttr.BaseURL = %q, want %q", recordBlankAttr.BaseURL, "https://meta.example.com")
	}

	// Nil auth or unconfigured auth leaves BaseURL empty
	reporterNilAuth := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)
	recordNilAuth := reporterNilAuth.buildRecord(usage.Detail{TotalTokens: 10}, false, usage.Failure{})
	if recordNilAuth.BaseURL != "" {
		t.Fatalf("recordNilAuth.BaseURL = %q, want empty", recordNilAuth.BaseURL)
	}
}

func TestUsageReporter_AllocatesUniqueUUIDPerAttemptWithSharedTraceID(t *testing.T) {
	ctx := logging.WithRequestID(context.Background(), "00000042")

	// First execution attempt
	reporter1 := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)
	record1 := reporter1.buildRecord(usage.Detail{TotalTokens: 10}, false, usage.Failure{})

	// Second execution attempt (retry)
	reporter2 := NewUsageReporter(ctx, "claude", "claude-3-7-sonnet", nil)
	record2 := reporter2.buildRecord(usage.Detail{TotalTokens: 20}, false, usage.Failure{})

	if record1.TraceID != "00000042" {
		t.Fatalf("record1.TraceID = %q, want 00000042", record1.TraceID)
	}
	if record2.TraceID != "00000042" {
		t.Fatalf("record2.TraceID = %q, want 00000042", record2.TraceID)
	}

	if record1.RequestID == "" {
		t.Fatal("record1.RequestID should not be empty")
	}
	if record2.RequestID == "" {
		t.Fatal("record2.RequestID should not be empty")
	}

	if record1.RequestID == record2.RequestID {
		t.Fatalf("consecutive attempts must have distinct UUID RequestIDs: got %q and %q", record1.RequestID, record2.RequestID)
	}

	// Verify UUID format and version 4
	u1, err1 := uuid.Parse(record1.RequestID)
	if err1 != nil || u1.Version() != 4 {
		t.Fatalf("record1.RequestID %q is not valid UUID v4: %v", record1.RequestID, err1)
	}
	u2, err2 := uuid.Parse(record2.RequestID)
	if err2 != nil || u2.Version() != 4 {
		t.Fatalf("record2.RequestID %q is not valid UUID v4: %v", record2.RequestID, err2)
	}
}

func TestUsageReporter_ExplicitTraceIDPrecedenceOverLogRequestID(t *testing.T) {
	ctx := logging.WithRequestID(context.Background(), "log-id-1")
	ctx = usage.WithTraceID(ctx, "explicit-trace-1")

	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)
	record := reporter.buildRecord(usage.Detail{TotalTokens: 10}, false, usage.Failure{})

	if record.TraceID != "explicit-trace-1" {
		t.Fatalf("record.TraceID = %q, want explicit-trace-1", record.TraceID)
	}
}

func TestUsageReporter_DistinguishesNativeKey(t *testing.T) {
	t.Run("native_key_via_gin", func(t *testing.T) {
		w := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(w)
		ginCtx.Set("userApiKey", "sk-native-key")
		ginCtx.Set("accessProvider", "config-inline")

		ctx := context.WithValue(context.Background(), "gin", ginCtx)
		reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)
		if !reporter.IsNativeKey() {
			t.Fatalf("reporter.IsNativeKey() = false, want true")
		}
		if reporter.AccessProvider() != "config-inline" {
			t.Fatalf("reporter.AccessProvider() = %q, want %q", reporter.AccessProvider(), "config-inline")
		}

		record := reporter.buildRecord(usage.Detail{TotalTokens: 10}, false, usage.Failure{})
		if !record.IsNativeKey {
			t.Fatalf("record.IsNativeKey = false, want true")
		}
		if record.AccessProvider != "config-inline" {
			t.Fatalf("record.AccessProvider = %q, want %q", record.AccessProvider, "config-inline")
		}
	})

	t.Run("plugin_key_via_gin", func(t *testing.T) {
		w := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(w)
		ginCtx.Set("userApiKey", "plugin-principal")
		ginCtx.Set("accessProvider", "custom-auth-plugin")

		ctx := context.WithValue(context.Background(), "gin", ginCtx)
		reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)
		if reporter.IsNativeKey() {
			t.Fatalf("reporter.IsNativeKey() = true, want false")
		}
		if reporter.AccessProvider() != "custom-auth-plugin" {
			t.Fatalf("reporter.AccessProvider() = %q, want %q", reporter.AccessProvider(), "custom-auth-plugin")
		}

		record := reporter.buildRecord(usage.Detail{TotalTokens: 10}, false, usage.Failure{})
		if record.IsNativeKey {
			t.Fatalf("record.IsNativeKey = true, want false")
		}
		if record.AccessProvider != "custom-auth-plugin" {
			t.Fatalf("record.AccessProvider = %q, want %q", record.AccessProvider, "custom-auth-plugin")
		}
	})

	t.Run("native_key_via_context", func(t *testing.T) {
		ctx := usage.WithAccessProvider(context.Background(), "config-api-key")
		reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)
		if !reporter.IsNativeKey() {
			t.Fatalf("reporter.IsNativeKey() = false, want true")
		}
		if reporter.AccessProvider() != "config-api-key" {
			t.Fatalf("reporter.AccessProvider() = %q, want %q", reporter.AccessProvider(), "config-api-key")
		}

		record := reporter.buildRecord(usage.Detail{TotalTokens: 10}, false, usage.Failure{})
		if !record.IsNativeKey {
			t.Fatalf("record.IsNativeKey = false, want true")
		}
		if record.AccessProvider != "config-api-key" {
			t.Fatalf("record.AccessProvider = %q, want %q", record.AccessProvider, "config-api-key")
		}
	})
}

func TestUsageReporterCapturesSelectionIDForDelayedPublish(t *testing.T) {
	ctx := usage.WithSelectionID(context.Background(), "sel-delayed-123")
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)
	if got := reporter.SelectionID(); got != "sel-delayed-123" {
		t.Fatalf("reporter.SelectionID() = %q, want %q", got, "sel-delayed-123")
	}

	record := reporter.buildRecord(usage.Detail{TotalTokens: 42}, false, usage.Failure{})
	if record.SelectionID != "sel-delayed-123" {
		t.Fatalf("record.SelectionID = %q, want %q", record.SelectionID, "sel-delayed-123")
	}
}

type streamTruncatedTestError struct {
	msg string
}

func (e *streamTruncatedTestError) Error() string           { return e.msg }
func (e *streamTruncatedTestError) StatusCode() int         { return 502 }
func (e *streamTruncatedTestError) IsStreamTruncated() bool { return true }

func TestFailFromErrorsCarriesStreamTruncatedCode(t *testing.T) {
	fail := failFromErrors(&streamTruncatedTestError{msg: "upstream stream closed before finishReason"})
	if fail.Code != usage.FailureStreamTruncatedCode {
		t.Fatalf("fail.Code = %q, want %q", fail.Code, usage.FailureStreamTruncatedCode)
	}
	if fail.StatusCode != 502 {
		t.Fatalf("fail.StatusCode = %d, want 502", fail.StatusCode)
	}

	plain := failFromErrors(errors.New("upstream stream closed before finishReason"))
	if plain.Code != "" {
		t.Fatalf("plain error must not gain truncated classification, got code %q", plain.Code)
	}
}

func TestParseClaudeUsageExtractsCacheTTLServerToolsIterationsAndCost(t *testing.T) {
	var buffer StreamUsageBuffer
	lineStart := []byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":4,"cache_creation_input_tokens":3036,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":3036},"output_tokens":16,"service_tier":"standard","inference_geo":"global","speed":"standard"}}}`)
	lineToolUse := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"anthropic"}}}`)
	lineToolResult := []byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://www.anthropic.com","title":"Anthropic"},{"type":"web_search_result","url":"https://docs.anthropic.com","title":"Docs"}]}}`)
	lineDelta := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":6,"cache_creation_input_tokens":8846,"cache_read_input_tokens":3036,"output_tokens":483,"output_tokens_details":{"thinking_tokens":45},"server_tool_use":{"web_search_requests":1,"web_fetch_requests":0},"iterations":[{"input_tokens":4,"output_tokens":53,"cache_read_input_tokens":0,"cache_creation_input_tokens":3036,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":3036},"type":"message","model":null},{"input_tokens":2,"output_tokens":430,"cache_read_input_tokens":3036,"cache_creation_input_tokens":5810,"cache_creation":{"ephemeral_5m_input_tokens":5810,"ephemeral_1h_input_tokens":0},"type":"message","model":null}],"cost":0.035471,"is_byok":false,"cost_details":{"upstream_inference_cost":0.035471,"upstream_inference_prompt_cost":0.023151,"upstream_inference_completions_cost":0.00232}}}`)

	buffer.ObserveClaudeStream(lineStart)
	buffer.ObserveClaudeStream(lineToolUse)
	buffer.ObserveClaudeStream(lineToolResult)
	buffer.ObserveClaudeStream(lineDelta)

	detail, ok := buffer.Detail()
	if !ok {
		t.Fatal("expected observed stream usage")
	}
	if detail.InputTokens != 6 || detail.CacheReadTokens != 3036 || detail.CacheCreationTokens != 8846 || detail.OutputTokens != 483 || detail.ReasoningTokens != 45 || detail.TotalTokens != 12371 {
		t.Fatalf("unexpected token totals: %+v", detail)
	}
	if detail.CacheCreation5mTokens == nil || *detail.CacheCreation5mTokens != 5810 {
		t.Fatalf("CacheCreation5mTokens = %v, want 5810", detail.CacheCreation5mTokens)
	}
	if detail.CacheCreation1hTokens == nil || *detail.CacheCreation1hTokens != 3036 {
		t.Fatalf("CacheCreation1hTokens = %v, want 3036", detail.CacheCreation1hTokens)
	}
	if detail.WebSearchRequests == nil || *detail.WebSearchRequests != 1 {
		t.Fatalf("WebSearchRequests = %v, want 1", detail.WebSearchRequests)
	}
	if detail.ToolUsageDetails == nil || detail.ToolUsageDetails.WebSearchCalls == nil || *detail.ToolUsageDetails.WebSearchCalls != 1 || detail.ToolUsageDetails.WebFetchCalls == nil || *detail.ToolUsageDetails.WebFetchCalls != 0 || detail.ToolUsageDetails.WebSearchQueries == nil || *detail.ToolUsageDetails.WebSearchQueries != 1 || detail.ToolUsageDetails.NumSearchResults == nil || *detail.ToolUsageDetails.NumSearchResults != 2 {
		t.Fatalf("ToolUsageDetails = %+v, want web_search=1 queries=1 results=2 web_fetch=0", detail.ToolUsageDetails)
	}
	iterations := detail.IterationSlice()
	if len(iterations) != 2 {
		t.Fatalf("len(iterations) = %d, want 2", len(iterations))
	}
	if iterations[0].CacheCreation1hTokens == nil || *iterations[0].CacheCreation1hTokens != 3036 || iterations[1].CacheCreation5mTokens == nil || *iterations[1].CacheCreation5mTokens != 5810 {
		t.Fatalf("iterations = %+v", iterations)
	}
	if detail.InferenceGeo != "global" || detail.Speed != "standard" || detail.UpstreamServiceTier != "standard" {
		t.Fatalf("geo=%q speed=%q tier=%q", detail.InferenceGeo, detail.Speed, detail.UpstreamServiceTier)
	}
	if detail.CostDetails == nil || detail.CostDetails.Cost == nil || *detail.CostDetails.Cost != 0.035471 || detail.CostDetails.IsBYOK == nil || *detail.CostDetails.IsBYOK != false {
		t.Fatalf("CostDetails = %+v", detail.CostDetails)
	}
}

func TestParseGeminiAndAntigravityUsageWithModalitiesAndTools(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"executableCode":{"language":"PYTHON","code":"print(1)"}},{"functionCall":{"name":"lookup","args":{"q":"a"}}},{"inlineData":{"mimeType":"image/png","data":"AA=="}}]},"groundingMetadata":{"webSearchQueries":["gemini pricing","vertex grounding"],"groundingChunks":[{"web":{"uri":"https://a.example"}},{"web":{"uri":"https://b.example"}},{"retrievedContext":{"uri":"gs://b/doc"}}]},"urlContextMetadata":{"urlMetadata":[{"retrievedUrl":"https://c.example"}]}}],"usageMetadata":{"promptTokenCount":1000,"cachedContentTokenCount":400,"candidatesTokenCount":300,"thoughtsTokenCount":120,"toolUsePromptTokenCount":50,"totalTokenCount":1470,"trafficType":"ON_DEMAND_PRIORITY","promptTokensDetails":[{"modality":"TEXT","tokenCount":800},{"modality":"IMAGE","tokenCount":150},{"modality":"DOCUMENT","tokenCount":50}],"cacheTokensDetails":[{"modality":"TEXT","tokenCount":400}],"candidatesTokensDetails":[{"modality":"TEXT","tokenCount":100},{"modality":"IMAGE","tokenCount":200}]}}`)
	detail := ParseGeminiUsage(payload)
	ApplyGeminiRequestImageConfig(&detail, []byte(`{"generationConfig":{"imageConfig":{"aspectRatio":"16:9","imageSize":"2K"}}}`))
	if detail.InputTokens != 1050 || detail.OutputTokens != 300 || detail.ReasoningTokens != 120 || detail.TotalTokens != 1470 {
		t.Fatalf("unexpected Gemini token totals: %+v", detail)
	}
	if detail.TextInputTokens == nil || *detail.TextInputTokens != 800 || detail.ImageInputTokens == nil || *detail.ImageInputTokens != 150 || detail.FileInputTokens == nil || *detail.FileInputTokens != 50 || detail.CachedTextTokens == nil || *detail.CachedTextTokens != 400 {
		t.Fatalf("unexpected Gemini input modalities: %+v", detail)
	}
	if detail.TextOutputTokens == nil || *detail.TextOutputTokens != 100 || detail.ImageOutputTokens == nil || *detail.ImageOutputTokens != 200 {
		t.Fatalf("unexpected Gemini output modalities: %+v", detail)
	}
	if detail.UpstreamServiceTier != "ON_DEMAND_PRIORITY" || detail.ResponseServiceTier != "ON_DEMAND_PRIORITY" {
		t.Fatalf("unexpected Gemini trafficType tier: upstream=%q response=%q", detail.UpstreamServiceTier, detail.ResponseServiceTier)
	}
	if detail.WebSearchRequests == nil || *detail.WebSearchRequests != 2 || detail.ToolUsageDetails == nil ||
		detail.ToolUsageDetails.WebSearchCalls == nil || *detail.ToolUsageDetails.WebSearchCalls != 2 ||
		detail.ToolUsageDetails.WebSearchQueries == nil || *detail.ToolUsageDetails.WebSearchQueries != 2 ||
		detail.ToolUsageDetails.NumSearchResults == nil || *detail.ToolUsageDetails.NumSearchResults != 2 ||
		detail.ToolUsageDetails.FileSearchCalls == nil || *detail.ToolUsageDetails.FileSearchCalls != 1 ||
		detail.ToolUsageDetails.WebFetchCalls == nil || *detail.ToolUsageDetails.WebFetchCalls != 1 ||
		detail.ToolUsageDetails.CodeExecutionCalls == nil || *detail.ToolUsageDetails.CodeExecutionCalls != 1 ||
		detail.ToolUsageDetails.ToolCallsRequested == nil || *detail.ToolUsageDetails.ToolCallsRequested != 1 ||
		detail.ToolUsageDetails.Extra["tool_use_prompt_tokens"] != 50 {
		t.Fatalf("unexpected Gemini ToolUsageDetails: %+v", detail.ToolUsageDetails)
	}
	if detail.ImageCount == nil || *detail.ImageCount != 1 || detail.ImageDetails == nil || detail.ImageDetails.MimeType != "image/png" {
		t.Fatalf("unexpected Gemini response ImageCount/ImageDetails: count=%+v details=%+v", detail.ImageCount, detail.ImageDetails)
	}
	if detail.RequestedImageDetails == nil || detail.RequestedImageDetails.AspectRatio != "16:9" || detail.RequestedImageDetails.ImageSize != "2K" {
		t.Fatalf("unexpected Gemini RequestedImageDetails: %+v", detail.RequestedImageDetails)
	}

	var streamBuf StreamUsageBuffer
	streamBuf.ObserveAntigravityStream([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"tool_a","args":{}}}]},"groundingMetadata":{"webSearchQueries":["q1"],"groundingChunks":[{"web":{"uri":"https://x.example"}}]}}]}}`))
	streamBuf.ObserveAntigravityStream([]byte(`data: {"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":30,"totalTokenCount":150}}}`))
	streamDetail, ok := streamBuf.Detail()
	if !ok {
		t.Fatal("expected streamBuf.Detail() ok = true")
	}
	if streamDetail.TotalTokens != 150 || streamDetail.WebSearchRequests == nil || *streamDetail.WebSearchRequests != 1 || streamDetail.ToolUsageDetails == nil || streamDetail.ToolUsageDetails.ToolCallsRequested == nil || *streamDetail.ToolUsageDetails.ToolCallsRequested != 1 {
		t.Fatalf("unexpected Antigravity stream merged usage: %+v tools=%+v", streamDetail, streamDetail.ToolUsageDetails)
	}
}

func TestParseCodexUsageWithCollectedOutputItemsAndImageToolUsage(t *testing.T) {
	completed := []byte(`{"type":"response.completed","response":{"id":"resp_codex_1","service_tier":"priority","prompt_cache_retention":"24h","output":[],"usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1024,"text_tokens":1100,"image_tokens":100,"cached_tokens_details":{"text_tokens":1024}},"output_tokens":350,"output_tokens_details":{"reasoning_tokens":200,"text_tokens":150},"total_tokens":1550,"attribution":{"tier":"priority"}},"tool_usage":{"image_gen":{"input_tokens":80,"input_tokens_details":{"text_tokens":30,"image_tokens":50},"output_tokens":1056,"output_tokens_details":{"image_tokens":1056,"text_tokens":0},"total_tokens":1136}}}}`)
	itemsByIndex := map[int64][]byte{
		0: []byte(`{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"openai responses usage","sources":[{"url":"https://a.example"},{"url":"https://b.example"}]}}`),
		1: []byte(`{"id":"ci_1","type":"code_interpreter_call","status":"completed"}`),
		2: []byte(`{"id":"ig_1","type":"image_generation_call","status":"completed","size":"1024x1024","quality":"high","background":"transparent","output_format":"png","result":"AA=="}`),
		3: []byte(`{"id":"fc_1","type":"function_call","status":"completed","name":"run_check","call_id":"call_1","arguments":"{}"}`),
	}
	detail, ok := ParseCodexUsageWithOutputItems(completed, itemsByIndex, nil)
	if !ok {
		t.Fatal("expected ParseCodexUsageWithOutputItems ok = true")
	}
	if detail.ResponseServiceTier != "priority" || detail.UpstreamServiceTier != "priority" {
		t.Fatalf("unexpected service tier: response=%q upstream=%q", detail.ResponseServiceTier, detail.UpstreamServiceTier)
	}
	if detail.CacheDetails == nil || detail.CacheDetails.Retention != "24h" {
		t.Fatalf("unexpected CacheDetails: %+v", detail.CacheDetails)
	}
	if detail.CachedTextTokens == nil || *detail.CachedTextTokens != 1024 {
		t.Fatalf("unexpected CachedTextTokens: %+v", detail.CachedTextTokens)
	}
	if detail.WebSearchRequests == nil || *detail.WebSearchRequests != 1 {
		t.Fatalf("unexpected WebSearchRequests: %+v", detail.WebSearchRequests)
	}
	if detail.ToolUsageDetails == nil ||
		detail.ToolUsageDetails.WebSearchCalls == nil || *detail.ToolUsageDetails.WebSearchCalls != 1 ||
		detail.ToolUsageDetails.WebSearchQueries == nil || *detail.ToolUsageDetails.WebSearchQueries != 1 ||
		detail.ToolUsageDetails.NumSourcesUsed == nil || *detail.ToolUsageDetails.NumSourcesUsed != 2 ||
		detail.ToolUsageDetails.CodeExecutionCalls == nil || *detail.ToolUsageDetails.CodeExecutionCalls != 1 ||
		detail.ToolUsageDetails.ImageGenerationCalls == nil || *detail.ToolUsageDetails.ImageGenerationCalls != 1 ||
		detail.ToolUsageDetails.ToolCallsRequested == nil || *detail.ToolUsageDetails.ToolCallsRequested != 1 {
		t.Fatalf("unexpected ToolUsageDetails: %+v", detail.ToolUsageDetails)
	}
	if detail.ImageCount != nil || detail.ImageDetails != nil {
		t.Fatalf("expected orchestrator record ImageCount/ImageDetails to be nil when tool_usage.image_gen is present, got count=%+v details=%+v", detail.ImageCount, detail.ImageDetails)
	}

	imgToolDetail, okImg := ParseCodexImageToolUsageWithOutputItems([]byte(`{"tools":[{"type":"image_generation","input_fidelity":"high"}]}`), completed, itemsByIndex, nil)
	if !okImg {
		t.Fatal("expected ParseCodexImageToolUsageWithOutputItems ok = true")
	}
	if imgToolDetail.TotalTokens != 1136 || imgToolDetail.TextInputTokens == nil || *imgToolDetail.TextInputTokens != 30 || imgToolDetail.ImageInputTokens == nil || *imgToolDetail.ImageInputTokens != 50 || imgToolDetail.ImageOutputTokens == nil || *imgToolDetail.ImageOutputTokens != 1056 {
		t.Fatalf("unexpected image tool token breakdown: %+v", imgToolDetail)
	}
	if imgToolDetail.ImageCount == nil || *imgToolDetail.ImageCount != 1 || imgToolDetail.ImageDetails == nil || imgToolDetail.ImageDetails.Size != "1024x1024" || imgToolDetail.ImageDetails.InputFidelity != "" {
		t.Fatalf("unexpected image tool response metadata: count=%+v details=%+v", imgToolDetail.ImageCount, imgToolDetail.ImageDetails)
	}
	if imgToolDetail.RequestedImageDetails == nil || imgToolDetail.RequestedImageDetails.InputFidelity != "high" {
		t.Fatalf("unexpected image tool requested metadata: %+v", imgToolDetail.RequestedImageDetails)
	}
}

func TestStreamUsageBufferObserveOpenAIStreamToolCallsAndImageCompleted(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.ObserveOpenAIStream([]byte(`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f1","arguments":""}}]}}]}`))
	buffer.ObserveOpenAIStream([]byte(`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}},{"index":1,"id":"call_2","type":"function","function":{"name":"f2","arguments":"{}"}}]}}]}`))
	buffer.ObserveOpenAIStream([]byte(`data: {"id":"chatcmpl-1","service_tier":"default","choices":[],"usage":{"prompt_tokens":50,"completion_tokens":20,"total_tokens":70}}`))
	detail, ok := buffer.Detail()
	if !ok {
		t.Fatal("expected buffer.Detail() ok = true")
	}
	if detail.TotalTokens != 70 || detail.ToolUsageDetails == nil || detail.ToolUsageDetails.ToolCallsRequested == nil || *detail.ToolUsageDetails.ToolCallsRequested != 2 {
		t.Fatalf("unexpected OpenAI stream tool call usage: %+v, tools=%+v", detail, detail.ToolUsageDetails)
	}

	var imgBuffer StreamUsageBuffer
	imgBuffer.ObserveOpenAIStream([]byte(`data: {"type":"image_generation.completed","b64_json":"AA==","size":"1024x1536","quality":"medium","background":"opaque","output_format":"webp","usage":{"input_tokens":25,"input_tokens_details":{"text_tokens":25,"image_tokens":0},"output_tokens":1500,"output_tokens_details":{"image_tokens":1500,"text_tokens":0},"total_tokens":1525}}`))
	imgDetail, okImg := imgBuffer.Detail()
	if !okImg {
		t.Fatal("expected imgBuffer.Detail() ok = true")
	}
	if imgDetail.TotalTokens != 1525 || imgDetail.ImageCount == nil || *imgDetail.ImageCount != 1 || imgDetail.ImageDetails == nil || imgDetail.ImageDetails.Size != "1024x1536" || imgDetail.ImageDetails.Quality != "medium" || imgDetail.ImageDetails.OutputFormat != "webp" {
		t.Fatalf("unexpected OpenAI image stream usage: %+v, details=%+v", imgDetail, imgDetail.ImageDetails)
	}
}

func TestParseCodexUsageExtractsXAIServerSideToolUsageAndModalities(t *testing.T) {
	data := []byte(`{"type":"response.completed","response":{"id":"resp_xai_1","usage":{"input_tokens":3804,"input_tokens_details":{"cached_tokens":3072,"text_tokens":3500,"image_tokens":304,"cached_prompt_text_tokens":3072},"output_tokens":889,"output_tokens_details":{"reasoning_tokens":828,"text_tokens":61,"accepted_prediction_tokens":5,"rejected_prediction_tokens":2},"total_tokens":4693,"num_sources_used":0,"num_server_side_tools_used":1,"cost_in_usd_ticks":76246640,"server_side_tool_usage_details":{"web_search_calls":0,"x_search_calls":1,"x_posts_fetched":1,"x_users_fetched":0,"code_interpreter_calls":0,"file_search_calls":0,"mcp_calls":0,"document_search_calls":0,"image_generation_calls":0}}}}`)
	detail, ok := ParseCodexUsage(data)
	if !ok {
		t.Fatal("expected ParseCodexUsage ok = true")
	}
	if detail.TextInputTokens == nil || *detail.TextInputTokens != 3500 || detail.ImageInputTokens == nil || *detail.ImageInputTokens != 304 || detail.CachedTextTokens == nil || *detail.CachedTextTokens != 3072 {
		t.Fatalf("unexpected input modality tokens: %+v", detail)
	}
	if detail.TextOutputTokens == nil || *detail.TextOutputTokens != 61 || detail.AcceptedPredictionTokens == nil || *detail.AcceptedPredictionTokens != 5 || detail.RejectedPredictionTokens == nil || *detail.RejectedPredictionTokens != 2 {
		t.Fatalf("unexpected output modality tokens: %+v", detail)
	}
	if detail.ToolUsageDetails == nil || detail.ToolUsageDetails.XSearchCalls == nil || *detail.ToolUsageDetails.XSearchCalls != 1 || detail.ToolUsageDetails.XPostsFetched == nil || *detail.ToolUsageDetails.XPostsFetched != 1 || detail.ToolUsageDetails.NumServerSideToolsUsed == nil || *detail.ToolUsageDetails.NumServerSideToolsUsed != 1 {
		t.Fatalf("unexpected ToolUsageDetails: %+v", detail.ToolUsageDetails)
	}
	if detail.CostDetails == nil || detail.CostDetails.CostInUSDTicks == nil || *detail.CostDetails.CostInUSDTicks != 76246640 {
		t.Fatalf("unexpected CostDetails: %+v", detail.CostDetails)
	}
}

func TestParseXAIImageAndVideoUsage(t *testing.T) {
	imgDetail := ParseXAIImageUsage(
		[]byte(`{"model":"grok-imagine-image","prompt":"cat","aspect_ratio":"16:9","resolution":"2k","quality":"medium"}`),
		[]byte(`{"data":[{"b64_json":"AA=="},{"b64_json":"BB=="}],"usage":{"prompt_tokens":12,"completion_tokens":100,"total_tokens":112}}`),
	)
	if imgDetail.TotalTokens != 112 || imgDetail.ImageCount == nil || *imgDetail.ImageCount != 2 {
		t.Fatalf("unexpected image usage: %+v", imgDetail)
	}
	if imgDetail.RequestedImageDetails == nil || imgDetail.RequestedImageDetails.AspectRatio != "16:9" || imgDetail.RequestedImageDetails.Resolution != "2k" || imgDetail.RequestedImageDetails.Quality != "medium" {
		t.Fatalf("unexpected requested image details: %+v", imgDetail.RequestedImageDetails)
	}

	vidDetail := ParseXAIVideoUsage(
		[]byte(`{"model":"grok-imagine-video","prompt":"wave","aspect_ratio":"16:9","resolution":"720p","duration":5}`),
		[]byte(`{"status":"completed","url":"https://example.com/v.mp4","video":{"resolution":"720p","aspect_ratio":"16:9"},"usage":{"prompt_tokens":20,"completion_tokens":400,"total_tokens":420}}`),
	)
	if vidDetail.TotalTokens != 420 || vidDetail.ImageDetails == nil || vidDetail.ImageDetails.Resolution != "720p" || vidDetail.ImageDetails.AspectRatio != "16:9" || vidDetail.RequestedImageDetails == nil || vidDetail.RequestedImageDetails.Resolution != "720p" {
		t.Fatalf("unexpected video usage: %+v", vidDetail)
	}
	if vidDetail.MediaCounts == nil || vidDetail.MediaCounts.CompletionMedia == nil || *vidDetail.MediaCounts.CompletionMedia != 1 || vidDetail.MediaCounts.AudioSeconds == nil || *vidDetail.MediaCounts.AudioSeconds != 5 {
		t.Fatalf("unexpected video media counts: %+v", vidDetail.MediaCounts)
	}
}

func TestStreamUsageBufferObserveClaudeStream_PreservesSingleTurnCacheCreationTTL(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-haiku-5-5","usage":{"input_tokens":16,"cache_creation_input_tokens":45045,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":45045},"output_tokens":4,"speed":"standard"}}}`))
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`))

	detail, ok := buffer.Detail()
	if !ok {
		t.Fatal("expected observed stream usage")
	}
	if detail.CacheCreation5mTokens == nil || *detail.CacheCreation5mTokens != 0 {
		t.Fatalf("CacheCreation5mTokens = %v, want explicit 0", detail.CacheCreation5mTokens)
	}
	if detail.CacheCreation1hTokens == nil || *detail.CacheCreation1hTokens != 45045 {
		t.Fatalf("CacheCreation1hTokens = %v, want 45045", detail.CacheCreation1hTokens)
	}
}

func TestClaudeStreamUsageOwnsStrings(t *testing.T) {
	line := []byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1,"speed":"standard","service_tier":"priority","inference_geo":"global","cache_creation":{"mode":"explicit","ttl":"2h","retention":"24h","ephemeral_2h_input_tokens":5},"server_tool_use":{"custom_tool_requests":2},"iterations":[{"type":"message","model":"claude-sonnet"}]}}}`)
	var b StreamUsageBuffer
	b.ObserveClaudeStream(line)
	for i := range line {
		line[i] = 'X'
	}
	d, ok := b.Detail()
	if !ok || d.ResponseServiceTier != "standard" || d.Speed != "standard" ||
		d.UpstreamServiceTier != "priority" || d.InferenceGeo != "global" {
		t.Fatalf("usage strings changed after input reuse: %+v", d)
	}
	iterations := d.IterationSlice()
	if len(iterations) != 1 || iterations[0].Type != "message" || iterations[0].Model != "claude-sonnet" {
		t.Fatalf("iteration strings changed after input reuse: %+v", iterations)
	}
	if d.CacheDetails == nil || d.CacheDetails.Mode != "explicit" || d.CacheDetails.TTL != "2h" ||
		d.CacheDetails.Retention != "24h" || d.CacheDetails.ByTTL["2h"] != 5 {
		t.Fatalf("cache strings changed after input reuse: %+v", d.CacheDetails)
	}
	if d.ToolUsageDetails == nil || d.ToolUsageDetails.Extra["custom_tool_requests"] != 2 {
		t.Fatalf("tool counter key changed after input reuse: %+v", d.ToolUsageDetails)
	}
}

func TestClaudeStreamUsageSurvivesScannerReuse(t *testing.T) {
	start := `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1,"speed":"standard","service_tier":"priority","inference_geo":"global"}}}`
	delta := `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"` + strings.Repeat("A", 100) + `"}}`
	stream := start + "\n" + strings.Repeat(delta+"\n", 100) + `data: {"type":"message_delta","usage":{"output_tokens":10}}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(stream))
	var b StreamUsageBuffer
	for scanner.Scan() {
		b.ObserveClaudeStream(scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	d, ok := b.Detail()
	if !ok || d.InputTokens != 10 || d.OutputTokens != 10 || d.Speed != "standard" ||
		d.UpstreamServiceTier != "priority" || d.InferenceGeo != "global" {
		t.Fatalf("scanner reuse corrupted usage: %+v", d)
	}
}

func TestClaudeStreamToolDeduplicationOwnsIDsAndCountsMultipleSearches(t *testing.T) {
	use1 := []byte(`data: {"type":"content_block_start","content_block":{"type":"server_tool_use","id":"tool_1","name":"web_search"}}`)
	result1 := []byte(`data: {"type":"content_block_start","content_block":{"type":"web_search_tool_result","tool_use_id":"tool_1","content":[]}}`)
	result2 := []byte(`data: {"type":"content_block_start","content_block":{"type":"web_search_tool_result","tool_use_id":"tool_2","content":[]}}`)
	var b StreamUsageBuffer
	for i := 0; i < 2; i++ {
		useCopy, resultCopy := bytes.Clone(use1), bytes.Clone(result1)
		b.ObserveClaudeStream(useCopy)
		b.ObserveClaudeStream(resultCopy)
		for j := range useCopy {
			useCopy[j] = 'X'
		}
		for j := range resultCopy {
			resultCopy[j] = 'X'
		}
	}
	b.ObserveClaudeStream(result2)
	d, ok := b.Detail()
	if !ok || d.ToolUsageDetails == nil || d.ToolUsageDetails.WebSearchQueries == nil ||
		*d.ToolUsageDetails.WebSearchQueries != 1 || d.ToolUsageDetails.WebSearchCalls == nil ||
		*d.ToolUsageDetails.WebSearchCalls != 2 || d.WebSearchRequests == nil || *d.WebSearchRequests != 2 {
		t.Fatalf("unexpected Claude stream search counts: detail=%+v tools=%+v", d, d.ToolUsageDetails)
	}
}

func TestGeminiStreamUsageAccumulatesToolExecutions(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrapped=%v", wrapped), func(t *testing.T) {
			var b StreamUsageBuffer
			observe := func(payload string) {
				if wrapped {
					b.ObserveAntigravityStream([]byte(`data: {"response":` + payload + `}`))
				} else {
					b.ObserveGeminiStream([]byte("data: " + payload))
				}
			}
			for _, name := range []string{"first", "second"} {
				observe(fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":%q,"args":{}}},{"executableCode":{"language":"PYTHON","code":"print(1)"}},{"inlineData":{"mimeType":"image/png","data":"AA=="}}]},"groundingMetadata":{"webSearchQueries":["q1","q2"]}}]}`, name))
				d, ok := b.Detail()
				if !ok || d.ToolUsageDetails == nil || d.ToolUsageDetails.ToolCallsRequested == nil ||
					d.ToolUsageDetails.CodeExecutionCalls == nil {
					t.Fatalf("missing incremental tool usage: %+v", d)
				}
				want := int64(1)
				if name == "second" {
					want = 2
				}
				if *d.ToolUsageDetails.ToolCallsRequested != want || *d.ToolUsageDetails.CodeExecutionCalls != want ||
					d.ImageCount == nil || *d.ImageCount != want {
					t.Fatalf("incremental counts: tools=%+v images=%v, want %d", d.ToolUsageDetails, d.ImageCount, want)
				}
			}
			observe(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12,"toolUsePromptTokenCount":1}}`)
			d, _ := b.Detail()
			if d.TotalTokens != 12 || *d.ToolUsageDetails.ToolCallsRequested != 2 ||
				*d.ToolUsageDetails.CodeExecutionCalls != 2 || *d.ImageCount != 2 ||
				d.WebSearchRequests == nil || *d.WebSearchRequests != 2 ||
				d.ToolUsageDetails.Extra["tool_use_prompt_tokens"] != 1 {
				t.Fatalf("terminal usage lost counts or double-counted grounding: %+v tools=%+v", d, d.ToolUsageDetails)
			}
		})
	}
}

func TestStripUsageMetadataPreservesToolArguments(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrapped=%v", wrapped), func(t *testing.T) {
			raw := `{"usageMetadata":{"promptTokenCount":5},"candidates":[{"content":{"parts":[{"functionCall":{"name":"save","args":{"usageMetadata":{"value":1}}}}]}}]}`
			prefix := ""
			if wrapped {
				raw = `{"response":` + raw + `}`
				prefix = "response."
			}
			out, changed := StripUsageMetadataFromJSON([]byte(raw))
			if !changed || !gjson.ValidBytes(out) || gjson.GetBytes(out, prefix+"usageMetadata").Exists() ||
				gjson.GetBytes(out, prefix+"cpaUsageMetadata.promptTokenCount").Int() != 5 ||
				gjson.GetBytes(out, prefix+"candidates.0.content.parts.0.functionCall.args.usageMetadata.value").Int() != 1 {
				t.Fatalf("metadata rename changed the wrong field: %s", out)
			}
		})
	}
}

func TestCodexUsageOwnsImageMetadataStrings(t *testing.T) {
	for _, imageTool := range []bool{false, true} {
		t.Run(fmt.Sprintf("image_tool=%v", imageTool), func(t *testing.T) {
			raw := []byte(`{"response":{"usage":{"input_tokens":1,"output_tokens":1,"server_tool_use":{"custom_tool_requests":3}},"prompt_cache_retention":"24h","tool_usage":{"image_gen":{"input_tokens":1,"output_tokens":2}},"output":[{"type":"image_generation_call","status":"completed","size":"1024x1024","quality":"high"}]}}`)
			var detailJSON []byte
			if !imageTool {
				detailJSON = bytes.ReplaceAll(raw, []byte(`"image_gen"`), []byte(`"other_tool"`))
			} else {
				detailJSON = raw
			}
			d, ok := ParseCodexUsage(detailJSON)
			if imageTool {
				d, ok = ParseCodexImageToolUsage(detailJSON)
			}
			for i := range detailJSON {
				detailJSON[i] = 'X'
			}
			if !ok || d.ImageDetails == nil || d.ImageDetails.Size != "1024x1024" || d.ImageDetails.Quality != "high" {
				t.Fatalf("image metadata aliases input buffer: %+v", d.ImageDetails)
			}
			if !imageTool && (d.CacheDetails == nil || d.CacheDetails.Retention != "24h" ||
				d.ToolUsageDetails == nil || d.ToolUsageDetails.Extra["custom_tool_requests"] != 3) {
				t.Fatalf("cache metadata or tool keys alias input buffer: %+v", d)
			}
		})
	}
}

func TestResponsesCacheWriteLifetimesAreReportedNotInferred(t *testing.T) {
	tests := []struct {
		name, body  string
		short, long *int64
	}{
		{name: "mixed", body: `{"response":{"prompt_cache_options":{"mode":"explicit","ttl":"12h"},"usage":{"input_tokens":100,"output_tokens":1,"total_tokens":101,"input_tokens_details":{"cache_write_tokens":70,"cache_write_tokens_details":{"ttl_30m_tokens":30,"ttl_12h_tokens":40}}}}}`, short: usage.Int64Ptr(30), long: usage.Int64Ptr(40)},
		{name: "explicit zero", body: `{"response":{"usage":{"input_tokens":100,"output_tokens":1,"input_tokens_details":{"cache_write_tokens":70,"cache_write_tokens_details":{"ttl_30m_tokens":0,"ttl_12h_tokens":70}}}}}`, short: usage.Int64Ptr(0), long: usage.Int64Ptr(70)},
		{name: "ttl does not imply bucket", body: `{"response":{"prompt_cache_options":{"mode":"explicit","ttl":"12h"},"usage":{"input_tokens":100,"output_tokens":1,"input_tokens_details":{"cache_write_tokens":70}}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := ParseCodexUsage([]byte(tt.body))
			if !ok {
				t.Fatal("missing usage")
			}
			check := func(name string, got, want *int64) {
				t.Helper()
				if (got == nil) != (want == nil) || (got != nil && *got != *want) {
					t.Errorf("%s got %v want %v", name, got, want)
				}
			}
			check("30m", d.CacheCreation30mTokens, tt.short)
			check("12h", d.CacheCreation12hTokens, tt.long)
			if d.CacheCreationTokens != 70 {
				t.Fatalf("cache total=%d", d.CacheCreationTokens)
			}
		})
	}
}

func TestOpenAIStreamToolCallsAcrossChoicesAndIncrementalChunks(t *testing.T) {
	var b StreamUsageBuffer
	b.ObserveOpenAIStream([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a"}]}}]}`))
	b.ObserveOpenAIStream([]byte(`data: {"choices":[{"index":1,"delta":{"tool_calls":[{"index":0,"id":"b"}]}}]}`))
	d, ok := b.Detail()
	if !ok || d.ToolUsageDetails == nil || d.ToolUsageDetails.ToolCallsRequested == nil || *d.ToolUsageDetails.ToolCallsRequested != 2 {
		t.Fatalf("partial tool calls = %+v, want 2", d.ToolUsageDetails)
	}
	b.ObserveOpenAIStream([]byte(`data: {"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	d, _ = b.Detail()
	if d.ToolUsageDetails == nil || d.ToolUsageDetails.ToolCallsRequested == nil || *d.ToolUsageDetails.ToolCallsRequested != 2 {
		t.Fatalf("terminal tool calls = %+v, want 2", d.ToolUsageDetails)
	}

	var inc StreamUsageBuffer
	inc.ObserveOpenAIStream([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0}]}}]}`))
	inc.ObserveOpenAIStream([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1}]}}]}`))
	dInc, _ := inc.Detail()
	if dInc.ToolUsageDetails == nil || dInc.ToolUsageDetails.ToolCallsRequested == nil || *dInc.ToolUsageDetails.ToolCallsRequested != 2 {
		t.Fatalf("incremental tool calls = %+v, want 2", dInc.ToolUsageDetails)
	}
}

func TestOpenAIStreamMultipleImageCompletionsWithoutUsage(t *testing.T) {
	var b StreamUsageBuffer
	b.ObserveOpenAIStream([]byte(`data: {"type":"image_generation.completed","b64_json":"AA==","size":"1024x1024"}`))
	b.ObserveOpenAIStream([]byte(`data: {"type":"image_generation.completed","b64_json":"BB==","size":"1024x1536"}`))
	d, ok := b.Detail()
	if !ok || d.ImageCount == nil || *d.ImageCount != 2 || d.ImageDetails == nil || d.ImageDetails.Size != "1024x1536" {
		t.Fatalf("image stream detail = %+v details=%+v, want ImageCount=2 and latest size", d, d.ImageDetails)
	}
}

func TestGeminiPartialArgsDeduplicationAndScannerReuse(t *testing.T) {
	combined := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"Par","willContinue":true}],"willContinue":true}},{"functionCall":{"partialArgs":[{"jsonPath":"$.city","stringValue":"is"}],"willContinue":true}},{"functionCall":{}},{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"Lyon"}],"willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`)
	d := ParseGeminiUsage(combined)
	if d.ToolUsageDetails == nil || d.ToolUsageDetails.ToolCallsRequested == nil || *d.ToolUsageDetails.ToolCallsRequested != 2 {
		t.Fatalf("direct ParseGeminiUsage ToolCallsRequested = %+v, want 2", d.ToolUsageDetails)
	}

	var b StreamUsageBuffer
	for _, chunk := range []string{
		`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"Tok","willContinue":true}],"willContinue":true}}]}}]}`,
		`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city","stringValue":"yo","willContinue":true}],"willContinue":true}}]}}]}`,
		`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city"},{"jsonPath":"$.days","numberValue":0}],"willContinue":true}}]}}]}`,
		`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"Os","willContinue":true}],"willContinue":true}}]}}]}`,
		`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city","stringValue":"aka"}]}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`,
	} {
		b.ObserveGeminiStream([]byte(chunk))
	}
	dStream, ok := b.Detail()
	if !ok || dStream.ToolUsageDetails == nil || dStream.ToolUsageDetails.ToolCallsRequested == nil || *dStream.ToolUsageDetails.ToolCallsRequested != 2 {
		t.Fatalf("stream ToolCallsRequested = %+v, want 2", dStream.ToolUsageDetails)
	}

	first := `data: {"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"name":"weather","partialArgs":[{"jsonPath":"$.city","stringValue":"Paris"}],"willContinue":true}}]}}]}`
	filler := `data: {"candidates":[{"content":{"parts":[{"text":"` + strings.Repeat("X", 120) + `"}]}}]}`
	last := `data: {"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"name":"weather","partialArgs":[{"jsonPath":"$.city","stringValue":"London"}],"willContinue":false}}]}}]}`
	scanner := bufio.NewScanner(strings.NewReader(first + "\n" + strings.Repeat(filler+"\n", 100) + last + "\n"))
	var scannerBuf StreamUsageBuffer
	for scanner.Scan() {
		scannerBuf.ObserveGeminiStream(scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	dScanner, ok := scannerBuf.Detail()
	if !ok || dScanner.ToolUsageDetails == nil || dScanner.ToolUsageDetails.ToolCallsRequested == nil || *dScanner.ToolUsageDetails.ToolCallsRequested != 2 {
		t.Fatalf("scanner reuse ToolCallsRequested = %+v, want 2", dScanner.ToolUsageDetails)
	}
}

func TestGeminiStreamUsageMergePreservesEarlierModalitiesAndTier(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(map[bool]string{false: "gemini", true: "antigravity"}[wrapped], func(t *testing.T) {
			var b StreamUsageBuffer
			observe := func(s string) {
				if wrapped {
					b.ObserveAntigravityStream([]byte(`data: {"response":` + s + `}`))
				} else {
					b.ObserveGeminiStream([]byte("data: " + s))
				}
			}
			observe(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11,"trafficType":"ON_DEMAND","promptTokensDetails":[{"modality":"IMAGE","tokenCount":10}]}}`)
			observe(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}`)
			d, _ := b.Detail()
			if d.ImageInputTokens == nil || *d.ImageInputTokens != 10 || d.UpstreamServiceTier != "ON_DEMAND" {
				t.Fatalf("lost structured usage: %+v", d)
			}
		})
	}
}

func TestCodexCustomToolCallWithoutXSearchPrefixCountsAsRequestedToolCall(t *testing.T) {
	d, ok := ParseCodexUsage([]byte(`{"response":{"usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"custom_tool_call","status":"completed","name":"x_keyword_search","call_id":"call_local_1"}]}}`))
	if !ok || d.ToolUsageDetails == nil || d.ToolUsageDetails.ToolCallsRequested == nil || *d.ToolUsageDetails.ToolCallsRequested != 1 {
		t.Fatalf("misclassified client custom_tool_call: %+v", d.ToolUsageDetails)
	}
}

func TestClaudeStreamProvisionalZeroWebSearchDoesNotFreezeCompletedSearches(t *testing.T) {
	for _, initial := range []string{"", `,"server_tool_use":{"web_search_requests":0}`} {
		t.Run(fmt.Sprintf("initial_zero=%v", initial != ""), func(t *testing.T) {
			var b StreamUsageBuffer
			b.ObserveClaudeStream([]byte(fmt.Sprintf(`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1%s}}}`, initial)))
			for i := 1; i <= 2; i++ {
				b.ObserveClaudeStream([]byte(fmt.Sprintf(`data: {"type":"content_block_start","content_block":{"type":"web_search_tool_result","tool_use_id":"tool_%d","content":[]}}`, i)))
			}
			b.ObserveClaudeStream([]byte(`data: {"type":"message_delta","usage":{"output_tokens":20}}`))
			d, ok := b.Detail()
			if !ok || d.WebSearchRequests == nil || *d.WebSearchRequests != 2 ||
				d.ToolUsageDetails == nil || d.ToolUsageDetails.WebSearchCalls == nil || *d.ToolUsageDetails.WebSearchCalls != 2 {
				t.Fatalf("expected 2 completed searches, got detail=%+v tools=%+v", d, d.ToolUsageDetails)
			}
		})
	}
}

func TestOpenAIStreamPreservesCacheTTLBucketsAndStructuredOnlyUsage(t *testing.T) {
	var cacheBuf StreamUsageBuffer
	cacheBuf.ObserveOpenAIStream([]byte(`data: {"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,"prompt_tokens_details":{"cache_write_tokens":70,"cache_write_tokens_details":{"ttl_30m_tokens":30,"ttl_12h_tokens":40}}}}`))
	cacheDetail, ok := cacheBuf.Detail()
	if !ok || cacheDetail.CacheCreation30mTokens == nil || *cacheDetail.CacheCreation30mTokens != 30 ||
		cacheDetail.CacheCreation12hTokens == nil || *cacheDetail.CacheCreation12hTokens != 40 {
		t.Fatalf("stream lost cache TTL buckets: %+v", cacheDetail)
	}

	var costBuf StreamUsageBuffer
	costBuf.ObserveOpenAIStream([]byte(`data: {"usage":{"cost":0.25,"cost_in_usd_ticks":2500000000,"server_tool_use":{"web_search_requests":2}}}`))
	costDetail, ok := costBuf.Detail()
	if !ok || costDetail.CostDetails == nil || costDetail.CostDetails.Cost == nil || *costDetail.CostDetails.Cost != 0.25 ||
		costDetail.WebSearchRequests == nil || *costDetail.WebSearchRequests != 2 {
		t.Fatalf("stream lost structured-only usage: %+v", costDetail)
	}
}

func TestObserveResponsesStreamPreservesOutputItemDoneToolUsageWhenCompletedOutputEmpty(t *testing.T) {
	var b StreamUsageBuffer
	b.ObserveResponsesStream([]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"ct_1","type":"custom_tool_call","status":"completed","call_id":"xs_1","name":"x_keyword_search","input":"{}"}}`))
	b.ObserveResponsesStream([]byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"lookup","arguments":"{}"}}`))
	b.ObserveResponsesStream([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","output":[],"usage":{"input_tokens":20,"output_tokens":10,"total_tokens":30}}}`))
	d, ok := b.Detail()
	if !ok || d.ToolUsageDetails == nil ||
		d.ToolUsageDetails.XSearchCalls == nil || *d.ToolUsageDetails.XSearchCalls != 1 ||
		d.ToolUsageDetails.ToolCallsRequested == nil || *d.ToolUsageDetails.ToolCallsRequested != 1 {
		t.Fatalf("ObserveResponsesStream lost output_item.done tool usage: %+v", d.ToolUsageDetails)
	}

	var pluginBuf StreamUsageBuffer
	ObservePluginExecutorStreamUsage("openai-response", []byte(strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"lookup","arguments":"{}"}}`,
		"",
		`data: {"type":"response.completed","response":{"output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
	}, "\n")), &pluginBuf)
	pd, pok := pluginBuf.Detail()
	if !pok || pd.ToolUsageDetails == nil || pd.ToolUsageDetails.ToolCallsRequested == nil || *pd.ToolUsageDetails.ToolCallsRequested != 1 {
		t.Fatalf("ObservePluginExecutorStreamUsage lost output_item.done tool usage: %+v", pd.ToolUsageDetails)
	}
}
