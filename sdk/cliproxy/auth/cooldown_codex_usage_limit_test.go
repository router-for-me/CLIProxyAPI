package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const codexUsageLimitTestBody = `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_at":1795734131,"eligible_promo":null,"limit_window_minutes":300,"resets_in_seconds":8223}}`

func TestModelCooldownReportsCodexUsageLimit(t *testing.T) {
	for _, tt := range []struct {
		name  string
		cause string
	}{
		{name: "error envelope", cause: codexUsageLimitTestBody},
		{name: "prefixed cause", cause: `status 429: {"error":{"type":"usage_limit_reached","plan_type":"plus"}}`},
		{name: "top-level type", cause: `{"type":"usage_limit_reached","plan_type":"plus"}`},
		{name: "body.error envelope", cause: `{"body":{"error":{"type":"usage_limit_reached"}}}`},
		{name: "response.error envelope", cause: `{"response":{"error":{"type":"usage_limit_reached"}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now().Unix()
			err := newModelCooldownErrorWithCause("gpt-6.2-sol", "", 2*time.Hour, errors.New(tt.cause))
			body := ojson.Parse(err.Error()).Get("error")
			if got := body.Get("code").String(); got != "model_cooldown" {
				t.Fatalf("error.code = %q, want model_cooldown kept", got)
			}
			if got := body.Get("type").String(); got != "usage_limit_reached" {
				t.Fatalf("error.type = %q, want usage_limit_reached", got)
			}
			resetsAt := body.Get("resets_at").Int()
			if resetsAt < before+7200 || resetsAt > time.Now().Unix()+7200 {
				t.Fatalf("error.resets_at = %d, want now+7200", resetsAt)
			}
			if got := body.Get("resets_in_seconds").Int(); got != 7200 {
				t.Fatalf("error.resets_in_seconds = %d, want 7200", got)
			}
			if got := err.SafeResponseHeaders().Get("Retry-After"); got != "7200" {
				t.Fatalf("Retry-After = %q, want 7200 kept", got)
			}
		})
	}
}

func TestModelCooldownWithoutCodexUsageLimitUnchanged(t *testing.T) {
	for _, tt := range []struct {
		name    string
		resetIn time.Duration
		cause   error
	}{
		{name: "no cause", resetIn: 2 * time.Hour},
		{name: "other upstream error", resetIn: 2 * time.Hour, cause: errors.New(`{"error":{"type":"server_error","message":"usage limit reached on a dependency"}}`)},
		{name: "not JSON", resetIn: 2 * time.Hour, cause: errors.New("usage_limit_reached")},
		{name: "short cooldown", resetIn: 4 * time.Minute, cause: errors.New(codexUsageLimitTestBody)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := newModelCooldownErrorWithCause("gpt-6.2-sol", "", tt.resetIn, tt.cause)
			body := ojson.Parse(err.Error()).Get("error")
			if body.Get("type").Exists() || body.Get("resets_at").Exists() {
				t.Fatalf("body = %s, want no usage-limit fields", err.Error())
			}
		})
	}
}

type codexUsageLimitTestError struct{}

func (codexUsageLimitTestError) Error() string              { return codexUsageLimitTestBody }
func (codexUsageLimitTestError) StatusCode() int            { return http.StatusTooManyRequests }
func (codexUsageLimitTestError) IsCredentialScoped() bool   { return true }
func (codexUsageLimitTestError) RetryAfter() *time.Duration { d := 8223 * time.Second; return &d }
func (codexUsageLimitTestError) Identifier() string         { return "codex-usage-limit-test" }
func (codexUsageLimitTestError) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e codexUsageLimitTestError) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e
}
func (e codexUsageLimitTestError) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, e
}
func (e codexUsageLimitTestError) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e
}
func (codexUsageLimitTestError) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// After the upstream refusal, the next request is answered by the cooldown, which
// must still be recognisable as a usage limit with the reset Codex can resume at.
func TestManagerCooldownAfterCodexUsageLimitReportsUsageLimit(t *testing.T) {
	model := "codex-usage-limit-model-" + uuid.NewString()
	auth := &Auth{ID: "codex-usage-limit-auth-" + uuid.NewString(), Provider: "codex-usage-limit-test", Status: StatusActive}
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(codexUsageLimitTestError{})
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	request := cliproxyexecutor.Request{Model: model}
	if _, errFirst := manager.Execute(context.Background(), []string{auth.Provider}, request, cliproxyexecutor.Options{}); errFirst == nil {
		t.Fatal("first Execute() succeeded, want the upstream usage limit")
	}
	_, errCooldown := manager.Execute(context.Background(), []string{auth.Provider}, request, cliproxyexecutor.Options{})
	var cooldown *modelCooldownError
	if !errors.As(errCooldown, &cooldown) {
		t.Fatalf("second Execute() error = %v, want a model cooldown", errCooldown)
	}
	body := ojson.Parse(cooldown.Error()).Get("error")
	if body.Get("type").String() != "usage_limit_reached" || body.Get("plan_type").String() != "plus" || body.Get("resets_at").Int() <= time.Now().Unix() {
		t.Fatalf("cooldown body = %s, want usage_limit_reached with plan_type and a future resets_at", cooldown.Error())
	}
}
