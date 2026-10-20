package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtime "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	access "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type delegatedAccess struct{}

func (delegatedAccess) Identifier() string { return "synthetic" }
func (delegatedAccess) Authenticate(_ context.Context, r *http.Request) (*access.Result, *access.AuthError) {
	// Test-only authentication provider, all credentials synthetic.
	return &access.Result{Provider: "synthetic", Principal: r.Header.Get("X-Test-Principal")}, nil
}

type delegatedHTTPTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (d delegatedHTTPTransport) RoundTripperFor(*auth.Auth) http.RoundTripper { return d }
func (d delegatedHTTPTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "chatgpt.com" {
		return nil, fmt.Errorf("unexpected outbound target")
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	clone.URL = &u
	clone.URL.Host = d.target.Host
	clone.URL.Scheme = d.target.Scheme
	return d.base.RoundTrip(clone)
}

type delegatedHTTPFixture struct {
	engine      *gin.Engine
	cfg         *config.Config
	generations atomic.Int32
	compactions atomic.Int32
	probes      atomic.Int32
	clock       atomic.Int64
	allowed     atomic.Bool
	quota       atomic.Bool
	mode        atomic.Value
	cacheKey    atomic.Value
}

func newDelegatedHTTPFixture(t *testing.T) *delegatedHTTPFixture {
	t.Helper()
	f := &delegatedHTTPFixture{cfg: &config.Config{}}
	f.allowed.Store(true)
	f.clock.Store(time.Now().UnixNano())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Unix(0, f.clock.Load()).UTC().Format(http.TimeFormat))
		if r.Header.Get("Chatgpt-Account-Id") != "synthetic-account" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("wrong bound credentials")
		}
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			f.probes.Add(1)
			if r.Method != "GET" {
				t.Error("probe generated")
			}
			_, _ = fmt.Fprintf(w, `{"account_id":"synthetic-account","rate_limit":{"allowed":%t,"limit_reached":false,"primary_window":{"used_percent":12,"reset_at":%d,"limit_window_seconds":18000},"secondary_window":{"used_percent":12,"reset_at":%d,"limit_window_seconds":604800}}}`, f.allowed.Load(), time.Unix(0, f.clock.Load()).Add(time.Hour).Unix(), time.Unix(0, f.clock.Load()).Add(24*time.Hour).Unix())
		case "/backend-api/codex/responses/compact":
			f.compactions.Add(1)
			f.generations.Add(1)
			_, _ = fmt.Fprint(w, `{"id":"compact_synthetic","object":"response.compaction","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		case "/backend-api/codex/responses":
			f.generations.Add(1)
			if r.Method != "POST" {
				t.Error("wrong generation method")
			}
			body, _ := io.ReadAll(r.Body)
			var request struct {
				Model string `json:"model"`
				Cache string `json:"prompt_cache_key"`
			}
			_ = json.Unmarshal(body, &request)
			f.cacheKey.Store(request.Cache)
			if request.Model != "gpt-6.1-sol" {
				t.Errorf("wrong final model: %s", request.Model)
			}
			if mode, _ := f.mode.Load().(string); mode == "redirect" {
				w.Header().Set("Location", "https://chatgpt.com/backend-api/codex/redirected")
				w.WriteHeader(302)
				return
			}
			if f.quota.Load() {
				w.WriteHeader(429)
				_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached"}}`))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			mode, _ := f.mode.Load().(string)
			if mode == "sse-quota" || mode == "throttle" || mode == "capacity" || (mode == "tool-quota" && f.generations.Load() > 1) {
				code := "usage_limit_reached"
				if mode == "throttle" {
					code = "rate_limit_exceeded"
				}
				if mode == "capacity" {
					code = "model_at_capacity"
				}
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"synthetic\",\"status\":\"in_progress\",\"output\":[]}}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"type\":%q,\"code\":%q}}}\n\n", code, code)
				return
			}
			if mode == "tool-quota" {
				item := `{"type":"function_call","id":"fc_synthetic","call_id":"call_synthetic","name":"control","arguments":"{}"}`
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[%s]}}\n\n", item, item)
				return
			}
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[]}}\n\n")
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(upstream.Close)
	m := auth.NewManager(nil, nil, nil)
	m.RegisterExecutor(runtime.NewCodexAutoExecutor(f.cfg))
	m.SetRetryConfig(3, time.Second, 0)
	target, _ := url.Parse(upstream.URL)
	m.SetRoundTripperProvider(delegatedHTTPTransport{target, upstream.Client().Transport})
	id := t.Name()
	_, err := m.Register(context.Background(), &auth.Auth{ID: id, Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"type": "codex", "account_id": "synthetic-account", "access_token": "synthetic-token"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-6.1-sol"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	accessManager := access.NewManager()
	accessManager.SetProviders([]access.Provider{delegatedAccess{}})
	f.engine = gin.New()
	v1 := f.engine.Group("/v1")
	v1.Use(AuthMiddleware(accessManager))
	registerDelegatedLaunch(v1, auth.NewLaunchPoolWithClock(m, func() time.Time { return time.Unix(0, f.clock.Load()) }))
	return f
}
func (f *delegatedHTTPFixture) call(method, path, principal, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	r.Header.Set("X-Test-Principal", principal)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	return w
}
func (f *delegatedHTTPFixture) prepare(t *testing.T) auth.LaunchResult {
	t.Helper()
	w := f.call("POST", "/v1/delegated-launch/prepare", "client", `{"clientRequestId":"request","sessionId":"session","provider":"codex","model":"gpt-6.1-sol","blockedQuotaDomains":[]}`)
	var result auth.LaunchResult
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Status != "ready" {
		t.Fatalf("prepare: %d %s", w.Code, w.Body.String())
	}
	if f.generations.Load() != 0 {
		t.Fatal("prepare generated")
	}
	return result
}

func TestDelegatedLaunchHTTPRealExecutor(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			f := newDelegatedHTTPFixture(t)
			r := f.prepare(t)
			w := f.call("POST", r.BasePath+"/responses", "client", fmt.Sprintf(`{"model":"gpt-6.1-sol","input":"hello","stream":%t}`, stream))
			if w.Code != 200 || f.generations.Load() != 1 || f.probes.Load() != 2 || !strings.Contains(w.Body.String(), "synthetic") {
				t.Fatalf("dispatch: %d %s; generations=%d probes=%d", w.Code, w.Body.String(), f.generations.Load(), f.probes.Load())
			}
			if stream && !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
				t.Fatal("wrong stream type")
			}
		})
	}
}
func TestDelegatedLaunchHTTPDenied(t *testing.T) {
	for _, kind := range []string{"principal", "session", "model", "ordinary-false", "websocket", "unauthenticated", "payload-rule"} {
		t.Run(kind, func(t *testing.T) {
			f := newDelegatedHTTPFixture(t)
			r := f.prepare(t)
			method, path, principal, body := "POST", r.BasePath+"/responses", "client", `{"model":"gpt-6.1-sol","input":"hello"}`
			switch kind {
			case "principal":
				principal = "attacker"
			case "session":
				path = strings.Replace(path, "/sessions/session/", "/sessions/other/", 1)
			case "model":
				body = `{"model":"gpt-6-astra","input":"hello"}`
			case "ordinary-false":
				f.allowed.Store(false)
			case "websocket":
				method = "GET"
			case "unauthenticated":
				principal = ""
			case "session-body":
				body = `{"model":"gpt-6.1-sol","prompt_cache_key":"other"}`
			case "payload-rule":
				f.cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"model": "gpt-6-astra"}}}
			}
			w := f.call(method, path, principal, body)
			if w.Code < 400 || f.generations.Load() != 0 {
				t.Fatalf("denied request generated: %d %s count=%d", w.Code, w.Body.String(), f.generations.Load())
			}
			var typed struct {
				Error struct {
					Code string `json:"code"`
				}
			}
			if json.Unmarshal(w.Body.Bytes(), &typed) != nil || typed.Error.Code == "" {
				t.Fatalf("untyped refusal: %s", w.Body.String())
			}
		})
	}
}
func TestDelegatedLaunchHTTPQuotaRaceNoReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			f := newDelegatedHTTPFixture(t)
			r := f.prepare(t)
			f.quota.Store(true)
			w := f.call("POST", r.BasePath+"/responses", "client", fmt.Sprintf(`{"model":"gpt-6.1-sol","input":"hello","stream":%t}`, stream))
			var refusal struct{ Error auth.LaunchRefusal }
			_ = json.Unmarshal(w.Body.Bytes(), &refusal)
			if w.Code != 429 || refusal.Error.Code != "usage_limit" || refusal.Error.BindingID != r.BindingID || refusal.Error.QuotaScope != "account" || f.generations.Load() != 1 {
				t.Fatalf("race: %d %s count=%d", w.Code, w.Body.String(), f.generations.Load())
			}
		})
	}
}

func TestDelegatedLaunchCacheAffinityIsNotIdentity(t *testing.T) {
	for _, key := range []string{"", `,"prompt_cache_key":"native-generated-uuid"`, `,"prompt_cache_key":"custom-affinity"`} {
		f := newDelegatedHTTPFixture(t)
		r := f.prepare(t)
		w := f.call("POST", r.BasePath+"/responses", "client", `{"model":"gpt-6.1-sol"`+key+`}`)
		if w.Code != 200 || f.generations.Load() != 1 {
			t.Fatalf("cache key denied: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestDelegatedLaunchCompactPin(t *testing.T) {
	for _, kind := range []string{"healthy", "principal", "session", "model", "payload-rule"} {
		t.Run(kind, func(t *testing.T) {
			f := newDelegatedHTTPFixture(t)
			r := f.prepare(t)
			path, principal, body := r.BasePath+"/responses/compact", "client", `{"model":"gpt-6.1-sol","input":[]}`
			switch kind {
			case "principal":
				principal = "attacker"
			case "session":
				path = strings.Replace(path, "/sessions/session/", "/sessions/other/", 1)
			case "model":
				body = `{"model":"gpt-6-astra","input":[]}`
			case "payload-rule":
				f.cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"model": "gpt-6-astra"}}}
			}
			w := f.call("POST", path, principal, body)
			if kind == "healthy" {
				if w.Code != 200 || f.generations.Load() != 1 || !strings.Contains(w.Body.String(), "compact_synthetic") {
					t.Fatalf("compact=%d %s", w.Code, w.Body.String())
				}
			} else if w.Code < 400 || f.generations.Load() != 0 {
				t.Fatalf("denied compact ran: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestDelegatedLaunchControlIsolation(t *testing.T) {
	f := newDelegatedHTTPFixture(t)
	r := f.prepare(t)
	if w := f.call("GET", r.BasePath+"/state", "attacker", ""); !strings.Contains(w.Body.String(), `"state":"invalid"`) {
		t.Fatal(w.Body.String())
	}
	_ = f.call("POST", r.BasePath+"/release", "attacker", "")
	if w := f.call("GET", r.BasePath+"/state", "client", ""); !strings.Contains(w.Body.String(), `"state":"pending"`) {
		t.Fatal(w.Body.String())
	}
	for i := 0; i < 2; i++ {
		if w := f.call("POST", r.BasePath+"/release", "client", ""); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if w := f.call("POST", r.BasePath+"/responses", "client", `{"model":"gpt-6.1-sol"}`); w.Code < 400 || f.generations.Load() != 0 {
		t.Fatal("released binding ran")
	}
}

func TestDelegatedLaunchRefusalVersionControl(t *testing.T) {
	f := newDelegatedHTTPFixture(t)
	r := f.prepare(t)
	f.allowed.Store(false)
	inspect := func() auth.LaunchBindingState {
		t.Helper()
		w := f.call("GET", r.BasePath+"/state", "client", "")
		var state auth.LaunchBindingState
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil {
			t.Fatal(w.Body.String())
		}
		return state
	}
	refuse := func() auth.LaunchBindingState {
		t.Helper()
		if w := f.call("POST", r.BasePath+"/responses", "client", `{"model":"gpt-6.1-sol"}`); w.Code != 429 {
			t.Fatal(w.Code, w.Body.String())
		}
		return inspect()
	}
	first := refuse()
	for _, body := range []string{`{}`, `{"expectedExecutorAttempts":0}`, `{"expectedRefusalVersion":null}`, `{"expectedRefusalVersion":0}`, `{"expectedRefusalVersion":-1}`} {
		if w := f.call("POST", r.BasePath+"/resume", "client", body); w.Code != 400 {
			t.Fatal("missing/invalid version accepted", w.Code, body)
		}
	}
	ack := fmt.Sprintf(`{"expectedRefusalVersion":%d}`, first.RefusalVersion)
	if w := f.call("POST", r.BasePath+"/resume", "attacker", ack); w.Code != 409 {
		t.Fatal("wrong-principal resume accepted")
	}
	if current := refuse(); current.RefusalVersion != first.RefusalVersion {
		t.Fatal("existing latch acquired a new version")
	}
	if w := f.call("POST", r.BasePath+"/resume", "client", ack); w.Code != 200 {
		t.Fatal("fresh acknowledgement refused", w.Code)
	}
	second := refuse()
	if first.RefusalVersion == 0 || second.RefusalVersion <= first.RefusalVersion || second.ExecutorAttempts != 0 || f.generations.Load() != 0 {
		t.Fatal("probe refusals need distinct versions without execution", first, second)
	}
	if w := f.call("POST", r.BasePath+"/resume", "client", ack); w.Code != 409 || inspect().Refusal == nil {
		t.Fatal("stale acknowledgement cleared newer refusal")
	}
	ack = fmt.Sprintf(`{"expectedRefusalVersion":%d}`, second.RefusalVersion)
	if w := f.call("POST", r.BasePath+"/resume", "client", ack); w.Code != 200 {
		t.Fatal("fresh second acknowledgement refused")
	}
	f.allowed.Store(true)
	if w := f.call("POST", r.BasePath+"/responses", "client", `{"model":"gpt-6.1-sol"}`); w.Code != 200 || f.generations.Load() != 1 {
		t.Fatal("acknowledged same-pin continuation failed", w.Code, w.Body.String())
	}
}

func TestDelegatedLaunchExecutionRedirectRefused(t *testing.T) {
	f := newDelegatedHTTPFixture(t)
	r := f.prepare(t)
	f.mode.Store("redirect")
	w := f.call("POST", r.BasePath+"/responses", "client", `{"model":"gpt-6.1-sol"}`)
	if w.Code < 400 || f.generations.Load() != 1 {
		t.Fatalf("redirect followed: %d %s", w.Code, w.Body.String())
	}
}
