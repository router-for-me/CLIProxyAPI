package helps

import (
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
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

func TestParseOpenAIStreamUsageChatCompletions(t *testing.T) {
	line := []byte(`data: {"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail, ok := ParseOpenAIStreamUsage(line)
	if !ok {
		t.Fatal("expected usage to be parsed")
	}
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

func TestParseOpenAIStreamUsageResponses(t *testing.T) {
	line := []byte(`data: {"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail, ok := ParseOpenAIStreamUsage(line)
	if !ok {
		t.Fatal("expected usage to be parsed")
	}
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

func TestParseOpenAIStreamUsageNullUsageIgnored(t *testing.T) {
	line := []byte(`data: {"choices":[{"delta":{"content":"hi"}}],"usage":null}`)
	_, ok := ParseOpenAIStreamUsage(line)
	if ok {
		t.Fatal("expected usage:null chunk to be ignored")
	}
}

func TestParseOpenAIStreamUsageEmptyUsageObjectIgnored(t *testing.T) {
	line := []byte(`data: {"choices":[{"delta":{"content":"hi"}}],"usage":{}}`)
	_, ok := ParseOpenAIStreamUsage(line)
	if ok {
		t.Fatal("expected usage:{} chunk to be ignored")
	}
}

func TestParseOpenAIStreamUsageZeroUsageObjectIgnored(t *testing.T) {
	line := []byte(`data: {"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
	_, ok := ParseOpenAIStreamUsage(line)
	if ok {
		t.Fatal("expected all-zero usage chunk to be ignored")
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

func TestResolveUsageAPIKeyPrefersClientAPIKeyWhenPresent(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider:   "gemini",
		Attributes: map[string]string{"api_key": "upstream-key"},
	}

	apiKey := resolveUsageAPIKey(auth, "client-key")
	if apiKey != "client-key" {
		t.Fatalf("api key = %q, want client-key", apiKey)
	}
}

func TestResolveUsageAPIKeyFallsBackToUpstreamAPIKeyAuth(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider:   "gemini",
		Attributes: map[string]string{"api_key": "upstream-key"},
	}

	apiKey := resolveUsageAPIKey(auth, "")
	if apiKey != "upstream-key" {
		t.Fatalf("api key = %q, want upstream-key", apiKey)
	}
}

func TestResolveUsageAPIKeyFallsBackToContextForOAuth(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "gemini-cli",
		Metadata: map[string]any{"email": "user@example.com"},
	}

	apiKey := resolveUsageAPIKey(auth, "client-key")
	if apiKey != "client-key" {
		t.Fatalf("api key = %q, want client-key", apiKey)
	}
}
