package helps

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func usageLabelContext(apiKey, label string) context.Context {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	if apiKey != "" {
		ginCtx.Set("userApiKey", apiKey)
	}
	if label != "" {
		ginCtx.Set("userApiKeyLabel", label)
	}
	ginCtx.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestNewUsageReporterPrefersAPIKeyLabelForAttribution(t *testing.T) {
	tests := []struct {
		name   string
		apiKey string
		label  string
		want   string
	}{
		{name: "label wins when present", apiKey: "sk-raw-key", label: "alice", want: "alice"},
		{name: "raw key used without label", apiKey: "sk-raw-key", want: "sk-raw-key"},
		{name: "blank label falls back to raw key", apiKey: "sk-raw-key", label: "   ", want: "sk-raw-key"},
		{name: "no credentials yields empty attribution"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := usageLabelContext(tt.apiKey, tt.label)
			reporter := NewUsageReporter(ctx, "codex", "gpt-5.4", nil)
			record := reporter.buildRecord(usage.Detail{}, false)
			if record.APIKey != tt.want {
				t.Fatalf("record.APIKey = %q, want %q", record.APIKey, tt.want)
			}
			if record.Source != tt.want {
				t.Fatalf("record.Source = %q, want %q", record.Source, tt.want)
			}
			// The raw key stays reachable for authorization and cache scoping.
			if got := APIKeyFromContext(ctx); got != tt.apiKey {
				t.Fatalf("APIKeyFromContext() = %q, want %q", got, tt.apiKey)
			}
		})
	}
}
