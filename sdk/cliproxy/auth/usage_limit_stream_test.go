package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// creditsAfterOutputExecutor streams output and then fails with an explicit
// quota exhaustion on free quota; with Antigravity credits it streams a full
// answer.
type creditsAfterOutputExecutor struct {
	antigravityCreditsFallbackExecutor
}

func (e *creditsAfterOutputExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk, 2)
	if AntigravityCreditsRequested(ctx) {
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte("credits fallback")}
	} else {
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte("partial")}
		ch <- cliproxyexecutor.StreamChunk{Err: poolQuotaErr{}}
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{}, Chunks: ch}, nil
}

// A quota exhaustion after output on an Antigravity Claude route stays
// retryable while the credits fallback, which the next request tries before
// reporting a usage limit, still has a credential to try.
func TestStreamUsageLimitKeepsRetryableWhileCreditsCanServe(t *testing.T) {
	const model = "claude-opus-4-6-thinking"
	SetQuotaCooldownResponseMode("usage-limit")
	t.Cleanup(func() { SetQuotaCooldownResponseMode("") })
	for _, tc := range []struct {
		name           string
		credits        bool
		creditsOut     bool
		wantUsageLimit bool
	}{
		{name: "credits can serve", credits: true},
		{name: "credits known to be out", credits: true, creditsOut: true, wantUsageLimit: true},
		{name: "credits fallback off", wantUsageLimit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authID := "ag-stream-credits-" + tc.name
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: tc.credits}})
			manager.RegisterExecutor(&creditsAfterOutputExecutor{})
			registryRegister(t, authID, "antigravity", model)
			if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: authID, Provider: "antigravity"}); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			if tc.creditsOut {
				SetAntigravityCreditsHint(authID, AntigravityCreditsHint{Known: true, Available: false, UpdatedAt: time.Now()})
				t.Cleanup(func() { antigravityCreditsHintByAuth.Delete(authID) })
			}
			stream := func() (payload string, err error) {
				result, errExecute := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				if errExecute != nil {
					return "", errExecute
				}
				for chunk := range result.Chunks {
					payload += string(chunk.Payload)
					if chunk.Err != nil {
						err = chunk.Err
					}
				}
				return payload, err
			}

			payload, errStream := stream()
			if payload != "partial" || errStream == nil || IsUsageLimitReachedError(errStream) != tc.wantUsageLimit {
				t.Fatalf("first stream = %q, %v; want the partial output, then a usage limit %t", payload, errStream, tc.wantUsageLimit)
			}
			if tc.wantUsageLimit {
				return
			}
			// The client's next request reaches the credits fallback.
			if payload, errStream = stream(); payload != "credits fallback" || errStream != nil {
				t.Fatalf("next stream = %q, %v; want the credits fallback answer", payload, errStream)
			}
		})
	}
}
