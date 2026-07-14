package helps

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func testGinContextWithCaller(principal, provider string) context.Context {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	if principal != "" {
		ginCtx.Set("userApiKey", principal)
	}
	if provider != "" {
		ginCtx.Set("accessProvider", provider)
	}
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestIsolateClientReasoningReplaySessionKeyIncludesProvider(t *testing.T) {
	raw := "claude:shared-session"
	keyA := IsolateClientReasoningReplaySessionKey(testGinContextWithCaller("same-principal", "provider-a"), raw)
	keyB := IsolateClientReasoningReplaySessionKey(testGinContextWithCaller("same-principal", "provider-b"), raw)
	if keyA == "" || keyB == "" {
		t.Fatalf("expected isolated keys, got A=%q B=%q", keyA, keyB)
	}
	if keyA == keyB {
		t.Fatalf("same principal different provider must not share keys: %q", keyA)
	}
	if !strings.HasPrefix(keyA, "caller:") || !strings.Contains(keyA, raw) {
		t.Fatalf("keyA = %q, want caller-isolated form", keyA)
	}

	keyA2 := IsolateClientReasoningReplaySessionKey(testGinContextWithCaller("same-principal", "provider-a"), raw)
	if keyA != keyA2 {
		t.Fatalf("same principal+provider must be stable: %q vs %q", keyA, keyA2)
	}
}

func TestIsolateClientReasoningReplaySessionKeyPreservesOpaquePrincipals(t *testing.T) {
	raw := "prompt-cache:shared"
	// Principals that differ only in surrounding whitespace are distinct subjects
	// (AuthMiddleware stores them verbatim) and must not share a replay namespace.
	keyA := IsolateClientReasoningReplaySessionKey(testGinContextWithCaller("alice", "provider-a"), raw)
	keyB := IsolateClientReasoningReplaySessionKey(testGinContextWithCaller(" alice ", "provider-a"), raw)
	if keyA == "" || keyB == "" {
		t.Fatalf("expected isolated keys, got A=%q B=%q", keyA, keyB)
	}
	if keyA == keyB {
		t.Fatalf("whitespace-distinct principals must not share a replay namespace: %q", keyA)
	}
}

func TestIsolateClientReasoningReplaySessionKeyRequiresPrincipal(t *testing.T) {
	if got := IsolateClientReasoningReplaySessionKey(context.Background(), "claude:session"); got != "" {
		t.Fatalf("without principal got %q, want empty", got)
	}
	if got := IsolateClientReasoningReplaySessionKey(testGinContextWithCaller("", "provider-a"), "claude:session"); got != "" {
		t.Fatalf("empty principal got %q, want empty", got)
	}
}

func TestIsolateClientReasoningReplaySessionKeyAllowsExecutionWithoutPrincipal(t *testing.T) {
	got := IsolateClientReasoningReplaySessionKey(context.Background(), "execution:trusted")
	if got != "execution:trusted" {
		t.Fatalf("execution key = %q, want execution:trusted", got)
	}
}

func TestAccessProviderFromContext(t *testing.T) {
	if got := AccessProviderFromContext(testGinContextWithCaller("p", "config-inline")); got != "config-inline" {
		t.Fatalf("provider = %q, want config-inline", got)
	}
	if got := AccessProviderFromContext(context.Background()); got != "" {
		t.Fatalf("empty context provider = %q, want empty", got)
	}
}
