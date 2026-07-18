package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchXAIKeyUpdatesExecutionFields(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{XAIKey: []config.XAIKey{{
			APIKey:         "xai-key",
			Priority:       1,
			BaseURL:        "https://api.x.ai/v1",
			Websockets:     true,
			DisableCooling: false,
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/xai-api-key", strings.NewReader(`{
		"index": 0,
		"value": {
			"priority": 7,
			"websockets": false,
			"disable-cooling": true
		}
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PatchXAIKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	entry := h.cfg.XAIKey[0]
	if entry.Priority != 7 {
		t.Fatalf("priority = %d, want 7", entry.Priority)
	}
	if entry.Websockets {
		t.Fatal("websockets = true, want false")
	}
	if !entry.DisableCooling {
		t.Fatal("disable-cooling = false, want true")
	}
}
