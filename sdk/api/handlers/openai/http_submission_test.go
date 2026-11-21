package openai

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func submissionRequest(t *testing.T, upstream string, stream bool, transport http.RoundTripper) *httptest.ResponseRecorder {
	return submissionRequestPath(t, upstream, stream, transport, "/v1/chat/completions")
}

func submissionRequestPath(t *testing.T, upstream string, stream bool, transport http.RoundTripper, path string) *httptest.ResponseRecorder {
	return submissionRequestWithHook(t, upstream, stream, transport, path, nil, false)
}

func submissionRequestWithHook(t *testing.T, upstream string, stream bool, transport http.RoundTripper, path string, hook coreauth.Hook, homeMode bool) *httptest.ResponseRecorder {
	t.Helper()
	const provider = "submission-test"
	model := "submission-" + strings.ReplaceAll(t.Name(), "/", "-")
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, hook)
	manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(provider, &config.Config{}))
	if transport != nil {
		manager.SetRoundTripperProvider(submissionTransport{transport})
	}
	var auths []*coreauth.Auth
	for _, suffix := range []string{"a", "b"} {
		auth := &coreauth.Auth{ID: model + suffix, Provider: provider, Status: coreauth.StatusActive,
			ProxyURL: "direct", Attributes: map[string]string{"base_url": upstream, "api_key": "synthetic"}}
		// The injected transport exercises a real failed TCP connect before its successful attempt.
		if transport != nil {
			auth.ProxyURL = ""
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
		auths = append(auths, auth)
		registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	if homeMode {
		manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
		manager.PublishHomeDispatch(&submissionHomeDispatcher{auths: auths}, executionregistry.New(), 1)
	}
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	router := gin.New()
	router.POST("/v1/chat/completions", NewOpenAIAPIHandler(base).ChatCompletions)
	router.POST("/v1/messages", claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"max_tokens":64,"stream":%t}`, model, stream)
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

type submissionTransport struct{ transport http.RoundTripper }

func (s submissionTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return s.transport }

func TestHTTPSubmissionPostWriteDisconnectDoesNotReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var accepted atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, errRead := io.Copy(io.Discard, r.Body); errRead != nil {
					t.Error(errRead)
					return
				}
				accepted.Add(1)
				conn, _, errHijack := w.(http.Hijacker).Hijack()
				if errHijack != nil {
					t.Error(errHijack)
					return
				}
				_ = conn.Close()
			}))
			defer upstream.Close()
			response := submissionRequest(t, upstream.URL, stream, nil)
			t.Logf("accepted non-idempotent requests=%d, public status=%d", accepted.Load(), response.Code)
			if got := accepted.Load(); got != 1 {
				t.Fatalf("upstream accepted %d requests after post-write EOF, want 1", got)
			}
			if response.Code != http.StatusBadGateway {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			for _, value := range []string{`"code":"upstream_outcome_unknown"`, `"retryable":false`} {
				if !strings.Contains(response.Body.String(), value) {
					t.Fatalf("body=%s, missing %s", response.Body.String(), value)
				}
			}
			if got := response.Header().Get("x-should-retry"); got != "false" {
				t.Fatalf("x-should-retry=%q", got)
			}
		})
	}
}

func TestHTTPSubmissionPreWriteConnectFailureCanRetry(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	refused := listener.Addr().String()
	_ = listener.Close()
	var accepted, dials atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		accepted.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if dials.Add(1) == 1 {
			address = refused
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	defer transport.CloseIdleConnections()
	response := submissionRequest(t, upstream.URL, false, transport)
	if response.Code != http.StatusOK || accepted.Load() != 1 || dials.Load() != 2 {
		t.Fatalf("status=%d accepted=%d dials=%d body=%s", response.Code, accepted.Load(), dials.Load(), response.Body.String())
	}
}

func TestHTTPSubmissionExplicitRefusalCanFailOver(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"not accepted"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	response := submissionRequest(t, upstream.URL, false, nil)
	if response.Code != http.StatusOK || attempts.Load() != 2 {
		t.Fatalf("status=%d attempts=%d body=%s", response.Code, attempts.Load(), response.Body.String())
	}
}

func TestHTTPSubmissionAcceptedResponseDisconnectDoesNotReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var accepted atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				accepted.Add(1)
				w.Header().Set("Content-Length", "1000")
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				conn, _, errHijack := w.(http.Hijacker).Hijack()
				if errHijack != nil {
					t.Error(errHijack)
					return
				}
				_ = conn.Close()
			}))
			defer upstream.Close()
			response := submissionRequest(t, upstream.URL, stream, nil)
			if accepted.Load() != 1 || response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"code":"upstream_outcome_unknown"`) {
				t.Fatalf("accepted=%d status=%d body=%s", accepted.Load(), response.Code, response.Body.String())
			}
		})
	}
}

func TestHTTPSubmissionMessagesPostWriteDisconnectDoesNotReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var accepted atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, errRead := io.Copy(io.Discard, r.Body); errRead != nil {
					t.Error(errRead)
					return
				}
				accepted.Add(1)
				conn, _, errHijack := w.(http.Hijacker).Hijack()
				if errHijack != nil {
					t.Error(errHijack)
					return
				}
				_ = conn.Close()
			}))
			defer upstream.Close()
			response := submissionRequestPath(t, upstream.URL, stream, nil, "/v1/messages")
			t.Logf("accepted non-idempotent requests=%d, public status=%d", accepted.Load(), response.Code)
			if accepted.Load() != 1 || response.Code != http.StatusBadGateway {
				t.Fatalf("accepted=%d status=%d", accepted.Load(), response.Code)
			}
			for _, value := range []string{`"type":"error"`, `"code":"upstream_outcome_unknown"`, `"retryable":false`} {
				if !strings.Contains(response.Body.String(), value) {
					t.Fatalf("Messages response=%s, missing %s", response.Body.String(), value)
				}
			}
			if got := response.Header().Get("x-should-retry"); got != "false" {
				t.Fatalf("x-should-retry=%q", got)
			}
		})
	}
}
