package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestNewUsageReporterStampsEntryProviderKey guards the provider-budget
// attribution chain: when the selected upstream auth carries the synthesizer's
// entry_provider_key attribute (<provider-key>:key-<entryID>), the usage
// reporter must stamp it onto every record it publishes so the flusher can
// persist it on usage_events and the provider-budget query can attribute spend
// to the exact upstream API-key entry that served the request. Without this
// stamp the record carries an empty EntryProviderKey and per-entry budget
// tracking silently reads zero spend.
func TestNewUsageReporterStampsEntryProviderKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	auth := &cliproxyauth.Auth{
		Provider: "openai-compatible-akf-zhipu",
		Attributes: map[string]string{
			cliproxyauth.AttributeEntryProviderKey: "openai-compatible-akf-zhipu:key-91",
		},
	}
	reporter := NewUsageReporter(ctx, "openai-compatible-akf-zhipu", "glm-5.2", auth)
	record := reporter.buildRecord(ctx, usage.Detail{InputTokens: 10, OutputTokens: 5}, false)
	if record.EntryProviderKey != "openai-compatible-akf-zhipu:key-91" {
		t.Fatalf("EntryProviderKey = %q; want %q", record.EntryProviderKey, "openai-compatible-akf-zhipu:key-91")
	}

	// Auth without the attribute (legacy YAML entries, oauth channels) must
	// leave the field empty rather than fabricating an identity.
	plain := NewUsageReporter(ctx, "claude", "claude-opus", &cliproxyauth.Auth{Provider: "claude"})
	record2 := plain.buildRecord(ctx, usage.Detail{InputTokens: 1}, false)
	if record2.EntryProviderKey != "" {
		t.Fatalf("EntryProviderKey = %q; want empty for auth without the attribute", record2.EntryProviderKey)
	}
}
