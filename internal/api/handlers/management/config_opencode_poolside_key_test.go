package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchOpenCodeGoKeyUpdatesExecutionFields(t *testing.T) {
	disableCooling := false
	h := &Handler{
		cfg: &config.Config{OpenCodeGoKey: []config.OpenCodeGoKey{{
			APIKey:         "opencode-go-key",
			Priority:       1,
			BaseURL:        "https://opencode.ai/zen/go",
			Websockets:     true,
			DisableCooling: &disableCooling,
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/opencode-go-api-key", strings.NewReader(`{
		"index": 0,
		"value": {
			"priority": 7,
			"websockets": false,
			"disable-cooling": true,
			"request-retry": 0
		}
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PatchOpenCodeGoKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	entry := h.cfg.OpenCodeGoKey[0]
	if entry.Priority != 7 {
		t.Fatalf("priority = %d, want 7", entry.Priority)
	}
	if entry.Websockets {
		t.Fatal("websockets = true, want false")
	}
	if entry.DisableCooling == nil || !*entry.DisableCooling {
		t.Fatalf("disable-cooling = %v, want true", entry.DisableCooling)
	}
	if entry.RequestRetry == nil || *entry.RequestRetry != 0 {
		t.Fatalf("request-retry = %v, want 0", entry.RequestRetry)
	}
}

func TestPoolsideKeyCRUD(t *testing.T) {
	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	t.Run("Put", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/poolside-api-key", strings.NewReader(`[
			{"api-key":"poolside-k1","base-url":"https://inference.poolside.ai/v1","prefix":"prod"}
		]`))
		ctx.Request.Header.Set("Content-Type", "application/json")
		h.PutPoolsideKeys(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("put status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if len(h.cfg.PoolsideKey) != 1 {
			t.Fatalf("expected 1 poolside key, got %d", len(h.cfg.PoolsideKey))
		}
	})

	t.Run("Get", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/poolside-api-key", nil)
		h.GetPoolsideKeys(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("get status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("DeleteByIndex", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/poolside-api-key?index=0", nil)
		h.DeletePoolsideKey(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("delete status = %d, want %d", rec.Code, http.StatusOK)
		}
		if len(h.cfg.PoolsideKey) != 0 {
			t.Fatalf("expected 0 poolside keys after delete, got %d", len(h.cfg.PoolsideKey))
		}
	})
}
