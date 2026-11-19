package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestManagementV8ClaudePaidOverageSharedRejection(t *testing.T) {
	for _, tc := range []struct {
		name, extra                               string
		authScope, oi, recover, restore, ordinary bool
		statusOnly, reasonOnly                    bool
	}{
		{name: "model_disabled", statusOnly: true, extra: `{"is_enabled":false}`},
		{name: "auth_exhausted", authScope: true, reasonOnly: true, extra: `{"is_enabled":true,"utilization":100,"monthly_limit":1000,"used_credits":1000}`},
		{name: "model_oi_disabled", oi: true, extra: `{"is_enabled":false}`},
		{name: "auth_oi_exhausted", authScope: true, oi: true, extra: `{"is_enabled":true,"utilization":100,"monthly_limit":1000,"used_credits":1000}`},
		{name: "model_healthy", recover: true, extra: `{"is_enabled":true,"utilization":10,"monthly_limit":1000,"used_credits":100}`},
		{name: "auth_oi_healthy", authScope: true, oi: true, recover: true, extra: `{"is_enabled":true,"utilization":10,"monthly_limit":1000,"used_credits":100}`},
		{name: "model_restore_disabled", restore: true, extra: `{"is_enabled":false}`},
		{name: "auth_restore_disabled", authScope: true, restore: true, extra: `{"is_enabled":false}`},
		{name: "ordinary_shared_healthy", ordinary: true, recover: true, extra: `{"is_enabled":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if child := os.Getenv("CPA_OVERAGE_CHILD_CASE"); child == "" {
				runIsolatedOverageCase(t, tc.name)
				return
			} else if child != tc.name {
				t.Fatal("OVERAGE_CHILD_INFRASTRUCTURE: wrong paid/shared case")
			}
			usage := `{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"extra_usage":` + tc.extra + `}`
			fixture := newOverageRouteFixture(t, usage, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
				w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
				w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
				w.Header().Set("Retry-After", "120")
				if !tc.ordinary {
					if !tc.reasonOnly {
						w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
					}
					if !tc.statusOnly {
						w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Disabled-Reason", "org_spend_cap_reached")
					}
				}
				if tc.oi {
					w.Header().Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
				}
				w.WriteHeader(429)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Shared subscription and paid usage rejected."}}`)
			})
			fixture.cfg.Claude.ModelLevelCooling = !tc.authScope
			fixture.manager.SetConfig(fixture.cfg)
			started := time.Now()
			fixture.fail(t, overageTestModel)
			before, _ := fixture.manager.GetByID(fixture.auth.ID)
			if (before.Quota.Reason == "credential_quota") != tc.authScope {
				t.Fatal("actual shared rejection scope did not match cooling policy")
			}
			if tc.restore {
				fixture.restore(t)
				before, _ = fixture.manager.GetByID(fixture.auth.ID)
			}
			paid := coreauth.QuotaRecoveryRequiresClaudeExtraUsage(before)
			opaque := coreauth.QuotaRecoveryRequiresClaudeFablePool(before)
			records, err := fixture.store.Load(fixture.ctx)
			if err != nil {
				t.Fatal(err)
			}
			persisted := false
			for _, record := range records {
				for _, deadline := range []time.Time{record.ClaudeOverageCooldownUntil, record.IndependentClaudeOverageCooldownUntil} {
					if deadline.After(started) {
						persisted = true
						if deadline.After(started.Add(10 * time.Minute)) {
							t.Fatal("paid attribution inherited an unrelated quota boundary")
						}
					}
				}
			}
			fixture.usage(t)
			after, _ := fixture.manager.GetByID(fixture.auth.ID)
			if tc.recover {
				if after.Quota.Exceeded || after.Unavailable {
					t.Fatal("healthy paid/shared capacity did not recover auth")
				}
				if state := after.ModelStates[overageTestModel]; state != nil && (state.Unavailable || state.Quota.Exceeded) {
					t.Fatal("healthy paid/shared capacity did not recover model")
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatal("healthy shared usage cleared rejected paid-overage cooldown")
			}
			if !tc.ordinary && (!paid || opaque || !persisted) {
				t.Fatal("explicit paid-overage attribution lost with shared-window rejection")
			}
			if tc.ordinary && (paid || opaque || persisted) {
				t.Fatal("ordinary shared rejection acquired independent paid attribution")
			}
			if !t.Failed() {
				fmt.Println("OVERAGE_CASE_COMPLETED:" + tc.name)
			}
		})
	}
}
