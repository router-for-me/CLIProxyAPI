package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetStaticModelDefinitionsCursorUsesOAuthSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	_, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "cursor-disabled",
		Provider: cursorauth.Provider,
		Disabled: true,
		Metadata: map[string]any{
			cursorauth.ModelCacheKey: []cursorauth.ModelDetails{
				{ID: "cursor-model-high", DisplayName: "Cursor Model High"},
				{ID: "cursor-model-medium", DisplayName: "Cursor Model Medium"},
			},
		},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	handler := &Handler{authManager: manager}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/model-definitions/cursor", nil)
	ctx.Params = gin.Params{{Key: "channel", Value: "cursor"}}

	handler.GetStaticModelDefinitions(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Channel string `json:"channel"`
		Models  []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"models"`
	}
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if response.Channel != cursorauth.Provider {
		t.Fatalf("channel = %q, want cursor", response.Channel)
	}
	if len(response.Models) != 2 {
		t.Fatalf("models = %+v, want 2 dynamic Cursor models", response.Models)
	}
	if response.Models[0].ID != "cursor-model-high" || response.Models[1].ID != "cursor-model-medium" {
		t.Fatalf("models = %+v, want sorted Cursor snapshot", response.Models)
	}
}

func TestGetStaticModelDefinitionsCursorWithoutAuthReturnsEmptyList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{authManager: coreauth.NewManager(nil, nil, nil)}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/model-definitions/cursor", nil)
	ctx.Params = gin.Params{{Key: "channel", Value: "cursor"}}

	handler.GetStaticModelDefinitions(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Body.String(); got != `{"channel":"cursor","models":[]}` {
		t.Fatalf("body = %s, want empty Cursor model list", got)
	}
}
