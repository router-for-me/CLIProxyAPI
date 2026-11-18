package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestManagementV8ExpiredSiblingDiagnostic(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, mode := range []string{"expired", "live", "unpropagated", "reason_only", "private_rate", "force"} {
			for _, restored := range []bool{false, true} {
				if mode == "reason_only" && restored {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/restored=%t", provider, mode, restored), func(t *testing.T) {
					ctx := context.Background()
					cfg := &config.Config{}
					store := coreauth.NewFileCooldownStateStore(t.TempDir())
					manager := coreauth.NewManager(nil, nil, nil)
					manager.SetConfig(cfg)
					manager.SetCooldownStateStore(store)
					input := &coreauth.Auth{ID: t.Name(), Provider: provider, Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "sibling-token", "account_id": "same"}}
					auth, err := manager.Register(ctx, input)
					if err != nil {
						t.Fatal(err)
					}
					sibling, reporter := "claude-sonnet-4-5", "claude-haiku-4-5"
					usagePath, usageURL, usageBody := "/api/oauth/usage", "https://api.anthropic.com/api/oauth/usage", `{"five_hour":{"utilization":0},"seven_day":{"utilization":1}}`
					if provider == "codex" {
						sibling = "gpt-5"
						reporter = "gpt-5-codex"
						usagePath = "/backend-api/wham/usage"
						usageURL = "https://chatgpt.com/backend-api/wham/usage"
						usageBody = `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":1}}}`
					}
					calls := 0
					upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						if r.URL.Path != usagePath || r.Header.Get("Authorization") != "Bearer sibling-token" {
							t.Error("canonical usage changed inspected identity")
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, usageBody)
					}))
					defer upstream.Close()
					previous := http.DefaultTransport
					transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
						return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
					}}
					http.DefaultTransport = transport
					defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
					if mode == "private_rate" || mode == "force" {
						e := &coreauth.Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`}
						if provider == "codex" {
							e.Message = `{"error":{"type":"rate_limit_exceeded","message":"request rate exceeded"}}`
						}
						if mode == "force" {
							e.Code = coreauth.ErrorCodeForceCooldown
						}
						d := 45 * time.Minute
						manager.MarkResult(ctx, coreauth.Result{AuthID: auth.ID, Provider: provider, Model: sibling, Error: e, RetryAfter: &d})
					}
					diagnostic := &coreauth.Error{HTTPStatus: 404, Message: `{"error":{"message":"sibling route not found"}}`}
					short := 45 * time.Second
					manager.MarkResult(ctx, coreauth.Result{AuthID: auth.ID, Provider: provider, Model: sibling, Error: diagnostic, RetryAfter: &short})
					records, err := store.Load(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if mode != "live" && mode != "unpropagated" {
						current, _ := manager.GetByID(auth.ID)
						raw, err := json.Marshal(current)
						if err != nil {
							t.Fatal(err)
						}
						public := &coreauth.Auth{}
						if err = json.Unmarshal(raw, public); err != nil {
							t.Fatal(err)
						}
						found := false
						for _, record := range records {
							if record.Model == sibling {
								age := 2 * time.Minute
								if mode == "private_rate" || mode == "force" {
									age = time.Hour
								}
								state := public.ModelStates[sibling]
								state.NextRetryAfter = record.NextRetryAfter.Add(-age)
								state.UpdatedAt = record.UpdatedAt.Add(-age)
								if !record.Quota.NextRecoverAt.IsZero() {
									state.Quota.NextRecoverAt = record.Quota.NextRecoverAt.Add(-age)
								}
								public.NextRetryAfter = state.NextRetryAfter
								found = true
							}
						}
						if !found {
							t.Fatal("actual sibling404 record missing")
						}
						if mode == "expired" || mode == "reason_only" {
							// Native404 has private non-quota provenance too. Age the real
							// persisted restriction, then restore a fresh public diagnostic
							// snapshot so Update cannot correctly retain a still-live marker.
							for i := range records {
								records[i].NextRetryAfter = records[i].NextRetryAfter.Add(-2 * time.Minute)
								records[i].UpdatedAt = records[i].UpdatedAt.Add(-2 * time.Minute)
								if !records[i].NonQuotaCooldownUntil.IsZero() {
									records[i].NonQuotaCooldownUntil = records[i].NonQuotaCooldownUntil.Add(-2 * time.Minute)
								}
							}
							if err = store.Save(ctx, records); err != nil {
								t.Fatal(err)
							}
							manager = coreauth.NewManager(nil, nil, nil)
							manager.SetConfig(cfg)
							if _, err = manager.Register(ctx, public); err != nil {
								t.Fatal(err)
							}
							manager.SetCooldownStateStore(store)
							if err = manager.RestoreCooldownStates(ctx); err != nil {
								t.Fatal(err)
							}
						} else if _, err = manager.Update(ctx, public); err != nil {
							t.Fatal(err)
						}
					}
					plan := &coreauth.Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_error","message":"Number of requests has exceeded your 5-hour rate limit."}}`}
					if provider == "codex" {
						plan.Message = `{"error":{"type":"usage_limit_reached","message":"plan limit"}}`
					}
					model := reporter
					wide := true
					if mode == "unpropagated" || mode == "reason_only" {
						model = ""
						wide = false
					}
					hour := time.Hour
					manager.MarkResult(ctx, coreauth.Result{AuthID: auth.ID, Provider: provider, Model: model, Error: plan, RetryAfter: &hour, CredentialScope: wide})
					if mode == "reason_only" {
						current, _ := manager.GetByID(auth.ID)
						state := current.ModelStates[sibling]
						state.Quota = coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(time.Hour)}
						state.NextRetryAfter = state.Quota.NextRecoverAt
						if _, err = manager.Update(ctx, current); err != nil {
							t.Fatal(err)
						}
					}
					if restored {
						manager = coreauth.NewManager(nil, nil, nil)
						manager.SetConfig(cfg)
						if _, err = manager.Register(ctx, input.Clone()); err != nil {
							t.Fatal(err)
						}
						manager.SetCooldownStateStore(store)
						if err = manager.RestoreCooldownStates(ctx); err != nil {
							t.Fatal(err)
						}
					}
					before, _ := manager.GetByID(auth.ID)
					state := before.ModelStates[sibling]
					if state == nil || state.LastError == nil || state.LastError.HTTPStatus != 404 || state.LastError.Message != diagnostic.Message {
						t.Fatal("propagation or restore erased owned sibling diagnostic")
					}
					wantRecovery := mode == "expired"
					if got := coreauth.QuotaRecoveryCandidate(before); got != wantRecovery {
						t.Errorf("candidate confused retained diagnostic with active propagated layer: got=%t want=%t", got, wantRecovery)
					}
					router := gin.New()
					router.POST("/v8/management/requests/api-call", management.NewHandler(cfg, "", manager).APICallV8)
					headers := map[string]string{"Authorization": "Bearer $TOKEN$"}
					if provider == "codex" {
						headers["Chatgpt-Account-Id"] = "same"
					}
					body, err := json.Marshal(map[string]any{"authIndex": before.EnsureIndex(), "method": "GET", "url": usageURL, "header": headers})
					if err != nil {
						t.Fatal(err)
					}
					req := httptest.NewRequest(http.MethodPost, "/v8/management/requests/api-call", strings.NewReader(string(body)))
					req.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					router.ServeHTTP(response, req)
					if response.Code != 200 || calls != 1 {
						t.Fatalf("canonical usage forwarding failed: code=%d calls=%d body=%s", response.Code, calls, response.Body.String())
					}
					after, _ := manager.GetByID(auth.ID)
					if wantRecovery {
						if after.Unavailable || after.Quota.Exceeded {
							t.Fatal("expired sibling diagnostic prevented canonical subscription recovery")
						}
						for _, s := range after.ModelStates {
							if s != nil && (s.Unavailable || s.Quota.Exceeded) {
								t.Fatal("expired sibling diagnostic kept propagated model blocked")
							}
						}
					} else if !reflect.DeepEqual(before, after) {
						t.Fatal("canonical subscription usage erased owned or unproven model restriction")
					}
				})
			}
		}
	}
}
