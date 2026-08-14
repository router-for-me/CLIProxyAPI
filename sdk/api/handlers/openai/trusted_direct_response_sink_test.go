package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// trustedSinkTestHost is a minimal PluginInterceptorHost that terminates every
// request with a trusted local DirectResponse before upstream execution. This
// mirrors a trusted plugin/interceptor producing a downstream response through
// the real pre-output execution + sanitizer + WriteErrorResponse sink, in
// contrast to the unit-level sanitizer-only preservation test.
type trustedSinkTestHost struct {
	status int
	header http.Header
	body   []byte
}

func (*trustedSinkTestHost) HasStreamInterceptors() bool { return true }

func (host *trustedSinkTestHost) InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      host.status,
		ResponseHeaders: host.header,
		ResponseBody:    host.body,
	}
}

func (*trustedSinkTestHost) InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{}
}

func (*trustedSinkTestHost) InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{}
}

func (*trustedSinkTestHost) InterceptStreamChunk(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{}
}

func (*trustedSinkTestHost) CompleteRequest(context.Context, pluginapi.RequestCompletion) {}

// newTrustedSinkRouter builds an executor-backed base handler with a terminating
// plugin host and wires the representative OpenAI routes so requests flow through
// the real execute -> sanitizer -> WriteErrorResponse / sanitizer -> images/videos
// pre-output sinks.
func newTrustedSinkRouter(t *testing.T, host handlers.PluginInterceptorHost) (*gin.Engine, *int) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &peekStreamExecutor{}
	manager.RegisterExecutor(executor)

	called := 0
	provider := executor.Identifier()
	for _, model := range []struct {
		id  string
		ep  string
		typ string
	}{
		{"sink-chat-model", openAIChatEndpoint, ""},
		{"sink-resp-model", openAIResponsesEndpoint, ""},
		{"sink-img-model", "", registry.OpenAIImageModelType},
		{"sink-compat-image-model", "", registry.OpenAIImageModelType},
		{defaultXAIVideosModel, "", ""},
	} {
		authID := "sink-auth-" + model.id
		auth := &coreauth.Auth{ID: authID, Provider: provider, Status: coreauth.StatusActive}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth %s: %v", authID, err)
		}
		info := &registry.ModelInfo{ID: model.id}
		if model.ep != "" {
			info.SupportedEndpoints = []string{model.ep}
		}
		if model.typ != "" {
			info.Type = model.typ
		}
		registry.GetGlobalRegistry().RegisterClient(authID, provider, []*registry.ModelInfo{info})
		called++
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	base.SetPluginHost(host)
	openAI := NewOpenAIAPIHandler(base)
	responses := NewOpenAIResponsesAPIHandler(base)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/v1/chat/completions", openAI.ChatCompletions)
	router.POST("/v1/responses", responses.Responses)
	router.POST("/v1/images/generations", openAI.ImagesGenerations)
	router.POST("/v1/videos", openAI.VideosCreate)
	return router, &called
}

// executeTrustedSinkRequest dispatches to a route and returns the recorder.
func executeTrustedSinkRequest(t *testing.T, router *gin.Engine, route, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestOpenAITrustedDirectResponseSurvivesRealNonStreamSinks exercises a trusted
// plugin/interceptor DirectResponse through the actual non-streaming pre-output
// sinks for OpenAI chat, Responses, Images (generations + compat) and Videos.
// It asserts the exact status, verbatim body and safe plugin header survive to
// the recorder/client, and that the generic error envelope is absent.
func TestOpenAITrustedDirectResponseSurvivesRealNonStreamSinks(t *testing.T) {
	const secret = "trusted-sink-secret-4488"
	host := &trustedSinkTestHost{
		status: http.StatusTooManyRequests,
		header: http.Header{"X-Authorization-Request-Id": {"rq-7182"}},
		body:   []byte(`{"error":"blocked","detail":"` + secret + `"}`),
	}
	router, called := newTrustedSinkRouter(t, host)

	tests := []struct {
		name  string
		route string
		body  string
	}{
		{"chat", "/v1/chat/completions", `{"model":"sink-chat-model","messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", `{"model":"sink-resp-model","input":"hi"}`},
		{"images-generations", "/v1/images/generations", `{"model":"sink-img-model","prompt":"x"}`},
		{"images-compat", "/v1/images/generations", `{"model":"sink-compat-image-model","prompt":"x"}`},
		{"videos", "/v1/videos", `{"model":"` + defaultXAIVideosModel + `","prompt":"hi","seconds":"1"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := executeTrustedSinkRequest(t, router, tc.route, tc.body)
			raw := rec.Body.String()
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusTooManyRequests, raw)
			}
			if rec.Body.String() != string(host.body) {
				t.Fatalf("body = %q, want verbatim %q", rec.Body.String(), string(host.body))
			}
			if got := rec.Header().Get("X-Authorization-Request-Id"); got != "rq-7182" {
				t.Fatalf("X-Authorization-Request-Id = %q, want rq-7182", got)
			}
			if !strings.Contains(raw, secret) {
				t.Fatalf("trusted plugin body leaked key data: %q", raw)
			}
			// Generic envelope must be absent: WriteErrorResponse must not wrap
			// the trusted response with BuildErrorResponseBody.
			if strings.Contains(raw, `"error":{"message"`) {
				t.Fatalf("trusted response wrapped in generic envelope: %q", raw)
			}
		})
	}
	if *called != 5 {
		t.Fatalf("registered model clients = %d, want 5", *called)
	}
}

// TestOpenAITrustedDirectResponseSurvivesStreamingPeek exercises the same
// trusted plugin termination through the streaming-initial/peek path: the
// buffered errChan error is consumed before SSE headers are committed and the
// trusted body/header reach the recorder verbatim.
func TestOpenAITrustedDirectResponseSurvivesStreamingPeek(t *testing.T) {
	const secret = "trusted-sink-stream-secret-5590"
	host := &trustedSinkTestHost{
		status: http.StatusForbidden,
		header: http.Header{"X-Authorization-Request-Id": {"rq-stream-11"}},
		body:   []byte(`{"error":"stream-blocked","s":"` + secret + `"}`),
	}
	router, _ := newTrustedSinkRouter(t, host)

	rec := executeTrustedSinkRequest(t, router, "/v1/chat/completions",
		`{"model":"sink-chat-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	raw := rec.Body.String()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("peek status = %d, want 403; body=%q", rec.Code, raw)
	}
	if rec.Body.String() != string(host.body) {
		t.Fatalf("peek body = %q, want verbatim %q", rec.Body.String(), string(host.body))
	}
	if got := rec.Header().Get("X-Authorization-Request-Id"); got != "rq-stream-11" {
		t.Fatalf("peek X-Authorization-Request-Id = %q, want rq-stream-11", got)
	}
	if !strings.Contains(raw, secret) {
		t.Fatalf("trusted peek body leaked key data: %q", raw)
	}
	if strings.Contains(raw, `"error":{"message"`) {
		t.Fatalf("trusted peek response wrapped in generic envelope: %q", raw)
	}
}

// TestOpenAIUntrustedDirectResponseStrippedAtRealSink is the paired untrusted
// case: an upstream executor surfaces a RequestTerminatedError with the zero
// Trusted value, so DirectResponse reaches the real sink marked untrusted. The
// sink must strip Body and ResponseHeaders and produce the generic sanctioned
// error envelope with the secret redacted.
func TestOpenAIUntrustedDirectResponseStrippedAtRealSink(t *testing.T) {
	const secret = "untrusted-sink-secret-8831"
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &untrustedTerminationStreamExecutor{secret: secret}
	manager.RegisterExecutor(executor)

	authID := "sink-untrusted-auth"
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, executor.Identifier(),
		[]*registry.ModelInfo{{ID: "sink-untrusted-model", SupportedEndpoints: []string{openAIChatEndpoint}}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/v1/chat/completions", h.ChatCompletions)

	rec := executeTrustedSinkRequest(t, router, "/v1/chat/completions",
		`{"model":"sink-untrusted-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	raw := rec.Body.String()
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%q", rec.Code, raw)
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("untrusted sink leaked secret %q: %q", secret, raw)
	}
	if got := rec.Header().Get("X-Upstream"); got != "" {
		t.Fatalf("untrusted sink forwarded X-Upstream header: %q", got)
	}
	if !strings.Contains(raw, `"error":{"message"`) {
		t.Fatalf("untrusted sink did not produce generic envelope: %q", raw)
	}
	// The DirectResponse flag must not be observable downstream: sink rebuilt a
	// strict error rather than the original body (which embedded the secret).
	if raw == string(executor.body()) {
		t.Fatalf("untrusted DirectResponse body passed through verbatim: %q", raw)
	}
}

// untrustedTerminationStreamExecutor emits a single RequestTerminatedError chunk
// whose zero Trusted value marks the DirectResponse as untrusted at the sink.
type untrustedTerminationStreamExecutor struct {
	secret string
}

func (*untrustedTerminationStreamExecutor) Identifier() string { return "sink-untrusted" }

func (*untrustedTerminationStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreexecutor.RequestTerminatedError{HTTPStatus: http.StatusBadGateway}
}

func (e *untrustedTerminationStreamExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Err: &coreexecutor.RequestTerminatedError{
		HTTPStatus: http.StatusBadGateway,
		Header:     http.Header{"X-Upstream": {"leak"}},
		Body:       e.body(),
	}}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*untrustedTerminationStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*untrustedTerminationStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (*untrustedTerminationStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *untrustedTerminationStreamExecutor) body() []byte {
	return []byte(`{"raw":"` + e.secret + `"}`)
}

func TestSanitizedStreamErrorUnwrapReturnsNil(t *testing.T) {
	rawSecret := "raw-secret-key-12345"
	rawErr := errors.New("internal upstream error with api_key=" + rawSecret)
	errMsg := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      rawErr,
	}
	sanitized := sanitizeOpenAIErrorMessage(errMsg)
	if sanitized == nil || sanitized.Error == nil {
		t.Fatal("expected sanitized error, got nil")
	}
	if errors.Unwrap(sanitized.Error) != nil {
		t.Fatalf("errors.Unwrap(sanitized.Error) = %v, want nil to prevent raw cause leakage", errors.Unwrap(sanitized.Error))
	}
	if strings.Contains(sanitized.Error.Error(), rawSecret) {
		t.Fatalf("sanitized error leaked secret %q: %q", rawSecret, sanitized.Error.Error())
	}
}
