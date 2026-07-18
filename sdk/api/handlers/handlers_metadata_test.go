package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"golang.org/x/net/context"
)

func TestRequestExecutionMetadataIncludesExecutionSessionWithoutIdempotencyKey(t *testing.T) {
	ctx := WithExecutionSessionID(context.Background(), "session-1")

	meta := requestExecutionMetadata(ctx)
	if got := meta[coreexecutor.ExecutionSessionMetadataKey]; got != "session-1" {
		t.Fatalf("ExecutionSessionMetadataKey = %v, want %q", got, "session-1")
	}
	if _, ok := meta[idempotencyKeyMetadataKey]; ok {
		t.Fatalf("unexpected idempotency key in metadata: %v", meta[idempotencyKeyMetadataKey])
	}
}

func TestRequestExecutionMetadataTraceCallbackWebsocketDetection(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("skips websocket upgrade", func(t *testing.T) {
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		ginCtx.Request.Header.Set("Connection", "Upgrade")
		ginCtx.Request.Header.Set("Upgrade", "websocket")
		logging.SetGinRequestID(ginCtx, "1234abcd")
		ctx := context.WithValue(context.Background(), "gin", ginCtx)

		meta := requestExecutionMetadata(ctx)

		if _, exists := meta[coreexecutor.SelectedAuthIndexCallbackMetadataKey]; exists {
			t.Fatal("unexpected selected auth index callback for websocket upgrade")
		}
	})

	t.Run("keeps callback for incomplete upgrade headers", func(t *testing.T) {
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		ginCtx.Request.Header.Set("Upgrade", "websocket")
		logging.SetGinRequestID(ginCtx, "1234abcd")
		ctx := context.WithValue(context.Background(), "gin", ginCtx)

		meta := requestExecutionMetadata(ctx)

		if _, exists := meta[coreexecutor.SelectedAuthIndexCallbackMetadataKey]; !exists {
			t.Fatal("missing selected auth index callback for ordinary HTTP request")
		}
	})
}

func TestSetReasoningEffortMetadataUsesSuffixOverBody(t *testing.T) {
	meta := make(map[string]any)

	setReasoningEffortMetadata(meta, "openai", "gpt-5.4(high)", []byte(`{"reasoning_effort":"low"}`))

	if got := meta[coreexecutor.ReasoningEffortMetadataKey]; got != "high" {
		t.Fatalf("ReasoningEffortMetadataKey = %v, want %q", got, "high")
	}
}

func TestSetReasoningEffortMetadataSupportsOpenAIResponses(t *testing.T) {
	meta := make(map[string]any)

	setReasoningEffortMetadata(meta, "openai-response", "gpt-5.4", []byte(`{"reasoning":{"effort":"medium"}}`))

	if got := meta[coreexecutor.ReasoningEffortMetadataKey]; got != "medium" {
		t.Fatalf("ReasoningEffortMetadataKey = %v, want %q", got, "medium")
	}
}

func TestSetServiceTierMetadataExtractsValue(t *testing.T) {
	meta := make(map[string]any)

	setServiceTierMetadata(meta, []byte(`{"service_tier":"priority"}`))

	gotServiceTier := meta[coreexecutor.ServiceTierMetadataKey]
	if gotServiceTier != "priority" {
		t.Fatalf("ServiceTierMetadataKey = %v, want %q", gotServiceTier, "priority")
	}
}

func TestSetServiceTierMetadataDefaultsWhenMissing(t *testing.T) {
	meta := make(map[string]any)

	setServiceTierMetadata(meta, []byte(`{"model":"gpt-5.4"}`))

	gotServiceTier := meta[coreexecutor.ServiceTierMetadataKey]
	if gotServiceTier != "auto" {
		t.Fatalf("ServiceTierMetadataKey = %v, want %q", gotServiceTier, "auto")
	}
}

func TestSetServiceTierMetadataPreservesExplicitDefault(t *testing.T) {
	meta := make(map[string]any)

	setServiceTierMetadata(meta, []byte(`{"service_tier":"default"}`))

	if gotServiceTier := meta[coreexecutor.ServiceTierMetadataKey]; gotServiceTier != "default" {
		t.Fatalf("ServiceTierMetadataKey = %v, want %q", gotServiceTier, "default")
	}
}

func TestSetGenerateMetadataDefaultsWhenMissing(t *testing.T) {
	meta := make(map[string]any)

	setGenerateMetadata(meta, []byte(`{"model":"gpt-5.4"}`))

	if got := meta[coreexecutor.GenerateMetadataKey]; got != true {
		t.Fatalf("GenerateMetadataKey = %v, want true", got)
	}
}

func TestSetGenerateMetadataPreservesTrue(t *testing.T) {
	meta := make(map[string]any)

	setGenerateMetadata(meta, []byte(`{"generate":true}`))

	if got := meta[coreexecutor.GenerateMetadataKey]; got != true {
		t.Fatalf("GenerateMetadataKey = %v, want true", got)
	}
}

func TestSetGenerateMetadataHonorsExplicitFalse(t *testing.T) {
	meta := make(map[string]any)

	setGenerateMetadata(meta, []byte(`{"generate":false}`))

	if got := meta[coreexecutor.GenerateMetadataKey]; got != false {
		t.Fatalf("GenerateMetadataKey = %v, want false", got)
	}
}

func TestNormalizeClaudeIdentityBeforeAuthSynchronizesRequestAndMetadata(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"messages":[{"role":"user","content":"first human message"}]}`)
	req := coreexecutor.Request{Model: "claude-sonnet", Payload: payload}
	opts := coreexecutor.Options{
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"stale copy"}]}`),
		Metadata:        map[string]any{"preserve": "value"},
	}

	normalizedRequest, normalizedOptions, errIdentity := normalizeClaudeIdentityBeforeAuth("claude", req, opts)
	if errIdentity != nil {
		t.Fatalf("normalizeClaudeIdentityBeforeAuth() error = %v", errIdentity.Error)
	}
	requestUserID := gjson.GetBytes(normalizedRequest.Payload, "metadata.user_id").String()
	originalUserID := gjson.GetBytes(normalizedOptions.OriginalRequest, "metadata.user_id").String()
	identity, valid := coreauth.ParseClaudeUserID(requestUserID)
	if !valid {
		t.Fatalf("normalized request user_id is invalid: %q", requestUserID)
	}
	if originalUserID != requestUserID {
		t.Fatalf("OriginalRequest user_id = %q, want %q", originalUserID, requestUserID)
	}
	if got := normalizedOptions.Metadata[coreexecutor.ClaudeUserIDMetadataKey]; got != requestUserID {
		t.Fatalf("ClaudeUserIDMetadataKey = %v, want %q", got, requestUserID)
	}
	if got := normalizedOptions.Metadata[coreexecutor.ClaudeSessionIDMetadataKey]; got != identity.SessionID {
		t.Fatalf("ClaudeSessionIDMetadataKey = %v, want %q", got, identity.SessionID)
	}
	if got := normalizedOptions.Metadata["preserve"]; got != "value" {
		t.Fatalf("existing metadata = %v, want %q", got, "value")
	}
}

func TestNormalizeClaudeIdentityBeforeAuthLeavesOtherProtocolsUnchanged(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	req := coreexecutor.Request{Model: "gpt", Payload: payload}
	opts := coreexecutor.Options{OriginalRequest: payload}

	normalizedRequest, normalizedOptions, errIdentity := normalizeClaudeIdentityBeforeAuth("openai", req, opts)
	if errIdentity != nil {
		t.Fatalf("normalizeClaudeIdentityBeforeAuth() error = %v", errIdentity.Error)
	}
	if string(normalizedRequest.Payload) != string(payload) || string(normalizedOptions.OriginalRequest) != string(payload) {
		t.Fatal("non-Claude protocol payload changed")
	}
}
