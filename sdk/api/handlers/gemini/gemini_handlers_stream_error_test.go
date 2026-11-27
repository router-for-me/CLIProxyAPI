package gemini

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const (
	initialFailureGeminiModel = "initial-failure-gemini-model"
)

type initialFailureGeminiStreamExecutor struct{}

func (*initialFailureGeminiStreamExecutor) Identifier() string {
	return "initial-failure-gemini-stream-executor"
}

func (*initialFailureGeminiStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*initialFailureGeminiStreamExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Err: errors.New("upstream failed before first payload")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*initialFailureGeminiStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*initialFailureGeminiStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*initialFailureGeminiStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestGeminiStreamGenerateContentDoesNotLoseErrorBeforeFirstPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			executor := &initialFailureGeminiStreamExecutor{}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			authID := fmt.Sprintf("initial-failure-gemini-auth-%d", idx)
			auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Errorf("register auth %d: %v", idx, errRegister)
				return
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: initialFailureGeminiModel}})
			defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			h := NewGeminiAPIHandler(base)
			router := gin.New()
			router.POST("/v1beta/models/*action", h.GeminiHandler)

			request := httptest.NewRequest(http.MethodPost, "/v1beta/models/initial-failure-gemini-model:streamGenerateContent", strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code == http.StatusOK {
				t.Errorf("request %d lost the buffered initial error and returned HTTP 200: %q", idx, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "upstream failed before first payload") {
				t.Errorf("request %d lost the initial upstream error: status=%d body=%q", idx, recorder.Code, recorder.Body.String())
			}
		}(i)
	}
	wg.Wait()
}

type preFTKeepAliveGeminiExecutor struct {
	chunks chan coreexecutor.StreamChunk
}

func (*preFTKeepAliveGeminiExecutor) Identifier() string { return "pre-ft-keepalive-gemini-executor" }

func (*preFTKeepAliveGeminiExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *preFTKeepAliveGeminiExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return &coreexecutor.StreamResult{Chunks: e.chunks}, nil
}

func (*preFTKeepAliveGeminiExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*preFTKeepAliveGeminiExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*preFTKeepAliveGeminiExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestGeminiStreamGenerateContentSendKeepAliveBeforeFTWritesSSEErrorOnFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tickChan := make(chan time.Time)
	restore := handlers.SetStreamingKeepAliveTickerFactoryForTest(func(time.Duration) (<-chan time.Time, func()) {
		return tickChan, func() {}
	})
	defer restore()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	executor := &preFTKeepAliveGeminiExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "pre-ft-keepalive-gemini-model"
	auth := &coreauth.Auth{ID: "pre-ft-keepalive-gemini-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	go func() {
		tickChan <- time.Unix(1, 0)
		tickChan <- time.Unix(2, 0)
		chunks <- coreexecutor.StreamChunk{Err: errors.New("gemini upstream failed after keepalive")}
		close(chunks)
	}()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			KeepAliveSeconds:      10,
			SendKeepAliveBeforeFT: true,
		},
	}, manager)
	h := NewGeminiAPIHandler(base)
	router := gin.New()
	router.POST("/v1beta/models/*action", h.GeminiHandler)

	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":streamGenerateContent", strings.NewReader("{\"contents\":[{\"parts\":[{\"text\":\"hi\"}]}]}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after keepalive commits SSE; body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, ": keep-alive") {
		t.Fatalf("unexpected SSE keep-alive comment: %q", body)
	}
	if !strings.HasPrefix(body, "\n\n") {
		t.Fatalf("missing empty SSE keep-alive frame: %q", body)
	}
	if strings.Contains(body, "event: error") {
		t.Fatalf("unexpected SSE event: error frame after keepalive: %q", body)
	}
	payloads, apiErrors := parseGoGenAIStream(t, recorder.Body.Bytes())
	if len(payloads) != 0 {
		t.Fatalf("payload count = %d, want 0; body=%q", len(payloads), body)
	}
	if len(apiErrors) != 1 {
		t.Fatalf("APIError count = %d, want 1; body=%q", len(apiErrors), body)
	}
	if apiErrors[0].Code != http.StatusInternalServerError || apiErrors[0].Status != "INTERNAL" || !strings.Contains(apiErrors[0].Message, "gemini upstream failed after keepalive") {
		t.Fatalf("APIError = %+v, want 500 INTERNAL containing keepalive error", apiErrors[0])
	}
}

type testStatusError struct {
	code int
	msg  string
}

func (e testStatusError) Error() string   { return e.msg }
func (e testStatusError) StatusCode() int { return e.code }

type goGenAIAPIError struct {
	Code    int              `json:"code,omitempty"`
	Message string           `json:"message,omitempty"`
	Status  string           `json:"status,omitempty"`
	Details []map[string]any `json:"details,omitempty"`
}

type goGenAIResponseWithError struct {
	ErrorInfo *goGenAIAPIError `json:"error,omitempty"`
}

// parseGoGenAIStream mirrors google.golang.org/genai's iterateResponseStream
// framing and default-branch APIError decoding so both pre- and post-2026-10
// versions of the Go GenAI SDK accept the terminal error frame.
func parseGoGenAIStream(t *testing.T, body []byte) ([][]byte, []goGenAIAPIError) {
	t.Helper()
	var payloads [][]byte
	var apiErrors []goGenAIAPIError
	for _, frame := range bytes.Split(body, []byte("\n\n")) {
		if len(frame) == 0 {
			continue
		}
		prefix, data, _ := bytes.Cut(frame, []byte(":"))
		switch string(prefix) {
		case "data":
			payloads = append(payloads, bytes.TrimSpace(data))
		default:
			var respWithError goGenAIResponseWithError
			if errUnmarshal := json.Unmarshal(frame, &respWithError); errUnmarshal != nil || respWithError.ErrorInfo == nil {
				t.Fatalf("iterateResponseStream: invalid stream chunk: %s:%s (unmarshal err=%v)", string(prefix), string(data), errUnmarshal)
			}
			apiErrors = append(apiErrors, *respWithError.ErrorInfo)
		}
	}
	return payloads, apiErrors
}

func TestGeminiStreamGenerateContentPostCommitErrorEmitsGoGenAIAPIError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	thoughtChunk := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"reasoning"}]}}]}`)

	tests := []struct {
		name        string
		query       string
		streamErr   error
		wantCode    int
		wantStatus  string
		wantMessage string
		wantDetails int
	}{
		{
			name:        "plain_text_502_after_thought_chunk",
			query:       "?alt=sse",
			streamErr:   testStatusError{code: http.StatusBadGateway, msg: "upstream returned zero output with finishReason MALFORMED_FUNCTION_CALL"},
			wantCode:    http.StatusBadGateway,
			wantStatus:  "UNAVAILABLE",
			wantMessage: "upstream returned zero output with finishReason MALFORMED_FUNCTION_CALL",
		},
		{
			name:  "upstream_google_rpc_json_error_preserved",
			query: "?alt=sse",
			streamErr: testStatusError{
				code: http.StatusTooManyRequests,
				msg:  "{\n  \"error\": {\n    \"code\": 429,\n    \"message\": \"Quota exceeded for model\",\n    \"status\": \"RESOURCE_EXHAUSTED\",\n    \"details\": [{\"@type\": \"type.googleapis.com/google.rpc.ErrorInfo\", \"reason\": \"RATE_LIMIT_EXCEEDED\"}]\n  }\n}",
			},
			wantCode:    http.StatusTooManyRequests,
			wantStatus:  "RESOURCE_EXHAUSTED",
			wantMessage: "Quota exceeded for model",
			wantDetails: 1,
		},
		{
			name:        "openai_style_json_error_converted_to_google_rpc",
			query:       "?alt=sse",
			streamErr:   testStatusError{code: http.StatusBadGateway, msg: `{"error":{"message":"backend failure","type":"server_error","code":"internal_server_error"}}`},
			wantCode:    http.StatusBadGateway,
			wantStatus:  "UNAVAILABLE",
			wantMessage: "backend failure",
		},
		{
			name:        "non_sse_alt_json_emits_google_rpc_error",
			query:       "?alt=json",
			streamErr:   testStatusError{code: http.StatusBadGateway, msg: "upstream returned zero output with finishReason MALFORMED_FUNCTION_CALL"},
			wantCode:    http.StatusBadGateway,
			wantStatus:  "UNAVAILABLE",
			wantMessage: "upstream returned zero output with finishReason MALFORMED_FUNCTION_CALL",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chunks := make(chan coreexecutor.StreamChunk, 2)
			chunks <- coreexecutor.StreamChunk{Payload: thoughtChunk}
			chunks <- coreexecutor.StreamChunk{Err: tc.streamErr}
			close(chunks)

			exec := &preFTKeepAliveGeminiExecutor{chunks: chunks}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(exec)
			model := "post-commit-err-gemini-" + tc.name
			auth := &coreauth.Auth{ID: "post-commit-err-auth-" + tc.name, Provider: exec.Identifier(), Status: coreauth.StatusActive}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			})

			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			h := NewGeminiAPIHandler(base)
			router := gin.New()
			router.POST("/v1beta/models/*action", h.GeminiHandler)

			req := httptest.NewRequest(
				http.MethodPost,
				"/v1beta/models/"+model+":streamGenerateContent"+tc.query,
				strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`),
			)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
			}
			body := rec.Body.Bytes()
			if bytes.Contains(body, []byte("event: error")) {
				t.Fatalf("unexpected SSE event: error frame in body: %q", string(body))
			}

			if tc.query == "?alt=json" {
				if !bytes.HasPrefix(body, thoughtChunk) {
					t.Fatalf("non-SSE body missing initial chunk prefix: %q", string(body))
				}
				tail := bytes.TrimSpace(body[len(thoughtChunk):])
				var respWithError goGenAIResponseWithError
				if errUnmarshal := json.Unmarshal(tail, &respWithError); errUnmarshal != nil || respWithError.ErrorInfo == nil {
					t.Fatalf("non-SSE trailing error failed APIError unmarshal: err=%v tail=%q", errUnmarshal, string(tail))
				}
				if respWithError.ErrorInfo.Code != tc.wantCode || respWithError.ErrorInfo.Status != tc.wantStatus || respWithError.ErrorInfo.Message != tc.wantMessage {
					t.Fatalf("APIError = %+v, want code=%d status=%q message=%q", respWithError.ErrorInfo, tc.wantCode, tc.wantStatus, tc.wantMessage)
				}
				return
			}

			payloads, apiErrors := parseGoGenAIStream(t, body)
			if len(payloads) != 1 {
				t.Fatalf("payload count = %d, want 1; body=%q", len(payloads), string(body))
			}
			if len(apiErrors) != 1 {
				t.Fatalf("APIError count = %d, want 1; body=%q", len(apiErrors), string(body))
			}
			got := apiErrors[0]
			if got.Code != tc.wantCode || got.Status != tc.wantStatus || got.Message != tc.wantMessage {
				t.Fatalf("APIError = %+v, want code=%d status=%q message=%q", got, tc.wantCode, tc.wantStatus, tc.wantMessage)
			}
			if len(got.Details) != tc.wantDetails {
				t.Fatalf("APIError details count = %d, want %d; got=%+v", len(got.Details), tc.wantDetails, got.Details)
			}
		})
	}
}

func TestGeminiStreamGenerateContentPostThoughtMalformedFunctionCallThroughAntigravity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"planning tool call..."}]}}],"modelVersion":"gemini-4-neon"}}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig-only","text":""}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":50,"thoughtsTokenCount":10,"totalTokenCount":60},"modelVersion":"gemini-4-neon"}}`+"\n\n")
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor.NewAntigravityExecutor(cfg))

	model := "gemini-4-neon-issue8547"
	auth := &coreauth.Auth{
		ID:       "ag-issue8547",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token-8547",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-8547",
		},
		Attributes: map[string]string{"base_url": upstream.URL},
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register error = %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewGeminiAPIHandler(base)
	router := gin.New()
	router.POST("/v1beta/models/*action", h.GeminiHandler)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1beta/models/"+model+":streamGenerateContent?alt=sse",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"run tool"}]}]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after thought chunk commits stream; body=%q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("unexpected SSE event: error frame: %q", rec.Body.String())
	}

	payloads, apiErrors := parseGoGenAIStream(t, rec.Body.Bytes())
	if len(payloads) != 1 {
		t.Fatalf("payload count = %d, want 1 (thought chunk); body=%q", len(payloads), rec.Body.String())
	}
	if len(apiErrors) != 1 {
		t.Fatalf("APIError count = %d, want 1; body=%q", len(apiErrors), rec.Body.String())
	}
	if apiErrors[0].Code != http.StatusBadGateway || apiErrors[0].Status != "UNAVAILABLE" || !strings.Contains(apiErrors[0].Message, "MALFORMED_FUNCTION_CALL") {
		t.Fatalf("APIError = %+v, want 502 UNAVAILABLE containing MALFORMED_FUNCTION_CALL", apiErrors[0])
	}
}
