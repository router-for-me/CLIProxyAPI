package gemini

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type keepAliveStreamExecutor struct {
	id               string
	releaseBootstrap <-chan struct{}
	chunks           chan coreexecutor.StreamChunk
}

func (e *keepAliveStreamExecutor) Identifier() string { return e.id }

func (*keepAliveStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *keepAliveStreamExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if e.releaseBootstrap != nil {
		select {
		case <-e.releaseBootstrap:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &coreexecutor.StreamResult{Chunks: e.chunks}, nil
}

func (*keepAliveStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*keepAliveStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*keepAliveStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type flushNotifyRecorder struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
}

func newFlushNotifyRecorder() *flushNotifyRecorder {
	return &flushNotifyRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		flushed:          make(chan struct{}, 16),
	}
}

func (r *flushNotifyRecorder) Flush() {
	r.ResponseRecorder.Flush()
	select {
	case r.flushed <- struct{}{}:
	default:
	}
}

// assertGoGenAICompatibleSSEFrames validates the body using the framing rules of
// google.golang.org/genai's iterateResponseStream: split on "\n\n", skip empty
// frames, and reject any non-empty frame whose prefix before ":" is not "data".
func assertGoGenAICompatibleSSEFrames(t *testing.T, body []byte) [][]byte {
	t.Helper()
	var payloads [][]byte
	for _, frame := range bytes.Split(body, []byte("\n\n")) {
		if len(frame) == 0 {
			continue
		}
		prefix, data, ok := bytes.Cut(frame, []byte(":"))
		if !ok || string(prefix) != "data" {
			t.Fatalf("invalid google.golang.org/genai SSE frame %q in body %q", string(frame), string(body))
		}
		payloads = append(payloads, bytes.TrimSpace(data))
	}
	return payloads
}

func TestGeminiStreamGenerateContentSSEKeepAliveUsesEmptyFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)

	preFTTick := make(chan time.Time)
	midStreamTick := make(chan time.Time)
	midStreamStarted := make(chan struct{})
	var tickerCalls atomic.Int32
	restore := handlers.SetStreamingKeepAliveTickerWithResetFactoryForTest(func(time.Duration) (<-chan time.Time, func(), func()) {
		if tickerCalls.Add(1) == 1 {
			return preFTTick, func() {}, func() {}
		}
		close(midStreamStarted)
		return midStreamTick, func() {}, func() {}
	})
	defer restore()

	releaseBootstrap := make(chan struct{})
	chunks := make(chan coreexecutor.StreamChunk, 4)
	executor := &keepAliveStreamExecutor{
		id:               "gemini-sse-keepalive-executor",
		releaseBootstrap: releaseBootstrap,
		chunks:           chunks,
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "gemini-sse-keepalive-model"
	auth := &coreauth.Auth{ID: "gemini-sse-keepalive-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			KeepAliveSeconds:      15,
			SendKeepAliveBeforeFT: true,
		},
	}, manager)
	h := NewGeminiAPIHandler(base)
	router := gin.New()
	router.POST("/v1beta/models/*action", h.GeminiHandler)

	recorder := newFlushNotifyRecorder()
	go func() {
		// Pre-first-token keep-alive during bootstrap wait.
		preFTTick <- time.Unix(1, 0)
		<-recorder.flushed

		close(releaseBootstrap)

		// First chunk transitions into forwardGeminiStream.
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"candidates":[{"content":{"parts":[{"text":"part-1"}]}}]}`)}
		<-recorder.flushed
		<-midStreamStarted

		// Mid-stream keep-alive pause between chunks.
		midStreamTick <- time.Unix(2, 0)
		<-recorder.flushed

		// Second chunk and clean stream close.
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"candidates":[{"content":{"parts":[{"text":"part-2"}]}}]}`)}
		<-recorder.flushed
		close(chunks)
	}()

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1beta/models/"+model+":streamGenerateContent?alt=sse",
		strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, ": keep-alive") {
		t.Fatalf("Gemini SSE response must not emit SSE comment keep-alive frame; got %q", body)
	}
	wantBody := "\n\n" +
		`data: {"candidates":[{"content":{"parts":[{"text":"part-1"}]}}]}` + "\n\n" +
		"\n\n" +
		`data: {"candidates":[{"content":{"parts":[{"text":"part-2"}]}}]}` + "\n\n"
	if body != wantBody {
		t.Fatalf("body = %q, want %q", body, wantBody)
	}
	payloads := assertGoGenAICompatibleSSEFrames(t, recorder.Body.Bytes())
	if len(payloads) != 2 {
		t.Fatalf("payload count = %d, want 2", len(payloads))
	}
}

func TestGeminiStreamGenerateContentMidStreamKeepAliveWithoutPreFTUsesEmptyFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)

	midStreamTick := make(chan time.Time)
	midStreamStarted := make(chan struct{})
	restore := handlers.SetStreamingKeepAliveTickerWithResetFactoryForTest(func(time.Duration) (<-chan time.Time, func(), func()) {
		close(midStreamStarted)
		return midStreamTick, func() {}, func() {}
	})
	defer restore()

	chunks := make(chan coreexecutor.StreamChunk, 4)
	executor := &keepAliveStreamExecutor{
		id:     "gemini-sse-midstream-keepalive-executor",
		chunks: chunks,
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "gemini-sse-midstream-keepalive-model"
	auth := &coreauth.Auth{ID: "gemini-sse-midstream-keepalive-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			KeepAliveSeconds:      15,
			SendKeepAliveBeforeFT: false,
		},
	}, manager)
	h := NewGeminiAPIHandler(base)
	router := gin.New()
	router.POST("/v1beta/models/*action", h.GeminiHandler)

	recorder := newFlushNotifyRecorder()
	go func() {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"candidates":[{"content":{"parts":[{"text":"first"}]}}]}`)}
		<-recorder.flushed
		<-midStreamStarted

		midStreamTick <- time.Unix(1, 0)
		<-recorder.flushed

		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"candidates":[{"content":{"parts":[{"text":"second"}]}}]}`)}
		<-recorder.flushed
		close(chunks)
	}()

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1beta/models/"+model+":streamGenerateContent?alt=sse",
		strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	wantBody := `data: {"candidates":[{"content":{"parts":[{"text":"first"}]}}]}` + "\n\n" +
		"\n\n" +
		`data: {"candidates":[{"content":{"parts":[{"text":"second"}]}}]}` + "\n\n"
	if body != wantBody {
		t.Fatalf("body = %q, want %q", body, wantBody)
	}
	payloads := assertGoGenAICompatibleSSEFrames(t, recorder.Body.Bytes())
	if len(payloads) != 2 {
		t.Fatalf("payload count = %d, want 2", len(payloads))
	}
}

func TestGeminiInteractionsStreamKeepAliveUsesEmptyFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)

	preFTTick := make(chan time.Time)
	midStreamTick := make(chan time.Time)
	midStreamStarted := make(chan struct{})
	var tickerCalls atomic.Int32
	restore := handlers.SetStreamingKeepAliveTickerWithResetFactoryForTest(func(time.Duration) (<-chan time.Time, func(), func()) {
		if tickerCalls.Add(1) == 1 {
			return preFTTick, func() {}, func() {}
		}
		close(midStreamStarted)
		return midStreamTick, func() {}, func() {}
	})
	defer restore()

	releaseBootstrap := make(chan struct{})
	chunks := make(chan coreexecutor.StreamChunk, 4)
	executor := &keepAliveStreamExecutor{
		id:               "gemini-interactions",
		releaseBootstrap: releaseBootstrap,
		chunks:           chunks,
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "gemini-interactions-keepalive-model"
	auth := &coreauth.Auth{ID: "gemini-interactions-keepalive-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			KeepAliveSeconds:      15,
			SendKeepAliveBeforeFT: true,
		},
	}, manager)
	h := NewGeminiAPIHandler(base)
	router := gin.New()
	router.POST("/v1beta/interactions", h.Interactions)

	recorder := newFlushNotifyRecorder()
	go func() {
		preFTTick <- time.Unix(1, 0)
		<-recorder.flushed

		close(releaseBootstrap)

		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"content.delta","delta":{"type":"text","text":"hello"}}`)}
		<-midStreamStarted
		<-recorder.flushed

		midStreamTick <- time.Unix(2, 0)
		<-recorder.flushed

		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"interaction.completed"}`)}
		<-recorder.flushed
		close(chunks)
	}()

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1beta/interactions",
		strings.NewReader(`{"model":"`+model+`","input":"hi","stream":true}`),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, ": keep-alive") {
		t.Fatalf("Interactions SSE response must not emit SSE comment keep-alive frame; got %q", body)
	}
	wantBody := "\n\n" +
		`data: {"type":"content.delta","delta":{"type":"text","text":"hello"}}` + "\n\n" +
		"\n\n" +
		`data: {"type":"interaction.completed"}` + "\n\n"
	if body != wantBody {
		t.Fatalf("body = %q, want %q", body, wantBody)
	}
	payloads := assertGoGenAICompatibleSSEFrames(t, recorder.Body.Bytes())
	if len(payloads) != 2 {
		t.Fatalf("payload count = %d, want 2", len(payloads))
	}
}
