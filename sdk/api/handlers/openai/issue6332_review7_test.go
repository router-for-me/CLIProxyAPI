package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/sjson"
)

const issue6332ReviewModel = "gemini-3.7-flash"

func issue6332ToolFrame() string {
	return `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"native-call","name":"lookup","args":{"key":"value"}},"thoughtSignature":"` + strings.Repeat("s", 128) + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}}` + "\n\n"
}

func issue6332ReviewBase(t *testing.T, url string) (*handlers.BaseAPIHandler, *cliproxyauth.Manager) {
	t.Helper()
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(&config.Config{}))
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "antigravity", Status: cliproxyauth.StatusActive, Metadata: map[string]any{"access_token": "token", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "project"}, Attributes: map[string]string{"base_url": url}}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: issue6332ReviewModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	return handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager), manager
}

func issue6332ReviewBody(t *testing.T) []byte {
	return []byte(`{"model":"` + issue6332ReviewModel + `","stream":true,"metadata":{"session_id":"` + t.Name() + `"},"input":[{"role":"user","content":"hello"}]}`)
}

func issue6332ReviewNested(ctx context.Context, base *handlers.BaseAPIHandler, body []byte) error {
	stream, errExecute := base.ExecuteModelStream(ctx, handlers.ModelExecutionRequest{EntryProtocol: "openai-response", ExitProtocol: "openai-response", Model: issue6332ReviewModel, Stream: true, Body: body})
	if errExecute != nil {
		return errExecute.Error
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			return chunk.Err
		}
	}
	return ctx.Err()
}

type issue6332PeerHost struct {
	handlers.PluginInterceptorHost
	base     *handlers.BaseAPIHandler
	arrived  sync.WaitGroup
	finished sync.WaitGroup
	results  chan error
}

func (*issue6332PeerHost) HasRequestInterceptors() bool                                 { return false }
func (*issue6332PeerHost) HasStreamInterceptors() bool                                  { return true }
func (*issue6332PeerHost) CompleteRequest(context.Context, pluginapi.RequestCompletion) {}
func (h *issue6332PeerHost) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	if !strings.Contains(string(req.Body), "response.completed") || req.Metadata["source"] == "plugin_host_model_callback" {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	h.arrived.Done()
	h.arrived.Wait()
	// Both independent terminal interceptors are now active, before either delivery.
	nested, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	h.results <- issue6332ReviewNested(nested, h.base, req.RequestBody)
	h.finished.Done()
	h.finished.Wait()
	return pluginapi.StreamChunkInterceptResponse{}
}

func TestIssue6332Review7ConcurrentTerminalInterceptors(t *testing.T) {
	var requests atomic.Int32
	var initial sync.WaitGroup
	initial.Add(2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= 2 {
			// Both outer requests must pass their initial replay read before responding.
			initial.Done()
			initial.Wait()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, issue6332ToolFrame())
	}))
	defer server.Close()
	base, _ := issue6332ReviewBase(t, server.URL)
	host := &issue6332PeerHost{base: base, results: make(chan error, 2)}
	host.arrived.Add(2)
	host.finished.Add(2)
	base.SetPluginHost(host)
	router := gin.New()
	router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(base).Responses)
	var done sync.WaitGroup
	for range 2 {
		done.Add(1)
		go func() {
			defer done.Done()
			writer := httptest.NewRecorder()
			router.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(issue6332ReviewBody(t)))))
			if !strings.Contains(writer.Body.String(), "response.completed") {
				t.Errorf("outer completion missing: %s", writer.Body.String())
			}
		}()
	}
	done.Wait()
	for range 2 {
		if errNested := <-host.results; errNested != nil {
			t.Errorf("same-session peer terminal interceptor deadlocked: %v", errNested)
		}
	}
}

type issue6332GatedBody struct {
	io.Reader
	release <-chan struct{}
	closed  chan struct{}
}

func (b *issue6332GatedBody) Read(p []byte) (int, error) {
	n, errRead := b.Reader.Read(p)
	if errRead == io.EOF {
		<-b.release
	}
	return n, errRead
}
func (b *issue6332GatedBody) Close() error { close(b.closed); return nil }

type issue6332LifecycleTransport struct {
	calls      atomic.Int32
	release    <-chan struct{}
	closed     chan struct{}
	requests   chan []byte
	firstFrame string
}

func (rt *issue6332LifecycleTransport) RoundTripperFor(*cliproxyauth.Auth) http.RoundTripper {
	return rt
}
func (rt *issue6332LifecycleTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, errRead := io.ReadAll(r.Body)
	if errRead != nil {
		return nil, errRead
	}
	rt.requests <- body
	var response io.ReadCloser = io.NopCloser(strings.NewReader(issue6332ToolFrame()))
	if rt.calls.Add(1) == 1 {
		response = &issue6332GatedBody{Reader: strings.NewReader(rt.firstFrame), release: rt.release, closed: rt.closed}
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: response, Request: r}, nil
}

type issue6332CallbackHost struct {
	handlers.PluginInterceptorHost
	base   *handlers.BaseAPIHandler
	body   []byte
	result chan error
}

func (*issue6332CallbackHost) HasRequestInterceptors() bool { return false }
func (*issue6332CallbackHost) HasStreamInterceptors() bool  { return false }
func (h *issue6332CallbackHost) CompleteRequest(ctx context.Context, c pluginapi.RequestCompletion) {
	if c.Metadata["source"] == "plugin_host_model_callback" {
		return
	}
	if c.Outcome != pluginapi.RequestCompletionSucceeded {
		h.result <- fmt.Errorf("outer lifecycle failed: %+v", c)
		return
	}
	// A lifecycle callback starts a new turn without waiting for usage or upstream cleanup.
	nested, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	h.result <- issue6332ReviewNested(nested, h.base, h.body)
}

func TestIssue6332Review7LifecycleNextTurn(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(fmt.Sprintf("split_signature_%v", split), func(t *testing.T) {
			testIssue6332LifecycleNextTurn(t, split)
		})
	}
}

func testIssue6332LifecycleNextTurn(t *testing.T, split bool) {
	base, manager := issue6332ReviewBase(t, "http://upstream.invalid")
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	rt := &issue6332LifecycleTransport{release: release, closed: make(chan struct{}), requests: make(chan []byte, 2)}
	rt.firstFrame = issue6332ToolFrame()
	if split {
		// A finishReason alone is not the replay boundary: signature and usage follow.
		rt.firstFrame = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"native-call","name":"lookup","args":{"key":"value"}}}]},"finishReason":"STOP"}]}}` + "\n\n" +
			`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + strings.Repeat("s", 128) + `"}]}}]}}` + "\n\n" +
			`data: {"response":{"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}}` + "\n\n"
	}
	manager.SetRoundTripperProvider(rt)
	body := issue6332ReviewBody(t)
	second, _ := sjson.SetBytes(body, "input.-1", map[string]any{"type": "function_call", "call_id": "native-call", "name": "lookup", "arguments": `{"key":"value"}`})
	second, _ = sjson.SetBytes(second, "input.-1", map[string]any{"type": "function_call_output", "call_id": "native-call", "output": "found"})
	host := &issue6332CallbackHost{base: base, body: second, result: make(chan error, 1)}
	base.SetPluginHost(host)
	capture := &issue6332Capture{records: make(chan usage.Record, 4), authID: t.Name()}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), responsesUsageNop{}) })
	router := gin.New()
	router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(base).Responses)
	writer := httptest.NewRecorder()
	router.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body))))
	select {
	case errNested := <-host.result:
		if errNested != nil {
			t.Fatal(errNested)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle next turn blocked")
	}
	<-rt.requests
	secondRequest := <-rt.requests
	if !strings.Contains(string(secondRequest), strings.Repeat("s", 128)) {
		t.Errorf("lifecycle next turn read stale replay ledger: %s", secondRequest)
	}
	unblock()
	<-rt.closed
	// Join both producers only after the next-turn assertion.
	for range 2 {
		<-capture.records
	}
	cache.ClearAntigravityReasoningReplayCache()
}

func TestIssue6332Review7PartialEOFDoesNotCommitReplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Replace(issue6332ToolFrame(), `,"finishReason":"STOP"`, "", 1))
	}))
	defer server.Close()
	base, _ := issue6332ReviewBase(t, server.URL)
	router := gin.New()
	router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(base).Responses)
	writer := httptest.NewRecorder()
	router.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(issue6332ReviewBody(t)))))
	if !strings.Contains(writer.Body.String(), "response.completed") {
		t.Fatalf("expected synthetic EOF completion: %s", writer.Body.String())
	}
	items, ok := cache.GetAntigravityReasoningReplayItems(issue6332ReviewModel, "responses:"+t.Name())
	if ok || len(items) != 0 {
		t.Fatal("synthetic completion committed partial upstream output without finishReason")
	}
}
