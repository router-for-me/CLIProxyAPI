package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManagerAliasQuotaFailoverWithUnobservedTargetModel(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for name, newSelector := range map[string]func() Selector{
		"round-robin":          func() Selector { return &RoundRobinSelector{} },
		"weighted-round-robin": func() Selector { return &WeightedRoundRobinSelector{} },
		"fill-first":           func() Selector { return &FillFirstSelector{} },
	} {
		for _, path := range []string{"select", "execute", "stream"} {
			t.Run(name+"/"+path, func(t *testing.T) {
				const routeModel, targetModel, otherModel = "quota-route", "quota-target", "quota-other"
				manager := NewManager(nil, newSelector(), nil)
				manager.SetRetryConfig(3, 30*time.Second, 0)
				manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
					"codex": {{Name: targetModel, Alias: routeModel, Fork: true}},
				})
				highID, lowID := "alias-quota-high-"+t.Name(), "alias-quota-low-"+t.Name()
				for _, candidate := range []*Auth{
					{ID: highID, Provider: "codex", Status: StatusActive, Attributes: map[string]string{"priority": "4", AttributeWeight: "1"}},
					{ID: lowID, Provider: "codex", Status: StatusActive, Attributes: map[string]string{"priority": "3", AttributeWeight: "1"}},
				} {
					registry.GetGlobalRegistry().RegisterClient(candidate.ID, "codex", []*registry.ModelInfo{{ID: routeModel}, {ID: targetModel}, {ID: otherModel}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
					if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				retryAfter := time.Hour
				manager.MarkResult(context.Background(), Result{
					AuthID: highID, Provider: "codex", Model: otherModel,
					Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "other model rate limit"}, RetryAfter: &retryAfter,
				})
				high, _ := manager.GetByID(highID)
				if !high.Unavailable || high.ModelStates[targetModel] != nil {
					t.Fatal("expected aggregate cooldown with no state yet for the requested target")
				}
				var attempts []string
				execute := func(_ context.Context, selected *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					attempts = append(attempts, selected.ID)
					if req.Model != targetModel {
						t.Errorf("upstream model = %s, want %s", req.Model, targetModel)
					}
					if selected.ID == highID {
						return cliproxyexecutor.Response{}, streamQuotaError{
							customStatusError: customStatusError{code: http.StatusTooManyRequests, msg: "account quota exhausted", retryAfter: &retryAfter},
							credentialScoped:  true,
						}
					}
					return cliproxyexecutor.Response{Payload: []byte(lowID)}, nil
				}
				manager.RegisterExecutor(&customStreamMockExecutor{
					identifier: "codex", mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: execute},
					streamFn: func(ctx context.Context, selected *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						response, errExecute := execute(ctx, selected, req, opts)
						if errExecute != nil {
							return nil, errExecute
						}
						chunks := make(chan cliproxyexecutor.StreamChunk, 1)
						chunks <- cliproxyexecutor.StreamChunk{Payload: response.Payload}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
					},
				})
				switch path {
				case "select":
					selected, errSelect := manager.SelectAuth(context.Background(), "codex", routeModel, cliproxyexecutor.Options{})
					if errSelect != nil {
						t.Fatal(errSelect)
					}
					if selected.ID != highID || !selected.Unavailable || !selected.Quota.Exceeded {
						t.Fatalf("selection must preserve the selected auth's actual state: %+v", selected)
					}
					return
				case "execute":
					response, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					if string(response.Payload) != lowID {
						t.Fatalf("response = %s, want healthy lower-priority account", response.Payload)
					}
				case "stream":
					result, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{Stream: true})
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil || string(chunk.Payload) != lowID {
							t.Fatalf("chunk = %+v, want healthy lower-priority account", chunk)
						}
					}
				}
				if len(attempts) != 2 || attempts[0] != highID || attempts[1] != lowID {
					t.Fatalf("attempts = %v, want exhausted account then healthy account", attempts)
				}
			})
		}
	}
}

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
