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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtime "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	access "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
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

func newDelegatedHTTPFixture(t *testing.T, configure ...func(*auth.Auth)) *delegatedHTTPFixture {
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
	credential := &auth.Auth{ID: id, Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"type": "codex", "account_id": "synthetic-account", "access_token": "synthetic-token"}}
	for _, apply := range configure {
		apply(credential)
	}
	_, err := m.Register(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-6.1-sol"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	accessManager := access.NewManager()
	accessManager.SetProviders([]access.Provider{delegatedAccess{}})
	f.engine = gin.New()
	f.engine.Use((&Server{cfg: f.cfg}).maxRequestBodyBytesMiddleware())
	// Ordinary reader oracle shares the production request-size middleware.
	f.engine.POST("/ordinary-body", func(c *gin.Context) {
		body, err := handlers.ReadRequestBody(c)
		if err != nil {
			c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: fmt.Sprintf("Invalid request: %v", err), Type: "invalid_request_error"}})
			return
		}
		c.Data(200, "application/json", body)
	})
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

// Optional fixture export is consumed by the actual Python wrapper and T3 tests.
func TestDelegatedLaunchAttemptIdentityContract(t *testing.T) {
	for _, mode := range []string{"throttle", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			f := newDelegatedHTTPFixture(t)
			r := f.prepare(t)
			f.mode.Store(mode)
			f.call("POST", r.BasePath+"/responses", "client", `{"model":"gpt-6.1-sol","input":"hello","stream":true}`)
			w := f.call("GET", r.BasePath+"/state", "client", "")
			var state map[string]any
			if json.Unmarshal(w.Body.Bytes(), &state) != nil {
				t.Fatal(w.Body.String())
			}
			refusal, ok := state["refusal"].(map[string]any)
			if !ok {
				t.Fatal(w.Body.String())
			}
			if mode == "throttle" {
				if refusal["code"] != "throttled" || refusal["quotaScope"] != "transient" || len(refusal["quotaDomains"].([]any)) != 0 || refusal["attemptedAccountDomain"] != "codex:credential:synthetic-account" {
					t.Fatal("lost attempt identity or fabricated depletion:", w.Body.String())
				}
			} else if refusal["quotaScope"] != "model" {
				t.Fatal(w.Body.String())
			}
			if dir := os.Getenv("QUOTA_CONTRACT_EXPORT"); dir != "" {
				b, _ := json.Marshal(map[string]any{"ready": r, "state": state})
				if err := os.WriteFile(filepath.Join(dir, mode+".json"), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestDelegatedLaunchHTTPBodyPolicy(t *testing.T) {
	large := []byte(`{"model":"gpt-6.1-sol","input":"` + strings.Repeat("x", (2<<20)+1) + `"}`)
	small := []byte(`{"model":"gpt-6.1-sol","input":"hello"}`)
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	for _, endpoint := range []string{"/responses", "/responses/compact"} {
		for _, tc := range []struct {
			name     string
			body     []byte
			encoding string
			limit    int64
			chunked  bool
			status   int
		}{
			{name: "default-large", body: large, status: 200},
			{name: "configured-large", body: large, limit: 4 << 20, status: 200},
			{name: "zstd", body: encoder.EncodeAll(small, nil), encoding: "zstd", status: 200},
			{name: "zstd-large", body: encoder.EncodeAll(large, nil), encoding: "zstd", status: 200},
			{name: "zstd-configured-large", body: encoder.EncodeAll(large, nil), encoding: "zstd", limit: 4 << 20, status: 200},
			{name: "wire-limit", body: large, limit: 1024, status: 413},
			{name: "chunked-limit", body: large, limit: 1024, chunked: true, status: 413},
			{name: "decoded-limit", body: encoder.EncodeAll(large, nil), encoding: "zstd", limit: 1024, status: 413},
			{name: "unsupported", body: []byte("not-json"), encoding: "gzip", status: 400},
			{name: "malformed-zstd", body: []byte("not-zstd"), encoding: "zstd", status: 400},
			{name: "unsupported-json-fallback", body: small, encoding: "gzip", status: 200},
			{name: "zstd-json-fallback", body: small, encoding: "zstd", status: 200},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				f := newDelegatedHTTPFixture(t)
				binding := f.prepare(t)
				f.cfg.MaxRequestBodyBytes = tc.limit
				call := func(path string) *httptest.ResponseRecorder {
					req := httptest.NewRequest("POST", path, bytes.NewReader(tc.body))
					req.Header.Set("X-Test-Principal", "client")
					req.Header.Set("Content-Encoding", tc.encoding)
					if tc.chunked {
						req.ContentLength = -1
					}
					w := httptest.NewRecorder()
					f.engine.ServeHTTP(w, req)
					return w
				}
				ordinary := call("/ordinary-body")
				bound := call(binding.BasePath + endpoint)
				if ordinary.Code != tc.status || bound.Code != tc.status {
					t.Fatalf("ordinary=%d bound=%d want=%d bound body=%.500s", ordinary.Code, bound.Code, tc.status, bound.Body.String())
				}
				if tc.status == 200 {
					if f.generations.Load() != 1 {
						t.Fatal("accepted body did not generate exactly once")
					}
				} else {
					if f.generations.Load() != 0 {
						t.Fatal("invalid body generated")
					}
					if ordinary.Body.String() != bound.Body.String() {
						t.Fatalf("error semantics differ: ordinary=%s bound=%s", ordinary.Body.String(), bound.Body.String())
					}
				}
			})
		}
	}
}

func TestDelegatedLaunchHTTPEncodedGuards(t *testing.T) {
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	for _, endpoint := range []string{"/responses", "/responses/compact"} {
		for _, kind := range []string{"principal", "unauthenticated", "binding", "session", "model"} {
			t.Run(endpoint+"/"+kind, func(t *testing.T) {
				f := newDelegatedHTTPFixture(t)
				binding := f.prepare(t)
				path := binding.BasePath + endpoint
				principal := "client"
				model := "gpt-6.1-sol"
				switch kind {
				case "principal":
					principal = "attacker"
				case "unauthenticated":
					principal = ""
				case "binding":
					path = strings.Replace(path, binding.BindingID, "unknown", 1)
				case "session":
					path = strings.Replace(path, "/sessions/session/", "/sessions/other/", 1)
				case "model":
					model = "gpt-6-astra"
				}
				body := encoder.EncodeAll([]byte(fmt.Sprintf(`{"model":%q,"input":"hello"}`, model)), nil)
				req := httptest.NewRequest("POST", path, bytes.NewReader(body))
				req.Header.Set("X-Test-Principal", principal)
				req.Header.Set("Content-Encoding", "zstd")
				w := httptest.NewRecorder()
				f.engine.ServeHTTP(w, req)
				if w.Code < 400 || f.generations.Load() != 0 {
					t.Fatalf("guard bypass: %d generations=%d", w.Code, f.generations.Load())
				}
			})
		}
	}
}

func TestDelegatedLaunchHTTPControlBodyCaps(t *testing.T) {
	f := newDelegatedHTTPFixture(t)
	binding := f.prepare(t)
	for _, tc := range []struct{ path, body string }{
		{"/v1/delegated-launch/prepare", `{"clientRequestId":"` + strings.Repeat("x", 16<<10) + `"}`},
		{"/v1/delegated-launch/cancel", `{"clientRequestId":"` + strings.Repeat("x", 1024) + `"}`},
		{binding.BasePath + "/resume", `{"expectedRefusalVersion":1` + strings.Repeat(" ", 1024) + `}`},
	} {
		if w := f.call("POST", tc.path, "client", tc.body); w.Code != 400 {
			t.Fatalf("control cap %s: %d", tc.path, w.Code)
		}
	}
	if f.generations.Load() != 0 {
		t.Fatal("control request generated")
	}
}

func TestDelegatedLaunchHTTPExplicitSIWCFalse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		metadata  any
		attribute string
	}{
		{"bool", false, ""}, {"string", " false ", ""}, {"attribute", nil, "false"}, {"both", false, "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDelegatedHTTPFixture(t, func(a *auth.Auth) {
				if tc.metadata != nil {
					a.Metadata["use_siwc"] = tc.metadata
				}
				if tc.attribute != "" {
					a.Attributes = map[string]string{"use_siwc": tc.attribute}
				}
				a.Metadata["siwc"] = map[string]any{"access_token": "synthetic-nested-must-not-be-used"}
			})
			binding := f.prepare(t)
			for _, endpoint := range []string{"/responses", "/responses/compact"} {
				w := f.call("POST", binding.BasePath+endpoint, "client", `{"model":"gpt-6.1-sol","input":"hello"}`)
				if w.Code != 200 {
					t.Fatalf("outer OAuth dispatch: %d %s", w.Code, w.Body.String())
				}
			}
			if f.generations.Load() != 2 {
				t.Fatal("expected both outer OAuth requests")
			}
		})
	}
}
