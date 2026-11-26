package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type interactionsLeakTestExecutor struct {
	id         string
	streamFunc func(ctx context.Context) (*coreexecutor.StreamResult, error)
}

func (e *interactionsLeakTestExecutor) Identifier() string { return e.id }

func (e *interactionsLeakTestExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *interactionsLeakTestExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return e.streamFunc(ctx)
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

type blockingFirstFlushRecorder struct {
	*httptest.ResponseRecorder
	onFirstFlush func()
	once         sync.Once
}

func (r *blockingFirstFlushRecorder) Flush() {
	r.ResponseRecorder.Flush()
	r.once.Do(func() {
		if r.onFirstFlush != nil {
			r.onFirstFlush()
		}
	})
}

func assertChannelClosed[T any](t *testing.T, ch <-chan T, name string, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("%s received unexpected value instead of closing", name)
		}
	case <-timer.C:
		t.Fatalf("%s did not close within %v (forwarding goroutine leaked)", name, timeout)
	}
}

func hasInteractionsForwardingGoroutine() bool {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			stack := string(buf[:n])
			return strings.Contains(stack, "forwardInteractionsChunks.func") ||
				strings.Contains(stack, "(*GeminiAPIHandler).handleInteractionsStream.func")
		}
		buf = make([]byte, len(buf)*2)
	}
}

func waitForInteractionsForwardingGoroutineExit(timeout time.Duration) bool {
	if !hasInteractionsForwardingGoroutine() {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		runtime.Gosched()
		if !hasInteractionsForwardingGoroutine() {
			return true
		}
		select {
		case <-timer.C:
			return !hasInteractionsForwardingGoroutine()
		default:
		}
	}
}

func TestForwardInteractionsChunksExitsDeterministicallyOnCancellation(t *testing.T) {
	cases := []struct {
		name      string
		sendChunk bool
		trigger   func(cancel context.CancelFunc, done chan struct{})
	}{
		{
			name:      "blocked_data_send_unblocks_on_done",
			sendChunk: true,
			trigger: func(_ context.CancelFunc, done chan struct{}) {
				close(done)
			},
		},
		{
			name:      "blocked_data_send_unblocks_on_cli_ctx_cancel",
			sendChunk: true,
			trigger: func(cancel context.CancelFunc, _ chan struct{}) {
				cancel()
			},
		},
		{
			name:      "blocked_chunk_read_unblocks_on_done",
			sendChunk: false,
			trigger: func(_ context.CancelFunc, done chan struct{}) {
				close(done)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cliCtx, cliCancel := context.WithCancel(context.Background())
			defer cliCancel()

			// Unbuffered input with no receiver on data or errs guarantees the
			// forwarding goroutine is parked on data <- chunk.Payload once send completes.
			chunks := make(chan handlers.ModelExecutionChunk)
			done := make(chan struct{})
			data, errs := forwardInteractionsChunks(cliCtx, chunks, done)

			if tc.sendChunk {
				sendTimer := time.NewTimer(time.Second)
				defer sendTimer.Stop()
				select {
				case chunks <- handlers.ModelExecutionChunk{Payload: []byte(`{"type":"interaction.delta"}`)}:
				case <-sendTimer.C:
					t.Fatal("timed out sending chunk to forwarding goroutine")
				}
			}

			tc.trigger(cliCancel, done)

			// Wait for errs to close first while leaving data unread so a blocked
			// data <- chunk.Payload cannot be satisfied by the test itself.
			assertChannelClosed(t, errs, "errs", time.Second)
			assertChannelClosed(t, data, "data", time.Second)
		})
	}
}

func TestForwardInteractionsChunksDeliversErrorBeforeClose(t *testing.T) {
	chunks := make(chan handlers.ModelExecutionChunk, 1)
	done := make(chan struct{})
	defer close(done)

	data, errs := forwardInteractionsChunks(context.Background(), chunks, done)
	chunks <- handlers.ModelExecutionChunk{
		Err: &handlers.ModelExecutionStreamError{StatusCode: http.StatusBadGateway, Message: "upstream error"},
	}

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	var gotErr *interfaces.ErrorMessage
	select {
	case errMsg, ok := <-errs:
		if !ok || errMsg == nil {
			t.Fatalf("errs received (%v, %v), want non-nil error", errMsg, ok)
		}
		gotErr = errMsg
	case <-timer.C:
		t.Fatal("timed out waiting for error from errs channel")
	}
	if gotErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("StatusCode = %d, want %d", gotErr.StatusCode, http.StatusBadGateway)
	}
	assertChannelClosed(t, data, "data", time.Second)
	assertChannelClosed(t, errs, "errs", time.Second)
}

func TestInteractionsStreamClientDisconnectDoesNotLeakGoroutine(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const attempts = 8
	for attempt := 0; attempt < attempts; attempt++ {
		func() {
			chunks := make(chan coreexecutor.StreamChunk)
			var sentCount atomic.Int32
			var producerStarted atomic.Bool
			fourAccepted := make(chan struct{})
			producerDone := make(chan struct{})
			stopProducer := make(chan struct{})
			var stopOnce sync.Once
			stopAndWaitProducer := func() {
				stopOnce.Do(func() {
					close(stopProducer)
				})
				if !producerStarted.Load() {
					return
				}
				cleanupTimer := time.NewTimer(2 * time.Second)
				defer cleanupTimer.Stop()
				select {
				case <-producerDone:
				case <-cleanupTimer.C:
					t.Errorf("attempt %d: timed out waiting for test stream producer to stop", attempt+1)
				}
			}
			defer stopAndWaitProducer()

			exec := &interactionsLeakTestExecutor{
				id: "interactions-stream-leak-executor",
				streamFunc: func(ctx context.Context) (*coreexecutor.StreamResult, error) {
					producerStarted.Store(true)
					go func() {
						defer close(producerDone)
						defer close(chunks)
						payload := []byte(`{"type":"interaction.delta","delta":"hello"}`)
						for {
							select {
							case <-ctx.Done():
								return
							case <-stopProducer:
								return
							case chunks <- coreexecutor.StreamChunk{Payload: payload}:
								if sentCount.Add(1) == 4 {
									close(fourAccepted)
								}
							}
						}
					}()
					return &coreexecutor.StreamResult{Chunks: chunks}, nil
				},
			}

			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(exec)
			model := "interactions-leak-model"
			auth := &coreauth.Auth{
				ID:       "interactions-leak-auth",
				Provider: exec.Identifier(),
				Status:   coreauth.StatusActive,
			}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("manager.Register(): %v", errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			h := NewGeminiAPIHandler(base)
			router := gin.New()
			router.POST("/v1beta/interactions", h.Interactions)

			reqCtx, reqCancel := context.WithCancel(context.Background())
			defer reqCancel()
			req := httptest.NewRequest(http.MethodPost, "/v1beta/interactions", strings.NewReader(`{"model":"`+model+`","input":"hi","stream":true}`)).WithContext(reqCtx)
			req.Header.Set("Content-Type", "application/json")

			rec := &blockingFirstFlushRecorder{
				ResponseRecorder: httptest.NewRecorder(),
				onFirstFlush: func() {
					waitTimer := time.NewTimer(2 * time.Second)
					defer waitTimer.Stop()
					select {
					case <-fourAccepted:
					case <-waitTimer.C:
						t.Errorf("timed out waiting for forwarding goroutine to queue chunks")
					}
					reqCancel()
				},
			}

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("attempt %d: status = %d, want %d; body=%q", attempt+1, rec.Code, http.StatusOK, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "interaction.delta") {
				t.Fatalf("attempt %d: expected initial SSE chunk before disconnect, body=%q", attempt+1, rec.Body.String())
			}
			stopAndWaitProducer()
			if !waitForInteractionsForwardingGoroutineExit(200 * time.Millisecond) {
				t.Fatalf("attempt %d: forwarding goroutine in handleInteractionsStream leaked after client disconnect", attempt+1)
			}
		}()
	}
}
