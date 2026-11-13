package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestHTTPSubmissionClaudeCountTokensDisconnectCanFailOver(t *testing.T) {
	for _, homeMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("home=%t", homeMode), func(t *testing.T) {
			runSubmissionClaudeCountTokensDisconnect(t, homeMode)
		})
	}
}

func runSubmissionClaudeCountTokensDisconnect(t *testing.T, homeMode bool) {
	var accepted atomic.Int32
	var keys []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages/count_tokens" {
			t.Errorf("unexpected count request: %s %s", r.Method, r.URL.Path)
		}
		keys = append(keys, r.Header.Get("X-Api-Key"))
		if accepted.Add(1) == 1 {
			conn, _, errHijack := w.(http.Hijacker).Hijack()
			if errHijack != nil {
				t.Error(errHijack)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":42}`)
	}))
	defer upstream.Close()
	target, errURL := url.Parse(upstream.URL)
	if errURL != nil {
		t.Fatal(errURL)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{}))
	manager.SetRoundTripperProvider(submissionTransport{countLoopbackTransport{t: t, target: target, next: transport}})
	model := "claude-sonnet-5-5"
	var auths []*coreauth.Auth
	for _, suffix := range []string{"a", "b"} {
		auth := &coreauth.Auth{ID: "count-disconnect-" + suffix, Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "synthetic-" + suffix}}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
		auths = append(auths, auth)
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	if homeMode {
		manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
		manager.PublishHomeDispatch(&submissionHomeDispatcher{auths: auths}, executionregistry.New(), 1)
	}
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	router := gin.New()
	router.POST("/v1/messages/count_tokens", claude.NewClaudeCodeAPIHandler(base).ClaudeCountTokens)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	t.Logf("count POSTs=%d status=%d body=%s", accepted.Load(), response.Code, response.Body.String())
	if response.Code != http.StatusOK || accepted.Load() != 2 || ojson.GetBytes(response.Body.Bytes(), "input_tokens").Int() != 42 {
		t.Fatalf("side-effect-free count did not recover: count POSTs=%d status=%d body=%s", accepted.Load(), response.Code, response.Body.String())
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("count did not fail over to another credential: %v", keys)
	}
}

// Keep the first-party count decision while directing every byte to the fixture.
type countLoopbackTransport struct {
	t      *testing.T
	target *url.URL
	next   http.RoundTripper
}

func (c countLoopbackTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != "api.anthropic.com" {
		c.t.Errorf("count did not use the native upstream path: %s", request.URL.Host)
	}
	cloned := request.Clone(request.Context())
	cloned.URL.Scheme = c.target.Scheme
	cloned.URL.Host = c.target.Host
	cloned.Host = c.target.Host
	return c.next.RoundTrip(cloned)
}

func TestHTTPSubmissionOutcomeUnknownWireJSON(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var accepted atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				accepted.Add(1)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, `data: {"id":"cut","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`+"\n\n")
					w.(http.Flusher).Flush()
					_, _ = io.WriteString(w, `data: {"id":"cut"`)
					return
				}
				conn, _, errHijack := w.(http.Hijacker).Hijack()
				if errHijack != nil {
					t.Error(errHijack)
					return
				}
				_ = conn.Close()
			}))
			defer upstream.Close()
			response := submissionRequest(t, upstream.URL, stream, nil)
			body := response.Body.String()
			t.Logf("accepted=%d status=%d wire=%s", accepted.Load(), response.Code, body)
			if accepted.Load() != 1 {
				t.Fatal("generation was replayed")
			}
			if !stream {
				if response.Code != http.StatusBadGateway {
					t.Fatalf("non-stream status=%d", response.Code)
				}
				assertSubmissionUnknownJSON(t, body)
				return
			}
			if response.Code != http.StatusOK || !strings.Contains(body, "partial") {
				t.Fatalf("late SSE lost partial output: %s", body)
			}
			found := false
			for _, line := range strings.Split(body, "\n") {
				data, ok := strings.CutPrefix(line, "data: ")
				if !ok {
					continue
				}
				if !ojson.Valid(data) {
					t.Fatalf("SSE data is not JSON: %q", data)
				}
				if ojson.Get(data, "error").Exists() {
					assertSubmissionUnknownJSON(t, data)
					found = true
				}
			}
			if !found {
				t.Fatal("SSE did not contain a decodable unknown-outcome error")
			}
		})
	}
}

func assertSubmissionUnknownJSON(t *testing.T, body string) {
	t.Helper()
	if !ojson.Valid(body) || ojson.Get(body, "error.code").String() != "upstream_outcome_unknown" {
		t.Fatalf("unknown-outcome envelope is not decodable: %q", body)
	}
	retryable := ojson.Get(body, "error.retryable")
	if !retryable.Exists() || retryable.Bool() {
		t.Fatalf("unknown-outcome retryable is not false: %q", body)
	}
}

type submissionResultHook struct {
	coreauth.NoopHook
	mu      sync.Mutex
	results []coreauth.Result
}

func (h *submissionResultHook) OnResult(_ context.Context, result coreauth.Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.results = append(h.results, result)
}

func TestHTTPSubmissionUnknownStreamRecordsResult(t *testing.T) {
	for _, homeMode := range []bool{false, true} {
		for _, mode := range []string{"disconnect", "bootstrap", "empty"} {
			t.Run(fmt.Sprintf("home=%t/%s", homeMode, mode), func(t *testing.T) {
				var accepted atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					accepted.Add(1)
					if mode != "disconnect" {
						w.Header().Set("Content-Type", "text/event-stream")
						w.WriteHeader(http.StatusOK)
						if mode == "bootstrap" {
							_, _ = io.WriteString(w, `data: {"id":"cut"`)
						}
						return
					}
					conn, _, errHijack := w.(http.Hijacker).Hijack()
					if errHijack != nil {
						t.Error(errHijack)
						return
					}
					_ = conn.Close()
				}))
				defer upstream.Close()
				hook := &submissionResultHook{}
				response := submissionRequestWithHook(t, upstream.URL, true, nil, "/v1/chat/completions", hook, homeMode)
				hook.mu.Lock()
				defer hook.mu.Unlock()
				t.Logf("accepted=%d status=%d results=%d", accepted.Load(), response.Code, len(hook.results))
				if accepted.Load() != 1 || response.Code != http.StatusBadGateway || len(hook.results) != 1 {
					t.Fatalf("terminal attempt missing or duplicated: status=%d accepted=%d results=%d", response.Code, accepted.Load(), len(hook.results))
				}
				result := hook.results[0]
				if result.Success || result.CompletedResponse || result.CredentialScope || result.ExecutionSeq == 0 || result.Error == nil || !result.Error.IsRequestScoped() || result.Error.HTTPStatus != http.StatusBadGateway {
					t.Fatalf("terminal result is not a request-scoped failed attempt: %+v", result)
				}
			})
		}
	}
}

type submissionHomeDispatcher struct {
	auths []*coreauth.Auth
	next  atomic.Int32
}

func (*submissionHomeDispatcher) HeartbeatOK() bool       { return true }
func (*submissionHomeDispatcher) AbortAmbiguousDispatch() {}
func (d *submissionHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	index := int(d.next.Add(1)) - 1
	if index >= len(d.auths) {
		return nil, fmt.Errorf("fixture credentials exhausted")
	}
	doc := ojson.NewDoc([]byte(`{}`))
	if errSet := doc.Set("auth", d.auths[index]); errSet != nil {
		return nil, errSet
	}
	return doc.Bytes(), nil
}
