package helps

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
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

func TestParseUsageThinkingFromRequest(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantIntensity string
		wantMode      string
		wantLevel     string
		wantBudget    int
	}{
		{
			name:          "codex effort",
			body:          `{"reasoning":{"effort":"high"}}`,
			wantIntensity: "high",
			wantMode:      "level",
			wantLevel:     "high",
		},
		{
			name:          "gemini budget",
			body:          `{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`,
			wantIntensity: "medium",
			wantMode:      "budget",
			wantLevel:     "medium",
			wantBudget:    8192,
		},
		{
			name:          "gemini cli auto",
			body:          `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}}`,
			wantIntensity: "auto",
			wantMode:      "auto",
			wantLevel:     "auto",
			wantBudget:    -1,
		},
		{
			name:          "gemini custom budget",
			body:          `{"generationConfig":{"thinkingConfig":{"thinkingBudget":-2}}}`,
			wantIntensity: "",
			wantMode:      "budget",
			wantLevel:     "",
			wantBudget:    -2,
		},
		{
			name:          "claude disabled",
			body:          `{"thinking":{"type":"disabled"}}`,
			wantIntensity: "none",
			wantMode:      "none",
			wantLevel:     "none",
		},
		{
			name:          "claude adaptive effort",
			body:          `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`,
			wantIntensity: "max",
			wantMode:      "level",
			wantLevel:     "max",
		},
		{
			name:          "claude adaptive",
			body:          `{"thinking":{"type":"adaptive"}}`,
			wantIntensity: "auto",
			wantMode:      "auto",
			wantLevel:     "auto",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseUsageThinkingFromRequest([]byte(tt.body))
			if got.Intensity != tt.wantIntensity {
				t.Fatalf("intensity = %q, want %q", got.Intensity, tt.wantIntensity)
			}
			if got.Mode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", got.Mode, tt.wantMode)
			}
			if got.Level != tt.wantLevel {
				t.Fatalf("level = %q, want %q", got.Level, tt.wantLevel)
			}
			if got.Budget != tt.wantBudget {
				t.Fatalf("budget = %d, want %d", got.Budget, tt.wantBudget)
			}
		})
	}
}

func TestUsageReporterBuildRecordIncludesThinkingFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	CaptureUsageThinkingFromRequest(ctx, []byte(`{"reasoning_effort":"medium"}`))

	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: time.Now(),
	}
	reporter.captureThinkingFromContext(ctx)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Thinking == nil {
		t.Fatal("thinking metadata is nil, want populated")
	}
	if record.Thinking.Intensity != "medium" {
		t.Fatalf("intensity = %q, want medium", record.Thinking.Intensity)
	}
	if record.Thinking.Mode != "level" {
		t.Fatalf("mode = %q, want level", record.Thinking.Mode)
	}
	if record.Thinking.Level != "medium" {
		t.Fatalf("level = %q, want medium", record.Thinking.Level)
	}
}

func TestCaptureUsageThinkingFromRequestKeepsExistingOnEmptyBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	CaptureUsageThinkingFromRequest(ctx, []byte(`{"reasoning_effort":"high"}`))
	CaptureUsageThinkingFromRequest(ctx, nil)

	got := UsageThinkingFromContext(ctx)
	if got == nil {
		t.Fatal("thinking metadata is nil, want existing metadata")
	}
	if got.Intensity != "high" {
		t.Fatalf("intensity = %q, want high", got.Intensity)
	}
}
