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

func writeCooldownErrorResponse(t *testing.T, err error) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	NewBaseAPIHandlers(nil, nil).WriteErrorResponse(ctx, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      err,
	})
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	return recorder
}

func TestWriteErrorResponseReportsLongQuotaExhaustionAsUsageLimit(t *testing.T) {
	recorder := writeCooldownErrorResponse(t, coreauth.NewQuotaExhaustedModelCooldownError("gemini-4-argon-high", "antigravity", 140*time.Hour))

	if got := recorder.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want absent for a multi-day quota exhaustion", got)
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

func TestWriteErrorResponseKeepsCooldownWithoutQuotaEvidenceRetryable(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		wantRetryAfter string
	}{
		{name: "short cooldown", err: coreauth.NewModelCooldownError("gemini-4-argon-high", "antigravity", 30*time.Second), wantRetryAfter: "30"},
		{name: "long unclassified rate limit", err: coreauth.NewModelCooldownError("gemini-4-argon-high", "antigravity", 140*time.Hour), wantRetryAfter: "504000"},
		{name: "quota exhaustion at the threshold", err: coreauth.NewQuotaExhaustedModelCooldownError("gemini-4-argon-high", "antigravity", coreauth.QuotaExhaustedCooldownThreshold), wantRetryAfter: "300"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := writeCooldownErrorResponse(t, test.err)
			if got := recorder.Header().Get("Retry-After"); got != test.wantRetryAfter {
				t.Fatalf("Retry-After = %q, want %q", got, test.wantRetryAfter)
			}
			if got := recorder.Header().Get("X-Should-Retry"); got != "" {
				t.Fatalf("X-Should-Retry = %q, want absent", got)
			}
			if got := gjson.GetBytes(recorder.Body.Bytes(), "error.type").String(); got == "usage_limit_reached" {
				t.Fatalf("retryable cooldown reported as usage_limit_reached; body=%s", recorder.Body.String())
			}
		})
	}
}
