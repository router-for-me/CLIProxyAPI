package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type managementTestExecutor struct {
	id string
}

func (e managementTestExecutor) Identifier() string { return e.id }

func (managementTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`ok`)}, nil
}

func (managementTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (managementTestExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, nil
}

func (managementTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (managementTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestPutCodexKeys_DisabledEntrySyncsRuntimeImmediately(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("codex-api-key: []\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	cfg := &config.Config{
		AuthDir: t.TempDir(),
	}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(managementTestExecutor{id: "codex"})

	h := NewHandler(cfg, configPath, manager)

	put := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		req := httptest.NewRequest(http.MethodPut, "/v0/management/codex-api-key", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx.Request = req
		h.PutCodexKeys(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT /codex-api-key status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	authID, _ := synthesizer.NewStableIDGenerator().Next("codex:apikey", "sk-test", "https://codex.example.com")

	put(`[{"api-key":"sk-test","base-url":"https://codex.example.com","disabled":false}]`)

	active, ok := manager.GetByID(authID)
	if !ok || active == nil {
		t.Fatalf("expected auth %s to be registered after enable", authID)
	}
	if active.Disabled || active.Status == coreauth.StatusDisabled {
		t.Fatalf("expected auth active after enable, got disabled=%v status=%s", active.Disabled, active.Status)
	}

	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute(enabled) error = %v", err)
	}

	put(`[{"api-key":"sk-test","base-url":"https://codex.example.com","disabled":true}]`)

	disabled, ok := manager.GetByID(authID)
	if !ok || disabled == nil {
		t.Fatalf("expected auth %s to remain present after disable", authID)
	}
	if !disabled.Disabled || disabled.Status != coreauth.StatusDisabled {
		t.Fatalf("expected auth disabled after update, got disabled=%v status=%s", disabled.Disabled, disabled.Status)
	}

	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("expected Execute(disabled) to fail because runtime auth should be disabled immediately")
	}
}
