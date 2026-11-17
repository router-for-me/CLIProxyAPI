package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestManagementV8ClaudeFablePoolRecovery(t *testing.T) {
	for _, phase := range []string{"missing", "null", "healthy_extra", "healthy_scoped", "later_quota", "restore", "update", "rotation", "replacement", "count", "stream"} {
		t.Run(phase, func(t *testing.T) {
			if child := os.Getenv("CPA_OVERAGE_CHILD_CASE"); child == "" {
				runIsolatedOverageCase(t, phase)
				return
			} else if child != phase {
				t.Fatal("OVERAGE_CHILD_INFRASTRUCTURE: wrong Fable pool case")
			}
			usage := `{"five_hour":{"utilization":10},"seven_day":{"utilization":20}`
			switch phase {
			case "null":
				usage += `,"seven_day_opus":null,"iguana_necktie":null`
			case "healthy_extra":
				usage += `,"extra_usage":{"is_enabled":true,"utilization":0}`
			case "healthy_scoped":
				usage += `,"seven_day_opus":{"utilization":0},"limits":[{"kind":"weekly_scoped","percent":0,"scope":{"model":{"display_name":"Fable 5"}}}]`
			}
			usage += `}`
			calls := 0
			fixture := newOverageRouteFixture(t, usage, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
				if phase == "later_quota" && calls == 2 {
					w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
				} else {
					w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
					w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed_warning")
					w.Header().Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
				}
				w.WriteHeader(429)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Fable usage window rejected."}}`)
			})
			if phase == "later_quota" {
				fixture.cfg.Claude.ModelLevelCooling = true
				fixture.manager.SetConfig(fixture.cfg)
			}
			if phase == "count" || phase == "stream" {
				fixture.kind = phase
			}
			fixture.fail(t, overageTestModel)
			if phase == "later_quota" {
				fixture.fail(t, "claude-opus-5")
			}
			if phase == "update" || phase == "rotation" || phase == "replacement" {
				current, _ := fixture.manager.GetByID(fixture.auth.ID)
				raw, err := json.Marshal(current)
				if err != nil {
					t.Fatal(err)
				}
				updated := &coreauth.Auth{}
				if err = json.Unmarshal(raw, updated); err != nil {
					t.Fatal(err)
				}
				updated.Metadata["notes"] = "edit"
				if phase == "rotation" {
					updated.Metadata["access_token"] = "sk-ant-oat01-fable-rotated"
					fixture.bearer.Store("sk-ant-oat01-fable-rotated")
				}
				if phase == "replacement" {
					updated.Metadata["account_uuid"] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
				}
				if _, err = fixture.manager.Update(fixture.ctx, updated); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "restore" {
				fixture.restore(t)
			}
			before, _ := fixture.manager.GetByID(fixture.auth.ID)
			if !coreauth.QuotaRecoveryCandidate(before) {
				t.Fatal("actual standalone Fable rejection not recoverable quota candidate")
			}
			fixture.usage(t)
			after, _ := fixture.manager.GetByID(fixture.auth.ID)
			if phase == "replacement" {
				if state := after.ModelStates[overageTestModel]; state != nil && state.Quota.Exceeded {
					t.Fatal("replacement account quota did not recover")
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatal("shared usage cleared standalone Fable pool cooldown")
			}
			if !t.Failed() {
				fmt.Println("OVERAGE_CASE_COMPLETED:" + phase)
			}
		})
	}
}

func TestManagementV8ClaudeFablePoolAttribution(t *testing.T) {
	for _, phase := range []string{"sibling", "update", "refresh", "rotation", "replacement", "replacement_restore", "restore", "later_longer", "earlier_longer", "later_longer_restore", "shadow_restore", "credential_first", "late_old_account", "late_old_account_count", "late_old_account_stream", "late_same_account", "count", "stream", "manual", "manual_auth", "manual_auth_restore", "alias_source_later", "alias_target_later", "new_failure_during_usage"} {
		t.Run(phase, func(t *testing.T) {
			if child := os.Getenv("CPA_OVERAGE_CHILD_CASE"); child == "" {
				runIsolatedOverageCase(t, phase)
				return
			} else if child != phase {
				t.Fatal("OVERAGE_CHILD_INFRASTRUCTURE: wrong attribution case")
			}
			usage := `{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"extra_usage":{"is_enabled":false}}`
			entered := make(chan int, 2)
			releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
			var order atomic.Int32
			concurrent := phase == "later_longer" || phase == "earlier_longer" || phase == "later_longer_restore" || phase == "shadow_restore" || phase == "credential_first" || strings.HasPrefix(phase, "late_old_account") || phase == "late_same_account" || phase == "new_failure_during_usage"
			fixture := newOverageRouteFixture(t, usage, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var request struct {
					Model string `json:"model"`
				}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Error("local fixture received malformed business JSON")
					http.Error(w, "bad fixture request", 400)
					return
				}
				t.Logf("local fixture business model=%s", request.Model)
				if request.Model == "claude-opus-5" {
					w.Header().Set("Anthropic-Ratelimit-Unified-Status", "allowed")
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"msg-ok","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
					return
				}
				n := int(order.Add(1))
				if concurrent {
					entered <- n
					if n == 1 {
						<-releaseFirst
					} else {
						<-releaseSecond
					}
				}
				overage := true
				if (phase == "earlier_longer" || phase == "credential_first") && n == 1 || (phase == "later_longer" || phase == "later_longer_restore" || phase == "shadow_restore" || phase == "new_failure_during_usage") && n == 2 {
					overage = false
				}
				if !overage {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
					w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
					w.Header().Set("Retry-After", "3600")
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"shared subscription exhausted"}}`)
					return
				}
				writeStandaloneFablePoolRejection(w)
			})
			if strings.Contains(phase, "longer") || phase == "new_failure_during_usage" {
				fixture.cfg.Claude.ModelLevelCooling = true
				fixture.manager.SetConfig(fixture.cfg)
			}
			if phase == "count" || phase == "late_old_account_count" {
				fixture.kind = "count"
			}
			if phase == "stream" || phase == "late_old_account_stream" {
				fixture.kind = "stream"
			}
			var firstDeadline time.Time
			if concurrent {
				failures := make(chan error, 2)
				go func() { failures <- fixture.execute(overageTestModel) }()
				if n := <-entered; n != 1 {
					t.Fatal("first actual request ordering changed")
				}
				if strings.HasPrefix(phase, "late_old_account") || phase == "late_same_account" {
					current, _ := fixture.manager.GetByID(fixture.auth.ID)
					if strings.HasPrefix(phase, "late_old_account") {
						current.Metadata["account_uuid"] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
					} else {
						current.Metadata["notes"] = "same account update"
					}
					if _, err := fixture.manager.Update(fixture.ctx, current); err != nil {
						t.Fatal(err)
					}
					close(releaseFirst)
					if err := <-failures; err == nil {
						t.Fatal("late actual overage failure missing")
					}
				} else {
					go func() { failures <- fixture.execute(overageTestModel) }()
					if n := <-entered; n != 2 {
						t.Fatal("second actual request ordering changed")
					}
					if phase == "new_failure_during_usage" {
						// Request two records ordinary quota first. The earlier request's overage
						// result arrives only after the authenticated usage snapshot was taken.
						close(releaseSecond)
						if err := <-failures; err == nil {
							t.Fatal("ordinary actual failure missing")
						}
						usageEntered, usageRelease := make(chan struct{}), make(chan struct{})
						fixture.setUsageHook(func() { close(usageEntered); <-usageRelease })
						usageDone := make(chan struct{})
						go func() { defer close(usageDone); fixture.usage(t) }()
						<-usageEntered
						close(releaseFirst)
						if err := <-failures; err == nil {
							t.Fatal("new actual overage failure missing")
						}
						afterFailure, _ := fixture.manager.GetByID(fixture.auth.ID)
						close(usageRelease)
						<-usageDone
						afterUsage, _ := fixture.manager.GetByID(fixture.auth.ID)
						if !reflect.DeepEqual(afterFailure, afterUsage) {
							t.Fatal("stale usage snapshot cleared new actual overage failure")
						}
						if !t.Failed() {
							fmt.Println("OVERAGE_CASE_COMPLETED:" + phase)
						}
						return
					}
					close(releaseFirst)
					if err := <-failures; err == nil {
						t.Fatal("first actual rejection missing")
					}
					if phase != "earlier_longer" && phase != "credential_first" {
						firstDeadline = fixture.fablePoolDeadline(t, false)
					}
					close(releaseSecond)
					if err := <-failures; err == nil {
						t.Fatal("second actual rejection missing")
					}
				}
			} else if strings.HasPrefix(phase, "manual") {
				ctx := logging.WithResponseHeadersHolder(fixture.ctx)
				a, _ := fixture.manager.GetByID(fixture.auth.ID)
				_, err := runtimeexecutor.NewClaudeExecutor(fixture.cfg).Execute(ctx, a, cliproxyexecutor.Request{Model: overageTestModel, Payload: []byte(`{"model":"claude-fable-5-1","max_tokens":16,"messages":[{"role":"user","content":"test"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
				if err == nil || fixture.messages.Load() != 1 {
					t.Fatal("manual real executor rejection missing")
				}
				status, ok := err.(interface{ StatusCode() int })
				if !ok {
					t.Fatal("actual executor error lost HTTP status")
				}
				resultModel := overageTestModel
				if strings.HasPrefix(phase, "manual_auth") {
					resultModel = ""
				}
				result := coreauth.Result{AuthID: a.ID, Provider: "claude", Model: resultModel, Error: &coreauth.Error{HTTPStatus: status.StatusCode(), Message: err.Error()}}
				if hint, ok := err.(interface{ RetryAfter() *time.Duration }); ok {
					result.RetryAfter = hint.RetryAfter()
				}
				fixture.manager.MarkResult(ctx, result)
			} else {
				fixture.fail(t, overageTestModel)
			}

			if strings.HasPrefix(phase, "alias_") {
				fixture.fail(t, "claude-fable-5-2")
				records, err := fixture.store.Load(fixture.ctx)
				if err != nil {
					t.Fatal(err)
				}
				var latest time.Time
				for _, record := range records {
					if record.Model == "claude-fable-5-2" {
						latest = record.ClaudeFablePoolCooldownUntil
					}
				}
				target, source := overageTestModel, "claude-fable-5-2"
				if phase == "alias_target_later" {
					target, source = source, target
				}
				registry.GetGlobalRegistry().RegisterClient(fixture.auth.ID, "claude", []*registry.ModelInfo{{ID: target}, {ID: "claude-opus-5"}})
				fixture.manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{"claude": {{Name: target, Alias: source}}})
				merged, _ := fixture.manager.GetByID(fixture.auth.ID)
				if merged.ModelStates[source] != nil || merged.ModelStates[target] == nil {
					t.Fatal("actual alias reconciliation did not merge both model states")
				}
				records, err = fixture.store.Load(fixture.ctx)
				if err != nil {
					t.Fatal(err)
				}
				var actual time.Time
				for _, record := range records {
					if record.Model == target {
						actual = record.ClaudeFablePoolCooldownUntil
					}
				}
				if latest.IsZero() || !actual.Equal(latest) {
					t.Fatal("alias merge lost maximum Fable pool causal deadline")
				}
			}
			if phase == "sibling" {
				if err := fixture.execute("claude-opus-5"); err != nil {
					t.Fatalf("healthy sibling failed: %v", err)
				}
				sibling, _ := fixture.manager.GetByID(fixture.auth.ID)
				if sibling.Quota.Signals["Anthropic-Ratelimit-Unified-Status"] != "allowed" {
					t.Fatal("healthy sibling did not replace passive observation")
				}
			}
			if phase == "update" || phase == "refresh" || phase == "rotation" || phase == "replacement" || phase == "replacement_restore" {
				current, _ := fixture.manager.GetByID(fixture.auth.ID)
				updated := current.Clone()
				if phase == "update" || phase == "refresh" || phase == "rotation" {
					public, err := json.Marshal(current)
					if err != nil {
						t.Fatal(err)
					}
					updated = &coreauth.Auth{}
					if err = json.Unmarshal(public, updated); err != nil {
						t.Fatal(err)
					}
				}
				updated.Metadata["notes"] = "ordinary edit"
				if phase == "replacement" || phase == "replacement_restore" {
					updated.Metadata["account_uuid"] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
				}
				if phase == "rotation" {
					updated.Metadata["access_token"] = "sk-ant-oat01-overage-rotated"
					fixture.bearer.Store("sk-ant-oat01-overage-rotated")
				}
				var err error
				if phase == "refresh" || phase == "rotation" {
					_, err = fixture.manager.UpdateRefreshedAuth(fixture.ctx, current, updated)
				} else {
					_, err = fixture.manager.Update(fixture.ctx, updated)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(phase, "longer") {
				deadline := fixture.fablePoolDeadline(t, false)
				state, _ := fixture.manager.GetByID(fixture.auth.ID)
				if deadline.IsZero() || !deadline.Before(state.ModelStates[overageTestModel].Quota.NextRecoverAt.Add(-30*time.Minute)) {
					t.Fatal("unrelated long quota inflated Fable pool own deadline")
				}
				if !firstDeadline.IsZero() && !deadline.Equal(firstDeadline) {
					t.Fatal("later ordinary quota replaced Fable pool causal deadline")
				}
			}
			var independentDeadline time.Time
			if phase == "shadow_restore" || phase == "credential_first" {
				independentDeadline = fixture.fablePoolDeadline(t, true)
				if independentDeadline.IsZero() {
					t.Fatal("credential shadow lost Fable pool causal deadline")
				}
				current, _ := fixture.manager.GetByID(fixture.auth.ID)
				if !independentDeadline.Before(current.Quota.NextRecoverAt.Add(-30 * time.Minute)) {
					t.Fatal("outer credential quota inflated independent overage deadline")
				}
			}
			if phase == "restore" || phase == "later_longer_restore" || phase == "shadow_restore" || phase == "replacement_restore" || phase == "manual_auth_restore" || phase == "credential_first" {
				fixture.restore(t)
			}
			if !independentDeadline.IsZero() && !fixture.fablePoolDeadline(t, true).Equal(independentDeadline) {
				t.Fatal("restored independent cooldown lost Fable pool causal deadline")
			}
			beforeUsage, _ := fixture.manager.GetByID(fixture.auth.ID)
			recover := phase == "replacement" || phase == "replacement_restore" || strings.HasPrefix(phase, "late_old_account")
			if coreauth.QuotaRecoveryRequiresClaudeFablePool(beforeUsage) == recover {
				t.Fatalf("Fable pool attribution crossed configuration or was lost: phase=%s", phase)
			}
			fixture.usage(t)
			after, _ := fixture.manager.GetByID(fixture.auth.ID)
			if recover {
				state := after.ModelStates[overageTestModel]
				if state != nil && (state.Unavailable || state.Quota.Exceeded) {
					t.Fatal("replacement account ordinary cooldown did not recover")
				}
			} else if !reflect.DeepEqual(beforeUsage, after) {
				t.Fatal("unproven Fable pool capacity changed retained cooldown")
			}
			if !t.Failed() {
				fmt.Println("OVERAGE_CASE_COMPLETED:" + phase)
			}
		})
	}
}

func writeStandaloneFablePoolRejection(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
	w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
	w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed_warning")
	w.Header().Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
	w.Header().Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", "4102444800")
	w.WriteHeader(429)
	_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Fable usage window rejected."}}`)
}
func (f *overageRouteFixture) fablePoolDeadline(t *testing.T, independent bool) time.Time {
	t.Helper()
	records, err := f.store.Load(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Model == overageTestModel {
			if independent {
				return record.IndependentClaudeFablePoolCooldownUntil
			}
			return record.ClaudeFablePoolCooldownUntil
		}
	}
	t.Fatal("actual Fable cooldown record missing")
	return time.Time{}
}

func TestManagementV8ClaudeFablePoolLegacyRestore(t *testing.T) {
	t.Run("old_header_record", func(t *testing.T) {
		if child := os.Getenv("CPA_OVERAGE_CHILD_CASE"); child == "" {
			runIsolatedOverageCase(t, "old_header_record")
			return
		} else if child != "old_header_record" {
			t.Fatal("OVERAGE_CHILD_INFRASTRUCTURE: wrong legacy case")
		}
		f := newOverageRouteFixture(t, `{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"extra_usage":{"is_enabled":false}}`, func(w http.ResponseWriter, r *http.Request) { writeStandaloneFablePoolRejection(w) })
		f.fail(t, overageTestModel)
		records, err := f.store.Load(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		// Downgrade only the newly added fields on a real persisted wire rejection.
		// The existing bounded rejected headers are exactly the old record evidence.
		for i := range records {
			records[i].ClaudeFablePoolEvaluated = false
			records[i].ClaudeFablePoolCooldownUntil = time.Time{}
			records[i].IndependentClaudeFablePoolCooldownUntil = time.Time{}
		}
		if err = f.store.Save(f.ctx, records); err != nil {
			t.Fatal(err)
		}
		f.restore(t)
		before, _ := f.manager.GetByID(f.auth.ID)
		if !coreauth.QuotaRecoveryRequiresClaudeFablePool(before) {
			t.Fatal("legacy rejected standalone Fable headers lost attribution during restore")
		}
		f.usage(t)
		after, _ := f.manager.GetByID(f.auth.ID)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("unproven standalone Fable capacity changed legacy cooldown")
		}
		if !t.Failed() {
			fmt.Println("OVERAGE_CASE_COMPLETED:old_header_record")
		}
	})
}
