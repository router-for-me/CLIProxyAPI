package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type ultrafastQuotaError struct{}

func (ultrafastQuotaError) Error() string              { return "usage_limit_reached" }
func (ultrafastQuotaError) StatusCode() int            { return http.StatusTooManyRequests }
func (ultrafastQuotaError) IsCredentialScoped() bool   { return true }
func (ultrafastQuotaError) RetryAfter() *time.Duration { d := 10 * time.Minute; return &d }

type ultrafastRecordingExecutor struct {
	schedulerTestExecutor
	mu    sync.Mutex
	calls []string
	fail  map[string]error
}

func (e *ultrafastRecordingExecutor) record(auth *Auth) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, auth.ID)
	return e.fail[auth.ID]
}

func (e *ultrafastRecordingExecutor) recorded() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (e *ultrafastRecordingExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.record(auth)
}

func (e *ultrafastRecordingExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if err := e.record(auth); err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

// newUltrafastManager registers a higher-priority Pro account and a ProMax account.
func newUltrafastManager(t *testing.T, selector Selector, extra ...*Auth) (*Manager, *ultrafastRecordingExecutor, string) {
	t.Helper()
	model := "gpt-6.1-sol-" + uuid.NewString()
	manager := NewManager(nil, selector, nil)
	executor := &ultrafastRecordingExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: "codex"}, fail: map[string]error{}}
	manager.RegisterExecutor(executor)
	auths := append([]*Auth{
		{ID: "pro", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"plan_type": "pro", "priority": "10"}, Metadata: map[string]any{"access_token": "pro-token"}},
		{ID: "promax", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"plan_type": "promax", "priority": "0"}, Metadata: map[string]any{"access_token": "promax-token"}},
	}, extra...)
	ids := make([]string, 0, len(auths))
	for _, auth := range auths {
		if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
		ids = append(ids, auth.ID)
	}
	registerSchedulerModels(t, "codex", model, ids...)
	return manager, executor, model
}

func authID(auth *Auth) string {
	if auth == nil {
		return "<nil>"
	}
	return auth.ID
}

func tierOptions(tier string, metadata map[string]any) cliproxyexecutor.Options {
	meta := map[string]any{cliproxyexecutor.ServiceTierMetadataKey: tier}
	for key, value := range metadata {
		meta[key] = value
	}
	return cliproxyexecutor.Options{Metadata: meta}
}

func TestUltrafastSelectionUsesOnlyEligibleCodexAccounts(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		selector func() Selector
	}{
		{name: "scheduler", selector: func() Selector { return &RoundRobinSelector{} }},
		{name: "session affinity", selector: func() Selector { return NewSessionAffinitySelector(&RoundRobinSelector{}) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager, _, model := newUltrafastManager(t, testCase.selector())
			for _, tier := range []string{"ultrafast", "Ultrafast"} {
				for index := 0; index < 3; index++ {
					got, _, _, errPick := manager.pickNextMixed(context.Background(), []string{"codex"}, model, tierOptions(tier, nil), nil)
					if errPick != nil || got == nil || got.ID != "promax" {
						t.Fatalf("%s pick #%d = %s, %v; want promax", tier, index, authID(got), errPick)
					}
				}
			}
			for _, tier := range []string{"priority", "default", ""} {
				got, _, _, errPick := manager.pickNextMixed(context.Background(), []string{"codex"}, model, tierOptions(tier, nil), nil)
				if errPick != nil || got == nil || got.ID != "pro" {
					t.Fatalf("%q pick = %s, %v; want higher-priority pro", tier, authID(got), errPick)
				}
			}
		})
	}
}

func TestUltrafastSessionAffinityNeverReusesIneligibleBinding(t *testing.T) {
	affinity := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer affinity.Stop()
	manager, _, model := newUltrafastManager(t, affinity)
	session := map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "session-" + uuid.NewString()}

	pick := func(tier string) string {
		t.Helper()
		got, _, _, errPick := manager.pickNextMixed(context.Background(), []string{"codex"}, model, tierOptions(tier, session), nil)
		if errPick != nil || got == nil {
			t.Fatalf("%s pick = %s, %v", tier, authID(got), errPick)
		}
		return got.ID
	}
	if got := pick("priority"); got != "pro" {
		t.Fatalf("initial binding = %s, want pro", got)
	}
	if got := pick("ultrafast"); got != "promax" {
		t.Fatalf("ultrafast on a pro-bound session = %s, want promax", got)
	}
	if got := pick("ultrafast"); got != "promax" {
		t.Fatalf("repeated ultrafast = %s, want promax", got)
	}
}

func TestUltrafastFailoverAndRetriesStayOnEligibleAccounts(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			manager, executor, model := newUltrafastManager(t, NewSessionAffinitySelector(&RoundRobinSelector{}))
			manager.SetRetryConfig(2, time.Millisecond, 0)
			executor.fail["promax"] = ultrafastQuotaError{}
			opts := tierOptions("ultrafast", map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "session-" + uuid.NewString()})
			execute := func() error {
				if stream {
					_, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
					return err
				}
				_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
				return err
			}

			// The first request exhausts ProMax; the second finds it cooling down.
			for attempt := 0; attempt < 2; attempt++ {
				err := execute()
				var authErr *Error
				if err == nil || (errors.As(err, &authErr) && authErr.Code == ErrorCodeServiceTierUnavailable) {
					t.Fatalf("attempt %d error = %v, want the eligible account's quota error", attempt, err)
				}
			}
			for _, id := range executor.recorded() {
				if id != "promax" {
					t.Fatalf("executor calls = %v, want promax only", executor.recorded())
				}
			}
			if len(executor.recorded()) == 0 {
				t.Fatal("promax was never attempted")
			}

			_, _, _, errPick := manager.pickNextMixed(context.Background(), []string{"codex"}, model, tierOptions("priority", nil), nil)
			if errPick != nil {
				t.Fatalf("priority request after ProMax exhaustion error = %v, want pro", errPick)
			}
		})
	}
}

func TestUltrafastReportsUnavailableTierWithoutCallingIneligibleAccounts(t *testing.T) {
	assertUnavailable := func(t *testing.T, err error, executor *ultrafastRecordingExecutor) {
		t.Helper()
		var authErr *Error
		if !errors.As(err, &authErr) || authErr.Code != ErrorCodeServiceTierUnavailable || authErr.StatusCode() != http.StatusBadRequest {
			t.Fatalf("error = %v, want %s (400)", err, ErrorCodeServiceTierUnavailable)
		}
		if calls := executor.recorded(); len(calls) != 0 {
			t.Fatalf("executor calls = %v, want none", calls)
		}
	}

	t.Run("no eligible account", func(t *testing.T) {
		manager, executor, model := newUltrafastManager(t, &RoundRobinSelector{})
		manager.SetRetryConfig(2, time.Millisecond, 0)
		promax, _ := manager.GetByID("promax")
		promax.Disabled = true
		promax.Status = StatusDisabled
		if _, errUpdate := manager.Update(WithSkipPersist(context.Background()), promax); errUpdate != nil {
			t.Fatalf("Update(promax) error = %v", errUpdate)
		}
		_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, tierOptions("ultrafast", nil))
		assertUnavailable(t, err, executor)
		_, err = manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, tierOptions("ultrafast", nil))
		assertUnavailable(t, err, executor)
	})

	t.Run("session pinned to ineligible account", func(t *testing.T) {
		manager, executor, model := newUltrafastManager(t, &RoundRobinSelector{})
		opts := tierOptions("ultrafast", map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "pro"})
		_, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
		assertUnavailable(t, err, executor)
	})
}

func TestUltrafastKeepsCodexAPIKeyPassthrough(t *testing.T) {
	apiKey := &Auth{ID: "api-key", Provider: "codex", Status: StatusActive, Attributes: map[string]string{AttributeAPIKey: "test-key", "priority": "20"}}
	manager, _, model := newUltrafastManager(t, &RoundRobinSelector{}, apiKey)
	got, _, _, errPick := manager.pickNextMixed(context.Background(), []string{"codex"}, model, tierOptions("ultrafast", nil), nil)
	if errPick != nil || got == nil || got.ID != "api-key" {
		t.Fatalf("pick = %s, %v; want highest-priority API key", authID(got), errPick)
	}
}
