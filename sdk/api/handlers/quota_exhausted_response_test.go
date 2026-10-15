package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestWriteErrorResponseReportsLongQuotaCooldownAsUsageLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	err := coreauth.NewModelCooldownError("gemini-4-argon-high", "antigravity", 140*time.Hour)
	NewBaseAPIHandlers(nil, nil).WriteErrorResponse(ctx, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      err,
	})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	if got := recorder.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want absent for a multi-day quota cooldown", got)
	}
	if got := recorder.Header().Get("X-Should-Retry"); got != "false" {
		t.Fatalf("X-Should-Retry = %q, want false", got)
	}
	body := recorder.Body.Bytes()
	if got := gjson.GetBytes(body, "error.type").String(); got != "usage_limit_reached" {
		t.Fatalf("error.type = %q, want usage_limit_reached; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "error.resets_at").Int(); got <= time.Now().Unix() {
		t.Fatalf("error.resets_at = %d, want a future unix time; body=%s", got, body)
	}
}

func TestWriteErrorResponseKeepsShortCooldownRetryable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	err := coreauth.NewModelCooldownError("gemini-4-argon-high", "antigravity", 30*time.Second)
	NewBaseAPIHandlers(nil, nil).WriteErrorResponse(ctx, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      err,
	})

	if got := recorder.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("Retry-After = %q, want 30", got)
	}
	if got := recorder.Header().Get("X-Should-Retry"); got != "" {
		t.Fatalf("X-Should-Retry = %q, want absent", got)
	}
	if got := gjson.GetBytes(recorder.Body.Bytes(), "error.type").String(); got == "usage_limit_reached" {
		t.Fatalf("short cooldown reported as usage_limit_reached; body=%s", recorder.Body.String())
	}
}
