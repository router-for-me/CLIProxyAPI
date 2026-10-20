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
	probes      atomic.Int32
	allowed     atomic.Bool
	quota       atomic.Bool
}

func newDelegatedHTTPFixture(t *testing.T) *delegatedHTTPFixture {
	t.Helper()
	f := &delegatedHTTPFixture{cfg: &config.Config{}}
	f.allowed.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Chatgpt-Account-Id") != "synthetic-account" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("wrong bound credentials")
		}
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			f.probes.Add(1)
			if r.Method != "GET" {
				t.Error("probe generated")
			}
			_, _ = fmt.Fprintf(w, `{"account_id":"synthetic-account","rate_limit":{"allowed":%t,"primary_window":{"used_percent":12,"reset_at":%d,"limit_window_seconds":18000},"secondary_window":{"used_percent":12,"reset_at":%d,"limit_window_seconds":604800}}}`, f.allowed.Load(), time.Now().Add(time.Hour).Unix(), time.Now().Add(24*time.Hour).Unix())
		case "/backend-api/codex/responses":
			f.generations.Add(1)
			if r.Method != "POST" {
				t.Error("wrong generation method")
			}
			body, _ := io.ReadAll(r.Body)
			var request struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(body, &request)
			if request.Model != "gpt-6.1-sol" {
				t.Errorf("wrong final model: %s", request.Model)
			}
			if f.quota.Load() {
				w.WriteHeader(429)
				_, _ = w.Write([]byte(`{"error":{"code":"usage_limit_reached"}}`))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
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
	registerDelegatedLaunch(v1, auth.NewLaunchPool(m))
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
	for _, kind := range []string{"principal", "session", "model", "ordinary-false", "websocket", "unauthenticated", "session-body", "payload-rule"} {
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
			if w.Code != 409 || refusal.Error.Code != "usage_limit" || refusal.Error.BindingID != r.BindingID || refusal.Error.QuotaScope != "account" || f.generations.Load() != 1 {
				t.Fatalf("race: %d %s count=%d", w.Code, w.Body.String(), f.generations.Load())
			}
		})
	}
}
