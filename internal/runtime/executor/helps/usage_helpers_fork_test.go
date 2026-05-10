package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

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
