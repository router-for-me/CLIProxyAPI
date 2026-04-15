package auth

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func testOAuthQuotaConfig() *internalconfig.Config {
	return &internalconfig.Config{
		OAuthQuotaGroups: internalconfig.DefaultOAuthQuotaGroups(),
		OAuthModelAlias: map[string][]internalconfig.OAuthModelAlias{
			"antigravity": {{
				Name:  "gemini-3.1-flash-lite",
				Alias: "gpt-4o",
			}},
		},
	}
}

func TestResolveOAuthQuotaGroup_UsesAliasAndPriority(t *testing.T) {
	cfg := testOAuthQuotaConfig()
	SetOAuthQuotaRuntimeConfig(cfg)
	t.Cleanup(func() {
		SetOAuthQuotaRuntimeConfig(&internalconfig.Config{})
	})

	auth := &Auth{ID: "auth-1", Provider: "antigravity"}

	imageGroup, ok := resolveOAuthQuotaGroup(auth, "gemini-3.1-flash-image-4k")
	if !ok {
		t.Fatal("resolveOAuthQuotaGroup(image) = no match, want g3_flash")
	}
	if imageGroup.ID != internalconfig.OAuthQuotaGroupG3Flash {
		t.Fatalf("resolveOAuthQuotaGroup(image) = %q, want %q", imageGroup.ID, internalconfig.OAuthQuotaGroupG3Flash)
	}

	aliasGroup, ok := resolveOAuthQuotaGroup(auth, "gpt-4o")
	if !ok {
		t.Fatal("resolveOAuthQuotaGroup(alias) = no match, want g3_flash")
	}
	if aliasGroup.ID != internalconfig.OAuthQuotaGroupG3Flash {
		t.Fatalf("resolveOAuthQuotaGroup(alias) = %q, want %q", aliasGroup.ID, internalconfig.OAuthQuotaGroupG3Flash)
	}

	claudeGroup, ok := resolveOAuthQuotaGroup(auth, "Claude-Opus-4-6-Thinking")
	if !ok {
		t.Fatal("resolveOAuthQuotaGroup(claude) = no match, want claude_45")
	}
	if claudeGroup.ID != internalconfig.OAuthQuotaGroupClaude45 {
		t.Fatalf("resolveOAuthQuotaGroup(claude) = %q, want %q", claudeGroup.ID, internalconfig.OAuthQuotaGroupClaude45)
	}

	if _, ok := resolveOAuthQuotaGroup(auth, "gemini-2.5-pro"); ok {
		t.Fatal("resolveOAuthQuotaGroup(gemini-2.5-pro) matched, want no match")
	}

	if _, ok := resolveOAuthQuotaGroup(auth, "unclassified-model"); ok {
		t.Fatal("resolveOAuthQuotaGroup(unclassified) matched, want no match")
	}
}

func TestIsAuthBlockedForModel_UsesQuotaGroupState(t *testing.T) {
	now := time.Now().UTC()
	cfg := testOAuthQuotaConfig()
	cfg.OAuthAccountQuotaGroupState = []internalconfig.OAuthAccountQuotaGroupState{
		{
			AuthID:          "manual-auth",
			GroupID:         internalconfig.OAuthQuotaGroupClaude45,
			ManualSuspended: true,
			ManualReason:    "maintenance",
		},
		{
			AuthID:             "auto-auth",
			GroupID:            internalconfig.OAuthQuotaGroupG3Pro,
			AutoSuspendedUntil: now.Add(5 * time.Minute),
			AutoReason:         "quota_exhausted",
		},
	}
	SetOAuthQuotaRuntimeConfig(cfg)
	t.Cleanup(func() {
		SetOAuthQuotaRuntimeConfig(&internalconfig.Config{})
	})

	manualAuth := &Auth{ID: "manual-auth", Provider: "antigravity"}
	blocked, reason, next := isAuthBlockedForModel(manualAuth, "claude-sonnet-4-6", now)
	if !blocked {
		t.Fatal("manual quota-group block = false, want true")
	}
	if reason != blockReasonDisabled {
		t.Fatalf("manual quota-group reason = %v, want %v", reason, blockReasonDisabled)
	}
	if !next.IsZero() {
		t.Fatalf("manual quota-group next = %v, want zero", next)
	}

	autoAuth := &Auth{ID: "auto-auth", Provider: "antigravity"}
	blocked, reason, next = isAuthBlockedForModel(autoAuth, "gemini-3.1-pro-high", now)
	if !blocked {
		t.Fatal("auto quota-group block = false, want true")
	}
	if reason != blockReasonCooldown {
		t.Fatalf("auto quota-group reason = %v, want %v", reason, blockReasonCooldown)
	}
	if next.IsZero() || !next.After(now) {
		t.Fatalf("auto quota-group next = %v, want future timestamp", next)
	}

	blocked, reason, next = isAuthBlockedForModel(autoAuth, "gemini-3.1-flash-lite", now)
	if blocked {
		t.Fatal("flash group unexpectedly blocked by pro cooldown")
	}
	if reason != blockReasonNone {
		t.Fatalf("flash group reason = %v, want %v", reason, blockReasonNone)
	}
	if !next.IsZero() {
		t.Fatalf("flash group next = %v, want zero", next)
	}
}

func TestUpdateAggregatedAvailability_UsesEffectiveQuotaGroups(t *testing.T) {
	now := time.Now().UTC()
	cfg := testOAuthQuotaConfig()
	cfg.OAuthAccountQuotaGroupState = []internalconfig.OAuthAccountQuotaGroupState{
		{
			AuthID:          "agg-auth",
			GroupID:         internalconfig.OAuthQuotaGroupClaude45,
			ManualSuspended: true,
		},
	}
	SetOAuthQuotaRuntimeConfig(cfg)
	t.Cleanup(func() {
		SetOAuthQuotaRuntimeConfig(&internalconfig.Config{})
	})

	auth := &Auth{ID: "agg-auth", Provider: "antigravity"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{ID: "claude-sonnet-4-6"},
		{ID: "gemini-3.1-flash-lite"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	updateAggregatedAvailability(auth, now)
	if auth.Unavailable {
		t.Fatal("auth.Unavailable = true, want false when one effective group is still available")
	}

	cfg.OAuthAccountQuotaGroupState = append(cfg.OAuthAccountQuotaGroupState, internalconfig.OAuthAccountQuotaGroupState{
		AuthID:             "agg-auth",
		GroupID:            internalconfig.OAuthQuotaGroupG3Flash,
		AutoSuspendedUntil: now.Add(10 * time.Minute),
		AutoReason:         "quota_exhausted",
	})
	SetOAuthQuotaRuntimeConfig(cfg)

	updateAggregatedAvailability(auth, now)
	if !auth.Unavailable {
		t.Fatal("auth.Unavailable = false, want true when all effective groups are blocked")
	}
	if auth.NextRetryAfter.IsZero() || !auth.NextRetryAfter.After(now) {
		t.Fatalf("auth.NextRetryAfter = %v, want future timestamp", auth.NextRetryAfter)
	}
	if !auth.Quota.Exceeded {
		t.Fatal("auth.Quota.Exceeded = false, want true")
	}
}

func TestManagerMarkResult_QuotaGroupCooldownLifecycle(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	cfg := testOAuthQuotaConfig()
	manager.SetConfig(cfg)
	t.Cleanup(func() {
		manager.SetConfig(&internalconfig.Config{})
	})

	auth := &Auth{
		ID:       "quota-auth",
		Provider: "antigravity",
		Status:   StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	model := "gemini-3.1-flash-lite"
	retryAfter := 3 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   auth.Provider,
		Model:      model,
		Success:    false,
		RetryAfter: &retryAfter,
		Error: &Error{
			Message:    "quota exhausted",
			HTTPStatus: 429,
		},
	})

	runtimeCfg, _ := manager.runtimeConfig.Load().(*internalconfig.Config)
	if runtimeCfg == nil {
		t.Fatal("runtime config = nil")
	}
	state, ok := findOAuthQuotaGroupState(runtimeCfg.OAuthAccountQuotaGroupState, auth.ID, internalconfig.OAuthQuotaGroupG3Flash)
	if !ok {
		t.Fatal("quota-group state missing after 429")
	}
	if state.AutoReason != "quota_exhausted" {
		t.Fatalf("state.AutoReason = %q, want %q", state.AutoReason, "quota_exhausted")
	}
	if state.ResetTimeSource != "retry_after" {
		t.Fatalf("state.ResetTimeSource = %q, want %q", state.ResetTimeSource, "retry_after")
	}
	if state.SourceModel != model {
		t.Fatalf("state.SourceModel = %q, want %q", state.SourceModel, model)
	}

	updatedAuth, ok := manager.GetByID(auth.ID)
	if !ok || updatedAuth == nil {
		t.Fatal("updated auth missing")
	}
	modelState := updatedAuth.ModelStates[model]
	if modelState == nil {
		t.Fatal("model state missing after 429")
	}
	if modelState.Unavailable {
		t.Fatal("modelState.Unavailable = true, want false for quota-group cooldown")
	}
	if !modelState.NextRetryAfter.IsZero() {
		t.Fatalf("modelState.NextRetryAfter = %v, want zero", modelState.NextRetryAfter)
	}
	if modelState.Quota.Exceeded {
		t.Fatal("modelState.Quota.Exceeded = true, want false")
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  true,
	})

	runtimeCfg, _ = manager.runtimeConfig.Load().(*internalconfig.Config)
	if runtimeCfg == nil {
		t.Fatal("runtime config after success = nil")
	}
	if _, ok := findOAuthQuotaGroupState(runtimeCfg.OAuthAccountQuotaGroupState, auth.ID, internalconfig.OAuthQuotaGroupG3Flash); ok {
		t.Fatal("quota-group state still present after success")
	}
}

func TestManagerMarkResult_503DoesNotPersistQuotaGroupCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	cfg := testOAuthQuotaConfig()
	manager.SetConfig(cfg)
	t.Cleanup(func() {
		manager.SetConfig(&internalconfig.Config{})
	})

	auth := &Auth{
		ID:       "capacity-auth",
		Provider: "antigravity",
		Status:   StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	model := "claude-opus-4-6-thinking"
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error: &Error{
			Message:    "No capacity available",
			HTTPStatus: 503,
		},
	})

	runtimeCfg, _ := manager.runtimeConfig.Load().(*internalconfig.Config)
	if runtimeCfg == nil {
		t.Fatal("runtime config = nil")
	}
	if _, ok := findOAuthQuotaGroupState(runtimeCfg.OAuthAccountQuotaGroupState, auth.ID, internalconfig.OAuthQuotaGroupClaude45); ok {
		t.Fatal("quota-group state unexpectedly persisted for 503")
	}

	updatedAuth, ok := manager.GetByID(auth.ID)
	if !ok || updatedAuth == nil {
		t.Fatal("updated auth missing")
	}
	modelState := updatedAuth.ModelStates[model]
	if modelState == nil {
		t.Fatal("model state missing after 503")
	}
	if !modelState.Unavailable {
		t.Fatal("modelState.Unavailable = false, want true for transient 503 cooldown")
	}
	if modelState.NextRetryAfter.IsZero() {
		t.Fatal("modelState.NextRetryAfter = zero, want transient retry window")
	}
}

func TestManagerClearExpiredOAuthQuotaGroupAutoStates_RemovesExpiredState(t *testing.T) {
	now := time.Now().UTC()
	manager := NewManager(nil, nil, nil)
	cfg := testOAuthQuotaConfig()
	cfg.OAuthAccountQuotaGroupState = []internalconfig.OAuthAccountQuotaGroupState{
		{
			AuthID:             "auto-expired",
			GroupID:            internalconfig.OAuthQuotaGroupG3Pro,
			AutoSuspendedUntil: now.Add(-1 * time.Minute),
			AutoReason:         "quota_exhausted",
			SourceModel:        "gemini-3.1-pro-high",
			SourceProvider:     "antigravity",
			ResetTimeSource:    "retry_after",
		},
		{
			AuthID:             "manual-auth",
			GroupID:            internalconfig.OAuthQuotaGroupG3Flash,
			ManualSuspended:    true,
			ManualReason:       "manual override",
			AutoSuspendedUntil: now.Add(-2 * time.Minute),
			AutoReason:         "quota_exhausted",
			SourceModel:        "gemini-3.1-flash-lite",
			SourceProvider:     "antigravity",
			ResetTimeSource:    "retry_after",
		},
	}
	manager.SetConfig(cfg)
	t.Cleanup(func() {
		manager.SetConfig(&internalconfig.Config{})
	})

	if changed := manager.ClearExpiredOAuthQuotaGroupAutoStates(now); !changed {
		t.Fatal("ClearExpiredOAuthQuotaGroupAutoStates = false, want true")
	}

	runtimeCfg, _ := manager.runtimeConfig.Load().(*internalconfig.Config)
	if runtimeCfg == nil {
		t.Fatal("runtime config = nil")
	}
	if _, ok := findOAuthQuotaGroupState(runtimeCfg.OAuthAccountQuotaGroupState, "auto-expired", internalconfig.OAuthQuotaGroupG3Pro); ok {
		t.Fatal("expired auto cooldown state still present")
	}

	state, ok := findOAuthQuotaGroupState(runtimeCfg.OAuthAccountQuotaGroupState, "manual-auth", internalconfig.OAuthQuotaGroupG3Flash)
	if !ok {
		t.Fatal("manual state missing after cleanup")
	}
	if !state.ManualSuspended {
		t.Fatal("manual state lost manual suspension flag")
	}
	if !state.AutoSuspendedUntil.IsZero() {
		t.Fatalf("manual state auto cooldown still present: %v", state.AutoSuspendedUntil)
	}
	if state.AutoReason != "" || state.SourceModel != "" || state.SourceProvider != "" || state.ResetTimeSource != "" {
		t.Fatalf("manual state still has stale auto metadata: %#v", state)
	}
}
