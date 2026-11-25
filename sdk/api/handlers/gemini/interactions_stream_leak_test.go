package gemini

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type interactionsLeakTestExecutor struct {
	chunks <-chan coreexecutor.StreamChunk
}

func (e *interactionsLeakTestExecutor) Identifier() string {
	return "interactions-leak-test-provider"
}

func (e *interactionsLeakTestExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *interactionsLeakTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return &coreexecutor.StreamResult{Chunks: e.chunks}, nil
}

func (e *interactionsLeakTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *interactionsLeakTestExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *interactionsLeakTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type blockingFlushRecorder struct {
	*httptest.ResponseRecorder
	once         sync.Once
	firstFlushed chan struct{}
	releaseFlush chan struct{}
}

func (r *blockingFlushRecorder) Flush() {
	r.ResponseRecorder.Flush()
	r.once.Do(func() {
		close(r.firstFlushed)
		<-r.releaseFlush
	})
}

func hasInteractionsForwardingGoroutine() bool {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return bytes.Contains(buf[:n], []byte("handleInteractionsStream.func"))
		}
		buf = make([]byte, len(buf)*2)
	}
}

func TestInteractionsStreamClientDisconnectDoesNotLeakGoroutine(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk, 4)
	for range 4 {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"interaction.delta","text":"chunk"}`)}
	}
	defer close(chunks)
	exec := &interactionsLeakTestExecutor{chunks: chunks}

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)

	model := "interactions-stream-leak-test-model"
	auth := &coreauth.Auth{
		ID:       "interactions-stream-leak-test-auth",
		Provider: exec.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	h := NewGeminiAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.POST("/v1beta/interactions", h.Interactions)

	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1beta/interactions",
		strings.NewReader(`{"model":"`+model+`","input":"hi","stream":true}`),
	).WithContext(reqCtx)
	req.Header.Set("Content-Type", "application/json")

	rec := &blockingFlushRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		firstFlushed:     make(chan struct{}),
		releaseFlush:     make(chan struct{}),
	}

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		router.ServeHTTP(rec, req)
	}()

	// Wait until ForwardStream flushes the first chunk.
	<-rec.firstFlushed

	// Simulate client disconnect and let ForwardStream observe ctx.Done() and exit.
	cancelReq()
	close(rec.releaseFlush)

	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not exit after client disconnect")
	}

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for hasInteractionsForwardingGoroutine() {
		select {
		case <-deadline.C:
			t.Fatal("forwarding goroutine in handleInteractionsStream leaked after client disconnect")
		default:
			runtime.Gosched()
		}
	}
}
