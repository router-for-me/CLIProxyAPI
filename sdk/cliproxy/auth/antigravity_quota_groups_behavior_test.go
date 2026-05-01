package auth

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestManagerExecute_AntigravityClaudeQuotaGroupDoesNotBlockGemini(t *testing.T) {
	now := time.Now().UTC()

	manager := NewManager(nil, nil, nil)
	executor := &aliasRoutingExecutor{id: "antigravity"}
	manager.RegisterExecutor(executor)

	cfg := &internalconfig.Config{
		OAuthQuotaGroups: internalconfig.DefaultOAuthQuotaGroups(),
		OAuthAccountQuotaGroupState: []internalconfig.OAuthAccountQuotaGroupState{
			{
				AuthID:             "ag-auth",
				GroupID:            internalconfig.OAuthQuotaGroupClaude45,
				AutoSuspendedUntil: now.Add(30 * time.Minute),
				AutoReason:         "quota_exhausted",
				SourceModel:        "claude-sonnet-4-6",
				SourceProvider:     "antigravity",
				ResetTimeSource:    "retry_after",
			},
		},
	}
	manager.SetConfig(cfg)
	t.Cleanup(func() {
		manager.SetConfig(&internalconfig.Config{})
	})

	auth := &Auth{
		ID:       "ag-auth",
		Provider: "antigravity",
		Status:   StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{ID: "claude-sonnet-4-6"},
		{ID: "gemini-3.1-flash-lite"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	updatedAuth, ok := manager.GetByID(auth.ID)
	if !ok || updatedAuth == nil {
		t.Fatal("updated auth missing")
	}
	updateAggregatedAvailability(updatedAuth, now)
	if updatedAuth.Unavailable {
		t.Fatal("auth.Unavailable = true, want false while Gemini quota-group is still available")
	}

	geminiResp, err := manager.Execute(
		context.Background(),
		[]string{"antigravity"},
		cliproxyexecutor.Request{Model: "gemini-3.1-flash-lite"},
		cliproxyexecutor.Options{},
	)
	if err != nil {
		t.Fatalf("execute Gemini error = %v, want success", err)
	}
	if string(geminiResp.Payload) != "gemini-3.1-flash-lite" {
		t.Fatalf("execute Gemini payload = %q, want %q", string(geminiResp.Payload), "gemini-3.1-flash-lite")
	}

	_, err = manager.Execute(
		context.Background(),
		[]string{"antigravity"},
		cliproxyexecutor.Request{Model: "claude-sonnet-4-6"},
		cliproxyexecutor.Options{},
	)
	if err == nil {
		t.Fatal("execute Claude error = nil, want cooldown error")
	}
	if _, ok := err.(*modelCooldownError); !ok {
		t.Fatalf("execute Claude error = %T, want *modelCooldownError", err)
	}
}
