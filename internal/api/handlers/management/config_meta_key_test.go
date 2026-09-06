package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchMetaKeyUpdatesExecutionFields(t *testing.T) {
	disableCooling := false
	h := &Handler{
		cfg: &config.Config{MetaKey: []config.MetaKey{{
			APIKey:         "meta-key",
			Priority:       1,
			BaseURL:        "https://api.meta.ai/v1",
			DisableCooling: &disableCooling,
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/meta-api-key", strings.NewReader(`{
		"index": 0,
		"value": {
			"priority": 7,
			"disable-cooling": true,
			"request-retry": 0
		}
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PatchMetaKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	entry := h.cfg.MetaKey[0]
	if entry.Priority != 7 {
		t.Fatalf("priority = %d, want 7", entry.Priority)
	}
	if entry.DisableCooling == nil || !*entry.DisableCooling {
		t.Fatalf("disable-cooling = %v, want true", entry.DisableCooling)
	}
	if entry.RequestRetry == nil || *entry.RequestRetry != 0 {
		t.Fatalf("request-retry = %v, want 0", entry.RequestRetry)
	}
}
