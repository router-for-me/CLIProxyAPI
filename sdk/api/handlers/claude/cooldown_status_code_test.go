package claude

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestWriteClaudeErrorResponse_ConfiguredCooldownStatusCode(t *testing.T) {
	t.Cleanup(func() { coreauth.SetModelCooldownStatusCode(0) })
	coreauth.SetModelCooldownStatusCode(529)

	cooldownErr := coreauth.NewModelCooldownError("claude-sonnet-4-6", "claude", 20*time.Second)
	if got := clienterror.HTTPStatusFromError(cooldownErr); got != 529 {
		t.Fatalf("HTTPStatusFromError = %d, want 529", got)
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: 529,
		Error:      errors.New("model cooldown"),
	}

	handler.WriteErrorResponse(c, msg)

	if recorder.Code != 529 {
		t.Fatalf("status = %d, want 529", recorder.Code)
	}
	if got := gjson.GetBytes(recorder.Body.Bytes(), "error.type").String(); got != "overloaded_error" {
		t.Fatalf("error.type = %q, want overloaded_error; body=%s", got, recorder.Body.String())
	}
}

func TestWriteClaudeErrorResponse_DefaultCooldownStatusCodeStays429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: clienterror.HTTPStatusFromError(coreauth.NewModelCooldownError("claude-sonnet-4-6", "claude", 20*time.Second)),
		Error:      coreauth.NewModelCooldownError("claude-sonnet-4-6", "claude", 20*time.Second),
	}

	handler.WriteErrorResponse(c, msg)

	if recorder.Code != 429 {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	if got := gjson.GetBytes(recorder.Body.Bytes(), "error.type").String(); got != "rate_limit_error" {
		t.Fatalf("error.type = %q, want rate_limit_error; body=%s", got, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "20" {
		t.Fatalf("Retry-After = %q, want 20", got)
	}
}
