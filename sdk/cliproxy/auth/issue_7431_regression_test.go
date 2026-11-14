package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestIssue7431_LargeWeightedPoolRotatesAcrossTransientExclusionsAndCooldowns(t *testing.T) {
	for _, withAffinity := range []bool{false, true} {
		name := "scheduler-fast-path"
		if withAffinity {
			name = "session-affinity-legacy"
		}
		t.Run(name, func(t *testing.T) {
			const (
				totalCreds   = 1080
				model        = "issue-7431-large-wrr-model"
				compatName   = "issue-7431-pool"
				totalPicks   = 1120
				failingEvery = 40
			)
			providerKey := util.OpenAICompatibleProviderKey(compatName)
			disableCooling := true
			retryCount := 4
			cfg := &internalconfig.Config{
				RequestRetry: retryCount,
				OpenAICompatibility: []internalconfig.OpenAICompatibility{{
					Name:           compatName,
					BaseURL:        "https://example.invalid/v1",
					DisableCooling: &disableCooling,
					Models:         []internalconfig.OpenAICompatibilityModel{{Name: model, Alias: model}},
					RequestScopedErrors: []internalconfig.RequestScopedErrorRule{{
						Status: http.StatusTooManyRequests,
						Match:  []string{"server_busy"},
						Action: RequestScopedActionContinueAndCooldown,
					}},
				}},
			}

			weighted := &WeightedRoundRobinSelector{}
			var selector Selector = weighted
			if withAffinity {
				affinity := NewSessionAffinitySelector(weighted)
				t.Cleanup(affinity.Stop)
				selector = affinity
			}

			manager := NewManager(nil, selector, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(retryCount, 0, 0)

			reg := registry.GetGlobalRegistry()
			authIDs := make([]string, totalCreds)
			failSet := make(map[string]bool)
			for i := 0; i < totalCreds; i++ {
				id := fmt.Sprintf("thk-%s-%04d", name, i)
				authIDs[i] = id
				// First 4 IDs and every 40th ID fail with 429 so transient exclusions churn continuously.
				if i < 4 || i%failingEvery == 0 {
					failSet[id] = true
				}
				auth := &Auth{
					ID:       id,
					Provider: providerKey,
					Status:   StatusActive,
					Attributes: map[string]string{
						"auth_kind":    "apikey",
						"api_key":      fmt.Sprintf("key-%04d", i),
						"compat_name":  compatName,
						"provider_key": providerKey,
					},
				}
				reg.RegisterClient(id, providerKey, []*registry.ModelInfo{{ID: model}})
				if _, errReg := manager.Register(WithSkipPersist(t.Context()), auth); errReg != nil {
					t.Fatalf("register %s: %v", id, errReg)
				}
			}
			t.Cleanup(func() {
				for _, id := range authIDs {
					reg.UnregisterClient(id)
				}
			})

			selectedCounts := make(map[string]int, totalCreds)
			manager.RegisterExecutor(&mockCustomErrorExecutor{
				identifier: providerKey,
				executeFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					selectedCounts[auth.ID]++
					if failSet[auth.ID] {
						return cliproxyexecutor.Response{}, customStatusError{
							code: http.StatusTooManyRequests,
							msg:  "server_busy",
						}
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
				},
			})

			for reqIdx := 0; reqIdx < totalPicks; reqIdx++ {
				opts := cliproxyexecutor.Options{}
				if withAffinity {
					opts.Headers = http.Header{"Session_id": {fmt.Sprintf("req-session-%d", reqIdx)}}
				}
				if _, errExec := manager.Execute(t.Context(), []string{providerKey}, cliproxyexecutor.Request{Model: model}, opts); errExec != nil {
					t.Fatalf("request %d failed: %v", reqIdx, errExec)
				}
			}

			healthySelected := 0
			for _, id := range authIDs {
				if !failSet[id] && selectedCounts[id] > 0 {
					healthySelected++
				}
			}
			wantMinHealthy := totalCreds - len(failSet) - 10
			if healthySelected < wantMinHealthy {
				t.Fatalf("large WRR pool collapsed: only %d/%d healthy credentials selected across %d requests", healthySelected, totalCreds-len(failSet), totalPicks)
			}
		})
	}
}

func TestIssue7431_SmoothWeightedStateLargePoolRetainsTransientlyExcludedCredits(t *testing.T) {
	const members = 2131
	state := &smoothWeightedState{}
	full := make(map[string]int64, members)
	for i := 0; i < members; i++ {
		full[fmt.Sprintf("cred-%04d", i)] = 1
	}
	state.prepare(full)
	state.current["cred-0000"] = -(members - 1)

	subset := make(map[string]int64, members-1)
	for i := 1; i < members; i++ {
		subset[fmt.Sprintf("cred-%04d", i)] = 1
	}
	state.prepare(subset)

	if got := state.current["cred-0000"]; got != -(members - 1) {
		t.Fatalf("transiently excluded credential credit = %d, want %d", got, -(members - 1))
	}
}

func TestIssue7431_SchedulerRebuildPreservesUnchangedShardCursors(t *testing.T) {
	ctx := t.Context()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "openai"})
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "claude"})

	const model = "issue-7431-rebuild-cursor-model"
	reg := registry.GetGlobalRegistry()
	var ids []string
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("rr-auth-%02d", i)
		ids = append(ids, id)
		auth := &Auth{ID: id, Provider: "openai", Status: StatusActive}
		if _, errReg := manager.Register(WithSkipPersist(ctx), auth); errReg != nil {
			t.Fatalf("register %s: %v", id, errReg)
		}
		reg.RegisterClient(id, "openai", []*registry.ModelInfo{{ID: model}})
		manager.RefreshSchedulerEntry(id)
	}
	otherAuth := &Auth{ID: "claude-other-01", Provider: "claude", Status: StatusActive}
	if _, errReg := manager.Register(WithSkipPersist(ctx), otherAuth); errReg != nil {
		t.Fatalf("register other auth: %v", errReg)
	}
	reg.RegisterClient(otherAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	manager.RefreshSchedulerEntry(otherAuth.ID)
	t.Cleanup(func() {
		for _, id := range ids {
			reg.UnregisterClient(id)
		}
		reg.UnregisterClient(otherAuth.ID)
	})

	for wantIdx := 0; wantIdx < 3; wantIdx++ {
		picked, _, errPick := manager.pickNext(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
		if errPick != nil || picked == nil {
			t.Fatalf("pick %d failed: auth=%v err=%v", wantIdx, picked, errPick)
		}
		if picked.ID != ids[wantIdx] {
			t.Fatalf("pick %d = %q, want %q", wantIdx, picked.ID, ids[wantIdx])
		}
	}

	// Simulate scheduler rebuild triggered by unrelated config/provider updates.
	manager.scheduler.rebuild(manager.snapshotAuths())

	pickedAfter, _, errPick := manager.pickNext(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil || pickedAfter == nil {
		t.Fatalf("pick after rebuild failed: auth=%v err=%v", pickedAfter, errPick)
	}
	if pickedAfter.ID != ids[3] {
		t.Fatalf("pick after rebuild reset rotation to %q, want %q", pickedAfter.ID, ids[3])
	}
}

func TestIssue7431_DisableCoolingPreservesActiveForceCooldownOnReloadAndUpdate(t *testing.T) {
	ctx := WithSkipPersist(t.Context())
	const (
		compatName = "issue-7431-force-cool"
		model      = "issue-7431-force-cool-model"
	)
	providerKey := util.OpenAICompatibleProviderKey(compatName)
	disableCooling := true
	buildConfig := func(extraClaudeKey string) *internalconfig.Config {
		cfg := &internalconfig.Config{
			RequestRetry: 2,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           compatName,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCooling,
				Models:         []internalconfig.OpenAICompatibilityModel{{Name: model, Alias: model}},
				RequestScopedErrors: []internalconfig.RequestScopedErrorRule{{
					Status: http.StatusTooManyRequests,
					Match:  []string{"rate_limited"},
					Action: RequestScopedActionContinueAndCooldown,
				}},
			}},
		}
		if extraClaudeKey != "" {
			cfg.ClaudeKey = []internalconfig.ClaudeKey{{APIKey: extraClaudeKey}}
		}
		return cfg
	}

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetCooldownStateStore(store)
	manager.SetConfig(buildConfig(""))
	manager.SetRetryConfig(2, 0, 0)

	reg := registry.GetGlobalRegistry()
	auth0 := &Auth{
		ID:       "force-cool-auth-0",
		Provider: providerKey,
		Status:   StatusActive,
		Attributes: map[string]string{
			"auth_kind":    "apikey",
			"api_key":      "k0",
			"compat_name":  compatName,
			"provider_key": providerKey,
		},
	}
	auth1 := &Auth{
		ID:       "force-cool-auth-1",
		Provider: providerKey,
		Status:   StatusActive,
		Attributes: map[string]string{
			"auth_kind":    "apikey",
			"api_key":      "k1",
			"compat_name":  compatName,
			"provider_key": providerKey,
		},
	}
	for _, a := range []*Auth{auth0, auth1} {
		reg.RegisterClient(a.ID, providerKey, []*registry.ModelInfo{{ID: model}})
		if _, errReg := manager.Register(ctx, a); errReg != nil {
			t.Fatalf("register %s: %v", a.ID, errReg)
		}
		manager.RefreshSchedulerEntry(a.ID)
	}
	t.Cleanup(func() {
		reg.UnregisterClient(auth0.ID)
		reg.UnregisterClient(auth1.ID)
	})

	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: providerKey,
		executeFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			if auth.ID == auth0.ID {
				return cliproxyexecutor.Response{}, customStatusError{
					code: http.StatusTooManyRequests,
					msg:  "rate_limited",
				}
			}
			return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
		},
	})

	if _, errExec := manager.Execute(ctx, []string{providerKey}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExec != nil {
		t.Fatalf("initial execute failed: %v", errExec)
	}

	now := time.Now()
	got0, ok := manager.GetByID(auth0.ID)
	if !ok || IsAuthAvailableForModel(got0, model, now) {
		t.Fatalf("expected %s to enter force cooldown before reload", auth0.ID)
	}

	// 1. Unrelated config reload must NOT erase active ErrorCodeForceCooldown under disable-cooling: true.
	manager.SetConfig(buildConfig("sk-ant-unrelated"))
	got0AfterReload, _ := manager.GetByID(auth0.ID)
	if IsAuthAvailableForModel(got0AfterReload, model, now) {
		t.Fatalf("SetConfig erased active ErrorCodeForceCooldown on %s when disable-cooling: true", auth0.ID)
	}
	pickedAfterReload, _, errPick := manager.pickNext(ctx, providerKey, model, cliproxyexecutor.Options{}, nil)
	if errPick != nil || pickedAfterReload == nil || pickedAfterReload.ID != auth1.ID {
		t.Fatalf("scheduler selected %v (err=%v) after config reload, want %s while %s is force-cooled", pickedAfterReload, errPick, auth1.ID, auth0.ID)
	}

	// 2. Non-credential metadata update on auth0 must NOT erase active ErrorCodeForceCooldown.
	updatedAuth0 := got0AfterReload.Clone()
	updatedAuth0.Attributes["label_note"] = "reloaded"
	if _, errUpd := manager.Update(ctx, updatedAuth0); errUpd != nil {
		t.Fatalf("Update(%s) failed: %v", auth0.ID, errUpd)
	}
	got0AfterUpdate, _ := manager.GetByID(auth0.ID)
	if IsAuthAvailableForModel(got0AfterUpdate, model, now) {
		t.Fatalf("Update() erased active ErrorCodeForceCooldown on %s when disable-cooling: true", auth0.ID)
	}

	// 3. CooldownStateStore persistence & restore must also retain active ErrorCodeForceCooldown under disable-cooling: true.
	manager.PersistCooldownStates(ctx)
	if len(store.savedRecords()) == 0 {
		t.Fatalf("expected active ErrorCodeForceCooldown record to be persisted even with disable-cooling: true")
	}

	restoredManager := NewManager(nil, &RoundRobinSelector{}, nil)
	restoredManager.SetCooldownStateStore(&recordingCooldownStateStore{load: store.savedRecords()})
	restoredManager.SetConfig(buildConfig("sk-ant-unrelated"))
	freshAuth0 := &Auth{
		ID:         auth0.ID,
		Provider:   providerKey,
		Status:     StatusActive,
		Attributes: auth0.Attributes,
	}
	if _, errReg := restoredManager.Register(ctx, freshAuth0); errReg != nil {
		t.Fatalf("register freshAuth0: %v", errReg)
	}
	if errRestore := restoredManager.RestoreCooldownStates(ctx); errRestore != nil {
		t.Fatalf("RestoreCooldownStates failed: %v", errRestore)
	}
	got0Restored, _ := restoredManager.GetByID(auth0.ID)
	if IsAuthAvailableForModel(got0Restored, model, now) {
		t.Fatalf("RestoreCooldownStates dropped active ErrorCodeForceCooldown under disable-cooling: true")
	}
}

func TestIssue7431_LargeWeightedPoolEdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("small-subset-does-not-clamp-absent-member-debt", func(t *testing.T) {
		sel := &WeightedRoundRobinSelector{}
		route := "openai-compatible-edge:model-a"
		full := make([]*Auth, 1050)
		for i := range full {
			w := 10
			if i == 0 {
				w = 500
			}
			full[i] = &Auth{
				ID:         fmt.Sprintf("edge-auth-%04d", i),
				Provider:   "openai-compatible-edge",
				Status:     StatusActive,
				Attributes: map[string]string{"weight": fmt.Sprintf("%d", w)},
			}
		}
		picked, res := sel.reserve(route, full, "")
		if picked == nil || picked.ID != "edge-auth-0000" {
			t.Fatalf("first pick = %v, want edge-auth-0000", picked)
		}
		res.finish(true)

		// Shrink temporarily to just 2 low-weight members; edge-auth-0000 is absent (e.g. cooled).
		subset := []*Auth{full[1], full[2]}
		pickedSub, resSub := sel.reserve(route, subset, "")
		if pickedSub == nil {
			t.Fatal("subset pick returned nil")
		}
		resSub.finish(true)

		// When edge-auth-0000 returns, its large negative debt (-10490) must still hold it back,
		// rather than having been clamped to -200 (subsetTotal=20 * 10).
		pickedNext, resNext := sel.reserve(route, full, "")
		if pickedNext == nil {
			t.Fatal("next full pick returned nil")
		}
		resNext.finish(true)
		if pickedNext.ID == "edge-auth-0000" {
			t.Fatalf("edge-auth-0000 was reselected immediately after 2-auth subset; absent debt was prematurely clamped")
		}
	})

	t.Run("priority-buckets-isolated-and-forget-auth-prunes-dynamic-pool", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-prio"
		const model = "issue-7431-prio-model"
		sel := &WeightedRoundRobinSelector{}
		manager := NewManager(nil, sel, nil)
		manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: providerKey})
		reg := registry.GetGlobalRegistry()

		for i := 0; i < 1030; i++ {
			id := fmt.Sprintf("prio-hi-%04d", i)
			auth := &Auth{
				ID:       id,
				Provider: providerKey,
				Status:   StatusActive,
				Attributes: map[string]string{
					"provider_key": providerKey,
					"priority":     "10",
					"weight":       "5",
				},
			}
			if _, errReg := manager.Register(ctx, auth); errReg != nil {
				t.Fatalf("register %s: %v", id, errReg)
			}
			reg.RegisterClient(id, providerKey, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(id) })
		}
		lowAuth := &Auth{
			ID:       "prio-lo-0000",
			Provider: providerKey,
			Status:   StatusActive,
			Attributes: map[string]string{
				"provider_key": providerKey,
				"priority":     "0",
				"weight":       "5",
			},
		}
		if _, errReg := manager.Register(ctx, lowAuth); errReg != nil {
			t.Fatalf("register lowAuth: %v", errReg)
		}
		reg.RegisterClient(lowAuth.ID, providerKey, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(lowAuth.ID) })

		picked, _, errPick := manager.pickNext(ctx, providerKey, model, cliproxyexecutor.Options{}, nil)
		if errPick != nil || picked == nil || picked.ID != "prio-hi-0000" {
			t.Fatalf("pick = %v (err=%v), want prio-hi-0000", picked, errPick)
		}

		// Deregistering prio-hi-0000 must prune it from largeStates via ForgetAuth.
		if !manager.RemoveIf(ctx, "prio-hi-0000", nil) {
			t.Fatalf("RemoveIf(prio-hi-0000) returned false")
		}
		sel.mu.Lock()
		for _, state := range sel.largeStates {
			if _, found := state.findMember("prio-hi-0000"); found {
				sel.mu.Unlock()
				t.Fatalf("ForgetAuth failed to prune removed auth prio-hi-0000 from largeStates")
			}
			if _, foundLow := state.findMember("prio-lo-0000"); foundLow {
				sel.mu.Unlock()
				t.Fatalf("priority 0 auth leaked into priority 10 largeStates bucket")
			}
		}
		sel.mu.Unlock()
	})
}

func TestIssue7431_ForceCooldownEdgeCases(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	t.Run("mixed-ordinary-and-force-cooldowns-plus-wrapped-shadow-and-reset-quota", func(t *testing.T) {
		disableCooling := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{
				{
					Name:           "openai-compatible-issue-7431-edge",
					BaseURL:        "https://example.invalid/v1",
					DisableCooling: &disableCooling,
				},
			},
		}
		manager := NewManager(nil, &RoundRobinSelector{}, nil)
		manager.SetConfig(cfg)

		auth := &Auth{
			ID:       "force-edge-auth",
			Provider: "openai-compatible-issue-7431-edge",
			Status:   StatusError,
			Attributes: map[string]string{
				"provider_key": "openai-compatible-issue-7431-edge",
			},
			Unavailable:    true,
			NextRetryAfter: now.Add(5 * time.Minute),
			Quota: QuotaState{
				Exceeded:      true,
				Reason:        "credential_quota",
				NextRecoverAt: now.Add(5 * time.Minute),
			},
			LastError: &Error{Code: credentialQuotaErrorCode, HTTPStatus: 429, Message: "daily limit"},
			ModelStates: map[string]*ModelState{
				"model-force": {
					Unavailable:               true,
					Status:                    StatusError,
					NextRetryAfter:            now.Add(5 * time.Minute),
					Quota:                     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(5 * time.Minute)},
					credentialQuotaPropagated: true,
					credentialQuotaShadow: &ModelState{
						Unavailable:    true,
						Status:         StatusError,
						NextRetryAfter: now.Add(2 * time.Minute),
						Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(2 * time.Minute)},
						LastError:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 429, Message: "force model cooldown"},
						UpdatedAt:      now,
					},
					UpdatedAt: now,
				},
				"model-ordinary": {
					Unavailable:               true,
					Status:                    StatusError,
					NextRetryAfter:            now.Add(5 * time.Minute),
					Quota:                     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(5 * time.Minute)},
					credentialQuotaPropagated: true,
					credentialQuotaShadow: &ModelState{
						Unavailable:    true,
						Status:         StatusError,
						NextRetryAfter: now.Add(2 * time.Minute),
						Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(2 * time.Minute)},
						LastError:      &Error{HTTPStatus: 429, Message: "normal model cooldown"},
						UpdatedAt:      now,
					},
					UpdatedAt: now,
				},
				"model-expired-force": {
					Unavailable:    true,
					Status:         StatusError,
					NextRetryAfter: now.Add(-time.Second),
					Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(-time.Second)},
					LastError:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 429, Message: "expired force"},
					UpdatedAt:      now.Add(-time.Minute),
				},
			},
		}
		if _, errReg := manager.Register(ctx, auth); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		got, _ := manager.GetByID(auth.ID)
		if IsAuthAvailableForModel(got, "model-force", now) {
			t.Fatal("expected wrapped active ErrorCodeForceCooldown shadow on model-force to survive Register under disable-cooling")
		}
		if !IsAuthAvailableForModel(got, "model-ordinary", now) {
			t.Fatal("expected ordinary non-force cooldown on model-ordinary to be cleared under disable-cooling")
		}
		if !IsAuthAvailableForModel(got, "model-expired-force", now) {
			t.Fatal("expected expired ErrorCodeForceCooldown on model-expired-force to be cleared")
		}

		// Explicit admin ResetQuota must still clear ErrorCodeForceCooldown.
		if _, _, errReset := manager.ResetQuota(ctx, auth.ID); errReset != nil {
			t.Fatalf("ResetQuota: %v", errReset)
		}
		gotAfterReset, _ := manager.GetByID(auth.ID)
		if !IsAuthAvailableForModel(gotAfterReset, "model-force", now) {
			t.Fatal("expected explicit ResetQuota to clear ErrorCodeForceCooldown")
		}

		// Auth-level (Model == "") ErrorCodeForceCooldown recorded alongside existing ModelStates
		// must also survive SetConfig reload under disable-cooling: true.
		retry := 2 * time.Minute
		manager.MarkResult(ctx, Result{
			AuthID:     auth.ID,
			Provider:   auth.Provider,
			Success:    false,
			RetryAfter: &retry,
			Error:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "force auth cooldown"},
		})
		manager.SetConfig(cfg)
		gotAuthForce, _ := manager.GetByID(auth.ID)
		if IsAuthAvailableForModel(gotAuthForce, "", now) {
			t.Fatal("expected model-less auth-level ErrorCodeForceCooldown to survive SetConfig reload alongside ModelStates")
		}
	})

	t.Run("mark-result-interleaving-does-not-corrupt-force-credential-cooldown-provenance", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-provenance"
		disableCoolingTrue := true
		disableCoolingFalse := false
		cfgCoolingOff := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		cfgCoolingOn := &internalconfig.Config{
			DisableCooling: false,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingFalse,
			}},
		}

		// Order A: Force credential_quota first, followed by ordinary model error overwriting auth.LastError.
		mgrA := NewManager(nil, &RoundRobinSelector{}, nil)
		mgrA.SetConfig(cfgCoolingOff)
		authA := &Auth{
			ID:         "prov-auth-a",
			Provider:   providerKey,
			Status:     StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}
		if _, errReg := mgrA.Register(ctx, authA); errReg != nil {
			t.Fatalf("Register authA: %v", errReg)
		}
		fiveMin := 5 * time.Minute
		mgrA.MarkResult(ctx, Result{
			AuthID:          authA.ID,
			Provider:        providerKey,
			Model:           "model-1",
			Success:         false,
			CredentialScope: true,
			RetryAfter:      &fiveMin,
			Error:           &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "force account limit"},
		})
		mgrA.MarkResult(ctx, Result{
			AuthID:   authA.ID,
			Provider: providerKey,
			Model:    "model-2",
			Success:  false,
			Error:    &Error{Code: "server_error", HTTPStatus: http.StatusInternalServerError, Message: "ordinary 500"},
		})
		mgrA.SetConfig(cfgCoolingOff)
		gotA, _ := mgrA.GetByID(authA.ID)
		checkTime := time.Now()
		if IsAuthAvailableForModel(gotA, "model-1", checkTime) || IsAuthAvailableForModel(gotA, "model-2", checkTime) {
			t.Fatalf("ordinary model error overwrote auth.LastError and caused SetConfig to erase active force credential_quota: quota=%+v", gotA.Quota)
		}

		// Order B: Ordinary credential_quota first (cooling enabled), followed by model-scoped force cooldown,
		// then reload enabling disable-cooling: true.
		mgrB := NewManager(nil, &RoundRobinSelector{}, nil)
		mgrB.SetConfig(cfgCoolingOn)
		authB := &Auth{
			ID:         "prov-auth-b",
			Provider:   providerKey,
			Status:     StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}
		if _, errReg := mgrB.Register(ctx, authB); errReg != nil {
			t.Fatalf("Register authB: %v", errReg)
		}
		mgrB.MarkResult(ctx, Result{
			AuthID:          authB.ID,
			Provider:        providerKey,
			Model:           "model-1",
			Success:         false,
			CredentialScope: true,
			RetryAfter:      &fiveMin,
			Error:           &Error{Code: credentialQuotaErrorCode, HTTPStatus: http.StatusTooManyRequests, Message: "ordinary account limit"},
		})
		twoMin := 2 * time.Minute
		mgrB.MarkResult(ctx, Result{
			AuthID:     authB.ID,
			Provider:   providerKey,
			Model:      "model-2",
			Success:    false,
			RetryAfter: &twoMin,
			Error:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "force model limit"},
		})
		mgrB.SetConfig(cfgCoolingOff)
		gotB, _ := mgrB.GetByID(authB.ID)
		checkTimeB := time.Now()
		if !IsAuthAvailableForModel(gotB, "model-1", checkTimeB) {
			t.Fatalf("model-scoped force error overwrote auth.LastError and falsely preserved ordinary credential_quota on model-1: quota=%+v", gotB.Quota)
		}
		if IsAuthAvailableForModel(gotB, "model-2", checkTimeB) {
			t.Fatal("expected model-2's independent force cooldown to survive after ordinary credential_quota was cleared")
		}

		// Persistence + restore after auth.LastError was overwritten in Order A must also preserve force credential_quota.
		storeA := &recordingCooldownStateStore{}
		mgrA.SetCooldownStateStore(storeA)
		mgrA.PersistCooldownStates(ctx)
		mgrARestored := NewManager(nil, &RoundRobinSelector{}, nil)
		mgrARestored.SetCooldownStateStore(&recordingCooldownStateStore{load: storeA.savedRecords()})
		mgrARestored.SetConfig(cfgCoolingOff)
		if _, errReg := mgrARestored.Register(ctx, &Auth{
			ID:         authA.ID,
			Provider:   providerKey,
			Status:     StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}); errReg != nil {
			t.Fatalf("Register restored authA: %v", errReg)
		}
		if errRestore := mgrARestored.RestoreCooldownStates(ctx); errRestore != nil {
			t.Fatalf("RestoreCooldownStates authA: %v", errRestore)
		}
		gotARestored, _ := mgrARestored.GetByID(authA.ID)
		if IsAuthAvailableForModel(gotARestored, "model-1", time.Now()) || IsAuthAvailableForModel(gotARestored, "model-2", time.Now()) {
			t.Fatalf("RestoreCooldownStates lost force credential_quota after auth.LastError overwrite: quota=%+v", gotARestored.Quota)
		}
	})
}

func TestIssue7431_DynamicPoolBoundsAndReservationLifecycle(t *testing.T) {
	ctx := context.Background()

	t.Run("large-states-enforces-max-keys-capacity-bound", func(t *testing.T) {
		sel := &WeightedRoundRobinSelector{maxKeys: 4}
		pool := make([]*Auth, 1030)
		for i := range pool {
			pool[i] = &Auth{
				ID:         fmt.Sprintf("cap-auth-%04d", i),
				Provider:   "openai-compatible-cap",
				Status:     StatusActive,
				Attributes: map[string]string{"weight": "1"},
			}
		}
		_, inFlightRes := sel.reserve("openai-compatible-cap:route-0", pool, "")
		if inFlightRes == nil {
			t.Fatal("expected reservation on route-0")
		}
		for r := 1; r <= 6; r++ {
			_, res := sel.reserve(fmt.Sprintf("openai-compatible-cap:route-%d", r), pool, "")
			res.finish(true)
		}
		sel.mu.Lock()
		gotLargeLen := len(sel.largeStates)
		sel.mu.Unlock()
		if gotLargeLen > 4 {
			t.Fatalf("len(sel.largeStates) = %d, want <= maxKeys (4)", gotLargeLen)
		}
		inFlightRes.finish(false)

		// All-in-flight eviction must isolate stale reservations from newly created state for the same route.
		sel2 := &WeightedRoundRobinSelector{maxKeys: 2}
		_, evictResA := sel2.reserve("openai-compatible-cap:a", pool, "")
		_, evictResB := sel2.reserve("openai-compatible-cap:b", pool, "")
		_, evictResC := sel2.reserve("openai-compatible-cap:c", pool, "")
		evictResB.finish(true)
		evictResC.finish(true)
		// Re-create route "a" and commit one clean pick, then roll back stale evictResA.
		_, freshResA := sel2.reserve("openai-compatible-cap:a", pool, "")
		freshResA.finish(true)
		evictResA.finish(false)
		sel2.mu.Lock()
		stateA := sel2.largeStates["openai-compatible-cap:a"]
		for _, m := range stateA.members {
			if m.pending != 0 {
				sel2.mu.Unlock()
				t.Fatalf("evicted reservation rollback corrupted fresh route state: member %s pending=%d", m.id, m.pending)
			}
		}
		sel2.mu.Unlock()
	})

	t.Run("forget-and-reregister-same-id-does-not-pollute-pending-credit", func(t *testing.T) {
		sel := &WeightedRoundRobinSelector{}
		const route = "openai-compatible-epoch:model-x"
		pool := make([]*Auth, 1030)
		for i := range pool {
			pool[i] = &Auth{
				ID:         fmt.Sprintf("epoch-auth-%04d", i),
				Provider:   "openai-compatible-epoch",
				Status:     StatusActive,
				Attributes: map[string]string{"weight": "1"},
			}
		}
		picked1, staleRes := sel.reserve(route, pool, "")
		if picked1 == nil || picked1.ID != "epoch-auth-0000" {
			t.Fatalf("first pick = %v, want epoch-auth-0000", picked1)
		}

		// Permanently remove epoch-auth-0000 while staleRes is still in-flight,
		// then re-register a fresh credential with the same ID.
		sel.ForgetAuth("epoch-auth-0000")
		_, freshRes := sel.reserve(route, pool, "")
		freshRes.finish(true)

		// Roll back staleRes; it must NOT write +1029 pending credit onto the newly registered epoch-auth-0000.
		staleRes.finish(false)

		sel.mu.Lock()
		state := sel.largeStates[route]
		var nonZeroPendingID string
		var nonZeroPendingVal int64
		if state != nil {
			for _, m := range state.members {
				if m.pending != 0 {
					nonZeroPendingID = m.id
					nonZeroPendingVal = m.pending
					break
				}
			}
		}
		sel.mu.Unlock()
		if nonZeroPendingID != "" {
			t.Fatalf("member %s has leaked pending=%d after stale reservation rollback across ForgetAuth+re-register, want 0", nonZeroPendingID, nonZeroPendingVal)
		}
	})

	t.Run("large-weighted-pool-preserves-progress-across-scheduler-rebuild", func(t *testing.T) {
		const (
			providerA = "openai-compatible-wrr-rebuild-a"
			providerB = "openai-compatible-wrr-rebuild-b"
			model     = "issue-7431-wrr-rebuild-model"
		)
		sel := &WeightedRoundRobinSelector{}
		manager := NewManager(nil, sel, nil)
		manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: providerA})
		manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: providerB})
		reg := registry.GetGlobalRegistry()

		allAuths := make([]*Auth, 0, 1031)
		for i := 0; i < 1030; i++ {
			id := fmt.Sprintf("wrr-reb-%04d", i)
			a := &Auth{
				ID:         id,
				Provider:   providerA,
				Status:     StatusActive,
				Attributes: map[string]string{"provider_key": providerA, "weight": "1"},
			}
			reg.RegisterClient(id, providerA, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(id) })
			registered, errReg := manager.Register(WithSkipPersist(ctx), a)
			if errReg != nil {
				t.Fatalf("Register %s: %v", id, errReg)
			}
			allAuths = append(allAuths, registered)
		}

		for i := 0; i < 3; i++ {
			picked, _, errPick := manager.pickNext(ctx, providerA, model, cliproxyexecutor.Options{}, nil)
			if errPick != nil || picked == nil || picked.ID != fmt.Sprintf("wrr-reb-%04d", i) {
				t.Fatalf("pick %d before rebuild = %v (err=%v), want wrr-reb-%04d", i, picked, errPick, i)
			}
		}

		unrelated := &Auth{
			ID:         "unrelated-b-0000",
			Provider:   providerB,
			Status:     StatusActive,
			Attributes: map[string]string{"provider_key": providerB, "weight": "1"},
		}
		reg.RegisterClient(unrelated.ID, providerB, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(unrelated.ID) })
		regB, errReg := manager.Register(WithSkipPersist(ctx), unrelated)
		if errReg != nil {
			t.Fatalf("Register unrelated: %v", errReg)
		}
		allAuths = append(allAuths, regB)
		manager.scheduler.rebuild(allAuths)

		pickedAfter, _, errPick := manager.pickNext(ctx, providerA, model, cliproxyexecutor.Options{}, nil)
		if errPick != nil || pickedAfter == nil || pickedAfter.ID != "wrr-reb-0003" {
			t.Fatalf("pick after rebuild = %v (err=%v), want wrr-reb-0003", pickedAfter, errPick)
		}
	})
}

func TestIssue7431_SameLayerForceCooldownAndPersistenceAndEpochRaces(t *testing.T) {
	ctx := context.Background()

	t.Run("same-layer-ordinary-error-does-not-clear-active-force-cooldown", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-samelayer"
		disableCoolingTrue := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetConfig(cfg)

		authModel := &Auth{
			ID:         "samelayer-model-auth",
			Provider:   providerKey,
			Status:     StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}
		authGlobal := &Auth{
			ID:         "samelayer-global-auth",
			Provider:   providerKey,
			Status:     StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), authModel); errReg != nil {
			t.Fatalf("register authModel: %v", errReg)
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), authGlobal); errReg != nil {
			t.Fatalf("register authGlobal: %v", errReg)
		}

		twoMin := 2 * time.Minute
		// 1. Same model: force 503 followed by in-flight ordinary 500 under disable-cooling: true.
		mgr.MarkResult(ctx, Result{
			AuthID:     authModel.ID,
			Provider:   providerKey,
			Model:      "model-same",
			Success:    false,
			RetryAfter: &twoMin,
			Error:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "force 503"},
		})
		mgr.MarkResult(ctx, Result{
			AuthID:   authModel.ID,
			Provider: providerKey,
			Model:    "model-same",
			Success:  false,
			Error:    &Error{Code: "server_error", HTTPStatus: http.StatusInternalServerError, Message: "ordinary 500"},
		})
		now := time.Now()
		gotModel, _ := mgr.GetByID(authModel.ID)
		if IsAuthAvailableForModel(gotModel, "model-same", now) {
			t.Fatalf("ordinary 500 on same model cleared active force cooldown immediately: state=%+v", gotModel.ModelStates["model-same"])
		}
		mgr.SetConfig(cfg)
		gotModelAfterReload, _ := mgr.GetByID(authModel.ID)
		if IsAuthAvailableForModel(gotModelAfterReload, "model-same", now) {
			t.Fatalf("SetConfig after ordinary 500 on same model cleared active force cooldown: state=%+v", gotModelAfterReload.ModelStates["model-same"])
		}

		// 2. Model-less auth level (Model == ""): force 503 followed by in-flight ordinary 500 under disable-cooling: true.
		mgr.MarkResult(ctx, Result{
			AuthID:     authGlobal.ID,
			Provider:   providerKey,
			Success:    false,
			RetryAfter: &twoMin,
			Error:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "force 503"},
		})
		mgr.MarkResult(ctx, Result{
			AuthID:   authGlobal.ID,
			Provider: providerKey,
			Success:  false,
			Error:    &Error{Code: "server_error", HTTPStatus: http.StatusInternalServerError, Message: "ordinary 500"},
		})
		gotGlobal, _ := mgr.GetByID(authGlobal.ID)
		if IsAuthAvailableForModel(gotGlobal, "", now) {
			t.Fatalf("ordinary 500 on model-less auth cleared active force cooldown immediately: RetryAfter=%v", gotGlobal.NextRetryAfter)
		}
		mgr.SetConfig(cfg)
		gotGlobalAfterReload, _ := mgr.GetByID(authGlobal.ID)
		if IsAuthAvailableForModel(gotGlobalAfterReload, "", now) {
			t.Fatalf("SetConfig after ordinary 500 on model-less auth cleared active force cooldown: RetryAfter=%v", gotGlobalAfterReload.NextRetryAfter)
		}
	})

	t.Run("persisted-force-deadlines-preserve-exact-unequal-bounds-across-disk-roundtrip", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-disk"
		disableCoolingFalse := false
		disableCoolingTrue := true
		cfgOn := &internalconfig.Config{
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingFalse,
			}},
		}
		cfgOff := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}

		cdsDir := t.TempDir()
		fileStore := NewFileCooldownStateStore(cdsDir)
		mgr1 := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr1.SetCooldownStateStore(fileStore)
		mgr1.SetConfig(cfgOn)

		// Auth 1: 2m force cooldown on model-x, then extended to 10m by an ordinary 429.
		a1 := &Auth{ID: "disk-auth-1", Provider: providerKey, Status: StatusActive, Attributes: map[string]string{"provider_key": providerKey}}
		// Auth 2: 2m force credential_quota on model-a + 10m force independent shadow on model-b.
		a2 := &Auth{ID: "disk-auth-2", Provider: providerKey, Status: StatusActive, Attributes: map[string]string{"provider_key": providerKey}}
		if _, errReg := mgr1.Register(WithSkipPersist(ctx), a1); errReg != nil {
			t.Fatalf("register a1: %v", errReg)
		}
		if _, errReg := mgr1.Register(WithSkipPersist(ctx), a2); errReg != nil {
			t.Fatalf("register a2: %v", errReg)
		}

		twoMin := 2 * time.Minute
		tenMin := 10 * time.Minute
		mgr1.MarkResult(ctx, Result{
			AuthID: a1.ID, Provider: providerKey, Model: "model-x", RetryAfter: &twoMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "force 2m"},
		})
		mgr1.MarkResult(ctx, Result{
			AuthID: a1.ID, Provider: providerKey, Model: "model-x", RetryAfter: &tenMin,
			Error: &Error{Code: "rate_limited", HTTPStatus: http.StatusTooManyRequests, Message: "ordinary 10m"},
		})

		mgr1.MarkResult(ctx, Result{
			AuthID: a2.ID, Provider: providerKey, Model: "model-a", CredentialScope: true, RetryAfter: &twoMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "force cred 2m"},
		})
		mgr1.MarkResult(ctx, Result{
			AuthID: a2.ID, Provider: providerKey, Model: "model-b", RetryAfter: &tenMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "force model-b 10m"},
		})
		mgr1.PersistCooldownStates(ctx)

		// Restart with disable-cooling: true and restore from real .cds files on disk.
		mgr2 := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr2.SetCooldownStateStore(NewFileCooldownStateStore(cdsDir))
		mgr2.SetConfig(cfgOff)
		for _, id := range []string{a1.ID, a2.ID} {
			if _, errReg := mgr2.Register(WithSkipPersist(ctx), &Auth{
				ID: id, Provider: providerKey, Status: StatusActive, Attributes: map[string]string{"provider_key": providerKey},
			}); errReg != nil {
				t.Fatalf("register %s in mgr2: %v", id, errReg)
			}
		}
		if errRestore := mgr2.RestoreCooldownStates(ctx); errRestore != nil {
			t.Fatalf("RestoreCooldownStates: %v", errRestore)
		}

		baseNow := time.Now()
		gotA1, _ := mgr2.GetByID(a1.ID)
		if IsAuthAvailableForModel(gotA1, "model-x", baseNow.Add(time.Minute)) {
			t.Fatal("expected disk-auth-1 model-x to remain blocked within 2m force deadline")
		}
		if !IsAuthAvailableForModel(gotA1, "model-x", baseNow.Add(3*time.Minute)) {
			t.Fatalf("disk-auth-1 model-x remained blocked at +3m; 10m ordinary extension inflated restored force deadline: %+v", gotA1.ModelStates["model-x"])
		}

		gotA2, _ := mgr2.GetByID(a2.ID)
		if IsAuthAvailableForModel(gotA2, "model-a", baseNow.Add(time.Minute)) {
			t.Fatal("expected disk-auth-2 model-a to remain blocked within 2m force credential_quota")
		}
		if !IsAuthAvailableForModel(gotA2, "model-a", baseNow.Add(3*time.Minute)) {
			t.Fatalf("disk-auth-2 model-a remained blocked at +3m; model-b 10m shadow inflated credQuotaForceUntil: quota=%+v", gotA2.Quota)
		}
		if IsAuthAvailableForModel(gotA2, "model-b", baseNow.Add(5*time.Minute)) {
			t.Fatal("expected disk-auth-2 model-b to remain blocked by its 10m independent force shadow at +5m")
		}
	})

	t.Run("concurrent-load-persist-and-get-has-no-data-race", func(t *testing.T) {
		now := time.Now()
		loadStore := &blockingSelectiveAuthStore{
			onList: func() ([]*Auth, error) {
				return []*Auth{{
					ID:             "load-race-auth",
					Provider:       "xai",
					Status:         StatusError,
					Unavailable:    true,
					NextRetryAfter: now.Add(5 * time.Minute),
					LastError:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "force"},
					ModelStates: map[string]*ModelState{
						"grok-4": {
							Unavailable:    true,
							Status:         StatusError,
							NextRetryAfter: now.Add(5 * time.Minute),
							LastError:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "force"},
							UpdatedAt:      now,
						},
					},
				}}, nil
			},
		}
		cdsStore := &recordingCooldownStateStore{}
		mgr := NewManager(loadStore, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(cdsStore)
		if errLoad := mgr.Load(ctx); errLoad != nil {
			t.Fatalf("Load: %v", errLoad)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(3)
			go func() {
				defer wg.Done()
				<-start
				_ = mgr.Load(ctx)
			}()
			go func() {
				defer wg.Done()
				<-start
				mgr.PersistCooldownStates(ctx)
			}()
			go func() {
				defer wg.Done()
				<-start
				_, _ = mgr.GetByID("load-race-auth")
			}()
		}
		close(start)
		wg.Wait()
	})

	t.Run("delayed-forget-after-remove-does-not-wipe-reregistered-auth", func(t *testing.T) {
		const (
			providerKey = "openai-compatible-issue-7431-forget-race"
			model       = "issue-7431-forget-race-model"
		)
		sel := &WeightedRoundRobinSelector{}
		aff := NewSessionAffinitySelector(sel)
		t.Cleanup(aff.Stop)
		mgr := NewManager(nil, aff, nil)
		mgr.RegisterExecutor(&mockCustomErrorExecutor{identifier: providerKey})
		reg := registry.GetGlobalRegistry()

		for i := 0; i < 1030; i++ {
			id := fmt.Sprintf("fr-auth-%04d", i)
			reg.RegisterClient(id, providerKey, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(id) })
			if _, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
				ID: id, Provider: providerKey, Status: StatusActive,
				Attributes: map[string]string{"provider_key": providerKey, "weight": "1"},
			}); errReg != nil {
				t.Fatalf("register %s: %v", id, errReg)
			}
		}

		// Pick once so fr-auth-0000 (epoch 1) enters largeStates.
		picked1, _, errPick1 := mgr.pickNext(ctx, providerKey, model, cliproxyexecutor.Options{}, nil)
		if errPick1 != nil || picked1 == nil || picked1.ID != "fr-auth-0000" {
			t.Fatalf("first pick = %v (err=%v), want fr-auth-0000", picked1, errPick1)
		}

		// Remove fr-auth-0000, then immediately re-register fr-auth-0000 (higher RegistrationEpoch)
		// and select again so the new instance is active in largeStates with credit.
		if !mgr.RemoveIf(ctx, "fr-auth-0000", nil) {
			t.Fatal("RemoveIf(fr-auth-0000) returned false")
		}
		reRegistered, errReReg := mgr.Register(WithSkipPersist(ctx), &Auth{
			ID: "fr-auth-0000", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey, "weight": "1"},
		})
		if errReReg != nil {
			t.Fatalf("re-register fr-auth-0000: %v", errReReg)
		}
		// Pick on the pool so re-registered fr-auth-0000 (epoch >= 3) is tracked in largeStates.
		if _, _, errPick2 := mgr.pickNext(ctx, providerKey, model, cliproxyexecutor.Options{}, nil); errPick2 != nil {
			t.Fatalf("second pick failed: %v", errPick2)
		}

		// Simulate a delayed ForgetAuth from the prior RemoveIf (whose tombstoneEpoch < reRegistered.RegistrationEpoch).
		staleTombstoneEpoch := reRegistered.RegistrationEpoch - 1
		mgr.forgetWeightedAuthUpTo("fr-auth-0000", staleTombstoneEpoch, ^uint64(0))

		sel.mu.Lock()
		var foundNew bool
		for _, state := range sel.largeStates {
			if idx, ok := state.findMember("fr-auth-0000"); ok && state.members[idx].current != 0 {
				foundNew = true
			}
		}
		sel.mu.Unlock()
		if !foundNew {
			t.Fatal("delayed forget from earlier RemoveIf wiped re-registered fr-auth-0000 state")
		}
	})

	t.Run("ordinary-credential-scope-error-does-not-clear-active-model-force-cooldown", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-cred-ordinary"
		disableCoolingTrue := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		cdsDir := t.TempDir()
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(NewFileCooldownStateStore(cdsDir))
		mgr.SetConfig(cfg)

		a := &Auth{
			ID:         "cred-ordinary-over-model-force",
			Provider:   providerKey,
			Status:     StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), a); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		twoMin := 2 * time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID:     a.ID,
			Provider:   providerKey,
			Model:      "model-1",
			RetryAfter: &twoMin,
			Error:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "force 503"},
		})
		mgr.MarkResult(ctx, Result{
			AuthID:          a.ID,
			Provider:        providerKey,
			Model:           "model-1",
			CredentialScope: true,
			Error:           &Error{Code: credentialQuotaErrorCode, HTTPStatus: http.StatusTooManyRequests, Message: "ordinary cred 429"},
		})

		now := time.Now()
		gotImmediate, _ := mgr.GetByID(a.ID)
		if IsAuthAvailableForModel(gotImmediate, "model-1", now) {
			t.Fatalf("ordinary CredentialScope 429 erased active model force cooldown immediately: state=%+v", gotImmediate.ModelStates["model-1"])
		}
		mgr.SetConfig(cfg)
		gotAfterReload, _ := mgr.GetByID(a.ID)
		if IsAuthAvailableForModel(gotAfterReload, "model-1", now) {
			t.Fatalf("SetConfig after ordinary CredentialScope 429 erased active model force cooldown: state=%+v", gotAfterReload.ModelStates["model-1"])
		}
		mgr.PersistCooldownStates(ctx)
		mgrRestored := NewManager(nil, &RoundRobinSelector{}, nil)
		mgrRestored.SetCooldownStateStore(NewFileCooldownStateStore(cdsDir))
		mgrRestored.SetConfig(cfg)
		if _, errReg := mgrRestored.Register(WithSkipPersist(ctx), &Auth{
			ID: a.ID, Provider: providerKey, Status: StatusActive, Attributes: map[string]string{"provider_key": providerKey},
		}); errReg != nil {
			t.Fatalf("Register restored: %v", errReg)
		}
		if errRestore := mgrRestored.RestoreCooldownStates(ctx); errRestore != nil {
			t.Fatalf("RestoreCooldownStates: %v", errRestore)
		}
		gotRestored, _ := mgrRestored.GetByID(a.ID)
		if IsAuthAvailableForModel(gotRestored, "model-1", now) {
			t.Fatalf("RestoreCooldownStates lost active model force cooldown after ordinary CredentialScope 429: state=%+v", gotRestored.ModelStates["model-1"])
		}
	})

	t.Run("mixed-transport-large-websocket-pool-keeps-credits-when-http-peer-excluded", func(t *testing.T) {
		const model = "gpt-5-ws-mixed"
		wsCtx := cliproxyexecutor.WithDownstreamWebsocket(ctx)

		// 1. Direct selector / legacy path: excluding the sole HTTP peer in tried must not switch dynamic pool keys.
		sel := &WeightedRoundRobinSelector{}
		all := make([]*Auth, 0, 1031)
		wsOnly := make([]*Auth, 0, 1030)
		httpAuth := &Auth{
			ID:       "codex-http-0000",
			Provider: "codex",
			Status:   StatusActive,
			Attributes: map[string]string{
				"weight":     "1",
				"websockets": "false",
			},
		}
		all = append(all, httpAuth)
		for i := 0; i < 1030; i++ {
			a := &Auth{
				ID:       fmt.Sprintf("codex-ws-%04d", i),
				Provider: "codex",
				Status:   StatusActive,
				Attributes: map[string]string{
					"weight":     "1",
					"websockets": "true",
				},
			}
			all = append(all, a)
			wsOnly = append(wsOnly, a)
		}

		for i := 0; i < 3; i++ {
			picked, errPick := sel.Pick(wsCtx, "codex", model, cliproxyexecutor.Options{}, all)
			if errPick != nil || picked == nil || picked.ID != fmt.Sprintf("codex-ws-%04d", i) {
				t.Fatalf("selector pick %d with HTTP peer present = %v (err=%v), want codex-ws-%04d", i, picked, errPick, i)
			}
		}
		pickedWithoutHTTP, errPick := sel.Pick(wsCtx, "codex", model, cliproxyexecutor.Options{}, wsOnly)
		if errPick != nil || pickedWithoutHTTP == nil || pickedWithoutHTTP.ID != "codex-ws-0003" {
			t.Fatalf("selector pick after HTTP peer excluded = %v (err=%v), want codex-ws-0003", pickedWithoutHTTP, errPick)
		}

		// 2. Scheduler fast path: cooling the sole HTTP peer must not switch dynamic pool keys.
		schedSel := &WeightedRoundRobinSelector{}
		mgr := NewManager(nil, schedSel, nil)
		mgr.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex"})
		reg := registry.GetGlobalRegistry()
		for _, a := range all {
			id := a.ID
			reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(id) })
			if _, errReg := mgr.Register(WithSkipPersist(ctx), a.Clone()); errReg != nil {
				t.Fatalf("Register %s: %v", id, errReg)
			}
		}
		for i := 0; i < 3; i++ {
			picked, _, errPickSched := mgr.pickNext(wsCtx, "codex", model, cliproxyexecutor.Options{}, nil)
			if errPickSched != nil || picked == nil || picked.ID != fmt.Sprintf("codex-ws-%04d", i) {
				t.Fatalf("scheduler pick %d before HTTP cooldown = %v (err=%v), want codex-ws-%04d", i, picked, errPickSched, i)
			}
		}
		oneMin := time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID:     httpAuth.ID,
			Provider:   "codex",
			Model:      model,
			RetryAfter: &oneMin,
			Error:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "http down"},
		})
		pickedAfterCool, _, errPickAfter := mgr.pickNext(wsCtx, "codex", model, cliproxyexecutor.Options{}, nil)
		if errPickAfter != nil || pickedAfterCool == nil || pickedAfterCool.ID != "codex-ws-0003" {
			t.Fatalf("scheduler pick after HTTP peer cooled = %v (err=%v), want codex-ws-0003", pickedAfterCool, errPickAfter)
		}
	})

	t.Run("small-exact-pool-isolates-inflight-reservations-across-register-replacement-and-load", func(t *testing.T) {
		const (
			providerKey = "openai-compatible-issue-7431-small-epoch"
			route       = providerKey + ":small-model"
		)
		loadItems := []*Auth{
			{ID: "small-0", Provider: providerKey, Status: StatusActive, Attributes: map[string]string{"provider_key": providerKey, "weight": "1"}},
			{ID: "small-1", Provider: providerKey, Status: StatusActive, Attributes: map[string]string{"provider_key": providerKey, "weight": "1"}},
			{ID: "small-2", Provider: providerKey, Status: StatusActive, Attributes: map[string]string{"provider_key": providerKey, "weight": "1"}},
		}
		store := &schedulerLoadStore{auths: []*Auth{loadItems[0].Clone(), loadItems[1].Clone(), loadItems[2].Clone()}}
		sel := &WeightedRoundRobinSelector{}
		mgr := NewManager(store, sel, nil)
		if errLoad := mgr.Load(ctx); errLoad != nil {
			t.Fatalf("initial Load: %v", errLoad)
		}

		currentPool := func() []*Auth {
			out := make([]*Auth, 0, 3)
			for _, id := range []string{"small-0", "small-1", "small-2"} {
				a, _ := mgr.GetByID(id)
				out = append(out, a)
			}
			return out
		}

		// 1. In-place Register replacement (without RemoveIf) advances small-0's RegistrationEpoch.
		pickedStale, staleRegRes := sel.reserve(route, currentPool(), "")
		if pickedStale == nil || pickedStale.ID != "small-0" {
			t.Fatalf("initial pick = %v, want small-0", pickedStale)
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
			ID: "small-0", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey, "weight": "1"},
		}); errReg != nil {
			t.Fatalf("in-place Register replacement: %v", errReg)
		}
		// New small-0 (epoch 2) must not inherit staleRegRes's -2 pending debt, and a delayed
		// commit of staleRegRes must be ignored rather than charging the new small-0.
		pickedFresh, freshRegRes := sel.reserve(route, currentPool(), "")
		if pickedFresh == nil || pickedFresh.ID != "small-0" {
			t.Fatalf("pick after in-place Register replacement = %v, want fresh small-0 (not suppressed by stale pending)", pickedFresh)
		}
		freshRegRes.finish(false)
		staleRegRes.finish(true)

		sel.mu.Lock()
		for _, head := range sel.states {
			for st := head; st != nil; st = st.next {
				for _, m := range st.members {
					if m.pending != 0 || (m.id == "small-0" && m.current != 0) {
						sel.mu.Unlock()
						t.Fatalf("in-place Register replacement failed to isolate stale reservation: member %s current=%d pending=%d", m.id, m.current, m.pending)
					}
				}
			}
		}
		sel.mu.Unlock()

		// 2. Manager.Load advances RegistrationEpoch across retained IDs while a reservation is in-flight.
		_, staleLoadRes := sel.reserve(route, currentPool(), "")
		store.auths = []*Auth{loadItems[0].Clone(), loadItems[1].Clone(), loadItems[2].Clone()}
		if errLoad := mgr.Load(ctx); errLoad != nil {
			t.Fatalf("second Load: %v", errLoad)
		}
		pickedAfterLoad, freshLoadRes := sel.reserve(route, currentPool(), "")
		if pickedAfterLoad == nil || pickedAfterLoad.ID != "small-0" {
			t.Fatalf("pick after Load = %v, want small-0", pickedAfterLoad)
		}
		freshLoadRes.finish(false)
		staleLoadRes.finish(true)

		sel.mu.Lock()
		for _, head := range sel.states {
			for st := head; st != nil; st = st.next {
				for _, m := range st.members {
					if m.pending != 0 || m.current != 0 {
						sel.mu.Unlock()
						t.Fatalf("Load epoch bump failed to isolate stale reservation: member %s current=%d pending=%d", m.id, m.current, m.pending)
					}
				}
			}
		}
		sel.mu.Unlock()
	})

	t.Run("consecutive-force-429-without-retry-after-preserves-escalated-backoff-across-reload", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-backoff"
		disableCoolingTrue := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetConfig(cfg)

		past := time.Now().Add(-time.Minute)
		// Pre-seed BackoffLevel=3 (expired window) so next escalated quota backoff is 1s * 2^3 = 8s (>> 1s base).
		authModel := &Auth{
			ID:         "backoff-model-auth",
			Provider:   providerKey,
			Status:     StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
			ModelStates: map[string]*ModelState{
				"m1": {
					Status: StatusActive,
					Quota:  QuotaState{BackoffLevel: 3, NextRecoverAt: past},
				},
			},
		}
		authCred := &Auth{
			ID:         "backoff-cred-auth",
			Provider:   providerKey,
			Status:     StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
			Quota:      QuotaState{Exceeded: true, Reason: "credential_quota", BackoffLevel: 3, NextRecoverAt: past},
		}
		authGlobal := &Auth{
			ID:         "backoff-global-auth",
			Provider:   providerKey,
			Status:     StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
			Quota:      QuotaState{BackoffLevel: 3, NextRecoverAt: past},
		}
		for _, a := range []*Auth{authModel, authCred, authGlobal} {
			if _, errReg := mgr.Register(WithSkipPersist(ctx), a); errReg != nil {
				t.Fatalf("Register %s: %v", a.ID, errReg)
			}
		}
		// Restore BackoffLevel=3 in runtime after initial Register cleared expired QuotaState.
		mgr.mu.Lock()
		mgr.auths[authModel.ID].ModelStates["m1"].Quota.BackoffLevel = 3
		mgr.auths[authCred.ID].Quota.Exceeded = true
		mgr.auths[authCred.ID].Quota.Reason = "credential_quota"
		mgr.auths[authCred.ID].Quota.BackoffLevel = 3
		mgr.auths[authGlobal.ID].Quota.BackoffLevel = 3
		mgr.mu.Unlock()

		beforeMark := time.Now()
		mgr.MarkResult(ctx, Result{
			AuthID: authModel.ID, Provider: providerKey, Model: "m1",
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "force 429 no hint"},
		})
		mgr.MarkResult(ctx, Result{
			AuthID: authCred.ID, Provider: providerKey, Model: "m1", CredentialScope: true,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "force cred 429 no hint"},
		})
		mgr.MarkResult(ctx, Result{
			AuthID: authGlobal.ID, Provider: providerKey,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "force global 429 no hint"},
		})

		mgr.SetConfig(cfg)
		probeAt := beforeMark.Add(4 * time.Second) // > 1s base floor, < 8s escalated backoff
		gotModel, _ := mgr.GetByID(authModel.ID)
		if IsAuthAvailableForModel(gotModel, "m1", probeAt) {
			t.Fatalf("SetConfig truncated escalated model force 429 backoff to <=4s (want ~8s): state=%+v", gotModel.ModelStates["m1"])
		}
		gotCred, _ := mgr.GetByID(authCred.ID)
		if IsAuthAvailableForModel(gotCred, "m1", probeAt) {
			t.Fatalf("SetConfig truncated escalated credential_quota force 429 backoff to <=4s (want ~8s): quota=%+v", gotCred.Quota)
		}
		gotGlobal, _ := mgr.GetByID(authGlobal.ID)
		if IsAuthAvailableForModel(gotGlobal, "", probeAt) {
			t.Fatalf("SetConfig truncated escalated auth-level force 429 backoff to <=4s (want ~8s): NextRetryAfter=%v", gotGlobal.NextRetryAfter)
		}
	})

	t.Run("http-bound-large-pool-keeps-credits-when-last-websocket-peer-excluded", func(t *testing.T) {
		const model = "gpt-5-http-bound"
		wsCtx := cliproxyexecutor.WithDownstreamWebsocket(ctx)

		// 1. Legacy SessionAffinitySelector path: HTTP-bound session charges 1030 HTTP peers;
		// excluding the sole WS peer must keep charging the same "http" dynamic pool.
		baseSel := &WeightedRoundRobinSelector{}
		affSel := NewSessionAffinitySelector(baseSel)
		t.Cleanup(affSel.Stop)

		all := make([]*Auth, 0, 1031)
		httpOnly := make([]*Auth, 0, 1030)
		wsPeer := &Auth{
			ID:       "codex-ws-sole",
			Provider: "codex",
			Status:   StatusActive,
			Attributes: map[string]string{
				"weight":     "1",
				"websockets": "true",
			},
		}
		all = append(all, wsPeer)
		for i := 0; i < 1030; i++ {
			a := &Auth{
				ID:       fmt.Sprintf("codex-http-%04d", i),
				Provider: "codex",
				Status:   StatusActive,
				Attributes: map[string]string{
					"weight":     "1",
					"websockets": "false",
				},
			}
			all = append(all, a)
			httpOnly = append(httpOnly, a)
		}

		stickyOpts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"http-sticky-sess"}}}
		// Bind session to codex-http-0000 on an HTTP request first, then send downstream WS requests.
		if _, errBind := affSel.Pick(ctx, "codex", model, stickyOpts, httpOnly); errBind != nil {
			t.Fatalf("bind HTTP session: %v", errBind)
		}
		if _, errPickWS1 := affSel.Pick(wsCtx, "codex", model, stickyOpts, all); errPickWS1 != nil {
			t.Fatalf("WS pick with WS peer present: %v", errPickWS1)
		}
		if _, errPickWS2 := affSel.Pick(wsCtx, "codex", model, stickyOpts, httpOnly); errPickWS2 != nil {
			t.Fatalf("WS pick after WS peer excluded: %v", errPickWS2)
		}
		httpBucketKey := dynamicWeightedPoolKey("codex:"+model, 0, "http")
		baseSel.mu.Lock()
		largeBucketCount := len(baseSel.largeStates)
		httpBucket := baseSel.largeStates[httpBucketKey]
		var httpBoundDebt int64
		if httpBucket != nil {
			if idx, ok := httpBucket.findMember("codex-http-0000"); ok {
				httpBoundDebt = httpBucket.members[idx].current
			}
		}
		baseSel.mu.Unlock()
		if largeBucketCount != 2 || httpBoundDebt != -2*1029 {
			t.Fatalf("expected HTTP-bound WS traffic to accumulate both picks in 'http' bucket (debt=%d, want %d; buckets=%d, want 2)", httpBoundDebt, -2*1029, largeBucketCount)
		}

		// 2. Scheduler fast path: pinned HTTP auth on downstream WS keeps using the same "http" dynamic pool after sole WS peer cools.
		schedSel := &WeightedRoundRobinSelector{}
		mgr := NewManager(nil, schedSel, nil)
		mgr.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex"})
		reg := registry.GetGlobalRegistry()
		for _, a := range all {
			id := a.ID
			reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(id) })
			if _, errReg := mgr.Register(WithSkipPersist(ctx), a.Clone()); errReg != nil {
				t.Fatalf("Register %s: %v", id, errReg)
			}
		}
		pinOpts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "codex-http-0000"}}
		if _, _, errP1 := mgr.pickNext(wsCtx, "codex", model, pinOpts, nil); errP1 != nil {
			t.Fatalf("scheduler pinned pick before WS cooldown: %v", errP1)
		}
		oneMin := time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID: wsPeer.ID, Provider: "codex", Model: model, RetryAfter: &oneMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "ws down"},
		})
		if _, _, errP2 := mgr.pickNext(wsCtx, "codex", model, pinOpts, nil); errP2 != nil {
			t.Fatalf("scheduler pinned pick after WS cooldown: %v", errP2)
		}
		schedSel.mu.Lock()
		schedBucketCount := len(schedSel.largeStates)
		schedSel.mu.Unlock()
		if schedBucketCount != 1 {
			t.Fatalf("scheduler switched dynamic buckets when sole WS peer cooled: got %d buckets, want 1", schedBucketCount)
		}
	})

	t.Run("delayed-store-restore-does-not-shorten-newer-live-auth-force-cooldown", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-delayed-restore"
		disableCoolingTrue := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		baseNow := time.Now()
		staleRetry := baseNow.Add(2 * time.Minute)
		loadEntered := make(chan struct{})
		loadRelease := make(chan struct{})
		slowStore := &issue7431BlockingLoadCooldownStore{
			records: []CooldownStateRecord{{
				Provider:               providerKey,
				AuthID:                 "delayed-auth-0",
				Status:                 "cooling",
				NextRetryAfter:         staleRetry,
				Reason:                 ErrorCodeForceCooldown,
				LastError:              &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "older 2m force"},
				UpdatedAt:              baseNow.Add(-time.Minute),
				ForceCooldownUntil:     staleRetry,
				ForceCooldownEvaluated: true,
			}},
			entered: loadEntered,
			release: loadRelease,
		}

		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(slowStore)
		mgr.SetConfig(cfg)
		if _, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
			ID: "delayed-auth-0", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		restoreDone := make(chan error, 1)
		go func() {
			restoreDone <- mgr.RestoreCooldownStates(ctx)
		}()
		<-loadEntered

		// Record a newer 10-minute auth-level force cooldown while store.Load is in flight.
		tenMin := 10 * time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID: "delayed-auth-0", Provider: providerKey, RetryAfter: &tenMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "newer 10m force"},
		})
		close(loadRelease)
		if errRestore := <-restoreDone; errRestore != nil {
			t.Fatalf("RestoreCooldownStates: %v", errRestore)
		}

		got, _ := mgr.GetByID("delayed-auth-0")
		if IsAuthAvailableForModel(got, "", baseNow.Add(5*time.Minute)) {
			t.Fatalf("delayed RestoreCooldownStates shortened newer 10m auth-level force cooldown to %v", got.NextRetryAfter)
		}
	})

	t.Run("dynamic-pool-prunes-churned-members-and-shrinks-backing-array-despite-inflight-reservation", func(t *testing.T) {
		sel := &WeightedRoundRobinSelector{}
		const route = "openai-compatible-churn:model-c"
		makeBatch := func(batch int) []*Auth {
			out := make([]*Auth, 1030)
			for i := range out {
				out[i] = &Auth{
					ID:         fmt.Sprintf("churn-b%02d-%04d", batch, i),
					Provider:   "openai-compatible-churn",
					Status:     StatusActive,
					Attributes: map[string]string{"weight": "1"},
				}
			}
			return out
		}

		_, pinnedInFlight := sel.reserve(route, makeBatch(0), "")
		if pinnedInFlight == nil {
			t.Fatal("expected in-flight reservation on batch 0")
		}
		for b := 1; b <= 12; b++ {
			_, res := sel.reserve(route, makeBatch(b), "")
			res.finish(true)
		}

		sel.mu.Lock()
		state := sel.largeStates[route]
		gotLen := len(state.members)
		gotCap := cap(state.members)
		sel.mu.Unlock()
		if gotLen > 3200 || gotCap > 4096 {
			t.Fatalf("dynamic pool members grew unbounded while 1 reservation remained in-flight: len=%d cap=%d", gotLen, gotCap)
		}

		pinnedInFlight.finish(false)
		sel.mu.Lock()
		for _, m := range state.members {
			if m.pending != 0 {
				sel.mu.Unlock()
				t.Fatalf("member %s has non-zero pending=%d after pinned reservation rollback", m.id, m.pending)
			}
		}
		sel.mu.Unlock()
	})

	t.Run("ordinary-modelless-429-does-not-wipe-credential-scoped-force-cooldown", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-modelless-429"
		disableCoolingTrue := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		cdsPath := t.TempDir() + "/modelless-429.cds"
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(NewFileCooldownStateStore(cdsPath))
		mgr.SetConfig(cfg)
		if _, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
			ID: "ml429-auth", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		baseNow := time.Now()
		fiveMin := 5 * time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID: "ml429-auth", Provider: providerKey, Model: "m1", CredentialScope: true, RetryAfter: &fiveMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "force cred 429"},
		})
		// Ordinary model-less 429 under disable-cooling must not clobber credential_quota force state.
		mgr.MarkResult(ctx, Result{
			AuthID: "ml429-auth", Provider: providerKey,
			Error: &Error{Code: "rate_limited", HTTPStatus: http.StatusTooManyRequests, Message: "ordinary global 429"},
		})

		gotAfterMark, _ := mgr.GetByID("ml429-auth")
		if IsAuthAvailableForModel(gotAfterMark, "m1", baseNow.Add(2*time.Minute)) || IsAuthAvailableForModel(gotAfterMark, "", baseNow.Add(2*time.Minute)) {
			t.Fatalf("ordinary model-less 429 cleared active credential-scoped force cooldown: auth=%+v", gotAfterMark)
		}

		mgr.SetConfig(cfg)
		gotAfterReload, _ := mgr.GetByID("ml429-auth")
		if IsAuthAvailableForModel(gotAfterReload, "m1", baseNow.Add(2*time.Minute)) || IsAuthAvailableForModel(gotAfterReload, "", baseNow.Add(2*time.Minute)) {
			t.Fatalf("SetConfig after ordinary model-less 429 cleared credential-scoped force cooldown: auth=%+v", gotAfterReload)
		}

		mgr.PersistCooldownStates(ctx)
		mgr2 := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr2.SetCooldownStateStore(NewFileCooldownStateStore(cdsPath))
		mgr2.SetConfig(cfg)
		if _, errReg := mgr2.Register(WithSkipPersist(ctx), &Auth{
			ID: "ml429-auth", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}); errReg != nil {
			t.Fatalf("Register mgr2: %v", errReg)
		}
		if errRestore := mgr2.RestoreCooldownStates(ctx); errRestore != nil {
			t.Fatalf("RestoreCooldownStates: %v", errRestore)
		}
		gotRestored, _ := mgr2.GetByID("ml429-auth")
		if IsAuthAvailableForModel(gotRestored, "m1", baseNow.Add(2*time.Minute)) || IsAuthAvailableForModel(gotRestored, "", baseNow.Add(2*time.Minute)) {
			t.Fatalf("restored auth lost credential-scoped force cooldown after ordinary model-less 429: auth=%+v", gotRestored)
		}
	})

	t.Run("coexisting-credential-and-auth-force-cooldowns-preserve-longer-deadline-across-reload-and-disk", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-dual-force"
		disableCoolingTrue := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		cdsPath := t.TempDir() + "/dual-force.cds"
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(NewFileCooldownStateStore(cdsPath))
		mgr.SetConfig(cfg)
		for _, id := range []string{"dual-short-cred-long-auth", "dual-long-cred-short-auth"} {
			if _, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
				ID: id, Provider: providerKey, Status: StatusActive,
				Attributes: map[string]string{"provider_key": providerKey},
			}); errReg != nil {
				t.Fatalf("Register %s: %v", id, errReg)
			}
		}

		baseNow := time.Now()
		twoMin := 2 * time.Minute
		tenMin := 10 * time.Minute

		// Order A: 2m credential-scoped force 429, then 10m model-less force 503.
		mgr.MarkResult(ctx, Result{
			AuthID: "dual-short-cred-long-auth", Provider: providerKey, Model: "m1", CredentialScope: true, RetryAfter: &twoMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "2m cred force"},
		})
		mgr.MarkResult(ctx, Result{
			AuthID: "dual-short-cred-long-auth", Provider: providerKey, RetryAfter: &tenMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "10m auth force"},
		})

		// Order B: 10m credential-scoped force 429, then 2m model-less force 429.
		mgr.MarkResult(ctx, Result{
			AuthID: "dual-long-cred-short-auth", Provider: providerKey, Model: "m1", CredentialScope: true, RetryAfter: &tenMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "10m cred force"},
		})
		mgr.MarkResult(ctx, Result{
			AuthID: "dual-long-cred-short-auth", Provider: providerKey, RetryAfter: &twoMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "2m auth force"},
		})

		mgr.SetConfig(cfg)
		for _, id := range []string{"dual-short-cred-long-auth", "dual-long-cred-short-auth"} {
			got, _ := mgr.GetByID(id)
			if IsAuthAvailableForModel(got, "", baseNow.Add(5*time.Minute)) {
				t.Fatalf("%s truncated longer 10m force deadline on SetConfig: NextRetryAfter=%v", id, got.NextRetryAfter)
			}
		}
		if gotLongCred, _ := mgr.GetByID("dual-long-cred-short-auth"); IsAuthAvailableForModel(gotLongCred, "m1", baseNow.Add(5*time.Minute)) {
			t.Fatalf("dual-long-cred-short-auth lost 10m model wrap on SetConfig: %+v", gotLongCred)
		}

		mgr.PersistCooldownStates(ctx)
		mgr2 := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr2.SetCooldownStateStore(NewFileCooldownStateStore(cdsPath))
		mgr2.SetConfig(cfg)
		for _, id := range []string{"dual-short-cred-long-auth", "dual-long-cred-short-auth"} {
			if _, errReg := mgr2.Register(WithSkipPersist(ctx), &Auth{
				ID: id, Provider: providerKey, Status: StatusActive,
				Attributes: map[string]string{"provider_key": providerKey},
			}); errReg != nil {
				t.Fatalf("Register mgr2 %s: %v", id, errReg)
			}
		}
		if errRestore := mgr2.RestoreCooldownStates(ctx); errRestore != nil {
			t.Fatalf("RestoreCooldownStates: %v", errRestore)
		}
		mgr2.SetConfig(cfg)
		for _, id := range []string{"dual-short-cred-long-auth", "dual-long-cred-short-auth"} {
			got, _ := mgr2.GetByID(id)
			if IsAuthAvailableForModel(got, "", baseNow.Add(5*time.Minute)) {
				t.Fatalf("%s lost longer 10m force deadline across disk persistence + SetConfig: NextRetryAfter=%v", id, got.NextRetryAfter)
			}
		}
		if gotLongCred2, _ := mgr2.GetByID("dual-long-cred-short-auth"); IsAuthAvailableForModel(gotLongCred2, "m1", baseNow.Add(5*time.Minute)) {
			t.Fatalf("dual-long-cred-short-auth lost 10m model wrap across disk persistence + SetConfig: %+v", gotLongCred2)
		}
	})

	t.Run("concurrent-restore-during-update-save-preserves-force-provenance-across-reload", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-update-save"
		disableCoolingTrue := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		saveEntered := make(chan struct{})
		saveRelease := make(chan struct{})
		var saveOnce sync.Once
		authStore := &blockingSelectiveAuthStore{
			onSave: func(a *Auth) {
				if a != nil && a.ID == "save-race-auth" {
					saveOnce.Do(func() {
						close(saveEntered)
						<-saveRelease
					})
				}
			},
		}
		baseNow := time.Now()
		tenMinUntil := baseNow.Add(10 * time.Minute)
		cdsStore := &recordingCooldownStateStore{
			load: []CooldownStateRecord{{
				Provider:               providerKey,
				AuthID:                 "save-race-auth",
				Status:                 "cooling",
				NextRetryAfter:         tenMinUntil,
				Reason:                 ErrorCodeForceCooldown,
				LastError:              &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "restored 10m force"},
				UpdatedAt:              baseNow,
				ForceCooldownUntil:     tenMinUntil,
				ForceCooldownEvaluated: true,
			}},
		}

		mgr := NewManager(authStore, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(cdsStore)
		mgr.SetConfig(cfg)
		initial, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
			ID: "save-race-auth", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
			Metadata:   map[string]any{"rev": "1"},
		})
		if errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		upd := initial.Clone()
		upd.Metadata["rev"] = "2"
		updateDone := make(chan error, 1)
		go func() {
			_, errUpd := mgr.Update(ctx, upd)
			updateDone <- errUpd
		}()
		<-saveEntered

		if errRestore := mgr.RestoreCooldownStates(ctx); errRestore != nil {
			t.Fatalf("RestoreCooldownStates during Update save: %v", errRestore)
		}
		close(saveRelease)
		if errUpd := <-updateDone; errUpd != nil {
			t.Fatalf("Update: %v", errUpd)
		}

		// Reload config: restored 10m force provenance must have survived mergeAuthSaveDelta.
		mgr.SetConfig(cfg)
		got, _ := mgr.GetByID("save-race-auth")
		if IsAuthAvailableForModel(got, "", baseNow.Add(5*time.Minute)) {
			t.Fatalf("RestoreCooldownStates force provenance lost during concurrent Update Store.Save + SetConfig: %+v", got)
		}
	})

	t.Run("modelless-force-cooldown-survives-ordinary-model-error-reload-and-restore", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-agg-force"
		disableCoolingTrue := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		cdsPath := t.TempDir() + "/agg-force.cds"
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(NewFileCooldownStateStore(cdsPath))
		mgr.SetConfig(cfg)
		if _, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
			ID: "agg-force-auth", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		baseNow := time.Now()
		tenMin := 10 * time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID: "agg-force-auth", Provider: providerKey, RetryAfter: &tenMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "10m auth force"},
		})
		gotBeforeModelErr, _ := mgr.GetByID("agg-force-auth")
		wantDeadline := gotBeforeModelErr.NextRetryAfter

		// Subsequent ordinary model-level 500 under disable-cooling must not clear auth-level force cooldown.
		mgr.MarkResult(ctx, Result{
			AuthID: "agg-force-auth", Provider: providerKey, Model: "m1",
			Error: &Error{Code: "upstream_500", HTTPStatus: http.StatusInternalServerError, Message: "ordinary 500"},
		})
		gotAfterModelErr, _ := mgr.GetByID("agg-force-auth")
		if IsAuthAvailableForModel(gotAfterModelErr, "", baseNow.Add(5*time.Minute)) ||
			IsAuthAvailableForModel(gotAfterModelErr, "m1", baseNow.Add(5*time.Minute)) ||
			!gotAfterModelErr.NextRetryAfter.Equal(wantDeadline) {
			t.Fatalf("ordinary model 500 wiped/altered 10m auth-level force cooldown: got=%v want=%v unavailable=%v", gotAfterModelErr.NextRetryAfter, wantDeadline, gotAfterModelErr.Unavailable)
		}

		mgr.SetConfig(cfg)
		gotAfterReload, _ := mgr.GetByID("agg-force-auth")
		if IsAuthAvailableForModel(gotAfterReload, "", baseNow.Add(5*time.Minute)) ||
			IsAuthAvailableForModel(gotAfterReload, "m1", baseNow.Add(5*time.Minute)) ||
			!gotAfterReload.NextRetryAfter.Equal(wantDeadline) {
			t.Fatalf("SetConfig after ordinary model 500 lost 10m auth-level force cooldown: got=%v want=%v", gotAfterReload.NextRetryAfter, wantDeadline)
		}

		mgr.PersistCooldownStates(ctx)
		mgr2 := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr2.SetCooldownStateStore(NewFileCooldownStateStore(cdsPath))
		mgr2.SetConfig(cfg)
		if _, errReg := mgr2.Register(WithSkipPersist(ctx), &Auth{
			ID: "agg-force-auth", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}); errReg != nil {
			t.Fatalf("Register mgr2: %v", errReg)
		}
		if errRestore := mgr2.RestoreCooldownStates(ctx); errRestore != nil {
			t.Fatalf("RestoreCooldownStates: %v", errRestore)
		}
		gotRestored, _ := mgr2.GetByID("agg-force-auth")
		if IsAuthAvailableForModel(gotRestored, "", baseNow.Add(5*time.Minute)) ||
			IsAuthAvailableForModel(gotRestored, "m1", baseNow.Add(5*time.Minute)) ||
			!gotRestored.NextRetryAfter.Equal(wantDeadline) {
			t.Fatalf("restored auth lost 10m auth-level force cooldown: got=%v want=%v", gotRestored.NextRetryAfter, wantDeadline)
		}
	})

	t.Run("forget-auth-shrinks-large-dynamic-pool-backing-array-with-inflight-reservation", func(t *testing.T) {
		sel := &WeightedRoundRobinSelector{}
		const route = "openai-compatible-shrink:model-s"
		hugePool := make([]*Auth, 5000)
		for i := range hugePool {
			hugePool[i] = &Auth{
				ID:         fmt.Sprintf("shrink-auth-%04d", i),
				Provider:   "openai-compatible-shrink",
				Status:     StatusActive,
				Attributes: map[string]string{"weight": "1"},
			}
		}

		// Hold an in-flight reservation on the retained 1030 prefix while seeding 5000 members.
		_, seedRes := sel.reserve(route, hugePool, "")
		seedRes.finish(true)
		_, inFlightRes := sel.reserve(route, hugePool[:1030], "")
		if inFlightRes == nil {
			t.Fatal("expected in-flight reservation on retained 1030 subset")
		}

		// Permanently remove 3970 credentials so 1030 (> 1024) remain.
		for i := 1030; i < len(hugePool); i++ {
			sel.ForgetAuth(hugePool[i].ID)
		}

		sel.mu.Lock()
		state := sel.largeStates[route]
		gotLen := len(state.members)
		gotCap := cap(state.members)
		sel.mu.Unlock()
		if gotLen != 1030 || gotCap > 2060 {
			t.Fatalf("ForgetAuth left oversized backing array after shrinking 5000->1030: len=%d cap=%d (want len=1030, cap<=2060)", gotLen, gotCap)
		}

		inFlightRes.finish(false)
		sel.mu.Lock()
		for _, m := range state.members {
			if m.pending != 0 || m.inFlight != 0 {
				sel.mu.Unlock()
				t.Fatalf("member %s has leaked pending=%d inFlight=%d after rollback", m.id, m.pending, m.inFlight)
			}
		}
		sel.mu.Unlock()
	})

	t.Run("ordinary-credential-quota-plus-longer-auth-force-preserves-auth-force-across-disk-restore", func(t *testing.T) {
		cdsPath := t.TempDir() + "/ord-cred-long-force.cds"
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(NewFileCooldownStateStore(cdsPath))
		if _, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
			ID: "ord-cred-force-auth", Provider: "xai", Status: StatusActive,
		}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		baseNow := time.Now()
		twoMin := 2 * time.Minute
		tenMin := 10 * time.Minute
		// 1. Ordinary 2m credential-scoped 429 on m1 (CredQuotaForceCooldownUntil == zero).
		mgr.MarkResult(ctx, Result{
			AuthID: "ord-cred-force-auth", Provider: "xai", Model: "m1", CredentialScope: true, RetryAfter: &twoMin,
			Error: &Error{Code: "rate_limited", HTTPStatus: http.StatusTooManyRequests, Message: "ordinary 2m cred quota"},
		})
		// 2. Independent 10m model-less force 503 (ForceCooldownUntil == +10m).
		mgr.MarkResult(ctx, Result{
			AuthID: "ord-cred-force-auth", Provider: "xai", RetryAfter: &tenMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "10m auth force"},
		})
		mgr.PersistCooldownStates(ctx)

		mgr2 := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr2.SetCooldownStateStore(NewFileCooldownStateStore(cdsPath))
		if _, errReg := mgr2.Register(WithSkipPersist(ctx), &Auth{
			ID: "ord-cred-force-auth", Provider: "xai", Status: StatusActive,
		}); errReg != nil {
			t.Fatalf("Register mgr2: %v", errReg)
		}
		if errRestore := mgr2.RestoreCooldownStates(ctx); errRestore != nil {
			t.Fatalf("RestoreCooldownStates: %v", errRestore)
		}

		// At +3m: 2m ordinary credential_quota has expired, but 10m independent auth force must still block m1 and "".
		got, _ := mgr2.GetByID("ord-cred-force-auth")
		if IsAuthAvailableForModel(got, "m1", baseNow.Add(3*time.Minute)) || IsAuthAvailableForModel(got, "", baseNow.Add(3*time.Minute)) {
			t.Fatalf("restored auth lost 10m independent auth force after 2m ordinary credential_quota expired: %+v", got)
		}
	})

	t.Run("devin-quota-restoration-preserves-independent-modelless-force-cooldown", func(t *testing.T) {
		now := time.Now()
		existing := &Auth{
			ID:             "devin-dual-auth",
			Provider:       "devin",
			Status:         StatusError,
			Unavailable:    true,
			UpdatedAt:      now,
			NextRetryAfter: now.Add(10 * time.Minute),
			LastError:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "10m force 503"},
			StatusMessage:  "transient upstream error",
			Quota: QuotaState{
				Exceeded:      true,
				Reason:        "credential_quota",
				NextRecoverAt: now.Add(2 * time.Minute),
				ObservedAt:    now.Add(-time.Minute),
			},
			forceCooldownUntil:  now.Add(10 * time.Minute),
			credQuotaForceUntil: now.Add(2 * time.Minute),
			forceCooldownSeeded: true,
			ModelStates: map[string]*ModelState{
				"devin-m1": {
					Status:                    StatusError,
					Unavailable:               true,
					NextRetryAfter:            now.Add(2 * time.Minute),
					Quota:                     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(2 * time.Minute)},
					credentialQuotaPropagated: true,
				},
			},
		}
		refreshed := existing.Clone()
		refreshed.Quota.ObservedAt = now.Add(time.Second)
		refreshed.Quota.Signals = map[string]string{
			"daily_quota_remaining_percent":  "80",
			"weekly_quota_remaining_percent": "90",
		}

		evalNow := now.Add(2 * time.Second)
		if !applyDevinQuotaRestoration(refreshed, existing, evalNow) {
			t.Fatal("expected applyDevinQuotaRestoration to return true")
		}
		if IsAuthAvailableForModel(refreshed, "devin-m1", now.Add(5*time.Minute)) ||
			IsAuthAvailableForModel(refreshed, "", now.Add(5*time.Minute)) {
			t.Fatalf("applyDevinQuotaRestoration wiped unrelated 10m independent auth force cooldown: %+v", refreshed)
		}
	})

	t.Run("long-ordinary-credential-cooldown-plus-short-auth-force-clears-ordinary-tail-on-disable-cooling-reload", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-long-ord-short-force"
		disableCoolingFalse := false
		disableCoolingTrue := true
		cfgCoolOn := &internalconfig.Config{
			DisableCooling: false,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingFalse,
			}},
		}
		cfgCoolOff := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}

		cdsPath := t.TempDir() + "/long-ord-short-force.cds"
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(NewFileCooldownStateStore(cdsPath))
		mgr.SetConfig(cfgCoolOn)
		if _, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
			ID: "reverse-dual-auth", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		baseNow := time.Now()
		twentyMin := 20 * time.Minute
		twoMin := 2 * time.Minute

		// 1. Ordinary 20m credential-scoped 429 while cooling is enabled.
		mgr.MarkResult(ctx, Result{
			AuthID: "reverse-dual-auth", Provider: providerKey, Model: "m1", CredentialScope: true, RetryAfter: &twentyMin,
			Error: &Error{Code: "rate_limited", HTTPStatus: http.StatusTooManyRequests, Message: "ordinary 20m cred 429"},
		})
		// 2. Independent 2m model-less force 503.
		mgr.MarkResult(ctx, Result{
			AuthID: "reverse-dual-auth", Provider: providerKey, RetryAfter: &twoMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "2m auth force 503"},
		})
		mgr.PersistCooldownStates(ctx)

		// 3. Reload with disable-cooling: true: 2m force must stay active at +1m, 20m ordinary tail must clear by +3m.
		mgr.SetConfig(cfgCoolOff)
		gotReload, _ := mgr.GetByID("reverse-dual-auth")
		if IsAuthAvailableForModel(gotReload, "m1", baseNow.Add(time.Minute)) || IsAuthAvailableForModel(gotReload, "", baseNow.Add(time.Minute)) {
			t.Fatalf("2m auth force cooldown prematurely cleared at +1m: %+v", gotReload)
		}
		if !IsAuthAvailableForModel(gotReload, "m1", baseNow.Add(3*time.Minute)) || !IsAuthAvailableForModel(gotReload, "", baseNow.Add(3*time.Minute)) {
			t.Fatalf("20m ordinary credential cooldown survived disable-cooling reload past 2m force window (+3m): NextRetryAfter=%v Quota=%+v", gotReload.NextRetryAfter, gotReload.Quota)
		}

		// 4. Also verify restoring pre-reload .cds directly into a disable-cooling: true manager clamps to 2m.
		mgr2 := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr2.SetCooldownStateStore(NewFileCooldownStateStore(cdsPath))
		mgr2.SetConfig(cfgCoolOff)
		if _, errReg := mgr2.Register(WithSkipPersist(ctx), &Auth{
			ID: "reverse-dual-auth", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}); errReg != nil {
			t.Fatalf("Register mgr2: %v", errReg)
		}
		if errRestore := mgr2.RestoreCooldownStates(ctx); errRestore != nil {
			t.Fatalf("RestoreCooldownStates: %v", errRestore)
		}
		gotRestored, _ := mgr2.GetByID("reverse-dual-auth")
		if IsAuthAvailableForModel(gotRestored, "m1", baseNow.Add(time.Minute)) || IsAuthAvailableForModel(gotRestored, "", baseNow.Add(time.Minute)) {
			t.Fatalf("restored 2m auth force cooldown prematurely cleared at +1m: %+v", gotRestored)
		}
		if !IsAuthAvailableForModel(gotRestored, "m1", baseNow.Add(3*time.Minute)) || !IsAuthAvailableForModel(gotRestored, "", baseNow.Add(3*time.Minute)) {
			t.Fatalf("restored 20m ordinary credential cooldown survived disable-cooling restore past 2m force window (+3m): NextRetryAfter=%v Quota=%+v", gotRestored.NextRetryAfter, gotRestored.Quota)
		}
	})

	t.Run("delayed-store-restore-preserves-longer-persisted-independent-shadow-force-deadline", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-delayed-shadow"
		disableCoolingTrue := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		baseNow := time.Now()
		twoMinUntil := baseNow.Add(2 * time.Minute)
		tenMinUntil := baseNow.Add(10 * time.Minute)

		loadEntered := make(chan struct{})
		loadRelease := make(chan struct{})
		slowStore := &issue7431BlockingLoadCooldownStore{
			records: []CooldownStateRecord{
				{
					Provider:                    providerKey,
					AuthID:                      "delayed-shadow-auth",
					Status:                      "cooling",
					NextRetryAfter:              twoMinUntil,
					Reason:                      "credential_quota",
					Quota:                       QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: twoMinUntil},
					LastError:                   &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 429, Message: "2m cred force"},
					UpdatedAt:                   baseNow.Add(-time.Minute),
					CredQuotaForceCooldownUntil: twoMinUntil,
					ForceCooldownEvaluated:      true,
				},
				{
					Provider:                  providerKey,
					AuthID:                    "delayed-shadow-auth",
					Model:                     "m1",
					Status:                    "cooling",
					NextRetryAfter:            tenMinUntil,
					Reason:                    "credential_quota",
					Quota:                     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: twoMinUntil},
					LastError:                 &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 429, Message: "2m cred force"},
					UpdatedAt:                 baseNow.Add(-time.Minute),
					CredentialQuotaPropagated: true,
					IndependentState: &ModelState{
						Status:         StatusError,
						Unavailable:    true,
						NextRetryAfter: tenMinUntil,
						LastError:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "older 10m model force"},
						UpdatedAt:      baseNow.Add(-2 * time.Minute),
					},
					ForceCooldownUntil:            twoMinUntil,
					IndependentForceCooldownUntil: tenMinUntil,
					ForceCooldownEvaluated:        true,
				},
			},
			entered: loadEntered,
			release: loadRelease,
		}

		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(slowStore)
		mgr.SetConfig(cfg)
		if _, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
			ID: "delayed-shadow-auth", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		restoreDone := make(chan error, 1)
		go func() {
			restoreDone <- mgr.RestoreCooldownStates(ctx)
		}()
		<-loadEntered

		// Newer 3m model force arrives while store.Load is blocked.
		threeMin := 3 * time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID: "delayed-shadow-auth", Provider: providerKey, Model: "m1", RetryAfter: &threeMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "newer 3m model force"},
		})
		close(loadRelease)
		if errRestore := <-restoreDone; errRestore != nil {
			t.Fatalf("RestoreCooldownStates: %v", errRestore)
		}

		mgr.SetConfig(cfg)
		got, _ := mgr.GetByID("delayed-shadow-auth")
		if IsAuthAvailableForModel(got, "m1", baseNow.Add(5*time.Minute)) {
			t.Fatalf("delayed RestoreCooldownStates truncated 10m persisted independent shadow force deadline to %v", got.ModelStates["m1"].NextRetryAfter)
		}

		recStore := &recordingCooldownStateStore{}
		mgr.SetCooldownStateStore(recStore)
		mgr.PersistCooldownStates(ctx)
		foundTenMin := false
		for _, rec := range recStore.savedRecords() {
			if rec.Model == "m1" && rec.NextRetryAfter.Equal(tenMinUntil) && rec.IndependentForceCooldownUntil.Equal(tenMinUntil) {
				foundTenMin = true
			}
		}
		if !foundTenMin {
			t.Fatalf("re-persisted records lost 10m independent force deadline: %+v", recStore.savedRecords())
		}
	})

	t.Run("delayed-restore-with-stale-model-and-auth-records-preserves-newer-credential-force-cooldown", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-stale-model-auth"
		disableCoolingTrue := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		baseNow := time.Now()
		twoMinUntil := baseNow.Add(2 * time.Minute)
		threeMinUntil := baseNow.Add(3 * time.Minute)

		loadEntered := make(chan struct{})
		loadRelease := make(chan struct{})
		slowStore := &issue7431BlockingLoadCooldownStore{
			records: []CooldownStateRecord{
				{
					Provider:               providerKey,
					AuthID:                 "stale-combo-auth",
					Model:                  "m-stale",
					Status:                 "cooling",
					NextRetryAfter:         twoMinUntil,
					Reason:                 ErrorCodeForceCooldown,
					LastError:              &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "older 2m model force"},
					UpdatedAt:              baseNow.Add(-2 * time.Minute),
					ForceCooldownUntil:     twoMinUntil,
					ForceCooldownEvaluated: true,
				},
				{
					Provider:               providerKey,
					AuthID:                 "stale-combo-auth",
					Status:                 "cooling",
					NextRetryAfter:         threeMinUntil,
					Reason:                 "quota",
					Quota:                  QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: threeMinUntil},
					LastError:              &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 429, Message: "older 3m auth force 429"},
					UpdatedAt:              baseNow.Add(-time.Minute),
					ForceCooldownUntil:     threeMinUntil,
					ForceCooldownEvaluated: true,
				},
			},
			entered: loadEntered,
			release: loadRelease,
		}

		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(slowStore)
		mgr.SetConfig(cfg)
		if _, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
			ID: "stale-combo-auth", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		restoreDone := make(chan error, 1)
		go func() {
			restoreDone <- mgr.RestoreCooldownStates(ctx)
		}()
		<-loadEntered

		// Newer 10m credential-scoped force 429 arrives while store.Load is blocked.
		tenMin := 10 * time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID: "stale-combo-auth", Provider: providerKey, Model: "m-live", CredentialScope: true, RetryAfter: &tenMin,
			Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "newer 10m cred force 429"},
		})
		close(loadRelease)
		if errRestore := <-restoreDone; errRestore != nil {
			t.Fatalf("RestoreCooldownStates: %v", errRestore)
		}

		mgr.SetConfig(cfg)
		got, _ := mgr.GetByID("stale-combo-auth")
		if IsAuthAvailableForModel(got, "m-live", baseNow.Add(5*time.Minute)) ||
			IsAuthAvailableForModel(got, "m-other", baseNow.Add(5*time.Minute)) ||
			IsAuthAvailableForModel(got, "", baseNow.Add(5*time.Minute)) {
			t.Fatalf("delayed RestoreCooldownStates shortened newer 10m credential-scoped force cooldown: NextRetryAfter=%v Quota=%+v", got.NextRetryAfter, got.Quota)
		}
	})

	t.Run("cooldown-state-record-equal-detects-cred-quota-force-cooldown-until-change", func(t *testing.T) {
		now := time.Now().Round(0)
		recA := CooldownStateRecord{
			Provider:                    "xai",
			AuthID:                      "eq-auth",
			Status:                      "cooling",
			NextRetryAfter:              now.Add(10 * time.Minute),
			Reason:                      "credential_quota",
			Quota:                       QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(10 * time.Minute)},
			UpdatedAt:                   now,
			ForceCooldownEvaluated:      true,
			CredQuotaForceCooldownUntil: time.Time{},
		}
		recB := recA
		recB.CredQuotaForceCooldownUntil = now.Add(10 * time.Minute)
		if cooldownStateRecordEqual(recA, recB) {
			t.Fatal("cooldownStateRecordEqual ignored CredQuotaForceCooldownUntil difference")
		}
	})

	t.Run("legacy-dual-model-and-aggregated-auth-force-records-do-not-block-healthy-sibling-model", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-legacy-agg"
		disableCoolingTrue := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		baseNow := time.Now()
		tenMinUntil := baseNow.Add(10 * time.Minute)
		forceErr := &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "model-a force 503"}

		// Legacy store snapshot (ForceCooldownEvaluated == false):
		// 1) legacy-dual-auth: model-a failed with force 503 AND old code persisted an aggregated Model=="" record.
		// 2) legacy-global-auth: genuine model-less force 503 (only Model=="" record).
		legacyStore := &recordingCooldownStateStore{
			load: []CooldownStateRecord{
				{
					Provider:       providerKey,
					AuthID:         "legacy-dual-auth",
					Model:          "model-a",
					Status:         "cooling",
					NextRetryAfter: tenMinUntil,
					Reason:         ErrorCodeForceCooldown,
					LastError:      cloneError(forceErr),
					UpdatedAt:      baseNow,
				},
				{
					Provider:       providerKey,
					AuthID:         "legacy-dual-auth",
					Status:         "cooling",
					NextRetryAfter: tenMinUntil,
					Reason:         ErrorCodeForceCooldown,
					LastError:      cloneError(forceErr),
					UpdatedAt:      baseNow,
				},
				{
					Provider:       providerKey,
					AuthID:         "legacy-global-auth",
					Status:         "cooling",
					NextRetryAfter: tenMinUntil,
					Reason:         ErrorCodeForceCooldown,
					LastError:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "global force 503"},
					UpdatedAt:      baseNow,
				},
			},
		}

		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(legacyStore)
		mgr.SetConfig(cfg)
		for _, id := range []string{"legacy-dual-auth", "legacy-global-auth"} {
			if _, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
				ID: id, Provider: providerKey, Status: StatusActive,
				Attributes: map[string]string{"provider_key": providerKey},
			}); errReg != nil {
				t.Fatalf("Register %s: %v", id, errReg)
			}
		}
		if errRestore := mgr.RestoreCooldownStates(ctx); errRestore != nil {
			t.Fatalf("RestoreCooldownStates: %v", errRestore)
		}

		gotDual, _ := mgr.GetByID("legacy-dual-auth")
		if IsAuthAvailableForModel(gotDual, "model-a", baseNow.Add(2*time.Minute)) {
			t.Fatal("expected legacy model-a force cooldown to stay blocked")
		}
		if !IsAuthAvailableForModel(gotDual, "model-b", baseNow.Add(2*time.Minute)) {
			t.Fatalf("legacy aggregated auth record falsely promoted model-a force cooldown to block sibling model-b: %+v", gotDual)
		}
		// Subsequent success on model-a must clear the credential completely.
		mgr.MarkResult(ctx, Result{AuthID: "legacy-dual-auth", Provider: providerKey, Model: "model-a", Success: true})
		gotDualAfterSuccess, _ := mgr.GetByID("legacy-dual-auth")
		if !IsAuthAvailableForModel(gotDualAfterSuccess, "model-a", baseNow.Add(2*time.Minute)) ||
			!IsAuthAvailableForModel(gotDualAfterSuccess, "", baseNow.Add(2*time.Minute)) {
			t.Fatalf("model-a success failed to clear legacy-dual-auth: %+v", gotDualAfterSuccess)
		}

		// Genuine legacy model-less force record must still block all models.
		gotGlobal, _ := mgr.GetByID("legacy-global-auth")
		if IsAuthAvailableForModel(gotGlobal, "model-b", baseNow.Add(2*time.Minute)) ||
			IsAuthAvailableForModel(gotGlobal, "", baseNow.Add(2*time.Minute)) {
			t.Fatalf("expected genuine legacy model-less force cooldown to remain blocked: %+v", gotGlobal)
		}
	})

	t.Run("legacy-longer-model-force-plus-shorter-independent-auth-force-preserves-auth-block-for-sibling-models", func(t *testing.T) {
		const providerKey = "openai-compatible-issue-7431-legacy-coexist"
		disableCoolingTrue := true
		cfg := &internalconfig.Config{
			DisableCooling: true,
			OpenAICompatibility: []internalconfig.OpenAICompatibility{{
				Name:           providerKey,
				BaseURL:        "https://example.invalid/v1",
				DisableCooling: &disableCoolingTrue,
			}},
		}
		baseNow := time.Now()
		thirtyMinUntil := baseNow.Add(30 * time.Minute)
		tenMinUntil := baseNow.Add(10 * time.Minute)

		legacyStore := &recordingCooldownStateStore{
			load: []CooldownStateRecord{
				{
					Provider:       providerKey,
					AuthID:         "legacy-coexist-auth",
					Model:          "model-a",
					Status:         "cooling",
					NextRetryAfter: thirtyMinUntil,
					Reason:         ErrorCodeForceCooldown,
					LastError:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "30m model-a force"},
					UpdatedAt:      baseNow.Add(-2 * time.Minute),
				},
				{
					Provider:       providerKey,
					AuthID:         "legacy-coexist-auth",
					Status:         "cooling",
					NextRetryAfter: tenMinUntil,
					Reason:         ErrorCodeForceCooldown,
					LastError:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 503, Message: "10m auth force"},
					UpdatedAt:      baseNow.Add(-time.Minute),
				},
			},
		}

		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.SetCooldownStateStore(legacyStore)
		mgr.SetConfig(cfg)
		if _, errReg := mgr.Register(WithSkipPersist(ctx), &Auth{
			ID: "legacy-coexist-auth", Provider: providerKey, Status: StatusActive,
			Attributes: map[string]string{"provider_key": providerKey},
		}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}
		if errRestore := mgr.RestoreCooldownStates(ctx); errRestore != nil {
			t.Fatalf("RestoreCooldownStates: %v", errRestore)
		}
		mgr.SetConfig(cfg)

		got, _ := mgr.GetByID("legacy-coexist-auth")
		if IsAuthAvailableForModel(got, "model-b", baseNow.Add(5*time.Minute)) {
			t.Fatalf("expected 10m legacy independent auth force to block healthy model-b at +5m: %+v", got)
		}
		if !IsAuthAvailableForModel(got, "model-b", baseNow.Add(15*time.Minute)) {
			t.Fatalf("expected model-b to recover once 10m auth force expired at +15m: %+v", got)
		}
		if IsAuthAvailableForModel(got, "model-a", baseNow.Add(15*time.Minute)) {
			t.Fatalf("expected model-a 30m force cooldown to stay blocked at +15m: %+v", got.ModelStates["model-a"])
		}
	})
}

type issue7431BlockingLoadCooldownStore struct {
	records []CooldownStateRecord
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *issue7431BlockingLoadCooldownStore) Load(context.Context) ([]CooldownStateRecord, error) {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return cloneCooldownStateRecords(s.records), nil
}

func (s *issue7431BlockingLoadCooldownStore) Save(context.Context, []CooldownStateRecord) error {
	return nil
}
