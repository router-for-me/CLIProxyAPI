package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestListAuthFiles_ExposesAccountAbnormalFor401And403Auth(t *testing.T) {
	for _, statusCode := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			t.Setenv("MANAGEMENT_PASSWORD", "")
			gin.SetMode(gin.TestMode)

			authDir := t.TempDir()
			fileName := "codex-bad@example.com.json"
			filePath := filepath.Join(authDir, fileName)
			if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex","email":"bad@example.com"}`), 0o600); errWrite != nil {
				t.Fatalf("failed to write auth file: %v", errWrite)
			}

			manager := coreauth.NewManager(nil, nil, nil)
			record := &coreauth.Auth{
				ID:            fileName,
				FileName:      fileName,
				Provider:      "codex",
				Status:        coreauth.StatusError,
				StatusMessage: strings.ToLower(http.StatusText(statusCode)),
				LastError: &coreauth.Error{
					Message:    "account abnormal",
					HTTPStatus: statusCode,
				},
				Attributes: map[string]string{
					"path": filePath,
				},
				Metadata: map[string]any{
					"type":  "codex",
					"email": "bad@example.com",
				},
			}
			if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
				t.Fatalf("failed to register auth record: %v", errRegister)
			}

			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

			h.ListAuthFiles(ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
			}
			var payload struct {
				Files []map[string]any `json:"files"`
			}
			if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
				t.Fatalf("failed to decode list payload: %v", errUnmarshal)
			}
			if len(payload.Files) != 1 {
				t.Fatalf("expected one auth file, got %d", len(payload.Files))
			}
			got := payload.Files[0]
			if gotStatusCode, ok := got["status_code"].(float64); !ok || int(gotStatusCode) != statusCode {
				t.Fatalf("status_code = %#v, want %d", got["status_code"], statusCode)
			}
			if abnormal, ok := got["account_abnormal"].(bool); !ok || !abnormal {
				t.Fatalf("account_abnormal = %#v, want true", got["account_abnormal"])
			}
		})
	}
}
