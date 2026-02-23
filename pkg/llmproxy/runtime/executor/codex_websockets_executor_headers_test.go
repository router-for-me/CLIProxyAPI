package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestApplyCodexWebsocketHeaders_IncludesResponsesWebsocketsBetaByDefault(t *testing.T) {
	got := applyCodexWebsocketHeaders(context.Background(), nil, nil, "tok")
	if got.Get("OpenAI-Beta") != codexResponsesWebsocketBetaHeaderValue {
		t.Fatalf("expected OpenAI-Beta %q, got %q", codexResponsesWebsocketBetaHeaderValue, got.Get("OpenAI-Beta"))
	}
	if got.Get("Authorization") != "Bearer tok" {
		t.Fatalf("expected Authorization to be set, got %q", got.Get("Authorization"))
	}
}

func TestApplyCodexWebsocketHeaders_PreservesExplicitResponsesWebsocketsBeta(t *testing.T) {
	input := http.Header{}
	input.Set("OpenAI-Beta", "responses_websockets=2025-12-34,custom-beta")
	got := applyCodexWebsocketHeaders(context.Background(), input, nil, "tok")
	if got.Get("OpenAI-Beta") != "responses_websockets=2025-12-34,custom-beta" {
		t.Fatalf("unexpected OpenAI-Beta: %q", got.Get("OpenAI-Beta"))
	}
}

func TestApplyCodexWebsocketHeaders_ReplacesNonWebsocketBetaValue(t *testing.T) {
	input := http.Header{}
	input.Set("OpenAI-Beta", "foo=bar")
	got := applyCodexWebsocketHeaders(context.Background(), input, nil, "tok")
	if got.Get("OpenAI-Beta") != codexResponsesWebsocketBetaHeaderValue {
		t.Fatalf("expected fallback OpenAI-Beta %q, got %q", codexResponsesWebsocketBetaHeaderValue, got.Get("OpenAI-Beta"))
	}
}

func TestApplyCodexWebsocketHeaders_UsesGinOpenAIBeta(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request, _ = http.NewRequest(http.MethodPost, "http://127.0.0.1/v1/responses", strings.NewReader("{}"))
	ginCtx.Request.Header.Set("OpenAI-Beta", "responses_websockets=2030-01-01")
	ctx := context.WithValue(context.Background(), ginContextKey, ginCtx)

	got := applyCodexWebsocketHeaders(ctx, nil, nil, "tok")
	if got.Get("OpenAI-Beta") != "responses_websockets=2030-01-01" {
		t.Fatalf("unexpected OpenAI-Beta from gin headers: %q", got.Get("OpenAI-Beta"))
	}
}

func TestApplyCodexWebsocketHeaders_UsesAPICredentialsForOriginatorBehavior(t *testing.T) {
	got := applyCodexWebsocketHeaders(context.Background(), nil, nil, "tok")
	if got.Get("Originator") != "codex_cli_rs" {
		t.Fatalf("expected originator for token-based auth, got %q", got.Get("Originator"))
	}

	withAPIKey := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "api-key"}}
	got = applyCodexWebsocketHeaders(context.Background(), nil, withAPIKey, "tok")
	if got.Get("Originator") != "" {
		t.Fatalf("expected no originator when API key auth is present, got %q", got.Get("Originator"))
	}
}
