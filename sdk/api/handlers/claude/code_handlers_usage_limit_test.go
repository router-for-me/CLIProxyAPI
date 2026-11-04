package claude

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The Messages route writes the same retry headers as every other route: a
// usage limit tells the client not to retry, a cooldown keeps Retry-After.
func TestClaudeErrorResponseCarriesRetryHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	write := func(err error) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(nil, nil)).WriteErrorResponse(c, handlers.ExecutionErrorMessage(err))
		return recorder
	}

	limited := write(coreauth.NewUsageLimitModelCooldownError("gpt-6-astra", "codex", time.Now().Add(3*time.Hour), 300))
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("usage limit status = %d, want 429", limited.Code)
	}
	if got := limited.Header().Get("X-Should-Retry"); got != "false" {
		t.Fatalf("usage limit X-Should-Retry = %q, want false", got)
	}
	if got := limited.Header().Get("X-Codex-Primary-Used-Percent"); got != "100" {
		t.Fatalf("usage limit X-Codex-Primary-Used-Percent = %q, want 100", got)
	}
	if got := limited.Header().Get("Retry-After"); got != "" {
		t.Fatalf("usage limit Retry-After = %q, want none", got)
	}

	cooling := write(coreauth.NewModelCooldownError("gpt-6-astra", "codex", 3*time.Hour))
	if got := cooling.Header().Get("Retry-After"); got != "10800" {
		t.Fatalf("cooldown Retry-After = %q, want 10800", got)
	}
	if got := cooling.Header().Get("X-Should-Retry"); got != "" {
		t.Fatalf("cooldown X-Should-Retry = %q, want none", got)
	}
}
