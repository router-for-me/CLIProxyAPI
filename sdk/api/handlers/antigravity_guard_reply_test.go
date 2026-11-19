package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const (
	guardTestContentFilter = "{\n  \"error\": {\n    \"code\": 429,\n    \"message\": \"Resource has been exhausted (e.g. check quota).\",\n    \"status\": \"RESOURCE_EXHAUSTED\"\n  }\n}\n"
	guardTestQuota         = `{"error":{"code":429,"message":"You have exhausted your capacity on this model.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`
	guardTestDefaultYAML   = "port: 8317"
	guardTestOptOutYAML    = "port: 8317\noauth-request-scoped-errors:\n  antigravity: []\n"
)

func TestAntigravityGuardReplyReplacesContentFilter429(t *testing.T) {
	handler, _ := guardTestHandler(t, guardTestDefaultYAML)
	for protocol, want := range map[string]string{
		"openai":          `"code":"google_flagged_prompt"`,
		"openai-response": `"code":"google_flagged_prompt"`,
		"claude":          `"type":"permission_error"`,
		"gemini":          `"status":"PERMISSION_DENIED"`,
	} {
		original := guardTestMessage(http.StatusTooManyRequests, guardTestContentFilter)
		got := handler.antigravityGuardReply(context.Background(), protocol, "antigravity", original)
		assertAntigravityGuardReply(t, protocol, got, want)
		if got.Error != original.Error {
			t.Fatalf("%s: original error not kept", protocol)
		}
	}
}

func TestAntigravityGuardReplyKeepsOtherErrors(t *testing.T) {
	direct := guardTestMessage(http.StatusTooManyRequests, guardTestContentFilter)
	direct.DirectResponse = true
	cases := map[string]struct {
		yaml     string
		provider string
		msg      *interfaces.ErrorMessage
	}{
		"quota 429":        {guardTestDefaultYAML, "antigravity", guardTestMessage(http.StatusTooManyRequests, guardTestQuota)},
		"other provider":   {guardTestDefaultYAML, "vertex", guardTestMessage(http.StatusTooManyRequests, guardTestContentFilter)},
		"no provider":      {guardTestDefaultYAML, "", guardTestMessage(http.StatusTooManyRequests, guardTestContentFilter)},
		"other status":     {guardTestDefaultYAML, "antigravity", guardTestMessage(http.StatusBadGateway, guardTestContentFilter)},
		"direct":           {guardTestDefaultYAML, "antigravity", direct},
		"explicit opt-out": {guardTestOptOutYAML, "antigravity", guardTestMessage(http.StatusTooManyRequests, guardTestContentFilter)},
	}
	for name, tc := range cases {
		handler, _ := guardTestHandler(t, tc.yaml)
		if got := handler.antigravityGuardReply(context.Background(), "openai", tc.provider, tc.msg); got != tc.msg {
			t.Fatalf("%s: message was replaced: %+v", name, got)
		}
	}
}

func TestTrackSelectedProviderChainsExistingCallback(t *testing.T) {
	var previous string
	meta := map[string]any{coreexecutor.SelectedAuthProviderCallbackMetadataKey: func(provider string) { previous = provider }}
	recorder := trackSelectedProvider(meta)
	meta[coreexecutor.SelectedAuthProviderCallbackMetadataKey].(func(string))("antigravity")
	if recorder.get() != "antigravity" || previous != "antigravity" {
		t.Fatalf("recorder=%q previous=%q, want antigravity for both", recorder.get(), previous)
	}
	if trackSelectedProvider(nil).get() != "" {
		t.Fatal("nil metadata recorder is not empty")
	}
}

// Home runtime auths are not in the manager's auth map and are released after
// the failed attempt, so the reply must not depend on looking the auth up.
func TestAntigravityGuardReplyThroughHomeDispatch(t *testing.T) {
	handler, manager := guardTestHandler(t, guardTestDefaultYAML)
	cfg, errParse := internalconfig.ParseConfigBytes([]byte(guardTestDefaultYAML))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes: %v", errParse)
	}
	cfg.Home.Enabled = true
	manager.SetConfig(cfg)
	executor := &guardContentFilterExecutor{}
	manager.RegisterExecutor(executor)
	manager.PublishHomeDispatch(&guardHomeDispatcher{}, executionregistry.New(), 1)

	_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", "home-model", []byte(`{"model":"home-model"}`), "")
	assertAntigravityGuardReply(t, "execute", errMsg, `"code":"google_flagged_prompt"`)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "claude", "home-model", []byte(`{"model":"home-model"}`), "")
	if dataChan != nil {
		t.Fatal("rejected stream returned a data channel")
	}
	var streamErr *interfaces.ErrorMessage
	for msg := range errChan {
		if msg != nil {
			streamErr = msg
		}
	}
	assertAntigravityGuardReply(t, "stream", streamErr, `"type":"permission_error"`)

	if got := executor.calls; got != 2 {
		t.Fatalf("upstream calls = %d, want 1 per request", got)
	}
	if _, ok := manager.GetByID("guard-cred"); ok {
		t.Fatal("Home runtime auth unexpectedly in the manager auth map")
	}
}

func guardTestHandler(t *testing.T, yaml string) (*BaseAPIHandler, *coreauth.Manager) {
	t.Helper()
	cfg, errParse := internalconfig.ParseConfigBytes([]byte(yaml))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes: %v", errParse)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	return NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager), manager
}

func guardTestMessage(status int, body string) *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{StatusCode: status, Error: &coreauth.Error{HTTPStatus: status, Message: body}}
}

func assertAntigravityGuardReply(t *testing.T, name string, got *interfaces.ErrorMessage, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: no error message", name)
	}
	body := string(got.Body)
	if got.StatusCode != http.StatusForbidden || !got.DirectResponse || !strings.Contains(body, antigravityGuardMessage) || !strings.Contains(body, want) {
		t.Fatalf("%s: reply status=%d direct=%v body=%s", name, got.StatusCode, got.DirectResponse, body)
	}
}

type guardHomeDispatcher struct{}

func (*guardHomeDispatcher) HeartbeatOK() bool { return true }
func (*guardHomeDispatcher) RPopAuth(_ context.Context, model string, _ string, _ http.Header, _ int) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model":      model,
		"auth_index": "guard-cred",
		"auth":       map[string]any{"id": "guard-cred", "provider": "antigravity", "status": coreauth.StatusActive},
	})
}
func (*guardHomeDispatcher) AbortAmbiguousDispatch() {}

type guardContentFilterExecutor struct {
	calls int
}

func (*guardContentFilterExecutor) Identifier() string { return "antigravity" }

func (e *guardContentFilterExecutor) reject() error {
	e.calls++
	return &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: guardTestContentFilter}
}

func (e *guardContentFilterExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, e.reject()
}

func (e *guardContentFilterExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, e.reject()
}

func (*guardContentFilterExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, nil
}

func (*guardContentFilterExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (*guardContentFilterExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
