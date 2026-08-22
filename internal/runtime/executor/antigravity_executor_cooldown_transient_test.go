package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// TestAntigravityShortCooldownErrorIsTransient pins the classification of the
// synthetic 429 the executor raises while an auth sits in a short cooldown.
// The cooldown is a local, self-imposed pause of at most a few minutes, so the
// conductor has to read it as a transient rate limit and rotate to the next
// auth. Unclassified, the same error looks like an exhausted quota carrying a
// retry hint, and the conductor escalates BackoffLevel toward the 30 minute
// ceiling — parking an account that was never actually throttled upstream.
func TestAntigravityShortCooldownErrorIsTransient(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)

	exec := NewAntigravityExecutor(&config.Config{})
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
	}
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	for _, tc := range []struct {
		name  string
		model string
		call  func(auth *cliproxyauth.Auth, model string) error
	}{
		{
			name:  "execute",
			model: "gemini-3.6-flash",
			call: func(auth *cliproxyauth.Auth, model string) error {
				_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: model, Payload: payload}, opts)
				return err
			},
		},
		{
			name:  "execute-claude",
			model: "claude-sonnet-4-5",
			call: func(auth *cliproxyauth.Auth, model string) error {
				_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: model, Payload: payload}, opts)
				return err
			},
		},
		{
			name:  "execute-stream",
			model: "gemini-3.6-flash",
			call: func(auth *cliproxyauth.Auth, model string) error {
				_, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: model, Payload: payload}, opts)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{ID: "cooldown-transient-" + tc.name}
			if errMark := markAntigravityShortCooldownRequired(context.Background(), auth, tc.model, time.Now(), 30*time.Second); errMark != nil {
				t.Fatalf("markAntigravityShortCooldownRequired() error = %v", errMark)
			}

			err := tc.call(auth, tc.model)
			if err == nil {
				t.Fatal("expected the short cooldown to surface a 429")
			}

			var classified interface{ TransientRateLimit() bool }
			if !errors.As(err, &classified) {
				t.Fatalf("short-cooldown error carries no 429 classification: %T", err)
			}
			if !classified.TransientRateLimit() {
				t.Fatal("expected the synthetic short-cooldown 429 to be transient so the conductor rotates instead of escalating backoff")
			}

			var hinted interface{ RetryAfter() *time.Duration }
			if !errors.As(err, &hinted) || hinted.RetryAfter() == nil || *hinted.RetryAfter() <= 0 {
				t.Fatalf("expected a positive retry hint on the short-cooldown 429, got %v", err)
			}
		})
	}
}
