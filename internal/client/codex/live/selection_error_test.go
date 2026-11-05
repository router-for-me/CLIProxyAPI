package live

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestWriteSelectionErrorForwardsRetryGuidance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name            string
		path            string
		err             error
		wantRetryAfter  string
		wantShouldRetry string
	}{
		{name: "live long quota exhaustion", path: "/v1/live", err: auth.NewQuotaExhaustedModelCooldownError("gpt-realtime", "codex", 140*time.Hour), wantShouldRetry: "false"},
		{name: "realtime long quota exhaustion", path: "/v1/realtime/sessions", err: auth.NewQuotaExhaustedModelCooldownError("gpt-realtime", "codex", 140*time.Hour), wantShouldRetry: "false"},
		{name: "live long unclassified cooldown", path: "/v1/live", err: auth.NewModelCooldownError("gpt-realtime", "codex", 140*time.Hour), wantRetryAfter: "504000"},
		{name: "live short cooldown", path: "/v1/live", err: auth.NewModelCooldownError("gpt-realtime", "codex", 30*time.Second), wantRetryAfter: "30"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, test.path, nil)

			writeSelectionError(ctx, test.err)

			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", recorder.Code)
			}
			if got := recorder.Header().Get("Retry-After"); got != test.wantRetryAfter {
				t.Fatalf("Retry-After = %q, want %q", got, test.wantRetryAfter)
			}
			if got := recorder.Header().Get("X-Should-Retry"); got != test.wantShouldRetry {
				t.Fatalf("X-Should-Retry = %q, want %q", got, test.wantShouldRetry)
			}
		})
	}
}
