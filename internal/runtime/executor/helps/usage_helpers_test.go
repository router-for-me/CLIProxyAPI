package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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

func TestParseGeminiCLIUsage_TopLevelUsageMetadata(t *testing.T) {
	data := []byte(`{"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"thoughtsTokenCount":3,"totalTokenCount":21,"cachedContentTokenCount":5}}`)
	detail := ParseGeminiCLIUsage(data)
	if detail.InputTokens != 11 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 11)
	}
	if detail.OutputTokens != 7 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 7)
	}
	if detail.ReasoningTokens != 3 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 3)
	}
	if detail.TotalTokens != 21 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 21)
	}
	if detail.CachedTokens != 5 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 5)
	}
}

func TestParseGeminiCLIStreamUsage_ResponseSnakeCaseUsageMetadata(t *testing.T) {
	line := []byte(`data: {"response":{"usage_metadata":{"promptTokenCount":13,"candidatesTokenCount":2,"totalTokenCount":15}}}`)
	detail, ok := ParseGeminiCLIStreamUsage(line)
	if !ok {
		t.Fatal("ParseGeminiCLIStreamUsage() ok = false, want true")
	}
	if detail.InputTokens != 13 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 13)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.TotalTokens != 15 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 15)
	}
}

func TestParseGeminiCLIStreamUsage_IgnoresTrafficTypeOnlyUsageMetadata(t *testing.T) {
	line := []byte(`data: {"response":{"usageMetadata":{"trafficType":"ON_DEMAND"}}}`)
	if detail, ok := ParseGeminiCLIStreamUsage(line); ok {
		t.Fatalf("ParseGeminiCLIStreamUsage() = (%+v, true), want false for traffic-only usage metadata", detail)
	}
}

func TestUsageReporterBuildRecordIncludesLatency(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: time.Now().Add(-1500 * time.Millisecond),
	}

	record := reporter.buildRecord(context.Background(), usage.Detail{TotalTokens: 3}, false)
	if record.Latency < time.Second {
		t.Fatalf("latency = %v, want >= 1s", record.Latency)
	}
	if record.Latency > 3*time.Second {
		t.Fatalf("latency = %v, want <= 3s", record.Latency)
	}
}

func TestUsageReporterBuildRecordIncludesRequestedModelAlias(t *testing.T) {
	ctx := usage.WithRequestedModelAlias(context.Background(), "client-gpt")
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(ctx, usage.Detail{TotalTokens: 3}, false)
	if record.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want %q", record.Model, "gpt-5.4")
	}
	if record.Alias != "client-gpt" {
		t.Fatalf("alias = %q, want %q", record.Alias, "client-gpt")
	}
}

func TestUsageReporterUsesRecordedFinalRequestBodyWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "http://example.com/v1/chat/completions", nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	reporter := NewUsageReporter(ctx, "openai-compatibility", "gpt-5.4(high)", nil)
	RecordAPIRequest(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, UpstreamRequestLog{
		URL:      "https://example.com/v1/chat/completions",
		Method:   http.MethodPost,
		Body:     []byte(`{"reasoning_effort":"low"}`),
		Provider: "openai-compatibility",
	})

	record := reporter.buildRecord(ctx, usage.Detail{TotalTokens: 3}, false)
	if record.ThinkingEffort != "low" {
		t.Fatalf("thinking effort = %q, want low", record.ThinkingEffort)
	}
	if _, exists := ginCtx.Get(apiRequestKey); exists {
		t.Fatalf("request log was written even though RequestLog is disabled")
	}
}

func TestUsageReporterInfersOpenAIResponsesThinkingFormatFromRequestURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "responses",
			url:  "https://example.com/v1/responses",
			want: "medium",
		},
		{
			name: "responses compact",
			url:  "https://example.com/v1/responses/compact",
			want: "medium",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ginCtx.Request = httptest.NewRequest(http.MethodPost, tt.url, nil)
			ctx := context.WithValue(context.Background(), "gin", ginCtx)

			reporter := NewUsageReporter(ctx, "openai-compatibility", "gpt-5.4(high)", nil)
			RecordAPIRequest(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, UpstreamRequestLog{
				URL:      tt.url,
				Method:   http.MethodPost,
				Body:     []byte(`{"reasoning":{"effort":"medium"}}`),
				Provider: "openai-compatibility",
			})

			record := reporter.buildRecord(ctx, usage.Detail{TotalTokens: 3}, false)
			if record.ThinkingEffort != tt.want {
				t.Fatalf("thinking effort = %q, want %q", record.ThinkingEffort, tt.want)
			}
		})
	}
}

func TestUsageReporterFallsBackToModelSuffixWithoutRecordedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "http://example.com/v1/chat/completions", nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	reporter := NewUsageReporter(ctx, "openai-compatibility", "gpt-5.4(high)", nil)

	record := reporter.buildRecord(ctx, usage.Detail{TotalTokens: 3}, false)
	if record.ThinkingEffort != "high" {
		t.Fatalf("thinking effort = %q, want high", record.ThinkingEffort)
	}
}

func TestUsageReporterFallsBackToExplicitCaptureWhenRecordedBodyHasNoEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "http://example.com/v1/chat/completions", nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	reporter := NewUsageReporter(ctx, "openai-compatibility", "gpt-5.4(high)", nil)
	reporter.CaptureThinkingEffort([]byte(`{"reasoning_effort":"low"}`), "gpt-5.4", "openai", "openai")
	RecordAPIRequest(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, UpstreamRequestLog{
		URL:      "https://example.com/v1/chat/completions",
		Method:   http.MethodPost,
		Body:     []byte(`{}`),
		Provider: "openai-compatibility",
	})

	record := reporter.buildRecord(ctx, usage.Detail{TotalTokens: 3}, false)
	if record.ThinkingEffort != "low" {
		t.Fatalf("thinking effort = %q, want low", record.ThinkingEffort)
	}
}

func TestUsageReporterCapturesAntigravityThinkingEffortFromRecordedFinalBodyWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "http://example.com/v1beta/models/gemini-2.5-pro:generateContent", nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	reporter := NewUsageReporter(ctx, "antigravity", "gemini-2.5-pro", nil)
	RecordAPIRequest(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, UpstreamRequestLog{
		URL:      "https://example.com/v1beta/models/gemini-2.5-pro:generateContent",
		Method:   http.MethodPost,
		Body:     []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":1234}}}}`),
		Provider: "antigravity",
	})

	record := reporter.buildRecord(ctx, usage.Detail{TotalTokens: 3}, false)
	if record.ThinkingEffort != "budget:1234" {
		t.Fatalf("thinking effort = %q, want budget:1234", record.ThinkingEffort)
	}
	if _, exists := ginCtx.Get(apiRequestKey); exists {
		t.Fatalf("request log was written even though RequestLog is disabled")
	}
}

func TestRecordAPIRequestNilContextWithRequestLogDisabledDoesNotPanic(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("RecordAPIRequest panicked with nil context: %v", recovered)
		}
	}()

	RecordAPIRequest(nil, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, UpstreamRequestLog{
		Body: []byte(`{"reasoning_effort":"low"}`),
	})
}

func TestUsageReporterBuildRecordNilContextFallsBackToModelSuffix(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "openai", "gpt-5.4(high)", nil)

	var record usage.Record
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("buildRecord panicked with nil context: %v", recovered)
		}
	}()

	record = reporter.buildRecord(nil, usage.Detail{TotalTokens: 1}, false)
	if record.ThinkingEffort != "high" {
		t.Fatalf("thinking effort = %q, want high", record.ThinkingEffort)
	}
}

func TestUsageReporterBuildAdditionalModelRecordSkipsZeroTokens(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "codex",
		model:       "gpt-5.4",
		requestedAt: time.Now(),
	}

	if _, ok := reporter.buildAdditionalModelRecord(context.Background(), "gpt-image-2", usage.Detail{}); ok {
		t.Fatalf("expected all-zero token usage to be skipped")
	}
	if _, ok := reporter.buildAdditionalModelRecord(context.Background(), "gpt-image-2", usage.Detail{InputTokens: 2}); !ok {
		t.Fatalf("expected non-zero input token usage to be recorded")
	}
	if _, ok := reporter.buildAdditionalModelRecord(context.Background(), "gpt-image-2", usage.Detail{CachedTokens: 2}); !ok {
		t.Fatalf("expected non-zero cached token usage to be recorded")
	}
}

func TestUsageReporterBuildRecordIncludesFirstByteLatencyAndThinkingEffort(t *testing.T) {
	responseTime := time.Date(2026, 5, 2, 12, 0, 0, 250*int(time.Millisecond), time.UTC)
	reporter := &UsageReporter{
		provider:       "openai",
		model:          "gpt-5.4",
		requestedAt:    responseTime.Add(-250 * time.Millisecond),
		thinkingEffort: "high",
	}
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Set("API_RESPONSE_TIMESTAMP", responseTime)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	record := reporter.buildRecord(ctx, usage.Detail{TotalTokens: 3}, false)
	if record.FirstByteLatency != 250*time.Millisecond {
		t.Fatalf("first byte latency = %v, want 250ms", record.FirstByteLatency)
	}
	if record.ThinkingEffort != "high" {
		t.Fatalf("thinking effort = %q, want high", record.ThinkingEffort)
	}
}

func TestUsageReporterCaptureThinkingEffortOnlyRunsOnceForEmptyResult(t *testing.T) {
	reporter := &UsageReporter{}

	reporter.CaptureThinkingEffort(nil, "model", "openai", "openai")
	reporter.CaptureThinkingEffort(nil, "gpt-5.4(high)", "openai", "openai")

	if reporter.thinkingEffort != "" {
		t.Fatalf("thinking effort = %q, want empty after first empty extraction", reporter.thinkingEffort)
	}
}
