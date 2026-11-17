package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const overageTestModel = "claude-fable-5-1"
const overageTestToken = "sk-ant-oat01-overage-regression"

// Every cooldown in this fixture originates in the real Claude executor's wire
// classifier. The canonical host and OAuth bearer survive the local TLS dial.
func TestManagementV8ClaudeOverageRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, extra      string
		overage, recover bool
	}{
		{name: "overage_exhausted", overage: true, extra: `{"is_enabled":true,"utilization":100,"monthly_limit":1000,"used_credits":1000}`},
		{name: "overage_disabled", overage: true, extra: `{"is_enabled":false,"utilization":0,"monthly_limit":1000,"used_credits":0}`},
		{name: "overage_missing", overage: true},
		{name: "overage_restored", overage: true, extra: `{"is_enabled":true,"utilization":10,"monthly_limit":1000,"used_credits":100}`, recover: true},
		{name: "subscription_disabled_extra", extra: `{"is_enabled":false}`, recover: true},
		{name: "subscription_exhausted_extra", extra: `{"is_enabled":true,"utilization":100,"monthly_limit":1000,"used_credits":1000}`, recover: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if child := os.Getenv("CPA_OVERAGE_CHILD_CASE"); child == "" {
				runIsolatedOverageCase(t, tc.name)
				return
			} else if child != tc.name {
				t.Fatalf("OVERAGE_CHILD_INFRASTRUCTURE: selected %s, executing %s", child, tc.name)
			}
			usage := `{"five_hour":{"utilization":10},"seven_day":{"utilization":20}`
			if tc.extra != "" {
				usage += `,"extra_usage":` + tc.extra
			}
			usage += `}`
			fixture := newOverageRouteFixture(t, usage, func(w http.ResponseWriter, r *http.Request) {
				writeOverageTestRejection(w, tc.overage)
			})
			fixture.fail(t, overageTestModel)
			before, _ := fixture.manager.GetByID(fixture.auth.ID)
			state := before.ModelStates[overageTestModel]
			if state == nil || !state.Quota.Exceeded || !state.Unavailable || state.LastError == nil || state.LastError.HTTPStatus != 429 {
				t.Fatalf("real executor did not establish model quota: %+v", before)
			}
			if tc.overage && before.Quota.Reason == "credential_quota" {
				t.Fatal("overage became credential-scoped")
			}
			if !coreauth.QuotaRecoveryCandidate(before) {
				t.Fatal("wire quota did not reach recovery candidate")
			}
			fixture.usage(t)
			after, _ := fixture.manager.GetByID(fixture.auth.ID)
			afterState := after.ModelStates[overageTestModel]
			if tc.recover {
				if afterState != nil && (afterState.Unavailable || afterState.Quota.Exceeded || afterState.LastError != nil) {
					t.Fatalf("proven capacity did not recover model: %+v", afterState)
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatalf("unproven overage capacity changed cooldown: before=%+v after=%+v", state, afterState)
			}
			if !t.Failed() {
				fmt.Println("OVERAGE_CASE_COMPLETED:" + tc.name)
			}
		})
	}
}

type overageRouteFixture struct {
	store     *coreauth.FileCooldownStateStore
	cfgPath   string
	kind      string
	bearer    atomic.Value
	mu        sync.RWMutex
	usageHook func()

	manager              *coreauth.Manager
	auth                 *coreauth.Auth
	cfg                  *config.Config
	ctx                  context.Context
	server               *Server
	usageBody            string
	messages, usageCalls atomic.Int32
}

func newOverageRouteFixture(t *testing.T, usage string, messages http.HandlerFunc) *overageRouteFixture {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("port: 8317\nremote-management: {secret-key: test-password}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	f := &overageRouteFixture{manager: coreauth.NewManager(nil, nil, nil), cfg: cfg, usageBody: usage, cfgPath: cfgPath}
	f.bearer.Store(overageTestToken)
	f.store = coreauth.NewFileCooldownStateStore(t.TempDir())
	f.manager.SetCooldownStateStore(f.store)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.anthropic.com" || r.Header.Get("Authorization") != "Bearer "+f.bearer.Load().(string) || r.Header.Get("X-Api-Key") != "" {
			t.Errorf("canonical OAuth identity changed: host=%s bearer=%q api-key=%q", r.Host, r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"))
		}
		switch r.URL.Path {
		case "/v1/messages", "/v1/messages/count_tokens":
			f.messages.Add(1)
			messages(w, r)
		case "/api/oauth/usage":
			f.mu.RLock()
			hook := f.usageHook
			f.mu.RUnlock()
			if hook != nil {
				hook()
			}
			f.usageCalls.Add(1)
			w.Header().Set("X-Usage-Evidence", "unchanged")
			_, _ = io.WriteString(w, f.usageBody)
		default:
			t.Errorf("unexpected canonical upstream path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(upstream.Close)
	previous := http.DefaultTransport
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous; transport.CloseIdleConnections() })
	f.ctx = context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	f.manager.SetConfig(cfg)
	f.manager.SetRetryConfig(0, 0, 0)
	f.manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
	f.auth, err = f.manager.Register(f.ctx, &coreauth.Auth{ID: t.Name(), Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": overageTestToken, "refresh_token": "overage-refresh", "account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "organization_uuid": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(f.auth.ID, "claude", []*registry.ModelInfo{{ID: overageTestModel}, {ID: "claude-fable-5-2"}, {ID: "claude-opus-5"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(f.auth.ID) })
	h := management.NewHandler(cfg, cfgPath, f.manager)
	h.SetLocalPassword("test-password")
	f.server = &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	f.server.managementRoutesEnabled.Store(true)
	f.server.registerManagementRoutes()
	return f
}
func (f *overageRouteFixture) fail(t *testing.T, model string) {
	t.Helper()
	before := f.messages.Load()
	err := f.execute(model)
	var status interface{ StatusCode() int }
	if err == nil || !errors.As(err, &status) || status.StatusCode() != 429 || f.messages.Load() != before+1 {
		t.Fatalf("OVERAGE_CHILD_INFRASTRUCTURE: actual local Claude429 missing: calls=%d err=%v", f.messages.Load()-before, err)
	}
}
func (f *overageRouteFixture) usage(t *testing.T) {
	t.Helper()
	before := f.usageCalls.Load()
	body, err := json.Marshal(map[string]any{"authIndex": f.auth.EnsureIndex(), "method": "GET", "url": "https://api.anthropic.com/api/oauth/usage", "header": map[string]string{"Authorization": "Bearer $TOKEN$"}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v8/management/requests/api-call", strings.NewReader(string(body))).WithContext(f.ctx)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer test-password")
	response := httptest.NewRecorder()
	f.server.engine.ServeHTTP(response, request)
	if response.Code != 200 || f.usageCalls.Load() != before+1 {
		t.Fatalf("authenticated canonical usage did not forward: status=%d calls=%d body=%s", response.Code, f.usageCalls.Load()-before, response.Body.String())
	}
	var payload struct {
		Status int         `json:"status_code"`
		Header http.Header `json:"header"`
		Body   string      `json:"body"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != 200 || payload.Body != f.usageBody || payload.Header.Get("X-Usage-Evidence") != "unchanged" {
		t.Fatalf("provider usage payload changed: %+v", payload)
	}
}
func writeOverageTestRejection(w http.ResponseWriter, overage bool) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
	if overage {
		w.Header().Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day_overage_included")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.69")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.00")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d_oi-Utilization", "1.02")
		w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
		w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Disabled-Reason", "org_spend_cap_reached")
		w.Header().Set("Retry-After", "121180")
	} else {
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
		w.Header().Set("Retry-After", "120")
	}
	w.WriteHeader(429)
	_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Fable usage window rejected."}}`)
}

// A child process owns its complete HTTP environment. The shared parent never
// changes a package-global transport and accepts only an actual completed case.
func runIsolatedOverageCase(t *testing.T, name string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("OVERAGE_CHILD_INFRASTRUCTURE: executable: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^"+strings.Split(t.Name(), "/")[0]+"$/^"+name+"$", "-test.v")
	cmd.Env = append(os.Environ(), "CPA_OVERAGE_CHILD_CASE="+name, "GOMAXPROCS=1")
	cmd.WaitDelay = 5 * time.Second
	output := &overageChildOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	err = cmd.Run()
	if ctx.Err() != nil || output.overflow {
		t.Fatalf("OVERAGE_CHILD_INFRASTRUCTURE: timeout=%v output_overflow=%v\n%s", ctx.Err(), output.overflow, output.String())
	}
	if err != nil {
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Fatalf("OVERAGE_CHILD_INFRASTRUCTURE: exit=%v\n%s", err, output.String())
		}
		t.Fatalf("isolated OAuth assertion failed: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "OVERAGE_CASE_COMPLETED:"+name+"\n") {
		t.Fatalf("OVERAGE_CHILD_INFRASTRUCTURE: missing actual-case completion\n%s", output.String())
	}
	t.Log(output.String())
}

type overageChildOutput struct {
	bytes.Buffer
	overflow bool
}

func (o *overageChildOutput) Write(p []byte) (int, error) {
	remaining := 1024*1024 - o.Len()
	n := len(p)
	if n > remaining {
		o.overflow = true
		p = p[:remaining]
	}
	_, _ = o.Buffer.Write(p)
	return n, nil
}

func TestManagementV8ClaudeOverageAttribution(t *testing.T) {
	for _, phase := range []string{"sibling", "update", "provider_case", "refresh", "rotation", "replacement", "replacement_restore", "restore", "later_longer", "earlier_longer", "later_longer_restore", "shadow_restore", "credential_first", "late_old_account", "late_old_account_count", "late_old_account_stream", "late_same_account", "count", "stream", "manual", "manual_auth", "manual_auth_restore", "alias_source_later", "alias_target_later", "new_failure_during_usage"} {
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
				writeOverageTestRejection(w, true)
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
						firstDeadline = fixture.overageDeadline(t, false)
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
						latest = record.ClaudeOverageCooldownUntil
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
						actual = record.ClaudeOverageCooldownUntil
					}
				}
				if latest.IsZero() || !actual.Equal(latest) {
					t.Fatal("alias merge lost maximum overage causal deadline")
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
			var providerCaseDeadline time.Time
			if phase == "provider_case" {
				providerCaseDeadline = fixture.overageDeadline(t, false)
				if providerCaseDeadline.IsZero() {
					t.Fatal("provider case fixture has no persisted causal deadline")
				}
			}
			if phase == "provider_case" || phase == "update" || phase == "refresh" || phase == "rotation" || phase == "replacement" || phase == "replacement_restore" {
				current, _ := fixture.manager.GetByID(fixture.auth.ID)
				updated := current.Clone()
				if phase == "provider_case" || phase == "update" || phase == "refresh" || phase == "rotation" {
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
				if phase == "provider_case" {
					updated.Provider = "ClAuDe"
				}
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
			if phase == "provider_case" {
				current, _ := fixture.manager.GetByID(fixture.auth.ID)
				if current.Provider != "ClAuDe" {
					t.Fatal("public Update did not commit provider case edit")
				}
				if !fixture.overageDeadline(t, false).Equal(providerCaseDeadline) {
					t.Error("provider case update lost persisted pool deadline")
				}
				lowercase := current.Clone()
				lowercase.Provider = "claude"
				if _, err := fixture.manager.Update(fixture.ctx, lowercase); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(phase, "longer") {
				deadline := fixture.overageDeadline(t, false)
				state, _ := fixture.manager.GetByID(fixture.auth.ID)
				if deadline.IsZero() || !deadline.Before(state.ModelStates[overageTestModel].Quota.NextRecoverAt.Add(-30*time.Minute)) {
					t.Fatal("unrelated long quota inflated overage own deadline")
				}
				if !firstDeadline.IsZero() && !deadline.Equal(firstDeadline) {
					t.Fatal("later ordinary quota replaced overage causal deadline")
				}
			}
			var independentDeadline time.Time
			if phase == "shadow_restore" || phase == "credential_first" {
				independentDeadline = fixture.overageDeadline(t, true)
				if independentDeadline.IsZero() {
					t.Fatal("credential shadow lost overage causal deadline")
				}
				current, _ := fixture.manager.GetByID(fixture.auth.ID)
				if !independentDeadline.Before(current.Quota.NextRecoverAt.Add(-30 * time.Minute)) {
					t.Fatal("outer credential quota inflated independent overage deadline")
				}
			}
			if phase == "restore" || phase == "later_longer_restore" || phase == "shadow_restore" || phase == "replacement_restore" || phase == "manual_auth_restore" || phase == "credential_first" {
				fixture.restore(t)
			}
			if !independentDeadline.IsZero() && !fixture.overageDeadline(t, true).Equal(independentDeadline) {
				t.Fatal("restored independent cooldown lost overage causal deadline")
			}
			beforeUsage, _ := fixture.manager.GetByID(fixture.auth.ID)
			if phase == "provider_case" {
				if beforeUsage.Provider != "claude" {
					t.Fatal("public Update did not restore lowercase provider")
				}
				if !fixture.overageDeadline(t, false).Equal(providerCaseDeadline) {
					t.Error("provider case round trip lost persisted pool deadline")
				}
				fixture.usage(t)
				afterCaseUsage, _ := fixture.manager.GetByID(fixture.auth.ID)
				if !reflect.DeepEqual(beforeUsage, afterCaseUsage) {
					t.Error("provider case round trip let shared usage clear retained pool cooldown")
				}
			}
			recover := phase == "replacement" || phase == "replacement_restore" || strings.HasPrefix(phase, "late_old_account")
			if coreauth.QuotaRecoveryRequiresClaudeExtraUsage(beforeUsage) == recover {
				t.Fatalf("overage attribution crossed configuration or was lost: phase=%s", phase)
			}
			fixture.usage(t)
			after, _ := fixture.manager.GetByID(fixture.auth.ID)
			if recover {
				state := after.ModelStates[overageTestModel]
				if state != nil && (state.Unavailable || state.Quota.Exceeded) {
					t.Fatal("replacement account ordinary cooldown did not recover")
				}
			} else if !reflect.DeepEqual(beforeUsage, after) {
				t.Fatal("unproven overage capacity changed retained cooldown")
			}
			if !t.Failed() {
				fmt.Println("OVERAGE_CASE_COMPLETED:" + phase)
			}
		})
	}
}

func (f *overageRouteFixture) execute(model string) error {
	request := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"` + model + `","max_tokens":16,"messages":[{"role":"user","content":"test"}]}`)}
	options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	switch f.kind {
	case "count":
		_, err := f.manager.ExecuteCount(f.ctx, []string{"claude"}, request, options)
		return err
	case "stream":
		_, err := f.manager.ExecuteStream(f.ctx, []string{"claude"}, request, options)
		return err
	default:
		_, err := f.manager.Execute(f.ctx, []string{"claude"}, request, options)
		return err
	}
}
func (f *overageRouteFixture) overageDeadline(t *testing.T, independent bool) time.Time {
	t.Helper()
	records, err := f.store.Load(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Model == overageTestModel {
			if independent {
				return record.IndependentClaudeOverageCooldownUntil
			}
			return record.ClaudeOverageCooldownUntil
		}
	}
	t.Fatal("actual cooldown record missing")
	return time.Time{}
}
func (f *overageRouteFixture) restore(t *testing.T) {
	t.Helper()
	current, _ := f.manager.GetByID(f.auth.ID)
	fresh := coreauth.NewManager(nil, nil, nil)
	fresh.SetConfig(f.cfg)
	fresh.SetRetryConfig(0, 0, 0)
	fresh.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(f.cfg))
	_, err := fresh.Register(f.ctx, &coreauth.Auth{ID: current.ID, Provider: current.Provider, Status: coreauth.StatusActive, Metadata: current.Metadata, Attributes: current.Attributes})
	if err != nil {
		t.Fatal(err)
	}
	fresh.SetCooldownStateStore(f.store)
	if err = fresh.RestoreCooldownStates(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.manager = fresh
	handler := management.NewHandler(f.cfg, f.cfgPath, f.manager)
	handler.SetLocalPassword("test-password")
	f.server = &Server{cfg: f.cfg, engine: gin.New(), mgmt: handler}
	f.server.managementRoutesEnabled.Store(true)
	f.server.registerManagementRoutes()
}

func (f *overageRouteFixture) setUsageHook(hook func()) {
	f.mu.Lock()
	f.usageHook = hook
	f.mu.Unlock()
}

func TestManagementV8ClaudeOverageLegacyRestore(t *testing.T) {
	t.Run("old_header_record", func(t *testing.T) {
		if child := os.Getenv("CPA_OVERAGE_CHILD_CASE"); child == "" {
			runIsolatedOverageCase(t, "old_header_record")
			return
		} else if child != "old_header_record" {
			t.Fatal("OVERAGE_CHILD_INFRASTRUCTURE: wrong legacy case")
		}
		f := newOverageRouteFixture(t, `{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"extra_usage":{"is_enabled":false}}`, func(w http.ResponseWriter, r *http.Request) { writeOverageTestRejection(w, true) })
		f.fail(t, overageTestModel)
		records, err := f.store.Load(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		// Downgrade only the newly added fields on a real persisted wire rejection.
		// The existing bounded rejected headers are exactly the old record evidence.
		for i := range records {
			records[i].ClaudeOverageEvaluated = false
			records[i].ClaudeOverageCooldownUntil = time.Time{}
			records[i].IndependentClaudeOverageCooldownUntil = time.Time{}
		}
		if err = f.store.Save(f.ctx, records); err != nil {
			t.Fatal(err)
		}
		f.restore(t)
		before, _ := f.manager.GetByID(f.auth.ID)
		if !coreauth.QuotaRecoveryRequiresClaudeExtraUsage(before) {
			t.Fatal("legacy rejected overage headers lost attribution during restore")
		}
		f.usage(t)
		after, _ := f.manager.GetByID(f.auth.ID)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("unproven overage capacity changed legacy cooldown")
		}
		if !t.Failed() {
			fmt.Println("OVERAGE_CASE_COMPLETED:old_header_record")
		}
	})
}
