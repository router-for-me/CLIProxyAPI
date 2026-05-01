package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestListAuthFiles_IncludesRecentRequestsBuckets(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       "runtime-only-auth-1",
		Provider: "codex",
		Attributes: map[string]string{
			"runtime_only": "true",
		},
		Metadata: map[string]any{
			"type": "codex",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.tokenStore = &memoryAuthStore{}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	ginCtx.Request = req

	h.ListAuthFiles(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode list payload: %v", errUnmarshal)
	}
	filesRaw, ok := payload["files"].([]any)
	if !ok {
		t.Fatalf("expected files array, payload: %#v", payload)
	}
	if len(filesRaw) != 1 {
		t.Fatalf("expected 1 auth entry, got %d", len(filesRaw))
	}

	fileEntry, ok := filesRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("expected file entry object, got %#v", filesRaw[0])
	}

	recentRaw, ok := fileEntry["recent_requests"].([]any)
	if !ok {
		t.Fatalf("expected recent_requests array, got %#v", fileEntry["recent_requests"])
	}
	if len(recentRaw) != 20 {
		t.Fatalf("expected 20 recent_requests buckets, got %d", len(recentRaw))
	}
	for idx, item := range recentRaw {
		bucket, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("expected bucket object at %d, got %#v", idx, item)
		}
		if _, ok := bucket["time"].(string); !ok {
			t.Fatalf("expected bucket time string at %d, got %#v", idx, bucket["time"])
		}
		if _, ok := bucket["success"].(float64); !ok {
			t.Fatalf("expected bucket success number at %d, got %#v", idx, bucket["success"])
		}
		if _, ok := bucket["failed"].(float64); !ok {
			t.Fatalf("expected bucket failed number at %d, got %#v", idx, bucket["failed"])
		}
	}
}
