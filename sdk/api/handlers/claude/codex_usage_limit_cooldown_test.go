package claude

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// codexUsageLimitExecutor refuses every request the way the Codex backend does
// when an account's 5-hour window is used up.
type codexUsageLimitExecutor struct{}

func (codexUsageLimitExecutor) Error() string {
	return `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","limit_window_minutes":300,"resets_in_seconds":8223}}`
}
func (codexUsageLimitExecutor) StatusCode() int            { return http.StatusTooManyRequests }
func (codexUsageLimitExecutor) IsCredentialScoped() bool   { return true }
func (codexUsageLimitExecutor) RetryAfter() *time.Duration { d := 8223 * time.Second; return &d }
func (codexUsageLimitExecutor) Identifier() string         { return "codex-usage-limit-handler-test" }
func (codexUsageLimitExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (e codexUsageLimitExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, e
}
func (e codexUsageLimitExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, e
}
func (e codexUsageLimitExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, e
}
func (codexUsageLimitExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// codexUsageLimitCooldown returns the cooldown error a request gets after the
// upstream usage-limit refusal.
func codexUsageLimitCooldown(t *testing.T) error {
	t.Helper()
	model := "codex-usage-limit-model-" + uuid.NewString()
	auth := &coreauth.Auth{ID: "codex-usage-limit-auth-" + uuid.NewString(), Provider: "codex-usage-limit-handler-test", Status: coreauth.StatusActive}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(codexUsageLimitExecutor{})
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	request := coreexecutor.Request{Model: model}
	_, _ = manager.Execute(context.Background(), []string{auth.Provider}, request, coreexecutor.Options{})
	_, errCooldown := manager.Execute(context.Background(), []string{auth.Provider}, request, coreexecutor.Options{})
	if errCooldown == nil {
		t.Fatal("second Execute() succeeded, want the cooldown")
	}
	return errCooldown
}

// A Codex-backed Messages request keeps Anthropic's rate_limit_error type and its
// reset hint, even though the cooldown carries Codex fields.
func TestClaudeCooldownAfterCodexUsageLimitKeepsRateLimitError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	errCooldown := codexUsageLimitCooldown(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{}).WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: errCooldown})

	body := ojson.ParseBytes(recorder.Body.Bytes())
	if got := body.Get("error.type").String(); got != "rate_limit_error" {
		t.Fatalf("error.type = %q, want rate_limit_error; body = %s", got, recorder.Body.String())
	}
	if message := body.Get("error.message").String(); !strings.Contains(message, "reset after ") {
		t.Fatalf("error.message = %q, want the reset hint", message)
	}
}
