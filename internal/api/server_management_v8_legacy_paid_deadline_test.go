package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestManagementV8LegacyPaidDeadline(t *testing.T) {
	for _, scope := range []string{"auth", "model", "shadow"} {
		for _, mode := range []string{"mixed_expired", "mixed_active", "claim_long", "unknown_age", "paid_only"} {
			name := scope + "_" + mode
			t.Run(name, func(t *testing.T) {
				if child := os.Getenv("CPA_OVERAGE_CHILD_CASE"); child == "" {
					runIsolatedOverageCase(t, name)
					return
				} else if child != name {
					t.Fatal("OVERAGE_CHILD_INFRASTRUCTURE: wrong legacy paid case")
				}
				var calls atomic.Int32
				f := newOverageRouteFixture(t, `{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"extra_usage":{"is_enabled":false}}`, func(w http.ResponseWriter, r *http.Request) {
					first := calls.Add(1) == 1
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
					shared := "rejected"
					if first && mode == "paid_only" {
						shared = "allowed"
					}
					w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", shared)
					w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", shared)
					w.Header().Set("Retry-After", "3600")
					if first {
						w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
					}
					if first && mode == "claim_long" {
						w.Header().Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day_overage_included")
					}
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"subscription and paid capacity rejected"}}`)
				})
				f.cfg.Claude.ModelLevelCooling = scope != "auth"
				f.manager.SetConfig(f.cfg)
				f.fail(t, overageTestModel)
				records, err := f.store.Load(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				age := time.Duration(0)
				if mode == "mixed_expired" || mode == "claim_long" {
					age = 2 * time.Minute
				}
				// Advance the original actual failure consistently; its real generic shared
				// window remains in the future, while its short paid fallback can expire.
				shift := func(value time.Time) time.Time {
					if value.IsZero() {
						return value
					}
					return value.Add(-age)
				}
				for i := range records {
					records[i].UpdatedAt = shift(records[i].UpdatedAt)
					records[i].NextRetryAfter = shift(records[i].NextRetryAfter)
					records[i].Quota.NextRecoverAt = shift(records[i].Quota.NextRecoverAt)
					records[i].ClaudeOverageCooldownUntil = shift(records[i].ClaudeOverageCooldownUntil)
				}
				if err = f.store.Save(f.ctx, records); err != nil {
					t.Fatal(err)
				}
				if scope == "shadow" {
					// Restore the aged model failure, then produce a new real credential-wide
					// rejection on a healthy sibling. Its wrapper timestamp must not become
					// the original independent failure's age during the next restore.
					f.restore(t)
					f.cfg.Claude.ModelLevelCooling = false
					f.manager.SetConfig(f.cfg)
					f.fail(t, "claude-fable-5-2")
					records, err = f.store.Load(f.ctx)
					if err != nil {
						t.Fatal(err)
					}
				}
				found := false
				for i := range records {
					record := &records[i]
					if record.Model == overageTestModel {
						if scope == "shadow" {
							if record.IndependentState == nil || record.IndependentCooldownSignals[http.CanonicalHeaderKey("Anthropic-Ratelimit-Unified-Overage-Status")] != "rejected" {
								t.Fatal("actual mixed failure did not survive in persisted shadow")
							}
							if mode == "unknown_age" {
								record.IndependentState.UpdatedAt = time.Time{}
							}
						} else if record.CooldownSignals[http.CanonicalHeaderKey("Anthropic-Ratelimit-Unified-Overage-Status")] != "rejected" {
							t.Fatal("actual mixed paid signals were not persisted")
						}
						found = true
					}
					// These four fields and evaluation flags did not exist in the supported
					// pre-marker schema. Keep all original signals, identity and retry state.
					record.ClaudeOverageEvaluated = false
					record.ClaudeOverageCooldownUntil = time.Time{}
					record.IndependentClaudeOverageCooldownUntil = time.Time{}
					record.ClaudeFablePoolEvaluated = false
					record.ClaudeFablePoolCooldownUntil = time.Time{}
					record.IndependentClaudeFablePoolCooldownUntil = time.Time{}
					if mode == "unknown_age" {
						record.UpdatedAt = time.Time{}
					}
				}
				if !found {
					t.Fatal("actual model cooldown record missing")
				}
				if err = f.store.Save(f.ctx, records); err != nil {
					t.Fatal(err)
				}
				f.restore(t)
				before, _ := f.manager.GetByID(f.auth.ID)
				wantPaid := mode != "mixed_expired"
				if got := coreauth.QuotaRecoveryRequiresClaudeExtraUsage(before); got != wantPaid {
					t.Errorf("legacy restore borrowed shared deadline or lost known paid restriction: paid=%t want=%t", got, wantPaid)
				}
				f.usage(t)
				after, _ := f.manager.GetByID(f.auth.ID)
				if !wantPaid && scope != "shadow" {
					blocked := after.Unavailable || after.Quota.Exceeded
					for _, state := range after.ModelStates {
						if state != nil {
							blocked = blocked || state.Unavailable || state.Quota.Exceeded
						}
					}
					if blocked {
						t.Fatal("expired original paid fallback kept healthy shared usage blocked after legacy restore")
					}
				} else if !reflect.DeepEqual(before, after) {
					t.Fatal("legacy restore erased active or attributed paid restriction")
				}
				if !t.Failed() {
					fmt.Println("OVERAGE_CASE_COMPLETED:" + name)
				}
			})
		}
	}
}
