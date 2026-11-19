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

// These cases pin the conservative local recovery contract. They establish no
// mapping from 7d_oi to a canonical provider usage field or paid extra_usage.
func TestManagementV8ClaudeFablePoolSharedStatusContract(t *testing.T) {
	for _, tc := range []struct {
		name, five, seven string
		paid              bool
	}{
		{name: "shared_five_rejected", five: "rejected", seven: "allowed"},
		{name: "shared_seven_rejected", five: "allowed", seven: "rejected"},
		{name: "shared_both_rejected", five: "rejected", seven: "rejected"},
		{name: "shared_missing"},
		{name: "shared_unknown", five: "unknown", seven: "unknown"},
		{name: "qualified_paid_overage", paid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if child := os.Getenv("CPA_OVERAGE_CHILD_CASE"); child == "" {
				runIsolatedOverageCase(t, tc.name)
				return
			} else if child != tc.name {
				t.Fatal("OVERAGE_CHILD_INFRASTRUCTURE: wrong shared-status contract case")
			}
			usage := `{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"extra_usage":{"is_enabled":true,"utilization":10,"monthly_limit":1000,"used_credits":100}}`
			fixture := newOverageRouteFixture(t, usage, func(w http.ResponseWriter, r *http.Request) {
				if tc.paid {
					writeOverageTestRejection(w, true)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
				w.Header().Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
				w.Header().Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", "4102444800")
				if tc.five != "" {
					w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", tc.five)
				}
				if tc.seven != "" {
					w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", tc.seven)
				}
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(429)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Fable usage window rejected."}}`)
			})
			// Keep the actual shared-window rejection on the model layer so the
			// independent credential-wrapper guard cannot mask capacity admission.
			fixture.cfg.Claude.ModelLevelCooling = true
			fixture.manager.SetConfig(fixture.cfg)
			started := time.Now()
			fixture.fail(t, overageTestModel)
			before, _ := fixture.manager.GetByID(fixture.auth.ID)
			if !coreauth.QuotaRecoveryCandidate(before) {
				t.Fatal("shared-status fixture did not establish a recoverable quota candidate")
			}
			if coreauth.QuotaRecoveryRequiresClaudeFablePool(before) == tc.paid {
				t.Fatal("standalone Fable attribution changed with shared-window status")
			}
			records, err := fixture.store.Load(fixture.ctx)
			if err != nil {
				t.Fatal(err)
			}
			persisted := false
			for _, record := range records {
				for _, deadline := range []time.Time{record.ClaudeFablePoolCooldownUntil, record.IndependentClaudeFablePoolCooldownUntil} {
					if deadline.After(started) {
						persisted = true
						if deadline.After(started.Add(10 * time.Minute)) {
							t.Fatal("standalone Fable causal deadline inherited seven-day reset")
						}
					}
				}
			}
			if persisted == tc.paid {
				t.Fatal("standalone Fable causal deadline was not persisted independently")
			}
			fixture.usage(t)
			after, _ := fixture.manager.GetByID(fixture.auth.ID)
			if tc.paid {
				if state := after.ModelStates[overageTestModel]; state != nil && (state.Unavailable || state.Quota.Exceeded) {
					t.Fatal("qualified paid-overage capacity did not recover")
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatal("shared healthy usage cleared rejected standalone Fable pool")
			}
			if !t.Failed() {
				fmt.Println("OVERAGE_CASE_COMPLETED:" + tc.name)
			}
		})
	}
}
