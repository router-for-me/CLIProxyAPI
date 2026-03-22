package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type stressResponseExecutor struct {
	delay time.Duration
	calls atomic.Int64
}

func (e *stressResponseExecutor) Identifier() string { return "stress-provider" }

func (e *stressResponseExecutor) Execute(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
	e.calls.Add(1)
	return coreexecutor.Response{Payload: []byte(`{"id":"resp_stress","object":"response","status":"completed","output":[]}`)}, nil
}

func (e *stressResponseExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *stressResponseExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *stressResponseExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *stressResponseExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAIResponsesLongTPMHighRPM(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		totalRequests          = 768
		concurrency            = 96
		approxTokensPerRequest = 8000
		executorDelay          = 5 * time.Millisecond
	)

	executor := &stressResponseExecutor{delay: executorDelay}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "stress-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "stress-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	longInput := strings.Repeat("token ", approxTokensPerRequest)
	rawBody := []byte(fmt.Sprintf(`{"model":"stress-model","input":"%s"}`, longInput))

	start := make(chan struct{})
	var wg sync.WaitGroup
	var failures atomic.Int64
	sem := make(chan struct{}, concurrency)

	begin := time.Now()
	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			<-start

			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(rawBody))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)

			if resp.Code != http.StatusOK {
				failures.Add(1)
				return
			}
			if !strings.Contains(resp.Body.String(), `"status":"completed"`) {
				failures.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	elapsed := time.Since(begin)

	if failures.Load() != 0 {
		t.Fatalf("stress run had %d failed responses", failures.Load())
	}
	if got := executor.calls.Load(); got != totalRequests {
		t.Fatalf("executor calls = %d, want %d", got, totalRequests)
	}

	rps := float64(totalRequests) / elapsed.Seconds()
	rpm := rps * 60
	tokensPerSecond := float64(totalRequests*approxTokensPerRequest) / elapsed.Seconds()
	tpm := tokensPerSecond * 60

	t.Logf(
		"long TPM/high RPM simulation: requests=%d concurrency=%d approx_tokens_per_request=%d delay=%s elapsed=%s rps=%.2f rpm=%.0f tps=%.0f tpm=%.0f",
		totalRequests,
		concurrency,
		approxTokensPerRequest,
		executorDelay,
		elapsed,
		rps,
		rpm,
		tokensPerSecond,
		tpm,
	)
}
