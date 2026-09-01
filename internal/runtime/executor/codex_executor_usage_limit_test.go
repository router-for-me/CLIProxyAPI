package executor

import (
	"context"
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

func TestCodexExecutor_AuthManager_UsageLimitBlocksAllModelsOnCredential(t *testing.T) {
	var upstreamAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAttempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":18000}}`))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(NewCodexExecutor(&config.Config{DisableCooling: false}))

	auth := &cliproxyauth.Auth{
		ID:       uuid.NewString() + "-codex-plus",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "plus-exhausted",
			"base_url": server.URL,
		},
	}
	registerCodexAuthModels(t, auth.ID, "gpt-5.4", "gpt-5.4-mini")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	payload := []byte(`{"model":"gpt-5.4","input":"hello"}`)
	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err == nil {
		t.Fatal("expected usage limit error, got nil")
	}
	if attempts := upstreamAttempts.Load(); attempts != 1 {
		t.Fatalf("upstream attempts = %d, want 1", attempts)
	}

	_, errMini := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","input":"hello"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errMini == nil {
		t.Fatal("expected sibling model to be blocked locally, got nil")
	}
	if attempts := upstreamAttempts.Load(); attempts != 1 {
		t.Fatalf("upstream attempts after sibling model = %d, want 1 (must be blocked locally)", attempts)
	}

	registeredAuth, ok := manager.GetByID(auth.ID)
	if !ok || registeredAuth == nil {
		t.Fatal("auth not found")
	}
	if !registeredAuth.Unavailable || !registeredAuth.Quota.Exceeded || registeredAuth.Quota.Reason != "credential_quota" {
		t.Fatalf("auth cooldown = unavailable=%v quota=%+v, want credential_quota", registeredAuth.Unavailable, registeredAuth.Quota)
	}
}

func TestCodexExecutor_AuthManager_UsageLimitFailsOverToAnotherAccount(t *testing.T) {
	var exhaustedAttempts atomic.Int32
	var healthyAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		switch authHeader {
		case "Bearer plus-exhausted":
			exhaustedAttempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":18000}}`))
		case "Bearer plus-healthy":
			healthyAttempts.Add(1)
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(NewCodexExecutor(&config.Config{DisableCooling: false}))

	exhausted := &cliproxyauth.Auth{
		ID:       uuid.NewString() + "-codex-exhausted",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "plus-exhausted",
			"base_url": server.URL,
			"priority": "10",
		},
	}
	healthy := &cliproxyauth.Auth{
		ID:       uuid.NewString() + "-codex-healthy",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "plus-healthy",
			"base_url": server.URL,
		},
	}
	registerCodexAuthModels(t, exhausted.ID, "gpt-5.4")
	registerCodexAuthModels(t, healthy.ID, "gpt-5.4")
	if _, errRegister := manager.Register(context.Background(), exhausted); errRegister != nil {
		t.Fatalf("failed to register exhausted auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), healthy); errRegister != nil {
		t.Fatalf("failed to register healthy auth: %v", errRegister)
	}

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("Execute error = %v, want failover success", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected failover response payload")
	}
	if got := exhaustedAttempts.Load(); got != 1 {
		t.Fatalf("exhausted account attempts = %d, want 1", got)
	}
	if got := healthyAttempts.Load(); got != 1 {
		t.Fatalf("healthy account attempts = %d, want 1", got)
	}

	updatedExhausted, ok := manager.GetByID(exhausted.ID)
	if !ok || updatedExhausted == nil {
		t.Fatal("exhausted auth not found")
	}
	if !updatedExhausted.Quota.Exceeded || updatedExhausted.Quota.Reason != "credential_quota" {
		t.Fatalf("exhausted auth quota = %+v, want credential_quota", updatedExhausted.Quota)
	}
	if updatedExhausted.Quota.NextRecoverAt.Before(time.Now().Add(4 * time.Hour)) {
		t.Fatalf("exhausted auth recover at %v, want about 5 hours", updatedExhausted.Quota.NextRecoverAt)
	}
}

func registerCodexAuthModels(t *testing.T, authID string, models ...string) {
	t.Helper()
	infos := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model})
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", infos)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})
}
