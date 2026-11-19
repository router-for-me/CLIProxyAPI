package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestManagementV8ClaudeMixedPaidDeadline(t *testing.T) {
	for _, scope := range []string{"model", "auth"} {
		for _, phase := range []string{"unknown_live", "unknown_expired", "shared_live", "shared_expired", "overage_live", "overage_healthy", "unknown_healthy"} {
			name := scope + "_" + phase
			t.Run(name, func(t *testing.T) {
				if child := os.Getenv("CPA_OVERAGE_CHILD_CASE"); child == "" {
					runIsolatedOverageCase(t, name)
					return
				} else if child != name {
					t.Fatal("OVERAGE_CHILD_INFRASTRUCTURE: wrong mixed deadline case")
				}
				known := phase == "overage_live" || phase == "overage_healthy"
				healthy := phase == "overage_healthy" || phase == "unknown_healthy"
				expired := phase == "unknown_expired" || phase == "shared_expired"
				extra := `{"is_enabled":false}`
				if healthy {
					extra = `{"is_enabled":true,"utilization":10,"monthly_limit":1000,"used_credits":100}`
				}
				f := newOverageRouteFixture(t, `{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"extra_usage":`+extra+`}`, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
					w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
					w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
					w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
					w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Disabled-Reason", "org_spend_cap_reached")
					if known {
						w.Header().Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day_overage_included")
					} else if phase == "shared_live" || phase == "shared_expired" {
						w.Header().Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day")
					}
					w.Header().Set("Retry-After", "3600")
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Shared subscription and paid usage rejected."}}`)
				})
				f.cfg.Claude.ModelLevelCooling = scope == "model"
				f.manager.SetConfig(f.cfg)
				began := time.Now()
				f.fail(t, overageTestModel)
				records, err := f.store.Load(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				seen, generic := false, false
				for i := range records {
					r := &records[i]
					if r.Quota.NextRecoverAt.After(began.Add(30*time.Minute)) || r.NextRetryAfter.After(began.Add(30*time.Minute)) {
						generic = true
					}
					for _, own := range []*time.Time{&r.ClaudeOverageCooldownUntil, &r.IndependentClaudeOverageCooldownUntil} {
						if own.IsZero() {
							continue
						}
						seen = true
						if known {
							if own.Before(began.Add(59*time.Minute)) || own.After(time.Now().Add(61*time.Minute)) {
								t.Error("representative overage claim lost its paid-attributed retry hint")
							}
						} else {
							if own.Before(began) || own.After(time.Now().Add(5*time.Second)) {
								t.Error("ambiguous mixed response borrowed generic shared retry deadline")
							}
							// Move only the observed causal marker across the clock boundary. The
							// actual generic plan window remains future, including after restore.
							if expired {
								*own = own.Add(-2 * time.Minute)
							} else {
								*own = own.Add(2 * time.Minute)
							}
						}
					}
				}
				if !seen || !generic {
					t.Fatal("actual mixed rejection did not persist paid and generic deadlines")
				}
				if err = f.store.Save(f.ctx, records); err != nil {
					t.Fatal(err)
				}
				f.restore(t)
				before, _ := f.manager.GetByID(f.auth.ID)
				if (before.Quota.Reason == "credential_quota") != (scope == "auth") {
					t.Fatal("mixed rejection scope changed on restore")
				}
				paid := coreauth.QuotaRecoveryRequiresClaudeExtraUsage(before)
				if paid == expired {
					t.Error("restored paid marker did not respect its independent clock boundary")
				}
				if coreauth.QuotaRecoveryRequiresClaudeFablePool(before) {
					t.Fatal("mixed paid rejection acquired opaque pool attribution")
				}
				planFuture := before.Quota.NextRecoverAt.After(time.Now().Add(30 * time.Minute))
				if s := before.ModelStates[overageTestModel]; s != nil {
					planFuture = planFuture || s.Quota.NextRecoverAt.After(time.Now().Add(30*time.Minute))
				}
				if !planFuture {
					t.Fatal("generic shared plan boundary was aged with paid marker")
				}
				f.usage(t)
				after, _ := f.manager.GetByID(f.auth.ID)
				if expired || healthy {
					blocked := after.Unavailable || after.Quota.Exceeded
					if s := after.ModelStates[overageTestModel]; s != nil {
						blocked = blocked || s.Unavailable || s.Quota.Exceeded
					}
					if blocked {
						t.Error("healthy shared usage did not recover after independent paid cause allowed it")
					}
				} else if !reflect.DeepEqual(before, after) {
					t.Error("healthy shared usage cleared live paid restriction without paid capacity")
				}
				if !t.Failed() {
					fmt.Println("OVERAGE_CASE_COMPLETED:" + name)
				}
			})
		}
	}
	// The SDK result boundary accepts a provider-supplied hint independently of
	// the built-in HTTP parser, which suppresses wire hints for overage-only errors.
	for _, scope := range []string{"model", "auth"} {
		name := scope + "_sdk_overage_only"
		t.Run(name, func(t *testing.T) {
			if child := os.Getenv("CPA_OVERAGE_CHILD_CASE"); child == "" {
				runIsolatedOverageCase(t, name)
				return
			} else if child != name {
				t.Fatal("OVERAGE_CHILD_INFRASTRUCTURE: wrong SDK deadline case")
			}
			f := newOverageRouteFixture(t, `{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"extra_usage":{"is_enabled":false}}`, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
				w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
				w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
				w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
				w.Header().Set("Retry-After", "3600")
				w.WriteHeader(429)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Paid overage rejected."}}`)
			})
			ctx := logging.WithResponseHeadersHolder(f.ctx)
			a, _ := f.manager.GetByID(f.auth.ID)
			_, err := runtimeexecutor.NewClaudeExecutor(f.cfg).Execute(ctx, a, cliproxyexecutor.Request{Model: overageTestModel, Payload: []byte(`{"model":"claude-fable-5-1","max_tokens":16,"messages":[{"role":"user","content":"test"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			status, ok := err.(interface{ StatusCode() int })
			if !ok || status.StatusCode() != 429 || f.messages.Load() != 1 {
				t.Fatal("SDK control did not capture a real local HTTP rejection")
			}
			if hint, ok := err.(interface{ RetryAfter() *time.Duration }); !ok || hint.RetryAfter() != nil {
				t.Fatal("overage-only wire parser unexpectedly retained a generic retry hint")
			}
			model := overageTestModel
			if scope == "auth" {
				model = ""
			}
			hour := time.Hour
			began := time.Now()
			// This is an explicit supported SDK result, not a claim that the built-in
			// HTTP parser returned the long provider hint.
			f.manager.MarkResult(ctx, coreauth.Result{AuthID: a.ID, Provider: "claude", Model: model, RetryAfter: &hour, Error: &coreauth.Error{HTTPStatus: status.StatusCode(), Message: err.Error()}})
			records, loadErr := f.store.Load(f.ctx)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			seen := false
			for _, record := range records {
				if record.Model != model {
					continue
				}
				own := record.ClaudeOverageCooldownUntil
				if own.Before(began.Add(59*time.Minute)) || own.After(time.Now().Add(61*time.Minute)) {
					t.Error("SDK overage-only hint lost its independent paid deadline")
				}
				if !record.Quota.NextRecoverAt.After(began.Add(59 * time.Minute)) {
					t.Error("SDK result lost its generic quota deadline")
				}
				seen = true
			}
			if !seen {
				t.Fatal("SDK result missing scoped persistence record")
			}
			f.restore(t)
			before, _ := f.manager.GetByID(f.auth.ID)
			if !coreauth.QuotaRecoveryRequiresClaudeExtraUsage(before) || coreauth.QuotaRecoveryRequiresClaudeFablePool(before) {
				t.Fatal("SDK result lost separate paid attribution after restore")
			}
			f.usage(t)
			after, _ := f.manager.GetByID(f.auth.ID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("healthy shared usage cleared SDK paid hint without paid capacity")
			}
			if !t.Failed() {
				fmt.Println("OVERAGE_CASE_COMPLETED:" + name)
			}
		})
	}
}
