package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type managementRefreshExecutor struct {
	provider string
	err      error
	calls    atomic.Int32
}

func (e *managementRefreshExecutor) Identifier() string { return e.provider }
func (e *managementRefreshExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (e *managementRefreshExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}
func (e *managementRefreshExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.calls.Add(1)
	if e.err != nil {
		return nil, e.err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "new-secret-access-token"
	auth.Metadata["refresh_token"] = "new-secret-refresh-token"
	return auth, nil
}
func (e *managementRefreshExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (e *managementRefreshExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func registerManagementRefreshAuth(t *testing.T, manager *coreauth.Manager, provider string) *coreauth.Auth {
	t.Helper()
	auth := &coreauth.Auth{
		ID:       "refresh-handler-auth",
		FileName: "refresh-handler-auth.json",
		Provider: provider,
		Metadata: map[string]any{
			"access_token":  "old-secret-access-token",
			"refresh_token": "old-secret-refresh-token",
		},
	}
	auth.EnsureIndex()
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	return auth
}

func performRefreshRequest(h *Handler, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/auth-files/refresh",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.RefreshAuthFile(ctx)
	return recorder
}

func TestRefreshAuthFileRefreshesCredentialWithoutReturningTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &managementRefreshExecutor{provider: "codex"}
	manager.RegisterExecutor(executor)
	auth := registerManagementRefreshAuth(t, manager, "codex")
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	recorder := performRefreshRequest(h, `{"auth_index":"`+auth.Index+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "new-secret") || strings.Contains(recorder.Body.String(), "old-secret") {
		t.Fatalf("response leaked credential material: %s", recorder.Body.String())
	}

	var payload map[string]any
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if payload["status"] != "ok" || payload["auth_index"] != auth.Index {
		t.Fatalf("response = %#v", payload)
	}
	if strings.TrimSpace(payload["last_refresh"].(string)) == "" {
		t.Fatalf("last_refresh = %#v, want timestamp", payload["last_refresh"])
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || current.Metadata["access_token"] != "new-secret-access-token" {
		t.Fatalf("runtime credential = %#v", current)
	}
	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("Refresh() calls = %d, want 1", got)
	}
}

func TestRefreshAuthFileRejectsCredentialWithoutRefreshCapability(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := registerManagementRefreshAuth(t, manager, "gemini")
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	recorder := performRefreshRequest(h, `{"auth_index":"`+auth.Index+`"}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusUnprocessableEntity, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "credential_not_refreshable") {
		t.Fatalf("body = %s, want stable error code", recorder.Body.String())
	}
}

func TestRefreshAuthFileMapsProviderUnauthorizedWithoutManagementLogout(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &managementRefreshExecutor{
		provider: "codex",
		err: &coreauth.Error{
			HTTPStatus: http.StatusUnauthorized,
			Code:       "invalid_grant",
			Message:    "refresh token rejected",
		},
	}
	manager.RegisterExecutor(executor)
	auth := registerManagementRefreshAuth(t, manager, "codex")
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	recorder := performRefreshRequest(h, `{"auth_index":"`+auth.Index+`"}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusUnprocessableEntity, recorder.Body.String())
	}
	if recorder.Code == http.StatusUnauthorized {
		t.Fatal("provider rejection must not look like a management authentication failure")
	}
	if !strings.Contains(recorder.Body.String(), "credential_reauthentication_required") {
		t.Fatalf("body = %s, want reauthentication error code", recorder.Body.String())
	}
}

func TestRefreshAuthFileRequiresExactAuthIndex(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	registerManagementRefreshAuth(t, manager, "codex")
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	recorder := performRefreshRequest(h, `{"auth_index":"refresh-handler-auth.json"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "credential_not_found") {
		t.Fatalf("body = %s, want stable error code", recorder.Body.String())
	}
}

func TestAuthFileRefreshableSupportsHomeOAuthButNotAPIKeys(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{Home: config.HomeConfig{Enabled: true}}, coreauth.NewManager(nil, nil, nil))

	oauth := &coreauth.Auth{
		Provider: "custom-oauth",
		Metadata: map[string]any{"access_token": "token"},
	}
	if !h.authFileRefreshable(oauth) {
		t.Fatal("Home OAuth credential should be refreshable")
	}

	apiKey := &coreauth.Auth{
		Provider: "custom-api-key",
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindAPIKey,
			coreauth.AttributeAPIKey:   "secret",
		},
	}
	if h.authFileRefreshable(apiKey) {
		t.Fatal("Home API key credential should not be refreshable")
	}
}

func TestRefreshAuthFileManagerUnavailable(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	recorder := performRefreshRequest(h, `{"auth_index":"anything"}`)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "auth_manager_unavailable") {
		t.Fatalf("body = %s, want stable error code", recorder.Body.String())
	}
}
