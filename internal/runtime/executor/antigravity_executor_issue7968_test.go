package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature/signaturetest"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityFirstRefreshPreservesSignatureCacheScope(t *testing.T) {
	resetAntigravityRefreshGroupForTest()
	t.Cleanup(resetAntigravityRefreshGroupForTest)
	cache.ClearSignatureCache("")
	t.Cleanup(func() { cache.ClearSignatureCache("") })

	auth := &cliproxyauth.Auth{
		ID: "ag-first-refresh",
		Metadata: map[string]any{
			"access_token":  "a1",
			"refresh_token": "r1",
			"project_id":    "p1",
		},
	}
	beforeScope := antigravitySignatureCacheScope(auth)
	const model = "claude-sonnet-4-6"
	const thinkingText = "cached before first refresh"
	signature := signaturetest.AntigravityCAQSWithChannel(37)
	beforeContext := cache.WithSignatureCacheScope(context.Background(), beforeScope)
	if !cache.CacheSignatureBestEffort(beforeContext, model, thinkingText, signature) {
		t.Fatal("failed to seed pre-refresh signature cache")
	}

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"a2","refresh_token":"r2","expires_in":3600}`)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
	refreshed, errRefresh := NewAntigravityExecutor(&config.Config{}).refreshTokenWithOptions(ctx, auth, ctx, false)
	if errRefresh != nil {
		t.Fatalf("refreshTokenWithOptions() error = %v", errRefresh)
	}

	afterScope := antigravitySignatureCacheScope(refreshed)
	if afterScope != beforeScope {
		t.Fatalf("antigravitySignatureCacheScope changed on first refresh: before=%q after=%q", beforeScope, afterScope)
	}
	afterContext := cache.WithSignatureCacheScope(context.Background(), afterScope)
	got, errSignature := cache.GetCachedSignatureRequired(afterContext, model, thinkingText)
	if errSignature != nil {
		t.Fatalf("GetCachedSignatureRequired() error = %v", errSignature)
	}
	if got != signature {
		t.Fatalf("cached signature after first refresh = %q, want %q", got, signature)
	}
}
