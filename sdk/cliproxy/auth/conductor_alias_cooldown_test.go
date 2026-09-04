package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManagerExecute_ModelAliasRequestNotBlockedByOtherModelQuotaCooldown(t *testing.T) {
	const (
		provider     = "antigravity"
		requestModel = "gemini-3.6-flash"
		targetModel  = "gemini-3.6-flash-high"
		imageModel   = "gemini-3.1-flash-image"
	)

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &aliasRoutingExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: requestModel,
			Fork:  true,
		}},
	})

	now := time.Now()
	next := now.Add(1 * time.Hour)

	auth := &Auth{
		ID:       "antigravity-auth-1",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Status: StatusActive,
			},
			imageModel: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: next,
				},
			},
		},
	}
	updateAggregatedAvailability(auth, now)
	if !auth.Quota.Exceeded {
		t.Fatalf("precondition failed: auth.Quota.Exceeded should be true after updateAggregatedAvailability")
	}
	if auth.Unavailable {
		t.Fatalf("precondition failed: auth.Unavailable should be false since targetModel is active")
	}

	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: requestModel},
		{ID: targetModel},
		{ID: imageModel},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	resp, errExecute := manager.Execute(
		context.Background(),
		[]string{provider},
		cliproxyexecutor.Request{Model: requestModel},
		cliproxyexecutor.Options{},
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want success", errExecute)
	}
	if string(resp.Payload) != targetModel {
		t.Fatalf("Execute() payload = %q, want %q", string(resp.Payload), targetModel)
	}
}

func TestManagerSelectAuth_ModelAliasRequestNotBlockedByOtherModelQuotaCooldown(t *testing.T) {
	const (
		provider     = "antigravity"
		requestModel = "gemini-3.6-flash"
		targetModel  = "gemini-3.6-flash-high"
		imageModel   = "gemini-3.1-flash-image"
	)

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &aliasRoutingExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: requestModel,
			Fork:  true,
		}},
	})

	now := time.Now()
	next := now.Add(1 * time.Hour)

	auth := &Auth{
		ID:       "antigravity-auth-2",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Status: StatusActive,
			},
			imageModel: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: next,
				},
			},
		},
	}
	updateAggregatedAvailability(auth, now)
	if !auth.Quota.Exceeded {
		t.Fatalf("precondition failed: auth.Quota.Exceeded should be true after updateAggregatedAvailability")
	}
	if auth.Unavailable {
		t.Fatalf("precondition failed: auth.Unavailable should be false since targetModel is active")
	}

	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: requestModel},
		{ID: targetModel},
		{ID: imageModel},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	selected, errSelect := manager.SelectAuth(
		context.Background(),
		provider,
		requestModel,
		cliproxyexecutor.Options{},
	)
	if errSelect != nil {
		t.Fatalf("SelectAuth() error = %v, want success", errSelect)
	}
	if selected == nil || selected.ID != auth.ID {
		t.Fatalf("SelectAuth() selected = %#v, want %s", selected, auth.ID)
	}
}

func TestManagerExecute_ModelAliasRequestBlockedWhenTargetModelInQuotaCooldown(t *testing.T) {
	const (
		provider     = "antigravity"
		requestModel = "gemini-3.6-flash"
		targetModel  = "gemini-3.6-flash-high"
	)

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &aliasRoutingExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: requestModel,
			Fork:  true,
		}},
	})

	now := time.Now()
	next := now.Add(1 * time.Hour)

	auth := &Auth{
		ID:       "antigravity-auth-3",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: next,
				},
			},
		},
	}
	updateAggregatedAvailability(auth, now)

	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: requestModel},
		{ID: targetModel},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	_, errExecute := manager.Execute(
		context.Background(),
		[]string{provider},
		cliproxyexecutor.Request{Model: requestModel},
		cliproxyexecutor.Options{},
	)
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want cooldown error")
	}
	var cooldownErr *modelCooldownError
	if !errors.As(errExecute, &cooldownErr) {
		t.Fatalf("Execute() error = %T (%v), want *modelCooldownError", errExecute, errExecute)
	}
	if cooldownErr.model != requestModel {
		t.Fatalf("cooldown model = %q, want %q", cooldownErr.model, requestModel)
	}
}
