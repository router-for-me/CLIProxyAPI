package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type managementRefreshExecutor struct{}

func (managementRefreshExecutor) Identifier() string { return "codex" }

func (managementRefreshExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (managementRefreshExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (managementRefreshExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Attributes["plan_type"] = "plus"
	auth.Metadata["access_token"] = "new-access-secret"
	auth.Metadata["refresh_token"] = "new-refresh-secret"
	auth.Metadata["id_token"] = "new-id-secret"
	return auth, nil
}

func (managementRefreshExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (managementRefreshExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRefreshAuthFileResolvesFilenameAndOmitsSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(managementRefreshExecutor{})
	record := &coreauth.Auth{
		ID:         "codex-runtime-id",
		FileName:   "codex-user.json",
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "free"},
		Metadata: map[string]any{
			"access_token":  "old-access-secret",
			"refresh_token": "old-refresh-secret",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/refresh", strings.NewReader(`{"name":"codex-user.json"}`))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	handler.RefreshAuthFile(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response map[string]any
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if got := response["id"]; got != record.ID {
		t.Fatalf("id = %#v, want %q", got, record.ID)
	}
	if got := response["plan_type"]; got != "plus" {
		t.Fatalf("plan_type = %#v, want plus", got)
	}
	for _, secret := range []string{"old-access-secret", "old-refresh-secret", "new-access-secret", "new-refresh-secret", "new-id-secret"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("response leaked secret %q: %s", secret, recorder.Body.String())
		}
	}

	updated, ok := manager.GetByID(record.ID)
	if !ok || updated == nil {
		t.Fatal("refreshed auth missing from manager")
	}
	if got := updated.Attributes["plan_type"]; got != "plus" {
		t.Fatalf("updated plan_type = %q, want plus", got)
	}
}

func TestRefreshAuthFileRejectsAuthWithoutRefreshCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(managementRefreshExecutor{})
	record := &coreauth.Auth{
		ID:       "codex-api-key",
		FileName: "codex-api-key.json",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "access-token"},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/refresh", strings.NewReader(`{"id":"codex-api-key"}`))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	handler.RefreshAuthFile(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "not refreshable") {
		t.Fatalf("body = %s, want not refreshable error", recorder.Body.String())
	}
}
