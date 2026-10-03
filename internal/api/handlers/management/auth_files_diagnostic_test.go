package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/refreshdiagnostic"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type terminalRefreshExecutor struct{ refreshRecordExecutor }

func (e *terminalRefreshExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, refreshdiagnostic.FromResponse(401, []byte(`{"error":{"code":"refresh_token_expired","message":"secret-fixture"}}`))
}

func TestRefreshAuthFilesTerminalDiagnosticKeepsManagementStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&terminalRefreshExecutor{refreshRecordExecutor{provider: "codex"}})
	ctx := context.Background()
	_, err := manager.Register(ctx, &coreauth.Auth{ID: "fixture.json", FileName: "fixture.json", Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"refresh_token": "fixture"}, Attributes: map[string]string{"path": "fixture.json"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	engine := gin.New()
	engine.POST("/credentials/refresh", handler.RefreshAuthFiles)
	for _, test := range []struct {
		body   string
		status int
	}{{`{"name":"fixture.json"}`, 500}, {`{"all":true}`, 200}} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/credentials/refresh", strings.NewReader(test.body))
		request.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(response, request)
		body := response.Body.String()
		if response.Code != test.status || !strings.Contains(body, `"reauth_required":true`) || !strings.Contains(body, "refresh_token_expired") || strings.Contains(body, "secret-fixture") {
			t.Fatalf("response %d %s", response.Code, body)
		}
	}
	auth, _ := manager.GetByID("fixture.json")
	entry := handler.buildAuthFileEntryLocked(auth)
	if entry["reauth_required"] != true || entry["reauth_reason"] != "refresh_token_expired" {
		t.Fatalf("list diagnostic absent: %+v", entry)
	}
}
