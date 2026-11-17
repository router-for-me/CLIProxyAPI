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

func TestManagementV8HintlessIndependentDeadline(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, scope := range []string{"model", "auth"} {
			for _, expired := range []bool{false, true} {
				for _, prior := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/expired=%t/prior=%t", provider, scope, expired, prior), func(t *testing.T) {
						ctx := context.Background()
						cfg := &config.Config{}
						cfg.Claude.ModelLevelCooling = true
						cfg.Codex.ModelLevelCooling = true
						store := coreauth.NewFileCooldownStateStore(t.TempDir())
						manager := coreauth.NewManager(nil, nil, nil)
						manager.SetConfig(cfg)
						manager.SetCooldownStateStore(store)
						input := &coreauth.Auth{ID: t.Name(), Provider: provider, Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "hintless-token"}}
						auth, err := manager.Register(ctx, input)
						if err != nil {
							t.Fatal(err)
						}
						model := ""
						if scope == "model" {
							model = "claude-sonnet-4-5"
							if provider == "codex" {
								model = "gpt-5"
							}
						}
						usagePath, usageURL, usageBody := "/api/oauth/usage", "https://api.anthropic.com/api/oauth/usage", `{"five_hour":{"utilization":0},"seven_day":{"utilization":1}}`
						if provider == "codex" {
							usagePath = "/backend-api/wham/usage"
							usageURL = "https://chatgpt.com/backend-api/wham/usage"
							usageBody = `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":1}}}`
						}
						calls := 0
						upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							calls++
							if r.URL.Path != usagePath || r.Header.Get("Authorization") != "Bearer hintless-token" {
								t.Error("canonical usage did not inspect selected identity")
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
						router := gin.New()
						router.POST("/v8/management/requests/api-call", management.NewHandler(cfg, "", manager).APICallV8)
						usage := func() {
							current, _ := manager.GetByID(auth.ID)
							body, err := json.Marshal(map[string]any{"authIndex": current.EnsureIndex(), "method": "GET", "url": usageURL, "header": map[string]string{"Authorization": "Bearer $TOKEN$"}})
							if err != nil {
								t.Fatal(err)
							}
							req := httptest.NewRequest(http.MethodPost, "/v8/management/requests/api-call", strings.NewReader(string(body)))
							req.Header.Set("Content-Type", "application/json")
							response := httptest.NewRecorder()
							beforeCalls := calls
							router.ServeHTTP(response, req)
							if response.Code != 200 || calls != beforeCalls+1 {
								t.Fatalf("canonical usage forwarding failed: status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
							}
						}
						independentError := &coreauth.Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`}
						planError := &coreauth.Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_error","message":"Number of requests has exceeded your 5-hour rate limit."}}`}
						if provider == "codex" {
							independentError.Message = `{"error":{"type":"rate_limit_exceeded","message":"request rate exceeded"}}`
							planError.Message = `{"error":{"type":"usage_limit_reached","message":"plan limit"}}`
						}
						mark := func(err *coreauth.Error, hint *time.Duration) {
							manager.MarkResult(ctx, coreauth.Result{AuthID: auth.ID, Provider: provider, Model: model, Error: err, RetryAfter: hint})
						}
						own := func(record coreauth.CooldownStateRecord) time.Time { return record.NonQuotaCooldownUntil }
						setOwn := func(record *coreauth.CooldownStateRecord, value time.Time) { record.NonQuotaCooldownUntil = value }
						var original time.Time
						if prior {
							d := 45 * time.Second
							mark(independentError, &d)
							records, err := store.Load(ctx)
							if err != nil {
								t.Fatal(err)
							}
							for _, r := range records {
								if r.Model == model {
									original = own(r)
								}
							}
							if original.IsZero() {
								t.Fatal("prior independent deadline was not persisted")
							}
							if original.Before(time.Now().Add(40*time.Second)) || original.After(time.Now().Add(46*time.Second)) {
								t.Fatal("explicit independent hint did not keep its own 45 second deadline")
							}
						}
						hour := time.Hour
						mark(planError, &hour)
						began := time.Now()
						mark(independentError, nil)
						records, err := store.Load(ctx)
						if err != nil {
							t.Fatal(err)
						}
						var deadline time.Time
						for _, r := range records {
							if r.Model == model {
								deadline = own(r)
							}
						}
						if deadline.IsZero() {
							t.Fatal("hintless independent deadline was not persisted")
						}
						if prior {
							if !deadline.Equal(original) {
								t.Error("hintless fallback inflated or shortened prior independent maximum")
							}
						} else if deadline.Before(began) || deadline.After(time.Now().Add(5*time.Second)) {
							t.Error("hintless independent deadline borrowed the long plan window")
						}
						mark(planError, &hour)
						records, err = store.Load(ctx)
						if err != nil {
							t.Fatal(err)
						}
						for i := range records {
							if records[i].Model == model {
								if !own(records[i]).Equal(deadline) {
									t.Error("later plan diagnostic changed independent deadline")
								}
								if expired {
									setOwn(&records[i], own(records[i]).Add(-2*time.Minute))
								}
							}
						}
						if err = store.Save(ctx, records); err != nil {
							t.Fatal(err)
						}
						manager = coreauth.NewManager(nil, nil, nil)
						manager.SetConfig(cfg)
						if _, err = manager.Register(ctx, input.Clone()); err != nil {
							t.Fatal(err)
						}
						manager.SetCooldownStateStore(store)
						if err = manager.RestoreCooldownStates(ctx); err != nil {
							t.Fatal(err)
						}
						router = gin.New()
						router.POST("/v8/management/requests/api-call", management.NewHandler(cfg, "", manager).APICallV8)
						before, _ := manager.GetByID(auth.ID)
						if before.LastError == nil || before.LastError.Message != planError.Message {
							t.Fatal("later plan diagnostic was not restored")
						}
						usage()
						after, _ := manager.GetByID(auth.ID)
						if expired {
							blocked := after.Unavailable || after.Quota.Exceeded
							if s := after.ModelStates[model]; model != "" && s != nil {
								blocked = blocked || s.Unavailable || s.Quota.Exceeded
							}
							if blocked {
								t.Fatal("expired hintless restriction kept healthy plan usage blocked after restore")
							}
						} else if !reflect.DeepEqual(before, after) {
							t.Fatal("healthy plan usage erased live hintless restriction")
						}
					})
				}
			}
		}
	}
}
