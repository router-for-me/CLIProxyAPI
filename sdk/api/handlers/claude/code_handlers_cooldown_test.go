package claude

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestClaudeCooldownMessageMatchesRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name       string
		duration   time.Duration
		header     string
		resetAfter string
	}{
		{name: "reported delay", duration: 8250 * time.Second, header: "8250", resetAfter: "2h17m30s"},
		{name: "round up fractional second", duration: 60*time.Second + 100*time.Millisecond, header: "61", resetAfter: "1m1s"},
		{name: "largest duration", duration: time.Duration(1<<63 - 1), header: "9223372037", resetAfter: "9223372037s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
			handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
				StatusCode: http.StatusTooManyRequests,
				Error:      coreauth.NewModelCooldownError("claude-sonnet-4-5-20250929", "claude", tt.duration),
			})

			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", recorder.Code)
			}
			if got := recorder.Header().Get("Retry-After"); got != tt.header {
				t.Fatalf("Retry-After = %q, want %q", got, tt.header)
			}
			if got := ojson.GetBytesNoCopy(recorder.Body.Bytes(), "error.type").String(); got != "rate_limit_error" {
				t.Fatalf("error.type = %q, want rate_limit_error", got)
			}
			message := ojson.GetBytesNoCopy(recorder.Body.Bytes(), "error.message").String()
			if !strings.Contains(message, "cooling down") || !strings.Contains(message, "reset after "+tt.resetAfter) {
				t.Fatalf("error.message = %q, want cooldown message with reset after %s", message, tt.resetAfter)
			}
		})
	}
}

func TestClaudeCooldownMessageInCommittedStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Header("Content-Type", "text/event-stream")
	if _, errWrite := c.Writer.Write([]byte(": keep-alive\n\n")); errWrite != nil {
		t.Fatalf("write keep-alive: %v", errWrite)
	}
	c.Writer.Flush()

	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      coreauth.NewModelCooldownError("claude-sonnet-4-5-20250929", "claude", 8250*time.Second),
	}
	close(errs)
	handler.forwardClaudeStream(c, c.Writer, func(error) {}, nil, errs)

	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "event: error\ndata: ") {
		t.Fatalf("status = %d, body = %q; want a terminal SSE error", recorder.Code, body)
	}
	_, event, _ := strings.Cut(body, "event: error\ndata: ")
	message := ojson.Get(strings.TrimSpace(event), "error.message").String()
	if !strings.Contains(message, "cooling down") || !strings.Contains(message, "reset after 2h17m30s") {
		t.Fatalf("SSE error.message = %q, want cooldown message with reset after 2h17m30s", message)
	}
}

func TestClaudeCooldownLeavesOrdinary429MessageUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, nil))
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New(`{"error":{"type":"rate_limit_error","message":"upstream quota exceeded"}}`),
		Addon:      http.Header{"Retry-After": {"300"}},
	})
	if got := recorder.Header().Get("Retry-After"); got != "300" {
		t.Fatalf("Retry-After = %q, want passed-through 300", got)
	}
	if got := ojson.GetBytesNoCopy(recorder.Body.Bytes(), "error.message").String(); got != "upstream quota exceeded" {
		t.Fatalf("error.message = %q, want original upstream message", got)
	}
}

func TestClaudeCooldownTrustedRetryAfterWinsOverPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, nil))
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      coreauth.NewModelCooldownError("claude-model", "claude", 60*time.Second+100*time.Millisecond),
		Addon:      http.Header{"Retry-After": {"300"}},
	})
	if got := recorder.Header().Get("Retry-After"); got != "61" {
		t.Fatalf("Retry-After = %q, want trusted 61", got)
	}
	if got := ojson.GetBytesNoCopy(recorder.Body.Bytes(), "error.message").String(); !strings.Contains(got, "reset after 1m1s") {
		t.Fatalf("error.message = %q, want trusted retry delay", got)
	}
}

func TestClaudeCooldownSkipsZeroDelayAndOtherStatuses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name     string
		status   int
		duration time.Duration
	}{
		{name: "zero delay", status: http.StatusTooManyRequests},
		{name: "other status", status: http.StatusServiceUnavailable, duration: time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
			handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
				StatusCode: tt.status,
				Error:      coreauth.NewModelCooldownError("claude-model", "claude", tt.duration),
			})
			if got := ojson.GetBytesNoCopy(recorder.Body.Bytes(), "error.message").String(); strings.Contains(got, "reset after") {
				t.Fatalf("error.message = %q, want no reset hint", got)
			}
		})
	}
}

func TestClaudeCooldownDirectResponseIsUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	body := `{"type":"error","error":{"type":"rate_limit_error","message":"original response"}}`
	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode:     http.StatusTooManyRequests,
		Error:          coreauth.NewModelCooldownError("claude-model", "claude", time.Minute),
		DirectResponse: true,
		Body:           []byte(body),
	})
	if got := recorder.Body.String(); got != body {
		t.Fatalf("direct response = %q, want %q", got, body)
	}
}

func TestClaudeCooldownMessagesRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	chunks := make(chan coreexecutor.StreamChunk)
	close(chunks)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	const model = "issue-7285-cooldown-model"
	const authID = "issue-7285-cooldown-auth"
	next := time.Now().Add(8250 * time.Second)
	auth := &coreauth.Auth{
		ID:             authID,
		Provider:       executor.Identifier(),
		Status:         coreauth.StatusActive,
		Unavailable:    true,
		NextRetryAfter: next,
		Quota:          coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: next},
		LastError:      &coreauth.Error{Code: "rate_limit_error", Message: "upstream quota exceeded", HTTPStatus: http.StatusTooManyRequests},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, executor.Identifier(), []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})
	handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.POST("/v1/messages", handler.ClaudeMessages)

	for _, streaming := range []bool{false, true} {
		t.Run(strconv.FormatBool(streaming), func(t *testing.T) {
			requestBody := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"stream":` + strconv.FormatBool(streaming) + `}`
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(requestBody))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429; body=%s", recorder.Code, recorder.Body.String())
			}
			seconds, errSeconds := strconv.ParseInt(recorder.Header().Get("Retry-After"), 10, 64)
			if errSeconds != nil || seconds <= 0 {
				t.Fatalf("Retry-After = %q, want positive seconds", recorder.Header().Get("Retry-After"))
			}
			message := ojson.GetBytesNoCopy(recorder.Body.Bytes(), "error.message").String()
			if !strings.Contains(message, "upstream quota exceeded") || !strings.Contains(message, "reset after "+(time.Duration(seconds)*time.Second).String()) {
				t.Fatalf("error.message = %q, want upstream cause and reset matching Retry-After %d", message, seconds)
			}
		})
	}
}
