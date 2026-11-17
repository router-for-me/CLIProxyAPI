package api

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
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
		})
	}
}

type overageRouteFixture struct {
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
	f := &overageRouteFixture{manager: coreauth.NewManager(nil, nil, nil), cfg: cfg, usageBody: usage}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.anthropic.com" || r.Header.Get("Authorization") != "Bearer "+overageTestToken || r.Header.Get("X-Api-Key") != "" {
			t.Errorf("canonical OAuth identity changed: host=%s bearer=%q api-key=%q", r.Host, r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"))
		}
		switch r.URL.Path {
		case "/v1/messages":
			f.messages.Add(1)
			messages(w, r)
		case "/api/oauth/usage":
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
	registry.GetGlobalRegistry().RegisterClient(f.auth.ID, "claude", []*registry.ModelInfo{{ID: overageTestModel}, {ID: "claude-opus-5"}})
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
	_, err := f.manager.Execute(f.ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"` + model + `","max_tokens":16,"messages":[{"role":"user","content":"test"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err == nil || f.messages.Load() != before+1 {
		t.Fatalf("actual Claude wire rejection missing: calls=%d err=%v", f.messages.Load()-before, err)
	}
}
func (f *overageRouteFixture) usage(t *testing.T) {
	t.Helper()
	before := f.usageCalls.Load()
	body, err := json.Marshal(map[string]any{"authIndex": f.auth.EnsureIndex(), "method": "GET", "url": "https://api.anthropic.com/api/oauth/usage", "header": map[string]string{"Authorization": "Bearer $TOKEN$"}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v8/management/requests/api-call", strings.NewReader(string(body)))
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
