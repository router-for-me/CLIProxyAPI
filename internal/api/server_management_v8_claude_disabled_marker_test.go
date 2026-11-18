package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestManagementV8ClaudeDisabledMarkerClear(t *testing.T) {
	for _, pool := range []string{"paid", "opaque"} {
		for _, phase := range []string{"toggle", "toggle_restore", "json_toggle", "enabled_update", "auth_toggle", "auth_toggle_restore", "auth_force", "auth_models_sdk_toggle", "auth_models_sdk_toggle_restore", "auth_models_sdk_force", "auth_models_sdk_enabled_update"} {
			name := pool + "_" + phase
			t.Run(name, func(t *testing.T) {
				if child := os.Getenv("CPA_OVERAGE_CHILD_CASE"); child == "" {
					runIsolatedOverageCase(t, name)
					return
				} else if child != name {
					t.Fatal("OVERAGE_CHILD_INFRASTRUCTURE: wrong disabled marker case")
				}
				var ordinary atomic.Bool
				f := newOverageRouteFixture(t, `{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"extra_usage":{"is_enabled":false}}`, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
					w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
					w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
					w.Header().Set("Retry-After", "120")
					if !ordinary.Load() {
						if pool == "paid" {
							w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
							w.Header().Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day_overage_included")
						} else {
							w.Header().Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
						}
					}
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Subscription capacity rejected."}}`)
				})
				authScope := strings.HasPrefix(phase, "auth_")
				force := strings.HasSuffix(phase, "_force")
				withModels := strings.HasPrefix(phase, "auth_models_")
				f.cfg.Claude.ModelLevelCooling = !authScope
				f.manager.SetConfig(f.cfg)
				tokenDir := t.TempDir()
				store := sdkauth.NewFileTokenStore()
				store.SetBaseDir(tokenDir)
				f.manager.SetStore(store)
				a, _ := f.manager.GetByID(f.auth.ID)
				a.FileName = "credential.json"
				a.Metadata["type"] = "claude"
				if _, err := f.manager.Update(f.ctx, a); err != nil {
					t.Fatal(err)
				}
				if withModels {
					f.cfg.Claude.ModelLevelCooling = true
					f.manager.SetConfig(f.cfg)
					f.fail(t, overageTestModel)
					f.cfg.Claude.ModelLevelCooling = false
					f.manager.SetConfig(f.cfg)
					ctx := logging.WithResponseHeadersHolder(f.ctx)
					selected, _ := f.manager.GetByID(f.auth.ID)
					calls := f.messages.Load()
					_, wireErr := runtimeexecutor.NewClaudeExecutor(f.cfg).Execute(ctx, selected, coreexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"test"}]}`)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
					status, ok := wireErr.(interface{ StatusCode() int })
					if !ok || status.StatusCode() != 429 || f.messages.Load() != calls+1 {
						t.Fatal("actual SDK auth-scope rejection missing")
					}
					result := coreauth.Result{AuthID: f.auth.ID, Provider: "claude", Error: &coreauth.Error{HTTPStatus: status.StatusCode(), Message: wireErr.Error()}}
					if retry, ok := wireErr.(interface{ RetryAfter() *time.Duration }); ok {
						result.RetryAfter = retry.RetryAfter()
					}
					f.manager.MarkResult(ctx, result)
					records, loadErr := f.store.Load(f.ctx)
					if loadErr != nil {
						t.Fatal(loadErr)
					}
					authMarker := false
					for _, record := range records {
						if record.Model == "" {
							if pool == "paid" {
								authMarker = record.ClaudeOverageCooldownUntil.After(time.Now())
							} else {
								authMarker = record.ClaudeFablePoolCooldownUntil.After(time.Now())
							}
						}
					}
					if !authMarker {
						t.Fatal("SDK report did not persist an auth-owned pool marker")
					}

				} else {
					f.fail(t, overageTestModel)
				}
				before, _ := f.manager.GetByID(f.auth.ID)
				if withModels {
					if !before.Quota.Exceeded || !before.Unavailable {
						t.Fatal("SDK auth report did not retain its auth-level quota restriction")
					}
				} else if (before.Quota.Reason == "credential_quota") != authScope {
					t.Fatal("actual rejected scope did not match cooling configuration")
				}
				if withModels && len(before.ModelStates) == 0 {
					t.Fatal("auth boundary did not retain actual prior model state")
				}

				if coreauth.QuotaRecoveryRequiresClaudeExtraUsage(before) != (pool == "paid") || coreauth.QuotaRecoveryRequiresClaudeFablePool(before) != (pool == "opaque") {
					t.Fatal("actual HTTP failure did not establish expected causal pool")
				}
				var forceDeadline time.Time
				if force {
					retry := 10 * time.Minute
					f.manager.MarkResult(f.ctx, coreauth.Result{AuthID: f.auth.ID, Provider: "claude", Error: &coreauth.Error{HTTPStatus: 429, Code: coreauth.ErrorCodeForceCooldown, Message: "operator force cooldown"}, RetryAfter: &retry})
					before, _ = f.manager.GetByID(f.auth.ID)
					forceDeadline = before.NextRetryAfter
					if !forceDeadline.After(time.Now().Add(9 * time.Minute)) {
						t.Fatal("supported force result did not establish active force boundary")
					}
				}
				updated := before.Clone()
				if phase == "json_toggle" {
					raw, err := json.Marshal(updated)
					if err != nil {
						t.Fatal(err)
					}
					updated = &coreauth.Auth{}
					if err = json.Unmarshal(raw, updated); err != nil {
						t.Fatal(err)
					}
				}
				toggle := !strings.HasSuffix(phase, "enabled_update")
				updated.Metadata["disable_cooling"] = toggle
				if _, err := f.manager.Update(f.ctx, updated); err != nil {
					t.Fatal(err)
				}
				saved, err := os.ReadFile(filepath.Join(tokenDir, "credential.json"))
				if err != nil {
					t.Fatal(err)
				}
				var metadata map[string]any
				if err = json.Unmarshal(saved, &metadata); err != nil {
					t.Fatal(err)
				}
				if metadata["disable_cooling"] != toggle || metadata["access_token"] != overageTestToken {
					t.Fatal("public Update did not persist same-account cooling policy")
				}
				afterUpdate, _ := f.manager.GetByID(f.auth.ID)
				paid, opaque := coreauth.QuotaRecoveryRequiresClaudeExtraUsage(afterUpdate), coreauth.QuotaRecoveryRequiresClaudeFablePool(afterUpdate)
				if force {
					if afterUpdate.LastError == nil || afterUpdate.LastError.Code != coreauth.ErrorCodeForceCooldown || !afterUpdate.Unavailable || !afterUpdate.NextRetryAfter.Equal(forceDeadline) {
						t.Fatal("cooling-disabled save lost active operator force cooldown")
					}
				} else if toggle {
					if paid || opaque {
						t.Error("post-save merge resurrected intentionally cleared Claude pool marker")
					}
					afterUpdate.Metadata["disable_cooling"] = false
					if _, err = f.manager.Update(f.ctx, afterUpdate); err != nil {
						t.Fatal(err)
					}
					ordinary.Store(true)
					f.fail(t, overageTestModel)
					if strings.HasSuffix(phase, "_restore") {
						f.restore(t)
						f.auth, _ = f.manager.GetByID(f.auth.ID)
					}
					actual, _ := f.manager.GetByID(f.auth.ID)
					if coreauth.QuotaRecoveryRequiresClaudeExtraUsage(actual) || coreauth.QuotaRecoveryRequiresClaudeFablePool(actual) {
						t.Error("ordinary subscription failure inherited disabled-period Claude pool marker")
					}
				} else if paid != (pool == "paid") || opaque != (pool == "opaque") {
					t.Error("enabled same-account save lost legitimate Claude pool marker")
				}
				beforeUsage, _ := f.manager.GetByID(f.auth.ID)
				f.usage(t)
				after, _ := f.manager.GetByID(f.auth.ID)
				if force {
					if !reflect.DeepEqual(beforeUsage, after) {
						t.Fatal("healthy shared usage erased active force cooldown after disabled save")
					}
				} else if toggle {
					blocked := after.Unavailable || after.Quota.Exceeded
					if state := after.ModelStates[overageTestModel]; state != nil {
						blocked = blocked || state.Unavailable || state.Quota.Exceeded
					}
					if blocked {
						t.Fatal("healthy shared usage stayed blocked by intentionally cleared Claude pool marker")
					}
				} else if !reflect.DeepEqual(beforeUsage, after) {
					t.Fatal("healthy shared usage erased legitimate enabled-cooling pool restriction")
				}
				if !t.Failed() {
					fmt.Println("OVERAGE_CASE_COMPLETED:" + name)
				}
			})
		}
	}
}
