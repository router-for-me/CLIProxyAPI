package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestCodexSubscriptionTypeCustomCodeCapture(t *testing.T) {
	for _, scope := range []string{"model", "auth"} {
		t.Run(scope, func(t *testing.T) {
			withQuotaCooldownEnabled(t)
			ctx := context.Background()
			cfg := &internalconfig.Config{}
			cfg.Codex.ModelLevelCooling = true
			m := NewManager(nil, nil, nil)
			m.SetConfig(cfg)
			id, route := t.Name(), t.Name()+"-model"
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: route}})
			t.Cleanup(func() { reg.UnregisterClient(id) })
			a, err := m.Register(ctx, &Auth{ID: id, Provider: "codex", Status: StatusActive, Metadata: map[string]any{"account_id": "same-account", "access_token": "same-token"}})
			if err != nil {
				t.Fatal(err)
			}
			resultErr := &Error{HTTPStatus: 429, Code: "provider_detail", Message: `{"error":{"type":"usage_limit_reached"}}`}
			if !quotaCodexSubscriptionError(resultErr) {
				t.Fatal("fixture is not an explicit subscription rejection")
			}
			model := ""
			if scope == "model" {
				model = route
			}
			duration := time.Minute
			m.MarkResult(ctx, Result{AuthID: a.ID, Provider: "codex", Model: model, Error: resultErr, RetryAfter: &duration})
			before, _ := m.GetByID(id)
			if !before.nonQuotaCooldownUntil.IsZero() {
				t.Error("recognized subscription rejection acquired an auth independent deadline")
			}
			if model != "" {
				state := before.ModelStates[model]
				if state == nil || !state.Quota.Exceeded {
					t.Fatal("model quota restriction missing")
				}
				if !state.nonQuotaCooldownUntil.IsZero() {
					t.Error("recognized subscription rejection acquired a model independent deadline")
				}
			}
			if !QuotaRecoveryCandidate(before) {
				t.Error("recognized subscription rejection lost canonical recovery eligibility")
			}
			got, _, err := m.ResetQuotaIfUnchanged(ctx, before)
			if err != nil || got == nil {
				t.Fatalf("healthy same-identity recovery=%v err=%v", got != nil, err)
			}
			if got.Quota.Exceeded || got.Unavailable || !got.NextRetryAfter.IsZero() {
				t.Fatal("canonical recovery did not clear subscription restriction")
			}
		})
	}
}

type codexCustomCodeBodyQuotaErr struct{ detail map[string]string }

func (e codexCustomCodeBodyQuotaErr) Error() string     { return "upstream 429" }
func (e codexCustomCodeBodyQuotaErr) StatusCode() int   { return 429 }
func (e codexCustomCodeBodyQuotaErr) ErrorCode() string { return "provider_detail" }
func (e codexCustomCodeBodyQuotaErr) ResponseBody() []byte {
	return []byte(`{"error":{"code":"quota_exceeded"}}`)
}
func (e codexCustomCodeBodyQuotaErr) TOSAppealMetadata() map[string]string { return e.detail }

func TestCodexContinuityCustomCodeResponseBody(t *testing.T) {
	for _, scope := range []string{"model", "auth"} {
		t.Run(scope, func(t *testing.T) {
			withQuotaCooldownEnabled(t)
			source := codexCustomCodeBodyQuotaErr{detail: map[string]string{"provider": "preserve detail"}}
			if !isPositiveQuotaRejectionError(source) {
				t.Fatal("source error is not an eligible continuity quota rejection")
			}
			converted := resultErrorFromError(source)
			if converted.Code != "provider_detail" || converted.Message != "upstream 429" || converted.HTTPStatus != 429 || converted.Metadata["provider"] != "preserve detail" {
				t.Fatalf("conversion changed existing provider fields: %#v", converted)
			}
			if converted.Metadata[positiveQuotaRejectionMetadataKey] != "true" {
				t.Error("converted result lost positive quota classification")
			}
			if _, changed := source.detail[positiveQuotaRejectionMetadataKey]; changed {
				t.Error("conversion mutated provider-owned metadata")
			}
			cfg := &internalconfig.Config{}
			cfg.Codex.ModelLevelCooling = scope == "model"
			clock := newFakeContinuityClock(time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC))
			m := NewManager(nil, nil, nil)
			m.SetConfig(cfg)
			m.SetContinuityClockForTest(clock)
			m.SetRetryConfig(1, 0, 0)
			var attempts atomic.Int32
			m.RegisterExecutor(&continuityTestExecutor{provider: "codex", execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if attempts.Add(1) == 1 {
					return cliproxyexecutor.Response{}, source
				}
				return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
			}})
			id, model := t.Name(), t.Name()+"-model"
			if _, err := m.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			var calls atomic.Int32
			m.SetPluginScheduler(&testContinuityScheduler{hasContinuity: true, continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				if calls.Add(1) == 1 {
					return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 25, WaitBudgetMs: 500}, true, nil
				}
				m.mu.Lock()
				cur := m.auths[id]
				clearCooldownStateForAuth(cur, time.Now())
				cur.LastError = nil
				cur.Status = StatusActive
				snap := cur.Clone()
				m.mu.Unlock()
				if m.scheduler != nil {
					m.scheduler.upsertAuth(snap)
				}
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
			}})
			response, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			if err != nil {
				t.Fatalf("actual wrapped-body Execute failed: %v", err)
			}
			if string(response.Payload) != `{"ok":true}` || attempts.Load() != 2 || calls.Load() != 2 {
				t.Fatalf("attempts=%d calls=%d response=%s", attempts.Load(), calls.Load(), response.Payload)
			}
			if _, changed := source.detail[positiveQuotaRejectionMetadataKey]; changed {
				t.Fatal("execution mutated provider-owned metadata")
			}
		})
	}
}
