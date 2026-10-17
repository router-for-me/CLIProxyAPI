package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestAntigravityExecutor_WeeklyModelResetDoesNotBlockSibling(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	const (
		exhaustedModel = "claude-opus-4-6"
		siblingModel   = "gemini-3.8-flash"
	)

	var opusAttempts, flashAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			http.Error(w, "failed to read sanitized test request", http.StatusBadRequest)
			return
		}
		switch {
		case strings.Contains(string(body), `"model":"`+exhaustedModel+`"`):
			opusAttempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write(antigravityWeeklyReset429Body("RATE_LIMIT_EXCEEDED", "166h9m16s"))
		case strings.Contains(string(body), `"model":"`+siblingModel+`"`):
			flashAttempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`))
		default:
			http.Error(w, "unexpected sanitized test model", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{DisableCooling: false}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(NewAntigravityExecutor(cfg))

	auth := &cliproxyauth.Auth{
		ID:       uuid.NewString() + "-antigravity-weekly-scope",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: exhaustedModel}, {ID: siblingModel}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	payloadOpus := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	_, errOpus := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
		Model:   exhaustedModel,
		Payload: payloadOpus,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity})
	if errOpus == nil {
		t.Fatal("expected weekly model reset to return 429")
	}
	if got := opusAttempts.Load(); got != 1 {
		t.Fatalf("exhausted model upstream attempts = %d, want 1", got)
	}

	var retry interface{ RetryAfter() *time.Duration }
	if !errors.As(errOpus, &retry) || retry == nil || retry.RetryAfter() == nil {
		t.Fatalf("expected RetryAfter on weekly reset error, got %v", errOpus)
	}
	if got := *retry.RetryAfter(); got != antigravityQuotaCooldownCeiling {
		t.Fatalf("RetryAfter = %v, want capped %v", got, antigravityQuotaCooldownCeiling)
	}

	updatedAuth, ok := manager.GetByID(auth.ID)
	if !ok || updatedAuth == nil {
		t.Fatal("auth not found")
	}
	opusState := updatedAuth.ModelStates[exhaustedModel]
	if opusState == nil {
		t.Fatal("exhausted model state not found")
	}
	if opusState.Quota.NextRecoverAt.After(time.Now().Add(antigravityQuotaCooldownCeiling + time.Minute)) {
		t.Fatalf("exhausted model cooldown too long: NextRecoverAt = %v", opusState.Quota.NextRecoverAt)
	}
	if updatedAuth.Quota.Reason == "credential_quota" {
		t.Fatalf("weekly model reset parked the credential: %+v", updatedAuth.Quota)
	}

	payloadFlash := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	_, errFlash := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
		Model:   siblingModel,
		Payload: payloadFlash,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity})
	if errFlash != nil {
		t.Fatalf("expected sibling model to reach upstream on the same credential, got: %v", errFlash)
	}
	if got := flashAttempts.Load(); got != 1 {
		t.Fatalf("sibling model upstream attempts = %d, want 1", got)
	}
}
