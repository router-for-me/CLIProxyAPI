package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type authKindClaudeExecutor struct{ served []string }

func (e *authKindClaudeExecutor) Identifier() string { return "claude" }

func (e *authKindClaudeExecutor) Execute(_ context.Context, a *auth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.served = append(e.served, a.ID)
	return coreexecutor.Response{Payload: []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"auth-kind-model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)}, nil
}

func (e *authKindClaudeExecutor) ExecuteStream(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}

func (e *authKindClaudeExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a, nil
}

func (e *authKindClaudeExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *authKindClaudeExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestMessagesResponseNamesTheKindOfCredentialThatServedIt(t *testing.T) {
	for _, test := range []struct {
		name       string
		attributes map[string]string
		want       string
	}{
		{name: "api key", attributes: map[string]string{auth.AttributeAPIKey: "sk-test"}, want: "apikey"},
		{name: "login", attributes: map[string]string{auth.AttributeAuthKind: "oauth"}, want: "oauth"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			executor := &authKindClaudeExecutor{}
			server.handlers.AuthManager.RegisterExecutor(executor)
			credential := &auth.Auth{ID: "auth-kind-" + test.want, Provider: "claude", Status: auth.StatusActive, Attributes: test.attributes}
			if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
				t.Fatalf("register: %v", errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(credential.ID, "claude", []*registry.ModelInfo{{ID: "auth-kind-model", OwnedBy: "anthropic"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })

			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"auth-kind-model","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			if len(executor.served) != 1 {
				t.Fatalf("served by %v", executor.served)
			}
			if got := rr.Header().Get(internallogging.CPAAuthKindHeader); got != test.want {
				t.Fatalf("%s = %q, want %q", internallogging.CPAAuthKindHeader, got, test.want)
			}
			if !strings.Contains(rr.Header().Get("Access-Control-Expose-Headers"), internallogging.CPAAuthKindHeader) {
				t.Fatalf("Access-Control-Expose-Headers = %q, want it to include %s", rr.Header().Get("Access-Control-Expose-Headers"), internallogging.CPAAuthKindHeader)
			}
		})
	}
}
